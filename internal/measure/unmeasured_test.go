package measure

import (
	"encoding/json"
	"strings"
	"testing"
)

// A run of the change that measured nothing counts as a goal not met before the change may pass, so
// a change cannot leave out a case it failed (13.9, since 1.10): not worse only when it holds so;
// worse when the measured runs alone are worse; else too few to tell.
func TestAChangesRunThatMeasuredNothingCountsAsNotMet(t *testing.T) {
	ten := rep(10, true)
	// The change measured nine cases and passed them, and measured nothing on the tenth: counted as
	// not met it is worse, so it is too few to tell, not not worse.
	change := side(ten[:9]...)
	change.Unmeasured = map[int64]int{10: 1}
	d := NotWorse(side(ten...), change, 5)
	if d.Verdict != TooFew || d.UnmeasuredRuns != 1 || !strings.HasPrefix(d.Why, "1 runs of the change measured nothing, and counted as goals not met it would be worse: ") {
		t.Fatalf("a failed case left out: %+v", d)
	}

	// A case that passed every time before, and measured nothing in each repeat of the change: counted,
	// it is lost, though the share stays within the margin.
	base := append(rep(8, true, true, true), []bool{true, false, false}, []bool{true, true, true})
	hidden := side(base[:9]...)
	hidden.Unmeasured = map[int64]int{10: 3}
	if d := NotWorse(side(base...), hidden, 5); d.Verdict != TooFew || len(d.Lost) != 1 || d.Lost[0] != 10 || d.UnmeasuredRuns != 3 {
		t.Fatalf("a lost case left out: %+v", d)
	}

	// Counted as not met it is still not worse: not worse, with its count and the counted share.
	before := append(rep(9, true), []bool{false})
	harmless := side(rep(9, true)...)
	harmless.Unmeasured = map[int64]int{10: 1}
	d = NotWorse(side(before...), harmless, 5)
	if d.Verdict != NotWorseVerdict || d.UnmeasuredRuns != 1 || d.PassedAfter < 0.8999 || d.PassedAfter > 0.9001 ||
		!strings.HasSuffix(d.Why, ", with 1 runs of the change that measured nothing counted as goals not met") {
		t.Fatalf("an unmeasured run that changes nothing: %+v", d)
	}

	// Worse by its measured runs alone: worse.
	worse := side(append(rep(4, false), rep(5, true)...)...)
	worse.Unmeasured = map[int64]int{10: 1}
	if d := NotWorse(side(ten...), worse, 5); d.Verdict != WorseVerdict || d.UnmeasuredRuns != 1 {
		t.Fatalf("worse as measured: %+v", d)
	}

	// The baseline's runs that measured nothing are left out, as before.
	b := side(ten[:9]...)
	b.Unmeasured = map[int64]int{10: 1}
	if d := NotWorse(b, side(ten[:9]...), 5); d.Verdict != NotWorseVerdict || d.UnmeasuredRuns != 0 || d.Cases != 9 {
		t.Fatalf("the baseline's: %+v", d)
	}

	// A case that measured nothing on both sides, as when the base's setup fails, is left out of both:
	// the change is not blamed for it (the review of the change).
	both := side(ten[:9]...)
	both.Unmeasured = map[int64]int{10: 1}
	if d := NotWorse(b, both, 5); d.Verdict != NotWorseVerdict || d.UnmeasuredRuns != 0 || d.Cases != 9 || d.PassedAfter != 1 {
		t.Fatalf("both sides: %+v", d)
	}
	// Beside a case only the change measured nothing on, which is counted, the case of both is still
	// left out: counted, the change passes eight of nine, as before.
	mixedBase := side(append(rep(8, true), []bool{false})...)
	mixedBase.Unmeasured = map[int64]int{10: 1}
	mixed := side(rep(8, true)...)
	mixed.Unmeasured = map[int64]int{9: 1, 10: 1}
	if d := NotWorse(mixedBase, mixed, 5); d.Verdict != NotWorseVerdict || d.UnmeasuredRuns != 1 || d.Cases != 9 {
		t.Fatalf("one case of both sides beside one of the change: %+v", d)
	}
}

// A side stored before 1.10 has no unmeasured runs and decides as it did; one with none is stored as
// it was. With adds a project's side to another's, its unmeasured runs too (10.5).
func TestASideStoredBeforeHasNoUnmeasuredRuns(t *testing.T) {
	var old Side
	if err := json.Unmarshal([]byte(`{"runs": {"1": [true], "2": [false]}}`), &old); err != nil || old.Unmeasured != nil || old.Cases() != 2 {
		t.Fatalf("%+v %v", old, err)
	}
	if raw, _ := json.Marshal(side([]bool{true})); string(raw) != `{"runs":{"1":[true]}}` {
		t.Fatalf("stored as %s", raw)
	}
	a := Side{Runs: map[int64][]bool{1: {true}}, Unmeasured: map[int64]int{2: 1}}
	got := Side{Runs: map[int64][]bool{}}.With(a).With(Side{Runs: map[int64][]bool{3: {false}}, Unmeasured: map[int64]int{4: 2}})
	if got.Cases() != 2 || len(got.Unmeasured) != 2 || got.Unmeasured[2] != 1 || got.Unmeasured[4] != 2 {
		t.Fatalf("the sides added: %+v", got)
	}
	a.With(Side{Runs: map[int64][]bool{5: {true}}, Unmeasured: map[int64]int{4: 2}})
	if a.Unmeasured[4] != 0 || len(a.Unmeasured) != 1 || len(a.Runs) != 1 {
		t.Fatalf("With changed its side: %+v", a)
	}
}

// A release's run that measured nothing keeps its case in the comparison across suites, counted as not
// met for the bound (16.2 step 5, since 1.10); the earlier suite's are left out.
func TestAReleaseRunThatMeasuredNothingStaysInTheComparison(t *testing.T) {
	ten := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	pass := func(int64, int) bool { return true }
	before := append(runs(ten, 3, pass), Run{Case: 11, Repeat: 1, Unmeasured: true})
	after := append(runs(ten[:9], 1, pass), Run{Case: 10, Repeat: 1, Unmeasured: true}, Run{Case: 11, Repeat: 1, Met: true})
	bs, as := Common(before, after)
	if bs.Cases() != 10 || as.Cases() != 9 || as.Unmeasured[10] != 1 || len(as.Unmeasured) != 1 || bs.Unmeasured != nil {
		t.Fatalf("the common sides: %+v %+v", bs, as)
	}
	if d := AgainstEarlier(before, after, 9); d.Verdict != TooFew || d.UnmeasuredRuns != 1 || len(d.Lost) != 1 || d.Lost[0] != 10 {
		t.Fatalf("a release case that measured nothing: %+v", d)
	}
}
