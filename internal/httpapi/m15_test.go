package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/versions"
)

// The Analytics screen and the Crew level read their numbers from the server (9.7, 10.7), and the
// signals are listed by project and kind.
func TestTheAnalyticsAndTheCrewLevelAreRead(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	stop := k.run()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	stop()
	var p struct {
		Done, Tickets int
		TokensByRole  []struct{ Label string }
		DonePerDay    []struct{ Value float64 }
	}
	k.call("GET", "/api/projects/shop/analytics", "", 200, &p)
	if p.Done != 1 || p.Tickets != 1 || len(p.DonePerDay) != 14 || p.DonePerDay[13].Value != 1 || p.TokensByRole == nil {
		t.Fatalf("analytics %+v", p)
	}
	// Use in tokens and time, and no cost (since 0.53).
	var raw map[string]any
	k.call("GET", "/api/projects/shop/analytics", "", 200, &raw)
	for _, gone := range []string{"costUsd", "costPerTicket", "costByRole", "costByModel"} {
		if _, ok := raw[gone]; ok {
			t.Errorf("the analytics still have %s", gone)
		}
	}
	k.call("GET", "/api/projects/nothing/analytics", "", 404, nil)
	var c struct {
		Done, Projects int
		ProjectKeys    []struct{ Key string }
		Kinds          []struct{ Kind, Label string }
		Versions       []struct {
			Version string
			Tickets int
			Running bool
		}
	}
	k.call("GET", "/api/crew/analytics", "", 200, &c)
	if c.Done != 1 || c.Projects != 1 || len(c.ProjectKeys) != 1 || c.ProjectKeys[0].Key != "SF" || len(c.Kinds) == 0 ||
		len(c.Versions) != 1 || c.Versions[0].Tickets != 1 {
		t.Fatalf("crew %+v", c)
	}
	var s struct {
		Signals []struct{ Kind, Project string }
		Kinds   []struct{ Kind string }
	}
	k.call("GET", "/api/crew/signals?project=SF&kind=sandbox_startup", "", 200, &s)
	if len(s.Signals) == 0 || s.Signals[0].Project != "shop" || len(s.Kinds) == 0 {
		t.Fatalf("signals %+v", s)
	}
	k.call("GET", "/api/crew/signals?since=yesterday", "", 400, nil)
	k.call("GET", "/api/crew/signals?pageSize=0", "", 400, nil)
}

