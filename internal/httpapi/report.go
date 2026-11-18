package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Report is a project's final report (7.2): the lead's sections, the facts Crew counted, and why
// sections are missing when the lead could not write them.
type Report struct {
	Number   int               `json:"number"`
	Sections map[string]string `json:"sections"`
	Facts    json.RawMessage   `json:"facts"`
	Missing  *string           `json:"missing,omitempty"`
	At       time.Time         `json:"at"`
}

func (s *Server) reportRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/report", s.getReport)
}

// getReport gives a project's final reports, the newest first.
func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	p, err := b.Project(r.Context(), r.PathValue("p"))
	if err != nil {
		s.boardError(w, "reading the project", err)
		return
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT number, sections, facts, missing, at FROM reports WHERE project = $1 ORDER BY number DESC`, p.ID)
	if err != nil {
		s.boardError(w, "reading the reports", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Report, error) {
		var rep Report
		return rep, row.Scan(&rep.Number, &rep.Sections, &rep.Facts, &rep.Missing, &rep.At)
	})
	if err != nil {
		s.boardError(w, "reading the reports", err)
		return
	}
	if list == nil {
		list = []Report{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": list})
}
