package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/settings"
)

// A running project pauses when the tickets agents added pass their share of the plan's tickets, in the
// pipeline's pass; the inbox asks to raise the limit, your Resume is refused while it is passed, and
// raising the share in Settings resumes it (R32, 7.7; 22, "Limits of a project"). What its runs cost,
// as written on them before 0.53, pauses nothing.
func TestAProjectPastItsShareOfAddedTicketsPausesUntilTheShareIsRaised(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop(nil, nil, "One")
	ctx := context.Background()
	setShare := func(share float64) {
		var doc map[string]any
		k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
		doc["limits"].(map[string]any)["max_added_share"] = share
		b, _ := json.Marshal(doc)
		k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
	}
	if _, err := k.d.Server.Exec(ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			SELECT 'project', id, 99, 'breakdown', 'lead' FROM projects WHERE name = 'shop' RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, cost_usd, cost_basis)
		SELECT gen_random_uuid(), id, 1, 'ended', 'agy', 'v', 'img', 1000, 'estimated' FROM s`); err != nil {
		t.Fatal(err)
	}
	// SF-1 is the plan's, as a breakdown's ticket is; SF-2 an agent added, and Analysis approved.
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET origin = 'plan' WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, analysis)
		SELECT id, 2, 'Added', 'feature', 'analysis', 'agent', 'lead', 99, '{"status": "ready"}' FROM projects WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	// The ticket is held, so no agent starts when the project resumes: the test is about the project.
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET hold = true`); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	defer stop()
	state := func() string {
		var s string
		k.d.Server.QueryRow(ctx, `SELECT state FROM projects WHERE name = 'shop'`).Scan(&s)
		return s
	}
	k.until("the project pauses past its share", func() bool { return state() == "paused" })
	var in struct{ Items []inboxItem }
	k.call("GET", "/api/inbox", "", 200, &in)
	found := false
	for _, it := range in.Items {
		if it.ID == "paused/shop" {
			found = strings.HasPrefix(it.Body, "Agents added 1 ticket, over 25% of the plan's 1.") &&
				it.Alternatives[len(it.Alternatives)-1].Label == "Raise the limit"
		}
	}
	if !found {
		t.Fatalf("the inbox: %+v", in.Items)
	}
	k.call("POST", "/api/projects/shop/resume", "", 400, nil)
	setShare(1)
	k.until("raising the share resumes the project", func() bool { return state() == "running" })
}

