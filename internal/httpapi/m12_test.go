package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
)

// mainFile is a file of the shop's main, or nil when it is not there or cannot be read.
func mainFile(g *gitstore.Store, ctx context.Context, path string) []byte {
	b, _ := g.File(ctx, "shop", path)
	return b
}

// The routes of a project's knowledge (9, 15.4): the operator writes memory, reports and closes a bug,
// gives feedback and a rule, and keeps a comment for the project; what is refused says why.
func TestTheKnowledgeRoutes(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop([]string{}, nil, "one")

	var e struct {
		Number int
		Author string
		Status string
	}
	k.call("POST", "/api/projects/shop/memory", `{"kind": "convention", "title": "Error responses use problem+json", "appliesTo": ["builder", "reviewer"]}`, 201, &e)
	if e.Number != 1 || e.Author != "operator" || e.Status != "active" {
		t.Fatalf("entry %+v", e)
	}
	k.call("POST", "/api/projects/shop/memory", `{"kind": "convention", "title": "Error responses use problem+json"}`, 409, nil)
	k.call("POST", "/api/projects/shop/memory", `{"kind": "rumour", "title": "x"}`, 400, nil)
	k.call("PATCH", "/api/projects/shop/memory/1", `{"retire": "stale"}`, 200, &e)
	if e.Status != "retired" {
		t.Fatalf("retired: %+v", e)
	}
	k.call("PATCH", "/api/projects/shop/memory/9", `{"retire": "stale"}`, 404, nil)

	var bug struct {
		Bug       struct{ Number int }
		FixTicket string
	}
	k.call("POST", "/api/projects/shop/bugs", `{"title": "Sign-up takes a trailing space", "severity": "low", "happens": "a@x  is taken"}`, 201, &bug)
	if bug.Bug.Number != 1 || bug.FixTicket != "SF-2" {
		t.Fatalf("bug %+v", bug)
	}
	k.call("POST", "/api/projects/shop/bugs/1/close", `{"reason": "wontfix"}`, 400, nil)
	k.call("POST", "/api/projects/shop/bugs/1/close", `{"reason": "not_reproducible", "note": "Trimmed on main."}`, 200, nil)
	var bugs struct {
		Bugs []struct{ Status, ClosedReason, FoundBy string }
	}
	k.call("GET", "/api/projects/shop/bugs", "", 200, &bugs)
	if len(bugs.Bugs) != 1 || bugs.Bugs[0].Status != "closed" || bugs.Bugs[0].FoundBy != "operator" {
		t.Fatalf("bugs %+v", bugs)
	}

	var f struct {
		Number  int
		Status  string
		Outcome *string
	}
	k.call("POST", "/api/projects/shop/feedback", `{"about": "reviewer", "kind": "rule", "body": "Reviews check that the note says how the work was tested."}`, 201, &f)
	if f.Status != "applied" || f.Outcome == nil || *f.Outcome != "memory 0002" {
		t.Fatalf("a rule %+v", f)
	}
	k.call("POST", "/api/projects/shop/feedback", `{"about": "lead", "body": "Tickets were too large."}`, 201, &f)
	k.call("POST", "/api/projects/shop/feedback/2/resolve", `{"outcome": "none", "reason": "Said already."}`, 200, &f)
	if f.Status != "dismissed" {
		t.Fatalf("dismissed %+v", f)
	}

	// A comment kept for the project is a memory entry of the operator's, with its ticket.
	k.call("POST", "/api/projects/shop/comments", `{"ticket": "SF-1", "text": "Money is stored as integer cents\nNever floats.", "remember": true}`, 201, nil)
	var mem struct {
		Entries []struct {
			Number       int
			Title, Body  string
			SourceTicket *string
			Author       string
		}
	}
	k.call("GET", "/api/projects/shop/memory", "", 200, &mem)
	if len(mem.Entries) != 3 || mem.Entries[0].Title != "Money is stored as integer cents" || mem.Entries[0].Body != "Never floats." ||
		mem.Entries[0].SourceTicket == nil || *mem.Entries[0].SourceTicket != "SF-1" {
		t.Fatalf("memory %+v", mem)
	}
	// What it would keep is refused, so the comment is not saved either.
	before := k.count(`SELECT count(*) FROM records WHERE kind = 'comment'`)
	k.call("POST", "/api/projects/shop/comments", `{"ticket": "SF-1", "text": "Money is stored as integer cents\nNever as floats.", "remember": true}`, 409, nil)
	if k.count(`SELECT count(*) FROM records WHERE kind = 'comment'`) != before {
		t.Fatal("the comment was saved with its entry refused")
	}
	var fb struct {
		Feedback []struct{ About, From string }
	}
	k.call("GET", "/api/projects/shop/feedback", "", 200, &fb)
	if len(fb.Feedback) != 2 || !strings.Contains(fb.Feedback[1].About, "reviewer") {
		t.Fatalf("feedback %+v", fb)
	}
}

