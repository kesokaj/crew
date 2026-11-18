package board_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

func replacement(title string) board.NewTicket {
	n := ready(title)
	return n
}

// Cancel and replace (7.7): the cancelled ticket is Closed as cancelled with the lead's reason; its
// replacements start in Backlog as the lead's tickets with replaces, at its priority, and with no
// budget (since 0.53); a ticket that depended on it depends on them. A replacement is not replaced
// again: it is cancelled with none, and its reason is what is missing.
func TestCancelAndReplace(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	high := 1
	n := ready("Sign-in")
	n.Priority = &high
	one := e.add(p, n, board.OriginPlan)
	dep := ready("Sessions")
	dep.DependsOn = []string{one.Key}
	two := e.add(p, dep, board.OriginPlan)
	e.ok(p)
	p = e.reload(p)
	if _, err := e.b.CancelAndReplace(e.ctx, p, one.Key, "too big", nil, "lead · r1"); kindOf(err) != board.Invalid {
		t.Fatalf("no replacements for a ticket: %v", err)
	}
	reps, err := e.b.CancelAndReplace(e.ctx, p, one.Key, "It is two pieces of work.", []board.NewTicket{replacement("Form"), replacement("Check")}, "lead · r1")
	if err != nil {
		t.Fatal(err)
	}
	got := e.ticket(p, one.Key)
	if got.Stage != "closed" || *got.ClosedReason != "cancelled" || got.Analysis.Reason != "It is two pieces of work." {
		t.Fatalf("the cancelled ticket: %s %v %q", got.Stage, got.ClosedReason, got.Analysis.Reason)
	}
	var keys []string
	for _, r := range reps {
		keys = append(keys, r.Key)
		if r.Stage != "backlog" || r.Origin != board.OriginAgent || r.CreatedBy != "lead · r1" || r.Priority != 1 ||
			!slices.Contains(r.Links, board.Link{Kind: "replaces", To: one.Key}) || !r.IsReplacement() {
			t.Fatalf("a replacement: %+v", r)
		}
	}
	// Sessions depended on Sign-in; now it depends on both replacements, and on Sign-in no more.
	var deps []string
	for _, l := range e.ticket(p, two.Key).Links {
		if l.Kind == "depends_on" {
			deps = append(deps, l.To)
		}
	}
	slices.Sort(deps)
	if !slices.Equal(deps, keys) {
		t.Fatalf("Sessions depends on %v, want %v", deps, keys)
	}
	// A replacement is not replaced again.
	if _, err := e.b.CancelAndReplace(e.ctx, p, keys[0], "again", []board.NewTicket{replacement("More")}, "lead · r2"); kindOf(err) != board.Refused {
		t.Fatalf("a replacement replaced: %v", err)
	}
	if _, err := e.b.CancelAndReplace(e.ctx, p, keys[0], "The form needs a design nobody has.", nil, "lead · r2"); err != nil {
		t.Fatal(err)
	}
	var text string
	var missing bool
	if err := e.db.Server.QueryRow(e.ctx, `SELECT detail->>'text', (detail->>'missing')::bool FROM signals
		WHERE kind = 'cancel_and_replace' ORDER BY id DESC LIMIT 1`).Scan(&text, &missing); err != nil {
		t.Fatal(err)
	}
	if !missing || !strings.Contains(text, "What is missing: The form needs a design nobody has.") {
		t.Fatalf("%v %q", missing, text)
	}
}