// A ticket that cannot get through goes to the lead (7.7; 22, "Tickets that cannot get through").
// SF-1 does not get through, by its stopped build or its fix rounds; the lead gives it a new approach,
// which a builder carries out, and it is Done. SF-2's new approach fails too, and the lead cancels it and replaces it with two tickets, which
// start in Backlog with replaces and an equal share of what is left of its budget. Both calls are news.
func TestATicketThatCannotGetThroughGetsANewApproachThenIsReplaced(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		n := strings.TrimPrefix(key, "SF-")
		branch := map[string]string{"SF-1": "task/SF-1-one", "SF-2": "task/SF-2-two"}[key]
		switch {
		case key != "SF-1" && key != "SF-2":
			return true // the replacements are only looked at here
		case strings.HasPrefix(kind, "build"):
			// The first build does not help; the new approach's builder works on SF-1, and not on SF-2.
			files := map[string]string{"a.txt": fmt.Sprint(time.Now().UnixNano())}
			if key == "SF-1" && strings.Contains(p, "The lead's new approach") {
				files[n+".txt"] = key
				k.tool(spec.Run, "add_note", map[string]any{"text": "Took the new approach."})
			}
			k.pushFromRoot(branch, files)
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		case strings.HasPrefix(kind, "look_again") && strings.Contains(p, "had its new approach already"):
			k.tool(spec.Run, "new_approach", map[string]any{"reason": "again", "approach": "again"}) // refused: once only
			k.tool(spec.Run, "cancel_and_replace", map[string]any{"reason": "It is two pieces of work.", "tickets": []map[string]any{
				{"title": "Half one", "why": "w", "what": "w", "goal": "g", "checks": []map[string]any{{"kind": "cmd", "spec": "test -f h1.txt"}}},
				{"title": "Half two", "why": "w", "what": "w", "goal": "g", "checks": []map[string]any{{"kind": "cmd", "spec": "test -f h2.txt"}}},
			}})
			return true
		case strings.HasPrefix(kind, "look_again"):
			k.tool(spec.Run, "new_approach", map[string]any{"reason": "The builder never made " + n + ".txt.", "approach": "1. Make " + n + ".txt."})
			return true
		case strings.HasPrefix(kind, "fix"):
			// A fix round does not help; the new approach's builder works on SF-1, and not on SF-2.
			files := map[string]string{"b.txt": fmt.Sprint(time.Now().UnixNano())}
			if key == "SF-1" && strings.Contains(p, "The lead's new approach") {
				files[n+".txt"] = key
			}
			k.pushMerged(branch, files)
			k.tool(spec.Run, "add_note", map[string]any{"text": "Took the new approach."})
			k.tool(spec.Run, "fix_done", map[string]any{})
			return true
		}
		return false
	})
	k.shop([]string{}, map[string]any{"max_goal_rounds": 1, "max_fix_rounds": 1}, "one", "two")
	defer k.run()()
	k.until("SF-1 Done after its new approach, and SF-2 cancelled", func() bool {
		return k.stageOf("SF-1") == "done" && k.stageOf("SF-2") == "closed"
	})
	ctx := context.Background()
	var approaches int
	var reason string
	k.d.Server.QueryRow(ctx, `SELECT approaches FROM tickets WHERE number = 1`).Scan(&approaches)
	k.d.Server.QueryRow(ctx, `SELECT closed_reason FROM tickets WHERE number = 2`).Scan(&reason)
	if approaches != 1 || reason != "cancelled" {
		t.Fatalf("SF-1 approaches %d, SF-2 closed as %q", approaches, reason)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1 AND s.state = 'superseded'`); n != 1 {
		t.Fatalf("%d of SF-1's subtasks superseded, want its stopped build", n)
	}
	// The replacements: origin agent, in Backlog or past it, each with replaces and the priority of the
	// ticket they replace; no budget (since 0.53).
	rows, err := k.d.Server.Query(ctx, `SELECT t.title, t.origin, t.priority = (SELECT priority FROM tickets WHERE number = 2),
			t.budget_usd IS NULL, l.kind FROM tickets t
		JOIN ticket_links l ON l.from_ticket = t.id AND l.to_ticket = (SELECT id FROM tickets WHERE number = 2) ORDER BY t.number`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var title, origin, link string
		var priority, noBudget bool
		rows.Scan(&title, &origin, &priority, &noBudget, &link)
		got = append(got, fmt.Sprintf("%s %s %s %v %v", title, origin, link, priority, noBudget))
	}
	rows.Close()
	if len(got) != 2 || got[0] != "Half one agent replaces true true" || got[1] != "Half two agent replaces true true" {
		t.Fatalf("the replacements: %v", got)
	}
	var feed struct{ Lines []struct{ Kind, Text string } }
	k.call("GET", "/api/projects/shop/activity", "", 200, &feed)
	var news []string
	for _, l := range feed.Lines {
		if l.Kind == "news" {
			news = append(news, l.Text)
		}
	}
	all := strings.Join(news, "\n")
	if !strings.Contains(all, "The lead gave SF-1 a new approach: The builder never made 1.txt.") ||
		!strings.Contains(all, "The lead cancelled SF-2 and replaced it with SF-3, SF-4: It is two pieces of work.") {
		t.Fatalf("the news:\n%s", all)
	}
	k.mu.Lock()
	refused := slices.Contains(k.calls, "new_approach:false")
	k.mu.Unlock()
	if !refused {
		t.Fatal("a second new approach was not refused")
	}
}

// A look again that stops twice cancels the ticket with that reason (21).
func TestALookAgainThatStopsTwiceCancelsTheTicket(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		switch {
		case strings.HasPrefix(kind, "build"):
			k.pushFromRoot("task/SF-1-one", map[string]string{"a.txt": fmt.Sprint(time.Now().UnixNano())})
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		case strings.HasPrefix(kind, "look_again"):
			return true // the lead says nothing: its run ends without a call, twice
		}
		return false
	})
	k.shop([]string{}, map[string]any{"max_goal_rounds": 1}, "one")
	defer k.run()()
	k.until("SF-1 cancelled", func() bool { return k.stageOf("SF-1") == "closed" })
	var reason, why string
	k.d.Server.QueryRow(context.Background(), `SELECT closed_reason, analysis->>'reason' FROM tickets WHERE number = 1`).Scan(&reason, &why)
	if reason != "cancelled" || !strings.HasPrefix(why, "the lead's look again stopped twice") {
		t.Fatalf("%q %q", reason, why)
	}
}

// A look again on a ticket whose build stopped twice without pushing reads main, and a look again
// ended by crew-run's own step is retried up to three times before the ticket waits for the operator
// without being cancelled, while a look again that stops for the lead's own reasons still cancels
// the ticket (7.7, 21, BUG-030).
func TestALookAgainEndedByCrewRunsOwnStepDoesNotCancelTheTicket(t *testing.T) {
	k := newTicketKit(t)
	k.builder(nil)
	prev := k.custom
	sf2LookRuns := 0
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		key := k.keyOf(spec, p)
		switch {
		case strings.HasPrefix(kind, "build"):
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl",
				Exit: runspec.Exit{Reason: cli.ReasonError, Detail: "stopped before pushing"}}, true
		case strings.HasPrefix(kind, "look_again") && key == "SF-2":
			k.mu.Lock()
			sf2LookRuns++
			n := sf2LookRuns
			k.mu.Unlock()
			if n <= 2 {
				return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl",
					Exit: runspec.Exit{Reason: cli.ReasonError, Step: runspec.StepWorkspace,
						Error: "git fetch origin refs/heads/task/SF-2-two: exit status 128"}}, true
			}
			// On SF-2's retried look again, the lead ends its turn twice without deciding: its own stop cancels SF-2.
			return recording(spec), true
		case strings.HasPrefix(kind, "look_again"):
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl",
				Exit: runspec.Exit{Reason: cli.ReasonError, Step: runspec.StepWorkspace,
					Error: "git fetch origin refs/heads/task/SF-1-one: exit status 128"}}, true
		}
		return prev(kind, spec, p)
	}
	k.shop([]string{}, nil, "one", "two")
	defer k.run()()
	k.until("SF-1 waits after three look-again workspace errors and SF-2 cancels on the lead's own stop", func() bool {
		sf1Done := k.count(`SELECT count(*) FROM tickets WHERE number = 1 AND stuck IS NOT NULL AND stuck NOT LIKE 'the lead looks at it again:%'`) == 1 ||
			k.stageOf("SF-1") == "closed"
		return sf1Done && k.stageOf("SF-2") == "closed"
	})
	ctx := context.Background()
	var stage, closed, stuck string
	k.d.Server.QueryRow(ctx, `SELECT stage, coalesce(closed_reason, ''), coalesce(stuck, '') FROM tickets WHERE number = 1`).Scan(&stage, &closed, &stuck)
	if stage != "in_progress" || closed != "" || !strings.Contains(stuck, "workspace") || !strings.Contains(stuck, "operator") {
		t.Fatalf("SF-1: stage=%q closed=%q stuck=%q", stage, closed, stuck)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1 AND s.kind = 'look_again'`); n != 3 {
		t.Fatalf("SF-1: %d look-again subtasks, want 3", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again' AND title LIKE 'Look again: its subtask % Look again:%'`); n != 0 {
		t.Fatal("the retried look again nested the previous look again's title")
	}
	var sf2Closed, sf2Why string
	k.d.Server.QueryRow(ctx, `SELECT coalesce(closed_reason, ''), coalesce(analysis->>'reason', '') FROM tickets WHERE number = 2`).Scan(&sf2Closed, &sf2Why)
	if sf2Closed != "cancelled" || !strings.HasPrefix(sf2Why, "the lead's look again stopped twice") {
		t.Fatalf("SF-2: closed=%q why=%q", sf2Closed, sf2Why)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2 AND s.kind = 'look_again'`); n != 2 {
		t.Fatalf("SF-2: %d look-again subtasks, want 2 (one workspace error retry, then cancelled on lead stop)", n)
	}
	k.mu.Lock()
	spec := k.specs["look_again"][0]
	prompt := k.prompts["look_again"][0]
	k.mu.Unlock()
	if spec.Base != "refs/heads/main" {
		t.Fatalf("look_again base = %q, want refs/heads/main", spec.Base)
	}
	if !strings.Contains(prompt, "never pushed") || strings.Contains(prompt, "a clone of its branch") {
		t.Fatalf("look_again prompt for an unpushed branch:\n%s", prompt)
	}
}

// Pause all and Resume through the app (7.9; 22, "Pause all, and the disk guard"): while paused the
// frame says so, no analysis starts for a new ticket, and a new feature's clock waits; Resume moves
// that clock on by the pause, and the analysis starts. Each switch has its row in the history.
func TestPauseAllHoldsTheBoardAndResumeGoesOn(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	ctx := context.Background()
	k.call("POST", "/api/crew/pause", "", 200, nil)
	var f struct{ Held string }
	k.call("GET", "/api/frame", "", 200, &f)
	if f.Held != "Crew is paused: Pause all" {
		t.Fatalf("the frame: %q", f.Held)
	}
	// A new feature whose notice ran out while Crew was paused does not go ahead.
	if _, err := k.d.Server.Exec(ctx, `UPDATE crew_state SET paused_at = now() - interval '1 hour' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var notice int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, analysis, notice_until)
		SELECT id, 9, 'A feature', 'feature', 'analysis', 'agent', 'builder · r', 9, '{"status": "new_feature_notice"}', now() - interval '10 minutes'
		FROM projects WHERE name = 'shop' RETURNING id`).Scan(&notice); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	time.Sleep(500 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM runs`); n != 0 {
		stop()
		t.Fatalf("%d runs started while Crew was paused", n)
	}
	if k.stageOf("SF-9") != "analysis" || k.count(`SELECT count(*) FROM tickets WHERE id = $1 AND analysis->>'status' = 'new_feature_notice'`, notice) != 1 {
		stop()
		t.Fatal("a new feature went ahead while Crew was paused")
	}
	k.call("POST", "/api/crew/resume", "", 200, nil)
	// Its notice ended 10 minutes into the hour Crew was paused: 50 minutes are left of it.
	var left float64
	k.d.Server.QueryRow(ctx, `SELECT extract(epoch FROM notice_until - now()) / 60 FROM tickets WHERE id = $1`, notice).Scan(&left)
	if left < 49 || left > 51 {
		stop()
		t.Fatalf("%.1f minutes of the notice are left, want 50", left)
	}
	k.until("the analysis starts after Resume", func() bool { return k.count(`SELECT count(*) FROM runs`) > 0 })
	stop()
	if n := k.count(`SELECT count(*) FROM changes WHERE tbl = 'crew_state' AND who = 'operator'`); n != 2 {
		t.Fatalf("%d rows of history for the switches, want 2", n)
	}
}

