package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/kesokaj/crew/internal/httpapi"
)

// Agents create tickets (7.5, R27): a lead in Analysis and a builder each see work outside their own
// ticket and call create_ticket. Each new ticket is in the Backlog with what its creator saw, marked as
// created by an agent with the role and the run, and linked to the ticket that run worked on. Another
// agent analyses it (7.6): a reviewer for the lead's, the lead for the builder's, never the creating
// run; its analysis reads what the creator saw, and gives notice of a new feature.
func TestAgentsCreateTicketsThatAnotherAgentAnalyses(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	var mu sync.Mutex
	creators := map[string]string{} // the run that created each new ticket, by its title
	created := map[string]bool{}
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		create := func(title string) {
			mu.Lock()
			defer mu.Unlock()
			if created[title] {
				return
			}
			created[title] = true
			text, ok := k.tool(spec.Run, "create_ticket", map[string]any{"title": title, "kind": "feature",
				"seen":   "search_test.go:12 fails: Search(\"Apple\") finds nothing when the name is apple.",
				"why":    "Customers type names in any case.",
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f search.txt"}}})
			if !ok || !strings.Contains(text, "Another agent analyses it") {
				t.Errorf("create_ticket: %s", text)
			}
			creators[title] = spec.Run
		}
		switch {
		case kind == "analysis" && key == "SF-1":
			create("Search ignores case")
			return false
		case kind == "build" && key == "SF-1":
			create("Search by part of a name")
			return false
		case kind == "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "notice", "reason": "The plan has no search by name.",
				"what": "Search names in any case.", "goal": "Search(\"Apple\") finds apple.",
				"approach": "Lower the case of both sides.", "docsReason": "none",
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f search.txt"}}})
			return true
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("both new tickets noticed as new features", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE origin = 'agent' AND analysis->>'status' = 'new_feature_notice'`) == 2
	})
	for _, c := range []struct{ key, title, role, analyser string }{
		{"SF-2", "Search ignores case", "curator", "reviewer"},
		{"SF-3", "Search by part of a name", "builder", "curator"},
	} {
		var title, createdBy, seen, analysedBy string
		var hours float64
		if err := k.d.Server.QueryRow(ctx, `SELECT title, created_by, seen, analysis->>'decidedBy',
				extract(epoch FROM notice_until - now()) / 3600 FROM tickets WHERE number = $1`, c.key[3:]).Scan(&title, &createdBy, &seen, &analysedBy, &hours); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		creator := creators[c.title]
		mu.Unlock()
		if title != c.title || createdBy != c.role+" · "+creator || !strings.Contains(seen, "search_test.go:12") {
			t.Fatalf("%s: %q by %q, seen %q", c.key, title, createdBy, seen)
		}
		if n := k.count(`SELECT count(*) FROM ticket_links l JOIN tickets f ON f.id = l.from_ticket JOIN tickets t ON t.id = l.to_ticket
			WHERE f.number = $1 AND t.number = 1 AND l.kind = 'created_from'`, c.key[3:]); n != 1 {
			t.Fatalf("%s is not linked to the ticket it was created from", c.key)
		}
		var role, run string
		if err := k.d.Server.QueryRow(ctx, `SELECT s.role, r.id::text FROM subtasks s JOIN tickets t ON t.id = s.ticket JOIN runs r ON r.subtask = s.id
			WHERE t.number = $1 AND s.kind = 'analysis' ORDER BY r.number LIMIT 1`, c.key[3:]).Scan(&role, &run); err != nil {
			t.Fatal(err)
		}
		if role != c.analyser || run == creator || !strings.HasPrefix(analysedBy, c.analyser) {
			t.Fatalf("%s was analysed by the %s in %s (%q); its creator ran %s", c.key, role, run, analysedBy, creator)
		}
		if hours < 23.5 || hours > 24.01 {
			t.Fatalf("%s goes ahead in %.2f hours, not 24", c.key, hours)
		}
	}
	// The analysis read what the creator saw, and where it came from.
	k.mu.Lock()
	var read bool
	for _, p := range k.prompts["analysis"] {
		read = read || (strings.Contains(p, "SF-3 Search by part of a name") && strings.Contains(p, "Seen by its creator: search_test.go:12") &&
			strings.Contains(p, "Created while working on: SF-1"))
	}
	k.mu.Unlock()
	if !read {
		t.Fatal("the analysis of SF-3 was not given what its creator saw")
	}
	if n := k.count(`SELECT count(*) FROM mcp_calls WHERE tool = 'create_ticket' AND ok`); n != 2 {
		t.Fatalf("%d calls of create_ticket recorded", n)
	}
}

// inboxItem is an item of the inbox as the page reads it.
type inboxItem struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Alternatives []struct {
		Label  string         `json:"label"`
		Action string         `json:"action"`
		Body   map[string]any `json:"body"`
		Link   string         `json:"link"`
	} `json:"alternatives"`
}

// features are the new-feature items of the inbox.
func (k *ticketKit) features() []inboxItem {
	k.t.Helper()
	var in struct{ Items []inboxItem }
	k.call("GET", "/api/inbox", "", 200, &in)
	var out []inboxItem
	for _, it := range in.Items {
		if it.Kind == "new_feature" {
			out = append(out, it)
		}
	}
	return out
}

// press sends the alternative of an item by its label, as the inbox page does.
func (k *ticketKit) press(it inboxItem, label string) {
	k.t.Helper()
	for _, a := range it.Alternatives {
		if a.Label == label {
			b, _ := json.Marshal(a.Body)
			k.call("POST", a.Action, string(b), 200, nil)
			return
		}
	}
	k.t.Fatalf("%s has no %q: %+v", it.ID, label, it.Alternatives)
}

// A new feature goes ahead after its notice, shown in the app (7.6, 7.3): the inbox has it with Open,
// Approve now, Discard, and Hold; a hold stops its clock and Lift the hold starts it again with what
// was left; when nobody objected it goes ahead by itself, and the news says so. Approve now and Discard
// end a notice at once, and with approvals.new_feature set to operator a notice has no clock.
func TestANewFeatureGoesAheadAfterItsNotice(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	k.b.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	later := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	k.shop([]string{}, nil, "one")
	p, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	notice := func(title string) string {
		t.Helper()
		tk, err := k.b.AddTicket(ctx, "shop", board.NewTicket{Title: title, Why: "Customers ask.", What: "Add " + title + ".", Goal: title + " works.",
			Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f x.txt"}}}, board.OriginAgent, "builder · 00000000-0000-4000-8000-0000000000d1")
		if err != nil {
			t.Fatal(err)
		}
		if err := k.b.Reconcile(ctx, "shop"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.b.Decide(ctx, p, tk.Key, board.Decision{Choice: "notice", Reason: "The plan has no export."}, "lead · r_1"); err != nil {
			t.Fatal(err)
		}
		return tk.Key
	}
	key := notice("Export")
	items := k.features()
	if len(items) != 1 || items[0].Title != "New feature: "+key+" Export" || !strings.Contains(items[0].Body, "Created by the builder.") ||
		!strings.Contains(items[0].Body, "goes ahead by itself at") {
		t.Fatalf("the inbox: %+v", items)
	}
	var labels []string
	for _, a := range items[0].Alternatives {
		labels = append(labels, a.Label)
	}
	if strings.Join(labels, ", ") != "Open, Approve now, Discard, Hold" {
		t.Fatalf("its alternatives: %v", labels)
	}
	// Held for 20 hours, nothing goes ahead; let go, the 24 hours go on from where they stopped.
	later(4 * time.Hour)
	k.press(items[0], "Hold")
	held := k.features()
	if len(held) != 1 || !strings.Contains(held[0].Body, "On hold") || held[0].Alternatives[3].Label != "Lift the hold" {
		t.Fatalf("held: %+v", held)
	}
	later(20 * time.Hour)
	k.b.Reconcile(ctx, "shop")
	if len(k.features()) != 1 {
		t.Fatal("a held new feature went ahead")
	}
	k.press(held[0], "Lift the hold")
	later(19 * time.Hour)
	k.b.Reconcile(ctx, "shop")
	if len(k.features()) != 1 {
		t.Fatal("it went ahead before its 24 hours")
	}
	later(90 * time.Minute)
	k.b.Reconcile(ctx, "shop")
	if len(k.features()) != 0 || k.count(`SELECT count(*) FROM tickets WHERE number = $1 AND analysis->>'status' = 'ready'`, key[3:]) != 1 {
		t.Fatalf("it did not go ahead: %+v", k.features())
	}
	var lines []struct{ Kind, Text string }
	k.call("GET", "/api/projects/shop/activity", "", 200, &struct {
		Lines *[]struct{ Kind, Text string }
	}{&lines})
	var news []string
	for _, l := range lines {
		if l.Kind == "news" && strings.Contains(l.Text, key) {
			news = append(news, l.Text)
		}
	}
	if len(news) != 2 || !strings.Contains(news[0], "went ahead") || !strings.Contains(news[1], "is a new feature") {
		t.Fatalf("the news: %q", news)
	}
	// Approve now and Discard end a notice at once.
	approved, discarded := notice("Import"), notice("Print")
	for _, it := range k.features() {
		switch {
		case strings.Contains(it.Title, approved):
			k.press(it, "Approve now")
		case strings.Contains(it.Title, discarded):
			k.press(it, "Discard")
		}
	}
	if len(k.features()) != 0 || k.ticket(discarded).Ticket.Stage != "closed" ||
		k.count(`SELECT count(*) FROM tickets WHERE number = $1 AND analysis->>'status' = 'ready' AND analysis->>'decidedBy' = 'operator'`, approved[3:]) != 1 {
		t.Fatalf("after Approve now and Discard: %+v", k.features())
	}
	// With approvals.new_feature: operator, a notice waits for the operator with no clock.
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["approvals"].(map[string]any)["new_feature"] = "operator"
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	waits := notice("Search")
	later(100 * time.Hour)
	k.b.Reconcile(ctx, "shop")
	if items := k.features(); len(items) != 1 || !strings.Contains(items[0].Title, waits) || !strings.Contains(items[0].Body, "It waits for you") {
		t.Fatalf("a notice with no clock: %+v", items)
	}
}

// A ticket an agent created may change files that instruct agents only once the operator agreed
// (13.8): its analysis names them, it waits in Analysis, and the inbox asks. Agree lets it go on with
// them, Refuse lets it go on without them. Agree asks for a fresh sign-in, as taking a held main does
// (rule 13); Refuse does not. A ticket of the plan needs no yes, and a new list after an agreement
// needs a new one.
func TestAnAgentsTicketChangesInstructionsOnlyWhenTheOperatorAgrees(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.shop([]string{}, map[string]any{}, "one")
	p, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, origin string) string {
		t.Helper()
		tk, err := k.b.AddTicket(ctx, "shop", board.NewTicket{Title: title, Why: "w", What: "w", Goal: "g",
			Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f x.txt"}}}, origin, "builder · 00000000-0000-4000-8000-0000000000d2")
		if err != nil {
			t.Fatal(err)
		}
		return tk.Key
	}
	agree, refuse, plan := add("Rules for agents", board.OriginAgent), add("More rules", board.OriginAgent), "SF-1"
	// Analysis has room for two: the plan's ticket goes on first, and makes room.
	w := "w"
	for _, key := range []string{plan, agree, refuse} {
		if err := k.b.Reconcile(ctx, "shop"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.b.Decide(ctx, p, key, board.Decision{Choice: "approve", Why: &w, MayChange: []string{"AGENTS.md"}}, "curator · r_1"); err != nil {
			t.Fatal(err)
		}
	}
	k.b.Reconcile(ctx, "shop")
	items := func() map[string]inboxItem {
		var in struct{ Items []inboxItem }
		k.call("GET", "/api/inbox", "", 200, &in)
		out := map[string]inboxItem{}
		for _, it := range in.Items {
			if it.Kind == "may_change" {
				out[strings.Fields(it.Title)[0]] = it
			}
		}
		return out
	}
	got := items()
	if len(got) != 2 || !strings.Contains(got[agree].Body, "AGENTS.md") || got[plan].ID != "" {
		t.Fatalf("the inbox: %+v", got)
	}
	for _, key := range []string{agree, refuse} {
		if v := k.ticket(key); v.Ticket.Stage != "analysis" || !strings.Contains(deref(v.Ticket.Waiting), "waits for you to agree") {
			t.Fatalf("%s: %s, %q", key, v.Ticket.Stage, deref(v.Ticket.Waiting))
		}
	}
	if k.stageOf(plan) == "analysis" {
		t.Fatal("a ticket of the plan waited for a yes")
	}
	agreeAt := func(h map[string]string) (int, string) {
		return do(t, k.srv, "POST", "/api/projects/shop/tasks/"+agree+"/may-change", h, `{"choice": "agree"}`)
	}
	if code, body := agreeAt(signedInAt(stale, "")); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("agree, stale: %d %s", code, body)
	}
	if code, body := agreeAt(with(map[string]string{"Origin": "https://" + host, httpapi.HeaderAuthTime: ""})); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("agree, no time of a sign-in: %d %s", code, body)
	}
	if v := k.ticket(agree); v.Ticket.Stage != "analysis" {
		t.Fatalf("a stale agree moved %s: %s", agree, v.Ticket.Stage)
	}
	if code, body := agreeAt(signedInAt(time.Minute, "")); code != 200 {
		t.Fatalf("agree, fresh: %d %s", code, body)
	}
	k.press(got[refuse], "Refuse")
	k.b.Reconcile(ctx, "shop")
	if len(items()) != 0 || k.stageOf(agree) == "analysis" || k.stageOf(refuse) == "analysis" {
		t.Fatalf("after the answers: %s %s, %+v", k.stageOf(agree), k.stageOf(refuse), items())
	}
	var agreed, refused board.Analysis
	k.d.Server.QueryRow(ctx, `SELECT analysis FROM tickets WHERE number = $1`, agree[3:]).Scan(&agreed)
	k.d.Server.QueryRow(ctx, `SELECT analysis FROM tickets WHERE number = $1`, refuse[3:]).Scan(&refused)
	if !agreed.MayChangeAgreed || len(agreed.MayChange) != 1 || len(refused.MayChange) != 0 {
		t.Fatalf("agreed %+v, refused %+v", agreed, refused)
	}
	// Answering again is refused: nothing waits.
	k.call("POST", "/api/projects/shop/tasks/"+agree+"/may-change", `{"choice": "agree"}`, 400, nil)
}

// codeItem is the inbox's item for a ticket whose files that are code wait, or nil.
func (k *ticketKit) codeItem() *inboxItem {
	var in struct{ Items []inboxItem }
	k.call("GET", "/api/inbox", "", 200, &in)
	for _, it := range in.Items {
		if it.Kind == "code_change" {
			return &it
		}
	}
	return nil
}

// A ticket whose branch changes files that are code for the agent CLI never merges by itself (13.8):
// the inbox has it with its diff. Refuse sends that head back to the builder in place; the head the
// builder pushes then asks again; Allow merges it. An answer about a head that is no longer the one
// waiting is refused.
func TestFilesThatAreCodeMergeOnlyWhenTheOperatorAllows(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind != "build" {
			return false
		}
		k.push(spec.Run, "task/SF-1-one", map[string]string{"1.txt": "one", ".agents/hooks.json": "{}"})
		k.tool(spec.Run, "add_note", map[string]any{"text": "Built, with a hook"})
		k.tool(spec.Run, "request_move", map[string]any{})
		return true
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	var first *inboxItem
	// The item comes with the head's code_wait, and the ticket says why it waits a moment after.
	k.until("the inbox to ask about SF-1's code, and SF-1 to say it waits", func() bool {
		first = k.codeItem()
		return first != nil && strings.Contains(deref(k.ticket("SF-1").Ticket.Stuck), "waits for your answer in the inbox")
	})
	if !strings.Contains(first.Body, ".agents/hooks.json") || !strings.Contains(first.Alternatives[0].Link, "/p/shop/git?from=") {
		t.Fatalf("the item: %+v", first)
	}
	// Refuse works without a fresh sign-in; Allow requires a fresh sign-in.
	for _, a := range first.Alternatives {
		if a.Label == "Refuse" {
			rb, _ := json.Marshal(a.Body)
			if code, body := do(t, k.srv, "POST", a.Action, signedInAt(stale, ""), string(rb)); code != 200 {
				t.Fatalf("stale Refuse = %d %s", code, body)
			}
		}
	}
	k.until("the builder's fix round", func() bool { return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'fix'`) == 1 })
	told := func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, ps := range k.prompts {
			for _, p := range ps {
				if strings.Contains(p, "The operator refused the change to these files") && strings.Contains(p, ".agents/hooks.json") {
					return true
				}
			}
		}
		return false
	}
	k.until("the builder to be told what the operator refused", told)
	// The fix keeps the hook: its new head asks again, and the old one cannot be answered.
	var second *inboxItem
	k.until("the inbox to ask about the new head", func() bool {
		second = k.codeItem()
		return second != nil && second.ID != first.ID
	})
	b, _ := json.Marshal(first.Alternatives[2].Body)
	k.call("POST", first.Alternatives[2].Action, string(b), 400, nil)
	if k.stageOf("SF-1") != "in_progress" {
		t.Fatal("SF-1 moved before an answer")
	}
	for _, a := range second.Alternatives {
		if a.Label == "Allow the merge" {
			ab, _ := json.Marshal(a.Body)
			if code, body := do(t, k.srv, "POST", a.Action, signedInAt(stale, ""), string(ab)); code != 401 || reason(body) != httpapi.ReasonFresh {
				t.Fatalf("stale Allow = %d %s, want 401 %s", code, body, httpapi.ReasonFresh)
			}
		}
	}
	if k.stageOf("SF-1") != "in_progress" {
		t.Fatal("SF-1 moved on stale Allow")
	}
	k.press(*second, "Allow the merge")
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	if k.codeItem() != nil {
		t.Fatal("the item stayed after the merge")
	}
}

