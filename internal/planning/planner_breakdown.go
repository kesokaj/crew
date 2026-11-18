package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
)

// breakdownPrompt is the planner's prompt for the breakdown of a project, with the project's overrides of
// the curator's file, the guide, and the template (10.3).
func (pl *Planner) breakdownPrompt(ctx context.Context, project, name, key, amendment string) []runs.PromptPart {
	part := func(part, path string, body bool) runs.PromptPart {
		text, source, err := board.BaselineText(ctx, pl.Pool, project, path, pl.Version)
		if err != nil {
			pl.log().Error("reading a project's override", "project", project, "path", path, "error", err)
			text, source = baseline.MustText(path), pl.source(path)+" (its override could not be read)"
		}
		if body {
			text = baseline.Body(text)
		}
		return runs.PromptPart{Part: part, Source: source, Text: text}
	}
	parts := []runs.PromptPart{part("agent", "agents/curator.md", true), part("guide", "guides/breakdown.md", false), part("template", "templates/task.md", false)}
	if amendment != "" {
		return append(parts, runs.PromptPart{Part: "context", Source: "crew", Text: fmt.Sprintf("The project is %s, and its tickets are numbered %s-1, %s-2, and on. "+
			"It has its OK and runs; the operator amended its plan with the text below. Your workspace is its repository, on main. "+
			"Break the amendment down now, against the plan and the board as they are: write its breakdown, and propose only the new "+
			"tickets it needs. They wait in Planning for the operator's OK.\n\nThe amendment, the operator's own text:\n\n%s", name, key, key, amendment)})
	}
	return append(parts, runs.PromptPart{Part: "context", Source: "crew", Text: fmt.Sprintf("The project is %s, and its tickets are numbered %s-1, %s-2, and on. "+
		"Your workspace is its repository, on main. Break its plan down now: write the breakdown, and propose the tickets.", name, key, key)})
}

const nudge = "You ended your turn without writing the breakdown with write_breakdown and proposing its tickets with " +
	"propose_tickets. The operator reads only what those tools write. Do both now."

