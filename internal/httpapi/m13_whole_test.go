package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/settings"
)

// The whole project of M13's done-when: a fixture project runs from its OK to Done with nobody acting,
// as a test on the fake harness, with every role played. The agents here do what their stage prompt
// asks, through the Crew MCP, and push to the quarantine repository as their runs would.

// anyTicket is a ticket's key in any project's prompt.
var anyTicket = regexp.MustCompile(`this run works on its ticket ([A-Z][A-Z0-9]*-\d+)`)

// wholeAgents plays every role for the shop project:
//   - the lead approves each ticket; SF-2 depends on SF-1; SF-3 is split into two parts that run in
//     parallel; a ticket an agent created gets its what, goal, and check;
//   - SF-1's builder creates a ticket about a defect it saw; every builder pushes its ticket's file;
//   - the reviewer asks SF-4 for changes once, and approves everything else;
//   - the tester, the E2E tester, the verifier, the summary writer, and the final report do their part.
func (k *ticketKit) wholeAgents(t *testing.T) {
	var mu sync.Mutex
	created, reviewed4 := false, 0
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		// A part of a ticket that the lead split (7.11).
		if m := partKey.FindStringSubmatch(p); m != nil && strings.Contains(p, "# A subtask: one part of a ticket") {
			file := map[string]string{"SF-3.2": "cart/api.txt", "SF-3.3": "cart/page.txt"}[m[1]]
			k.push(spec.Run, spec.Branch, map[string]string{file: m[1]})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Did " + m[1]})
			k.tool(spec.Run, "subtask_done", map[string]any{})
			return recording(spec), true
		}
		key := k.keyOf(spec, p)
		n := strings.TrimPrefix(key, "SF-")
		switch kind {
		case "analysis":
			args := map[string]any{"choice": "approve", "docsReason": "none"}
			switch {
			case key == "SF-2":
				args["dependsOn"] = []string{"SF-1"}
			case key == "SF-3":
				for _, part := range []map[string]any{
					{"title": "The cart's API", "what": "Add cart/api.txt.", "doneWhen": "cart/api.txt exists.", "role": "builder", "touches": []string{"cart/api.txt"}, "check": "test -f cart/api.txt"},
					{"title": "The cart's page", "what": "Add cart/page.txt.", "doneWhen": "cart/page.txt exists.", "role": "builder", "touches": []string{"cart/page.txt"}, "check": "test -f cart/page.txt"},
				} {
					if text, ok := k.tool(spec.Run, "create_subtask", part); !ok {
						t.Errorf("create_subtask: %s", text)
					}
				}
				args["checks"] = []map[string]any{{"kind": "cmd", "spec": "test -f cart/api.txt"}, {"kind": "cmd", "spec": "test -f cart/page.txt"}}
			case strings.Contains(p, "Search ignores case"):
				args["what"], args["goal"] = "Search matches names in any case.", "Search(\"Apple\") finds apple."
				args["checks"] = []map[string]any{{"kind": "cmd", "spec": "test -f " + n + ".txt"}}
			}
			k.tool(spec.Run, "analyse_ticket", args)
		case "build":
			mu.Lock()
			first := !created && key == "SF-1"
			created = created || first
			mu.Unlock()
			if first {
				if text, ok := k.tool(spec.Run, "create_ticket", map[string]any{"title": "Search ignores case", "kind": "fix",
					"seen": "search_test.go fails: Search(\"Apple\") finds nothing.", "why": "Customers type names in any case."}); !ok {
					t.Errorf("create_ticket: %s", text)
				}
			}
			k.push(spec.Run, spec.Branch, map[string]string{n + ".txt": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "fix":
			k.push(spec.Run, spec.Branch, map[string]string{n + ".fixed": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Fixed " + key})
			k.tool(spec.Run, "fix_done", map[string]any{})
		case "review":
			verdict, text := "approve", "It does what "+key+" asks."
			if key == "SF-4" {
				mu.Lock()
				reviewed4++
				if reviewed4 == 1 {
					verdict, text = "changes_requested", "receipt.go: the total leaves out the tax. Add it."
				}
				mu.Unlock()
			}
			k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": verdict, "text": text}))
		case "test":
			k.tool(spec.Run, "add_test_result", map[string]any{"result": "pass", "text": "Ran the checks of " + key})
		case "e2e":
			k.tool(spec.Run, "add_e2e_result", map[string]any{"result": "pass", "text": "Drove the flows of " + key})
		case "verify":
			k.tool(spec.Run, "add_verification", map[string]any{"result": "pass", "text": key + " does what its goal and the plan ask."})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		case "report":
			k.tool(spec.Run, "write_report", map[string]any{"built": "The shop.", "skipped": "Nothing.", "cancelled": "Nothing.",
				"missing": "Nothing.", "learned": "Small tickets go fast."})
		default:
			return harness.Script{}, false
		}
		return recording(spec), true
	}
}

// A project runs from its OK to Done with nobody acting (M13's done-when), on the fixture project with
// every optional stage on: four tickets of the plan, one that waits for another, one split into two
// parts that run at the same time, one sent back by its review for a fix round, and one an agent
// created while it built. Nothing of the operator's is written after the OK until the project is Done
// and its final report written. Then a ticket the operator adds opens the project again, with no new
// OK, and is Done too (7.13).
func TestAProjectRunsFromItsOKToDoneWithNobodyActing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.wholeAgents(t)
	k.shop([]string{"review", "test", "e2e", "verification"}, nil, "Accounts", "Sign-in", "Cart", "Receipts")
	var okAt time.Time
	k.d.Server.QueryRow(context.Background(), `SELECT ok_at FROM projects WHERE name = 'shop'`).Scan(&okAt)
	stop := k.run()
	defer stop()
	k.until("the project is Done with its final report", func() bool {
		return k.count(`SELECT count(*) FROM projects WHERE name = 'shop' AND state = 'done'`) == 1 &&
			k.count(`SELECT count(*) FROM reports`) == 1
	})
	var doneAt time.Time
	k.d.Server.QueryRow(context.Background(), `SELECT max(created_at) FROM reports`).Scan(&doneAt)
	if n := k.count(`SELECT count(*) FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'shop' AND t.stage = 'done'`); n != 5 {
		rows, _ := k.d.Server.Query(context.Background(), `SELECT number, title, stage, coalesce(closed_reason, ''), analysis::text FROM tickets ORDER BY number`)
		for rows.Next() {
			var num int
			var title, stage, closed, analysis string
			rows.Scan(&num, &title, &stage, &closed, &analysis)
			t.Logf("SF-%d %s: %s %s %s", num, title, stage, closed, analysis)
		}
		rows.Close()
		t.Fatalf("%d tickets are Done, want the plan's four and the one an agent created", n)
	}
	// Nobody acted: the operator wrote nothing between the OK and the final report.
	if n := k.count(`SELECT count(*) FROM changes WHERE who = 'operator' AND at > $1 AND at < $2`, okAt, doneAt); n != 0 {
		t.Fatalf("%d changes of the operator's while the project ran", n)
	}
	// Every optional stage was passed through.
	for _, stage := range []string{"review", "test", "e2e", "verification"} {
		if n := k.count(`SELECT count(*) FROM stage_moves m JOIN tickets t ON t.id = m.ticket WHERE t.number = 1 AND m.to_stage = $1`, stage); n != 1 {
			t.Errorf("SF-1 passed %s %d times", stage, n)
		}
	}
	// SF-2 waited for SF-1: its build began after SF-1 was Done.
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN tickets t ON t.id = s.ticket
		WHERE t.number = 2 AND s.kind = 'build' AND r.started_at < (SELECT stage_at FROM tickets WHERE number = 1)`); n != 0 {
		t.Fatal("SF-2 was built before SF-1, which it depends on, was Done")
	}
	// SF-3's two parts ran at the same time, and were joined.
	if n := k.count(`SELECT count(*) FROM subtasks a JOIN subtasks b ON a.ticket = b.ticket AND a.id < b.id
		JOIN tickets t ON t.id = a.ticket WHERE t.number = 3 AND a.kind = 'part' AND b.kind = 'part'
		AND (SELECT min(started_at) FROM runs WHERE subtask = b.id) < (SELECT max(ended_at) FROM runs WHERE subtask = a.id)
		AND a.joined_at IS NOT NULL AND b.joined_at IS NOT NULL`); n != 1 {
		t.Fatal("SF-3's parts did not run at the same time, or were not joined")
	}
	// SF-4 had one fix round, for its review.
	if n := k.count(`SELECT fix_rounds FROM tickets WHERE number = 4`); n != 1 {
		t.Fatalf("SF-4 had %d fix rounds", n)
	}
	// The ticket an agent created was analysed by another agent, and is Done.
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Search ignores case' AND origin = 'agent' AND stage = 'done'
		AND analysis->>'decidedBy' LIKE 'curator%'`); n != 1 {
		t.Fatal("the ticket SF-1's builder created is not Done through another agent's analysis")
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again'`); n != 0 {
		t.Fatal("the lead had to look again at a ticket")
	}
	// More work after Done: a ticket the operator adds runs with no new OK.
	oks := k.count(`SELECT count(*) FROM plans WHERE kind IN ('ok', 'amendment_ok')`)
	k.call("POST", "/api/projects/shop/tasks", `{"title": "Receipts by mail", "why": "Customers ask.", "what": "Mail the receipt.",
		"goal": "A receipt arrives.", "checks": [{"kind": "cmd", "spec": "test -f 6.txt"}], "docsReason": "none"}`, 201, nil)
	k.until("SF-6 is Done", func() bool { return k.stageOf("SF-6") == "done" })
	if n := k.count(`SELECT count(*) FROM plans WHERE kind IN ('ok', 'amendment_ok')`); n != oks {
		t.Fatal("the ticket added after Done needed a new OK")
	}
}

// pushIn commits files on a branch of a project's quarantine repository, as a run's push does.
func (k *ticketKit) pushIn(project, runID, branch string, files map[string]string) {
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
	run("clone", "-q", k.git.Quarantine.Path(project), ".")
	old := run("rev-parse", "HEAD")
	if run("ls-remote", "origin", "refs/heads/"+branch) != "" {
		run("fetch", "-q", "origin", branch)
		run("checkout", "-q", "-B", branch, "FETCH_HEAD")
		old = run("rev-parse", "HEAD")
	} else {
		run("checkout", "-q", "-b", branch)
	}
	for f, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, f)), 0o755)
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "Work")
	run("push", "-q", "origin", branch)
	k.driver.RecordPush(gitendpoint.Access{Repo: project, Run: runID, Who: "run " + runID}, "refs/heads/"+branch, old, run("rev-parse", "HEAD"))
}

