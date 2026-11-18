package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/rooms"
)

func (s *Server) roomRoutes(api *routes) {
	api.HandleFunc("GET /api/rooms", s.listRooms)
	api.HandleFunc("GET /api/projects/{p}/room", s.getProjectRoom)
	api.HandleFunc("GET /api/projects/{p}/room/threads", s.listRoomThreads)
	api.HandleFunc("POST /api/projects/{p}/room/threads", s.createRoomThread)
	api.HandleFunc("GET /api/projects/{p}/room/threads/{thread...}", s.getRoomThread)
	api.HandleFunc("GET /api/projects/{p}/room/messages", s.listRoomMessages)
	api.HandleFunc("POST /api/projects/{p}/room/messages", s.postRoomMessage)
	api.HandleFunc("POST /api/projects/{p}/room/choices/{key}/respond", s.respondRoomChoice)
}

func (s *Server) roomService(w http.ResponseWriter, r *http.Request) (*rooms.Service, *board.Board, bool) {
	b := s.Board.Load()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "board is not ready")
		return nil, nil, false
	}
	if rm := s.Rooms.Load(); rm != nil {
		return rm, b, true
	}
	pool := s.Pool.Load()
	if pool == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "database is not ready")
		return nil, nil, false
	}
	rm := &rooms.Service{Pool: pool, Board: b, Log: s.Log}
	rm.AttachBoard(b)
	return rm, b, true
}

func (s *Server) roomError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, rooms.ErrCrossProjectBoundary):
		writeError(w, http.StatusForbidden, "PERMISSION_DENIED", err.Error())
	case errors.Is(err, rooms.ErrStaleChoiceVersion):
		writeError(w, http.StatusConflict, "STALE_CHOICE_VERSION", err.Error())
	case errors.Is(err, rooms.ErrMaxAgentHops):
		writeError(w, http.StatusConflict, "MAX_AGENT_HOPS", err.Error())
	case errors.Is(err, rooms.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	case strings.Contains(err.Error(), "not found"):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	default:
		s.boardError(w, what, err)
	}
}

