package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kesokaj/crew/internal/assistant"
)

// The conversations of the operator and the chats of the tickets (11.8, 11.9; plan M20). Every route
// is plain: a turn is a run of an agent, as a plan draft's message is (D15).
func (s *Server) conversationRoutes(api *routes) {
	api.HandleFunc("GET /api/conversations", s.listConversations)
	api.HandleFunc("POST /api/conversations", s.startConversation)
	api.HandleFunc("GET /api/conversations/{id}", s.getConversation)
	api.HandleFunc("POST /api/conversations/{id}/messages", s.conversationMessage)
	api.HandleFunc("POST /api/conversations/{id}/pick", s.pickConversation)
	api.HandleFunc("POST /api/conversations/{id}/save", s.saveConversation)
	api.HandleFunc("POST /api/conversations/{id}/close", s.closeConversation)
	api.HandleFunc("DELETE /api/conversations/{id}", s.deleteConversation)
	api.HandleFunc("POST /api/conversations/{id}/ticket", s.conversationTicket)
	api.HandleFunc("POST /api/conversations/{id}/tell", s.conversationTell)
	api.HandleFunc("POST /api/conversations/{id}/remember", s.conversationRemember)
	api.HandleFunc("POST /api/conversations/{id}/plan", s.conversationPlan)
	api.HandleFunc("GET /api/conversations/{id}/files", s.conversationFiles)
	api.HandleFunc("GET /api/conversations/{id}/preview/{path...}", s.conversationPreview)
	api.HandleFunc("POST /api/conversations/{id}/branch", s.conversationBranch)
	api.HandleFunc("GET /api/projects/{p}/tasks/{t}/chat", s.getChat)
	api.HandleFunc("POST /api/projects/{p}/tasks/{t}/chat", s.chatMessage)
}

func (s *Server) assistant(w http.ResponseWriter) *assistant.Service {
	a := s.Assistant.Load()
	if a == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "conversations are not available: the runs cannot start yet")
	}
	return a
}

// conversationError answers the service's refusals, and the board's through boardError.
func (s *Server) conversationError(w http.ResponseWriter, doing string, err error) {
	msg := func() string {
		_, m, _ := strings.Cut(err.Error(), ": ")
		return m
	}
	switch {
	case errors.Is(err, assistant.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such conversation")
	case errors.Is(err, assistant.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", msg())
	case errors.Is(err, assistant.ErrRefused):
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", msg())
	default:
		s.boardError(w, doing, err)
	}
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	list, err := a.List(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.conversationError(w, "listing the conversations", err)
		return
	}
	if list == nil {
		list = []assistant.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

func (s *Server) startConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var req assistant.Start
	if !decode(w, r, &req) {
		return
	}
	v, err := a.Start(r.Context(), req)
	if err != nil {
		s.conversationError(w, "starting a conversation", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	v, err := a.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.conversationError(w, "reading the conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) conversationMessage(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var m assistant.Message
	if !decode(w, r, &m) {
		return
	}
	id, err := a.Send(r.Context(), r.PathValue("id"), m)
	if err != nil {
		s.conversationError(w, "keeping the message", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

func (s *Server) pickConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var req struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := a.Pick(r.Context(), r.PathValue("id"), req.Model, req.Effort); err != nil {
		s.conversationError(w, "picking the agent", err)
		return
	}
	s.getConversation(w, r)
}

func (s *Server) saveConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var req struct {
		Saved *bool `json:"saved"`
	}
	if !decode(w, r, &req) {
		return
	}
	saved := true
	if req.Saved != nil {
		saved = *req.Saved
	}
	v, err := a.Save(r.Context(), r.PathValue("id"), saved)
	if err != nil {
		s.conversationError(w, "saving the conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) closeConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	if err := a.Close(r.Context(), r.PathValue("id")); err != nil {
		s.conversationError(w, "closing the conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	if err := a.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.conversationError(w, "deleting the conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) conversationTicket(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var f assistant.TicketForm
	if !decode(w, r, &f) {
		return
	}
	t, err := a.MakeTicket(r.Context(), r.PathValue("id"), f)
	if err != nil {
		s.conversationError(w, "making the ticket", err)
		return
	}
	s.kickBoard()
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) conversationTell(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var f assistant.TellForm
	if !decode(w, r, &f) {
		return
	}
	c, err := a.Tell(r.Context(), r.PathValue("id"), f)
	if err != nil {
		s.conversationError(w, "telling the agent", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) conversationRemember(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var f assistant.RememberForm
	if !decode(w, r, &f) {
		return
	}
	e, err := a.Remember(r.Context(), r.PathValue("id"), f)
	if err != nil {
		s.conversationError(w, "keeping the entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) conversationPlan(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var req struct{}
	if !decode(w, r, &req) {
		return
	}
	draft, err := a.Plan(r.Context(), r.PathValue("id"))
	if err != nil {
		s.conversationError(w, "starting the plan", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"draft": draft})
}

func (s *Server) conversationFiles(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	files, err := a.Files(r.Context(), r.PathValue("id"))
	if err != nil {
		s.conversationError(w, "reading the work folder", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) conversationPreview(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	data, ct, err := a.File(r.Context(), r.PathValue("id"), r.PathValue("path"))
	if err != nil {
		s.conversationError(w, "reading the file", err)
		return
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) conversationBranch(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	var req struct {
		Project string `json:"project"`
	}
	if !decode(w, r, &req) {
		return
	}
	proj, branch, commit, err := a.PushBranch(r.Context(), r.PathValue("id"), req.Project)
	if err != nil {
		s.conversationError(w, "pushing the work folder to a branch", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": proj, "branch": branch, "commit": commit})
}

// getChat is a ticket's chat, or an empty one before its first message (D30).
func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	v, err := a.ChatOf(r.Context(), p, r.PathValue("t"), false)
	if err != nil {
		s.conversationError(w, "reading the chat", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// chatMessage is a question in a ticket's chat: the chat is made by its first message (D30).
func (s *Server) chatMessage(w http.ResponseWriter, r *http.Request) {
	a := s.assistant(w)
	if a == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	var m assistant.Message
	if !decode(w, r, &m) {
		return
	}
	conv, id, err := a.Ask(r.Context(), p, r.PathValue("t"), m)
	if err != nil {
		s.conversationError(w, "keeping the question", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"conversation": conv, "id": id})
}

// kickBoard asks the board and the pipeline to look again after a ticket was made.
func (s *Server) kickBoard() {
	if b := s.Board.Load(); b != nil {
		b.Kick()
	}
}
