package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// The Versions screen and what it asks (design 16.3 to 16.5, 15.4, versions.png). Every route is the
// operator's, behind the ingress: the Crew MCP has no tool for any of it, and no sandbox reaches it.
func (s *Server) versionRoutes(api *routes) {
	api.HandleFunc("GET /api/crew/versions", s.getVersions)
	api.HandleFunc("GET /api/crew/versions/{v}", s.getVersion)
	api.HandleFunc("POST /api/crew/versions/build", s.buildVersion)
	api.HandleFunc("POST /api/crew/versions/{v}/gate", s.runLiveGate)
	api.HandleFunc("POST /api/crew/versions/{v}/gate/install", s.setGateAutoInstall)
	api.HandleFunc("POST /api/crew/versions/{v}/install", s.installVersion)
	api.HandleFunc("POST /api/crew/versions/{v}/deploy", s.installVersion)
	api.HandleFunc("POST /api/crew/versions/{v}/hold", s.holdVersion)
	api.HandleFunc("DELETE /api/crew/versions/{v}/hold", s.holdVersion)
	api.HandleFunc("GET /api/crew/updater", s.getVersions)
	api.HandleFunc("POST /api/crew/updater/stop", s.stopUpdater)
}

func (s *Server) versions(w http.ResponseWriter) *versions.Service {
	v := s.Versions.Load()
	if v == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "releases are not set up on this server yet")
	}
	return v
}

func (s *Server) getVersions(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	screen, err := v.View(r.Context())
	if err != nil {
		s.boardError(w, "reading the releases", err)
		return
	}
	writeJSON(w, http.StatusOK, screen)
}

// getVersion is one version: its build, with the end of the build's output, its gate with where its
// live part is, and its release.json and gate.json once it is a release folder, the gate.json
// whatever its state, so a gate that failed shows (16.2 step 6).
func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	version := r.PathValue("v")
	if !release.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", version+" is not a version")
		return
	}
	out := struct {
		Version  string            `json:"version"`
		Build    *versions.Build   `json:"build"`
		Gate     *versions.GateRow `json:"gate"`
		Manifest *release.Manifest `json:"manifest"`
		GateFile *release.Gate     `json:"gateFile"`
		// What it holds and its kind (16.2 step 6, since 0.54).
		Changes *versions.Changes  `json:"changes,omitempty"`
		Kind    *versions.KindView `json:"kind,omitempty"`
	}{Version: version}
	if v.Builder != nil {
		bd, err := v.Builder.Get(r.Context(), version)
		if err != nil {
			s.boardError(w, "reading the build", err)
			return
		}
		out.Build = bd
		if out.Gate, err = v.Builder.GetGate(r.Context(), version); err != nil {
			s.boardError(w, "reading the gate", err)
			return
		}
	}
	if f, err := release.Read(filepath.Join(v.Releases, version)); err == nil {
		out.Manifest = &f.Manifest
		// Shown without the amounts a server before 0.53 wrote into its lines (since 0.54).
		g, _ := release.ReadGate(f.Dir)
		out.GateFile = g.Unpriced()
		out.Kind = v.KindOf(f)
	}
	commit := ""
	switch {
	case out.Manifest != nil:
		commit = out.Manifest.Commit
	case out.Build != nil:
		commit = out.Build.Commit
	}
	if commit != "" {
		// What it holds is a part of the detail: when it cannot be read, a note says so, and the gate and
		// the press stay (reviews of 95d116d).
		ch, err := v.Holds(r.Context(), version, commit)
		if err != nil {
			s.Log.Error("reading what a version holds", "version", version, "error", err)
			ch = &versions.Changes{Tickets: []versions.ChangeTicket{}, Outside: []versions.ChangeCommit{},
				Note: "what it holds cannot be read: the server's log says why"}
		}
		out.Changes = ch
	}
	if out.Build == nil && out.Manifest == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no build and no release "+version)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// buildVersion is Build a release (16.2 step 3): the newest commit of Crew's project's main, stopped