// The operator's own override of the prompt writer's rules applies at once, and the prompt writer
// reads it for that project's texts and not for another's (10.3, 7.2). An override a lead proposed for
// a checking role is in the inbox, and the operator applies or refuses it there.
func TestOverridesThroughTheServer(t *testing.T) {
	t.Parallel()
	k := newPlanKit(t)
	for _, p := range []string{"shop", "other"} {
		k.call("POST", "/api/projects", `{"name": "`+p+`", "key": "`+strings.ToUpper(p[:2])+`", "goal": "g",
			"plan": "## Goal\n\nx\n", "repository": {"kind": "local"}}`, 201, nil)
	}
	k.call("POST", "/api/projects/shop/overrides", `{"path": "tools.yaml", "text": "x"}`, 400, nil)
	var o struct {
		Number  int
		Status  string
		Version int
	}
	k.call("POST", "/api/projects/shop/overrides", `{"path": "agents/prompt-writer.md", "text": "Write like a sailor.", "why": "my taste"}`, 201, &o)
	if o.Status != "applied" || o.Number != 1 {
		t.Fatalf("the operator's override: %+v", o)
	}
	k.rewriter = func(spec harness.RunSpec) harness.Script {
		return harness.Script{Lines: answerLines(spec, goodRewrite)}
	}
	k.rewriteDone(k.rewriteNow("/api/rewrites", `{"kind": "improve", "project": "shop", "text": "lock accounts"}`).ID)
	if p := k.rewrites[len(k.rewrites)-1].Prompt; !strings.Contains(p, "Write like a sailor.") {
		t.Fatalf("the prompt writer did not read the override: %s", p)
	}
	k.rewriteDone(k.rewriteNow("/api/rewrites", `{"kind": "improve", "project": "other", "text": "lock accounts"}`).ID)
	if p := k.rewrites[len(k.rewrites)-1].Prompt; strings.Contains(p, "sailor") {
		t.Fatal("another project's prompt writer read the override")
	}

	// A lead's proposal for the reviewer waits in the inbox.
	var id string
	k.d.Server.QueryRow(context.Background(), `SELECT id::text FROM projects WHERE name = 'shop'`).Scan(&id)
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO overrides (project, number, path, version, text, why, status, proposed_by)
		VALUES ($1, 2, 'agents/reviewer.md', 1, 'Review harder.', 'misses', 'waiting', 'lead · run')`, id); err != nil {
		t.Fatal(err)
	}
	var inbox struct {
		Items []struct {
			ID, Kind, Title string
			Alternatives    []struct {
				Label, Action string
				Body          map[string]any
				Main          bool
			}
		}
	}
	k.call("GET", "/api/inbox", "", 200, &inbox)
	var action string
	for _, it := range inbox.Items {
		if it.Kind == "override" {
			if it.ID != "override/shop/2" || !strings.Contains(it.Title, "agents/reviewer.md") {
				t.Fatalf("the item: %+v", it)
			}
			for _, a := range it.Alternatives {
				if a.Label == "Apply" {
					action = a.Action
				}
				// It is read before it applies: Open is the main way (security review of M15).
				if a.Main != (a.Label == "Open") {
					t.Errorf("the main way is not Open: %+v", it.Alternatives)
				}
			}
		}
	}
	if action != "/api/projects/shop/overrides/2" {
		t.Fatalf("no Apply in the inbox: %+v", inbox.Items)
	}
	k.call("POST", action, `{"choice": "apply"}`, 200, &o)
	if o.Status != "applied" {
		t.Fatalf("applied from the inbox: %+v", o)
	}
	k.call("POST", action, `{"choice": "apply"}`, 400, nil)
	var list struct {
		Overrides []struct{ Number int }
		Files     []struct {
			Path  string
			Waits bool
		}
	}
	k.call("GET", "/api/projects/shop/overrides", "", 200, &list)
	waits := map[string]bool{}
	for _, f := range list.Files {
		waits[f.Path] = f.Waits
	}
	if len(list.Overrides) != 2 || !waits["agents/reviewer.md"] || waits["agents/builder.md"] {
		t.Fatalf("the list: %+v", list)
	}
	k.call("POST", "/api/projects/shop/overrides/9", `{"choice": "apply"}`, 404, nil)
}

// evalScript plays the runs of an eval: the builder pushes its work to the eval's branch and fixes it
// when asked, the reviewer asks for changes once and then approves, and the tester passes. It returns
// the specs of the eval's runs by role.
func (k *ticketKit) evalScript(t *testing.T) map[string][]harness.RunSpec {
	var mu sync.Mutex
	specs := map[string][]harness.RunSpec{}
	reviews := 0
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		k.mu.Lock()
		first := k.firstPrompt(spec)
		k.mu.Unlock()
		if !strings.Contains(first, "this run is an eval of the project") {
			return prev(kind, spec, p)
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(first, "# In progress: build one ticket"):
			specs["builder"] = append(specs["builder"], spec)
			files := map[string]string{"eval-work.txt": "work"}
			switch {
			case strings.Contains(p, "these failed"):
				files = map[string]string{"eval-fixed.txt": "fixed"}
			case strings.Contains(p, "The reviewer asks for changes"):
				files = map[string]string{"eval-named.txt": "named"}
			}
			if spec.Branch == "" {
				t.Errorf("an eval builder with no branch")
			}
			k.push(spec.Run, spec.Branch, files)
			return recording(spec), true
		case strings.Contains(first, "# Review: read one ticket's change"):
			specs["reviewer"] = append(specs["reviewer"], spec)
			reviews++
			if reviews == 1 {
				return harness.Script{Lines: answerLines(spec, "VERDICT: changes\nName the file better.")}, true
			}
			return harness.Script{Lines: answerLines(spec, "VERDICT: approve")}, true
		case strings.Contains(first, "# Test: test one ticket"):
			specs["tester"] = append(specs["tester"], spec)
			return harness.Script{Lines: answerLines(spec, "RESULT: pass")}, true
		}
		t.Errorf("an eval run of no known role: %s", first[:200])
		return recording(spec), true
	}
	return specs
}

// firstPrompt is the first prompt a subtask's runs were given; k.mu is held.
func (k *ticketKit) firstPrompt(spec harness.RunSpec) string {
	if k.firsts == nil {
		k.firsts = map[int64]string{}
	}
	if _, ok := k.firsts[spec.Subtask]; !ok {
		k.firsts[spec.Subtask] = spec.Prompt
	}
	return k.firsts[spec.Subtask]
}

// An eval replays a Done ticket from the code before its merge (9.6): its builder works on eval/<run>
// with no Crew MCP and not main's instructions, the reviewer judges on another family, the tester reads
// the branch, and Crew's checks run with the held-out file copied in. A review that asks for changes is
// a fix round and a failed check a check round; nothing merges, the branch goes, and the result is kept.
func TestAnEvalReplaysADoneTicket(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	stop := k.run()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	stop()
	main := k.git.Main(context.Background(), "shop")

	k.call("POST", "/api/projects/shop/evals/run", "", 400, nil) // no case yet
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["a.txt"], "why": "typical"}`, 400, nil)
	var c struct {
		Number     int
		Ticket     string
		BaseCommit string
		HeldOut    []string
		Checks     []struct{ Spec string }
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "Typical ticket for this project"}`, 201, &c)
	if c.Number != 1 || c.Ticket != "SF-1" || len(c.HeldOut) != 1 || len(c.Checks) != 1 || c.Checks[0].Spec != "test -f 1.txt" || c.BaseCommit == main {
		t.Fatalf("the case: %+v", c)
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"]}`, 400, nil) // a case already
	// The case waits for its fit check, which finds nothing missing (design 0.51).
	k.call("POST", "/api/projects/shop/evals/run", "", 400, nil)
	stop = k.run()
	k.until("the case active", func() bool { return k.cases()[1].Status == "active" })
	stop()
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["knowledge"].(map[string]any)["evals"].(map[string]any)["repeats"] = 1
	// An eval's held-out checks run in the project's check sandboxes' mode (13.6, since 0.53).
	doc["sandbox"].(map[string]any)["check_network"] = "sources"
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)

	specs := k.evalScript(t)
	var modeMu sync.Mutex
	evalModes := map[string]int{}
	// The checks fail once on the builder's first work, and pass on its fix.
	k.f.Check = func(spec harness.CheckSpec, cmd harness.CheckCmd) harness.CheckResult {
		names := tarNames(spec.Tree)
		if slices.Contains(names, "eval-work.txt") {
			modeMu.Lock()
			evalModes[spec.Mode]++
			modeMu.Unlock()
		}
		if slices.Contains(names, "eval-work.txt") && !slices.Contains(names, "eval-fixed.txt") {
			return harness.CheckResult{Code: 1, Output: "the held-out test failed"}
		}
		return checkSandbox(spec, cmd)
	}
	k.call("POST", "/api/projects/shop/evals/run", "", 202, nil)
	k.call("POST", "/api/projects/shop/evals/run", "", 400, nil) // one at a time
	defer k.run()()
	k.until("the suite done", func() bool { return k.count(`SELECT count(*) FROM eval_suites WHERE state = 'done'`) == 1 })

	var state string
	var met *bool
	var rework, checks int
	var branch string
	k.d.Server.QueryRow(context.Background(), `SELECT state, goal_met, rework, check_rounds, branch FROM eval_runs`).Scan(&state, &met, &rework, &checks, &branch)
	if state != "done" || met == nil || !*met || rework != 1 || checks != 1 || !strings.HasPrefix(branch, "eval/") {
		t.Fatalf("the eval run: %s %v rework %d checks %d %s", state, met, rework, checks, branch)
	}
	// It records the CLI and its version its builder ran (9.6), as the builder's run recorded them.
	if n := k.count(`SELECT count(*) FROM eval_runs e JOIN runs r ON r.id = e.builder_run
		WHERE e.cli = r.cli AND e.cli_version = r.cli_version`); n != 1 {
		t.Fatal("the eval run does not record its builder's CLI version")
	}
	modeMu.Lock()
	if len(evalModes) != 1 || evalModes["sources"] == 0 {
		t.Fatalf("the eval's checks ran in %v", evalModes)
	}
	modeMu.Unlock()
	// Build, fix for the review, fix for the checks; each fixed head is reviewed and tested again.
	if len(specs["builder"]) != 3 || len(specs["reviewer"]) != 3 || len(specs["tester"]) != 2 {
		t.Fatalf("runs: %d builders, %d reviewers, %d testers", len(specs["builder"]), len(specs["reviewer"]), len(specs["tester"]))
	}
	for _, s := range specs["builder"] {
		if s.MCP != "" || s.MainInstructions || s.Branch != branch {
			t.Fatalf("an eval builder's spec: MCP %q main %v branch %q", s.MCP, s.MainInstructions, s.Branch)
		}
		if strings.Contains(s.Prompt, "the held-out test failed") {
			t.Fatal("the builder was shown the output of the held-out test")
		}
	}
	// The reviewer reads the branch with tools, as a ticket's, and is given the diff (design 0.50).
	if r := specs["reviewer"][0]; r.Judge || !r.Tools || !r.ReadOnly || r.Branch != branch || r.MCP != "" || !strings.Contains(r.Prompt, "eval-work.txt") {
		t.Fatalf("the reviewer: judge %v tools %v read-only %v branch %q MCP %q", r.Judge, r.Tools, r.ReadOnly, r.Branch, r.MCP)
	}
	if ts := specs["tester"][1]; !ts.ReadOnly || ts.Branch != branch {
		t.Fatalf("the tester: %+v", ts)
	}
	if k.git.Main(context.Background(), "shop") != main {
		t.Fatal("main moved")
	}
	if h, _ := k.git.Head(context.Background(), "shop", branch); h != "" {
		t.Fatal("the eval's branch is still there")
	}
	var scope string
	k.d.Server.QueryRow(context.Background(), `SELECT DISTINCT scope FROM subtasks WHERE kind LIKE 'eval_%'`).Scan(&scope)
	if scope != "eval" {
		t.Fatalf("the eval's runs have the scope %q", scope)
	}
	var ev struct {
		Suites []struct {
			State  string
			Result struct {
				Baseline struct {
					Cases  int
					Passed float64
				}
			}
		}
	}
	k.call("GET", "/api/projects/shop/evals", "", 200, &ev)
	if len(ev.Suites) != 1 || ev.Suites[0].Result.Baseline.Cases != 1 || ev.Suites[0].Result.Baseline.Passed != 1 {
		t.Fatalf("the suite: %+v", ev.Suites)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'eval_result'`); n != 1 {
		t.Fatalf("%d eval_result signals", n)
	}
}

// evalShop is shop with SF-1 Done, its case with 1.txt held out, and the knowledge settings given.
func (k *ticketKit) evalShop(evals map[string]any) {
	k.t.Helper()
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	stop := k.run()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	// It joins the suite once its fit check finds nothing missing (design 0.51).
	k.until("the case active", func() bool { return k.cases()[1].Status == "active" })
	stop()
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	for key, v := range evals {
		doc["knowledge"].(map[string]any)["evals"].(map[string]any)[key] = v
	}
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
}

// A lead's override goes to the project's evals by itself (9.6): its suite runs every case on the
// baseline and with the override, the change's runs read the override's text, and evals that are not
// worse apply it, with the numbers kept beside it. With fewer cases than knowledge.evals.min_cases it
// stays in testing, and waits for the operator in the inbox.
func TestAnOverrideIsDecidedByItsEvals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		minCases int
		want     string
	}{{"enough cases", 1, "applied"}, {"too few cases", 10, "testing"}} {
		t.Run(tc.name, func(t *testing.T) {
			k := newTicketKit(t)
			k.evalShop(map[string]any{"repeats": 1, "min_cases": tc.minCases})
			specs := k.evalScript(t)
			var id string
			k.d.Server.QueryRow(context.Background(), `SELECT id::text FROM projects WHERE name = 'shop'`).Scan(&id)
			if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO overrides (project, number, path, version, text, why, status, proposed_by)
				VALUES ($1, 1, 'stages/in_progress.md', 1, '# In progress: build one ticket'||chr(10)||'OVERRIDE MARK: run the checks first.', 'check rounds', 'testing', 'lead · run')`, id); err != nil {
				t.Fatal(err)
			}
			defer k.run()()
			k.until("its suite done", func() bool {
				return k.count(`SELECT count(*) FROM eval_suites WHERE state = 'done' AND purpose = 'override'`) == 1
			})
			var status string
			var eval []byte
			k.d.Server.QueryRow(context.Background(), `SELECT status, coalesce(eval, '{}') FROM overrides`).Scan(&status, &eval)
			if status != tc.want {
				t.Fatalf("the override is %s: %s", status, eval)
			}
			marked := 0
			for _, s := range specs["builder"] {
				if strings.Contains(s.Prompt, "OVERRIDE MARK") {
					marked++
				}
			}
			if marked == 0 || marked == len(specs["builder"]) {
				t.Fatalf("%d of %d builders read the override: only the change's should", marked, len(specs["builder"]))
			}
			var sides int
			k.d.Server.QueryRow(context.Background(), `SELECT count(DISTINCT side) FROM eval_runs`).Scan(&sides)
			if sides != 2 {
				t.Fatalf("%d sides", sides)
			}
			var inbox struct{ Items []struct{ Kind string } }
			k.call("GET", "/api/inbox", "", 200, &inbox)
			waiting := false
			for _, it := range inbox.Items {
				waiting = waiting || it.Kind == "override"
			}
			if waiting != (tc.want == "testing") {
				t.Fatalf("in the inbox: %v", waiting)
			}
		})
	}
}

