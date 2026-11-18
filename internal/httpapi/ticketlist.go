package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// TicketRow is a line of a project's Tickets screen (20.1, tickets.png): every ticket with what it is
// doing now, where it came from, its subtasks, fix rounds, and its runs' tokens (since 0.53 no cost).
type TicketRow struct {
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Goal         string    `json:"goal"`
	Kind         string    `json:"kind"`
	Stage        string    `json:"stage"`
	ClosedReason *string   `json:"closedReason,omitempty"`
	Analysis     string    `json:"analysis"`
	Waiting      *string   `json:"waiting,omitempty"`
	Stuck        *string   `json:"stuck,omitempty"`
	Hold         bool      `json:"hold"`
	Origin       string    `json:"origin"`
	CreatedBy    string    `json:"createdBy"`
	From         *string   `json:"from,omitempty"`
	Subtasks     int       `json:"subtasks"`
	FixRounds    int       `json:"fixRounds"`
	Tokens       int64     `json:"tokens"`
	Role         *string   `json:"role,omitempty"`
	CLI          *string   `json:"cli,omitempty"`
	Model        *string   `json:"model,omitempty"`
	Effort       *string   `json:"effort,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (s *Server) ticketListRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/tickets", s.listTickets)
}

// listTickets gives every ticket of the project, newest first.
func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT p.key || '-' || t.number, t.title, t.goal, t.kind, t.stage, t.closed_reason,
			coalesce(t.analysis->>'status', ''), t.waiting, t.stuck, t.hold, t.origin, t.created_by,
			(SELECT p.key || '-' || o.number FROM ticket_links l JOIN tickets o ON o.id = l.to_ticket
				WHERE l.from_ticket = t.id AND l.kind IN ('created_from', 'replaces', 'reverts') ORDER BY l.kind LIMIT 1),
			(SELECT count(*) FROM subtasks s WHERE s.ticket = t.id AND s.kind <> 'helper')::int, t.fix_rounds,
			coalesce((SELECT sum(r.tokens_in + r.tokens_out) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id), 0)::bigint,
			a.role, a.cli, a.model, a.effort, t.created_at
		FROM tickets t JOIN projects p ON p.id = t.project
		LEFT JOIN LATERAL (SELECT s.role, r.cli, r.model, r.effort FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE s.ticket = t.id AND r.state IN ('starting', 'working', 'idle') ORDER BY r.started_at DESC LIMIT 1) a ON true
		WHERE t.project = $1 ORDER BY t.number DESC`, p.ID)
	if err != nil {
		s.boardError(w, "reading the tickets", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TicketRow, error) {
		var t TicketRow
		return t, row.Scan(&t.Key, &t.Title, &t.Goal, &t.Kind, &t.Stage, &t.ClosedReason, &t.Analysis, &t.Waiting, &t.Stuck, &t.Hold,
			&t.Origin, &t.CreatedBy, &t.From, &t.Subtasks, &t.FixRounds, &t.Tokens,
			&t.Role, &t.CLI, &t.Model, &t.Effort, &t.CreatedAt)
	})
	if err != nil {
		s.boardError(w, "reading the tickets", err)
		return
	}
	if list == nil {
		list = []TicketRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": list})
}
