package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Trajectories screen (design 8.3, 20.1, traj.png): every run of a project with its agent, its
// model, the conversation it continues, its events, turns, tokens, and outcome.
func (s *Server) trajectoryRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/runs", s.projectRuns)
}

// TrajectoryRow is one run of a project.
type TrajectoryRow struct {
	ID           string     `json:"id"`
	Subtask      string     `json:"subtask"` // SF-8.2, or the subtask's id for a run of no ticket
	Ticket       *string    `json:"ticket"`
	Title        string     `json:"title"`
	TicketTitle  *string    `json:"ticketTitle"`
	Role         string     `json:"role"`
	CLI          string     `json:"cli"`
	Model        *string    `json:"model"`
	Effort       *string    `json:"effort"`
	Way          string     `json:"way"`
	Conversation string     `json:"conversation"`
	Continues    *string    `json:"continues"`
	Events       int64      `json:"events"`
	Turns        int        `json:"turns"`
	Tokens       int64      `json:"tokens"`
	State        string     `json:"state"`
	StopReason   *string    `json:"stopReason"`
	Outcome      *string    `json:"outcome"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt"`
}

func (s *Server) projectRuns(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	role, search := q.Get("role"), q.Get("q")
	if len(search) > 200 || len(role) > 40 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "role or q: too long")
		return
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT r.id::text,
			CASE WHEN t.id IS NULL THEN s.id::text ELSE $2 || '-' || t.number || '.' || s.number END,
			CASE WHEN t.id IS NULL THEN NULL ELSE $2 || '-' || t.number END, s.title, t.title, s.role, r.cli, r.model, r.effort,
			r.way, r.conversation::text, r.continues::text, r.events, r.turn, r.tokens_in + r.tokens_out,
			r.state, r.stop_reason, s.reported->>'tool', r.started_at, r.ended_at
		FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN tickets t ON t.id = s.ticket
		WHERE s.project = $1 AND ($3 = '' OR s.role = $3)
		  AND ($4 = '' OR s.title ILIKE '%' || $4 || '%' OR t.title ILIKE '%' || $4 || '%'
		       OR ($2 || '-' || t.number) = upper($4) OR r.id::text = $4 OR r.conversation::text = $4)
		ORDER BY r.started_at DESC LIMIT 500`, p.ID, p.Key, role, search)
	if err != nil {
		s.boardError(w, "reading the runs", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrajectoryRow, error) {
		var x TrajectoryRow
		return x, r.Scan(&x.ID, &x.Subtask, &x.Ticket, &x.Title, &x.TicketTitle, &x.Role, &x.CLI, &x.Model, &x.Effort, &x.Way,
			&x.Conversation, &x.Continues, &x.Events, &x.Turns, &x.Tokens, &x.State, &x.StopReason, &x.Outcome,
			&x.StartedAt, &x.EndedAt)
	})
	if err != nil {
		s.boardError(w, "reading the runs", err)
		return
	}
	if list == nil {
		list = []TrajectoryRow{}
	}
	var events, tokens int64
	for _, x := range list {
		events += x.Events
		tokens += x.Tokens
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": list, "events": events, "tokens": tokens})
}
