package httpapi

import (
	"net/http"

	"github.com/kesokaj/crew/internal/pins"
)

// The daily look (12.8, since 0.56): its newest look, and Look now, which runs one at once. The
// running server alone looks; a check server has none and makes no ticket.
func (s *Server) pinRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/pins", s.getPins)
	api.HandleFunc("POST /api/crew/pins/look", s.lookNow)
}

func (s *Server) getPins(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	last, err := pins.Last(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the daily look", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"look": last, "looks": s.Pins.Load() != nil})
}

func (s *Server) lookNow(w http.ResponseWriter, r *http.Request) {
	l := s.Pins.Load()
	if l == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the daily look does not run on this server")
		return
	}
	res, err := l.Once(r.Context())
	if err != nil {
		s.boardError(w, "the daily look", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"look": res, "looks": true})
}