// A case comes from a fixed bug through its fix ticket, and a retired case leaves the suite but stays in
// the record; a ticket that is not Done, and a bug that is not fixed, make none (9.6).
func TestCasesFromBugsAndRetired(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.evalShop(nil)
	k.call("POST", "/api/projects/shop/evals/cases/1/retire", `{"why": ""}`, 400, nil)
	k.call("POST", "/api/projects/shop/evals/cases/1/retire", `{"why": "replaced by the bug's case"}`, 200, nil)
	k.call("POST", "/api/projects/shop/evals/cases/1/retire", `{"why": "again"}`, 404, nil)
	var id string
	k.d.Server.QueryRow(context.Background(), `SELECT id::text FROM projects WHERE name = 'shop'`).Scan(&id)
	k.d.Server.Exec(context.Background(), `INSERT INTO bugs (project, number, title, severity, status, found_by, fix_ticket)
		VALUES ($1, 1, 'Lost', 'high', 'open', 'tester', (SELECT id FROM tickets WHERE number = 1)),
		       ($1, 2, 'Gone', 'low', 'fixed', 'tester', (SELECT id FROM tickets WHERE number = 1))`, id)
	k.call("POST", "/api/projects/shop/evals/cases", `{"bug": 1, "heldOut": ["1.txt"]}`, 400, nil)
	var c struct {
		Number int
		Origin string
		Bug    int
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"bug": 2, "heldOut": ["1.txt"], "why": "a bug that escaped"}`, 201, &c)
	if c.Number != 2 || c.Origin != "bug" || c.Bug != 2 {
		t.Fatalf("the bug's case: %+v", c)
	}
	k.call("POST", "/api/projects/shop/tasks", `{"title": "two", "why": "w", "what": "w", "goal": "g", "checks": [{"kind": "cmd", "spec": "true"}], "docsReason": "none"}`, 201, nil)
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-2"}`, 400, nil)
	var ev struct {
		Cases []struct {
			Number int
			Status string
		}
	}
	k.call("GET", "/api/projects/shop/evals", "", 200, &ev)
	// The bug's case waits for its fit check (design 0.51).
	if len(ev.Cases) != 2 || ev.Cases[1].Status != "retired" || ev.Cases[0].Status != "waiting" {
		t.Fatalf("the cases: %+v", ev.Cases)
	}
}

