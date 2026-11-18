package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
)

// States of a project's warm Kata microVM (design 1.37, 13.3).
const (
	StateStopped = "stopped"
	StateRunning = "running"
	StatePaused  = "paused"
)

// ErrProjectSandboxBusy is returned when an operator or reaper tries to suspend a project microVM
// while ACP sessions are still active without force.
var ErrProjectSandboxBusy = errors.New("project has active ACP sessions; stop or wait for runs to finish before suspending")

var projectLocks sync.Map // map[string]*sync.Mutex

func lockProject(projectID string) func() {
	if projectID == "" {
		return func() {}
	}
	v, _ := projectLocks.LoadOrStore(projectID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// PausableRuntime is the optional pause/unpause seam implemented by *orchestrator.Orchestrator.
type PausableRuntime interface {
	PauseSandbox(ctx context.Context, id string) error
	UnpauseSandbox(ctx context.Context, id string) error
	SandboxPaused(ctx context.Context, id string) (bool, error)
}

// ProjectSandbox is the database record of a project's persistent-home microVM (`project_sandboxes`).
type ProjectSandbox struct {
	ProjectID      string    `json:"projectId"`
	SandboxID      string    `json:"sandboxId"`
	State          string    `json:"state"`
	HomePath       string    `json:"homePath"`
	ImageTag       string    `json:"imageTag"`
	SnapshotID     string    `json:"snapshotId"`
	ActiveSessions int       `json:"activeSessions"`
	LastActiveAt   time.Time `json:"lastActiveAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ProjectRoot returns the directory `<sandboxes>/projects/<projectID>` that holds `home/` and `snapshots/`.
// Placing project roots inside `m.Dir` (`/var/lib/crew/sandboxes`) ensures `crew-server` and the
// host Docker daemon share the exact same bind-mounted host path from `deploy/compose.yaml`.
func (m *Manager) ProjectRoot(projectID string) string {
	return filepath.Join(m.Dir, "projects", projectID)
}

// ProjectHome returns the persistent `/home/agent` directory on the host for a project (`<sandboxes>/projects/<id>/home`).
func (m *Manager) ProjectHome(projectID string) string {
	return filepath.Join(m.ProjectRoot(projectID), "home")
}

// ProjectSnapshotsDir returns the directory where sealed home snapshots for a project are stored.
func (m *Manager) ProjectSnapshotsDir(projectID string) string {
	return filepath.Join(m.ProjectRoot(projectID), "snapshots")
}

// projectSandboxID builds a deterministic-prefix sandbox ID `proj-<short>-<rand>` valid for SandboxSpec.ID.
func projectSandboxID(projectID string) (string, error) {
	clean := idRand.ReplaceAllString(strings.ToLower(projectID), "")
	if len(clean) > 12 {
		clean = clean[:12]
	}
	if clean == "" {
		clean = "p"
	}
	return newID("proj-" + clean)
}

// GetProjectSandbox returns the project's `project_sandboxes` row, or a default `stopped` struct if none exists yet.
func (m *Manager) GetProjectSandbox(ctx context.Context, projectID string) (ProjectSandbox, error) {
	var ps ProjectSandbox
	err := m.Pool.QueryRow(ctx, `SELECT project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id,
			active_sessions, last_active_at, updated_at
		FROM project_sandboxes WHERE project_id = $1::uuid`, projectID).Scan(
		&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
		&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		now := time.Now().UTC()
		return ProjectSandbox{
			ProjectID:    projectID,
			State:        StateStopped,
			HomePath:     m.ProjectHome(projectID),
			LastActiveAt: now,
			UpdatedAt:    now,
		}, nil
	}
	return ps, err
}

// EnsureProjectSandbox starts or unpauses the project's single warm Kata microVM (`crew-proj-<project>`),
// mounting the persistent `<CREW_DATA_DIR>/projects/<id>/home` directory (design 1.37, 13.3).
func (m *Manager) EnsureProjectSandbox(ctx context.Context, projectID string, spec Spec) (ProjectSandbox, orchestrator.Sandbox, error) {
	if projectID == "" {
		return ProjectSandbox{}, orchestrator.Sandbox{}, errors.New("projectID is required")
	}
	unlock := lockProject(projectID)
	defer unlock()
	if spec.Kind == "" {
		spec.Kind = KindRun
	}
	if spec.CLI == "" {
		spec.CLI = cli.AgyName
	}
	spec.Project = projectID

	ps, err := m.GetProjectSandbox(ctx, projectID)
	if err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, err
	}
	// A project's warm microVM always has guest Docker (Minimal: false) and is sized from the
	// project's configured sandbox.{cpus, memory_gb} (default 4 vCPU, 6 GB RAM) rather than the
	// first waking role's size (design 1.38, 13.3, 13.10, R71).
	spec.Minimal = false
	projCfg := m.ProjectConfigFor(ctx, projectID)
	spec.Sizing = (capacity.Size{
		CPUs:   float64(projCfg.CPUs),
		MemMB:  int64(projCfg.MemoryGB) * 1024,
		Docker: true,
	}).Sizing()
	if (ps.State == StateRunning || ps.State == StatePaused) && ps.SandboxID != "" {
		if row, err := m.Get(ctx, ps.SandboxID); err == nil {
			if spec.Mode == "" {
				spec.Mode = row.Mode
			}
			wantMode := ModeFor(spec.Kind, spec.Mode)
			if row.Mode != wantMode || (spec.Image != "" && spec.Image != ps.ImageTag) {
				if ps.ActiveSessions > 0 {
					return ProjectSandbox{}, orchestrator.Sandbox{}, ErrProjectSandboxBusy
				}
				_, _ = m.stopProjectSandboxLocked(ctx, projectID)
				ps.State = StateStopped
				ps.SandboxID = ""
			}
		}
	}
	wantMode := ModeFor(spec.Kind, spec.Mode)
	if !slices.Contains(config.Modes, wantMode) {
		return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("no network mode %q", wantMode)
	}

	// 1. If paused, unpause the existing container in sub-second time.
	if ps.State == StatePaused && ps.SandboxID != "" {
		if _, err := m.Get(ctx, ps.SandboxID); err == nil {
			if pr, ok := m.Runtime.(PausableRuntime); ok {
				if err := pr.UnpauseSandbox(ctx, ps.SandboxID); err != nil {
					m.Log.Warn("unpausing project sandbox failed; recreating", "project", projectID, "sandbox", ps.SandboxID, "error", err)
				} else {
					_, _ = m.Pool.Exec(ctx, `UPDATE sandbox_tokens SET ends_at = greatest(ends_at, now() + '2 hours'::interval) WHERE sandbox = $1`, ps.SandboxID)
					err := m.Pool.QueryRow(ctx, `UPDATE project_sandboxes
						SET state = $2, last_active_at = now(), updated_at = now()
						WHERE project_id = $1::uuid
						RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
						projectID, StateRunning).Scan(
						&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
						&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
					)
					if err == nil {
						m.projHomes.Store(ps.SandboxID, ps.HomePath)
						err = m.prepareReusedProjectSession(ctx, ps, spec)
					}
					return ps, orchestrator.Sandbox{
						ID:        ps.SandboxID,
						Kind:      KindRun,
						Container: orchestrator.SandboxName(ps.SandboxID),
						Network:   orchestrator.SandboxName(ps.SandboxID),
						Mode:      ModeFor(spec.Kind, spec.Mode),
					}, err
				}
			} else {
				err := m.Pool.QueryRow(ctx, `UPDATE project_sandboxes
					SET state = $2, last_active_at = now(), updated_at = now()
					WHERE project_id = $1::uuid
					RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
					projectID, StateRunning).Scan(
					&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
					&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
				)
				if err == nil {
					m.projHomes.Store(ps.SandboxID, ps.HomePath)
					err = m.prepareReusedProjectSession(ctx, ps, spec)
				}
				return ps, orchestrator.Sandbox{
					ID:        ps.SandboxID,
					Kind:      KindRun,
					Container: orchestrator.SandboxName(ps.SandboxID),
					Network:   orchestrator.SandboxName(ps.SandboxID),
					Mode:      ModeFor(spec.Kind, spec.Mode),
				}, err
			}
		}
	}

	// 2. If already running and its sandbox row is still open, reuse it.
	if ps.State == StateRunning && ps.SandboxID != "" {
		if row, err := m.Get(ctx, ps.SandboxID); err == nil {
			if err := m.Pool.QueryRow(ctx, `UPDATE project_sandboxes
				SET last_active_at = now(), updated_at = now()
				WHERE project_id = $1::uuid
				RETURNING last_active_at, updated_at`, projectID).Scan(&ps.LastActiveAt, &ps.UpdatedAt); err != nil {
				m.Log.Warn("updating project sandbox last_active_at", "project", projectID, "error", err)
			}
			_, _ = m.Pool.Exec(ctx, `UPDATE sandbox_tokens SET ends_at = greatest(ends_at, now() + '2 hours'::interval) WHERE sandbox = $1`, ps.SandboxID)
			m.projHomes.Store(ps.SandboxID, ps.HomePath)
			if err := m.prepareReusedProjectSession(ctx, ps, spec); err != nil {
				return ProjectSandbox{}, orchestrator.Sandbox{}, err
			}
			return ps, orchestrator.Sandbox{
				ID:        ps.SandboxID,
				Kind:      row.Kind,
				Container: orchestrator.SandboxName(ps.SandboxID),
				Network:   orchestrator.SandboxName(ps.SandboxID),
				Mode:      row.Mode,
			}, nil
		}
	}

	// 3. Otherwise boot a fresh microVM mounted on the persistent project home.
	home := m.ProjectHome(projectID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("project home: %w", err)
	}
	if err := homepath.MkdirAll(home, ".crew", 0o700); err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("project home .crew: %w", err)
	}
	if err := homepath.MkdirAll(home, "worktrees", 0o755); err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("project worktrees dir: %w", err)
	}

	id, err := projectSandboxID(projectID)
	if err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, err
	}
	sbToken, sbHash, err := newToken()
	if err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, err
	}
	if err := homepath.WriteFile(home, ".crew/token", []byte(sbToken+"\n"), 0o600); err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, err
	}
	runHash := sbHash
	if spec.RunID != "" {
		runToken, rHash, err := newToken()
		if err != nil {
			return ProjectSandbox{}, orchestrator.Sandbox{}, err
		}
		runHash = rHash
		relTok := runspec.RunControlDir(spec.RunID) + "/token"
		if err := homepath.WriteFile(home, relTok, []byte(runToken+"\n"), 0o600); err != nil {
			return ProjectSandbox{}, orchestrator.Sandbox{}, err
		}
	}

	mode := wantMode
	repos := spec.Repos
	if repos == nil {
		repos = []string{}
	}
	image := spec.Image
	if image == "" && ps.ImageTag != "" {
		image = ps.ImageTag
	}
	if image != "" {
		if hi, ok := m.Runtime.(interface {
			HasImage(ctx context.Context, ref string) (bool, error)
		}); ok {
			if exists, err := hi.HasImage(ctx, image); err == nil && !exists {
				if m.Log != nil {
					m.Log.Warn("project custom image not found in Docker; using default runtime image", "project", projectID, "image", image)
				}
				image = ""
			}
		}
	}
	recorded := m.Image
	if image != "" {
		recorded = image
	}
	life := spec.Life
	if life == 0 {
		life = 2 * time.Hour
	}

	err = pgx.BeginFunc(ctx, m.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO sandboxes (id, kind, project, container, network, image, cli, repos, gcloud, mode)
			VALUES ($1, $2, $3::uuid, $4, $4, $5, $6, $7, false, $8)`,
			id, KindRun, projectID, orchestrator.SandboxName(id), recorded, spec.CLI, repos, mode); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at)
			VALUES ($1, $2, now() + $3::interval)`,
			sbHash, id, fmt.Sprintf("%d seconds", int(life.Seconds()))); err != nil {
			return err
		}
		if runHash != sbHash {
			if _, err := tx.Exec(ctx, `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at)
				VALUES ($1, $2, now() + $3::interval)`,
				runHash, id, fmt.Sprintf("%d seconds", int(life.Seconds()))); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, image_tag, last_active_at, updated_at)
			VALUES ($1::uuid, $2, $3, $4, $5, now(), now())
			ON CONFLICT (project_id) DO UPDATE
			SET sandbox_id = EXCLUDED.sandbox_id,
			    state = EXCLUDED.state,
			    home_path = EXCLUDED.home_path,
			    image_tag = CASE WHEN EXCLUDED.image_tag <> '' THEN EXCLUDED.image_tag ELSE project_sandboxes.image_tag END,
			    last_active_at = now(),
			    updated_at = now()
			RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
			projectID, id, StateRunning, home, ps.ImageTag).Scan(
			&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
			&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
		)
	})
	if err != nil {
		return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("recording project sandbox: %w", err)
	}
	m.projHomes.Store(ps.SandboxID, ps.HomePath)

	if spec.Prepare != nil {
		if err := spec.Prepare(id, home, runHash); err != nil {
			_, _ = m.stopProjectSandboxLocked(context.Background(), projectID)
			return ProjectSandbox{}, orchestrator.Sandbox{}, fmt.Errorf("preparing project sandbox: %w", err)
		}
	}

	sb, err := m.Runtime.StartSandbox(ctx, orchestrator.SandboxSpec{
		ID:      id,
		Kind:    KindRun,
		Home:    home,
		Env:     spec.Env,
		Minimal: spec.Minimal,
		Mode:    mode,
		Sizing:  spec.Sizing,
		Image:   image,
		Labels:  map[string]string{"crew.project": projectID},
	})
	if err != nil {
		_, _ = m.stopProjectSandboxLocked(context.Background(), projectID)
		return ProjectSandbox{}, orchestrator.Sandbox{}, err
	}
	m.adoptMu.Lock()
	m.startedAt = time.Now()
	m.adoptMu.Unlock()
	return ps, sb, nil
}

// AcquireProjectSession increments `active_sessions` and updates `last_active_at` on the project sandbox.
func (m *Manager) AcquireProjectSession(ctx context.Context, projectID string) error {
	_, err := m.Pool.Exec(ctx, `UPDATE project_sandboxes
		SET active_sessions = active_sessions + 1, last_active_at = now(), updated_at = now()
		WHERE project_id = $1::uuid`, projectID)
	return err
}

// ReleaseProjectSession decrements `active_sessions` (floor 0) and updates `last_active_at` on the project sandbox.
func (m *Manager) ReleaseProjectSession(ctx context.Context, projectID string) error {
	_, err := m.Pool.Exec(ctx, `UPDATE project_sandboxes
		SET active_sessions = greatest(active_sessions - 1, 0), last_active_at = now(), updated_at = now()
		WHERE project_id = $1::uuid`, projectID)
	return err
}

// PauseProjectSandbox suspends a project's warm microVM (`ContainerPause`, 0% CPU) when no sessions
// are active (or when `force` is true).
func (m *Manager) PauseProjectSandbox(ctx context.Context, projectID string, force bool) (ProjectSandbox, error) {
	unlock := lockProject(projectID)
	defer unlock()
	ps, err := m.GetProjectSandbox(ctx, projectID)
	if err != nil {
		return ProjectSandbox{}, err
	}
	if ps.ActiveSessions > 0 && !force {
		return ps, fmt.Errorf("%w (%d active)", ErrProjectSandboxBusy, ps.ActiveSessions)
	}
	if ps.State != StateRunning || ps.SandboxID == "" {
		return ps, nil
	}
	if pr, ok := m.Runtime.(PausableRuntime); ok {
		if err := pr.PauseSandbox(ctx, ps.SandboxID); err != nil {
			return ps, err
		}
	}
	err = m.Pool.QueryRow(ctx, `UPDATE project_sandboxes
		SET state = $2,
		    active_sessions = CASE WHEN $3 THEN 0 ELSE active_sessions END,
		    updated_at = now()
		WHERE project_id = $1::uuid
		RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
		projectID, StatePaused, force).Scan(
		&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
		&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
	)
	return ps, err
}

