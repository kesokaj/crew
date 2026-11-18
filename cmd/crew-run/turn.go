package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// The time a CLI gets to end its turn after the interrupt, before its process group is killed: the
// interrupt ends a claude turn with a result, and the terminate signal would leave none (12.2).
var interruptGrace = 20 * time.Second

// timeoutGrace is how much later than the CLI's own limit the driver stops it, for a CLI that does
// not keep its own (12.2).
var timeoutGrace = 30 * time.Second

// turn runs one turn of a run (12.4):
//
//	crew-run turn turn-0001
//
// It pulls the run's credentials, checks the CLI's login, writes the CLI's settings, clones the
// run's branch the first time, and runs the CLI in a process group of its own, with the prompt from
// the turn's folder and standard input closed after it. The CLI writes its output straight into the
// spool, events.jsonl, which the backend reads as it grows. The turn ends when the CLI ends, when
// the backend asks it to stop, or at the timeout; then the work in the workspace is kept on the
// run's wip ref, and exit.json is written, last. crew-run ends with 0 whenever it wrote exit.json.
func turn(e env, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || !runspec.ValidTurnDir(args[0]) {
		fmt.Fprintln(stderr, "usage: crew-run turn turn-NNNN")
		return 2
	}
	t := &turnRun{env: e, dir: filepath.Join(e.Home, runspec.Dir, args[0]), log: stderr}
	ex := t.run()
	b, _ := json.MarshalIndent(ex, "", "  ")
	if err := writeFile(filepath.Join(t.dir, runspec.ExitFile), append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, "crew-run turn: writing exit.json:", err)
		return 1
	}
	fmt.Fprintf(stdout, "crew-run turn: %s %s\n", ex.Reason, ex.Detail)
	return 0
}

type turnRun struct {
	env     env
	dir     string
	log     io.Writer
	spec    runspec.Run
	a       cli.Adapter
	values  map[string]string
	token   string
	ctrlKey string
	mcp     bool
}

func (t *turnRun) logf(format string, args ...any) {
	fmt.Fprintf(t.log, "crew-run turn: "+format+"\n", args...)
}

func (t *turnRun) run() runspec.Exit {
	b, err := os.ReadFile(filepath.Join(t.env.Home, runspec.RunFile))
	if err != nil {
		return own(runspec.StepRunFile, "run.json: %v", err)
	}
	if err := json.Unmarshal(b, &t.spec); err != nil {
		return own(runspec.StepRunFile, "run.json: %v", err)
	}
	a, ok := cli.Get(t.spec.CLI)
	if !ok {
		return own(runspec.StepCLI, "run.json names no CLI crew-run knows: %q", t.spec.CLI)
	}
	t.a = a
	prompt, err := os.ReadFile(filepath.Join(t.dir, runspec.PromptFile))
	if err != nil {
		return own(runspec.StepPrompt, "the prompt: %v", err)
	}
	tok, err := os.ReadFile(filepath.Join(t.env.Home, runspec.TokenFile))
	if err != nil {
		return own(runspec.StepToken, "the run's token: %v", err)
	}
	t.token = strings.TrimSpace(string(tok))
	if reason, stop := t.stopAsked(); stop {
		return runspec.Exit{Exit: -1, Reason: reason, Detail: "stopped before the turn began"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	body, err := pull(ctx, t.env, "/v1/creds")
	if err != nil {
		return own(runspec.StepBroker, "the broker: %v", err)
	}
	var c credsAnswer
	if err := json.Unmarshal(body, &c); err != nil {
		return own(runspec.StepBroker, "the broker's answer: %v", err)
	}
	if _, err := apply(t.env.Home, c); err != nil {
		return own(runspec.StepCredentials, "the credentials: %v", err)
	}
	t.values = c.Env
	if r := a.LoginCheck(ctx, commandRunner(t.env.Home, t.values), false); r.State == cli.LoginRefused {
		return runspec.Exit{Exit: -1, Reason: cli.ReasonLogin, Detail: r.Detail}
	}
	var mcp *cli.MCP
	if t.spec.MCP != "" {
		if t.env.Broker == "" || !strings.HasPrefix(t.spec.MCP, "/") {
			return own(runspec.StepMCP, "run.json names Crew's MCP server, and there is no door to reach it at")
		}
		mcp = &cli.MCP{URL: strings.TrimRight(t.env.Broker, "/") + t.spec.MCP, Token: t.token}
		if err := checkMCP(ctx, mcp); err != nil {
			return own(runspec.StepMCP, "Crew's MCP server does not answer: %v", err)
		}
	}
	if err := a.PrepareRun(t.env.Home, runspec.Workspace, t.spec.Tools, mcp); err != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", err)
	}
	t.mcp = mcp != nil
	if err := t.workspace(ctx); err != nil {
		return own(runspec.StepWorkspace, runspec.WorkspaceFailed+"%v", err)
	}
	if t.spec.NoRepoConfig {
		if err := t.keepOutRepoConfig(ctx); err != nil {
			return own(runspec.StepWorkspace, runspec.WorkspaceFailed+"%v", err)
		}
	}
	if t.spec.NoRepoConfig || (t.spec.MainInstructions && !t.spec.Git.OwnOnly) {
		if err := t.instructionsFromMain(ctx); err != nil {
			return own(runspec.StepWorkspace, runspec.WorkspaceFailed+"the instructions: %v", err)
		}
	}

	// The project's setup, once in a sandbox, before the CLI starts: a setup that fails uses no
	// subscription (11.2, 13.5). PrepareRun runs again after setup so a setup script cannot tamper
	// with settings.json, config.json, or mcp_config.json.
	if ex, ok := t.setup(); !ok {
		return ex
	}
	if err := a.PrepareRun(t.env.Home, runspec.Workspace, t.spec.Tools, mcp); err != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", err)
	}
	ex := t.call(prompt)
	if sha, err := t.keepWIP(); err != nil {
		t.logf("the work in progress was not kept: %v", err)
		wipNotKept(&ex, err)
	} else {
		ex.WIP = sha
	}
	return ex
}