// The retrospective (9.8, R64): a part of the plan whose tickets are all Done gets one, and the tag
// part-<name> on its last merge; the Done project gets one before its final report, and the tag done.
// What the lead writes as lessons is in the report's facts.
func TestTheRetrospectives(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	var mu sync.Mutex
	var retros []string
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if strings.Contains(spec.Prompt, "# Retrospective: learn from a part") {
			mu.Lock()
			retros = append(retros, spec.Prompt)
			n := len(retros)
			mu.Unlock()
			title := map[int]string{1: "Small tickets pass their checks the first time", 2: "A renamed fixture breaks the merge of the next ticket"}[n]
			if out, ok := k.tool(spec.Run, "remember", map[string]any{"kind": "lesson", "title": title, "body": "Seen in the summaries."}); !ok {
				t.Errorf("remember: %s", out)
			}
			return recording(spec), true
		}
		return prev(kind, spec, p)
	}
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "stages": [], "repository": {"kind": "local"}}`, 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = ""
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	titles := []string{"one", "two", "three"}
	for i, part := range []string{"Sign-in", "Sign-in", ""} {
		partJSON := `"part": "` + part + `", `
		if part == "" {
			partJSON = ""
		}
		k.call("POST", "/api/projects/shop/tasks", fmt.Sprintf(`{"title": %q, %s"why": "w", "what": "w", "goal": "g",
			"checks": [{"kind": "cmd", "spec": "test -f %d.txt"}], "docsReason": "none"}`, titles[i], partJSON, i+1), 201, nil)
	}
	k.call("POST", "/api/projects/shop/ok", "", 200, nil)
	if n := k.count(`SELECT count(*) FROM tickets WHERE part = 'Sign-in'`); n != 2 {
		t.Fatalf("%d tickets of the part", n)
	}
	defer k.run()()
	k.until("the final report", func() bool { return k.count(`SELECT count(*) FROM reports`) == 1 })

	var rows []struct {
		part, tag, state string
	}
	r, _ := k.d.Server.Query(context.Background(), `SELECT coalesce(part, ''), coalesce(tag, ''), state FROM retrospectives ORDER BY id`)
	for r.Next() {
		var x struct{ part, tag, state string }
		r.Scan(&x.part, &x.tag, &x.state)
		rows = append(rows, x)
	}
	r.Close()
	// The part's retrospective runs when the part is complete before the project is Done; a part the
	// last ticket completes is read by the Done project's.
	if len(rows) != 2 || rows[0].part != "Sign-in" || rows[0].tag != "part-sign-in" || rows[0].state != "done" ||
		rows[1].part != "" || rows[1].tag != "done" || rows[1].state != "done" {
		t.Fatalf("retrospectives: %+v", rows)
	}
	var partRan bool
	k.d.Server.QueryRow(context.Background(), `SELECT run IS NOT NULL FROM retrospectives WHERE part = 'Sign-in'`).Scan(&partRan)
	mu.Lock()
	ran := len(retros)
	mu.Unlock()
	if (partRan && ran != 2) || (!partRan && ran != 1) {
		t.Fatalf("%d retrospective runs, the part's ran: %v", ran, partRan)
	}
	tags, _ := k.git.Tags(context.Background(), "shop")
	names := map[string]string{}
	for _, tg := range tags {
		names[tg.Name] = tg.Commit
	}
	if names["part-sign-in"] == "" || names["done"] == "" || names["part-sign-in"] == names["done"] {
		t.Fatalf("tags %v", names)
	}
	mu.Lock()
	first := retros[0]
	mu.Unlock()
	if partRan {
		if !strings.Contains(first, `The part \"Sign-in\" of its plan is complete`) && !strings.Contains(first, `The part "Sign-in" of its plan is complete`) {
			t.Fatalf("the part's retrospective: %s", first)
		}
		if !strings.Contains(first, "SF-1") || !strings.Contains(first, "SF-2") || strings.Contains(first, `"SF-3"`) {
			t.Fatal("the part's retrospective reads other tickets, or not its own")
		}
	}
	// The report started after the Done retrospective ended, and lists its lessons.
	var reportStart, retroEnd time.Time
	k.d.Server.QueryRow(context.Background(), `SELECT min(created_at) FROM subtasks WHERE kind = 'report'`).Scan(&reportStart)
	k.d.Server.QueryRow(context.Background(), `SELECT ended_at FROM retrospectives WHERE part IS NULL`).Scan(&retroEnd)
	if reportStart.Before(retroEnd) {
		t.Fatalf("the report started at %v, before the retrospective ended at %v", reportStart, retroEnd)
	}
	var facts struct{ Lessons []string }
	var raw []byte
	k.d.Server.QueryRow(context.Background(), `SELECT facts FROM reports`).Scan(&raw)
	json.Unmarshal(raw, &facts)
	if len(facts.Lessons) != ran {
		t.Fatalf("the report's lessons: %v, of %d retrospectives", facts.Lessons, ran)
	}
}

