package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/rooms"
)

func (t *Tools) roomService() *rooms.Service {
	if t.Rooms != nil {
		return t.Rooms
	}
	return &rooms.Service{
		Pool:         t.Pool,
		Board:        t.Board,
		KickPlanner:  t.KickPlanner,
		KickPipeline: t.Kick,
	}
}

func (t *Tools) registerRoomTools(s *mcp.Server) error {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	for _, tl := range []mcp.Tool{
		{
			Name:  "read_chat",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"threadKey": str("Optional feature thread key such as feat/001-cache; empty reads #<project>."),
				"messageId": map[string]any{"type": "integer", "minimum": 1, "description": "Fetch one full un-offloaded chat message by id."},
				"afterId":   map[string]any{"type": "integer", "minimum": 0, "description": "Return messages strictly after this message id."},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Maximum number of messages to return."},
			}),
			Handle: t.readChatTool,
		},
		{
			Name:  "post_chat",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"threadKey":    str("Optional feature thread key such as feat/001-cache; empty posts in #<project>."),
				"spec":         str("Optional Tier-2 feature spec key, number, or path linked to the thread."),
				"ticket":       str("Optional related ticket key, e.g. SF-12."),
				"kind":         str("Optional envelope kind: text (default), event, or choice_prompt."),
				"intent":       str("Optional delivery intent: inform, request, or question."),
				"eventType":    str("Optional dotted event identifier when kind is event."),
				"replyToId":    map[string]any{"type": "integer", "minimum": 1, "description": "Optional parent message id in the same room or thread."},
				"to":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional explicit recipient handles (e.g. [\"@builder\"])."},
				"metadata":     map[string]any{"type": "object", "description": "Optional structured metadata (reserved Crew keys stripped on ingress)."},
				"choicePrompt": map[string]any{"type": "object", "description": "Structured keyed choice card when kind is choice_prompt ({id, question, choices, default, update, specRef})."},
				"body":         str("Message text to share. Actionable @peer pings mint a tracked Tasks API item; name passive bystanders without @."),
				"actionable":   map[string]any{"type": "boolean", "description": "Override whether @peer mentions trigger a tracked peer task (defaults to true for request/question intent)."},
			}, "body"),
			Handle: t.postChatTool,
		},
		{
			Name:  "respond_choice_prompt",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"promptKey":       str("Stable slug key of the choice_prompt card."),
				"choice":          str("Chosen option id or exact label from the card's choices."),
				"promptVersion":   map[string]any{"type": "integer", "minimum": 1, "description": "Optional version guard (rejects superseded versions)."},
				"sourceMessageId": map[string]any{"type": "integer", "minimum": 1, "description": "Required @user prose reply message id when an agent interprets an answer."},
				"rationale":       str("Optional rationale recorded on spec_decisions."),
			}, "promptKey", "choice"),
			Handle: t.respondChoicePromptTool,
		},
	} {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) readChatTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		ThreadKey string `json:"threadKey"`
		MessageID int64  `json:"messageId"`
		AfterID   int64  `json:"afterId"`
		Limit     int    `json:"limit"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	if in.MessageID > 0 {
		msg, err := t.roomService().ReadMessageByID(ctx, p, in.MessageID, true)
		if err != nil {
			return nil, roomMCPError(err)
		}
		return map[string]any{
			"channel": "#" + p.Name,
			"message": msg,
		}, nil
	}
	msgs, err := t.roomService().ReadMessages(ctx, p, in.ThreadKey, in.AfterID, in.Limit, true)
	if err != nil {
		return nil, roomMCPError(err)
	}
	if msgs == nil {
		msgs = []rooms.Message{}
	}
	return map[string]any{
		"channel":   "#" + p.Name,
		"threadKey": strings.TrimSpace(in.ThreadKey),
		"messages":  msgs,
	}, nil
}

func (t *Tools) postChatTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		ThreadKey    string                  `json:"threadKey"`
		Spec         string                  `json:"spec"`
		Ticket       string                  `json:"ticket"`
		Kind         string                  `json:"kind"`
		Intent       string                  `json:"intent"`
		EventType    string                  `json:"eventType"`
		ReplyToID    int64                   `json:"replyToId"`
		To           []string                `json:"to"`
		Metadata     map[string]any          `json:"metadata"`
		ChoicePrompt *rooms.ChoicePromptSpec `json:"choicePrompt"`
		Body         string                  `json:"body"`
		Actionable   *bool                   `json:"actionable"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	var secrets []string
	if t.Secrets != nil {
		secrets = t.Secrets(ctx, c.Run)
	}
	sender := strings.TrimSpace(c.Role)
	if sender == "" {
		sender = "user"
	}
	res, err := t.roomService().PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:    in.ThreadKey,
		SpecRef:      in.Spec,
		TicketID:     c.Ticket,
		TicketKey:    in.Ticket,
		SenderRole:   sender,
		RunID:        c.Run,
		Kind:         in.Kind,
		Intent:       in.Intent,
		EventType:    in.EventType,
		ReplyToID:    in.ReplyToID,
		To:           in.To,
		Metadata:     in.Metadata,
		ChoicePrompt: in.ChoicePrompt,
		Body:         in.Body,
		Secrets:      secrets,
		Actionable:   in.Actionable,
	})
	if err != nil {
		return nil, roomMCPError(err)
	}
	t.kick()
	if res.SpawnedTask != nil && res.SpawnedTask.Role == "@planner" {
		t.kickPlanner()
	}
	return res, nil
}

func (t *Tools) respondChoicePromptTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		PromptKey       string `json:"promptKey"`
		Choice          string `json:"choice"`
		PromptVersion   int    `json:"promptVersion"`
		SourceMessageID int64  `json:"sourceMessageId"`
		Rationale       string `json:"rationale"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	if err := t.noSecret(ctx, c, in.PromptKey, in.Choice, in.Rationale); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	sender := strings.TrimSpace(c.Role)
	if sender == "" {
		sender = "user"
	}
	res, dec, err := t.roomService().RespondChoicePrompt(ctx, p, rooms.RespondChoiceInput{
		PromptKey:       in.PromptKey,
		PromptVersion:   in.PromptVersion,
		Choice:          in.Choice,
		SenderRole:      sender,
		RunID:           c.Run,
		SourceMessageID: in.SourceMessageID,
		Rationale:       in.Rationale,
	})
	if err != nil {
		return nil, roomMCPError(err)
	}
	t.kick()
	return map[string]any{
		"decision": dec,
		"post":     res,
	}, nil
}

func roomMCPError(err error) error {
	if errors.Is(err, rooms.ErrMaxAgentHops) ||
		errors.Is(err, rooms.ErrInvalid) ||
		errors.Is(err, rooms.ErrCrossProjectBoundary) ||
		errors.Is(err, rooms.ErrStaleChoiceVersion) {
		return mcp.Errf("%v", err)
	}
	return boardError(err)
}