// before a second improvement's merge, as the next patch number, or the minor or major asked for.
func (s *Server) buildVersion(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	var body struct {
		Bump string `json:"bump"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	switch body.Bump {
	case "", versions.BumpPatch, versions.BumpMinor, versions.BumpMajor:
	default:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bump is patch, minor, or major")
		return
	}
	if v.Builder == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "this server builds no release: it has no sandboxes")
		return
	}
	bd, next, err := v.BuildNow(r.Context(), body.Bump)
	switch {
	case errors.Is(err, versions.ErrBuildRuns):
		writeError(w, http.StatusConflict, "ABORTED", err.Error())
	case errors.Is(err, versions.ErrNothingToBuild), errors.Is(err, versions.ErrNoCrewProject), errors.Is(err, versions.ErrBuiltOnce):
		writeError(w, http.StatusUnprocessableEntity, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		s.boardError(w, "starting a build", err)
	default:
		s.Log.Info("the operator asked for a build of a release", "version", bd.Version, "commit", bd.Commit)
		writeJSON(w, http.StatusAccepted, map[string]any{"build": bd, "next": next})
	}
}

// runLiveGate is Run the live gate (16.2 step 5): a release whose runtime image's inputs differ from
// the running release's gets the operator's login in its live steps only after this press, which the
// screen makes after it showed the diff, GET /api/crew/versions/{v}'s gate.diff, and which names the
// hash of the inputs it showed, the release's manifest.runtimeSha256.
func (s *Server) runLiveGate(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	version := r.PathValue("v")
	if !release.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", version+" is not a version")
		return
	}
	if v.Builder == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "this server runs no gate: it has no sandboxes")
		return
	}
	// The press names the runtime image's inputs whose diff it showed, by their hash.
	var body struct {
		RuntimeSHA256 string `json:"runtimeSha256"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	err := v.Builder.RunLiveGate(r.Context(), version, body.RuntimeSHA256, "operator")
	switch {
	case errors.Is(err, versions.ErrNotWaitingGate), errors.Is(err, versions.ErrStaleDiff), errors.Is(err, versions.ErrBadRuntimeHash):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		s.boardError(w, "running the live gate", err)
	default:
		s.Log.Info("the operator ran the live gate", "version", version)
		g, err := v.Builder.GetGate(r.Context(), version)
		if err != nil {
			s.boardError(w, "reading the gate", err)
			return
		}
		writeJSON(w, http.StatusAccepted, g)
	}
}

func (s *Server) setGateAutoInstall(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	version := r.PathValue("v")
	if !release.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", version+" is not a version")
		return
	}
	if v.Builder == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "this server runs no gate: it has no sandboxes")
		return
	}
	var body struct {
		Install bool `json:"install"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	err := v.Builder.SetInstallWhenDone(r.Context(), version, body.Install)
	switch {
	case errors.Is(err, versions.ErrNotWaitingGate):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		s.boardError(w, "setting auto-install on the gate", err)
	default:
		s.Log.Info("the operator toggled install when gate passes", "version", version, "install", body.Install)
		g, err := v.Builder.GetGate(r.Context(), version)
		if err != nil {
			s.boardError(w, "reading the gate", err)
			return
		}
		writeJSON(w, http.StatusOK, g)
	}
}

// installVersion is Install now, Install, Update, and Roll back to this: the same request to the
// updater. A roll back that reads a snapshot back needs the version's name typed, as confirm.
func (s *Server) installVersion(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	version := r.PathValue("v")
	if !release.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", version+" is not a version")
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	req, err := v.Install(r.Context(), version, body.Confirm)
	var refused *updater.Refusal
	switch {
	case errors.Is(err, updater.ErrBusy):
		writeError(w, http.StatusConflict, "ABORTED", err.Error())
	case errors.As(err, &refused):
		writeError(w, http.StatusUnprocessableEntity, "FAILED_PRECONDITION", refused.Reason)
	case err != nil:
		writeError(w, http.StatusBadGateway, "UNAVAILABLE", err.Error())
	default:
		s.Log.Info("the operator asked the updater for a version", "version", version)
		writeJSON(w, http.StatusAccepted, req)
	}
}

func (s *Server) holdVersion(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	err := v.Hold(r.Context(), r.PathValue("v"), r.Method == http.MethodPost)
	switch {
	case errors.Is(err, versions.ErrNotWaiting):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
	case err != nil:
		s.boardError(w, "holding a release", err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"held": r.Method == http.MethodPost})
	}
}

func (s *Server) stopUpdater(w http.ResponseWriter, r *http.Request) {
	v := s.versions(w)
	if v == nil {
		return
	}
	if err := v.Updater.Stop(r.Context()); err != nil {
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": "yes"})
}