// A bug an agent reports becomes a fix ticket that another agent analyses (9.4): approved, its check
// fails on main before the work, so it is the bug's regression check; built and merged, the bug is
// fixed. A second report that Analysis discards closes its bug and is feedback on the builder who
// reported it (21, row 19). Memory reaches every run, fenced as data (9.3).
func TestABugBecomesAFixTicketAndIsFixed(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var once sync.Once
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		switch {
		case kind == "build" && key == "SF-1":
			once.Do(func() {
				for _, title := range []string{"Two", "The colour is wrong"} {
					if text, ok := k.tool(spec.Run, "report_bug", map[string]any{"title": title, "severity": "medium",
						"happens": title + ".", "steps": "ls"}); !ok || !strings.Contains(text, "fixTicket") {
						t.Errorf("report_bug: %s", text)
					}
				}
			})
		case kind == "analysis" && key == "SF-2":
			// Approving a bug's fix, Analysis says which Done ticket the defect is in: none is Done yet (0.54).
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "bugIn": "none",
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f 2.txt"}, {"kind": "cmd", "spec": "true", "keeps": true}}})
			return true
		case kind == "analysis" && key == "SF-3":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "discard", "reason": "The colour is the plan's."})
			return true
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	k.call("POST", "/api/projects/shop/memory", `{"kind": "fact", "title": "Every file the tickets add is a .txt file"}`, 201, nil)
	defer k.run()()
	k.until("the bug fixed", func() bool { return k.count(`SELECT count(*) FROM bugs WHERE status = 'fixed'`) == 1 })
	var bugs struct {
		Bugs []struct {
			Number                              int
			Status, FoundBy                     string
			FoundIn, FixTicket, RegressionCheck *string
			ClosedReason                        *string
			InTicket                            *string
			InConfirmed                         bool
		}
	}
	k.call("GET", "/api/projects/shop/bugs", "", 200, &bugs)
	if len(bugs.Bugs) != 2 {
		t.Fatalf("bugs %+v", bugs)
	}
	fixed, closed := bugs.Bugs[1], bugs.Bugs[0]
	if fixed.FoundBy != "builder" || deref(fixed.FoundIn) != "SF-1" || deref(fixed.FixTicket) != "SF-2" || deref(fixed.RegressionCheck) != "test -f 2.txt" ||
		!fixed.InConfirmed || fixed.InTicket != nil {
		t.Fatalf("the fixed bug %+v", fixed)
	}
	if k.stageOf("SF-2") != "done" {
		t.Fatalf("SF-2 is in %s", k.stageOf("SF-2"))
	}
	if closed.Status != "closed" || deref(closed.ClosedReason) != "discarded" {
		t.Fatalf("the discarded bug %+v", closed)
	}
	var fb struct {
		Feedback []struct{ From, About, Body string }
	}
	k.call("GET", "/api/projects/shop/feedback", "", 200, &fb)
	if len(fb.Feedback) != 1 || fb.Feedback[0].From != "system" || fb.Feedback[0].About != "builder" || !strings.Contains(fb.Feedback[0].Body, "SF-3") {
		t.Fatalf("feedback %+v", fb)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, p := range k.prompts["build"] {
		if !strings.Contains(p, "The project's memory") || !strings.Contains(p, "- 0001 (fact) Every file the tickets add") {
			t.Fatalf("a build's prompt has no memory:\n%s", p)
		}
	}
}

