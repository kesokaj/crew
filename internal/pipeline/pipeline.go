// Package pipeline is the orchestrator's part for tickets (design 15.2, 11.3): it analyses a ticket
// in Analysis, starts the builder of a ticket in In progress and the agent of each stage after it,
// runs the goal loop on what each run reports, runs the checks that decide in a check sandbox,
// merges a ticket that passed its last stage, and has its summary written. It derives what runs from
// the board and the runs' records, keeps no plan of its own, and acts only after a run's turn ended:
// a tool call records what the agent says, and Crew acts on it (11.3).
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/rooms"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// Runs is what the pipeline asks of the run driver.
type Runs interface {
	StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error)
	Continue(ctx context.Context, subtask int64, req runs.ContinueRequest) (string, error)
	Finish(ctx context.Context, runID string) error
	End(ctx context.Context, runID, reason, detail string) error
	// EndWaited is End for a run that ended while it waited for Crew to act on its turn (since 0.55).
	EndWaited(ctx context.Context, runID, reason, detail string) error
	Warn(ctx context.Context, runID, text string)
	// Stop ends a run now, with its work in progress kept (12.10).
	Stop(ctx context.Context, runID string) error
}

// Checks runs a check sandbox (13.9).
type Checks interface {
	ExecFresh(ctx context.Context, spec harness.CheckSpec) ([]harness.CheckResult, error)
}

// Pipeline drives every running project's tickets.
type Pipeline struct {
	Pool   *pgxpool.Pool
	Board  *board.Board
	Runs   Runs
	Checks Checks
	// Settings are the machine's settings as they are now.
	Settings func(ctx context.Context) (settings.Doc, error)
	// Identity is the operator's git identity, which a merge commit is made as.
	Identity func(ctx context.Context) (gitstore.Identity, error)
	// Chooser is Crew's automatic choice of the agents' entries (12.3); nil leaves every agent on its
	// list.
	Chooser *models.Chooser
	// Room is the capacity check (7.9, R71): a check sandbox starts only when it fits on the machine,
	// as a run does in the driver. nil checks nothing.
	Room *capacity.Gate
	// Rooms manages each project's unified #<project> channel and feature threads (R214).
	Rooms *rooms.Service
	// Version is Crew's version, which is the baseline's (10.3).
	Version string
	// Improve is the Crew level's side of improvements (10.5); nil runs no improver.
	Improve *improve.Service
	// Example rewrites an earlier first message of the operator's with the given rules of the prompt
	// writer, or its own with nil (7.2); nil makes no examples.
	Example func(ctx context.Context, rewriteID int64, rules map[string]string) (string, error)
	Log     *slog.Logger
	// Ready, when set, is closed once the server has taken over the runs of the server before it.
	Ready <-chan struct{}

	mu   sync.Mutex
	kick chan struct{}
	// evalChecks are the eval runs whose checks run now, in the background, and exampling says the
	// examples of a change of the prompt writer's rules are being made.
	evalChecks map[int64]bool
	exampling  bool
	// evaluated is when Crew last looked at its numbers: once a minute is enough.
	evaluated time.Time
	// wg waits for the check runs in flight, in tests; a check run's row says it runs, so a pass that
	// runs again starts none twice.
	wg sync.WaitGroup
	// fetching holds the projects whose remote main is being fetched (6.6).
	fetching sync.Map
	// shares are the turns projects take on a group of models close to its limit (7.9, R164).
	shares shares
}

func (p *Pipeline) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Kick asks for a reconcile soon.
func (p *Pipeline) Kick() {
	p.mu.Lock()
	if p.kick == nil {
		p.kick = make(chan struct{}, 1)
	}
	k := p.kick
	p.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// Run reconciles on a timer and whenever Kick is called, until ctx ends. Check runs that a server
// before this one left running are errors: they are tried again.
func (p *Pipeline) Run(ctx context.Context, every time.Duration) {
	if p.Ready != nil {
		select {
		case <-ctx.Done():
			return
		case <-p.Ready:
		}
	}
	if _, err := p.Pool.Exec(ctx, `UPDATE check_runs SET state = 'error', detail = 'Crew restarted while it ran', ended_at = now()
		WHERE state = 'running'`); err != nil {
		p.log().Error("ending the check runs of the server before", "error", err)
	}
	if _, err := p.Pool.Exec(ctx, `UPDATE remote_mains SET state = 'error', detail = 'Crew restarted while it ran' WHERE state = 'checking'`); err != nil {
		p.log().Error("ending the checks of the remote's main of the server before", "error", err)
	}
	// A check of the project's setup on main that a restart cut off could not tell: it is acted on as
	// one, so the runs that wait for it are not held for ever (review of M13).
	if _, err := p.Pool.Exec(ctx, `UPDATE setup_checks SET state = 'error', detail = 'Crew restarted while it ran', decided_at = now(), acted_at = now()
		WHERE state = 'checking'`); err != nil {
		p.log().Error("ending the checks of the setup of the server before", "error", err)
	}
	p.Kick()
	p.mu.Lock()
	k := p.kick
	p.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k:
		}
		p.Reconcile(ctx)
	}
}

