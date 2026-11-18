package runs

import (
	"context"
	"time"
)

// Limits are the watchdog's numbers for one run (8.2). A project's settings give them; Defaults are
// the design's. No limit is in dollars (7.7, since 0.53): a run is held to its turns, its time, and
// these.
type Limits struct {
	SilentWarn, SilentStop time.Duration
	FailWarn, FailStop     int
}

// DefaultLimits are the design's numbers (8.2), for a run whose project gives none.
func DefaultLimits() Limits {
	return Limits{SilentWarn: SilentWarn, SilentStop: SilentStop, FailWarn: FailWarn, FailStop: FailStop}
}

// ReasonBudget ended a run whose estimated cost reached its budget, before 0.53: a stop of such a run
// still counts, and no run gets it now.
const ReasonBudget = "budget"

// arm reads what the watchdog holds the run to, at the start of a turn.
func (d *Driver) arm(ctx context.Context, lr *liveRun) {
	l := DefaultLimits()
	if d.LimitsFor != nil {
		if got, err := d.LimitsFor(ctx, lr.subtask); err != nil {
			d.Log.Warn("the run's limits cannot be read; it keeps the design's", "run", lr.id, "error", err)
		} else {
			l = got
		}
	}
	lr.mu.Lock()
	lr.limits = l
	lr.mu.Unlock()
}