// A Done project gets its final report (7.2): the lead's five sections beside the facts Crew counted,
// on GET /api/projects/{p}/report, once for each time the project is Done.
func TestADoneProjectGetsItsFinalReport(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if !strings.HasPrefix(kind, "report") {
			return false
		}
		if !strings.Contains(p, `"done": [`) || !strings.Contains(p, "SF-1 one") {
			t.Errorf("the report's prompt has no facts:\n%s", p)
		}
		k.tool(spec.Run, "write_report", map[string]any{"built": "One file.", "skipped": "Nothing.", "cancelled": "Nothing.",
			"missing": "Nothing.", "learned": "Small tickets go fast."})
		return true
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("the final report", func() bool { return k.count(`SELECT count(*) FROM reports`) == 1 })
	var got struct {
		Reports []struct {
			Number   int
			Sections map[string]string
			Facts    struct {
				Done []string
				Runs int
			}
			Missing *string
		}
	}
	k.call("GET", "/api/projects/shop/report", "", 200, &got)
	if len(got.Reports) != 1 {
		t.Fatalf("%+v", got)
	}
	r := got.Reports[0]
	if r.Number != 1 || r.Sections["learned"] != "Small tickets go fast." || r.Missing != nil || len(r.Facts.Done) != 1 || r.Facts.Runs == 0 {
		t.Fatalf("the report: %+v", r)
	}
	time.Sleep(300 * time.Millisecond)
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'report'`); n != 1 {
		t.Fatalf("%d report runs for one Done", n)
	}
}

// A report the lead cannot write keeps Crew's facts, with the reason it is missing (21).
func TestAReportTheLeadCannotWriteKeepsTheFacts(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		return strings.HasPrefix(kind, "report") // the lead never calls write_report
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("a report with the facts only", func() bool { return k.count(`SELECT count(*) FROM reports WHERE missing IS NOT NULL`) == 1 })
	time.Sleep(300 * time.Millisecond)
	var missing string
	k.d.Server.QueryRow(context.Background(), `SELECT missing FROM reports`).Scan(&missing)
	if n := k.count(`SELECT count(*) FROM reports`); n != 1 || !strings.HasPrefix(missing, "the lead's run stopped twice") {
		t.Fatalf("%d reports: %q", n, missing)
	}
}

// Revert (7.13; 22, "After Done"): a revert of a Done ticket is a new ticket that reverts it; Crew
// commits the revert on its branch, its checks pass once the change is gone, and it merges. main no
// longer has the change, and the Done ticket stays Done.
func TestARevertTakesTheChangeOutOfMainThroughTheChecks(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	stop := k.run()
	defer func() { stop() }()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	if !strings.Contains(string(mainFile(k.git, context.Background(), "1.txt")), "SF-1") {
		t.Fatal("main has no 1.txt after SF-1")
	}
	k.call("POST", "/api/projects/shop/tasks/SF-2/revert", "", 404, nil) // no such ticket
	var r struct{ Key, Kind string }
	k.call("POST", "/api/projects/shop/tasks/SF-1/revert", "", 201, &r)
	if r.Key != "SF-2" || r.Kind != "revert" {
		t.Fatalf("%+v", r)
	}
	// A revert is the operator stepping in (10.2).
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'operator_intervention' AND detail->>'what' = 'merge_reverted'
		AND detail->>'revert' = 'SF-2' AND ticket = (SELECT id FROM tickets WHERE number = 1)`); n != 1 {
		t.Fatalf("%d signals of the revert", n)
	}
	k.call("POST", "/api/projects/shop/tasks/SF-1/revert", "", 400, nil) // one revert at a time
	k.until("the revert Done", func() bool { return k.stageOf("SF-2") == "done" })
	k.call("POST", "/api/projects/shop/tasks/SF-1/revert", "", 400, nil) // and not a second once it is Done
	if mainFile(k.git, context.Background(), "1.txt") != nil {
		t.Fatal("main still has 1.txt after the revert")
	}
	if k.stageOf("SF-1") != "done" {
		t.Fatal("the reverted ticket left Done")
	}
	if n := k.count(`SELECT count(*) FROM ticket_links l JOIN tickets a ON a.id = l.from_ticket JOIN tickets b ON b.id = l.to_ticket
		WHERE a.number = 2 AND b.number = 1 AND l.kind = 'reverts'`); n != 1 {
		t.Fatal("no reverts link")
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2 AND s.role IN ('lead', 'builder')`); n != 0 {
		t.Fatalf("%d lead or builder runs for a revert that applied", n)
	}
	// The Tickets screen lists both, newest first, the revert with what it reverts.
	var list struct {
		Tickets []struct {
			Key, Kind, Stage, Origin string
			From                     *string
			Subtasks                 int
		}
	}
	k.call("GET", "/api/projects/shop/tickets", "", 200, &list)
	if len(list.Tickets) != 2 || list.Tickets[0].Key != "SF-2" || list.Tickets[0].Kind != "revert" || list.Tickets[0].From == nil ||
		*list.Tickets[0].From != "SF-1" || list.Tickets[1].Stage != "done" || list.Tickets[1].Subtasks == 0 {
		t.Fatalf("the Tickets screen: %+v", list.Tickets)
	}
}

// An amendment to the plan (7.13): the lead breaks it down while the project runs on; its tickets
// wait in Planning; the inbox asks for its OK; the OK moves them to Backlog. One is open at a time.
func TestAnAmendmentIsBrokenDownAndGetsItsOK(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if !strings.Contains(p, "the operator amended its plan") {
			return false
		}
		k.tool(spec.Run, "write_breakdown", map[string]any{"text": "## Amendment 1\n\nSF-2."})
		k.tool(spec.Run, "propose_tickets", map[string]any{"tickets": []any{map[string]any{"title": "Sign-out", "kind": "feature",
			"why": "The amendment asks for it.", "what": "A sign-out button.", "goal": "One can sign out.",
			"checks": []any{map[string]any{"kind": "cmd", "spec": "test -f 2.txt"}}, "docsReason": "none"}}})
		return true
	})
	k.shop([]string{}, nil, "one")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pl := &planning.Planner{Pool: k.d.Server, Runs: k.driver, Version: "1.0.0", Log: log,
		Settings: func(ctx context.Context) (settings.Doc, error) {
			return settings.Effective(ctx, k.d.Server, settings.Machine{Version: "1.0.0"})
		}}
	go pl.Run(ctx, 50*time.Millisecond)
	stop := k.run()
	defer func() { stop() }()
	k.call("POST", "/api/projects/shop/amendments", `{"text": "Add sign-out."}`, 201, nil)
	k.call("POST", "/api/projects/shop/amendments", `{"text": "And more."}`, 400, nil)
	k.until("the amendment's ticket in Planning", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE stage = 'planning' AND amendment IS NOT NULL`) == 1
	})
	var in struct{ Items []inboxItem }
	k.until("the OK in the inbox", func() bool {
		k.call("GET", "/api/inbox", "", 200, &in)
		for _, it := range in.Items {
			if it.ID == "ok/shop" && strings.HasPrefix(it.Title, "OK for amendment 1 of shop") {
				return true
			}
		}
		return false
	})
	if st := k.count(`SELECT count(*) FROM projects WHERE name = 'shop' AND state = 'running'`); st != 1 {
		t.Fatal("the project does not run on")
	}
	k.call("POST", "/api/projects/shop/ok", "", 200, nil)
	if k.stageOf("SF-2") == "planning" || k.count(`SELECT count(*) FROM plans WHERE kind = 'amendment_ok' AND number = 1`) != 1 {
		t.Fatalf("after the OK: SF-2 in %s", k.stageOf("SF-2"))
	}
}

