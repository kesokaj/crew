package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runs"
)

func (s *Server) agentAPIRoutes(api *routes) {
	api.HandleFunc("GET /api/agents/runtime", s.agentRuntime)
	api.HandleFunc("GET /api/agents/sessions", s.listAgentSessions)
	api.HandleFunc("POST /api/agents/sessions", s.spawnAgentSession)
	api.HandleFunc("GET /api/agents/sessions/{id}", s.getAgentSession)
	api.HandleFunc("POST /api/agents/sessions/{id}/turn", s.turnAgentSession)
	api.HandleFunc("POST /api/agents/sessions/{id}/steer", s.steerAgentSession)
	api.HandleFunc("POST /api/agents/sessions/{id}/abort", s.abortAgentSession)
	api.HandleFunc("GET /api/agents/sessions/{id}/stream", s.streamAgentSession)
}

func (s *Server) agentRuntime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"harness":        cli.DescribeRuntime(),
		"computeDriver":  "docker-kata",
		"kubernetesSeam": true,
	})
}

func (s *Server) listAgentSessions(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	q := r.URL.Query()
	activeOnly := q.Get("active") == "1" || q.Get("active") == "true"
	list, err := d.ListPeerSessions(r.Context(), q.Get("project"), activeOnly)
	if err != nil {
		s.Log.Error("listing peer agent sessions", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not list agent sessions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) spawnAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	b := s.board(w)
	if b == nil {
		return
	}
	var req struct {
		Project     string `json:"project"`
		Ticket      string `json:"ticket"`
		TaskID      string `json:"taskId"`
		TaskKind    string `json:"taskKind"`
		Untracked   bool   `json:"untracked"`
		Role        string `json:"role"`
		Title       string `json:"title"`
		Prompt      string `json:"prompt"`
		Branch      string `json:"branch"`
		Base        string `json:"base"`
		ReadOnly    *bool  `json:"readOnly"`
		Model       string `json:"model"`
		Effort      string `json:"effort"`
		AvoidFamily string `json:"avoidFamily"`
	}
	if !decode(w, r, &req) {
		return
	}
	proj, err := b.Project(r.Context(), req.Project)
	if err != nil {
		s.boardError(w, "resolving project", err)
		return
	}
	var ticketID int64
	if strings.TrimSpace(req.Ticket) != "" {
		tk, err := b.Ticket(r.Context(), proj, req.Ticket)
		if err != nil {
			s.boardError(w, "resolving ticket", err)
			return
		}
		ticketID = tk.ID
		if req.Branch == "" && tk.Branch != nil {
			req.Branch = *tk.Branch
		}
	}
	customReadOnly := req.ReadOnly
	if _, rerr := runs.NormalizePeerRole(req.Role); rerr != nil {
		ag, body, lerr := b.LoadCustomWorkflowAgent(r.Context(), proj, req.Role)
		if lerr != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("%s: %v", rerr.Error(), lerr))
			return
		}
		if customReadOnly == nil {
			ro := ag.ReadOnly
			customReadOnly = &ro
		}
		if body != "" {
			req.Prompt = body + "\n\n" + req.Prompt
		}
		req.Role = "@" + ag.Slug
	}
	var preMintedTaskID string
	if ticketID == 0 {
		if req.Untracked || (strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.TaskKind) == "") {
			s.writeRunError(w, "spawning peer agent session", runs.ErrUntrackedWork)
			return
		}
		if strings.TrimSpace(req.TaskID) == "" {
			ft, ferr := b.CreateFactoryTask(r.Context(), proj.ID, board.NewFactoryTask{
				Kind:      req.TaskKind,
				Title:     strings.TrimSpace(req.Title),
				Role:      req.Role,
				CreatedBy: nonEmptyStr(req.Role, board.Operator),
			})
			if ferr != nil {
				s.writeRunError(w, "spawning peer agent session", fmt.Errorf("%v: %w", ferr, runs.ErrUntrackedWork))
				return
			}
			req.TaskID = ft.ID
			preMintedTaskID = ft.ID
		}
	}
	sess, err := d.SpawnPeerSession(r.Context(), runs.SpawnPeerRequest{
		ProjectID:   proj.ID,
		TicketID:    ticketID,
		TaskID:      req.TaskID,
		TaskKind:    req.TaskKind,
		Untracked:   req.Untracked,
		Role:        req.Role,
		Title:       req.Title,
		Prompt:      req.Prompt,
		Branch:      req.Branch,
		Base:        req.Base,
		ReadOnly:    customReadOnly,
		ModelPin:    req.Model,
		EffortPin:   req.Effort,
		AvoidFamily: req.AvoidFamily,
	})
	if err != nil {
		if preMintedTaskID != "" {
			_, _ = b.CompleteFactoryTask(r.Context(), proj.ID, preMintedTaskID, "failed", err.Error(), "", board.Operator)
		}
		s.writeRunError(w, "spawning peer agent session", err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func nonEmptyStr(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return b
}

func (s *Server) getAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	sess, err := d.GetPeerSession(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeRunError(w, "reading peer agent session", err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) turnAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	sess, err := d.TurnPeerSession(r.Context(), r.PathValue("id"), req.Text)
	if err != nil {
		s.writeRunError(w, "continuing peer agent session", err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) steerAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := d.SteerPeerSession(r.Context(), r.PathValue("id"), req.Text); err != nil {
		s.writeRunError(w, "steering peer agent session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steered": true})
}

func (s *Server) abortAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	id := r.PathValue("id")
	if err := d.AbortPeerSession(r.Context(), id); err != nil {
		s.writeRunError(w, "aborting peer agent session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aborted": true, "id": id})
}

func (s *Server) streamAgentSession(w http.ResponseWriter, r *http.Request) {
	d := s.driver(w)
	if d == nil {
		return
	}
	id := r.PathValue("id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	wantSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.URL.Query().Get("sse") == "1"
	if !wantSSE {
		evs, err := d.StreamPeerSession(r.Context(), id, after, limit)
		if err != nil {
			s.writeRunError(w, "streaming peer agent session", err)
			return
		}
		nextAfter := after
		if len(evs) > 0 {
			nextAfter = evs[len(evs)-1].Seq
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs, "nextAfter": nextAfter})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	once := r.URL.Query().Get("once") == "1"
	cursor := after
	for {
		evs, err := d.StreamPeerSession(r.Context(), id, cursor, limit)
		if err != nil {
			return
		}
		for _, ev := range evs {
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, b)
			cursor = ev.Seq
		}
		if flusher != nil {
			flusher.Flush()
		}
		if once {
			return
		}
		sess, err := d.GetPeerSession(r.Context(), id)
		if err != nil || sess.State == "ended" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (s *Server) writeRunError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such agent session or project")
	case errors.Is(err, runs.ErrUntrackedWork):
		writeError(w, http.StatusConflict, "UNTRACKED_WORK", err.Error())
	case errors.Is(err, runs.ErrEnded), errors.Is(err, runs.ErrCheckFamily):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case errors.Is(err, runs.ErrBusy), errors.Is(err, runs.ErrHeld), errors.Is(err, runs.ErrNoRoom), errors.Is(err, runs.ErrNotSignedIn):
		writeError(w, http.StatusConflict, "ABORTED", err.Error())
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "unsupported peer"):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	default:
		s.Log.Error(what, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
