package httpapi_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/version"
	"github.com/kesokaj/crew/internal/versions"
)

// earlierSuite makes shop with one active case and runs its suite on demand on the running release,
// twice a case, every run meeting its goal: the suite a release's suite is compared with. It returns
// shop, the suite, the active cases, and the specs of the suite's runs by role.
func (k *ticketKit) earlierSuite(t *testing.T) (*board.Project, int64, []int64, map[string][]harness.RunSpec) {
	t.Helper()
	ctx := context.Background()
	k.fitShop(map[string]any{"repeats": 2, "min_cases": 1}, map[string]any{"max_fix_rounds": 1})
	specs := k.evalScript(t)
	k.call("POST", "/api/projects/shop/evals/run", "", 202, nil)
	stop := k.run()
	k.until("the earlier suite done", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE purpose = 'on_demand' AND state = 'done'`) == 1
	})
	stop()
	var earlier int64
	k.d.Server.QueryRow(ctx, `SELECT id FROM eval_suites WHERE purpose = 'on_demand'`).Scan(&earlier)
	if n := k.count(`SELECT count(*) FROM eval_runs WHERE suite = $1 AND goal_met`, earlier); n != 2 {
		t.Fatalf("the earlier suite met %d goals of 2", n)
	}
	proj, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var cases []int64
	rows, _ := k.d.Server.Query(ctx, `SELECT id FROM eval_cases WHERE project = $1 AND status = 'active' ORDER BY id`, proj.ID)
	for rows.Next() {
		var c int64
		rows.Scan(&c)
		cases = append(cases, c)
	}
	rows.Close()
	return proj, earlier, cases, specs
}

// A release's eval suite (16.2 step 5, since 0.52) runs the case once, on side release: every agent run
// of it starts from the candidate's runtime image and records it, its checks run as every eval's, and
// its prompt lays the candidate's eval files over the baseline, but for a file the project has an
// override of, which wins. Its runs record the gate's version. It is compared with the earlier suite on
// the cases both measured: the case that met its goal every time before fails now, so it is worse. It
// sends no eval_result signal. A release suite is refused for an image that is no id, a gate that is
// not live, cases none of which is active, and a project with a suite open.
func TestAGateEvalSuiteRunsTheCandidateAgainstAnEarlierSuite(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	proj, earlier, cases, earlierSpecs := k.earlierSuite(t)
	for role, list := range earlierSpecs {
		for _, s := range list {
			if s.Image != "" {
				t.Fatalf("a %s run of the earlier suite started from %q", role, s.Image)
			}
		}
	}
	img := "sha256:" + strings.Repeat("f", 64)
	layer := map[string]string{
		"stages/in_progress.md": "# In progress: build one ticket\n\nCANDIDATE-STAGE: build as the candidate says.\n",
		"agents/builder.md":     "---\nname: builder\n---\nCANDIDATE-AGENT\n",
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO overrides (project, number, path, version, text, why, status, proposed_by)
		VALUES ($1, 1, 'agents/builder.md', 1, $2, 'the operator''s own', 'applied', 'operator')`, proj.ID, "---\nname: builder\n---\nOVERRIDE-WINS\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO gates (version, state) VALUES ('0.3.0', 'live'), ('0.2.8', 'waiting')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		gate, image string
		cases       []int64
	}{{"0.3.0", "crew-runtime:0.3.0", cases}, {"0.2.9", img, cases}, {"0.2.8", img, cases}, {"0.3.0", img, []int64{999999}}} {
		if _, err := k.b.StartReleaseSuite(ctx, proj, tc.gate, tc.image, earlier, tc.cases, layer, "gate"); err == nil {
			t.Fatalf("a release suite of %s from %q on %v started", tc.gate, tc.image, tc.cases)
		}
	}
	// A suite of the operator's that waits keeps the release suite out until it ended.
	var own int64
	k.d.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by) VALUES ($1, 'on_demand', 1, 10, 'operator') RETURNING id`,
		proj.ID).Scan(&own)
	if _, err := k.b.StartReleaseSuite(ctx, proj, "0.3.0", img, earlier, cases, layer, "gate"); err == nil {
		t.Fatal("a release suite of shop started while the operator's waits")
	}
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET state = 'stopped' WHERE id = $1`, own)
	id, err := k.b.StartReleaseSuite(ctx, proj, "0.3.0", img, earlier, cases, layer, "gate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.b.StartReleaseSuite(ctx, proj, "0.3.0", img, earlier, cases, layer, "gate"); err == nil {
		t.Fatal("a second suite of shop started while one is open")
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE gates SET suites = jsonb_build_array(jsonb_build_object('project', $2::text, 'name', 'shop', 'suite', $1::bigint))`,
		id, proj.ID); err != nil {
		t.Fatal(err)
	}

	// The candidate's builder does work the case's checks fail on.
	var mu sync.Mutex
	specs := map[string]harness.RunSpec{}
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		mu.Lock()
		specs[spec.Run] = spec
		mu.Unlock()
		k.mu.Lock()
		first := k.firstPrompt(spec)
		k.mu.Unlock()
		if strings.Contains(first, "CANDIDATE-STAGE") {
			k.push(spec.Run, spec.Branch, map[string]string{"release-work.txt": "work of " + spec.Run})
			return recording(spec), true
		}
		return prev(kind, spec, p)
	}
	k.f.Check = func(spec harness.CheckSpec, cmd harness.CheckCmd) harness.CheckResult {
		if slices.Contains(tarNames(spec.Tree), "release-work.txt") {
			return harness.CheckResult{Code: 1, Output: "the candidate's work fails"}
		}
		return checkSandbox(spec, cmd)
	}
	stop := k.run()
	k.until("the release suite done", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND state = 'done'`, id) == 1
	})
	stop()

	var raw []byte
	var waitFor *string
	k.d.Server.QueryRow(ctx, `SELECT result, wait_for FROM eval_suites WHERE id = $1`, id).Scan(&raw, &waitFor)
	var res struct {
		Baseline struct {
			Cases  int
			Passed float64
		}
		Release *struct {
			Cases  int
			Passed float64
		}
		Change   any
		Against  int64
		Decision struct {
			Verdict string
			Lost    []int64
		}
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Decision.Verdict != "worse" || !slices.Equal(res.Decision.Lost, cases) || res.Against != earlier || res.Release == nil ||
		res.Release.Cases != 1 || res.Release.Passed != 0 || res.Baseline.Cases != 1 || res.Baseline.Passed != 1 || res.Change != nil || waitFor != nil {
		t.Fatalf("the result: %s, waiting for %v", raw, waitFor)
	}
	if n := k.count(`SELECT count(*) FROM eval_runs WHERE suite = $1 AND (side <> 'release' OR crew_version <> '0.3.0')`, id); n != 0 {
		t.Fatalf("%d of its runs are not the release's side on 0.3.0", n)
	}
	// The Evals tab is told which gate the suite is of.
	var evals struct {
		Suites []struct {
			ID      int64
			Purpose string
			Gate    *string
		}
	}
	k.call("GET", "/api/projects/shop/evals", "", 200, &evals)
	if len(evals.Suites) == 0 || evals.Suites[0].ID != id || evals.Suites[0].Purpose != "release" || evals.Suites[0].Gate == nil || *evals.Suites[0].Gate != "0.3.0" {
		t.Fatalf("the suites the Evals tab gets: %+v", evals.Suites)
	}

	// Every agent run of it started from the candidate's image, and its rows say so.
	rows, _ := k.d.Server.Query(ctx, `SELECT r.id::text, r.image FROM runs r WHERE r.id::text IN
		(SELECT jsonb_array_elements_text(e.detail->'runs') FROM eval_runs e WHERE e.suite = $1)`, id)
	n := 0
	for rows.Next() {
		var run, image string
		rows.Scan(&run, &image)
		mu.Lock()
		spec := specs[run]
		mu.Unlock()
		if image != img || spec.Image != img {
			t.Errorf("run %s records %q and started from %q", run, image, spec.Image)
		}
		n++
	}
	rows.Close()
	if n < 3 {
		t.Fatalf("%d runs of the release suite", n)
	}

	// Its builder had the candidate's stage, and the project's override of the builder's agent file.
	builder := ""
	k.d.Server.QueryRow(ctx, `SELECT builder_run::text FROM eval_runs WHERE suite = $1`, id).Scan(&builder)
	mu.Lock()
	prompt := specs[builder].Prompt
	mu.Unlock()
	if !strings.Contains(prompt, "CANDIDATE-STAGE") || !strings.Contains(prompt, "OVERRIDE-WINS") || strings.Contains(prompt, "CANDIDATE-AGENT") {
		t.Fatalf("the release's builder was told: %s", prompt)
	}
	if n := k.count(`SELECT count(*) FROM eval_runs WHERE suite = $1 AND overrides::text LIKE '%the release under test, of baseline/stages/in_progress.md@0.3.0%'`, id); n != 1 {
		t.Fatal("the release's stage file is not named as the release's")
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'eval_result' AND (detail->>'suite')::bigint = $1`, id); n != 0 {
		t.Fatal("a release suite sent an eval_result signal")
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'eval_result' AND (detail->>'suite')::bigint = $1`, earlier); n != 1 {
		t.Fatal("the earlier suite sent no eval_result signal")
	}
}