func (t *turnRun) stopAsked() (string, bool) {
	b, err := os.ReadFile(filepath.Join(t.env.Home, runspec.StopFile))
	if err != nil {
		return "", false
	}
	reason, ok := parseStopContent(string(b), t.ctrlKey)
	if !ok {
		_ = os.Remove(filepath.Join(t.env.Home, runspec.StopFile))
		return "", false
	}
	return reason, true
}

func parseStopContent(raw, ctrlKey string) (string, bool) {
	if ctrlKey != "" {
		first, rest, hasNewline := strings.Cut(raw, "\n")
		if strings.TrimSpace(first) != ctrlKey {
			return "", false
		}
		if hasNewline {
			raw = rest
		} else {
			raw = ""
		}
	}
	reason := strings.TrimSpace(raw)
	if reason == "" {
		reason = "operator"
	}
	return reason, true
}

func (t *turnRun) gitEnv() []string {
	env := append(cleanEnv(t.env.Home), "GIT_TERMINAL_PROMPT=0")
	if t.token != "" && t.env.Broker != "" && t.spec.Git.Path != "" {
		url := strings.TrimRight(t.env.Broker, "/") + t.spec.Git.Path
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+url+".extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+t.token,
		)
	}
	return env
}

// gitCmd runs git in the workspace, as the run's user, with the run's token for Crew's endpoint and
// no prompt for anything.
func (t *turnRun) gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = t.gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// gitStdout runs git as gitCmd does and returns its standard output alone.
func (t *turnRun) gitStdout(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = t.gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// workspace clones the run's branch from Crew's git endpoint the first time (12.4). The branch
// starts from the run's base: the work in progress of the run it continues, or main. The token is
// kept in the clone's own configuration, so the agent's own push reaches the endpoint.
func (t *turnRun) workspace(ctx context.Context) error {
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	g := t.spec.Git
	// A run with no repository, such as the planning agent's before a project exists or a normal
	// conversation outside a project, works in an empty workspace; a normal conversation can clone any
	// non-archived project's repository read-only from Crew's git endpoint using its run token (11.8).
	if g.Path == "" && g.Branch == "" && g.WIP == "" {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			return err
		}
		if t.spec.Project == "" && t.spec.MCP != "" && t.env.Broker != "" && t.token != "" {
			prefix := strings.TrimRight(t.env.Broker, "/") + "/git/"
			if _, err := t.gitCmd(ctx, ws, "config", "--file", filepath.Join(t.env.Home, ".gitconfig"), "http."+prefix+".extraHeader", "Authorization: Bearer "+t.token); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err == nil {
		if g.Refresh {
			return t.refresh(ctx, ws)
		}
		return nil
	}
	if g.Path == "" || g.Branch == "" || t.env.Broker == "" {
		return errors.New("run.json names no repository and branch, or there is no door")
	}
	url := strings.TrimRight(t.env.Broker, "/") + g.Path
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "-q", "--initial-branch=" + g.Branch},
		// The run's token goes to Crew's endpoint only, not to any other host the workspace reaches.
		{"config", "http." + url + ".extraHeader", "Authorization: Bearer " + t.token},
		{"remote", "add", "origin", url},
		{"config", "remote.origin.fetch", "+refs/heads/" + g.Branch + ":refs/remotes/origin/" + g.Branch},
		{"config", "branch." + g.Branch + ".remote", "origin"},
		{"config", "branch." + g.Branch + ".merge", "refs/heads/" + g.Branch},
		{"config", "push.default", "upstream"},
	}
	for _, s := range steps {
		if _, err := t.gitCmd(ctx, ws, s...); err != nil {
			return err
		}
	}
	// Where the branch starts: the base when it is work in progress, else the branch as pushed, else
	// the base.
	out, err := t.gitCmd(ctx, ws, "ls-remote", "origin", "refs/heads/"+g.Branch)
	if err != nil {
		return err
	}
	from := g.Base
	if strings.TrimSpace(out) != "" && !strings.HasPrefix(g.Base, "refs/crew/wip/") {
		from = "refs/heads/" + g.Branch
	}
	if from == "" {
		from = "refs/heads/main"
	}
	if _, err := t.gitCmd(ctx, ws, "fetch", "-q", "origin", from); err != nil {
		return err
	}
	if _, err := t.gitCmd(ctx, ws, "checkout", "-q", "-B", g.Branch, "FETCH_HEAD"); err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		t.gitCmd(ctx, ws, "fetch", "-q", "origin")
	}
	// main as origin/main, which every run may read: what a reviewer and a tester compare the branch
	// with, and what a builder merges when it resolves a conflict. A run of an eval reads its branch only.
	if g.OwnOnly {
		return nil
	}
	if _, err := t.gitCmd(ctx, ws, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main"); err != nil {
		return err
	}
	if g.Refresh {
		return t.refresh(ctx, ws)
	}
	return nil
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// refresh fetches a read-only workspace's branch and its other branches again, and resets the work
// tree to the branch, or to its commit, with nothing the turn before left in it: a ticket's chat reads
// its ticket as it is now at every turn (11.9, D7). It runs before the files the CLI loads by itself
// are kept out and main's instructions are taken, which the turn then does again (13.8, rule 13).
func (t *turnRun) refresh(ctx context.Context, ws string) error {
	g := t.spec.Git
	refs := []string{"+refs/heads/" + g.Branch + ":refs/remotes/origin/" + g.Branch, "+refs/heads/main:refs/remotes/origin/main"}
	for _, b := range g.Also {
		refs = append(refs, "+refs/heads/"+b+":refs/remotes/origin/"+b)
	}
	for _, r := range refs {
		if _, err := t.gitCmd(ctx, ws, "fetch", "-q", "origin", r); err != nil {
			return err
		}
	}
	to := "refs/remotes/origin/" + g.Branch
	if g.Commit != "" {
		if !commitID.MatchString(g.Commit) {
			return fmt.Errorf("run.json names %q as the commit to read, which is not one", g.Commit)
		}
		to = g.Commit
	}
	for _, s := range [][]string{{"reset", "-q", "--hard", to}, {"clean", "-q", "-ffdx"}} {
		if _, err := t.gitCmd(ctx, ws, s...); err != nil {
			return err
		}
	}
	return nil
}

