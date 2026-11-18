package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/memory"
	"github.com/kesokaj/crew/internal/settings"
)

// mergeQueue merges a project's tickets one at a time (7.10): a ticket that passed its last stage is
// merged with main in the canonical repository, where no sandbox reaches; a check sandbox runs the
// ticket's checks and the project's tests on the merge; when they pass, the board moves the ticket to
// Done and main to the merge. A conflict or a failure is a fix round, and the ticket stays in its
// stage.
func (p *Pipeline) mergeQueue(ctx context.Context, j *job) error {
	var busy bool
	if err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM check_runs WHERE project = $1 AND purpose = 'merge' AND state = 'running')`,
		j.proj.ID).Scan(&busy); err != nil || busy {
		return err
	}
	tickets, err := p.Board.Tickets(ctx, j.proj)
	if err != nil {
		return err
	}
	var queue []*board.Ticket
	for _, t := range tickets {
		next, ok := j.pipe.Next(t.Stage, t.Skips)
		if !ok || next.ID != settings.StageDone || t.Hold || t.Branch == nil || t.Stage == settings.StageAnalysis {
			continue
		}
		queue = append(queue, t)
	}
	sort.SliceStable(queue, func(a, b int) bool {
		if queue[a].Priority != queue[b].Priority {
			return queue[a].Priority < queue[b].Priority
		}
		return queue[a].StageAt.Before(queue[b].StageAt)
	})
	// Before a merge, the remote's main is fetched again: commits pushed there become the base first.
	var holdID int64
	var holdDetail string
	if len(queue) > 0 {
		if hold, err := p.remote(ctx, j, true); err != nil || hold {
			return err
		}
		var err error
		holdID, _, holdDetail, err = p.Board.RemoteHold(ctx, j.proj)
		if err != nil {
			return err
		}
	}
	// One ticket's error does not hold the tickets behind it: it waits with a fixed reason, and the pass
	// goes on; the errors are returned together, for the server's log (the security review of 0.3.7,
	// finding 4).
	var errs []error
	for _, t := range queue {
		if holdID != 0 && t.ID != holdID {
			if err := p.Board.Wait(ctx, t, holdDetail); err != nil {
				return errors.Join(append(errs, err)...)
			}
			continue
		}
		started, err := p.mergeOne(ctx, j, t)
		if started || ctx.Err() != nil {
			return errors.Join(append(errs, err)...)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Key, err))
			if werr := p.Board.Wait(ctx, t, CouldNotMerge); werr != nil {
				return errors.Join(append(errs, werr)...)
			}
		}
	}
	return errors.Join(errs...)
}

// reconcile moves what the board can move in the project, inside one ticket's step. Its error can be
// another ticket's, which the board already wrote on that ticket and logged, or the project's: it is
// logged, and the step goes on, so one ticket's error does not hold another (the security review of
// 0.3.7, finding 4).
func (p *Pipeline) reconcile(ctx context.Context, j *job) {
	if err := p.Board.Reconcile(ctx, j.proj.ID); err != nil {
		p.log().Error("reconciling the board in a ticket's step", "project", j.proj.Name, "error", err)
	}
}

// CouldNotMerge is why a ticket waits when Crew could not try its merge. It holds no error's text: an
// agent reads it through get_ticket, and an error can name the server's paths (13.8).
const CouldNotMerge = "Crew could not try its merge; the error is in Crew's server log"

// mergeOne tries the merge of one ticket whose every other check passes, and says whether it took the
// project's turn to merge.
func (p *Pipeline) mergeOne(ctx context.Context, j *job, t *board.Ticket) (bool, error) {
	subs, err := p.subtasks(ctx, t)
	if err != nil {
		return false, err
	}
	for _, s := range subs {
		if s.active() {
			return false, nil
		}
	}
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil || head == "" {
		return false, err
	}
	// A ticket that changes files that are code merges only once the operator allowed this head (13.8).
	// A head they refused goes back to its builder in place, once; any other head waits for them.
	code, err := p.Board.CodeChanges(ctx, j.proj, t)
	if err != nil {
		return false, err
	}
	if len(code) == 0 && t.CodeWait != nil {
		// The head that waited is gone, and this one changes no file that is code: nothing waits.
		if _, err := p.Pool.Exec(ctx, `UPDATE tickets SET code_wait = NULL WHERE id = $1`, t.ID); err != nil {
			return false, err
		}
	}
	if len(code) > 0 && !board.CodeAllowed(t, head) {
		if say := t.CodeSay; say != nil && say.Head == head && say.Choice == "refuse" {
			if fixedSince(t, subs, say.At) {
				return false, nil
			}
			return p.fix(ctx, j, t, subs, head, "operator", extra{name: "What the operator refused", who: "the operator, through Crew",
				text: "The operator refused the change to these files, which are code for the agent CLI or GitHub workflows: " +
					strings.Join(code, ", ") + ". Take those changes out of the branch: make the files as they are on main, commit, " +
					"push, add a note, and call fix_done."})
		}
		if err := p.Board.SetCodeWait(ctx, t, board.CodeWait{Head: head, Main: p.Board.Git.Main(ctx, j.proj.Name), Files: code}); err != nil {
			return false, err
		}
		return false, p.Board.Wait(ctx, t, board.CodeWaitText(code))
	}
	// When In progress is the last stage, a head that a fix round pushed has no run of the ticket's
	// checks yet, and no stage agent comes to run them: Crew runs them first. In a later stage its
	// agent and Crew's run after it do.
	if cmds := board.CmdChecks(t); len(cmds) > 0 && t.Stage == settings.StageInProgress {
		run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeExit, board.PurposeTest)
		if err != nil {
			return false, err
		}
		tries := 0
		if run != nil && run.State == board.CheckError {
			if tries, err = p.errorsOn(ctx, t, head, board.PurposeExit, board.PurposeTest); err != nil {
				return false, err
			}
		}
		switch {
		case run == nil:
			_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
			return false, err
		case run.State == board.CheckError && tries < 3:
			_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
			return false, err
		case run.State == board.CheckError:
			return false, p.Board.Wait(ctx, t, "Crew could not run its checks on "+head[:12]+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed:
			// Between the joins of parallel work a head fails for a while: it waits for the last join.
			// Otherwise the fix did not make the checks pass, and it is a fix round again; a ticket that
			// used its rounds goes to the lead, who looks at it again (7.7, 7.10).
			if partsOpen(subs) {
				return false, p.Board.Wait(ctx, t, "its checks failed on "+head[:12]+" before its parallel work is joined")
			}
			return p.fix(ctx, j, t, subs, head, "checks", extra{name: "What failed in Crew's run of the ticket's checks",
				who: "the commands, in Crew's check sandbox", text: failuresText(run)})
		}
	}
	results, err := p.Board.ExitResults(ctx, j.proj, t)
	if err != nil {
		return false, err
	}
	owed := false
	for _, r := range results {
		switch {
		case r.Passed || r.Name == "merges_and_passes_on_main":
		case r.Name == "review_covers_head":
			owed = true
		default:
			return false, nil
		}
	}
	// A head Review did not approve, or a branch that changes a package file, is read by a reviewer
	// first, in the ticket's stage (7.4, 13.8, since 0.90); that does not take the turn to merge.
	if owed {
		o, err := p.Board.ReviewOwed(ctx, j.proj, t, head)
		if err != nil || !o.Need {
			return false, err
		}
		return false, p.review(ctx, j, t, subs, head, o.Approved)
	}
	main := p.Board.Git.Main(ctx, j.proj.Name)
	// The newest merge of this head on main as it is decides what comes next.
	var last *board.CheckRun
	runs, err := p.Board.CheckRuns(ctx, t)
	if err != nil {
		return false, err
	}
	for i := range runs {
		r := runs[i]
		if r.Purpose == board.PurposeMerge && derefS(r.Head) == head && derefS(r.Main) == main {
			last = &r
			break
		}
	}
	if last != nil {
		switch last.State {
		case board.CheckPassed:
			// The board moves it to Done, with main to the merge, which is then recorded (7.11).
			p.reconcile(ctx, j)
			if now, err := p.Board.Ticket(ctx, j.proj, t.Key); err == nil && now.Stage == settings.StageDone {
				_, _ = p.Board.SyncMainWorkflow(ctx, p.Pool, j.proj)
				newMain := p.Board.Git.Main(ctx, j.proj.Name)
				if touched, terr := p.Board.Git.Touched(ctx, j.proj.Name, derefS(last.Main), newMain); terr == nil && len(touched) > 0 {
					paths := make([]string, 0, len(touched))
					for pth := range touched {
						paths = append(paths, pth)
					}
					_, _, _ = memory.ApplyCommitDrift(ctx, p.Pool, j.proj.ID, newMain, paths)
				}
				p.recordMerge(ctx, j, t)
			}
			return true, nil
		case board.CheckFailed:
			if fixedAfterRun(t, subs, last) {
				return false, nil
			}
			text := failuresText(last)
			if last.Commit == head {
				text = derefS(last.Detail)
			}
			// A ticket at its limit of fix rounds gives up its turn, so the next one merges.
			return p.fix(ctx, j, t, subs, head, "merge", extra{name: "What failed when Crew merged the ticket with main",
				who: "git and the commands, in Crew's own work tree and check sandbox", text: text})
		case board.CheckError:
			// Each candidate is a new commit: the errors are counted by the head they merge.
			n, err := p.mergeErrors(ctx, t, head)
			if err != nil {
				return false, err
			}
			if n >= 3 {
				return false, p.Board.Wait(ctx, t, "Crew could not check its merge with main three times: "+board.PublicDetail(last))
			}
		}
	}
	id, err := p.Identity(ctx)
	if err != nil {
		return false, err
	}
	overlay, err := p.mergeOverlay(ctx, j, t)
	if err != nil {
		return false, err
	}
	c, err := p.Board.Git.MergeCandidate(ctx, j.proj.Name, *t.Branch, board.MergeMessage(t), id, overlay)
	if errors.Is(err, gitstore.ErrNoHead) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(c.Conflicts) > 0 {
		// A conflict is recorded as a merge that failed on the ticket's head, with the files.
		detail := "The ticket's branch conflicts with main in: " + strings.Join(c.Conflicts, ", ") +
			". Merge main into the branch, resolve the conflicts, and push."
		return true, p.Board.RecordCheckRun(ctx, j.proj, t, board.PurposeMerge, head, c.Main, head, board.CheckFailed, nil, detail)
	}
	if c.UpToDate {
		return false, p.Board.Wait(ctx, t, "main has its branch already: there is nothing to merge")
	}
	if c.FastForward {
		if prev, err := board.LatestCheckRun(ctx, p.Pool, t.ID, c.Head, board.PurposeTest); err == nil && prev != nil && prev.State == board.CheckPassed {
			return true, p.Board.RecordCheckRun(ctx, j.proj, t, board.PurposeMerge, c.Commit, c.Main, c.Head, board.CheckPassed, prev.Results, "")
		}
	}
	_, err = p.runChecks(ctx, j, t, board.PurposeMerge, c.Commit, c.Main, c.Head,
		checkCmds(board.CmdChecks(t), str(j.doc, "sandbox.test")))
	return err == nil, err
}

// mergeErrors counts the merges of head that ended as errors, or returns the error that kept it from
// counting: no merge is tried on a count that was not read.
func (p *Pipeline) mergeErrors(ctx context.Context, t *board.Ticket, head string) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND purpose = 'merge' AND head = $2 AND state = 'error' AND `+notSetup+``,
		t.ID, head).Scan(&n)
	return n, err
}

