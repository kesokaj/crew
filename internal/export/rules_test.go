package export

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// The board's rules an import keeps: moves that chain from where a ticket began to where it is, never
// over a fixed stage, and only to the right.
func TestTheMovesOfAnImportedTicket(t *testing.T) {
	d, _ := settings.ProjectDefaults("shop", "SF")
	pipe := board.PipelineOf(d)
	mv := func(pairs ...string) []Move {
		var out []Move
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, Move{From: pairs[i], To: pairs[i+1], Actor: "backend"})
		}
		return out
	}
	ok := board.Analysis{Status: board.StatusReady}
	for _, c := range []struct {
		name  string
		stage string
		moves []Move
		want  string // "" is accepted
	}{
		{"the whole line", "done", mv("planning", "backlog", "backlog", "analysis", "analysis", "in_progress", "in_progress", "review", "review", "test", "test", "done"), ""},
		{"added after the OK", "analysis", mv("backlog", "analysis"), ""},
		{"no move, in backlog", "backlog", nil, ""},
		{"closed from planning", "closed", mv("planning", "closed"), ""},
		{"over Analysis and In progress", "done", mv("planning", "backlog", "backlog", "done"), "passes over analysis"},
		{"no move, in done", "done", nil, "its moves end in planning"},
		{"to the left", "backlog", mv("planning", "backlog", "backlog", "analysis", "analysis", "backlog"), "to the right"},
		{"Test skipped", "done", mv("planning", "backlog", "backlog", "analysis", "analysis", "in_progress", "in_progress", "review", "review", "done"), ""},
		{"over Review, on and not skipped", "done", mv("planning", "backlog", "backlog", "analysis", "analysis", "in_progress", "in_progress", "done"), "passes over review"},
		{"a gap", "review", mv("planning", "backlog", "analysis", "in_progress"), "does not follow"},
	} {
		tf := TaskFile{Key: "SF-1", Stage: c.stage, Moves: c.moves, Analysis: ok, Origin: "agent"}
		if c.stage == "closed" {
			r := "discarded"
			tf.ClosedReason = &r
		}
		if c.name == "Test skipped" {
			tf.Skips = []string{"test"}
		}
		err := boardRules(&tf, pipe)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if err == nil {
			for _, m := range tf.Moves {
				if !m.Imported {
					t.Errorf("%s: a move is not marked imported", c.name)
				}
			}
		}
	}
}