// call runs the CLI once and reads why it ended.
func (t *turnRun) call(prompt []byte) runspec.Exit {
	spec := t.spec
	timeout := time.Duration(spec.Limits.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	command := t.a.TurnCommand(cli.Turn{
		Prompt: string(prompt), Model: spec.Model, Effort: spec.Effort, Workspace: runspec.Workspace,
		Continue: spec.Conversation, Tools: spec.Tools,
		Timeout: timeout, MaxTurns: spec.Limits.MaxTurns, Home: t.env.Home, MCP: t.mcp,
	})
	events, err := os.OpenFile(filepath.Join(t.dir, runspec.EventsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return own(runspec.StepSpool, "the spool: %v", err)
	}
	defer events.Close()
	errlog, err := os.OpenFile(filepath.Join(t.dir, runspec.StderrFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return own(runspec.StepStderr, "standard error: %v", err)
	}
	defer errlog.Close()

	cmd := exec.Command(command.Args[0], command.Args[1:]...)
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	cmd.Dir = ws
	cmd.Env = cleanEnv(t.env.Home)
	for k, v := range t.values {
		if !cli.ForbiddenEnv(k) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, command.Env...)
	cmd.Stdin = bytes.NewReader(command.Stdin)
	cmd.Stdout, cmd.Stderr = events, errlog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return own(runspec.StepStart, "the CLI did not start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(timeout + timeoutGrace)
	beat := time.NewTicker(time.Second)
	defer beat.Stop()
	t.touch()
	lastBeat := time.Now()
	stopReason := ""
	var interrupted time.Time
	var werr error
wait:
	for {
		select {
		case werr = <-done:
			break wait
		case now := <-beat.C:
			if now.Sub(lastBeat) >= runspec.HeartbeatEvery {
				t.touch()
				lastBeat = now
			}
			if stopReason == "" {
				if reason, ok := t.stopAsked(); ok {
					stopReason = reason
				} else if now.After(deadline) {
					stopReason = cli.ReasonTimeout
				}
				if stopReason != "" {
					t.logf("stopping the CLI: %s", stopReason)
					syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
					interrupted = now
				}
			} else if !interrupted.IsZero() && now.Sub(interrupted) > interruptGrace {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				interrupted = time.Time{}
			}
		}
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	t.touch()

	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(werr, &exitErr):
		code = exitErr.ExitCode()
	case werr != nil:
		code = -1
	}
	errb, _ := readTail(filepath.Join(t.dir, runspec.StderrFile), 64<<10)
	result, conv := t.lastResult()
	out := t.a.Outcome(cli.TurnOutput{Result: result, Stderr: errb, Exit: code})
	ex := runspec.Exit{Exit: code, Reason: out.Reason, Detail: out.Detail, Conversation: conv}
	if stopReason != "" {
		ex.Reason, ex.Detail = stopReason, "stopped: "+stopReason+"; the CLI said: "+out.Detail
	}
	return ex
}
