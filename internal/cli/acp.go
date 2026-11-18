package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kesokaj/crew/internal/homepath"
)

// ACPModeBypass is the `agy acp` mode for full access inside a sandbox (12.2).
const (
	ACPModeBypass = "bypassPermissions"
	ACPModePlan   = "plan"
)

// ACPCommand returns the command line that starts `agy` as a warm Agent Client Protocol (ACP)
// JSON-RPC 2.0 server with one worker process per session (design 12.2, 12.4, since 1.37).
func ACPCommand() []string {
	return (Agy{}).ACPCommand()
}

const defaultBashrc = `# ~/.bashrc: reset by crew-run before each session and check
export PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
`

const defaultProfile = `# ~/.profile: reset by crew-run before each session and check
export PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
`

// SanitizeHome resets `.bashrc`, `.profile`, `settings.json` (`useG1Credits: false`, provider keys
// stripped, `trustedWorkspaces` set), and `config.json` (`remoteControlEnabled: false`) in a
// persistent project home before each ACP session or check (design 12.2, 12.4, 13.5).
func SanitizeHome(home, workspace string, tools bool) error {
	if err := homepath.WriteFile(home, ".bashrc", []byte(defaultBashrc), 0o644); err != nil {
		return err
	}
	if err := homepath.WriteFile(home, ".profile", []byte(defaultProfile), 0o644); err != nil {
		return err
	}
	return (Agy{}).PrepareRun(home, workspace, tools, nil)
}

// ACPHeader is a single HTTP header for an ACP session's MCP server configuration.
type ACPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ACPMCPServer is one MCP server passed in `NewSessionParams.mcpServers` or `LoadSessionParams.mcpServers`.
type ACPMCPServer struct {
	Type    string      `json:"type"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Headers []ACPHeader `json:"headers,omitempty"`
}

// NewSessionParams are the parameters for `session/new`.
type NewSessionParams struct {
	Cwd        string         `json:"cwd"`
	MCPServers []ACPMCPServer `json:"mcpServers,omitempty"`
	Model      string         `json:"model,omitempty"`
}

// NewSessionResponse is the result of `session/new`.
type NewSessionResponse struct {
	SessionID string `json:"sessionId"`
}

// LoadSessionParams are the parameters for `session/load`.
type LoadSessionParams struct {
	SessionID  string         `json:"sessionId"`
	Cwd        string         `json:"cwd"`
	MCPServers []ACPMCPServer `json:"mcpServers,omitempty"`
	Model      string         `json:"model,omitempty"`
}

// LoadSessionResponse is the result of `session/load`.
type LoadSessionResponse struct {
	SessionID string `json:"sessionId"`
}

// ACPContentBlock is a content block in `session/prompt` or `session/update`.
type ACPContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptResponse is the result of `session/prompt`.
type PromptResponse struct {
	StopReason string         `json:"stopReason"`
	Usage      map[string]any `json:"usage,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("acp error (%d): %s", e.Code, e.Message)
}

// ACPClient is a concurrent JSON-RPC 2.0 client for `agy acp --stdio --process-per-session`.
type ACPClient struct {
	w         io.Writer
	writeMu   sync.Mutex
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan rpcEnvelope
	subMu     sync.Mutex
	subs      map[string]func(json.RawMessage)
	allowTool atomic.Bool
	closed    chan struct{}
	closeOnce sync.Once
	readErr   error
}

// NewACPClient creates a JSON-RPC 2.0 client over `r` and `w` and starts its reader loop.
func NewACPClient(r io.Reader, w io.Writer) *ACPClient {
	c := &ACPClient{
		w:       w,
		pending: make(map[int64]chan rpcEnvelope),
		subs:    make(map[string]func(json.RawMessage)),
		closed:  make(chan struct{}),
	}
	c.allowTool.Store(true)
	go c.readLoop(r)
	return c
}

// SetAllowTools controls how incoming `session/request_permission` calls from the server are answered.
func (c *ACPClient) SetAllowTools(allow bool) {
	c.allowTool.Store(allow)
}

func (c *ACPClient) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var env rpcEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		// Server-initiated request (has both ID and Method, e.g., session/request_permission).
		if len(env.ID) > 0 && string(env.ID) != "null" && env.Method != "" {
			c.handleServerRequest(env.ID, env.Method, env.Params)
			continue
		}
		// Response to a client request.
		if len(env.ID) > 0 && string(env.ID) != "null" {
			var numID int64
			if err := json.Unmarshal(env.ID, &numID); err == nil {
				c.pendingMu.Lock()
				ch, ok := c.pending[numID]
				if ok {
					delete(c.pending, numID)
				}
				c.pendingMu.Unlock()
				if ok {
					ch <- env
				}
			}
			continue
		}
		// Notification (e.g. session/update).
		if env.Method == "session/update" {
			var note struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if json.Unmarshal(env.Params, &note) == nil && note.SessionID != "" {
				c.subMu.Lock()
				fn := c.subs[note.SessionID]
				c.subMu.Unlock()
				if fn != nil {
					fn(note.Update)
				}
			}
		}
	}
	c.closeOnce.Do(func() {
		c.readErr = sc.Err()
		if c.readErr == nil {
			c.readErr = io.EOF
		}
		close(c.closed)
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
	})
}