// A release suite keeps the kind of what its next agent run waits for, which its gate reads (16.2
// step 5, since 0.52): with the CLI signed out it waits for a sign-in, not at a limit, and once a run of
// it starts it waits for nothing.
func TestAGateEvalSuiteWaitsForASignIn(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	proj, earlier, cases, _ := k.earlierSuite(t)
	k.d.Server.Exec(ctx, `INSERT INTO gates (version, state) VALUES ('0.3.0', 'live')`)
	id, err := k.b.StartReleaseSuite(ctx, proj, "0.3.0", "sha256:"+strings.Repeat("f", 64), earlier, cases, map[string]string{}, "gate")
	if err != nil {
		t.Fatal(err)
	}
	k.d.Server.Exec(ctx, `UPDATE gates SET suites = jsonb_build_array(jsonb_build_object('project', $2::text, 'suite', $1::bigint))`, id, proj.ID)
	k.d.Server.Exec(ctx, `UPDATE cli_logins SET state = 'signed_out'`)
	// The suite's builder works on, so the suite is read while it runs, not once it ended.
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		k.mu.Lock()
		first := k.firstPrompt(spec)
		k.mu.Unlock()
		if strings.Contains(first, "this run is an eval of the project") && strings.Contains(first, "# In progress: build one ticket") {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl", Hold: true}, true
		}
		return prev(kind, spec, p)
	}
	stop := k.run()
	defer stop()
	k.until("the suite waits for a sign-in", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND wait_for = 'login'`, id) == 1
	})
	k.d.Server.Exec(ctx, `UPDATE cli_logins SET state = 'signed_in'`)
	k.until("a run of it started", func() bool {
		return k.count(`SELECT count(*) FROM eval_runs WHERE suite = $1 AND builder_run IS NOT NULL`, id) == 1
	})
	if n := k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND state = 'running' AND wait_for IS NULL`, id); n != 1 {
		t.Fatal("the running suite still says it waits")
	}
}

