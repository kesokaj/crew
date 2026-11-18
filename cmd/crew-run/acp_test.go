package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/runspec"
)

const fakeACPScript = `#!/usr/bin/env python3
import json, os, sys

seq = 0
sessions = {}
pending_prompts = {}

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    req = json.loads(raw)
    mid = req.get("id")
    method = req.get("method", "")
    params = req.get("params") or {}

    if not method and mid in pending_prompts:
        p = pending_prompts.pop(mid)
        perm_option = ((req.get("result") or {}).get("outcome") or {}).get("optionId", "")
        sid = p["sid"]
        text = p["text"]
        orig_id = p["mid"]
        info = sessions.get(sid) or {}
        cwd = info.get("cwd", "")
        if cwd and os.path.isdir(cwd):
            with open(os.path.join(cwd, "acp-session.txt"), "w") as f:
                f.write(json.dumps({"sessionId": sid, "mcp": info.get("mcp"), "permOption": perm_option}))
        upd = {
            "jsonrpc": "2.0",
            "method": "session/update",
            "params": {
                "sessionId": sid,
                "update": {
                    "sessionUpdate": "agent_message_chunk",
                    "content": {"type": "text", "text": f"handled:{text}"},
                },
            },
        }
        print(json.dumps(upd), flush=True)
        resp = {
            "jsonrpc": "2.0",
            "id": orig_id,
            "result": {
                "stopReason": "end_turn",
                "usage": {"inputTokens": 20, "outputTokens": 10},
            },
        }
        print(json.dumps(resp), flush=True)
        continue

    if method == "initialize":
        resp = {"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentCapabilities": {"loadSession": True}}}
        print(json.dumps(resp), flush=True)
    elif method == "session/new":
        seq += 1
        sid = f"acp-sess-{seq}"
        cwd = params.get("cwd", "")
        mcp = params.get("mcpServers") or []
        sessions[sid] = {"cwd": cwd, "mcp": mcp}
        if cwd and os.path.isdir(cwd):
            with open(os.path.join(cwd, "acp-session.txt"), "w") as f:
                f.write(json.dumps({"sessionId": sid, "mcp": mcp}))
        resp = {"jsonrpc": "2.0", "id": mid, "result": {"sessionId": sid}}
        print(json.dumps(resp), flush=True)
    elif method == "session/load":
        sid = params.get("sessionId", "")
        cwd = params.get("cwd", "")
        mcp = params.get("mcpServers") or []
        sessions[sid] = {"cwd": cwd, "mcp": mcp}
        resp = {"jsonrpc": "2.0", "id": mid, "result": {}}
        print(json.dumps(resp), flush=True)
    elif method in ("session/set_model", "session/set_mode", "session/cancel", "session/close"):
        resp = {"jsonrpc": "2.0", "id": mid, "result": {}}
        print(json.dumps(resp), flush=True)
    elif method == "session/prompt":
        sid = params.get("sessionId", "")
        blocks = params.get("prompt") or []
        text = blocks[0].get("text", "") if blocks else ""
        seq += 1
        perm_id = f"jetski-acp-front:{seq}"
        pending_prompts[perm_id] = {"mid": mid, "sid": sid, "text": text}
        perm_req = {
            "jsonrpc": "2.0",
            "id": perm_id,
            "method": "session/request_permission",
            "params": {
                "sessionId": sid,
                "options": [
                    {"optionId": "allow", "name": "Allow", "kind": "allow_once"},
                    {"optionId": "deny", "name": "Deny", "kind": "reject_once"},
                ],
            },
        }
        print(json.dumps(perm_req), flush=True)
`

