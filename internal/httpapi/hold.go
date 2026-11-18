package httpapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/hold"
)

// The two switches for the whole machine (7.9, 15.4): Pause all, Stop all, and Resume; and the
// machine's own items (21). Each is the operator's, with its row in changes; no agent and no MCP tool
// can reach them.
func (s *Server) holdRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/hold", s.getHold)
	api.HandleFunc("POST /api/crew/pause", s.pauseAll)
	api.HandleFunc("POST /api/crew/stop", s.stopAll)
	api.HandleFunc("POST /api/crew/resume", s.resumeAll)
	// The machine's two items of the inbox (21): microVMs that do not boot, and the database written
	// again.
	api.HandleFunc("POST /api/machine/boot/probe", s.probeBoot)
	api.HandleFunc("POST /api/machine/boot/resume", s.resumeBoot)
	api.HandleFunc("POST /api/machine/db-writable/read", s.readWritable)
}

// probeBoot is Run the check: a probe microVM now, in the background, unless one runs (21, D11).
func (s *Server) probeBoot(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	if d.Probe == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no microVM can start here")
		return
	}
	started := d.ProbeNow(context.WithoutCancel(r.Context()))
	s.Log.Info("the operator ran the check of the machine's microVMs", "started", started)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started})
}

// resumeBoot is Resume of the boot hold: runs may start again, and two more failures in a row hold
// again (D22). It lifts a hold, so it asks for a fresh sign-in (freshRoutes, 20.3).
func (s *Server) resumeBoot(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error { return hold.ResumeBoot(r.Context(), tx, board.Operator) }); err != nil {
		s.boardError(w, "resuming after microVMs that did not boot", err)
		return
	}
	s.Log.Info("the operator resumed Crew after microVMs that did not boot")
	s.getHold(w, r)
}

// readWritable is Read it of the database written again.
func (s *Server) readWritable(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error { return hold.ReadWritable(r.Context(), tx, board.Operator) }); err != nil {
		s.boardError(w, "reading the time the database could not be written", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) getHold(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	st, err := hold.Read(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading whether Crew is paused", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": st, "held": st.Held()})
}

func (s *Server) pauseAll(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	st, err := d.PauseAll(r.Context(), board.Operator)
	if err != nil {
		s.boardError(w, "pausing Crew", err)
		return
	}
	s.Log.Info("the operator paused all of Crew")
	writeJSON(w, http.StatusOK, map[string]any{"state": st, "held": st.Held()})
}

func (s *Server) stopAll(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	st, n, err := d.StopAll(r.Context(), board.Operator)
	if err != nil {
		s.boardError(w, "stopping Crew", err)
		return
	}
	s.Log.Info("the operator stopped all of Crew", "runs", n)
	writeJSON(w, http.StatusOK, map[string]any{"state": st, "held": st.Held(), "stopped": n})
}

func (s *Server) resumeAll(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	if err := d.ResumeAll(r.Context(), board.Operator); err != nil {
		s.boardError(w, "resuming Crew", err)
		return
	}
	s.Log.Info("the operator resumed Crew")
	s.getHold(w, r)
}
