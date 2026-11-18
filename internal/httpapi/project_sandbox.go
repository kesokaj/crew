package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// ProjectSandboxController is implemented by *sandboxes.Manager for managing a project's warm Kata
// microVM (`crew-proj-<project>`) and persistent `/home/agent` snapshots (design 1.37, 13.3).
type ProjectSandboxController interface {
	ProjectHome(projectID string) string
	ProjectConfigFor(ctx context.Context, projectID string) sandboxes.ProjectConfig
	GetProjectSandbox(ctx context.Context, projectID string) (sandboxes.ProjectSandbox, error)
	EnsureProjectSandbox(ctx context.Context, projectID string, spec sandboxes.Spec) (sandboxes.ProjectSandbox, orchestrator.Sandbox, error)
	PauseProjectSandbox(ctx context.Context, projectID string, force bool) (sandboxes.ProjectSandbox, error)
	StopProjectSandbox(ctx context.Context, projectID string) (sandboxes.ProjectSandbox, error)
	SetProjectImageTag(ctx context.Context, projectID, imageTag string) (sandboxes.ProjectSandbox, error)
	SnapshotProjectHome(ctx context.Context, projectID, label string, seal sandboxes.Sealer, redactFn func([]byte) []byte) (sandboxes.ProjectSnapshot, error)
	RestoreProjectHome(ctx context.Context, projectID, snapshotID string, seal sandboxes.Sealer) (sandboxes.ProjectSandbox, error)
	ListProjectSnapshots(ctx context.Context, projectID string) ([]sandboxes.ProjectSnapshot, error)
}

func (s *Server) projectSandboxCtrl() (ProjectSandboxController, sandboxes.Sealer) {
	d := s.Runs.Load()
	if d == nil || d.Sandboxes == nil {
		return nil, nil
	}
	ctrl, ok := d.Sandboxes.(ProjectSandboxController)
	if !ok {
		return nil, nil
	}
	return ctrl, d.Seal
}

func (s *Server) syncProjectSkillsHome(ctx context.Context, b *board.Board, p *board.Project, deletedPath string) {
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil || b == nil || p == nil {
		return
	}
	_ = b.SyncProjectHomeHarness(ctx, p, ctrl.ProjectHome(p.ID), deletedPath)
}

func (s *Server) syncGlobalSkillsAllHomes(ctx context.Context) {
	ctrl, _ := s.projectSandboxCtrl()
	pool := s.Pool.Load()
	if ctrl == nil || pool == nil {
		return
	}
	_ = board.SyncAllProjectHomesGlobalHarness(ctx, pool, ctrl.ProjectHome)
}

func (s *Server) getProjectSandboxStatus(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"projectId":           p.ID,
			"sandboxId":           "",
			"state":               sandboxes.StateStopped,
			"imageTag":            "",
			"snapshotId":          "",
			"activeSessions":      0,
			"cpus":                4,
			"memoryGB":            6,
			"diskGB":              20,
			"suspendAfterMinutes": 5,
			"stopAfterMinutes":    30,
			"maxSnapshots":        sandboxes.DefaultMaxSnapshots,
			"snapshots":           []sandboxes.ProjectSnapshot{},
		})
		return
	}
	ps, err := ctrl.GetProjectSandbox(r.Context(), p.ID)
	if err != nil {
		s.boardError(w, "reading the project sandbox", err)
		return
	}
	snaps, err := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	if err != nil {
		s.boardError(w, "reading the project snapshots", err)
		return
	}
	_ = b
	writeJSON(w, http.StatusOK, s.projectSandboxPayloadWithConfig(r.Context(), ctrl, ps, snaps))
}

func (s *Server) startProjectSandbox(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	mode, err := settings.NetworkMode(r.Context(), b.Pool, p.ID, 0)
	if err != nil {
		s.boardError(w, "reading the project network mode", err)
		return
	}
	ps, _, err := ctrl.EnsureProjectSandbox(r.Context(), p.ID, sandboxes.Spec{
		Kind:    sandboxes.KindRun,
		Project: p.ID,
		Mode:    mode,
	})
	if err != nil {
		s.boardError(w, "starting the project sandbox", err)
		return
	}
	_ = b.SyncProjectHomeHarness(r.Context(), p, ps.HomePath, "")
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, projectSandboxPayload(ps, snaps))
}

