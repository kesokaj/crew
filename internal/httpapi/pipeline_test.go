package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// The paths of 11.3, 7.10, and 13.8 that the happy path of TestATicketGoesFromBacklogToDoneWithNoOperator
// does not take, each with agents of its own.

// recording is a turn of agy that says done.
func recording(harness.RunSpec) harness.Script {
	return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}
}

var ticketKey = regexp.MustCompile(`this run works on its ticket (SF-\d+)`)

// keyOf is the ticket a turn's prompt names, or the one its subtask's first prompt named.
func (k *ticketKit) keyOf(spec harness.RunSpec, p string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = map[int64]string{}
	}
	if m := ticketKey.FindStringSubmatch(p); m != nil {
		k.keys[spec.Subtask] = m[1]
	}
	return k.keys[spec.Subtask]
}

// shop makes the running project shop with the optional stages given, limits laid over its settings,
// and tickets that check for a file of their own: SF-n checks `test -f n.txt`.
func (k *ticketKit) shop(stages []string, limits map[string]any, titles ...string) {
	k.t.Helper()
	st, _ := json.Marshal(stages)
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "stages": `+string(st)+`,
		"repository": {"kind": "local"}}`, 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	l := doc["limits"].(map[string]any)
	for key, v := range limits {
		l[key] = v
	}
	doc["sandbox"].(map[string]any)["setup"] = ""
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	for i, title := range titles {
		k.call("POST", "/api/projects/shop/tasks", fmt.Sprintf(`{"title": %q, "why": "", "what": "w", "goal": "g",
			"checks": [{"kind": "cmd", "spec": "test -f %d.txt"}], "docsReason": "none"}`, title, i+1), 201, nil)
	}
	k.call("POST", "/api/projects/shop/ok", "", 200, nil)
}

// run starts the loops of the board, the pipeline, and the run driver's monitor, which retries. Its
// stop returns once the loops have, so a pass that was still going cannot start a run while the test
// reads what was started.
func (k *ticketKit) run() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); k.b.Run(ctx, 50*time.Millisecond) }()
	go func() { defer wg.Done(); k.pipe.Run(ctx, 50*time.Millisecond) }()
	go func() { defer wg.Done(); k.driver.Monitor(ctx, 100*time.Millisecond) }()
	return func() { cancel(); wg.Wait(); k.pipe.Wait() }
}

func (k *ticketKit) stageOf(key string) string { return k.ticket(key).Ticket.Stage }

// pushFromRoot pushes files on a new branch cut from main's first commit, as two builders that began
// before either merged: two such branches that change one file conflict, whichever merges first.
func (k *ticketKit) pushFromRoot(branch string, files map[string]string) {
	k.t.Helper()
	work := k.t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@x",
			"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Errorf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("clone", "-q", k.git.Quarantine.Path("shop"), ".")
	root := run("rev-list", "--max-parents=0", "origin/main")
	if run("ls-remote", "origin", "refs/heads/"+branch) != "" {
		run("fetch", "-q", "origin", branch)
		run("checkout", "-q", "-B", branch, "FETCH_HEAD")
	} else {
		run("checkout", "-q", "-b", branch, root)
	}
	for f, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, f)), 0o755)
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "work")
	run("push", "-q", "origin", branch)
}

// pushMerged pushes files on a branch after merging main into it, keeping the branch's side of a
// conflict, as a builder that resolves one does.
func (k *ticketKit) pushMerged(branch string, files map[string]string) {
	k.t.Helper()
	work := k.t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@x",
			"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			k.t.Errorf("git %v: %v %s", args, err, out)
		}
	}
	run("clone", "-q", "-b", branch, k.git.Quarantine.Path("shop"), ".")
	run("merge", "-q", "-X", "ours", "origin/main", "-m", "merge main")
	for f, body := range files {
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "fix")
	run("push", "-q", "origin", branch)
}

// builder plays agents that approve in analysis, and build a ticket by pushing its own file and a.txt,
// which every ticket changes; a fix merges main and pushes again.
func (k *ticketKit) builder(extra func(kind, key string, spec harness.RunSpec, p string) bool) {
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		n := strings.TrimPrefix(key, "SF-")
		branch := map[string]string{"SF-1": "task/SF-1-one", "SF-2": "task/SF-2-two", "SF-3": "task/SF-3-three"}[key]
		if extra != nil && extra(kind, key, spec, p) {
			return recording(spec), true
		}
		switch kind {
		case "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none"})
		case "build":
			k.pushFromRoot(branch, map[string]string{n + ".txt": key, "a.txt": "from " + key + "\n"})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "fix":
			k.pushMerged(branch, map[string]string{n + ".fixed": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Merged main, kept " + key})
			k.tool(spec.Run, "fix_done", map[string]any{})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
}

// When In progress is a project's last stage, a ticket whose merge conflicts gets a fix round, and the
// fixed head gets its checks run before it merges: the second of two tickets that change one file
// merges too. The builder is never told that the merge failed its build, and each ticket has one
// merge subtask, recorded once main moved.
func TestAConflictInTheLastStageIsFixedAndMerged(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	// The first run of the checks on a fixed head is Crew's own error: it is run again.
	var mu sync.Mutex
	erred := false
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		mu.Lock()
		defer mu.Unlock()
		fixed := false
		for _, n := range tarNames(spec.Tree) {
			fixed = fixed || strings.HasSuffix(n, ".fixed")
		}
		if fixed && !erred && strings.HasPrefix(c.Name, "test -f") && len(spec.Cmds) == 1 {
			erred = true
			return harness.CheckResult{Code: 124, Error: "it ran longer than 30m0s"}
		}
		return checkSandbox(spec, c)
	}
	k.shop([]string{}, nil, "one", "two")
	defer k.run()()
	// The merge subtask is recorded once main moved, by the pass that sees it: at the latest the next.
	k.until("both Done, with their merges recorded", func() bool {
		return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" && k.count(`SELECT count(*) FROM subtasks WHERE kind = 'merge'`) >= 2
	})
	k.mu.Lock()
	fixes := k.turns["fix"]
	for _, p := range append(k.prompts["build-again"], k.prompts["comment"]...) {
		if strings.Contains(p, "merges_and_passes_on_main") {
			t.Error("a builder was told the merge failed its build")
		}
	}
	k.mu.Unlock()
	if fixes != 1 {
		t.Fatalf("%d fix rounds", fixes)
	}
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'merge' AND state = 'failed' AND detail LIKE '%conflicts with main in: a.txt%'`); n != 1 {
		t.Fatalf("%d conflicts recorded", n)
	}
	// Its fix round says what failed, not only the stage's word (dave, 0.2.6: "merge").
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'rework' AND detail->>'reason' = 'merge'
		AND detail->>'text' = 'The merge with main failed: The ticket''s branch conflicts with main in: a.txt. Merge main into the branch, resolve the conflicts, and push.'`); n != 1 {
		t.Fatalf("%d fix rounds say what failed in the merge", n)
	}
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'exit' AND state = 'error'`); n != 1 {
		t.Fatalf("%d exit runs ended as errors, not the 1 that was run again", n)
	}
	if n := k.count(`SELECT count(*) FROM (SELECT ticket FROM subtasks WHERE kind = 'merge' GROUP BY ticket HAVING count(*) = 1) x`); n != 2 {
		t.Fatalf("%d tickets have one merge subtask, not 2", n)
	}
}