// When In progress is the last stage, Crew runs the ticket's checks on a head a fix round pushed before
// it merges; a run that ends as Crew's own error is tried again, and after three on that head the
// ticket waits instead of merging (merge.go, an M6 path with no test of its own until M9).
func TestTheChecksOfAFixedHeadAreTriedThreeTimes(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		for _, n := range tarNames(spec.Tree) {
			if strings.HasSuffix(n, ".fixed") && len(spec.Cmds) == 1 {
				return harness.CheckResult{Code: 124, Error: "the check sandbox did not start"}
			}
		}
		return checkSandbox(spec, c)
	}
	// Two tickets that change one file: the second merge conflicts, and its fix is a new head.
	k.shop([]string{}, nil, "one", "two")
	defer k.run()()
	var fixed string
	k.until("a ticket to wait after three errors", func() bool {
		for _, key := range []string{"SF-1", "SF-2"} {
			if strings.Contains(deref(k.ticket(key).Ticket.Stuck), "Crew could not run its checks on") &&
				strings.Contains(deref(k.ticket(key).Ticket.Stuck), "three times") {
				fixed = key
				return true
			}
		}
		return false
	})
	if n := k.count(`SELECT count(*) FROM check_runs c JOIN tickets t ON t.id = c.ticket WHERE t.number = $1 AND c.purpose = 'exit'
		AND c.state = 'error'`, fixed[3:]); n != 3 {
		t.Fatalf("%s: %d check runs ended as errors, not 3", fixed, n)
	}
	time.Sleep(300 * time.Millisecond)
	if k.stageOf(fixed) == "done" || k.count(`SELECT count(*) FROM check_runs c JOIN tickets t ON t.id = c.ticket WHERE t.number = $1
		AND c.purpose = 'exit' AND c.state = 'error'`, fixed[3:]) != 3 {
		t.Fatalf("%s merged, or its checks were tried a fourth time", fixed)
	}
}

