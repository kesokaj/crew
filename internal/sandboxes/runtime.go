package sandboxes

import (
	"context"

	"github.com/kesokaj/crew/internal/orchestrator"
)

// Driver names reported by ComputeRuntime implementations (design 13.12, R219).
const (
	DriverDockerKata     = "docker-kata"
	DriverKubernetesKata = "kubernetes-kata"
)

// ComputeRuntime formalizes sandbox lifecycle operations across Tier-1 Docker + Kata on dave
// and future Kubernetes (`RuntimeClass: kata` + BlobStore snapshot offload) backends (design 13.12, R219).
type ComputeRuntime interface {
	Driver() string
	Start(ctx context.Context, req StartRequest) (RuntimeInstance, error)
	Exec(ctx context.Context, instanceID string, cmd ExecCommand) (ExecOutput, error)
	Suspend(ctx context.Context, projectID string, force bool) (ProjectSandbox, error)
	Resume(ctx context.Context, projectID string, spec Spec) (ProjectSandbox, error)
	Snapshot(ctx context.Context, projectID, label string) (ProjectSnapshot, error)
	Restore(ctx context.Context, projectID, snapshotID string) (ProjectSandbox, error)
	Stats(ctx context.Context, projectOrSandboxID string) (RuntimeStats, error)
	Destroy(ctx context.Context, projectOrSandboxID string) error
}

// StartRequest starts either a project's warm persistent-home microVM or a throwaway sandbox.
type StartRequest struct {
	ProjectID     string
	WarmProjectVM bool
	Spec          Spec
}

// RuntimeInstance describes a started sandbox instance.
type RuntimeInstance struct {
	Driver    string               `json:"driver"`
	SandboxID string               `json:"sandboxId"`
	ProjectID string               `json:"projectId,omitempty"`
	Container string               `json:"container"`
	State     string               `json:"state"`
	Mode      string               `json:"mode"`
	HomePath  string               `json:"homePath,omitempty"`
	Sandbox   orchestrator.Sandbox `json:"-"`
}

// ExecCommand describes a non-interactive command executed inside a sandbox.
type ExecCommand struct {
	Cmd     []string
	User    string
	Env     []string
	WorkDir string
	Stdin   []byte
}

// ExecOutput holds the exit code and bounded output of Exec.
type ExecOutput struct {
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

// RuntimeStats reports live sandbox state and resource consumption.
type RuntimeStats struct {
	Driver         string  `json:"driver"`
	ProjectID      string  `json:"projectId,omitempty"`
	SandboxID      string  `json:"sandboxId"`
	State          string  `json:"state"`
	ActiveSessions int     `json:"activeSessions"`
	CPUPercent     float64 `json:"cpuPercent"`
	MemUsedMB      int64   `json:"memUsedMB"`
	MemLimitMB     int64   `json:"memLimitMB"`
}