// A ticket at its limit of fix rounds goes to the lead, who looks at it again (7.7), and gives up the
// project's turn to merge: the ticket after it merges.
func TestTheMergeQueueGoesOnPastATicketAtItsLimit(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	// A fix that does not merge main: the conflict stays, and the second one reaches the limit.
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind != "fix" {
			return false
		}
		branch := map[string]string{"SF-1": "task/SF-1-one", "SF-2": "task/SF-2-two"}[key]
		k.pushFromRoot(branch, map[string]string{"tried.txt": fmt.Sprint(time.Now().UnixNano())})
		k.tool(spec.Run, "add_note", map[string]any{"text": "Tried."})
		k.tool(spec.Run, "fix_done", map[string]any{})
		return true
	})
	k.shop([]string{}, map[string]any{"max_fix_rounds": 1}, "one", "two")
	defer k.run()()
	k.until("one Done and the other waiting", func() bool {
		a, b := k.ticket("SF-1").Ticket, k.ticket("SF-2").Ticket
		waits := func(v m6View) bool {
			return k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket JOIN projects p ON p.id = t.project
				WHERE p.key || '-' || t.number = $1 AND s.kind = 'look_again' AND s.title LIKE 'Look again: it used its 1 fix rounds%'`, v.Ticket.Key) > 0
		}
		return (a.Stage == "done" && waits(k.ticket("SF-2"))) || (b.Stage == "done" && waits(k.ticket("SF-1")))
	})
	// A third ticket that conflicts with neither is added and merges past the waiting one.
	k.call("POST", "/api/projects/shop/tasks", `{"title": "three", "why": "w", "what": "w", "goal": "g",
		"checks": [{"kind": "cmd", "spec": "test -f 3.txt"}], "docsReason": "none"}`, 201, nil)
	k.mu.Lock()
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if kind == "build" && k.keyOf(spec, p) == "SF-3" {
			k.push(spec.Run, "task/SF-3-three", map[string]string{"3.txt": "three"})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built SF-3"})
			k.tool(spec.Run, "request_move", map[string]any{})
			return recording(spec), true
		}
		return prev(kind, spec, p)
	}
	k.mu.Unlock()
	k.until("SF-3 Done", func() bool { return k.stageOf("SF-3") == "done" })
}

// The goal loop ends after limits.max_goal_rounds failed rounds; the run is retried once, with a budget
// of rounds of its own, and after that its subtask is stopped and the lead looks at the ticket again
// (7.7). A round is a signal once, when its turn is given.
func TestTheGoalLoopEndsAtItsLimitAndTheRetryHasItsOwnRounds(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if !strings.HasPrefix(kind, "build") {
			return false
		}
		// A builder that never pushes its ticket's file.
		k.push(spec.Run, "task/SF-1-one", map[string]string{"other.txt": fmt.Sprint(time.Now().UnixNano())})
		k.tool(spec.Run, "add_note", map[string]any{"text": "Done, I think."})
		k.tool(spec.Run, "request_move", map[string]any{})
		return true
	})
	k.shop([]string{}, map[string]any{"max_goal_rounds": 2}, "one")
	defer k.run()()
	k.until("its run stopped twice, and the lead looks again", func() bool {
		return k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1 AND s.kind = 'look_again'`) > 0
	})
	k.mu.Lock()
	builds := k.turns["build"] + k.turns["build-again"]
	k.mu.Unlock()
	// Two runs, each with its first turn and one more round.
	if builds != 4 {
		t.Fatalf("%d turns of the builder", builds)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_round' AND NOT detail ? 'last'`); n != 2 {
		t.Fatalf("%d rounds given as signals", n)
	}
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'build' AND r.stop_reason = 'rounds'`); n != 2 {
		t.Fatalf("%d runs ended at their limit", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'build' AND state = 'stopped'`); n != 1 {
		t.Fatal("the build is not stopped")
	}
	// The failed checks reach the builder as data: they name the ticket's checks, which an agent wrote.
	k.mu.Lock()
	again := k.prompts["build-again"]
	k.mu.Unlock()
	if len(again) == 0 || !strings.Contains(again[0], "BEGIN The exit checks that did not pass") {
		t.Fatalf("the round's failures are not fenced: %q", again)
	}
}

