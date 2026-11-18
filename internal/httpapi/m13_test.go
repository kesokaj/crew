package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

// room gives the kit a machine of this many cores and GB, and the 4 GB the reserve grew by in 1.08, so
// its room is what it was: the run driver, the pipeline's check sandboxes, and the front page count
// its room (7.9).
func (k *ticketKit) room(cores, gb int) *capacity.Gate {
	g := &capacity.Gate{Pool: k.d.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: cores, MemMiB: (gb + 4) * 1024}, nil
		},
		Settings: func(ctx context.Context) (settings.Doc, error) {
			return settings.Effective(ctx, k.d.Server, settings.Machine{Version: "1.0.0"})
		}}
	k.driver.Room, k.pipe.Room = g, g
	k.api.Room.Store(g)
	return g
}

type overviewRoom struct {
	Room *struct {
		Cores int `json:"cores"`
		Room  struct {
			CPUs  float64 `json:"cpus"`
			MemMB int64   `json:"memMB"`
		} `json:"room"`
		Used struct {
			CPUs  float64 `json:"cpus"`
			Large int     `json:"large"`
			Small int     `json:"small"`
		} `json:"used"`
		Waiting []struct {
			Ticket string `json:"ticket"`
			Size   string `json:"size"`
			What   string `json:"what"`
		} `json:"waiting"`
	} `json:"room"`
}

// agentRun is a running agent on a project's Agents screen.
type agentRun struct {
	Role, Subtask, CLI string
	Size, Sandbox      *string
}

// builders is the builders among running agents.
func builders(running []agentRun) []agentRun {
	var out []agentRun
	for _, r := range running {
		if r.Role == "builder" {
			out = append(out, r)
		}
	}
	return out
}

// The machine is full (21): runs wait, and the Projects screen shows what waits and why. On a machine
// with room for one large sandbox, SF-1's builder takes it; SF-2's next run waits for room, which its
// card and the front page say; when SF-1's builder ends, SF-2 goes on, and both are Done.
func TestWhenTheMachineIsFullRunsWaitAndTheFrontPageSaysWhat(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.room(4, 12) // 4 virtual CPUs and 8 GB: one large sandbox, or two small
	release := make(chan struct{})
	k.builder(nil)
	built := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		sc, ok := built(kind, spec, p)
		if kind == "build" && k.keyOf(spec, p) == "SF-1" {
			sc.Wait = release
		}
		return sc, ok
	}
	k.shop([]string{}, nil, "One", "Two")
	stop := k.run()
	defer stop()
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	var o overviewRoom
	// The project's Agents screen: SF-1's builder runs in a large sandbox, SF-2 waits for room, and
	// the analyses that ended say how.
	var agents struct {
		Running  []agentRun                   `json:"running"`
		Waiting  []struct{ What, For string } `json:"waiting"`
		Finished []struct {
			Role, Result string
		} `json:"finished"`
	}
	var seen string
	defer func() {
		if t.Failed() {
			t.Logf("last seen: %s", seen)
		}
	}()
	k.until("SF-1 builds and SF-2 waits for room, as the front page, the card, and the Agents screen say", func() bool {
		k.call("GET", "/api/overview", "", 200, &o)
		k.call("GET", "/api/projects/shop/sandboxes", "", 200, &agents)
		seen = fmt.Sprintf("room %+v, SF-2 stuck %q, agents %+v", o.Room, deref(k.ticket("SF-2").Ticket.Stuck), agents)
		return o.Room != nil && o.Room.Used.Large == 1 && len(o.Room.Waiting) == 1 && o.Room.Waiting[0].Ticket == "SF-2" &&
			strings.Contains(deref(k.ticket("SF-2").Ticket.Stuck), "waits for room on the machine") &&
			len(builders(agents.Running)) == 1 && len(agents.Waiting) == 1
	})
	if o.Room.Cores != 4 || o.Room.Room.CPUs != 4 || o.Room.Room.MemMB != 8192 || o.Room.Used.CPUs != 4 {
		t.Fatalf("the front page's room: %+v", o.Room)
	}
	// SF-1's builder holds the room. Which of SF-2's runs waits for it depends on which came first: its
	// build, or, when SF-1's builder started before SF-2's analysis was through, the analysis's checks
	// on main, whose lead is then idle and not counted (7.9).
	if b := builders(agents.Running); !strings.HasPrefix(b[0].Subtask, "SF-1.") || deref(b[0].Size) != "large" || b[0].Sandbox == nil {
		t.Fatalf("running: %+v", agents.Running)
	}
	if !strings.HasPrefix(agents.Waiting[0].What, "SF-2 ") || !strings.HasPrefix(agents.Waiting[0].For, "room on the machine for a ") {
		t.Fatalf("waiting: %+v", agents.Waiting)
	}
	if len(agents.Finished) < 1 || agents.Finished[0].Role != "curator" || agents.Finished[0].Result != "Approved, ready to build" {
		t.Fatalf("finished: %+v", agents.Finished)
	}
	free()
	k.until("both tickets are Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	if n := k.count(`SELECT count(*) FROM runs WHERE size IS NULL`); n != 0 {
		t.Fatalf("%d runs have no size", n)
	}
}