// A decision whose command checks cannot be run on main is tried three times, then its run ends as a
// stop of Crew's own sandbox (analysis.go, an M6 path whose number had no test of its own until M9).
// The three are counted for the subtask: its retry, a second run of the same subtask, has one try
// left; a second analysis, a new subtask, has three again, and after it the ticket waits with the
// decision not applied: 3 + 1 + 3 + 1 tries in all.
func TestTheBaseCheckIsTriedThreeTimesARun(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.f.CheckErr = errors.New("the check sandbox does not start")
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("the analysis to fail twice, and the lead to look again", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again' AND title = 'Look again: its analysis failed twice'`) > 0
	})
	if n := k.count(`SELECT count(*) FROM check_runs c WHERE c.purpose = 'base' AND c.state = 'error' AND c.started_at <
		(SELECT r.started_at FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'analysis' ORDER BY r.started_at OFFSET 1 LIMIT 1)`); n != 3 {
		t.Fatalf("the first analysis run had %d tries, not 3", n)
	}
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'base' AND state = 'error'`); n != 8 {
		t.Fatalf("%d base check runs ended as errors, not 3 + 1 for each of the two analyses", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'analysis' AND state = 'stopped'`); n != 2 {
		t.Fatalf("%d analyses stopped, not 2", n)
	}
	if k.stageOf("SF-1") != "analysis" || k.count(`SELECT count(*) FROM tickets WHERE analysis->>'status' = 'ready'`) != 0 {
		t.Fatal("the decision was applied")
	}
}

