package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runspec"
)

// Pause all (7.9): no new run starts and no turn that is not a running run's own; a running run
// finishes, its goal loop's next turn too. Resume lets runs start again.
func TestPauseAllStartsNothingAndLetsRunsFinish(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	run := k.start(StartRequest{})
	v := k.wait(run, "idle")
	if _, err := k.d.PauseAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "Another."}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a run started while paused: %v", err)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "From the operator."}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a turn started while paused: %v", err)
	}
	// The run's own goal loop finishes it.
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "One more round.", Auto: true, Finishing: true}); err != nil {
		t.Fatalf("a running run could not finish: %v", err)
	}
	k.wait(run, "idle")
	if err := k.d.ResumeAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	k.start(StartRequest{Prompt: "After the pause."})
}

// A restart for security updates that waited 7 days (16.10) holds like Pause all: nothing new starts,
// and a running run's own turns finish it. While the machine goes down, after the server's yes, not
// even those.
func TestARestartThatWaitedLetsRunsFinish(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	run := k.start(StartRequest{})
	v := k.wait(run, "idle")
	if _, err := k.db.Server.Exec(ctx, `UPDATE crew_state SET reboot_wanted_at = now() - interval '7 days', restart_wait = now()`); err != nil {
		t.Fatal(err)
	}
	if why := hold.Held(ctx, k.d.Pool); why != hold.RestartWaitWhy {
		t.Fatalf("held: %q", why)
	}
	if _, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "Another."}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a run started while a restart waits: %v", err)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "From the operator."}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a turn started while a restart waits: %v", err)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "One more round.", Auto: true, Finishing: true}); err != nil {
		t.Fatalf("a running run could not finish: %v", err)
	}
	k.wait(run, "idle")
	if _, err := k.db.Server.Exec(ctx, `UPDATE crew_state SET restart_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "Again.", Auto: true, Finishing: true}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a turn started while the machine goes down: %v", err)
	}
}

// Stop all (7.9): every run ends now with its conversation kept, and its subtask waits; nothing
// continues by itself while Crew is stopped, not even the goal loop; after Resume the monitor
// continues it, and that is no continue of Crew's own.
func TestStopAllEndsRunsAndResumeContinuesThem(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run := k.start(StartRequest{})
	v := k.wait(run, "working")
	st, n, err := k.d.StopAll(ctx, "operator")
	if err != nil || n != 1 || st.Why != "stop" {
		t.Fatalf("%+v %d %v", st, n, err)
	}
	ended := k.wait(run, "ended")
	if ended.StopReason != ReasonOperator {
		t.Fatalf("stop reason %s", ended.StopReason)
	}
	var state, waitFor string
	var autos int
	read := func() {
		k.db.Server.QueryRow(ctx, `SELECT state, coalesce(wait_for, ''), auto_continues FROM subtasks WHERE id = $1`, v.Subtask).Scan(&state, &waitFor, &autos)
	}
	read()
	if state != "waiting" || waitFor != "held" {
		t.Fatalf("the subtask: %s %s", state, waitFor)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "x", Auto: true, Finishing: true}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a stopped run's goal loop went on: %v", err)
	}
	k.d.Tick(ctx)
	if got := len(started(k)) + len(continued(k)); got != 1 {
		t.Fatalf("something started while stopped: %d", got)
	}
	if err := k.d.ResumeAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	read()
	if state == "waiting" || autos != 0 {
		t.Fatalf("after Resume: %s %s, %d continues of Crew's own", state, waitFor, autos)
	}
	if s, _ := hold.Read(ctx, k.db.Server); s.Held() != "" {
		t.Fatalf("still held: %+v", s)
	}
}

// A state that cannot be read holds: the brake never fails open.
func TestAHoldThatCannotBeReadHolds(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	owner, err := db.Open(ctx, k.db.Conn(db.RoleMigrate))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, `ALTER TABLE crew_state RENAME TO crew_state_gone`); err != nil {
		t.Fatal(err)
	}
	if why := hold.Held(ctx, k.db.Server); why == "" {
		t.Fatal("an unreadable state let runs start")
	}
}

// Stop all and a run that starts at the same moment: the run read the state before Stop all wrote
// it, and Stop all read the runs that go before the run was among them. The run reads the state
// again once it is registered, so it ends before its microVM starts, waits for Resume, and continues
// after it. A trigger writes Stop all's state as the run is recorded, which is that moment.
func TestARunThatStartsAsStopAllComesEndsToo(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	owner, err := db.Open(ctx, k.db.Conn(db.RoleMigrate))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, `CREATE FUNCTION stop_all_now() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN UPDATE crew_state SET paused_at = now(), paused_by = 'operator', why = 'stop' WHERE id = 1; RETURN NEW; END $$;
		CREATE TRIGGER stop_all_now AFTER INSERT ON runs FOR EACH ROW EXECUTE FUNCTION stop_all_now()`); err != nil {
		t.Fatal(err)
	}
	run, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "Begin."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER stop_all_now ON runs`); err != nil {
		t.Fatal(err)
	}
	v := k.wait(run, "ended")
	if v.StopReason != ReasonOperator || len(started(k)) != 0 {
		t.Fatalf("stop reason %s, %d microVMs started", v.StopReason, len(started(k)))
	}
	var state, waitFor string
	k.db.Server.QueryRow(ctx, `SELECT state, coalesce(wait_for, '') FROM subtasks WHERE id = $1`, v.Subtask).Scan(&state, &waitFor)
	if state != "waiting" || waitFor != "held" {
		t.Fatalf("the subtask: %s %s", state, waitFor)
	}
	if err := k.d.ResumeAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	if got := len(started(k)) + len(continued(k)); got != 1 {
		t.Fatalf("after Resume %d runs started or continued, want the one", got)
	}
}

