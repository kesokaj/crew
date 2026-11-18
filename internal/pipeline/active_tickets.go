package pipeline

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// ActiveTicketsSummary describes machine-wide active-ticket concurrency (7.9, R52, R53).
type ActiveTicketsSummary struct {
	Active int `json:"active"`
	Max    int `json:"max"`
	Set    int `json:"set"`
}

// ActiveTicketsOf returns the machine-wide active-ticket count, effective max (halved when any model
// group of agy is at or above quota.slow_down_at, floor 1), and configured max_active_tickets.
func ActiveTicketsOf(ctx context.Context, pool *pgxpool.Pool, machine settings.Doc) (ActiveTicketsSummary, error) {
	setMax := int(num(machine, "max_active_tickets", 12))
	if setMax < 1 {
		setMax = 12
	}
	effMax := setMax
	slow := num(machine, "quota.slow_down_at", 0)
	if guard, err := models.LoadGuard(ctx, pool, slow); err == nil {
		if guard.CLIClose(cli.AgyName) && effMax > 1 {
			effMax = max(1, effMax/2)
		}
	}
	var active int
	err := pool.QueryRow(ctx, `SELECT count(DISTINCT t.id)
		FROM tickets t
		JOIN projects p ON p.id = t.project
		WHERE p.state <> 'archived' AND t.stage NOT IN ('done', 'closed')
		  AND (
		      EXISTS (
		          SELECT 1 FROM subtasks s JOIN runs r ON r.subtask = s.id
		          WHERE s.ticket = t.id AND s.kind NOT IN ('helper', 'chat', 'summary')
		            AND r.state IN ('starting', 'working')
		      )
		      OR EXISTS (
		          SELECT 1 FROM check_runs c WHERE c.ticket = t.id AND c.state = 'running'
		      )
		  )`).Scan(&active)
	return ActiveTicketsSummary{Active: active, Max: effMax, Set: setMax}, err
}

// ProjectActiveTickets returns the active-ticket count and configured limits.max_active_tickets for one project.
func ProjectActiveTickets(ctx context.Context, pool *pgxpool.Pool, projectID string, projectDoc settings.Doc) (active, maxTickets int, err error) {
	maxTickets = int(num(projectDoc, "limits.max_active_tickets", 4))
	if maxTickets < 1 {
		maxTickets = 4
	}
	err = pool.QueryRow(ctx, `SELECT count(DISTINCT t.id)
		FROM tickets t
		WHERE t.project = $1::uuid AND t.stage NOT IN ('done', 'closed')
		  AND (
		      EXISTS (
		          SELECT 1 FROM subtasks s JOIN runs r ON r.subtask = s.id
		          WHERE s.ticket = t.id AND s.kind NOT IN ('helper', 'chat', 'summary')
		            AND r.state IN ('starting', 'working')
		      )
		      OR EXISTS (
		          SELECT 1 FROM check_runs c WHERE c.ticket = t.id AND c.state = 'running'
		      )
		  )`, projectID).Scan(&active)
	return active, maxTickets, err
}

// ticketAlreadyActive reports whether ticketID already holds an active run or running check, so parallel
// part subtasks or judge checks on the same ticket can start without consuming an extra ticket slot.
func ticketAlreadyActive(ctx context.Context, pool *pgxpool.Pool, ticketID int64) (bool, error) {
	var already bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM subtasks s JOIN runs r ON r.subtask = s.id
		WHERE s.ticket = $1 AND s.kind NOT IN ('helper', 'chat', 'summary')
		  AND r.state IN ('starting', 'working')
	) OR EXISTS (
		SELECT 1 FROM check_runs c WHERE c.ticket = $1 AND c.state = 'running'
	)`, ticketID).Scan(&already)
	return already, err
}

// sortTicketsForReconcile orders a project's tickets for reconciliation by stage furthest right on the
// board (7.9, R52, R53), then operator_priority, then ticket priority, preserving board position within
// a stage and priority.
func sortTicketsForReconcile(pipe board.Pipeline, tickets []*board.Ticket) {
	slices.SortStableFunc(tickets, func(a, b *board.Ticket) int {
		aDone := a.Stage == settings.StageDone || a.Stage == settings.StageClosed
		bDone := b.Stage == settings.StageDone || b.Stage == settings.StageClosed
		if aDone != bDone {
			if aDone {
				return 1
			}
			return -1
		}
		if !aDone {
			if a.Stage != b.Stage {
				if pipe.Right(a.Stage, b.Stage) {
					return 1
				}
				if pipe.Right(b.Stage, a.Stage) {
					return -1
				}
			}
		}
		aAcross, aOwn := queuePriorities(a)
		bAcross, bOwn := queuePriorities(b)
		if aAcross != bAcross {
			return aAcross - bAcross
		}
		if aOwn != bOwn {
			return aOwn - bOwn
		}
		return 0
	})
}

// checkActiveTicketLimit returns a non-empty waiting reason when starting a new run on ticket t would
// exceed the project's limits.max_active_tickets or the machine's max_active_tickets (7.9, R52).
func (p *Pipeline) checkActiveTicketLimit(ctx context.Context, j *job, t *board.Ticket, kind string) (string, error) {
	if kind == kindSummary || t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
		return "", nil
	}
	already, err := ticketAlreadyActive(ctx, p.Pool, t.ID)
	if err != nil || already {
		return "", err
	}
	projActive, projMax, err := ProjectActiveTickets(ctx, p.Pool, j.proj.ID, j.doc)
	if err != nil {
		return "", err
	}
	if projActive >= projMax {
		return fmt.Sprintf("waits for an active-ticket slot in %s (%d of %d active)", j.proj.Name, projActive, projMax), nil
	}
	mach, err := ActiveTicketsOf(ctx, p.Pool, j.machine)
	if err != nil {
		return "", err
	}
	if mach.Active >= mach.Max {
		return fmt.Sprintf("waits for a machine active-ticket slot (%d of %d active)", mach.Active, mach.Max), nil
	}
	return "", nil
}