// StopProjectSandbox stops and removes a project's Kata container (`0 MB` RAM) while keeping its
// persistent `<CREW_DATA_DIR>/projects/<id>/home` intact (design 1.37, 13.3).
func (m *Manager) StopProjectSandbox(ctx context.Context, projectID string) (ProjectSandbox, error) {
	unlock := lockProject(projectID)
	defer unlock()
	return m.stopProjectSandboxLocked(ctx, projectID)
}

func (m *Manager) stopProjectSandboxLocked(ctx context.Context, projectID string) (ProjectSandbox, error) {
	ps, err := m.GetProjectSandbox(ctx, projectID)
	if err != nil {
		return ProjectSandbox{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()

	if ps.SandboxID != "" {
		_, _ = m.Pool.Exec(ctx, `DELETE FROM sandbox_tokens WHERE sandbox = $1`, ps.SandboxID)
		if pr, ok := m.Runtime.(PausableRuntime); ok && ps.State == StatePaused {
			_ = pr.UnpauseSandbox(ctx, ps.SandboxID)
		}
		rerr := m.Runtime.RemoveSandbox(ctx, ps.SandboxID)
		if m.OnRemove != nil {
			m.OnRemove(ctx, ps.SandboxID)
		}
		m.noToken.Delete(ps.SandboxID)
		if rerr != nil {
			return ps, rerr
		}
		_, _ = m.Pool.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1 AND removed_at IS NULL`, ps.SandboxID)
	}
	// Strip the ephemeral sandbox token from the persistent home, keeping all conversation DBs and caches.
	_ = homepath.Remove(m.ProjectHome(projectID), ".crew/token")

	home := m.ProjectHome(projectID)
	err = m.Pool.QueryRow(ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, active_sessions, last_active_at, updated_at)
		VALUES ($1::uuid, '', $2, $3, 0, now(), now())
		ON CONFLICT (project_id) DO UPDATE
		SET sandbox_id = '',
		    state = $2,
		    active_sessions = 0,
		    updated_at = now()
		RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
		projectID, StateStopped, home).Scan(
		&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
		&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
	)
	return ps, err
}