func TestACPDaemonConcurrentWorktreesAndScopedTokens(t *testing.T) {
	bin := t.TempDir()
	acpScript := filepath.Join(bin, "fake-acp.py")
	if err := os.WriteFile(acpScript, []byte(fakeACPScript), 0o755); err != nil {
		t.Fatal(err)
	}
	agyStub := "#!/bin/sh\nif [ \"$1\" = models ]; then\n  printf 'gemini-4-argon\\tGemini 4 Argon\\n'\n  exit 0\nfi\nexec python3 " + acpScript + "\n"
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(agyStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CREW_ACP_CMD", "python3 "+acpScript)

	ep := &gitendpoint.Endpoint{Dir: t.TempDir()}
	ctx := context.Background()
	if err := ep.Ensure(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := ep.Init(ctx, "fixture", map[string]string{"README.md": "hello\n"}, "Initial"); err != nil {
		t.Fatal(err)
	}

	const tokBuilder = "tok-builder-secret-1111"
	const tokChat = "tok-chat-readonly-2222"

	ep.Auth = func(r *http.Request, repo string) (gitendpoint.Access, error) {
		switch gitendpoint.Token(r) {
		case tokBuilder:
			return gitendpoint.Access{
				Repo: "fixture", Who: "r-builder",
				Read: []string{"refs/heads/main", "refs/heads/task/1-a"},
				Push: []string{"refs/heads/task/1-a", "refs/crew/wip/r-builder"},
			}, nil
		case tokChat:
			return gitendpoint.Access{
				Repo: "fixture", Who: "r-chat",
				Read: []string{"refs/heads/main", "refs/heads/task/1-a"},
			}, nil
		default:
			return gitendpoint.Access{}, gitendpoint.ErrRefused
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/git/", ep)
	mux.HandleFunc("GET /v1/creds", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+tokBuilder && auth != "Bearer "+tokChat {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		files := []any{
			map[string]string{
				"path": ".gemini/antigravity-cli/antigravity-oauth-token",
				"mode": "0600",
				"data": b64(`{"token":{"access_token":"made-up"}}`),
			},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"env":   map[string]string{"CREW_GIT_NAME": "Op", "CREW_GIT_EMAIL": "op@example.com"},
			"files": files,
		})
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+tokBuilder && auth != "Bearer "+tokChat {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	sockPath := filepath.Join(home, ".crew", "acp.sock")
	t.Setenv("CREW_ACP_SOCK", sockPath)
	e := env{Home: home, Broker: srv.URL}

	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	t.Cleanup(stopDaemon)
	go func() {
		var errb bytes.Buffer
		_ = runACPDaemon(daemonCtx, e, sockPath, &errb)
	}()

	setupRun := func(runID, token, branch, prompt string, tools, refresh bool, wip string) {
		ctrl := filepath.Join(home, runspec.RunControlDir(runID))
		tdir := filepath.Join(home, runspec.RunTurnDir(runID, 1))
		if err := os.MkdirAll(tdir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ctrl, "token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ctrl, "ctrl.key"), []byte("ctrl-"+runID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tdir, runspec.PromptFile), []byte(prompt), 0o644); err != nil {
			t.Fatal(err)
		}
		spec := runspec.Run{
			Run:     runID,
			Subtask: 1,
			Project: "proj-1",
			CLI:     "agy",
			Model:   "gemini-4-argon",
			Tools:   tools,
			MCP:     "/mcp",
			Setup:   "echo setup-ok > .setup-ran",
			Limits:  runspec.Limits{TimeoutSeconds: 60, MaxTurns: 20},
			Git: runspec.Git{
				Path:    "/git/fixture.git",
				Branch:  branch,
				Base:    "refs/heads/main",
				WIP:     wip,
				Refresh: refresh,
			},
		}
		b, _ := json.Marshal(spec)
		if err := os.WriteFile(filepath.Join(ctrl, "run.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	setupRun("r-builder", tokBuilder, "task/1-a", "Build feature A", true, false, "refs/crew/wip/r-builder")
	setupRun("r-chat", tokChat, "task/1-a", "Explain ticket A on same branch", false, true, "")

	var wg sync.WaitGroup
	var codeBuilder, codeChat int
	var errBuilder, errChat bytes.Buffer
	wg.Add(2)
	go func() {
		defer wg.Done()
		var out bytes.Buffer
		codeBuilder = acpTurn(e, []string{"--run", "r-builder", "turn-0001"}, &out, &errBuilder)
	}()
	go func() {
		defer wg.Done()
		var out bytes.Buffer
		codeChat = acpTurn(e, []string{"--run", "r-chat", "turn-0001"}, &out, &errChat)
	}()
	wg.Wait()

	if codeBuilder != 0 {
		t.Fatalf("r-builder exit %d: %s", codeBuilder, errBuilder.String())
	}
	if codeChat != 0 {
		t.Fatalf("r-chat exit %d: %s", codeChat, errChat.String())
	}

	readExit := func(runID string) runspec.Exit {
		var ex runspec.Exit
		b, err := os.ReadFile(filepath.Join(home, runspec.RunTurnDir(runID, 1), runspec.ExitFile))
		if err != nil {
			t.Fatalf("read %s exit.json: %v", runID, err)
		}
		if err := json.Unmarshal(b, &ex); err != nil {
			t.Fatal(err)
		}
		if ex.Reason != "ok" {
			t.Fatalf("%s failed: %+v (stderr: builder=%q chat=%q)", runID, ex, errBuilder.String(), errChat.String())
		}
		return ex
	}
	exBuilder := readExit("r-builder")
	_ = readExit("r-chat")

	// Verify both worktrees exist independently and each received only its own MCP bearer token.
	wtBuilder := worktreeDir(home, "r-builder")
	wtChat := worktreeDir(home, "r-chat")
	for _, wt := range []string{wtBuilder, wtChat} {
		if _, err := os.Stat(filepath.Join(wt, ".setup-ran")); err != nil {
			t.Fatalf("expected setup to run in worktree %s: %v", wt, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bareMirrorDir(home), "hooks")); !os.IsNotExist(err) {
		t.Fatalf("expected repo.git/hooks to be removed")
	}
	bareCfg, err := os.ReadFile(filepath.Join(bareMirrorDir(home), "config"))
	if err != nil || !strings.Contains(string(bareCfg), "repositoryformatversion = 1") {
		t.Fatalf("expected core.repositoryformatversion = 1 in repo.git/config, got: %s", bareCfg)
	}
	bInfo, err := os.ReadFile(filepath.Join(wtBuilder, "acp-session.txt"))
	if err != nil {
		t.Fatalf("read builder acp-session.txt: %v", err)
	}
	if !strings.Contains(string(bInfo), tokBuilder) || strings.Contains(string(bInfo), tokChat) || !strings.Contains(string(bInfo), `"permOption": "allow"`) {
		t.Fatalf("builder session MCP/perm info leaked or unexpected: %s", bInfo)
	}
	cInfo, err := os.ReadFile(filepath.Join(wtChat, "acp-session.txt"))
	if err != nil {
		t.Fatalf("read chat acp-session.txt: %v", err)
	}
	if !strings.Contains(string(cInfo), tokChat) || strings.Contains(string(cInfo), tokBuilder) || !strings.Contains(string(cInfo), `"permOption": "deny"`) {
		t.Fatalf("chat session MCP/perm info leaked or unexpected: %s", cInfo)
	}

	// Verify exit.json and WIP push for builder.
	if exBuilder.Reason != "ok" || exBuilder.Conversation == "" || exBuilder.WIP == "" || exBuilder.WIPNotKept {
		t.Fatalf("unexpected builder exit: %+v", exBuilder)
	}

	// Verify live steering (`crew-run acp-steer`) on builder's session requires its control key.
	if err := os.WriteFile(filepath.Join(home, runspec.RunControlDir("r-builder"), "ctrl.key"), []byte("ctrl-r-builder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var steerOut, steerErr bytes.Buffer
	if code := acpSteer(e, []string{"--run", "r-builder", "turn-0001", "Focus on edge case B"}, &steerOut, &steerErr); code != 0 {
		t.Fatalf("acp-steer exit %d: %s", code, steerErr.String())
	}
	if !strings.Contains(steerOut.String(), exBuilder.Conversation) {
		t.Fatalf("expected steer output to name %s, got %q", exBuilder.Conversation, steerOut.String())
	}

	// Verify `crew-run acp-cancel` closes session and removes the worktree.
	if err := os.WriteFile(filepath.Join(home, runspec.RunControlDir("r-chat"), "ctrl.key"), []byte("ctrl-r-chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cancelOut, cancelErr bytes.Buffer
	if code := acpCancel(e, []string{"--run", "r-chat", "turn-0001"}, &cancelOut, &cancelErr); code != 0 {
		t.Fatalf("acp-cancel exit %d: %s", code, cancelErr.String())
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := os.Stat(wtChat); !os.IsNotExist(err) {
		t.Fatalf("expected worktree %s to be removed after acp-cancel, err=%v", wtChat, err)
	}

	// Verify failPendingOnChildExit drains pending requests with a JSON-RPC error response.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	amux := &acpMux{
		pending: map[int64]muxPending{
			7: {conn: &muxConn{c: c1}, clientID: json.RawMessage(`42`), method: "session/prompt"},
		},
		bySess: map[string]map[*muxConn]struct{}{
			"sess-1": {&muxConn{c: c1}: {}},
		},
	}
	doneRead := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := c2.Read(buf)
		doneRead <- string(buf[:n])
	}()
	amux.failPendingOnChildExit()
	gotErrLine := <-doneRead
	if !strings.Contains(gotErrLine, `"id":42`) || !strings.Contains(gotErrLine, "acp child process exited") {
		t.Fatalf("unexpected child-exit error line: %s", gotErrLine)
	}
	if len(amux.pending) != 0 || len(amux.bySess) != 0 {
		t.Fatalf("expected pending and bySess cleared, got %d %d", len(amux.pending), len(amux.bySess))
	}

	// Verify unrouted session/request_permission fails closed with reject_once and does not leak to a different session's connection.
	childR, childW := net.Pipe()
	defer childR.Close()
	defer childW.Close()
	mc1 := &muxConn{c: c1}
	amux.conns = map[*muxConn]struct{}{mc1: {}}
	amux.bySess = map[string]map[*muxConn]struct{}{"sess-1": {mc1: {}}}
	amux.childStdin = childW
	doneChild := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := childR.Read(buf)
		doneChild <- string(buf[:n])
	}()
	amux.dispatchChildLine([]byte(`{"jsonrpc":"2.0","id":"jetski-acp-front:77","method":"session/request_permission","params":{"sessionId":"sess-unmapped","options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"deny","kind":"reject_once"}]}}`), 1, nil)
	gotChildReply := <-doneChild
	if !strings.Contains(gotChildReply, `"id":"jetski-acp-front:77"`) || !strings.Contains(gotChildReply, `"deny"`) {
		t.Fatalf("expected fail-closed deny on unmapped session/request_permission, got: %s", gotChildReply)
	}

	// Verify unmapped/empty session/update notifications are never broadcast to unrelated connections.
	readNonBlocking := make(chan string, 2)
	go func() {
		for {
			buf := make([]byte, 512)
			n, err := c2.Read(buf)
			if err != nil {
				return
			}
			readNonBlocking <- string(buf[:n])
		}
	}()
	amux.dispatchChildLine([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-unmapped","update":{"sessionUpdate":"agent_message_chunk"}}}`), 1, nil)
	amux.dispatchChildLine([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk"}}}`), 1, nil)
	select {
	case leaked := <-readNonBlocking:
		t.Fatalf("unmapped session/update leaked to unrelated connection: %s", leaked)
	case <-time.After(30 * time.Millisecond):
	}
	amux.dispatchChildLine([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk"}}}`), 1, nil)
	select {
	case got := <-readNonBlocking:
		if !strings.Contains(got, `"sessionId":"sess-1"`) {
			t.Fatalf("unexpected mapped session/update payload: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mapped session/update")
	}

	if hasEffortSuffix("gemini-3.8-flash") {
		t.Fatalf("expected gemini-3.8-flash not to have an effort suffix")
	}
	if !hasEffortSuffix("gemini-3.8-flash-low") || !hasEffortSuffix("gpt-oss-120b-medium") {
		t.Fatalf("expected effort suffixes to be detected")
	}
}