// A new approach (7.7): once a ticket, never in Analysis, and it supersedes the stopped subtasks.
func TestNewApproach(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	one := e.add(p, ready("Sign-in"), board.OriginPlan)
	e.ok(p)
	p = e.reload(p)
	if _, err := e.b.NewApproach(e.ctx, p, one.Key, "x", "lead · r1"); kindOf(err) != board.Refused {
		t.Fatalf("a new approach in Backlog: %v", err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES
		('ticket', $1, $2, 1, 'build', 'builder', 'stopped'), ('ticket', $1, $2, 2, 'part', 'builder', 'queued'),
		('ticket', $1, $2, 3, 'part', 'builder', 'done')`, p.ID, one.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.b.NewApproach(e.ctx, p, one.Key, "The approach was wrong.", "lead · r1")
	if err != nil || got.Approaches != 1 || got.ApproachAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	var states []string
	rows, _ := e.db.Server.Query(e.ctx, `SELECT state FROM subtasks WHERE ticket = $1 ORDER BY number`, one.ID)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		states = append(states, s)
	}
	rows.Close()
	if !slices.Equal(states, []string{"superseded", "superseded", "done"}) {
		t.Fatalf("the subtasks: %v", states)
	}
	if _, err := e.b.NewApproach(e.ctx, p, one.Key, "again", "lead · r2"); kindOf(err) != board.Refused {
		t.Fatalf("a second new approach: %v", err)
	}
}

// A task with no clear why, what, or goal cannot leave Analysis (21): an approval with none is refused
// and says what it lacks; once the analysis writes them, it moves on.
func TestATicketWithNoWhatOrGoalStaysInAnalysis(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Sign-in"), board.OriginPlan)
	e.ok(p)
	p = e.reload(p)
	seen := "sign-in fails with a long email"
	e.add(p, board.NewTicket{Title: "Long emails", Why: "They fail.", Seen: &seen}, board.OriginAgent)
	e.reconcile(p)
	if e.stage(p, "SF-2") != "analysis" {
		t.Fatalf("SF-2 in %s", e.stage(p, "SF-2"))
	}
	_, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "approve", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test-long"}}}, "lead · r1")
	if err == nil || !strings.Contains(err.Error(), "has no what") {
		t.Fatalf("an approval with no what: %v", err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-2") != "analysis" {
		t.Fatalf("SF-2 left Analysis with no what: %s", e.stage(p, "SF-2"))
	}
	what, goal := "Accept emails of 254 characters.", "A 254-character email signs in."
	e.approve(p, "SF-2", board.Decision{What: &what, Goal: &goal, Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test-long"}}})
	e.reconcile(p)
	if e.stage(p, "SF-2") == "analysis" {
		t.Fatalf("SF-2 stays in Analysis with its what and goal: %v", e.ticket(p, "SF-2").Waiting)
	}
}

// Superseded and cancelled parts and joins wait for nothing (7.7), so they do not hold a ticket in
// In progress; a done part that is not joined yet still holds it (7.5, 7.11).
func TestASupersededOrCancelledPartDoesNotHoldInProgress(t *testing.T) {
	e := newEnv(t)
	e.b.Checks = scripted()
	p := e.project("shop", "SF")
	e.add(p, ready("One"), board.OriginPlan)
	e.add(p, ready("Two"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.approve(p, "SF-2", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)

	one := e.ticket(p, "SF-1")
	two := e.ticket(p, "SF-2")
	if one.Stage != "in_progress" || two.Stage != "in_progress" {
		t.Fatalf("stages = %s, %s; want in_progress", one.Stage, two.Stage)
	}
	e.push(p, *one.Branch, map[string]string{"one.go": "package shop\n"})
	e.push(p, *two.Branch, map[string]string{"two.go": "package shop\n"})

	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES
		('ticket', $1, $2, 1, 'part', 'builder', 'superseded'),
		('ticket', $1, $2, 2, 'part', 'builder', 'cancelled'),
		('ticket', $1, $2, 3, 'join', 'builder', 'superseded'),
		('ticket', $1, $3, 1, 'part', 'builder', 'done')`, p.ID, one.ID, two.ID); err != nil {
		t.Fatal(err)
	}

	e.reconcile(p)
	if got := e.stage(p, "SF-1"); got == "in_progress" {
		w := ""
		if tk := e.ticket(p, "SF-1"); tk.Waiting != nil {
			w = *tk.Waiting
		}
		t.Fatalf("SF-1 stayed in in_progress (waiting: %q)", w)
	}
	two = e.ticket(p, "SF-2")
	if two.Stage != "in_progress" || two.Waiting == nil || *two.Waiting != "1 of its subtasks are not done and joined yet" {
		t.Fatalf("SF-2 = %s, waiting %v", two.Stage, two.Waiting)
	}
}
