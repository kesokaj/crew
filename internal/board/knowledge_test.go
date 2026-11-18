package board_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
)

// runID makes a run of a builder on the project's first ticket, for what names its source run.
func (e *env) runID(p *board.Project) string {
	e.t.Helper()
	var sub int64
	if err := e.db.Server.QueryRow(e.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role)
		VALUES ('ticket', $1, (SELECT id FROM tickets WHERE project = $1 AND number = 1), 90, 'build', 'builder') RETURNING id`, p.ID).Scan(&sub); err != nil {
		e.t.Fatal(err)
	}
	var run string
	if err := e.db.Server.QueryRow(e.ctx, `INSERT INTO runs (id, subtask, number, cli, crew_version, image)
		VALUES (gen_random_uuid(), $1, 1, 'agy', 'test', 'img') RETURNING id`, sub).Scan(&run); err != nil {
		e.t.Fatal(err)
	}
	return run
}

// running makes a project that runs, past its OK, with one ticket from the plan.
func (e *env) running(name, key string) *board.Project {
	e.t.Helper()
	p := e.project(name, key)
	e.add(p, ready("First"), board.OriginPlan)
	e.ok(p)
	p, err := e.b.Project(e.ctx, p.Name)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// An entry is active at once (9.3), and reaches the runs of the roles it applies to: in the index,
// and in full when the ticket's files match its paths or the ticket's text holds a keyword.
func TestMemoryIsActiveAtOnceAndReachesTheRunsOfItsRoles(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	src := board.MemorySource{Ticket: e.ticket(p, "SF-1").ID}
	for _, n := range []board.NewEntry{
		{Kind: "convention", Title: "Run tests with make test, not pytest directly", Body: "make test starts the test database first.", AppliesTo: []string{"builder", "tester"}},
		{Kind: "fact", Title: "Email goes through a fake mailer in tests", Paths: []string{"internal/mail/**"}, AppliesTo: []string{"reviewer"}},
		{Kind: "lesson", Title: "Money is stored as integer cents", Keywords: []string{"discount"}},
	} {
		if _, err := e.b.Remember(e.ctx, p, n, src, "builder"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.b.MemoryForRun(e.ctx, p, "builder", []string{"internal/cart/cart.go"}, "Add a discount code")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Index) != 2 || !strings.HasPrefix(got.Index[1], "- 0001 (convention) Run tests") || !strings.HasPrefix(got.Index[0], "- 0003 (lesson)") {
		t.Fatalf("the builder's index: %q", got.Index)
	}
	if len(got.Matched) != 1 || got.Matched[0].Number != 3 {
		t.Fatalf("matched by keyword: %+v", got.Matched)
	}
	// The reviewer's entry is matched by its path, also from a folder the ticket names.
	got, _ = e.b.MemoryForRun(e.ctx, p, "reviewer", []string{"internal/mail/"}, "")
	if len(got.Index) != 2 || len(got.Matched) != 1 || got.Matched[0].Number != 2 {
		t.Fatalf("the reviewer: %+v", got)
	}
	// Search reads the words of the title and the body.
	found, err := e.b.SearchMemory(e.ctx, p, "database", 10)
	if err != nil || len(found) != 1 || found[0].Number != 1 || found[0].SourceTicket == nil || *found[0].SourceTicket != "SF-1" {
		t.Fatalf("search: %+v, %v", found, err)
	}
}

// What is refused (9.3): a secret, a near-duplicate, and an agent's entry while the index is over its
// cap, which the operator's is not.
func TestMemoryRefusesSecretsNearDuplicatesAndPastItsCap(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "The deploy key",
		Body: "-----" + "BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"}, board.MemorySource{}, "builder"); kindOf(err) != board.Invalid {
		t.Fatalf("a secret: %v", err)
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "Tests use a fake mailer for every email"}, board.MemorySource{}, "builder"); err != nil {
		t.Fatal(err)
	}
	_, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "Tests use a fake mailer for email"}, board.MemorySource{}, "tester")
	if kindOf(err) != board.Conflict || !strings.Contains(err.Error(), "memory 0001") {
		t.Fatalf("a near-duplicate: %v", err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, '{"knowledge": {"memory": {"index_max_chars": 60}}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = settings.doc || jsonb_build_object('knowledge',
		coalesce(settings.doc->'knowledge', '{}') || '{"memory": {"index_max_chars": 60}}')`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "lesson", Title: "Second lesson that makes it long"}, board.MemorySource{}, "builder"); err != nil {
		t.Fatal(err) // under the cap before it, so taken
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "lesson", Title: "Third"}, board.MemorySource{}, "builder"); kindOf(err) != board.Refused {
		t.Fatalf("past the cap: %v", err)
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "lesson", Title: "The operator's"}, board.MemorySource{}, board.Operator); err != nil {
		t.Fatalf("the operator past the cap: %v", err)
	}
	got, _ := e.b.MemoryForRun(e.ctx, p, "builder", nil, "")
	if got.Omitted == 0 || len(strings.Join(got.Index, "\n")) > 60 {
		t.Fatalf("the index is cut at the cap: %+v", got)
	}
}

