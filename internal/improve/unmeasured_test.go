package improve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/measure"
)

// An improvement whose change measured nothing on a case, in one of its projects, does not pass: its
// projects' sides are read as one with their unmeasured runs, which counted as goals not met make it
// worse, so it waits in testing (10.5, 13.9; since 1.10). Read as before, by the measured runs alone,
// it was not worse and passed.
func TestAnImprovementWhoseChangeMeasuredNothingDoesNotPass(t *testing.T) {
	k := newKit(t)
	a := k.project("alpha", "AL")
	b := k.project("beta", "BE")
	patch := map[string]string{"baseline/stages/in_progress.md": "# In progress\n\nRun the ticket's checks before you ask for a move.\n"}
	x, err := k.s.Propose(k.ctx, "", Proposal{Title: "Builders run the checks first", Patch: patch, Measure: "first_request_pass_rate"})
	if err != nil || x.Status != Testing {
		t.Fatalf("%+v %v", x, err)
	}
	for _, p := range []*board.Project{a, b} {
		for i := 1; i <= 6; i++ {
			k.exec(`INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by) VALUES ($1, $2, 't', 'operator', 'b', 'w', 'w', 'g', 'operator')`, p.ID, i)
		}
	}
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := k.d.Server.Query(k.ctx, `SELECT id, project::text FROM eval_suites WHERE improvement = $1 ORDER BY id`, x.ID)
	type su struct {
		id      int64
		project string
	}
	var list []su
	for rows.Next() {
		var s su
		rows.Scan(&s.id, &s.project)
		list = append(list, s)
	}
	rows.Close()
	if len(list) != 2 {
		t.Fatalf("%d suites", len(list))
	}
	// Six cases each, all passed before; the change passed eleven and measured nothing on beta's sixth.
	for i, s := range list {
		k.exec(`UPDATE eval_suites SET state = 'done' WHERE id = $1`, s.id)
		base, change := measure.Side{Runs: map[int64][]bool{}}, measure.Side{Runs: map[int64][]bool{}}
		for c := 1; c <= 6; c++ {
			id := int64(i*10 + c)
			base.Runs[id] = []bool{true}
			if i == 1 && c == 6 {
				change.Unmeasured = map[int64]int{id: 1}
				continue
			}
			change.Runs[id] = []bool{true}
		}
		if err := k.s.RecordEvals(k.ctx, x.ID, s.project, s.id, base, change); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	x, _ = k.s.Get(k.ctx, x.Number)
	var ev map[string]any
	json.Unmarshal(x.Evals, &ev)
	why, _ := ev["why"].(string)
	if x.Status != Testing || x.WaitsFor == nil || *x.WaitsFor != WaitsCases || ev["verdict"] != measure.TooFew || ev["unmeasuredRuns"] != float64(1) ||
		!strings.HasPrefix(why, "1 runs of the change measured nothing, and counted as goals not met it would be worse: ") {
		t.Fatalf("the improvement %+v, its evals %v", x, ev)
	}
}
