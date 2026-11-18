package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeACPServer simulates `agy acp --stdio --process-per-session` over an in-memory pipe pair.
type fakeACPServer struct {
	mu       sync.Mutex
	inited   bool
	sessions map[string]*fakeACPSession
	steered  []string
	canceled []string
	closed   []string
}

type fakeACPSession struct {
	id         string
	cwd        string
	model      string
	mode       string
	mcpServers []ACPMCPServer
}

func startFakeACPServer(t *testing.T) (*ACPClient, *fakeACPServer) {
	t.Helper()
	cToSR, cToSW := io.Pipe()
	sToCR, sToCW := io.Pipe()
	t.Cleanup(func() {
		_ = cToSW.Close()
		_ = sToCW.Close()
	})
	srv := &fakeACPServer{sessions: make(map[string]*fakeACPSession)}
	go srv.serve(cToSR, sToCW)
	return NewACPClient(sToCR, cToSW), srv
}

func (s *fakeACPServer) serve(r io.Reader, w io.Writer) {
	sc := bufio.NewScanner(r)
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(append(b, '\n'))
	}
	for sc.Scan() {
		var req rpcEnvelope
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		id := req.ID
		switch req.Method {
		case "initialize":
			s.mu.Lock()
			s.inited = true
			s.mu.Unlock()
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"protocolVersion": 1},
			})
		case "session/new":
			var p NewSessionParams
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			sid := "sess-1"
			if len(s.sessions) > 0 {
				sid = "sess-2"
			}
			s.sessions[sid] = &fakeACPSession{id: sid, cwd: p.Cwd, model: p.Model, mcpServers: p.MCPServers}
			s.mu.Unlock()
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"sessionId": sid},
			})
		case "session/load":
			var p LoadSessionParams
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			s.sessions[p.SessionID] = &fakeACPSession{id: p.SessionID, cwd: p.Cwd, model: p.Model, mcpServers: p.MCPServers}
			s.mu.Unlock()
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"sessionId": p.SessionID},
			})
		case "session/set_model":
			var p struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			if sess := s.sessions[p.SessionID]; sess != nil {
				sess.model = p.ModelID
			}
			s.mu.Unlock()
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/set_mode":
			var p struct {
				SessionID string `json:"sessionId"`
				ModeID    string `json:"modeId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			if sess := s.sessions[p.SessionID]; sess != nil {
				sess.mode = p.ModeID
			}
			s.mu.Unlock()
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/prompt":
			var p struct {
				SessionID string            `json:"sessionId"`
				Prompt    []ACPContentBlock `json:"prompt"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			if strings.HasPrefix(text, "STEER:") {
				s.mu.Lock()
				s.steered = append(s.steered, text)
				s.mu.Unlock()
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
				continue
			}
			// Emit streaming tool call (pending + in_progress for the same toolCallId) + completed + message chunk.
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "call-1",
						"title":         "read_file",
						"status":        "pending",
						"rawInput":      map[string]any{"path": "README.md"},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"toolCallId":    "call-1",
						"title":         "read_file",
						"status":        "in_progress",
						"rawInput":      map[string]any{"path": "README.md"},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"toolCallId":    "call-1",
						"title":         "read_file",
						"status":        "completed",
						"rawInput":      map[string]any{"path": "README.md"},
						"rawOutput":     "ok",
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Hello from " + p.SessionID},
					},
				},
			})
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"stopReason": "end_turn",
					"usage": map[string]any{
						"input_tokens":      120,
						"output_tokens":     45,
						"cache_read_tokens": 30,
					},
				},
			})
		case "session/cancel":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			s.canceled = append(s.canceled, p.SessionID)
			s.mu.Unlock()
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/close":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			s.closed = append(s.closed, p.SessionID)
			s.mu.Unlock()
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		}
	}
}