// Wait waits for the check runs and the fetches in flight: for tests.
func (p *Pipeline) Wait() { p.wg.Wait() }

// Reconcile looks at every running project's tickets once and starts what waits.
func (p *Pipeline) Reconcile(ctx context.Context) {
	doc, err := p.Settings(ctx)
	if err != nil {
		p.log().Error("the pipeline cannot read the settings", "error", err)
		return
	}
	projects, err := p.Board.Projects(ctx)
	if err != nil {
		p.log().Error("the pipeline cannot read the projects", "error", err)
		return
	}
	// The guard as this pass reads it: a group close to its limit starts one run a pass (7.9, R164).
	if g, err := models.LoadGuard(ctx, p.Pool, num(doc, "quota.slow_down_at", 0)); err != nil {
		p.log().Error("the pipeline cannot read the quota guard", "error", err)
		p.shares.beginPass(nil)
	} else {
		p.shares.beginPass(&g)
	}
	// The project served least recently goes first, so one project cannot spend a window while the
	// others wait for it (7.9, R164).
	if err := p.leastServedFirst(ctx, projects); err != nil {
		p.log().Error("the pipeline cannot order the projects", "error", err)
	}
	// A lead run, or a sandbox that waited for room, this pass did not ask for again no longer waits.
	pass := time.Now()
	defer func() {
		if err := planning.Sweep(context.WithoutCancel(ctx), p.Pool, "pipeline", pass); err != nil {
			p.log().Error("the pipeline cannot clear the lead queue", "error", err)
		}
		if p.Room != nil {
			if err := p.Room.Sweep(context.WithoutCancel(ctx), "pipeline", pass); err != nil {
				p.log().Error("the pipeline cannot clear the queue for room", "error", err)
			}
		}
	}()
	for _, proj := range projects {
		if proj.State == "archived" {
			continue
		}
		// A project pauses at its limits, and resumes once they are raised (7.7).
		switch did, err := p.Board.HoldLimits(ctx, proj); {
		case err != nil:
			p.log().Error("the project's limits", "project", proj.Name, "error", err)
		case did == "paused":
			proj.State = "paused"
			p.log().Info("paused at a limit", "project", proj.Name)
		case did == "resumed":
			proj.State = "running"
			p.log().Info("resumed: no limit is passed any more", "project", proj.Name)
		}
		if err := p.reconcileProject(ctx, doc, proj); err != nil {
			p.log().Error("the pipeline", "project", proj.Name, "error", err)
		}
	}
	for _, proj := range projects {
		if proj.State == "archived" {
			continue
		}
		if err := p.reconcileMaintenance(ctx, doc, proj); err != nil {
			p.log().Error("the pipeline maintenance", "project", proj.Name, "error", err)
		}
	}
	// The eval suites: one on the machine at a time (9.6). Nothing new starts while Crew holds.
	if hold.Held(ctx, p.Pool) == "" {
		if err := p.evals(ctx, doc); err != nil {
			p.log().Error("the evals", "error", err)
		}
		// The improver and the improvements (10.4, 10.5).
		if err := p.improver(ctx, doc); err != nil {
			p.log().Error("the improver", "error", err)
		}
	}
	// Crew looks at its numbers when runs of the agents it chooses for have ended (12.3, 15.2).
	p.mu.Lock()
	look := p.Chooser != nil && time.Since(p.evaluated) >= time.Minute
	if look {
		p.evaluated = time.Now()
	}
	p.mu.Unlock()
	if look {
		for _, role := range models.ChosenRoles {
			if err := p.Chooser.Evaluate(ctx, role); err != nil {
				p.log().Error("Crew's choice of a model", "role", role, "error", err)
			}
		}
	}
}

// leastServedFirst orders the projects by when a run last started in each, the one never served first.
func (p *Pipeline) leastServedFirst(ctx context.Context, projects []*board.Project) error {
	rows, err := p.Pool.Query(ctx, `SELECT s.project::text, max(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.project IS NOT NULL GROUP BY s.project`)
	if err != nil {
		return err
	}
	last := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if rows.Scan(&id, &at) == nil {
			last[id] = at
		}
	}
	rows.Close()
	slices.SortStableFunc(projects, func(a, b *board.Project) int { return last[a.ID].Compare(last[b.ID]) })
	return rows.Err()
}

// job is what one pass knows of a project: its settings, the machine's, and its pipeline.
type job struct {
	machine, doc settings.Doc
	proj         *board.Project
	pipe         board.Pipeline
	// held says why nothing new starts in this pass (7.9, 16.10): the turns of runs that are going are
	// still acted on, so running runs finish.
	held string
}