func (s *Server) suspendProjectSandbox(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	decodeOptional(r, &req)
	ps, err := ctrl.PauseProjectSandbox(r.Context(), p.ID, req.Force)
	if err != nil {
		if errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
			writeError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		s.boardError(w, "suspending the project sandbox", err)
		return
	}
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, projectSandboxPayload(ps, snaps))
}

func (s *Server) stopProjectSandbox(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	ps, err := ctrl.StopProjectSandbox(r.Context(), p.ID)
	if err != nil {
		s.boardError(w, "stopping the project sandbox", err)
		return
	}
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, projectSandboxPayload(ps, snaps))
}

func (s *Server) snapshotProjectSandbox(w http.ResponseWriter, r *http.Request) {
	_, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, seal := s.projectSandboxCtrl()
	if ctrl == nil || seal == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	decodeOptional(r, &req)
	snap, err := ctrl.SnapshotProjectHome(r.Context(), p.ID, strings.TrimSpace(req.Label), seal, redactTextBytes)
	if err != nil {
		if errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
			writeError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		s.boardError(w, "snapshotting the project sandbox", err)
		return
	}
	ps, _ := ctrl.GetProjectSandbox(r.Context(), p.ID)
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	out := projectSandboxPayload(ps, snaps)
	out["snapshot"] = snap
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) restoreProjectSandboxSnapshot(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, seal := s.projectSandboxCtrl()
	if ctrl == nil || seal == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	snapID := strings.TrimSpace(r.PathValue("id"))
	if snapID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "snapshot id is required")
		return
	}
	ps, err := ctrl.RestoreProjectHome(r.Context(), p.ID, snapID, seal)
	if err != nil {
		if strings.Contains(err.Error(), "sha256 mismatch") {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		s.boardError(w, "restoring the project snapshot", err)
		return
	}
	_ = b.SyncProjectHomeHarness(r.Context(), p, ps.HomePath, "")
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	out := projectSandboxPayload(ps, snaps)
	out["restored"] = snapID
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) buildProjectSandboxImage(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctrl, _ := s.projectSandboxCtrl()
	if ctrl == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sandbox manager is not ready")
		return
	}
	var req struct {
		Dockerfile string `json:"dockerfile"`
	}
	decodeOptional(r, &req)
	df := strings.TrimSpace(req.Dockerfile)
	if df == "" && b.Git != nil {
		if main := b.Git.Main(r.Context(), p.Name); main != "" {
			if raw, err := b.Git.FileAt(r.Context(), p.Name, main, ".crew/Dockerfile"); err == nil && len(raw) > 0 {
				df = strings.TrimSpace(string(raw))
			}
		}
	}
	if df == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no .crew/Dockerfile found on main; commit .crew/Dockerfile starting with FROM crew-runtime first")
		return
	}
	if !validProjectDockerfile(df) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ".crew/Dockerfile must start with FROM crew-runtime and contain no other base image")
		return
	}
	sum := sha256.Sum256([]byte(df))
	tag := fmt.Sprintf("crew-project-runtime:%s-%s", strings.ToLower(p.Key), hex.EncodeToString(sum[:6]))
	if err := s.runProjectImageBuild(r.Context(), p, df, tag); err != nil {
		s.boardError(w, "building the project runtime image", err)
		return
	}
	ps, err := ctrl.SetProjectImageTag(r.Context(), p.ID, tag)
	if err != nil {
		s.boardError(w, "setting the project image tag", err)
		return
	}
	snaps, _ := ctrl.ListProjectSnapshots(r.Context(), p.ID)
	out := projectSandboxPayload(ps, snaps)
	out["builtImage"] = tag
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runProjectImageBuild(ctx context.Context, p *board.Project, df, tag string) error {
	d := s.Runs.Load()
	if d == nil || d.Harness == nil {
		return nil
	}
	ib, ok := d.Harness.(harness.ImageBuilder)
	if !ok {
		return nil
	}
	var tree bytes.Buffer
	tw := tar.NewWriter(&tree)
	content := []byte(df + "\n")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     ".crew/Dockerfile",
		Mode:     0o644,
		Size:     int64(len(content)),
		ModTime:  time.Now().UTC(),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "crew-proj-img-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	outPath := filepath.Join(tmpDir, "out", harness.ImageFile)
	if _, err := ib.BuildImage(ctx, harness.ImageSpec{
		Project:    p.ID,
		Mode:       config.ModeSources,
		Tree:       tree.Bytes(),
		Dockerfile: ".crew/Dockerfile",
		Out:        outPath,
		Timeout:    30 * time.Minute,
	}); err != nil {
		return err
	}
	type imageLoader interface {
		LoadImage(ctx context.Context, r io.Reader) (string, error)
		TagImage(ctx context.Context, id, ref string) error
	}
	var loader imageLoader
	if mgr, ok := d.Sandboxes.(*sandboxes.Manager); ok && mgr != nil {
		loader, _ = mgr.Runtime.(imageLoader)
	}
	if loader == nil {
		return nil
	}
	src, err := os.Open(outPath)
	if err != nil {
		return err
	}
	defer src.Close()
	cleanPath := filepath.Join(tmpDir, "clean.tar")
	cleanOut, err := os.OpenFile(cleanPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	imgID, err := hosting.CleanImage(src, cleanOut)
	_ = cleanOut.Close()
	if err != nil {
		return err
	}
	cf, err := os.Open(cleanPath)
	if err != nil {
		return err
	}
	defer cf.Close()
	loadedID, err := loader.LoadImage(ctx, cf)
	if err != nil {
		return err
	}
	if loadedID != imgID.ID {
		return fmt.Errorf("loaded image id %s does not match manifest %s", loadedID, imgID.ID)
	}
	return loader.TagImage(ctx, imgID.ID, tag)
}

func redactTextBytes(b []byte) []byte {
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return b
	}
	return []byte(redact.New().Text(string(b)))
}