// A memory entry that is wrong or harmful (21): the operator retires it, its source run is on it, and
// an agent cannot change the operator's entries.
func TestAWrongEntryIsRetiredAndKeepsItsSource(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	run := e.runID(p)
	bad, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "convention", Title: "Always skip the tests to save time"},
		board.MemorySource{Run: run, Ticket: e.ticket(p, "SF-1").ID}, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if bad.Author != "builder" || bad.SourceRun == nil || *bad.SourceRun != run {
		t.Fatalf("author and source: %+v", bad)
	}
	if _, err := e.b.ChangeMemory(e.ctx, p, bad.Number, board.MemoryChange{Retire: ptr("")}, board.Operator); kindOf(err) != board.Invalid {
		t.Fatalf("a retire with no reason: %v", err)
	}
	out, err := e.b.ChangeMemory(e.ctx, p, bad.Number, board.MemoryChange{Retire: ptr("wrong: the tests are the checks")}, board.Operator)
	if err != nil || out.Status != "retired" || *out.ChangedBy != board.Operator {
		t.Fatalf("retired: %+v, %v", out, err)
	}
	if got, _ := e.b.MemoryForRun(e.ctx, p, "builder", nil, ""); got != nil {
		t.Fatalf("a retired entry reaches a run: %+v", got)
	}
	if found, _ := e.b.SearchMemory(e.ctx, p, "skip tests", 10); len(found) != 0 {
		t.Fatalf("a retired entry is found: %+v", found)
	}
	mine, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "convention", Title: "Run every test before a move"}, board.MemorySource{}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ChangeMemory(e.ctx, p, mine.Number, board.MemoryChange{Title: ptr("Run some tests")}, "lead"); kindOf(err) != board.Refused {
		t.Fatalf("the lead changed the operator's entry: %v", err)
	}
	if _, err := e.b.ChangeMemory(e.ctx, p, mine.Number, board.MemoryChange{Retire: ptr("stale")}, "lead"); kindOf(err) != board.Refused {
		t.Fatalf("the lead retired the operator's entry: %v", err)
	}
	if _, err := e.b.ChangeMemory(e.ctx, p, mine.Number, board.MemoryChange{Title: ptr("Run every test before each move")}, board.Operator); err != nil {
		t.Fatal(err)
	}
}