// A reviewer that ends its turn without write_review is reminded once; then its run fails, is retried
// once with a reminder of its own, and its subtask is stopped.
func TestAMissingCallGetsOneReminderARun(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		return kind == "review" || kind == "other"
	})
	k.shop([]string{"review"}, nil, "one")
	defer k.run()()
	k.until("the review stopped", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'review' AND state = 'stopped'`) == 1
	})
	reminders := k.count(`SELECT count(*) FROM turns WHERE speaker = 'operator' AND text LIKE 'You ended your turn without calling write_review%'`)
	if reminders != 2 {
		t.Fatalf("%d reminders", reminders)
	}
}

// A decision whose checks cannot be run on main is never applied: the analysis ends, and the ticket
// does not leave Analysis.
func TestABaseCheckThatCannotRunApprovesNothing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.f.CheckErr = errors.New("the check sandbox does not start")
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("the analysis to fail", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'analysis' AND state = 'stopped'`) >= 1
	})
	v := k.ticket("SF-1")
	if v.Ticket.Stage != "analysis" {
		t.Fatalf("SF-1 left Analysis: %s", v.Ticket.Stage)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE analysis->>'status' = 'ready'`); n != 0 {
		t.Fatal("a decision was applied with no base check")
	}
}

// A merge whose check fails once and passes when it is run again merges, recorded as flaky.
func TestAFlakyMergeCheckMerges(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	var mu sync.Mutex
	failed := false
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(c.Name, "the project's tests") && !failed {
			failed = true
			return harness.CheckResult{Code: 1, Output: "FAIL: once"}
		}
		return checkSandbox(spec, c)
	}
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	if k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'merge' AND state = 'passed' AND flaky`) != 1 ||
		k.count(`SELECT count(*) FROM signals WHERE kind = 'flaky_check'`) != 1 {
		t.Fatal("the flaky merge is not recorded")
	}
}

