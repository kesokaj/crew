package pipeline

import (
	"context"
	"strings"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// ownStage drives a stage the project added (7.4, R101): its agent, defined in a file on main and
// told by the stage's prompt, reads the branch as it is and records its verdict; then Crew runs the
// stage's commands in a check sandbox, and a judge answers its criteria. A verdict that does not pass,
// a command that fails, and a criterion not met are each a fix round in place. The agent runs on the
// reviewer's model, on another family than the builder (12.3).
func (p *Pipeline) ownStage(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head string, st settings.Stage) error {
	if head == "" {
		return nil
	}
	done := on(since(t, subs), kindStage, head)
	if len(done) == 0 {
		// The agent's definition is read from main, never from the branch it judges (13.8).
		def, err := p.Board.Git.File(ctx, j.proj.Name, st.Agent)
		if err != nil {
			return err
		}
		if len(def) == 0 {
			return p.Board.Wait(ctx, t, "the agent of the stage "+st.ID+" is defined in "+st.Agent+", which main does not have")
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
		parts := p.ticketPrompt(ctx, promptInput{stage: "stage", role: "stage", project: j.proj, ticket: t, plan: plan, records: recs,
			comments: unresolved, context: "Your workspace is a fresh clone of the ticket's branch at " + head[:12] + ", which you read and do not push."})
		own := []runs.PromptPart{
			{Part: "stage agent", Source: "main:" + st.Agent, Text: strings.TrimSpace(baseline.Body(string(def)))},
			{Part: "stage prompt", Source: "settings: stages." + st.ID + ".prompt", Text: "What this stage asks of you, from the project's settings:\n\n" + st.Prompt},
		}
		parts = append(parts[:2], append(own, parts[2:]...)...)
		title := "Look at the change for the stage " + stageTitle(st)
		_, err = p.start(ctx, j, t, startSpec{kind: kindStage, role: "stage", modelRole: "reviewer", title: title, parts: parts,
			branch: *t.Branch, readOnly: true, commit: head, comments: unresolved})
		return err
	}
	last := done[len(done)-1]
	if last.active() || fixedAfter(t, subs, last) {
		return nil
	}
	rec, err := p.stageVerdict(ctx, t, st.ID)
	if err != nil || rec == nil || rec.Commit == nil || *rec.Commit != head {
		return err
	}
	if rec.Result == nil || *rec.Result != "pass" {
		_, err := p.fix(ctx, j, t, subs, head, "stage "+st.ID, extra{name: "The verdict of the stage " + stageTitle(st), who: "the agent of the stage", text: rec.Body})
		return err
	}
	if cmds := board.OwnStageCmds(st.Exit); len(cmds) > 0 {
		run, err := board.LatestStageRun(ctx, p.Pool, t.ID, head, st.ID, board.PurposeStage)
		if err != nil {
			return err
		}
		switch {
		case run == nil:
			_, err := p.runChecksFor(ctx, j, t, board.PurposeStage, st.ID, head, "", "", checkCmds(cmds, ""))
			return err
		case run.State == board.CheckRunning:
			return nil
		case run.State == board.CheckError:
			n, err := p.stageErrorsOn(ctx, t, head, st.ID, board.PurposeStage)
			if err != nil {
				return err
			}
			if n < 3 {
				_, err := p.runChecksFor(ctx, j, t, board.PurposeStage, st.ID, head, "", "", checkCmds(cmds, ""))
				return err
			}
			return p.Board.Wait(ctx, t, "Crew could not run the commands of the stage "+st.ID+" on "+head[:12]+" three times: "+board.PublicDetail(run))
		case run.State == board.CheckFailed:
			_, err := p.fix(ctx, j, t, subs, head, "stage "+st.ID, extra{name: "What failed in Crew's run of the stage's commands",
				who: "the commands, in Crew's check sandbox", text: failuresText(run)})
			return err
		}
	}
	ok, err := p.judgeOn(ctx, j, t, subs, head, board.OwnStageJudges(st.Exit), board.PurposeStageJudge, st.ID)
	if err != nil || !ok {
		return err
	}
	if run, err := board.LatestStageRun(ctx, p.Pool, t.ID, head, st.ID, board.PurposeStageJudge); err != nil {
		return err
	} else if run != nil && run.State == board.CheckFailed {
		_, err := p.fix(ctx, j, t, subs, head, "stage "+st.ID, extra{name: "What a judge found not met for the stage " + stageTitle(st),
			who: "the judge", text: judgeText(run)})
		return err
	}
	p.reconcile(ctx, j)
	return nil
}

// stageErrorsOn counts the check runs of purpose for the stage on commit that ended as errors, or
// returns the error that kept it from counting.
func (p *Pipeline) stageErrorsOn(ctx context.Context, t *board.Ticket, commit, stage, purpose string) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND commit = $2 AND purpose = $3 AND stage = $4 AND state = 'error' AND `+notSetup+``,
		t.ID, commit, purpose, stage).Scan(&n)
	return n, err
}

// stageVerdict is the newest verdict of a project's own stage's agent about that stage, or nil.
func (p *Pipeline) stageVerdict(ctx context.Context, t *board.Ticket, stage string) (*board.Record, error) {
	all, err := p.Board.Records(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind == board.RecordStage && all[i].Stage != nil && *all[i].Stage == stage {
			return &all[i], nil
		}
	}
	return nil, nil
}

// stageTitle is a stage's name on the board.
func stageTitle(st settings.Stage) string {
	if st.Title != "" {
		return st.Title
	}
	return st.ID
}
