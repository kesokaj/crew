package measure

import "testing"

func side(runs ...[]bool) Side {
	s := Side{Runs: map[int64][]bool{}}
	for i, r := range runs {
		s.Runs[int64(i+1)] = r
	}
	return s
}

func rep(n int, rs ...bool) [][]bool {
	out := make([][]bool, n)
	for i := range out {
		out[i] = rs
	}
	return out
}

// "Not worse" (10.5, R167): within the margin, which is the spread between the baseline's repeats, and
// no case that passed every time now fails every time; too few cases tell nothing. Results alone
// decide: since 0.53 no cost is compared.
func TestNotWorse(t *testing.T) {
	// Ten cases, three repeats: eight pass always; one passes once of three; one never.
	base := append(rep(8, true, true, true), []bool{true, false, false}, []bool{false, false, false})
	b := side(base...)
	if m := Margin(b); m < 0.0999 || m > 0.1001 {
		t.Fatalf("margin %v, want 0.1 (shares 1.0, 0.8, 0.8 less ... )", m)
	}
	same := side(base...)
	if d := NotWorse(b, same, 10); d.Verdict != NotWorseVerdict {
		t.Fatalf("the same: %+v", d)
	}
	// One case more fails in one repeat: inside the margin.
	slight := append(rep(7, true, true, true), []bool{true, true, false}, []bool{true, false, false}, []bool{false, false, false})
	if d := NotWorse(b, side(slight...), 10); d.Verdict != NotWorseVerdict {
		t.Fatalf("inside the margin: %+v", d)
	}
	// A case that passed always now fails always, though the share barely moves: worse.
	lost := append(rep(7, true, true, true), []bool{false, false, false}, []bool{true, true, true}, []bool{false, false, false})
	if d := NotWorse(b, side(lost...), 10); d.Verdict != WorseVerdict || len(d.Lost) != 1 || d.Lost[0] != 8 {
		t.Fatalf("a lost case: %+v", d)
	}
	// Three fewer passing in every repeat: past the margin.
	drop := append(rep(5, true, true, true), rep(5, false, false, false)...)
	if d := NotWorse(b, side(drop...), 10); d.Verdict != WorseVerdict {
		t.Fatalf("a drop: %+v", d)
	}
	// Nine cases: too few.
	if d := NotWorse(side(base[:9]...), side(base[:9]...), 10); d.Verdict != TooFew {
		t.Fatalf("nine cases: %+v", d)
	}
	// One repeat: the margin is zero.
	if Margin(side([]bool{true}, []bool{false})) != 0 {
		t.Fatal("one repeat has a margin")
	}
}

// runs is the runs of cases, repeats each, met as met says.
func runs(cases []int64, repeats int, met func(c int64, r int) bool) []Run {
	var out []Run
	for r := 1; r <= repeats; r++ {
		for _, c := range cases {
			out = append(out, Run{Case: c, Repeat: r, Met: met(c, r)})
		}
	}
	return out
}

// A release's side against an earlier suite (16.2 step 5, since 0.52): only the cases both measured
// count, though the earlier suite ran each case three times and the release once; a case that passed
// every earlier repeat and fails now is lost, and the margin is the earlier suite's spread between its
// repeats, read by repeat whatever order the runs come in.
func TestAgainstEarlier(t *testing.T) {
	ten := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	pass := func(int64, int) bool { return true }
	fail := func(int64, int) bool { return false }
	before := runs(ten, 3, pass)

	// Case 11 passed every time and was retired since; case 12 was added and fails: neither counts.
	b := append(append([]Run{}, before...), runs([]int64{11}, 3, pass)...)
	a := append(runs(ten, 1, pass), runs([]int64{12}, 1, fail)...)
	if d := AgainstEarlier(b, a, 10); d.Verdict != NotWorseVerdict || d.Cases != 10 || d.PassedBefore != 1 || d.PassedAfter != 1 {
		t.Fatalf("a retired and an added case: %+v", d)
	}
	bs, as := Common(b, a)
	if bs.Cases() != 10 || as.Cases() != 10 || len(bs.Runs[1]) != 3 || len(as.Runs[1]) != 1 {
		t.Fatalf("the common sides: %+v %+v", bs, as)
	}

	// Case 3 passed every earlier repeat and fails its one release run: lost.
	if d := AgainstEarlier(before, runs(ten, 1, func(c int64, _ int) bool { return c != 3 }), 10); d.Verdict != WorseVerdict ||
		len(d.Lost) != 1 || d.Lost[0] != 3 {
		t.Fatalf("a lost case: %+v", d)
	}

	// The earlier repeats pass 10, 8 and 9 of ten (cases 1 and 2 fail in the second, case 1 in the
	// third): a margin of 0.2. The release passes 8, which is inside it, and neither case passed every
	// time. The runs come with case 1's repeats out of order: read in that order the margin is 0.
	spread := func(c int64, r int) bool { return !(r == 2 && c <= 2) && !(r == 3 && c == 1) }
	var mixed []Run
	for _, r := range runs(ten, 3, spread) {
		if r.Case != 1 {
			mixed = append(mixed, r)
		}
	}
	mixed = append(mixed, Run{Case: 1, Repeat: 2, Met: false}, Run{Case: 1, Repeat: 1, Met: true}, Run{Case: 1, Repeat: 3, Met: false})
	after := runs(ten, 1, func(c int64, _ int) bool { return c > 2 })
	if d := AgainstEarlier(mixed, after, 10); d.Verdict != NotWorseVerdict || d.Margin < 0.1999 || d.Margin > 0.2001 {
		t.Fatalf("the margin by repeat: %+v", d)
	}

	// Nine cases both measured, of ten: too few.
	if d := AgainstEarlier(before, runs(ten[:9], 1, pass), 10); d.Verdict != TooFew || d.Cases != 9 {
		t.Fatalf("nine in common: %+v", d)
	}
}
