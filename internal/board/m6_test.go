package board_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

var me = gitstore.Identity{Name: "Op Erator", Email: "op@example.com"}

// passRun records a check run of purpose on commit that ended as state, with the ticket's checks.
func (e *env) passRun(p *board.Project, t *board.Ticket, purpose, commit, state string, extra ...string) int64 {
	e.t.Helper()
	main, head := "", ""
	if purpose == board.PurposeMerge {
		main, head = extra[0], extra[1]
	}
	id, err := e.b.StartCheckRunFor(e.ctx, p, t, purpose, commit, main, head, "", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	var res []board.CmdResult
	for _, c := range board.CmdChecks(t) {
		res = append(res, board.CmdResult{Name: c.Spec, Passed: state == board.CheckPassed})
	}
	if err := e.b.FinishCheckRun(e.ctx, id, state, res, "", false, false); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) record(p *board.Project, key string, n board.NewRecord) board.Record {
	e.t.Helper()
	r, err := e.b.AddRecord(e.ctx, p, key, n)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// The checks of M6 read Crew's own records and runs: a ticket leaves In progress with a note and its
// command checks passed on its head in a check sandbox; Review with an approval of that head; Test
// when the tester finished on it and Crew's run passed; and it enters Done when main moves to the
// merge its checks passed on. A later commit makes every one of them old.
func TestTheChecksOfM6ReadTheRecordsAndTheCheckRuns(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", "review", "test")
	e.add(p, ready("Lock account"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	e.push(p, *tk.Branch, map[string]string{"auth.go": "package auth\n"})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "the builder wrote no note" {
		t.Fatalf("waiting: %s", w)
	}
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordNote, Author: "builder", Body: "Added the lock. Ran make test."})
	e.reconcile(p)
	head, _ := e.b.Head(e.ctx, p, tk)
	if w := *e.ticket(p, "SF-1").Waiting; w != "Crew has not run its checks on "+head[:12]+" yet" {
		t.Fatalf("waiting: %s", w)
	}
	e.passRun(p, tk, board.PurposeExit, head, board.CheckFailed)
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "its checks failed on "+head[:12]+": make test" {
		t.Fatalf("waiting: %s", w)
	}
	e.passRun(p, tk, board.PurposeExit, head, board.CheckPassed)
	e.reconcile(p)
	if s := e.stage(p, "SF-1"); s != "review" {
		t.Fatalf("SF-1 is in %s", s)
	}

	// Review: a review of the head that asks for changes holds it; one of an earlier commit does not
	// count; an approval of the head moves it.
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Commit: head, Verdict: board.VerdictChanges, Body: "Test the unlock."})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "its newest review asks for changes" {
		t.Fatalf("waiting: %s", w)
	}
	e.push(p, *tk.Branch, map[string]string{"auth_test.go": "package auth\n"})
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Commit: head, Verdict: board.VerdictApprove, Body: "Fine."})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "its newest review is about an earlier commit than its head" {
		t.Fatalf("waiting: %s", w)
	}
	head2, _ := e.b.Head(e.ctx, p, tk)
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Commit: head2, Verdict: board.VerdictApprove, Body: "Fine now."})
	e.reconcile(p)
	if s := e.stage(p, "SF-1"); s != "test" {
		t.Fatalf("SF-1 is in %s: %s", s, *e.ticket(p, "SF-1").Waiting)
	}

	// Test: the tester's entry alone moves nothing, even one that says it passed (22, "The checks
	// decide").
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordTest, Author: "tester", Commit: head2, Result: "pass", Body: "All passed."})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; !strings.HasPrefix(w, "Crew runs the checks and the tests on") {
		t.Fatalf("waiting: %s", w)
	}
	e.passRun(p, tk, board.PurposeTest, head2, board.CheckFailed)
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; !strings.HasPrefix(w, "Crew's run of the checks and the tests failed") {
		t.Fatalf("waiting: %s", w)
	}
	e.passRun(p, tk, board.PurposeTest, head2, board.CheckPassed)
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "it waits for its turn to merge into main" || e.stage(p, "SF-1") != "test" {
		t.Fatalf("waiting: %s", w)
	}

	// The merge: a candidate whose checks passed on main as it is moves main, and the ticket is Done.
	main := e.git.Main(e.ctx, "shop")
	c, err := e.git.MergeCandidate(e.ctx, "shop", *tk.Branch, board.MergeMessage(tk), me, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.passRun(p, tk, board.PurposeMerge, c.Commit, board.CheckFailed, main, head2)
	e.reconcile(p)
	if e.stage(p, "SF-1") != "test" || e.git.Main(e.ctx, "shop") != main {
		t.Fatal("a merge whose checks failed moved")
	}
	e.passRun(p, tk, board.PurposeMerge, c.Commit, board.CheckPassed, main, head2)
	e.reconcile(p)
	if e.stage(p, "SF-1") != "done" || e.git.Main(e.ctx, "shop") != c.Commit {
		t.Fatalf("SF-1 is in %s, main at %s", e.stage(p, "SF-1"), e.git.Main(e.ctx, "shop"))
	}
	if got := git(t, e.git.Quarantine.Path("shop"), "rev-parse", "refs/heads/main"); got != c.Commit {
		t.Fatal("the quarantine repository's main did not follow")
	}
	if msg := git(t, e.git.Canonical("shop"), "log", "-1", "--format=%B", c.Commit); !strings.Contains(msg, "Ticket: SF-1") {
		t.Fatalf("the merge commit: %q", msg)
	}
}

