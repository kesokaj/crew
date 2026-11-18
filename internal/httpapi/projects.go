package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// The routes of projects and their boards (design 15.4). Every change is the operator's, through the
// board, which records it; agents reach the board through the Crew MCP (11.5), never through these.
func (s *Server) projectRoutes(api *routes) {
	api.HandleFunc("GET /api/projects", s.listProjects)
	api.HandleFunc("POST /api/projects", s.createProject)
	api.HandleFunc("GET /api/projects/{p}/overview", s.projectOverview)
	api.HandleFunc("GET /api/projects/{p}/plan", s.projectPlan)
	api.HandleFunc("POST /api/projects/{p}/ok", s.projectOK)
	api.HandleFunc("POST /api/projects/{p}/pause", s.projectState("pause"))
	api.HandleFunc("POST /api/projects/{p}/resume", s.projectState("resume"))
	api.HandleFunc("POST /api/projects/{p}/archive", s.projectState("archive"))
	api.HandleFunc("GET /api/projects/{p}/board", s.projectBoard)
	api.HandleFunc("GET /api/projects/{p}/analysis", s.projectAnalysis)
	api.HandleFunc("GET /api/projects/{p}/stages", s.getStages)
	api.HandleFunc("PUT /api/projects/{p}/stages", s.putStages)
	api.HandleFunc("PUT /api/projects/{p}/stages/{id}", s.putOwnStage)
	api.HandleFunc("DELETE /api/projects/{p}/stages/{id}", s.deleteOwnStage)
	api.HandleFunc("GET /api/projects/{p}/network", s.getNetwork)
	api.HandleFunc("POST /api/projects/{p}/remote/mains/{id}/take", s.remoteMain(true))
	api.HandleFunc("POST /api/projects/{p}/remote/mains/{id}/leave", s.remoteMain(false))
	api.HandleFunc("GET /api/projects/{p}/records/{id}/files/{name}", s.recordFile)
	api.HandleFunc("PUT /api/projects/{p}/network", s.putNetwork)
	api.HandleFunc("GET /api/projects/{p}/settings", s.getProjectSettings)
	api.HandleFunc("PUT /api/projects/{p}/settings", s.putProjectSettings)
	api.HandleFunc("GET /api/projects/{p}/settings/history", s.projectSettingsHistory)
	api.HandleFunc("POST /api/projects/{p}/settings/putback", s.projectSettingsPutBack)
	api.HandleFunc("POST /api/projects/{p}/export", s.exportProject)
	api.HandleFunc("POST /api/projects/{p}/tasks", s.addTask)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}", s.getTask)
	api.HandleFunc("PATCH /api/projects/{p}/tasks/{t}", s.editTask)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/decision", s.taskDecision)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/pull/keep", s.taskKeepBranch)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/may-change", s.taskMayChange)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/code", s.taskCode)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/discard", s.discardTask)
	api.HandleFunc("GET /api/projects/{p}/git", s.projectGit)
	api.HandleFunc("GET /api/projects/{p}/mcp/calls", s.projectMCPCalls)
}

func (s *Server) board(w http.ResponseWriter) *board.Board {
	b := s.Board.Load()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the board is not available: the database does not answer yet")
	}
	return b
}

// The largest request body of these routes: a plan is at most 1 MB.
const maxProjectBody = 2 << 20

// decode reads a JSON body into v, refusing fields v does not have. An empty body is an empty object.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProjectBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "the request is too large")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the request is not what this route takes: "+err.Error())
		return false
	}
	return true
}

