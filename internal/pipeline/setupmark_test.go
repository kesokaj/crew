package pipeline

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// checksFunc is a check sandbox that answers with f.
type checksFunc func(spec harness.CheckSpec) []harness.CheckResult

func (f checksFunc) ExecFresh(_ context.Context, spec harness.CheckSpec) ([]harness.CheckResult, error) {
	return f(spec), nil
}

// spoofedSetup is a command an agent named like a setup's end, and timedOut Crew's own error on it:
// the detail of its run begins as a setup's that failed does (13.9).
const (
	spoofedSetup = harness.SetupError + " ended with 1"
	timedOut     = "it ran longer than 30m0s"
)

// setupMarkKit is the project shop with SF-1 and the setup "make setup", whose check sandbox answers
// with checks; every command passes without it.
type setupMarkKit struct {
	t    *testing.T
	ctx  context.Context
	p    *Pipeline
	j    *job
	one  *board.Ticket
	main string
}

func newSetupMarkKit(t *testing.T, checks checksFunc) *setupMarkKit {
	t.Helper()
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	one, err := tools.Board.Ticket(ctx, proj, "SF-1")
	if err != nil {
		t.Fatal(err)
	}
	if checks == nil {
		checks = func(spec harness.CheckSpec) []harness.CheckResult {
			var out []harness.CheckResult
			for _, c := range spec.Cmds {
				out = append(out, harness.CheckResult{Name: c.Name, Passed: true})
			}
			return out
		}
	}
	k := &setupMarkKit{t: t, ctx: ctx, one: one, main: tools.Board.Git.Main(ctx, proj.Name),
		p: &Pipeline{Pool: tools.Pool, Board: tools.Board, Identity: tools.Board.Identity, Checks: checks, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		j: &job{doc: settings.Doc{"sandbox": settings.Doc{"setup": "make setup"}}, proj: proj}}
	if k.main == "" {
		t.Fatal("the project has no main")
	}
	return k
}

// errored records an errored check run of SF-1 with detail, marked or not, as execChecks would have.
func (k *setupMarkKit) errored(detail string, marked bool) int64 {
	k.t.Helper()
	var id int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, detail, setup_failed, started_at, ended_at)
		VALUES ($1, $2, 'exit', $3, 'error', $4, $5, clock_timestamp(), clock_timestamp()) RETURNING id`,
		k.j.proj.ID, k.one.ID, k.main, detail, marked).Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	return id
}

func (k *setupMarkKit) count(q string, args ...any) int {
	k.t.Helper()
	var n int
	if err := k.p.Pool.QueryRow(k.ctx, q, args...).Scan(&n); err != nil {
		k.t.Fatal(err)
	}
	return n
}

// The count of a ticket's check runs that ended as Crew's errors, of which three make it wait, leaves
// out a run whose setup failed by its mark, and counts a command an agent named like a setup's end
// (13.9): its errors are tried three times, not without end.
func TestNotSetupReadsTheMark(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	k.errored(harness.SetupError+" ended with 2", true)
	spoofed := k.errored(spoofedSetup+": "+timedOut, false)
	var id int64
	if n := k.count(`SELECT count(*) FROM check_runs WHERE state = 'error' AND ` + notSetup); n != 1 {
		t.Fatalf("%d errors counted", n)
	}
	if err := k.p.Pool.QueryRow(k.ctx, `SELECT id FROM check_runs WHERE state = 'error' AND `+notSetup).Scan(&id); err != nil || id != spoofed {
		t.Fatalf("counted %d, not the command named like a setup's end %d (%v)", id, spoofed, err)
	}
	// Each of the four error-count sites (errorsOn, mergeErrors, judgeErrorsOn, stageErrorsOn)
	// applies notSetup on its own query (register row 76).
	for _, purpose := range []string{board.PurposeMerge, board.PurposeJudge, board.PurposeStage} {
		for _, m := range []struct {
			detail string
			marked bool
		}{
			{harness.SetupError + " ended with 2", true},
			{spoofedSetup + ": " + timedOut, false},
		} {
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, head, stage, state, detail, setup_failed, started_at, ended_at)
				VALUES ($1, $2, $3, $4, $4, 'qa', 'error', $5, $6, clock_timestamp(), clock_timestamp())`,
				k.j.proj.ID, k.one.ID, purpose, k.main, m.detail, m.marked); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, fn := range map[string]func() (int, error){
		"errorsOn":      func() (int, error) { return k.p.errorsOn(k.ctx, k.one, k.main, board.PurposeExit) },
		"mergeErrors":   func() (int, error) { return k.p.mergeErrors(k.ctx, k.one, k.main) },
		"judgeErrorsOn": func() (int, error) { return k.p.judgeErrorsOn(k.ctx, k.one, k.main, board.PurposeJudge, "qa") },
		"stageErrorsOn": func() (int, error) { return k.p.stageErrorsOn(k.ctx, k.one, k.main, "qa", board.PurposeStage) },
	} {
		if n, err := fn(); err != nil || n != 1 {
			t.Errorf("%s = %d, %v; want 1, nil", name, n, err)
		}
	}
}

// Two check runs in a row whose setup failed have the setup run on main; two whose command an agent
// named like a setup's end do not (13.5, 13.9).
func TestWatchSetupCountsOnlyMarkedRuns(t *testing.T) {
	for _, c := range []struct {
		name   string
		detail string
		marked bool
		checks int
	}{
		{"a command named like a setup's end", spoofedSetup + ": " + timedOut, false, 0},
		{"a setup that failed", harness.SetupError + " ended with 2", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			k.errored(c.detail, c.marked)
			k.errored(c.detail, c.marked)
			if err := k.p.watchSetup(k.ctx, k.j); err != nil {
				t.Fatal(err)
			}
			k.p.wg.Wait()
			if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != c.checks {
				t.Fatalf("%d checks of the setup on main, not %d", n, c.checks)
			}
		})
	}
}

