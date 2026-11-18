package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// setupErrors records n checks of the setup on main that could not tell, acted on, before now.
func (k *setupMarkKit) setupErrors(n int) {
	k.t.Helper()
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, detail, decided_at, acted_at)
		SELECT $1, $2, now() - interval '1 minute' * g, 'error', 'the check sandbox did not start', now(), now()
		FROM generate_series(1, $3::int) g`, k.j.proj.ID, k.main, n); err != nil {
		k.t.Fatal(err)
	}
}

// marked records an errored check run of SF-1 for purpose and stage on main whose setup failed.
func (k *setupMarkKit) marked(purpose, stage string) int64 {
	k.t.Helper()
	var id int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, stage, commit, head, state, detail, setup_failed, started_at, ended_at)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $5, 'error', $6, true, clock_timestamp(), clock_timestamp()) RETURNING id`,
		k.j.proj.ID, k.one.ID, purpose, stage, k.main, harness.SetupError+" ended with 2").Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	return id
}

func (k *setupMarkKit) stuck() string {
	k.t.Helper()
	var s *string
	if err := k.p.Pool.QueryRow(k.ctx, `SELECT stuck FROM tickets WHERE id = $1`, k.one.ID).Scan(&s); err != nil {
		k.t.Fatal(err)
	}
	return derefS(s)
}

// A check run whose setup failed is left out of Crew's three tries while Crew looks after the setup
// on main, and counts in them once Crew has stopped trying there, until a check of the setup on main
// passes or fails again; every count of Crew's errors reads it so (13.5, 13.9, since 1.12).
func TestAMarkedRunCountsOnceCrewStoppedTrying(t *testing.T) {
	for _, c := range []struct {
		name, purpose, stage string
		count                func(k *setupMarkKit) (int, error)
	}{
		{"errorsOn", board.PurposeExit, "", func(k *setupMarkKit) (int, error) {
			return k.p.errorsOn(k.ctx, k.one, k.main, board.PurposeExit, board.PurposeTest)
		}},
		{"mergeErrors", board.PurposeMerge, "", func(k *setupMarkKit) (int, error) { return k.p.mergeErrors(k.ctx, k.one, k.main) }},
		{"judgeErrorsOn", board.PurposeJudge, "", func(k *setupMarkKit) (int, error) {
			return k.p.judgeErrorsOn(k.ctx, k.one, k.main, board.PurposeJudge, "")
		}},
		{"stageErrorsOn", board.PurposeStage, "docs", func(k *setupMarkKit) (int, error) {
			return k.p.stageErrorsOn(k.ctx, k.one, k.main, "docs", board.PurposeStage)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			count := func() int {
				t.Helper()
				n, err := c.count(k)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			for range 3 {
				k.marked(c.purpose, c.stage)
			}
			k.setupErrors(2)
			if n := count(); n != 0 {
				t.Fatalf("%d errors counted while Crew still tries the setup on main", n)
			}
			k.setupErrors(1)
			if n := count(); n != 3 {
				t.Fatalf("%d errors counted after Crew stopped trying the setup on main, want 3", n)
			}
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at, acted_at)
				VALUES ($1, $2, now() + interval '1 minute', 'passed', now(), now())`, k.j.proj.ID, k.main); err != nil {
				t.Fatal(err)
			}
			if n := count(); n != 0 {
				t.Fatalf("%d errors counted after a check of the setup on main passed again", n)
			}
		})
	}
}

// After a merge on GitHub, the check on main whose setup failed three times makes the ticket wait once
// Crew has stopped trying the setup on main, and is started again before (13.5, since 1.12).
func TestAnOutsideCheckWhoseSetupFailsWaitsOnceCrewStoppedTrying(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		k := newSetupMarkKit(t, nil)
		var last int64
		for range 3 {
			last = k.marked(board.PurposeOutside, "")
		}
		if stopped {
			k.setupErrors(3)
		}
		if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO pull_requests (ticket, project, repo, branch, state, outside, outside_check)
			VALUES ($1, $2, 'o/shop', 'sf-1', 'open', 'merged', $3)`, k.one.ID, k.j.proj.ID, last); err != nil {
			t.Fatal(err)
		}
		if err := k.p.outsideMerges(k.ctx, k.j); err != nil {
			t.Fatal(err)
		}
		var check *int64
		if err := k.p.Pool.QueryRow(k.ctx, `SELECT outside_check FROM pull_requests WHERE ticket = $1`, k.one.ID).Scan(&check); err != nil {
			t.Fatal(err)
		}
		stuck := k.stuck()
		switch {
		case !stopped && (check != nil || stuck != ""):
			t.Fatalf("while Crew tries the setup: check %v, stuck %q; want it started again", check, stuck)
		case stopped && (check == nil || !strings.HasPrefix(stuck, "Crew could not run its checks on main after the merge on GitHub three times: "+harness.SetupError)):
			t.Fatalf("after Crew stopped trying: check %v, stuck %q; want it to wait with the setup's end", check, stuck)
		}
	}
}

// endRuns is a run driver that records the runs it was asked to end.
type endRuns struct {
	Runs
	ended []string
}

func (r *endRuns) End(_ context.Context, run, _, _ string) error {
	r.ended = append(r.ended, run)
	return nil
}

// A decision's check on main whose setup failed three times is not applied once Crew has stopped
// trying the setup on main: the ticket waits and the lead's run ends; before, it is asked again
// (7.6, 13.5, since 1.12).
func TestABaseCheckWhoseSetupFailsWaitsOnceCrewStoppedTrying(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		k := newSetupMarkKit(t, nil)
		ended := &endRuns{}
		k.p.Runs = ended
		s := &sub{run: "00000000-0000-4000-8000-0000000000d1", created: time.Now().Add(-time.Minute)}
		var last int64
		for range 3 {
			last = k.marked(board.PurposeBase, "")
		}
		if stopped {
			k.setupErrors(3)
		}
		r := &report{BaseRun: last}
		if _, _, err := k.p.baseCheck(k.ctx, k.j, k.one, s, r, []board.CheckSpec{{Kind: "cmd", Spec: "true"}}); err != nil {
			t.Fatal(err)
		}
		stuck := k.stuck()
		switch {
		case !stopped && (r.BaseRun != 0 || stuck != "" || len(ended.ended) != 0):
			t.Fatalf("while Crew tries the setup: base run %d, stuck %q, ended %v; want it asked again", r.BaseRun, stuck, ended.ended)
		case stopped && (len(ended.ended) != 1 || !strings.HasPrefix(stuck, "Crew could not run its checks on main three times: "+harness.SetupError)):
			t.Fatalf("after Crew stopped trying: stuck %q, ended %v; want it to wait with the setup's end", stuck, ended.ended)
		}
	}
}

