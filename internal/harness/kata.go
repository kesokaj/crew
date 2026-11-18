package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The limits on what is taken back from a run's home, which the agent can write as root (12.4).
const (
	maxLine     = 16 << 20 // one line of the spool
	maxExitFile = 64 << 10 // exit.json
	maxCLIFile  = 64 << 20 // one of the CLI's own files: it is read into memory, and redacted
)

// Sandboxes is the part of sandboxes.Manager the harness uses.
type Sandboxes interface {
	Start(ctx context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error)
	Cut(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Home(id string) string
}

// Runtime is the part of the orchestrator the harness uses.
type Runtime interface {
	ExecDetached(ctx context.Context, container string, spec orchestrator.ExecSpec) (string, error)
	Exec(ctx context.Context, container string, spec orchestrator.ExecSpec) (int, string, error)
	Running(ctx context.Context, container string) (bool, error)
}

// Kata runs project runs, ticket chats, and conversations inside the project's warm Kata microVM via
// `crew-run acp-turn` (13.3, design 1.37), or in a dedicated microVM when `ProjectSandboxes` is not
// configured or the run is a judge/improver, and follows each turn's spool through the run's home (12.4).
type Kata struct {
	Sandboxes        Sandboxes
	ProjectSandboxes ProjectSandboxes
	Runtime          Runtime
	Log              *slog.Logger
	// Poll is how often a spool is read; 0 is a quarter of a second.
	Poll time.Duration
	// AliveEvery is how often a turn's microVM is asked whether it still runs; 0 is 30 seconds.
	AliveEvery time.Duration

	mu   sync.Mutex
	runs map[string]*kataRun
}

type kataRun struct {
	k         *Kata
	id        string
	sandbox   string
	container string
	home      string
	a         cli.Adapter
	spec      runspec.Run
	// keep is the folder of the home saved after each turn (RunSpec.KeepFolder).
	keep    string
	acp     bool
	project string
	token   string
	ctrlKey string

	mu       sync.Mutex
	turn     int
	offset   int64
	turnDone bool
	conv     string

	events chan RunEvent
	wake   chan struct{}
	done   chan struct{}
}

func (k *Kata) poll() time.Duration {
	if k.Poll > 0 {
		return k.Poll
	}
	return 250 * time.Millisecond
}

func (k *Kata) aliveEvery() time.Duration {
	if k.AliveEvery > 0 {
		return k.AliveEvery
	}
	return 30 * time.Second
}

func (k *Kata) get(id string) (*kataRun, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	r, ok := k.runs[id]
	return r, ok
}

func (k *Kata) put(r *kataRun) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.runs == nil {
		k.runs = map[string]*kataRun{}
	}
	k.runs[r.id] = r
}