// A check sandbox is large, and waits for room as a run does (7.9): with a terminal and a login check
// on a machine of 8 virtual CPUs, SF-1's analysis fits, and the checks it runs on main wait, which its
// card says, until the login check's sandbox is gone.
func TestACheckSandboxWaitsForRoomToo(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.room(6, 16) // 8 virtual CPUs and 12 GB
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO sandboxes (id, kind, container, image) VALUES
		('crew-term', 'terminal', 'c1', 'img'), ('crew-login', 'login-check', 'c2', 'img')`); err != nil {
		t.Fatal(err)
	}
	k.builder(nil)
	k.shop([]string{}, nil, "One")
	stop := k.run()
	defer stop()
	k.until("SF-1's checks on main wait for room", func() bool {
		return strings.HasPrefix(deref(k.ticket("SF-1").Ticket.Stuck), "Crew's checks: waits for room on the machine")
	})
	if n := k.count(`SELECT count(*) FROM check_runs`); n != 0 {
		t.Fatalf("%d check runs started on a full machine", n)
	}
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE sandboxes SET removed_at = now() WHERE id = 'crew-login'`); err != nil {
		t.Fatal(err)
	}
	k.until("SF-1 is Done", func() bool { return k.stageOf("SF-1") == "done" })
}

// setupKit plays a project whose setup is `make setup`, broken as broken says from the files of the
// tree it runs on: a run's branch, or main for a run with none, and the tree of a check sandbox when
// checks is true. Runs whose setup failed never played a turn: the kit learns their kind and ticket
// from the prompt.
func setupKit(t *testing.T, broken func(has func(path string) bool) bool, checks bool) *ticketKit {
	k := newTicketKit(t)
	k.f.Setup = func(spec harness.RunSpec) (bool, string) {
		k.keyOf(spec, spec.Prompt)
		k.mu.Lock()
		for marker, kind := range map[string]string{"# In progress: build one ticket": "build", "# Test: test one ticket": "test"} {
			if strings.Contains(spec.Prompt, marker) {
				k.kindOf[spec.Subtask] = kind
			}
		}
		k.mu.Unlock()
		// A branch not pushed yet starts from main, as crew-run checks it out.
		branch := spec.Branch
		h, _ := k.git.Head(context.Background(), "shop", branch)
		has := func(path string) bool {
			if branch == "" || h == "" {
				return mainFile(k.git, context.Background(), path) != nil
			}
			b, _ := k.git.BranchFile(context.Background(), "shop", branch, path)
			return b != nil
		}
		if broken(has) {
			return false, "make: *** No rule to make target 'node_modules'."
		}
		return true, ""
	}
	if checks {
		check := k.f.Check
		k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
			names := tarNames(spec.Tree)
			if spec.Setup != "" && broken(func(path string) bool { return slices.Contains(names, path) }) {
				return harness.CheckResult{Code: 2, Error: harness.SetupError + " ended with 2", Output: "No rule to make target 'node_modules'."}
			}
			return check(spec, c)
		}
	}
	return k
}

// withSetup gives the project the setup command.
func (k *ticketKit) withSetup() {
	k.t.Helper()
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = "make setup"
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
}

