package pipeline

import (
	"context"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// markMovedOn marks taken up a builder whose run ended while it waited, once its ticket has moved on
// from In progress: only In progress takes such a builder up, and the merge that its judge's answer
// lets start, or the board's move to a later stage on that answer, can move the ticket before a pass
// comes back to finish the run (BUG-028). A closed ticket did not move on by its checks: its run
// keeps saying that no next turn came.
func (p *Pipeline) markMovedOn(ctx context.Context, t *board.Ticket, subs []*sub) error {
	if t.Stage == settings.StageInProgress || t.Stage == settings.StageClosed {
		return nil
	}
	var last *sub
	for _, s := range subs {
		if s.kind == kindBuild || s.kind == kindFix {
			last = s
		}
	}
	if last == nil || !last.waitedOut() {
		return nil
	}
	return p.finish(ctx, last)
}
