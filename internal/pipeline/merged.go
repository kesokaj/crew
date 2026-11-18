package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
)

// outsideMerges follows the tickets whose pull request someone merged on GitHub (21, 7.12, since
// 0.47): their work that still runs is stopped; once main has the merge, their command checks and the
// project's tests run on it, because work that came in from outside is not taken as checked; checks
// that fail make a fix ticket.
func (p *Pipeline) outsideMerges(ctx context.Context, j *job) error {
	rows, err := p.Pool.Query(ctx, `SELECT r.ticket, coalesce(r.outside_head, ''), r.outside_check FROM pull_requests r
		WHERE r.project = $1 AND r.outside = 'merged' AND NOT r.outside_done`, j.proj.ID)
	if err != nil {
		return err
	}
	type merged struct {
		ticket int64
		head   string
		check  *int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (merged, error) {
		var x merged
		return x, r.Scan(&x.ticket, &x.head, &x.check)
	})
	if err != nil {
		return err
	}
	for _, x := range list {
		if err := p.stopWorkOf(ctx, x.ticket); err != nil {
			return err
		}
		t, err := p.ticketByID(ctx, j, x.ticket)
		if err != nil {
			return err
		}
		if x.check == nil {
			main := p.Board.Git.Main(ctx, j.proj.Name)
			// The merge reaches main through the fetch of the remote's main, which may wait for the
			// operator when it changes files that are code (13.8).
			if main == "" || (x.head != "" && !p.Board.Git.Follows(ctx, j.proj.Name, x.head, main)) || j.held != "" {
				continue
			}
			id, err := p.runChecks(ctx, j, t, board.PurposeOutside, main, "", "", checkCmds(board.CmdChecks(t), str(j.doc, "sandbox.test")))
			if err != nil {
				return err
			}
			if id == 0 {
				continue // it waits for room or for the setup, as the ticket says
			}
			if _, err := p.Pool.Exec(ctx, `UPDATE pull_requests SET outside_check = $2 WHERE ticket = $1`, t.ID, id); err != nil {
				return err
			}
			continue
		}
		run, err := p.checkRun(ctx, *x.check)
		if err != nil {
			return err
		}
		switch run.State {
		case board.CheckPassed:
			_, err = p.Pool.Exec(ctx, `UPDATE pull_requests SET outside_done = true WHERE ticket = $1`, t.ID)
		case board.CheckError:
			// A count that cannot be read starts no check again (BUG-012's rule).
			var errs int
			if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND purpose = 'outside' AND state = 'error' AND `+notSetup, t.ID).Scan(&errs); err != nil {
				return err
			}
			if errs < 3 {
				_, err = p.Pool.Exec(ctx, `UPDATE pull_requests SET outside_check = NULL WHERE ticket = $1`, t.ID)
			} else {
				err = p.Board.Wait(ctx, t, "Crew could not run its checks on main after the merge on GitHub three times: "+board.PublicDetail(run))
			}
		case board.CheckFailed:
			err = p.outsideFix(ctx, j, t, run)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// outsideFix makes the fix ticket for checks that fail on main after a merge on GitHub, once.
func (p *Pipeline) outsideFix(ctx context.Context, j *job, t *board.Ticket, run *board.CheckRun) error {
	seen := failuresText(run)
	title := "Fix main after " + t.Key + " was merged on GitHub"
	// A fix ticket made on a pass whose record of it failed is found again, not made twice.
	// A lookup that fails makes no ticket, which would be a second one (BUG-012's rule).
	var have int64
	switch err := p.Pool.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND title = $2 AND created_by = $3 ORDER BY id LIMIT 1`,
		j.proj.ID, title, board.Backend+" · a merge on GitHub").Scan(&have); {
	case err == nil:
		_, err := p.Pool.Exec(ctx, `UPDATE pull_requests SET outside_ticket = $2, outside_done = true WHERE ticket = $1`, t.ID, have)
		return err
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	fix, err := p.Board.AddTicket(ctx, j.proj.ID, board.NewTicket{Title: title, Kind: "fix",
		Why:  t.Key + "'s pull request was merged on GitHub by someone else, not by Crew. Crew ran its checks and the project's tests on main after it, and they fail.",
		What: "Make " + t.Key + "'s checks and the project's tests pass on main again.", Goal: t.Key + "'s goal holds on main: " + t.Goal, Seen: &seen},
		board.OriginAgent, board.Backend+" · a merge on GitHub")
	if err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE pull_requests SET outside_ticket = $2, outside_done = true WHERE ticket = $1`, t.ID, fix.ID)
	if err == nil {
		p.log().Info("the checks of a ticket merged on GitHub fail on main: a fix ticket", "project", j.proj.Name, "ticket", t.Key, "fix", fix.Key)
	}
	return err
}

// stopWorkOf stops the runs that still work on a ticket that is Done: all but its summary's.
func (p *Pipeline) stopWorkOf(ctx context.Context, ticket int64) error {
	rows, err := p.Pool.Query(ctx, `SELECT r.id FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.ticket = $1 AND s.kind <> $2 AND r.state IN ('starting', 'working', 'idle')`, ticket, kindSummary)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := p.Runs.Stop(ctx, id); err != nil {
			p.log().Warn("stopping a run of a ticket merged on GitHub", "run", id, "error", err)
		}
	}
	return nil
}

// ticketByID reads a ticket of the job's project.
func (p *Pipeline) ticketByID(ctx context.Context, j *job, id int64) (*board.Ticket, error) {
	var n int
	if err := p.Pool.QueryRow(ctx, `SELECT number FROM tickets WHERE id = $1 AND project = $2`, id, j.proj.ID).Scan(&n); err != nil {
		return nil, err
	}
	return p.Board.Ticket(ctx, j.proj, fmt.Sprintf("%s-%d", j.proj.Key, n))
}
