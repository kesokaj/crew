package cli

import (
	"encoding/json"
	"time"
)

// Turn is one headless call of a CLI in a run (12.4): the prompt, and how the call is made.
type Turn struct {
	Prompt    string
	Model     string // the model's name as the CLI takes it
	Effort    string // "" when the name holds it or the CLI's default is wanted
	Workspace string // the absolute path of the clone the CLI works in
	// Continue is the CLI's own id of the conversation this turn continues, or "".
	Continue string
	Tools    bool          // false: a call with no tools, for a judge or a summary
	Timeout  time.Duration // the CLI's own limit, where it has one
	// MaxTurns is the CLI's own limit of steps, where it has one. 0 is none.
	MaxTurns int
	// Home is the run's home, and MCP says PrepareRun wrote Crew's MCP server there for this run.
	Home string
	MCP  bool
}

// Command is how a turn is started: the command line, what goes on standard input, which is then
// closed, and variables added to the environment.
type Command struct {
	Args  []string
	Stdin []byte
	Env   []string
}

// The kinds of run event (12.5). Every line a CLI prints becomes exactly one event of one kind; a
// line the adapter does not know is a status event, so nothing is dropped.
const (
	EventMessage    = "message"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventStatus     = "status"
	EventUsage      = "usage"
	EventError      = "error"
)

// Event is one line of a CLI's output, normalized (12.5). Payload holds what the monitor and the
// UI read; the line itself is kept beside it as the original.
type Event struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
	// ConversationID is the CLI's id of the conversation, when the line names it.
	ConversationID string `json:"-"`
	// Result is set on a line that is a result of the turn: the last one before the process ended
	// is the turn's (12.4).
	Result bool `json:"-"`
	// Tool and ToolInput identify a tool call, and ToolFailed a failed result, for the watchdog's
	// "same action failing in a row" (8.2). ToolID ties a result to its call where the CLI says.
	Tool       string `json:"-"`
	ToolInput  string `json:"-"`
	ToolID     string `json:"-"`
	ToolFailed bool   `json:"-"`
	// Speaks is the text the agent said in a message, for the turns of its conversation (12.10).
	Speaks string `json:"-"`
}

// The reasons a turn ends (12.4). The ones only the backend can set, such as silent or operator,
// are the backend's; the adapter tells these apart from what the CLI printed.
const (
	ReasonOK      = "ok"
	ReasonRefused = "refused"
	ReasonError   = "error"
	ReasonLogin   = "login"
	ReasonQuota   = "quota"
	ReasonTimeout = "timeout"
	// ReasonModel says the CLI refused the name of the model it was given (12.3, 21).
	ReasonModel = "model"
	// ReasonSetup ends a run whose project setup failed before the CLI started (12.4, since 0.47).
	ReasonSetup = "setup"
)

// TurnOutput is what a turn left: its last result line, standard error, and the exit code.
type TurnOutput struct {
	Result []byte // the last result line, or nil when the CLI printed none
	Stderr []byte
	Exit   int
}

// Outcome is why a turn ended, as the adapter reads it.
type Outcome struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	// Response is the agent's final answer of the turn, when there is one.
	Response string `json:"response,omitempty"`
}

// Usage is what one result says the conversation has used.
type Usage struct {
	TokensIn  int64 `json:"tokensIn"`
	TokensOut int64 `json:"tokensOut"`
	// TokensCached are the tokens read from the cache, which agy counts apart from the tokens in and
	// which have a price of their own (12.7).
	TokensCached int64   `json:"tokensCached"`
	CostUSD      float64 `json:"costUSD"`
	HasCost      bool    `json:"hasCost"`
	HasTokens    bool    `json:"hasTokens"`
	// TokensTotal and CostTotal say the numbers are the total of the whole conversation, not of
	// this call, so Crew takes the difference to the last result (12.10).
	TokensTotal bool `json:"tokensTotal"`
	CostTotal   bool `json:"costTotal"`
}

// payload decodes a line into a map for an event's payload, or nil when it is not a JSON object.
func payload(line []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	return m
}

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

func num(m map[string]any, keys ...string) float64 {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = mm[k]
	}
	f, _ := cur.(float64)
	return f
}

func obj(m map[string]any, keys ...string) map[string]any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	o, _ := cur.(map[string]any)
	return o
}

// short is s cut to n bytes, for what the monitor shows of a long text; the original line keeps it
// whole.
func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// compact is v as one line of JSON, for a tool's input as the watchdog compares it.
func compact(v any) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
