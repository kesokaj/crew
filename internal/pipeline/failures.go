package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/kesokaj/crew/internal/board"
)

// ticketFailuresSinceRelease counts how many times t has failed since the operator last lifted its
// hold: fix rounds started after that point plus non-helper, non-look-again subtasks that stopped for
// agent-counted reasons (not crew-run's own end_step) after that point (7.7).
func (p *Pipeline) ticketFailuresSinceRelease(ctx context.Context, project string, t *board.Ticket) (int, error) {
	var lastRelease time.Time
	if err := p.Pool.QueryRow(ctx, `SELECT coalesce(max(at), 'epoch') FROM changes
		WHERE project = $1 AND tbl = 'tickets' AND row_key = $2 AND who = $3
		  AND after ? 'hold' AND coalesce((after->>'hold')::boolean, true) = false`,
		project, t.Key, board.Operator).Scan(&lastRelease); err != nil {
		return 0, err
	}
	var failures int
	if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM subtasks s
		WHERE s.ticket = $1
		  AND (
			(s.kind = 'fix' AND coalesce(s.title, '') NOT LIKE 'New approach:%' AND s.created_at > $2)
			OR (s.kind NOT IN ('helper', 'look_again', 'fix')
				AND (s.state = 'stopped' OR (s.state = 'superseded' AND s.stops >= 2))
				AND s.updated_at > $2
				AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.subtask = s.id AND coalesce(r.end_step, '') <> ''))
		  )`,
		t.ID, lastRelease).Scan(&failures); err != nil {
		return 0, err
	}
	return failures, nil
}

// heldByBackend says t's newest hold change was recorded by Crew's backend, not by the operator.
func (p *Pipeline) heldByBackend(ctx context.Context, project, key string) (bool, error) {
	var who string
	err := p.Pool.QueryRow(ctx, `SELECT coalesce((SELECT who FROM changes
		WHERE project = $1 AND tbl = 'tickets' AND row_key = $2 AND after ? 'hold'
		  AND coalesce((after->>'hold')::boolean, false) = true
		ORDER BY seq DESC LIMIT 1), '')`, project, key).Scan(&who)
	return who != "" && who != board.Operator, err
}

// releaseIfFailureLimitRaised lifts a hold Crew set at limits.max_ticket_failures when the operator
// raised the limit above the ticket's failure count; an operator hold is never touched (7.7).
func (p *Pipeline) releaseIfFailureLimitRaised(ctx context.Context, j *job, t *board.Ticket) error {
	if !t.Hold {
		return nil
	}
	backend, err := p.heldByBackend(ctx, j.proj.ID, t.Key)
	if err != nil || !backend {
		return err
	}
	lim := board.LimitsOf(j.doc)
	failures, err := p.ticketFailuresSinceRelease(ctx, j.proj.ID, t)
	if err != nil || lim.TicketFailuresReached(failures) {
		return err
	}
	fa := false
	updated, err := p.Board.EditTicket(ctx, j.proj, t.Key, board.Edit{Hold: &fa}, board.Backend)
	if err != nil {
		return err
	}
	if _, err := p.Pool.Exec(ctx, `UPDATE tickets SET stuck = NULL WHERE id = $1 AND stuck LIKE 'Held after %'`, t.ID); err != nil {
		return err
	}
	t.Hold = updated.Hold
	t.Stuck = nil
	return nil
}

// holdIfFailureLimitReached puts t on hold with a KindCrewCannot inbox notice and ticket_failure_hold
// signal when its failures since the operator last lifted its hold reach limits.max_ticket_failures,
// stopping runaway fix, look-again, and cancel_and_replace loops (7.7).
func (p *Pipeline) holdIfFailureLimitReached(ctx context.Context, j *job, t *board.Ticket, why string) (bool, error) {
	lim := board.LimitsOf(j.doc)
	if lim.MaxTicketFailures <= 0 {
		return false, nil
	}
	failures, err := p.ticketFailuresSinceRelease(ctx, j.proj.ID, t)
	if err != nil {
		return false, err
	}
	if !lim.TicketFailuresReached(failures) {
		return false, nil
	}
	msg := fmt.Sprintf("Held after %d failures (limits.max_ticket_failures is %d): %s", failures, lim.MaxTicketFailures, why)
	if !t.Hold {
		tr := true
		if _, err := p.Board.EditTicket(ctx, j.proj, t.Key, board.Edit{Hold: &tr}, board.Backend); err != nil {
			return false, err
		}
		t.Hold = true
		p.signal(ctx, j, t, "", "ticket_failure_hold", map[string]any{
			"failures": failures,
			"limit":    lim.MaxTicketFailures,
			"text":     fmt.Sprintf("%s was held after %d failures (%s)", t.Key, failures, why),
		})
	}
	if err := p.Board.Wait(ctx, t, msg); err != nil {
		return false, err
	}
	return true, nil
}
