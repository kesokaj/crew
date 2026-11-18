package sandboxes_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type fakeComputeOrchestrator struct {
	*fakePausableRuntime
	execCalls []string
	stats     orchestrator.ContainerStats
}

func newComputeFake() *fakeComputeOrchestrator {
	return &fakeComputeOrchestrator{
		fakePausableRuntime: newPausableFake(),
		stats: orchestrator.ContainerStats{
			CPUPercent: 14.2,
			MemUsedMB:  512,
			MemLimitMB: 6144,
		},
	}
}

func (f *fakeComputeOrchestrator) Exec(_ context.Context, containerID string, spec orchestrator.ExecSpec) (int, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, containerID+":"+strings.Join(spec.Cmd, " "))
	return 0, "ok", nil
}

func (f *fakeComputeOrchestrator) SandboxStats(_ context.Context, _ string) (orchestrator.ContainerStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, nil
}

func TestDockerKataComputeRuntimeLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	rt := newComputeFake()
	mgr := &sandboxes.Manager{
		Runtime: rt,
		Pool:    pool,
		Dir:     filepath.Join(t.TempDir(), "sandboxes"),
		Image:   "crew-runtime:test",
		Log:     slog.New(slog.DiscardHandler),
	}
	seal := testSealer{key: "m21-key"}
	var cr sandboxes.ComputeRuntime = mgr.NewComputeRuntime(seal, nil)
	if cr.Driver() != sandboxes.DriverDockerKata {
		t.Fatalf("Driver() = %q, want %q", cr.Driver(), sandboxes.DriverDockerKata)
	}

	// 1. Start warm project VM + Exec inside it.
	inst, err := cr.Start(ctx, sandboxes.StartRequest{
		ProjectID:     fixtureProjectID,
		WarmProjectVM: true,
		Spec: sandboxes.Spec{
			Kind:  sandboxes.KindRun,
			CLI:   "agy",
			RunID: "run-a",
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.SandboxID == "" || inst.State != sandboxes.StateRunning {
		t.Fatalf("unexpected Start instance: %+v", inst)
	}
	out, err := cr.Exec(ctx, inst.SandboxID, sandboxes.ExecCommand{Cmd: []string{"echo", "hi"}, User: "agent"})
	if err != nil || out.ExitCode != 0 || out.Output != "ok" {
		t.Fatalf("Exec: %+v err=%v", out, err)
	}

	// Write both persistent session state and an excluded credential file.
	convFile := filepath.Join(inst.HomePath, ".gemini", "antigravity-cli", "conversations", "peer-1.db")
	if err := os.MkdirAll(filepath.Dir(convFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(convFile, []byte("peer-state"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Active session prevents non-forced Suspend, then Suspend (PauseSandbox) + Resume (UnpauseSandbox) work cleanly.
	if err := mgr.AcquireProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := cr.Suspend(ctx, fixtureProjectID, false); !errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
		t.Fatalf("Suspend while busy = %v, want ErrProjectSandboxBusy", err)
	}
	if err := mgr.ReleaseProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	suspended, err := cr.Suspend(ctx, fixtureProjectID, false)
	if err != nil || suspended.State != sandboxes.StatePaused {
		t.Fatalf("Suspend: %+v err=%v", suspended, err)
	}
	if paused, _ := rt.SandboxPaused(ctx, inst.SandboxID); !paused {
		t.Fatal("expected underlying orchestrator PauseSandbox state")
	}

	resumed, err := cr.Resume(ctx, fixtureProjectID, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy", RunID: "run-b"})
	if err != nil || resumed.State != sandboxes.StateRunning || resumed.SandboxID != inst.SandboxID {
		t.Fatalf("Resume: %+v err=%v", resumed, err)
	}
	if paused, _ := rt.SandboxPaused(ctx, inst.SandboxID); paused {
		t.Fatal("expected underlying orchestrator UnpauseSandbox state")
	}

	// 3. Stats, Snapshot (credential-stripped + sealed), Restore, and Destroy.
	st, err := cr.Stats(ctx, fixtureProjectID)
	if err != nil || st.MemUsedMB != 512 || st.CPUPercent != 14.2 {
		t.Fatalf("Stats: %+v err=%v", st, err)
	}
	snap, err := cr.Snapshot(ctx, fixtureProjectID, "m21-pre-destroy")
	if err != nil || snap.ID == "" {
		t.Fatalf("Snapshot: %+v err=%v", snap, err)
	}
	if err := os.WriteFile(convFile, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := cr.Restore(ctx, fixtureProjectID, snap.ID)
	if err != nil || restored.SnapshotID != snap.ID {
		t.Fatalf("Restore: %+v err=%v", restored, err)
	}
	b, err := os.ReadFile(convFile)
	if err != nil || string(b) != "peer-state" {
		t.Fatalf("restored conv file = %q, err=%v", b, err)
	}
	if err := cr.Destroy(ctx, fixtureProjectID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	finalPS, err := mgr.GetProjectSandbox(ctx, fixtureProjectID)
	if err != nil || finalPS.State != sandboxes.StateStopped {
		t.Fatalf("final project sandbox = %+v err=%v", finalPS, err)
	}
}

func TestKubernetesKataSeamImplementsComputeRuntime(t *testing.T) {
	t.Parallel()
	var cr sandboxes.ComputeRuntime = sandboxes.NewKubernetesKataRuntime("crew", nil)
	if cr.Driver() != sandboxes.DriverKubernetesKata {
		t.Fatalf("Driver() = %q", cr.Driver())
	}
	if _, err := cr.Start(context.Background(), sandboxes.StartRequest{}); !errors.Is(err, sandboxes.ErrKubernetesNotConfigured) {
		t.Fatalf("expected ErrKubernetesNotConfigured, got %v", err)
	}
}
