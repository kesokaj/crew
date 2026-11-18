package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kesokaj/crew/internal/mcp"
)

// The operator's personal tokens for their own tools (11.7): made here while signed in and shown
// once, listed with their last use, switched to answer, renewed, and revoked. Never through the MCP.
func (s *Server) tokenRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/mcp/tokens", s.listTokens)
	api.HandleFunc("POST /api/crew/mcp/tokens", s.createToken)
	api.HandleFunc("PATCH /api/crew/mcp/tokens/{id}", s.changeToken)
	api.HandleFunc("POST /api/crew/mcp/tokens/{id}/renew", s.renewToken)
	api.HandleFunc("POST /api/crew/mcp/tokens/{id}/revoke", s.revokeToken)
}

func (s *Server) tokens(w http.ResponseWriter) *mcp.Tokens {
	t := s.Tokens.Load()
	if t == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the database is not ready")
	}
	return t
}

// tokenError answers a refusal of the token store as a bad request, and anything else as Crew's own.
func (s *Server) tokenError(w http.ResponseWriter, what string, err error) {
	var me *mcp.Error
	if errors.As(err, &me) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", me.Msg)
		return
	}
	s.boardError(w, what, err)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	t := s.tokens(w)
	if t == nil {
		return
	}
	list, err := t.List(r.Context())
	if err != nil {
		s.tokenError(w, "listing the tokens", err)
		return
	}
	if list == nil {
		list = []mcp.PersonalToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	t := s.tokens(w)
	if t == nil {
		return
	}
	var req struct {
		Name      string   `json:"name"`
		Projects  []string `json:"projects"`
		MayAnswer bool     `json:"mayAnswer"`
	}
	if !decode(w, r, &req) {
		return
	}
	plain, pt, err := t.Create(r.Context(), req.Name, req.Projects, req.MayAnswer)
	if err != nil {
		s.tokenError(w, "making the token", err)
		return
	}
	// The token is in this answer and nowhere else: Crew keeps its hash.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "about": pt})
}

func (s *Server) tokenID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such token")
		return 0, false
	}
	return id, true
}

func (s *Server) changeToken(w http.ResponseWriter, r *http.Request) {
	t := s.tokens(w)
	id, ok := s.tokenID(w, r)
	if t == nil || !ok {
		return
	}
	var req struct {
		MayAnswer *bool `json:"mayAnswer"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MayAnswer == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "mayAnswer: true or false")
		return
	}
	pt, err := t.SetAnswer(r.Context(), id, *req.MayAnswer)
	if err != nil {
		s.tokenError(w, "changing the token", err)
		return
	}
	writeJSON(w, http.StatusOK, pt)
}

func (s *Server) renewToken(w http.ResponseWriter, r *http.Request) {
	t := s.tokens(w)
	id, ok := s.tokenID(w, r)
	if t == nil || !ok {
		return
	}
	pt, err := t.Renew(r.Context(), id)
	if err != nil {
		s.tokenError(w, "renewing the token", err)
		return
	}
	writeJSON(w, http.StatusOK, pt)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	t := s.tokens(w)
	id, ok := s.tokenID(w, r)
	if t == nil || !ok {
		return
	}
	pt, err := t.Revoke(r.Context(), id)
	if err != nil {
		s.tokenError(w, "revoking the token", err)
		return
	}
	writeJSON(w, http.StatusOK, pt)
}