// setupAgents approve, build a ticket by pushing its own file on the ticket's branch with extra files,
// test with a pass, and analyse the setup's fix ticket with the check that it adds setup.fixed, which
// its builder pushes.
func (k *ticketKit) setupAgents(extra func(key string) map[string]string) {
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		fix := strings.Contains(p, "Fix the project's setup")
		switch kind {
		case "analysis":
			args := map[string]any{"choice": "approve", "docsReason": "none"}
			if fix {
				args["checks"] = []map[string]any{{"kind": "cmd", "spec": "test -f setup.fixed"}}
			}
			k.tool(spec.Run, "analyse_ticket", args)
		case "build":
			files := map[string]string{strings.TrimPrefix(key, "SF-") + ".txt": key}
			if fix {
				files = map[string]string{"setup.fixed": "yes"}
			}
			if extra != nil {
				maps.Copy(files, extra(key))
			}
			k.push(spec.Run, spec.Branch, files)
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "test":
			k.tool(spec.Run, "add_test_result", map[string]any{"result": "pass", "text": "Tested " + key})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
}

// A project's setup that fails (21, 13.5): runs and check sandboxes end before their agent or their
// commands start. Two in a row have the setup run on main, where it fails too: one fix ticket, with news, and the runs that need the setup
// wait for it. Its builder starts although the setup fails. Once it is Done, the waiting tickets'
// branches are brought up to date with main, their runs go on, and every ticket is Done; no run that
// failed at its setup counted as a stop.
func TestAProjectsSetupThatFailsGetsOneFixTicket(t *testing.T) {
	t.Parallel()
	k := setupKit(t, func(has func(string) bool) bool { return !has("setup.fixed") }, true)
	k.setupAgents(nil)
	k.shop([]string{}, nil, "One", "Two")
	k.withSetup()
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["check_network"] = "sources"
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	stop := k.run()
	defer stop()
	// The news follows the ticket, in a statement of its own.
	k.until("the setup's fix ticket and its news", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`) == 1 &&
			k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed' AND detail->>'text' LIKE '%SF-3 fixes it%'`) > 0
	})
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed' AND detail->>'text' LIKE '%SF-3 fixes it%'`); n != 1 {
		t.Fatalf("%d news of the setup", n)
	}
	// Here the checks of Analysis on main find it first: a check sandbox runs the setup too.
	if n := k.count(`SELECT (SELECT count(*) FROM runs WHERE stop_reason = 'setup') +
		(SELECT count(*) FROM check_runs WHERE state = 'error' AND setup_failed)`); n < 2 {
		t.Fatalf("%d runs and checks ended at their setup", n)
	}
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE state = 'failed'`); n != 1 {
		t.Fatalf("%d checks of the setup on main failed", n)
	}
	k.until("SF-1's card says it waits for SF-3", func() bool {
		return strings.Contains(deref(k.ticket("SF-1").Ticket.Stuck), "waits for SF-3, which fixes the project's setup")
	})
	k.until("every ticket is Done", func() bool {
		return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" && k.stageOf("SF-3") == "done"
	})
	// While SF-3 was open, nothing that needs the setup started for SF-1 and SF-2.
	if n := k.count(`SELECT (SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN tickets t ON t.id = s.ticket
			WHERE t.number IN (1, 2) AND s.role IN ('builder', 'tester', 'e2e')
			AND r.started_at BETWEEN (SELECT created_at FROM tickets WHERE number = 3) AND (SELECT stage_at FROM tickets WHERE number = 3))
		+ (SELECT count(*) FROM check_runs c JOIN tickets t ON t.id = c.ticket WHERE t.number IN (1, 2)
			AND c.started_at BETWEEN (SELECT created_at FROM tickets WHERE number = 3) AND (SELECT stage_at FROM tickets WHERE number = 3))`); n != 0 {
		t.Fatalf("%d runs and checks that need the setup started while its fix ticket was open", n)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`); n != 1 {
		rows, _ := k.d.Server.Query(context.Background(), `SELECT number, stage, coalesce(created_by, '<null>') FROM tickets WHERE title = 'Fix the project''s setup'`)
		for rows.Next() {
			var n int
			var st, by string
			_ = rows.Scan(&n, &st, &by)
			t.Logf("SF-%d %s created by %q", n, st, by)
		}
		rows.Close()
		t.Fatalf("%d fix tickets", n)
	}
	if n := k.count(`SELECT coalesce(sum(stops), 0) FROM subtasks`); n != 0 {
		t.Fatalf("failures at the setup counted %d stops", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again'`); n != 0 {
		t.Fatal("the lead looked again at a ticket that only waited for the setup")
	}
	// The fix ticket's builder started although the setup failed in its sandbox.
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN tickets t ON t.id = s.ticket
		WHERE t.number = 3 AND s.kind = 'build' AND r.stop_reason = 'setup'`); n != 0 {
		t.Fatal("the fix ticket's builder was stopped by the setup it fixes")
	}
	// The check of the setup on main ran in the project's check sandboxes' mode, as every check did
	// (13.6, since 0.53).
	stop()
	onMain := 0
	for _, c := range append([]harness.CheckSpec(nil), k.f.Checks...) {
		if c.Mode != "sources" {
			t.Fatalf("a check sandbox in %q: %+v", c.Mode, c.Cmds)
		}
		if len(c.Cmds) == 1 && c.Cmds[0].Name == "the project's setup on main" {
			onMain++
		}
	}
	if onMain == 0 {
		t.Fatal("no check of the setup on main")
	}
}

// A setup that only a ticket's branch breaks is the ticket's own (13.5, since 0.47): it passes on main,
// so no fix ticket is made, and the failures count as stops of the subtask, which the lead looks at.
// SF-1's builder pushes setup.broken, which breaks the setup of its tester's sandboxes.
func TestASetupOnlyABranchBreaksIsTheTicketsOwn(t *testing.T) {
	t.Parallel()
	k := setupKit(t, func(has func(string) bool) bool { return has("setup.broken") }, false)
	k.setupAgents(func(key string) map[string]string { return map[string]string{"setup.broken": key} })
	k.shop([]string{"test"}, nil, "One")
	k.withSetup()
	stop := k.run()
	defer stop()
	k.until("the lead looks again at SF-1", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again'`) >= 1
	})
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE state = 'passed'`); n != 1 {
		t.Fatalf("%d checks of the setup on main passed", n)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`); n != 0 {
		t.Fatal("a fix ticket for a setup that works on main")
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'test' AND stops >= 2`); n != 1 {
		t.Fatalf("%d test subtasks with their setup failures counted as stops", n)
	}
}

// A ticket whose tester waited for the setup's fix goes on with its branch brought up to date with
// main (13.5, since 0.47): SF-1 is built while the setup works; then the setup breaks, its tester fails
// at it twice on SF-1's branch, and the fix ticket is made. Once that is Done, SF-1's branch has main's
// fix joined in, its tester passes, and SF-1 is Done with no stop counted.
func TestATicketThatWaitedForTheSetupGoesOnUpToDate(t *testing.T) {
	t.Parallel()
	var broken atomic.Bool
	k := setupKit(t, func(has func(string) bool) bool { return broken.Load() && !has("setup.fixed") }, true)
	k.setupAgents(nil)
	k.shop([]string{"test"}, nil, "One")
	k.withSetup()
	stop := k.run()
	defer stop()
	k.until("SF-1 is built", func() bool { return k.stageOf("SF-1") == "test" })
	broken.Store(true)
	k.until("the setup's fix ticket", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`) == 1
	})
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'test' AND state = 'waiting' AND wait_for = 'setup'`); n != 1 {
		t.Fatalf("%d tester subtasks wait for the setup", n)
	}
	k.until("SF-1 and the fix are Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	var branch string
	k.d.Server.QueryRow(context.Background(), `SELECT branch FROM tickets WHERE number = 1`).Scan(&branch)
	if b, _ := k.git.BranchFile(context.Background(), "shop", branch, "setup.fixed"); b == nil {
		t.Fatalf("%s was not brought up to date with main's fix", branch)
	}
	if n := k.count(`SELECT coalesce(sum(stops), 0) FROM subtasks`); n != 0 {
		t.Fatalf("failures at the setup counted %d stops", n)
	}
}

// An end-to-end run that cannot start the application twice in a row goes to the lead (21, since
// 0.47): Crew's own E2E run tells it apart by its exit code, 3. SF-1's application never starts: one
// fix round, then the lead looks again. SF-2's run could not start, then failed a test, then could not
// start again, then passed: the failing test broke the row, so each was a fix round and it is Done.
func TestAnEndToEndRunThatCannotStartTheApplicationTwiceGoesToTheLead(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	fixes := func(key string, names []string) int {
		n := 0
		for _, f := range names {
			if strings.HasPrefix(f, strings.TrimPrefix(key, "SF-")+".fix") {
				n++
			}
		}
		return n
	}
	check := k.f.Check
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		if !strings.HasPrefix(c.Name, "the end-to-end tests: ") {
			return check(spec, c)
		}
		names := tarNames(spec.Tree)
		cannot := harness.CheckResult{Code: 3, Output: "the application could not start: port 8080 is taken"}
		switch {
		case slices.Contains(names, "1.txt"):
			return cannot
		case slices.Contains(names, "2.txt"):
			switch fixes("SF-2", names) {
			case 0, 2:
				return cannot
			case 1:
				return harness.CheckResult{Code: 1, Output: "FAIL: sign-in shows no error"}
			}
		}
		return harness.CheckResult{Passed: true}
	}
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		switch kind {
		case "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none"})
		case "build":
			k.push(spec.Run, spec.Branch, map[string]string{strings.TrimPrefix(key, "SF-") + ".txt": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "fix":
			var names []string
			for i := 1; i < 9; i++ {
				f := fmt.Sprintf("%s.fix%d", strings.TrimPrefix(key, "SF-"), i)
				if b, _ := k.git.BranchFile(context.Background(), "shop", spec.Branch, f); b == nil {
					names = append(names, f)
					break
				}
			}
			k.push(spec.Run, spec.Branch, map[string]string{names[0]: key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Fixed " + key})
			k.tool(spec.Run, "fix_done", map[string]any{})
		case "e2e":
			k.tool(spec.Run, "add_e2e_result", map[string]any{"result": "pass", "text": "Drove the flows of " + key})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
	k.shop([]string{"e2e"}, map[string]any{"max_fix_rounds": 5}, "One", "Two")
	stop := k.run()
	defer stop()
	k.until("the lead looks again at SF-1", func() bool {
		return k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1 AND s.kind = 'look_again'
			AND s.title LIKE 'Look again: Crew''s end-to-end run could not start the application 2 times%'`) == 1
	})
	if n := k.count(`SELECT fix_rounds FROM tickets WHERE number = 1`); n != 1 {
		t.Fatalf("SF-1 had %d fix rounds before the lead, want the one after its first run", n)
	}
	k.until("SF-2 is Done", func() bool { return k.stageOf("SF-2") == "done" })
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2 AND s.kind = 'look_again'`); n != 0 {
		t.Fatal("the lead looked again at SF-2, whose failing test broke the row")
	}
	if n := k.count(`SELECT fix_rounds FROM tickets WHERE number = 2`); n != 3 {
		t.Fatalf("SF-2 had %d fix rounds, want 3", n)
	}
}

// A builder who weakens its own test (21, 7.1): the reviewer is shown every test file the change
// touches, with its diff, and an approve that does not say why each is right is refused. SF-1's
// builder changes lock_test.go and deletes tests/old_test.py; the reviewer's approve without reasons
// is refused, and with them it is recorded with the reasons.
func TestTheReviewerIsShownEveryChangedTestFile(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var refusal string
	var prompt string
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		switch kind {
		case "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none"})
		case "build":
			k.push(spec.Run, spec.Branch, map[string]string{"1.txt": key, "lock_test.go": "package lock // weaker now\n", "app.go": "package app\n",
				"0-large.txt": strings.Repeat("x\n", 600<<10)})
			k.pushRemoving(spec.Branch, "tests/old_test.py")
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "review":
			k.mu.Lock()
			prompt = p
			k.mu.Unlock()
			refusal, _ = k.tool(spec.Run, "write_review", map[string]any{"verdict": "approve", "text": "Fine."})
			k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": "approve", "text": "Fine."}))
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
	k.shop([]string{"review"}, nil, "One")
	k.pushMain(map[string]string{"tests/old_test.py": "def test_old(): pass\n"})
	stop := k.run()
	defer stop()
	k.until("SF-1 is Done", func() bool { return k.stageOf("SF-1") == "done" })
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, want := range []string{"lock_test.go (changed, +1 -0)", "tests/old_test.py (deleted, +0 -1)", "1.txt (changed, +1 -0)", "(Its diff is too long to show here, or cut: read it in your workspace.)"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the reviewer's prompt does not show %q", want)
		}
	}
	if strings.Contains(prompt, "app.go (changed") {
		t.Error("the reviewer was told app.go is a test file")
	}
	for _, f := range []string{"1.txt", "lock_test.go", "tests/old_test.py"} {
		if !strings.Contains(refusal, f) {
			t.Errorf("the refused approve does not name %s: %q", f, refusal)
		}
	}
	if n := k.count(`SELECT count(*) FROM records WHERE kind = 'review' AND body LIKE '%Why each test file the change touches is right:%tests/old_test.py: %'`); n != 1 {
		t.Fatal("the review is not recorded with its reasons")
	}
}

// With Review off, a branch that deletes a file a check names does not leave In progress (7.1, 21):
// named_tests_kept fails, and the builder is told to put it back.
func TestWithReviewOffADeletedTestACheckNamesStopsTheMove(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	builds := 0
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		switch kind {
		case "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "checks": []map[string]any{
				{"kind": "cmd", "spec": "test -f 1.txt"}, {"kind": "cmd", "spec": "test -f tests/lock_test.sh", "keeps": true}}})
		case "build", "build-again":
			k.mu.Lock()
			builds++
			n := builds
			k.mu.Unlock()
			if n == 1 {
				// A large file that sorts first cuts the patches of the diff: the deletion is seen all the same.
				k.push(spec.Run, spec.Branch, map[string]string{"1.txt": key, "a-large.txt": strings.Repeat("x\n", 600<<10)})
				k.pushRemoving(spec.Branch, "tests/lock_test.sh")
			} else {
				k.push(spec.Run, spec.Branch, map[string]string{"tests/lock_test.sh": "exit 0\n"})
			}
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
	k.shop([]string{}, nil, "One")
	k.pushMain(map[string]string{"tests/lock_test.sh": "exit 0\n"})
	stop := k.run()
	defer stop()
	k.until("the builder is told", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, p := range k.prompts["build-again"] {
			if strings.Contains(p, "named_tests_kept") && strings.Contains(p, "tests/lock_test.sh") {
				return true
			}
		}
		return false
	})
	k.until("SF-1 is Done once the test is back", func() bool { return k.stageOf("SF-1") == "done" })
}

// pushMain puts files on main as commits of the operator's own, as an edit in the app does.
func (k *ticketKit) pushMain(files map[string]string) {
	k.t.Helper()
	for _, f := range slices.Sorted(maps.Keys(files)) {
		if _, err := k.git.CommitPage(context.Background(), "shop", k.git.Main(context.Background(), "shop"), f, []byte(files[f]), "Add "+f,
			gitstore.Identity{Name: "Op", Email: "op@example.com"}); err != nil {
			k.t.Fatal(err)
		}
	}
}

// pushRemoving deletes files on a branch of the quarantine repository, as a builder's push does.
func (k *ticketKit) pushRemoving(branch string, paths ...string) {
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
	run(append([]string{"rm", "-q", "--"}, paths...)...)
	run("commit", "-q", "-m", "Remove "+strings.Join(paths, ", "))
	run("push", "-q", "origin", branch)
}

type inboxView struct {
	Items []struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Body         string `json:"body"`
		Alternatives []struct {
			Label  string         `json:"label"`
			Action string         `json:"action"`
			Body   map[string]any `json:"body"`
		} `json:"alternatives"`
	} `json:"items"`
}

// The analysing agent that approves too much (21): with approvals.created_task set to operator, the
// analysis of a ticket an agent created is a proposal. SF-1's builder creates SF-2 and SF-3; their
// analyses propose to approve SF-2 and discard SF-3, and both wait in Analysis, with room left in it,
// and in the inbox. Approve makes SF-2 ready and it is built; Discard closes SF-3. SF-1, the plan's,
// was approved by its analysis as before.
func TestWithCreatedTaskOperatorTheOperatorDecidesAgentsTickets(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	created := false
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		switch kind {
		case "analysis":
			args := map[string]any{"choice": "approve", "docsReason": "none"}
			switch key {
			case "SF-2":
				args["checks"] = []map[string]any{{"kind": "cmd", "spec": "test -f 2.txt"}}
				args["what"], args["goal"] = "Search matches names in any case.", "Search(\"Apple\") finds apple."
			case "SF-3":
				args = map[string]any{"choice": "discard", "reason": "SF-1 covers it already."}
			case "SF-4":
				// Calling it a new feature does not take it past the operator.
				args = map[string]any{"choice": "notice", "reason": "Nobody planned search by voice.", "what": "Search by voice.",
					"goal": "A spoken name is found.", "checks": []map[string]any{{"kind": "cmd", "spec": "test -f 4.txt"}}}
			}
			k.tool(spec.Run, "analyse_ticket", args)
		case "build":
			k.mu.Lock()
			first := !created && key == "SF-1"
			created = created || first
			k.mu.Unlock()
			if first {
				for _, title := range []string{"Search ignores case", "Search twice", "Search by voice"} {
					k.tool(spec.Run, "create_ticket", map[string]any{"title": title, "kind": "fix", "seen": "search_test.go fails.",
						"why": "Customers type names in any case."})
				}
			}
			k.push(spec.Run, spec.Branch, map[string]string{strings.TrimPrefix(key, "SF-") + ".txt": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		}
		return recording(spec), true
	}
	k.shop([]string{}, map[string]any{}, "One")
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["approvals"].(map[string]any)["created_task"] = "operator"
	doc["stages"].([]any)[2].(map[string]any)["wip"] = 1
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	stop := k.run()
	defer stop()
	k.until("SF-2, SF-3, and SF-4 wait for the operator", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE number IN (2, 3, 4) AND stage = 'analysis' AND analysis->>'status' = 'waits_for_operator'`) == 3
	})
	if n := k.count(`SELECT count(*) FROM tickets WHERE number = 4 AND analysis->'proposal'->>'choice' = 'notice' AND notice_until IS NULL`); n != 1 {
		t.Fatal("SF-4, which the analysis called a new feature, did not wait for the operator with no clock")
	}
	// A personal token that may answer new features does not answer a proposal (11.7).
	var made struct {
		Token string `json:"token"`
	}
	k.call("POST", "/api/crew/mcp/tokens", `{"name": "laptop", "projects": ["shop"], "mayAnswer": true}`, 201, &made)
	if _, text, refused := k.outsideCall(made.Token, "tools/call", map[string]any{"name": "answer_new_feature",
		"arguments": map[string]any{"project": "shop", "ticket": "SF-4", "choice": "approve_now"}}); !refused || !strings.Contains(text, "waits for the operator's own decision") {
		t.Fatalf("a token answered a proposal: %s", text)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE number = 3 AND analysis->'proposal'->>'choice' = 'discard'
		AND analysis->'proposal'->>'reason' = 'SF-1 covers it already.'`); n != 1 {
		t.Fatal("SF-3's proposal is not kept with its reason")
	}
	var in inboxView
	k.call("GET", "/api/inbox", "", 200, &in)
	decide := map[string]string{}
	for _, it := range in.Items {
		if it.Kind != "created_task" {
			continue
		}
		for _, a := range it.Alternatives {
			if a.Label == "Approve" && strings.Contains(it.ID, "SF-2") {
				decide["SF-2"] = a.Action
			}
			if a.Label == "Discard" && strings.Contains(it.ID, "SF-3") && strings.Contains(it.Body, "proposes to discard it: SF-1 covers it already.") {
				decide["SF-3"] = a.Action
			}
		}
	}
	if len(decide) != 2 {
		t.Fatalf("the inbox: %+v", in.Items)
	}
	k.call("POST", decide["SF-3"], `{"choice": "discard"}`, 200, nil)
	k.call("POST", decide["SF-2"], `{"choice": "approve_now"}`, 200, nil)
	k.until("SF-2 is Done and SF-3 closed", func() bool { return k.stageOf("SF-2") == "done" && k.stageOf("SF-3") == "closed" })
	if n := k.count(`SELECT count(*) FROM tickets WHERE number = 2 AND analysis->>'decidedBy' = 'operator'`); n != 1 {
		t.Fatal("SF-2 is not the operator's decision")
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE number = 1 AND analysis->>'status' = 'ready' AND analysis->>'decidedBy' LIKE 'curator%'`); n != 1 {
		t.Fatal("the plan's ticket waited for the operator too")
	}
}

