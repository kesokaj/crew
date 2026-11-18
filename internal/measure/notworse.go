package measure

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
)

// Side is one side of an eval, the baseline or the change: for each case, whether its goal was met in
// each repeat.
type Side struct {
	// Runs are each case's results, one per repeat, by the case.
	Runs map[int64][]bool `json:"runs"`
	// Unmeasured are how many of each case's runs ended without measuring it, by the case (since
	// 1.10). A row stored before has none.
	Unmeasured map[int64]int `json:"unmeasured,omitempty"`
}

// unmeasured is how many of the side's runs measured nothing on a case the baseline measured.
func (s Side) unmeasured(baseline Side) int {
	n := 0
	for c, k := range s.Unmeasured {
		if _, ok := baseline.Runs[c]; ok {
			n += max(k, 0)
		}
	}
	return n
}

// counted is the side with each of its runs that measured nothing on a case the baseline measured
// counted as a goal not met. A case that measured nothing on both sides is left out, as Common leaves
// it out of a comparison across suites: a failure of both, such as the base's setup, is not the
// change's (since 1.10, the review of the change).
func (s Side) counted(baseline Side) Side {
	out := Side{Runs: make(map[int64][]bool, len(s.Runs))}
	for c, r := range s.Runs {
		out.Runs[c] = slices.Clone(r)
	}
	for c, k := range s.Unmeasured {
		if _, ok := baseline.Runs[c]; !ok {
			continue
		}
		for range max(k, 0) {
			out.Runs[c] = append(out.Runs[c], false)
		}
	}
	return out
}

// With is the side with other's cases added, each in place of the side's own of the same case: the
// sides of several projects' suites, whose cases differ, read as one (10.5).
func (s Side) With(other Side) Side {
	out := Side{Runs: maps.Clone(s.Runs), Unmeasured: maps.Clone(s.Unmeasured)}
	if out.Runs == nil {
		out.Runs = map[int64][]bool{}
	}
	maps.Copy(out.Runs, other.Runs)
	if len(other.Unmeasured) > 0 {
		if out.Unmeasured == nil {
			out.Unmeasured = map[int64]int{}
		}
		maps.Copy(out.Unmeasured, other.Unmeasured)
	}
	return out
}

// Cases is how many cases the side measured.
func (s Side) Cases() int { return len(s.Runs) }

// Shares is the share of cases passed in each repeat.
func (s Side) Shares() []float64 {
	repeats := 0
	for _, r := range s.Runs {
		repeats = max(repeats, len(r))
	}
	out := make([]float64, repeats)
	for i := range out {
		n, passed := 0, 0
		for _, r := range s.Runs {
			if i < len(r) {
				n++
				if r[i] {
					passed++
				}
			}
		}
		if n > 0 {
			out[i] = float64(passed) / float64(n)
		}
	}
	return out
}