// watchSetup reads the stop as notSetup does: two check runs in a row whose setup failed start a
// check of the setup on main after two that could not tell, and none after three, where the news says
// what goes on and what waits (13.5, since 1.12).
func TestWatchSetupStopsWhereTheCountsDo(t *testing.T) {
	for _, couldNot := range []int{2, 3} {
		k := newSetupMarkKit(t, nil)
		k.setupErrors(couldNot)
		k.marked(board.PurposeExit, "")
		k.marked(board.PurposeExit, "")
		if err := k.p.watchSetup(k.ctx, k.j); err != nil {
			t.Fatal(err)
		}
		k.p.wg.Wait()
		want := couldNot
		if couldNot < maxSetupErrors {
			want++
		}
		if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != want {
			t.Fatalf("after %d that could not tell: %d checks of the setup on main, want %d", couldNot, n, want)
		}
		news := k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed' AND detail->>'text' = $1`,
			"Crew could not run the setup of shop on main 3 times and stops trying: new runs start without waiting for it, "+
				"a check that ends at the setup counts as Crew's error, and a run that already waits for the setup waits on")
		if stopped := couldNot >= maxSetupErrors; (news == 1) != stopped {
			t.Fatalf("after %d that could not tell: %d news that Crew stops trying", couldNot, news)
		}
	}
}

