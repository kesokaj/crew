package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
)

// Script is what one turn of a FakeHarness run plays: the lines of a recording, in order, through
// the run's adapter, then the turn's end.
type Script struct {
	// Recording is a file of recorded output, such as testdata/agy/normal-run-flash-low.jsonl;
	// Lines are used when it is empty.
	Recording string
	Lines     [][]byte
	// Exit is how the turn ends. With Hold, the turn waits after its lines until it is interrupted,
	// and then ends with the reason it was given.
	Exit runspec.Exit
	Hold bool
	// Files are the CLI's own files of the conversation after the turn.
	Files map[string][]byte
	// Delay is the time between two lines.
	Delay time.Duration
	// ByHarness makes the end the harness's own, such as a microVM that died during the turn.
	ByHarness bool
	// Wait, when set, holds the turn before its first line until it is closed: the run works on
	// meanwhile, as a long turn does.
	Wait <-chan struct{}
	// Folder is what a run with a KeepFolder has in it after the turn, by path in the folder: it is
	// saved as the Kata harness saves it, under the Fake's Dir.
	Folder map[string][]byte
}

// Fake is the FakeHarness of 12.5: it plays scripts, so the driver, the monitor, and the UI are
// tested without a model or a microVM. Next gives the script of each turn; a run's lines carry the
// offsets a spool would give them, so a resumed run goes on after the last line that was stored.
type Fake struct {
	// Next returns the script of a turn: the spec of the run, and the turn's number. Nil plays one
	// line of status and ends with ok.
	Next func(spec RunSpec, turn int) Script
	// StartErr, when set, is what StartRun returns: a microVM that does not start.
	StartErr error
	// Boot, when set, hears of each start as orchestrator.StartSandbox's OnBoot does (21, D10): false
	// when StartErr is a microVM that did not boot (orchestrator.ErrNoBoot), true when the run's
	// sandbox starts, and nothing for another StartErr.
	Boot func(ok bool, why string)
	// ContinueErr, when set, is what Continue returns: a turn that does not start. ContinueHook, when
	// set, runs first, such as to cancel the caller's context.
	ContinueErr  error
	ContinueHook func()
	// MakeSandbox, when set, records a sandbox the run gets, before the run is told its id: what
	// sandboxes.Manager does with its row.
	MakeSandbox func(id string) error
	// Dir holds the homes and the saved work folders of a Fake's runs with a KeepFolder; "" is the
	// system's temporary folder.
	Dir string
	// Setup, when set, is how the project's setup ends in a new sandbox of a run that has one, as
	// crew-run runs it before the CLI: a failure ends the run with the reason setup and its output,
	// unless the run's setup may fail (11.2, since 0.47).
	Setup func(spec RunSpec) (ok bool, output string)

	mu      sync.Mutex
	runs    map[string]*fakeRun
	Started []RunSpec
	// Stopped and Interrupted name what the driver asked for, in order.
	Stopped     []string
	Interrupted []string
	Continued   []string
	Steered     []string
	AllowSteer  bool
	// Resumes are what the driver asked to follow again after a restart.
	Resumes []Resumed
	// Check answers one command of a check sandbox, and CheckErr is a check sandbox that does not
	// start; Checks are the check runs asked for. CheckSetup plays the project's setup of a check with
	// one: a result with an Error ends the check before its commands, as Kata's does.
	Check      func(spec CheckSpec, c CheckCmd) CheckResult
	CheckErr   error
	Checks     []CheckSpec
	CheckSetup func(spec CheckSpec) CheckResult
	// BuildFile gives the body of each file a build writes, and BuildErr is a build that fails; Builds
	// records every build (16.2 step 4).
	BuildFile func(spec BuildSpec, name string) []byte
	BuildErr  error
	Builds    []BuildSpec
	// Agy is what each build was given of agy's tarball, read while it ran.
	Agy [][]byte
	// MakeCheckErr fails the gate's make check, and MakeChecks records each (16.2 step 5).
	MakeCheckErr error
	MakeChecks   []BuildSpec
	// ImageTar is what a project's build leaves, ImageErr a build that fails; Images records each (19.2).
	ImageTar  []byte
	ImageErr  error
	Images    []ImageSpec
	sandboxes int
}

type fakeRun struct {
	spec      RunSpec
	sandbox   string
	turn      int
	conv      string
	events    chan RunEvent
	interrupt chan string
	done      chan struct{}
	busy      bool
	alive     bool
	// script is the current turn's, kept so a resumed run plays its rest.
	script Script
}

func (f *Fake) script(spec RunSpec, turn int) Script {
	if f.Next == nil {
		return Script{Lines: [][]byte{[]byte(`{"type":"status"}`)}, Exit: runspec.Exit{Reason: cli.ReasonOK}}
	}
	return f.Next(spec, turn)
}

