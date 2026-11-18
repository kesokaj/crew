package pipeline

import (
	"context"
	"encoding/json"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/measure"
)

// overrideMetric is what an override is measured by: it names none of its own.
const overrideMetric = "first_request_pass_rate"

// measureOverrides measures each applied override of a project once its next tickets are done (9.6):
// the first of them against the last before it. A worse result takes it back, with news; an
// inconclusive one leaves it, with its numbers.
func (p *Pipeline) measureOverrides(ctx context.Context, j *job) error {
	list, err := p.Board.Overrides(ctx, j.proj)
	if err != nil {
		return err
	}
	n := int(num(j.machine, "improvements.measure_after_tasks", 20))
	m, _ := measure.MetricOf(overrideMetric)
	for _, o := range list {
		if o.Status != board.OverrideApplied || o.AppliedAt == nil || len(o.Measure) > 0 {
			continue
		}
		before, after, err := measure.AroundOverride(ctx, p.Pool, j.proj.ID, *o.AppliedAt, n)
		if err != nil {
			return err
		}
		if len(after) < n {
			continue
		}
		r := measure.Compare(m, before, after, n, overrideMargin(o.Eval, n))
		if r.Verdict == measure.Worse {
			if err := p.Board.TakeBack(ctx, o.ID, "it measured worse: "+r.Why, r); err != nil {
				return err
			}
			continue
		}
		if err := p.Board.Measured(ctx, o.ID, r); err != nil {
			return err
		}
	}
	return nil
}

// overrideMargin is the spread its evals measured between repeats of the baseline, or, with no evals,
// one ticket's share of the tickets it is measured over.
func overrideMargin(eval json.RawMessage, n int) float64 {
	var e struct {
		Margin *float64 `json:"margin"`
	}
	if json.Unmarshal(eval, &e) == nil && e.Margin != nil {
		return *e.Margin
	}
	return 1 / float64(n)
}