var partKey = regexp.MustCompile(`You work on one subtask of the ticket, (SF-\d+\.\d+)\.`)

// partKit plays a lead that plans SF-1's build in parts with create_subtask, and the parts' builders:
// each pushes the files part gives for it to its own branch, writes a note, and says subtask_done.
func (k *ticketKit) partKit(t *testing.T, plan []map[string]any, files map[string]map[string]string) *sync.Map {
	seen := &sync.Map{} // a part's key, with the branch its run worked on
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind == "analysis" && key == "SF-1" {
			for _, part := range plan {
				if text, ok := k.tool(spec.Run, "create_subtask", part); !ok {
					t.Errorf("create_subtask: %s", text)
				}
			}
			checks := []map[string]any{{"kind": "cmd", "spec": "test -f c.txt"}}
			if k.partChecks != nil {
				checks = k.partChecks
			}
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "checks": checks})
			return true
		}
		m := partKey.FindStringSubmatch(p)
		if m == nil || !strings.Contains(p, "# A subtask: one part of a ticket") {
			return false
		}
		seen.Store(m[1], spec.Branch)
		k.push(spec.Run, spec.Branch, files[m[1]])
		k.tool(spec.Run, "add_note", map[string]any{"text": "Did " + m[1]})
		k.tool(spec.Run, "subtask_done", map[string]any{})
		return true
	})
	return seen
}

