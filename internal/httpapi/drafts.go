package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/planning"
)

// The routes of plan drafts (15.4): developing a plan with the planning agent before any project
// exists (7.2). A project is made from a draft with POST /api/projects and its "draft".
func (s *Server) draftRoutes(api *routes) {
	api.HandleFunc("GET /api/drafts", s.listDrafts)
	api.HandleFunc("POST /api/drafts", s.createDraft)
	api.HandleFunc("GET /api/drafts/{d}", s.getDraft)
	api.HandleFunc("POST /api/drafts/{d}/messages", s.draftMessage)
	api.HandleFunc("GET /api/crew/mcp/tools", s.mcpTools)
	api.HandleFunc("GET /api/crew/mcp", s.mcpScreen)
}

func (s *Server) drafts(w http.ResponseWriter) *planning.Drafts {
	d := s.Drafts.Load()
	if d == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the drafts are not available: the database does not answer yet")
	}
	return d
}

func (s *Server) draftError(w http.ResponseWriter, doing string, err error) {
	var r *planning.Refusal
	switch {
	case errors.As(err, &r) && r.NotFound:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Msg)
	case errors.As(err, &r):
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", r.Msg)
	default:
		s.Log.Error(doing, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", doing+" failed; the server's log says why")
	}
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	d := s.drafts(w)
	if d == nil {
		return
	}
	list, err := d.List(r.Context())
	if err != nil {
		s.draftError(w, "listing the drafts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": list})
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	d := s.drafts(w)
	if d == nil {
		return
	}
	var n planning.NewDraft
	if !decode(w, r, &n) {
		return
	}
	id, err := d.Create(r.Context(), n)
	if err != nil {
		s.draftError(w, "starting a draft", err)
		return
	}
	x, err := d.Get(r.Context(), id)
	if err != nil {
		s.draftError(w, "reading the draft", err)
		return
	}
	writeJSON(w, http.StatusCreated, x)
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	d := s.drafts(w)
	if d == nil {
		return
	}
	x, err := d.Get(r.Context(), r.PathValue("d"))
	if err != nil {
		s.draftError(w, "reading the draft", err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

func (s *Server) draftMessage(w http.ResponseWriter, r *http.Request) {
	d := s.drafts(w)
	if d == nil {
		return
	}
	// A message, or a request about a part of the plan: its quote and what is asked of it (7.2).
	var req planning.Said
	if !decode(w, r, &req) {
		return
	}
	if err := d.MessageAbout(r.Context(), r.PathValue("d"), req); err != nil {
		s.draftError(w, "adding the message", err)
		return
	}
	x, err := d.Get(r.Context(), r.PathValue("d"))
	if err != nil {
		s.draftError(w, "reading the draft", err)
		return
	}
	writeJSON(w, http.StatusAccepted, x)
}

// projectGit is a project's repository (15.4): where it came from, its remote, main, and its pushes.
func (s *Server) projectGit(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	pushes, err := b.Pushes(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the pushes", err)
		return
	}
	pulls, err := b.PullRequests(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the pull requests", err)
		return
	}
	if pulls == nil {
		pulls = []board.PullRequest{}
	}
	// What Crew fetched from the remote, and what came in on its main from outside (6.6, 7.12).
	var fetchedAt *time.Time
	var fetchDetail *string
	if err := b.Pool.QueryRow(r.Context(), `SELECT fetched_at, fetch_detail FROM projects WHERE id = $1`, p.ID).Scan(&fetchedAt, &fetchDetail); err != nil {
		s.boardError(w, "reading the fetch", err)
		return
	}
	type remoteMain struct {
		Remote string    `json:"remote"`
		Kind   string    `json:"kind"`
		State  string    `json:"state"`
		Detail *string   `json:"detail"`
		Ticket *string   `json:"ticket"`
		At     time.Time `json:"at"`
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT m.remote, m.kind, m.state, m.detail, CASE WHEN t.id IS NULL THEN NULL ELSE $2 || '-' || t.number END, m.created_at
		FROM remote_mains m LEFT JOIN tickets t ON t.id = m.ticket WHERE m.project = $1 ORDER BY m.id DESC LIMIT 10`, p.ID, p.Key)
	if err != nil {
		s.boardError(w, "reading the remote's mains", err)
		return
	}
	outside, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (remoteMain, error) {
		var m remoteMain
		return m, r.Scan(&m.Remote, &m.Kind, &m.State, &m.Detail, &m.Ticket, &m.At)
	})
	if err != nil {
		s.boardError(w, "reading the remote's mains", err)
		return
	}
	if outside == nil {
		outside = []remoteMain{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": p.RepoSource, "remote": p.Remote, "main": b.Git.Main(r.Context(), p.Name),
		"pushes": pushes, "pulls": pulls, "usesPulls": b.UsesPulls(r.Context(), p), "fetchedAt": fetchedAt, "fetchDetail": fetchDetail, "outside": outside})
}

// mcpCall is one call to the Crew MCP, as the Crew MCP screen lists it (20.1).
type mcpCall struct {
	At     time.Time `json:"at"`
	Run    *string   `json:"run"`
	Role   string    `json:"role"`
	Tool   string    `json:"tool"`
	Args   any       `json:"args"`
	OK     bool      `json:"ok"`
	Result string    `json:"result"`
	MS     int       `json:"ms"`
}

// projectMCPCalls lists the calls agents made to the Crew MCP in a project, and in the draft it was
// made from, newest first.
func (s *Server) projectMCPCalls(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rows, err := b.Pool.Query(r.Context(), `SELECT at, run::text, role, tool, args, ok, result, ms FROM mcp_calls
		WHERE project = $1 OR (draft IS NOT NULL AND draft = $2) ORDER BY id DESC LIMIT 200`, p.ID, p.Draft)
	if err != nil {
		s.boardError(w, "reading the calls", err)
		return
	}
	calls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mcpCall, error) {
		var c mcpCall
		return c, row.Scan(&c.At, &c.Run, &c.Role, &c.Tool, &c.Args, &c.OK, &c.Result, &c.MS)
	})
	if err != nil {
		s.boardError(w, "reading the calls", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": calls})
}

// mcpTools lists the tools of the Crew MCP with who may call each (15.4).
func (s *Server) mcpTools(w http.ResponseWriter, r *http.Request) {
	tools, err := baseline.Tools()
	if err != nil {
		s.Log.Error("reading the baseline's tools", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the tools could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}
