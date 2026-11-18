package pipeline

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kesokaj/crew/internal/knowledge"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/memory"
)

func (t *Tools) registerM26Tools(s *mcp.Server) error {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strs := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	for _, tl := range []mcp.Tool{
		{
			Name:  "dreamstore_remember",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"kind":           map[string]any{"type": "string", "enum": memory.DreamKinds, "description": "Memory tier: episodic, semantic, or procedural."},
				"title":          str("One-line title of what was observed."),
				"body":           str("Reusable operational finding, free of secrets."),
				"paths":          strs("File paths this memory applies to."),
				"keywords":       strs("Search keywords."),
				"verifiedCommit": str("Optional commit SHA where this behavior was verified."),
			}, "kind", "title", "body"),
			Handle: t.dreamstoreRememberTool,
		},
		{
			Name:  "dreamstore_search",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"query": str("Search words (empty lists highest-validity active entries)."),
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum entries to return."},
			}),
			Handle: t.dreamstoreSearchTool,
		},
		{
			Name:  "search_cross_knowledge",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"query": str("Search query across sibling factories."),
				"kind":  map[string]any{"type": "string", "enum": knowledge.Kinds, "description": "Optional entry class filter."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum entries to return."},
			}),
			Handle: t.searchCrossKnowledgeTool,
		},
		{
			Name:  "act_on_knowledge",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"key":          str("Knowledge entry key such as KNOW-001."),
				"createTicket": map[string]any{"type": "boolean", "description": "Also emit a Backlog ticket referencing the fenced advisory."},
				"ticketKind":   map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
				"checkCmd":     str("Deterministic shell command check for the Backlog ticket when createTicket is true."),
			}, "key"),
			Handle: t.actOnKnowledgeTool,
		},
	} {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) dreamstoreRememberTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Kind           string   `json:"kind"`
		Title          string   `json:"title"`
		Body           string   `json:"body"`
		Paths          []string `json:"paths"`
		Keywords       []string `json:"keywords"`
		VerifiedCommit string   `json:"verifiedCommit"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	texts := make([]string, 0, 4+len(a.Paths)+len(a.Keywords))
	texts = append(texts, a.Kind, a.Title, a.Body, a.VerifiedCommit)
	texts = append(texts, a.Paths...)
	texts = append(texts, a.Keywords...)
	if err := t.noSecret(ctx, c, texts...); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	var runs []string
	if strings.TrimSpace(c.Run) != "" {
		runs = []string{c.Run}
	}
	e, err := memory.Add(ctx, t.Pool, memory.AddInput{
		ProjectID:      p.ID,
		Kind:           a.Kind,
		Title:          a.Title,
		Body:           a.Body,
		AppliesTo:      []string{"all"},
		Paths:          a.Paths,
		Keywords:       a.Keywords,
		VerifiedCommit: a.VerifiedCommit,
		EvidenceRuns:   runs,
		Author:         who(c),
	})
	if err != nil {
		return nil, mcp.Errf("%v", err)
	}
	return e, nil
}

func (t *Tools) dreamstoreSearchTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	entries, err := memory.Search(ctx, t.Pool, p.ID, a.Query, a.Limit, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries}, nil
}

func (t *Tools) searchCrossKnowledgeTool(ctx context.Context, _ mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	entries, err := knowledge.Search(ctx, t.Pool, a.Query, a.Kind, a.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries}, nil
}

func (t *Tools) actOnKnowledgeTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Key          string `json:"key"`
		CreateTicket bool   `json:"createTicket"`
		TicketKind   string `json:"ticketKind"`
		CheckCmd     string `json:"checkCmd"`
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	if err := t.noSecret(ctx, c, a.Key, a.TicketKind, a.CheckCmd); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	entry, ft, tk, err := knowledge.ActOnEntry(ctx, t.Board, p, knowledge.ActInput{
		EntryKey:     strings.TrimSpace(a.Key),
		CreateTicket: a.CreateTicket,
		TicketKind:   a.TicketKind,
		CheckCmd:     a.CheckCmd,
		Actor:        who(c),
	})
	if err != nil {
		return nil, mcp.Errf("%v", err)
	}
	return map[string]any{"entry": entry, "task": ft, "ticket": tk}, nil
}
