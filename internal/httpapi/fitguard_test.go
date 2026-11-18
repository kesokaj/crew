package httpapi_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// refusedRuns is the run driver, counting the fit checks that the guard every run passes refused.
type refusedRuns struct {
	pipeline.Runs
	mu      sync.Mutex
	refused int
}

func (r *refusedRuns) StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error) {
	run, sub, err := r.Runs.StartRole(ctx, rr)
	if rr.Kind == "eval_fit" && errors.Is(err, runs.ErrCheckFamily) {
		r.mu.Lock()
		r.refused++
		r.mu.Unlock()
	}
	return run, sub, err
}

// A fit check whose judge the guard every run passes refuses, because the guard reads the project's
// builder on the judge's family, waits to be checked, as at a limit: no judge starts, the case keeps
// its state, and the pass logs no error; once the guard lets it, it is checked (12.7, since 1.15).
func TestAFitCheckTheGuardRefusesWaitsToBeChecked(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n")
	var logs syncBuffer
	k.pipe.Log = slog.New(slog.NewTextHandler(&logs, nil))
	rr := &refusedRuns{Runs: k.driver}
	k.pipe.Runs = rr
	// The pipeline chooses the builder on Claude and the judge on Gemini; the guard reads the machine's
	// settings, where the builder is on Gemini.
	machine := k.pipe.Settings
	k.pipe.Settings = func(ctx context.Context) (settings.Doc, error) {
		doc, err := machine(ctx)
		if err != nil {
			return nil, err
		}
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		b := settings.Doc{}
		for key, v := range roles["builder"].(settings.Doc) {
			b[key] = v
		}
		b["model"] = []any{"agy/claude-sonnet-5-5"}
		roles["builder"] = b
		return doc, nil
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	stop := k.run()
	k.until("the judge refused twice", func() bool {
		rr.mu.Lock()
		defer rr.mu.Unlock()
		return rr.refused >= 2
	})
	stop()
	if c := k.cases()[1]; c.Status != "waiting" || c.Fit.State != "pending" {
		t.Fatalf("the case: %+v", c)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit'`); n != 0 {
		t.Fatalf("%d fit checks started", n)
	}
	// The pass's own end, when the test stops it, logs a cancelled context; the guard's reason is not
	// an error of it.
	if strings.Contains(logs.String(), "a check does not run on the builder's family") {
		t.Fatalf("the pass logged the wait as an error: %s", logs.String())
	}
	k.pipe.Settings = machine
	stop = k.run()
	k.until("the case checked", func() bool { return k.cases()[1].Fit.State == "checked" })
	stop()
	if c := k.cases()[1]; !slices.Equal(c.Fit.List, []string{"Counts"}) {
		t.Fatalf("the case once checked: %+v", c)
	}
}
