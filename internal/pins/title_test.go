package pins

import (
	"strings"
	"testing"
)

// A title names each image it moves, and their count when the names would not fit a title's 200
// characters (board.maxTitle).
func TestAnImagesTitleFits(t *testing.T) {
	f := Finding{Tool: "the images"}
	for _, k := range []string{"GO_IMAGE", "SERVER_BASE_IMAGE"} {
		f.Set = append(f.Set, KV{k, "x"})
	}
	if got := title(f); got != "Move the pinned images GO_IMAGE, SERVER_BASE_IMAGE to their tags' new digests" {
		t.Fatalf("%q", got)
	}
	f.Set = nil
	for i := range 10 {
		f.Set = append(f.Set, KV{strings.Repeat("A", 20) + string(rune('A'+i)) + "_IMAGE", "x"})
	}
	if got := title(f); got != "Move 10 pinned images to their tags' new digests" {
		t.Fatalf("%q", got)
	}
}
