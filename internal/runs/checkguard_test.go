package runs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

// guardKit is the ticket kit with the models agy lists recorded.
func guardKit(t *testing.T) (*kit, int64) {
	t.Helper()
	k, ticket := ticketKit(t)
	modelstest.Recorded(t, k.db.Server)
	return k, ticket
}

// built records a builder's run of the ticket on model, ended.
func (k *kit) built(ticket int64, model string) {
	k.t.Helper()
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: model})
	k.wait(run, "working")
	k.d.Stop(context.Background(), run)
	k.wait(run, "idle", "ended")
}

// setting stores the machine's own settings, or a project's.
func (k *kit) setting(project, doc string) {
	k.t.Helper()
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `DELETE FROM settings WHERE project IS NOT DISTINCT FROM nullif($1, '')::uuid`, project); err != nil {
		k.t.Fatal(err)
	}
	if doc == "" {
		return
	}
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (nullif($1, '')::uuid, $2, 'test')`, project, doc); err != nil {
		k.t.Fatal(err)
	}
}

// check starts a run of role on the ticket on model, as the pipeline does.
func (k *kit) check(ticket int64, role, kind, model string) (string, error) {
	rr := RoleRun{Role: role, Kind: kind, Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true, Model: model, CLI: "agy",
		Parts: []PromptPart{{Part: "agent", Text: "Check it."}}}
	if role == "judge" {
		rr.Judge, rr.Branch, rr.ReadOnly = true, "", false
	}
	run, _, err := k.d.StartRole(context.Background(), rr)
	return run, err
}

// A reviewer, a verifier, a project's own stage's agent, and a judge on a ticket do not start on the
// family of the builder's last run, whatever the caller passes; on another family they start, and a
// tester checks nothing (safety rule 15, since 1.15).
func TestACheckDoesNotStartOnTheBuildersFamily(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	for _, c := range []struct{ role, kind string }{{"reviewer", "review"}, {"verifier", "verify"}, {"stage", "stage"}, {"judge", "judge"}} {
		_, err := k.check(ticket, c.role, c.kind, "gemini-4-argon")
		if !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "the "+c.role+" waits: gemini-4-argon is of the builder's family, gemini") {
			t.Errorf("a %s on the builder's family: %v", c.role, err)
		}
		if n := k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1 AND role = $2`, ticket, c.role); n != 0 {
			t.Errorf("a %s that did not start recorded %d subtasks", c.role, n)
		}
		run, err := k.check(ticket, c.role, c.kind, "claude-opus-5-5")
		if err != nil {
			t.Errorf("a %s on another family: %v", c.role, err)
			continue
		}
		k.wait(run, "idle", "ended")
	}
	run, err := k.check(ticket, "tester", "test", "gemini-4-argon")
	if err != nil {
		t.Fatalf("a tester: %v", err)
	}
	k.wait(run, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 0 {
		t.Fatalf("%d news of a rule not met where it was met", n)
	}
}