// Tickets update their pages (9.9): a ticket's decision record and its changelog entry are in its merge
// commit; the pages on main are indexed and shown; the operator edits a page, and an edit from a page
// that changed since is refused (21, row 25).
func TestTicketsWriteTheirPagesAndTheChangelog(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind == "build" && key == "SF-1" {
			if text, ok := k.tool(spec.Run, "record_decision", map[string]any{"title": "Files are plain text",
				"context": "The tests read files.", "decision": "Every ticket adds a .txt file.", "reason": "test -f reads it."}); !ok || !strings.Contains(text, "0001-files-are-plain-text") {
				t.Errorf("record_decision: %s", text)
			}
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	log := string(mainFile(k.git, ctx, "CHANGELOG.md"))
	if !strings.HasPrefix(log, "# Changelog\n\nOne entry per ticket that reaches Done.\n\n## SF-1: one\n") || !strings.Contains(log, "The builder's last note: Built SF-1") {
		t.Fatalf("the changelog on main:\n%s", log)
	}
	if page := string(mainFile(k.git, ctx, "docs/decisions/0001-files-are-plain-text.md")); !strings.Contains(page, "## Decision\n\nEvery ticket adds a .txt file.") {
		t.Fatalf("the decision's page on main:\n%s", page)
	}
	var docs struct {
		Main    string
		Current int
		Pages   []struct {
			Path, Kind string
			LastTicket *string
		}
	}
	k.call("GET", "/api/projects/shop/docs", "", 200, &docs)
	byPath := map[string]string{}
	for _, d := range docs.Pages {
		byPath[d.Path] = d.Kind + " " + deref(d.LastTicket)
	}
	if byPath["docs/decisions/0001-files-are-plain-text.md"] != "decision SF-1" || byPath["CHANGELOG.md"] != "changelog SF-1" || docs.Current != len(docs.Pages) {
		t.Fatalf("docs %+v", docs)
	}
	var hits struct{ Pages []struct{ Path string } }
	k.call("GET", "/api/projects/shop/docs/search?q=plain", "", 200, &hits)
	if len(hits.Pages) != 1 {
		t.Fatalf("search %+v", hits)
	}

	// The operator edits the overview from the main it was read at; a second edit from that main of the
	// same page is refused.
	var page struct {
		Main string
		Page struct{ Body string }
	}
	k.call("GET", "/api/projects/shop/docs/page?path=README.md", "", 200, &page)
	k.call("PUT", "/api/projects/shop/docs/page", fmt.Sprintf(`{"path": "README.md", "base": %q, "content": "# shop\n\nSells things.\n"}`, page.Main), 200, nil)
	if got := string(mainFile(k.git, ctx, "README.md")); got != "# shop\n\nSells things.\n" {
		t.Fatalf("README on main: %q", got)
	}
	k.call("PUT", "/api/projects/shop/docs/page", fmt.Sprintf(`{"path": "README.md", "base": %q, "content": "# mine\n"}`, page.Main), 409, nil)
	k.call("PUT", "/api/projects/shop/docs/page", fmt.Sprintf(`{"path": "CHANGELOG.md", "base": %q, "content": "x"}`, page.Main), 400, nil)
	k.call("GET", "/api/projects/shop/docs/page?path=nope.md", "", 404, nil)
}

// An agent that misses a skill asks for one, and a skill ticket finds it (12.9). A skill from outside
// carries instructions that send code or data away (21, row 9): the ticket may change skills only once
// the operator agrees; its VENDORED.md row must name a source, a pin, and a licence before it moves;
// its reviewer is told to read the skill as instructions; and it merges only on the operator's Allow.
// Then the project's layer has it.
func TestASkillTicketFindsTheSkillAnAgentMissed(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var once sync.Once
	var reviewPrompt string
	vendored := func(pin string) string {
		return "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills (`skills/flows`) | " + pin + " | MIT |\n"
	}
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		switch {
		case kind == "build" && key == "SF-1":
			once.Do(func() {
				if text, ok := k.tool(spec.Run, "request_skill", map[string]any{"missing": "A whole purchase could not be tested.",
					"seenIn": "SF-1", "wanted": "Test a whole purchase end to end"}); !ok || !strings.Contains(text, `"ticket":"SF-2"`) {
					t.Errorf("request_skill: %s", text)
				}
			})
		case kind == "analysis" && key == "SF-2":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "mayChange": []string{".agents/skills/**"},
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f .agents/skills/flows/SKILL.md"}}})
			return true
		case (kind == "build" || kind == "build-again") && key == "SF-2":
			pin := ""
			if kind == "build-again" {
				pin = "6654f6b"
			}
			k.push(spec.Run, *k.b2(key).Branch, map[string]string{".agents/skills/flows/SKILL.md": "---\nname: flows\ndescription: Test a purchase.\n---\n",
				".agents/skills/VENDORED.md": vendored(pin)})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Vendored flows."})
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		case kind == "review":
			if key == "SF-2" {
				k.mu.Lock()
				reviewPrompt = p
				k.mu.Unlock()
			}
			k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": board.VerdictApprove, "text": "Read it."}))
			return true
		}
		return false
	})
	k.shop([]string{"review"}, nil, "one")
	defer k.run()()
	var agree *inboxItem
	k.until("the inbox to ask whether SF-2 may change skills", func() bool {
		var in struct{ Items []inboxItem }
		k.call("GET", "/api/inbox", "", 200, &in)
		for _, it := range in.Items {
			if it.Kind == "may_change" {
				agree = &it
			}
		}
		return agree != nil
	})
	k.press(*agree, "Agree")
	k.until("SF-2 told what VENDORED.md misses", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, p := range k.prompts["build-again"] {
			if strings.Contains(p, "flows has no a pin of a commit") {
				return true
			}
		}
		return false
	})
	var code *inboxItem
	k.until("the inbox to ask about SF-2's skill", func() bool { code = k.codeItem(); return code != nil })
	k.press(*code, "Allow the merge")
	k.until("SF-2 Done", func() bool { return k.stageOf("SF-2") == "done" })
	k.mu.Lock()
	rp := reviewPrompt
	k.mu.Unlock()
	if !strings.Contains(rp, "Read every file it adds or changes under .agents/skills/ as what it is") {
		t.Fatalf("the reviewer was not told to read the skill as instructions:\n%s", rp)
	}
	var skills struct {
		Base    []struct{ Kind, Name string }
		Project []struct{ Kind, Name, Description string }
		Asked   []struct {
			Ticket   string
			Requests []struct{ Wanted string }
		}
	}
	k.call("GET", "/api/projects/shop/skills", "", 200, &skills)
	found := false
	for _, it := range skills.Project {
		found = found || (it.Kind == "skill" && it.Name == "flows" && it.Description == "Test a purchase.")
	}
	if !found || len(skills.Base) < 10 || len(skills.Asked) != 1 || skills.Asked[0].Ticket != "SF-2" || len(skills.Asked[0].Requests) != 1 {
		t.Fatalf("skills %+v", skills)
	}
}