// Agents create subtasks on their own ticket, and those that do not depend on each other run in
// parallel on their own branches and are joined by the backend (7.5, 7.11; the done-when of M9): the
// lead plans SF-1 in three parts. Two that name different files run at the same time; the third comes
// after the first and starts only once that one is joined. Each part's check runs on its branch and
// again on its join. After the last join the ticket's checks run, and SF-1 merges with all of it.
func TestSubtasksRunInParallelAndAreJoined(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	plan := []map[string]any{
		{"title": "The API", "what": "Add a/a.txt.", "doneWhen": "a/a.txt exists.", "role": "builder", "touches": []string{"a/"}, "check": "test -f a/a.txt"},
		{"title": "The page", "what": "Add b/b.txt.", "doneWhen": "b/b.txt exists.", "role": "builder", "touches": []string{"b/"}, "check": "test -f b/b.txt"},
		{"title": "The wiring", "what": "Add c.txt.", "doneWhen": "c.txt exists.", "role": "builder", "touches": []string{"c.txt"}, "after": []int{2}},
	}
	files := map[string]map[string]string{"SF-1.2": {"a/a.txt": "a"}, "SF-1.3": {"b/b.txt": "b"}, "SF-1.4": {"c.txt": "c"}}
	seen := k.partKit(t, plan, files)
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	for key, branch := range map[string]string{"SF-1.2": "sub/SF-1.2", "SF-1.3": "sub/SF-1.3", "SF-1.4": "sub/SF-1.4"} {
		if b, ok := seen.Load(key); !ok || b != branch {
			t.Errorf("%s ran on %v, not %s", key, b, branch)
		}
	}
	type row struct {
		number          int
		state, created  string
		started, joined *time.Time
	}
	rows, err := k.d.Server.Query(ctx, `SELECT s.number, s.state, s.created_by, (SELECT min(started_at) FROM runs WHERE subtask = s.id), s.joined_at
		FROM subtasks s WHERE s.kind = 'part' ORDER BY s.number`)
	if err != nil {
		t.Fatal(err)
	}
	var parts []row
	for rows.Next() {
		var r row
		rows.Scan(&r.number, &r.state, &r.created, &r.started, &r.joined)
		parts = append(parts, r)
	}
	rows.Close()
	if len(parts) != 3 {
		t.Fatalf("parts: %+v", parts)
	}
	for _, r := range parts {
		if r.state != "done" || r.joined == nil || !strings.HasPrefix(r.created, "curator · ") {
			t.Fatalf("a part: %+v", r)
		}
	}
	// The first two were running at once: the second's run started before the first's ended.
	if n := k.count(`SELECT count(*) FROM runs a JOIN subtasks sa ON sa.id = a.subtask, runs b JOIN subtasks sb ON sb.id = b.subtask
		WHERE sa.kind = 'part' AND sa.number = 2 AND sb.kind = 'part' AND sb.number = 3 AND b.started_at < a.ended_at`); n != 1 {
		t.Fatal("the two parts that name different files did not run at the same time")
	}
	if !parts[2].started.After(*parts[0].joined) {
		t.Fatal("the part that comes after the first started before the first was joined")
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'join' AND role = 'crew' AND title LIKE 'Join SF-1.%'`); n != 3 {
		t.Fatalf("%d joins recorded", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'build'`); n != 0 {
		t.Fatal("a builder of the whole ticket ran beside its parts")
	}
	// Each check ran once: on the part's branch, and on its join, also a join that is a merge commit.
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'part'`); n != 2 {
		t.Fatalf("%d checks of a part ran on its branch, not 2", n)
	}
	if n := k.count(`SELECT count(*) FROM check_runs WHERE purpose = 'join'`); n != 2 {
		t.Fatalf("%d checks of a part ran on its join, not 2", n)
	}
	if n := k.count(`SELECT count(*) FROM check_runs c WHERE purpose = 'join' AND state = 'passed'
		AND (SELECT count(*) FROM regexp_matches(c.commit, '.', 'g')) = 40 AND c.commit <> c.head`); n < 1 {
		t.Fatal("no join was a merge commit")
	}
	main := k.git.Main(ctx, "shop")
	for _, f := range []string{"a/a.txt", "b/b.txt", "c.txt"} {
		if mainFile(k.git, ctx, f) == nil {
			t.Fatalf("main %s lacks %s", main, f)
		}
	}
	// The task view says who created each part, in which subtask, where it works, and that it joined.
	var subs struct {
		Subtasks []struct {
			ID, Kind, CreatedBy string
			CreatedIn, Branch   *string
			Touches             []string
			JoinedAt            *time.Time
			Added               bool
		}
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/subtasks", "", 200, &subs)
	var part2 bool
	for _, s := range subs.Subtasks {
		if s.ID == "SF-1.2" {
			part2 = s.Kind == "part" && strings.HasPrefix(s.CreatedBy, "curator · ") && s.CreatedIn != nil && *s.CreatedIn == "SF-1.1" &&
				s.Branch != nil && *s.Branch == "sub/SF-1.2" && len(s.Touches) == 1 && s.Touches[0] == "a/" && s.JoinedAt != nil && s.Added
		}
	}
	if !part2 {
		t.Fatalf("the task view's subtasks: %+v", subs.Subtasks)
	}
	// The Crew MCP screen counts them, lists them, and the calls of create_subtask.
	var screen struct {
		Tools []struct {
			Name  string
			Calls int
		}
		Calls []struct {
			Tool    string
			Subtask *string
		}
		Created []struct{ Key, What, State string }
		Stats   struct{ Subtasks, Done int }
	}
	k.call("GET", "/api/crew/mcp", "", 200, &screen)
	if screen.Stats.Subtasks != 3 || screen.Stats.Done != 3 || len(screen.Created) != 3 || screen.Created[0].What != "subtask" ||
		screen.Created[0].State != "joined" {
		t.Fatalf("the screen's counts and list: %+v %+v", screen.Stats, screen.Created)
	}
	calls := 0
	for _, tl := range screen.Tools {
		if tl.Name == "create_subtask" {
			calls = tl.Calls
		}
	}
	var fromPart bool
	for _, c := range screen.Calls {
		fromPart = fromPart || (c.Tool == "subtask_done" && c.Subtask != nil && strings.HasPrefix(*c.Subtask, "SF-1."))
	}
	if calls != 3 || !fromPart {
		t.Fatalf("create_subtask was called %d times; a call of subtask_done names its subtask: %v", calls, fromPart)
	}
}

// joinBranch merges a part's branch into the ticket's branch, keeping the ticket's side of a
// conflict, and pushes it with more files: what a builder that joins the work does.
func (k *ticketKit) joinBranch(branch, part string, files map[string]string) {
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
	if part != "" {
		run("fetch", "-q", "origin", part)
		run("merge", "-q", "-X", "ours", "FETCH_HEAD", "-m", "join "+part)
	}
	for f, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, f)), 0o755)
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "joined")
	run("push", "-q", "origin", branch)
}

// Parallel work that does not join by itself gets one builder that joins it (7.11, step 5): two parts
// that changed one file conflict when the second joins, and a builder merges it on the ticket's
// branch; then the ticket's checks fail on the joined work, and a second builder fixes that. Neither
// counts toward max_added_subtasks, and SF-1 merges.
func TestWorkThatDoesNotJoinGetsABuilder(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	plan := []map[string]any{
		{"title": "One side", "what": "Add a/a.txt.", "doneWhen": "a/a.txt exists.", "role": "builder", "touches": []string{"a/"}},
		{"title": "The other side", "what": "Add b/b.txt.", "doneWhen": "b/b.txt exists.", "role": "builder", "touches": []string{"b/"}},
	}
	// Both write x.txt, which neither names: the kit pushes past the git endpoint's test, as a part
	// that conflicts with the ticket's own branch would.
	files := map[string]map[string]string{"SF-1.2": {"a/a.txt": "a", "x.txt": "one\n"}, "SF-1.3": {"b/b.txt": "b", "x.txt": "two\n"}}
	k.partKit(t, plan, files)
	var mu sync.Mutex
	var joins []string
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if !strings.Contains(p, "# Join the parallel work of a ticket") {
			return prev(kind, spec, p)
		}
		mu.Lock()
		joins = append(joins, p)
		mu.Unlock()
		if strings.Contains(p, "conflicts in: x.txt") {
			part := regexp.MustCompile(`Merging (sub/SF-1\.\d+) into`).FindStringSubmatch(p)
			k.joinBranch(spec.Branch, part[1], nil)
		} else {
			k.joinBranch(spec.Branch, "", map[string]string{"c.txt": "c"})
		}
		k.tool(spec.Run, "add_note", map[string]any{"text": "Joined"})
		k.tool(spec.Run, "subtask_done", map[string]any{})
		return recording(spec), true
	}
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	mu.Lock()
	defer mu.Unlock()
	if len(joins) != 2 || !strings.Contains(joins[0], "conflicts in: x.txt") || !strings.Contains(joins[1], "The ticket's checks fail on its branch after the last join") {
		t.Fatalf("the joins by hand: %d", len(joins))
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'join' AND role = 'builder' AND NOT added AND state = 'done'`); n != 2 {
		t.Fatalf("%d builders joined, not 2", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'part' AND joined_at IS NOT NULL`); n != 2 {
		t.Fatalf("%d parts joined", n)
	}
}

// A builder splits a part off its own ticket while it builds (7.5): the part runs on its own branch
// beside it, the builder's request for a move does not move the ticket while the part is not joined,
// and the ticket merges with both once the part is joined and the ticket's checks pass on the result.
func TestABuilderCreatesASubtaskOnItsOwnTicket(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		switch {
		case kind == "analysis":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none",
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f page/p.txt"}, {"kind": "cmd", "spec": "test -f 1.txt"}}})
			return true
		case kind == "build":
			if text, ok := k.tool(spec.Run, "create_subtask", map[string]any{"title": "The page", "what": "Add page/p.txt.",
				"doneWhen": "page/p.txt exists.", "role": "builder", "touches": []string{"page/"}}); !ok || !strings.Contains(text, "SF-1.3") {
				t.Errorf("create_subtask: %s", text)
			}
			k.push(spec.Run, "task/SF-1-one", map[string]string{"1.txt": "one"})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built mine; the page is SF-1.3"})
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		}
		if m := partKey.FindStringSubmatch(p); m != nil {
			k.push(spec.Run, spec.Branch, map[string]string{"page/p.txt": "p"})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Did " + m[1]})
			k.tool(spec.Run, "subtask_done", map[string]any{})
			return true
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	ctx := context.Background()
	for _, f := range []string{"1.txt", "page/p.txt"} {
		if mainFile(k.git, ctx, f) == nil {
			t.Fatalf("main lacks %s", f)
		}
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'part' AND number = 3 AND added AND created_by LIKE 'builder · %' AND joined_at IS NOT NULL`); n != 1 {
		t.Fatal("the builder's part was not recorded as added by it, and joined")
	}
}

