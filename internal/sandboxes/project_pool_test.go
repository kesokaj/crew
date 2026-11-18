package sandboxes_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type fakePausableRuntime struct {
	*fakeRuntime
	paused map[string]bool
}

func newPausableFake() *fakePausableRuntime {
	return &fakePausableRuntime{
		fakeRuntime: newFake(),
		paused:      map[string]bool{},
	}
}

func (f *fakePausableRuntime) PauseSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running[id] {
		return errors.New("container not running")
	}
	f.paused[id] = true
	return nil
}

func (f *fakePausableRuntime) UnpauseSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.paused, id)
	return nil
}

func (f *fakePausableRuntime) SandboxPaused(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused[id], nil
}

type testSealer struct {
	key string
}

func (s testSealer) Seal(purpose string, data []byte) ([]byte, error) {
	prefix := []byte("SEALED:" + s.key + ":" + purpose + "\n")
	return append(prefix, data...), nil
}

func (s testSealer) Open(purpose string, blob []byte) ([]byte, error) {
	prefix := []byte("SEALED:" + s.key + ":" + purpose + "\n")
	if !bytes.HasPrefix(blob, prefix) {
		return nil, errors.New("invalid seal")
	}
	return bytes.TrimPrefix(blob, prefix), nil
}

const fixtureProjectID = "f1c50000-0000-4000-8000-000000000001"

