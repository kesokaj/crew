package pipeline

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/mcp"
)

func (t *Tools) registerSpecTools(s *mcp.Server) error {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	check := object(map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"cmd", "judge"}},
		"spec": str("For cmd, the command; for judge, a single-line acceptance criterion."),
	}, "kind", "spec")
	specTicket := object(map[string]any{
		"title":         str("Short ticket title."),
		"kind":          map[string]any{"type": "string", "enum": []string{"feature", "fix", "chore", "docs"}},
		"why":           str("Why this ticket is needed."),
		"what":          str("What is in scope and what is not."),
		"goal":          str("Observable done criteria."),
		"checks":        map[string]any{"type": "array", "items": check, "description": "At least one verification check."},
		"docs":          strList("Repo-relative docs paths updated by this ticket."),
		"dependsOn":     strList("Ticket keys or @N references within this decide_spec call."),
		"priority":      map[string]any{"type": "integer", "minimum": 0, "maximum": 3},
		"maxTries":      map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
		"assignedAgent": str("Preferred peer role handle, e.g. @builder."),
	}, "title", "why", "what", "goal", "checks")

	for _, tl := range []mcp.Tool{
		{
			Name:   "list_specs",
			Scope:  mcp.ScopeProject,
			Input:  object(nil),
			Handle: t.listSpecsTool,
		},
		{
			Name:  "get_spec",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"spec": str("Spec key (SPEC-000, SPEC-001), number, slug, or .crew/specs/ path."),
			}, "spec"),
			Handle: t.getSpecTool,
		},
		{
			Name:  "write_spec",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"ref":              str("Optional existing Tier-2 feature spec key, number, or path to update."),
				"title":            str("Feature spec title (required when creating a new spec)."),
				"slug":             str("Optional URL-friendly slug for .crew/specs/NNN-<slug>.md."),
				"bodyMarkdown":     str("Markdown body for .crew/specs/NNN-<slug>.md."),
				"touchedPaths":     strList("Repository paths this feature spec will touch (checked against 13.8)."),
				"altersFoundation": map[string]any{"type": "boolean", "description": "True if this changes 000-foundation.md scope or non-goals."},
				"threadRef":        str("Optional feature thread id, e.g. feat/001-cache."),
			}),
			Handle: t.writeSpecTool,
		},
		{
			Name:  "ask_planner",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"spec":             str("Optional Tier-2 feature spec key/path (creates a new feature spec if omitted)."),
				"title":            str("Short title when initiating a new Tier-2 feature spec."),
				"slug":             str("Optional slug when creating a new Tier-2 feature spec."),
				"ticket":           str("Optional originating ticket key, e.g. FC-1."),
				"question":         str("Concrete feature design question for @planner."),
				"context":          str("Relevant observed context from files, failures, or checks."),
				"touchedPaths":     strList("Candidate files affected if known."),
				"altersFoundation": map[string]any{"type": "boolean", "description": "True if answering may change 000-foundation.md scope."},
			}, "question"),
			Handle: t.askPlannerTool,
		},
		{
			Name:  "decide_spec",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"spec":             str("Tier-2 feature spec key, number, or .crew/specs/ path."),
				"questionId":       map[string]any{"type": "integer", "minimum": 1},
				"question":         str("Optional question summary if resolving directly without an open question."),
				"bodyMarkdown":     str("Updated markdown body for .crew/specs/NNN-<slug>.md."),
				"choice":           str("Single-line chosen architectural/design decision."),
				"alternatives":     strList("Alternatives evaluated."),
				"rationale":        str("Reasoning behind the decision."),
				"touchedPaths":     strList("Paths affected by carrying out the decision (checked against 13.8)."),
				"altersFoundation": map[string]any{"type": "boolean", "description": "True if this decision alters 000-foundation.md scope or non-goals."},
				"tickets":          map[string]any{"type": "array", "items": specTicket, "description": "Executable code tickets (feature, fix, chore, docs) to emit into Backlog."},
			}, "spec", "choice", "rationale"),
			Handle: t.decideSpecTool,
		},
	} {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) listSpecsTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	if err := mcp.Decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	list, err := t.Board.ListSpecs(ctx, p)
	if err != nil {
		return nil, boardError(err)
	}
	return map[string]any{"specs": list}, nil
}

