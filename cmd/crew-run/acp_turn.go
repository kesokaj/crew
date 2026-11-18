package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

var acpTokenCache sync.Map

func readCtrlKey(ctrlDir string) string {
	if k := strings.TrimSpace(os.Getenv("CREW_CTRL_KEY")); k != "" {
		_ = os.Remove(filepath.Join(ctrlDir, "ctrl.key"))
		return k
	}
	p := filepath.Join(ctrlDir, "ctrl.key")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	return strings.TrimSpace(string(b))
}

func resolveCtrlRel(home, ctrlDir string) string {
	rel, err := filepath.Rel(home, ctrlDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return runspec.Dir
	}
	return filepath.ToSlash(rel)
}

func parseRunAndTurnArgs(args []string) (runID, turnName string, rest []string, ok bool) {
	for len(args) >= 2 && args[0] == "--run" {
		runID = strings.TrimSpace(args[1])
		args = args[2:]
	}
	if len(args) == 0 {
		return runID, "", nil, false
	}
	if runspec.ValidTurnDir(args[0]) {
		return runID, args[0], args[1:], true
	}
	if len(args) >= 2 && runID == "" && runspec.ValidTurnDir(args[1]) {
		return strings.TrimSpace(args[0]), args[1], args[2:], true
	}
	return runID, "", args, false
}

func resolveCtrlDir(home, runID string) string {
	if runID != "" {
		return filepath.Join(home, runspec.RunControlDir(runID))
	}
	return filepath.Join(home, runspec.Dir)
}

// acpTurn runs one turn of a run against the warm `acp-daemon` in the project microVM (design 12.4, since 1.37):
//
//	crew-run acp-turn [--run <run-id>] turn-0001
func acpTurn(e env, args []string, stdout, stderr io.Writer) int {
	runID, turnName, rest, ok := parseRunAndTurnArgs(args)
	if !ok || len(rest) != 0 {
		fmt.Fprintln(stderr, "usage: crew-run acp-turn [--run <run-id>] turn-NNNN")
		return 2
	}
	ctrlDir := resolveCtrlDir(e.Home, runID)
	turnDir := filepath.Join(ctrlDir, turnName)
	t := &turnRun{env: e, dir: turnDir, log: stderr}
	ex := t.runACP(ctrlDir)
	b, _ := json.MarshalIndent(ex, "", "  ")
	if err := writeFile(filepath.Join(turnDir, runspec.ExitFile), append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, "crew-run acp-turn: writing exit.json:", err)
		return 1
	}
	fmt.Fprintf(stdout, "crew-run acp-turn: %s %s\n", ex.Reason, ex.Detail)
	return 0
}

