package sandboxes

import (
	"context"
	"errors"
	"strings"

	"github.com/kesokaj/crew/internal/orchestrator"
)

// ExecRuntime is implemented by *orchestrator.Orchestrator for running commands inside a sandbox.
type ExecRuntime interface {
	Exec(ctx context.Context, containerID string, spec orchestrator.ExecSpec) (int, string, error)
}

// DockerKataRuntime adapts *Manager's Docker + Kata warm project VMs (`PauseSandbox`/`UnpauseSandbox`),
// sealed home snapshots (`SnapshotProjectHome`/`RestoreProjectHome`), and ephemeral sandboxes behind
// ComputeRuntime (design 13.12, R219).
type DockerKataRuntime struct {
	Manager *Manager
	Seal    Sealer
	Redact  func([]byte) []byte
}

// NewComputeRuntime returns a ComputeRuntime backed by m's Docker + Kata orchestrator.
func (m *Manager) NewComputeRuntime(seal Sealer, redactFn func([]byte) []byte) *DockerKataRuntime {
	return &DockerKataRuntime{Manager: m, Seal: seal, Redact: redactFn}
}

func (r *DockerKataRuntime) Driver() string { return DriverDockerKata }

func (r *DockerKataRuntime) Start(ctx context.Context, req StartRequest) (RuntimeInstance, error) {
	if r == nil || r.Manager == nil {
		return RuntimeInstance{}, errors.New("compute runtime manager is nil")
	}
	spec := req.Spec
	if req.ProjectID != "" && spec.Project == "" {
		spec.Project = req.ProjectID
	}
	if req.WarmProjectVM && spec.Project != "" {
		ps, sb, err := r.Manager.EnsureProjectSandbox(ctx, spec.Project, spec)
		if err != nil {
			return RuntimeInstance{}, err
		}
		return RuntimeInstance{
			Driver:    DriverDockerKata,
			SandboxID: ps.SandboxID,
			ProjectID: ps.ProjectID,
			Container: sb.Container,
			State:     ps.State,
			Mode:      sb.Mode,
			HomePath:  ps.HomePath,
			Sandbox:   sb,
		}, nil
	}
	row, sb, err := r.Manager.Start(ctx, spec)
	if err != nil {
		return RuntimeInstance{}, err
	}
	return RuntimeInstance{
		Driver:    DriverDockerKata,
		SandboxID: row.ID,
		ProjectID: row.Project,
		Container: sb.Container,
		State:     StateRunning,
		Mode:      row.Mode,
		HomePath:  r.Manager.Home(row.ID),
		Sandbox:   sb,
	}, nil
}

func (r *DockerKataRuntime) Exec(ctx context.Context, instanceID string, cmd ExecCommand) (ExecOutput, error) {
	if r == nil || r.Manager == nil || r.Manager.Runtime == nil {
		return ExecOutput{}, errors.New("compute runtime is not initialized")
	}
	er, ok := r.Manager.Runtime.(ExecRuntime)
	if !ok {
		return ExecOutput{}, errors.New("sandbox runtime does not support exec")
	}
	target := strings.TrimSpace(instanceID)
	if target == "" {
		return ExecOutput{}, errors.New("instanceID is required")
	}
	if !strings.HasPrefix(target, "crew-") {
		target = orchestrator.SandboxName(target)
	}
	code, out, err := er.Exec(ctx, target, orchestrator.ExecSpec{
		Cmd:     cmd.Cmd,
		User:    cmd.User,
		Env:     cmd.Env,
		WorkDir: cmd.WorkDir,
		Stdin:   cmd.Stdin,
	})
	return ExecOutput{ExitCode: code, Output: out}, err
}

func (r *DockerKataRuntime) Suspend(ctx context.Context, projectID string, force bool) (ProjectSandbox, error) {
	if r == nil || r.Manager == nil {
		return ProjectSandbox{}, errors.New("compute runtime manager is nil")
	}
	return r.Manager.PauseProjectSandbox(ctx, projectID, force)
}

func (r *DockerKataRuntime) Resume(ctx context.Context, projectID string, spec Spec) (ProjectSandbox, error) {
	if r == nil || r.Manager == nil {
		return ProjectSandbox{}, errors.New("compute runtime manager is nil")
	}
	ps, _, err := r.Manager.EnsureProjectSandbox(ctx, projectID, spec)
	return ps, err
}

func (r *DockerKataRuntime) Snapshot(ctx context.Context, projectID, label string) (ProjectSnapshot, error) {
	if r == nil || r.Manager == nil {
		return ProjectSnapshot{}, errors.New("compute runtime manager is nil")
	}
	return r.Manager.SnapshotProjectHome(ctx, projectID, label, r.Seal, r.Redact)
}

func (r *DockerKataRuntime) Restore(ctx context.Context, projectID, snapshotID string) (ProjectSandbox, error) {
	if r == nil || r.Manager == nil {
		return ProjectSandbox{}, errors.New("compute runtime manager is nil")
	}
	return r.Manager.RestoreProjectHome(ctx, projectID, snapshotID, r.Seal)
}

func (r *DockerKataRuntime) Stats(ctx context.Context, projectOrSandboxID string) (RuntimeStats, error) {
	if r == nil || r.Manager == nil {
		return RuntimeStats{}, errors.New("compute runtime manager is nil")
	}
	target := strings.TrimSpace(projectOrSandboxID)
	if isUUIDString(target) {
		ps, err := r.Manager.GetProjectSandbox(ctx, target)
		if err != nil {
			return RuntimeStats{}, err
		}
		out := RuntimeStats{
			Driver:         DriverDockerKata,
			ProjectID:      ps.ProjectID,
			SandboxID:      ps.SandboxID,
			State:          ps.State,
			ActiveSessions: ps.ActiveSessions,
		}
		if ps.SandboxID != "" && ps.State != StateStopped {
			st, err := r.Manager.SandboxStats(ctx, ps.SandboxID)
			if err != nil {
				return RuntimeStats{}, err
			}
			out.CPUPercent = st.CPUPercent
			out.MemUsedMB = st.MemUsedMB
			out.MemLimitMB = st.MemLimitMB
		}
		return out, nil
	}
	st, err := r.Manager.SandboxStats(ctx, target)
	if err != nil {
		return RuntimeStats{}, err
	}
	return RuntimeStats{
		Driver:     DriverDockerKata,
		SandboxID:  target,
		State:      StateRunning,
		CPUPercent: st.CPUPercent,
		MemUsedMB:  st.MemUsedMB,
		MemLimitMB: st.MemLimitMB,
	}, nil
}

func (r *DockerKataRuntime) Destroy(ctx context.Context, projectOrSandboxID string) error {
	if r == nil || r.Manager == nil {
		return errors.New("compute runtime manager is nil")
	}
	target := strings.TrimSpace(projectOrSandboxID)
	if isUUIDString(target) {
		_, err := r.Manager.StopProjectSandbox(ctx, target)
		return err
	}
	return r.Manager.Remove(ctx, target)
}

func isUUIDString(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
