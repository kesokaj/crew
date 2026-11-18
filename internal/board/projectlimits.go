package board

import (
	"context"
	"fmt"

	"github.com/kesokaj/crew/internal/db"
)

// OverLimit says whether a project's limit is passed, in words for the inbox and the history, or ""
// when it is not (7.7): the share of the tickets of the approved plan that agents may add (R32, since
// 0.53 a count, and no limit in dollars). Work added by agents is the tickets of origin agent that
// Analysis approved; a replacement of a cancelled ticket (it carries replaces) is the same work and
// does not count, and neither does a discarded one, nor one the backend made itself (created_by
// backend: a fix of the setup, of a fetch, of flaky tests, a merge on GitHub). A ticket the operator
// made, also through a token, a conversation, or a chat, is the operator's. The plan's tickets are
// those of origin plan.
func (b *Board) OverLimit(ctx context.Context, q db.Querier, p *Project) (string, error) {
	doc, err := b.Settings(ctx, q, p)
	if err != nil {
		return "", err
	}
	l := LimitsOf(doc)
	var plan, added, openAdded int
	err = q.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.origin = 'plan'),
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.origin = 'agent'
				AND split_part(t.created_by, ' · ', 1) <> 'backend'
				AND NOT EXISTS (SELECT 1 FROM ticket_links l WHERE l.from_ticket = t.id AND l.kind = 'replaces')
				AND (t.closed_reason IS DISTINCT FROM 'discarded')
				AND (t.analysis->>'status' IN ('ready', 'new_feature_notice')
					OR t.stage NOT IN ('planning', 'backlog', 'analysis', 'closed')
					OR t.closed_reason = 'cancelled')),
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.origin = 'agent'
				AND split_part(t.created_by, ' · ', 1) <> 'backend'
				AND NOT EXISTS (SELECT 1 FROM ticket_links l WHERE l.from_ticket = t.id AND l.kind = 'replaces')
				AND t.stage NOT IN ('done', 'closed')
				AND (t.analysis->>'status' IN ('ready', 'new_feature_notice')
					OR t.stage NOT IN ('planning', 'backlog', 'analysis', 'closed')))`,
		p.ID).Scan(&plan, &added, &openAdded)
	if err != nil {
		return "", err
	}
	if l.AddedShareExceeded(added, plan) {
		word := "tickets"
		if added == 1 {
			word = "ticket"
		}
		return fmt.Sprintf("Agents added %d %s, over %.0f%% of the plan's %d", added, word, l.MaxAddedShare*100, plan), nil
	}
	if l.AddedTicketsExceeded(openAdded, plan) {
		word := "tickets"
		if openAdded == 1 {
			word = "ticket"
		}
		return fmt.Sprintf("Agents have %d open added %s, over the limit of %d", openAdded, word, l.MaxAddedTickets), nil
	}
	return "", nil
}

// CrewProject is the name of the project the machine's improvements.crew_project names, "" when none
// (16.2).
func CrewProject(ctx context.Context, q db.Querier) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT coalesce((SELECT doc->'improvements'->>'crew_project' FROM settings WHERE project IS NULL), '')`).Scan(&name)
	return name, err
}

// PausedByCrew says the project's newest pause was the backend's at a limit, not the operator's.
func PausedByCrew(ctx context.Context, q db.Querier, project string) (bool, error) {
	var who string
	err := q.QueryRow(ctx, `SELECT coalesce((SELECT who FROM changes WHERE tbl = 'projects' AND row_key = $1
		AND after->>'state' = 'paused' ORDER BY seq DESC LIMIT 1), '')`, project).Scan(&who)
	return who != "" && who != Operator, err
}

// HoldLimits pauses a running project that passed a limit, and resumes one the backend paused once
// no limit is passed: raising the limit is what resumes it (7.7). A project the operator paused is
// left as it is. It says what it did.
func (b *Board) HoldLimits(ctx context.Context, p *Project) (string, error) {
	if p.State != "running" && p.State != "paused" {
		return "", nil
	}
	over, err := b.OverLimit(ctx, b.Pool, p)
	if err != nil {
		return "", err
	}
	switch {
	case p.State == "running" && over != "":
		_, err = b.SetState(ctx, p.ID, "pause", over, Backend)
		return "paused", err
	case p.State == "paused" && over == "":
		crew, err := PausedByCrew(ctx, b.Pool, p.ID)
		if err != nil || !crew {
			return "", err
		}
		_, err = b.SetState(ctx, p.ID, "resume", "", Backend)
		return "resumed", err
	}
	return "", nil
}
