package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

// The subtasks agents create on a ticket, run in parallel and joined by the backend (7.5, 7.11, 11.4):
//
//  1. Start: every queued subtask whose after is done and joined, and whose files no running subtask
//     names, starts, up to limits.max_parallel_subtasks, each on its branch sub/<ticket>.<n> cut from
//     the ticket's branch. A subtask that names no files runs alone.
//  2. Check: when its run says subtask_done, its own check runs on its branch in a check sandbox.
//  3. Join: one at a time, while no run works on the ticket's branch, the backend merges a done
//     subtask's branch into the ticket's branch, runs its check on the result, and moves the branch.
//  4. A conflict, or checks that fail after the last join, is one builder subtask that joins the work
//     on the ticket's branch, with both sides and the output. A second one that does not get through
//     sends the ticket to the lead, who looks at it again (7.7).
//  5. After the last join the ticket's checks run on its branch, and the board moves the ticket.

// partsOpen says some parallel work of the ticket is not done and joined yet.
func partsOpen(subs []*sub) bool {
	return slices.ContainsFunc(subs, func(s *sub) bool {
		switch {
		case s.kind != kindPart && s.kind != kindJoin:
			return false
		case s.state == "cancelled" || s.state == "superseded":
			return false
		case s.kind == kindPart && s.state == "done":
			return s.joined == nil
		}
		return s.state != "done"
	})
}

// hasParts says agents created subtasks for the current build of the ticket, or an earlier part is
// not done and joined yet. Parts cancelled or superseded, or already done and joined before a new
// approach's build, do not send the new build through parts().
func hasParts(subs []*sub) bool {
	var build int64
	for _, s := range subs {
		if s.kind == kindBuild {
			build = s.id
		}
	}
	return slices.ContainsFunc(subs, func(s *sub) bool {
		return s.kind == kindPart && s.state != "cancelled" && s.state != "superseded" &&
			(s.id > build || !(s.state == "done" && s.joined != nil))
	})
}

// partBranch is a parallel subtask's own branch (7.11).
func partBranch(t *board.Ticket, s *sub) string { return fmt.Sprintf("sub/%s.%d", t.Key, s.number) }

// overlaps says two subtasks name a file or a folder in common, or one of them names none, which
// runs alone (7.11).
func overlaps(a, b *sub) bool {
	if len(a.touches) == 0 || len(b.touches) == 0 {
		return true
	}
	for _, x := range a.touches {
		for _, y := range b.touches {
			if cli.Below(strings.TrimSuffix(x, "/"), []string{y}) || cli.Below(strings.TrimSuffix(y, "/"), []string{x}) {
				return true
			}
		}
	}
	return false
}

// parts is a pass over a ticket in In progress whose work is in subtasks agents created.
func (p *Pipeline) parts(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	if t.Branch == nil {
		return nil
	}
	byID := map[int64]*sub{}
	for _, s := range subs {
		byID[s.id] = s
	}
	var running []*sub
	for _, s := range subs {
		if (s.kind == kindPart || s.kind == kindJoin) && s.state == "stopped" {
			return p.lookAgain(ctx, j, t, subs, fmt.Sprintf("its subtask %s.%d %s stopped twice", t.Key, s.number, s.title))
		}
		if s.kind == kindPart && s.active() {
			running = append(running, s)
		}
	}
	// 1. Start what may start.
	room := board.LimitsOf(j.doc).ParallelRoom(len(running))
	for _, s := range subs {
		if room == 0 {
			break
		}
		if s.kind != kindPart || s.state != "queued" {
			continue
		}
		ready := true
		for _, a := range s.after {
			if b := byID[a]; b == nil || b.state != "done" || b.joined == nil {
				ready = false
			}
		}
		for _, r := range running {
			ready = ready && !overlaps(s, r)
		}
		if !ready {
			continue
		}
		run, err := p.startPart(ctx, j, t, s)
		if err != nil || run == "" {
			return err
		}
		running = append(running, s)
		room--
	}
	// 3. Join, one at a time, while nothing works on the ticket's branch.
	for _, s := range subs {
		if s.active() && (s.branch == *t.Branch || s.kind == kindJoin) {
			return nil
		}
	}
	if busy, err := p.checking(ctx, t); err != nil || busy {
		return err
	}
	for _, s := range subs {
		if s.kind == kindPart && s.state == "done" && s.joined == nil {
			return p.join(ctx, j, t, subs, s)
		}
	}
	// 5. Everything is done and joined: the ticket's checks on its branch decide the move.
	for _, s := range subs {
		if (s.kind == kindPart || s.kind == kindJoin) && s.state != "done" && s.state != "cancelled" && s.state != "superseded" {
			return nil
		}
	}
	return p.afterJoins(ctx, j, t, subs)
}