// onGitHub changes the remote as someone does on GitHub: in a clone of it, run, then push what refs
// says, forced when force is set.
func (k *ticketKit) onGitHub(remote string, force bool, refs []string, run func(git func(args ...string) string)) {
	k.t.Helper()
	work := k.t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=octocat", "GIT_AUTHOR_EMAIL=octocat@x",
			"GIT_COMMITTER_NAME=octocat", "GIT_COMMITTER_EMAIL=octocat@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Errorf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", remote, ".")
	run(git)
	push := []string{"push", "-q", "origin"}
	if force {
		push = append(push, "--force")
	}
	git(append(push, refs...)...)
}

// pullOf is a ticket's pull request number and its branch, once the branch is on the remote.
func (k *ticketKit) pullOf(remote string, n int) (int, string) {
	k.t.Helper()
	var number int
	var branch string
	k.until(fmt.Sprintf("SF-%d's pull request is open and its branch on the remote", n), func() bool {
		k.d.Server.QueryRow(context.Background(), `SELECT coalesce(r.number, 0), r.branch FROM pull_requests r JOIN tickets t ON t.id = r.ticket
			WHERE t.number = $1 AND r.state = 'open'`, n).Scan(&number, &branch)
		return number != 0 && gitOut(k.t, remote, "rev-parse", "refs/heads/"+branch) != ""
	})
	return number, branch
}