func (p *Pipeline) reconcileProject(ctx context.Context, machine settings.Doc, proj *board.Project) error {
	if proj.State != "running" && proj.State != "done" {
		return nil
	}
	doc, err := p.Board.Settings(ctx, p.Pool, proj)
	if err != nil {
		return err
	}
	j := &job{machine: machine, doc: doc, proj: proj, pipe: board.PipelineOf(doc), held: hold.Held(ctx, p.Pool)}
	// What the project's overrides measured, once enough tickets were done after them (9.6).
	if err := p.measureOverrides(ctx, j); err != nil {
		p.log().Error("measuring the overrides", "project", proj.Name, "error", err)
	}
	// The project's setup, when runs fail at it (21, 13.5).
	if err := p.watchSetup(ctx, j); err != nil {
		p.log().Error("the project's setup", "project", proj.Name, "error", err)
	}
	// The tickets whose pull request someone merged on GitHub (21, 7.12).
	if err := p.outsideMerges(ctx, j); err != nil {
		p.log().Error("the pull requests merged on GitHub", "project", proj.Name, "error", err)
	}
	tickets, err := p.Board.Tickets(ctx, proj)
	if err != nil {
		return err
	}
	sortTicketsForReconcile(j.pipe, tickets)
	if rows, err := p.Pool.Query(ctx, `SELECT DISTINCT s.ticket FROM subtasks s JOIN runs r ON r.subtask = s.id
		WHERE s.project = $1 AND s.ticket IS NOT NULL AND r.state = 'idle'`, proj.ID); err == nil {
		idleTickets := map[int64]bool{}
		for rows.Next() {
			var tid int64
			if rows.Scan(&tid) == nil {
				idleTickets[tid] = true
			}
		}
		rows.Close()
		if len(idleTickets) > 0 {
			slices.SortStableFunc(tickets, func(a, b *board.Ticket) int {
				switch {
				case idleTickets[a.ID] && !idleTickets[b.ID]:
					return -1
				case !idleTickets[a.ID] && idleTickets[b.ID]:
					return 1
				default:
					return 0
				}
			})
		}
	}
	var errs []error
	for _, t := range tickets {
		if t.Stage == settings.StagePlanning || t.Stage == settings.StageBacklog {
			continue
		}
		if err := p.ticket(ctx, j, t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Key, err))
		}
	}
	// A ticket long in its stage is news (8.2): a side task, logged when it fails.
	if err := p.slowStages(ctx, j, time.Now()); err != nil {
		p.log().Error("the time in a stage", "project", proj.Name, "error", err)
	}
	if j.held != "" {
		return errors.Join(errs...)
	}
	// What came in on the remote's main from outside (6.6, 7.12).
	mainWaits, err := p.remote(ctx, j, false)
	if err != nil {
		errs = append(errs, fmt.Errorf("the remote's main: %w", err))
	}
	if proj.State == "running" && !mainWaits {
		if err := p.mergeQueue(ctx, j); err != nil {
			errs = append(errs, fmt.Errorf("the merge queue: %w", err))
		}
		if err := p.planComments(ctx, j); err != nil {
			errs = append(errs, fmt.Errorf("the comments on the plan: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (p *Pipeline) reconcileMaintenance(ctx context.Context, machine settings.Doc, proj *board.Project) error {
	doc, err := p.Board.Settings(ctx, p.Pool, proj)
	if err != nil {
		return err
	}
	j := &job{machine: machine, doc: doc, proj: proj, pipe: board.PipelineOf(doc), held: hold.Held(ctx, p.Pool)}
	if proj.State == "running" || proj.State == "done" {
		// Its retrospectives (9.8): when a part of the plan is complete, and when it is Done, before its
		// final report. A side task too.
		if err := p.retrospectives(ctx, j); err != nil {
			p.log().Error("the retrospective", "project", proj.Name, "error", err)
		}
		// A Done project's final report (7.2): a side task, logged when it fails.
		if err := p.finalReport(ctx, j); err != nil {
			p.log().Error("the final report", "project", proj.Name, "error", err)
		}
		// The curator's housekeeping (9.8): a side task too.
		if err := p.housekeeping(ctx, j); err != nil {
			p.log().Error("housekeeping", "project", proj.Name, "error", err)
		}
		// The fit checks of its waiting eval cases (9.6, since 0.51): a side task too.
		if err := p.fitChecks(ctx, j); err != nil {
			p.log().Error("the fit checks of the eval cases", "project", proj.Name, "error", err)
		}
	}
	// A conversation grows in every project that is not archived: a breakdown's too. Its summary is
	// a side task: an error is logged, and the tickets go on.
	if err := p.conversations(ctx, j); err != nil {
		p.log().Error("the summaries of conversations", "project", proj.Name, "error", err)
	}
	return nil
}

// ticket is one ticket's step of a pass: a turn that ended is acted on first; then what its stage
// needs is started.
func (p *Pipeline) ticket(ctx context.Context, j *job, t *board.Ticket) error {
	if err := p.endHelpers(ctx, t.ID); err != nil {
		return err
	}
	subs, err := p.subtasks(ctx, t)
	if err != nil {
		return err
	}
	if done, err := p.reportedDone(ctx, j, t, subs); err != nil || done {
		return err
	}
	p.steerActiveComments(ctx, j, t, subs)
	// A judge's turn first: the builder's run waits idle for its answer (7.1).
	for _, s := range subs {
		if s.runState == "idle" && s.kind == kindJudge {
			return p.afterTurn(ctx, j, t, subs, s)
		}
	}
	for _, s := range subs {
		if s.runState == "idle" {
			return p.afterTurn(ctx, j, t, subs, s)
		}
	}
	if err := p.markMovedOn(ctx, t, subs); err != nil {
		return err
	}
	if j.held != "" {
		return nil
	}
	switch t.Stage {
	case settings.StageAnalysis:
		return p.analysis(ctx, j, t, subs)
	case settings.StageDone, settings.StageClosed:
		return p.summary(ctx, j, t, subs)
	}
	return p.stage(ctx, j, t, subs)
}

// afterTurn acts on a turn that ended well, by the kind of its subtask. The operator's comments that
// came meanwhile go first, as a turn of their own in the same conversation (11.6, step 3).
func (p *Pipeline) afterTurn(ctx context.Context, j *job, t *board.Ticket, subs []*sub, s *sub) error {
	// A summary writer and a judge are one call each: a comment waits for the next run that works.
	if s.kind != kindSummary && s.kind != kindJudge && t.Stage != settings.StageDone && t.Stage != settings.StageClosed {
		waiting, err := p.Board.Waiting(ctx, j.proj.ID, t.ID)
		if err != nil {
			return err
		}
		if len(waiting) > 0 {
			return p.continueRun(ctx, j, s, commentsText(waiting)+"\nThen go on with your work where you left it.", deliver(waiting))
		}
	}
	switch s.kind {
	case kindAnalysis:
		return p.afterAnalysis(ctx, j, t, s)
	case kindBuild, kindFix:
		return p.afterBuild(ctx, j, t, subs, s)
	case kindReview, kindTest, kindE2E, kindVerify, kindStage:
		return p.afterJudging(ctx, j, t, s)
	case kindSummary:
		return p.afterSummary(ctx, j, t, s)
	case kindJudge:
		return p.afterJudge(ctx, j, t, s)
	case kindPart, kindJoin:
		return p.afterPart(ctx, j, t, s)
	case kindResearch:
		return p.afterResearch(ctx, j, t, s)
	case kindLookAgain:
		return p.afterLookAgain(ctx, j, t, subs, s)
	}
	return p.Runs.Finish(ctx, s.run)
}

// missing handles a turn that ended without the call that reports: one more turn that asks for it,
// and after that the run has failed (11.3): it ends as a stop, and its subtask is retried once.
func (p *Pipeline) missing(ctx context.Context, j *job, s *sub) error {
	tool := toolOf[s.kind]
	if !s.reminded {
		text := fmt.Sprintf("You ended your turn without calling %s, which is how Crew learns that your work is done. "+
			"Finish what is left, call %s, and end your turn.", tool, tool)
		return p.continueRun(ctx, j, s, text, func(ctx context.Context, tx pgx.Tx, _ string) error {
			_, err := tx.Exec(ctx, `UPDATE subtasks SET reminded = true WHERE id = $1`, s.id)
			return err
		})
	}
	return p.Runs.End(ctx, s.run, runs.ReasonRounds, "the run did not call "+tool+" after a reminder")
}

// signal records a signal (10.2); a failure to record one is logged, and stops nothing.
func (p *Pipeline) signal(ctx context.Context, j *job, t *board.Ticket, run, kind string, detail map[string]any) {
	var ticket int64
	if t != nil {
		ticket = t.ID
	}
	if err := board.RecordSignal(ctx, p.Pool, board.Signal{Project: j.proj.ID, Ticket: ticket, Run: run, Kind: kind, Detail: detail}); err != nil {
		p.log().Error("recording a signal", "kind", kind, "error", err)
	}
}

// num reads a number of a settings document by its dotted path, or def.
func num(d settings.Doc, path string, def float64) float64 {
	var cur any = d
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return def
		}
		cur = m[k]
	}
	switch n := cur.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return def
}

// str reads a string of a settings document by its dotted path.
func str(d settings.Doc, path string) string {
	var cur any = d
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}
