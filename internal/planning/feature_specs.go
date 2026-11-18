package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// finishFeatureSpecs closes idle @planner feature-spec runs once their open questions are resolved
// and records token usage onto the linked off-board spec factory_task.
func (pl *Planner) finishFeatureSpecs(ctx context.Context) error {
	rows, err := pl.Pool.Query(ctx, `SELECT r.id::text, s.factory_task_id::text, r.tokens_in, r.tokens_out
		FROM runs r
		JOIN subtasks s ON s.id = r.subtask
		WHERE r.state = 'idle'
		  AND s.kind = 'feature_spec'
		  AND NOT EXISTS (
			SELECT 1 FROM spec_questions sq
			WHERE sq.planner_run = r.id AND sq.state IN ('open', 'processing')
		  )`)
	if err != nil {
		return err
	}
	type idleRun struct {
		runID     string
		taskID    *string
		tokensIn  int64
		tokensOut int64
	}
	var list []idleRun
	for rows.Next() {
		var x idleRun
		if err := rows.Scan(&x.runID, &x.taskID, &x.tokensIn, &x.tokensOut); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range list {
		if x.taskID != nil && *x.taskID != "" {
			if _, err := pl.Pool.Exec(ctx, `UPDATE factory_tasks
				SET tokens_in = greatest(tokens_in, $2), tokens_out = greatest(tokens_out, $3), updated_at = now()
				WHERE id = $1::uuid`, *x.taskID, x.tokensIn, x.tokensOut); err != nil {
				pl.log().Warn("syncing feature_spec token usage", "task", *x.taskID, "error", err)
			}
		}
		if err := pl.Runs.Finish(ctx, x.runID); err != nil && !errors.Is(err, runs.ErrEnded) {
			pl.log().Warn("finishing idle feature_spec run", "run", x.runID, "error", err)
		}
	}
	return nil
}

// featureSpecJobs finds Tier-2 feature specs with open peer questions and queues @planner on Gemini (Rule 15).
func (pl *Planner) featureSpecJobs(ctx context.Context) ([]job, error) {
	rows, err := pl.Pool.Query(ctx, `SELECT s.id, s.project_id::text, pr.name, pr.key, s.spec_key, s.path,
			s.title, s.body_markdown, s.approval_mode, s.thread_ref, s.factory_task_id::text,
			coalesce((SELECT wd.max_agent_hops FROM workflow_defs wd WHERE wd.project_id = s.project_id), 3),
			coalesce((SELECT wd.feature_specs FROM workflow_defs wd WHERE wd.project_id = s.project_id), 'autonomous')
		FROM specs s
		JOIN projects pr ON pr.id = s.project_id
		WHERE s.tier = 'feature'
		  AND s.state IN ('draft', 'deliberating')
		  AND pr.state <> 'archived'
		  AND EXISTS (SELECT 1 FROM spec_questions sq WHERE sq.spec_id = s.id AND sq.state = 'open')
		  AND NOT EXISTS (
			SELECT 1 FROM spec_questions sq
			JOIN runs r ON r.id = sq.planner_run
			WHERE sq.spec_id = s.id AND r.state IN ('starting', 'working')
		  )
		ORDER BY s.id ASC`)
	if err != nil {
		return nil, err
	}
	type pendingSpec struct {
		id, maxHops          int64
		projectID, projName  string
		projKey, specKey     string
		path, title, body    string
		approvalMode, thread string
		taskID               *string
		wfFeatureMode        string
	}
	var specs []pendingSpec
	for rows.Next() {
		var s pendingSpec
		if err := rows.Scan(&s.id, &s.projectID, &s.projName, &s.projKey, &s.specKey, &s.path,
			&s.title, &s.body, &s.approvalMode, &s.thread, &s.taskID, &s.maxHops, &s.wfFeatureMode); err != nil {
			rows.Close()
			return nil, err
		}
		specs = append(specs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []job
	for _, s := range specs {
		s := s
		questions, maxQID, err := pl.openSpecQuestions(ctx, s.id)
		if err != nil || len(questions) == 0 {
			continue
		}
		foundationBody, err := pl.foundationSpecBody(ctx, s.projectID)
		if err != nil {
			pl.log().Warn("reading 000-foundation.md for feature_spec", "project", s.projectID, "error", err)
		}
		doc, _ := pl.projectSettings(ctx, s.projectID)
		effMode := "autonomous"
		if s.wfFeatureMode == "manual" || settingStringOrDefault(doc, "approvals.feature_specs") == "manual" {
			effMode = "manual"
		}
		qIDs := make([]int64, 0, len(questions))
		for _, q := range questions {
			qIDs = append(qIDs, q.ID)
		}
		parts := pl.featureSpecPrompt(ctx, s.projectID, s.projName, s.specKey, s.path, s.title, s.body, s.thread, effMode, int(s.maxHops), foundationBody, questions)
		record := func(ctx context.Context, tx pgx.Tx, runID string) error {
			tag, err := tx.Exec(ctx, `UPDATE spec_questions SET state = 'processing', planner_run = $2::uuid
				WHERE id = ANY($1) AND state = 'open'`, qIDs, runID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return runs.ErrBusy
			}
			if s.taskID != nil && *s.taskID != "" {
				var subID int64
				var convID *string
				if err := tx.QueryRow(ctx, `SELECT subtask, conversation::text FROM runs WHERE id = $1::uuid`, runID).Scan(&subID, &convID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE subtasks SET factory_task_id = $2::uuid WHERE id = $1`, subID, *s.taskID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE factory_tasks
					SET state = 'running', subtask_id = $2, session_id = $3::uuid, updated_at = now()
					WHERE id = $1::uuid`, *s.taskID, subID, convID); err != nil {
					return err
				}
			}
			return nil
		}
		jobKey := fmt.Sprintf("feat-spec:%s:%s:%d", s.projectID, s.specKey, maxQID)
		out = append(out, job{
			what:    fmt.Sprintf("feature spec %s of %s", s.specKey, s.projName),
			project: s.projectID,
			role:    "planner",
			key:     jobKey,
			start: func(ctx context.Context, c choice) (string, error) {
				run, _, err := pl.Runs.StartRole(ctx, runs.RoleRun{
					Role: "planner", Kind: "feature_spec",
					Title:   fmt.Sprintf("Feature spec %s", s.specKey),
					Project: s.projectID, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort,
					From: c.ModelFrom, EffortFrom: c.EffortFrom, ReadOnly: true,
					Parts: parts, Timeout: breakdownTimeout, Record: record,
				})
				return run, err
			},
		})
	}
	return out, nil
}

func (pl *Planner) openSpecQuestions(ctx context.Context, specID int64) ([]board.SpecQuestion, int64, error) {
	rows, err := pl.Pool.Query(ctx, `SELECT id, project_id::text, spec_id, ticket_id, asking_role, asking_run::text,
			thread_ref, question, context_note, hop_count, planner_run::text, answer, state, created_at, answered_at
		FROM spec_questions WHERE spec_id = $1 AND state = 'open' ORDER BY id ASC`, specID)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (board.SpecQuestion, error) {
		var q board.SpecQuestion
		err := r.Scan(&q.ID, &q.ProjectID, &q.SpecID, &q.TicketID, &q.AskingRole, &q.AskingRun,
			&q.ThreadRef, &q.Question, &q.ContextNote, &q.HopCount, &q.PlannerRun, &q.Answer, &q.State, &q.CreatedAt, &q.AnsweredAt)
		return q, err
	})
	if err != nil {
		return nil, 0, err
	}
	var maxID int64
	for _, q := range list {
		if q.ID > maxID {
			maxID = q.ID
		}
	}
	return list, maxID, nil
}

func (pl *Planner) foundationSpecBody(ctx context.Context, projectID string) (string, error) {
	var body string
	err := pl.Pool.QueryRow(ctx, `SELECT body_markdown FROM specs WHERE project_id = $1 AND number = 0`, projectID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return body, err
}

func (pl *Planner) featureSpecPrompt(ctx context.Context, projectID, projName, specKey, specPath, title, body, thread, effMode string, maxHops int, foundationBody string, questions []board.SpecQuestion) []runs.PromptPart {
	part := func(name, path string, stripFront bool) runs.PromptPart {
		text, source, err := board.BaselineText(ctx, pl.Pool, projectID, path, pl.Version)
		if err != nil {
			text, source = baseline.MustText(path), pl.source(path)
		}
		if stripFront {
			text = baseline.Body(text)
		}
		return runs.PromptPart{Part: name, Source: source, Text: text}
	}
	parts := []runs.PromptPart{
		part("agent", "agents/curator.md", true),
		part("guide", "guides/planning.md", false),
		part("stage", "stages/feature_spec.md", false),
	}
	if strings.TrimSpace(foundationBody) != "" {
		f := fence.New(foundationBody)
		parts = append(parts, runs.PromptPart{
			Part:   "foundation",
			Source: ".crew/specs/000-foundation.md",
			Text:   f.Data("Foundation spec (000-foundation.md)", "operator-approved", foundationBody),
		})
	}
	parts = append(parts, runs.PromptPart{
		Part:   "spec",
		Source: specPath,
		Text: fmt.Sprintf("Project: %s\nSpecKey: %s\nTitle: %s\nPath: %s\nThread: %s\napprovals.feature_specs: %s\nmax_agent_hops: %d\n\n%s",
			projName, specKey, title, specPath, thread, effMode, maxHops, body),
	})
	for _, q := range questions {
		payload := q.Question
		if strings.TrimSpace(q.ContextNote) != "" {
			payload += "\n\nContext:\n" + q.ContextNote
		}
		f := fence.New(payload)
		who := "@" + q.AskingRole
		if q.AskingRun != nil && *q.AskingRun != "" {
			who += " (run " + *q.AskingRun + ")"
		}
		parts = append(parts, runs.PromptPart{
			Part:   "question",
			Source: fmt.Sprintf("spec_questions/%d", q.ID),
			Text: fmt.Sprintf("Peer question #%d (hop %d/%d) from %s in thread %s:\n\n%s",
				q.ID, q.HopCount, maxHops, who, q.ThreadRef, f.Data("Peer design question", who, payload)),
		})
	}
	return parts
}

func settingStringOrDefault(doc settings.Doc, path string) string {
	v := dig(doc, strings.Split(path, ".")...)
	s, _ := v.(string)
	return s
}