// Delete (7.13): refused unless the name is typed; then the project is archived, its runs end, and
// every row of it goes, its history too, in one transaction; its repositories after; one row in
// deleted_projects says it existed. The server's role does it, through the database's function.
func TestDeleteRemovesAProjectAndKeepsOneRow(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	stop := k.run()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	k.until("the final report", func() bool { return k.count(`SELECT count(*) FROM reports`) == 1 })
	stop()
	k.call("DELETE", "/api/projects/shop", `{"name": "shp"}`, 400, nil)
	if k.count(`SELECT count(*) FROM projects WHERE name = 'shop'`) != 1 {
		t.Fatal("a wrong name deleted the project")
	}
	k.call("DELETE", "/api/projects/shop", `{"name": "shop"}`, 200, nil)
	for _, table := range []string{"projects WHERE name = 'shop'", "tickets", "runs r JOIN subtasks s ON s.id = r.subtask WHERE s.project IS NOT NULL",
		"changes WHERE project IS NOT NULL AND project <> 'f1c50000-0000-4000-8000-000000000001'", "events", "reports", "signals WHERE project IS NOT NULL"} {
		if n := k.count(`SELECT count(*) FROM ` + table); n != 0 {
			t.Errorf("%d rows left in %s", n, table)
		}
	}
	var name string
	var tickets, runsN int
	k.d.Server.QueryRow(context.Background(), `SELECT name, tickets, runs FROM deleted_projects`).Scan(&name, &tickets, &runsN)
	if name != "shop" || tickets != 1 || runsN == 0 {
		t.Fatalf("deleted_projects: %s %d %d", name, tickets, runsN)
	}
	if k.git.Exists("shop") {
		t.Fatal("the repositories are still there")
	}
	k.call("GET", "/api/projects/shop/overview", "", 404, nil)
}