// project makes a running project with tickets that check for a file of their own, as shop does.
func (k *ticketKit) project(name, key string, stages []string, titles ...string) {
	k.t.Helper()
	st, _ := json.Marshal(stages)
	k.call("POST", "/api/projects", `{"name": "`+name+`", "key": "`+key+`", "plan": "## Goal\n\nWork.\n", "stages": `+string(st)+`,
		"repository": {"kind": "local"}}`, 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/"+name+"/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = ""
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/"+name+"/settings", string(b), 200, nil)
	for i, title := range titles {
		k.call("POST", "/api/projects/"+name+"/tasks", fmt.Sprintf(`{"title": %q, "why": "w", "what": "w", "goal": "g",
			"checks": [{"kind": "cmd", "spec": "test -f %d.txt"}], "docsReason": "none"}`, title, i+1), 201, nil)
	}
	k.call("POST", "/api/projects/"+name+"/ok", "", 200, nil)
}

// manyAgents play every role for tickets of any project: approve, build the ticket's file, review,
// test, summarise, and write the final report.
func (k *ticketKit) manyAgents(repos map[string]string) {
	keys := sync.Map{}
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if m := anyTicket.FindStringSubmatch(p); m != nil {
			keys.Store(spec.Subtask, m[1])
		}
		v, _ := keys.Load(spec.Subtask)
		key, _ := v.(string)
		prefix, n, _ := strings.Cut(key, "-")
		switch kind {
		case "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none"})
		case "build":
			k.pushIn(repos[prefix], spec.Run, spec.Branch, map[string]string{n + ".txt": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "review":
			k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": "approve", "text": "It does what " + key + " asks."}))
		case "test":
			k.tool(spec.Run, "add_test_result", map[string]any{"result": "pass", "text": "Ran the checks of " + key})
		case "summary":
			return harness.Script{Lines: summaryLines(spec)}, true
		case "report":
			k.tool(spec.Run, "write_report", map[string]any{"built": "It.", "skipped": "Nothing.", "cancelled": "Nothing.",
				"missing": "Nothing.", "learned": "Nothing new."})
		default:
			return harness.Script{}, false
		}
		// A turn takes a while, as a real one does, so runs of the projects overlap.
		sc := recording(spec)
		sc.Delay = 15 * time.Millisecond
		return sc, true
	}
}

