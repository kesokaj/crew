package versions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/measure"
)

// A project whose release suite could not tell because runs of the release measured nothing holds the
// release, as a worse one does, though another project is not worse: such runs may be the candidate's
// own way to leave out the cases it fails (16.2 step 5, since 1.10). Read as before, it was a line
// "not compared" that held nothing, and the step passed. A project too few to tell for its cases holds
// nothing, as before.
func TestTheGatesEvalSuitesHoldAReleaseThatCouldNotTell(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	g := &LiveRunner{Pool: d.Server}
	suite := func(project, name string, dec measure.Decision) suiteEntry {
		t.Helper()
		if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, $2, upper(left($2, 2)), 'running', 'local')`, project, name); err != nil {
			t.Fatal(err)
		}
		res, _ := json.Marshal(map[string]any{"decision": dec})
		var id int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, result)
			VALUES ($1, 'on_demand', 1, 10, 'done', 'gate', $2) RETURNING id`, project, res).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return suiteEntry{Project: project, Name: name, Suite: id}
	}
	entries := []suiteEntry{
		suite("00000000-0000-4000-8000-000000000001", "shop", measure.Decision{Verdict: measure.NotWorseVerdict, Why: "within"}),
		suite("00000000-0000-4000-8000-000000000002", "cart", measure.Decision{Verdict: measure.TooFew, UnmeasuredRuns: 2,
			Why: "2 runs of the change measured nothing, and counted as goals not met it would be worse: x"}),
		suite("00000000-0000-4000-8000-000000000003", "tiny", measure.Decision{Verdict: measure.TooFew, Why: "3 cases, and at least 10 are needed to tell"}),
	}
	res, err := g.endSuites(ctx, "0.3.0", entries, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Step.OK || res.Skipped != "" ||
		res.Hold != "the eval suites could not tell in 1 projects: runs of the release measured nothing; the numbers are on Versions" ||
		!strings.Contains(res.Step.Detail, "cart: could not tell: 2 runs of the change measured nothing") ||
		!strings.Contains(res.Step.Detail, "tiny: not compared: 3 cases") || strings.Contains(res.Hold, "cart") {
		t.Fatalf("%+v", res)
	}
	// With a worse one too, the hold says both.
	entries = append(entries, suite("00000000-0000-4000-8000-000000000004", "bank", measure.Decision{Verdict: measure.WorseVerdict, Why: "past"}))
	if res, err = g.endSuites(ctx, "0.3.0", entries, ""); err != nil || res.Hold != "the eval suites measured worse in 1 of 2 compared projects; the numbers are on Versions; "+
		"the eval suites could not tell in 1 projects: runs of the release measured nothing; the numbers are on Versions" {
		t.Fatalf("%+v %v", res, err)
	}
}
