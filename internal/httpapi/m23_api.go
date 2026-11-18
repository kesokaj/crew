package httpapi

import (
	"net/http"
	"strings"

	"github.com/kesokaj/crew/internal/board"
)

func (s *Server) m23Routes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/specs", s.listSpecs)
	api.HandleFunc("POST /api/projects/{p}/specs", s.writeFeatureSpec)
	api.HandleFunc("GET /api/projects/{p}/specs/{spec}", s.getSpec)
	api.HandleFunc("POST /api/projects/{p}/specs/ask", s.askPlanner)
	api.HandleFunc("POST /api/projects/{p}/specs/{spec}/decide", s.decideSpec)
	api.HandleFunc("POST /api/projects/{p}/specs/{spec}/decision", s.operatorSpecDecision)
	api.HandleFunc("GET /api/projects/{p}/spec-decisions", s.listSpecDecisions)
}

func (s *Server) listSpecs(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.ListSpecs(r.Context(), p)
	if err != nil {
		s.boardError(w, "listing specs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"specs": list})
}

func (s *Server) getSpec(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	sp, err := b.GetSpec(r.Context(), p, r.PathValue("spec"))
	if err != nil {
		s.boardError(w, "reading spec", err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) writeFeatureSpec(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Ref              string   `json:"ref"`
		Title            string   `json:"title"`
		Slug             string   `json:"slug"`
		BodyMarkdown     string   `json:"bodyMarkdown"`
		TouchedPaths     []string `json:"touchedPaths"`
		AltersFoundation bool     `json:"altersFoundation"`
		ThreadRef        string   `json:"threadRef"`
	}
	if !decode(w, r, &req) {
		return
	}
	sp, err := b.WriteFeatureSpec(r.Context(), p, board.EnsureSpecInput{
		Ref:              req.Ref,
		Title:            req.Title,
		Slug:             req.Slug,
		BodyMarkdown:     req.BodyMarkdown,
		TouchedPaths:     req.TouchedPaths,
		AltersFoundation: req.AltersFoundation,
		ThreadRef:        req.ThreadRef,
		CreatedBy:        board.Operator,
	})
	if err != nil {
		s.boardError(w, "writing feature spec", err)
		return
	}
	writeJSON(w, http.StatusCreated, sp)
}

func (s *Server) askPlanner(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Spec             string   `json:"spec"`
		Title            string   `json:"title"`
		Slug             string   `json:"slug"`
		Ticket           string   `json:"ticket"`
		AskingRole       string   `json:"askingRole"`
		AskingRun        string   `json:"askingRun"`
		Question         string   `json:"question"`
		Context          string   `json:"context"`
		TouchedPaths     []string `json:"touchedPaths"`
		AltersFoundation bool     `json:"altersFoundation"`
	}
	if !decode(w, r, &req) {
		return
	}
	sp, q, err := b.AskPlanner(r.Context(), p, board.AskPlannerInput{
		SpecRef:          req.Spec,
		Title:            req.Title,
		Slug:             req.Slug,
		TicketKey:        req.Ticket,
		AskingRole:       req.AskingRole,
		AskingRun:        req.AskingRun,
		Question:         req.Question,
		ContextNote:      req.Context,
		TouchedPaths:     req.TouchedPaths,
		AltersFoundation: req.AltersFoundation,
	})
	if err != nil {
		s.boardError(w, "asking @planner on feature spec", err)
		return
	}
	if pl := s.Planner.Load(); pl != nil {
		pl.Kick()
	}
	s.changed(w, b, http.StatusCreated, map[string]any{"spec": sp, "question": q})
}

func (s *Server) decideSpec(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		QuestionID       int64             `json:"questionId"`
		AskingRole       string            `json:"askingRole"`
		DecidedByRole    string            `json:"decidedByRole"`
		DecidedByRun     string            `json:"decidedByRun"`
		BodyMarkdown     string            `json:"bodyMarkdown"`
		Question         string            `json:"question"`
		Choice           string            `json:"choice"`
		Alternatives     []string          `json:"alternatives"`
		Rationale        string            `json:"rationale"`
		TouchedPaths     []string          `json:"touchedPaths"`
		AltersFoundation bool              `json:"altersFoundation"`
		Tickets          []board.NewTicket `json:"tickets"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := b.DecideSpec(r.Context(), p, board.DecideSpecInput{
		SpecRef:          r.PathValue("spec"),
		QuestionID:       req.QuestionID,
		AskingRole:       req.AskingRole,
		DecidedByRole:    req.DecidedByRole,
		DecidedByRun:     req.DecidedByRun,
		BodyMarkdown:     req.BodyMarkdown,
		Question:         req.Question,
		Choice:           req.Choice,
		Alternatives:     req.Alternatives,
		Rationale:        req.Rationale,
		TouchedPaths:     req.TouchedPaths,
		AltersFoundation: req.AltersFoundation,
		Tickets:          req.Tickets,
	})
	if err != nil {
		s.boardError(w, "deciding feature spec", err)
		return
	}
	if pipe := s.Pipeline.Load(); pipe != nil {
		pipe.Kick()
	}
	if pl := s.Planner.Load(); pl != nil {
		pl.Kick()
	}
	s.changed(w, b, http.StatusOK, res)
}

func (s *Server) operatorSpecDecision(w http.ResponseWriter, r *http.Request) {
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
	if strings.EqualFold(strings.TrimSpace(req.Choice), "approve") {
		sp, err := b.GetSpec(r.Context(), p, r.PathValue("spec"))
		if err == nil && len(board.ProtectedPaths138(sp.TouchedPaths)) > 0 && !sessionOf(r).fresh {
			s.signIn(w, ReasonFresh, "approving a feature spec that touches 13.8 instruction or code files needs a fresh sign-in", nil)
			return
		}
	}
	res, err := b.OperatorDecideSpec(r.Context(), p, r.PathValue("spec"), req.Choice, req.Reason, board.Operator)
	if err != nil {
		s.boardError(w, "deciding escalated feature spec", err)
		return
	}
	if pipe := s.Pipeline.Load(); pipe != nil {
		pipe.Kick()
	}
	s.changed(w, b, http.StatusOK, res)
}

func (s *Server) listSpecDecisions(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	list, err := b.ListSpecDecisions(r.Context(), p)
	if err != nil {
		s.boardError(w, "listing spec decisions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": list})
}
