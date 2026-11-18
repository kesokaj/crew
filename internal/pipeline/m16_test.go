package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// stopRuns is a run driver that records what it is asked to stop.
type stopRuns struct {
	Runs
	mu      sync.Mutex
	stopped []string
}

func (s *stopRuns) Stop(_ context.Context, run string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, run)
	return nil
}

// An eval run that ends stops its runs that still go (9.6, since 0.51): one mid-turn, and one its
// subtask went on in after it waited at a limit, which the eval run does not name. A run that ended is
// left as it is.
func TestAnEndedEvalStopsItsRuns(t *testing.T) {
	ctx := context.Background()
	p, tools, er := evalKit(t, "building", "quota")
	driver := &stopRuns{}
	p.Runs = driver
	var builder string
	var sub int64
	tools.Pool.QueryRow(ctx, `SELECT r.id::text, r.subtask FROM eval_runs e JOIN runs r ON r.id = e.builder_run WHERE e.id = $1`, er).Scan(&builder, &sub)
	// The subtask went on at its next choice, in a run of its own that works now.
	var next string
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO runs (id, subtask, number, cli, crew_version, image, state) VALUES (gen_random_uuid(), $1, 2, 'agy', 'test', 'img', 'working')
		RETURNING id::text`, sub).Scan(&next); err != nil {
		t.Fatal(err)
	}
	var proj string
	tools.Pool.QueryRow(ctx, `SELECT project::text FROM eval_runs WHERE id = $1`, er).Scan(&proj)
	pr, err := tools.Board.Project(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	e := &evalRun{id: er, builder: builder, detail: map[string]any{"runs": []string{builder}}}
	if err := p.endEval(ctx, &job{proj: pr}, e, evalFailed, nil, "the builder's run ended for Crew's own reason: quota"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(driver.stopped, []string{next}) {
		t.Fatalf("stopped %v, not the run that works, %s", driver.stopped, next)
	}
}

// The fit check's answer is read in its form only (9.6): NOTHING MISSING, or MISSING: lines; anything
// else is no list, and what is kept is redacted and cut.
func TestAFitChecksAnswer(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   []string
		bad    bool
	}{
		{"NOTHING MISSING", []string{}, false},
		{"```\nNOTHING MISSING.\n```", []string{}, false},
		{"MISSING: the type Counts\n\nMISSING: the constant bufferSize", []string{"the type Counts", "the constant bufferSize"}, false},
		// A token's shape, made here so that no file holds one.
		{"MISSING: token: " + "gh" + "p_" + strings.Repeat("a1", 18), []string{"[redacted: " + "gh" + "p_]"}, false},
		{"The case is fine.", nil, true},
		{"MISSING: Counts\nAnd also the constant.", nil, true},
		{"MISSING:", nil, true},
		{"", nil, true},
		{strings.Repeat("MISSING: x\n", maxFitLines+1), nil, true},
	} {
		got, err := fitList(tc.answer)
		if (err != nil) != tc.bad || (!tc.bad && !slices.Equal(got, tc.want)) {
			t.Errorf("%q: %q, %v", tc.answer, got, err)
		}
	}
	long, err := fitList("MISSING: " + strings.Repeat("é", maxFitLineSize+10))
	if err != nil || len([]rune(long[0])) != maxFitLineSize+1 {
		t.Fatalf("a long line: %v", err)
	}
}

// What is kept of a failed held-out check is its end, from a whole character.
func TestLastBytes(t *testing.T) {
	if got := lastBytes("abc", 5); got != "abc" {
		t.Fatal(got)
	}
	if got := lastBytes("aéb", 2); got != "b" {
		t.Fatalf("%q", got)
	}
	if got := lastBytes("aéb", 3); got != "éb" {
		t.Fatalf("%q", got)
	}
}

// What is kept of a check's output is text a column takes: no NUL, and no rune cut in two.
func TestStorable(t *testing.T) {
	if got := storable("a\x00b\xe2\x82c"); got != "ab�c" {
		t.Fatalf("%q", got)
	}
}

// What an eval's next agent run waits for is kept by its kind, which a release's gate reads (16.2 step
// 5, since 0.52): a model at its limit is a limit; a sign-in, a pause of everything, and room are not.
func TestAGateEvalSuiteWaitKinds(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&models.WaitError{Reason: "the builder waits for agy/gemini: its limit"}, "model"},
		{fmt.Errorf("starting: %w", runs.ErrNotSignedIn), "login"},
		{runs.ErrHeld, "held"},
		{runs.ErrNoRoom, "room"},
		{errors.New("something else"), ""},
		{nil, ""},
	} {
		if got := waitKind(tc.err); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got, tc.want)
		}
	}
}

// releaseSuite records a done on_demand suite of project and a queued release suite of gate against
// it, and returns the release suite.
func releaseSuite(t *testing.T, tools *Tools, project, gate string) int64 {
	t.Helper()
	ctx := context.Background()
	var earlier, id int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by)
		VALUES ($1, 'on_demand', 1, 10, 'done', 'operator') RETURNING id`, project).Scan(&earlier); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by, gate, image, compare_to, layer)
		VALUES ($1, 'release', 1, 10, 'gate', $2, $3, $4, '{}') RETURNING id`, project, gate, "sha256:"+strings.Repeat("e", 64), earlier).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A release suite waits behind the operator's own: the machine's one suite at a time takes a queued
// suite of the operator's first, though the release suite was queued before it (16.2 step 5, since
// 0.52).
func TestAGateEvalSuiteComesAfterTheOperatorsSuites(t *testing.T) {
	ctx := context.Background()
	tools, proj, _ := toolsKit(t)
	p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Version: "1.2.0"}
	tools.Pool.Exec(ctx, `UPDATE projects SET state = 'running' WHERE id = $1`, proj.ID)
	release := releaseSuite(t, tools, proj.ID, "0.3.0")
	var other string
	var operator int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'second', 'SE', 'running', 'local') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by) VALUES ($1, 'on_demand', 1, 10, 'operator') RETURNING id`,
		other).Scan(&operator); err != nil {
		t.Fatal(err)
	}
	if release > operator {
		t.Fatal("the release suite was not queued first")
	}
	s, err := p.currentSuite(ctx)
	if err != nil || s == nil || s.id != operator {
		t.Fatalf("the suite taken: %+v %v, want the operator's %d", s, err, operator)
	}
}

