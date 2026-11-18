// Package mcp is the Crew MCP server (design 7.5, 11.5): MCP over HTTP at /mcp on each sandbox's
// door, answered as plain JSON, as the spike's stand-in was for both CLIs (spike items 10 and 71).
// Who calls is decided by the run's token alone: its role, and the one project or plan draft it
// names. What a tool's arguments say about who is calling is never read. Every call is recorded.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
)

// Caller is who calls a tool, as the run's token says.
type Caller struct {
	Run     string
	Role    string
	Project string // the project's id, or ""
	Draft   string // the plan draft's id, or ""
	// Subtask is the run's subtask, and Ticket its ticket's id, or 0 for a run on no ticket: a write
	// to a ticket's record is to this one only (11.5).
	Subtask int64
	Ticket  int64
	// Token is the operator's personal token a call from their own tools carries (11.7), with its
	// name, the projects it names, and whether it may answer; 0 for a run.
	Token     int64
	TokenName string
	Projects  []string
	MayAnswer bool
	// Crew says the call is the improver's (10.4): its run's subtask has the scope improver, set by the
	// backend only, and it names no project and no draft. It reaches the Crew level's tools only.
	Crew bool
	// Conversation is the conversation of the operator's or the ticket's chat whose turn calls, and
	// ChatTicket the chat's ticket, or 0 for a conversation of the operator's (11.8, 11.9): the role is
	// the assistant's.
	Conversation string
	ChatTicket   int64
}

// Scope says what a tool works on: a plan draft, or a project.
type Scope int

const (
	ScopeProject Scope = iota
	ScopeDraft
	// ScopeOperator is a tool of the operator's own tools, called with a personal token (11.7).
	ScopeOperator
	// ScopeCrew is a tool of the Crew level, for the improver's token only (10.4).
	ScopeCrew
	// ScopeConversation is a tool of a normal conversation outside a project, called with the
	// assistant's run token when Caller.Project == "" (11.8).
	ScopeConversation
)

// Handler does a tool's work. Its result is given to the agent as JSON. An *Error is a refusal the
// agent reads; any other error is reported as a failure of Crew, without its text.
type Handler func(ctx context.Context, c Caller, args json.RawMessage) (any, error)

// Tool is one tool of the server. Its words and its roles are the baseline's (tools.yaml).
type Tool struct {
	Name   string
	Scope  Scope
	Input  map[string]any // the JSON schema of its arguments
	Handle Handler
	// Answers marks a tool of the operator's tools that answers in their name: only a token with
	// "may answer" has it (11.7).
	Answers bool
	// AlsoCrew marks a tool of a project that the improver calls too, at the Crew level, with the same
	// name: get_metrics, search_summaries, and get_summary (10.4). Its handler tells the two by
	// Caller.Crew.
	AlsoCrew bool
	// AlsoConversation marks a read-only tool of a project that a normal conversation outside a project
	// calls too, naming the project in its arguments (11.8). ConversationInput, when set, is the JSON
	// schema shown to a normal conversation in tools/list so project runs keep their own schema.
	AlsoConversation  bool
	ConversationInput map[string]any
}