func validProjectDockerfile(df string) bool {
	// Join backslash-continued lines before scanning so FROM or --from cannot hide across wraps,
	// and reject Dockerfile parser directives (# escape=, # syntax=) before skipping comments.
	rawLines := strings.Split(strings.ReplaceAll(df, "\r\n", "\n"), "\n")
	var logical []string
	var cur strings.Builder
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			dir := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "#")))
			if key, _, ok := strings.Cut(dir, "="); ok {
				switch strings.TrimSpace(key) {
				case "escape", "syntax":
					return false
				}
			}
			continue
		}
		if strings.HasSuffix(trimmed, "`") {
			return false
		}
		if withoutCont, ok := strings.CutSuffix(trimmed, "\\"); ok {
			if cur.Len() > 0 && withoutCont != "" {
				cur.WriteByte(' ')
			}
			cur.WriteString(strings.TrimSpace(withoutCont))
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(trimmed)
		logical = append(logical, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		logical = append(logical, cur.String())
	}

	sawFrom := false
	for _, line := range logical {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		inst := strings.ToUpper(fields[0])
		if !sawFrom && inst == "ARG" {
			for _, tok := range fields[1:] {
				if strings.HasPrefix(tok, "--") {
					return false
				}
			}
			continue
		}
		if !sawFrom && inst != "FROM" {
			return false
		}
		switch inst {
		case "FROM":
			if sawFrom || strings.Contains(line, "$") {
				return false
			}
			idx := 1
			for idx < len(fields) && strings.HasPrefix(fields[idx], "--") {
				idx++
			}
			if idx >= len(fields) {
				return false
			}
			ref := fields[idx]
			if ref != "crew-runtime" && !strings.HasPrefix(ref, "crew-runtime:") && !strings.HasPrefix(ref, "crew-runtime@") {
				return false
			}
			sawFrom = true
		case "COPY", "ADD":
			idx := 1
			for idx < len(fields) && strings.HasPrefix(fields[idx], "--") {
				tok := fields[idx]
				lower := strings.ToLower(tok)
				if strings.Contains(tok, "$") || lower == "--from" || strings.HasPrefix(lower, "--from=") {
					return false
				}
				idx++
			}
			if idx >= len(fields) {
				return false
			}
			if inst == "ADD" {
				var srcs []string
				rest := strings.TrimSpace(strings.Join(fields[idx:], " "))
				if strings.HasPrefix(rest, "[") {
					var arr []string
					if err := json.Unmarshal([]byte(rest), &arr); err != nil || len(arr) < 2 {
						return false
					}
					srcs = arr[:len(arr)-1]
				} else {
					if len(fields)-idx < 2 {
						return false
					}
					srcs = fields[idx : len(fields)-1]
				}
				for _, src := range srcs {
					lower := strings.ToLower(strings.TrimSpace(src))
					if strings.Contains(src, "$") ||
						strings.HasPrefix(lower, "http://") ||
						strings.HasPrefix(lower, "https://") ||
						strings.HasPrefix(lower, "git://") ||
						strings.HasPrefix(lower, "git@") {
						return false
					}
				}
			}
		case "RUN":
			for _, tok := range fields[1:] {
				if !strings.HasPrefix(tok, "--") {
					break
				}
				lower := strings.ToLower(tok)
				if strings.Contains(tok, "$") ||
					lower == "--network" || strings.HasPrefix(lower, "--network=") ||
					lower == "--security" || strings.HasPrefix(lower, "--security=") {
					return false
				}
				if rest, ok := strings.CutPrefix(lower, "--mount="); ok {
					for _, opt := range strings.Split(rest, ",") {
						cleanOpt := strings.Trim(strings.TrimSpace(opt), "\"'")
						if strings.HasPrefix(cleanOpt, "from=") || cleanOpt == "type=secret" || cleanOpt == "type=ssh" {
							return false
						}
					}
				}
			}
		case "ENV", "ARG", "WORKDIR", "USER", "EXPOSE", "LABEL", "VOLUME",
			"ENTRYPOINT", "CMD", "SHELL", "STOPSIGNAL", "HEALTHCHECK":
		default:
			return false
		}
	}
	return sawFrom
}

