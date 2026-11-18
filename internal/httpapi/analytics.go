package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kesokaj/crew/internal/analytics"
	"github.com/kesokaj/crew/internal/version"
)

// The analytics of a project (9.7) and of the Crew level (10.7), for the operator. Agents reach a
// project's through get_metrics; no agent reaches the Crew level's.
func (s *Server) analyticsRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/analytics", s.projectAnalytics)
	api.HandleFunc("GET /api/crew/analytics", s.crewAnalytics)
	api.HandleFunc("GET /api/crew/signals", s.crewSignals)
}

func (s *Server) projectAnalytics(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	now := time.Now()
	m, err := analytics.Cached(r.Context(), pool, version.Version, analytics.ProjectCacheKey(p.ID), p.ID, now, func() (*analytics.Project, error) {
		return analytics.ForProject(r.Context(), pool, p.ID, now)
	})
	if err != nil {
		s.boardError(w, "computing the analytics", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) crewAnalytics(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	now := time.Now()
	m, err := analytics.Cached(r.Context(), pool, version.Version, analytics.CrewKey, "", now, func() (*analytics.Crew, error) {
		return analytics.ForCrew(r.Context(), pool, version.Version, now)
	})
	if err != nil {
		s.boardError(w, "computing the Crew level", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// crewSignals lists signals, filtered by kind, role, project, and time (10.4, list_signals).
func (s *Server) crewSignals(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	f := analytics.Filter{Kind: r.URL.Query().Get("kind"), Role: r.URL.Query().Get("role"), Project: r.URL.Query().Get("project")}
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "since: a time in RFC 3339")
			return
		}
		f.Since = t
	}
	if v := r.URL.Query().Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pageSize: a number from 1")
			return
		}
		f.Limit = n
	}
	list, err := analytics.Signals(r.Context(), pool, f)
	if err != nil {
		s.boardError(w, "reading the signals", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signals": list, "kinds": analytics.Kinds})
}
