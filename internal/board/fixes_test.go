package board_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// setWIP sets the limit of one stage in a project's settings.
func (e *env) setWIP(p *board.Project, stage string, wip int) {
	e.t.Helper()
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	for _, s := range doc["stages"].([]any) {
		if m := s.(settings.Doc); m["id"] == stage {
			m["wip"] = wip
		}
	}
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		e.t.Fatal(err)
	}
}

// A ticket whose step fails does not stop the others: the pass goes on, the ticket says the board could
// not try it with no error's text, the error is logged, and the error is returned.
func TestReconcileGoesOnPastATicketThatFails(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.setWIP(p, "analysis", 1)
	e.add(p, ready("A"), board.OriginPlan)
	e.add(p, ready("B"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	if e.stage(p, "SF-1") != "in_progress" || e.stage(p, "SF-2") != "analysis" {
		t.Fatalf("SF-1 %s, SF-2 %s", e.stage(p, "SF-1"), e.stage(p, "SF-2"))
	}
	e.add(p, ready("C"), board.OriginOperator)
	if _, err := e.b.Discard(e.ctx, p, "SF-2", "not needed", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.b.Checks = map[string]board.Check{"branch_pushed": func(context.Context, *board.CheckContext) (board.Result, error) {
		return board.Result{}, errors.New("git read failed in /var/lib/crew/canonical/shop")
	}}
	var log bytes.Buffer
	e.b.Log = slog.New(slog.NewTextHandler(&log, nil))
	err := e.b.Reconcile(e.ctx, p.Name)
	if err == nil || !strings.Contains(err.Error(), "SF-1") {
		t.Fatalf("Reconcile = %v", err)
	}
	if e.stage(p, "SF-3") != "analysis" {
		t.Fatalf("the ticket behind the failing one is in %s", e.stage(p, "SF-3"))
	}
	// The reason is fixed, since an agent reads it through get_ticket; the error is in the log.
	if w := *e.ticket(p, "SF-1").Waiting; w != board.CouldNotTry {
		t.Fatalf("SF-1 waits: %s", w)
	}
	if l := log.String(); !strings.Contains(l, "ticket=SF-1") || !strings.Contains(l, "/var/lib/crew/canonical/shop") {
		t.Fatalf("the log: %s", l)
	}
}

// A skipped ticket's condition is tested also when the project has settled to Done, which is when
// all_done is met; the ticket to analyse again makes the project run again (7.2, 7.6).
func TestALookAgainFiresOnASettledProject(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Later"), board.OriginPlan)
	e.add(p, ready("Dated"), board.OriginPlan)
	e.add(p, ready("Other"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "skip", Reason: "After the rest.",
		LookAgain: &board.LookAgain{Kind: "date", Value: "2026-12-01"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "skip", Reason: "When all is done.",
		LookAgain: &board.LookAgain{Kind: "all_done"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	// SF-3 reaches Done inside a pass, after that pass read the clocks: the pass settles the project
	// with SF-2's condition met and not yet seen.
	e.b.Checks = scripted()
	e.reconcile(p)
	e.approve(p, "SF-3", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	e.push(p, *e.ticket(p, "SF-3").Branch, map[string]string{"x.go": "package x\n"})
	e.reconcile(p)
	p, _ = e.b.Project(e.ctx, p.Name)
	if p.State != "done" || e.stage(p, "SF-3") != "done" || e.ticket(p, "SF-2").Analysis.Status != board.StatusSkipped {
		t.Fatalf("the project is %s, SF-3 %s, SF-2 %s", p.State, e.stage(p, "SF-3"), e.ticket(p, "SF-2").Analysis.Status)
	}
	// From here the server's own loop, which reconciles every project.
	all := func() {
		t.Helper()
		if err := e.b.ReconcileAll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	all()
	if st := e.ticket(p, "SF-2").Analysis.Status; st != board.StatusWaiting {
		t.Fatalf("the all_done skip on a settled project: %s", st)
	}
	if p, _ = e.b.Project(e.ctx, p.Name); p.State != "running" {
		t.Fatalf("after a ticket to analyse again the project is %s", p.State)
	}
	if st := e.ticket(p, "SF-1").Analysis.Status; st != board.StatusSkipped {
		t.Fatalf("the dated skip before its date: %s", st)
	}
	// The dated one fires on a settled project too, when its date comes.
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "skip", Reason: "Still later.",
		LookAgain: &board.LookAgain{Kind: "date", Value: "2027-06-01"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	all()
	if p, _ = e.b.Project(e.ctx, p.Name); p.State != "done" {
		t.Fatalf("settled again: %s", p.State)
	}
	e.clock.Add(60 * 24 * time.Hour)
	all()
	if st := e.ticket(p, "SF-1").Analysis.Status; st != board.StatusWaiting {
		t.Fatalf("the dated skip after its date: %s", st)
	}
}

// A notice that had passed when it was held goes ahead as soon as the hold is let go.
func TestAHoldOnAPassedNotice(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Planned"), board.OriginPlan)
	e.ok(p)
	e.add(p, ready("New"), board.OriginAgent)
	e.reconcile(p)
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "notice", Reason: "New."}, "lead"); err != nil {
		t.Fatal(err)
	}
	e.clock.Add(24*time.Hour + 5*time.Second)
	on, off := true, false
	if _, err := e.b.EditTicket(e.ctx, p, "SF-2", board.Edit{Hold: &on}, board.Operator); err != nil {
		t.Fatal(err)
	}
	e.clock.Add(time.Hour)
	if _, err := e.b.EditTicket(e.ctx, p, "SF-2", board.Edit{Hold: &off}, board.Operator); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if st := e.ticket(p, "SF-2").Analysis.Status; st != board.StatusReady {
		t.Fatalf("after its hold was let go: %s", st)
	}
}

// One logical change is one transaction (6.4): a move that fails after its first write leaves the
// ticket where it was, with no move, no check result, and no history row. The fault is a trigger on
// the history table, which fails the last of the move's writes.
func TestAMoveThatFailsLateLeavesNothing(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("A"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	conn := e.db.ConnectAs(t, db.RoleMigrate)
	if _, err := conn.Exec(e.ctx, `CREATE FUNCTION fail_move() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'the history cannot be written'; END $$;
		CREATE TRIGGER fail_move BEFORE INSERT ON changes FOR EACH ROW
		WHEN (NEW.tbl = 'tickets' AND NEW.after ? 'stage') EXECUTE FUNCTION fail_move()`); err != nil {
		t.Fatal(err)
	}
	changes := e.changes(p)
	if err := e.b.Reconcile(e.ctx, p.Name); err == nil {
		t.Fatal("a move whose history could not be written succeeded")
	}
	tk := e.ticket(p, "SF-1")
	var results int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM check_results WHERE ticket = (SELECT id FROM tickets WHERE project = $1 AND number = 1)`, p.ID).Scan(&results)
	if tk.Stage != "analysis" || tk.Branch != nil || len(e.moves(tk)) != 2 || results != 0 || e.changes(p) != changes {
		t.Fatalf("after the failed move: %s, branch %v, moves %v, %d check results, %d new history rows",
			tk.Stage, tk.Branch, e.moves(tk), results, e.changes(p)-changes)
	}
	if _, err := conn.Exec(e.ctx, `DROP TRIGGER fail_move ON changes`); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-1") != "in_progress" {
		t.Fatal("the move did not happen once the history could be written")
	}
}

// Enter checks of Analysis are evaluated on the way in from Backlog, like every stage's (7.4).
func TestTheEnterChecksOfAnalysis(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["stages"].([]any)[2].(settings.Doc)["enter"] = []any{"has_check"}
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	n := ready("No check")
	n.Checks = nil
	e.add(p, n, board.OriginPlan)
	e.add(p, ready("With a check"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	if e.stage(p, "SF-1") != "backlog" || *e.ticket(p, "SF-1").Waiting != "it has no check" || e.stage(p, "SF-2") != "analysis" {
		t.Fatalf("SF-1 %s (%v), SF-2 %s", e.stage(p, "SF-1"), e.ticket(p, "SF-1").Waiting, e.stage(p, "SF-2"))
	}
}

// A project's name is never read as another project's id, and a closed or done ticket says so when it
// is discarded again.
func TestNamesAndRefusals(t *testing.T) {
	e := newEnv(t)
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "f1c50000-0000-4000-8000-000000000001", Key: "UU", Plan: plan,
		Repo: board.Repo{Kind: "local"}}, board.Operator)
	if kindOf(err) != board.Invalid {
		t.Fatalf("a name shaped as an id: %v", err)
	}
	p := e.project("shop", "SF")
	e.add(p, ready("A"), board.OriginPlan)
	if _, err := e.b.Discard(e.ctx, p, "SF-1", "no", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Discard(e.ctx, p, "SF-1", "no", board.Operator); err == nil || err.Error() != "SF-1 is closed already" {
		t.Fatalf("discarded twice: %v", err)
	}
}

// The operator pins a model with its CLI, an effort, or both, for a role on a ticket (12.3), with its
// row in the history; a pin that is not one is refused, and a done ticket takes none.
func TestATicketCanPinAModelAndAnEffort(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Pinned"), board.OriginPlan)
	pin := map[string]any{"builder": map[string]any{"model": "claude/sonnet", "effort": "medium"}, "reviewer": map[string]any{"effort": "low"}}
	tk, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Agents: pin}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := tk.Agents["builder"].(map[string]any); b["model"] != "claude/sonnet" || b["effort"] != "medium" {
		t.Fatalf("%+v", tk.Agents)
	}
	h, err := e.b.History(e.ctx, tk)
	if err != nil || len(h) == 0 || !strings.Contains(string(h[len(h)-1].After), "claude/sonnet") {
		t.Fatalf("no history of the pin: %+v %v", h, err)
	}
	for _, bad := range []map[string]any{
		{"builder": map[string]any{"model": "sonnet"}},
		{"builder": map[string]any{"effort": "huge"}},
		{"nobody": map[string]any{"effort": "low"}},
		{"at_limit": "wait"},
	} {
		if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Agents: bad}, board.Operator); err == nil {
			t.Errorf("%v was taken as a pin", bad)
		}
	}
	// A done ticket takes no pin: a change is a new ticket.
	if _, err := e.b.Pool.Exec(e.ctx, `UPDATE tickets SET stage = 'done' WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Agents: pin}, board.Operator); err == nil {
		t.Fatal("a done ticket took a pin")
	}
}
