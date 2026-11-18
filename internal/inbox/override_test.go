package inbox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

// An override in testing whose evals could not tell waits for the operator with the evals' why: too few
// cases, or runs of the change that measured nothing, which counted as goals not met would make it
// worse (9.6, since 1.10).
func TestAnOverrideWhoseEvalsCouldNotTellSaysWhy(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	const why = "1 runs of the change measured nothing, and counted as goals not met it would be worse: 50% of the runs met their goal, against 100% before, past the margin of 0 points"
	for _, sql := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('00000000-0000-4000-8000-000000000001', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO overrides (project, number, path, version, text, status, proposed_by, eval) VALUES ('00000000-0000-4000-8000-000000000001', 1,
			'stages/in_progress.md', 1, 'x', 'testing', 'lead · r_1', jsonb_build_object('verdict', 'too_few', 'why', '` + why + `'))`,
	} {
		if _, err := d.Server.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	it := itemOf(t, items, inbox.KindOverride)
	if it == nil || !strings.Contains(it.Body, "Its evals could not tell whether it is worse ("+why+"), so it waits") || strings.Contains(it.Body, "too few cases") {
		t.Fatalf("%+v", items)
	}
}
