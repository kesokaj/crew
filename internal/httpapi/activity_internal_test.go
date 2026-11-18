package httpapi

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

// Every check-run purpose has its words in the activity feed, so no check line begins with " of "
// (dave, 0.3.19: judge and e2e check runs read " of GATE-32 passed").
func TestPurposeWordsCoverEveryCheckRunPurpose(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		board.PurposeBase, board.PurposeExit, board.PurposeTest, board.PurposeMerge,
		board.PurposePart, board.PurposeJoin, board.PurposeJudge, board.PurposeE2E,
		board.PurposeStage, board.PurposeStageJudge, board.PurposeOutside,
	} {
		w := purposeWord(p)
		if w == "" || w == "The checks" || !strings.HasPrefix(w, "The ") {
			t.Fatalf("%s: %q", p, w)
		}
	}
	if got := purposeWord("unknown"); got != "The checks" {
		t.Fatal(got)
	}
}

// Why Crew chose, in the activity (12.3): better is the builder's passed share, and for an agent that
// checks fewer defects escaped; "for less" is only on a choice made before 0.53.
func TestWhyChosenIsTheRoles(t *testing.T) {
	t.Parallel()
	if got := whyChosen("builder", "better"); got != "it passed Crew's checks more often" {
		t.Fatal(got)
	}
	for _, role := range []string{"reviewer", "tester", "e2e", "verifier"} {
		if got := whyChosen(role, "better"); got != "fewer defects escaped it, and it asked for fixes about as often" {
			t.Fatalf("%s: %s", role, got)
		}
	}
	if got := whyChosen("reviewer", "cheaper"); got != "it did as well for less (before 0.53)" {
		t.Fatal(got)
	}
	if got := whyChosen("builder", "newer"); got != "a newer model in its bracket was offered" {
		t.Fatal(got)
	}
}