// passedReal is a gate's real ticket that passed.
const passedReal = `{"step": {"name": "the real ticket on crew-gate", "ok": true, "detail": "GATE-1 reached Done on its checks"}}`

// gateEvals is a gate kit whose Crew's project holds the baseline's eval files at its main, with mark
// at the end of the builder's stage, and whose gate 0.3.0 runs its live part with its real ticket
// passed. It returns the live start of that gate, for a release that changes what agents see.
func gateEvals(t *testing.T, mark string) (*ticketKit, *versions.LiveRunner, versions.LiveStart) {
	t.Helper()
	files := map[string]string{}
	for _, f := range baseline.EvalFiles() {
		text, err := baseline.Text(f)
		if err != nil {
			t.Fatal(err)
		}
		if f == "stages/in_progress.md" {
			text += mark
		}
		files["baseline/"+f] = text
	}
	k, live, img := gateKitWith(t, files)
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `UPDATE gates SET real_step = $1 WHERE version = '0.3.0'`, passedReal); err != nil {
		t.Fatal(err)
	}
	var name string
	k.d.Server.QueryRow(ctx, `SELECT name FROM projects WHERE key = 'CREW'`).Scan(&name)
	return k, live, versions.LiveStart{Version: "0.3.0", Image: img, Agents: true, Project: name, Commit: k.mainOf(name), SameRuntime: true}
}

// mainOf is the commit of a project's main.
func (k *ticketKit) mainOf(name string) string {
	k.t.Helper()
	log, err := k.b.Git.Log(context.Background(), name, "main", "", 1)
	if err != nil || len(log) != 1 {
		k.t.Fatalf("the main of %s: %v %v", name, log, err)
	}
	return log[0].ID
}