// checking says a check run of the ticket runs now.
func (p *Pipeline) checking(ctx context.Context, t *board.Ticket) (bool, error) {
	var busy bool
	err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM check_runs WHERE ticket = $1 AND state = 'running')`, t.ID).Scan(&busy)
	return busy, err
}

// startPart starts the run of a queued subtask on its own branch, cut from the ticket's branch, or
// from main while the ticket's branch is not pushed yet.
func (p *Pipeline) startPart(ctx context.Context, j *job, t *board.Ticket, s *sub) (string, error) {
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil {
		return "", err
	}
	base := "refs/heads/main"
	if head != "" {
		base = "refs/heads/" + *t.Branch
	}
	branch := partBranch(t, s)
	recs, err := p.records(ctx, t)
	if err != nil {
		return "", err
	}
	plan, err := p.planOf(ctx, j, t)
	if err != nil {
		return "", err
	}
	touches := "It names no files, so it runs alone."
	if len(s.touches) > 0 {
		touches = "Change only these files or folders: " + strings.Join(s.touches, ", ") + ". A push that changes another is refused."
	}
	text := fmt.Sprintf("%s.%d %s\nWhat to do: %s\nDone when: %s\n", t.Key, s.number, s.title, s.what, s.doneAs)
	if s.check != "" {
		text += "Its check, which Crew runs on its branch: " + s.check + "\n"
	}
	creator, _, _ := strings.Cut(s.createdBy, " · ")
	parts := p.ticketPrompt(ctx, promptInput{stage: "part", role: s.role, project: j.proj, ticket: t, plan: plan, records: recs,
		extra: []extra{{name: "This subtask", who: "the " + creator + " that created it with create_subtask", text: text}},
		context: fmt.Sprintf("You work on one subtask of the ticket, %s.%d. Your workspace is a clone of its own branch %s, cut from the "+
			"ticket's branch; push only that branch. %s Other subtasks may run beside you on branches of their own.", t.Key, s.number, branch, touches)})
	return p.start(ctx, j, t, startSpec{kind: kindPart, role: s.role, title: s.title, parts: parts, branch: branch, base: base, subtask: s.id})
}

// afterPart acts on the turn of a subtask an agent created, or of a join: with subtask_done, its own
// check runs on its branch, and the run ends when it passes; what fails goes back to the run as a
// round of its goal loop (11.3).
func (p *Pipeline) afterPart(ctx context.Context, j *job, t *board.Ticket, s *sub) error {
	if s.reported == nil {
		return p.missing(ctx, j, s)
	}
	branch := s.branch
	if branch == "" {
		branch = *t.Branch
	}
	head, err := p.Board.Git.Head(ctx, j.proj.Name, branch)
	if err != nil {
		return err
	}
	if head == "" {
		return p.round(ctx, j, t, s, "Your branch "+branch+" is not pushed: Crew finds no commit of it. Commit, push, and call subtask_done again.")
	}
	if s.kind == kindPart && s.check != "" {
		run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposePart)
		if err != nil {
			return err
		}
		tries := 0
		if run != nil && run.State == board.CheckError {
			if tries, err = p.errorsOn(ctx, t, head, board.PurposePart); err != nil {
				return err
			}
		}
		cmds := checkCmds([]board.CheckSpec{{Kind: "cmd", Spec: s.check}}, "")
		switch {
		case run == nil || (run.State == board.CheckError && tries < 3):
			_, err := p.runChecks(ctx, j, t, board.PurposePart, head, "", "", cmds)
			return err
		case run.State == board.CheckRunning:
			return nil
		case run.State == board.CheckError:
			return p.Board.Wait(ctx, t, "Crew could not run the check of "+t.Key+"."+fmt.Sprint(s.number)+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed:
			printed := failuresText(run)
			f := newFence(printed)
			return p.round(ctx, j, t, s, "Crew ran this subtask's check on your branch, and it did not pass.\n\n"+
				f.data("What the check printed in Crew's check sandbox", "the command", printed)+
				"\n\nFix what failed, commit, push, and call subtask_done again.")
		}
	}
	if err := p.Runs.Finish(ctx, s.run); err != nil {
		return err
	}
	p.Board.Kick()
	return nil
}

// join merges a done subtask's branch into the ticket's branch: its check runs on the merge, and the
// ticket's branch moves to it when it passes. A conflict, or a check that fails there, is a builder
// subtask that joins the work by hand.
func (p *Pipeline) join(ctx context.Context, j *job, t *board.Ticket, subs []*sub, s *sub) error {
	id, err := p.Identity(ctx)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%s: join %s.%d %s\n\nTicket: %s\n", t.Key, t.Key, s.number, s.title, t.Key)
	c, err := p.Board.Git.JoinCandidate(ctx, j.proj.Name, *t.Branch, s.branch, msg, id)
	switch {
	case errors.Is(err, gitstore.ErrNoHead):
		// It pushed nothing: there is nothing to join.
		return p.joined(ctx, j, t, s, "it pushed nothing")
	case err != nil:
		return err
	case c.UpToDate:
		return p.joined(ctx, j, t, s, "the ticket's branch has it already")
	case len(c.Conflicts) > 0:
		return p.joinByHand(ctx, j, t, subs, s, fmt.Sprintf("Merging %s into %s conflicts in: %s.", s.branch, *t.Branch,
			strings.Join(c.Conflicts, ", ")))
	}
	if s.check != "" {
		run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, c.Commit, board.PurposeJoin)
		if err != nil {
			return err
		}
		tries := 0
		if run != nil && run.State == board.CheckError {
			if tries, err = p.errorsOn(ctx, t, c.Commit, board.PurposeJoin); err != nil {
				return err
			}
		}
		cmds := checkCmds([]board.CheckSpec{{Kind: "cmd", Spec: s.check}}, "")
		switch {
		case run == nil || (run.State == board.CheckError && tries < 3):
			_, err := p.runChecks(ctx, j, t, board.PurposeJoin, c.Commit, c.Base, c.Head, cmds)
			return err
		case run.State == board.CheckError:
			return p.Board.Wait(ctx, t, "Crew could not run the check of the join of "+t.Key+"."+fmt.Sprint(s.number)+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed:
			return p.joinByHand(ctx, j, t, subs, s, "The check of "+t.Key+"."+fmt.Sprint(s.number)+" fails on its merge into the ticket's branch:\n"+
				failuresText(run))
		}
	}
	if err := p.Board.Git.AdvanceBranch(ctx, j.proj.Name, *t.Branch, c.Commit); err != nil {
		// A run moved the ticket's branch meanwhile: the next pass joins again on top of it.
		p.log().Warn("the ticket's branch did not move to its join", "ticket", t.Key, "error", err)
		return nil
	}
	return p.joined(ctx, j, t, s, "merged as "+c.Commit[:12])
}

// joined marks a subtask joined, with a subtask of the backend that records the join (7.11).
func (p *Pipeline) joined(ctx context.Context, j *job, t *board.Ticket, s *sub, how string) error {
	err := pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		// The project and the ticket are locked before a subtask's row, in the board's order.
		if err := board.LockForSubtasks(ctx, tx, t.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE subtasks SET joined_at = now() WHERE id = $1 AND joined_at IS NULL`, s.id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title, created_by, ended_at)
			VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, 'crew', 'done', $4, 'backend', now())`,
			j.proj.ID, t.ID, kindJoin, fmt.Sprintf("Join %s.%d: %s", t.Key, s.number, how))
		return err
	})
	if err == nil {
		p.Kick()
	}
	return err
}

// joinByHand starts one builder subtask that joins work that did not join by itself, on the ticket's
// branch, with what failed (7.11, step 5). It does not count toward max_added_subtasks. A second one
// that does not get through sends the ticket to the lead, who looks at it again (7.7); the tries
// before a new approach no longer count.
func (p *Pipeline) joinByHand(ctx context.Context, j *job, t *board.Ticket, subs []*sub, s *sub, why string) error {
	n := 0
	for _, x := range subs {
		if x.kind == kindJoin && x.role == "builder" && (t.ApproachAt == nil || x.created.After(*t.ApproachAt)) {
			n++
		}
	}
	if n >= 2 {
		return p.lookAgain(ctx, j, t, subs, "its parallel work did not join after two tries")
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return err
	}
	read := ""
	base := ""
	if s != nil {
		read = " The subtask's branch " + s.branch + " is readable as origin/" + s.branch + ": merge it into the ticket's branch."
		base = "refs/heads/" + s.branch
	}
	parts := p.ticketPrompt(ctx, promptInput{stage: "join", role: "builder", project: j.proj, ticket: t, records: recs,
		extra:   []extra{{name: "What did not join", who: "git and the commands, in Crew's own work tree and check sandbox", text: why}},
		context: "Your workspace is a clone of the ticket's branch " + *t.Branch + ", which you push." + read})
	_, err = p.start(ctx, j, t, startSpec{kind: kindJoin, role: "builder", title: "Join the parallel work", parts: parts,
		branch: *t.Branch, base: base})
	return err
}

// afterJoins runs the ticket's checks on its branch once every subtask is done and joined: when they
// pass, the board moves the ticket; when they fail, a builder joins the work by hand.
func (p *Pipeline) afterJoins(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil || head == "" {
		return err
	}
	if cmds := board.CmdChecks(t); len(cmds) > 0 {
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
		case run == nil || (run.State == board.CheckError && tries < 3):
			_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
			return err
		case run.State == board.CheckRunning:
			return nil
		case run.State == board.CheckError:
			return p.Board.Wait(ctx, t, "Crew could not run its checks on "+head[:12]+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed:
			for _, x := range subs {
				// The builder that joined the work has this head's failure already.
				if x.kind == kindJoin && x.role == "builder" && x.created.After(*run.EndedAt) {
					return nil
				}
			}
			return p.joinByHand(ctx, j, t, subs, nil, "The ticket's checks fail on its branch after the last join:\n"+failuresText(run))
		}
	}
	// The commands pass on the joined work: a judge reads what they cannot prove (7.1).
	if ok, err := p.judge(ctx, j, t, subs, head); err != nil || !ok {
		return err
	}
	if run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeJudge); err != nil {
		return err
	} else if run != nil && run.State == board.CheckFailed {
		for _, x := range subs {
			if x.kind == kindJoin && x.role == "builder" && x.created.After(*run.EndedAt) {
				return nil
			}
		}
		return p.joinByHand(ctx, j, t, subs, nil, "A judge found criteria of the ticket not met after the last join:\n"+judgeText(run))
	}
	p.reconcile(ctx, j)
	return nil
}