// fixedSince says a fix round began after a time.
func fixedSince(t *board.Ticket, subs []*sub, at time.Time) bool {
	for _, s := range subs {
		if fixOrApproach(t, s) && s.created.After(at) {
			return true
		}
	}
	return false
}

// fixedAfterRun says a fix round began after the check run ended.
func fixedAfterRun(t *board.Ticket, subs []*sub, r *board.CheckRun) bool {
	for _, s := range subs {
		if fixOrApproach(t, s) && r.EndedAt != nil && s.created.After(*r.EndedAt) {
			return true
		}
	}
	return false
}

// recordMerge records the merge that moved main as a subtask of the ticket with no agent (7.11), for
// the ticket's view, once.
func (p *Pipeline) recordMerge(ctx context.Context, j *job, t *board.Ticket) {
	err := pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		if err := board.LockForSubtasks(ctx, tx, t.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title, ended_at)
			SELECT 'ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, 'crew', 'done',
				'Merge into main', now()
			WHERE NOT EXISTS (SELECT 1 FROM subtasks WHERE ticket = $2 AND kind = $3)`, j.proj.ID, t.ID, kindMerge)
		return err
	})
	if err != nil {
		p.log().Error("recording the merge subtask", "ticket", t.Key, "error", err)
	}
}

// mergeOverlay is what Crew writes into the ticket's merge commit itself (9.9): its entry in the
// changelog, from its title, its what, and the builder's last note, and its decision records' pages.
func (p *Pipeline) mergeOverlay(ctx context.Context, j *job, t *board.Ticket) (gitstore.Overlay, error) {
	recs, err := p.Board.Records(ctx, t, false)
	if err != nil {
		return nil, err
	}
	note := ""
	for _, r := range recs {
		if r.Kind == board.RecordNote && r.Author == "builder" {
			note = r.Body
		}
	}
	o, err := p.Board.MergeOverlay(ctx, t, str(j.doc, "docs.changelog"), note, time.Now())
	if err != nil {
		return nil, err
	}
	if m, merr := p.Board.MirrorOverlay(ctx, p.Pool, j.proj, t); merr == nil {
		for k, v := range m {
			o[k] = v
		}
	}
	return o, nil
}
