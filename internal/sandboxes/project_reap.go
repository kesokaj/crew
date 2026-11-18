package sandboxes

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
)

// StatsRuntime is the optional container stats seam implemented by *orchestrator.Orchestrator.
type StatsRuntime interface {
	SandboxStats(ctx context.Context, idOrContainer string) (orchestrator.ContainerStats, error)
}

// SandboxStats reads a sandbox container's live CPU and memory usage if supported by the runtime.
func (m *Manager) SandboxStats(ctx context.Context, idOrContainer string) (orchestrator.ContainerStats, error) {
	if m == nil || m.Runtime == nil {
		return orchestrator.ContainerStats{}, nil
	}
	sr, ok := m.Runtime.(StatsRuntime)
	if !ok {
		return orchestrator.ContainerStats{}, nil
	}
	return sr.SandboxStats(ctx, idOrContainer)
}

func (m *Manager) prepareReusedProjectSession(ctx context.Context, ps ProjectSandbox, spec Spec) error {
	if spec.Prepare == nil {
		return nil
	}
	if err := os.MkdirAll(ps.HomePath, 0o700); err != nil {
		return err
	}
	if err := homepath.MkdirAll(ps.HomePath, ".crew", 0o700); err != nil {
		return err
	}
	runToken, runHash, err := newToken()
	if err != nil {
		return err
	}
	life := spec.Life
	if life == 0 {
		life = 2 * time.Hour
	}
	// Never overwrite the shared .crew/token of an active project microVM, which would clobber
	// concurrent sessions. Write the per-session token into .crew/runs/<runID>/token instead,
	// and keep .crew/token as a separate sandbox-only token.
	if _, err := homepath.ReadFile(ps.HomePath, ".crew/token", 4096); os.IsNotExist(err) {
		sbToken := runToken
		if spec.RunID != "" {
			var sbHash string
			sbToken, sbHash, err = newToken()
			if err != nil {
				return err
			}
			if _, err := m.Pool.Exec(ctx, `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at)
				VALUES ($1, $2, now() + $3::interval)`,
				sbHash, ps.SandboxID, fmt.Sprintf("%d seconds", int(life.Seconds()))); err != nil {
				return err
			}
		}
		if err := homepath.WriteFile(ps.HomePath, ".crew/token", []byte(sbToken+"\n"), 0o600); err != nil {
			return err
		}
	}
	if spec.RunID != "" {
		relTok := runspec.RunControlDir(spec.RunID) + "/token"
		if err := homepath.WriteFile(ps.HomePath, relTok, []byte(runToken+"\n"), 0o600); err != nil {
			return err
		}
	}
	if _, err := m.Pool.Exec(ctx, `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at)
		VALUES ($1, $2, now() + $3::interval)`,
		runHash, ps.SandboxID, fmt.Sprintf("%d seconds", int(life.Seconds()))); err != nil {
		return err
	}
	return spec.Prepare(ps.SandboxID, ps.HomePath, runHash)
}

// SetProjectImageTag sets the optional per-project derived Docker image tag (`FROM crew-runtime:<release>`).
func (m *Manager) SetProjectImageTag(ctx context.Context, projectID, imageTag string) (ProjectSandbox, error) {
	home := m.ProjectHome(projectID)
	var ps ProjectSandbox
	err := m.Pool.QueryRow(ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, image_tag, last_active_at, updated_at)
		VALUES ($1::uuid, '', $2, $3, $4, now(), now())
		ON CONFLICT (project_id) DO UPDATE
		SET image_tag = EXCLUDED.image_tag,
		    updated_at = now()
		RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
		projectID, StateStopped, home, imageTag).Scan(
		&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
		&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
	)
	return ps, err
}

// ProjectConfig holds the resolved per-project sandbox sizing and lifecycle settings (design 1.38, 13.3, 13.10).
type ProjectConfig struct {
	CPUs                int
	MemoryGB            int
	DiskGB              int
	SuspendAfterMinutes int
	StopAfterMinutes    int
	MaxSnapshots        int
}