// Many projects at once (M13's done-when): the lead slots, the capacity check, and the quota guard
// hold across three projects. agy has one lead slot; the machine has room for one large sandbox or two
// small ones; the Gemini group is close to its limit. Sampled all the while: never more lead runs
// than slots, and never more given out than the room. Runs waited for room, and every project is
// Done with its final report.
func TestManyProjectsShareTheLeadSlotsTheMachineAndTheQuota(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.room(4, 12) // 4 virtual CPUs and 8 GB: one large sandbox, or two small
	repos := map[string]string{"SF": "shop", "BL": "blog", "WK": "wiki"}
	k.manyAgents(repos)
	for key, name := range repos {
		k.project(name, key, []string{"review", "test"}, "One", "Two")
	}
	if err := models.RecordLimits(ctx, k.d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.9}}, "login check"); err != nil {
		t.Fatal(err)
	}
	machine, err := settings.Effective(ctx, k.d.Server, settings.Machine{Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu                   sync.Mutex
		problems             []string
		maxLeads, waitedRoom int
		maxCPU               float64
		samples              int
	)
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			slots, err0 := planning.Slots(ctx, k.d.Server, machine)
			free, err := planning.FreeSlots(ctx, k.d.Server, machine)
			u, err2 := capacity.Used(ctx, k.d.Server, capacity.Sizes(machine))
			if err0 != nil || err != nil || err2 != nil {
				continue
			}
			var waits int
			k.d.Server.QueryRow(ctx, `SELECT count(*) FROM room_waits`).Scan(&waits)
			mu.Lock()
			samples++
			if leads := slots["agy"] - free["agy"]; leads > maxLeads {
				maxLeads = leads
			}
			if free["agy"] < 0 {
				problems = append(problems, fmt.Sprintf("%d runs on %d slots", slots["agy"]-free["agy"], slots["agy"]))
			}
			if u.CPUs > 4 || u.MemMB > 8*1024 {
				problems = append(problems, fmt.Sprintf("%.0f virtual CPUs and %d MB given out of 4 and 8192", u.CPUs, u.MemMB))
			}
			maxCPU = max(maxCPU, u.CPUs)
			if waits > 0 {
				waitedRoom++
			}
			mu.Unlock()
		}
	}()
	stop := k.run()
	k.until("every project is Done with its final report", func() bool {
		return k.count(`SELECT count(*) FROM projects WHERE name IN ('shop', 'blog', 'wiki') AND state = 'done'`) == 3 &&
			k.count(`SELECT count(*) FROM reports`) == 3
	})
	stop()
	close(done)
	<-sampled
	mu.Lock()
	defer mu.Unlock()
	if len(problems) > 0 {
		t.Fatalf("%d of %d samples broke a limit; the first: %s", len(problems), samples, problems[0])
	}
	if waitedRoom == 0 || maxCPU == 0 {
		t.Fatalf("no run waited for room in %d samples (%.0f virtual CPUs at most): the machine was never full", samples, maxCPU)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE stage = 'done'`); n != 6 {
		t.Fatalf("%d tickets are Done, not 6", n)
	}
	if n := k.count(`SELECT count(DISTINCT s.project) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.model LIKE 'gemini%'`); n != 3 {
		t.Fatalf("%d projects ran on the Gemini group close to its limit", n)
	}
	t.Logf("%d samples: at most %d lead run, %.0f virtual CPUs given out; runs waited for room in %d", samples, maxLeads, maxCPU, waitedRoom)
}
