package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/dreamer"
	"github.com/kesokaj/crew/internal/knowledge"
	"github.com/kesokaj/crew/internal/memory"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/skills"
)

func (s *Server) projectHomeDir(projectID string) string {
	if s.ProjectHomeDir != nil {
		return s.ProjectHomeDir(projectID)
	}
	if ctrl, _ := s.projectSandboxCtrl(); ctrl != nil {
		return ctrl.ProjectHome(projectID)
	}
	return ""
}

func (s *Server) m26Routes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/dreamstore", s.getDreamstore)
	api.HandleFunc("POST /api/projects/{p}/dreamstore", s.postDreamstore)
	api.HandleFunc("POST /api/projects/{p}/dreamstore/{n}/outcome", s.postDreamstoreOutcome)
	api.HandleFunc("POST /api/projects/{p}/dreamstore/drift", s.postDreamstoreDrift)

	api.HandleFunc("GET /api/projects/{p}/dream/proposals", s.getDreamProposals)
	api.HandleFunc("POST /api/projects/{p}/dream/run", s.postDreamRun)
	api.HandleFunc("POST /api/projects/{p}/dream/proposals/{key}/decision", s.postDreamDecision)

	api.HandleFunc("POST /api/projects/{p}/skills-registry/vet", s.postSkillVet)
	api.HandleFunc("POST /api/projects/{p}/skills-registry/install", s.postSkillInstall)

	api.HandleFunc("GET /api/knowledge", s.getCrossKnowledge)
	api.HandleFunc("POST /api/knowledge/promote", s.postCrossKnowledgePromote)
	api.HandleFunc("POST /api/projects/{p}/knowledge/{key}/act", s.postCrossKnowledgeAct)
}

