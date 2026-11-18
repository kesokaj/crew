package sandboxes

import (
	"context"
	"errors"
)

// ErrKubernetesNotConfigured is returned by the Kubernetes ComputeRuntime seam when no cluster
// driver is configured (dave runs Docker + Kata Tier-1 exclusively).
var ErrKubernetesNotConfigured = errors.New("kubernetes kata compute runtime is not configured on this host")

// BlobSnapshotStore abstracts sealed home snapshot offload for Kubernetes RuntimeClass=kata
// pod suspend/resume across cluster nodes (design 13.12, R219).
type BlobSnapshotStore interface {
	PutSnapshot(ctx context.Context, key string, sealedArchive []byte) error
	GetSnapshot(ctx context.Context, key string) ([]byte, error)
}

// KubernetesKataRuntime defines the cluster-driver seam implementing ComputeRuntime with
// `RuntimeClass: kata` and BlobStore-backed home snapshots (design 13.12, R219).
type KubernetesKataRuntime struct {
	Namespace        string
	RuntimeClassName string
	Snapshots        BlobSnapshotStore
}

// NewKubernetesKataRuntime returns an unconfigured Kubernetes RuntimeClass=kata seam.
func NewKubernetesKataRuntime(namespace string, snapshots BlobSnapshotStore) *KubernetesKataRuntime {
	return &KubernetesKataRuntime{
		Namespace:        namespace,
		RuntimeClassName: "kata",
		Snapshots:        snapshots,
	}
}

func (k *KubernetesKataRuntime) Driver() string { return DriverKubernetesKata }

func (k *KubernetesKataRuntime) Start(context.Context, StartRequest) (RuntimeInstance, error) {
	return RuntimeInstance{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Exec(context.Context, string, ExecCommand) (ExecOutput, error) {
	return ExecOutput{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Suspend(context.Context, string, bool) (ProjectSandbox, error) {
	return ProjectSandbox{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Resume(context.Context, string, Spec) (ProjectSandbox, error) {
	return ProjectSandbox{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Snapshot(context.Context, string, string) (ProjectSnapshot, error) {
	return ProjectSnapshot{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Restore(context.Context, string, string) (ProjectSandbox, error) {
	return ProjectSandbox{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Stats(context.Context, string) (RuntimeStats, error) {
	return RuntimeStats{}, ErrKubernetesNotConfigured
}

func (k *KubernetesKataRuntime) Destroy(context.Context, string) error {
	return ErrKubernetesNotConfigured
}