// boardError answers a refusal of the board, or logs what is not one.
func (s *Server) boardError(w http.ResponseWriter, what string, err error) {
	if s.writeStale(w, err) {
		return
	}
	var be *board.Error
	var inv *settings.InvalidError
	switch {
	case errors.As(err, &be):
		switch be.Kind {
		case board.NotFound:
			writeError(w, http.StatusNotFound, "NOT_FOUND", be.Msg)
		case board.Invalid:
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", be.Msg)
		case board.Conflict:
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", be.Msg)
		default:
			writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", be.Msg)
		}
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", inv.Error())
	default:
		s.Log.Error(what, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", what+" failed")
	}
}

// changed answers with v after a change, and asks the board to reconcile soon (15.2).
func (s *Server) changed(w http.ResponseWriter, b *board.Board, code int, v any) {
	b.Kick()
	// A new project, or a breakdown sent back, is the planner's to act on (15.2).
	if pl := s.Planner.Load(); pl != nil {
		pl.Kick()
	}
	writeJSON(w, code, v)
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) (*board.Board, *board.Project) {
	return s.projectNamed(w, r, r.PathValue("p"))
}

// projectNamed is the project a request names by its name or its id, or an answer that it is not.
func (s *Server) projectNamed(w http.ResponseWriter, r *http.Request, ref string) (*board.Board, *board.Project) {
	b := s.board(w)
	if b == nil {
		return nil, nil
	}
	p, err := b.Project(r.Context(), ref)
	if err != nil {
		s.boardError(w, "reading the project", err)
		return nil, nil
	}
	return b, p
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	ps, err := b.Projects(r.Context())
	if err != nil {
		s.boardError(w, "reading the projects", err)
		return
	}
	// A hidden project is Crew's own, the gate's (16.2): no list shows it.
	list := []*board.Project{}
	for _, p := range ps {
		if !p.Hidden {
			list = append(list, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": list})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	var n board.NewProject
	if !decode(w, r, &n) {
		return
	}
	p, err := b.CreateProject(r.Context(), n, board.Operator)
	if err != nil {
		s.boardError(w, "creating the project", err)
		return
	}
	s.changed(w, b, http.StatusCreated, p)
}

// projectOverview is one project's goal, state, progress by stage, and its runs' tokens in and out
// (15.4; since 0.53 no budget and no cost).
func (s *Server) projectOverview(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ts, err := b.Tickets(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the tickets", err)
		return
	}
	var tokens int64
	if err := b.Pool.QueryRow(r.Context(), `SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1`, p.ID).Scan(&tokens); err != nil {
		s.boardError(w, "reading the tokens", err)
		return
	}
	byStage := map[string]int{}
	for _, t := range ts {
		byStage[t.Stage]++
	}
	crew, _ := board.CrewProject(r.Context(), b.Pool)
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "locked": crew != "" && crew == p.Name, "tickets": len(ts), "byStage": byStage,
		"tokens": tokens})
}

func (s *Server) projectPlan(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	pl, err := b.Plan(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the plan", err)
		return
	}
	writeJSON(w, http.StatusOK, pl)
}

// projectOK gives the project OK, or sends the breakdown back with a comment (7.2, step 5).
func (s *Server) projectOK(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	var req struct {
		SendBack *string `json:"sendBack"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SendBack != nil {
		if err := b.SendBack(r.Context(), r.PathValue("p"), *req.SendBack, board.Operator); err != nil {
			s.boardError(w, "sending the breakdown back", err)
			return
		}
		p, _ := b.Project(r.Context(), r.PathValue("p"))
		s.changed(w, b, http.StatusOK, p)
		return
	}
	p, err := b.OK(r.Context(), r.PathValue("p"), board.Operator)
	if err != nil {
		s.boardError(w, "the project OK", err)
		return
	}
	s.changed(w, b, http.StatusOK, p)
}

func (s *Server) projectState(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := s.board(w)
		if b == nil {
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &req) {
			return
		}
		if action == "pause" && req.Reason == "" {
			req.Reason = "paused by the operator"
		}
		p, err := b.SetState(r.Context(), r.PathValue("p"), action, req.Reason, board.Operator)
		if err != nil {
			s.boardError(w, action+" the project", err)
			return
		}
		// An archived project is not hosted; Pause and Resume do not touch hosting (19).
		if action == "archive" {
			s.hostingArchived(r.Context(), p.ID)
		}
		s.changed(w, b, http.StatusOK, p)
	}
}

// boardCard is a ticket as a column of the board shows it (20.1, board.png): its number, kind,
// title, and goal; the agents on it with their models, and the steps of the one that runs; why it
// waits and why nothing more starts for it; and its marks.
type boardCard struct {
	Key       string      `json:"key"`
	Title     string      `json:"title"`
	Goal      string      `json:"goal"`
	Kind      string      `json:"kind"`
	Priority  int         `json:"priority"`
	Origin    string      `json:"origin"`
	CreatedBy string      `json:"createdBy"`
	Hold      bool        `json:"hold"`
	Analysis  string      `json:"analysis"`
	Waiting   *string     `json:"waiting"`
	Stuck     *string     `json:"stuck"`
	Replaces  bool        `json:"replaces"`
	FixRounds int         `json:"fixRounds"`
	Agents    []cardAgent `json:"agents"`
}

// cardAgent is a run on a ticket that has not ended, with the steps its subtask set.
type cardAgent struct {
	Role      string  `json:"role"`
	CLI       string  `json:"cli"`
	Model     *string `json:"model"`
	Effort    *string `json:"effort"`
	State     string  `json:"state"`
	StepsDone int     `json:"stepsDone"`
	Steps     int     `json:"steps"`
}

// cardAgents reads the runs of a project that have not ended, by ticket.
func cardAgents(ctx context.Context, q db.Querier, project string) (map[int64][]cardAgent, error) {
	rows, err := q.Query(ctx, `SELECT s.ticket, s.role, r.cli, r.model, r.effort, r.state,
			(SELECT count(*) FROM subtask_steps WHERE subtask = s.id AND done_at IS NOT NULL),
			(SELECT count(*) FROM subtask_steps WHERE subtask = s.id)
		FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1 AND s.ticket IS NOT NULL AND r.state <> 'ended' ORDER BY r.started_at`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]cardAgent{}
	for rows.Next() {
		var id int64
		var a cardAgent
		if err := rows.Scan(&id, &a.Role, &a.CLI, &a.Model, &a.Effort, &a.State, &a.StepsDone, &a.Steps); err != nil {
			return nil, err
		}
		out[id] = append(out[id], a)
	}
	return out, rows.Err()
}

type boardColumn struct {
	ID       string      `json:"id"`
	Title    string      `json:"title,omitempty"` // a project's own stage's name (7.4)
	Optional bool        `json:"optional"`
	Role     string      `json:"role,omitempty"`
	WIP      int         `json:"wip,omitempty"`
	Tickets  []boardCard `json:"tickets"`
}

// projectBoard is the board: one column per stage that is on, with its tickets in their order (15.4).
// With no optional stage on, it says that no other agent looks at the work (7.4).
func (s *Server) projectBoard(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	doc, err := b.Settings(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	ts, err := b.Tickets(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the tickets", err)
		return
	}
	agents, err := cardAgents(r.Context(), b.Pool, p.ID)
	if err != nil {
		s.boardError(w, "reading the runs", err)
		return
	}
	pipe := board.PipelineOf(doc)
	cols := []boardColumn{}
	at := map[string]int{}
	checked := false
	for _, st := range pipe.Columns() {
		at[st.ID] = len(cols)
		cols = append(cols, boardColumn{ID: st.ID, Title: st.Title, Optional: st.Optional, Role: st.Role, WIP: st.WIP, Tickets: []boardCard{}})
		checked = checked || st.Optional
	}
	closed, elsewhere := 0, []boardCard{}
	for _, t := range ts {
		c := boardCard{Key: t.Key, Title: t.Title, Goal: t.Goal, Kind: t.Kind, Priority: t.Priority, Origin: t.Origin,
			CreatedBy: t.CreatedBy, Hold: t.Hold, Analysis: t.Analysis.Status, Waiting: t.Waiting, Stuck: t.Stuck,
			FixRounds: t.FixRounds, Agents: agents[t.ID]}
		if c.Agents == nil {
			c.Agents = []cardAgent{}
		}
		c.Replaces = slices.ContainsFunc(t.Links, func(l board.Link) bool { return l.Kind == "replaces" })
		if t.Stage == settings.StageClosed {
			closed++
			continue
		}
		if i, ok := at[t.Stage]; ok {
			cols[i].Tickets = append(cols[i].Tickets, c)
		} else {
			// In a stage that was switched off after it got there: it finishes that stage.
			elsewhere = append(elsewhere, c)
		}
	}
	// The track of every stage, the ones that are off among them, as the board's head shows it.
	type trackStage struct {
		ID       string `json:"id"`
		Title    string `json:"title,omitempty"`
		Optional bool   `json:"optional"`
		On       bool   `json:"on"`
	}
	track := []trackStage{}
	for _, st := range pipe {
		track = append(track, trackStage{ID: st.ID, Title: st.Title, Optional: st.Optional, On: !st.Optional || st.On})
	}
	out := map[string]any{"project": p, "columns": cols, "closed": closed, "inStagesOff": elsewhere, "stages": track}
	if !checked {
		out["notice"] = "No optional stage is on: a ticket goes from In progress to Done on its command checks alone, and no other agent looks at the work."
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectAnalysis(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ts, err := b.Analyses(r.Context(), p)
	if err != nil {
		s.boardError(w, "reading the analysis decisions", err)
		return
	}
	type row struct {
		Key          string         `json:"key"`
		Title        string         `json:"title"`
		Stage        string         `json:"stage"`
		ClosedReason *string        `json:"closedReason"`
		Analysis     board.Analysis `json:"analysis"`
	}
	out := []row{}
	for _, t := range ts {
		out = append(out, row{t.Key, t.Title, t.Stage, t.ClosedReason, t.Analysis})
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
}

func (s *Server) getStages(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	doc, err := b.Settings(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stages": board.PipelineOf(doc)})
}

// putStages switches optional stages on or off: {"e2e": true, "review": false} (7.4).
func (s *Server) putStages(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var on map[string]bool
	if !decode(w, r, &on) {
		return
	}
	if err := b.SetStages(r.Context(), p, on, board.Operator); err != nil {
		s.boardError(w, "switching the stages", err)
		return
	}
	b.Kick()
	s.getStages(w, r)
}

// remoteMain answers a main from the remote that changes files that are code (7.12, 13.8).
func (s *Server) remoteMain(take bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, p := s.project(w, r)
		if p == nil {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no such main")
			return
		}
		if err := b.DecideRemoteMain(r.Context(), p, id, take, board.Operator); err != nil {
			s.boardError(w, "answering the remote's main", err)
			return
		}
		s.changed(w, b, http.StatusOK, map[string]any{"taken": take})
	}
}

// getNetwork says a project's network mode (13.6): what it set, inherit or a mode, the machine's mode,
// and the mode its sandboxes start in.
func (s *Server) getNetwork(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	doc, err := b.Settings(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	own, _ := doc["sandbox"].(settings.Doc)["network"].(string)
	machine, err := settings.NetworkMode(r.Context(), b.Pool, "", 0)
	if err != nil {
		s.boardError(w, "reading the machine's network mode", err)
		return
	}
	mode, err := settings.NetworkMode(r.Context(), b.Pool, p.ID, 0)
	if err != nil {
		s.boardError(w, "reading the network mode", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"own": own, "machine": machine, "mode": mode})
}

// putNetwork sets a project's network mode: {"mode": "model_only"}, or "inherit" for the machine's.
func (s *Server) putNetwork(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	guard, err := s.projectGuard(r.Context(), r, b.Pool)
	if err != nil {
		s.boardError(w, "reading the machine's network mode", err)
		return
	}
	if err := b.SetNetwork(r.Context(), p, in.Mode, board.Operator, guard); err != nil {
		s.boardError(w, "setting the network mode", err)
		return
	}
	s.getNetwork(w, r)
}

// putOwnStage adds a project's own stage, or replaces it (7.4, R101): {"title", "role", "agent",
// "prompt", "wip", "exit", "on", "after"}.
func (s *Server) putOwnStage(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var in board.OwnStage
	if !decode(w, r, &in) {
		return
	}
	in.ID = r.PathValue("id")
	if err := b.SetOwnStage(r.Context(), p, in, board.Operator); err != nil {
		s.boardError(w, "setting the stage", err)
		return
	}
	b.Kick()
	s.getStages(w, r)
}

func (s *Server) deleteOwnStage(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	if err := b.RemoveOwnStage(r.Context(), p, r.PathValue("id"), board.Operator); err != nil {
		s.boardError(w, "removing the stage", err)
		return
	}
	s.getStages(w, r)
}

func (s *Server) getProjectSettings(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	doc, err := b.Settings(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) putProjectSettings(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "the settings are too large")
		return
	}
	d, err := settings.ParseJSON(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	guard, err := s.projectGuard(r.Context(), r, b.Pool)
	if err != nil {
		s.boardError(w, "reading the machine's network mode", err)
		return
	}
	before, hosted := s.hostingBefore(r.Context(), p.ID)
	if err := b.SaveSettings(r.Context(), p, d, board.Operator, guard); err != nil {
		s.boardError(w, "saving the settings", err)
		return
	}
	s.hostingAfter(r.Context(), p.ID, before, hosted)
	b.Kick()
	s.getProjectSettings(w, r)
}

// projectSettingsHistory is a key's history in a project's settings (14.2): ?key=, a dotted path.
func (s *Server) projectSettingsHistory(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	h, err := settings.ProjectHistory(r.Context(), b.Pool, p.ID, p.Name, p.Key, r.URL.Query().Get("key"))
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) projectSettingsPutBack(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var in putBack
	if !decode(w, r, &in) {
		return
	}
	guard, err := s.projectGuard(r.Context(), r, b.Pool)
	if err != nil {
		s.boardError(w, "reading the machine's network mode", err)
		return
	}
	before, hosted := s.hostingBefore(r.Context(), p.ID)
	if err := b.PutBackSettings(r.Context(), p, in.Seq, in.Key, board.Operator, guard); err != nil {
		if errors.Is(err, settings.ErrNoSuchKey) || errors.Is(err, settings.ErrNoSuchChange) {
			s.settingsError(w, err)
			return
		}
		s.boardError(w, "putting the setting back", err)
		return
	}
	s.hostingAfter(r.Context(), p.ID, before, hosted)
	b.Kick()
	s.getProjectSettings(w, r)
}

// exportProject writes the project out as the files of 6.2, as a tar archive to save (15.4).
func (s *Server) exportProject(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	x := s.Export.Load()
	if x == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "export is not available")
		return
	}
	// Written to a file first, so a failure is an error answer and not half an archive, and the
	// bundle is not held in memory.
	f, err := os.CreateTemp("", "crew-export-*.tar")
	if err != nil {
		s.boardError(w, "writing the export", err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := x.Write(r.Context(), p.ID, f); err != nil {
		s.boardError(w, "writing the export", err)
		return
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		s.boardError(w, "writing the export", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", p.Name+"-export.tar"))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

// addTask adds a ticket as the operator: approved by being added (7.3).
func (s *Server) addTask(w http.ResponseWriter, r *http.Request) {
	b := s.board(w)
	if b == nil {
		return
	}
	var n board.NewTicket
	if !decode(w, r, &n) {
		return
	}
	t, err := b.AddTicket(r.Context(), r.PathValue("p"), n, board.OriginOperator, board.Operator)
	if err != nil {
		s.boardError(w, "adding the ticket", err)
		return
	}
	s.changed(w, b, http.StatusCreated, t)
}

// getTask is a ticket with its record and its history (15.4).
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "reading the ticket", err)
		return
	}
	h, err := b.History(r.Context(), t)
	if err != nil {
		s.boardError(w, "reading the ticket's history", err)
		return
	}
	if h == nil {
		h = []db.Change{}
	}
	// Its record (6.3): the entries agents and the operator wrote, and Crew's own check runs.
	recs, err := b.Records(r.Context(), t, false)
	if err != nil {
		s.boardError(w, "reading the ticket's records", err)
		return
	}
	checks, err := b.CheckRuns(r.Context(), t)
	if err != nil {
		s.boardError(w, "reading the ticket's check runs", err)
		return
	}
	if recs == nil {
		recs = []board.Record{}
	}
	if checks == nil {
		checks = []board.CheckRun{}
	}
	// The tickets that link to it (20.2, Links): those that depend on it, were created from it, or
	// replace it.
	rows, err := b.Pool.Query(r.Context(), `SELECT l.kind, $2 || '-' || f.number FROM ticket_links l JOIN tickets f ON f.id = l.from_ticket
		WHERE l.to_ticket = $1 ORDER BY l.kind, f.number`, t.ID, p.Key)
	if err != nil {
		s.boardError(w, "reading the ticket's links", err)
		return
	}
	back, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (board.Link, error) {
		var l board.Link
		return l, r.Scan(&l.Kind, &l.To)
	})
	if err != nil {
		s.boardError(w, "reading the ticket's links", err)
		return
	}
	if back == nil {
		back = []board.Link{}
	}
	// The images kept with entries: an E2E tester's screenshots (7.4), by the entry's id.
	files := map[string][]board.RecordFile{}
	for _, rec := range recs {
		if rec.Kind != board.RecordE2E {
			continue
		}
		fs, err := b.RecordFiles(r.Context(), rec.ID)
		if err != nil {
			s.boardError(w, "reading the entry's files", err)
			return
		}
		if len(fs) > 0 {
			files[strconv.FormatInt(rec.ID, 10)] = fs
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": t, "history": h, "records": recs, "checkRuns": checks, "linkedFrom": back, "files": files})
}

// recordFile serves one image kept with an entry, as an image and nothing else: it was made in a
// sandbox, so it is never shown as a page (7.4).
func (s *Server) recordFile(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such file")
		return
	}
	f, err := b.RecordFileData(r.Context(), p, id, r.PathValue("name"))
	if err != nil {
		s.boardError(w, "reading the file", err)
		return
	}
	w.Header().Set("Content-Type", f.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(f.Data)
}

func (s *Server) editTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var e board.Edit
	if !decode(w, r, &e) {
		return
	}
	if e.Network != nil && !sessionOf(r).fresh {
		lan, err := s.ticketLAN(r, b, p, *e.Network)
		if err != nil {
			s.boardError(w, "reading the ticket's network mode", err)
			return
		}
		if lan {
			s.writeStale(w, &staleError{keys: []string{"network"}})
			return
		}
	}
	t, err := b.EditTicket(r.Context(), p, r.PathValue("t"), e, board.Operator)
	if err != nil {
		s.boardError(w, "changing the ticket", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}

// taskDecision is Approve now, Discard, or Hold on a new feature during its notice, and Approve or
// Discard on a ticket whose analysis waits for the operator, with approvals.created_task set to
// operator (7.3).
func (s *Server) taskDecision(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Choice string `json:"choice"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := b.NoticeDecision(r.Context(), p, r.PathValue("t"), req.Choice, req.Reason, board.Operator)
	if err != nil {
		s.boardError(w, "the decision", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}

// taskKeepBranch is Keep Crew's on a ticket whose branch went another way on GitHub (21).
func (s *Server) taskKeepBranch(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	if err := b.KeepCrewsBranch(r.Context(), p, r.PathValue("t"), board.Operator); err != nil {
		s.boardError(w, "keeping Crew's branch", err)
		return
	}
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "the ticket", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}

// taskMayChange is Agree or Refuse on a ticket an agent created that may change files that instruct
// agents (13.8).
func (s *Server) taskMayChange(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Choice string `json:"choice"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Agree lets a ticket an agent created change files that instruct agents, which waited for the
	// operator as a held main does (rule 13): a fresh sign-in, as taking one asks. Refuse only narrows.
	if req.Choice == "agree" && !sessionOf(r).fresh {
		s.signIn(w, ReasonFresh, "agreeing that a ticket may change files that instruct agents needs a fresh sign-in", nil)
		return
	}
	t, err := b.MayChangeAnswer(r.Context(), p, r.PathValue("t"), req.Choice, board.Operator)
	if err != nil {
		s.boardError(w, "the answer", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}

// taskCode is Allow or Refuse on a ticket whose files that are code wait, about the head the operator
// saw (13.8).
func (s *Server) taskCode(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Choice string `json:"choice"`
		Head   string `json:"head"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Allow lets a ticket whose branch changes protected code/hooks/workflows merge into main (rule 13).
	// Refuse only sends the head back to the builder, so it asks nothing.
	if req.Choice == "allow" && !sessionOf(r).fresh {
		s.signIn(w, ReasonFresh, "allowing a ticket whose files that are code wait needs a fresh sign-in", nil)
		return
	}
	t, err := b.CodeAnswer(r.Context(), p, r.PathValue("t"), req.Choice, req.Head, board.Operator)
	if err != nil {
		s.boardError(w, "the answer", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}

func (s *Server) discardTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := b.Discard(r.Context(), p, r.PathValue("t"), req.Reason, board.Operator)
	if err != nil {
		s.boardError(w, "discarding the ticket", err)
		return
	}
	s.changed(w, b, http.StatusOK, t)
}
