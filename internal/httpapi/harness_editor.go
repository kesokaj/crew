package httpapi

import (
	"net/http"
	"strings"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/version"
)

func (s *Server) getGlobalBaselineFile(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	pth := strings.TrimSpace(r.URL.Query().Get("path"))
	det, err := board.ReadGlobalHarnessFile(r.Context(), pool, pth)
	if err != nil {
		s.boardError(w, "reading the global file", err)
		return
	}
	writeJSON(w, http.StatusOK, det)
}

func (s *Server) saveGlobalBaselineFile(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	det, err := board.SaveGlobalHarnessFile(r.Context(), pool, strings.TrimSpace(req.Path), req.Content, board.Operator)
	if err != nil {
		s.boardError(w, "saving the global file", err)
		return
	}
	s.syncGlobalSkillsAllHomes(r.Context())
	writeJSON(w, http.StatusOK, det)
}

func (s *Server) deleteGlobalBaselineFile(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	pth := strings.TrimSpace(r.URL.Query().Get("path"))
	if err := board.DeleteGlobalHarnessFile(r.Context(), pool, pth); err != nil {
		s.boardError(w, "resetting the global file", err)
		return
	}
	s.syncGlobalSkillsAllHomes(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"reset": true, "path": pth})
}

func (s *Server) getProjectSkillFile(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	pth := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel, ok := strings.CutPrefix(pth, "baseline/"); ok {
		txt, src, err := board.BaselineText(r.Context(), b.Pool, p.ID, rel, version.Version)
		if err != nil {
			s.boardError(w, "reading the baseline file", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": pth, "content": txt, "source": src})
		return
	}
	base := r.URL.Query().Get("base") == "1" || strings.HasPrefix(pth, "harness/")
	content, err := b.ReadSkillFile(r.Context(), p, pth, base)
	if err != nil {
		s.boardError(w, "reading the skill file", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": pth, "content": content, "base": base})
}

func (s *Server) saveProjectSkillFile(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	if b.Identity == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	id, err := b.Identity(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	commit, err := b.SaveSkillFile(r.Context(), p, strings.TrimSpace(req.Path), req.Content, id)
	if err != nil {
		s.boardError(w, "saving the skill file", err)
		return
	}
	s.syncProjectSkillsHome(r.Context(), b, p, "")
	s.changed(w, b, http.StatusOK, map[string]any{"main": commit, "path": strings.TrimSpace(req.Path)})
}

func (s *Server) deleteProjectSkillFile(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	if b.Identity == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	id, err := b.Identity(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the git identity is not set up: set it up in Setup")
		return
	}
	pth := strings.TrimSpace(r.URL.Query().Get("path"))
	commit, err := b.DeleteSkillFile(r.Context(), p, pth, id)
	if err != nil {
		s.boardError(w, "removing the skill file", err)
		return
	}
	s.syncProjectSkillsHome(r.Context(), b, p, pth)
	s.changed(w, b, http.StatusOK, map[string]any{"main": commit, "path": pth})
}

func (s *Server) requestProjectSkill(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req board.SkillRequest
	if !decode(w, r, &req) {
		return
	}
	t, joined, err := b.RequestSkill(r.Context(), p, req, "", board.Operator, board.Operator)
	if err != nil {
		s.boardError(w, "requesting a skill", err)
		return
	}
	s.changed(w, b, http.StatusCreated, map[string]any{"ticket": t.Key, "title": t.Title, "joined": joined})
}