// setup says whether the CLI of a run starts after the project's setup.
func (f *Fake) setup(spec RunSpec) (bool, string) {
	if f.Setup == nil || spec.Setup == "" {
		return true, ""
	}
	ok, out := f.Setup(spec)
	return ok || spec.SetupMayFail, out
}

// StartRun starts a run that plays its first script.
func (f *Fake) StartRun(_ context.Context, spec RunSpec) (RunHandle, error) {
	if _, ok := cli.Get(spec.CLI); !ok {
		return RunHandle{}, fmt.Errorf("no CLI %q", spec.CLI)
	}
	f.mu.Lock()
	if f.runs == nil {
		f.runs = map[string]*fakeRun{}
	}
	f.Started = append(f.Started, spec)
	f.sandboxes++
	sandbox := fmt.Sprintf("run-fake%04d", f.sandboxes)
	startErr := f.StartErr
	f.mu.Unlock()
	if startErr != nil {
		if f.Boot != nil && errors.Is(startErr, orchestrator.ErrNoBoot) {
			f.Boot(false, startErr.Error())
		}
		return RunHandle{}, startErr
	}
	if f.MakeSandbox != nil {
		if err := f.MakeSandbox(sandbox); err != nil {
			return RunHandle{}, err
		}
	}
	if f.Boot != nil {
		f.Boot(true, "")
	}
	if spec.OnSandbox != nil {
		if err := spec.OnSandbox(sandbox, "fakehash-"+sandbox); err != nil {
			return RunHandle{}, err
		}
	}
	r := &fakeRun{spec: spec, sandbox: sandbox, turn: 1, conv: spec.Continue, events: make(chan RunEvent, 64),
		interrupt: make(chan string, 1), done: make(chan struct{}), busy: true, alive: true}
	// The agent's turn is not played when the setup failed before it: the CLI never started.
	if ok, out := f.setup(spec); ok {
		r.script = f.script(spec, 1)
	} else {
		r.script = Script{Exit: runspec.Exit{Exit: 1, Reason: cli.ReasonSetup, Detail: fmt.Sprintf("the project's setup %q ended with 1: %s", spec.Setup, out)}}
	}
	f.mu.Lock()
	f.runs[spec.Run] = r
	f.mu.Unlock()
	go f.play(r, 1, r.script, 0)
	return RunHandle{Run: spec.Run, Sandbox: sandbox}, nil
}

func lines(s Script) [][]byte {
	if s.Recording == "" {
		return s.Lines
	}
	b, err := os.ReadFile(s.Recording)
	if err != nil {
		return [][]byte{[]byte("cannot read " + s.Recording)}
	}
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			out = append(out, append([]byte(nil), sc.Bytes()...))
		}
	}
	return out
}

// play sends the script's lines after offset, as a spool would, then the turn's end.
func (f *Fake) play(r *fakeRun, turn int, s Script, after int64) {
	a, _ := cli.Get(r.spec.CLI)
	send := func(e RunEvent) bool {
		select {
		case r.events <- e:
			return true
		case <-r.done:
			return false
		}
	}
	var off int64
	var result []byte
	reason := ""
	if s.Wait != nil {
		select {
		case <-s.Wait:
		case reason = <-r.interrupt:
		case <-r.done:
			return
		}
	}
	for _, l := range lines(s) {
		if reason != "" {
			break
		}
		off += int64(len(l)) + 1
		e := a.Parse(l)
		if e.Result {
			result = l
		}
		if e.ConversationID != "" && r.conv == "" {
			f.mu.Lock()
			r.conv = e.ConversationID
			f.mu.Unlock()
		}
		if off <= after {
			continue
		}
		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case reason = <-r.interrupt:
			case <-r.done:
				return
			}
		}
		if reason != "" {
			break
		}
		if !send(RunEvent{Run: r.spec.Run, Turn: turn, At: time.Now().UTC(), Event: e, Raw: l, Offset: off}) {
			return
		}
	}
	ex := s.Exit
	if reason == "" && s.Hold {
		select {
		case reason = <-r.interrupt:
		case <-r.done:
			return
		}
	}
	if reason != "" {
		ex.Reason, ex.Detail = reason, "stopped: "+reason
	} else {
		// An interrupt asked for after the last line, while the turn ends by itself, is spent.
		select {
		case <-r.interrupt:
		default:
		}
	}
	if ex.Reason == "" {
		ex.Reason = cli.ReasonOK
	}
	f.mu.Lock()
	if ex.Conversation == "" {
		ex.Conversation = r.conv
	}
	r.busy = false
	f.mu.Unlock()
	end := &TurnEnd{Exit: ex, Result: result, Files: s.Files, ByHarness: s.ByHarness}
	if r.spec.KeepFolder != "" {
		end.FolderPath, end.FolderNote = f.folder(r, s.Folder)
	}
	send(RunEvent{Run: r.spec.Run, Turn: turn, At: time.Now().UTC(),
		Event: cli.Event{Type: cli.EventStatus, Payload: map[string]any{"status": "ended", "reason": ex.Reason}},
		End:   end})
}