// A check whose builder's model is not known does not start on any family; a reviewer's analysis of a
// ticket with no builder's run, before anything is built, checks nothing built, and starts (7.6); in a
// later stage a ticket with no builder's run is one whose builder's run is missing.
func TestACheckWithNoBuildersModelDoesNotStart(t *testing.T) {
	k, ticket := guardKit(t)
	ctx := context.Background()
	for _, stage := range []string{"planning", "backlog", "analysis"} {
		k.db.Server.Exec(ctx, `UPDATE tickets SET stage = $2 WHERE id = $1`, ticket, stage)
		run, err := k.check(ticket, "reviewer", "analysis", "gemini-4-argon")
		if err != nil {
			t.Fatalf("an analysis in %s: %v", stage, err)
		}
		k.wait(run, "idle", "ended")
	}
	// A builder's subtask of any kind, with no run yet, is something of the builder's on the ticket.
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES ('ticket', $1, $2, 99, 'x', 'builder', 'queued')`,
		tshop, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := k.check(ticket, "reviewer", "analysis", "gemini-4-argon"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("an analysis with a builder's subtask on the ticket: %v", err)
	}
	k.db.Server.Exec(ctx, `DELETE FROM subtasks WHERE ticket = $1 AND number = 99`, ticket)
	for _, stage := range []string{"in_progress", "review", "verification"} {
		k.db.Server.Exec(ctx, `UPDATE tickets SET stage = $2 WHERE id = $1`, ticket, stage)
		if _, err := k.check(ticket, "reviewer", "review", "claude-opus-5-5"); !errors.Is(err, ErrCheckFamily) ||
			!strings.Contains(err.Error(), "the builder's model is not known") {
			t.Fatalf("a check in %s with no builder's run: %v", stage, err)
		}
	}
	// The ticket's stage that cannot be read starts nothing.
	cfg := k.db.Server.Config()
	cfg.ConnConfig.Tracer = cancelOn{marker: "FROM tickets t WHERE t.id = $1", seen: &atomic.Int32{}}
	failing, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer failing.Close()
	d := k.driver()
	d.Pool = failing
	if _, _, err := d.StartRole(ctx, RoleRun{Role: "reviewer", Kind: "analysis", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		CLI: "agy", Model: "gemini-4-argon", Parts: []PromptPart{{Part: "agent", Text: "Check it."}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stage that cannot be read: %v", err)
	}
	k.db.Server.Exec(ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, ticket)
	k.built(ticket, "")
	for _, model := range []string{"gemini-4-argon", "claude-opus-5-5"} {
		if _, err := k.check(ticket, "reviewer", "review", model); !errors.Is(err, ErrCheckFamily) ||
			!strings.Contains(err.Error(), "the reviewer waits: the builder's model is not known") {
			t.Errorf("a review on %s of a build with no model: %v", model, err)
		}
	}
}

// With the rule for checks off for the project, a check runs on the builder's family; with a list that
// offers no other family it runs there too, and the news says so with the run; with another family
// offered it does not start (12.3).
func TestACheckOnTheBuildersFamilyRunsOnlyWhenTheRuleIsOffOrCannotBeMet(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "claude-sonnet-5-5")
	k.setting(tshop, `{"agents": {"checks_use_another_family": false}}`)
	run, err := k.check(ticket, "reviewer", "review", "claude-opus-5-5")
	if err != nil {
		t.Fatalf("the rule off: %v", err)
	}
	k.wait(run, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 0 {
		t.Fatalf("the rule off sent %d news of it not met", n)
	}
	k.setting(tshop, "")
	if _, err := k.check(ticket, "reviewer", "review", "claude-opus-5-5"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("the rule on, another family offered: %v", err)
	}
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/claude-opus-5-5", "agy/claude-sonnet-5-5"]}}}}`)
	run, err = k.check(ticket, "stage", "stage", "claude-opus-5-5")
	if err != nil {
		t.Fatalf("no other family offered: %v", err)
	}
	k.wait(run, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met' AND run = $1 AND ticket = $2 AND project = $3
		AND detail->>'family' = 'claude' AND detail->>'role' = 'stage'`, run, ticket, tshop); n != 1 {
		t.Fatalf("the news of the rule not met: %d", n)
	}
}

// An eval's reviewer keeps off the family of its eval run's builder, and waits with none; an eval
// case's fit check keeps its judge off the family the project's builder is chosen on (9.6, 12.7).
func TestAnEvalsChecksKeepOffTheirBuildersFamily(t *testing.T) {
	k := newKit(t)
	modelstest.Recorded(t, k.db.Server)
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	evalOn(t, k, "eval/8", "b45e0001")
	review := func(model string) (string, error) {
		run, _, err := k.d.StartRole(context.Background(), RoleRun{Role: "reviewer", Kind: "eval_review", Project: fixture, Branch: "eval/8", ReadOnly: true,
			Eval: true, CLI: "agy", Model: model, Parts: []PromptPart{{Part: "agent", Text: "Review it."}}})
		return run, err
	}
	if _, err := review("claude-opus-5-5"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("an eval's reviewer with no builder's run: %v", err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	build, _ := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: fixture, Branch: "eval/8", Eval: true, Model: "gemini-3.8-flash"})
	k.wait(build, "working")
	k.d.Stop(context.Background(), build)
	k.wait(build, "idle", "ended")
	if _, err := k.d.Pool.Exec(context.Background(), `UPDATE eval_runs SET builder_run = $1 WHERE branch = 'eval/8'`, build); err != nil {
		t.Fatal(err)
	}
	if _, err := review("gemini-4-argon"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("an eval's reviewer on its builder's family: %v", err)
	}
	run, err := review("claude-opus-5-5")
	if err != nil {
		t.Fatalf("an eval's reviewer on another family: %v", err)
	}
	k.wait(run, "idle", "ended")
	// No other family offered: it runs on its builder's, and an eval's run sends no news, as before.
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/gemini-4-argon"]}}}}`)
	run, err = review("gemini-4-argon")
	if err != nil {
		t.Fatalf("an eval's reviewer whose rule cannot be met: %v", err)
	}
	k.wait(run, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 0 {
		t.Fatalf("an eval's run sent %d news", n)
	}
	k.setting("", "")
	// The project's builder is chosen on gemini-4-argon, the first of its list.
	ctx := context.Background()
	fit := func(model string) (string, int64, error) {
		return k.d.StartRole(ctx, RoleRun{Role: "judge", Kind: "eval_fit", Project: fixture, Judge: true, Eval: true, CLI: "agy",
			Model: model, Parts: []PromptPart{{Part: "agent", Text: "Judge it."}}})
	}
	if _, _, err := fit("gemini-3.8-flash"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a fit check's judge on the builder's family: %v", err)
	}
	judge, sub, err := fit("claude-sonnet-5-5")
	if err != nil {
		t.Fatalf("a fit check's judge on another family: %v", err)
	}
	k.wait(judge, "idle", "ended")
	// Crew's move of the judge keeps off the builder's family too.
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-low"}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a fit check's judge moved onto the builder's family: %v", err)
	}
	moved, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "claude-opus-5-5", Name: "claude-opus-5-5-low"})
	if err != nil {
		t.Fatalf("a fit check's judge moved onto another family: %v", err)
	}
	k.wait(moved, "idle", "ended")
	// A builder that cannot be chosen now is no family to keep off: the judge waits.
	k.setting("", `{"agents": {"roles": {"builder": {"model": ["agy/no-such-model"]}}}}`)
	if _, _, err := fit("claude-sonnet-5-5"); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "the builder's model is not known") {
		t.Fatalf("a fit check whose builder cannot be chosen: %v", err)
	}
}