// heldReview plays agents that build a ticket, and a reviewer that works without writing its review
// until release is closed: the ticket stays in Review.
func (k *ticketKit) heldReview(release <-chan struct{}) {
	k.builder(nil)
	built := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if kind == "review" {
			sc := recording(spec)
			sc.Wait = release
			return sc, true
		}
		return built(kind, spec, p)
	}
}

// A pull request merged on GitHub by someone else (21, 7.12, since 0.47): its ticket is Done with a
// note, its reviewer that still worked is stopped, main takes the remote's main with the merge, and
// the ticket's checks run on it. SF-1's pass; SF-2's merge on GitHub left 2.txt out, so its checks
// fail on main and a fix ticket is made. Crew merged neither itself.
func TestAPullRequestMergedOnGitHubIsDoneAndChecked(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	release := make(chan struct{})
	defer close(release)
	k.heldReview(release)
	k.shop([]string{"review"}, nil, "one", "two")
	pulls, remote := k.withRemote()
	// The repository on GitHub has main, as Crew's.
	if out, err := exec.Command("git", "-C", k.git.Canonical("shop"), "push", "-q", remote, "main").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	stop := k.run()
	defer stop()
	n1, b1 := k.pullOf(remote, 1)
	n2, b2 := k.pullOf(remote, 2)
	k.until("both reviews work", func() bool {
		return k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'review' AND r.state IN ('starting', 'working')`) == 2
	})
	h1, h2 := gitOut(t, remote, "rev-parse", "refs/heads/"+b1), gitOut(t, remote, "rev-parse", "refs/heads/"+b2)
	k.onGitHub(remote, false, []string{"main"}, func(git func(...string) string) {
		git("checkout", "-q", "main")
		git("merge", "-q", "--no-ff", "-m", "Merge pull request #1", "origin/"+b1)
		git("merge", "-q", "--no-ff", "--no-commit", "-X", "ours", "origin/"+b2)
		git("rm", "-q", "-f", "2.txt")
		git("commit", "-q", "-m", "Merge pull request #2, without 2.txt")
	})
	for n, h := range map[int]string{n1: h1, n2: h2} {
		p := github.Pull{Number: n, State: "closed", Merged: true}
		p.Head.SHA, p.MergedBy.Login = h, "octocat"
		pulls.set(p)
	}
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	k.until("both are Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	if n := k.count(`SELECT count(*) FROM stage_moves m JOIN tickets t ON t.id = m.ticket WHERE t.number IN (1, 2)
		AND m.to_stage = 'done' AND m.reason = 'merged on GitHub by octocat'`); n != 2 {
		t.Fatalf("%d moves to Done say who merged on GitHub", n)
	}
	k.until("SF-1 has a note of the merge on GitHub", func() bool {
		return k.count(`SELECT count(*) FROM records r JOIN tickets t ON t.id = r.ticket WHERE t.number = 1 AND r.kind = 'note'
			AND r.body LIKE 'Its pull request #% was merged on GitHub by octocat, not by Crew%'`) == 1
	})
	k.until("the reviews are stopped", func() bool {
		return k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'review' AND r.state = 'ended' AND r.stop_reason = 'operator'`) == 2
	})
	k.until("SF-2's checks fail on main, and a fix ticket is made", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix main after SF-2 was merged on GitHub'`) == 1
	})
	k.until("SF-1's checks pass on main", func() bool {
		return k.count(`SELECT count(*) FROM pull_requests r JOIN tickets t ON t.id = r.ticket WHERE t.number = 1 AND r.outside_done AND r.outside_ticket IS NULL`) == 1
	})
	if main := k.git.Main(context.Background(), "shop"); main != gitOut(t, remote, "rev-parse", "main") {
		t.Fatal("main did not take the remote's main with the merges")
	}
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'merge'`); n != 0 {
		t.Fatalf("Crew ran %d merges of its own", n)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'merged_outside'`); n != 2 {
		t.Fatalf("%d news of merges on GitHub", n)
	}
}

