package pipeline

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// A stopped build subtask followed by a finished child helper subtask still starts look_again rather
// than deadlocking, while a held ticket starts nothing (7.7, 7.11).
func TestStoppedBuildWithTrailingHelperStartsLookAgain(t *testing.T) {
	k, rr := familyKit(t)
	var err error
	if k.j.doc, err = k.p.Board.Settings(k.ctx, k.p.Pool, k.j.proj); err != nil {
		t.Fatal(err)
	}
	k.j.pipe = board.PipelineOf(k.j.doc)
	k.one.Stage = settings.StageInProgress
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}

	buildID := k.ran(kindBuild, "builder", "gemini-4-carbon", 4)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'stopped', stops = 2, title = 'Build' WHERE id = $1`, buildID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO subtasks (scope, project, ticket, parent, number, kind, role, title, state)
		VALUES ('ticket', $1, $2, $3, 2, 'helper', 'explorer', 'Read files', 'done')`,
		k.j.proj.ID, k.one.ID, buildID); err != nil {
		t.Fatal(err)
	}

	subs, err := k.p.subtasks(k.ctx, k.one)
	if err != nil {
		t.Fatal(err)
	}
	k.one.Hold = true
	if err := k.p.stage(k.ctx, k.j, k.one, subs); err != nil {
		t.Fatal(err)
	}
	if len(rr.started) != 0 {
		t.Fatalf("held ticket started %+v", rr.started)
	}

	k.one.Hold = false
	if err := k.p.stage(k.ctx, k.j, k.one, subs); err != nil {
		t.Fatal(err)
	}
	if len(rr.started) != 1 || rr.started[0].Kind != kindLookAgain {
		t.Fatalf("started = %+v, want one look_again run", rr.started)
	}
}

// A build or fix subtask that pushed a commit to the ticket's branch and recorded request_move or
// fix_done before hitting its turn/quota/timeout limit is marked done by reportedDone; an unpushed
// build or a fix still on its starting commit is not rescued (7.11, 11.3).
func TestReportedDoneRescuesPushedBuildAndFixAtTurnLimit(t *testing.T) {
	k, _ := familyKit(t)
	k.one.Stage = settings.StageInProgress
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}

	// Unpushed build: request_move recorded, but ticket has no pushed branch yet -> not rescued.
	unpushed := k.ran(kindBuild, "builder", "gemini-4-carbon", 5)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'stopped', stops = 1, reported = '{"tool": "request_move"}' WHERE id = $1`, unpushed); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE runs SET stop_reason = $2 WHERE subtask = $1`, unpushed, runs.ReasonTurns); err != nil {
		t.Fatal(err)
	}
	subs, err := k.p.subtasks(k.ctx, k.one)
	if err != nil {
		t.Fatal(err)
	}
	if ended, err := k.p.reportedDone(k.ctx, k.j, k.one, subs); err != nil || ended {
		t.Fatalf("unpushed build rescued: ended=%v err=%v", ended, err)
	}

	// Push the ticket branch: now reportedDone rescues the build subtask and marks the run IdleEnded.
	_, pushed := k.branched()
	q := quarantineGit(t, &Tools{Pool: k.p.Pool, Board: k.p.Board}, k.j.proj.Name)
	q("update-ref", "refs/heads/task/SF-1-one", pushed)
	if ended, err := k.p.reportedDone(k.ctx, k.j, k.one, subs); err != nil || !ended {
		t.Fatalf("pushed build not rescued: ended=%v err=%v", ended, err)
	}
	subs, err = k.p.subtasks(k.ctx, k.one)
	if err != nil {
		t.Fatal(err)
	}
	if subs[0].state != "done" || subs[0].stopReason != cli.ReasonOK || !subs[0].waitedOut() {
		t.Fatalf("rescued build = %+v (waitedOut=%v)", subs[0], subs[0].waitedOut())
	}

	// A fix subtask still on its starting commit is not rescued until a newer commit is pushed.
	fixID := k.ran(kindFix, "builder", "gemini-4-carbon", 2)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'waiting', stops = 1, commit = $2, reported = '{"tool": "fix_done"}' WHERE id = $1`, fixID, pushed); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE runs SET stop_reason = $2 WHERE subtask = $1`, fixID, runs.ReasonTurns); err != nil {
		t.Fatal(err)
	}
	subs, _ = k.p.subtasks(k.ctx, k.one)
	if ended, err := k.p.reportedDone(k.ctx, k.j, k.one, subs); err != nil || ended {
		t.Fatalf("unchanged-commit fix rescued: ended=%v err=%v", ended, err)
	}
	next := q("commit-tree", k.main+"^{tree}", "-p", pushed, "-m", "the fix")
	q("update-ref", "refs/heads/task/SF-1-one", next)
	if ended, err := k.p.reportedDone(k.ctx, k.j, k.one, subs); err != nil || !ended {
		t.Fatalf("pushed fix not rescued: ended=%v err=%v", ended, err)
	}
}

