package analytics

import (
	"context"
	"time"

	"github.com/kesokaj/crew/internal/db"
)

// Filter chooses signals by kind, role, project (its name or key), and time.
type Filter struct {
	Kind, Role, Project string
	Since               time.Time
	Limit               int
}

// Signal is one signal with what it ran on.
type Signal struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Project string         `json:"project"`
	Ticket  *string        `json:"ticket"`
	Run     *string        `json:"run"`
	Kind    string         `json:"kind"`
	Label   string         `json:"label"`
	Role    *string        `json:"role"`
	CLI     *string        `json:"cli"`
	Model   *string        `json:"model"`
	Effort  *string        `json:"effort"`
	Version *string        `json:"version"`
	Detail  map[string]any `json:"detail"`
}

// Signals lists the signals the filter chooses, newest first, at most 500. A hidden project's, the
// gate's (16.2), are not listed: the improver reads them here.
func Signals(ctx context.Context, q db.Querier, f Filter) ([]Signal, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `SELECT g.id, g.at, coalesce(p.name, ''), CASE WHEN t.id IS NULL THEN NULL ELSE p.key || '-' || t.number END,
			g.run::text, `+displayKind+`, g.role, g.cli, g.model, g.effort, g.crew_version, g.detail
		FROM signals g LEFT JOIN projects p ON p.id = g.project LEFT JOIN tickets t ON t.id = g.ticket
		WHERE ($1 = '' OR `+displayKind+` = $1 OR g.kind = $1) AND ($2 = '' OR g.role = $2)
		  AND ($3 = '' OR p.name = $3 OR p.key = $3) AND g.at >= $4 AND p.hidden IS NOT TRUE
		ORDER BY g.id DESC LIMIT $5`, f.Kind, f.Role, f.Project, f.Since, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signal{}
	for rows.Next() {
		var s Signal
		if err := rows.Scan(&s.ID, &s.At, &s.Project, &s.Ticket, &s.Run, &s.Kind, &s.Role, &s.CLI, &s.Model, &s.Effort, &s.Version, &s.Detail); err != nil {
			return nil, err
		}
		s.Label = KindOf(s.Kind).Label
		out = append(out, s)
	}
	return out, rows.Err()
}