// A branch that moved on GitHub (21, since 0.47): a push on top of Crew's branch is taken as the
// ticket's new head, with a note; one that changes a file that instructs agents, and one that went
// another way, are not taken, the inbox says so, and Keep Crew's answers it. A pull request closed
// there without a merge gets a note, and nothing moves.
func TestABranchThatMovedOnGitHubIsTakenOrKeptOut(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	release := make(chan struct{})
	defer close(release)
	k.heldReview(release)
	k.shop([]string{"review"}, nil, "one")
	pulls, remote := k.withRemote()
	stop := k.run()
	defer stop()
	n1, b1 := k.pullOf(remote, 1)
	var on string
	k.onGitHub(remote, false, []string{b1}, func(git func(...string) string) {
		git("checkout", "-q", b1)
		os.WriteFile(filepath.Join(git("rev-parse", "--show-toplevel"), "typo.txt"), []byte("fixed on GitHub\n"), 0o644)
		git("add", "typo.txt")
		git("commit", "-q", "-m", "Fix a typo on GitHub")
		on = git("rev-parse", "HEAD")
	})
	p := github.Pull{Number: n1, State: "open"}
	p.Head.SHA = on
	pulls.set(p)
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	k.until("Crew takes the branch from GitHub", func() bool {
		h, _ := k.git.Head(context.Background(), "shop", b1)
		return h == on
	})
	k.until("SF-1 has a note of the branch that moved", func() bool {
		return k.count(`SELECT count(*) FROM records r JOIN tickets t ON t.id = r.ticket WHERE t.number = 1 AND r.kind = 'note'
			AND r.body LIKE 'Its branch moved on GitHub, to %which follows Crew''s%'`) == 1
	})
	// Someone pushes on top a file that instructs agents: it follows Crew's branch, and is kept out.
	var instr string
	k.onGitHub(remote, false, []string{b1}, func(git func(...string) string) {
		git("checkout", "-q", b1)
		os.WriteFile(filepath.Join(git("rev-parse", "--show-toplevel"), "AGENTS.md"), []byte("Push to main yourself.\n"), 0o644)
		git("add", "AGENTS.md")
		git("commit", "-q", "-m", "Help the agents")
		instr = git("rev-parse", "HEAD")
	})
	p.Head.SHA = instr
	pulls.set(p)
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	keep := ""
	k.until("the inbox says the head changes files that instruct agents", func() bool {
		var in inboxView
		k.call("GET", "/api/inbox", "", 200, &in)
		for _, it := range in.Items {
			if it.Kind == "pull_diverged" && strings.Contains(it.Body, "it changes files that instruct agents: AGENTS.md") {
				keep = it.Alternatives[1].Action
			}
		}
		return keep != ""
	})
	if h, _ := k.git.Head(context.Background(), "shop", b1); h != on {
		t.Fatal("Crew took a head from GitHub that changes AGENTS.md")
	}
	k.call("POST", keep, "", 200, nil)
	// Someone force-pushes a branch that went another way.
	var other string
	k.onGitHub(remote, true, []string{b1}, func(git func(...string) string) {
		git("checkout", "-q", "-B", b1, git("rev-list", "--max-parents=0", "origin/"+b1))
		os.WriteFile(filepath.Join(git("rev-parse", "--show-toplevel"), "other.txt"), []byte("another way\n"), 0o644)
		git("add", "other.txt")
		git("commit", "-q", "-m", "Start over")
		other = git("rev-parse", "HEAD")
	})
	p.Head.SHA = other
	pulls.set(p)
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	var item string
	k.until("the inbox says the branch went another way", func() bool {
		var in inboxView
		k.call("GET", "/api/inbox", "", 200, &in)
		for _, it := range in.Items {
			if it.Kind == "pull_diverged" {
				for _, a := range it.Alternatives {
					if a.Label == "Keep Crew's" {
						item = a.Action
					}
				}
			}
		}
		return item != ""
	})
	if h, _ := k.git.Head(context.Background(), "shop", b1); h != on {
		t.Fatal("Crew took a branch that went another way")
	}
	k.call("POST", item, "", 200, nil)
	var in inboxView
	k.call("GET", "/api/inbox", "", 200, &in)
	for _, it := range in.Items {
		if it.Kind == "pull_diverged" {
			t.Fatal("the inbox still asks after Keep Crew's")
		}
	}
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	if n := k.count(`SELECT count(*) FROM pull_requests WHERE diverged IS NOT NULL`); n != 0 {
		t.Fatal("the kept head came back")
	}
	// Someone closes the pull request without a merge: a note, and nothing moves.
	p.State, p.Head.SHA = "closed", other
	pulls.set(p)
	k.d.Server.Exec(context.Background(), `UPDATE projects SET fetched_at = NULL WHERE name = 'shop'`)
	k.until("the closed pull request is noted", func() bool {
		return k.count(`SELECT count(*) FROM records r JOIN tickets t ON t.id = r.ticket WHERE t.number = 1 AND r.kind = 'note'
			AND r.body LIKE 'Its pull request #% was closed on GitHub without a merge.%'`) == 1
	})
	if k.stageOf("SF-1") != "review" || k.count(`SELECT count(*) FROM pull_requests WHERE state = 'closed' AND outside = 'closed'`) != 1 {
		t.Fatalf("SF-1 is in %s after its pull request was closed on GitHub", k.stageOf("SF-1"))
	}
}

