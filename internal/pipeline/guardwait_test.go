package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// guardRuns refuses every start as the guard of the rule for checks does (internal/runs/checkguard.go).
type guardRuns struct{ recRuns }

var errGuard = fmt.Errorf("the reviewer waits: gemini-4-argon is of the builder's family, gemini: %w", runs.ErrCheckFamily)

func (r *guardRuns) StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error) {
	r.recRuns.StartRole(ctx, rr)
	return "", 0, errGuard
}

// A ticket's check that the guard every run passes refuses waits as at a limit, with the guard's
// reason on its card, and is no error of the pass (since 1.15).
func TestACheckTheGuardRefusesWaitsWithItsReason(t *testing.T) {
	k, _ := familyKit(t)
	rr := &guardRuns{}
	k.p.Runs = rr
	k.ran("build", "builder", "claude-sonnet-5-5", 1)
	if _, err := k.p.start(k.ctx, k.j, k.one, startSpec{kind: kindReview, role: "reviewer", title: "Review the change"}); err != nil || len(rr.started) != 1 {
		t.Fatalf("%v, %d runs asked", err, len(rr.started))
	}
	if want := "Review the change: " + errGuard.Error(); k.stuck() != want {
		t.Fatalf("the card: %q", k.stuck())
	}
	// The news of a rule not met is the guard's, recorded with the run it starts: the pipeline sends none.
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 0 {
		t.Fatalf("%d news from the pipeline", n)
	}
}

// An eval's reviewer that the guard refuses makes its suite wait for a model, and is no error of the
// pass (9.6, since 1.15).
func TestAnEvalsReviewerTheGuardRefusesWaitsForAModel(t *testing.T) {
	ctx := context.Background()
	p, tools, er := evalKit(t, "building", "ok")
	modelstest.Recorded(t, tools.Pool)
	tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ('agy', 'signed_in', now(), '1.0') ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`)
	rr := &guardRuns{}
	p.Runs = rr
	evalBranchAt(t, tools, er, true)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'claude-sonnet-5-5' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	s, err := p.currentSuite(ctx)
	if err != nil || s == nil {
		t.Fatalf("the suite: %v", err)
	}
	e, err := p.nextEvalRun(ctx, s.id)
	if err != nil || e == nil {
		t.Fatalf("the eval run: %v", err)
	}
	proj, err := tools.Board.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	ec, err := tools.Board.Case(ctx, tools.Pool, proj.ID, e.caseID)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := settings.Defaults(settings.Machine{Version: "test"})
	doc, err := tools.Board.Settings(ctx, tools.Pool, proj)
	if err != nil {
		t.Fatal(err)
	}
	j := &job{machine: machine, doc: doc, proj: proj, pipe: board.PipelineOf(doc)}
	run, _, err := p.startEval(ctx, j, s, e, ec, kindEvalReview, "reviewer", tools.Board.Git.Main(ctx, "shop"), "")
	if err != nil || run != "" || len(rr.started) != 1 {
		t.Fatalf("%v, run %q, %d asked", err, run, len(rr.started))
	}
	var waits string
	if err := tools.Pool.QueryRow(ctx, `SELECT coalesce(wait_for, '') FROM eval_suites WHERE id = $1`, s.id).Scan(&waits); err != nil || waits != "model" {
		t.Fatalf("the suite waits for %q: %v", waits, err)
	}
}

// An eval's reviewer at a limit moves to its next choice off its eval run's builder's family, which it
// reads as the guard every run passes does (runs.EvalBuilderModel, since 1.15): with a Claude builder,
// past Opus to Gemini 3.8 Flash.
func TestAnEvalsReviewerAtALimitMovesOffItsBuildersFamily(t *testing.T) {
	ctx := context.Background()
	p, tools, er, sub := evalAtLimit(t)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'claude-sonnet-5-5' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon', model_name = 'gemini-4-argon' WHERE subtask = $1`, sub)
	tools.Pool.Exec(ctx, `DELETE FROM quota`)
	for _, eff := range []string{"low", "medium", "high"} {
		if err := models.Refused(ctx, tools.Pool, "agy", "gemini-4-argon-"+eff); err != nil {
			t.Fatal(err)
		}
	}
	p.Settings = func(context.Context) (settings.Doc, error) {
		def, err := settings.Defaults(settings.Machine{Version: "test"})
		own := settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/claude-opus-5-5",
			"agy/gemini-3.8-flash"}}}}}
		return settings.Merge(def, own), err
	}
	req, err := p.NextChoice(ctx, sub)
	if err != nil || req == nil || req.Name != "gemini-3.8-flash-high" {
		t.Fatalf("the eval's reviewer: %+v %v", req, err)
	}
}