// Passed is the share of all runs that met their goal.
func (s Side) Passed() float64 {
	n, p := 0, 0
	for _, r := range s.Runs {
		for _, ok := range r {
			n++
			if ok {
				p++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(p) / float64(n)
}

// The verdicts of evals (10.5).
const (
	NotWorseVerdict = "not_worse"
	WorseVerdict    = "worse"
	TooFew          = "too_few"
)

// Decision is what the evals decided, with the numbers behind it.
type Decision struct {
	Verdict      string  `json:"verdict"`
	Why          string  `json:"why"`
	Cases        int     `json:"cases"`
	PassedBefore float64 `json:"passedBefore"`
	PassedAfter  float64 `json:"passedAfter"`
	Margin       float64 `json:"margin"`
	// Lost are the cases that passed every time before and fail every time with the change.
	Lost []int64 `json:"lost"`
	// UnmeasuredRuns are the change's runs that measured nothing on a case the baseline measured, which
	// the decision counted as goals not met before it let the change pass (since 1.10). PassedAfter and
	// Lost are of the side so counted when the change is not worse or could not tell, and of its
	// measured runs when those alone decide; a suite's own numbers of the side are of its measured runs.
	UnmeasuredRuns int `json:"unmeasuredRuns,omitempty"`
}

// Margin is the spread between repeats of the same baseline: the highest share of passed cases in one
// repeat less the lowest. With one repeat it is zero.
func Margin(baseline Side) float64 {
	shares := baseline.Shares()
	if len(shares) == 0 {
		return 0
	}
	lo, hi := shares[0], shares[0]
	for _, v := range shares {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return hi - lo
}

// NotWorse decides whether a change is not worse than the baseline (10.5, R167): the share of passed
// cases is not lower by more than the margin, and no case that passed every time fails every time now.
// Results alone decide: since 0.53 no cost is compared. With fewer than minCases cases it is too few to
// tell. A run of the change that measured nothing may have been the change's own way to leave out a
// case it failed (13.9), so the change is not worse only when it is not worse with each such run on a
// case the baseline measured counted as a goal not met; it is worse when its measured runs alone are
// worse, and otherwise too few to tell, which leaves the change to the operator (since 1.10). The
// baseline's runs that measured nothing are left out: no change's text reaches them.
func NotWorse(baseline, change Side, minCases int) Decision {
	n := change.unmeasured(baseline)
	if n == 0 {
		return notWorse(baseline, change, minCases)
	}
	counted := notWorse(baseline, change.counted(baseline), minCases)
	counted.UnmeasuredRuns = n
	if counted.Verdict == NotWorseVerdict {
		counted.Why += fmt.Sprintf(", with %d runs of the change that measured nothing counted as goals not met", n)
		return counted
	}
	measured := notWorse(baseline, change, minCases)
	measured.UnmeasuredRuns = n
	if measured.Verdict != NotWorseVerdict {
		return measured
	}
	counted.Verdict = TooFew
	counted.Why = fmt.Sprintf("%d runs of the change measured nothing, and counted as goals not met it would be worse: %s", n, counted.Why)
	return counted
}

// notWorse is NotWorse on the runs the sides hold.
func notWorse(baseline, change Side, minCases int) Decision {
	d := Decision{Cases: baseline.Cases(), PassedBefore: baseline.Passed(), PassedAfter: change.Passed(), Margin: Margin(baseline),
		Lost: []int64{}}
	if d.Cases < minCases || change.Cases() < minCases {
		d.Verdict, d.Why = TooFew, fmt.Sprintf("%d cases, and at least %d are needed to tell", min(d.Cases, change.Cases()), minCases)
		return d
	}
	for id, before := range baseline.Runs {
		after := change.Runs[id]
		if all(before, true) && len(after) > 0 && all(after, false) {
			d.Lost = append(d.Lost, id)
		}
	}
	sort.Slice(d.Lost, func(i, j int) bool { return d.Lost[i] < d.Lost[j] })
	switch {
	case d.PassedAfter < d.PassedBefore-d.Margin-1e-9:
		d.Verdict, d.Why = WorseVerdict, fmt.Sprintf("%.0f%% of the runs met their goal, against %.0f%% before, past the margin of %.0f points",
			d.PassedAfter*100, d.PassedBefore*100, d.Margin*100)
	case len(d.Lost) > 0:
		d.Verdict, d.Why = WorseVerdict, fmt.Sprintf("%d cases that passed every time fail every time now", len(d.Lost))
	default:
		d.Verdict, d.Why = NotWorseVerdict, fmt.Sprintf("%.0f%% of the runs met their goal, against %.0f%% before, within the margin of %.0f points",
			d.PassedAfter*100, d.PassedBefore*100, d.Margin*100)
	}
	return d
}

// Run is one eval run as a comparison across suites reads it: its case, its repeat, and whether it met
// its goal.
type Run struct {
	Case   int64
	Repeat int
	Met    bool
	// Unmeasured is a run that ended without measuring its case; its Met is not read (since 1.10).
	Unmeasured bool
}

// Common is the two sides of a comparison across suites, on the cases the earlier suite measured and
// the later one ran, with each case's results in the order of its repeats: a case may be retired or
// added between two suites, and the earlier one ran each case more often (16.2 step 5, since 0.52). A
// later run that measured nothing is kept in the later side's Unmeasured, so a case it measured nothing
// on does not leave the comparison (since 1.10); an earlier one is left out.
func Common(before, after []Run) (Side, Side) {
	cases := func(rs []Run, unmeasured bool) map[int64]bool {
		out := map[int64]bool{}
		for _, r := range rs {
			if unmeasured || !r.Unmeasured {
				out[r.Case] = true
			}
		}
		return out
	}
	inBefore, inAfter := cases(before, false), cases(after, true)
	side := func(rs []Run, later bool) Side {
		rs = slices.Clone(rs)
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Repeat < rs[j].Repeat })
		s := Side{Runs: map[int64][]bool{}}
		for _, r := range rs {
			switch {
			case !inBefore[r.Case] || !inAfter[r.Case]:
			case !r.Unmeasured:
				s.Runs[r.Case] = append(s.Runs[r.Case], r.Met)
			case later:
				if s.Unmeasured == nil {
					s.Unmeasured = map[int64]int{}
				}
				s.Unmeasured[r.Case]++
			}
		}
		return s
	}
	return side(before, false), side(after, true)
}

// AgainstEarlier decides a release's side against an earlier suite's baseline side (16.2 step 5): by
// the rule an override is judged by, on Common's sides, so the margin is the earlier suite's spread
// between its repeats and a lost case one that passed every earlier repeat.
func AgainstEarlier(before, after []Run, minCases int) Decision {
	b, a := Common(before, after)
	return NotWorse(b, a, minCases)
}

func all(rs []bool, v bool) bool {
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r != v {
			return false
		}
	}
	return true
}
