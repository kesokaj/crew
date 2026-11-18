package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// continueRun gives a subtask's idle run its next turn in the same conversation, once: record runs in
// the transaction that records the turn. When the guard holds or slows its last run's group, the
// subtask moves within its bracket to a model in another group that has room, in a new sandbox (12.7).
// A lead's turn waits for a lead slot of its CLI (7.9).
func (p *Pipeline) continueRun(ctx context.Context, j *job, s *sub, text string, record func(ctx context.Context, tx pgx.Tx, run string) error) error {
	req := runs.ContinueRequest{Text: text, Auto: true, Finishing: true, Record: record}
	if s.cli != "" && s.model != "" {
		slow, err := models.GroupSlowOrLimited(ctx, p.Pool, j.machine, s.cli, s.model)
		if err != nil {
			return err
		}
		if slow {
			next, err := p.NextChoice(ctx, s.id)
			if err != nil {
				return err
			}
			if next != nil {
				req.CLI, req.Model, req.Name, req.Effort = next.CLI, next.Model, next.Name, next.Effort
				req.ModelFrom, req.EffortFrom = next.ModelFrom, next.EffortFrom
			}
		}
	}
	defer planning.LockSlots(p.Pool)()
	cliName := s.cli
	if req.CLI != "" {
		cliName = req.CLI
	}
	what := "the " + s.role + "'s next turn"
	if s.role == "lead" {
		what = "the lead's next turn"
	}
	ask := planning.LeadAsk{Key: fmt.Sprintf("turn:%d", s.id), Owner: "pipeline", Class: planning.ClassOperatorWaiting,
		Continues: true, Priority: planning.NormalPriority, Own: planning.NormalPriority, Project: j.proj.ID, CLI: cliName, What: what}
	// A turn with no ticket is the project's; one whose ticket cannot be read takes no slot, which
	// would be in the wrong class and priority.
	var ticket, stage string
	var priority, own int
	switch err := p.Pool.QueryRow(ctx, `SELECT p.key || '-' || t.number, coalesce(t.operator_priority, $2), t.priority, t.stage FROM subtasks s
		JOIN tickets t ON t.id = s.ticket JOIN projects p ON p.id = t.project WHERE s.id = $1`, s.id, planning.NormalPriority).Scan(&ticket, &priority, &own, &stage); {
	case err == nil:
		cls := planning.ClassTicket
		what = "Continue " + s.role + " on " + ticket
		if s.role == "curator" || s.role == "lead" || s.kind == kindAnalysis {
			cls = planning.ClassAnalysis
			what = "Analyse the ticket " + ticket
		}
		stageRank := capacity.StageRank(stage)
		if s.kind == kindSummary {
			stageRank = 1
		}
		ask.Class, ask.StageRank, ask.Ticket, ask.Priority, ask.Own, ask.What = cls, stageRank, ticket, priority, own, what
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	ok, err := planning.TakeSlot(ctx, p.Pool, j.machine, ask)
	if err != nil || !ok {
		return err
	}
	key := ask.Key
	_, err = p.Runs.Continue(ctx, s.id, req)
	tErr := planning.Taken(ctx, p.Pool, key)
	if errors.Is(err, runs.ErrBusy) || errors.Is(err, board.ErrTaken) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
		return nil
	}
	if err == nil {
		err = tErr
	}
	return err
}

// deliver marks the comments given to the run that takes them (11.6), in its transaction.
func deliver(cs []board.Record) func(ctx context.Context, tx pgx.Tx, run string) error {
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return func(ctx context.Context, tx pgx.Tx, run string) error {
		if len(ids) == 0 {
			return nil
		}
		return board.Deliver(ctx, tx, ids, run)
	}
}

// give marks the comments a new run's prompt holds as its own, so it may answer them (11.6).
func give(cs []board.Record) func(ctx context.Context, tx pgx.Tx, run string) error {
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return func(ctx context.Context, tx pgx.Tx, run string) error {
		if len(ids) == 0 {
			return nil
		}
		return board.Give(ctx, tx, ids, run)
	}
}

// both runs two recorders in one transaction.
func both(a, b func(ctx context.Context, tx pgx.Tx, run string) error) func(ctx context.Context, tx pgx.Tx, run string) error {
	return func(ctx context.Context, tx pgx.Tx, run string) error {
		if a != nil {
			if err := a(ctx, tx, run); err != nil {
				return err
			}
		}
		if b != nil {
			return b(ctx, tx, run)
		}
		return nil
	}
}

