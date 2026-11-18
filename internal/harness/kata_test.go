package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type fakeSandboxes struct {
	dir     string
	door    string
	mu      sync.Mutex
	removed []string
	cut     []string
	cutErr  error
	onCut   func(id string)
	specs   []sandboxes.Spec
}

func (s *fakeSandboxes) Home(id string) string { return filepath.Join(s.dir, id, "home") }

func (s *fakeSandboxes) Start(_ context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error) {
	id := "run-0001"
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	door := s.door
	s.mu.Unlock()
	home := s.Home(id)
	os.MkdirAll(filepath.Join(home, ".crew"), 0o700)
	os.WriteFile(filepath.Join(home, ".crew", "token"), []byte("tok\n"), 0o600)
	if spec.Prepare != nil {
		if err := spec.Prepare(id, home, "hash"); err != nil {
			return sandboxes.Row{}, orchestrator.Sandbox{}, err
		}
	}
	return sandboxes.Row{ID: id, Kind: spec.Kind, CLI: spec.CLI}, orchestrator.Sandbox{ID: id, Container: "crew-" + id, BrokerIP: "10.9.1.254", Door: door, IP: "10.9.1.2"}, nil
}

func (s *fakeSandboxes) Cut(_ context.Context, id string) error {
	s.mu.Lock()
	s.cut = append(s.cut, id)
	err, fn := s.cutErr, s.onCut
	s.mu.Unlock()
	if fn != nil {
		fn(id)
	}
	return err
}

func (s *fakeSandboxes) Remove(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, id)
	return nil
}

type fakeRuntime struct {
	mu       sync.Mutex
	turns    []string
	gone     bool
	failExec bool
	onTurn   func(turn string)
	execs    []orchestrator.ExecSpec
	answer   func(line string) (int, string, error)
}

func (r *fakeRuntime) ExecDetached(_ context.Context, _ string, spec orchestrator.ExecSpec) (string, error) {
	r.mu.Lock()
	if r.failExec {
		r.mu.Unlock()
		return "", errors.New("the exec failed")
	}
	r.turns = append(r.turns, strings.Join(spec.Cmd, " "))
	f := r.onTurn
	r.mu.Unlock()
	if f != nil {
		go f(spec.Cmd[len(spec.Cmd)-1])
	}
	return "exec", nil
}

func (r *fakeRuntime) Exec(_ context.Context, _ string, spec orchestrator.ExecSpec) (int, string, error) {
	r.mu.Lock()
	r.execs = append(r.execs, spec)
	f := r.answer
	r.mu.Unlock()
	if f != nil {
		return f(spec.Cmd[len(spec.Cmd)-1])
	}
	return 0, "", nil
}

func (r *fakeRuntime) Running(context.Context, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.gone, nil
}

func kata(t *testing.T) (*Kata, *fakeSandboxes, *fakeRuntime) {
	sb := &fakeSandboxes{dir: t.TempDir(), door: "10.9.1.254:18091"}
	rt := &fakeRuntime{}
	return &Kata{Sandboxes: sb, Runtime: rt, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Poll: 10 * time.Millisecond,
		AliveEvery: 50 * time.Millisecond}, sb, rt
}

func spec() RunSpec {
	return RunSpec{Run: "r1", Subtask: 1, Repo: "fixture", Branch: "subtask/1", Base: "refs/heads/main",
		CLI: "agy", Model: "gemini-3.8-flash-low", Prompt: "Do it.", Tools: true,
		Limits: Limits{Timeout: time.Hour, MaxTurns: 100}}
}

// writeExit writes exit.json as crew-run does, under another name and renamed into place: the
// follower polls, and a file written in place could be read half written.
func writeExit(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path+".tmp", b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func next(t *testing.T, ch <-chan RunEvent) RunEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return RunEvent{}
}

