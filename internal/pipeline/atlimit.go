package pipeline

import (
	"context"
	"encoding/json"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runs"
)

// NextChoice is the next choice of a subtask that stopped for its CLI's sign-in, its group's limit, or
// a model its CLI refused (12.7): its agent's choice as it would be made now, which passes over what
// cannot run, when that is another CLI or model than the stopped run's. It is nil when the subtask
// waits: with agents.at_limit wait, when nothing else can run, or when the choice is the run's own
// again, which the subtask continues on after the sign-in or the reset.
func (p *Pipeline) NextChoice(ctx context.Context, subtask int64) (*runs.ContinueRequest, error) {
	var role, kind, scope, branch, project, lastCLI, lastModel string
	var ticket int64
	err := p.Pool.QueryRow(ctx, `SELECT s.role, s.kind, s.scope, coalesce(s.branch, ''), coalesce(s.project::text, ''), coalesce(s.ticket, 0), r.cli, coalesce(r.model, '')
		FROM subtasks s JOIN LATERAL (SELECT * FROM runs WHERE subtask = s.id ORDER BY number DESC LIMIT 1) r ON true
		WHERE s.id = $1`, subtask).Scan(&role, &kind, &scope, &branch, &project, &ticket, &lastCLI, &lastModel)
	if err != nil {
		return nil, err
	}
	machine, err := p.Settings(ctx)
	if err != nil {
		return nil, err
	}
	// A project's own stage's agent runs on the reviewer's model (7.4), at a limit as at its start.
	mrole := role
	if kind == kindStage {
		mrole = "reviewer"
	}
	ask := models.Ask{Role: mrole, Machine: machine}
	if project != "" {
		proj, err := p.Board.Project(ctx, project)
		if err != nil {
			return nil, err
		}
		if ask.Project, err = p.Board.Settings(ctx, p.Pool, proj); err != nil {
			return nil, err
		}
	}
	var ticketKind string
	if ticket != 0 {
		var pins []byte
		if err := p.Pool.QueryRow(ctx, `SELECT agents, coalesce(kind, '') FROM tickets WHERE id = $1`, ticket).Scan(&pins, &ticketKind); err != nil {
			return nil, err
		}
		var all map[string]any
		if json.Unmarshal(pins, &all) == nil {
			ask.Pin, _ = all[mrole].(map[string]any)
			if ask.Pin == nil && mrole == "curator" {
				ask.Pin, _ = all["lead"].(map[string]any)
			} else if ask.Pin == nil && mrole == "lead" {
				ask.Pin, _ = all["curator"].(map[string]any)
			}
		}
	}
	ask.Bracket = models.TicketBracket(mrole, kind, ticketKind)
	// A check does not land on the builder's family (12.7), an eval's no more than a ticket's: an eval's
	// reviewer at its limit moved to its builder's model (M15 on dave). The model is the one Crew passed,
	// not what the run's own events said it ran (security review of M8).
	if role != "builder" {
		var model string
		// Only a role that checks the builder's work, under the rule for checks, needs the builder's family:
		// without it, it waits rather than choose with no family to avoid. A tester, or an analyst before a
		// build, moves on (review of 0.2.4); on a ticket by the kinds a check's start goes by, so a
		// reviewer's analysis of a lead's ticket moves too (since 1.14, the review of 1.14's change).
		guard := checksBuild(mrole, ask)
		switch {
		case ticket != 0:
			guard = checksTicketBuild(kind, mrole, ask)
			// The builder's last run is read as a new check's start reads it, a part or a join by the
			// builder included (checkfamily.go; since 1.14).
			if model, err = builderModel(ctx, p.Pool, ticket); err != nil {
				return nil, err
			}
			if model == "" && guard {
				return nil, nil
			}
		case scope == "eval":
			// An eval's check whose builder's model Crew cannot read, with no branch, no eval run of it,
			// or no model recorded, waits rather than choose with no family to avoid (security review
			// of 0.50).
			if model, err = runs.EvalBuilderModel(ctx, p.Pool, project, branch); err != nil {
				return nil, err
			}
			if model == "" && guard {
				return nil, nil
			}
		}
		if guard && model != "" {
			ask.Avoid = cli.Family(model)
		}
	}
	c, err := models.Choose(ctx, p.Pool, ask)
	if _, ok := models.IsWait(err); ok {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.CLI == lastCLI && c.Model == lastModel {
		return nil, nil
	}
	if scope == "eval" && c.Off && c.ModelFrom == models.FromLimit {
		return nil, nil
	}
	from := c.ModelFrom
	if from != models.FromCheckRule {
		from = models.FromLimit
	}
	return &runs.ContinueRequest{CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, ModelFrom: from, EffortFrom: c.EffortFrom}, nil
}