// StartRun prepares the run's home with its control folder, starts its microVM (or reuses the project's
// warm ACP microVM), and starts the first turn detached (12.4, 13.3).
func (k *Kata) StartRun(ctx context.Context, spec RunSpec) (RunHandle, error) {
	a, ok := cli.Get(spec.CLI)
	if !ok {
		return RunHandle{}, fmt.Errorf("no CLI %q", spec.CLI)
	}
	if err := checkSpec(spec); err != nil {
		return RunHandle{}, err
	}
	rs := runspec.Run{
		Run: spec.Run, Subtask: spec.Subtask, Project: spec.Project, CLI: spec.CLI, Model: spec.Model, Effort: spec.Effort,
		Conversation: spec.Continue, Tools: spec.Tools,
		Limits: runspec.Limits{TimeoutSeconds: int(spec.Limits.Timeout.Seconds()), MaxTurns: spec.Limits.MaxTurns},
		Git:    RunGit(spec.Run, spec.Repo, spec.Branch, spec.Base), MCP: spec.MCP, NoRepoConfig: spec.NoRepoConfig,
		MainInstructions: spec.MainInstructions,
		Setup:            spec.Setup, SetupTimeoutSeconds: int(spec.SetupTimeout.Seconds()), SetupMayFail: spec.SetupMayFail,
	}
	if spec.ReadOnly {
		rs.Git.WIP = ""
	}
	OwnBranch(&rs.Git, spec)
	kind, env := sandboxes.KindRun, []string{"CREW_HARNESS=1"}
	switch {
	case spec.Conversation:
		kind = sandboxes.KindConversation
	case spec.Chat:
		kind = sandboxes.KindChat
	}
	if spec.Judge {
		kind, env = sandboxes.KindJudge, nil
	}
	if spec.Improver {
		kind = sandboxes.KindImprover
	}
	if k.useProjectACP(spec) {
		return k.startProjectACPRun(ctx, spec, a, rs, kind, env)
	}
	r := &kataRun{k: k, id: spec.Run, a: a, spec: rs, turn: 1, conv: spec.Continue, keep: spec.KeepFolder,
		events: make(chan RunEvent, 64), wake: make(chan struct{}, 1), done: make(chan struct{})}
	loginOf := spec.CLI
	if spec.WithoutLogin {
		loginOf = ""
	}
	row, sb, err := k.Sandboxes.Start(ctx, sandboxes.Spec{
		Kind: kind, CLI: loginOf, Project: spec.Project, Repos: spec.Repos, Mode: spec.Mode,
		Minimal: spec.Minimal || spec.Judge, Sizing: spec.Sizing, Life: 24 * time.Hour, Image: spec.Image,
		// The harness is in a run's home also when its microVM has no Docker (12.1); a judge's home
		// has none, so a short call does not read its context (spike item 79).
		Env: env,
		Prepare: func(id, home, hash string) error {
			if err := writeControl(home, rs, 1, spec.Prompt); err != nil {
				return err
			}
			for p, data := range spec.Files {
				if err := writeHome(home, p, data, 0o600); err != nil {
					return fmt.Errorf("putting back %s: %w", p, err)
				}
			}
			// The work folder goes back as it was stored; one that cannot be put back leaves an
			// empty folder, and the conversation goes on without it.
			if spec.KeepFolder != "" {
				if err := PutFolder(home, spec.KeepFolder, spec.Folder); err != nil && len(spec.Folder) > 0 {
					k.Log.Warn("the work folder was not put back", "run", spec.Run, "error", err)
				}
			}
			if spec.OnSandbox != nil {
				return spec.OnSandbox(id, hash)
			}
			return nil
		},
	})
	if err != nil {
		return RunHandle{}, err
	}
	r.sandbox, r.container, r.home = row.ID, sb.Container, k.Sandboxes.Home(row.ID)
	if err := r.startTurn(ctx); err != nil {
		k.remove(row.ID)
		return RunHandle{}, err
	}
	k.put(r)
	go r.follow()
	return RunHandle{Run: spec.Run, Sandbox: row.ID}, nil
}

func (k *Kata) remove(sandbox string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := k.Sandboxes.Remove(ctx, sandbox); err != nil {
		k.Log.Warn("removing a run's sandbox", "sandbox", sandbox, "error", err)
	}
}

// writeControl writes run.json and the turn's prompt into the home.
func writeControl(home string, rs runspec.Run, turn int, prompt string) error {
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := writeHome(home, runspec.RunFile, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return writeHome(home, path.Join(runspec.TurnDir(turn), runspec.PromptFile), []byte(prompt), 0o644)
}

// writeHome writes a file into a run's home, which the sandbox may write too, as root. The write stays
// inside the home: a folder that is a link out of it is refused (os.Root). The file is made new, under
// a name of its own, never opened where the sandbox could have put a link or a FIFO, which would block
// the write for ever; then it replaces rel.
func writeHome(home, rel string, data []byte, mode os.FileMode) error {
	return writeHomeFrom(home, rel, bytes.NewReader(data), mode)
}

// writeHomeFrom is writeHome of what r reads, for a file too large to hold in memory: agy's tarball
// for a build.
func writeHomeFrom(home, rel string, r io.Reader, mode os.FileMode) error {
	if err := homepath.Valid(rel); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	tmp := fmt.Sprintf("%s.crew-%x", rel, rnd)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		root.Remove(tmp)
		return fmt.Errorf("%s: not a regular file", tmp)
	}
	_, werr := io.Copy(f, r)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Chmod(tmp, mode)
	}
	if werr != nil {
		root.Remove(tmp)
		return werr
	}
	return root.Rename(tmp, rel)
}