// startSpec is a run to start on a ticket.
type startSpec struct {
	kind, role, title string
	parts             []runs.PromptPart
	branch            string
	// base is where branch starts when it is not pushed, and a branch the run may read (7.11); subtask
	// is the queued subtask an agent created that the run takes.
	base            string
	subtask         int64
	readOnly, judge bool
	continues       string
	commit          string // the head its stage agent judges
	comments        []board.Record
	// modelRole is whose model the run takes, when it is not its role's own: a project's own stage's
	// agent runs on the reviewer's (7.4).
	modelRole string
}

// start starts a run on a ticket with the role's model (12.3), and, for the lead, a lead slot of its
// CLI (7.9). It returns "" when the run waits for a slot or a model. What the run is given is marked
// taken with it.
func (p *Pipeline) start(ctx context.Context, j *job, t *board.Ticket, sp startSpec) (string, error) {
	role := sp.role
	mrole := role
	if sp.modelRole != "" {
		mrole = sp.modelRole
	}
	// A run that needs the project's setup waits while the ticket that fixes it is open (21).
	if why, err := p.waitsForSetup(ctx, j, t, role); err != nil || why != "" {
		if err != nil {
			return "", err
		}
		return "", p.Board.Wait(ctx, t, sp.title+": "+why)
	}
	// A ticket that is not already active waits when the project's or machine's max_active_tickets is reached (7.9, R52).
	if why, err := p.checkActiveTicketLimit(ctx, j, t, sp.kind); err != nil || why != "" {
		if err != nil {
			return "", err
		}
		return "", p.Board.Wait(ctx, t, sp.title+": "+why)
	}
	pin, _ := t.Agents[mrole].(map[string]any)
	ask := models.Ask{Role: mrole, Machine: j.machine, Project: j.doc, Pin: pin, Bracket: models.TicketBracket(mrole, sp.kind, t.Kind)}
	// A role that checks keeps off the builder's family, and waits with none to keep off (checkfamily.go).
	if why, err := avoidBuilder(ctx, p.Pool, t, sp.kind, &ask); err != nil || why != "" {
		if err != nil {
			return "", err
		}
		return "", p.Board.Wait(ctx, t, sp.title+": "+why)
	}
	if err := p.steer(ctx, j, t, mrole, &ask); err != nil {
		return "", err
	}
	c, err := models.Choose(ctx, p.Pool, ask)
	if w, ok := models.IsWait(err); ok {
		return "", p.Board.Wait(ctx, t, w.Reason)
	}
	if err != nil {
		return "", err
	}
	// A group close to its limit starts one run a pass, for the project served least recently on it.
	turn, group, first, taken, err := p.takeTurn(ctx, j.proj.ID, c)
	if err != nil {
		return "", err
	}
	if !turn {
		why := sp.title + ": " + group + " is close to its limit, and one run starts on it at a time"
		var name string
		if first != "" && p.Pool.QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, first).Scan(&name) == nil {
			why += "; " + name + " has its turn first"
		}
		return "", p.Board.Wait(ctx, t, why)
	}
	defer planning.LockSlots(p.Pool)()
	leadKey := sp.kind + ":" + t.Key
	if sp.subtask != 0 {
		leadKey = fmt.Sprintf("%s:%s:%d", sp.kind, t.Key, sp.subtask)
	}
	cls := planning.ClassTicket
	if sp.kind == kindAnalysis || role == "curator" || role == "lead" {
		cls = planning.ClassAnalysis
	}
	stageRank := capacity.StageRank(t.Stage)
	if sp.kind == kindSummary {
		stageRank = 1
	}
	across, own := queuePriorities(t)
	ok, err := planning.TakeSlot(ctx, p.Pool, j.machine, planning.LeadAsk{Key: leadKey, Owner: "pipeline", Class: cls,
		Continues: sp.continues != "", StageRank: stageRank,
		Priority: across, Own: own, Project: j.proj.ID, Ticket: t.Key, CLI: c.CLI, What: sp.title + " " + t.Key})
	if err != nil {
		return "", err
	}
	if !ok {
		if sp.kind == kindAnalysis || role == "curator" || role == "lead" {
			return "", p.Board.Wait(ctx, t, "its analysis waits for a lead slot of "+c.CLI)
		}
		return "", p.Board.Wait(ctx, t, sp.title+": waits for a run slot of "+c.CLI)
	}
	timeout := time.Duration(num(j.doc, "limits.run_timeout_minutes", 60)) * time.Minute
	if sp.judge {
		timeout = 10 * time.Minute
	}
	commit := sp.commit
	// Every run on a ticket is given its project's knowledge (9.3, 9.9).
	parts := slices.Concat(sp.parts, p.knowledgeParts(ctx, j, t, role))
	run, subtask, err := p.Runs.StartRole(ctx, runs.RoleRun{
		Role: role, Kind: sp.kind, Title: sp.title, Project: j.proj.ID, Ticket: t.ID, Branch: sp.branch, Base: sp.base, Subtask: sp.subtask,
		ReadOnly: sp.readOnly, Judge: sp.judge, Continues: sp.continues, Owner: "pipeline",
		CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: timeout,
		MaxTurns: int(num(j.doc, "limits.run_max_turns", 200)),
		Record: both(give(sp.comments), func(ctx context.Context, tx pgx.Tx, run string) error {
			if err := board.Unstuck(ctx, tx, t.ID); err != nil {
				return err
			}
			if commit == "" {
				return nil
			}
			_, err := tx.Exec(ctx, `UPDATE subtasks SET commit = $2 WHERE id = (SELECT subtask FROM runs WHERE id = $1)`, run, commit)
			return err
		}),
	})
	if leadKey != "" {
		if tErr := planning.Taken(ctx, p.Pool, leadKey); tErr != nil {
			p.log().Error("taking a started lead run out of the queue", "error", tErr)
		}
	}
	if errors.Is(err, runs.ErrNoRoom) {
		// The machine is full (7.9, 21): the ticket says so on its card, and asks again next pass.
		return "", p.Board.Wait(ctx, t, sp.title+": "+capacity.Waits(capacity.SizeOf(role)))
	}
	if errors.Is(err, runs.ErrCheckFamily) {
		// The guard every run passes kept a check off the builder's family (internal/runs/checkguard.go):
		// it waits as at a limit, with Crew's own reason.
		return "", p.Board.Wait(ctx, t, sp.title+": "+err.Error())
	}
	if errors.Is(err, runs.ErrNotSignedIn) || errors.Is(err, board.ErrTaken) || errors.Is(err, runs.ErrHeld) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	taken(true)
	// The news that the rule for checks was not met is recorded with the run, by the guard every run
	// passes (internal/runs/checkguard.go, since 1.15).
	// A pin or a project's setting that names a model no CLI lists now was passed over: the operator
	// gets news of it (12.3).
	if len(c.Ignored) > 0 {
		p.signal(ctx, j, t, run, "model_gone", map[string]any{"role": role, "ignored": c.Ignored, "ran": c.Entry()})
	}
	p.log().Info("a ticket's run started", "ticket", t.Key, "kind", sp.kind, "role", role, "run", run, "subtask", subtask,
		"cli", c.CLI, "model", c.Model)
	return run, nil
}

