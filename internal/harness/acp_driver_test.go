package harness

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type fakeProjectPool struct {
	mu        sync.Mutex
	home      string
	ensured   int
	active    int
	released  int
	lastSpecs []sandboxes.Spec
}

func (p *fakeProjectPool) EnsureProjectSandbox(_ context.Context, projectID string, spec sandboxes.Spec) (sandboxes.ProjectSandbox, orchestrator.Sandbox, error) {
	p.mu.Lock()
	p.ensured++
	p.lastSpecs = append(p.lastSpecs, spec)
	home := p.home
	p.mu.Unlock()
	id := "proj-test0001"
	os.MkdirAll(filepath.Join(home, ".crew"), 0o700)
	os.WriteFile(filepath.Join(home, ".crew", "token"), []byte("tok\n"), 0o600)
	if spec.RunID != "" {
		ctrl := filepath.Join(home, runspec.RunControlDir(spec.RunID))
		os.MkdirAll(ctrl, 0o700)
		os.WriteFile(filepath.Join(ctrl, "token"), []byte("tok-"+spec.RunID+"\n"), 0o600)
	}
	if spec.Prepare != nil {
		if err := spec.Prepare(id, home, "hash-"+id); err != nil {
			return sandboxes.ProjectSandbox{}, orchestrator.Sandbox{}, err
		}
	}
	return sandboxes.ProjectSandbox{
		ProjectID: projectID,
		SandboxID: id,
		State:     sandboxes.StateRunning,
		HomePath:  home,
	}, orchestrator.Sandbox{
		ID:        id,
		Container: "crew-" + id,
		Door:      "10.9.1.254:18091",
	}, nil
}

func (p *fakeProjectPool) AcquireProjectSession(_ context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active++
	return nil
}

func (p *fakeProjectPool) ReleaseProjectSession(_ context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
	p.released++
	return nil
}

func TestKataProjectACPRunSteerAndSharedSandboxRelease(t *testing.T) {
	sb := &fakeSandboxes{dir: t.TempDir(), door: "10.9.1.254:18091"}
	rt := &fakeRuntime{}
	pool := &fakeProjectPool{home: filepath.Join(t.TempDir(), "proj-home")}
	k := &Kata{
		Sandboxes:        sb,
		ProjectSandboxes: pool,
		Runtime:          rt,
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		Poll:             10 * time.Millisecond,
		AliveEvery:       50 * time.Millisecond,
	}

	s1 := spec()
	s1.Run = "run-acp-1"
	s1.Project = "11111111-1111-4111-8111-111111111111"
	h1, err := k.StartRun(context.Background(), s1)
	if err != nil {
		t.Fatalf("StartRun s1: %v", err)
	}
	if h1.Sandbox != "proj-test0001" {
		t.Fatalf("h1.Sandbox = %q, want proj-test0001", h1.Sandbox)
	}

	s2 := spec()
	s2.Run = "run-acp-2"
	s2.Project = s1.Project
	h2, err := k.StartRun(context.Background(), s2)
	if err != nil {
		t.Fatalf("StartRun s2: %v", err)
	}
	if h2.Sandbox != h1.Sandbox {
		t.Fatalf("h2.Sandbox = %q, want %q", h2.Sandbox, h1.Sandbox)
	}
	if got := runspec.RunControlDir("../bad/id"); got != ".crew/runs/badid" {
		t.Fatalf("RunControlDir sanitization = %q, want .crew/runs/badid", got)
	}
	for _, runID := range []string{"run-acp-1", "run-acp-2"} {
		tokPath := filepath.Join(pool.home, runspec.RunControlDir(runID), "token")
		if b, err := os.ReadFile(tokPath); err != nil || strings.TrimSpace(string(b)) != "tok-"+runID {
			t.Fatalf("token for %s = %q, err = %v", runID, string(b), err)
		}
	}
	pool.mu.Lock()
	specs := append([]sandboxes.Spec(nil), pool.lastSpecs...)
	pool.mu.Unlock()
	if len(specs) != 2 || specs[0].RunID != "run-acp-1" || specs[1].RunID != "run-acp-2" || specs[0].Minimal || specs[1].Minimal {
		t.Fatalf("EnsureProjectSandbox specs = %+v", specs)
	}

	// Verify both turns launched with `crew-run acp-turn --run <id> turn-0001`.
	rt.mu.Lock()
	turns := append([]string(nil), rt.turns...)
	rt.mu.Unlock()
	if len(turns) != 2 || turns[0] != "crew-run acp-turn --run run-acp-1 turn-0001" || turns[1] != "crew-run acp-turn --run run-acp-2 turn-0001" {
		t.Fatalf("turns = %v", turns)
	}

	// Steer s1 mid-turn.
	if err := k.Steer(context.Background(), s1.Run, "Please update the tests first."); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	steerBytes, err := os.ReadFile(filepath.Join(pool.home, runspec.RunTurnDir(s1.Run, 1), runspec.SteerFile))
	if err != nil {
		t.Fatalf("read steer.txt: %v", err)
	}
	if strings.TrimSpace(string(steerBytes)) != "Please update the tests first." {
		t.Fatalf("steer.txt = %q", string(steerBytes))
	}

	// Complete s1's turn and verify events come from s1's isolated spool.
	turn1Dir := filepath.Join(pool.home, runspec.RunTurnDir(s1.Run, 1))
	appendTo(t, filepath.Join(turn1Dir, runspec.EventsFile), `{"event":"step_update","step_update":{"conversation_id":"11111111-2222-4333-8444-555555555555","step_index":1,"state":"RUNNING","step_type":"agent_response","text_delta":"done s1"}}`+"\n")
	writeExit(t, filepath.Join(turn1Dir, runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","conversation":"11111111-2222-4333-8444-555555555555"}`))

	ev1 := next(t, k.Events(s1.Run))
	if ev1.Speaks != "done s1" {
		t.Fatalf("ev1.Speaks = %q, want done s1", ev1.Speaks)
	}
	end1 := next(t, k.Events(s1.Run))
	if end1.End == nil || end1.End.Reason != "ok" {
		t.Fatalf("end1 = %+v", end1)
	}

	// Stop s1 and s2: verify the shared project microVM is NOT destroyed via Sandboxes.Remove,
	// while ReleaseProjectSession is called for each session.
	if err := k.Stop(context.Background(), s1.Run); err != nil {
		t.Fatalf("Stop s1: %v", err)
	}
	if err := k.Stop(context.Background(), s2.Run); err != nil {
		t.Fatalf("Stop s2: %v", err)
	}
	sb.mu.Lock()
	removed := len(sb.removed)
	sb.mu.Unlock()
	if removed != 0 {
		t.Fatalf("shared project sandbox was removed (%d times), want 0", removed)
	}
	pool.mu.Lock()
	active, released := pool.active, pool.released
	pool.mu.Unlock()
	if active != 0 || released != 2 {
		t.Fatalf("pool active=%d released=%d, want active=0 released=2", active, released)
	}
}