// Archive (7.13): the project leaves the menu and the front page, nothing is deleted, and nothing
// starts in it: its waiting ticket gets no analysis.
func TestAnArchivedProjectIsHiddenAndStartsNothing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	k.call("POST", "/api/projects/shop/archive", "", 200, nil)
	stop := k.run()
	time.Sleep(400 * time.Millisecond)
	stop()
	if n := k.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("%d runs started in an archived project", n)
	}
	var f struct{ Projects []struct{ Name string } }
	k.call("GET", "/api/frame", "", 200, &f)
	var o struct{ Projects []struct{ Name string } }
	k.call("GET", "/api/overview", "", 200, &o)
	for _, p := range append(f.Projects, o.Projects...) {
		if p.Name == "shop" {
			t.Fatalf("an archived project is listed: %+v %+v", f, o)
		}
	}
	if k.count(`SELECT count(*) FROM tickets`) != 1 {
		t.Fatal("archive deleted something")
	}
}

// Joined subtasks that still fail (21): the builder that joins them gets one more try, and after that
// the lead looks at the ticket again (7.7).
func TestWorkThatStillDoesNotJoinGoesToTheLead(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	plan := []map[string]any{
		{"title": "One side", "what": "Add a/a.txt.", "doneWhen": "a/a.txt exists.", "role": "builder", "touches": []string{"a/"}},
		{"title": "The other side", "what": "Add b/b.txt.", "doneWhen": "b/b.txt exists.", "role": "builder", "touches": []string{"b/"}},
	}
	files := map[string]map[string]string{"SF-1.2": {"a/a.txt": "a", "x.txt": "one\n"}, "SF-1.3": {"b/b.txt": "b", "x.txt": "two\n"}}
	k.partKit(t, plan, files)
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		switch {
		case strings.Contains(p, "# Look again: a ticket that cannot get through"):
			return recording(spec), true
		case !strings.Contains(p, "# Join the parallel work of a ticket"):
			return prev(kind, spec, p)
		case strings.Contains(p, "conflicts in: x.txt"):
			part := regexp.MustCompile(`Merging (sub/SF-1\.\d+) into`).FindStringSubmatch(p)
			k.joinBranch(spec.Branch, part[1], nil)
		default:
			// The ticket's checks need c.txt; this join adds something else.
			k.joinBranch(spec.Branch, "", map[string]string{"y.txt": "y"})
		}
		k.tool(spec.Run, "add_note", map[string]any{"text": "Joined"})
		k.tool(spec.Run, "subtask_done", map[string]any{})
		return recording(spec), true
	}
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("the lead looks again", func() bool {
		return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'look_again' AND title = 'Look again: its parallel work did not join after two tries'`) == 1
	})
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'join' AND role = 'builder'`); n != 2 {
		t.Fatalf("%d builders joined, want 2 before the lead", n)
	}
}

