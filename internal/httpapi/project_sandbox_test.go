package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type testPausableRuntime struct {
	mu      sync.Mutex
	started []string
	paused  map[string]bool
	removed []string
}

func (r *testPausableRuntime) StartSandbox(_ context.Context, spec orchestrator.SandboxSpec) (orchestrator.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused == nil {
		r.paused = map[string]bool{}
	}
	r.started = append(r.started, spec.ID)
	r.paused[spec.ID] = false
	return orchestrator.Sandbox{
		ID:        spec.ID,
		Kind:      spec.Kind,
		Container: orchestrator.SandboxName(spec.ID),
		Network:   orchestrator.SandboxName(spec.ID),
		Mode:      spec.Mode,
	}, nil
}

func (r *testPausableRuntime) CutSandbox(context.Context, string) error { return nil }

func (r *testPausableRuntime) AdoptSandbox(context.Context, string) error { return nil }

func (r *testPausableRuntime) SandboxAt(string, string) (string, bool) { return "", false }

func (r *testPausableRuntime) IsSandboxDoor(string) bool { return false }

func (r *testPausableRuntime) Sandboxes(context.Context) ([]orchestrator.SandboxInfo, error) {
	return nil, nil
}

func (r *testPausableRuntime) RemoveSandbox(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.paused, id)
	r.removed = append(r.removed, id)
	return nil
}

func (r *testPausableRuntime) ListSandboxes(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id := range r.paused {
		out = append(out, orchestrator.SandboxName(id))
	}
	return out, nil
}

func (r *testPausableRuntime) PauseSandbox(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused[id] = true
	return nil
}

func (r *testPausableRuntime) UnpauseSandbox(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused[id] = false
	return nil
}

func (r *testPausableRuntime) SandboxPaused(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused[id], nil
}

type xorTestSealer struct{}

func (xorTestSealer) Seal(_ string, data []byte) ([]byte, error) {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ 0x5a
	}
	return out, nil
}

func (xorTestSealer) Open(_ string, blob []byte) ([]byte, error) {
	out := make([]byte, len(blob))
	for i, b := range blob {
		out[i] = b ^ 0x5a
	}
	return out, nil
}

