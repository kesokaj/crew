package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/mcp"
)

func (t *Tools) registerFactoryTools(s *mcp.Server) error {
	for _, tl := range []mcp.Tool{
		{
			Name:  "list_factory_tasks",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"kind":  map[string]any{"type": "string", "description": "Optional kind filter: spec, research, dream, coordinator, chat_action."},
				"state": map[string]any{"type": "string", "description": "Optional state filter: queued, running, done, failed, cancelled."},
			}),
			Handle: t.listFactoryTasks,
		},
		{
			Name:  "create_factory_task",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"kind":       map[string]any{"type": "string", "description": "Off-board kind: spec, research, dream, coordinator, or chat_action."},
				"title":      map[string]any{"type": "string", "description": "One-line task summary."},
				"role":       map[string]any{"type": "string", "description": "Target peer role (e.g. @researcher, @coordinator, @planner)."},
				"ticket":     map[string]any{"type": "string", "description": "Optional linked ticket key such as FC-1."},
				"specRef":    map[string]any{"type": "string", "description": "Optional .crew/specs/ path."},
				"originRoom": map[string]any{"type": "string", "description": "Optional origin channel or thread."},
			}, "kind", "title"),
			Handle: t.createFactoryTask,
		},
		{
			Name:  "complete_factory_task",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"task":         map[string]any{"type": "string", "description": "Factory task UUID or key (e.g. FC-T1)."},
				"state":        map[string]any{"type": "string", "enum": []string{"done", "failed", "cancelled"}, "description": "Outcome state."},
				"summary":      map[string]any{"type": "string", "description": "Short factual outcome summary."},
				"artifactPath": map[string]any{"type": "string", "description": "Optional repo-relative output artifact path."},
			}, "task", "state"),
			Handle: t.completeFactoryTask,
		},
		{
			Name:   "rank_backlog",
			Scope:  mcp.ScopeProject,
			Input:  object(nil),
			Handle: t.rankBacklogTool,
		},
		{
			Name:  "write_research_brief",
			Scope: mcp.ScopeProject,
			Input: object(map[string]any{
				"ticket":            map[string]any{"type": "string", "description": "Ticket key (optional when caller is already bound to that ticket)."},
				"diagnosis":         map[string]any{"type": "string", "description": "Clean-context root-cause diagnosis of why checks failed."},
				"recommendedAction": map[string]any{"type": "string", "enum": []string{"retry_builder", "ask_planner", "ask_user"}, "description": "Next action recommended for this ticket."},
			}, "diagnosis", "recommendedAction"),
			Handle: t.writeResearchBrief,
		},
	} {
		if err := s.Add(tl); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) listFactoryTasks(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, mcp.Errf("%s", err)
		}
	}
	list, err := t.Board.ListFactoryTasks(ctx, c.Project, in.Kind, in.State)
	if err != nil {
		return nil, boardError(err)
	}
	return map[string]any{"tasks": list}, nil
}

func (t *Tools) createFactoryTask(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Kind       string `json:"kind"`
		Title      string `json:"title"`
		Role       string `json:"role"`
		Ticket     string `json:"ticket"`
		SpecRef    string `json:"specRef"`
		OriginRoom string `json:"originRoom"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, mcp.Errf("%s", err)
	}
	if err := t.noSecret(ctx, c, in.Kind, in.Title, in.Role, in.Ticket, in.SpecRef, in.OriginRoom); err != nil {
		return nil, err
	}
	var tid *int64
	if in.Ticket == "" && c.Ticket != 0 {
		tid = &c.Ticket
	}
	ft, err := t.Board.CreateFactoryTask(ctx, c.Project, board.NewFactoryTask{
		Kind:       in.Kind,
		Title:      in.Title,
		Role:       in.Role,
		TicketRef:  in.Ticket,
		TicketID:   tid,
		SpecRef:    in.SpecRef,
		OriginRoom: in.OriginRoom,
		CreatedBy:  who(c),
	})
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return ft, nil
}

func (t *Tools) completeFactoryTask(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Task         string `json:"task"`
		State        string `json:"state"`
		Summary      string `json:"summary"`
		ArtifactPath string `json:"artifactPath"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, mcp.Errf("%s", err)
	}
	if err := t.noSecret(ctx, c, in.Task, in.State, in.Summary, in.ArtifactPath); err != nil {
		return nil, err
	}
	ft, err := t.Board.CompleteFactoryTask(ctx, c.Project, in.Task, in.State, in.Summary, in.ArtifactPath, who(c))
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return ft, nil
}

func (t *Tools) rankBacklogTool(ctx context.Context, c mcp.Caller, _ json.RawMessage) (any, error) {
	res, err := t.Board.RankBacklog(ctx, c.Project)
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return res, nil
}

func (t *Tools) writeResearchBrief(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var in struct {
		Ticket            string `json:"ticket"`
		Diagnosis         string `json:"diagnosis"`
		RecommendedAction string `json:"recommendedAction"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, mcp.Errf("%s", err)
	}
	if err := t.noSecret(ctx, c, in.Ticket, in.Diagnosis, in.RecommendedAction); err != nil {
		return nil, err
	}
	p, tk, err := t.own(ctx, c)
	if err != nil && strings.TrimSpace(in.Ticket) != "" {
		p, err = t.Board.Project(ctx, c.Project)
		if err == nil {
			tk, err = t.Board.Ticket(ctx, p, in.Ticket)
		}
	}
	if err != nil {
		return nil, boardError(err)
	}
	var taskID, wipRef, headSHA string
	if err := t.Pool.QueryRow(ctx, `SELECT coalesce(s.factory_task_id::text, ''), coalesce(r.wip_ref, ''), coalesce(r.git_commit, '')
		FROM subtasks s LEFT JOIN runs r ON r.subtask = s.id WHERE s.id = $1 ORDER BY (r.id::text = $2) DESC, r.number DESC LIMIT 1`, c.Subtask, c.Run).Scan(&taskID, &wipRef, &headSHA); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if taskID == "" {
		taskID = t.Board.LatestResearchTaskID(ctx, tk.ID)
	}
	if wipRef == "" {
		if err := t.Pool.QueryRow(ctx, `SELECT coalesce(wip_ref, '') FROM runs WHERE subtask IN (SELECT id FROM subtasks WHERE ticket = $1 AND kind IN ('build','fix')) AND wip_ref IS NOT NULL ORDER BY started_at DESC LIMIT 1`, tk.ID).Scan(&wipRef); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	brief, err := t.Board.SaveResearchBrief(ctx, p, board.SaveResearchBriefInput{
		ProjectID:         p.ID,
		Ticket:            tk,
		TaskID:            taskID,
		RunID:             c.Run,
		SubtaskID:         c.Subtask,
		WIPRef:            wipRef,
		HeadSHA:           headSHA,
		Diagnosis:         in.Diagnosis,
		RecommendedAction: in.RecommendedAction,
	})
	if err != nil {
		return nil, boardError(err)
	}
	t.kick()
	return brief, nil
}