// Crew's own move of a check at a limit keeps off the builder's family as its start does; the
// operator's own continue onto a model is the operator's; and a move whose rule cannot be met sends
// the news with the run it starts (12.7).
func TestCrewsMoveOfACheckKeepsOffTheBuildersFamily(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	ctx := context.Background()
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "claude-opus-5-5"})
	k.wait(run, "working")
	k.d.Stop(ctx, run)
	k.wait(run, "idle", "ended")
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "gemini-4-argon", Name: "gemini-4-argon-high"}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a move onto the builder's family: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub); n != 1 {
		t.Fatalf("a refused move recorded a run: %d", n)
	}
	// A move on another family goes on.
	next, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "claude-sonnet-5-5", Name: "claude-sonnet-5-5-high"})
	if err != nil {
		t.Fatalf("a move onto another family: %v", err)
	}
	k.wait(next, "idle", "ended")
	// The operator's own continue onto the builder's family is moved by the rule as a pin is (12.3).
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Model: "gemini-4-argon"}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("the operator's own continue onto the builder's family: %v", err)
	}
	// A move that changes only the name, which is what its CLI runs, in a new microVM: an idle run on its
	// own model continues in its own, where its CLI keeps its name.
	k.d.Stop(ctx, next)
	k.wait(next, "ended")
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "claude-sonnet-5-5", Name: "gemini-4-argon-high"}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a move onto a name of the builder's family: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub); n != 2 {
		t.Fatalf("refused continues recorded runs: %d", n)
	}
	// A monitor tick whose move is refused by the guard does not log a Warn on every tick (register row 66).
	var log lockedLog
	k.d.Log = logTo(&log)
	if _, err := k.d.Pool.Exec(ctx, `UPDATE subtasks SET state = 'waiting', wait_for = 'quota:agy' WHERE id = $1`, sub); err != nil {
		t.Fatal(err)
	}
	k.d.SetNext(func(context.Context, int64) (*ContinueRequest, error) {
		return &ContinueRequest{CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high"}, nil
	})
	k.d.Tick(ctx)
	k.d.SetNext(nil)
	if strings.Contains(log.String(), "did not move to its next choice") {
		t.Fatalf("guard refusal logged at Warn on Tick:\n%s", log.String())
	}

	// No other family offered: the move runs on the builder's, with its news.
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/gemini-4-argon", "agy/gemini-3.8-flash"]}}}}`)
	moved, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-high"})
	if err != nil {
		t.Fatalf("a move whose rule cannot be met: %v", err)
	}
	k.wait(moved, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met' AND run = $1 AND ticket = $2 AND detail->>'family' = 'gemini'
		AND detail->>'role' = 'reviewer'`, moved, ticket); n != 1 {
		t.Fatalf("the news of the move: %d", n)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 1 {
		t.Fatalf("%d news in all", n)
	}
	// A continue on the run's own model, whose rule still cannot be met, goes on, and its news is the
	// move's only.
	k.d.Stop(ctx, moved)
	k.wait(moved, "ended")
	again, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true})
	if err != nil {
		t.Fatalf("a continue on the run's own model: %v", err)
	}
	k.wait(again, "idle", "ended")
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 1 {
		t.Fatalf("%d news after a continue on the run's own model", n)
	}
	// With another family offered again, a continue on the run's own model passes the guard too.
	k.setting("", "")
	k.d.Stop(ctx, again)
	k.wait(again, "ended")
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Auto: true}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a continue on the builder's family once another is offered: %v", err)
	}
}

// A check continued after the builder ran again, on the check's family, does not go on there: in a new
// microVM and in its own alike (the second round of the reviews of 1.15's change, finding 2).
func TestAnOldCheckContinuedAfterANewerBuildKeepsOffItsFamily(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	ctx := context.Background()
	k.play(harness.Script{Recording: agyRun, Hold: true})
	stopped, ssub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "claude-opus-5-5"})
	k.wait(stopped, "working")
	k.d.Stop(ctx, stopped)
	k.wait(stopped, "ended")
	idle, isub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "claude-opus-5-5"})
	k.wait(idle, "idle")
	k.built(ticket, "claude-sonnet-5-5")
	if _, err := k.d.Continue(ctx, ssub, ContinueRequest{}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a stopped check continued on its own model: %v", err)
	}
	if _, err := k.d.Continue(ctx, isub, ContinueRequest{Text: "Go on."}); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("an idle check continued in its own microVM: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE subtask IN ($1, $2)`, ssub, isub); n != 2 {
		t.Fatalf("refused continues recorded runs: %d", n)
	}
}