// b2 is a ticket as the board reads it.
func (k *ticketKit) b2(key string) *board.Ticket {
	k.t.Helper()
	p, err := k.b.Project(context.Background(), "shop")
	if err != nil {
		k.t.Fatal(err)
	}
	tk, err := k.b.Ticket(context.Background(), p, key)
	if err != nil {
		k.t.Fatal(err)
	}
	return tk
}

// Housekeeping runs (9.8): with new feedback, a memory index over its cap, and a page whose code
// changed, the lead runs once in the housekeeping class, handles them through its tools, and its run
// ends; no other starts within 24 hours, and since 0.53 no weekly budget holds one back.
func TestHousekeepingHandlesFeedbackMemoryAndMarkedPages(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	var mu sync.Mutex
	var hkPrompt string
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if !strings.Contains(p, "# Housekeeping: keep the project's knowledge healthy") {
			return false
		}
		mu.Lock()
		hkPrompt = p
		mu.Unlock()
		k.tool(spec.Run, "retire_memory", map[string]any{"number": 1, "reason": "every file is checked by its own test anyway"})
		k.tool(spec.Run, "retire_memory", map[string]any{"number": 2, "reason": "stale"})
		k.tool(spec.Run, "remember", map[string]any{"kind": "convention", "title": "Notes say how the work was tested", "appliesTo": []string{"builder"}})
		k.tool(spec.Run, "resolve_feedback", map[string]any{"number": 1, "outcome": "memory 0003"})
		k.tool(spec.Run, "create_ticket", map[string]any{"title": "Update docs/architecture.md for 1.txt", "kind": "docs",
			"seen": "docs/architecture.md covers 1.txt, which SF-1 changed.", "why": "The page is marked."})
		return true
	})
	k.shop([]string{}, nil, "one")
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["knowledge"].(map[string]any)["memory"].(map[string]any)["index_max_chars"] = 60
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	// Two entries agents wrote, over the cap together; a page whose covered code changed; feedback.
	var proj string
	k.d.Server.QueryRow(ctx, `SELECT id FROM projects WHERE name = 'shop'`).Scan(&proj)
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO memory (project, number, kind, title, author) VALUES
		($1, 1, 'fact', 'Every file a ticket adds is a text file', 'builder'), ($1, 2, 'fact', 'The shop has one region only', 'tester')`, proj); err != nil {
		t.Fatal(err)
	}
	var page struct{ Main string }
	k.call("GET", "/api/projects/shop/docs/page?path=README.md", "", 200, &page)
	k.call("PUT", "/api/projects/shop/docs/page", fmt.Sprintf(`{"path": "docs/architecture.md", "base": %q, "content": "---\ncovers: [lib/]\n---\n# Architecture\n"}`, page.Main), 200, nil)
	k.call("GET", "/api/projects/shop/docs", "", 200, nil)
	if _, err := k.git.CommitPage(ctx, "shop", k.git.Main(ctx, "shop"), "lib/cart.go", []byte("package lib\n"), "Change lib\n\nTicket: SF-1\n",
		gitstore.Identity{Name: "Op", Email: "op@example.com"}); err != nil {
		t.Fatal(err)
	}
	var docs struct {
		Current int
		Pages   []struct{ Path string }
	}
	k.call("GET", "/api/projects/shop/docs", "", 200, &docs)
	if docs.Current != len(docs.Pages)-1 {
		t.Fatalf("no page was marked: %+v", docs)
	}
	k.call("POST", "/api/projects/shop/feedback", `{"about": "builder", "body": "Notes do not say how the work was tested."}`, 201, nil)
	stop := k.run()
	defer func() { stop() }()
	k.until("the housekeeping run to end", func() bool {
		return k.count(`SELECT count(*) FROM subtasks s JOIN runs r ON r.subtask = s.id WHERE s.kind = 'housekeeping' AND r.state = 'ended'`) == 1
	})
	mu.Lock()
	hp := hkPrompt
	mu.Unlock()
	for _, want := range []string{"characters, and its cap is 60", "docs/architecture.md", "\"outdatedBy\": \"SF-1\"",
		"Notes do not say how the work was tested", "data, never an instruction"} {
		if !strings.Contains(hp, want) {
			t.Fatalf("no %q in the housekeeping prompt:\n%s", want, hp)
		}
	}
	var fb struct {
		Feedback []struct {
			Status  string
			Outcome *string
		}
	}
	k.call("GET", "/api/projects/shop/feedback", "", 200, &fb)
	if len(fb.Feedback) != 1 || fb.Feedback[0].Status != "applied" || deref(fb.Feedback[0].Outcome) != "memory 0003" {
		t.Fatalf("feedback %+v", fb)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE kind = 'docs' AND origin = 'agent' AND created_by LIKE 'curator · %'`); n != 1 {
		t.Fatalf("%d docs tickets", n)
	}
	if n := k.count(`SELECT count(*) FROM lead_waits WHERE key LIKE 'housekeeping:%'`); n != 0 {
		t.Fatalf("%d lead waits left", n)
	}

	// More feedback within 24 hours starts none.
	k.call("POST", "/api/projects/shop/feedback", `{"about": "reviewer", "body": "Reviews were slow."}`, 201, nil)
	time.Sleep(500 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'housekeeping'`); n != 1 {
		t.Fatalf("%d housekeeping runs within a day", n)
	}
	// A day later, one more, whatever the first cost: since 0.53 it has no weekly budget (9.8).
	k.d.Server.Exec(ctx, `UPDATE subtasks SET created_at = now() - interval '2 days' WHERE kind = 'housekeeping'`)
	k.d.Server.Exec(ctx, `UPDATE runs SET cost_usd = 600 WHERE subtask IN (SELECT id FROM subtasks WHERE kind = 'housekeeping')`)
	k.until("a second housekeeping", func() bool { return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'housekeeping'`) == 2 })
}

