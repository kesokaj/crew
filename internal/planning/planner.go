package planning

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// Runs is what the planner asks of the run driver.
type Runs interface {
	StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error)
	Continue(ctx context.Context, subtask int64, req runs.ContinueRequest) (string, error)
	Finish(ctx context.Context, runID string) error
	Stop(ctx context.Context, runID string) error
}

// Planner is the orchestrator's part for plans (15.2): it queues the planning agent for a draft with
// a message from the operator, the lead for a project with no breakdown, and the lead again for a
// breakdown sent back with a comment. They are lead runs: each takes a lead slot of its CLI, and
// waits for one in the class operator_waiting, a draft before a breakdown, then by age (7.9).
type Planner struct {
	Pool *pgxpool.Pool
	Runs Runs
	// Settings are the machine's settings as they are now.
	Settings func(ctx context.Context) (settings.Doc, error)
	// Version is Crew's version, which is the baseline's (10.3).
	Version string
	Log     *slog.Logger
	// Ready, when set, is closed once the server has taken over the runs of the server before it and
	// the sandboxes' door answers: the planner starts nothing before (21, "Backend restart").
	Ready <-chan struct{}

	mu   sync.Mutex
	kick chan struct{}
}

// The limits of a planning turn and of the breakdown.
const (
	planningTimeout  = 30 * time.Minute
	breakdownTimeout = time.Hour
)

func (pl *Planner) log() *slog.Logger {
	if pl.Log != nil {
		return pl.Log
	}
	return slog.Default()
}

// Kick asks for a reconcile soon.
func (pl *Planner) Kick() {
	pl.mu.Lock()
	if pl.kick == nil {
		pl.kick = make(chan struct{}, 1)
	}
	k := pl.kick
	pl.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// Run reconciles on a timer and whenever Kick is called, until ctx ends.
func (pl *Planner) Run(ctx context.Context, every time.Duration) {
	if pl.Ready != nil {
		select {
		case <-ctx.Done():
			return
		case <-pl.Ready:
		}
	}
	pl.Kick()
	pl.mu.Lock()
	k := pl.kick
	pl.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k:
		}
		pl.Reconcile(ctx)
	}
}

// choice is the CLI, the model, and the effort a run of a role starts on (12.3).
type choice = models.Choice

func (pl *Planner) choose(ctx context.Context, doc settings.Doc, role string, project settings.Doc) (choice, error) {
	return models.Choose(ctx, pl.Pool, models.Ask{Role: role, Machine: doc, Project: project})
}

