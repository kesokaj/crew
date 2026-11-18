package pins_test

import (
	"strings"
	"testing"
)

// A versions.lock past the bound of a read is no lock to look by, never a main with none: the look
// says so and makes no ticket (since 1.04).
func TestALockPastTheBoundIsNoLockToLookBy(t *testing.T) {
	k := newLookKit(t)
	k.git.MaxOutput = 64
	if r := k.once(); len(r.Made) != 0 || len(r.Lines) != 1 || r.Lines[0].OK || !strings.Contains(r.Lines[0].Text, "main of fixture's versions.lock could not be read") {
		t.Fatalf("past the bound: %+v", r)
	}
	k.git.MaxOutput = 0
	if r := k.once(); len(r.Made) != 1 {
		t.Fatalf("within the bound: %+v", r)
	}
}
