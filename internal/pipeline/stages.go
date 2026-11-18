package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/kesokaj/crew/internal/analytics"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// stage starts what a ticket's stage needs (15.2): its builder in In progress; in Review and Test the
// stage's agent for the branch as it is, once per commit, and Crew's own run of the checks after the
// tester; and a fix round for a stage that failed. A ticket whose run stopped twice goes to the lead,
// who looks at it again (7.7).
func (p *Pipeline) stage(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	st, ok := j.pipe.Stage(t.Stage)
	if !ok || st.Role == "" {
		return nil
	}
	// Work in subtasks agents created runs in parallel, beside a builder that may still work (7.11).
	if t.Stage == settings.StageInProgress && hasParts(subs) {
		if err := p.releaseIfFailureLimitRaised(ctx, j, t); err != nil {
			return err
		}
		if t.Hold {
			return nil
		}
		return p.parts(ctx, j, t, subs)
	}
	for _, s := range subs {
		if s.active() {
			return nil
		}
	}
	if err := p.releaseIfFailureLimitRaised(ctx, j, t); err != nil {
		return err
	}
	if t.Hold {
		return nil
	}
	if last := lastNonHelper(subs); last != nil && last.state == "stopped" {
		return p.lookAgain(ctx, j, t, subs, fmt.Sprintf("its subtask %s.%d %s stopped twice", t.Key, last.number, last.title))
	}
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil {
		return err
	}
	switch t.Stage {
	case settings.StageInProgress:
		return p.inProgress(ctx, j, t, subs)
	case settings.StageReview:
		return p.review(ctx, j, t, subs, head, "")
	case settings.StageTest:
		return p.test(ctx, j, t, subs, head)
	case settings.StageE2E:
		return p.e2e(ctx, j, t, subs, head, st.Run)
	case settings.StageVerification:
		return p.verification(ctx, j, t, subs, head)
	}
	if st.Own() {
		return p.ownStage(ctx, j, t, subs, head, st)
	}
	return p.Board.Wait(ctx, t, "Crew has no agent for the stage "+t.Stage)
}

// since are the subtasks a ticket made in its stage.
func since(t *board.Ticket, subs []*sub) []*sub {
	var out []*sub
	for _, s := range subs {
		if !s.created.Before(t.StageAt) {
			out = append(out, s)
		}
	}
	return out
}