// The improver (10.4): asked for, it runs with no project, in Model only, and works through the Crew
// level's tools with its own token; a project's tool refuses it, and a project's run reaches no Crew
// level tool. Its proposals are checked: one that names a project is refused, one in general words is
// tested, and waits for cases. The operator reads an improvement with its files, and takes it out.
func TestTheImprover(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.evalShop(nil)
	k.pipe.Improve = &improve.Service{Pool: k.d.Server, Board: k.b, Running: "0.0.0-test",
		Settings: func(ctx context.Context) (settings.Doc, error) { return k.pipe.Settings(ctx) }}
	k.tools.Improve, k.tools.Version = k.pipe.Improve, "0.0.0-test"
	var mu sync.Mutex
	results := map[string]bool{}
	var proposals []string
	var builderRun string
	k.d.Server.QueryRow(context.Background(), `SELECT r.id::text FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.role = 'builder' LIMIT 1`).Scan(&builderRun)
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if !strings.Contains(spec.Prompt, "You are the improver of Crew") {
			return prev(kind, spec, p)
		}
		call := func(name string, args any) (string, bool) {
			out, ok := k.tool(spec.Run, name, args)
			mu.Lock()
			results[name] = ok
			mu.Unlock()
			return out, ok
		}
		call("list_signals", map[string]any{"project": "SF"})
		call("get_metrics", map[string]any{})
		call("get_trajectory", map[string]any{"run": builderRun})
		call("get_baseline", map[string]any{"path": "baseline/stages/in_progress.md"})
		call("list_rewrites", map[string]any{})
		call("list_improvements", map[string]any{})
		call("search_summaries", map[string]any{"query": "lock"})
		call("note_for_operator", map[string]any{"project": "shop", "text": "Its setup is slow."})
		if out, ok := k.tool(spec.Run, "add_note", map[string]any{"text": "x"}); ok {
			t.Errorf("the improver called a project's tool: %s", out)
		}
		for _, patch := range []string{"Do as shop does.\n", "# In progress\n\nRun the ticket's checks before you ask for a move.\n"} {
			out, _ := call("propose_improvement", map[string]any{"title": "Run the checks first", "patch": map[string]any{"baseline/stages/in_progress.md": patch},
				"evidence": map[string]any{"signal": "check_round", "text": "Builders ask too early."}, "expected": "More pass first time.", "measure": "first_request_pass_rate"})
			mu.Lock()
			proposals = append(proposals, out)
			mu.Unlock()
		}
		return recording(spec), true
	}
	k.call("POST", "/api/crew/improver/run", "", 202, nil)
	defer k.run()()
	k.until("the improver's run ended", func() bool {
		return k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.scope = 'improver' AND r.state = 'ended'`) == 1
	})
	mu.Lock()
	for _, name := range []string{"list_signals", "get_metrics", "get_trajectory", "get_baseline", "list_rewrites", "list_improvements", "search_summaries", "note_for_operator"} {
		if !results[name] {
			t.Errorf("the improver could not call %s", name)
		}
	}
	mu.Unlock()
	var spec harness.RunSpec
	k.mu.Lock()
	for _, specs := range k.specs {
		for _, s := range specs {
			if s.Improver {
				spec = s
			}
		}
	}
	k.mu.Unlock()
	if !spec.Improver || spec.Repo != "" || spec.Mode != "model_only" || spec.MCP == "" {
		t.Fatalf("the improver's spec: %+v", spec)
	}
	var list struct {
		Improvements []struct {
			Number     int
			Status     string
			WaitsFor   *string
			RefusedWhy *string
		}
	}
	k.call("GET", "/api/crew/improvements", "", 200, &list)
	if len(list.Improvements) != 2 || list.Improvements[1].Status != "refused" || list.Improvements[0].Status != "testing" {
		t.Fatalf("the improvements: %+v (%v)", list.Improvements, proposals)
	}
	k.until("the tested one waits for cases", func() bool {
		return k.count(`SELECT count(*) FROM improvements WHERE status = 'testing' AND waits_for = 'cases'`) == 1
	})
	var one struct {
		Improvement struct{ Title string }
		Files       []struct{ Path, Before, After string }
	}
	k.call("GET", "/api/crew/improvements/2", "", 200, &one)
	if len(one.Files) != 1 || one.Files[0].Path != "baseline/stages/in_progress.md" || one.Files[0].Before == one.Files[0].After {
		t.Fatalf("an improvement's files: %+v", one)
	}
	k.call("GET", "/api/crew/improvements/9", "", 404, nil)
	k.call("POST", "/api/crew/improvements/2", `{"choice": "take_out"}`, 200, nil)
	k.call("POST", "/api/crew/improvements/2", `{"choice": "take_out"}`, 400, nil)
	var news int
	k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM signals WHERE kind = 'improver_note' AND detail->>'text' = 'The improver: Its setup is slow.'`).Scan(&news)
	if news != 1 {
		t.Fatal("no note for the operator")
	}
	var b struct {
		Version string
		Files   []struct {
			Path      string
			MayChange bool
		}
	}
	k.call("GET", "/api/crew/baseline", "", 200, &b)
	may := map[string]bool{}
	for _, f := range b.Files {
		may[f.Path] = f.MayChange
	}
	if !may["baseline/stages/in_progress.md"] || may["baseline/agents/reviewer.md"] || may["baseline/tools.yaml"] {
		t.Fatalf("the baseline: %+v", b)
	}
}