// A merge that ends as Crew's own error three times stops being tried: the errors are counted by the
// head they merge, not by the candidate, which is a new commit each time.
func TestMergeErrorsStopAtThree(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		if strings.HasPrefix(c.Name, "the project's tests") {
			// Over a second each, so every candidate is made in a second of its own and is another
			// commit: only errors counted by the head reach three.
			time.Sleep(1100 * time.Millisecond)
			return harness.CheckResult{Code: 124, Error: "it ran longer than 30m0s"}
		}
		return checkSandbox(spec, c)
	}
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 to wait", func() bool {
		return strings.Contains(deref(k.ticket("SF-1").Ticket.Stuck), "could not check its merge with main three times")
	})
	time.Sleep(500 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'merge'`); n != 3 {
		t.Fatalf("%d merges", n)
	}
	if n := k.count(`SELECT count(DISTINCT commit) FROM check_runs WHERE purpose = 'merge'`); n != 3 {
		t.Fatalf("the candidates were %d commits, not 3: the test does not tell head from candidate", n)
	}
}

// A ticket that changes a file that is code for agy, or a GitHub workflow, never merges by itself
// (13.8): its checks pass, and main does not move.
func TestFilesThatAreCodeDoNotMerge(t *testing.T) {
	t.Parallel()
	for _, f := range []string{".agents/hooks.json", ".agents/plugins/p/plugin.json", ".github/workflows/ci.yml"} {
		t.Run(f, func(t *testing.T) {
			k := newTicketKit(t)
			k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
				if kind != "build" {
					return false
				}
				k.push(spec.Run, "task/SF-1-one", map[string]string{"1.txt": "one", f: "{}"})
				k.tool(spec.Run, "add_note", map[string]any{"text": "Built"})
				k.tool(spec.Run, "request_move", map[string]any{})
				return true
			})
			k.shop([]string{}, nil, "one")
			main := ""
			defer k.run()()
			k.until("SF-1 to wait", func() bool {
				if main == "" {
					main = k.git.Main(context.Background(), "shop")
				}
				return strings.Contains(deref(k.ticket("SF-1").Ticket.Waiting), "it changes files that are code ("+f)
			})
			time.Sleep(300 * time.Millisecond)
			if k.stageOf("SF-1") != "in_progress" || k.git.Main(context.Background(), "shop") != main ||
				k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'merge'`) != 0 {
				t.Fatal("a ticket that changes code merged, or was tried")
			}
		})
	}
}