// Error is a refusal: bad arguments, or a call the token does not allow. Its text goes to the agent.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Errf makes an Error.
func Errf(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// ErrNoToken is returned by a resolver for a request whose token is not a live run's.
var ErrNoToken = errors.New("mcp: no live run has this token")

// Server is the Crew MCP server.
type Server struct {
	Pool *pgxpool.Pool
	// Resolve says who a request comes from: the run whose token it carries, at that run's own door.
	Resolve func(r *http.Request) (Caller, error)
	Log     *slog.Logger

	tools map[string]*Tool
	words map[string]baseline.Tool
}

// New makes a server with the baseline's words for its tools.
func New(pool *pgxpool.Pool, resolve func(r *http.Request) (Caller, error), log *slog.Logger) (*Server, error) {
	words, err := baseline.Tools()
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{Pool: pool, Resolve: resolve, Log: log, tools: map[string]*Tool{}, words: words}, nil
}

// Add adds a tool. A tool the baseline has no words for is refused.
func (s *Server) Add(t Tool) error {
	if _, ok := s.words[t.Name]; !ok {
		return fmt.Errorf("mcp: %s has no words in the baseline's tools.yaml", t.Name)
	}
	if t.Handle == nil || t.Input == nil {
		return fmt.Errorf("mcp: %s needs its input and its handler", t.Name)
	}
	if _, ok := s.tools[t.Name]; ok {
		return fmt.Errorf("mcp: %s is added twice", t.Name)
	}
	tt := t
	s.tools[t.Name] = &tt
	return nil
}

// Missing lists the tools the baseline has words for that the server does not offer.
func (s *Server) Missing() []string {
	var out []string
	for name := range s.words {
		if _, ok := s.tools[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Tool is a tool the server offers, or nil: the operator's server wraps the read tools of the runs'
// one (11.7).
func (s *Server) Tool(name string) *Tool { return s.tools[name] }

// Names lists the tools the server offers.
func (s *Server) Names() []string {
	out := make([]string, 0, len(s.tools))
	for name := range s.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Allowed says whether c may call the tool: its role is one the baseline names for it, and the tool
// works on what the token names, a plan draft or a project.
func (s *Server) Allowed(c Caller, name string) bool {
	t, ok := s.tools[name]
	if !ok {
		return false
	}
	// The improver reaches the Crew level's tools and nothing of a project's; no project's run reaches
	// the Crew level's (10.4, 10.6).
	if c.Crew {
		return c.Token == 0 && c.Project == "" && c.Draft == "" && c.Role == "improver" &&
			(t.Scope == ScopeCrew || t.AlsoCrew) && slices.Contains(s.words[name].Roles, "improver")
	}
	normalConv := c.Token == 0 && !c.Crew && c.Role == "assistant" && c.Conversation != "" &&
		c.Project == "" && c.Draft == "" && c.ChatTicket == 0
	switch t.Scope {
	case ScopeCrew:
		return false
	case ScopeConversation:
		roles := s.words[name].Roles
		return normalConv && (slices.Contains(roles, "assistant") || slices.Contains(roles, "conversation"))
	case ScopeDraft:
		if c.Draft == "" || c.Project != "" {
			return false
		}
	case ScopeProject:
		if c.Token != 0 {
			return false
		}
		if c.Project == "" && !(normalConv && t.AlsoConversation) {
			return false
		}
	case ScopeOperator:
		// A personal token reaches only these, and no run's token does (11.7).
		return c.Token != 0 && slices.Contains(s.words[name].Roles, "token") && (!t.Answers || c.MayAnswer)
	}
	if c.Token != 0 {
		return false
	}
	roles := s.words[name].Roles
	// The assistant of a conversation or a ticket's chat reaches only the tools that name it: the
	// word assistant, or conversation in a normal conversation outside a project, or chat for a
	// chat, never all, which holds tools that write (11.8, 11.9, D5).
	if c.Role == "assistant" {
		if c.ChatTicket != 0 {
			return c.Conversation != "" && slices.Contains(roles, "chat")
		}
		if normalConv {
			return slices.Contains(roles, "assistant") || slices.Contains(roles, "conversation")
		}
		return c.Conversation != "" && slices.Contains(roles, "assistant")
	}
	return slices.Contains(roles, c.Role) || (slices.Contains(roles, "all") && c.Project != "")
}

// The protocol versions Crew answers; the newest is given to a client that asks for another.
var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxBody is the largest request: a plan of 1 MB, as JSON.
const maxBody = 3 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP answers MCP's streamable HTTP with plain JSON: a POST holds one message; a notification
// gets 202; GET, which opens a stream of the server's own messages, is not offered.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, err := s.Resolve(r)
	if errors.Is(err, ErrTooMany) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method == http.MethodDelete {
		// The end of a session: Crew keeps none, so there is nothing to end.
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "the request is too large"})
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "a JSON-RPC request was expected"}})
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.answer(r.Context(), c, req)
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) answer(ctx context.Context, c Caller, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := versions[0]
		if slices.Contains(versions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "crew", "version": "1"},
			"instructions":    "Crew's own tools: what this run may read and write in its project or plan draft.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := []map[string]any{}
		for _, name := range s.Names() {
			if s.Allowed(c, name) {
				t := s.tools[name]
				schema := t.Input
				if c.Project == "" && t.ConversationInput != nil {
					schema = t.ConversationInput
				}
				list = append(list, map[string]any{
					"name": name, "description": strings.TrimSpace(s.words[name].Description), "inputSchema": schema,
				})
			}
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{-32602, "tools/call needs a name"}
		}
		return s.call(ctx, c, p.Name, p.Arguments), nil
	}
	return nil, &rpcError{-32601, "not found"}
}