// A release suite runs only while its gate runs it: a gate that is not live, whose suites step ended,
// or whose list names the suite's project with another suite, or not at all, has its suite stopped,
// queued or running; one its live gate names is left, and so is one whose project its gate names with
// no suite yet, as between the suite's start and its record (16.2 step 5, since 0.52; review of P4d).
func TestAGateEvalSuiteWhoseGateEndedIsStopped(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, gate string
		stopped    bool
	}{
		{"no gate", ``, true},
		{"a gate that is done", `INSERT INTO gates (version, state, suites) VALUES ('0.3.0', 'done', jsonb_build_array(jsonb_build_object('project', $2::text, 'suite', $1::bigint)))`, true},
		{"a gate whose step ended", `INSERT INTO gates (version, state, suites, suites_step) VALUES ('0.3.0', 'live', jsonb_build_array(jsonb_build_object('project', $2::text, 'suite', $1::bigint)), '{}')`, true},
		{"a gate that names another suite", `INSERT INTO gates (version, state, suites) VALUES ('0.3.0', 'live', jsonb_build_array(jsonb_build_object('project', $2::text, 'suite', $1::bigint + 100)))`, true},
		{"a gate that names another project", `INSERT INTO gates (version, state, suites) VALUES ('0.3.0', 'live', jsonb_build_array(jsonb_build_object('project', 'other-' || $2::text, 'suite', $1::bigint)))`, true},
		{"a gate that runs it", `INSERT INTO gates (version, state, suites) VALUES ('0.3.0', 'live', jsonb_build_array(jsonb_build_object('project', $2::text, 'suite', $1::bigint)))`, false},
		{"a gate that started it and has not recorded it", `INSERT INTO gates (version, state, suites) SELECT '0.3.0', 'live', jsonb_build_array(jsonb_build_object('project', $2::text)) WHERE $1::bigint > 0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, proj, _ := toolsKit(t)
			p := &Pipeline{Pool: tools.Pool, Board: tools.Board, Version: "1.2.0"}
			tools.Pool.Exec(ctx, `UPDATE projects SET state = 'running' WHERE id = $1`, proj.ID)
			id := releaseSuite(t, tools, proj.ID, "0.3.0")
			if tc.gate != "" {
				if _, err := tools.Pool.Exec(ctx, tc.gate, id, proj.ID); err != nil {
					t.Fatal(err)
				}
			}
			// The evals' pass stops the suites whose gate ended before it takes one; the one its gate
			// runs is stepped by it, so it is only left by the stop.
			stop := p.stopEndedReleaseSuites
			if tc.stopped {
				stop = func(ctx context.Context) error { return p.evals(ctx, settings.Doc{}) }
			}
			if err := stop(ctx); err != nil {
				t.Fatal(err)
			}
			var state string
			tools.Pool.QueryRow(ctx, `SELECT state FROM eval_suites WHERE id = $1`, id).Scan(&state)
			if (state == "stopped") != tc.stopped {
				t.Fatalf("the suite is %s", state)
			}
		})
	}
}

// A release's suite lays the candidate's version of every file an eval's runs read, and nothing else
// (16.2 step 5, since 0.52): the stage and agent files of the builder, the reviewer, and the tester.
func TestAGateEvalSuiteLaysEveryFileAnEvalReads(t *testing.T) {
	var read []string
	for kind, role := range map[string]string{kindEvalBuild: "builder", kindEvalReview: "reviewer", kindEvalTest: "tester"} {
		read = append(read, "stages/"+evalStage[kind]+".md", "agents/"+role+".md")
	}
	slices.Sort(read)
	files := baseline.EvalFiles()
	slices.Sort(files)
	if !slices.Equal(read, files) || len(evalStage) != 3 {
		t.Fatalf("an eval reads %v, a release's suite lays %v", read, files)
	}
}

// list_eval_results gives a project's agents its suites, and leaves out a release's: that measured a
// candidate in a gate, not the project as it runs (16.2 step 5, since 0.52; review of P4d).
func TestListEvalResultsLeavesOutAGatesSuites(t *testing.T) {
	ctx := context.Background()
	tools, proj, c := toolsKit(t)
	release := releaseSuite(t, tools, proj.ID, "0.3.0")
	out, err := tools.listEvalResults(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	suites := out.(map[string]any)["suites"].([]*board.EvalSuite)
	if len(suites) != 1 || suites[0].ID == release || suites[0].Purpose != "on_demand" {
		t.Fatalf("list_eval_results: %+v", suites)
	}
}

// list_eval_results lists a project's own suites up to its limit, however many release suites came
// after them (review of P4d, round 2).
func TestListEvalResultsLeavesOutManyGatesSuites(t *testing.T) {
	ctx := context.Background()
	tools, proj, c := toolsKit(t)
	releaseSuite(t, tools, proj.ID, "0.3.0")
	for i := 1; i <= 25; i++ {
		if _, err := tools.Pool.Exec(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by, gate, image, compare_to, layer)
			SELECT project, 'release', 1, 10, 'gate', $2, image, compare_to, '{}' FROM eval_suites WHERE purpose = 'release' AND project = $1 LIMIT 1`,
			proj.ID, fmt.Sprintf("0.4.%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tools.listEvalResults(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if suites := out.(map[string]any)["suites"].([]*board.EvalSuite); len(suites) != 1 || suites[0].Purpose != "on_demand" {
		t.Fatalf("list_eval_results: %d suites", len(suites))
	}
}