// A run's tools reach its own ticket and its own kind of run only: a reply to a comment it was not
// given, a move a reviewer asks for, a second review, a note past the cap, and a decision of a builder
// are refused.
func TestARunsToolsAreItsOwn(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var mu sync.Mutex
	runsOf := map[string]string{}
	k.builder(nil)
	analyse := k.custom
	// The builders' turns stay open, so their tokens answer at the Crew MCP (11.5).
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if kind == "build" {
			mu.Lock()
			runsOf[k.keyOf(spec, p)+":"+kind] = spec.Run
			mu.Unlock()
			sc := recording(spec)
			sc.Hold = true
			return sc, true
		}
		return analyse(kind, spec, p)
	}
	k.shop([]string{"review"}, nil, "one", "two")
	defer k.run()()
	k.until("both builders working", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runsOf["SF-1:build"] != "" && runsOf["SF-2:build"] != ""
	})
	mu.Lock()
	b1 := runsOf["SF-1:build"]
	mu.Unlock()
	var other board.Record
	k.call("POST", "/api/projects/shop/comments", `{"ticket": "SF-2", "text": "For SF-2 only."}`, 201, &other)
	var onPlan board.Record
	k.call("POST", "/api/projects/shop/comments", `{"text": "On the plan."}`, 201, &onPlan)
	refused := func(name string, args any) {
		t.Helper()
		if _, ok := k.tool(b1, name, args); ok {
			t.Errorf("%s was answered", name)
		}
	}
	refused("reply_comment", map[string]any{"id": other.ID, "text": "Silenced."})
	refused("reply_comment", map[string]any{"id": onPlan.ID, "text": "Silenced."})
	refused("write_review", map[string]any{"verdict": "approve", "text": "Fine."})
	refused("analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none"})
	refused("fix_done", map[string]any{})
	for i := 0; i < 20; i++ {
		if _, ok := k.tool(b1, "add_note", map[string]any{"text": "note"}); !ok {
			t.Fatalf("note %d was refused", i+1)
		}
	}
	refused("add_note", map[string]any{"text": "one too many"})
	if k.count(`SELECT count(*) FROM records WHERE id IN ($1, $2) AND resolved`, other.ID, onPlan.ID) != 0 {
		t.Fatal("a comment was resolved by a run it was not given to")
	}
}

// A check run that a server before this one left running is an error when the pipeline starts, so it
// is tried again; and a conversation's summary that fails is tried three times, then left, while the
// project's tickets go on.
func TestARestartEndsACheckRunAndSummariesStopAtThree(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.builder(nil)
	played := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if kind == "conversation" {
			return harness.Script{Lines: answerLines(spec, "")}, true
		}
		return played(kind, spec, p)
	}
	k.shop([]string{}, nil, "one")
	var proj string
	var ticket int64
	k.d.Server.QueryRow(ctx, `SELECT p.id, t.id FROM projects p JOIN tickets t ON t.project = p.id WHERE p.name = 'shop'`).Scan(&proj, &ticket)
	var left int64
	k.d.Server.QueryRow(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit) VALUES ($1, $2, 'exit', repeat('a', 40)) RETURNING id`,
		proj, ticket).Scan(&left)
	// A conversation of 26 turns, whose summary writer answers with nothing.
	const conv = "c0000000-0000-4000-8000-0000000000c2"
	k.d.Server.Exec(ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'run', 'agy')`, conv, proj)
	for n := 1; n <= 26; n++ {
		k.d.Server.Exec(ctx, `INSERT INTO turns (conversation, number, speaker, text) VALUES ($1, $2, 'agent', 'x')`, conv, n)
	}
	defer k.run()()
	k.until("three summary tries, and the ticket Done", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'conversation_summary' AND state = 'done'`) == 3 && k.stageOf("SF-1") == "done"
	})
	time.Sleep(500 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'conversation_summary'`); n != 3 {
		t.Fatalf("%d tries of a summary", n)
	}
	if k.count(`SELECT count(*) FROM check_runs WHERE id = $1 AND state = 'error' AND detail = 'Crew restarted while it ran'`, left) != 1 {
		t.Fatal("the check run left running is not an error")
	}
}

