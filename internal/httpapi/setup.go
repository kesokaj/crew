package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/kesokaj/crew/internal/setup"
)

// Setup's routes (design 12.6, 20.1). Every credential is set up once, for the machine.
func (s *Server) setupRoutes(api *routes) {
	api.HandleFunc("GET /api/setup", s.setupStatus)
	api.HandleFunc("GET /api/setup/terminal", s.setupTerminal)
	api.HandleFunc("POST /api/setup/github-app", s.setupGitHubApp)
	api.HandleFunc("POST /api/setup/close", s.setupClose)
}

// setupService returns Setup, or answers 503 while the server cannot do it yet: no database, or
// no key file, or no Docker.
func (s *Server) setupService(w http.ResponseWriter) *setup.Service {
	sv := s.Setup.Load()
	if sv == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Setup is not available: the database, the key file, or Docker is missing (see the server's log)")
	}
	return sv
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	sv := s.setupService(w)
	if sv == nil {
		return
	}
	st, err := sv.Status(r.Context())
	if err != nil {
		s.Log.Error("reading Setup", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Setup cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) setupTerminal(w http.ResponseWriter, r *http.Request) {
	sv := s.setupService(w)
	if sv == nil {
		return
	}
	sv.Terminal(s.Hostname).ServeHTTP(w, r)
}

// The key file of a GitHub App is a few KB.
const maxAppBody = 64 << 10

func (s *Server) setupGitHubApp(w http.ResponseWriter, r *http.Request) {
	sv := s.setupService(w)
	if sv == nil {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAppBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "that is larger than a key file")
		return
	}
	var in struct {
		AppID string `json:"appId"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "send {\"appId\", \"key\"}")
		return
	}
	info, err := sv.SetGitHubApp(r.Context(), in.AppID, in.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) setupClose(w http.ResponseWriter, r *http.Request) {
	sv := s.setupService(w)
	if sv == nil {
		return
	}
	if err := sv.Close(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "NOT_REMOVED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