// queuePriorities are a ticket's places in the lead queue (7.9, from 0.42): across projects the
// priority the operator set, else normal; within its project its own priority, an agent's too.
func queuePriorities(t *board.Ticket) (across, own int) {
	if t.OperatorPriority != nil {
		return *t.OperatorPriority, t.Priority
	}
	return planning.NormalPriority, t.Priority
}

// steer sends a builder's ticket where Crew wants it (12.3): every build of one ticket runs on the
// pick of its first, so a fix round continues its conversation; a new ticket may be a try or a unit of
// a comparison. Nothing is steered on a ticket with a pin, or in a project that set the agent itself.
func (p *Pipeline) steer(ctx context.Context, j *job, t *board.Ticket, role string, ask *models.Ask) error {
	if !slices.Contains(models.ChosenRoles, role) || p.Chooser == nil {
		return nil
	}
	own, _ := j.doc["agents"].(settings.Doc)
	_, projectSet := own[role].(settings.Doc)
	pinned := len(ask.Pin) > 0 || projectSet
	if pinned {
		return nil
	}
	// An agent that checks: a unit is one run, steered by its ticket and role (12.3, from M12).
	if role != "builder" {
		pick, from, err := p.Chooser.Steer(ctx, role, t.Project+"/"+t.Key+"/"+role, pinned)
		if err != nil || from == "" {
			return err
		}
		ask.Prefer, ask.PreferEffort, ask.PreferFrom = pick.Entry, pick.Effort, from
		return nil
	}
	var entry, effort, from string
	err := p.Pool.QueryRow(ctx, `SELECT r.cli || '/' || r.model, coalesce(r.effort, ''), coalesce(r.model_from, '')
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = $1 AND s.kind = 'build' AND r.model IS NOT NULL
		ORDER BY r.started_at LIMIT 1`, t.ID).Scan(&entry, &effort, &from)
	switch {
	case err == nil && from != models.FromLimit && from != models.FromCheckRule:
		ask.Prefer, ask.PreferEffort, ask.PreferFrom = entry, effort, from
		return nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	pick, from, err := p.Chooser.Steer(ctx, role, t.Project+"/"+t.Key, pinned)
	if err != nil || from == "" {
		return err
	}
	ask.Prefer, ask.PreferEffort, ask.PreferFrom = pick.Entry, pick.Effort, from
	return nil
}
