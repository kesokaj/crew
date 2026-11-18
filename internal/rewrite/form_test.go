package rewrite

import (
	"slices"
	"testing"
)

// The prompt writer's answer is read in its fixed form, and only in it (7.2, 22: "Anything else ...
// sends the operator's own text").
func TestTheAnswerIsReadInItsForm(t *testing.T) {
	answer := "## Rewrite\nGoal: count words.\nIt reads standard input.\n\n## Changes\n- Put the goal first\n- Added where it reads\n\n" +
		"## Assumptions\n- It reads standard input.\n- It is written in Go.\n\n## Questions\n- Which platforms?\n"
	f, ok := readRewrite(answer)
	if !ok || f.good || f.rewrite != "Goal: count words.\nIt reads standard input." {
		t.Fatalf("%+v %v", f, ok)
	}
	if !slices.Equal(f.changes, []string{"Put the goal first", "Added where it reads"}) || !slices.Equal(f.questions, []string{"Which platforms?"}) {
		t.Errorf("%+v", f)
	}
	// An assumption that is not a sentence of the rewrite cannot be struck out of it: it is left out.
	if !slices.Equal(f.assumptions, []string{"It reads standard input."}) {
		t.Errorf("assumptions %q", f.assumptions)
	}
	if f, ok := readRewrite("  GOOD AS IT IS \n"); !ok || !f.good {
		t.Errorf("good as it is: %+v %v", f, ok)
	}
	none, ok := readRewrite("## Rewrite\nx\n## Changes\nnone\n## Assumptions\nnone\n## Questions\n- none")
	if !ok || len(none.changes)+len(none.assumptions)+len(none.questions) != 0 {
		t.Errorf("lists that are none: %+v %v", none, ok)
	}
	// A line of the rewrite that only begins like a heading is the rewrite's own.
	own, ok := readRewrite("## Rewrite\nGoal: a tool.\n## Changes to make\nDo X, then Y.\n\n## Changes\n- a\n## Assumptions\nnone\n## Questions\nnone")
	if !ok || own.rewrite != "Goal: a tool.\n## Changes to make\nDo X, then Y." {
		t.Errorf("a rewrite with a heading of its own: %q %v", own.rewrite, ok)
	}
	for name, bad := range map[string]string{
		"a word before the form":   "Sure!\n" + answer,
		"a heading out of order":   "## Rewrite\nx\n## Assumptions\n- a\n## Changes\n- b\n## Questions\n- c",
		"a heading missing":        "## Rewrite\nx\n## Changes\n- a\n## Questions\n- c",
		"an empty rewrite":         "## Rewrite\n\n## Changes\n- a\n## Assumptions\nnone\n## Questions\nnone",
		"four questions":           "## Rewrite\nx\n## Changes\n- a\n## Assumptions\nnone\n## Questions\n- 1\n- 2\n- 3\n- 4",
		"an answer to the request": "Here is the plan for your word counter: first, ...",
		"nothing":                  "",
	} {
		if f, ok := readRewrite(bad); ok {
			t.Errorf("%s was read: %+v", name, f)
		}
	}
}

func TestAReviewIsAtMostFiveNotes(t *testing.T) {
	notes, ok := readNotes("## Notes\n- It does not say who uses it.\n- \"Fast\" is not a number.\n")
	if !ok || !slices.Equal(notes, []string{"It does not say who uses it.", "\"Fast\" is not a number."}) {
		t.Fatalf("%q %v", notes, ok)
	}
	for _, bad := range []string{"## Notes\n", "## Notes\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6", "Notes:\n- a", "## Rewrite\nx"} {
		if n, ok := readNotes(bad); ok {
			t.Errorf("%q was read: %q", bad, n)
		}
	}
}