// A project's run, while it works, reaches no tool of the Crew level (10.4, 10.6).
func TestAProjectsRunReachesNoCrewTool(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var mu sync.Mutex
	tried := map[string]string{}
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind == "build" {
			for _, name := range []string{"list_signals", "propose_improvement", "list_rewrites", "get_trajectory"} {
				out, ok := k.tool(spec.Run, name, map[string]any{})
				mu.Lock()
				if ok {
					tried[name] = "called: " + out
				} else {
					tried[name] = out
				}
				mu.Unlock()
			}
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	mu.Lock()
	defer mu.Unlock()
	if len(tried) != 4 {
		t.Fatalf("tried %v", tried)
	}
	for name, out := range tried {
		if !strings.Contains(out, "is not one of the tools of this run") {
			t.Errorf("a builder's %s: %s", name, out)
		}
	}
}

// An improvement that measured worse in a release that is no longer the running one is taken out with a
// revert of its ticket on Crew's project, which is not the operator stepping in; and the generic check
// refuses a patch that names a file a cited ticket changed (10.5).
func TestAWorseImprovementIsRevertedOnCrewsProject(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one", "two")
	stop := k.run()
	k.until("both Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	stop()
	ctx := context.Background()
	if err := improve.Generic(ctx, k.d.Server, k.git, map[string]string{"baseline/guides/planning.md": "Keep 1.txt small.\n"}, []string{"SF-1"}); err == nil ||
		!strings.Contains(err.Error(), "a file a cited ticket changed") {
		t.Fatalf("a cited ticket's file: %v", err)
	}
	k.d.Server.Exec(ctx, `UPDATE runs SET crew_version = CASE WHEN s.ticket = (SELECT id FROM tickets WHERE number = 1) THEN 'A' ELSE 'B' END
		FROM subtasks s WHERE s.id = runs.subtask AND s.ticket IS NOT NULL`)
	k.d.Server.Exec(ctx, `INSERT INTO signals (project, ticket, kind) SELECT project, id, 'check_round' FROM tickets WHERE number = 2`)
	k.d.Server.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('B', 'c', 's', '{}', now(), 'text', now())`)
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, status, target, patch, measure, ticket, release_from, release_to, evals)
		VALUES (1, 'x', 'applied', '{baseline/guides/planning.md}', '{"baseline/guides/planning.md": "x"}', 'first_request_pass_rate',
			(SELECT id FROM tickets WHERE number = 2), 'A', 'B', '{"margin": 0}')`); err != nil {
		t.Fatal(err)
	}
	k.crewProjectIs("shop")
	rolledBack := false
	svc := &improve.Service{Pool: k.d.Server, Board: k.b, Running: "C",
		Settings: func(ctx context.Context) (settings.Doc, error) { return k.pipe.Settings(ctx) },
		Rollback: func(context.Context, string) error { rolledBack = true; return nil }}
	if err := svc.Measure(ctx, 1); err != nil {
		t.Fatal(err)
	}
	x, _ := svc.Get(ctx, 1)
	if x.Status != "rolled_back" || rolledBack {
		t.Fatalf("taken out: %s, rolled back by the updater: %v", x.Status, rolledBack)
	}
	var r struct{ Ticket struct{ Kind string } }
	k.call("GET", "/api/projects/shop/tasks/SF-3", "", 200, &r)
	if r.Ticket.Kind != "revert" {
		t.Fatalf("SF-3 is %+v", r)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'operator_intervention'`); n != 0 {
		t.Fatalf("Crew's revert counted as the operator stepping in: %d", n)
	}
}

// An example of a change of the prompt writer's rules rewrites an earlier first message again: by the
// rules as they are, and by the new ones in their place, and keeps no rewrite of the operator's (7.2).
func TestAnExampleOfAChangeOfThePromptWritersRules(t *testing.T) {
	t.Parallel()
	k := newPlanKit(t)
	var prompts []string
	var mu sync.Mutex
	k.rewriter = func(spec harness.RunSpec) harness.Script {
		mu.Lock()
		prompts = append(prompts, spec.Prompt)
		mu.Unlock()
		return harness.Script{Lines: answerLines(spec, goodRewrite)}
	}
	ctx := context.Background()
	var id int64
	if err := k.d.Server.QueryRow(ctx, `WITH d AS (INSERT INTO plan_drafts (id) VALUES (gen_random_uuid()) RETURNING id)
		INSERT INTO prompt_rewrites (kind, draft, original, state, choice) SELECT 'first', id, 'make a shop', 'ready', 'mine' FROM d RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	old, err := k.rw.Example(ctx, id, nil)
	if err != nil || old == "" {
		t.Fatalf("by the rules as they are: %q %v", old, err)
	}
	if _, err := k.rw.Example(ctx, id, map[string]string{"agents/prompt-writer.md": "---\nname: prompt-writer\n---\nNEW RULES MARK"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || strings.Contains(prompts[0], "NEW RULES MARK") || !strings.Contains(prompts[1], "NEW RULES MARK") || !strings.Contains(prompts[1], "make a shop") {
		t.Fatalf("the prompts: %d", len(prompts))
	}
	var n int
	k.d.Server.QueryRow(ctx, `SELECT count(*) FROM prompt_rewrites`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rewrites kept", n)
	}
}

// A passed improvement is applied once a release that holds its ticket's merge is installed: that
// release is the version it shipped in, and the one installed before it the version it is measured
// against (10.5, step 5).
func TestAPassedImprovementIsAppliedByTheReleaseThatHoldsIt(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one", "two")
	stop := k.run()
	k.until("both Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	stop()
	ctx := context.Background()
	var merge string
	k.d.Server.QueryRow(ctx, `SELECT commit FROM check_runs WHERE purpose = 'merge' AND state = 'passed' AND ticket = (SELECT id FROM tickets WHERE number = 2) ORDER BY id DESC LIMIT 1`).Scan(&merge)
	before, err := k.git.FirstParent(ctx, "shop", merge)
	if err != nil {
		t.Fatal(err)
	}
	k.d.Server.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES
		('A', $1, 's', '{}', now(), 'text', now() - interval '2 hours'), ('B', $2, 's', '{}', now(), 'text', now())`, before, k.git.Main(ctx, "shop"))
	k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, status, target, patch, measure, ticket)
		VALUES (1, 'x', 'passed', '{baseline/stages/in_progress.md}', '{"baseline/stages/in_progress.md": "x"}', 'first_request_pass_rate', (SELECT id FROM tickets WHERE number = 2))`)
	k.crewProjectIs("shop")
	svc := &improve.Service{Pool: k.d.Server, Board: k.b, Settings: func(ctx context.Context) (settings.Doc, error) { return k.pipe.Settings(ctx) }}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	x, _ := svc.Get(ctx, 1)
	if x.Status != "applied" || x.ReleaseTo == nil || *x.ReleaseTo != "B" || x.ReleaseFrom == nil || *x.ReleaseFrom != "A" || x.AppliedAt == nil {
		t.Fatalf("applied: %+v", x)
	}
}

// The Baseline's Versions card titles every version: by the improvement it carried, else by its
// release's own first change (crew--baseline.png; M15 on dave showed only the number).
func TestTheBaselinesVersionsHaveATitle(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	for v, notes := range map[string]string{"0.2.0": "# Crew 0.2.0\n\n- Evals replay done tickets\n- Analytics\n", "0.2.1": "# Crew 0.2.1\n\n- The menu marks its own page\n",
		// A title given at the build heads the notes, and wins over the first commit line; the heading
		// of another version's number is no title of this one.
		"0.2.2": "# Crew 0.2.2: Evals measure only what ran\n\nThe commits since abc:\n\n- The review's second round (abc)\n",
		"0.2.3": "# Crew 0.2.2: not this one\n\n- An eval's check stays off the builder's family\n",
		// A title read from the notes loses what could make it read as another, and is cut at 120.
		"0.2.4": "# Crew 0.2.4: Safe\u202e \u200bnaming\n", "0.2.5": "# Crew 0.2.5: " + strings.Repeat("é", 130) + "\n"} {
		os.MkdirAll(filepath.Join(dir, v), 0o755)
		os.WriteFile(filepath.Join(dir, v, "notes.md"), []byte(notes), 0o644)
	}
	k.api.Versions.Store(&versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1"})
	for i, v := range []string{"0.1.0", "0.2.0", "0.2.1", "0.2.2", "0.2.3", "0.2.4", "0.2.5"} {
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at)
			VALUES ($1, 'c', 's', '{}', now(), 'code', now() + make_interval(mins => $2))`, v, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, status, target, patch, measure, release_to)
		VALUES (1, 'Builders run the checks first', 'applied', '{}', '{}', 'fix_rounds_per_ticket', '0.2.0')`); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Versions []struct{ Version, Note string }
	}
	k.call("GET", "/api/crew/baseline", "", 200, &b)
	got := map[string]string{}
	for _, v := range b.Versions {
		got[v.Version] = v.Note
	}
	// 0.1.0's folder is gone, so it has no line.
	want := map[string]string{"0.2.5": strings.Repeat("é", 120), "0.2.4": "Safe naming", "0.2.3": "An eval's check stays off the builder's family", "0.2.2": "Evals measure only what ran", "0.2.1": "The menu marks its own page",
		"0.2.0": "Builders run the checks first", "0.1.0": ""}
	if !maps.Equal(got, want) {
		t.Fatalf("the versions' titles: %v", got)
	}
}

// crewProjectIs makes a project Crew's project in the settings, with the remote the check of 16.2
// asks for: updates.source_repo's (design 0.51).
func (k *ticketKit) crewProjectIs(name string) {
	k.t.Helper()
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE projects SET remote = 'git@github.com:kesokaj/crew.git' WHERE name = $1`, name); err != nil {
		k.t.Fatal(err)
	}
	var doc map[string]any
	k.call("GET", "/api/settings/machine", "", 200, &doc)
	doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew"
	doc["improvements"].(map[string]any)["crew_project"] = name
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/settings/machine", string(b), 200, nil)
}