// checkOf records an ended check run of ticket for purpose on main in state, whose setup did not fail.
func (k *setupMarkKit) checkOf(ticket int64, purpose, state string) {
	k.t.Helper()
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp(), clock_timestamp())`, k.j.proj.ID, ticket, purpose, k.main, state); err != nil {
		k.t.Fatal(err)
	}
}

// Two check runs in a row of one open ticket whose setup failed start a check of the setup on main,
// through them, also where another ticket's checks ran between them and last; a check of its own that
// passed between them breaks the two, a judge's answer does not, and a closed ticket's two, such as the
// setup's closed fix ticket's, or two before the last decided check start none (13.5, since 1.12: the
// security review of 1.12's change, finding 1, and its second round).
func TestTwoMarkedChecksOfOneTicketStartACheckOnMain(t *testing.T) {
	for _, c := range []struct {
		name    string
		between string // what SF-1 ran between its two marked runs: "" for nothing of its own
		closed  bool   // SF-1 is closed, as the setup's fix ticket that was closed
		decided bool   // a check of the setup on main passed after the two
		started int
	}{
		{"another ticket's runs between", "", false, false, 1},
		{"a check of its own that passed between", board.PurposeExit, false, false, 0},
		{"a judge's answer between", board.PurposeJudge, false, false, 1},
		{"a stage judge's answer between", board.PurposeStageJudge, false, false, 1},
		{"a closed ticket", "", true, false, 0},
		{"two before the last decided check", "", false, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			two, err := k.p.Board.AddTicket(k.ctx, k.j.proj.Name, board.NewTicket{Title: "Two", Why: "w", What: "w", Goal: "g",
				Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
			if err != nil {
				t.Fatal(err)
			}
			if c.closed {
				if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE id = $1`, k.one.ID); err != nil {
					t.Fatal(err)
				}
			}
			k.marked(board.PurposeMerge, "")
			k.checkOf(two.ID, board.PurposeExit, board.CheckPassed)
			if c.between != "" {
				k.checkOf(k.one.ID, c.between, board.CheckPassed)
			}
			k.marked(board.PurposeMerge, "")
			k.checkOf(two.ID, board.PurposeExit, board.CheckPassed)
			before := 0
			if c.decided {
				if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at, acted_at)
					VALUES ($1, $2, clock_timestamp(), 'passed', now(), now())`, k.j.proj.ID, k.main); err != nil {
					t.Fatal(err)
				}
				before = 1
			}
			if err := k.p.watchSetup(k.ctx, k.j); err != nil {
				t.Fatal(err)
			}
			k.p.wg.Wait()
			if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID) - before; n != c.started {
				t.Fatalf("%d checks of the setup on main started, want %d", n, c.started)
			}
			// The check's through covers the two, so a pass there makes them the branch's failures.
			if n := k.count(`SELECT count(*) FROM check_runs WHERE ticket = $1 AND setup_failed
				AND started_at > (SELECT max(through) FROM setup_checks WHERE project = $2)`, k.one.ID, k.j.proj.ID); c.started == 1 && n != 0 {
				t.Fatalf("%d of the two after the check's through", n)
			}
		})
	}
}

// failQuery is a query tracer that ends every query whose text holds marker before it is sent, and,
// when arg is not "", that is given arg.
type failQuery struct{ marker, arg string }

func (f failQuery) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, f.marker) && (f.arg == "" || slices.ContainsFunc(data.Args, func(a any) bool { return fmt.Sprint(a) == f.arg })) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}

func (failQuery) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// failing gives the kit's pipeline a pool on which every query whose text holds marker fails.
func (k *setupMarkKit) failing(marker string) {
	k.t.Helper()
	k.p.Pool = failingPool(k.t, k.p.Pool, marker)
}

// failingPool is a pool to pool's database on which every query whose text holds marker fails.
func failingPool(t *testing.T, pool *pgxpool.Pool, marker string) *pgxpool.Pool {
	t.Helper()
	return failingPoolFor(t, pool, marker, "")
}

// failingPoolFor is failingPool for the queries given arg alone, or every one when arg is "".
func failingPoolFor(t *testing.T, pool *pgxpool.Pool, marker, arg string) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = failQuery{marker: marker, arg: arg}
	failing, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(failing.Close)
	return failing
}

// The query of one ticket's two is asked only when the project's two did not start the check of the
// setup on main: where it fails, watchSetup says so and starts nothing, and where the project's two
// start the check, its failure holds nothing back (13.5, since 1.12: the security review of 1.12's
// change, second round, finding 2).
func TestTheQueryOfOneTicketsTwoHoldsBackNoOtherStart(t *testing.T) {
	for _, projectTwo := range []bool{false, true} {
		k := newSetupMarkKit(t, nil)
		k.failing("PARTITION BY c.ticket")
		k.marked(board.PurposeMerge, "")
		if !projectTwo {
			k.checkOf(k.one.ID, board.PurposeJudge, board.CheckPassed)
		}
		k.marked(board.PurposeMerge, "")
		if !projectTwo {
			k.checkOf(k.one.ID, board.PurposeJudge, board.CheckPassed)
		}
		err := k.p.watchSetup(k.ctx, k.j)
		k.p.wg.Wait()
		n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID)
		switch {
		case !projectTwo && (err == nil || !strings.Contains(err.Error(), "context canceled") || n != 0):
			t.Fatalf("the failed query of one ticket's two: %v, %d checks of the setup on main", err, n)
		case projectTwo && (err != nil || n != 1):
			t.Fatalf("the project's two: %v, %d checks of the setup on main, want one", err, n)
		}
	}
}

// A query of whether Crew has stopped trying the setup on main that fails is said, and starts no
// check there, though two check runs in a row ended at the setup (BUG-012's rule, since 1.12).
func TestAFailedQueryOfTheStopStartsNoCheck(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	k.failing("FROM projects p WHERE p.id = $1")
	k.marked(board.PurposeMerge, "")
	k.marked(board.PurposeMerge, "")
	err := k.p.watchSetup(k.ctx, k.j)
	k.p.wg.Wait()
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); err == nil || !strings.Contains(err.Error(), "context canceled") || n != 0 {
		t.Fatalf("the failed query of the stop: %v, %d checks of the setup on main", err, n)
	}
}

// When a ticket that was the setup's fix ticket (in setup_checks) is merged on GitHub (stage 'done')
// and two of its PurposeOutside check runs fail at the setup, watchSetup starts checkSetup once (both
// when no other ticket's runs are between them and when another ticket's check run is between them
// via ticketTwice) with through >= the second run's started_at, and a later pass with no new failing
// run starts no second check on main; meanwhile a closed ticket or non-outside runs of a setup fix
// ticket start none (13.5, register row 12, CREW-35).
func TestTheSetupsFixTicketStartsACheckOnMainAfterAMergeFromGitHub(t *testing.T) {
	for _, between := range []bool{false, true} {
		name := "in_a_row"
		if between {
			name = "another_tickets_run_between"
		}
		t.Run(name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			two, err := k.p.Board.AddTicket(k.ctx, k.j.proj.Name, board.NewTicket{Title: "Two", Why: "w", What: "w", Goal: "g",
				Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
			if err != nil {
				t.Fatal(err)
			}
			// SF-1 was the setup's fix ticket from an earlier failure, and was then merged on GitHub (stage 'done').
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, ticket, decided_at, acted_at)
				VALUES ($1, $2, clock_timestamp() - interval '1 minute', 'failed', $3, now(), now())`, k.j.proj.ID, k.main, k.one.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'done' WHERE id = $1`, k.one.ID); err != nil {
				t.Fatal(err)
			}
			k.marked(board.PurposeOutside, "")
			if between {
				k.checkOf(two.ID, board.PurposeExit, board.CheckPassed)
			}
			second := k.marked(board.PurposeOutside, "")
			if between {
				// Even when the first query returns no row (or an older row), ticketTwice advances latest.
				k.checkOf(two.ID, board.PurposeExit, board.CheckPassed)
				k.checkOf(two.ID, board.PurposeExit, board.CheckPassed)
			}
			if err := k.p.watchSetup(k.ctx, k.j); err != nil {
				t.Fatal(err)
			}
			k.p.wg.Wait()
			if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != 2 {
				t.Fatalf("%d setup_checks rows, want 2", n)
			}
			if n := k.count(`SELECT count(*) FROM check_runs WHERE id = $1 AND started_at <= (SELECT max(through) FROM setup_checks WHERE project = $2)`, second, k.j.proj.ID); n != 1 {
				t.Fatal("the new setup_checks row's through did not advance past the second outside run")
			}
			// Mark the new setup_checks row acted on, then verify a second pass with no new run starts no new check.
			if _, err := k.p.Pool.Exec(k.ctx, `UPDATE setup_checks SET state = 'passed', decided_at = now(), acted_at = now() WHERE acted_at IS NULL`); err != nil {
				t.Fatal(err)
			}
			if err := k.p.watchSetup(k.ctx, k.j); err != nil {
				t.Fatal(err)
			}
			k.p.wg.Wait()
			if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != 2 {
				t.Fatalf("after second pass with no new run: %d setup_checks rows, want 2", n)
			}
		})
	}

	// Non-outside runs of a setup fix ticket and outside runs of a closed ticket start none.
	k := newSetupMarkKit(t, nil)
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, ticket, decided_at, acted_at)
		VALUES ($1, $2, clock_timestamp() - interval '1 minute', 'failed', $3, now(), now())`, k.j.proj.ID, k.main, k.one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}
	k.marked(board.PurposeExit, "")
	k.marked(board.PurposeExit, "")
	if err := k.p.watchSetup(k.ctx, k.j); err != nil {
		t.Fatal(err)
	}
	k.p.wg.Wait()
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != 1 {
		t.Fatalf("non-outside runs of a setup fix ticket started a check: %d", n)
	}
}
