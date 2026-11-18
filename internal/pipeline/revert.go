package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

// revertStep is In progress for a ticket of the kind revert (7.13). The backend commits the revert of
// the merge on main as it is now, as the ticket's branch, and notes it; the ticket's checks then
// decide, as a builder's request_move would. A revert that does not apply gets a builder, with the
// files that conflict; one whose checks fail gets a fix round. What follows In progress is the
// project's stages, as for any ticket.
func (p *Pipeline) revertStep(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	for _, s := range since(t, subs) {
		if s.kind == kindBuild || s.kind == kindFix {
			return nil
		}
	}
	if t.Branch == nil || t.RevertOf == nil {
		return nil
	}
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil {
		return err
	}
	if head == "" {
		return p.commitRevert(ctx, j, t, subs)
	}
	cmds := board.CmdChecks(t)
	run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeExit, board.PurposeTest)
	if err != nil {
		return err
	}
	tries := 0
	if run != nil && run.State == board.CheckError {
		if tries, err = p.errorsOn(ctx, t, head, board.PurposeExit, board.PurposeTest); err != nil {
			return err
		}
	}
	switch {
	case run == nil:
		_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
		return err
	case run.State == board.CheckRunning:
		return nil
	case run.State == board.CheckError && tries < 3:
		_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
		return err
	case run.State == board.CheckError:
		return p.Board.Wait(ctx, t, "Crew could not run its checks on "+head[:12]+" three times: "+board.PublicDetail(run))
	case run.State == board.CheckFailed:
		_, err := p.fix(ctx, j, t, subs, head, "checks", extra{name: "What failed in Crew's run of the revert's checks",
			who: "the commands, in Crew's check sandbox", text: failuresText(run)})
		return err
	}
	p.reconcile(ctx, j)
	p.Board.Kick()
	return nil
}

func (p *Pipeline) commitRevert(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	merge := *t.RevertOf
	var id gitstore.Identity
	if p.Identity != nil {
		var err error
		if id, err = p.Identity(ctx); err != nil {
			return err
		}
	}
	msg := fmt.Sprintf("%s\n\nThis reverts the merge %s.\n\nCrew-Ticket: %s", t.Title, merge, t.Key)
	commit, conflicts, err := p.Board.Git.Revert(ctx, j.proj.Name, merge, msg, id)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		recs, err := p.records(ctx, t)
		if err != nil {
			return err
		}
		text := fmt.Sprintf("Crew could not revert the merge %s by itself: main changed these files since, and they conflict:\n\n- %s\n\n"+
			"Run git revert -m 1 %s on your branch, resolve the conflicts so that the change of the merge is gone and what came after stays, "+
			"run the ticket's checks, commit, push, add a note, and call request_move.", merge[:12], strings.Join(conflicts, "\n- "), merge)
		parts := p.ticketPrompt(ctx, promptInput{stage: "in_progress", role: "builder", project: j.proj, ticket: t, records: recs,
			extra:   []extra{{name: "The revert that did not apply", who: "Crew, naming files of the repository", text: text}},
			context: "Your workspace is a clone of the ticket's branch " + *t.Branch + ", cut from main, which you push."})
		_, err = p.start(ctx, j, t, startSpec{kind: kindBuild, role: "builder", title: "Revert by hand", parts: parts, branch: *t.Branch})
		return err
	}
	if err := p.Board.Git.AdvanceBranch(ctx, j.proj.Name, *t.Branch, commit); err != nil {
		return err
	}
	_, err = p.Board.AddRecord(ctx, j.proj, t.Key, board.NewRecord{Kind: board.RecordNote, Author: "crew",
		Body: fmt.Sprintf("Crew committed the revert of the merge %s as %s, on main as it was. The ticket's checks run next.", merge[:12], commit[:12])})
	return err
}
