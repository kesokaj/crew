package httpapi

import (
	"net/http"

	"github.com/kesokaj/crew/internal/board"
)

func (s *Server) workflowRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/workflow", s.getWorkflow)
	api.HandleFunc("POST /api/projects/{p}/workflow/sync", s.syncWorkflow)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/yaml", s.getTicketYAML)
	api.HandleFunc("POST /api/projects/{p}/tickets/sync", s.syncTicketsYAML)
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	wf, err := b.GetWorkflow(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading workflow", err)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) syncWorkflow(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	if _, err := b.SyncMainWorkflow(r.Context(), b.Pool, p); err != nil {
		s.boardError(w, "syncing workflow from main", err)
		return
	}
	wf, err := b.GetWorkflow(r.Context(), b.Pool, p)
	if err != nil {
		s.boardError(w, "reading synced workflow", err)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) getTicketYAML(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ref := r.PathValue("t")
	tk, err := b.Ticket(r.Context(), p, ref)
	if err != nil {
		s.boardError(w, "reading ticket", err)
		return
	}
	raw, err := b.FormatTicketYAML(r.Context(), p, ref)
	if err != nil {
		s.boardError(w, "formatting ticket yaml", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":  tk.Key,
		"path": ".crew/tickets/" + tk.Key + ".md",
		"yaml": string(raw),
	})
}

func (s *Server) syncTicketsYAML(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	added, updated, err := b.SyncTicketsFromMain(r.Context(), p, board.Operator)
	if err != nil {
		s.boardError(w, "syncing .crew/tickets/*.md from main", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added":   added,
		"updated": updated,
	})
}