// caseProject makes a project of state with cases active eval cases, and no suite.
func (k *ticketKit) caseProject(name, state string, cases int) string {
	k.t.Helper()
	ctx := context.Background()
	var id string
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), $1, upper($1), $2, 'local') RETURNING id::text`,
		name, state).Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	for i := 1; i <= cases; i++ {
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by, status)
			VALUES ($1, $2, 'A case', 'operator', 'abc', 'w', 'w', 'g', 'operator', 'active')`, id, i); err != nil {
			k.t.Fatal(err)
		}
	}
	return id
}

// suiteOn records a suite of project in state whose runs on side ran on version, one a case for its
// first cases cases, each met or not, and returns it.
func (k *ticketKit) suiteOn(project, state, version, side string, met bool, cases int) int64 {
	k.t.Helper()
	ctx := context.Background()
	var id int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by)
		VALUES ($1, 'on_demand', 1, 10, $2, 'operator') RETURNING id`, project, state).Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, state, goal_met, crew_version)
		SELECT $1, $2, id, 1, $3, 'done', $4, $5 FROM eval_cases WHERE project = $2 AND number <= $6`, id, project, side, met, version, cases); err != nil {
		k.t.Fatal(err)
	}
	return id
}

// failOn makes an eval's builder whose first prompt holds mark push work its case's checks fail on.
func (k *ticketKit) failOn(mark string) {
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		k.mu.Lock()
		first := k.firstPrompt(spec)
		k.mu.Unlock()
		if strings.Contains(first, mark) {
			k.push(spec.Run, spec.Branch, map[string]string{"release-work.txt": "work of " + spec.Run})
			return recording(spec), true
		}
		return prev(kind, spec, p)
	}
	k.f.Check = func(spec harness.CheckSpec, cmd harness.CheckCmd) harness.CheckResult {
		if slices.Contains(tarNames(spec.Tree), "release-work.txt") {
			return harness.CheckResult{Code: 1, Output: "the candidate's work fails"}
		}
		return checkSandbox(spec, cmd)
	}
}

// gateSuite is the first release suite version's gate names.
func (k *ticketKit) gateSuite(version string) int64 {
	var id *int64
	k.d.Server.QueryRow(context.Background(), `SELECT (e->>'suite')::bigint FROM gates g, jsonb_array_elements(coalesce(g.suites, '[]')) e
		WHERE g.version = $1 AND e ? 'suite' LIMIT 1`, version).Scan(&id)
	if id == nil {
		return 0
	}
	return *id
}

// The eval suites of a release's gate (16.2 step 5, since 0.52) start after the real ticket passed: a
// project that can be compared gets a release suite of the gate, in the candidate's image. Its result
// is worse, so the step passes and holds the release, with the numbers in the step's detail and a hold
// that names no project, round after round; since 0.53 no cost is on it.
func TestTheGatesEvalSuitesHoldAWorseRelease(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "\nCANDIDATE-STAGE\n")
	ctx := context.Background()
	k.earlierSuite(t)
	k.failOn("CANDIDATE-STAGE")
	if res, err := live.Step(ctx, l); err != nil || res != nil {
		t.Fatalf("the first step: %+v %v", res, err)
	}
	suite := k.gateSuite("0.3.0")
	if suite == 0 || k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND purpose = 'release' AND gate = '0.3.0' AND image = $2`, suite, l.Image) != 1 {
		t.Fatalf("the gate's suite %d", suite)
	}
	stop := k.run()
	k.until("the release suite done", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND state = 'done'`, suite) == 1
	})
	stop()
	// A cost written on the suite, as suites had before 0.53, is on no report.
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET cost_usd = 2.25 WHERE id = $1`, suite)
	for range 2 {
		res, err := live.Step(ctx, l)
		if err != nil || res == nil || res.Live != release.LivePassed || len(res.Steps) != 2 || !res.Steps[0].OK || res.Steps[1].Name != "the eval suites" ||
			!res.Steps[1].OK || !strings.Contains(res.Steps[1].Detail, "shop: worse") || len(res.Skipped) != 0 ||
			res.Hold != "the eval suites measured worse in 1 of 1 compared projects; the numbers are on Versions" || strings.Contains(res.Steps[1].Detail, "$") {
			t.Fatalf("%+v %v", res, err)
		}
	}
}

// A gate whose compared projects are none worse passes, and a project that is not compared is a line
// of the step's detail that holds nothing (16.2 step 5, since 0.52).
func TestTheGatesEvalSuitesPassWhenNoneIsWorse(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	ctx := context.Background()
	k.earlierSuite(t)
	k.caseProject("aaa", "paused", 1)
	if res, err := live.Step(ctx, l); err != nil || res != nil {
		t.Fatalf("the first step: %+v %v", res, err)
	}
	suite := k.gateSuite("0.3.0")
	stop := k.run()
	k.until("the release suite done", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE id = $1 AND state = 'done'`, suite) == 1
	})
	stop()
	res, err := live.Step(ctx, l)
	if err != nil || res == nil || res.Live != release.LivePassed || len(res.Steps) != 2 || !res.Steps[1].OK || res.Hold != "" || len(res.Skipped) != 0 ||
		!strings.Contains(res.Steps[1].Detail, "aaa: not compared: the project is paused") || !strings.Contains(res.Steps[1].Detail, "shop: not worse") {
		t.Fatalf("%+v %v", res, err)
	}
	// The steps rest on what the runs in a candidate image of other code than the running release's did
	// and said, a reviewer's verdict among them: the release waits for Install (security review of M16
	// P4d).
	l.SameRuntime = false
	if res, err := live.Step(ctx, l); err != nil || res == nil || res.Live != release.LivePassed ||
		res.Hold != "its live steps ran in a candidate runtime image whose code is not the running release's, and the steps rest on what its runs did and said: Install decides" {
		t.Fatalf("%+v %v", res, err)
	}
}