// A merge that passed on an older main does not count once main moved: the ticket waits for a new one.
func TestAMergeOnAnOlderMainDoesNotCount(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...) // no optional stage: In progress is its last
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	e.push(p, *tk.Branch, map[string]string{"one.go": "package one\n"})
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordNote, Author: "builder", Body: "Done."})
	head, _ := e.b.Head(e.ctx, p, tk)
	e.passRun(p, tk, board.PurposeExit, head, board.CheckPassed)
	main := e.git.Main(e.ctx, "shop")
	c, _ := e.git.MergeCandidate(e.ctx, "shop", *tk.Branch, "m", me, nil)
	// main moves under it, as another ticket's merge does.
	e.push(p, "task/SF-9-other", map[string]string{"other.go": "package other\n"})
	other, _ := e.git.MergeCandidate(e.ctx, "shop", "task/SF-9-other", "m", me, nil)
	if err := e.git.AdvanceMain(e.ctx, "shop", main, other.Commit); err != nil {
		t.Fatal(err)
	}
	e.passRun(p, tk, board.PurposeMerge, c.Commit, board.CheckPassed, main, head)
	e.reconcile(p)
	if s := e.stage(p, "SF-1"); s != "in_progress" || *e.ticket(p, "SF-1").Waiting != "it waits for its turn to merge into main" {
		t.Fatalf("SF-1 is in %s: %s", s, *e.ticket(p, "SF-1").Waiting)
	}
	if e.git.Main(e.ctx, "shop") != other.Commit {
		t.Fatal("main moved to a merge made on an older main")
	}
}