// A run of a check kind names what its kind does, and its ticket is its project's: a fit check that
// names a ticket, a ticket's check that is an eval's, and a review of another project's ticket do not
// start (the second round of the security review of 1.15's change, finding 1).
func TestACheckNamesWhatItsKindDoes(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	ctx := context.Background()
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	for _, c := range []struct {
		name string
		rr   RoleRun
	}{
		{"a fit check that names a ticket", RoleRun{Role: "judge", Kind: "eval_fit", Project: tshop, Ticket: ticket, Judge: true, Eval: true}},
		{"a review that is an eval's", RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true, Eval: true}},
		{"a review with no ticket", RoleRun{Role: "reviewer", Kind: "review", Project: fixture, Judge: true, Eval: true}},
		{"an eval's review with no branch", RoleRun{Role: "reviewer", Kind: "eval_review", Project: fixture, Judge: true, Eval: true}},
		{"a judge of a ticket that is an eval's", RoleRun{Role: "judge", Kind: "judge", Project: tshop, Ticket: ticket, Judge: true, Eval: true}},
	} {
		c.rr.CLI, c.rr.Model, c.rr.Parts = "agy", "claude-opus-5-5", []PromptPart{{Part: "agent", Text: "Check it."}}
		if _, _, err := k.d.StartRole(ctx, c.rr); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "names what another kind does") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	var other int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'Other', 'feature', 'review', 'plan', 'lead', 1) RETURNING id`, fixture).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := k.check(other, "reviewer", "review", "claude-opus-5-5"); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "its ticket is not of its project") {
		t.Fatalf("a review of another project's ticket: %v", err)
	}
}

// The builder's last run is a part or a join by the builder too, as a check's start reads it in the
// pipeline (12.3).
func TestTheGuardReadsTheBuildersLastPart(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	part, _ := k.startRole(RoleRun{Role: "builder", Kind: "part", Project: tshop, Ticket: ticket, Branch: "sub/SF-1.2", Base: "refs/heads/task/SF-1-lock",
		Model: "claude-sonnet-5-5"})
	k.wait(part, "working")
	k.d.Stop(context.Background(), part)
	k.wait(part, "ended")
	if _, err := k.check(ticket, "reviewer", "review", "claude-opus-5-5"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a review on the family of the builder's part: %v", err)
	}
	run, err := k.check(ticket, "reviewer", "review", "gemini-4-argon")
	if err != nil {
		t.Fatalf("a review on the family of the build before the part: %v", err)
	}
	k.wait(run, "idle", "ended")
}

// cancelOn is a query tracer that ends, before it is sent, the first query whose text holds marker: the
// guard's, which comes before every other read of the same text in a start or a move.
type cancelOn struct {
	marker string
	seen   *atomic.Int32
}

func (c cancelOn) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, c.marker) && c.seen.Add(1) == 1 {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}

func (cancelOn) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A guard whose read fails starts nothing, and the error is not a wait of the rule's (BUG-012's rule).
func TestACheckWhoseGuardCannotReadStartsNothing(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "claude-sonnet-5-5")
	ctx := context.Background()
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "gemini-4-argon"})
	k.wait(run, "working")
	k.d.Stop(ctx, run)
	k.wait(run, "idle", "ended")
	for _, marker := range []string{"FROM settings WHERE project IS NULL", "SELECT name, key FROM projects WHERE id::text", "FROM settings WHERE project = $1",
		"AND s.role = 'builder'))", "FROM models ORDER BY cli, model, name", "SELECT role, kind, scope, coalesce(project::text, '')"} {
		cfg := k.db.Server.Config()
		cfg.ConnConfig.Tracer = cancelOn{marker: marker, seen: &atomic.Int32{}}
		failing, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		d := k.driver()
		d.Pool = failing
		before := k.count(`SELECT count(*) FROM runs`)
		// The same family, so that the test of the list is read too; a move reads its subtask first.
		if marker == "SELECT role, kind, scope, coalesce(project::text, '')" {
			_, err = d.Continue(ctx, sub, ContinueRequest{Auto: true, Model: "claude-opus-5-5", Name: "claude-opus-5-5-high"})
		} else {
			_, _, err = d.StartRole(ctx, RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
				CLI: "agy", Model: "claude-opus-5-5", Parts: []PromptPart{{Part: "agent", Text: "Check it."}}})
		}
		failing.Close()
		if err == nil || errors.Is(err, ErrCheckFamily) || !errors.Is(err, context.Canceled) {
			t.Errorf("%s fails: %v", marker, err)
		}
		if n := k.count(`SELECT count(*) FROM runs`); n != before {
			t.Errorf("%s fails: %d runs recorded", marker, n-before)
		}
	}
}

// A check on the builder's family whose news of the rule not met cannot be recorded starts nothing, at
// its start and at a move: the news is recorded with the run or not at all (since 1.15).
func TestARuleNotMetWhoseNewsCannotBeRecordedStartsNothing(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "claude-sonnet-5-5")
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/claude-opus-5-5", "agy/claude-sonnet-5-5"]}}}}`)
	ctx := context.Background()
	failing := func() *Driver {
		cfg := k.db.Server.Config()
		cfg.ConnConfig.Tracer = cancelOn{marker: "INSERT INTO signals", seen: &atomic.Int32{}}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		d := k.driver()
		d.Pool = pool
		return d
	}
	_, _, err := failing().StartRole(ctx, RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		CLI: "agy", Model: "claude-opus-5-5", Parts: []PromptPart{{Part: "agent", Text: "Check it."}}})
	if !errors.Is(err, context.Canceled) || k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1 AND role = 'reviewer'`, ticket) != 0 {
		t.Fatalf("a start whose news failed: %v", err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "claude-opus-5-5"})
	k.wait(run, "working")
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
	if _, err := failing().Continue(ctx, sub, ContinueRequest{Auto: true, Model: "claude-sonnet-5-5", Name: "claude-sonnet-5-5-high"}); !errors.Is(err, context.Canceled) ||
		k.count(`SELECT count(*) FROM runs WHERE subtask = $1`, sub) != 1 {
		t.Fatalf("a move whose news failed: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 1 {
		t.Fatalf("%d news, not the one of the start that went on", n)
	}
}

// A check is judged by what its CLI runs: the name it is given, which a caller can set apart from the
// model, and a check that names no model does not start (the review and the security review of 1.15's
// change, finding 1).
func TestACheckIsJudgedByWhatItRuns(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	ctx := context.Background()
	start := func(model, name string) (string, error) {
		run, _, err := k.d.StartRole(ctx, RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
			CLI: "agy", Model: model, Name: name, Parts: []PromptPart{{Part: "agent", Text: "Check it."}}})
		return run, err
	}
	for _, name := range []string{"gemini-4-argon-high", "Gemini 4 High"} {
		if _, err := start("claude-opus-5-5", name); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "its name "+name+" is not of its model's family, claude") {
			t.Fatalf("a name %q of another family than its model: %v", name, err)
		}
	}
	if _, err := start("gemini-4-argon", "gemini-4-argon-high"); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "gemini-4-argon-high is of the builder's family") {
		t.Fatalf("a model and its name of the builder's family: %v", err)
	}
	if _, err := start("", ""); !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "the reviewer waits: it names no model") {
		t.Fatalf("no model: %v", err)
	}
	if _, err := start("", "claude-opus-5-5-high"); !errors.Is(err, ErrCheckFamily) {
		t.Fatalf("a name with no model: %v", err)
	}
	run, err := start("claude-opus-5-5", "claude-opus-5-5-high")
	if err != nil {
		t.Fatalf("a model and its name of another family: %v", err)
	}
	k.wait(run, "idle", "ended")
}

// A run of a kind that checks is a check whatever role its caller names, with its role's list; and a
// check that names no ticket and no eval has no builder it could keep off, and does not start (the
// security review of 1.15's change, findings 2 and 5).
func TestARunOfACheckKindIsACheckWhateverItsRole(t *testing.T) {
	k, ticket := guardKit(t)
	k.built(ticket, "gemini-3.8-flash")
	ctx := context.Background()
	for _, kind := range []string{"review", "verify", "stage", "judge"} {
		if _, err := k.check(ticket, "tester", kind, "gemini-4-argon"); !errors.Is(err, ErrCheckFamily) {
			t.Errorf("a tester's run of kind %s on the builder's family: %v", kind, err)
		}
	}
	// And a role that checks is a check whatever kind it names.
	for _, role := range []string{"reviewer", "verifier", "stage", "judge"} {
		if _, err := k.check(ticket, role, "x", "gemini-4-argon"); !errors.Is(err, ErrCheckFamily) {
			t.Errorf("a %s's run of another kind on the builder's family: %v", role, err)
		}
	}
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	for _, c := range []struct{ role, kind string }{{"judge", "eval_fit"}, {"lead", "eval_fit"}, {"lead", "eval_review"}, {"reviewer", "x"}} {
		_, _, err := k.d.StartRole(ctx, RoleRun{Role: c.role, Kind: c.kind, Project: fixture, Judge: true, CLI: "agy", Model: "claude-sonnet-5-5",
			Parts: []PromptPart{{Part: "agent", Text: "Judge it."}}})
		if !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "it names no ticket and no eval") {
			t.Errorf("a %s of kind %s with no ticket and no eval: %v", c.role, c.kind, err)
		}
	}
	// With the rule off, it is no check of the rule's.
	k.setting(fixture, `{"agents": {"checks_use_another_family": false}}`)
	run, _, err := k.d.StartRole(ctx, RoleRun{Role: "judge", Kind: "eval_fit", Project: fixture, Judge: true, CLI: "agy", Model: "claude-sonnet-5-5",
		Parts: []PromptPart{{Part: "agent", Text: "Judge it."}}})
	if err != nil {
		t.Fatalf("the rule off: %v", err)
	}
	k.wait(run, "idle", "ended")
}

// A check whose own model, or whose builder's model, is of a family Crew does not know waits with
// ErrCheckFamily naming the model, before RuleCannotBeMet can let it run; a builder on an unknown
// family, a check on a known family other than its builder's, and a check with the rule off for the
// project run (12.3, safety rule 15, register row 17).
func TestACheckOnAFamilyCrewDoesNotKnowWaits(t *testing.T) {
	k, ticket := guardKit(t)
	ctx := context.Background()
	k.built(ticket, "claude-opus-5-5")
	// Even with no other family on the role's list, an unknown family waits before RuleCannotBeMet.
	k.setting("", `{"agents": {"roles": {"reviewer": {"model": ["agy/claude-opus-5-5"]}, "verifier": {"model": ["agy/claude-opus-5-5"]}, "judge": {"model": ["agy/claude-opus-5-5"]}}}}`)
	for _, c := range []struct{ role, kind string }{{"reviewer", "review"}, {"verifier", "verify"}, {"stage", "stage"}, {"judge", "judge"}} {
		_, err := k.check(ticket, c.role, c.kind, "nova-1")
		if !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "nova-1") || !strings.Contains(err.Error(), cli.UnknownFamily) {
			t.Fatalf("a %s on nova-1 for a builder on claude-opus-5-5: %v", c.role, err)
		}
		if n := k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1 AND role = $2`, ticket, c.role); n != 0 {
			t.Fatalf("a %s on nova-1 recorded %d subtasks", c.role, n)
		}
	}
	// A builder on nova-1 runs, and a check of its work on gemini-4-argon waits with ErrCheckFamily naming nova-1.
	k.built(ticket, "nova-1")
	for _, c := range []struct{ role, kind string }{{"reviewer", "review"}, {"verifier", "verify"}, {"stage", "stage"}, {"judge", "judge"}} {
		_, err := k.check(ticket, c.role, c.kind, "gemini-4-argon")
		if !errors.Is(err, ErrCheckFamily) || !strings.Contains(err.Error(), "nova-1") || !strings.Contains(err.Error(), cli.UnknownFamily) {
			t.Fatalf("a %s on gemini-4-argon for a builder on nova-1: %v", c.role, err)
		}
		if n := k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1 AND role = $2`, ticket, c.role); n != 0 {
			t.Fatalf("a %s for a builder on nova-1 recorded %d subtasks", c.role, n)
		}
	}
	k.setting("", "")
	// An eval's review and fit check wait on an unknown family too.
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	evalOn(t, k, "eval/9", "b45e0001")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	eb, _ := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: fixture, Branch: "eval/9", Eval: true, Model: "nova-1"})
	k.wait(eb, "working")
	k.d.Stop(ctx, eb)
	k.wait(eb, "idle", "ended")
	if _, err := k.d.Pool.Exec(ctx, `UPDATE eval_runs SET builder_run = $1 WHERE branch = 'eval/9'`, eb); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.d.StartRole(ctx, RoleRun{Role: "reviewer", Kind: "eval_review", Project: fixture, Branch: "eval/9", ReadOnly: true,
		Eval: true, CLI: "agy", Model: "gemini-4-argon", Parts: []PromptPart{{Part: "agent", Text: "Review it."}}}); !errors.Is(err, ErrCheckFamily) ||
		!strings.Contains(err.Error(), "nova-1") {
		t.Fatalf("an eval's reviewer for a builder on nova-1: %v", err)
	}
	if _, _, err := k.d.StartRole(ctx, RoleRun{Role: "judge", Kind: "eval_fit", Project: fixture, Judge: true, Eval: true, CLI: "agy",
		Model: "nova-1", Parts: []PromptPart{{Part: "agent", Text: "Judge it."}}}); !errors.Is(err, ErrCheckFamily) ||
		!strings.Contains(err.Error(), "nova-1") {
		t.Fatalf("a fit check's judge on nova-1: %v", err)
	}
	// With the rule for checks off for the project, the reviewer on nova-1 runs.
	k.setting(tshop, `{"agents": {"checks_use_another_family": false}}`)
	run, err := k.check(ticket, "reviewer", "review", "nova-1")
	if err != nil {
		t.Fatalf("the rule off, reviewer on nova-1: %v", err)
	}
	k.wait(run, "idle", "ended")
	// With the rule on and the builder on gemini-4-argon, a reviewer on claude-opus-5-5 runs.
	k.setting(tshop, "")
	k.built(ticket, "gemini-4-argon")
	run, err = k.check(ticket, "reviewer", "review", "claude-opus-5-5")
	if err != nil {
		t.Fatalf("reviewer on claude-opus-5-5 for builder on gemini-4-argon: %v", err)
	}
	k.wait(run, "idle", "ended")
}