// What one run may do through the Crew MCP: a run that a text it read turned against Crew cannot
// fill the database, whose history the server may only add to (security review of M5).
const (
	MaxCallsPerRun = 400
	maxArgsStored  = 8 << 10
	maxToolName    = 100
)

// overLimit says the run has made as many calls as a run may.
func (s *Server) overLimit(ctx context.Context, c Caller) (bool, error) {
	if c.Run == "" {
		return false, nil
	}
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_calls WHERE run = $1`, c.Run).Scan(&n)
	return n >= MaxCallsPerRun, err
}

// call calls one tool and records the call (11.5). A run past its limit of calls is refused, and the
// refusal is not recorded, so a run that keeps calling adds nothing.
func (s *Server) call(ctx context.Context, c Caller, name string, args json.RawMessage) map[string]any {
	start := time.Now()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	over, err := s.overLimit(ctx, c)
	switch {
	case err != nil:
		s.Log.Error("counting a run's calls to the Crew MCP", "run", c.Run, "error", err)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "Crew could not do this now; the server's log says why. Try again later, or carry on without it."}}, "isError": true}
	case over:
		s.Log.Warn("a run reached its limit of calls to the Crew MCP", "run", c.Run, "limit", MaxCallsPerRun)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf(
			"this run has made the %d calls to Crew's tools a run may make; end the turn and say so", MaxCallsPerRun)}}, "isError": true}
	}
	if len(name) > maxToolName {
		name = cut(name, maxToolName)
	}
	var text string
	ok := false
	switch t := s.tools[name]; {
	case t == nil:
		text = fmt.Sprintf("there is no tool %q", name)
	case !s.Allowed(c, name):
		text = fmt.Sprintf("%s is not one of the tools of this run", name)
	default:
		res, err := t.Handle(ctx, c, args)
		var re *Error
		switch {
		case errors.As(err, &re):
			text = re.Msg
		case err != nil:
			s.Log.Error("a tool of the Crew MCP failed", "tool", name, "run", c.Run, "error", err)
			text = "Crew could not do this now; the server's log says why. Try again later, or carry on without it."
		default:
			b, merr := json.Marshal(res)
			if merr != nil {
				text = "Crew could not write the answer"
			} else {
				text, ok = string(b), true
			}
		}
	}
	s.record(context.WithoutCancel(ctx), c, name, args, ok, text, time.Since(start))
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": !ok}
}

// PruneOwnCalls removes the calls of the operator's own tools, with a personal token and no run, older
// than 180 days (6.5), and says how many. A run's calls are in its trajectory, kept for good. The database's function decides the age:
// the role crew only adds to mcp_calls (schema step 70).
func PruneOwnCalls(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT crew_prune_own_calls()`).Scan(&n)
	return n, err
}

// record writes the call's row. The arguments keep their shape with every long text shortened, so a
// plan or a breakdown is in its own table and not twice.
func (s *Server) record(ctx context.Context, c Caller, name string, args json.RawMessage, ok bool, result string, took time.Duration) {
	var v any
	if json.Unmarshal(args, &v) != nil {
		v = map[string]any{"unreadable": len(args)}
	}
	short, _ := json.Marshal(shorten(v, 0))
	// An array of many short strings is shortened by none of them: the whole is cut too.
	if len(short) > maxArgsStored {
		short, _ = json.Marshal(map[string]any{"cut": cut(string(short), 2000), "bytes": len(args)})
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO mcp_calls (run, project, draft, role, tool, args, ok, result, ms, token)
		VALUES (nullif($1, '')::uuid, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6, $7, $8, $9, nullif($10, 0))`,
		c.Run, c.Project, c.Draft, c.Role, name, short, ok, cut(result, 2000), took.Milliseconds(), c.Token)
	if err != nil {
		s.Log.Error("recording a call of the Crew MCP", "tool", name, "run", c.Run, "error", err)
	}
}

// shorten cuts every string longer than 300 characters to its start and its length, and stops at a
// depth of 8.
func shorten(v any, depth int) any {
	if depth > 8 {
		return "…"
	}
	switch t := v.(type) {
	case string:
		if utf8.RuneCountInString(t) > 300 {
			return fmt.Sprintf("%s… (%d characters)", string([]rune(t)[:200]), utf8.RuneCountInString(t))
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = shorten(x, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = shorten(x, depth+1)
		}
		return out
	}
	return v
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// Decode reads a tool's arguments into v, refusing fields v does not have, so a typo is told and not
// ignored.
func Decode(args json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(args)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Errf("the arguments do not read: %v", err)
	}
	return nil
}