// folder writes a turn's work folder into a home of the run's under Dir and saves it as the Kata
// harness does.
func (f *Fake) folder(r *fakeRun, files map[string][]byte) (string, string) {
	base, err := os.MkdirTemp(f.Dir, "fake-run-")
	if err != nil {
		return "", err.Error()
	}
	home := filepath.Join(base, "home")
	os.MkdirAll(home, 0o755)
	for p, b := range files {
		full := filepath.Join(home, r.spec.KeepFolder, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, b, 0o644)
	}
	// The files are written anew each turn; a sandbox's unchanged files keep their times, which the tar
	// holds, so the same files get the same time here and a turn that changes nothing stores nothing
	// (BUG-010).
	filepath.WalkDir(filepath.Join(home, r.spec.KeepFolder), func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			os.Chtimes(p, fakeFolderTime, fakeFolderTime)
		}
		return nil
	})
	return keepFolder(home, r.spec.KeepFolder)
}

// fakeFolderTime is the time of every entry of a fake's work folder.
var fakeFolderTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func (f *Fake) get(id string) (*fakeRun, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	return r, ok
}

// Continue plays the run's next script.
func (f *Fake) Continue(_ context.Context, runID, conversation, text string) error {
	r, ok := f.get(runID)
	if !ok {
		return ErrNoRun
	}
	if f.ContinueHook != nil {
		f.ContinueHook()
	}
	f.mu.Lock()
	if f.ContinueErr != nil {
		f.mu.Unlock()
		return f.ContinueErr
	}
	if r.busy {
		f.mu.Unlock()
		return ErrBusy
	}
	r.busy = true
	r.turn++
	turn := r.turn
	if conversation != "" {
		r.conv = conversation
	}
	spec := r.spec
	spec.Prompt, spec.Continue = text, r.conv
	f.Continued = append(f.Continued, runID)
	f.mu.Unlock()
	r.script = f.script(spec, turn)
	go f.play(r, turn, r.script, 0)
	return nil
}

// Interrupt ends the running turn with reason.
func (f *Fake) Interrupt(_ context.Context, runID, reason string) error {
	r, ok := f.get(runID)
	if !ok {
		return ErrNoRun
	}
	f.mu.Lock()
	f.Interrupted = append(f.Interrupted, runID+":"+reason)
	f.mu.Unlock()
	select {
	case r.interrupt <- reason:
	default:
	}
	return nil
}

// Steer sends a live mid-turn steering prompt to an active turn on Fake when AllowSteer is set.
func (f *Fake) Steer(_ context.Context, runID, text string) error {
	r, ok := f.get(runID)
	if !ok {
		return ErrNoRun
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.AllowSteer {
		return errors.New("run is not an ACP session")
	}
	if !r.busy {
		return errors.New("no active turn to steer")
	}
	f.Steered = append(f.Steered, runID+":"+text)
	return nil
}

// Stop removes the run.
func (f *Fake) Stop(_ context.Context, runID string) error {
	f.mu.Lock()
	r, ok := f.runs[runID]
	if !ok {
		f.mu.Unlock()
		return ErrNoRun
	}
	delete(f.runs, runID)
	f.Stopped = append(f.Stopped, runID)
	r.alive = false
	f.mu.Unlock()
	close(r.done)
	return nil
}

// Events is the run's events.
func (f *Fake) Events(runID string) <-chan RunEvent {
	r, ok := f.get(runID)
	if !ok {
		return nil
	}
	return r.events
}

// Exec answers every command with nothing.
func (f *Fake) Exec(_ context.Context, runID string, _ []string) (ExecResult, error) {
	if _, ok := f.get(runID); !ok {
		return ExecResult{}, ErrNoRun
	}
	return ExecResult{}, nil
}

// ExecFresh plays a check sandbox: CheckSetup ends it first when it fails, Check answers each
// command, and every command passes without them.
func (f *Fake) ExecFresh(ctx context.Context, spec CheckSpec) ([]CheckResult, error) {
	f.mu.Lock()
	f.Checks = append(f.Checks, spec)
	check, startErr, setup := f.Check, f.CheckErr, f.CheckSetup
	f.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	if setup != nil && strings.TrimSpace(spec.Setup) != "" {
		if end := setup(spec); end.Error != "" {
			return setupEnded(spec, end), nil
		}
	}
	var out []CheckResult
	for _, c := range spec.Cmds {
		r := CheckResult{Name: c.Name, Passed: true}
		if check != nil {
			r = check(spec, c)
			r.Name = c.Name
		}
		out = append(out, r)
	}
	return out, nil
}

// Resume plays the rest of the run's current turn after a restart: the lines after the offset that
// was stored. A run the fake never started is gone, as a microVM that did not survive.
func (f *Fake) Resume(_ context.Context, res Resumed) error {
	f.mu.Lock()
	f.Resumes = append(f.Resumes, res)
	old, ok := f.runs[res.Run]
	f.mu.Unlock()
	if !ok {
		return ErrNoRun
	}
	close(old.done)
	r := &fakeRun{spec: old.spec, sandbox: old.sandbox, turn: res.Turn, conv: res.Conversation, events: make(chan RunEvent, 64),
		interrupt: make(chan string, 1), done: make(chan struct{}), busy: !res.TurnDone, alive: old.alive, script: old.script}
	f.mu.Lock()
	f.runs[res.Run] = r
	f.mu.Unlock()
	if !res.TurnDone {
		go f.play(r, res.Turn, r.script, res.Offset)
	}
	return nil
}

// Alive reports whether the run is still there.
func (f *Fake) Alive(_ context.Context, runID string) bool {
	r, ok := f.get(runID)
	return ok && r.alive
}

// Calls returns copies of what the driver asked for: the runs started, stopped, interrupted (as
// run:reason), and continued.
func (f *Fake) Calls() (started []RunSpec, stopped, interrupted, continued []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunSpec(nil), f.Started...), append([]string(nil), f.Stopped...),
		append([]string(nil), f.Interrupted...), append([]string(nil), f.Continued...)
}