// A gate that can compare no project runs no suite, and its step did not run: the release waits for
// Install (16.2 step 5, since 0.52). Each project says why: paused; too few active cases; no suite on
// the running release. Since 0.53 no project's budget keeps it out: it has none.
func TestTheGatesEvalSuitesCompareNothing(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	ctx := context.Background()
	k.fitShop(map[string]any{"min_cases": 1}, nil)
	k.caseProject("aaa", "paused", 1)
	k.caseProject("bbb", "running", 1)
	k.caseProject("ccc", "running", 10)
	// ddd's suite on the running release stopped, and its done one ran on another release.
	ddd := k.caseProject("ddd", "running", 10)
	k.suiteOn(ddd, "stopped", version.Version, "baseline", true, 10)
	k.suiteOn(ddd, "done", "0.1.0", "baseline", true, 10)
	// fff's suite on the running release measured ten cases, of which one is retired and one met no
	// goal either way: nine are left.
	fff := k.caseProject("fff", "running", 11)
	k.suiteOn(fff, "done", version.Version, "baseline", true, 11)
	k.d.Server.Exec(ctx, `UPDATE eval_cases SET status = 'retired' WHERE project = $1 AND number = 11`, fff)
	k.d.Server.Exec(ctx, `UPDATE eval_runs SET goal_met = NULL, state = 'failed' WHERE project = $1
		AND eval_case = (SELECT id FROM eval_cases WHERE project = $1 AND number = 10)`, fff)
	// ggg's suite on the running release measured its change side only.
	// crew-gate is hidden: a case of its is in no gate's suites.
	k.d.Server.Exec(ctx, `INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by, status)
		SELECT id, 1, 'A case', 'operator', 'abc', 'w', 'w', 'g', 'operator', 'active' FROM projects WHERE name = 'crew-gate'`)
	ggg := k.caseProject("ggg", "running", 10)
	s := k.suiteOn(ggg, "done", version.Version, "change", true, 10)
	k.d.Server.Exec(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, state, crew_version)
		SELECT $1, $2, id, 1, 'baseline', 'failed', $3 FROM eval_cases WHERE project = $2`, s, ggg, version.Version)
	res, err := live.Step(ctx, l)
	if err != nil || res == nil || res.Live != release.LiveSkipped || len(res.Skipped) != 1 || res.Skipped[0] != "the eval suites: no project could be compared" ||
		len(res.Steps) != 2 || res.Steps[1].OK || res.Hold != "" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, line := range []string{"aaa: not compared: the project is paused", "bbb: not compared: 1 active cases, and at least 10 are needed to tell",
		"ccc: not compared: no suite on the running release", "ddd: not compared: no suite on the running release",
		"fff: not compared: 9 cases measured on the running release are still active, and at least 10 are needed to tell",
		"ggg: not compared: no suite on the running release", "shop: not compared: no suite on the running release"} {
		if !strings.Contains(res.Steps[1].Detail, line) {
			t.Errorf("the detail does not say %q: %s", line, res.Steps[1].Detail)
		}
	}
	if n := k.count(`SELECT count(*) FROM eval_suites WHERE purpose = 'release'`); n != 0 || strings.Contains(res.Steps[1].Detail, "crew-gate") {
		t.Fatalf("%d release suites, and the detail %s", n, res.Steps[1].Detail)
	}
}

// The suites step is skipped, never failed, so the release waits for Install (16.2 step 5, since
// 0.52): when no agent CLI is signed in, at its start or on its way; when its suite waited at a limit
// for six hours on end; and when it took 24 hours outside a limit, the time at a limit not counted.
func TestTheGatesEvalSuitesAreSkipped(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	ctx := context.Background()
	k.earlierSuite(t)
	signedIn, kicks := true, 0
	live.Use(versions.LiveDeps{Board: k.b, Kick: func() { kicks++ }, SignedIn: func(context.Context) []string {
		if signedIn {
			return []string{"agy"}
		}
		return nil
	}})
	t0 := time.Now()
	now := t0
	live.Now = func() time.Time { return now }
	step := func(v string) *updater.LiveAsk {
		t.Helper()
		l.Version = v
		res, err := live.Step(ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	skipped := func(res *updater.LiveAsk, why string) {
		t.Helper()
		if res == nil || res.Live != release.LiveSkipped || len(res.Skipped) != 1 || res.Skipped[0] != "the eval suites: "+why || len(res.Steps) != 2 || res.Steps[1].OK {
			t.Fatalf("not skipped for %q: %+v", why, res)
		}
	}
	gate := func(v string) {
		t.Helper()
		// The suite of the last gate is stopped, as the pipeline stops it once its gate's step ended.
		k.d.Server.Exec(ctx, `UPDATE eval_suites SET state = 'stopped' WHERE purpose = 'release' AND state IN ('queued', 'running')`)
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO gates (version, state, same_runtime, real_step) VALUES ($1, 'live', true, $2)`, v, passedReal); err != nil {
			t.Fatal(err)
		}
		now = t0
	}

	gate("0.3.1")
	signedIn = false
	skipped(step("0.3.1"), "no agent CLI is signed in")
	if n := k.count(`SELECT count(*) FROM eval_suites WHERE gate = '0.3.1'`); n != 0 {
		t.Fatalf("%d suites of a gate signed out", n)
	}
	signedIn = true

	gate("0.3.2")
	if step("0.3.2") != nil || k.gateSuite("0.3.2") == 0 || kicks != 1 {
		t.Fatalf("no suite started, or the pipeline was not woken (%d kicks)", kicks)
	}
	signedIn = false
	skipped(step("0.3.2"), "no agent CLI is signed in")
	signedIn = true

	gate("0.3.3")
	step("0.3.3")
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET wait_for = 'model' WHERE id = $1`, k.gateSuite("0.3.3"))
	if step("0.3.3") != nil {
		t.Fatal("ended at the start of a limit")
	}
	now = t0.Add(6*time.Hour - time.Minute)
	if step("0.3.3") != nil {
		t.Fatal("ended before six hours at a limit")
	}
	now = t0.Add(6 * time.Hour)
	res := step("0.3.3")
	skipped(res, "it waited at a limit for 6h0m0s")
	if !strings.Contains(res.Steps[1].Detail, "shop: not compared: its suite did not end") {
		t.Fatalf("the detail: %s", res.Steps[1].Detail)
	}

	gate("0.3.4")
	step("0.3.4")
	now = t0.Add(24*time.Hour - time.Minute)
	if step("0.3.4") != nil {
		t.Fatal("ended before 24 hours")
	}
	now = t0.Add(24 * time.Hour)
	skipped(step("0.3.4"), "they ran out of time after 24h0m0s")

	// Five hours at a limit do not count: the 24 hours end at 29.
	gate("0.3.5")
	step("0.3.5")
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET wait_for = 'model' WHERE id = $1`, k.gateSuite("0.3.5"))
	step("0.3.5")
	now = t0.Add(5 * time.Hour)
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET wait_for = NULL WHERE id = $1`, k.gateSuite("0.3.5"))
	if step("0.3.5") != nil {
		t.Fatal("ended when the limit ended")
	}
	now = t0.Add(29*time.Hour - time.Minute)
	if step("0.3.5") != nil {
		t.Fatal("the time at a limit counted")
	}
	now = t0.Add(29 * time.Hour)
	skipped(step("0.3.5"), "they ran out of time after 24h0m0s")

	// A limit that begins after 20 hours is not counted while it lasts.
	gate("0.3.6")
	step("0.3.6")
	now = t0.Add(20 * time.Hour)
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET wait_for = 'model' WHERE id = $1`, k.gateSuite("0.3.6"))
	step("0.3.6")
	now = t0.Add(25 * time.Hour)
	if res := step("0.3.6"); res != nil {
		t.Fatalf("the limit going on counted: %+v", res)
	}
}

