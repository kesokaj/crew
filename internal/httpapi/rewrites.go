package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/kesokaj/crew/internal/rewrite"
)

// The prompt writer (design 7.2): a rewrite of what the operator wrote, and the operator's choice.
func (s *Server) rewriteRoutes(api *routes) {
	api.HandleFunc("POST /api/rewrites", s.startRewrite)
	api.HandleFunc("GET /api/rewrites/{id}", s.getRewrite)
	api.HandleFunc("POST /api/rewrites/{id}/choice", s.chooseRewrite)
}

func (s *Server) rewrites(w http.ResponseWriter) *rewrite.Service {
	x := s.Rewrites.Load()
	if x == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the prompt writer is not available: the database or Docker is missing")
	}
	return x
}

func (s *Server) rewriteError(w http.ResponseWriter, doing string, err error) {
	var e *rewrite.Error
	switch {
	case errors.As(err, &e) && e.NotFound:
		writeError(w, http.StatusNotFound, "NOT_FOUND", e.Msg)
	case errors.As(err, &e):
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", e.Msg)
	default:
		s.draftError(w, doing, err)
	}
}

// startRewrite asks the prompt writer for a rewrite. A first message with no draft starts the draft,
// with no message yet: the planning agent waits for the text the operator chooses.
func (s *Server) startRewrite(w http.ResponseWriter, r *http.Request) {
	x := s.rewrites(w)
	if x == nil {
		return
	}
	var req struct {
		rewrite.Request
		Title string `json:"title"`
		Repo  string `json:"repo"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Project != "" {
		_, p := s.projectNamed(w, r, req.Project)
		if p == nil {
			return
		}
		req.Project = p.ID
	}
	// A request that is refused makes no draft.
	if err := rewrite.Check(req.Request); err != nil {
		s.rewriteError(w, "asking for a rewrite", err)
		return
	}
	made := ""
	if req.Kind == rewrite.KindFirst && req.Draft == "" && req.Previous == 0 {
		d := s.drafts(w)
		if d == nil {
			return
		}
		id, err := d.CreateEmpty(r.Context(), req.Title, req.Repo)
		if err != nil {
			s.draftError(w, "starting a draft", err)
			return
		}
		req.Draft, made = id, id
	}
	out, err := x.Start(r.Context(), req.Request)
	if err != nil {
		if d := s.Drafts.Load(); made != "" && d != nil {
			if derr := d.DropEmpty(context.WithoutCancel(r.Context()), made); derr != nil {
				s.Log.Error("removing a draft no rewrite was made for", "draft", made, "error", derr)
			}
		}
		s.rewriteError(w, "asking for a rewrite", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getRewrite(w http.ResponseWriter, r *http.Request) {
	x := s.rewrites(w)
	if x == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such rewrite")
		return
	}
	out, err := x.Get(r.Context(), id)
	if err != nil {
		s.rewriteError(w, "reading the rewrite", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) chooseRewrite(w http.ResponseWriter, r *http.Request) {
	x := s.rewrites(w)
	if x == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such rewrite")
		return
	}
	var req struct {
		Choice string `json:"choice"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	out, err := x.Choose(r.Context(), id, req.Choice, req.Text)
	if err != nil {
		s.rewriteError(w, "recording the choice", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