func TestACPClientLifecycleStreamingSteerAndIsolation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, srv := startFakeACPServer(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Start 2 isolated sessions in separate worktrees with distinct per-session MCP bearer tokens.
	s1, err := client.NewSession(ctx, NewSessionParams{
		Cwd:   "/home/agent/worktrees/run-1",
		Model: "gemini-4-argon-high",
		MCPServers: []ACPMCPServer{{
			Type:    "http",
			Name:    MCPServer,
			URL:     "http://10.9.1.254:8080/mcp",
			Headers: []ACPHeader{{Name: "Authorization", Value: "Bearer tok-builder"}},
		}},
	})
	if err != nil || s1.SessionID != "sess-1" {
		t.Fatalf("NewSession 1: %+v, err=%v", s1, err)
	}
	s2, err := client.NewSession(ctx, NewSessionParams{
		Cwd:   "/home/agent/worktrees/run-2",
		Model: "gemini-3.8-flash-high",
		MCPServers: []ACPMCPServer{{
			Type:    "http",
			Name:    MCPServer,
			URL:     "http://10.9.1.254:8080/mcp",
			Headers: []ACPHeader{{Name: "Authorization", Value: "Bearer tok-chat-readonly"}},
		}},
	})
	if err != nil || s2.SessionID != "sess-2" {
		t.Fatalf("NewSession 2: %+v, err=%v", s2, err)
	}

	if err := client.SetSessionModel(ctx, s1.SessionID, "gemini-4-argon-high"); err != nil {
		t.Fatal(err)
	}
	if err := client.SetSessionMode(ctx, s1.SessionID, ACPModeBypass); err != nil {
		t.Fatal(err)
	}

	var events []Event
	pr, resLine, err := client.Prompt(ctx, s1.SessionID, "gemini-4-argon-high", "/home/agent/worktrees/run-1", "Build feature", func(_ []byte, ev Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if pr.StopReason != "end_turn" {
		t.Fatalf("unexpected stopReason: %s", pr.StopReason)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events (init, tool_call, tool_result, message, result), got %d: %+v", len(events), events)
	}
	out := (Agy{}).Outcome(TurnOutput{Result: resLine, Exit: 0})
	if out.Reason != ReasonOK || out.Response != "Hello from sess-1" {
		t.Fatalf("unexpected Outcome: %+v", out)
	}
	usage := (Agy{}).Usage(resLine)
	if usage.TokensIn != 120 || usage.TokensOut != 45 || usage.TokensCached != 30 {
		t.Fatalf("unexpected Usage: %+v", usage)
	}

	// Live mid-turn steering (`Tell the agent`).
	if err := client.Steer(ctx, s1.SessionID, "STEER: also update the test"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if err := client.Cancel(ctx, s2.SessionID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := client.CloseSession(ctx, s1.SessionID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.steered) != 1 || srv.steered[0] != "STEER: also update the test" {
		t.Fatalf("expected steer recorded, got %v", srv.steered)
	}
	if len(srv.canceled) != 1 || srv.canceled[0] != "sess-2" {
		t.Fatalf("expected cancel recorded, got %v", srv.canceled)
	}
	if srv.sessions["sess-1"].mcpServers[0].Headers[0].Value == srv.sessions["sess-2"].mcpServers[0].Headers[0].Value {
		t.Fatalf("sessions must have distinct MCP bearer tokens")
	}
}

func TestSanitizeHomeResetsDotfilesAndSettings(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("alias git='evil'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeHome(home, "/home/agent/worktrees/run-1", true); err != nil {
		t.Fatalf("SanitizeHome: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil || strings.Contains(string(b), "evil") {
		t.Fatalf(".bashrc was not sanitized: %s", b)
	}
}

func TestACPUsageCamelCaseAndFlatUsageUpdate(t *testing.T) {
	t.Parallel()
	u := (Agy{}).Usage([]byte(`{"event":"result","result":{"usage":{"inputTokens":2400,"outputTokens":310,"cachedReadTokens":1800}}}`))
	if !u.HasTokens || u.TokensIn != 2400 || u.TokensOut != 310 || u.TokensCached != 1800 {
		t.Fatalf("camelCase Usage = %+v", u)
	}
	u2 := (Agy{}).Usage([]byte(`{"event":"result","result":{"usage":{"inputTokens":500,"outputTokens":0,"cacheReadTokens":100}}}`))
	if !u2.HasTokens || u2.TokensIn != 500 || u2.TokensOut != 0 || u2.TokensCached != 100 {
		t.Fatalf("cacheReadTokens Usage = %+v", u2)
	}
	var stepIdx atomic.Int64
	var mu sync.Mutex
	var resp strings.Builder
	var lastUsage map[string]any
	seen := map[string]bool{}
	handleACPSessionUpdate("s1", json.RawMessage(`{"sessionUpdate":"usage_update","inputTokens":900,"outputTokens":75,"cachedReadTokens":400}`), &stepIdx, &mu, &resp, &lastUsage, seen, nil)
	resLine := emitACPResult("s1", "done", PromptResponse{StopReason: "end_turn"}, nil, lastUsage, stepIdx.Load(), time.Second, nil)
	u3 := (Agy{}).Usage(resLine)
	if !u3.HasTokens || u3.TokensIn != 900 || u3.TokensOut != 75 || u3.TokensCached != 400 {
		t.Fatalf("flat usage_update Usage = %+v", u3)
	}
}

func TestACPClientRejectsDisabledSubagentPermissions(t *testing.T) {
	t.Parallel()
	cToSR, cToSW := io.Pipe()
	sToCR, sToCW := io.Pipe()
	t.Cleanup(func() {
		_ = cToSW.Close()
		_ = sToCW.Close()
	})
	client := NewACPClient(sToCR, cToSW)
	client.SetAllowTools(true)

	askPermission := func(id int, toolName string) string {
		req, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "session/request_permission",
			"params": map[string]any{
				"sessionId": "sess-1",
				"toolCall":  map[string]any{"name": toolName},
				"options": []map[string]any{
					{"optionId": "allow_once", "kind": "allow_once"},
					{"optionId": "reject_once", "kind": "reject_once"},
				},
			},
		})
		_, _ = sToCW.Write(append(req, '\n'))
		buf := make([]byte, 4096)
		n, err := cToSR.Read(buf)
		if err != nil {
			t.Fatalf("read permission reply for %s: %v", toolName, err)
		}
		var reply struct {
			Result struct {
				Outcome struct {
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		if err := json.Unmarshal(buf[:n], &reply); err != nil {
			t.Fatalf("decode reply for %s: %v", toolName, err)
		}
		return reply.Result.Outcome.OptionID
	}

	if got := askPermission(1, "run_command"); got != "allow_once" {
		t.Fatalf("run_command optionId = %q, want allow_once", got)
	}
	for i, tool := range []string{"invoke_subagent", "swarm", "define_subagent", "run_workflow"} {
		if got := askPermission(i+2, tool); got != "reject_once" {
			t.Fatalf("%s optionId = %q, want reject_once", tool, got)
		}
	}
}