func (t *Tools) getSpecTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Spec string `json:"spec"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	sp, err := t.Board.GetSpec(ctx, p, in.Spec)
	if err != nil {
		return nil, boardError(err)
	}
	return sp, nil
}

func (t *Tools) writeSpecTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Ref              string   `json:"ref"`
		Title            string   `json:"title"`
		Slug             string   `json:"slug"`
		BodyMarkdown     string   `json:"bodyMarkdown"`
		TouchedPaths     []string `json:"touchedPaths"`
		AltersFoundation bool     `json:"altersFoundation"`
		ThreadRef        string   `json:"threadRef"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	texts := make([]string, 0, 5+len(in.TouchedPaths))
	texts = append(texts, in.Ref, in.Title, in.Slug, in.BodyMarkdown, in.ThreadRef)
	texts = append(texts, in.TouchedPaths...)
	if err := t.noSecret(ctx, c, texts...); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	sp, err := t.Board.WriteFeatureSpec(ctx, p, board.EnsureSpecInput{
		Ref:              in.Ref,
		Title:            in.Title,
		Slug:             in.Slug,
		BodyMarkdown:     in.BodyMarkdown,
		TouchedPaths:     in.TouchedPaths,
		AltersFoundation: in.AltersFoundation,
		ThreadRef:        in.ThreadRef,
		CreatedBy:        who(c),
	})
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return sp, nil
}

func (t *Tools) askPlannerTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Spec             string   `json:"spec"`
		Title            string   `json:"title"`
		Slug             string   `json:"slug"`
		Ticket           string   `json:"ticket"`
		Question         string   `json:"question"`
		Context          string   `json:"context"`
		TouchedPaths     []string `json:"touchedPaths"`
		AltersFoundation bool     `json:"altersFoundation"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	texts := make([]string, 0, 6+len(in.TouchedPaths))
	texts = append(texts, in.Spec, in.Title, in.Slug, in.Ticket, in.Question, in.Context)
	texts = append(texts, in.TouchedPaths...)
	if err := t.noSecret(ctx, c, texts...); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	sp, q, err := t.Board.AskPlanner(ctx, p, board.AskPlannerInput{
		SpecRef:          in.Spec,
		Title:            in.Title,
		Slug:             in.Slug,
		TicketID:         c.Ticket,
		TicketKey:        in.Ticket,
		AskingRole:       c.Role,
		AskingRun:        c.Run,
		Question:         in.Question,
		ContextNote:      in.Context,
		TouchedPaths:     in.TouchedPaths,
		AltersFoundation: in.AltersFoundation,
	})
	if err != nil {
		return nil, boardError(err)
	}
	t.kickPlanner()
	t.kick()
	return map[string]any{"spec": sp, "question": q}, nil
}

func (t *Tools) decideSpecTool(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Spec             string            `json:"spec"`
		QuestionID       int64             `json:"questionId"`
		Question         string            `json:"question"`
		BodyMarkdown     string            `json:"bodyMarkdown"`
		Choice           string            `json:"choice"`
		Alternatives     []string          `json:"alternatives"`
		Rationale        string            `json:"rationale"`
		TouchedPaths     []string          `json:"touchedPaths"`
		AltersFoundation bool              `json:"altersFoundation"`
		Tickets          []board.NewTicket `json:"tickets"`
	}
	if err := mcp.Decode(args, &in); err != nil {
		return nil, err
	}
	texts := make([]string, 0, 5+len(in.Alternatives)+len(in.TouchedPaths)+len(in.Tickets)*6)
	texts = append(texts, in.Spec, in.Question, in.BodyMarkdown, in.Choice, in.Rationale)
	texts = append(texts, in.Alternatives...)
	texts = append(texts, in.TouchedPaths...)
	for _, tk := range in.Tickets {
		texts = append(texts, tk.Title, tk.Why, tk.What, tk.Goal, tk.SpecRef)
		texts = append(texts, tk.Docs...)
		for _, chk := range tk.Checks {
			texts = append(texts, chk.Spec)
		}
	}
	if err := t.noSecret(ctx, c, texts...); err != nil {
		return nil, err
	}
	p, err := t.Board.Project(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	res, err := t.Board.DecideSpec(ctx, p, board.DecideSpecInput{
		SpecRef:          in.Spec,
		QuestionID:       in.QuestionID,
		DecidedByRole:    c.Role,
		DecidedByRun:     c.Run,
		BodyMarkdown:     in.BodyMarkdown,
		Question:         in.Question,
		Choice:           strings.TrimSpace(in.Choice),
		Alternatives:     in.Alternatives,
		Rationale:        in.Rationale,
		TouchedPaths:     in.TouchedPaths,
		AltersFoundation: in.AltersFoundation,
		Tickets:          in.Tickets,
	})
	if err != nil {
		return nil, boardError(err)
	}
	t.kickPlanner()
	t.kick()
	return res, nil
}