// A paused project, and a ticket on hold, start nothing (15.2, 7.9): no waiting subtask of theirs
// continues by itself, whatever it waits for, and after Resume, or once the hold is let go, it goes on.
func TestAPausedProjectOrAHeldTicketContinuesNothingByItself(t *testing.T) {
	for _, c := range []struct {
		name, waitFor, until string
		next                 bool
	}{
		{name: "retry", waitFor: "retry"},
		{name: "restart", waitFor: "restart"},
		{name: "a limit's reset", waitFor: "quota:agy", until: "now() - interval '1 minute'"},
		{name: "a sign-in", waitFor: "login:agy"},
		{name: "a next choice", waitFor: "model:agy", next: true},
		{name: "Stop all's Resume", waitFor: "held"},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, ticket := ticketKit(t)
			ctx := context.Background()
			k.play(harness.Script{Recording: agyRun, Exit: runspec.Exit{Reason: cli.ReasonError, Exit: 1, Detail: "broke"}})
			run, sub := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock"})
			k.wait(run, "ended")
			until := "NULL"
			if c.until != "" {
				until = c.until
			}
			// Older than the CLI's last good sign-in, so a subtask that waits for one may go on.
			if _, err := k.db.Server.Exec(ctx, `UPDATE subtasks SET state = 'waiting', wait_for = $2, wait_until = `+until+`,
				updated_at = now() - interval '1 hour' WHERE id = $1`, sub, c.waitFor); err != nil {
				t.Fatal(err)
			}
			if c.next {
				k.d.SetNext(func(context.Context, int64) (*ContinueRequest, error) {
					return &ContinueRequest{CLI: "agy", Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-high"}, nil
				})
			}
			runsOf := func() int { return k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub) }
			for _, held := range []struct{ what, on, off string }{
				{"the project paused", `UPDATE projects SET state = 'paused' WHERE id = (SELECT project FROM tickets WHERE id = $1)`,
					`UPDATE projects SET state = 'running' WHERE id = (SELECT project FROM tickets WHERE id = $1)`},
				{"the ticket on hold", `UPDATE tickets SET hold = true WHERE id = $1`, `UPDATE tickets SET hold = false WHERE id = $1`},
			} {
				if _, err := k.db.Server.Exec(ctx, held.on, ticket); err != nil {
					t.Fatal(err)
				}
				k.d.Tick(ctx)
				if n := runsOf(); n != 1 {
					t.Fatalf("with %s, the subtask continued by itself: %d runs", held.what, n)
				}
				if k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND state = 'waiting' AND wait_for = $2 AND auto_continues = 0`, sub, c.waitFor) != 1 {
					t.Fatalf("with %s, the subtask no longer waits as it did", held.what)
				}
				if _, err := k.db.Server.Exec(ctx, held.off, ticket); err != nil {
					t.Fatal(err)
				}
			}
			k.play(harness.Script{Recording: agyRun})
			k.d.Tick(ctx)
			if n := runsOf(); n != 2 {
				t.Fatalf("after Resume the subtask did not go on: %d runs", n)
			}
		})
	}
}

// A subtask that waits for its group's limit continues before its stored wait_until once Crew's own
// login check reads that group free after the wait began; a run's report or a check from before the
// wait began never ends the wait early (12.7).
func TestAQuotaWaitEndsWhenALoginCheckReadsTheGroupFree(t *testing.T) {
	k, ticket := ticketKit(t)
	ctx := context.Background()
	k.play(harness.Script{
		Recording: agyRun,
		Exit:      runspec.Exit{Reason: cli.ReasonQuota, Exit: 3, Detail: "agy reached a limit: RESOURCE_EXHAUSTED"},
	})
	run, sub := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: "gemini-3.8-flash", Effort: "low"})
	k.wait(run, "ended")
	// The wait began 20 minutes ago with a stored reset an hour from now; the run's own "limit"
	// window is past RetryAfter (15 minutes), so only what is written below can hold or free the group.
	if _, err := k.db.Server.Exec(ctx, `UPDATE subtasks SET wait_until = now() + interval '1 hour',
		updated_at = now() - interval '20 minutes' WHERE id = $1`, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `UPDATE quota SET reached_until = now() + interval '1 hour',
		read_at = now() - interval '20 minutes' WHERE cli = 'agy' AND grp = 'gemini'`); err != nil {
		t.Fatal(err)
	}
	runsOf := func() int { return k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub) }

	// A run's own report never ends a wait early.
	if err := models.RecordLimits(ctx, k.db.Server, "agy", []cli.Limit{
		{Group: "gemini", Window: "5h", Used: 0.1},
		{Group: "gemini", Window: "weekly", Used: 0.2},
	}, "run"); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	if n := runsOf(); n != 1 {
		t.Fatalf("a run's report ended the wait early: %d runs", n)
	}

	// A login check from before the subtask began to wait does not end the wait.
	if err := models.RecordLimits(ctx, k.db.Server, "agy", []cli.Limit{
		{Group: "gemini", Window: "5h", Used: 0.1},
		{Group: "gemini", Window: "weekly", Used: 0.2},
	}, "login check"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `UPDATE quota SET read_at = now() - interval '25 minutes' WHERE cli = 'agy' AND grp = 'gemini'`); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	if n := runsOf(); n != 1 {
		t.Fatalf("a login check from before the wait ended it: %d runs", n)
	}

	// A login check after the wait began while a window of the group is still reached keeps waiting.
	if err := models.RecordLimits(ctx, k.db.Server, "agy", []cli.Limit{
		{Group: "gemini", Window: "5h", Used: 0.1},
		{Group: "gemini", Window: "weekly", Used: 1, Reached: true, ResetsAt: time.Now().Add(time.Hour)},
	}, "login check"); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	if n := runsOf(); n != 1 {
		t.Fatalf("a reached window let the subtask continue: %d runs", n)
	}

	// A login check after the wait began reads every window of the group free: the next tick continues it.
	if err := models.RecordLimits(ctx, k.db.Server, "agy", []cli.Limit{
		{Group: "gemini", Window: "5h", Used: 0.1},
		{Group: "gemini", Window: "weekly", Used: 0.2},
	}, "login check"); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun})
	k.d.Tick(ctx)
	if n := runsOf(); n != 2 {
		t.Fatalf("after the login check read the group free, %d runs, want 2", n)
	}
	if k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND auto_continues = 1`, sub) != 1 {
		t.Fatal("auto_continues was not counted")
	}
}