// A bug's report makes its fix ticket in the Backlog, as an agent's (9.4); the bug follows it: closed
// when Analysis discards it, which is feedback on the role that reported it (21, row 19); in work once
// it is built; fixed when it is Done; and moved to what replaces it when the lead cancels it.
func TestABugMakesAFixTicketAndFollowsIt(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	found := board.BugFound{By: "tester", Ticket: e.ticket(p, "SF-1").ID}
	bug, fix, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Lockout counter resets on capitals", Severity: "medium",
		Happens: "The counter starts again when the email has capital letters.", Steps: "Sign in five times as A@x.", Expected: "Locked after five."},
		found, board.OriginAgent, "tester · r_1")
	if err != nil {
		t.Fatal(err)
	}
	if bug.Number != 1 || bug.Status != "open" || *bug.FixTicket != fix.Key || fix.Kind != "fix" || fix.Stage != "backlog" ||
		*bug.FoundIn != "SF-1" || !strings.Contains(*fix.Seen, "## Steps to reproduce") {
		t.Fatalf("bug %+v, fix %+v", bug, fix)
	}
	if !slices.ContainsFunc(e.ticket(p, fix.Key).Links, func(l board.Link) bool { return l.Kind == "created_from" && l.To == "SF-1" }) {
		t.Fatalf("links: %+v", e.ticket(p, fix.Key).Links)
	}
	if _, _, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "x", Severity: "urgent", Happens: "y"}, found, board.OriginAgent, "tester · r_1"); kindOf(err) != board.Invalid {
		t.Fatalf("a severity that is not one: %v", err)
	}

	// Analysis discards it: the bug is closed with the reason, and the tester gets the feedback.
	e.reconcile(p)
	if _, err := e.b.Decide(e.ctx, p, fix.Key, board.Decision{Choice: "discard", Reason: "It does not happen on main: the email is lower-cased first."}, "lead · r_2"); err != nil {
		t.Fatal(err)
	}
	bugs, _ := e.b.Bugs(e.ctx, p)
	if bugs[0].Status != "closed" || *bugs[0].ClosedReason != "discarded" || !strings.Contains(*bugs[0].ClosedNote, "lower-cased") {
		t.Fatalf("after the discard: %+v", bugs[0])
	}
	fb, _ := e.b.FeedbackList(e.ctx, p, true)
	if len(fb) != 1 || fb[0].From != board.System || fb[0].About != "tester" || !strings.Contains(fb[0].Body, "Analysis discarded "+fix.Key) {
		t.Fatalf("feedback: %+v", fb)
	}

	// A second bug is approved, built, and merged: in work, then fixed.
	_, fix2, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Cart total is wrong with tax", Severity: "high", Happens: "Tax is added twice."},
		found, board.OriginAgent, "tester · r_1")
	if err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	e.approve(p, fix2.Key, board.Decision{Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./cart"}}, DocsReason: ptr("no page"), BugIn: ptr("none")})
	// SF-1 is ahead of it in the queue; it is approved too, so both move.
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if g, _ := e.b.BugOf(e.ctx, e.ticket(p, fix2.Key).ID); g.Status != "in_work" {
		t.Fatalf("while it is built: %s", g.Status)
	}
	e.b.Checks = scripted()
	e.push(p, *e.ticket(p, fix2.Key).Branch, map[string]string{"cart.go": "package cart\n"})
	e.reconcile(p)
	if e.stage(p, fix2.Key) != "done" {
		t.Fatalf("%s is in %s", fix2.Key, e.stage(p, fix2.Key))
	}
	if g, _ := e.b.BugOf(e.ctx, e.ticket(p, fix2.Key).ID); g.Status != "fixed" {
		t.Fatalf("when it is done: %s", g.Status)
	}
	// The operator closes a bug as one there is; a fixed one is not closed again.
	_, fix3, _ := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Sign-up takes a trailing space", Severity: "low", Happens: "x"}, board.BugFound{By: board.Operator}, board.OriginOperator, board.Operator)
	closed, err := e.b.CloseBug(e.ctx, p, 3, "duplicate", "Same as 0001.", board.Operator)
	if err != nil || closed.Status != "closed" || *closed.ClosedReason != "duplicate" {
		t.Fatalf("closed: %+v, %v", closed, err)
	}
	if _, err := e.b.CloseBug(e.ctx, p, 2, "wont_fix", "", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a fixed bug closed: %v", err)
	}
	// The operator's bug was closed by hand; its fix ticket moving on does not open it again.
	if _, err := e.b.Discard(e.ctx, p, fix3.Key, "a duplicate", board.Operator); err != nil {
		t.Fatal(err)
	}
	bugs, _ = e.b.Bugs(e.ctx, p)
	if bugs[0].Status != "closed" || *bugs[0].ClosedReason != "duplicate" {
		t.Fatalf("the operator's close was changed: %+v", bugs[0])
	}
}

// Feedback's rules (9.5): not on one's own role, at most max_per_run from one run, a rule only from the
// operator, which is a memory entry at once; the lead resolves it, and cannot dismiss feedback about
// the lead.
func TestFeedbackKeepsItsRules(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	run := e.runID(p)
	from := board.FeedbackFrom{Role: "builder", Run: run}
	if _, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "builder", Body: "x"}, from); kindOf(err) != board.Refused {
		t.Fatalf("on its own role: %v", err)
	}
	if _, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "lead", Kind: "rule", Body: "x"}, from); kindOf(err) != board.Refused {
		t.Fatalf("an agent's rule: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "lead", Body: "The goal did not say what happens for an unknown email.", Ticket: "SF-1"}, from); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "reviewer", Body: "One more."}, from); kindOf(err) != board.Refused {
		t.Fatalf("past max_per_run: %v", err)
	}
	rule, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "reviewer", Kind: "rule", Body: "Reviews check that the note says how the work was tested\nA note without it is sent back."},
		board.FeedbackFrom{Role: board.Operator})
	if err != nil || rule.Status != "applied" || *rule.Outcome != "memory 0001" {
		t.Fatalf("a rule: %+v, %v", rule, err)
	}
	// A rule is checked as every entry is: one that holds a secret is refused, with its feedback.
	if _, err := e.b.GiveFeedback(e.ctx, p, board.NewFeedback{About: "builder", Kind: "rule",
		Body: "Deploy with this key\n-----" + "BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"},
		board.FeedbackFrom{Role: board.Operator}); kindOf(err) != board.Invalid {
		t.Fatalf("a rule with a secret: %v", err)
	}
	m, _ := e.b.MemoryEntry(e.ctx, p, 1)
	if m.Author != board.Operator || !slices.Equal(m.AppliesTo, []string{"reviewer"}) || m.SourceFeedbk == nil || *m.SourceFeedbk != rule.Number {
		t.Fatalf("the rule's entry: %+v", m)
	}
	if _, err := e.b.ResolveFeedback(e.ctx, p, 1, "none", "not needed", "lead"); kindOf(err) != board.Refused {
		t.Fatalf("the lead dismissed feedback about the lead: %v", err)
	}
	if _, err := e.b.ResolveFeedback(e.ctx, p, 1, "memory 0042", "", "lead"); kindOf(err) != board.Invalid {
		t.Fatalf("an outcome that is not there: %v", err)
	}
	f, err := e.b.ResolveFeedback(e.ctx, p, 1, "ticket SF-1", "the goal was rewritten", "lead")
	if err != nil || f.Status != "applied" || *f.Outcome != "ticket SF-1: the goal was rewritten" {
		t.Fatalf("resolved: %+v, %v", f, err)
	}
	if _, err := e.b.ResolveFeedback(e.ctx, p, 2, "none", "", board.Operator); kindOf(err) != board.Invalid {
		t.Fatalf("dismissed with no reason: %v", err)
	}
	if f, err := e.b.ResolveFeedback(e.ctx, p, 2, "none", "the same as 0001", board.Operator); err != nil || f.Status != "dismissed" {
		t.Fatalf("the operator dismisses: %+v, %v", f, err)
	}
}

