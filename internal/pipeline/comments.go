package pipeline

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// LiveSteerer is implemented by *runs.Driver to deliver operator comments mid-turn into an active
// `agy acp` session (`session/prompt` via `crew-run acp-steer`, design 1.37, 11.6).
type LiveSteerer interface {
	Steer(ctx context.Context, runID, text string, record func(ctx context.Context, tx pgx.Tx, run string) error) error
}

// steerActiveComments delivers waiting operator comments (`Tell the agent`) mid-turn into an active
// working ACP turn if the run's harness supports live ACP steering; otherwise it leaves them waiting
// for `afterTurn` to deliver at the end of the turn.
func (p *Pipeline) steerActiveComments(ctx context.Context, j *job, t *board.Ticket, subs []*sub) {
	ls, ok := p.Runs.(LiveSteerer)
	if !ok || t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
		return
	}
	for _, s := range subs {
		if s.runState != "working" || s.run == "" || s.kind == kindSummary || s.kind == kindJudge {
			continue
		}
		waiting, err := p.Board.Waiting(ctx, j.proj.ID, t.ID)
		if err != nil || len(waiting) == 0 {
			return
		}
		text := commentsText(waiting) + "\nThen go on with your work where you left it."
		_ = ls.Steer(ctx, s.run, text, deliver(waiting))
		return
	}
}

// planComments gives the operator's comments on the plan of a running project to the planner or
// curator (11.6, step 4): only a comment on the plan starts that subtask's next turn, which continues
// its last conversation of the project in a new sandbox.
func (p *Pipeline) planComments(ctx context.Context, j *job) error {
	waiting, err := p.Board.Waiting(ctx, j.proj.ID, 0)
	if err != nil || len(waiting) == 0 {
		return err
	}
	var id int64
	var role, cliName, state string
	err = p.Pool.QueryRow(ctx, `SELECT s.id, s.role, coalesce(r.cli, ''), coalesce(r.state, '') FROM subtasks s
		LEFT JOIN LATERAL (SELECT cli, state FROM runs WHERE subtask = s.id ORDER BY number DESC LIMIT 1) r ON true
		WHERE s.project = $1 AND s.ticket IS NULL AND s.role IN ('planner', 'curator', 'lead') ORDER BY s.id DESC LIMIT 1`, j.proj.ID).Scan(&id, &role, &cliName, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || state == "starting" || state == "working" {
		return err
	}
	s := &sub{id: id, role: role, cli: cliName}
	text := commentsText(waiting) + "\nThe project runs. You change no ticket and no stage now: answer each comment with " +
		"reply_comment, say what should change and why, and record a choice the plan does not settle with decide. The operator decides."
	if err := p.continueRun(ctx, j, s, text, deliver(waiting)); err != nil && !errors.Is(err, runs.ErrNotSignedIn) {
		return err
	}
	return nil
}