// records are a ticket's notes, reviews, and test entries, for a prompt.
func (p *Pipeline) records(ctx context.Context, t *board.Ticket) ([]board.Record, error) {
	all, err := p.Board.Records(ctx, t, false)
	if err != nil {
		return nil, err
	}
	// The newest review and the newest test entry always, then the newest notes, ten entries in all:
	// enough to know what happened, a prompt that does not grow without end, and no note that pushes
	// a verdict out of it.
	keep := map[int64]bool{}
	for _, kind := range []string{board.RecordReview, board.RecordTest, board.RecordE2E, board.RecordVerify, board.RecordStage} {
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].Kind == kind {
				keep[all[i].ID] = true
				break
			}
		}
	}
	for i := len(all) - 1; i >= 0 && len(keep) < 10; i-- {
		if all[i].Kind == board.RecordNote {
			keep[all[i].ID] = true
		}
	}
	var out []board.Record
	for _, r := range all {
		if keep[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (p *Pipeline) planOf(ctx context.Context, j *job, t *board.Ticket) (string, error) {
	plan, err := p.Board.Plan(ctx, j.proj)
	if err != nil {
		return "", err
	}
	return planPart(plan.Original, t), nil
}

// inProgress starts the ticket's builder, once: its goal loop and its retry continue the same run.
func (p *Pipeline) inProgress(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	if t.Kind == "revert" {
		return p.revertStep(ctx, j, t, subs)
	}
	// Its builder ran: what follows is the merge queue's, which says why it waits. A builder whose turn
	// asked to move, and whose run then ended while it waited, is taken up as if that turn had just
	// ended: its checks and its judge start (since 0.55, the operator's answer 1 of report 10).
	var last *sub
	for _, s := range since(t, subs) {
		if s.kind == kindBuild || s.kind == kindFix {
			last = s
		}
	}
	if last != nil {
		if last.waitedOut() {
			return p.afterBuild(ctx, j, t, subs, last)
		}
		return nil
	}
	if t.Branch == nil {
		return nil
	}
	if p.circuitBreakerWaiting(ctx, j.proj.ID, t, subs) {
		return nil
	}
	plan, err := p.planOf(ctx, j, t)
	if err != nil {
		return err
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return err
	}
	unresolved, err := p.Board.Unresolved(ctx, t)
	if err != nil {
		return err
	}
	parts := p.ticketPrompt(ctx, promptInput{stage: "in_progress", role: "builder", project: j.proj, ticket: t, plan: plan, records: recs,
		comments: unresolved, context: "Your workspace is a clone of the ticket's branch " + *t.Branch + ", which you push."})
	_, err = p.start(ctx, j, t, startSpec{kind: kindBuild, role: "builder", title: "Build", parts: parts, branch: *t.Branch,
		continues: lastBuild(subs), comments: unresolved})
	return err
}

// afterBuild acts on a builder's turn. After request_move Crew runs the ticket's command checks on
// the branch's head in a check sandbox, and then the stage's exit checks: the ticket moves, or the
// failures go back into the same conversation as one more round (11.3). After fix_done the run ends
// once something was pushed: the stage then looks at the branch again.
func (p *Pipeline) afterBuild(ctx context.Context, j *job, t *board.Ticket, subs []*sub, s *sub) error {
	if t.Stage == settings.StageDone || t.Stage == settings.StageClosed || (s.kind == kindBuild && t.Stage != settings.StageInProgress) {
		return p.finish(ctx, s)
	}
	r := s.reported
	if r == nil {
		return p.missing(ctx, j, s)
	}
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil {
		return err
	}
	if head == "" {
		return p.round(ctx, j, t, s, "Your branch is not pushed: Crew finds no commit of it. Commit, push, and call "+r.Tool+" again.")
	}
	// A ticket with parts agents created is checked after its last join, on all of its work (7.11):
	// the builder's own part is done.
	if s.kind == kindBuild && hasParts(subs) {
		if err := p.finish(ctx, s); err != nil {
			return err
		}
		p.Board.Kick()
		return nil
	}
	if s.kind == kindFix {
		if head == s.commit {
			return p.round(ctx, j, t, s, "Nothing was pushed since this fix began: the branch is still at "+head[:12]+
				". Commit and push your fix, then call fix_done again.")
		}
		if err := p.finish(ctx, s); err != nil {
			return err
		}
		p.Board.Kick()
		return nil
	}
	cmds := board.CmdChecks(t)
	if len(cmds) > 0 {
		run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeExit, board.PurposeTest)
		if err != nil {
			return err
		}
		switch {
		case run == nil:
			_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
			return err
		case run.State == board.CheckRunning:
			return nil
		case run.State == board.CheckError:
			n, err := p.errorsOn(ctx, t, head, board.PurposeExit, board.PurposeTest)
			if err != nil {
				return err
			}
			if n < 3 {
				_, err := p.runChecks(ctx, j, t, board.PurposeExit, head, "", "", checkCmds(cmds, ""))
				return err
			}
			return p.Board.Wait(ctx, t, "Crew could not run its checks on "+head[:12]+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckPassed:
			// The commands pass: a judge reads what they cannot prove (7.1).
			if ok, err := p.judge(ctx, j, t, subs, head); err != nil || !ok {
				return err
			}
		}
	} else if ok, err := p.judge(ctx, j, t, subs, head); err != nil || !ok {
		return err
	}
	p.reconcile(ctx, j)
	now, err := p.Board.Ticket(ctx, j.proj, t.Key)
	if err != nil {
		return err
	}
	if now.Stage != settings.StageInProgress {
		return p.finish(ctx, s)
	}
	results, err := p.Board.ExitResults(ctx, j.proj, now)
	if err != nil {
		return err
	}
	var failed []string
	for _, x := range results {
		// The merge, and the review of the head before it, are Crew's own steps after the builder's:
		// they are no failure of the build.
		if !x.Passed && x.Name != "merges_and_passes_on_main" && x.Name != "review_covers_head" {
			failed = append(failed, "- "+x.Name+": "+x.Detail)
		}
	}
	if len(failed) == 0 {
		// It passes and waits only for room in the next stage, or for its merge: the run's work is done.
		return p.finish(ctx, s)
	}
	// The details name the ticket's checks, which an agent wrote: they are data, as the commands'
	// output is (13.8).
	list := strings.Join(failed, "\n")
	printed := ""
	if run, _ := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeExit); run != nil && run.State == board.CheckFailed {
		printed = failuresText(run)
	}
	if (t.SpecRef != "" || t.AssignedAgent != "") && t.TriesUsed+1 >= p.EffectiveMaxTries(ctx, j.proj.ID, t) {
		if err := p.Pool.QueryRow(ctx, `UPDATE tickets SET tries_used = tries_used + 1 WHERE id = $1 RETURNING tries_used`, t.ID).Scan(&t.TriesUsed); err != nil {
			return err
		}
		_, err := p.TriggerResearchCircuitBreaker(ctx, j.proj, t, s.run, list+"\n\n"+printed)
		return err
	}
	f := newFence(list, printed)
	text := "Crew ran the exit checks of In progress, and some did not pass.\n\n" + f.data("The exit checks that did not pass", "Crew, naming the ticket's checks", list)
	if printed != "" {
		text += "\n\n" + f.data("What the failed commands printed in Crew's check sandbox", "the commands", printed)
	}
	text += "\n\nFix what failed, commit, push, add a note, and call request_move again."
	return p.round(ctx, j, t, s, text)
}

// errorsOn counts the check runs of purposes on commit that ended as errors: each kind of check has
// its own three tries. A count that cannot be read is an error, which starts nothing (BUG-012's rule).
func (p *Pipeline) errorsOn(ctx context.Context, t *board.Ticket, commit string, purposes ...string) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND commit = $2 AND state = 'error' AND `+notSetup+` AND purpose = ANY($3)`,
		t.ID, commit, purposes).Scan(&n)
	return n, err
}

// afterJudging acts on a reviewer's or a tester's turn: with its entry written, the run ends; what
// the entry says is acted on by the stage, beside Crew's own checks.
func (p *Pipeline) afterJudging(ctx context.Context, j *job, t *board.Ticket, s *sub) error {
	stageOf := map[string]string{kindReview: settings.StageReview, kindTest: settings.StageTest, kindE2E: settings.StageE2E,
		kindVerify: settings.StageVerification}
	if st, ok := j.pipe.Stage(t.Stage); ok && st.Own() {
		stageOf[kindStage] = t.Stage
	}
	// A reviewer also reads a head in a later stage, before the merge (7.4, since 0.90).
	inPlace := s.kind == kindReview && t.Stage != settings.StageDone && t.Stage != settings.StageClosed
	if s.reported == nil && (t.Stage == stageOf[s.kind] || inPlace) {
		return p.missing(ctx, j, s)
	}
	if err := p.Runs.Finish(ctx, s.run); err != nil {
		return err
	}
	p.Board.Kick()
	return nil
}

// on are the subtasks of kind that judged commit.
func on(subs []*sub, kind, commit string) []*sub {
	var out []*sub
	for _, s := range subs {
		if s.kind == kind && s.commit == commit {
			out = append(out, s)
		}
	}
	return out
}

// fixOrApproach says s is a fix round, or the stopped build of the ticket's new approach (7.7).
func fixOrApproach(t *board.Ticket, s *sub) bool {
	return s.kind == kindFix || (s.kind == kindBuild && s.state == "stopped" && t != nil && t.ApproachAt != nil && !s.created.Before(*t.ApproachAt))
}

// fixedAfter says a fix round began after the subtask, for the same failure.
func fixedAfter(t *board.Ticket, subs []*sub, s *sub) bool {
	for _, x := range subs {
		if fixOrApproach(t, x) && x.id > s.id {
			return true
		}
	}
	return false
}

// test starts the tester for the branch as it is, then Crew's own run of the ticket's checks and the
// project's tests in a check sandbox, which decides (13.9); a run that fails is a fix round. When a
// prior test subtask already ran on an earlier commit of this ticket (so this commit is a fix round of
// Test), Crew runs PurposeTest before starting another tester run so a broken fix does not spend a
// second tester turn before the commands pass.
func (p *Pipeline) test(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head string) error {
	if head == "" {
		return nil
	}
	done := on(subs, kindTest, head)
	run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeTest)
	if err != nil {
		return err
	}
	test := str(j.doc, "sandbox.test")
	hasPriorTest := false
	for _, s := range subs {
		if s.kind == kindTest {
			hasPriorTest = true
			break
		}
	}
	if len(done) == 0 && hasPriorTest && (run == nil || run.State != board.CheckPassed) {
		switch {
		case run == nil:
			_, err := p.runChecks(ctx, j, t, board.PurposeTest, head, "", "", checkCmds(board.CmdChecks(t), test))
			return err
		case run.State == board.CheckError:
			n, err := p.errorsOn(ctx, t, head, board.PurposeTest)
			if err != nil {
				return err
			}
			if n < 3 {
				_, err := p.runChecks(ctx, j, t, board.PurposeTest, head, "", "", checkCmds(board.CmdChecks(t), test))
				return err
			}
			return p.Board.Wait(ctx, t, "Crew could not run the tests on "+head[:12]+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed && !fixedAfterRun(t, subs, run):
			_, err := p.fix(ctx, j, t, subs, head, "test", extra{name: "What failed in Crew's run of the checks and the tests",
				who: "the commands, in Crew's check sandbox", text: failuresText(run)})
			return err
		}
		return nil
	}
	if len(done) == 0 {
		recs, err := p.records(ctx, t)
		if err != nil {
			return err
		}
		unresolved, err := p.Board.Unresolved(ctx, t)
		if err != nil {
			return err
		}
		ctxNote := "Your workspace is a fresh clone of the ticket's branch at " + head[:12] + ". You push nothing."
		if exitRun, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeExit); err == nil && exitRun != nil && exitRun.State == board.CheckPassed {
			ctxNote += " Crew's exit command checks already passed on this commit; run only fast targeted edge-case checks (<10s) on the changed packages, because Crew's own check sandbox runs the full test suite right after your turn."
		}
		parts := p.ticketPrompt(ctx, promptInput{stage: "test", role: "tester", project: j.proj, ticket: t, records: recs, comments: unresolved,
			context: ctxNote})
		_, err = p.start(ctx, j, t, startSpec{kind: kindTest, role: "tester", title: "Test in a fresh sandbox", parts: parts,
			branch: *t.Branch, readOnly: true, commit: head, comments: unresolved})
		return err
	}
	last := done[len(done)-1]
	switch {
	case run == nil:
		_, err := p.runChecks(ctx, j, t, board.PurposeTest, head, "", "", checkCmds(board.CmdChecks(t), test))
		return err
	case run.State == board.CheckError:
		n, err := p.errorsOn(ctx, t, head, board.PurposeTest)
		if err != nil {
			return err
		}
		if n < 3 {
			_, err := p.runChecks(ctx, j, t, board.PurposeTest, head, "", "", checkCmds(board.CmdChecks(t), test))
			return err
		}
		return p.Board.Wait(ctx, t, "Crew could not run the tests on "+head[:12]+" three times: "+board.PublicDetail(run))
	case run.State == board.CheckFailed && !fixedAfter(t, subs, last):
		_, err := p.fix(ctx, j, t, subs, head, "test", extra{name: "What failed in Crew's run of the checks and the tests",
			who: "the commands, in Crew's check sandbox", text: failuresText(run)})
		return err
	}
	return nil
}

// e2e starts the E2E tester for the branch as it is, in a fresh microVM with Docker, then Crew's own
// run of the stage's end-to-end command in a check sandbox, which decides (7.4, 13.9); a run that
// fails is a fix round.
func (p *Pipeline) e2e(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head, cmd string) error {
	if head == "" {
		return nil
	}
	if strings.TrimSpace(cmd) == "" {
		return p.Board.Wait(ctx, t, "the E2E stage names no command to run the end-to-end tests: set its run in the project's stages")
	}
	done := on(subs, kindE2E, head)
	if len(done) == 0 {
		plan, err := p.planOf(ctx, j, t)
		if err != nil {
			return err
		}
		recs, err := p.records(ctx, t)
		if err != nil {
			return err
		}
		unresolved, err := p.Board.Unresolved(ctx, t)
		if err != nil {
			return err
		}
		parts := p.ticketPrompt(ctx, promptInput{stage: "e2e", role: "e2e", project: j.proj, ticket: t, plan: plan, records: recs, comments: unresolved,
			context: "Your workspace is a fresh clone of the ticket's branch at " + head[:12] + ", in a microVM with Docker. You push nothing. " +
				"Crew runs the end-to-end tests itself afterwards with: " + cmd})
		_, err = p.start(ctx, j, t, startSpec{kind: kindE2E, role: "e2e", title: "Drive the user flows end to end", parts: parts,
			branch: *t.Branch, readOnly: true, commit: head, comments: unresolved})
		return err
	}
	last := done[len(done)-1]
	if last.active() {
		return nil
	}
	run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, head, board.PurposeE2E)
	if err != nil {
		return err
	}
	cmds := []harness.CheckCmd{{Name: "the end-to-end tests: " + cmd, Run: cmd}}
	switch {
	case run == nil:
		_, err := p.runChecks(ctx, j, t, board.PurposeE2E, head, "", "", cmds)
		return err
	case run.State == board.CheckError:
		n, err := p.errorsOn(ctx, t, head, board.PurposeE2E)
		if err != nil {
			return err
		}
		if n < 3 {
			_, err := p.runChecks(ctx, j, t, board.PurposeE2E, head, "", "", cmds)
			return err
		}
		return p.Board.Wait(ctx, t, "Crew could not run the end-to-end tests on "+head[:12]+" three times: "+board.PublicDetail(run))
	case run.State == board.CheckFailed && !fixedAfter(t, subs, last):
		// An application that could not start twice in a row goes to the lead: the cause is usually the
		// project's setup, not the ticket (21, since 0.47).
		if n, err := p.couldNotStart(ctx, t); err != nil || n >= 2 {
			if err != nil {
				return err
			}
			return p.lookAgain(ctx, j, t, subs, fmt.Sprintf("Crew's end-to-end run could not start the application %d times in a row (exit code %d): %s",
				n, e2eCouldNotStart, short(failuresText(run), 600)))
		}
		_, err := p.fix(ctx, j, t, subs, head, "end-to-end tests", extra{name: "What failed in Crew's run of the end-to-end tests",
			who: "the commands, in Crew's check sandbox", text: failuresText(run)})
		return err
	}
	return nil
}

// e2eCouldNotStart is the exit code of the E2E stage's run command that says the application could not
// start (14.1, since 0.47); any other failure is a failing test.
const e2eCouldNotStart = 3

// couldNotStart counts Crew's E2E runs of the ticket, newest first, that in a row could not start the
// application: a run that passed or failed a test ends the row. A run whose sandbox could not tell is
// passed over.
func (p *Pipeline) couldNotStart(ctx context.Context, t *board.Ticket) (int, error) {
	rows, err := p.Pool.Query(ctx, `SELECT state = 'failed' AND coalesce((results->0->>'code')::int, 0) = $2
		FROM check_runs WHERE ticket = $1 AND purpose = 'e2e' AND state IN ('passed', 'failed') ORDER BY started_at DESC, id DESC`,
		t.ID, e2eCouldNotStart)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var not bool
		if err := rows.Scan(&not); err != nil {
			return n, err
		}
		if !not {
			break
		}
		n++
	}
	return n, rows.Err()
}

// verification starts the verifier for the branch as it is, on another family than the builder's
// (12.3), with the goal and the part of the plan the ticket comes from; a verification that says it
// does not do what was asked is a fix round (7.4).
func (p *Pipeline) verification(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head string) error {
	if head == "" {
		return nil
	}
	done := on(subs, kindVerify, head)
	if len(done) == 0 {
		plan, err := p.planOf(ctx, j, t)
		if err != nil {
			return err
		}
		recs, err := p.records(ctx, t)
		if err != nil {
			return err
		}
		unresolved, err := p.Board.Unresolved(ctx, t)
		if err != nil {
			return err
		}
		parts := p.ticketPrompt(ctx, promptInput{stage: "verification", role: "verifier", project: j.proj, ticket: t, plan: plan, records: recs,
			comments: unresolved, context: "Your workspace is a fresh clone of the ticket's branch at " + head[:12] + ", which you read and do not push."})
		_, err = p.start(ctx, j, t, startSpec{kind: kindVerify, role: "verifier", title: "Verify against the goal and the plan", parts: parts,
			branch: *t.Branch, readOnly: true, commit: head, comments: unresolved})
		return err
	}
	last := done[len(done)-1]
	rec, err := p.newest(ctx, t, board.RecordVerify)
	if err != nil || rec == nil || rec.Commit == nil || *rec.Commit != head || rec.Result == nil || *rec.Result == "pass" || fixedAfter(t, subs, last) {
		return err
	}
	_, err = p.fix(ctx, j, t, subs, head, "verification", extra{name: "The verification that says it does not do what was asked", who: "the verifier", text: rec.Body})
	return err
}

// newest is a ticket's newest entry of kind, or nil.
func (p *Pipeline) newest(ctx context.Context, t *board.Ticket, kind string) (*board.Record, error) {
	all, err := p.Board.Records(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind == kind {
			return &all[i], nil
		}
	}
	return nil, nil
}

// fix starts a fix round (7.4): the ticket stays in its stage, and a builder fixes it on its branch,
// in the builder's conversation, with what failed. At limits.max_fix_rounds, and two more after a new
// approach, the lead looks at the ticket again (7.7). It says whether a fix round began.
func (p *Pipeline) fix(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head, why string, found extra) (bool, error) {
	if held, err := p.holdIfFailureLimitReached(ctx, j, t, "the "+why+" failed"); err != nil || held {
		return false, err
	}
	max := int(num(j.doc, "limits.max_fix_rounds", 3)) + 2*t.Approaches
	if t.FixRounds >= max {
		return false, p.lookAgain(ctx, j, t, subs, fmt.Sprintf("it used its %d fix rounds; the last was for the %s", max, why))
	}
	if t.Branch == nil {
		return false, nil
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return false, err
	}
	unresolved, err := p.Board.Unresolved(ctx, t)
	if err != nil {
		return false, err
	}
	parts := p.ticketPrompt(ctx, promptInput{stage: "fix", role: "builder", project: j.proj, ticket: t, records: recs, comments: unresolved,
		extra: []extra{found}, context: fmt.Sprintf("The ticket is in %s. Your workspace is a clone of its branch %s, which you push.", t.Stage, *t.Branch)})
	run, err := p.start(ctx, j, t, startSpec{kind: kindFix, role: "builder", title: "Fix: address the " + why, parts: parts,
		branch: *t.Branch, continues: lastBuild(subs), commit: head, comments: unresolved})
	if err != nil || run == "" {
		return false, err
	}
	if _, err := p.Board.FixRound(ctx, j.proj, t, why); err != nil {
		return true, err
	}
	p.signal(ctx, j, t, run, "rework", map[string]any{"reason": why, "stage": t.Stage, "text": analytics.FixRound(why, found.text)})
	return true, nil
}
