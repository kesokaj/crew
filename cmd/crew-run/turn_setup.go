package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// setup runs the project's setup command in the workspace, as the CLI would run there, once in a
// sandbox. It says how the run ends when the setup failed and the CLI may not start.
func (t *turnRun) setup() (runspec.Exit, bool) {
	cmdline := strings.TrimSpace(t.spec.Setup)
	done := filepath.Join(t.env.Home, runspec.SetupDone)
	if t.env.Workspace != "" && t.env.Workspace != runspec.Workspace {
		done = filepath.Join(filepath.Dir(t.dir), "setup.done")
	}
	if cmdline == "" {
		return runspec.Exit{}, true
	}
	if _, err := os.Stat(done); err == nil {
		return runspec.Exit{}, true
	}
	timeout := time.Duration(t.spec.SetupTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	logPath := filepath.Join(t.dir, runspec.SetupLog)
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return own(runspec.StepSetupLog, "the setup's log: %v", err), false
	}
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", cmdline)
	cmd.Dir = ws
	cmd.Env = cleanEnv(t.env.Home)
	for k, v := range t.values {
		if !cli.ForbiddenEnv(k) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// The setup can take minutes: the heartbeat says the run is alive meanwhile, and a stop the
	// backend asks for ends it at once (review of M13).
	beat := time.NewTicker(runspec.HeartbeatEvery)
	poll := time.NewTicker(500 * time.Millisecond)
	stopBeat := make(chan struct{})
	var stopReason string
	var stopMu sync.Mutex
	ctrlDir := filepath.Dir(t.dir)
	go func() {
		for {
			select {
			case <-beat.C:
				t.touchIn(ctrlDir)
			case <-poll.C:
				if reason, stop := t.stopAskedIn(ctrlDir); stop {
					stopMu.Lock()
					stopReason = reason
					stopMu.Unlock()
					cancel()
					return
				}
			case <-stopBeat:
				return
			}
		}
	}()
	t.touchIn(ctrlDir)
	runErr := cmd.Run()
	close(stopBeat)
	beat.Stop()
	poll.Stop()
	out.Close()
	stopMu.Lock()
	stopped := stopReason
	stopMu.Unlock()
	if stopped != "" {
		return runspec.Exit{Exit: -1, Reason: stopped, Detail: "stopped during the project's setup"}, false
	}
	if runErr == nil {
		os.WriteFile(done, nil, 0o644)
		return runspec.Exit{}, true
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	tail, _ := readTail(logPath, 2000)
	// What the setup printed leaves the sandbox in the stop's detail: no value the broker handed the
	// run goes with it (security review of M13).
	for _, v := range t.values {
		if len(v) >= 8 {
			tail = bytes.ReplaceAll(tail, []byte(v), []byte("[removed]"))
		}
	}
	why := fmt.Sprintf("the project's setup %q ended with %d", cmdline, code)
	if ctx.Err() != nil {
		why = fmt.Sprintf("the project's setup %q ran past %s", cmdline, timeout)
	}
	if t.spec.SetupMayFail {
		// The builder of the ticket that fixes the setup starts all the same, with what it printed.
		t.logf("%s; the CLI starts all the same: %s", why, logPath)
		os.WriteFile(done, nil, 0o644)
		return runspec.Exit{}, true
	}
	return runspec.Exit{Exit: code, Reason: cli.ReasonSetup, Detail: why + ": " + strings.TrimSpace(string(tail))}, false
}

func (t *turnRun) touch() {
	p := filepath.Join(t.env.Home, runspec.Heartbeat)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		os.WriteFile(p, nil, 0o644)
	}
}

// lastResult is the turn's result, the last result line the CLI printed before it ended (12.4), and
// the CLI's id of the conversation.
func (t *turnRun) lastResult() ([]byte, string) {
	f, err := os.Open(filepath.Join(t.dir, runspec.EventsFile))
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	var result []byte
	conv := ""
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && len(line) < 64<<20 {
			ev := t.a.Parse(bytes.TrimSpace(line))
			if ev.Result {
				result = append([]byte(nil), bytes.TrimSpace(line)...)
			}
			if ev.ConversationID != "" && conv == "" {
				conv = ev.ConversationID
			}
		}
		if err != nil {
			break
		}
	}
	return result, conv
}

// keepWIP commits what is in the workspace and pushes it to the run's wip ref, a place only the
// backend reads (11.2), when the workspace holds anything the wip ref does not. The agent's own
// hooks do not run.
func (t *turnRun) keepWIP() (string, error) {
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	if t.spec.Git.WIP == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	g := func(args ...string) (string, error) {
		return t.gitCmd(ctx, ws, append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	}
	status, err := g("status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := g("add", "-A"); err != nil {
			return "", err
		}
		if _, err := g("commit", "-q", "--no-verify", "-m", "crew: work in progress of run "+t.spec.Run); err != nil {
			return "", err
		}
	}
	head, err := g("rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return "", nil // no commit at all: nothing to keep
	}
	head = strings.TrimSpace(head)
	if _, err := g("push", "-q", "--force", "origin", "HEAD:"+t.spec.Git.WIP); err != nil {
		return "", err
	}
	return head, nil
}

// cleanEnv is the environment of what crew-run starts: its own, with the home, and never any
// variable forbidden by the CLI adapter or CREW_CTRL_KEY (12.2, 13.5).
func cleanEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !cli.ForbiddenEnv(k) && k != "HOME" && k != "CREW_CTRL_KEY" {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home)
}

// readTail reads at most the last max bytes of a file.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// checkMCP asks Crew's MCP server to initialize, with the run's token, as the CLI will.
func checkMCP(ctx context.Context, m *cli.MCP) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"crew-run","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Result *struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.Result == nil || out.Result.ProtocolVersion == "" {
		return errors.New("its answer to initialize does not read")
	}
	return nil
}

// keepOutRepoConfig takes out of the workspace the files the CLI loads from a repository by itself,
// such as agy's hooks and MCP servers, before every turn of a run that does not build (13.8). Each
// part of a path is looked at without following it: a link is itself what is taken out, so a link
// in the repository can never make crew-run remove a file outside the workspace. What git tracks is
// marked skip-worktree first, so its absence is no change the run's work in progress keeps.
func (t *turnRun) keepOutRepoConfig(ctx context.Context) error {
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	_, gitErr := os.Stat(filepath.Join(ws, ".git"))
	for _, rel := range t.a.WorkspaceConfig() {
		target, err := firstOf(ws, rel)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		if gitErr == nil {
			out, err := t.gitStdout(ctx, ws, "ls-files", "-z", "--", target)
			if err != nil {
				return err
			}
			for _, f := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
				if f == "" {
					continue
				}
				if _, err := t.gitCmd(ctx, ws, "update-index", "--skip-worktree", "--", f); err != nil {
					return err
				}
			}
		}
		if err := os.RemoveAll(filepath.Join(ws, filepath.FromSlash(target))); err != nil {
			return err
		}
		t.logf("kept %s out of the workspace: a run that does not build loads no configuration of the repository", target)
	}
	return nil
}

// firstOf is what of rel is there to take out, relative to ws: the path itself, or the first part of
// it that is a link; "" when nothing of it is there. No part is followed.
func firstOf(ws, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		fi, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(cur)))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		switch {
		case i == len(parts)-1 || fi.Mode()&os.ModeSymlink != 0:
			return cur, nil
		case !fi.IsDir():
			return "", nil // a file where a folder belongs holds nothing the CLI could load
		}
	}
	return "", nil
}