func TestProjectSandboxLifecycleSnapshotsImageAndSkillSyncAPI(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop([]string{}, nil, "one")

	rt := &testPausableRuntime{}
	mgr := &sandboxes.Manager{
		Runtime: rt,
		Pool:    k.b.Pool,
		Dir:     filepath.Join(t.TempDir(), "sandboxes"),
		Image:   "crew-runtime:test",
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	k.driver.Sandboxes = mgr
	k.driver.Seal = xorTestSealer{}

	type sbStatus struct {
		ProjectID      string                      `json:"projectId"`
		SandboxID      string                      `json:"sandboxId"`
		State          string                      `json:"state"`
		ImageTag       string                      `json:"imageTag"`
		SnapshotID     string                      `json:"snapshotId"`
		ActiveSessions int                         `json:"activeSessions"`
		Snapshots      []sandboxes.ProjectSnapshot `json:"snapshots"`
		Snapshot       sandboxes.ProjectSnapshot   `json:"snapshot"`
		BuiltImage     string                      `json:"builtImage"`
		Restored       string                      `json:"restored"`
	}

	// 1. Initial status is stopped.
	var st sbStatus
	k.call("GET", "/api/projects/shop/sandbox", "", 200, &st)
	if st.State != "stopped" || st.SandboxID != "" {
		t.Fatalf("initial status = %+v, want stopped", st)
	}

	// 2. Start project sandbox.
	k.call("POST", "/api/projects/shop/sandbox/start", `{}`, 200, &st)
	if st.State != "running" || st.SandboxID == "" {
		t.Fatalf("after start = %+v, want running", st)
	}
	firstSandboxID := st.SandboxID

	// 3. Live skill sync: saving a project skill writes directly into the persistent project home's .gemini/config/skills/.
	k.call("PUT", "/api/projects/shop/skills/file", `{"path":".agents/skills/shop-acp/SKILL.md","content":"---\nname: shop-acp\ndescription: \"ACP skill for shop.\"\n---\n\n# Shop ACP Skill\n"}`, 200, nil)
	syncedSkill := filepath.Join(mgr.ProjectHome(st.ProjectID), ".gemini", "config", "skills", "shop-acp", "SKILL.md")
	skillBytes, err := os.ReadFile(syncedSkill)
	if err != nil || !strings.Contains(string(skillBytes), "# Shop ACP Skill") {
		t.Fatalf("expected live synced skill at %s, got %q (err %v)", syncedSkill, string(skillBytes), err)
	}

	// 4. Active session blocks suspend without force (409 Conflict).
	if err := mgr.AcquireProjectSession(context.Background(), st.ProjectID); err != nil {
		t.Fatalf("AcquireProjectSession: %v", err)
	}
	k.call("POST", "/api/projects/shop/sandbox/suspend", `{}`, 409, nil)
	if err := mgr.ReleaseProjectSession(context.Background(), st.ProjectID); err != nil {
		t.Fatalf("ReleaseProjectSession: %v", err)
	}

	// 5. Suspend project sandbox (0% CPU) and resume via start.
	k.call("POST", "/api/projects/shop/sandbox/suspend", `{}`, 200, &st)
	if st.State != "paused" || st.SandboxID != firstSandboxID {
		t.Fatalf("after suspend = %+v, want paused on %s", st, firstSandboxID)
	}
	k.call("POST", "/api/projects/shop/sandbox/start", `{}`, 200, &st)
	if st.State != "running" || st.SandboxID != firstSandboxID {
		t.Fatalf("after unpause start = %+v, want running on %s", st, firstSandboxID)
	}

	// 6. Snapshot persistent project home, stop (0 MB RAM), and restore snapshot.
	markerFile := filepath.Join(mgr.ProjectHome(st.ProjectID), ".gemini", "antigravity-cli", "conversations", "c1.db")
	secretNote := filepath.Join(mgr.ProjectHome(st.ProjectID), ".gemini", "notes.txt")
	binFile := filepath.Join(mgr.ProjectHome(st.ProjectID), ".gemini", "bin.dat")
	_ = os.MkdirAll(filepath.Dir(markerFile), 0o755)
	_ = os.WriteFile(markerFile, []byte("sqlite-conv-state"), 0o600)
	_ = os.WriteFile(secretNote, []byte("API_TOKEN=fake_test_value_12345\n"), 0o600)
	_ = os.WriteFile(binFile, []byte("BIN\x00API_TOKEN=keep"), 0o600)

	k.call("POST", "/api/projects/shop/sandbox/snapshot", `{"label":"before-upgrade"}`, 201, &st)
	if st.Snapshot.ID == "" || len(st.Snapshots) != 1 {
		t.Fatalf("after snapshot = %+v", st)
	}
	snapID := st.Snapshot.ID

	k.call("POST", "/api/projects/shop/sandbox/stop", `{}`, 200, &st)
	if st.State != "stopped" || st.SandboxID != "" {
		t.Fatalf("after stop = %+v, want stopped", st)
	}

	_ = os.Remove(markerFile)
	_ = os.Remove(secretNote)
	_ = os.Remove(binFile)
	k.call("POST", "/api/projects/shop/sandbox/snapshots/"+snapID+"/restore", `{}`, 200, &st)
	if st.Restored != snapID || st.SnapshotID != snapID {
		t.Fatalf("after restore = %+v", st)
	}
	if b, err := os.ReadFile(markerFile); err != nil || string(b) != "sqlite-conv-state" {
		t.Fatalf("restored markerFile = %q, err = %v", string(b), err)
	}
	if b, err := os.ReadFile(secretNote); err != nil || strings.Contains(string(b), "fake_test_value_12345") {
		t.Fatalf("restored secretNote leaked unredacted text = %q, err = %v", string(b), err)
	}
	if b, err := os.ReadFile(binFile); err != nil || string(b) != "BIN\x00API_TOKEN=keep" {
		t.Fatalf("restored binFile was altered = %q, err = %v", string(b), err)
	}

	// 7. Build project image requires fresh sign-in, validates single FROM crew-runtime, rejects external/multi-stage sources, and ignores client tags.
	if code, body := do(t, k.srv, "POST", "/api/projects/shop/sandbox/build-image", signedInAt(stale, ""), `{"dockerfile":"FROM crew-runtime:2026.11\nRUN echo ok\n"}`); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("stale sandbox build-image = %d %s, want 401 %s", code, body, httpapi.ReasonFresh)
	}
	for _, badDF := range []string{
		"FROM ubuntu:24.04\n",
		"FROM crew-runtime-other:1.0\n",
		"FROM crew-runtime:2026.11\nFROM alpine:3.20\n",
		"FROM crew-runtime:2026.11\nFROM crew-runtime:2026.11\n",
		"FROM crew-runtime:2026.11\nCOPY --from=alpine:3.20 /etc/passwd /tmp/p\n",
		"FROM crew-runtime:2026.11\nADD --from=busybox /bin/sh /tmp/sh\n",
		"FROM crew-runtime:2026.11\nRUN --mount=type=bind,from=ubuntu:24.04,target=/m ls /m\n",
		"FROM crew-runtime:2026.11\nCOPY \\\n --from=alpine:3.20 /a /b\n",
		"# escape=`\nFROM crew-runtime:2026.11\nCOPY `\n--from=alpine:3.20 /etc/passwd /tmp/p\n",
		"# syntax=docker/dockerfile:1\nFROM crew-runtime:2026.11\nRUN echo ok\n",
		"ARG BASE=alpine:3.20\nFROM $BASE\n",
		"ARG IMG=crew-runtime:2026.11\nFROM ${IMG}\n",
		"FROM crew-runtime:2026.11\nADD https://example.com/payload.sh /tmp/p.sh\n",
		"FROM crew-runtime:2026.11\nRUN --network=host id\n",
		"FROM crew-runtime:2026.11\nRUN --security=insecure id\n",
		"FROM crew-runtime:2026.11\nRUN --mount=type=secret,id=s cat /run/secrets/s\n",
		"FROM crew-runtime:2026.11\nRUN --mount=type=ssh ssh-add -l\n",
		"FROM crew-runtime:2026.11\nONBUILD RUN id\n",
		"FROM crew-runtime:2026.11\nUNKNOWN_INSTRUCTION foo\n",
	} {
		payload := `{"dockerfile":` + `"` + strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t", "\"", "\\\"").Replace(badDF) + `"` + `}`
		k.call("POST", "/api/projects/shop/sandbox/build-image", payload, 400, nil)
	}
	k.call("POST", "/api/projects/shop/sandbox/build-image", `{"dockerfile":"FROM crew-runtime:2026.11\nRUN echo ok\n","tag":"evil:unvalidated"}`, 200, &st)
	if !strings.HasPrefix(st.ImageTag, "crew-project-runtime:sf-") {
		t.Fatalf("imageTag = %q, want crew-project-runtime:sf-*", st.ImageTag)
	}

	// 8. Top-level GET /api/sandboxes and PUT /api/capacity/active-tickets.
	k.call("PUT", "/api/capacity/active-tickets", `{"max":0}`, 400, nil)
	k.call("PUT", "/api/capacity/active-tickets", `{"max":65}`, 400, nil)
	k.call("PUT", "/api/capacity/active-tickets", `{"max":8}`, 204, nil)

	var all httpapi.AllSandboxesView
	k.call("GET", "/api/sandboxes", "", 200, &all)
	if all.Machine.SetMaxActiveTickets != 8 || all.Machine.MaxActiveTickets != 8 {
		t.Fatalf("expected machine maxActiveTickets=8 on GET /api/sandboxes, got %+v", all.Machine)
	}
	var psView *httpapi.ProjectSandboxOverview
	for i := range all.ProjectSandboxes {
		if all.ProjectSandboxes[i].Project == "shop" {
			psView = &all.ProjectSandboxes[i]
			break
		}
	}
	if psView == nil {
		t.Fatalf("expected shop project sandbox in GET /api/sandboxes, got %+v", all.ProjectSandboxes)
	}
	if psView.ProjectKey != "SF" || psView.CPUs != 6 || psView.MemoryGB != 8 || psView.DiskGB != 20 || psView.MaxActiveTickets != 4 || psView.SnapshotsCount != 1 {
		t.Fatalf("unexpected project sandbox view: %+v", *psView)
	}

	var projSb struct {
		ActiveTickets struct {
			Active        int `json:"active"`
			Max           int `json:"max"`
			Set           int `json:"set"`
			ProjectActive int `json:"projectActive"`
			ProjectMax    int `json:"projectMax"`
		} `json:"activeTickets"`
	}
	k.call("GET", "/api/projects/shop/sandboxes", "", 200, &projSb)
	if projSb.ActiveTickets.Max != 8 || projSb.ActiveTickets.Set != 8 || projSb.ActiveTickets.ProjectMax != 4 {
		t.Fatalf("unexpected activeTickets on GET /api/projects/shop/sandboxes: %+v", projSb.ActiveTickets)
	}
}
