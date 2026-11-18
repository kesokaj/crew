package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"

	"github.com/kesokaj/crew/internal/hosting"
)

// Hosting a project on the machine (design 19).
func (s *Server) hostingRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/hosting", s.hostingView)
	api.HandleFunc("PUT /api/projects/{p}/hosting/settings", s.setHostedValues)
	api.HandleFunc("POST /api/projects/{p}/hosting/deploys", s.hostDeploy)
	api.HandleFunc("POST /api/projects/{p}/hosting/deploys/{d}/rollback", s.hostRollBack)
	api.HandleFunc("DELETE /api/projects/{p}/hosting", s.hostStop)
	api.HandleFunc("GET /api/projects/{p}/hosting/logs", s.hostLogs)
}

// hostingError answers what the hosting service refused, or failed at.
func (s *Server) hostingError(w http.ResponseWriter, what string, err error) {
	var pe *hosting.PreconditionError
	var ie *hosting.InputError
	switch {
	case errors.As(err, &pe):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", pe.Msg)
	case errors.As(err, &ie):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ie.Msg)
	default:
		s.Log.Error(what, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", what+" failed")
	}
}

// hostDeploy is Host locally and Deploy again (19.2, 19.5): main's head, built and checked beside what
// serves. With no Dockerfile on main, the ticket that makes the project hostable, once (plan M19 D14).
func (s *Server) hostDeploy(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	d, err := h.Deploy(r.Context(), p.ID, hosting.Ask{Why: "asked", By: "operator"})
	var nd *hosting.NoDockerfileError
	if errors.As(err, &nd) {
		key, err := h.Hostable(r.Context(), p.ID)
		if err != nil {
			s.hostingError(w, "making the project hostable", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"state": "waiting_dockerfile", "ticket": key})
		return
	}
	if err != nil {
		s.hostingError(w, "a deploy", err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

// hostRollBack switches back to a kept deploy, with the data as it left it when asked (19.5, plan M19
// D15), behind a fresh sign-in.
func (s *Server) hostRollBack(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	n, err := strconv.Atoi(r.PathValue("d"))
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a deploy is named by its number")
		return
	}
	var in struct {
		RestoreData bool `json:"restoreData"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := h.Deploy(r.Context(), p.ID, hosting.Ask{Why: "rollback", By: "operator", From: n, RestoreData: in.RestoreData})
	if err != nil {
		s.hostingError(w, "a roll back", err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

// hostStop is Stop hosting (19.5): the address stops answering; the data is kept.
func (s *Server) hostStop(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	if err := h.Stop(r.Context(), p.ID); err != nil {
		s.hostingError(w, "stopping the hosting", err)
		return
	}
	s.hostingView(w, r)
}

// hostLogs are the newest lines of what the hosted project printed (plan M19 D17): ?lines= 1..2000.
func (s *Server) hostLogs(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	n := 500
	if q := r.URL.Query().Get("lines"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 1 || v > hosting.MaxLogLines {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "lines is from 1 to 2000")
			return
		}
		n = v
	}
	lines, err := h.Logs(p.ID, n)
	if err != nil {
		s.hostingError(w, "reading the logs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// hostingOf returns the hosting service, or answers 503 while it is not there yet.
func (s *Server) hostingOf(w http.ResponseWriter) *hosting.Service {
	h := s.Hosting.Load()
	if h == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "hosting is not available yet: the database or the key file is not there")
	}
	return h
}

func (s *Server) hostingView(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	v, err := h.View(r.Context(), p.ID, p.Name)
	if err != nil {
		s.Log.Error("reading the hosting", "project", p.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the hosting cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setHostedValues stores the settings and secrets a hosted project gets, the whole set, behind a
// fresh sign-in (20.3, plan M19 D12): no value is in an answer or a log line.
func (s *Server) setHostedValues(w http.ResponseWriter, r *http.Request) {
	h := s.hostingOf(w)
	if h == nil {
		return
	}
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	var in struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	// On a live project the values reach the app with a settings deploy of its image (plan M19 D21).
	err := h.SetValuesAndApply(r.Context(), p.ID, in.Values, r.Header.Get(HeaderEmail))
	var pe *hosting.PreconditionError
	var ie *hosting.InputError
	switch {
	case errors.As(err, &ie):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ie.Msg)
		return
	case errors.As(err, &pe):
		// Stored; the deploy that runs began with the values before, and the next one has these.
		s.Log.Info("a hosted project's values are stored for its next deploy", "project", p.Name, "why", pe.Msg)
	case err != nil:
		s.Log.Error("storing a hosted project's values", "project", p.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the values cannot be stored")
		return
	}
	s.hostingView(w, r)
}

// ingressRoutes is the ingress's door (19.4, plan M19 D5): the hosted routes, for the ingress's own
// call alone, which carries the shared token and no identity header; a request the ingress passes on
// always carries one. Anything else, and the door from a sandbox's bridge, is not found.
func (s *Server) ingressRoutes(w http.ResponseWriter, r *http.Request) {
	got := []byte(r.Header.Get(HeaderIngress))
	if len(s.IngressToken) == 0 || subtle.ConstantTimeCompare(got, s.IngressToken) != 1 || len(r.Header.Values(HeaderEmail)) > 0 {
		http.NotFound(w, r)
		return
	}
	h := s.Hosting.Load()
	if h == nil {
		writeJSON(w, http.StatusOK, map[string]any{"routes": []hosting.Route{}})
		return
	}
	routes, err := h.Routes(r.Context())
	if err != nil {
		s.Log.Error("reading the hosted routes", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the hosted routes cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// hostingBefore is a project's hosting settings before a change of its settings, for hostingAfter.
func (s *Server) hostingBefore(ctx context.Context, projectID string) (hosting.Settings, bool) {
	h := s.Hosting.Load()
	if h == nil {
		return hosting.Settings{}, false
	}
	st, err := h.Snapshot(ctx, projectID)
	return st, err == nil
}

// hostingAfter starts a settings deploy when a change of a live project's settings asks for one (plan
// M19 D21). The settings are stored whatever it says.
func (s *Server) hostingAfter(ctx context.Context, projectID string, before hosting.Settings, ok bool) {
	h := s.Hosting.Load()
	if h == nil || !ok {
		return
	}
	if err := h.Changed(ctx, projectID, before); err != nil {
		s.Log.Warn("the hosted project's settings apply at its next deploy", "project", projectID, "why", err)
	}
}

// hostingArchived stops hosting an archived project (plan M19, Stop); one not hosted is no error.
func (s *Server) hostingArchived(ctx context.Context, projectID string) {
	h := s.Hosting.Load()
	if h == nil {
		return
	}
	var pe *hosting.PreconditionError
	if err := h.Stop(ctx, projectID); err != nil && !errors.As(err, &pe) {
		s.Log.Error("stopping the hosting of an archived project", "project", projectID, "error", err)
	}
}
