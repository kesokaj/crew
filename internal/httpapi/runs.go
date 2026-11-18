package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runs"
)

// The routes of the Runs and Run screens (design 8.1, 8.3, 12.4, 12.10, 20.1): the runs with their
// state, one run with its prompt, turns, and live trajectory, and Start, Stop, Finish, and Continue.
// A run started here is on the fixture project, with a subtask of its own and no ticket; the runs of
// a ticket come with M6.
func (s *Server) runRoutes(api *routes) {
	api.HandleFunc("GET /api/runs", s.listRuns)
	api.HandleFunc("POST /api/runs", s.startRun)
	api.HandleFunc("GET /api/runs/options", s.runOptions)
	api.HandleFunc("GET /api/runs/{id}", s.getRun)
	api.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	api.HandleFunc("POST /api/runs/{id}/stop", s.stopRun)
	api.HandleFunc("POST /api/runs/{id}/finish", s.finishRun)
	api.HandleFunc("POST /api/subtasks/{id}/continue", s.continueSubtask)
}

func (s *Server) driver(w http.ResponseWriter) *runs.Driver {
	d := s.Runs.Load()
	if d == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "runs are not available: the database, the key file, or Docker is missing")
	}
	return d
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	l, err := d.List(r.Context())
	if err != nil {
		s.Log.Error("reading the runs", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the runs cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// runOptions is what the start form offers: each CLI, whether it is signed in, and the models it
// listed itself (12.3); a CLI whose list is not read yet takes a name typed in.
func (s *Server) runOptions(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	type option struct {
		CLI      string   `json:"cli"`
		SignedIn bool     `json:"signedIn"`
		Models   []string `json:"models"`
	}
	out := []option{}
	for _, name := range cli.Names() {
		o := option{CLI: name, Models: []string{}}
		var state string
		if err := pool.QueryRow(r.Context(), `SELECT state FROM cli_logins WHERE cli = $1`, name).Scan(&state); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.boardError(w, "reading the CLI logins", err)
			return
		}
		o.SignedIn = state == "signed_in"
		rows, err := pool.Query(r.Context(), `SELECT name FROM models WHERE cli = $1 AND offered ORDER BY name`, name)
		if err == nil {
			for rows.Next() {
				var m string
				if rows.Scan(&m) == nil {
					o.Models = append(o.Models, m)
				}
			}
			rows.Close()
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"clis": out, "project": "fixture"})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "the request is too large")
		return false
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the request does not read: "+err.Error())
		return false
	}
	return true
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	var req struct {
		CLI            string `json:"cli"`
		Model          string `json:"model"`
		Effort         string `json:"effort"`
		Title          string `json:"title"`
		Prompt         string `json:"prompt"`
		NoTools        bool   `json:"noTools"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
		MaxTurns       int    `json:"maxTurns"`
		WithoutLogin   bool   `json:"withoutLogin"`
		Minimal        bool   `json:"minimal"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id, err := d.Start(r.Context(), runs.StartRequest{CLI: req.CLI, Model: req.Model, Effort: req.Effort, Title: req.Title,
		Prompt: req.Prompt, NoTools: req.NoTools, Timeout: time.Duration(req.TimeoutSeconds) * time.Second, MaxTurns: req.MaxTurns,
		WithoutLogin: req.WithoutLogin, Minimal: req.Minimal})
	if err != nil {
		code, status := http.StatusBadRequest, "INVALID_ARGUMENT"
		if errors.Is(err, runs.ErrNotSignedIn) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
			code, status = http.StatusConflict, "FAILED_PRECONDITION"
		}
		writeError(w, code, status, err.Error())
		return
	}
	s.Log.Info("the operator started a run", "run", id, "cli", req.CLI, "model", req.Model)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	det, err := d.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) || isBadUUID(err) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such run")
		return
	}
	if err != nil {
		s.Log.Error("reading a run", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the run cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, det)
}

// isBadUUID says the database refused an id that is not a UUID.
func isBadUUID(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 22P02")
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var ev []runs.Event
	var err error
	if tail, _ := strconv.Atoi(r.URL.Query().Get("tail")); tail > 0 {
		ev, err = d.Tail(r.Context(), r.PathValue("id"), tail)
	} else {
		ev, err = d.Events(r.Context(), r.PathValue("id"), after, limit, r.URL.Query().Get("raw") == "true")
	}
	if isBadUUID(err) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such run")
		return
	}
	if err != nil {
		s.Log.Error("reading a run's events", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the events cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	err := d.Stop(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, pgx.ErrNoRows), isBadUUID(err):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such run")
	case errors.Is(err, runs.ErrEnded):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	default:
		s.Log.Info("the operator stopped a run", "run", r.PathValue("id"))
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *Server) finishRun(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	err := d.Finish(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, pgx.ErrNoRows), isBadUUID(err):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such run")
	case err != nil:
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) continueSubtask(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such subtask")
		return
	}
	var req struct {
		Text   string `json:"text"`
		Way    string `json:"way"`
		CLI    string `json:"cli"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	run, err := d.Continue(r.Context(), id, runs.ContinueRequest{Text: req.Text, Way: req.Way, CLI: req.CLI, Model: req.Model, Effort: req.Effort})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such subtask")
	case errors.Is(err, runs.ErrBusy), errors.Is(err, runs.ErrNotSignedIn), errors.Is(err, runs.ErrHeld), errors.Is(err, runs.ErrNoRoom),
		errors.Is(err, runs.ErrCheckFamily):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	default:
		s.Log.Info("the operator continued a subtask", "subtask", id, "run", run, "way", req.Way)
		writeJSON(w, http.StatusOK, map[string]string{"run": run})
	}
}