// Dead makes the run's microVM seem gone to Alive, while the fake still knows the run: a microVM that
// died while its run waited.
func (f *Fake) Dead(runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[runID]; ok {
		r.alive = false
	}
}

// Kill makes the run's microVM vanish, as a machine that restarted.
func (f *Fake) Kill(runID string) {
	f.mu.Lock()
	r, ok := f.runs[runID]
	delete(f.runs, runID)
	f.mu.Unlock()
	if ok {
		close(r.done)
	}
}

// MakeCheck plays the gate's make check in a build sandbox: it records it in MakeChecks, and fails
// with MakeCheckErr.
func (f *Fake) MakeCheck(ctx context.Context, spec BuildSpec) (BuildResult, error) {
	if spec.Recorded != nil {
		spec.Recorded()
	}
	f.mu.Lock()
	f.MakeChecks = append(f.MakeChecks, spec)
	fail := f.MakeCheckErr
	f.mu.Unlock()
	if fail != nil {
		return BuildResult{Output: "make check failed"}, fail
	}
	return BuildResult{Output: "make check: ok"}, nil
}

// Build plays a build sandbox: it records the build, and writes each of BuildFiles into spec.Out with
// what BuildFile gives, or a line naming it. BuildErr is a build that fails.
func (f *Fake) Build(ctx context.Context, spec BuildSpec) (BuildResult, error) {
	if spec.Recorded != nil {
		spec.Recorded()
	}
	agy, _ := os.ReadFile(spec.Agy)
	f.mu.Lock()
	f.Builds = append(f.Builds, spec)
	f.Agy = append(f.Agy, agy)
	file, fail := f.BuildFile, f.BuildErr
	f.mu.Unlock()
	if fail != nil {
		return BuildResult{Output: "the build failed"}, fail
	}
	res := BuildResult{Hashes: map[string]string{}, Output: "built " + spec.Version}
	for _, name := range BuildFiles {
		body := []byte(name + " of " + spec.Version)
		if file != nil {
			body = file(spec, name)
		}
		p := filepath.Join(spec.Out, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return res, err
		}
		if err := os.WriteFile(p, body, 0o640); err != nil {
			return res, err
		}
		sum := sha256.Sum256(body)
		res.Hashes[name] = hex.EncodeToString(sum[:])
	}
	return res, nil
}

// BuildImage plays a project's build: it records it, and writes ImageTar to spec.Out; ImageErr is a
// build that fails.
func (f *Fake) BuildImage(ctx context.Context, spec ImageSpec) (BuildResult, error) {
	if spec.Recorded != nil {
		spec.Recorded()
	}
	f.mu.Lock()
	f.Images = append(f.Images, spec)
	body, fail := f.ImageTar, f.ImageErr
	f.mu.Unlock()
	if fail != nil {
		return BuildResult{Output: "the build failed"}, fail
	}
	if err := os.Mkdir(filepath.Dir(spec.Out), 0o750); err != nil {
		return BuildResult{}, err
	}
	if err := os.WriteFile(spec.Out, body, 0o640); err != nil {
		return BuildResult{}, err
	}
	sum := sha256.Sum256(body)
	return BuildResult{Hashes: map[string]string{ImageFile: hex.EncodeToString(sum[:])}, Output: "built"}, nil
}