// The news of the models (21, rows on a model no longer offered and on agy models failing): a pin
// passed over because its model is gone, and a list that could not be read twice in a row, are lines
// of the activity.
func TestTheNewsOfTheModels(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop([]string{}, nil, "one")
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO signals (project, ticket, kind, detail)
		SELECT t.project, t.id, 'model_gone', '{"role": "builder", "ignored": ["agy/gemini-9-old"], "ran": "agy/gemini-4-argon · high"}'
		FROM tickets t WHERE t.number = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE cli_logins SET models_failures = 2, models_failed_at = now(), models_error = 'agy models: exit 1' WHERE cli = 'agy'`); err != nil {
		t.Fatal(err)
	}
	var feed struct{ Lines []struct{ Kind, Text string } }
	k.call("GET", "/api/activity", "", 200, &feed)
	var news []string
	for _, l := range feed.Lines {
		if l.Kind == "news" {
			news = append(news, l.Text)
		}
	}
	all := strings.Join(news, "\n")
	for _, want := range []string{"agy/gemini-9-old is no longer offered: the builder ran on agy/gemini-4-argon · high in its place",
		"Crew could not read agy's list of models: agy models: exit 1. It keeps the last list"} {
		if !strings.Contains(all, want) {
			t.Errorf("no news %q in:\n%s", want, all)
		}
	}
}

// Delete refuses before it touches anything (review of M11): the fixture's name archives nothing and
// stops nothing. The database's function refuses on its own what the server checks before it, the row
// of a deleted project can only be added to, and another project is untouched (security review of
// M11). A repository that cannot be removed is said, and swept when the server starts again.
func TestDeleteRefusesFirstAndSweepsWhatIsLeft(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	ctx := context.Background()
	k.call("DELETE", "/api/projects/fixture", `{"name": "fixture"}`, 400, nil)
	if n := k.count(`SELECT count(*) FROM projects WHERE name = 'fixture' AND state <> 'archived'`); n != 1 {
		t.Fatal("a refused delete archived the fixture")
	}
	if _, err := k.d.Server.Exec(ctx, `SELECT crew_delete_project(id, 'test') FROM projects WHERE name = 'shop'`); err == nil ||
		!strings.Contains(err.Error(), "archived first") {
		t.Fatalf("the function deleted a running project: %v", err)
	}
	if _, err := k.d.Server.Exec(ctx, `SELECT crew_delete_project('f1c50000-0000-4000-8000-000000000001', 'test')`); err == nil {
		t.Fatal("the function deleted the fixture")
	}
	// Archived, with a run that has not ended: the function refuses by itself.
	if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET state = 'archived' WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			SELECT 'project', id, 90, 'breakdown', 'lead' FROM projects WHERE name = 'shop' RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image) SELECT gen_random_uuid(), id, 1, 'working', 'agy', 'v', 'img' FROM s`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `SELECT crew_delete_project(id, 'test') FROM projects WHERE name = 'shop'`); err == nil ||
		!strings.Contains(err.Error(), "runs that have not ended") {
		t.Fatalf("the function deleted a project with a run going: %v", err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE runs SET state = 'ended' WHERE state = 'working'`); err != nil {
		t.Fatal(err)
	}
	// An open terminal refuses the delete before anything is done.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, project, container, image) SELECT 'term-1', 'terminal', id, 'term-1', 'img' FROM projects WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	k.call("DELETE", "/api/projects/shop", `{"name": "shop"}`, 400, nil)
	if _, err := k.d.Server.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = 'term-1'`); err != nil {
		t.Fatal(err)
	}
	// Another project, which must survive.
	k.call("POST", "/api/projects", `{"name": "other", "key": "OT", "plan": "## Goal\n\nStay.\n", "repository": {"kind": "local"}}`, 201, nil)
	k.call("POST", "/api/projects/other/tasks", `{"title": "Stays", "why": "w", "what": "w", "goal": "g",
		"checks": [{"kind": "cmd", "spec": "true"}], "docsReason": "none"}`, 201, nil)
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO signals (project, ticket, kind) SELECT project, id, 'test' FROM tickets WHERE title = 'Stays'`); err != nil {
		t.Fatal(err)
	}
	others := func() int {
		return k.count(`SELECT (SELECT count(*) FROM tickets t JOIN projects x ON x.id = t.project WHERE x.name = 'other')
			+ (SELECT count(*) FROM signals g JOIN projects x ON x.id = g.project WHERE x.name = 'other')
			+ (SELECT count(*) FROM changes c JOIN projects x ON x.id = c.project WHERE x.name = 'other')
			+ (SELECT count(*) FROM plans p JOIN projects x ON x.id = p.project WHERE x.name = 'other')`)
	}
	before := others()
	// The canonical repository cannot be removed now.
	canon := k.git.Canonical("shop")
	if err := os.Chmod(filepath.Dir(canon), 0o555); err != nil {
		t.Fatal(err)
	}
	var out struct{ Deleted, RepositoriesLeft string }
	k.call("DELETE", "/api/projects/shop", `{"name": "shop"}`, 200, &out)
	os.Chmod(filepath.Dir(canon), 0o755)
	if out.Deleted != "shop" || out.RepositoriesLeft == "" || !k.git.Exists("shop") {
		t.Fatalf("the answer: %+v, left %v", out, k.git.Exists("shop"))
	}
	if err := k.b.SweepDeleted(ctx); err != nil || k.git.Exists("shop") {
		t.Fatalf("the sweep: %v, left %v", err, k.git.Exists("shop"))
	}
	if after := others(); after != before || before < 4 {
		t.Fatalf("the other project's rows: %d before, %d after", before, after)
	}
	if _, err := k.d.Server.Exec(ctx, `DELETE FROM deleted_projects`); err == nil {
		t.Fatal("the server's role deleted the row that says a project existed")
	}
}

// A new approach the board refuses after the turn goes back to the lead as one more round, and does
// not crash the pass (review of M11): here another new approach was recorded meanwhile.
func TestARefusedLookAgainCallGoesBackToTheLead(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		switch {
		case strings.HasPrefix(kind, "build"):
			k.pushFromRoot("task/SF-1-one", map[string]string{"a.txt": fmt.Sprint(time.Now().UnixNano())})
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		case strings.Contains(p, "Crew could not apply your call"):
			k.tool(spec.Run, "cancel_and_replace", map[string]any{"reason": "It cannot be done as it is.", "tickets": []map[string]any{
				{"title": "Smaller", "why": "w", "what": "w", "goal": "g", "checks": []map[string]any{{"kind": "cmd", "spec": "test -f s.txt"}}}}})
			return true
		case strings.HasPrefix(kind, "look_again"):
			k.tool(spec.Run, "new_approach", map[string]any{"reason": "r", "approach": "a"})
			if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET approaches = 1 WHERE number = 1`); err != nil {
				t.Error(err)
			}
			return true
		}
		return false
	})
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("SF-1 cancelled after the refused call", func() bool { return k.stageOf("SF-1") == "closed" })
}
