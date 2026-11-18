package httpapi_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/harness"
)

// holdUntilMoved holds the pipeline's read of a ticket's subtasks, once from holds, until the board
// has moved SF-1 to stage: the pass after the judge's answer then comes only after the ticket left In
// progress. held says a read came after from.
type holdUntilMoved struct {
	server      *pgxpool.Pool
	from, stage string
	held        *atomic.Bool
	failed      *atomic.Value
}

func (h holdUntilMoved) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(data.SQL, "LEFT JOIN LATERAL (SELECT * FROM runs WHERE subtask = s.id") {
		return ctx
	}
	stage := func() (from bool, moved bool) {
		if err := h.server.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM check_runs WHERE `+h.from+`),
			EXISTS (SELECT 1 FROM tickets WHERE number = 1 AND stage = $1)`, h.stage).Scan(&from, &moved); err != nil && ctx.Err() == nil {
			h.failed.Store(err.Error())
		}
		return from, moved
	}
	from, moved := stage()
	if !from {
		return ctx
	}
	// The first read after from comes after the move, by itself or by this hold.
	h.held.Store(true)
	if moved {
		return ctx
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx
		case <-tick.C:
			if _, moved := stage(); moved {
				return ctx
			}
		}
	}
}

func (holdUntilMoved) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A builder whose run ended while it waited is marked taken up also when its ticket left In progress
// before the pipeline's next pass came to it (BUG-028, m16_test.go's long check of session 82): the
// merge that the judge's answer let start can end and the board move the ticket to Done first, and in a
// project with a stage after In progress the board moves it there on the judge's answer alone.
// Serial: the race between the board's move and the pipeline's next read flakes under parallel load.
func TestABuilderTakenUpIsMarkedWhenItsTicketLeftInProgressFirst(t *testing.T) {
	for _, c := range []struct {
		way, from, stage string
		stages           []string
	}{
		{"merged", "purpose = 'merge'", "done", []string{}},
		{"later stage", "purpose = 'judge' AND state = 'passed'", "review", []string{"review"}},
	} {
		t.Run(c.way, func(t *testing.T) {
			k := newTicketKit(t)
			var later atomic.Int64
			k.driver.Now = func() time.Time { return time.Now().Add(time.Duration(later.Load())) }
			k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
				if kind == "analysis" {
					k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "checks": []map[string]any{
						{"kind": "cmd", "spec": "test -f 1.txt"}, {"kind": "judge", "spec": "1.txt names its ticket."}}})
					return true
				}
				return false
			})
			inner := k.custom
			k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
				if kind == "judge" {
					return harness.Script{Lines: answerLines(spec, "1. MET: 1.txt says SF-1.")}, true
				}
				return inner(kind, spec, p)
			}
			// The first check of the built head holds until the monitor ended the builder's run.
			release := make(chan struct{})
			var once sync.Once
			unhold := func() { once.Do(func() { close(release) }) }
			var holding atomic.Bool
			k.f.Check = func(spec harness.CheckSpec, cmd harness.CheckCmd) harness.CheckResult {
				if slices.Contains(tarNames(spec.Tree), "1.txt") && holding.CompareAndSwap(false, true) {
					<-release
				}
				return checkSandbox(spec, cmd)
			}
			var held atomic.Bool
			var failed atomic.Value
			cfg := k.pipe.Pool.Config()
			cfg.ConnConfig.Tracer = holdUntilMoved{server: k.d.Server, from: c.from, stage: c.stage, held: &held, failed: &failed}
			pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			k.pipe.Pool = pool
			k.shop(c.stages, nil, "one")
			stop := k.run()
			defer stop()
			// Let go of the check on every way out, before stop waits for it: a test that fails fails by
			// its assertion, not by go test's timeout.
			defer unhold()
			builder := `FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'build'`
			k.until("the check holds and the builder's run idles", func() bool {
				return holding.Load() && k.count(`SELECT count(*) `+builder+` AND r.state = 'idle'`) == 1
			})
			later.Store(int64(11 * time.Minute))
			k.until("the monitor ended the builder's run", func() bool {
				return k.count(`SELECT count(*) `+builder+` AND r.state = 'ended' AND r.stop_detail = 'no next turn came'`) == 1
			})
			later.Store(0)
			unhold()
			k.until("SF-1 in "+c.stage, func() bool { return k.stageOf("SF-1") == c.stage })
			if err := failed.Load(); err != nil {
				t.Fatalf("the hold's read of the board failed: %v", err)
			}
			if !held.Load() {
				t.Fatal("no read of the pipeline came after the judge's answer: the test did not set up its order")
			}
			k.until("the run taken up is marked so", func() bool {
				return k.count(`SELECT count(*) `+builder+` AND r.stop_detail = 'no next turn came; Crew took up its turn after'`) == 1
			})
			if n := k.count(`SELECT count(*) ` + builder); n != 1 {
				t.Fatalf("the builder had %d runs, not its one", n)
			}
		})
	}
}
