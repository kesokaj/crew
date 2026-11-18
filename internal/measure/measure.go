// Package measure says whether a change made things better or worse (10.5): an improvement of the
// baseline over the tickets of the versions before and after it, and a project's override over the
// project's tickets before and after it was applied. It also says whether evals are not worse.
package measure

import (
	"fmt"
	"math"
)

// Metric is one of the fixed numbers a change is measured by, with the direction that is better.
type Metric struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Higher is better for a rate of passing; lower for the rest.
	Higher bool `json:"higher"`
	// Rate is a share between 0 and 1, whose margin is in points; the others are per ticket, whose
	// margin is a share of the value before.
	Rate bool `json:"rate"`
}

// Metrics is the list an improvement names its metric from (since 0.49).
var Metrics = []Metric{
	{"first_request_pass_rate", "Checks passed on the first request", true, true},
	{"passed_first_time_rate", "Passed the first time: no failed check and no fix round", true, true},
	{"fix_rounds_per_ticket", "Fix rounds per ticket", false, false},
	{"tokens_per_ticket", "Tokens in and out per ticket", false, false},
	{"interventions_per_ticket", "Times the operator stepped in per ticket", false, false},
	{"escaped_defects_per_ticket", "Bugs found after Done per ticket", false, false},
	// A change of the prompt writer's rules is measured by the operator's choices, not by tickets (7.2).
	{RewritesUsed, "Rewrites the operator used as they were", true, true},
}

// RewritesUsed is the metric of a change of the prompt writer's rules, over first messages of plans.
const RewritesUsed = "rewrites_used_as_is"

// MetricOf returns a metric by its name.
func MetricOf(name string) (Metric, bool) {
	for _, m := range Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

// Facts are what one Done ticket shows.
type Facts struct {
	FirstRequest  bool  `json:"firstRequest"`
	PassedFirst   bool  `json:"passedFirst"`
	FixRounds     int   `json:"fixRounds"`
	Interventions int   `json:"interventions"`
	Escaped       int   `json:"escaped"`
	Tokens        int64 `json:"tokens"`
	// NoTokens is true when any turn of the ticket ended without reporting usage tokens, so the
	// ticket is excluded from tokens_per_ticket rather than counted as a 0-token improvement.
	NoTokens bool `json:"noTokens,omitempty"`
	// UsedAsIs is, for a rewrite rather than a ticket, that the operator sent it as it was.
	UsedAsIs bool `json:"usedAsIs,omitempty"`
}

func filterForMetric(m Metric, fs []Facts) []Facts {
	if m.Name != "tokens_per_ticket" {
		return fs
	}
	out := make([]Facts, 0, len(fs))
	for _, f := range fs {
		if !f.NoTokens {
			out = append(out, f)
		}
	}
	return out
}

// Value is a metric over tickets; 0 for none.
func Value(m Metric, fs []Facts) float64 {
	fs = filterForMetric(m, fs)
	if len(fs) == 0 {
		return 0
	}
	var sum float64
	for _, f := range fs {
		switch m.Name {
		case "first_request_pass_rate":
			sum += b2f(f.FirstRequest)
		case "passed_first_time_rate":
			sum += b2f(f.PassedFirst)
		case "fix_rounds_per_ticket":
			sum += float64(f.FixRounds)
		case "tokens_per_ticket":
			sum += float64(f.Tokens)
		case "interventions_per_ticket":
			sum += float64(f.Interventions)
		case "escaped_defects_per_ticket":
			sum += float64(f.Escaped)
		case RewritesUsed:
			sum += b2f(f.UsedAsIs)
		}
	}
	return sum / float64(len(fs))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// The verdicts of a measurement (10.5).
const (
	Better       = "better"
	Same         = "same"
	Worse        = "worse"
	Inconclusive = "inconclusive"
)

// Result is what a measurement found, with its numbers.
type Result struct {
	Metric        string  `json:"metric"`
	Before        float64 `json:"before"`
	After         float64 `json:"after"`
	TicketsBefore int     `json:"ticketsBefore"`
	TicketsAfter  int     `json:"ticketsAfter"`
	Margin        float64 `json:"margin"`
	Verdict       string  `json:"verdict"`
	Why           string  `json:"why"`
}

// Compare measures a metric over the tickets before a change and the tickets after it. It needs n of
// each: with fewer it is inconclusive, with its numbers (21, "too few tasks to tell"). A difference
// within the margin is noise: the same. The first n tickets after are compared with the last n before;
// the caller passes them in that order, the oldest first.
func Compare(m Metric, before, after []Facts, n int, margin float64) Result {
	before = filterForMetric(m, before)
	after = filterForMetric(m, after)
	if len(before) > n {
		before = before[len(before)-n:]
	}
	if len(after) > n {
		after = after[:n]
	}
	r := Result{Metric: m.Name, Before: Value(m, before), After: Value(m, after), TicketsBefore: len(before), TicketsAfter: len(after), Margin: margin}
	if len(before) < n || len(after) < n {
		r.Verdict = Inconclusive
		r.Why = fmt.Sprintf("too few tickets to tell: %d before and %d after, of %d each", len(before), len(after), n)
		return r
	}
	// gain is how much better after is, in the metric's own measure: points for a rate, a share of the
	// value before for the rest.
	gain := r.After - r.Before
	if !m.Higher {
		gain = -gain
	}
	if !m.Rate {
		switch {
		case r.Before > 0:
			gain /= r.Before
		case r.After == 0:
			gain = 0
		}
	}
	switch {
	case gain < -margin-1e-9:
		r.Verdict, r.Why = Worse, fmt.Sprintf("%s went from %s to %s, past the margin of %s", m.Label, show(m, r.Before), show(m, r.After), showMargin(m, margin))
	case gain > margin+1e-9:
		r.Verdict, r.Why = Better, fmt.Sprintf("%s went from %s to %s", m.Label, show(m, r.Before), show(m, r.After))
	default:
		r.Verdict, r.Why = Same, fmt.Sprintf("%s went from %s to %s, within the margin of %s", m.Label, show(m, r.Before), show(m, r.After), showMargin(m, margin))
	}
	return r
}

func show(m Metric, v float64) string {
	if m.Rate {
		return fmt.Sprintf("%.0f%%", math.Round(v*100))
	}
	return fmt.Sprintf("%.2f", v)
}

func showMargin(m Metric, v float64) string {
	if m.Rate {
		return fmt.Sprintf("%.0f points", math.Round(v*100))
	}
	return fmt.Sprintf("%.0f%%", math.Round(v*100))
}