func parseLimitQuery(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *Server) listRooms(w http.ResponseWriter, r *http.Request) {
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	list, err := rm.ListRooms(r.Context())
	if err != nil {
		s.boardError(w, "listing project rooms", err)
		return
	}
	if list == nil {
		list = []rooms.Room{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": list})
}

func (s *Server) getProjectRoom(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	view, err := rm.GetRoomView(r.Context(), p, parseLimitQuery(r))
	if err != nil {
		s.roomError(w, "reading project room", err)
		return
	}
	if view.Threads == nil {
		view.Threads = []rooms.Thread{}
	}
	if view.Messages == nil {
		view.Messages = []rooms.Message{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) listRoomThreads(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	list, err := rm.ListThreads(r.Context(), p)
	if err != nil {
		s.roomError(w, "listing feature threads", err)
		return
	}
	if list == nil {
		list = []rooms.Thread{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": list})
}

func (s *Server) createRoomThread(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	var req struct {
		ThreadKey string `json:"threadKey"`
		Title     string `json:"title"`
		Spec      string `json:"spec"`
	}
	if !decode(w, r, &req) {
		return
	}
	room, thread, err := rm.EnsureThread(r.Context(), p, req.ThreadKey, req.Title, req.Spec)
	if err != nil {
		s.roomError(w, "creating feature thread", err)
		return
	}
	s.changed(w, b, http.StatusCreated, map[string]any{"room": room, "thread": thread})
}

func (s *Server) getRoomThread(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	view, err := rm.GetThreadView(r.Context(), p, r.PathValue("thread"), parseLimitQuery(r))
	if err != nil {
		s.roomError(w, "reading feature thread", err)
		return
	}
	if view.Messages == nil {
		view.Messages = []rooms.Message{}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) listRoomMessages(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fa := strings.EqualFold(q.Get("forAgent"), "true") || q.Get("forAgent") == "1"
	if rawMsgID := strings.TrimSpace(q.Get("messageId")); rawMsgID != "" {
		msgID, err := strconv.ParseInt(rawMsgID, 10, 64)
		if err != nil || msgID <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "messageId must be a positive integer")
			return
		}
		msg, err := rm.ReadMessageByID(r.Context(), p, msgID, fa)
		if err != nil {
			s.roomError(w, "reading room message", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": msg})
		return
	}
	var afterID int64
	if raw := strings.TrimSpace(q.Get("afterId")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			afterID = n
		}
	}
	msgs, err := rm.ReadMessages(r.Context(), p, q.Get("threadKey"), afterID, parseLimitQuery(r), fa)
	if err != nil {
		s.roomError(w, "reading room messages", err)
		return
	}
	if msgs == nil {
		msgs = []rooms.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) postRoomMessage(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	var req struct {
		ThreadKey    string                  `json:"threadKey"`
		Spec         string                  `json:"spec"`
		Ticket       string                  `json:"ticket"`
		SenderRole   string                  `json:"senderRole"`
		RunID        string                  `json:"runId"`
		Kind         string                  `json:"kind"`
		Intent       string                  `json:"intent"`
		EventType    string                  `json:"eventType"`
		ReplyToID    int64                   `json:"replyToId"`
		To           []string                `json:"to"`
		Interrupt    bool                    `json:"interrupt"`
		Metadata     map[string]any          `json:"metadata"`
		ChoicePrompt *rooms.ChoicePromptSpec `json:"choicePrompt"`
		Body         string                  `json:"body"`
		Actionable   *bool                   `json:"actionable"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := rm.PostMessage(r.Context(), p, rooms.PostInput{
		ThreadKey:    req.ThreadKey,
		SpecRef:      req.Spec,
		TicketKey:    req.Ticket,
		SenderRole:   req.SenderRole,
		RunID:        req.RunID,
		Kind:         req.Kind,
		Intent:       req.Intent,
		EventType:    req.EventType,
		ReplyToID:    req.ReplyToID,
		To:           req.To,
		Interrupt:    req.Interrupt,
		Metadata:     req.Metadata,
		ChoicePrompt: req.ChoicePrompt,
		Body:         req.Body,
		Actionable:   req.Actionable,
	})
	if err != nil {
		s.roomError(w, "posting room message", err)
		return
	}
	if pipe := s.Pipeline.Load(); pipe != nil && res.SpawnedTask != nil {
		pipe.Kick()
	}
	if pl := s.Planner.Load(); pl != nil && res.SpawnedTask != nil && res.SpawnedTask.Role == "@planner" {
		pl.Kick()
	}
	s.changed(w, b, http.StatusCreated, res)
}

func (s *Server) respondRoomChoice(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	rm, _, ok := s.roomService(w, r)
	if !ok {
		return
	}
	var req struct {
		PromptVersion   int    `json:"promptVersion"`
		Choice          string `json:"choice"`
		SenderRole      string `json:"senderRole"`
		RunID           string `json:"runId"`
		SourceMessageID int64  `json:"sourceMessageId"`
		Rationale       string `json:"rationale"`
	}
	if !decode(w, r, &req) {
		return
	}
	role := strings.TrimSpace(req.SenderRole)
	if role == "" {
		role = "user"
	}
	res, dec, err := rm.RespondChoicePrompt(r.Context(), p, rooms.RespondChoiceInput{
		PromptKey:       r.PathValue("key"),
		PromptVersion:   req.PromptVersion,
		Choice:          req.Choice,
		SenderRole:      role,
		RunID:           req.RunID,
		SourceMessageID: req.SourceMessageID,
		Rationale:       req.Rationale,
	})
	if err != nil {
		s.roomError(w, "responding to choice prompt", err)
		return
	}
	if pipe := s.Pipeline.Load(); pipe != nil {
		pipe.Kick()
	}
	s.changed(w, b, http.StatusCreated, map[string]any{"decision": dec, "post": res})
}
