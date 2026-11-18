package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// This file holds the guard of safety rule 15 that every run passes: at its start (StartRole) and at
// every continue of it (continueNew, and continueSubtask's in its own microVM), Crew's at a limit and
// the operator's. A reviewer, a verifier, a judge, and a
// project's own stage's agent, on a ticket's build, an eval's branch, or an eval case's fit check, do
// not run on the family of the builder whose work they check while the rule for checks holds, unless
// the rule cannot be met: then they run, and the news of a ticket's check says so (12.3, 12.7). A check
// whose model, or whose builder's model, is of a family Crew does not know does not start either. It is
// on release.Protected with roles.go and continue.go, so what an unprotected caller passes or leaves
// out cannot put a check on the builder's family (since 1.15: the security review of 1.14's change,
// finding 1, and the operator's popup answers of session 82).

// ErrCheckFamily says a run that checks would run on its builder's family, that its model or its
// builder's model is of a family Crew does not know, or that the builder's model is not recorded: it
// does not start, and waits as at a limit. Its text is Crew's own, for the card.
var ErrCheckFamily = errors.New("a check does not run on the builder's family")

// builderModelQuery is the model of the ticket's builder's last run: the newest run of a build, a fix,
// or a part or a join by the builder. A run that has none reads "".
const builderModelQuery = `SELECT coalesce(r.model, '') FROM runs r JOIN subtasks s ON s.id = r.subtask
	WHERE s.ticket = $1 AND (s.kind IN ('build', 'fix') OR (s.kind IN ('part', 'join') AND s.role = 'builder'))
	ORDER BY r.started_at DESC, s.id DESC, r.number DESC LIMIT 1`

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// BuilderModel is the model of the ticket's builder's last run, "" with no model recorded; found says
// the ticket has such a run.
func BuilderModel(ctx context.Context, q rowQuerier, ticket int64) (model string, found bool, err error) {
	err = q.QueryRow(ctx, builderModelQuery, ticket).Scan(&model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return model, err == nil, err
}

// EvalBuilderModel is the model of the builder's run of the project's eval run on branch, "" with no
// such run or no model recorded. An eval run's branch is named for its own id (eval/<id>, evals.go), so
// one row is read.
func EvalBuilderModel(ctx context.Context, q rowQuerier, project, branch string) (string, error) {
	var model string
	err := q.QueryRow(ctx, `SELECT coalesce(r.model, '') FROM eval_runs e JOIN runs r ON r.id = e.builder_run
		WHERE e.project::text = $1 AND e.branch = $2`, project, branch).Scan(&model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return model, err
}

// checkRun is what the guard reads of a run: its role and kind, where it runs, and the model it would
// run on with the name its CLI is given for it.
type checkRun struct {
	role, kind, project, branch, model, name string
	ticket                                   int64
	eval, judge                              bool
}

// checkKinds are the kinds of the runs that check a builder's work, with the role whose list their model
// comes from: a run of one is a check whatever role its caller names (the security review of 1.15's
// change, finding 5).
var checkKinds = map[string]string{"review": "reviewer", "verify": "verifier", "stage": "reviewer", "judge": "judge", "eval_review": "reviewer",
	"eval_fit": "judge"}

// beforeBuild are the stages of a ticket before anything is built on it: a check there with no
// builder's run, as a reviewer's analysis of a ticket a lead created (7.6), has nothing to keep off.
var beforeBuild = []string{settings.StagePlanning, settings.StageBacklog, settings.StageAnalysis}

// checkModelRole is the role whose list a run's model comes from, and whether the run checks: a role of
// models.CheckRoles, a project's own stage's agent, which runs on the reviewer's model (7.4), or a run of
// one of checkKinds.
func checkModelRole(role, kind string) (string, bool) {
	if role == "stage" {
		return "reviewer", true
	}
	if slices.Contains(models.CheckRoles, role) {
		return role, true
	}
	r, ok := checkKinds[kind]
	return r, ok
}

// checkFamily says whether c may run on its model: an error wrapping ErrCheckFamily when it may not,
// and any other error when a read failed, which starts nothing. off says it runs on the builder's
// family, avoid, since the rule for checks cannot be met.
func (d *Driver) checkFamily(ctx context.Context, c checkRun) (off bool, avoid string, err error) {
	mrole, checks := checkModelRole(c.role, c.kind)
	if !checks {
		return false, "", nil
	}
	ask := models.Ask{Role: mrole}
	if ask.Machine, err = settings.Effective(ctx, d.Pool, settings.Machine{}); err != nil {
		return false, "", err
	}
	if c.project != "" {
		var name, key string
		if err := d.Pool.QueryRow(ctx, `SELECT name, key FROM projects WHERE id::text = $1`, c.project).Scan(&name, &key); err != nil {
			return false, "", err
		}
		if ask.Project, err = settings.EffectiveProject(ctx, d.Pool, c.project, name, key); err != nil {
			return false, "", err
		}
	}
	if !models.CheckRule(ask) {
		return false, "", nil
	}
	if c.ticket == 0 && !c.eval {
		// A check that names no ticket and no eval has no builder's work it could name: it does not run
		// (the security review of 1.15's change, finding 2).
		return false, "", fmt.Errorf("the %s waits: it names no ticket and no eval whose builder it checks: %w", c.role, ErrCheckFamily)
	}
	if !c.fits() {
		return false, "", fmt.Errorf("the %s waits: a run of kind %s names what another kind does: %w", c.role, c.kind, ErrCheckFamily)
	}
	builder, found := "", true
	switch {
	case c.ticket != 0:
		// The ticket is its project's, and a check with no builder's run runs only before anything of
		// the builder's is on it (the second round of the reviews of 1.15's change, findings 1 and 4).
		var project, stage, ticketKind string
		var builders bool
		if err := d.Pool.QueryRow(ctx, `SELECT t.project::text, t.stage, coalesce(t.kind, ''), EXISTS (SELECT 1 FROM subtasks s WHERE s.ticket = t.id AND s.role = 'builder')
			FROM tickets t WHERE t.id = $1`, c.ticket).Scan(&project, &stage, &ticketKind, &builders); err != nil {
			return false, "", err
		}
		if project != c.project {
			return false, "", fmt.Errorf("the %s waits: its ticket is not of its project: %w", c.role, ErrCheckFamily)
		}
		ask.Bracket = models.TicketBracket(mrole, c.kind, ticketKind)
		if builder, found, err = BuilderModel(ctx, d.Pool, c.ticket); err == nil && !found {
			if slices.Contains(beforeBuild, stage) && !builders {
				return false, "", nil
			}
			found = true
		}
	case c.judge:
		// An eval case's fit check: the project's builder's model, chosen as for a ticket (12.7).
		var b models.Choice
		b, err = models.Choose(ctx, d.Pool, models.Ask{Role: "builder", Machine: ask.Machine, Project: ask.Project})
		if _, ok := models.IsWait(err); ok {
			err = nil
		}
		builder = b.Model
	default:
		builder, err = EvalBuilderModel(ctx, d.Pool, c.project, c.branch)
	}
	if err != nil {
		return false, "", err
	}
	if builder == "" {
		return false, "", fmt.Errorf("the %s waits: the builder's model is not known: %w", c.role, ErrCheckFamily)
	}
	// What runs is the name its CLI is given, or the model with none; a check names a model, and a name
	// of its model's family (the reviews of 1.15's change, finding 1, and the second round's).
	if c.model == "" {
		return false, "", fmt.Errorf("the %s waits: it names no model: %w", c.role, ErrCheckFamily)
	}
	family := cli.Family(c.model)
	if !cli.Known(family) {
		return false, "", fmt.Errorf("the %s waits: %s is %s: a pin of the %s on a model of a known family (Gemini, Claude, GPT) lets it run: %w",
			c.role, c.model, cli.UnknownFamily, mrole, ErrCheckFamily)
	}
	if c.name != "" && cli.Family(c.name) != family {
		return false, "", fmt.Errorf("the %s waits: its name %s is not of its model's family, %s: %w", c.role, c.name, family, ErrCheckFamily)
	}
	ask.Avoid = cli.Family(builder)
	if !cli.Known(ask.Avoid) {
		return false, "", fmt.Errorf("the %s waits: the builder's model %s is %s: the check runs once the builder's last run is on a model of a known family, or with the rule for checks off for the project: %w",
			c.role, builder, cli.UnknownFamily, ErrCheckFamily)
	}
	if family != ask.Avoid {
		return false, "", nil
	}
	cannot, err := models.RuleCannotBeMet(ctx, d.Pool, ask)
	if err != nil {
		return false, "", err
	}
	if !cannot {
		return false, "", fmt.Errorf("the %s waits: %s is of the builder's family, %s: %w", c.role, passed(c.name, c.model), ask.Avoid, ErrCheckFamily)
	}
	return true, ask.Avoid, nil
}

// checkMove is checkFamily for every continue of l's subtask on model and name: Crew's at a limit (12.7),
// the operator's, whose choice the rule for checks moves as it moves a pin (12.3; the security review of
// 1.15's change, finding 3), and one on the run's own model, since the builder may have run again since
// (the second round's, finding 2). off is set only for a continue onto another model or name, whose
// news it is.
func (d *Driver) checkMove(ctx context.Context, l latest, model, name string) (off bool, avoid string, err error) {
	c := checkRun{model: model, name: name, branch: l.branch, judge: !l.tools && l.branch == ""}
	var scope string
	if err := d.Pool.QueryRow(ctx, `SELECT role, kind, scope, coalesce(project::text, ''), coalesce(ticket, 0) FROM subtasks WHERE id = $1`,
		l.subtask).Scan(&c.role, &c.kind, &scope, &c.project, &c.ticket); err != nil {
		return false, "", err
	}
	c.eval = scope == "eval"
	off, avoid, err = d.checkFamily(ctx, c)
	return off && !(model == l.model && passed(name, model) == l.name), avoid, err
}

// fits says a run of one of checkKinds names what its kind does: a fit check no ticket, as an eval's
// judge; an eval's review its branch and no ticket; a ticket's check its ticket and no eval. A run its
// role makes a check, of another kind, is taken as it names itself (the second round of the security
// review of 1.15's change, finding 1).
func (c checkRun) fits() bool {
	switch c.kind {
	case "eval_fit":
		return c.ticket == 0 && c.eval && c.judge
	case "eval_review":
		return c.ticket == 0 && c.eval && c.branch != ""
	case "review", "verify", "stage", "judge":
		return c.ticket != 0 && !c.eval
	}
	return true
}

// ruleNotMet is the news that a ticket's check runs on the builder's family, avoid, since the rule for
// checks could not be met (12.3), recorded with the run that does.
func ruleNotMet(ctx context.Context, tx pgx.Tx, project string, ticket int64, run, role, avoid string) error {
	if ticket == 0 {
		return nil
	}
	return board.RecordSignal(ctx, tx, board.Signal{Project: project, Ticket: ticket, Run: run, Kind: "check_rule_not_met",
		Detail: map[string]any{"role": role, "family": avoid}})
}
