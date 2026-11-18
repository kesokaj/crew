package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
)

func (s *Server) m22Routes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/factory-tasks", s.listFactoryTasks)
	api.HandleFunc("POST /api/projects/{p}/factory-tasks", s.createFactoryTask)
	api.HandleFunc("GET /api/projects/{p}/factory-tasks/{id}", s.getFactoryTask)
	api.HandleFunc("POST /api/projects/{p}/factory-tasks/{id}/complete", s.completeFactoryTask)
	api.HandleFunc("GET /api/projects/{p}/tasks-ledger", s.tasksLedger)
	api.HandleFunc("POST /api/projects/{p}/coordinator/rank", s.coordinatorRank)
	api.HandleFunc("POST /api/projects/{p}/coordinator/dispatch", s.coordinatorDispatch)
	api.HandleFunc("GET /api/queue/global", s.globalQueue)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/research", s.listTaskResearch)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/research", s.triggerTaskResearch)
}

func (s *Server) listFactoryTasks(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := r.URL.Query()
	list, err := b.ListFactoryTasks(r.Context(), p.ID, q.Get("kind"), q.Get("state"))
	if err != nil {
		s.boardError(w, "listing factory tasks", err)
		return
	}
	if list == nil {
		list = []board.FactoryTask{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": list})
}

func (s *Server) createFactoryTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Kind         string `json:"kind"`
		Title        string `json:"title"`
		Role         string `json:"role"`
		State        string `json:"state"`
		Ticket       string `json:"ticket"`
		SpecRef      string `json:"specRef"`
		OriginRoom   string `json:"originRoom"`
		MaxTries     int    `json:"maxTries"`
		BudgetTokens int64  `json:"budgetTokens"`
		ArtifactPath string `json:"artifactPath"`
	}
	if !decode(w, r, &req) {
		return
	}
	ft, err := b.CreateFactoryTask(r.Context(), p.ID, board.NewFactoryTask{
		Kind:         req.Kind,
		Title:        req.Title,
		Role:         req.Role,
		State:        req.State,
		TicketRef:    req.Ticket,
		SpecRef:      req.SpecRef,
		OriginRoom:   req.OriginRoom,
		MaxTries:     req.MaxTries,
		BudgetTokens: req.BudgetTokens,
		ArtifactPath: req.ArtifactPath,
		CreatedBy:    board.Operator,
	})
	if err != nil {
		s.boardError(w, "creating factory task", err)
		return
	}
	writeJSON(w, http.StatusCreated, ft)
}

func (s *Server) getFactoryTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ft, err := b.GetFactoryTask(r.Context(), p.ID, r.PathValue("id"))
	if err != nil {
		s.boardError(w, "reading factory task", err)
		return
	}
	writeJSON(w, http.StatusOK, ft)
}

func (s *Server) completeFactoryTask(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		State        string `json:"state"`
		Summary      string `json:"summary"`
		ArtifactPath string `json:"artifactPath"`
	}
	if !decode(w, r, &req) {
		return
	}
	ft, err := b.CompleteFactoryTask(r.Context(), p.ID, r.PathValue("id"), req.State, req.Summary, req.ArtifactPath, board.Operator)
	if err != nil {
		s.boardError(w, "completing factory task", err)
		return
	}
	writeJSON(w, http.StatusOK, ft)
}

func (s *Server) tasksLedger(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ledger, err := b.TasksLedger(r.Context(), p.ID)
	if err != nil {
		s.boardError(w, "reading tasks ledger", err)
		return
	}
	writeJSON(w, http.StatusOK, ledger)
}

func (s *Server) coordinatorRank(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	res, err := b.RankBacklog(r.Context(), p.ID)
	if err != nil {
		s.boardError(w, "ranking backlog", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) coordinatorDispatch(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		SpawnSession bool   `json:"spawnSession"`
		Prompt       string `json:"prompt"`
	}
	decodeOptional(r, &req)
	var kickFn func()
	if pl := s.Pipeline.Load(); pl != nil {
		kickFn = pl.Kick
	} else if pr := s.Planner.Load(); pr != nil {
		kickFn = pr.Kick
	}
	rank, err := planning.DispatchProjectToGlobalQueue(r.Context(), b, p.ID, kickFn)
	if err != nil {
		s.boardError(w, "dispatching coordinator", err)
		return
	}
	var sess *runs.PeerSession
	if req.SpawnSession {
		d := s.driver(w)
		if d == nil {
			return
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			prompt = "Sequence Backlog for " + p.Name + "; ready=" + strings.Join(rank.Ready, ",")
		}
		sess, err = d.SpawnPeerSession(r.Context(), runs.SpawnPeerRequest{
			ProjectID: p.ID,
			TaskKind:  "coordinator",
			Role:      "@coordinator",
			Title:     "Dispatch Backlog for " + p.Name,
			Prompt:    prompt,
		})
		if err != nil {
			s.writeRunError(w, "spawning @coordinator session", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rank": rank, "session": sess})
}

func (s *Server) globalQueue(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	snap, err := planning.GlobalQueueView(r.Context(), pool, s.Board.Load())
	if err != nil {
		s.boardError(w, "reading global queue", err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) listTaskResearch(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	tk, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "resolving ticket", err)
		return
	}
	list, err := b.ListResearchBriefs(r.Context(), p, tk.ID, tk.Key)
	if err != nil {
		s.boardError(w, "listing research briefs", err)
		return
	}
	if list == nil {
		list = []board.ResearchBrief{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefs": list})
}

func (s *Server) triggerTaskResearch(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	tk, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		s.boardError(w, "resolving ticket", err)
		return
	}
	var req struct {
		RunID      string `json:"runId"`
		FailOutput string `json:"failOutput"`
	}
	decodeOptional(r, &req)
	pipe := s.Pipeline.Load()
	if pipe == nil {
		d := s.driver(w)
		if d == nil {
			return
		}
		pipe = &pipeline.Pipeline{Pool: b.Pool, Board: b, Runs: d, Version: s.Machine.Version}
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		if err := b.Pool.QueryRow(r.Context(), `SELECT r.id::text FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE s.ticket = $1 AND s.kind IN ('build', 'fix') ORDER BY r.started_at DESC LIMIT 1`, tk.ID).Scan(&runID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.boardError(w, "resolving builder run", err)
			return
		}
	}
	failOutput := strings.TrimSpace(req.FailOutput)
	if failOutput == "" {
		if err := b.Pool.QueryRow(r.Context(), `SELECT coalesce(detail, '') FROM check_runs
			WHERE ticket = $1 AND state = 'failed' ORDER BY id DESC LIMIT 1`, tk.ID).Scan(&failOutput); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.boardError(w, "reading failed check output", err)
			return
		}
	}
	res, err := pipe.TriggerResearchCircuitBreaker(r.Context(), p, tk, runID, failOutput)
	if err != nil {
		s.writeRunError(w, "triggering @researcher circuit breaker", err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
