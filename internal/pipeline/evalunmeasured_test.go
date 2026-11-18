package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/settings"
)

// unmeasuredCases adds two eval cases to project and returns their ids.
func unmeasuredCases(t *testing.T, tools *Tools, project string) [2]int64 {
	t.Helper()
	var ids [2]int64
	for i := range ids {
		if err := tools.Pool.QueryRow(context.Background(), `INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by)
			VALUES ($1, $2, 'c', 'operator', 'abc', 'w', 'w', 'g', 'operator') RETURNING id`, project, i+1).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// endedEvalRun records an eval run of suite that ended: done with met, or failed with no goal when met is nil.
func endedEvalRun(t *testing.T, tools *Tools, suite int64, project string, c int64, side string, met *bool) {
	t.Helper()
	state := "done"
	if met == nil {
		state = "failed"
	}
	if _, err := tools.Pool.Exec(context.Background(), `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, state, goal_met, crew_version, detail)
		VALUES ($1, $2, $3, 1, $4, $5, $6, '1.2.0', '{"why": "no verdict"}')`, suite, project, c, side, state, met); err != nil {
		t.Fatal(err)
	}
}

// An override whose change side measured nothing on a case it may have failed is not applied: counted
// as not met it would be worse, so its evals cannot tell, it stays in testing, and the signal says why
// (9.6, 13.9; since 1.10). Read as before, by the measured runs alone, it was
// not worse and applied.
func TestAnOverridesChangeThatMeasuredNothingIsNotApplied(t *testing.T) {
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Version: "1.2.0"}
	if _, err := tools.Pool.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, '{"knowledge": {"evals": {"min_cases": 1}}}', 'test')`, proj.ID); err != nil {
		t.Fatal(err)
	}
	cases := unmeasuredCases(t, tools, proj.ID)
	var o, suite int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO overrides (project, number, path, version, text, status, proposed_by) VALUES ($1, 1, 'stages/in_progress.md', 1, 'x', 'testing', 'lead')
		RETURNING id`, proj.ID).Scan(&o); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, override, repeats, budget_usd, state, asked_by, started_at)
		VALUES ($1, 'override', $2, 1, 10, 'running', 'crew', now()) RETURNING id`, proj.ID, o).Scan(&suite); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Pool.Exec(ctx, `UPDATE overrides SET suite = $2 WHERE id = $1`, o, suite); err != nil {
		t.Fatal(err)
	}
	met := true
	endedEvalRun(t, tools, suite, proj.ID, cases[0], "baseline", &met)
	endedEvalRun(t, tools, suite, proj.ID, cases[1], "baseline", &met)
	endedEvalRun(t, tools, suite, proj.ID, cases[0], "change", &met)
	endedEvalRun(t, tools, suite, proj.ID, cases[1], "change", nil)
	if err := p.evals(ctx, settings.Doc{}); err != nil {
		t.Fatal(err)
	}
	var verdict, why, status, text string
	var unmeasured int
	tools.Pool.QueryRow(ctx, `SELECT coalesce(result->'decision'->>'verdict', ''), coalesce(result->'decision'->>'why', ''),
		coalesce((result->'decision'->>'unmeasuredRuns')::int, 0) FROM eval_suites WHERE id = $1`, suite).Scan(&verdict, &why, &unmeasured)
	tools.Pool.QueryRow(ctx, `SELECT status FROM overrides WHERE id = $1`, o).Scan(&status)
	tools.Pool.QueryRow(ctx, `SELECT coalesce(detail->>'text', '') FROM signals WHERE kind = 'eval_result' AND (detail->>'suite')::bigint = $1`, suite).Scan(&text)
	const want = "1 runs of the change measured nothing, and counted as goals not met it would be worse: "
	if verdict != measure.TooFew || unmeasured != 1 || !strings.HasPrefix(why, want) || status != "testing" || !strings.Contains(text, want) {
		t.Fatalf("the decision %q %q (%d), the override %q, the signal %q", verdict, why, unmeasured, status, text)
	}
}

// A release's case that measured nothing on its side stays in the comparison with the earlier suite,
// counted as not met for the bound: here it would be lost, so the project is not compared (16.2 step
// 5, since 1.10). Read as before, the case left both sides, and the release was not worse.
func TestAReleaseSuiteThatMeasuredNothingOnACaseIsNotCompared(t *testing.T) {
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Version: "1.2.0"}
	cases := unmeasuredCases(t, tools, proj.ID)
	var earlier, release int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by)
		VALUES ($1, 'on_demand', 1, 10, 'done', 'operator') RETURNING id`, proj.ID).Scan(&earlier); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, gate, image, compare_to, layer)
		VALUES ($1, 'release', 1, 10, 'running', 'gate', '0.3.0', $2, $3, '{}') RETURNING id`, proj.ID, "sha256:"+strings.Repeat("e", 64), earlier).Scan(&release); err != nil {
		t.Fatal(err)
	}
	met := true
	endedEvalRun(t, tools, earlier, proj.ID, cases[0], "baseline", &met)
	endedEvalRun(t, tools, earlier, proj.ID, cases[1], "baseline", &met)
	endedEvalRun(t, tools, release, proj.ID, cases[0], "release", &met)
	endedEvalRun(t, tools, release, proj.ID, cases[1], "release", nil)
	j := &job{proj: proj, doc: settings.Doc{"knowledge": settings.Doc{"evals": settings.Doc{"min_cases": float64(1)}}}}
	if err := p.finishReleaseSuite(ctx, j, &evalSuite{id: release, project: proj.ID, purpose: "release", against: earlier}, ""); err != nil {
		t.Fatal(err)
	}
	var verdict string
	var unmeasured, lost, before, after int
	if err := tools.Pool.QueryRow(ctx, `SELECT result->'decision'->>'verdict', coalesce((result->'decision'->>'unmeasuredRuns')::int, 0),
		jsonb_array_length(result->'decision'->'lost'), (result->'baseline'->>'cases')::int, (result->'release'->>'cases')::int
		FROM eval_suites WHERE id = $1`, release).Scan(&verdict, &unmeasured, &lost, &before, &after); err != nil {
		t.Fatal(err)
	}
	if verdict != measure.TooFew || unmeasured != 1 || lost != 1 || before != 2 || after != 1 {
		t.Fatalf("the decision %q, %d unmeasured, %d lost, the sides %d and %d cases", verdict, unmeasured, lost, before, after)
	}
}

// An improvement's suite gives the improvement each side's unmeasured runs with its measured ones,
// which its decision reads as one across projects (10.5, step 3; since 1.10).
func TestAnImprovementsSuiteKeepsItsUnmeasuredRuns(t *testing.T) {
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Version: "1.2.0", Improve: &improve.Service{Pool: tools.Pool}}
	cases := unmeasuredCases(t, tools, proj.ID)
	var x, suite int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO improvements (number, title, status, target, patch, measure) VALUES (1, 'x', 'testing', '{}', '{}', 'fix_rounds_per_ticket')
		RETURNING id`).Scan(&x); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, improvement, repeats, budget_usd, state, asked_by, started_at)
		VALUES ($1, 'improvement', $2, 1, 10, 'running', 'crew', now()) RETURNING id`, proj.ID, x).Scan(&suite); err != nil {
		t.Fatal(err)
	}
	met := true
	endedEvalRun(t, tools, suite, proj.ID, cases[0], "baseline", &met)
	endedEvalRun(t, tools, suite, proj.ID, cases[1], "baseline", nil)
	endedEvalRun(t, tools, suite, proj.ID, cases[0], "change", &met)
	endedEvalRun(t, tools, suite, proj.ID, cases[1], "change", nil)
	if err := p.evals(ctx, settings.Doc{}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := tools.Pool.QueryRow(ctx, `SELECT result FROM improvement_evals WHERE improvement = $1`, x).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var sides map[string]measure.Side
	if err := json.Unmarshal(raw, &sides); err != nil {
		t.Fatal(err)
	}
	if b, c := sides["baseline"], sides["change"]; len(c.Runs) != 1 || c.Unmeasured[cases[1]] != 1 || len(c.Unmeasured) != 1 ||
		len(b.Runs) != 1 || b.Unmeasured[cases[1]] != 1 {
		t.Fatalf("the sides kept: %s", raw)
	}
}