// The operator's comment goes on a ticket that is not done, or on the plan; it waits for a run until
// one takes it, once; an agent's reply resolves it. A done ticket takes no comment.
func TestACommentWaitsForItsRunAndIsAnswered(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("One"), board.OriginPlan)
	if _, err := e.b.Comment(e.ctx, p, "SF-1", "Use bcrypt.", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a comment before the OK: %v", err)
	}
	e.ok(p)
	c, err := e.b.Comment(e.ctx, p, "SF-1", "Use bcrypt.", board.Operator)
	if err != nil || c.Ticket == nil || *c.Ticket != "SF-1" {
		t.Fatalf("%+v %v", c, err)
	}
	onPlan, err := e.b.Comment(e.ctx, p, "", "Keep it small.", board.Operator)
	if err != nil || onPlan.Ticket != nil {
		t.Fatalf("%+v %v", onPlan, err)
	}
	tk := e.ticket(p, "SF-1")
	if w, _ := e.b.Waiting(e.ctx, p.ID, tk.ID); len(w) != 1 || w[0].Body != "Use bcrypt." {
		t.Fatalf("waiting on the ticket: %+v", w)
	}
	if w, _ := e.b.Waiting(e.ctx, p.ID, 0); len(w) != 1 || w[0].Body != "Keep it small." {
		t.Fatalf("waiting on the plan: %+v", w)
	}
	reply := e.record(p, "SF-1", board.NewRecord{Kind: board.RecordComment, Author: "builder", Body: "Done with bcrypt.", InReplyTo: c.ID})
	if reply.InReplyTo == nil || *reply.InReplyTo != c.ID {
		t.Fatalf("%+v", reply)
	}
	if u, _ := e.b.Unresolved(e.ctx, tk); len(u) != 0 {
		t.Fatalf("still unresolved: %+v", u)
	}
	if _, err := e.b.AddRecord(e.ctx, p, "SF-1", board.NewRecord{Kind: board.RecordComment, Author: "builder", Body: "x", InReplyTo: 9999}); kindOf(err) != board.NotFound {
		t.Fatalf("a reply to no comment: %v", err)
	}
	if _, err := e.b.AddRecord(e.ctx, p, "SF-1", board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Body: "x", Verdict: "maybe"}); kindOf(err) != board.Invalid {
		t.Fatalf("a review with no verdict: %v", err)
	}
	if _, err := e.b.Discard(e.ctx, p, "SF-1", "not needed", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Comment(e.ctx, p, "SF-1", "One more.", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a comment on a closed ticket: %v", err)
	}
}

// An agent's entry and the board's pass over the same ticket take their locks in the same order, the
// project's first: they wait for each other and never deadlock.
func TestAnEntryAndAMoveDoNotDeadlock(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	errs := make(chan error, 400)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 150; i++ {
			if _, err := e.b.AddRecord(e.ctx, p, "SF-1", board.NewRecord{Kind: board.RecordNote, Author: "lead", Body: "x"}); err != nil {
				errs <- err
			}
			if _, err := e.b.FixRound(e.ctx, p, tk, "x"); err != nil {
				errs <- err
			}
		}
	}()
	for {
		select {
		case <-done:
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			return
		default:
		}
		if err := e.b.Reconcile(e.ctx, p.Name); err != nil {
			t.Fatal(err)
		}
	}
}

// A run writes one review: the database holds it, also for two calls at once (security review of M6).
func TestOneReviewARun(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	const run = "a1111111-0000-4000-8000-000000000001"
	e.db.Server.Exec(e.ctx, `INSERT INTO conversations (id, project, purpose) VALUES ('c1111111-0000-4000-8000-000000000001', $1, 'run')`, p.ID)
	var sub int64
	e.db.Server.QueryRow(e.ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, 1, 'review', 'reviewer') RETURNING id`, p.ID).Scan(&sub)
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO runs (id, subtask, number, cli, crew_version, image, conversation)
		VALUES ($1, $2, 1, 'claude', 'x', 'x', 'c1111111-0000-4000-8000-000000000001')`, run, sub); err != nil {
		t.Fatal(err)
	}
	n := board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Run: run, Verdict: board.VerdictApprove, Body: "Fine."}
	if _, err := e.b.AddRecord(e.ctx, p, "SF-1", n); err != nil {
		t.Fatal(err)
	}
	n.Verdict = board.VerdictChanges
	if _, err := e.b.AddRecord(e.ctx, p, "SF-1", n); err == nil {
		t.Fatal("a second review of one run")
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "approve", DocsReason: ptr("none")}, "lead"); err != nil {
		t.Fatal(err)
	}
}

