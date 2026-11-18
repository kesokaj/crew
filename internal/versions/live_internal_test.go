package versions

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// The eval suites' detail is cut to what a gate's text may hold, at a whole project's line, and says
// how many more there are (16.2 step 5, since 0.52).
func TestTheGatesEvalSuitesDetailIsCut(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("project-%03d: not compared: no suite on the running release 0.2.7", i))
	}
	got := detailOf(lines)
	if len(got) > maxDetail || !strings.HasSuffix(got, " more: see each project's Evals tab") || !strings.HasPrefix(got, lines[0]+"; ") {
		t.Fatalf("%d bytes: …%s", len(got), got[max(0, len(got)-120):])
	}
	kept := strings.Split(got[:strings.LastIndex(got, "; and ")], "; ")
	for i, l := range kept {
		if l != lines[i] {
			t.Fatalf("line %d is cut: %q", i, l)
		}
	}
	if want := fmt.Sprintf("; and %d more: see each project's Evals tab", 100-len(kept)); !strings.HasSuffix(got, want) {
		t.Fatalf("%d kept, the end says %q", len(kept), got[strings.LastIndex(got, "; and "):])
	}
	if detailOf(lines[:2]) != lines[0]+"; "+lines[1] {
		t.Fatal("a short detail is changed")
	}
}

// The hold a server before 0.53 wrote for a release whose live steps ran in other code said what its
// runs cost; it is read as the hold written since (release.Unpriced), word for word.
func TestTheOldHoldOfOtherCodeIsReadAsToday(t *testing.T) {
	const old = "its live steps ran in a candidate runtime image whose code is not the running release's, which reports what its runs cost: Install decides"
	if got := release.Unpriced(old); got != otherCode {
		t.Fatalf("%q is read as %q, not as today's %q", old, got, otherCode)
	}
}
