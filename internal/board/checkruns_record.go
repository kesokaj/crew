package board

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/gitstore"
)

// StartCheckRunFor records a check run of a project's own stage, stage, and the criteria a judge is
// asked; "" and nil for neither.
func (b *Board) StartCheckRunFor(ctx context.Context, p *Project, t *Ticket, purpose, commit, main, head, stage string, criteria []string) (int64, error) {
	if !gitstore.ValidCommit(commit) {
		return 0, fmt.Errorf("board: %q is not a commit", commit)
	}
	var id int64
	err := b.Pool.QueryRow(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, main, head, stage, criteria)
		VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''), $8) RETURNING id`,
		p.ID, t.ID, purpose, commit, main, head, stage, criteria).Scan(&id)
	return id, err
}

// FinishCheckRun records how a check run ended, and asks the board to look again: the checks of a
// move read it. setup marks a run whose sandbox's project setup failed, which the pipeline counts by
// (13.9).
func (b *Board) FinishCheckRun(ctx context.Context, id int64, state string, results []CmdResult, detail string, flaky, setup bool) error {
	if results == nil {
		results = []CmdResult{}
	}
	ended := false
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE check_runs SET state = $2, results = $3, detail = nullif($4, ''), flaky = $5, setup_failed = $6, ended_at = now()
			WHERE id = $1 AND state = 'running'`, id, state, results, detail, flaky, setup)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ended = true
		// Its ticket's pull request shows how it ended (7.12).
		return pullStatus(ctx, tx, id)
	})
	if err == nil {
		// An agent reads Crew's own error as CouldNotCheck: the error itself is here, and on the
		// ticket's page; so a setup the runtime could not run, which the pipeline counts as a
		// failure. A setup's own end is the project's, and not logged.
		if ended && detail != "" && PublicDetail(&CheckRun{State: state, Detail: &detail}) != detail {
			b.log().Error("a check run ended as Crew's own error", "checkRun", id, "detail", detail)
		}
		b.Kick()
	}
	return err
}

// RecordCheckRun records a completed check run that needed no sandbox (such as a merge conflict or a
// fast-forward merge whose Test check already passed) in one transaction with its final passed/failed
// state, so capacity.Used never observes a transient state='running' check sandbox (BUG-038).
func (b *Board) RecordCheckRun(ctx context.Context, p *Project, t *Ticket, purpose, commit, main, head, state string, results []CmdResult, detail string) error {
	if !gitstore.ValidCommit(commit) {
		return fmt.Errorf("board: %q is not a commit", commit)
	}
	if state != CheckPassed && state != CheckFailed {
		return fmt.Errorf("board: %q is not a non-sandbox check outcome", state)
	}
	if results == nil {
		results = []CmdResult{}
	}
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, main, head, state, results, detail, ended_at)
			VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, $8, nullif($9, ''), now()) RETURNING id`,
			p.ID, t.ID, purpose, commit, main, head, state, results, detail).Scan(&id); err != nil {
			return err
		}
		return pullStatus(ctx, tx, id)
	})
	if err == nil {
		b.Kick()
	}
	return err
}