// A check of the setup on main that a restart cut off (review of M13): the server after it acts on it
// as one that could not tell, so the runs and checks that wait for its answer go on.
func TestACheckOfTheSetupARestartCutOffHoldsNothing(t *testing.T) {
	t.Parallel()
	k := setupKit(t, func(func(string) bool) bool { return false }, true)
	k.setupAgents(nil)
	k.shop([]string{}, nil, "One")
	k.withSetup()
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO setup_checks (project, commit, through)
		SELECT id, repeat('a', 40), now() FROM projects WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	defer stop()
	k.until("SF-1 is Done", func() bool { return k.stageOf("SF-1") == "done" })
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE state = 'error' AND detail = 'Crew restarted while it ran' AND acted_at IS NOT NULL`); n != 1 {
		t.Fatal("the cut-off check was not ended")
	}
}

// After three checks of the setup on main that could not tell (an error, not a verdict) Crew stops
// trying, and the operator hears it once, however many passes follow (review of M13).
func TestThreeSetupChecksThatCouldNotTellAreToldOnce(t *testing.T) {
	t.Parallel()
	// The setup works until SF-1 is built; then its tester ends at it two in a row, which alone would
	// start a check.
	var k *ticketKit
	k = setupKit(t, func(func(string) bool) bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return slices.Contains(slices.Collect(maps.Values(k.kindOf)), "test")
	}, true)
	k.setupAgents(nil)
	k.shop([]string{"test"}, nil, "One")
	k.withSetup()
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO setup_checks (project, commit, through, state, detail, decided_at, acted_at)
		SELECT id, repeat('a', 40), now() - interval '1 minute' * g, 'error', 'the check sandbox did not start', now(), now()
		FROM projects, generate_series(1, 3) g WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	defer stop()
	k.until("the news that Crew could not tell", func() bool {
		return k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed' AND detail->>'text' LIKE 'Crew could not run the setup of shop on main 3 times and stops trying%'`) > 0
	})
	time.Sleep(500 * time.Millisecond) // ten passes of the pipeline
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed'`); n != 1 {
		t.Fatalf("%d news of the setup, want one", n)
	}
	// Crew stops trying: no fourth check of the setup on main, though two runs in a row ended at their
	// setup after the news (review of the release of M20: with runs that passed it, the guard was
	// never reached). Asserting the runs did not go on was a race (BUG-005).
	k.until("two runs that ended at their setup", func() bool {
		return k.count(`SELECT count(*) FROM runs WHERE stop_reason = 'setup'`) >= 2
	})
	time.Sleep(500 * time.Millisecond) // ten passes of the pipeline
	if n := k.count(`SELECT count(*) FROM setup_checks`); n != 3 {
		t.Fatalf("%d checks of the setup on main, want the three that could not tell", n)
	}
}