// A conversation's summary whose run cannot be finished, as one no server follows any more, is logged,
// and the project's tickets go on.
func TestASummaryThatFailsStopsNoTicket(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	var proj string
	k.d.Server.QueryRow(ctx, `SELECT id FROM projects WHERE name = 'shop'`).Scan(&proj)
	const conv = "c0000000-0000-4000-8000-0000000000c3"
	k.d.Server.Exec(ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'run', 'agy')`, conv, proj)
	var sub int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state, summarises, through)
		VALUES ('project', $1, 99, 'conversation_summary', 'summary', 'idle', $2, 20) RETURNING id`, proj, conv).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation)
		VALUES ('a0000000-0000-4000-8000-0000000000a1', $1, 1, 'idle', 'agy', 'x', 'x', $2)`, sub, conv); err != nil {
		t.Fatal(err)
	}
	defer k.run()()
	k.until("SF-1 Done beside a summary that fails", func() bool { return k.stageOf("SF-1") == "done" })
}

// A ticket on hold in Analysis gets no analysis until it is let go (15.2).
func TestATicketOnHoldIsNotAnalysed(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go k.b.Run(ctx, 50*time.Millisecond)
	k.until("SF-1 in Analysis", func() bool { return k.stageOf("SF-1") == "analysis" })
	k.call("PATCH", "/api/projects/shop/tasks/SF-1", `{"hold": true}`, 200, nil)
	go k.pipe.Run(ctx, 50*time.Millisecond)
	time.Sleep(800 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'analysis'`); n != 0 {
		t.Fatalf("%d analyses of a ticket on hold", n)
	}
	k.call("PATCH", "/api/projects/shop/tasks/SF-1", `{"hold": false}`, 200, nil)
	k.until("its analysis once it is let go", func() bool { return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'analysis'`) == 1 })
	// The analysis' turn ends before the test does: its agent calls the Crew MCP, which needs the
	// database the test's end takes away.
	k.until("the analysis' turn ends", func() bool {
		return k.count(`SELECT count(*) FROM runs WHERE state IN ('starting', 'working')`) == 0
	})
}

// A ticket withdrawn in Planning gets the facts of a summary and no summary writer: it never ran
// (8.4, decided after M6).
func TestATicketWithdrawnInPlanningGetsFactsOnly(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "stages": [], "repository": {"kind": "local"}}`, 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = ""
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	for i, title := range []string{"one", "two"} {
		k.call("POST", "/api/projects/shop/tasks", fmt.Sprintf(`{"title": %q, "why": "w", "what": "w", "goal": "g",
			"checks": [{"kind": "cmd", "spec": "test -f %d.txt"}], "docsReason": "none"}`, title, i+1), 201, nil)
	}
	k.call("POST", "/api/projects/shop/tasks/SF-2/discard", `{"reason": "Not needed after all."}`, 200, nil)
	k.call("POST", "/api/projects/shop/ok", "", 200, nil)
	defer k.run()()
	k.until("SF-1 Done with its summary", func() bool {
		return k.count(`SELECT count(*) FROM summaries s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1 AND NOT s.missing`) == 1
	})
	if k.count(`SELECT count(*) FROM summaries s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2 AND NOT s.missing
		AND s.done IS NULL AND (s.facts->>'withdrawn_in_planning')::boolean`) != 1 {
		t.Fatal("the withdrawn ticket has no facts, or was given written sections")
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2`); n != 0 {
		t.Fatalf("%d runs on a ticket withdrawn in Planning", n)
	}
}