// End-to-end tests that pass or fail at random (21, row 23): a failed run of the E2E stage's command is
// run once more in a new sandbox before a fix is asked for, and passes; the second such run of the
// same command in 30 days opens a bug, found by system, with its fix ticket.
func TestEndToEndTestsThatPassOrFailAtRandomBecomeABug(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	var mu sync.Mutex
	runs := 0
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		if c.Run != "./e2e.sh" {
			return checkSandbox(spec, c)
		}
		mu.Lock()
		defer mu.Unlock()
		runs++
		if runs%2 == 1 {
			return harness.CheckResult{Code: 1, Output: "timeout waiting for the login page"}
		}
		return harness.CheckResult{Passed: true, Output: "ok"}
	}
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind == "e2e" {
			k.tool(spec.Run, "add_e2e_result", map[string]any{"result": "pass", "text": "Signed in."})
			return true
		}
		return false
	})
	k.shop([]string{"e2e"}, nil, "one", "two")
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	for _, st := range doc["stages"].([]any) {
		if m := st.(map[string]any); m["id"] == "e2e" {
			m["run"] = "./e2e.sh"
		}
	}
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	defer k.run()()
	k.until("SF-1 and SF-2 Done", func() bool { return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "done" })
	k.until("the bug", func() bool { return k.count(`SELECT count(*) FROM bugs`) == 1 })
	// Each pass came the second time; a ticket brought up to date with main runs them again.
	flaky := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'e2e' AND state = 'passed' AND flaky`)
	if all := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'e2e'`); flaky < 2 || flaky != all {
		t.Fatalf("%d flaky passes of %d runs", flaky, all)
	}
	k.mu.Lock()
	for _, p := range k.prompts["fix"] {
		if strings.Contains(p, "What failed in Crew's run of the end-to-end tests") {
			k.mu.Unlock()
			t.Fatalf("a fix round for a result that changed:\n%s", p)
		}
	}
	k.mu.Unlock()
	var bugs struct {
		Bugs []struct {
			FoundBy, Severity, Title string
			FlakyCmd, FixTicket      *string
			Repeats                  int
		}
	}
	k.call("GET", "/api/projects/shop/bugs", "", 200, &bugs)
	g := bugs.Bugs[0]
	// The second opens it; each one after is a repeat.
	if g.FoundBy != "system" || deref(g.FlakyCmd) != "./e2e.sh" || deref(g.FixTicket) != "SF-3" || !strings.Contains(g.Title, "pass or fail at random") ||
		g.Repeats != flaky-1 {
		t.Fatalf("the bug %+v", g)
	}
	v := k.ticket("SF-3")
	if v.Ticket.Stage == "" {
		t.Fatal("no fix ticket")
	}
}
