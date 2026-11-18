package gitstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A history reads git's output the same whatever pieces it comes in, a mark split across two of them
// included: each field kept to its cut, the cut ones said, the text of the key without its mark's
// bytes taken as text, and what follows the last record no record (6.1, since 1.09).
func TestAHistoryReadsTheSameInAnyPieces(t *testing.T) {
	m := newMarks()
	out := "id1" + m.field + "abcdefghij" + m.field + "x\x1fy\x1ez F" + m.key + "\x1f" + m.record +
		"\nid2" + m.field + m.field + "é" + m.record + "\n"
	want := []record{
		{fields: []string{"id1", "abcdefgh", "x\x1fy\x1ez F" + m.key + "\x1f"}, cut: []bool{false, true, false}},
		{fields: []string{"\nid2", "", "é"}, cut: []bool{false, false, false}},
	}
	for size := 1; size <= len(out); size++ {
		h := newHistory(m, []int{8, 8, 100}, 10, 1<<20, 1<<20)
		for p := out; len(p) > 0; {
			n := min(size, len(p))
			if k, err := h.Write([]byte(p[:n])); err != nil || k != n {
				t.Fatalf("pieces of %d: %d %v", size, k, err)
			}
			p = p[n:]
		}
		if !reflect.DeepEqual(h.recs, want) {
			t.Fatalf("pieces of %d: %#v", size, h.recs)
		}
	}
	// A rune the cut splits is left out.
	h := newHistory(m, []int{3}, 10, 1<<20, 1<<20)
	h.Write([]byte("aéé" + m.record))
	if got := h.recs[0]; got.fields[0] != "aé" || !got.cut[0] {
		t.Fatalf("%#v", got)
	}
}

// Past its records, past what it keeps in all, or past what it reads of git's output, a history stops
// with ErrOutputTooLarge, and stops git with it.
func TestAHistoryStopsPastItsRecordsAndWhatItKeeps(t *testing.T) {
	m := newMarks()
	two := strings.Repeat("abc"+m.field+"def"+m.record+"\n", 2)
	if _, err := newHistory(m, []int{8, 8}, 1, 1<<20, 1<<20).Write([]byte(two)); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("past its records: %v", err)
	}
	if _, err := newHistory(m, []int{8, 8}, 10, 12, 1<<20).Write([]byte(two)); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("past what it keeps: %v", err)
	}
	if _, err := newHistory(m, []int{8, 8}, 10, 1<<20, int64(len(two)-1)).Write([]byte(two)); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("past what it reads: %v", err)
	}
	if _, err := newHistory(m, []int{8, 8}, 2, 13, int64(len(two))).Write([]byte(two)); err != nil {
		t.Fatalf("within all three: %v", err)
	}
}