// startTurn starts the current turn's crew-run turn (or acp-turn), detached, as the sandbox's user.
func (r *kataRun) startTurn(ctx context.Context) error {
	r.mu.Lock()
	turn := r.turn
	acp := r.acp
	r.mu.Unlock()
	cmd := []string{"crew-run", "turn", filepath.Base(runspec.TurnDir(turn))}
	env := []string{"HOME=/home/" + orchestrator.GuestUser}
	if acp {
		cmd = []string{"crew-run", "acp-turn", "--run", r.id, filepath.Base(runspec.TurnDir(turn))}
		if r.ctrlKey != "" {
			env = append(env, "CREW_CTRL_KEY="+r.ctrlKey)
		}
	}
	_, err := r.k.Runtime.ExecDetached(ctx, r.container, orchestrator.ExecSpec{
		Cmd:  cmd,
		User: orchestrator.GuestUser, Env: env,
	})
	if err != nil {
		return fmt.Errorf("starting the turn: %w", err)
	}
	return nil
}

// Continue starts the next turn in the same microVM, which continues the conversation (12.10).
func (k *Kata) Continue(ctx context.Context, runID, conversation, text string) error {
	r, ok := k.get(runID)
	if !ok {
		return ErrNoRun
	}
	r.mu.Lock()
	if !r.turnDone {
		r.mu.Unlock()
		return ErrBusy
	}
	r.turn++
	turn := r.turn
	if conversation != "" {
		r.conv = conversation
	}
	r.spec.Conversation = r.conv
	rs := r.spec
	r.mu.Unlock()
	// A stop file left from an interrupt of the last turn would stop this one at once.
	if root, err := os.OpenRoot(r.home); err == nil {
		root.Remove(r.stopFile())
		root.Close()
	}
	undo := func(err error) error {
		r.mu.Lock()
		r.turn--
		r.mu.Unlock()
		return err
	}
	if err := r.clearTurnDir(turn); err != nil {
		return undo(err)
	}
	if err := r.writeCtrl(rs, turn, text); err != nil {
		return undo(err)
	}
	if err := r.startTurn(ctx); err != nil {
		// A microVM that is gone cannot take the turn: the caller continues in a new one.
		if alive, aerr := k.Runtime.Running(ctx, r.container); aerr == nil && !alive {
			return undo(ErrNoRun)
		}
		return undo(err)
	}
	r.mu.Lock()
	r.offset, r.turnDone = 0, false
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// Interrupt asks the running turn to stop: the file ~/.crew/stop (or ~/.crew/runs/<id>/stop), with
// the reason, which crew-run reads every second (12.4).
func (k *Kata) Interrupt(_ context.Context, runID, reason string) error {
	r, ok := k.get(runID)
	if !ok {
		return ErrNoRun
	}
	if r.acp && r.ctrlKey != "" {
		return writeHome(r.home, r.stopFile(), []byte(r.ctrlKey+"\n"+reason+"\n"), 0o600)
	}
	return writeHome(r.home, r.stopFile(), []byte(reason+"\n"), 0o644)
}

// Stop removes the run's microVM and its home (or closes its ACP session and releases its slot in the
// shared project microVM).
func (k *Kata) Stop(_ context.Context, runID string) error {
	// One caller finds and takes the run; another that stops it at the same time finds none.
	k.mu.Lock()
	r, ok := k.runs[runID]
	delete(k.runs, runID)
	k.mu.Unlock()
	if !ok {
		return ErrNoRun
	}
	close(r.done)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if r.acp {
		return r.stopACP(ctx)
	}
	return k.Sandboxes.Remove(ctx, r.sandbox)
}

// Events is the run's events.
func (k *Kata) Events(runID string) <-chan RunEvent {
	r, ok := k.get(runID)
	if !ok {
		return nil
	}
	return r.events
}

// Exec runs a command in the run's microVM as the sandbox's user.
func (k *Kata) Exec(ctx context.Context, runID string, cmd []string) (ExecResult, error) {
	r, ok := k.get(runID)
	if !ok {
		return ExecResult{}, ErrNoRun
	}
	code, out, err := k.Runtime.Exec(ctx, r.container, orchestrator.ExecSpec{Cmd: cmd, User: orchestrator.GuestUser,
		Env: []string{"HOME=/home/" + orchestrator.GuestUser}})
	return ExecResult{Code: code, Output: out}, err
}

// Resume follows a run again after a restart, from where its events were stored.
// The run is registered before anything that can fail, so a Stop after a failed Resume still
// removes its microVM. What it needs of the run comes from the database, not from the home, which the
// sandbox may have changed.
func (k *Kata) Resume(ctx context.Context, res Resumed) error {
	acp := strings.HasPrefix(res.Sandbox, "proj-") && res.Spec.Project != "" && k.projectPool() != nil
	r := &kataRun{k: k, id: res.Run, sandbox: res.Sandbox, container: orchestrator.SandboxName(res.Sandbox),
		home: k.Sandboxes.Home(res.Sandbox), spec: res.Spec, turn: res.Turn, offset: res.Offset, turnDone: res.TurnDone, keep: res.KeepFolder,
		acp: acp, project: res.Spec.Project,
		events: make(chan RunEvent, 64), wake: make(chan struct{}, 1), done: make(chan struct{})}
	if cli.SafeID(res.Conversation) {
		r.conv = res.Conversation
	}
	k.put(r)
	a, ok := cli.Get(res.CLI)
	if !ok {
		return fmt.Errorf("no CLI %q", res.CLI)
	}
	r.a = a
	go r.follow()
	return nil
}

// Alive reports whether the run's microVM runs.
func (k *Kata) Alive(ctx context.Context, runID string) bool {
	r, ok := k.get(runID)
	if !ok {
		return false
	}
	alive, err := k.Runtime.Running(ctx, r.container)
	return err == nil && alive
}

// checkSpec refuses a run with no id or prompt, and a repository without its branch or a branch
// without its repository: a run has both, or works in an empty folder with neither.
func checkSpec(spec RunSpec) error {
	if spec.Run == "" || spec.Prompt == "" || (spec.Repo != "") != (spec.Branch != "") {
		return errors.New("a run needs its id and a prompt, and a branch with its repository")
	}
	if spec.Judge && (spec.Repo != "" || spec.MCP != "" || spec.Tools || len(spec.Repos) > 0) {
		return errors.New("a judge has no repository, no Crew MCP, no tools, and no credential but its CLI's login")
	}
	if spec.Improver && (spec.Repo != "" || len(spec.Repos) > 0 || spec.Judge) {
		return errors.New("the improver has no repository and no credential but its CLI's login")
	}
	return nil
}

// RunGit is where a run's work comes from and goes to; a run with no repository has none.
func RunGit(run, repo, branch, base string) runspec.Git {
	if repo == "" {
		return runspec.Git{}
	}
	return runspec.Git{Path: "/git/" + repo + ".git", Branch: branch, Base: base, WIP: "refs/crew/wip/" + run}
}

// OwnBranch keeps a run of an eval to its branch, with the commit it takes its instructions from, and
// gives a chat its refresh, its commit and its other branches: the same when it starts and when a
// restart follows it again (review of M15 after the live checks).
func OwnBranch(g *runspec.Git, spec RunSpec) {
	if spec.OwnBranchOnly && g.Path != "" {
		g.OwnOnly, g.InstructionsAt = true, spec.InstructionsAt
	}
	if g.Path != "" {
		g.Refresh, g.Commit, g.Also = spec.Refresh, spec.Commit, spec.Also
	}
}
