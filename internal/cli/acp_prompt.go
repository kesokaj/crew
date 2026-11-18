package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prompt executes one turn (`session/prompt`) on `sessionID`, streaming normalized `agy` stream-json
// lines (`init`, `step_update`, `result`) and `cli.Event` values to `onEvent`, and returns the final
// `PromptResponse` and synthetic `result` line.
func (c *ACPClient) Prompt(ctx context.Context, sessionID, model, cwd, text string, onEvent func(line []byte, ev Event)) (PromptResponse, []byte, error) {
	started := time.Now()
	var stepIdx atomic.Int64
	var fullResponse strings.Builder
	var respMu sync.Mutex
	var lastUsage map[string]any
	seenTools := map[string]bool{}

	emitACPInit(sessionID, model, cwd, onEvent)

	c.subMu.Lock()
	c.subs[sessionID] = func(raw json.RawMessage) {
		handleACPSessionUpdate(sessionID, raw, &stepIdx, &respMu, &fullResponse, &lastUsage, seenTools, onEvent)
	}
	c.subMu.Unlock()
	defer func() {
		c.subMu.Lock()
		delete(c.subs, sessionID)
		c.subMu.Unlock()
	}()

	var pr PromptResponse
	err := c.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []ACPContentBlock{
			{Type: "text", Text: text},
		},
	}, &pr)

	respMu.Lock()
	ans := fullResponse.String()
	usage := pr.Usage
	if len(usage) == 0 {
		usage = lastUsage
	}
	respMu.Unlock()

	resLine := emitACPResult(sessionID, ans, pr, err, usage, stepIdx.Load(), time.Since(started), onEvent)
	return pr, resLine, err
}

func emitACPInit(sessionID, model, cwd string, onEvent func([]byte, Event)) {
	initLine, _ := json.Marshal(map[string]any{
		"event":           "init",
		"conversation_id": sessionID,
		"init": map[string]any{
			"model": model,
			"cwd":   cwd,
		},
	})
	if onEvent != nil {
		onEvent(initLine, (Agy{}).Parse(initLine))
	}
}

func handleACPSessionUpdate(
	sessionID string,
	raw json.RawMessage,
	stepIdx *atomic.Int64,
	respMu *sync.Mutex,
	fullResponse *strings.Builder,
	lastUsage *map[string]any,
	seenTools map[string]bool,
	onEvent func([]byte, Event),
) {
	var u map[string]any
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	switch str(u, "sessionUpdate") {
	case "agent_message_chunk":
		idx := stepIdx.Add(1)
		delta := str(u, "content", "text")
		if delta == "" {
			delta = str(u, "text")
		}
		respMu.Lock()
		fullResponse.WriteString(delta)
		respMu.Unlock()
		line, _ := json.Marshal(map[string]any{
			"event":           "step_update",
			"conversation_id": sessionID,
			"step_update": map[string]any{
				"conversation_id": sessionID,
				"step_index":      idx,
				"state":           "RUNNING",
				"step_type":       "agent_response",
				"text_delta":      delta,
			},
		})
		if onEvent != nil {
			onEvent(line, (Agy{}).Parse(line))
		}
	case "tool_call", "tool_call_update":
		state := acpToolState(u)
		callID := firstNonEmpty(str(u, "toolCallId"), str(u, "id"))
		if state == "RUNNING" && callID != "" {
			respMu.Lock()
			seen := seenTools[callID]
			seenTools[callID] = true
			respMu.Unlock()
			if seen {
				return
			}
		}
		idx := stepIdx.Add(1)
		line := buildACPToolStepLine(sessionID, idx, state, u)
		if onEvent != nil {
			onEvent(line, (Agy{}).Parse(line))
		}
	case "usage_update":
		usage := acpUsageMap(u)
		if len(usage) > 0 {
			respMu.Lock()
			*lastUsage = usage
			respMu.Unlock()
		}
	}
}

func acpToolState(u map[string]any) string {
	switch strings.ToUpper(str(u, "status")) {
	case "COMPLETED", "DONE":
		return "DONE"
	case "FAILED", "ERROR":
		return "ERROR"
	default:
		return "RUNNING"
	}
}

func acpUsageMap(u map[string]any) map[string]any {
	if usage := obj(u, "usage"); len(usage) > 0 {
		return usage
	}
	for _, k := range []string{"inputTokens", "outputTokens", "input_tokens", "output_tokens"} {
		if _, ok := u[k]; ok {
			return u
		}
	}
	return nil
}

func buildACPToolStepLine(sessionID string, idx int64, state string, u map[string]any) []byte {
	toolName := firstNonEmpty(str(u, "title"), str(u, "toolName"), "tool")
	params := obj(u, "rawInput")
	if params == nil {
		params = map[string]any{}
	}
	toolInfo := map[string]any{"parameters": params}
	if out, ok := u["rawOutput"]; ok {
		toolInfo["output"] = out
	}
	step := map[string]any{
		"conversation_id": sessionID,
		"step_index":      idx,
		"state":           state,
		"step_type":       "tool",
		"tool_name":       toolName,
		"tool_info":       toolInfo,
	}
	if state == "ERROR" {
		step["error"] = firstNonEmpty(str(u, "error"), "tool failed")
	}
	line, _ := json.Marshal(map[string]any{
		"event":           "step_update",
		"conversation_id": sessionID,
		"step_update":     step,
	})
	return line
}

func emitACPResult(
	sessionID, ans string,
	pr PromptResponse,
	callErr error,
	usage map[string]any,
	numTurns int64,
	elapsed time.Duration,
	onEvent func([]byte, Event),
) []byte {
	status := "SUCCESS"
	errStr := pr.Error
	if callErr != nil {
		status = "ERROR"
		if errStr == "" {
			errStr = callErr.Error()
		}
	} else if pr.StopReason == "error" || pr.StopReason == "refused" || pr.Error != "" {
		status = "ERROR"
	}
	resObj := map[string]any{
		"conversation_id":  sessionID,
		"status":           status,
		"response":         ans,
		"error":            errStr,
		"num_turns":        numTurns,
		"duration_seconds": elapsed.Seconds(),
	}
	if len(usage) > 0 {
		resObj["usage"] = usage
	}
	resLine, _ := json.Marshal(map[string]any{
		"event":           "result",
		"conversation_id": sessionID,
		"result":          resObj,
	})
	if onEvent != nil {
		onEvent(resLine, (Agy{}).Parse(resLine))
	}
	return resLine
}