// The board's own check refuses the merge of a ticket that changes a file that is code, even with a
// merge that passed: main does not move (13.8).
func TestTheMergeCheckRefusesCode(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	e.push(p, *tk.Branch, map[string]string{".agents/hooks.json": "{}"})
	e.record(p, "SF-1", board.NewRecord{Kind: board.RecordNote, Author: "builder", Body: "Done."})
	head, _ := e.b.Head(e.ctx, p, tk)
	e.passRun(p, tk, board.PurposeExit, head, board.CheckPassed)
	main := e.git.Main(e.ctx, "shop")
	c, _ := e.git.MergeCandidate(e.ctx, "shop", *tk.Branch, "m", me, nil)
	e.passRun(p, tk, board.PurposeMerge, c.Commit, board.CheckPassed, main, head)
	e.reconcile(p)
	if e.stage(p, "SF-1") != "in_progress" || e.git.Main(e.ctx, "shop") != main {
		t.Fatal("a ticket that changes code merged")
	}
	if w := *e.ticket(p, "SF-1").Waiting; !strings.Contains(w, "it changes files that are code (.agents/hooks.json)") {
		t.Fatalf("waiting: %s", w)
	}
}

// pushTree commits a branch made by script in a clone of the quarantine repository, and pushes it.
func (e *env) pushTree(p *board.Project, branch string, script func(dir string)) {
	e.t.Helper()
	work := e.t.TempDir()
	git(e.t, work, "clone", "-q", e.git.Quarantine.Path(p.Name), ".")
	git(e.t, work, "checkout", "-q", "-B", branch)
	script(work)
	git(e.t, work, "add", "-A")
	git(e.t, work, "commit", "-q", "-m", "work")
	git(e.t, work, "push", "-q", "-f", "origin", branch)
}

// A file that is code reached through a link in front of its folder, and a file in a folder that
// main's plugins.json names, are code too: neither merges by itself (13.8, review of M6, round 2).
func TestCodeBehindALinkOrInANamedFolderDoesNotMerge(t *testing.T) {
	// main holds no .agents of its own in the first case, and a plugins.json in the second, so the only
	// changed path that is code is the one each case is about.
	for name, script := range map[string]func(dir string){
		"a link for .agents": func(dir string) {
			os.MkdirAll(filepath.Join(dir, "ci/cfg"), 0o755)
			os.WriteFile(filepath.Join(dir, "ci/cfg/hooks.json"), []byte("{}"), 0o644)
			os.Symlink("ci/cfg", filepath.Join(dir, ".agents"))
		},
		"a folder main names": func(dir string) {
			os.MkdirAll(filepath.Join(dir, "tools/p"), 0o755)
			os.WriteFile(filepath.Join(dir, "tools/p/hooks.json"), []byte("{}"), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.project("shop", "SF", []string{}...)
			if name == "a folder main names" {
				// main names tools for agy's plugins.
				canon := e.git.Canonical("shop")
				seed := t.TempDir()
				git(t, seed, "clone", "-q", canon, ".")
				os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
				os.WriteFile(filepath.Join(seed, ".agents/plugins.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
				git(t, seed, "add", "-A")
				git(t, seed, "commit", "-q", "-m", "plugins")
				git(t, seed, "push", "-q", "origin", "main")
				git(t, canon, "push", "-q", e.git.Quarantine.Path("shop"), "main")
			}
			e.add(p, ready("One"), board.OriginPlan)
			e.ok(p)
			e.reconcile(p)
			e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
			e.reconcile(p)
			tk := e.ticket(p, "SF-1")
			e.pushTree(p, *tk.Branch, script)
			code, err := e.b.CodeChanges(e.ctx, p, tk)
			if err != nil || len(code) == 0 {
				t.Fatalf("no code seen: %v %v", code, err)
			}
		})
	}
}
