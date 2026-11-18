package httpapi

import (
	"net/http"
	"strconv"

	"github.com/kesokaj/crew/internal/board"
)

// A project's evals (9.6): its cases, the results of its suites and their runs, a case from a Done
// ticket, a waiting case accepted or its text changed, and a run of the suite on demand.
func (s *Server) evalRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/evals", s.listEvals)
	api.HandleFunc("GET /api/projects/{p}/evals/runs", s.listEvalRuns)
	api.HandleFunc("POST /api/projects/{p}/evals/cases", s.addCase)
	api.HandleFunc("PUT /api/projects/{p}/evals/cases/{n}", s.editCase)
	api.HandleFunc("POST /api/projects/{p}/evals/cases/{n}/accept", s.acceptCase)
	api.HandleFunc("POST /api/projects/{p}/evals/cases/{n}/retire", s.retireCase)
	api.HandleFunc("POST /api/projects/{p}/evals/run", s.runSuite)
}

// listEvalRuns lists the eval runs of one suite (?suite=N), with the kept output of their failed
// held-out checks: for the operator, never for an agent (9.6).
func (s *Server) listEvalRuns(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	suite, err := strconv.ParseInt(r.URL.Query().Get("suite"), 10, 64)
	if err != nil || suite < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "suite: name the suite by its number")
		return
	}
	list, err := b.EvalRuns(r.Context(), p, suite)
	if err != nil {
		s.boardError(w, "reading the eval runs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": list})
}

// acceptCase makes a waiting case active on the operator's word, whatever its fit check found.
func (s *Server) acceptCase(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "case")
	if !ok {
		return
	}
	c, err := b.AcceptCase(r.Context(), p, n, board.Operator)
	if err != nil {
		s.boardError(w, "accepting the eval case", err)
		return
	}
	s.changed(w, b, http.StatusOK, c)
}

// editCase changes a waiting case's text; its fit check runs again on the new text.
func (s *Server) editCase(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "case")
	if !ok {
		return
	}
	var req board.CaseText
	if !decode(w, r, &req) {
		return
	}
	c, err := b.EditCase(r.Context(), p, n, req, board.Operator)
	if err != nil {
		s.boardError(w, "changing the eval case", err)
		return
	}
	s.changed(w, b, http.StatusOK, c)
}

func (s *Server) listEvals(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	cases, err := b.Cases(r.Context(), p, false)
	if err != nil {
		s.boardError(w, "reading the eval cases", err)
		return
	}
	suites, err := b.Suites(r.Context(), p, 20, true)
	if err != nil {
		s.boardError(w, "reading the eval suites", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases, "suites": suites})
}

func (s *Server) addCase(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	var req board.NewCase
	if !decode(w, r, &req) {
		return
	}
	c, err := b.ProposeCase(r.Context(), p, req, board.Operator, "")
	if err != nil {
		s.boardError(w, "making the eval case", err)
		return
	}
	s.changed(w, b, http.StatusCreated, c)
}

func (s *Server) retireCase(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	n, ok := number(w, r, "case")
	if !ok {
		return
	}
	var req struct {
		Why string `json:"why"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := b.RetireCase(r.Context(), p, n, req.Why, board.Operator); err != nil {
		s.boardError(w, "retiring the eval case", err)
		return
	}
	s.changed(w, b, http.StatusOK, map[string]any{"retired": n})
}

// runSuite queues a run of the project's suite; it runs when no other suite runs on the machine.
func (s *Server) runSuite(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	id, err := b.StartSuite(r.Context(), p, "on_demand", 0, 0, board.Operator)
	if err != nil {
		s.boardError(w, "starting the eval suite", err)
		return
	}
	s.changed(w, b, http.StatusAccepted, map[string]any{"suite": id})
}
