package measure

import (
	"strings"
	"testing"
)

func many(n int, f func(i int) Facts) []Facts {
	out := make([]Facts, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

// A change is better, the same, or worse by the margin, in points for a rate and as a share of the value
// before for a number per ticket; with too few tickets on either side it is inconclusive (10.5).
func TestCompare(t *testing.T) {
	rate, _ := MetricOf("first_request_pass_rate")
	fix, _ := MetricOf("fix_rounds_per_ticket")
	pass := func(k int) []Facts { return many(20, func(i int) Facts { return Facts{FirstRequest: i < k} }) }
	rounds := func(per20 int) []Facts {
		return many(20, func(i int) Facts { return Facts{FixRounds: b2i(i < per20)} })
	}
	for _, tc := range []struct {
		name          string
		m             Metric
		before, after []Facts
		margin        float64
		want          string
	}{
		{"58% to 74% is better", rate, pass(12), pass(15), 0.05, Better},
		{"within the margin is the same", rate, pass(12), pass(13), 0.05, Same},
		{"a drop past the margin is worse", rate, pass(12), pass(10), 0.05, Worse},
		{"a drop of exactly the margin is the same", rate, pass(12), pass(11), 0.05, Same},
		{"too few after", rate, pass(12), pass(12)[:19], 0.05, Inconclusive},
		{"too few before", rate, pass(12)[:3], pass(12), 0.05, Inconclusive},
		{"fewer fix rounds is better", fix, rounds(10), rounds(5), 0.1, Better},
		{"more fix rounds is worse", fix, rounds(5), rounds(10), 0.1, Worse},
		{"from none to some fix rounds is worse", fix, rounds(0), rounds(4), 0.1, Worse},
		{"none before and after is the same", fix, rounds(0), rounds(0), 0.1, Same},
	} {
		r := Compare(tc.m, tc.before, tc.after, 20, tc.margin)
		if r.Verdict != tc.want {
			t.Errorf("%s: %s (%s)", tc.name, r.Verdict, r.Why)
		}
	}
	r := Compare(rate, pass(12), pass(15), 20, 0.05)
	if r.Before != 0.6 || r.After != 0.75 || r.TicketsBefore != 20 || !strings.Contains(r.Why, "60% to 75%") {
		t.Fatalf("%+v", r)
	}
	// Only the last n before and the first n after count.
	long := append(pass(0), pass(20)...)
	if r := Compare(rate, long, pass(20), 20, 0); r.Before != 1 || r.Verdict != Same {
		t.Fatalf("the last 20 before: %+v", r)
	}
	if r := Compare(rate, pass(20), append(pass(20), pass(0)...), 20, 0); r.After != 1 {
		t.Fatalf("the first 20 after: %+v", r)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Every metric reads its fact.
func TestEveryMetricHasAValue(t *testing.T) {
	f := Facts{FirstRequest: true, PassedFirst: true, FixRounds: 2, Interventions: 3, Escaped: 4, Tokens: 5, UsedAsIs: true}
	for _, m := range Metrics {
		if Value(m, []Facts{f}) == 0 {
			t.Errorf("%s reads nothing", m.Name)
		}
	}
	if _, ok := MetricOf("happiness"); ok {
		t.Error("a metric not on the list")
	}
}

func TestTokensPerTicketExcludesUnreportedRuns(t *testing.T) {
	tokMetric, _ := MetricOf("tokens_per_ticket")
	passMetric, _ := MetricOf("first_request_pass_rate")
	before := many(20, func(i int) Facts {
		return Facts{FirstRequest: true, Tokens: 1000}
	})
	afterWithUnreported := many(20, func(i int) Facts {
		if i < 5 {
			return Facts{FirstRequest: true, Tokens: 0, NoTokens: true}
		}
		return Facts{FirstRequest: true, Tokens: 1000}
	})
	if got := Value(tokMetric, afterWithUnreported); got != 1000 {
		t.Fatalf("expected Value(tokens_per_ticket) to ignore NoTokens entries and equal 1000, got %v", got)
	}
	res := Compare(tokMetric, before, afterWithUnreported, 20, 0.05)
	if res.Verdict != Inconclusive || res.TicketsAfter != 15 {
		t.Fatalf("expected Inconclusive with 15 reported tickets after filtering 5 NoTokens tickets, got %+v", res)
	}
	// Non-token metrics still include tickets where NoTokens is true.
	passRes := Compare(passMetric, before, afterWithUnreported, 20, 0.05)
	if passRes.TicketsAfter != 20 || passRes.Verdict != Same {
		t.Fatalf("expected non-token metric to keep all 20 tickets, got %+v", passRes)
	}
}