// The suites step is taken up from the gate's row (16.2 step 5, since 0.52): a server that starts again
// starts no second suite, and finds the suite a stop between its start and its record left; Abandon
// wakes the pipeline, which stops a suite its gate no longer runs.
func TestTheGatesEvalSuitesAreTakenUpAfterARestart(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	ctx := context.Background()
	proj, _, _, _ := k.earlierSuite(t)
	if res, err := live.Step(ctx, l); err != nil || res != nil {
		t.Fatalf("%+v %v", res, err)
	}
	suite := k.gateSuite("0.3.0")
	kicks := 0
	again := &versions.LiveRunner{Pool: k.d.Server}
	again.Use(versions.LiveDeps{Board: k.b, Kick: func() { kicks++ }, SignedIn: func(context.Context) []string { return []string{"agy"} }})
	if res, err := again.Step(ctx, l); err != nil || res != nil || k.count(`SELECT count(*) FROM eval_suites WHERE purpose = 'release'`) != 1 {
		t.Fatalf("after a restart: %+v %v", res, err)
	}
	k.d.Server.Exec(ctx, `UPDATE gates SET suites = jsonb_build_array(jsonb_build_object('project', $1::text, 'name', 'shop'))`, proj.ID)
	if res, err := again.Step(ctx, l); err != nil || res != nil || k.gateSuite("0.3.0") != suite || k.count(`SELECT count(*) FROM eval_suites WHERE purpose = 'release'`) != 1 {
		t.Fatalf("the suite a stop left: %+v %v, %d of %d", res, err, k.gateSuite("0.3.0"), suite)
	}
	if err := again.Abandon(ctx, "0.3.0", l.Image, "its gate passed"); err != nil || kicks != 1 {
		t.Fatalf("abandoned: %v, %d kicks", err, kicks)
	}
}