// A setup that passes on main makes a check run whose setup failed a failure of its branch, and leaves
// one whose command an agent named like a setup's end an error: it spends no fix round (13.5, 13.9).
func TestTheSetupPassingOnMainFailsOnlyMarkedRuns(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	marked := k.errored(harness.SetupError+" ended with 2", true)
	spoofed := k.errored(spoofedSetup+": "+timedOut, false)
	var id int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at)
		VALUES ($1, $2, clock_timestamp(), 'passed', now()) RETURNING id`, k.j.proj.ID, k.main).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := k.p.actOnSetup(k.ctx, k.j, id, "passed"); err != nil {
		t.Fatal(err)
	}
	state := func(id int64) string {
		var s string
		if err := k.p.Pool.QueryRow(k.ctx, `SELECT state || ' ' || detail FROM check_runs WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := state(marked); s != "failed "+harness.SetupError+" ended with 2; it passes on main, so this branch breaks it" {
		t.Errorf("the run whose setup failed: %s", s)
	}
	if s := state(spoofed); s != "error "+spoofedSetup+": "+timedOut {
		t.Errorf("the command named like a setup's end: %s", s)
	}
}

// The setup's own fix ticket fails its check where its setup still fails, and the run is marked;
// Crew's error on a command an agent named like a setup's end stays an error, unmarked, which spends
// no fix round (13.5, 13.9).
func TestTheSetupsFixTicketFailsOnlyOnTheMark(t *testing.T) {
	for _, c := range []struct {
		name   string
		result func(harness.CheckCmd) harness.CheckResult
		state  string
		marked bool
	}{
		{"a command named like a setup's end", func(c harness.CheckCmd) harness.CheckResult {
			return harness.CheckResult{Name: c.Name, Code: 124, Error: timedOut}
		}, board.CheckError, false},
		{"a setup that fails", func(c harness.CheckCmd) harness.CheckResult {
			return harness.CheckResult{Name: c.Name, Code: 2, Error: harness.SetupError + " ended with 2", Output: "npm ERR!"}
		}, board.CheckFailed, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, func(spec harness.CheckSpec) []harness.CheckResult {
				var out []harness.CheckResult
				for _, cmd := range spec.Cmds {
					out = append(out, c.result(cmd))
				}
				return out
			})
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at, acted_at, ticket)
				VALUES ($1, $2, now(), 'failed', now(), now(), $3)`, k.j.proj.ID, k.main, k.one.ID); err != nil {
				t.Fatal(err)
			}
			id, err := k.p.runChecks(k.ctx, k.j, k.one, board.PurposeExit, k.main, "", k.main,
				[]harness.CheckCmd{{Name: spoofedSetup, Run: spoofedSetup}})
			if err != nil || id == 0 {
				t.Fatalf("the check run %d: %v", id, err)
			}
			k.p.wg.Wait()
			var state string
			var marked bool
			if err := k.p.Pool.QueryRow(k.ctx, `SELECT state, setup_failed FROM check_runs WHERE id = $1`, id).Scan(&state, &marked); err != nil {
				t.Fatal(err)
			}
			if state != c.state || marked != c.marked {
				t.Fatalf("the fix ticket's check run: %s, marked %v; not %s, %v", state, marked, c.state, c.marked)
			}
		})
	}
}

// A merge check of the setup's own fix ticket that failed at its setup is run once more, and what is
// kept of it is the second run, with the second run's mark: a pass or a command's failure is not a
// setup that failed (13.9, the review of 1acf570, finding 1).
func TestTheFixTicketsMergeRunAgainKeepsItsMark(t *testing.T) {
	for _, c := range []struct {
		name  string
		again harness.CheckResult
		state string
	}{
		{"it passes", harness.CheckResult{Passed: true}, board.CheckPassed},
		{"a command fails", harness.CheckResult{Code: 1, Output: "FAIL"}, board.CheckFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			runs := 0
			k := newSetupMarkKit(t, func(spec harness.CheckSpec) []harness.CheckResult {
				runs++
				var out []harness.CheckResult
				for _, cmd := range spec.Cmds {
					r := c.again
					if runs == 1 {
						r = harness.CheckResult{Code: 2, Error: harness.SetupError + " ended with 2"}
					}
					r.Name = cmd.Name
					out = append(out, r)
				}
				return out
			})
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at, acted_at, ticket)
				VALUES ($1, $2, now(), 'failed', now(), now(), $3)`, k.j.proj.ID, k.main, k.one.ID); err != nil {
				t.Fatal(err)
			}
			id, err := k.p.runChecks(k.ctx, k.j, k.one, board.PurposeMerge, k.main, k.main, k.main,
				[]harness.CheckCmd{{Name: "make test", Run: "make test"}})
			if err != nil || id == 0 {
				t.Fatalf("the check run %d: %v", id, err)
			}
			k.p.wg.Wait()
			var state string
			var marked bool
			if err := k.p.Pool.QueryRow(k.ctx, `SELECT state, setup_failed FROM check_runs WHERE id = $1`, id).Scan(&state, &marked); err != nil {
				t.Fatal(err)
			}
			if runs != 2 || state != c.state || marked {
				t.Fatalf("after %d runs: %s, marked %v; not %s, unmarked", runs, state, marked, c.state)
			}
		})
	}
}