// breakdownJobs are the projects in planning whose lead has work: the breakdown, once; a comment the
// operator sent it back with, once each; and one reminder for a turn that ended with nothing written.
func (pl *Planner) breakdownJobs(ctx context.Context) ([]job, error) {
	// A project in planning, and one whose amendment to the plan waits for its breakdown and OK (7.13):
	// its breakdown is a new subtask after the amendment.
	rows, err := pl.Pool.Query(ctx, `SELECT p.id::text, p.name, p.key, coalesce(a.text, ''),
			(SELECT id FROM subtasks s WHERE s.project = p.id AND s.kind = 'breakdown' AND s.created_at > coalesce(a.created_at, '-infinity')
				ORDER BY id DESC LIMIT 1)
		FROM projects p LEFT JOIN LATERAL (`+board.OpenAmendmentSQL("p.id")+`) a ON true
		WHERE p.state = 'planning' OR (p.state IN ('running', 'done') AND a.id IS NOT NULL) ORDER BY p.created_at`)
	if err != nil {
		return nil, err
	}
	type proj struct {
		id, name, key, amendment string
		subtask                  *int64
	}
	var list []proj
	for rows.Next() {
		var p proj
		if err := rows.Scan(&p.id, &p.name, &p.key, &p.amendment, &p.subtask); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	var out []job
	for _, p := range list {
		p := p
		if p.subtask == nil {
			out = append(out, job{what: "the breakdown of " + p.name, project: p.id, role: "planner", key: "breakdown:" + p.id,
				start: func(ctx context.Context, c choice) (string, error) {
					run, _, err := pl.Runs.StartRole(ctx, runs.RoleRun{Role: "planner", Kind: "breakdown", Title: "Break the plan down",
						Project: p.id, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, ReadOnly: true,
						Parts: pl.breakdownPrompt(ctx, p.id, p.name, p.key, p.amendment), Timeout: breakdownTimeout})
					return run, err
				}})
			continue
		}
		sub := *p.subtask
		_, state, err := latestRun(ctx, pl.Pool, sub)
		if err != nil || state == "starting" || state == "working" {
			continue
		}
		// A comment the breakdown was sent back with, which no run was given yet.
		var number int
		var comment string
		err = pl.Pool.QueryRow(ctx, `SELECT number, text FROM plans WHERE project = $1 AND kind = 'sent_back'
				AND NOT EXISTS (SELECT 1 FROM jobs WHERE key = 'revise:' || $1 || ':' || number)
			ORDER BY number LIMIT 1`, p.id).Scan(&number, &comment)
		if err == nil {
			key := fmt.Sprintf("revise:%s:%d", p.id, number)
			text := "The operator sent the breakdown back with this comment:\n\n" + strings.TrimSpace(comment) +
				"\n\nRevise the breakdown, changing only what the comment asks for, with propose_tickets: revise a ticket to change it " +
				"in place, which keeps its number and what depends on it; withdraw a ticket that should go, with the reason; and propose " +
				"a new one only for work that is new. Then write the breakdown again with write_breakdown."
			out = append(out, pl.continueJob(key, "a revision of the breakdown of "+p.name, p.id, sub, text))
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		// A turn that ended with nothing written gets one reminder, also when its run ended well since,
		// by its idle time. A run the operator stopped, or the driver gave up on, or retries itself,
		// is left as it is (review of M5, round 2).
		if state == "idle" || (state == "ended" && pl.subtaskDone(ctx, sub)) {
			written, err := pl.written(ctx, p.id)
			if err != nil {
				return nil, err
			}
			if !written {
				out = append(out, pl.continueJob(fmt.Sprintf("nudge:%d", sub), "a reminder to the planner of "+p.name, p.id, sub, nudge))
			}
		}
	}
	return out, nil
}

// continueJob gives a subtask of the planner its next turn, once for key: the row of jobs that says it
// was given is written in the transaction that records the turn.
func (pl *Planner) continueJob(key, what, project string, sub int64, text string) job {
	record := func(ctx context.Context, tx pgx.Tx, run string) error {
		tag, err := tx.Exec(ctx, `INSERT INTO jobs (kind, target, key, class, state)
			SELECT $1, $2, $3, 'operator_waiting', 'done' WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE key = $3)`,
			strings.SplitN(key, ":", 2)[0], run, key)
		if err == nil && tag.RowsAffected() == 0 {
			err = runs.ErrBusy // given already
		}
		return err
	}
	unrecord := func(ctx context.Context, tx pgx.Tx, run string) error {
		_, err := tx.Exec(ctx, `DELETE FROM jobs WHERE key = $1 AND target = $2`, key, run)
		return err
	}
	return job{what: what, project: project, role: "planner", key: key,
		start: func(ctx context.Context, c choice) (string, error) {
			var done bool
			if err := pl.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE key = $1)`, key).Scan(&done); err != nil || done {
				return "", runs.ErrBusy
			}
			return pl.Runs.Continue(ctx, sub, runs.ContinueRequest{Text: text, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, ModelFrom: c.ModelFrom, EffortFrom: c.EffortFrom,
				Record: record, Unrecord: unrecord})
		}}
}

// subtaskDone says the subtask's last run ended well: it neither waits nor was stopped.
func (pl *Planner) subtaskDone(ctx context.Context, sub int64) bool {
	var done bool
	if err := pl.Pool.QueryRow(ctx, `SELECT state = 'done' FROM subtasks WHERE id = $1`, sub).Scan(&done); err != nil {
		pl.log().Warn("reading whether the planning subtask is done", "subtask", sub, "error", err)
		return false
	}
	return done
}

// endTakenDrafts ends the planning agent's runs of a draft that became a project: the draft takes no
// more messages, so its microVM, its login, and its token have nothing left to do (security review
// of M5). A turn that runs is stopped, with its work kept.
func (pl *Planner) endTakenDrafts(ctx context.Context) error {
	rows, err := pl.Pool.Query(ctx, `SELECT r.id::text, r.state FROM runs r JOIN subtasks s ON s.id = r.subtask
		JOIN plan_drafts d ON d.id = s.draft WHERE d.state <> 'open' AND r.state <> 'ended'`)
	if err != nil {
		return err
	}
	type live struct{ run, state string }
	var list []live
	for rows.Next() {
		var x live
		if err := rows.Scan(&x.run, &x.state); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		var err error
		if x.state == "idle" {
			err = pl.Runs.Finish(ctx, x.run)
		} else {
			err = pl.Runs.Stop(ctx, x.run)
		}
		if err != nil && !errors.Is(err, runs.ErrEnded) {
			pl.log().Warn("ending a run of a draft that became a project", "run", x.run, "error", err)
		}
	}
	return nil
}

// written says whether the lead wrote a breakdown and proposed at least one ticket from the plan.
func (pl *Planner) written(ctx context.Context, project string) (bool, error) {
	var ok bool
	err := pl.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans WHERE project = $1 AND kind = 'breakdown')
		AND EXISTS (SELECT 1 FROM tickets WHERE project = $1 AND (stage = 'planning' OR origin = 'plan'))`, project).Scan(&ok)
	return ok, err
}

// finishBreakdowns ends the lead's run of a breakdown whose turn ended with the breakdown written and
// no comment waiting, so its microVM is not kept for nothing; a comment later continues it in a new
// one (12.10).
func (pl *Planner) finishBreakdowns(ctx context.Context) error {
	rows, err := pl.Pool.Query(ctx, `SELECT r.id::text, s.project::text FROM runs r JOIN subtasks s ON s.id = r.subtask
		JOIN projects p ON p.id = s.project
		WHERE r.state = 'idle' AND s.kind = 'breakdown'
			AND NOT EXISTS (SELECT 1 FROM plans b WHERE b.project = s.project AND b.kind = 'sent_back'
				AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.key = 'revise:' || s.project::text || ':' || b.number))`)
	if err != nil {
		return err
	}
	type idle struct{ run, project string }
	var list []idle
	for rows.Next() {
		var x idle
		if err := rows.Scan(&x.run, &x.project); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		written, err := pl.written(ctx, x.project)
		if err != nil {
			return err
		}
		if !written {
			continue
		}
		if err := pl.Runs.Finish(ctx, x.run); err != nil {
			pl.log().Warn("finishing the lead's run of a breakdown", "run", x.run, "error", err)
		}
	}
	return nil
}