func dig(d settings.Doc, path ...string) any {
	var cur any = d
	for _, k := range path {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func str(v any) string { s, _ := v.(string); return s }

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ConfiguredSlots returns the configured max_concurrent_runs from doc (default 8), falling back to
// lead_slots.agy when a test passes an unnormalized doc.
func ConfiguredSlots(doc settings.Doc) int {
	if m, ok := doc["lead_slots"].(settings.Doc); ok {
		switch n := m[cli.AgyName].(type) {
		case int:
			if n >= 0 {
				return n
			}
		case float64:
			if int(n) >= 0 {
				return int(n)
			}
		}
	}
	switch v := doc["max_concurrent_runs"].(type) {
	case int:
		if v > 0 {
			return v
		}
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	}
	return 8
}

// Slots is the number of concurrent agent run slots of each CLI now: max_concurrent_runs, halved while
// a group of the CLI's models is close to its limit, and never below one (7.9, 12.7).
func Slots(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) (map[string]int, error) {
	out := map[string]int{}
	slow, _ := dig(doc, "quota", "slow_down_at").(float64)
	guard, err := models.LoadGuard(ctx, pool, slow)
	if err != nil {
		return nil, err
	}
	n := ConfiguredSlots(doc)
	if guard.CLIClose(cli.AgyName) && n > 1 {
		n /= 2
	}
	out[cli.AgyName] = n
	return out, nil
}

// SlotMu is the fallback mutex when LockSlots is called with a nil pool; per-pool mutexes in slotLocks
// keep parallel test databases from serializing on one process-wide lock while still serializing all
// loops (pipeline, planner, assistant) that share a database pool (7.9).
var (
	SlotMu    sync.Mutex
	slotLocks sync.Map
)

// LockSlots locks the slot mutex for pool and returns its unlock function.
func LockSlots(pool *pgxpool.Pool) func() {
	if pool == nil {
		SlotMu.Lock()
		return SlotMu.Unlock
	}
	v, _ := slotLocks.LoadOrStore(pool, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// FreeSlots counts the concurrent run slots of each CLI that are free now (7.9): its number less the
// agent runs whose turn runs on it (`starting` or `working`). An idle run waiting for the operator
// or for a check sandbox holds no slot.
func FreeSlots(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc) (map[string]int, error) {
	free, err := Slots(ctx, pool, doc)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT r.cli, count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE r.state IN ('starting', 'working')
		  AND (s.kind <> 'helper' OR s.parent IS NULL)
		GROUP BY r.cli`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return nil, err
		}
		free[c] -= n
	}
	return free, rows.Err()
}

// job is one lead run that waits: start it, or give its subtask its next turn.
type job struct {
	what    string
	project string // the project's id, for a breakdown
	role    string
	// key names the job in the lead queue while it waits; keyboard says the operator waits at the
	// keyboard for it (7.9).
	key      string
	keyboard bool
	// start starts the run, or the next turn, and marks what it takes in the same transaction.
	start func(ctx context.Context, c choice) (string, error)
}

// Reconcile looks at the drafts and the projects in planning, and starts what waits, as far as the
// lead slots go. Every action reads first what is there, so a reconcile that runs twice starts a
// thing once.
func (pl *Planner) Reconcile(ctx context.Context) {
	doc, err := pl.Settings(ctx)
	if err != nil {
		pl.log().Error("the planner cannot read the settings", "error", err)
		return
	}
	if err := pl.finishBreakdowns(ctx); err != nil {
		pl.log().Error("the planner cannot finish the breakdowns", "error", err)
	}
	if err := pl.finishFeatureSpecs(ctx); err != nil {
		pl.log().Error("the planner cannot finish feature specs", "error", err)
	}
	if err := pl.endTakenDrafts(ctx); err != nil {
		pl.log().Error("the planner cannot end the runs of drafts that became projects", "error", err)
	}
	var queue []job
	drafts, err := pl.draftJobs(ctx)
	if err != nil {
		pl.log().Error("the planner cannot read the drafts", "error", err)
	}
	queue = append(queue, drafts...)
	breakdowns, err := pl.breakdownJobs(ctx)
	if err != nil {
		pl.log().Error("the planner cannot read the projects in planning", "error", err)
	}
	queue = append(queue, breakdowns...)
	featureSpecs, err := pl.featureSpecJobs(ctx)
	if err != nil {
		pl.log().Error("the planner cannot read feature specs", "error", err)
	}
	queue = append(queue, featureSpecs...)
	defer LockSlots(pl.Pool)()
	pass := time.Now()
	// What this pass did not ask for again no longer waits.
	defer func() {
		if err := Sweep(context.WithoutCancel(ctx), pl.Pool, "planner", pass); err != nil {
			pl.log().Error("the planner cannot clear the lead queue", "error", err)
		}
	}()
	for _, j := range queue {
		var project settings.Doc
		if j.project != "" {
			if project, err = pl.projectSettings(ctx, j.project); err != nil {
				pl.log().Error("reading a project's settings", "project", j.project, "error", err)
				continue
			}
		}
		c, err := pl.choose(ctx, doc, j.role, project)
		if err != nil {
			pl.log().Warn("a lead run waits: none of its models can run", "what", j.what, "error", err)
			continue
		}
		// It waits in the one lead queue for a slot of its CLI, and does not move to another (7.9).
		ok, err := TakeSlot(ctx, pl.Pool, doc, LeadAsk{Key: j.key, Owner: "planner", Class: ClassOperatorWaiting, Keyboard: j.keyboard,
			Priority: NormalPriority, Own: NormalPriority, Project: j.project, CLI: c.CLI, What: j.what})
		if err != nil {
			pl.log().Error("the planner cannot queue a lead run", "what", j.what, "error", err)
			continue
		}
		if !ok {
			continue
		}
		run, err := j.start(ctx, c)
		if tErr := Taken(ctx, pl.Pool, j.key); tErr != nil {
			pl.log().Error("the planner cannot take a started run out of the lead queue", "error", tErr)
		}
		if errors.Is(err, runs.ErrBusy) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
			continue
		}
		if err != nil {
			pl.log().Warn("a lead run did not start; it is tried again", "what", j.what, "error", err)
			continue
		}
		pl.log().Info("a lead run started", "what", j.what, "run", run, "cli", c.CLI, "model", c.Model)
	}
}

func (pl *Planner) projectSettings(ctx context.Context, project string) (settings.Doc, error) {
	var name, key string
	if err := pl.Pool.QueryRow(ctx, `SELECT name, key FROM projects WHERE id = $1`, project).Scan(&name, &key); err != nil {
		return nil, err
	}
	return settings.EffectiveProject(ctx, pl.Pool, project, name, key)
}

// latestRun is the state of a subtask's newest run.
func latestRun(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, subtask int64) (id, state string, err error) {
	err = q.QueryRow(ctx, `SELECT id::text, state FROM runs WHERE subtask = $1 ORDER BY number DESC LIMIT 1`, subtask).Scan(&id, &state)
	return
}

// draftJobs are the drafts whose operator wrote what no run took yet, oldest message first.
func (pl *Planner) draftJobs(ctx context.Context) ([]job, error) {
	rows, err := pl.Pool.Query(ctx, `SELECT d.id::text, d.repo, min(m.id) FROM plan_drafts d JOIN draft_messages m ON m.draft = d.id
		WHERE d.state = 'open' AND m.run IS NULL GROUP BY d.id, d.repo ORDER BY min(m.id)`)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id   string
		repo *string
	}
	var list []pending
	for rows.Next() {
		var p pending
		var first int64
		if err := rows.Scan(&p.id, &p.repo, &first); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	var out []job
	for _, p := range list {
		var subtask int64
		err := pl.Pool.QueryRow(ctx, `SELECT id FROM subtasks WHERE draft = $1 AND kind = 'planning' ORDER BY id DESC LIMIT 1`, p.id).Scan(&subtask)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if subtask != 0 {
			if _, state, err := latestRun(ctx, pl.Pool, subtask); err == nil && (state == "starting" || state == "working") {
				continue // the message waits for the turn that runs to end
			}
		}
		ids, texts, err := pl.waitingMessages(ctx, p.id)
		if err != nil || len(ids) == 0 {
			continue
		}
		message := strings.Join(texts, "\n\n")
		draft, repo, sub := p.id, p.repo, subtask
		// The messages are marked taken in the transaction that records the run or the turn that
		// takes them, so each is given once, and none is lost (review of M5).
		take := func(ctx context.Context, tx pgx.Tx, run string) error {
			tag, err := tx.Exec(ctx, `UPDATE draft_messages SET run = $2 WHERE id = ANY($1) AND run IS NULL`, ids, run)
			if err == nil && tag.RowsAffected() != int64(len(ids)) {
				err = runs.ErrBusy // another pass took them meanwhile
			}
			return err
		}
		untake := func(ctx context.Context, tx pgx.Tx, run string) error {
			_, err := tx.Exec(ctx, `UPDATE draft_messages SET run = NULL WHERE id = ANY($1) AND run = $2`, ids, run)
			return err
		}
		j := job{what: "the planning agent of draft " + draft, role: "planner", key: "draft:" + draft, keyboard: true}
		if sub == 0 {
			j.start = func(ctx context.Context, c choice) (string, error) {
				run, _, err := pl.Runs.StartRole(ctx, runs.RoleRun{Role: "planner", Kind: "planning", Title: "Develop the plan",
					Draft: draft, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom,
					Parts: pl.planningPrompt(draft, repo, message), Timeout: planningTimeout, Record: take})
				return run, err
			}
		} else {
			j.start = func(ctx context.Context, c choice) (string, error) {
				return pl.Runs.Continue(ctx, sub, runs.ContinueRequest{Text: operatorSays(message), CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, ModelFrom: c.ModelFrom, EffortFrom: c.EffortFrom,
					Record: take, Unrecord: untake})
			}
		}
		out = append(out, j)
	}
	return out, nil
}

func (pl *Planner) waitingMessages(ctx context.Context, draft string) ([]int64, []string, error) {
	rows, err := pl.Pool.Query(ctx, `SELECT id, text, coalesce(quote, ''), coalesce(ask, '') FROM draft_messages
		WHERE draft = $1 AND run IS NULL ORDER BY id`, draft)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var texts []string
	for rows.Next() {
		var id int64
		var t, quote, ask string
		if err := rows.Scan(&id, &t, &quote, &ask); err != nil {
			return nil, nil, err
		}
		if quote != "" {
			t = aboutThePlan(t, quote, ask)
		}
		ids, texts = append(ids, id), append(texts, t)
	}
	return ids, texts, rows.Err()
}

// aboutThePlan is the operator's request about a part of the plan (7.2): the part is quoted as data,
// also when it holds something the planning agent once fetched from the internet (13.8).
func aboutThePlan(text, quote, ask string) string {
	what := "Improve this part of the plan: rewrite it in the plan, by the plan's template, and say in the conversation what you changed."
	if ask == AskReview {
		what = "Review this part of the plan: change nothing yet, and answer in the conversation with what is unclear, missing, or could be misunderstood."
	}
	f := fence.New(text, quote)
	out := what + "\n\n" + f.Data("The part of the plan", "the planning agent, in the plan", quote)
	if strings.TrimSpace(text) != "" {
		out += "\n\nThe operator adds: " + strings.TrimSpace(text)
	}
	return out
}

func operatorSays(message string) string {
	return "The operator's message:\n\n" + strings.TrimSpace(message)
}

func (pl *Planner) source(path string) string {
	return "baseline/" + path + "@" + pl.Version
}

// planningPrompt is the planning agent's first prompt: the lead's agent file, the planning guide,
// the plan's template, what the draft is, and the operator's message (8.6: each part with its source).
func (pl *Planner) planningPrompt(draft string, repo *string, message string) []runs.PromptPart {
	context := "This is plan draft " + draft + ". No project exists yet."
	if repo != nil {
		context += " The draft names the GitHub repository " + *repo + ": clone it with `git clone https://github.com/" + *repo +
			"` to read it."
	}
	return []runs.PromptPart{
		{Part: "agent", Source: pl.source("agents/curator.md"), Text: baseline.Body(baseline.MustText("agents/curator.md"))},
		{Part: "guide", Source: pl.source("guides/planning.md"), Text: baseline.MustText("guides/planning.md")},
		{Part: "template", Source: pl.source("templates/plan.md"), Text: "The plan template:\n\n```markdown\n" +
			strings.TrimSpace(baseline.MustText("templates/plan.md")) + "\n```"},
		{Part: "context", Source: "crew", Text: context},
		{Part: "operator", Source: "draft " + draft, Text: operatorSays(message)},
	}
}