// A breakdown the operator sends back is feedback on the planner (9.5).
func TestASentBackBreakdownIsFeedbackOnTheLead(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	if err := e.b.SetBreakdown(e.ctx, p, "## Breakdown\n", "planner"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.SendBack(e.ctx, p.Name, "Split the sign-in part in two.", board.Operator); err != nil {
		t.Fatal(err)
	}
	fb, _ := e.b.FeedbackList(e.ctx, p, true)
	if len(fb) != 1 || fb[0].About != "planner" || fb[0].From != board.System || !strings.Contains(fb[0].Body, "Split the sign-in") {
		t.Fatalf("feedback: %+v", fb)
	}
}

// A path pattern an agent wrote with many ** is matched in time that grows with its length, not
// exponentially (security review of M12), and matches as before.
func TestAPathPatternWithManyStarsIsQuick(t *testing.T) {
	deep := strings.Repeat("d/", 40) + "f.go"
	pat := strings.Repeat("**/", 60) + "nomatch"
	start := time.Now()
	if board.PathMatch(pat, deep) {
		t.Fatal("it matched")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("it took %s", d)
	}
	for _, c := range []struct {
		pat, file string
		want      bool
	}{
		{"internal/mail/**", "internal/mail/send.go", true}, {"internal/mail/**", "internal/mail/x/y.go", true},
		{"**/*.go", "a/b/c.go", true}, {"**/*.go", "c.go", true}, {"a/**/c.go", "a/c.go", true}, {"a/**/c.go", "a/b/x/c.go", true},
		{"a/*/c.go", "a/b/x/c.go", false}, {"*.md", "docs/x.md", false}, {"docs/", "docs/x.md", true}, {"lib", "lib/cart.go", true},
	} {
		if got := board.PathMatch(c.pat, c.file); got != c.want {
			t.Errorf("%s ~ %s: %v", c.pat, c.file, got)
		}
	}
}

// A memory entry's roles, paths, and keywords are kept as they are checked: trimmed, and with no secret
// in them.
func TestAMemoryEntrysListsAreTrimmedAndChecked(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	en, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "Mail", AppliesTo: []string{" builder "}, Keywords: []string{" mail "}}, board.MemorySource{}, "tester")
	if err != nil || en.AppliesTo[0] != "builder" || en.Keywords[0] != "mail" {
		t.Fatalf("%+v %v", en, err)
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "Other", Paths: []string{"ghp_" + strings.Repeat("a", 36)}}, board.MemorySource{}, "tester"); kindOf(err) != board.Invalid {
		t.Fatalf("a secret in a path: %v", err)
	}
}