func permissionToolDenied(params json.RawMessage) bool {
	var p struct {
		Tool     string `json:"tool"`
		ToolName string `json:"toolName"`
		Name     string `json:"name"`
		ToolCall struct {
			Name  string `json:"name"`
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	for _, candidate := range []string{p.Tool, p.ToolName, p.Name, p.ToolCall.Name, p.ToolCall.Title, p.ToolCall.Kind} {
		if IsDisabledSubagentTool(candidate) {
			return true
		}
	}
	return false
}

func (c *ACPClient) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method == "session/request_permission" {
		allow := c.allowTool.Load() && !permissionToolDenied(params)
		opt := "allow_always"
		if !allow {
			opt = "reject_once"
		}
		var req struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		if json.Unmarshal(params, &req) == nil && len(req.Options) > 0 {
			wantPrefix := "allow"
			if !allow {
				wantPrefix = "reject"
			}
			for _, o := range req.Options {
				if strings.HasPrefix(o.Kind, wantPrefix) && o.OptionID != "" {
					opt = o.OptionID
					break
				}
			}
		}
		_ = c.sendRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": opt,
				},
			},
		})
		return
	}
	_ = c.sendRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": "method not supported"},
	})
}

func (c *ACPClient) sendRaw(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// Call invokes a JSON-RPC 2.0 method and decodes the response into `out`.
func (c *ACPClient) Call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan rpcEnvelope, 1)
	c.pendingMu.Lock()
	select {
	case <-c.closed:
		c.pendingMu.Unlock()
		return fmt.Errorf("acp connection closed: %v", c.readErr)
	default:
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.sendRaw(req); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("acp connection closed: %v", c.readErr)
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("acp connection closed: %v", c.readErr)
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

// Initialize performs the ACP `initialize` handshake.
func (c *ACPClient) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "crew-run",
			"version": "1.0",
		},
	}
	var res map[string]any
	return c.Call(ctx, "initialize", params, &res)
}

// NewSession starts a new isolated ACP session (`session/new`).
func (c *ACPClient) NewSession(ctx context.Context, p NewSessionParams) (NewSessionResponse, error) {
	if p.MCPServers == nil {
		p.MCPServers = []ACPMCPServer{}
	}
	var res NewSessionResponse
	err := c.Call(ctx, "session/new", p, &res)
	return res, err
}

// LoadSession resumes an existing ACP session (`session/load`) from `~/.gemini/antigravity-cli/conversations/<id>.db`.
func (c *ACPClient) LoadSession(ctx context.Context, p LoadSessionParams) (LoadSessionResponse, error) {
	if p.MCPServers == nil {
		p.MCPServers = []ACPMCPServer{}
	}
	var res LoadSessionResponse
	err := c.Call(ctx, "session/load", p, &res)
	if err == nil && res.SessionID == "" {
		res.SessionID = p.SessionID
	}
	return res, err
}

// SetSessionModel switches the model of an ACP session (`session/set_model`).
func (c *ACPClient) SetSessionModel(ctx context.Context, sessionID, modelID string) error {
	if modelID == "" {
		return nil
	}
	return c.Call(ctx, "session/set_model", map[string]any{
		"sessionId": sessionID,
		"modelId":   modelID,
	}, nil)
}

// SetSessionMode switches the permission mode of an ACP session (`session/set_mode`).
func (c *ACPClient) SetSessionMode(ctx context.Context, sessionID, modeID string) error {
	if modeID == "" {
		modeID = ACPModeBypass
	}
	return c.Call(ctx, "session/set_mode", map[string]any{
		"sessionId": sessionID,
		"modeId":    modeID,
	}, nil)
}

// Cancel sends `session/cancel` to abort an active turn on `sessionID`.
func (c *ACPClient) Cancel(ctx context.Context, sessionID string) error {
	return c.Call(ctx, "session/cancel", map[string]any{"sessionId": sessionID}, nil)
}

// CloseSession sends `session/close` to release the session worker process.
func (c *ACPClient) CloseSession(ctx context.Context, sessionID string) error {
	return c.Call(ctx, "session/close", map[string]any{"sessionId": sessionID}, nil)
}

// Steer sends a live mid-turn `session/prompt` steer message to an active `sessionID` (design 11.6, 12.4).
func (c *ACPClient) Steer(ctx context.Context, sessionID, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("steer text is empty")
	}
	return c.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []ACPContentBlock{
			{Type: "text", Text: text},
		},
	}, nil)
}

// AuthSession registers or authenticates a per-run control key for sessionID with acp-daemon so a
// sibling session inside the project microVM cannot steer, cancel, or close this session.
func (c *ACPClient) AuthSession(ctx context.Context, sessionID, ctrlKey string) error {
	if sessionID == "" || ctrlKey == "" {
		return nil
	}
	return c.Call(ctx, "crew/authSession", map[string]any{
		"sessionId": sessionID,
		"ctrlKey":   ctrlKey,
	}, nil)
}