func (s *Server) getDreamstore(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	includeQuarantined := r.URL.Query().Get("includeQuarantined") == "true"
	var (
		entries []memory.Entry
		err     error
	)
	if q != "" {
		entries, err = memory.Search(r.Context(), b.Pool, p.ID, q, 100, false)
	} else {
		entries, err = memory.List(r.Context(), b.Pool, p.ID, includeQuarantined)
	}
	if err != nil {
		s.boardError(w, "reading dreamstore", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) postDreamstore(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Kind           string   `json:"kind"`
		Title          string   `json:"title"`
		Body           string   `json:"body"`
		AppliesTo      []string `json:"appliesTo"`
		Paths          []string `json:"paths"`
		Keywords       []string `json:"keywords"`
		VerifiedCommit string   `json:"verifiedCommit"`
		EvidenceRuns   []string `json:"evidenceRuns"`
		Author         string   `json:"author"`
	}
	if !decode(w, r, &req) {
		return
	}
	entry, err := memory.Add(r.Context(), b.Pool, memory.AddInput{
		ProjectID:      p.ID,
		Kind:           req.Kind,
		Title:          req.Title,
		Body:           req.Body,
		AppliesTo:      req.AppliesTo,
		Paths:          req.Paths,
		Keywords:       req.Keywords,
		VerifiedCommit: req.VerifiedCommit,
		EvidenceRuns:   req.EvidenceRuns,
		Author:         req.Author,
	})
	if err != nil {
		if errors.Is(err, memory.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		s.boardError(w, "adding dreamstore entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) postDreamstoreOutcome(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	num, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || num <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "dreamstore entry number must be a positive integer")
		return
	}
	var req struct {
		RunID          string `json:"runId"`
		VerifiedCommit string `json:"verifiedCommit"`
		PassedFirstTry bool   `json:"passedFirstTry"`
		Contradicted   bool   `json:"contradicted"`
	}
	if !decode(w, r, &req) {
		return
	}
	entry, err := memory.RecordOutcome(r.Context(), b.Pool, p.ID, num, req.RunID, req.VerifiedCommit, req.PassedFirstTry, req.Contradicted)
	if errors.Is(err, memory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "recording dreamstore outcome", err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) postDreamstoreDrift(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Commit       string   `json:"commit"`
		TouchedPaths []string `json:"touchedPaths"`
	}
	if !decode(w, r, &req) {
		return
	}
	decayed, quarantined, err := memory.ApplyCommitDrift(r.Context(), b.Pool, p.ID, req.Commit, req.TouchedPaths)
	if err != nil {
		s.boardError(w, "applying commit drift", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decayed": decayed, "quarantined": quarantined})
}

func (s *Server) getDreamProposals(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := dreamer.ListProposals(r.Context(), b.Pool, p.ID)
	if err != nil {
		s.boardError(w, "listing dream proposals", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": list})
}

func (s *Server) postDreamRun(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Title      string              `json:"title"`
		Actor      string              `json:"actor"`
		Prompt     string              `json:"prompt"`
		SpawnAgent bool                `json:"spawnAgent"`
		Candidates []dreamer.Candidate `json:"candidates"`
	}
	if !decode(w, r, &req) {
		return
	}
	var spawner dreamer.PeerSpawner
	if req.SpawnAgent {
		if drv := s.Runs.Load(); drv != nil {
			spawner = func(ctx context.Context, projectID, taskID, title, prompt string) (string, error) {
				ro := true
				ps, err := drv.SpawnPeerSession(ctx, runs.SpawnPeerRequest{
					ProjectID: projectID,
					TaskID:    taskID,
					TaskKind:  "dream",
					Role:      "@dreamer",
					Title:     title,
					Prompt:    prompt,
					ReadOnly:  &ro,
				})
				if err != nil {
					return "", err
				}
				return ps.ID, nil
			}
		}
	}
	ft, props, err := dreamer.RunCycle(r.Context(), b, p, dreamer.RunInput{
		Title:      req.Title,
		Actor:      req.Actor,
		Prompt:     req.Prompt,
		Candidates: req.Candidates,
		Spawn:      spawner,
	})
	if err != nil {
		if errors.Is(err, dreamer.ErrOutOfScope) || errors.Is(err, dreamer.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		s.boardError(w, "running dream cycle", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": ft, "proposals": props})
}

func (s *Server) postDreamDecision(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Actor    string `json:"actor"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Decision), "approve") && !sessionOf(r).fresh {
		s.signIn(w, ReasonFresh, "approving a dream proposal onto main requires a fresh sign-in", nil)
		return
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = board.Operator
	}
	prop, err := dreamer.DecideProposal(r.Context(), b, p, r.PathValue("key"), req.Decision, s.projectHomeDir(p.ID), actor)
	if errors.Is(err, dreamer.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if errors.Is(err, dreamer.ErrBlocked) {
		writeError(w, http.StatusUnprocessableEntity, "FAILED_PRECONDITION", err.Error())
		return
	}
	if errors.Is(err, dreamer.ErrOutOfScope) || errors.Is(err, dreamer.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "deciding dream proposal", err)
		return
	}
	writeJSON(w, http.StatusOK, prop)
}

func (s *Server) postSkillVet(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var in skills.RegisterInput
	if !decode(w, r, &in) {
		return
	}
	in.SourceProjectID = p.ID
	entry, report, err := skills.Register(r.Context(), b.Pool, in)
	if err != nil {
		if errors.Is(err, skills.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		s.boardError(w, "vetting skill", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": entry, "report": report})
}

func (s *Server) postSkillInstall(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Skill string `json:"skill"`
		Actor string `json:"actor"`
	}
	if !decode(w, r, &req) {
		return
	}
	entry, ft, err := skills.Install(r.Context(), b, p, req.Skill, s.projectHomeDir(p.ID), req.Actor)
	if errors.Is(err, skills.ErrBlocked) {
		writeError(w, http.StatusUnprocessableEntity, "FAILED_PRECONDITION", err.Error())
		return
	}
	if errors.Is(err, skills.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "installing skill", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": entry, "task": ft})
}

func (s *Server) getCrossKnowledge(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	entries, err := knowledge.Search(r.Context(), pool, q, kind, 50)
	if err != nil {
		s.boardError(w, "searching knowledge", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) postCrossKnowledgePromote(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	var in knowledge.PromoteInput
	if !decode(w, r, &in) {
		return
	}
	entry, err := knowledge.Promote(r.Context(), pool, in)
	if errors.Is(err, knowledge.ErrInvalid) || errors.Is(err, skills.ErrBlocked) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "promoting knowledge entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) postCrossKnowledgeAct(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var in knowledge.ActInput
	if !decode(w, r, &in) {
		return
	}
	in.EntryKey = r.PathValue("key")
	entry, ft, tk, err := knowledge.ActOnEntry(r.Context(), b, p, in)
	if errors.Is(err, knowledge.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if errors.Is(err, knowledge.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "acting on knowledge entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": entry, "task": ft, "ticket": tk})
}
