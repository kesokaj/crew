package httpapi

import (
	"net/http"

	"github.com/kesokaj/crew/internal/cli"
)

// The Agent CLIs screen's routes (design 12.6, 20.1): each CLI's login, when it was last checked
// and what the check said, and Check now.
func (s *Server) cliRoutes(api *routes) {
	api.HandleFunc("GET /api/clis", s.clis)
	api.HandleFunc("POST /api/clis/{cli}/check", s.checkCLI)
}

func (s *Server) clis(w http.ResponseWriter, r *http.Request) {
	l := s.Logins.Load()
	if l == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the login check is not available: the database, the key file, or Docker is missing")
		return
	}
	states, err := l.States(r.Context())
	if err != nil {
		s.Log.Error("reading the logins", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the logins cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clis": states})
}

// checkCLI starts a check with the real request and answers at once: the screen shows "checking"
// until it ends.
func (s *Server) checkCLI(w http.ResponseWriter, r *http.Request) {
	l := s.Logins.Load()
	if l == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the login check is not available")
		return
	}
	name := r.PathValue("cli")
	if _, ok := cli.Get(name); !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such CLI")
		return
	}
	// One that runs already answers for this click too.
	l.CheckNow(r.Context(), name, true)
	w.WriteHeader(http.StatusAccepted)
}