// acpSteer sends a live mid-turn `session/prompt` steer message to an active ACP session (design 11.6, 12.4):
//
//	crew-run acp-steer [--run <run-id>] turn-0001 [message]
func acpSteer(e env, args []string, stdout, stderr io.Writer) int {
	runID, turnName, rest, ok := parseRunAndTurnArgs(args)
	if !ok {
		fmt.Fprintln(stderr, "usage: crew-run acp-steer [--run <run-id>] turn-NNNN [message]")
		return 2
	}
	ctrlDir := resolveCtrlDir(e.Home, runID)
	turnDir := filepath.Join(ctrlDir, turnName)
	msg := strings.TrimSpace(strings.Join(rest, " "))
	if msg == "" {
		b, err := os.ReadFile(filepath.Join(turnDir, runspec.SteerFile))
		if err != nil {
			fmt.Fprintln(stderr, "crew-run acp-steer: reading steer.txt:", err)
			return 1
		}
		msg = strings.TrimSpace(string(b))
	}
	sessionID := readActiveSessionID(ctrlDir)
	if sessionID == "" {
		fmt.Fprintln(stderr, "crew-run acp-steer: no active session.id found")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := dialOrStartACPDaemon(ctx, e)
	if err != nil {
		fmt.Fprintln(stderr, "crew-run acp-steer: connecting to acp-daemon:", err)
		return 1
	}
	defer conn.Close()
	client := cli.NewACPClient(conn, conn)
	if ctrlKey := readCtrlKey(ctrlDir); ctrlKey != "" {
		if err := client.AuthSession(ctx, sessionID, ctrlKey); err != nil {
			fmt.Fprintln(stderr, "crew-run acp-steer:", err)
			return 1
		}
	}
	if err := client.Steer(ctx, sessionID, msg); err != nil {
		fmt.Fprintln(stderr, "crew-run acp-steer:", err)
		return 1
	}
	fmt.Fprintf(stdout, "crew-run acp-steer: steered %s\n", sessionID)
	return 0
}

// acpCancel cancels and closes an ACP session and optionally cleans up its worktree:
//
//	crew-run acp-cancel [--run <run-id>] [turn-NNNN]
func acpCancel(e env, args []string, stdout, stderr io.Writer) int {
	runID, _, _, _ := parseRunAndTurnArgs(args)
	ctrlDir := resolveCtrlDir(e.Home, runID)
	ctrlKey := readCtrlKey(ctrlDir)
	sessionID := readActiveSessionID(ctrlDir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if sessionID != "" {
		if conn, err := net.DialTimeout("unix", acpSocketPath(e.Home), 2*time.Second); err == nil {
			client := cli.NewACPClient(conn, conn)
			if ctrlKey != "" {
				_ = client.AuthSession(ctx, sessionID, ctrlKey)
			}
			_ = client.Cancel(ctx, sessionID)
			_ = client.CloseSession(ctx, sessionID)
			_ = conn.Close()
		}
	}
	if runID != "" {
		_ = removeWorktree(ctx, e, runID)
	}
	fmt.Fprintln(stdout, "crew-run acp-cancel: ok")
	return 0
}

func readActiveSessionID(ctrlDir string) string {
	if b, err := os.ReadFile(filepath.Join(ctrlDir, "session.id")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile(filepath.Join(ctrlDir, "run.json")); err == nil {
		var r runspec.Run
		if json.Unmarshal(b, &r) == nil {
			return strings.TrimSpace(r.Conversation)
		}
	}
	return ""
}

func (t *turnRun) stopAskedIn(ctrlDir string) (string, bool) {
	stopPath := filepath.Join(ctrlDir, "stop")
	if b, err := os.ReadFile(stopPath); err == nil {
		reason, ok := parseStopContent(string(b), t.ctrlKey)
		if !ok {
			_ = os.Remove(stopPath)
			return "", false
		}
		return reason, true
	}
	if ctrlDir == filepath.Join(t.env.Home, runspec.Dir) {
		return t.stopAsked()
	}
	return "", false
}

func (t *turnRun) touchIn(ctrlDir string) {
	now := time.Now()
	p := filepath.Join(ctrlDir, "heartbeat")
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, nil, 0o644)
	}
	t.touch()
}

func (t *turnRun) runACP(ctrlDir string) runspec.Exit {
	b, err := os.ReadFile(filepath.Join(ctrlDir, "run.json"))
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
	t.ctrlKey = readCtrlKey(ctrlDir)
	tokPath := filepath.Join(ctrlDir, "token")
	tok, err := os.ReadFile(tokPath)
	if err == nil {
		_ = os.Remove(tokPath)
		t.token = strings.TrimSpace(string(tok))
		if t.ctrlKey == "" && t.token != "" {
			acpTokenCache.Store(ctrlDir, t.token)
		}
	} else if t.ctrlKey == "" {
		if v, ok := acpTokenCache.Load(ctrlDir); ok {
			t.token, _ = v.(string)
		}
	}
	if t.token == "" {
		return own(runspec.StepToken, "the run's token: %v", err)
	}
	if reason, stop := t.stopAskedIn(ctrlDir); stop {
		return runspec.Exit{Exit: -1, Reason: reason, Detail: "stopped before the turn began"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	body, err := pullWithToken(ctx, t.env, "/v1/creds", t.token)
	if err != nil {
		return own(runspec.StepBroker, "the broker: %v", err)
	}
	var c credsAnswer
	if err := json.Unmarshal(body, &c); err != nil {
		return own(runspec.StepBroker, "the broker's answer: %v", err)
	}
	ctrlRel := resolveCtrlRel(t.env.Home, ctrlDir)
	unlock, lockErr := lockBareMirror(t.env.Home)
	if lockErr != nil {
		return own(runspec.StepCredentials, "the credentials: %v", lockErr)
	}
	_, applyErr := applyIn(t.env.Home, ctrlRel, c)
	unlock()
	if applyErr != nil {
		return own(runspec.StepCredentials, "the credentials: %v", applyErr)
	}
	t.values = c.Env
	if r := a.LoginCheck(ctx, commandRunner(t.env.Home, t.values), false); r.State == cli.LoginRefused {
		return runspec.Exit{Exit: -1, Reason: cli.ReasonLogin, Detail: r.Detail}
	}

	var mcpServers []cli.ACPMCPServer
	if t.spec.MCP != "" {
		if t.env.Broker == "" || !strings.HasPrefix(t.spec.MCP, "/") {
			return own(runspec.StepMCP, "run.json names Crew's MCP server, and there is no door to reach it at")
		}
		mcpURL := strings.TrimRight(t.env.Broker, "/") + t.spec.MCP
		if err := checkMCP(ctx, &cli.MCP{URL: mcpURL, Token: t.token}); err != nil {
			return own(runspec.StepMCP, "Crew's MCP server does not answer: %v", err)
		}
		mcpServers = []cli.ACPMCPServer{{
			Name:    "crew",
			Type:    "http",
			URL:     mcpURL,
			Headers: []cli.ACPHeader{{Name: "Authorization", Value: "Bearer " + t.token}},
		}}
	}

	ws, err := t.workspaceWorktree(ctx)
	if err != nil {
		return own(runspec.StepWorkspace, runspec.WorkspaceFailed+"%v", err)
	}
	defer t.clearWorktreeToken(ws)
	unlock, lockErr = lockBareMirror(t.env.Home)
	if lockErr != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", lockErr)
	}
	sanErr := cli.SanitizeHome(t.env.Home, ws, t.spec.Tools)
	unlock()
	if sanErr != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", sanErr)
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
	if ex, ok := t.setup(); !ok {
		return ex
	}
	unlock, lockErr = lockBareMirror(t.env.Home)
	if lockErr != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", lockErr)
	}
	sanErr = cli.SanitizeHome(t.env.Home, ws, t.spec.Tools)
	unlock()
	if sanErr != nil {
		return own(runspec.StepSettings, "the CLI's settings: %v", sanErr)
	}

	ex := t.callACP(ctrlDir, ws, prompt, mcpServers)
	t.clearWorktreeToken(ws)
	if sha, err := t.keepWIP(); err != nil {
		t.logf("the work in progress was not kept: %v", err)
		wipNotKept(&ex, err)
	} else {
		ex.WIP = sha
	}
	return ex
}

func (t *turnRun) callACP(ctrlDir, ws string, prompt []byte, mcpServers []cli.ACPMCPServer) runspec.Exit {
	spec := t.spec
	timeout := time.Duration(spec.Limits.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}

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

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 30*time.Second)
	conn, err := dialOrStartACPDaemon(dialCtx, t.env)
	dialCancel()
	if err != nil {
		return own(runspec.StepStart, "the CLI did not start: %v", err)
	}
	defer conn.Close()

	client := cli.NewACPClient(conn, conn)
	client.SetAllowTools(spec.Tools)
	turnCtx, turnCancel := context.WithTimeout(context.Background(), timeout+timeoutGrace)
	defer turnCancel()

	if err := client.Initialize(turnCtx); err != nil {
		return own(runspec.StepStart, "the CLI did not start: %v", err)
	}

	sessionID := strings.TrimSpace(spec.Conversation)
	if sessionID != "" {
		if t.ctrlKey != "" {
			_ = client.AuthSession(turnCtx, sessionID, t.ctrlKey)
		}
		if _, err := client.LoadSession(turnCtx, cli.LoadSessionParams{
			SessionID:  sessionID,
			Cwd:        ws,
			MCPServers: mcpServers,
		}); err != nil {
			sessionID = ""
		}
	}
	if sessionID == "" {
		ns, err := client.NewSession(turnCtx, cli.NewSessionParams{
			Cwd:        ws,
			MCPServers: mcpServers,
		})
		if err != nil {
			return own(runspec.StepStart, "the CLI did not start: %v", err)
		}
		sessionID = ns.SessionID
	}
	if t.ctrlKey != "" {
		if err := client.AuthSession(turnCtx, sessionID, t.ctrlKey); err != nil {
			return own(runspec.StepStart, "the CLI session auth failed: %v", err)
		}
	}
	_ = writeFile(filepath.Join(ctrlDir, "session.id"), []byte(sessionID+"\n"), 0o600)

	if modelID := spec.Model; modelID != "" {
		if spec.Effort != "" && !hasEffortSuffix(modelID) {
			if err := client.SetSessionModel(turnCtx, sessionID, modelID+"-"+spec.Effort); err != nil {
				if err2 := client.SetSessionModel(turnCtx, sessionID, modelID); err2 != nil {
					_, _ = fmt.Fprintf(errlog, "set_model %s: %v\n", modelID, err2)
				}
			}
		} else if err := client.SetSessionModel(turnCtx, sessionID, modelID); err != nil {
			_, _ = fmt.Fprintf(errlog, "set_model %s: %v\n", modelID, err)
		}
	}
	mode := cli.ACPModeBypass
	if !spec.Tools {
		mode = cli.ACPModePlan
	}
	_ = client.SetSessionMode(turnCtx, sessionID, mode)

	t.touchIn(ctrlDir)
	stopReason := ""
	var stopMu sync.Mutex
	doneWatch := make(chan struct{})
	go func() {
		beat := time.NewTicker(time.Second)
		defer beat.Stop()
		lastBeat := time.Now()
		deadline := time.Now().Add(timeout)
		for {
			select {
			case <-doneWatch:
				return
			case now := <-beat.C:
				if now.Sub(lastBeat) >= runspec.HeartbeatEvery {
					t.touchIn(ctrlDir)
					lastBeat = now
				}
				stopMu.Lock()
				already := stopReason != ""
				stopMu.Unlock()
				if !already {
					reason := ""
					if r, ok := t.stopAskedIn(ctrlDir); ok {
						reason = r
					} else if now.After(deadline) {
						reason = cli.ReasonTimeout
					}
					if reason != "" {
						stopMu.Lock()
						stopReason = reason
						stopMu.Unlock()
						cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						_ = client.Cancel(cctx, sessionID)
						cancel()
						turnCancel()
					}
				}
			}
		}
	}()

	var writeMu sync.Mutex
	_, resLine, promptErr := client.Prompt(turnCtx, sessionID, spec.Model, ws, string(prompt), func(line []byte, _ cli.Event) {
		writeMu.Lock()
		_, _ = events.Write(append(line, '\n'))
		writeMu.Unlock()
	})
	close(doneWatch)
	t.touchIn(ctrlDir)

	code := 0
	if promptErr != nil {
		code = 1
		_, _ = fmt.Fprintln(errlog, promptErr.Error())
	}
	errb, _ := readTail(filepath.Join(t.dir, runspec.StderrFile), 64<<10)
	out := t.a.Outcome(cli.TurnOutput{Result: resLine, Stderr: errb, Exit: code})
	ex := runspec.Exit{Exit: code, Reason: out.Reason, Detail: out.Detail, Conversation: sessionID}
	stopMu.Lock()
	sr := stopReason
	stopMu.Unlock()
	if sr != "" {
		ex.Reason, ex.Detail = sr, "stopped: "+sr+"; the CLI said: "+out.Detail
	}
	return ex
}

func hasEffortSuffix(modelID string) bool {
	for _, eff := range []string{"fast", "low", "medium", "high", "max"} {
		if strings.HasSuffix(modelID, "-"+eff) {
			return true
		}
	}
	return false
}

func dialOrStartACPDaemon(ctx context.Context, e env) (net.Conn, error) {
	sock := acpSocketPath(e.Home)
	if c, err := net.DialTimeout("unix", sock, 500*time.Millisecond); err == nil {
		return c, nil
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "crew-run"
	}
	cmd := exec.Command(exe, "acp-daemon", sock)
	cmd.Dir = e.Home
	cmd.Env = cleanEnv(e.Home)
	_ = cmd.Start()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acp-daemon socket %s did not become ready: %w", sock, ctx.Err())
		case <-ticker.C:
			if c, err := net.DialTimeout("unix", sock, 500*time.Millisecond); err == nil {
				return c, nil
			}
		}
	}
}