func TestKataWritesTheControlFolderAndStartsTheTurn(t *testing.T) {
	k, sb, rt := kata(t)
	s := spec()
	s.NoRepoConfig, s.MCP, s.OwnBranchOnly = true, "/mcp", true
	s.Files = map[string][]byte{".gemini/antigravity-cli/conversations/c1.db": []byte("{}\n")}
	var sawSandbox string
	s.OnSandbox = func(id, hash string) error { sawSandbox = id + "/" + hash; return nil }
	h, err := k.StartRun(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	home := sb.Home(h.Sandbox)
	var rs runspec.Run
	b, _ := os.ReadFile(filepath.Join(home, runspec.RunFile))
	if json.Unmarshal(b, &rs) != nil || rs.Git.Path != "/git/fixture.git" || rs.Git.WIP != "refs/crew/wip/r1" ||
		rs.Limits.TimeoutSeconds != 3600 || !rs.NoRepoConfig || rs.MCP != "/mcp" || !rs.Git.OwnOnly {
		t.Fatalf("%s", b)
	}
	if p, _ := os.ReadFile(filepath.Join(home, runspec.TurnDir(1), runspec.PromptFile)); string(p) != "Do it." {
		t.Fatalf("%q", p)
	}
	if f, _ := os.ReadFile(filepath.Join(home, ".gemini/antigravity-cli/conversations/c1.db")); string(f) != "{}\n" {
		t.Fatal("the CLI's file was not put back")
	}
	if sawSandbox != "run-0001/hash" || len(rt.turns) != 1 || rt.turns[0] != "crew-run turn turn-0001" {
		t.Fatalf("%q %v", sawSandbox, rt.turns)
	}
	// A run's sandbox gets the harness, also a minimal one (12.1).
	if env := sb.specs[0].Env; len(env) != 1 || env[0] != "CREW_HARNESS=1" || sb.specs[0].Kind != sandboxes.KindRun {
		t.Fatalf("%+v", sb.specs[0])
	}
}

// Every line of the spool becomes one event exactly once, in order, and a line that is still being
// written waits for its end.
func TestKataReadsTheSpoolAsItGrows(t *testing.T) {
	k, sb, _ := kata(t)
	h, err := k.StartRun(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(sb.Home(h.Sandbox), runspec.TurnDir(1), runspec.EventsFile)
	ch := k.Events("r1")
	appendTo(t, spool, `{"event":"init","conversation_id":"s1"}`+"\n"+`{"event":"step_update","step_update":{"conversation_id":"s1","step_index":1,"state":"ACTIVE","step_type":"agent_resp`)
	e := next(t, ch)
	if e.Type != "status" || e.ConversationID != "s1" || e.Offset != int64(len(`{"event":"init","conversation_id":"s1"}`)+1) {
		t.Fatalf("%+v", e)
	}
	select {
	case e := <-ch:
		t.Fatalf("half a line became an event: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
	appendTo(t, spool, `onse","text_delta":"hi"}}`+"\n")
	if e := next(t, ch); e.Type != "message" || e.Speaks != "hi" {
		t.Fatalf("%+v", e)
	}
	result := `{"event":"result","result":{"conversation_id":"s1","status":"SUCCESS","response":"done"}}`
	appendTo(t, spool, result+"\n")
	if e := next(t, ch); !e.Result {
		t.Fatalf("%+v", e)
	}
	writeExit(t, filepath.Join(sb.Home(h.Sandbox), runspec.TurnDir(1), runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","wip":"abc"}`))
	e = next(t, ch)
	if e.End == nil || e.End.Reason != "ok" || string(e.End.Result) != result || e.End.Conversation != "s1" || e.End.WIP != "abc" {
		t.Fatalf("%+v", e)
	}
}

// A run followed again after a restart goes on after the last line that was stored.
func TestKataResumesAfterTheStoredLine(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	spool := filepath.Join(home, runspec.TurnDir(1), runspec.EventsFile)
	first := `{"event":"init","conversation_id":"s1"}` + "\n"
	appendTo(t, spool, first+`{"event":"second"}`+"\n")
	ch := k.Events("r1")
	next(t, ch)
	next(t, ch)
	k.Stop(context.Background(), "r1") // the server goes away; the home stays, as the microVM did
	os.MkdirAll(filepath.Join(home, ".crew"), 0o700)

	k2, _, _ := kata(t)
	k2.Sandboxes = sb
	if err := k2.Resume(context.Background(), Resumed{Run: "r1", Sandbox: h.Sandbox, CLI: "agy", Turn: 1, Offset: int64(len(first))}); err != nil {
		t.Fatal(err)
	}
	e := next(t, k2.Events("r1"))
	if e.Type != "status" || e.Payload["kind"] != "second" {
		t.Fatalf("the first line came again, or another: %+v", e)
	}
}

func TestKataSaysWhenTheMicroVMIsGone(t *testing.T) {
	k, _, rt := kata(t)
	k.StartRun(context.Background(), spec())
	rt.mu.Lock()
	rt.gone = true
	rt.mu.Unlock()
	start := time.Now()
	for time.Since(start) < 40*time.Second {
		e := next(t, k.Events("r1"))
		if e.End != nil {
			if e.End.Reason != "sandbox" {
				t.Fatalf("%+v", e.End)
			}
			return
		}
	}
	t.Fatal("no end")
}

// A line longer than the limit is one error event, and the lines after it keep their places.
func TestKataCutsALineThatIsTooLong(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	spool := filepath.Join(sb.Home(h.Sandbox), runspec.TurnDir(1), runspec.EventsFile)
	long := strings.Repeat("x", maxLine+10)
	after := `{"event":"second"}`
	appendTo(t, spool, long+"\n"+after+"\n")
	ch := k.Events("r1")
	if e := next(t, ch); e.Type != "error" || e.Offset != int64(len(long)+1) {
		t.Fatalf("%v %d", e.Payload, e.Offset)
	}
	if e := next(t, ch); string(e.Raw) != after || e.Offset != int64(len(long)+1+len(after)+1) {
		t.Fatalf("%q %d", e.Raw, e.Offset)
	}
}

// The stop file and the files of a later turn are written inside the home: a link the sandbox made
// does not lead the server's write out of it.
func TestKataWritesNothingOutsideTheHome(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("keep"), 0o644)
	os.Symlink(outside, filepath.Join(home, runspec.StopFile))
	if err := k.Interrupt(context.Background(), "r1", "operator"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatal("the write followed the link out of the home")
	}
	os.RemoveAll(filepath.Join(home, ".crew"))
	os.Symlink(filepath.Dir(outside), filepath.Join(home, ".crew"))
	if err := k.Interrupt(context.Background(), "r1", "operator"); err == nil {
		t.Fatal("a folder that is a link out of the home was written into")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "stop")); err == nil {
		t.Fatal("the stop file landed outside the home")
	}
}

func TestKataContinuesInTheSameMicroVM(t *testing.T) {
	k, sb, rt := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	if err := k.Continue(context.Background(), "r1", "", "more"); err != ErrBusy {
		t.Fatalf("a second turn while the first runs: %v", err)
	}
	writeExit(t, filepath.Join(home, runspec.TurnDir(1), runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","conversation":"s1"}`))
	for e := next(t, k.Events("r1")); e.End == nil; e = next(t, k.Events("r1")) {
	}
	if err := k.Continue(context.Background(), "r1", "", "more"); err != nil {
		t.Fatal(err)
	}
	var rs runspec.Run
	b, _ := os.ReadFile(filepath.Join(home, runspec.RunFile))
	json.Unmarshal(b, &rs)
	if rs.Conversation != "s1" {
		t.Fatalf("%s", b)
	}
	if p, _ := os.ReadFile(filepath.Join(home, runspec.TurnDir(2), runspec.PromptFile)); string(p) != "more" {
		t.Fatalf("%q", p)
	}
	if len(rt.turns) != 2 || rt.turns[1] != "crew-run turn turn-0002" {
		t.Fatalf("%v", rt.turns)
	}
}

// Two stops at once remove the run once, and neither panics.
func TestKataStopsARunOnceWhenTwoStopItAtOnce(t *testing.T) {
	k, sb, _ := kata(t)
	k.StartRun(context.Background(), spec())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- k.Stop(context.Background(), "r1")
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 || len(sb.removed) != 1 {
		t.Fatalf("%d stops succeeded, %d removals", ok, len(sb.removed))
	}
}

// A run that cannot be followed again is still stopped, and its microVM removed.
func TestKataRemovesTheMicroVMOfARunItCouldNotFollowAgain(t *testing.T) {
	k, sb, _ := kata(t)
	if err := k.Resume(context.Background(), Resumed{Run: "r9", Sandbox: "run-0009", CLI: "codex"}); err == nil {
		t.Fatal("a run of a CLI Crew does not know was followed")
	}
	if err := k.Stop(context.Background(), "r9"); err != nil || len(sb.removed) != 1 || sb.removed[0] != "run-0009" {
		t.Fatalf("%v %v", err, sb.removed)
	}
}

// A FIFO the sandbox put where the server writes does not block the write.
func TestKataIsNotBlockedByAFIFOInTheHome(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	// At the stop file itself, and at the name the server once wrote through.
	for _, name := range []string{runspec.StopFile, runspec.StopFile + ".crew-new"} {
		if err := syscall.Mkfifo(filepath.Join(home, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- k.Interrupt(context.Background(), "r1", "operator") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write is blocked by the FIFO")
	}
	if b, err := os.ReadFile(filepath.Join(home, runspec.StopFile)); err != nil || string(b) != "operator\n" {
		t.Fatalf("%q %v", b, err)
	}
}

// A line that grows past the limit with no end is cut once, not read again from its start.
func TestKataCutsALineThatNeverEnds(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	spool := filepath.Join(sb.Home(h.Sandbox), runspec.TurnDir(1), runspec.EventsFile)
	appendTo(t, spool, strings.Repeat("y", maxLine+100))
	ch := k.Events("r1")
	if e := next(t, ch); e.Type != "error" || e.Offset != int64(maxLine+100) {
		t.Fatalf("%v %d", e.Payload, e.Offset)
	}
	appendTo(t, spool, `"tail"`+"\n"+`{"event":"second"}`+"\n")
	next(t, ch) // the rest of the cut line
	if e := next(t, ch); e.Payload["kind"] != "second" {
		t.Fatalf("%+v", e)
	}
}

// A turn that does not start in the microVM is undone, so the next one takes its number again.
func TestKataUndoesATurnThatDidNotStart(t *testing.T) {
	k, sb, rt := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	writeExit(t, filepath.Join(home, runspec.TurnDir(1), runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","conversation":"s1"}`))
	for e := next(t, k.Events("r1")); e.End == nil; e = next(t, k.Events("r1")) {
	}
	rt.mu.Lock()
	rt.failExec = true
	rt.mu.Unlock()
	if err := k.Continue(context.Background(), "r1", "", "more"); err == nil {
		t.Fatal("no error")
	}
	rt.mu.Lock()
	rt.failExec = false
	rt.mu.Unlock()
	if err := k.Continue(context.Background(), "r1", "", "more"); err != nil {
		t.Fatal(err)
	}
	if last := rt.turns[len(rt.turns)-1]; last != "crew-run turn turn-0002" {
		t.Fatalf("%v", rt.turns)
	}
}

// An eval's run of a release's suite starts its sandbox from the candidate's image the run names
// (16.2 step 5, since 0.52).
func TestKataStartsAGateEvalRunFromItsImage(t *testing.T) {
	k, sb, _ := kata(t)
	s := spec()
	s.Image = "sha256:" + strings.Repeat("9", 64)
	if _, err := k.StartRun(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(sb.specs) != 1 || sb.specs[0].Image != s.Image {
		t.Fatalf("%+v", sb.specs)
	}
}
