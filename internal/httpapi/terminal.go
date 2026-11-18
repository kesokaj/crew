package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/projterm"
)

// The project terminal (design 13.11): open it, switch its mode, close it, and attach to it or to a
// running run's microVM.
func (s *Server) terminalRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/terminal", s.getTerminal)
	api.HandleFunc("POST /api/projects/{p}/terminal", s.openTerminal)
	api.HandleFunc("PUT /api/projects/{p}/terminal/mode", s.terminalMode)
	api.HandleFunc("DELETE /api/projects/{p}/terminal", s.closeTerminal)
	api.HandleFunc("GET /api/projects/{p}/terminal/ws", s.attachTerminal)
	api.HandleFunc("GET /api/projects/{p}/terminal/mains", s.heldMains)
	api.HandleFunc("POST /api/projects/{p}/terminal/mains/{id}/take", s.decideMain(true))
	api.HandleFunc("POST /api/projects/{p}/terminal/mains/{id}/refuse", s.decideMain(false))
}

// heldMains are the mains the terminal pushed that wait for the operator's confirmation (13.11).
func (s *Server) heldMains(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	held, err := projterm.Held(r.Context(), b.Pool, p.ID)
	if err != nil {
		s.boardError(w, "reading the held mains", err)
		return
	}
	if held == nil {
		held = []projterm.HeldMain{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mains": held})
}

// decideMain takes a held main as the new base, or refuses it.
func (s *Server) decideMain(take bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ts := s.terminals(w)
		if ts == nil {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no such main")
			return
		}
		switch err := ts.DecideMain(r.Context(), r.PathValue("p"), id, take, r.Header.Get(HeaderEmail)); {
		case errors.Is(err, projterm.ErrNotHeld):
			writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		case err != nil:
			s.terminalError(w, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func (s *Server) terminals(w http.ResponseWriter) *projterm.Service {
	t := s.Terminals.Load()
	if t == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "terminals are not available: the database or Docker is missing")
	}
	return t
}

func (s *Server) terminalError(w http.ResponseWriter, err error) {
	var e *projterm.Error
	switch {
	case errors.Is(err, projterm.ErrNotOpen):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.As(err, &e):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", e.Msg)
	default:
		s.Log.Error("the project terminal", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the terminal failed: "+err.Error())
	}
}

func (s *Server) getTerminal(w http.ResponseWriter, r *http.Request) {
	ts := s.terminals(w)
	if ts == nil {
		return
	}
	t, err := ts.Get(r.Context(), r.PathValue("p"))
	if err != nil {
		s.terminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) openTerminal(w http.ResponseWriter, r *http.Request) {
	ts := s.terminals(w)
	if ts == nil {
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := ts.Open(r.Context(), r.PathValue("p"), req.Ticket, board.Operator)
	if err != nil {
		s.terminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) terminalMode(w http.ResponseWriter, r *http.Request) {
	ts := s.terminals(w)
	if ts == nil {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := ts.SetMode(r.Context(), r.PathValue("p"), req.Mode, board.Operator)
	if err != nil {
		s.terminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) closeTerminal(w http.ResponseWriter, r *http.Request) {
	ts := s.terminals(w)
	if ts == nil {
		return
	}
	if err := ts.Close(r.Context(), r.PathValue("p"), board.Operator, "closed by you"); err != nil {
		s.terminalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attachTerminal is a GET that upgrades to a WebSocket: the ingress has vouched for the operator, and
// the connection is taken only from a page of Crew's own address.
func (s *Server) attachTerminal(w http.ResponseWriter, r *http.Request) {
	// A terminal connection must come from a page of Crew's own address (20.3), as every change
	// must: the WebSocket library alone lets a request with no Origin through.
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "a terminal must be opened from a page of Crew's own address")
		return
	}
	ts := s.terminals(w)
	if ts == nil {
		return
	}
	ts.Attach(w, r, s.Hostname, r.PathValue("p"), r.URL.Query().Get("run"), board.Operator)
}