// outsideCall calls the Crew MCP of the operator's own tools at /mcp of the operator's door, as a tool
// on a laptop does: with a bearer token and no sign-in. It returns the HTTP status, the text, and
// whether the call was refused.
func (k *ticketKit) outsideCall(token, method string, params any) (int, string, bool) {
	k.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, k.srv.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	var res struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if json.Unmarshal(out.Result, &res) == nil && len(res.Content) > 0 {
		return resp.StatusCode, res.Content[0].Text, res.IsError
	}
	return resp.StatusCode, string(out.Result), false
}

// The operator's own tools use the Crew MCP with a personal token (11.7; the done-when of M9): a
// token made in the app is shown once; at /mcp of the operator's door, with no sign-in, it lists its
// projects, reads their tickets, creates a ticket marked as made through the MCP, and comments, and
// reaches no other project; it answers a new feature only with may answer; every call is recorded with
// it. A run's token does not work there, and the personal token does not work at a sandbox's door.
func TestTheOperatorsToolsUseTheMCPWithAPersonalToken(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.shop([]string{}, nil, "one")
	k.call("POST", "/api/projects", `{"name": "other", "key": "OT", "plan": "## Goal\n\nOther.\n", "repository": {"kind": "local"}}`, 201, nil)
	var made struct {
		Token string `json:"token"`
		About struct {
			ID        int64    `json:"id"`
			Projects  []string `json:"projects"`
			MayAnswer bool     `json:"mayAnswer"`
		} `json:"about"`
	}
	k.call("POST", "/api/crew/mcp/tokens", `{"name": "laptop", "projects": ["shop"]}`, 201, &made)
	if !strings.HasPrefix(made.Token, "crew_pat_") || made.About.MayAnswer || len(made.About.Projects) != 1 {
		t.Fatalf("the token: %+v", made)
	}
	var list struct{ Tokens []map[string]any }
	k.call("GET", "/api/crew/mcp/tokens", "", 200, &list)
	if b, _ := json.Marshal(list); len(list.Tokens) != 1 || strings.Contains(string(b), made.Token) {
		t.Fatalf("the list shows the token, or not one: %s", b)
	}
	tok := made.Token
	if code, text, _ := k.outsideCall(tok, "initialize", map[string]any{"protocolVersion": "2025-06-18"}); code != 200 || !strings.Contains(text, `"crew"`) {
		t.Fatalf("initialize: %d %s", code, text)
	}
	_, listed, _ := k.outsideCall(tok, "tools/list", map[string]any{})
	for _, want := range []string{"list_projects", "get_inbox", "get_ticket", "create_ticket", "comment"} {
		if !strings.Contains(listed, `"name":"`+want+`"`) {
			t.Errorf("tools/list lacks %s", want)
		}
	}
	for _, never := range []string{"answer_new_feature", "change_decision", "analyse_ticket", "request_move", "write_review", "create_subtask", "decide"} {
		if strings.Contains(listed, `"name":"`+never+`"`) {
			t.Errorf("tools/list offers %s", never)
		}
	}
	call := func(name string, args map[string]any) (string, bool) {
		_, text, refused := k.outsideCall(tok, "tools/call", map[string]any{"name": name, "arguments": args})
		return text, refused
	}
	if text, refused := call("list_projects", map[string]any{}); refused || !strings.Contains(text, `"shop"`) || strings.Contains(text, `"other"`) {
		t.Fatalf("list_projects: %s", text)
	}
	if text, refused := call("get_ticket", map[string]any{"project": "shop", "key": "SF-1"}); refused || !strings.Contains(text, "SF-1") {
		t.Fatalf("get_ticket: %s", text)
	}
	if text, refused := call("get_project", map[string]any{"project": "other"}); !refused || !strings.Contains(text, "may not use") {
		t.Fatalf("another project: %s", text)
	}
	text, refused := call("create_ticket", map[string]any{"project": "shop", "title": "Export to CSV", "why": "Customers ask for it."})
	if refused || !strings.Contains(text, `"origin":"token"`) {
		t.Fatalf("create_ticket: %s", text)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Export to CSV' AND origin = 'token' AND created_by = 'token laptop' AND stage = 'backlog'`); n != 1 {
		t.Fatal("the ticket is not marked as made through the MCP")
	}
	if text, refused := call("comment", map[string]any{"project": "shop", "ticket": "SF-1", "text": "Keep it small."}); refused {
		t.Fatalf("comment: %s", text)
	}
	if n := k.count(`SELECT count(*) FROM records WHERE kind = 'comment' AND author = 'token laptop'`); n != 1 {
		t.Fatal("the comment is not the token's")
	}
	// A token adds at most ten tickets that wait for analysis in a project.
	for i := 0; i < 9; i++ {
		if text, refused := call("create_ticket", map[string]any{"project": "shop", "title": fmt.Sprintf("More %d", i), "why": "w"}); refused {
			t.Fatalf("ticket %d: %s", i+2, text)
		}
	}
	if text, refused := call("create_ticket", map[string]any{"project": "shop", "title": "One too many", "why": "w"}); !refused || !strings.Contains(text, "wait for analysis") {
		t.Fatalf("the eleventh: %s", text)
	}
	// They are closed again, so the notice below has room in Analysis.
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE origin = 'token'`); err != nil {
		t.Fatal(err)
	}
	// Answering needs may answer: until it is on, the tool is not this token's.
	p, _ := k.b.Project(ctx, "shop")
	notice, err := k.b.AddTicket(ctx, "shop", board.NewTicket{Title: "Import", Why: "w", What: "Import.", Goal: "It imports.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f i.txt"}}}, board.OriginAgent, "builder · 00000000-0000-4000-8000-0000000000d3")
	if err != nil {
		t.Fatal(err)
	}
	k.until(notice.Key+" in Analysis", func() bool {
		k.b.Reconcile(ctx, "shop")
		k.b.Decide(ctx, p, "SF-1", board.Decision{Choice: "approve"}, "lead · r_1")
		return k.stageOf(notice.Key) == "analysis"
	})
	if _, err := k.b.Decide(ctx, p, notice.Key, board.Decision{Choice: "notice", Reason: "Not in the plan."}, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	answer := map[string]any{"project": "shop", "ticket": notice.Key, "choice": "approve_now"}
	if text, refused := call("answer_new_feature", answer); !refused || !strings.Contains(text, "not one of the tools") {
		t.Fatalf("an answer without may answer: %s", text)
	}
	k.call("PATCH", fmt.Sprintf("/api/crew/mcp/tokens/%d", made.About.ID), `{"mayAnswer": true}`, 200, nil)
	if text, refused := call("answer_new_feature", answer); refused || !strings.Contains(text, `"analysis":"ready"`) {
		t.Fatalf("an answer with may answer: %s", text)
	}
	var decidedBy string
	k.d.Server.QueryRow(ctx, `SELECT analysis->>'decidedBy' FROM tickets WHERE id = $1`, notice.ID).Scan(&decidedBy)
	if decidedBy != "token laptop" {
		t.Fatalf("the answer is recorded as %q", decidedBy)
	}
	// With may answer, a ticket is approved by being added, and still the token's (13.8).
	k.d.Server.Exec(ctx, `UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE origin = 'token'`)
	if text, refused := call("create_ticket", map[string]any{"project": "shop", "title": "Answered", "why": "w"}); refused || !strings.Contains(text, `"origin":"token"`) {
		t.Fatalf("a ticket of a token that may answer: %s", text)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Answered' AND origin = 'token' AND analysis->>'status' = 'waiting'
		AND (analysis->>'approvedByAdding')::boolean`); n != 1 {
		t.Fatal("the ticket of a token that may answer is not approved by being added")
	}
	if n := k.count(`SELECT count(*) FROM mcp_calls WHERE token = $1`, made.About.ID); n != 18 {
		t.Fatalf("%d calls recorded with the token", n)
	}
	// A run's token does not work here; the personal token does not work at a sandbox's door.
	if code, _, _ := k.outsideCall("the-run-token", "tools/list", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("a run's token: %d", code)
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	req, _ := http.NewRequest(http.MethodPost, k.mcpSrv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the personal token at a sandbox's door: %v %v", resp, err)
	}
	// Revoked, it is gone.
	k.call("POST", fmt.Sprintf("/api/crew/mcp/tokens/%d/revoke", made.About.ID), "", 200, nil)
	if code, _, _ := k.outsideCall(tok, "tools/list", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("a revoked token: %d", code)
	}
}