// A release whose commit Crew's project does not hold with its eval files skips the suites: they would
// measure the running prompts (16.2 step 5, since 0.52).
func TestTheGatesEvalSuitesNeedTheReleasesFiles(t *testing.T) {
	t.Parallel()
	k, live, _ := gateKit(t)
	ctx := context.Background()
	k.d.Server.Exec(ctx, `UPDATE gates SET real_step = $1 WHERE version = '0.3.0'`, passedReal)
	k.earlierSuite(t)
	var name string
	k.d.Server.QueryRow(ctx, `SELECT name FROM projects WHERE key = 'CREW'`).Scan(&name)
	res, err := live.Step(ctx, versions.LiveStart{Version: "0.3.0", Image: "sha256:" + strings.Repeat("c", 64), Agents: true, Project: name, Commit: k.mainOf(name)})
	if err != nil || res == nil || res.Live != release.LiveSkipped || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0], "the eval suites: the release's commit ") ||
		!strings.Contains(res.Skipped[0], "has no baseline/") || k.count(`SELECT count(*) FROM eval_suites WHERE purpose = 'release'`) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A project whose own suite waits or runs is taken after it ended, and compared then (16.2 step 5,
// since 0.52).
func TestTheGatesEvalSuitesWaitForAProjectsOwnSuite(t *testing.T) {
	t.Parallel()
	k, live, l := gateEvals(t, "")
	ctx := context.Background()
	proj, _, _, _ := k.earlierSuite(t)
	var own int64
	k.d.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by) VALUES ($1, 'on_demand', 1, 10, 'operator') RETURNING id`,
		proj.ID).Scan(&own)
	if res, err := live.Step(ctx, l); err != nil || res != nil || k.gateSuite("0.3.0") != 0 {
		t.Fatalf("the gate did not wait for shop's own suite: %+v %v", res, err)
	}
	k.d.Server.Exec(ctx, `UPDATE eval_suites SET state = 'stopped' WHERE id = $1`, own)
	if res, err := live.Step(ctx, l); err != nil || res != nil || k.gateSuite("0.3.0") == 0 {
		t.Fatalf("the gate did not take shop once its suite ended: %+v %v", res, err)
	}
}
