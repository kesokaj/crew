package board_test

import (
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// The limits of a project (7.7), as plain functions of numbers (22, "Limits of a project"). None is in
// dollars (since 0.53): the share agents may add is of the tickets of the approved plan.
func TestLimits(t *testing.T) {
	d, _ := settings.ProjectDefaults("shop", "SF")
	l := board.LimitsOf(d)
	if l.MaxFixRounds != 3 || l.MaxAddedShare != 0.25 || l.MaxAddedSubtasks != 6 || l.MaxParallelSubtask != 3 {
		t.Fatalf("limits = %+v", l)
	}
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"two fix rounds", l.FixRoundsReached(2), false},
		{"three fix rounds", l.FixRoundsReached(3), true},
		{"no plan's tickets", l.AddedShareExceeded(1000, 0), false},
		{"added tickets at their share", l.AddedShareExceeded(4, 16), false},
		{"added tickets past their share", l.AddedShareExceeded(5, 16), true},
		{"one added of one plan's ticket", l.AddedShareExceeded(1, 1), true},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v", c.name, c.got)
		}
	}
	if err := l.MayAddSubtask(5); err != nil {
		t.Errorf("the sixth subtask: %v", err)
	}
	if err := l.MayAddSubtask(6); err == nil {
		t.Error("the seventh subtask was allowed")
	}
	if l.ParallelRoom(1) != 2 || l.ParallelRoom(3) != 0 || l.ParallelRoom(5) != 0 {
		t.Errorf("parallel room: %d %d %d", l.ParallelRoom(1), l.ParallelRoom(3), l.ParallelRoom(5))
	}
}
