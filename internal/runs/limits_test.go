package runs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
)

// step is one agent_response of agy with the usage of that one call to the model (spike item 39).
func step(i int, in, out, cached int64) []byte {
	return []byte(fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":"%s","step_index":%d,"state":"DONE","step_type":"agent_response","text_delta":"working","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_tokens":%d}}}`,
		agyConv, i, in, out, cached))
}

// longTurn is a turn that reports its steps and then keeps going until it is stopped.
func longTurn(steps ...[]byte) harness.Script {
	lines := [][]byte{[]byte(`{"event":"init","conversation_id":"` + agyConv + `","init":{"model":"claude-opus-5-5-high"}}`)}
	return harness.Script{Lines: append(lines, steps...), Hold: true}
}

func limitsKit(t *testing.T, l Limits) (*kit, int64) {
	k, ticket := ticketKit(t)
	k.d.LimitsFor = func(context.Context, int64) (Limits, error) { return l, nil }
	return k, ticket
}

// No limit is in dollars (7.7, since 0.53): a run's usage, however large, stops nothing and warns of
// nothing, and its ticket passes no budget. A run is held to its turns, its time, and the watchdog.
func TestARunsUsageStopsNothing(t *testing.T) {
	k, ticket := limitsKit(t, DefaultLimits())
	ctx := context.Background()
	// Before 0.53 these steps were an estimated $48 at Opus's prices: past a run's budget of $5 and a
	// ticket's of $3.
	k.play(longTurn(step(1, 1_000_000, 1_000_000, 0), step(2, 1_000_000, 1_000_000, 0), step(3, 10_000, 1_000, 0)))
	run, _ := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: "claude-opus-5-5", Effort: "high"})
	k.wait(run, "working")
	waitFor(t, func() bool {
		return k.count(`SELECT count(*) FROM events WHERE run = $1 AND type = 'message'`, run) == 3
	})
	if v, _ := k.d.Get(ctx, run); v.State == "ended" {
		t.Fatalf("the run ended: %s %s", v.StopReason, v.StopDetail)
	}
	var warnings string
	k.db.Server.QueryRow(ctx, `SELECT coalesce(warnings::text, '') FROM runs WHERE id = $1`, run).Scan(&warnings)
	if strings.Contains(warnings, "budget") || strings.Contains(warnings, "$") {
		t.Fatalf("warnings %s", warnings)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE id = $1 AND over_budget IS NOT NULL`, ticket); n != 0 {
		t.Fatal("the ticket was marked over its budget")
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'over_budget'`); n != 0 {
		t.Fatalf("%d over_budget signals", n)
	}
}

// The project's watchdog numbers hold for its runs (8.2): here the same action failing twice stops.
func TestAProjectsWatchdogNumbersHold(t *testing.T) {
	l := DefaultLimits()
	l.FailWarn, l.FailStop = 1, 2
	k, ticket := limitsKit(t, l)
	fail := func(i int) []byte {
		return []byte(fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":"%s","step_index":%d,"state":"ERROR","step_type":"tool","tool_name":"run_command","tool_info":{"parameters":{"command":"make"}},"error":"exit 2"}}`, agyConv, i))
	}
	k.play(longTurn(fail(1), fail(2)))
	run, _ := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: "gemini-3.8-flash", Effort: "low"})
	v := k.wait(run, "ended")
	if !strings.Contains(v.StopDetail, "failed 2 times in a row") {
		t.Fatalf("%s %s", v.StopReason, v.StopDetail)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}
