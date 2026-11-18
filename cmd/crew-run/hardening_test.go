package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

func TestLockBareMirrorSerializesAcrossFileDescriptors(t *testing.T) {
	home := t.TempDir()
	lockDir := filepath.Join(home, filepath.Base(runspec.RepoMirror))
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	extFile, err := os.OpenFile(filepath.Join(lockDir, "crew.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer extFile.Close()
	if err := syscall.Flock(int(extFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		unlock, err := lockBareMirror(home)
		if err != nil {
			t.Errorf("lockBareMirror: %v", err)
			close(acquired)
			return
		}
		acquired <- unlock
	}()

	select {
	case <-acquired:
		t.Fatal("lockBareMirror acquired while external flock was held")
	case <-time.After(60 * time.Millisecond):
	}

	if err := syscall.Flock(int(extFile.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case unlock := <-acquired:
		if unlock != nil {
			unlock()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lockBareMirror did not acquire after external flock was released")
	}
}

func TestCleanEnvAndCredsStripForbiddenEnvVariables(t *testing.T) {
	for _, k := range []string{
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_GEMINI_BASE_URL",
		"AGY_ADC_AUTH",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_GENAI_USE_VERTEXAI",
		"AGY_LLM_GATEWAY_ENDPOINT",
		"AGY_BUSINESS_PAYGO_TIER",
		"AGY_CLI_DISABLE_SAFETY_FILTERING",
		"CREW_CTRL_KEY",
	} {
		t.Setenv(k, "secret-val")
		for _, kv := range cleanEnv("/home/agent") {
			if strings.HasPrefix(kv, k+"=") {
				t.Errorf("cleanEnv kept forbidden variable %q", kv)
			}
		}
		if k == "CREW_CTRL_KEY" {
			continue
		}
		if _, err := applyIn(t.TempDir(), ".crew/runs/r1", credsAnswer{Env: map[string]string{k: "x"}}); err == nil {
			t.Errorf("applyIn accepted forbidden variable %q", k)
		}
	}

	home := t.TempDir()
	if _, err := applyIn(home, ".crew/runs/r-scoped", credsAnswer{Env: map[string]string{"FOO": "bar"}}); err != nil {
		t.Fatalf("applyIn scoped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".crew", "creds.env")); !os.IsNotExist(err) {
		t.Fatalf("global .crew/creds.env should not be written by scoped applyIn, err=%v", err)
	}
	scopedEnv, err := os.ReadFile(filepath.Join(home, ".crew", "runs", "r-scoped", "creds.env"))
	if err != nil {
		t.Fatalf("reading scoped creds.env: %v", err)
	}
	for _, forbidden := range cli.ForbiddenEnvNames() {
		if !strings.Contains(string(scopedEnv), "unset "+forbidden+"\n") {
			t.Errorf("scoped creds.env missing unset %s:\n%s", forbidden, scopedEnv)
		}
	}

	asker := commandAsker(home, map[string]string{"GEMINI_API_KEY": "injected", "CREW_CTRL_KEY": "leaked", "HOME": "/wrong", "SAFE_VAR": "kept"})
	out, _, code, err := asker(context.Background(), nil, nil, "sh", "-c", "env")
	if err != nil || code != 0 {
		t.Fatalf("commandAsker env failed: code=%d err=%v", code, err)
	}
	envOut := string(out)
	for _, forbidden := range cli.ForbiddenEnvNames() {
		if strings.Contains(envOut, forbidden+"=") {
			t.Errorf("commandAsker kept forbidden variable %s in child env", forbidden)
		}
	}
	if strings.Contains(envOut, "CREW_CTRL_KEY=") || !strings.Contains(envOut, "SAFE_VAR=kept") {
		t.Errorf("unexpected child env filtering:\n%s", envOut)
	}
	if strings.Count(envOut, "\nHOME=")+(map[bool]int{true: 1}[strings.HasPrefix(envOut, "HOME=")]) != 1 || !strings.Contains(envOut, "HOME="+home+"\n") {
		t.Errorf("expected single HOME=%s entry, got:\n%s", home, envOut)
	}
}

func TestStopAskedRequiresMatchingCtrlKeyAndUnlinksForgedStop(t *testing.T) {
	home := t.TempDir()
	ctrlDir := filepath.Join(home, ".crew", "runs", "r1")
	if err := os.MkdirAll(ctrlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tr := &turnRun{env: env{Home: home}, ctrlKey: "secret-ctrl-key-1"}

	// Sibling writes a forged stop without knowing ctrlKey.
	stopPath := filepath.Join(ctrlDir, "stop")
	if err := os.WriteFile(stopPath, []byte("operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason, ok := tr.stopAskedIn(ctrlDir); ok {
		t.Fatalf("forged stop accepted with reason %q", reason)
	}
	if _, err := os.Stat(stopPath); !os.IsNotExist(err) {
		t.Fatalf("forged stop file was not removed")
	}

	// Sibling writes global .crew/stop: per-run stopAskedIn ignores it.
	if err := os.MkdirAll(filepath.Join(home, ".crew"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".crew", "stop"), []byte("secret-ctrl-key-1\noperator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason, ok := tr.stopAskedIn(ctrlDir); ok {
		t.Fatalf("global .crew/stop affected per-run session: %q", reason)
	}

	// Host writes authenticated stop with matching ctrlKey.
	if err := os.WriteFile(stopPath, []byte("secret-ctrl-key-1\ntimeout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason, ok := tr.stopAskedIn(ctrlDir); !ok || reason != "timeout" {
		t.Fatalf("authenticated stop = (%q, %v), want (\"timeout\", true)", reason, ok)
	}
}

func TestPrepareRunReRunsAfterSetupTampering(t *testing.T) {
	k := newTurnKit(t, true)
	k.writeRun(t, runspec.Run{
		Run:     "r1",
		Subtask: 1,
		CLI:     "agy",
		Model:   "gemini-3.8-flash",
		Tools:   true,
		Setup:   `printf '{"gcp":{"project":"evil"},"enableTelemetry":true,"useG1Credits":true}' > "$HOME/.gemini/antigravity-cli/settings.json"`,
		Limits:  runspec.Limits{TimeoutSeconds: 60, MaxTurns: 10},
		Git:     runspec.Git{Path: "/git/fixture.git", Branch: "task/1-x", Base: "refs/heads/main", WIP: "refs/crew/wip/r1"},
	})
	var out, errb bytes.Buffer
	if code := turn(k.env, []string{"turn-0001"}, &out, &errb); code != 0 {
		t.Fatalf("turn exit %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(k.home, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasGCP := doc["gcp"]; hasGCP {
		t.Fatalf("setup tampering with gcp survived PrepareRun after setup: %v", doc)
	}
	if doc["enableTelemetry"] != false || doc["useG1Credits"] != false {
		t.Fatalf("setup tampering with telemetry/credits survived PrepareRun: %v", doc)
	}
}

func TestInstructionsFromMainRejectsDeepPaths(t *testing.T) {
	k := newTurnKit(t, true)
	deepRel := ".agents/skills/" + strings.Repeat("sub/", 35) + "SKILL.md"
	pushTo(t, k, "refs/heads/main", map[string]string{deepRel: "deep skill"})
	k.writeRun(t, runspec.Run{
		Run:              "r-deep",
		Subtask:          1,
		CLI:              "agy",
		Model:            "gemini-3.8-flash",
		Tools:            true,
		MainInstructions: true,
		Limits:           runspec.Limits{TimeoutSeconds: 60, MaxTurns: 10},
		Git:              runspec.Git{Path: "/git/fixture.git", Branch: "task/1-x", Base: "refs/heads/main"},
	})
	var out, errb bytes.Buffer
	if code := turn(k.env, []string{"turn-0001"}, &out, &errb); code != 0 {
		t.Fatalf("turn exit %d: %s", code, errb.String())
	}
	exitBytes, err := os.ReadFile(filepath.Join(k.home, runspec.TurnDir(1), runspec.ExitFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exitBytes), "deeper than") {
		t.Fatalf("expected depth error in exit.json, got %s", exitBytes)
	}
}

func TestACPSessionAuthBlocksSiblingSteerAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	readyCh := make(chan struct{})
	close(readyCh)
	childR, childW := net.Pipe()
	defer childR.Close()
	defer childW.Close()

	amux := &acpMux{
		readyCh:    readyCh,
		initResult: json.RawMessage(`{"protocolVersion":"1"}`),
		pending:    make(map[int64]muxPending),
		conns:      make(map[*muxConn]struct{}),
		bySess:     make(map[string]map[*muxConn]struct{}),
		sessKey:    make(map[string]string),
		childStdin: childW,
	}

	// Owner connection registers ctrlKey for sess-1.
	ownerC1, ownerC2 := net.Pipe()
	defer ownerC1.Close()
	defer ownerC2.Close()
	ownerMC := &muxConn{c: ownerC1, authed: make(map[string]bool)}
	amux.conns[ownerMC] = struct{}{}
	go amux.serveConn(ctx, ownerMC)

	ownerClient := cli.NewACPClient(ownerC2, ownerC2)
	if err := ownerClient.AuthSession(ctx, "sess-1", "key-owner-1"); err != nil {
		t.Fatalf("owner AuthSession: %v", err)
	}

	// Sibling connection without ctrlKey cannot steer or cancel sess-1.
	sibC1, sibC2 := net.Pipe()
	defer sibC1.Close()
	defer sibC2.Close()
	sibMC := &muxConn{c: sibC1, authed: make(map[string]bool)}
	amux.conns[sibMC] = struct{}{}
	go amux.serveConn(ctx, sibMC)

	sibClient := cli.NewACPClient(sibC2, sibC2)
	if err := sibClient.AuthSession(ctx, "sess-1", "wrong-key"); err == nil {
		t.Fatal("sibling AuthSession with wrong key succeeded")
	}
	if err := sibClient.Steer(ctx, "sess-1", "malicious steer"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("sibling Steer without auth err = %v, want unauthorized", err)
	}
	if err := sibClient.Cancel(ctx, "sess-1"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("sibling Cancel without auth err = %v, want unauthorized", err)
	}

	// Even before a control key is registered (sessKey["sess-2"] == ""), a session created by ownerMC
	// rejects both unauthenticated steer/cancel AND first-claim hijacking via crew/authSession from sibMC.
	amux.mu.Lock()
	amux.bySess["sess-2"] = map[*muxConn]struct{}{ownerMC: {}}
	ownerMC.authed["sess-2"] = true
	amux.mu.Unlock()
	if err := sibClient.Steer(ctx, "sess-2", "unauthed steer before key"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected fail-closed unauthorized steer before key registration, got %v", err)
	}
	if err := sibClient.AuthSession(ctx, "sess-2", "hijack-key"); err == nil {
		t.Fatal("expected sibling first-claim AuthSession hijack to be rejected")
	}
	if err := ownerClient.AuthSession(ctx, "sess-2", "owner-key-2"); err != nil {
		t.Fatalf("owner initial AuthSession on sess-2 failed: %v", err)
	}
}

func TestClearWorktreeTokenRemovesHeaderAfterSetupContextExpires(t *testing.T) {
	k := newTurnKit(t, true)
	const wantTok = "run-token-0123456789"
	tr := &turnRun{
		env:   env{Home: k.home, Broker: k.env.Broker},
		token: wantTok,
		spec: runspec.Run{
			Run:   "r-clear-tok",
			Tools: true,
			Git:   runspec.Git{Path: "/git/fixture.git", Branch: "task/1-x", Base: "refs/heads/main", WIP: "refs/crew/wip/r1"},
		},
	}
	ws, err := tr.workspaceWorktree(context.Background())
	if err != nil {
		t.Fatalf("workspaceWorktree: %v", err)
	}
	cfgPath := filepath.Join(k.home, "repo.git", "worktrees", "r-clear-tok", "config.worktree")
	before, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(before), wantTok) {
		t.Fatalf("expected write token in config.worktree during turn, got %q (err %v)", before, err)
	}
	tr.clearWorktreeToken(ws)
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), wantTok) || strings.Contains(string(after), "extraHeader") {
		t.Fatalf("clearWorktreeToken left bearer token in config.worktree: %s", after)
	}
}