// Reaching limits.max_ticket_failures holds the ticket via the backend with a ticket_failure_hold
// signal instead of starting another fix or look_again; raising the limit releases a backend hold (while
// leaving an operator hold alone), and an operator hold release resets the failure count (7.7).
func TestTicketFailureCircuitBreakerHoldsAndResetsOnOperatorRelease(t *testing.T) {
	k, rr := familyKit(t)
	var err error
	if k.j.doc, err = k.p.Board.Settings(k.ctx, k.p.Pool, k.j.proj); err != nil {
		t.Fatal(err)
	}
	k.j.doc["limits"].(settings.Doc)["max_ticket_failures"] = 2
	k.j.pipe = board.PipelineOf(k.j.doc)
	k.one.Stage = settings.StageInProgress
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}

	fixID := k.ran(kindFix, "builder", "gemini-4-carbon", 6)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'done', title = 'Fix checks' WHERE id = $1`, fixID); err != nil {
		t.Fatal(err)
	}
	stopID := k.ran(kindBuild, "builder", "gemini-4-carbon", 3)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'stopped', stops = 2, title = 'Build' WHERE id = $1`, stopID); err != nil {
		t.Fatal(err)
	}

	subs, err := k.p.subtasks(k.ctx, k.one)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.p.stage(k.ctx, k.j, k.one, subs); err != nil {
		t.Fatal(err)
	}
	if len(rr.started) != 0 {
		t.Fatalf("started %+v when failure limit was reached", rr.started)
	}
	reloaded, err := k.p.Board.Ticket(k.ctx, k.j.proj, k.one.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Hold || reloaded.Stuck == nil || !strings.Contains(*reloaded.Stuck, "Held after 2 failures") {
		t.Fatalf("ticket = hold:%v stuck:%v", reloaded.Hold, reloaded.Stuck)
	}
	if sigs := k.count(`SELECT count(*) FROM signals WHERE ticket = $1 AND kind = 'ticket_failure_hold'`, k.one.ID); sigs != 1 {
		t.Fatalf("ticket_failure_hold signals = %d, want 1", sigs)
	}

	// Raising max_ticket_failures lifts the backend hold and starts look_again.
	k.j.doc["limits"].(settings.Doc)["max_ticket_failures"] = 4
	k.one = reloaded
	if err := k.p.stage(k.ctx, k.j, k.one, subs); err != nil {
		t.Fatal(err)
	}
	if k.one.Hold || len(rr.started) != 1 || rr.started[0].Kind != kindLookAgain {
		t.Fatalf("after raising limit: hold=%v started=%+v", k.one.Hold, rr.started)
	}

	// An operator hold is never lifted by releaseIfFailureLimitRaised; lifting a hold as operator resets failures.
	tr := true
	if k.one, err = k.p.Board.EditTicket(k.ctx, k.j.proj, k.one.Key, board.Edit{Hold: &tr}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if err := k.p.releaseIfFailureLimitRaised(k.ctx, k.j, k.one); err != nil || !k.one.Hold {
		t.Fatalf("operator hold lifted by backend: hold=%v err=%v", k.one.Hold, err)
	}
	fa := false
	if k.one, err = k.p.Board.EditTicket(k.ctx, k.j.proj, k.one.Key, board.Edit{Hold: &fa}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if failures, err := k.p.ticketFailuresSinceRelease(k.ctx, k.j.proj.ID, k.one); err != nil || failures != 0 {
		t.Fatalf("failures after operator release = %d (%v), want 0", failures, err)
	}
}