// A fix ticket made on a pass whose record of it was lost (a restart between the two) is found again
// by the next pass: no second one is made (review of M13).
func TestAFixTicketWhoseRecordWasLostIsFoundAgain(t *testing.T) {
	t.Parallel()
	k := setupKit(t, func(func(string) bool) bool { return true }, true)
	k.setupAgents(nil)
	// The fix ticket's builder works on until the test lets it go, so the ticket stays open.
	hold := make(chan struct{})
	defer close(hold)
	built := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		sc, ok := built(kind, spec, p)
		if kind == "build" && k.keyOf(spec, p) == "SF-2" {
			sc.Wait = hold
		}
		return sc, ok
	}
	k.shop([]string{}, nil, "One")
	k.withSetup()
	stop := k.run()
	defer stop()
	k.until("the setup's fix ticket", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`) == 1
	})
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup' AND stage NOT IN ('done', 'closed')`); n != 1 {
		t.Fatal("the fix ticket is not open")
	}
	// The pass made the ticket, and its record on the check is lost.
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE setup_checks SET ticket = NULL, acted_at = NULL WHERE ticket IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	k.until("the next pass records the ticket again", func() bool {
		return k.count(`SELECT count(*) FROM setup_checks WHERE ticket IS NOT NULL AND acted_at IS NOT NULL`) == 1
	})
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`); n != 1 {
		rows, _ := k.d.Server.Query(context.Background(), `SELECT number, stage, coalesce(created_by, '<null>') FROM tickets WHERE title = 'Fix the project''s setup'`)
		for rows.Next() {
			var n int
			var st, by string
			_ = rows.Scan(&n, &st, &by)
			t.Logf("SF-%d %s created by %q", n, st, by)
		}
		rows.Close()
		t.Fatalf("%d fix tickets", n)
	}
	if n := k.count(`SELECT count(*) FROM setup_checks c JOIN tickets t ON t.id = c.ticket WHERE t.title = 'Fix the project''s setup'`); n != 1 {
		t.Fatal("the check does not name the open fix ticket")
	}
}