func (s *Server) projectSandboxPayloadWithConfig(ctx context.Context, ctrl ProjectSandboxController, ps sandboxes.ProjectSandbox, snaps []sandboxes.ProjectSnapshot) map[string]any {
	out := projectSandboxPayload(ps, snaps)
	if ctrl != nil && ps.ProjectID != "" {
		cfg := ctrl.ProjectConfigFor(ctx, ps.ProjectID)
		out["cpus"] = cfg.CPUs
		out["memoryGB"] = cfg.MemoryGB
		out["diskGB"] = cfg.DiskGB
		out["suspendAfterMinutes"] = cfg.SuspendAfterMinutes
		out["stopAfterMinutes"] = cfg.StopAfterMinutes
		out["maxSnapshots"] = cfg.MaxSnapshots
	}
	return out
}

func projectSandboxPayload(ps sandboxes.ProjectSandbox, snaps []sandboxes.ProjectSnapshot) map[string]any {
	if snaps == nil {
		snaps = []sandboxes.ProjectSnapshot{}
	}
	return map[string]any{
		"projectId":      ps.ProjectID,
		"sandboxId":      ps.SandboxID,
		"state":          ps.State,
		"imageTag":       ps.ImageTag,
		"snapshotId":     ps.SnapshotID,
		"activeSessions": ps.ActiveSessions,
		"lastActiveAt":   ps.LastActiveAt,
		"updatedAt":      ps.UpdatedAt,
		"snapshots":      snaps,
	}
}

func decodeOptional(r *http.Request, dst any) {
	if r.Body == nil {
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return
	}
	_ = json.Unmarshal(data, dst)
}