// ProjectConfigFor reads the project's effective `sandbox.*` configuration from `settings`,
// falling back to the machine's `crew.yaml` defaults when per-project overrides are null.
func (m *Manager) ProjectConfigFor(ctx context.Context, projectID string) ProjectConfig {
	cfg := ProjectConfig{
		CPUs:                6,
		MemoryGB:            8,
		DiskGB:              20,
		SuspendAfterMinutes: 5,
		StopAfterMinutes:    30,
		MaxSnapshots:        DefaultMaxSnapshots,
	}
	if m == nil || m.Pool == nil {
		return cfg
	}
	var mSuspend, mStop, mSnaps *int
	if err := m.Pool.QueryRow(ctx, `SELECT
		(doc->'sandbox'->>'suspend_after_minutes')::int,
		(doc->'sandbox'->>'stop_after_minutes')::int,
		(doc->'sandbox'->>'max_snapshots')::int
		FROM settings WHERE project IS NULL`).Scan(&mSuspend, &mStop, &mSnaps); err == nil {
		if mSuspend != nil && *mSuspend > 0 {
			cfg.SuspendAfterMinutes = *mSuspend
		}
		if mStop != nil && *mStop > 0 {
			cfg.StopAfterMinutes = *mStop
		}
		if mSnaps != nil && *mSnaps > 0 {
			cfg.MaxSnapshots = *mSnaps
		}
	}
	if projectID == "" {
		return cfg
	}
	var cpus, memGB, diskGB, pSuspend, pStop, pSnaps *int
	if err := m.Pool.QueryRow(ctx, `SELECT
		(doc->'sandbox'->>'cpus')::int,
		(doc->'sandbox'->>'memory_gb')::int,
		(doc->'sandbox'->>'disk_gb')::int,
		(doc->'sandbox'->>'suspend_after_minutes')::int,
		(doc->'sandbox'->>'stop_after_minutes')::int,
		(doc->'sandbox'->>'max_snapshots')::int
		FROM settings WHERE project = $1::uuid`, projectID).Scan(
		&cpus, &memGB, &diskGB, &pSuspend, &pStop, &pSnaps,
	); err != nil {
		return cfg
	}
	if cpus != nil && *cpus >= 2 {
		cfg.CPUs = *cpus
	}
	if memGB != nil && *memGB >= 2 {
		cfg.MemoryGB = *memGB
	}
	if diskGB != nil && *diskGB >= 5 {
		cfg.DiskGB = *diskGB
	}
	if pSuspend != nil && *pSuspend >= 1 {
		cfg.SuspendAfterMinutes = *pSuspend
	}
	if pStop != nil && *pStop >= 1 {
		cfg.StopAfterMinutes = *pStop
	}
	if pSnaps != nil && *pSnaps >= 1 {
		cfg.MaxSnapshots = *pSnaps
	}
	return cfg
}

// ReapIdleProjectSandboxes transitions idle `running` project sandboxes to `paused` after `suspendAfter`
// (or the project's `sandbox.suspend_after_minutes` override) and idle `paused` project sandboxes to
// `stopped` (`0 MB` RAM) after `stopAfter` (or the project's `sandbox.stop_after_minutes` override).
func (m *Manager) ReapIdleProjectSandboxes(ctx context.Context, suspendAfter, stopAfter time.Duration) (paused, stopped int, err error) {
	type idleRow struct {
		projectID    string
		state        string
		idleSecs     int64
		pSuspendMins *int
		pStopMins    *int
	}
	rows, qerr := m.Pool.Query(ctx, `SELECT
		ps.project_id::text,
		ps.state,
		EXTRACT(EPOCH FROM (now() - ps.last_active_at))::bigint,
		(st.doc->'sandbox'->>'suspend_after_minutes')::int,
		(st.doc->'sandbox'->>'stop_after_minutes')::int
		FROM project_sandboxes ps
		LEFT JOIN settings st ON st.project = ps.project_id
		WHERE ps.state IN ('running', 'paused') AND ps.active_sessions = 0`)
	if qerr != nil {
		return 0, 0, qerr
	}
	items, qerr := pgx.CollectRows(rows, func(r pgx.CollectableRow) (idleRow, error) {
		var it idleRow
		return it, r.Scan(&it.projectID, &it.state, &it.idleSecs, &it.pSuspendMins, &it.pStopMins)
	})
	if qerr != nil {
		return 0, 0, qerr
	}
	for _, it := range items {
		effSuspend := suspendAfter
		if it.pSuspendMins != nil && *it.pSuspendMins >= 1 {
			effSuspend = time.Duration(*it.pSuspendMins) * time.Minute
		}
		effStop := stopAfter
		if it.pStopMins != nil && *it.pStopMins >= 1 {
			effStop = time.Duration(*it.pStopMins) * time.Minute
		}
		switch it.state {
		case StateRunning:
			if effSuspend > 0 && it.idleSecs >= int64(effSuspend.Seconds()) {
				if _, perr := m.PauseProjectSandbox(ctx, it.projectID, false); perr == nil {
					paused++
				}
			}
		case StatePaused:
			if effStop > 0 && it.idleSecs >= int64(effStop.Seconds()) {
				if _, serr := m.StopProjectSandbox(ctx, it.projectID); serr == nil {
					stopped++
				}
			}
		}
	}
	return paused, stopped, nil
}