func TestProjectSandboxStartSuspendStopAndReap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	rt := newPausableFake()
	mgr := &sandboxes.Manager{
		Runtime: rt,
		Pool:    pool,
		Dir:     filepath.Join(t.TempDir(), "sandboxes"),
		Image:   "crew-runtime:test",
		Log:     slog.New(slog.DiscardHandler),
	}

	// 1. EnsureProjectSandbox boots a warm microVM on persistent home, forces Minimal=false, and writes per-run token
	// distinct from the sandbox-level .crew/token.
	var run1PreparedHash string
	ps, sb, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{
		Kind:    sandboxes.KindRun,
		CLI:     "agy",
		RunID:   "run-1",
		Minimal: true,
		Prepare: func(id, home, hash string) error {
			run1PreparedHash = hash
			return nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureProjectSandbox start: %v", err)
	}
	if ps.State != sandboxes.StateRunning || ps.SandboxID == "" || sb.ID != ps.SandboxID {
		t.Fatalf("unexpected project sandbox after start: %+v, sb=%+v", ps, sb)
	}
	if !strings.HasPrefix(ps.HomePath, mgr.Dir+string(filepath.Separator)) {
		t.Fatalf("expected project home %q inside sandboxes dir %q", ps.HomePath, mgr.Dir)
	}
	if len(rt.specs) != 1 || rt.specs[0].Minimal {
		t.Fatalf("expected Minimal=false on project VM, got %+v", rt.specs)
	}
	rootToken1, err := os.ReadFile(filepath.Join(ps.HomePath, ".crew", "token"))
	if err != nil || strings.TrimSpace(string(rootToken1)) == "" {
		t.Fatalf("expected .crew/token, got err=%v", err)
	}
	run1Token, err := os.ReadFile(filepath.Join(ps.HomePath, ".crew", "runs", "run-1", "token"))
	if err != nil || strings.TrimSpace(string(run1Token)) == "" {
		t.Fatalf("expected .crew/runs/run-1/token, got err=%v", err)
	}
	if string(rootToken1) == string(run1Token) {
		t.Fatalf("expected .crew/token to be distinct from per-run .crew/runs/run-1/token")
	}
	if run1PreparedHash != sandboxes.Hash(strings.TrimSpace(string(run1Token))) {
		t.Fatalf("expected Prepare hash %s to match .crew/runs/run-1/token hash, not .crew/token", run1PreparedHash)
	}
	markerFile := filepath.Join(ps.HomePath, ".gemini", "antigravity-cli", "conversations", "sess-1.db")
	if err := os.MkdirAll(filepath.Dir(markerFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerFile, []byte("sqlite-session-state"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Second EnsureProjectSandbox reuses the same running sandbox without starting a second VM,
	// writes .crew/runs/run-2/token, and does not overwrite .crew/token.
	ps2, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{
		Kind:    sandboxes.KindRun,
		CLI:     "agy",
		RunID:   "run-2",
		Prepare: func(id, home, hash string) error { return nil },
	})
	if err != nil {
		t.Fatalf("EnsureProjectSandbox reuse: %v", err)
	}
	if ps2.SandboxID != ps.SandboxID || len(rt.specs) != 1 {
		t.Fatalf("expected 1 VM started and reused, got specs=%d id1=%s id2=%s", len(rt.specs), ps.SandboxID, ps2.SandboxID)
	}
	rootTokenAfter, _ := os.ReadFile(filepath.Join(ps.HomePath, ".crew", "token"))
	if string(rootTokenAfter) != string(rootToken1) {
		t.Fatalf("reused session overwrote .crew/token: got %q, want %q", string(rootTokenAfter), string(rootToken1))
	}
	run2Token, err := os.ReadFile(filepath.Join(ps.HomePath, ".crew", "runs", "run-2", "token"))
	if err != nil || strings.TrimSpace(string(run2Token)) == "" || string(run2Token) == string(run1Token) {
		t.Fatalf("expected distinct .crew/runs/run-2/token, got %q err=%v", string(run2Token), err)
	}

	// 3. Active session blocks non-forced PauseProjectSandbox and mode/image-mismatch EnsureProjectSandbox with ErrProjectSandboxBusy.
	if err := mgr.AcquireProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.PauseProjectSandbox(ctx, fixtureProjectID, false); !errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
		t.Fatalf("expected ErrProjectSandboxBusy while session active, got %v", err)
	}
	if _, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy", Mode: "model_only"}); !errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
		t.Fatalf("expected ErrProjectSandboxBusy on mode mismatch while session active, got %v", err)
	}
	if err := mgr.ReleaseProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}

	// 4. PauseProjectSandbox suspends the container (ContainerPause).
	pausedPS, err := mgr.PauseProjectSandbox(ctx, fixtureProjectID, false)
	if err != nil {
		t.Fatalf("PauseProjectSandbox: %v", err)
	}
	if pausedPS.State != sandboxes.StatePaused {
		t.Fatalf("expected state paused, got %s", pausedPS.State)
	}
	if paused, _ := rt.SandboxPaused(ctx, ps.SandboxID); !paused {
		t.Fatalf("expected fake runtime container to be paused")
	}

	// 5. EnsureProjectSandbox on a paused VM unpauses it in-place without booting a new container.
	unpausedPS, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	if err != nil {
		t.Fatalf("EnsureProjectSandbox unpause: %v", err)
	}
	if unpausedPS.State != sandboxes.StateRunning || unpausedPS.SandboxID != ps.SandboxID || len(rt.specs) != 1 {
		t.Fatalf("expected unpause of same container, got %+v specs=%d", unpausedPS, len(rt.specs))
	}

	// 6. StopProjectSandbox removes the container (0 MB RAM) while preserving the persistent project home.
	stoppedPS, err := mgr.StopProjectSandbox(ctx, fixtureProjectID)
	if err != nil {
		t.Fatalf("StopProjectSandbox: %v", err)
	}
	if stoppedPS.State != sandboxes.StateStopped || stoppedPS.SandboxID != "" {
		t.Fatalf("expected stopped state with empty sandboxId, got %+v", stoppedPS)
	}
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("expected persistent conversation db to survive StopProjectSandbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ps.HomePath, ".crew", "token")); !os.IsNotExist(err) {
		t.Fatalf("expected .crew/token to be removed on stop")
	}

	// 7. ReapIdleProjectSandboxes transitions running -> paused -> stopped.
	ps3, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE project_sandboxes SET last_active_at = now() - interval '10 minutes' WHERE project_id = $1::uuid`, fixtureProjectID)
	if err != nil {
		t.Fatal(err)
	}
	pCount, sCount, err := mgr.ReapIdleProjectSandboxes(ctx, 5*time.Minute, 30*time.Minute)
	if err != nil || pCount != 1 || sCount != 0 {
		t.Fatalf("expected 1 paused and 0 stopped, got p=%d s=%d err=%v", pCount, sCount, err)
	}
	_, err = pool.Exec(ctx, `UPDATE project_sandboxes SET last_active_at = now() - interval '40 minutes' WHERE project_id = $1::uuid`, fixtureProjectID)
	if err != nil {
		t.Fatal(err)
	}
	pCount, sCount, err = mgr.ReapIdleProjectSandboxes(ctx, 5*time.Minute, 30*time.Minute)
	if err != nil || pCount != 0 || sCount != 1 {
		t.Fatalf("expected 0 paused and 1 stopped, got p=%d s=%d err=%v", pCount, sCount, err)
	}
	afterReap, err := mgr.GetProjectSandbox(ctx, fixtureProjectID)
	if err != nil || afterReap.State != sandboxes.StateStopped || afterReap.SandboxID != "" {
		t.Fatalf("expected stopped after reap, got %+v (was %s)", afterReap, ps3.SandboxID)
	}
}

func TestProjectSnapshotStripsCredentialsVerifiesSHA256AndRestores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := dbtest.New(t, false).Server
	rt := newPausableFake()
	mgr := &sandboxes.Manager{
		Runtime: rt,
		Pool:    pool,
		Dir:     filepath.Join(t.TempDir(), "sandboxes"),
		Image:   "crew-runtime:test",
		Log:     slog.New(slog.DiscardHandler),
	}
	seal := testSealer{key: "secret-key-1"}

	ps, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{Kind: sandboxes.KindRun, CLI: "agy"})
	if err != nil {
		t.Fatal(err)
	}

	// Populate persistent home with a skill, a conversation DB, and sensitive credential files that must be stripped.
	skillFile := filepath.Join(ps.HomePath, ".gemini", "config", "skills", "custom-skill", "SKILL.md")
	oauthFile := filepath.Join(ps.HomePath, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oauthFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("# Custom Skill\ntoken=SUPER_SECRET_VALUE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oauthFile, []byte("oauth-refresh-token-do-not-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	credsEnvFile := filepath.Join(ps.HomePath, ".crew", "creds.env")
	runTokFile := filepath.Join(ps.HomePath, ".crew", "runs", "r1", "token")
	if err := os.MkdirAll(filepath.Dir(runTokFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credsEnvFile, []byte("GITHUB_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runTokFile, []byte("run-token-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerImgFile := filepath.Join(ps.HomePath, ".dockerd", fixtureProjectID+".img")
	if err := os.MkdirAll(filepath.Dir(dockerImgFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerImgFile, []byte("ext4-loopback"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(dockerImgFile, 600<<20); err != nil {
		t.Fatal(err)
	}
	browserProfile := filepath.Join(ps.HomePath, ".crew", "browser", "profile", "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(browserProfile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(browserProfile, []byte("browser-cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	wtConfig := filepath.Join(ps.HomePath, "repo.git", "worktrees", "r1", "config.worktree")
	if err := os.MkdirAll(filepath.Dir(wtConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtConfig, []byte("extraHeader = Authorization: Bearer secret-run-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create a symlink inside home pointing to an outside secret file and a hard link inside home to a credential file:
	// SnapshotProjectHome must skip both via OpenRoot/Lstat and Nlink > 1 check.
	outsideSecret := filepath.Join(t.TempDir(), "host-secret.key")
	if err := os.WriteFile(outsideSecret, []byte("HOST_SECRET_KEY_BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSecret, filepath.Join(ps.HomePath, "escaped-link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(oauthFile, filepath.Join(ps.HomePath, "hardlinked-oauth.txt")); err != nil {
		t.Fatal(err)
	}

	// SnapshotProjectHome must refuse while sessions are active (ErrProjectSandboxBusy).
	if err := mgr.AcquireProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SnapshotProjectHome(ctx, fixtureProjectID, "busy-attempt", seal, nil); !errors.Is(err, sandboxes.ErrProjectSandboxBusy) {
		t.Fatalf("expected ErrProjectSandboxBusy when snapshotting with active session, got %v", err)
	}
	if err := mgr.ReleaseProjectSession(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}

	var sawPausedDuringSnapshot bool
	snap, err := mgr.SnapshotProjectHome(ctx, fixtureProjectID, "before-upgrade", seal, func(b []byte) []byte {
		if paused, _ := rt.SandboxPaused(ctx, ps.SandboxID); paused {
			sawPausedDuringSnapshot = true
		}
		if bytes.Contains(b, []byte("HOST_SECRET_KEY_BYTES")) {
			t.Errorf("snapshot read outside secret through symlink!")
		}
		if bytes.Contains(b, []byte("oauth-refresh-token-do-not-archive")) {
			t.Errorf("snapshot read hard-linked credential file!")
		}
		return bytes.ReplaceAll(b, []byte("SUPER_SECRET_VALUE"), []byte("[REDACTED]"))
	})
	if err != nil {
		t.Fatalf("SnapshotProjectHome: %v", err)
	}
	if !sawPausedDuringSnapshot {
		t.Fatalf("expected running VM to be paused while SnapshotProjectHome reads home")
	}
	if paused, _ := rt.SandboxPaused(ctx, ps.SandboxID); paused {
		t.Fatalf("expected running VM to be unpaused after SnapshotProjectHome completes")
	}
	if snap.ID == "" || len(snap.SHA256) != 64 || snap.SizeBytes <= 0 {
		t.Fatalf("unexpected snapshot metadata: %+v", snap)
	}

	// Modify the live home and verify that a corrupted archive checksum is rejected BEFORE touching the live home.
	if err := os.WriteFile(skillFile, []byte("modified-in-place"), 0o644); err != nil {
		t.Fatal(err)
	}
	origBytes, err := os.ReadFile(snap.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snap.ArchivePath, append([]byte("corrupt"), origBytes...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RestoreProjectHome(ctx, fixtureProjectID, snap.ID, seal); !errors.Is(err, sandboxes.ErrSnapshotChecksum) {
		t.Fatalf("expected ErrSnapshotChecksum on corrupted archive, got %v", err)
	}
	currentSkill, err := os.ReadFile(skillFile)
	if err != nil || string(currentSkill) != "modified-in-place" {
		t.Fatalf("corrupted restore must not touch existing home; got %q", string(currentSkill))
	}

	// Put back valid archive bytes and restore.
	if err := os.WriteFile(snap.ArchivePath, origBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredPS, err := mgr.RestoreProjectHome(ctx, fixtureProjectID, snap.ID, seal)
	if err != nil {
		t.Fatalf("RestoreProjectHome: %v", err)
	}
	if restoredPS.SnapshotID != snap.ID || restoredPS.State != sandboxes.StateStopped {
		t.Fatalf("unexpected state after restore: %+v", restoredPS)
	}
	restoredSkill, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("reading restored skill file: %v", err)
	}
	if string(restoredSkill) != "# Custom Skill\ntoken=[REDACTED]\n" {
		t.Fatalf("expected redacted skill content after restore, got %q", string(restoredSkill))
	}
	if _, err := os.Stat(oauthFile); !os.IsNotExist(err) {
		t.Fatalf("expected oauth token file to be stripped from snapshot archive")
	}
	if _, err := os.Stat(credsEnvFile); !os.IsNotExist(err) {
		t.Fatalf("expected .crew/creds.env to be stripped from snapshot archive")
	}
	if _, err := os.Stat(runTokFile); !os.IsNotExist(err) {
		t.Fatalf("expected .crew/runs/r1/token to be stripped from snapshot archive")
	}
	if _, err := os.Stat(browserProfile); !os.IsNotExist(err) {
		t.Fatalf("expected .crew/browser profile to be stripped from snapshot archive")
	}
	if _, err := os.Stat(wtConfig); !os.IsNotExist(err) {
		t.Fatalf("expected repo.git/worktrees config.worktree to be stripped from snapshot archive")
	}
	if st, err := os.Stat(dockerImgFile); err != nil || st.Size() != 600<<20 {
		t.Fatalf("expected .dockerd loopback image to be preserved across restore: stat=%v err=%v", st, err)
	}

	// Replacing .dockerd with a symlink in the active home must not be preserved across RestoreProjectHome.
	if err := os.RemoveAll(filepath.Join(ps.HomePath, ".dockerd")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(ps.HomePath, ".dockerd")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RestoreProjectHome(ctx, fixtureProjectID, snap.ID, seal); err != nil {
		t.Fatalf("RestoreProjectHome with symlink .dockerd: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(ps.HomePath, ".dockerd")); !os.IsNotExist(err) {
		t.Fatalf("expected symlink .dockerd to be discarded on restore, got err=%v", err)
	}

	// Creating more than DefaultMaxSnapshots (5) prunes the oldest snapshots and their archive files.
	for i := 0; i < 6; i++ {
		if _, err := mgr.SnapshotProjectHome(ctx, fixtureProjectID, "extra", seal, nil); err != nil {
			t.Fatalf("extra snapshot %d: %v", i, err)
		}
	}
	kept, err := mgr.ListProjectSnapshots(ctx, fixtureProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != sandboxes.DefaultMaxSnapshots {
		t.Fatalf("expected %d kept snapshots after pruning, got %d", sandboxes.DefaultMaxSnapshots, len(kept))
	}
}

func TestProjectSandboxSizingAndLifecycleOverrides(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	rt := newPausableFake()
	dir := t.TempDir()
	mgr := &sandboxes.Manager{
		Pool:    d.Server,
		Runtime: rt,
		Dir:     dir,
		Image:   "crew-runtime:test",
		Log:     slog.New(slog.DiscardHandler),
	}

	// Default project sizing (6 vCPU, 8 GiB -> 5e9 NanoCPUs, 6 GiB MemBytes above Kata's 1 vCPU / 2 GiB base)
	// overrides a small role's Sizing passed in Spec.
	ps, _, err := mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{
		Kind:   sandboxes.KindRun,
		RunID:  "run-small-lead",
		Sizing: orchestrator.Sizing{NanoCPUs: 1e9, MemBytes: 0},
	})
	if err != nil {
		t.Fatalf("EnsureProjectSandbox default sizing: %v", err)
	}
	if len(rt.specs) != 1 || rt.specs[0].Sizing.NanoCPUs != 5e9 || rt.specs[0].Sizing.MemBytes != 6<<30 {
		t.Fatalf("expected default 6 vCPU / 8 GiB sizing (5e9, 6 GiB), got %+v", rt.specs)
	}
	if _, err := mgr.StopProjectSandbox(ctx, fixtureProjectID); err != nil {
		t.Fatal(err)
	}

	// Configure custom per-project sandbox settings in settings table.
	if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by)
		VALUES ($1::uuid, '{"sandbox":{"cpus":8,"memory_gb":12,"disk_gb":50,"suspend_after_minutes":2,"stop_after_minutes":4,"max_snapshots":2}}'::jsonb, 'op@example.com')`, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	cfg := mgr.ProjectConfigFor(ctx, fixtureProjectID)
	if cfg.CPUs != 8 || cfg.MemoryGB != 12 || cfg.DiskGB != 50 || cfg.SuspendAfterMinutes != 2 || cfg.StopAfterMinutes != 4 || cfg.MaxSnapshots != 2 {
		t.Fatalf("unexpected ProjectConfigFor: %+v", cfg)
	}

	ps, _, err = mgr.EnsureProjectSandbox(ctx, fixtureProjectID, sandboxes.Spec{
		Kind:  sandboxes.KindRun,
		RunID: "run-custom-size",
	})
	if err != nil {
		t.Fatalf("EnsureProjectSandbox custom sizing: %v", err)
	}
	last := rt.specs[len(rt.specs)-1]
	if last.Sizing.NanoCPUs != 7e9 || last.Sizing.MemBytes != 10<<30 {
		t.Fatalf("expected custom 8 vCPU / 12 GiB sizing (7e9, 10 GiB), got %+v", last.Sizing)
	}

	// Per-project suspend_after_minutes: 2 overrides a 60m machine default when idle for 3 minutes.
	if _, err := d.Server.Exec(ctx, `UPDATE project_sandboxes SET last_active_at = now() - interval '3 minutes' WHERE project_id = $1::uuid`, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	pausedCount, stoppedCount, err := mgr.ReapIdleProjectSandboxes(ctx, 60*time.Minute, 120*time.Minute)
	if err != nil || pausedCount != 1 || stoppedCount != 0 {
		t.Fatalf("expected per-project 2m suspend override to pause sandbox, got paused=%d stopped=%d err=%v", pausedCount, stoppedCount, err)
	}

	// Per-project stop_after_minutes: 4 overrides a 120m machine default when idle for 5 minutes.
	if _, err := d.Server.Exec(ctx, `UPDATE project_sandboxes SET last_active_at = now() - interval '5 minutes' WHERE project_id = $1::uuid`, fixtureProjectID); err != nil {
		t.Fatal(err)
	}
	pausedCount, stoppedCount, err = mgr.ReapIdleProjectSandboxes(ctx, 60*time.Minute, 120*time.Minute)
	if err != nil || pausedCount != 0 || stoppedCount != 1 {
		t.Fatalf("expected per-project 4m stop override to stop sandbox, got paused=%d stopped=%d err=%v", pausedCount, stoppedCount, err)
	}
	_ = ps
}
