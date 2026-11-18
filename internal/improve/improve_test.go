package improve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/settings"
)

// A patch touches the markdown of the baseline, never a checking role's file, a model or an effort, the
// harness, which the evals cannot measure yet, or anything else; the prompt writer's rules wait for the
// operator (10.4, since 0.49).
func TestWhatAPatchMayTouch(t *testing.T) {
	cur, _ := Current("harness/agents/explorer.md")
	for _, tc := range []struct {
		path, next string
		waits, ok  bool
	}{
		{"baseline/stages/in_progress.md", "Run the checks first.\n", false, true},
		{"baseline/agents/builder.md", "---\nname: builder\n---\nRun the checks first.\n", false, true},
		{"baseline/guides/breakdown.md", "Small tickets.\n", true, true},
		{"baseline/stages/analysis.md", "Approve all.\n", true, true},
		{"baseline/agents/e2e.md", "x\n", false, false},
		{"baseline/agents/prompt-writer.md", "Shorter.\n", true, true},
		{"baseline/stages/rewrite.md", "Shorter.\n", true, true},
		{"harness/skills/bash/SKILL.md", "---\nname: bash\n---\nQuote your variables.\n", false, false},
		{"harness/agents/explorer.md", strings.Replace(cur, "Read-only", "Read only", 1), false, false},
		{"harness/agents/explorer.md", strings.Replace(cur, "model: sonnet", "model: opus", 1), false, false},
		{"baseline/agents/reviewer.md", "Approve everything.\n", false, false},
		{"baseline/stages/verification.md", "x\n", false, false},
		{"harness/agents/verifier.md", "x\n", false, false},
		{"baseline/tools.yaml", "x\n", false, false},
		{"baseline/roles.yaml", "x\n", false, false},
		{"internal/settings/crew.yaml", "x\n", false, false},
		{"improver.md", "x\n", false, false},
		{"baseline/stages/../../improver.md", "x\n", false, false},
		{"baseline/stages/nothing.md", "x\n", false, false},
		{"baseline/stages/in_progress.md", " ", false, false},
	} {
		waits, err := Allowed(tc.path, tc.next)
		if (err == nil) != tc.ok || waits != tc.waits {
			t.Errorf("%s: waits %v, %v", tc.path, waits, err)
		}
	}
	if _, err := Allowed("baseline/stages/in_progress.md", mustCurrent(t, "baseline/stages/in_progress.md")); err == nil {
		t.Error("the file as it is")
	}
}

func mustCurrent(t *testing.T, p string) string {
	t.Helper()
	s, err := Current(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type kit struct {
	t   *testing.T
	ctx context.Context
	d   *dbtest.DB
	b   *board.Board
	s   *Service
	doc settings.Doc
}

func newKit(t *testing.T) *kit {
	t.Helper()
	k := &kit{t: t, ctx: context.Background(), d: dbtest.New(t, false), doc: settings.Doc{"improvements": settings.Doc{"approval": "auto"}}}
	dir := t.TempDir()
	k.b = &board.Board{Pool: k.d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	k.s = &Service{Pool: k.d.Server, Board: k.b, Running: "1.1.0", Settings: func(context.Context) (settings.Doc, error) { return k.doc, nil }}
	return k
}

func (k *kit) project(name, key string) *board.Project {
	k.t.Helper()
	p, err := k.b.CreateProject(k.ctx, board.NewProject{Name: name, Key: key, Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		k.t.Fatal(err)
	}
	return p
}

func (k *kit) exec(q string, args ...any) {
	k.t.Helper()
	if _, err := k.d.Server.Exec(k.ctx, q, args...); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
}

// The generic check (10.5, step 2) refuses a patch that names a project, a ticket, or a repository,
// or shares ten words in a row with what a project's agents wrote; a patch in general words passes.
func TestTheGenericCheck(t *testing.T) {
	k := newKit(t)
	p := k.project("storefront", "SF")
	tk, err := k.b.AddTicket(k.ctx, p.Name, board.NewTicket{Title: "Lock", Why: "w", What: "w", Goal: "g", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	if err != nil {
		t.Fatal(err)
	}
	var tid int64
	k.d.Server.QueryRow(k.ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, p.ID, tk.Number).Scan(&tid)
	k.exec(`INSERT INTO summaries (ticket, project, outcome, facts, done, how) VALUES ($1, $2, 'done', '{}', 'Added the lockout',
		'The counter of failed sign ins is kept in the accounts table and reset on every successful sign in')`, tid, p.ID)
	base := mustCurrent(t, "baseline/stages/in_progress.md")
	for name, line := range map[string]string{
		"a project's name":  "Work like the storefront team does.",
		"a ticket":          "Remember what SF-12 taught.",
		"a summary's words": "Note that the counter of failed sign ins is kept in the accounts table now.",
		"a repository":      "Clone storefront before you start.",
	} {
		err := Generic(k.ctx, k.d.Server, k.b.Git, map[string]string{"baseline/stages/in_progress.md": base + "\n" + line + "\n"}, nil)
		if err == nil {
			t.Errorf("%s passed", name)
		}
		if err != nil && strings.Contains(err.Error(), "storefront") {
			t.Errorf("the refusal names the project: %v", err)
		}
	}
	for name, line := range map[string]string{
		"general words":     "Run the ticket's checks before you ask for a move.",
		"nine shared words": "The counter of failed sign ins is kept in a cache.",
		"a short word":      "Use the SF symbol set.",
	} {
		if err := Generic(k.ctx, k.d.Server, k.b.Git, map[string]string{"baseline/stages/in_progress.md": base + "\n" + line + "\n"}, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The baseline's own lines are not the patch's: only what it adds is checked.
	if err := Generic(k.ctx, k.d.Server, k.b.Git, map[string]string{"baseline/stages/in_progress.md": base + "\nAlso this.\n"}, nil); err != nil {
		t.Errorf("the file's own lines: %v", err)
	}
}

// A path a cited ticket changed is no project's fact when the baseline itself names it whole, such as
// AGENTS.md or docs/architecture.md; any other path it changed, also one below a folder the baseline
// names, or one that holds a named path, still refuses the patch, and so do the file's words (design
// 0.50: M15 item 2's false positive).
func TestTheGenericCheckIgnoresAPathTheBaselineNames(t *testing.T) {
	k := newKit(t)
	p := k.project("alpha", "AL")
	tk, _ := k.b.AddTicket(k.ctx, p.Name, board.NewTicket{Title: "Lock", Why: "w", What: "w", Goal: "g", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	work := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", k.b.Git.Canonical(p.Name), ".")
	for path, text := range map[string]string{"AGENTS.md": "Lock an account after five failed sign ins in a row, and say so on the page.\n",
		".agents/skills/lockout/SKILL.md": "Lock it.\n", "src/lock.go": "package src\n", "docs/architecture.md": "# Parts\n"} {
		os.MkdirAll(filepath.Join(work, filepath.Dir(path)), 0o755)
		os.WriteFile(filepath.Join(work, path), []byte(text), 0o644)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "the work")
	merge := git("rev-parse", "HEAD")
	git("push", "-q", "origin", "HEAD:refs/heads/merged")
	git("push", "-q", k.b.Git.Quarantine.Path(p.Name), "HEAD:refs/heads/merged")
	k.exec(`UPDATE tickets SET stage = 'done' WHERE project = $1 AND number = $2`, p.ID, tk.Number)
	k.exec(`INSERT INTO check_runs (project, ticket, purpose, commit, state) SELECT $1, id, 'merge', $2, 'passed' FROM tickets WHERE project = $1 AND number = $3`,
		p.ID, merge, tk.Number)
	base := mustCurrent(t, "baseline/stages/in_progress.md")
	for line, refused := range map[string]bool{
		"Read AGENTS.md before you start.":                       false,
		"Read the rules in AGENTS.md.":                           false,
		"Both AGENTS.md AGENTS.md.":                              false,
		"Keep docs/architecture.md current.":                     false,
		"Read src/lock.go before you start.":                     true,
		"Read .agents/skills/lockout/SKILL.md before you start.": true,
		// A longer path that holds a named one is the project's own (review of 0.50).
		"Read services/payments/AGENTS.md first.": true,
		"Keep AGENTS.md.bak out of the commit.":   true,
		// The file's words count whatever its path.
		"Lock an account after five failed sign ins in a row.": true,
	} {
		err := Generic(k.ctx, k.d.Server, k.b.Git, map[string]string{"baseline/stages/in_progress.md": base + "\n" + line + "\n"}, []string{"AL-1"})
		if refused != (err != nil) {
			t.Errorf("%q: %v", line, err)
		}
	}
}

func side(n int, pass bool) measure.Side {
	s := measure.Side{Runs: map[int64][]bool{}}
	for i := 0; i < n; i++ {
		s.Runs[int64(i+1)] = []bool{pass}
	}
	return s
}

// An improvement goes from the proposal to its ticket (10.5): refused by what it touches, or by the
// generic check; in testing, it waits for cases while there are fewer than ten across all projects, then
// gets a suite in every project with cases; not worse passes it and makes the ticket on Crew's own
// project, which holds the change and none of the evidence; worse refuses it.
func TestAnImprovementFromProposalToTicket(t *testing.T) {
	k := newKit(t)
	a := k.project("alpha", "AL")
	b := k.project("beta", "BE")
	crew := k.project("crew", "CR")
	if _, err := k.s.Propose(k.ctx, "", Proposal{Title: "x", Patch: map[string]string{"baseline/agents/reviewer.md": "Approve.\n"}, Measure: "fix_rounds_per_ticket"}); err == nil {
		t.Fatal("a checking role's file")
	}
	if _, err := k.s.Propose(k.ctx, "", Proposal{Title: "x", Patch: map[string]string{"baseline/stages/in_progress.md": "x\n"}, Measure: "happiness"}); err == nil {
		t.Fatal("a metric not on the list")
	}
	named, err := k.s.Propose(k.ctx, "", Proposal{Title: "Learn from alpha", Patch: map[string]string{"baseline/stages/in_progress.md": "Do as alpha does.\n"}, Measure: "fix_rounds_per_ticket"})
	if err != nil || named.Status != Refused || named.RefusedWhy == nil || !strings.Contains(*named.RefusedWhy, "generic check") {
		t.Fatalf("a patch that names a project: %+v %v", named, err)
	}
	patch := map[string]string{"baseline/stages/in_progress.md": "# In progress\n\nRun the ticket's checks before you ask for a move.\n"}
	x, err := k.s.Propose(k.ctx, "", Proposal{Title: "Builders run the checks first", Patch: patch, Measure: "first_request_pass_rate",
		Expected: "More tickets pass on the first request.", Evidence: Evidence{Signal: "check_round", Text: "alpha and beta"}})
	if err != nil || x.Status != Testing || x.Number != 2 {
		t.Fatalf("a good patch: %+v %v", x, err)
	}
	// Five cases in each of two projects: ten, enough. With fewer it waits.
	addCases := func(p *board.Project, n int) {
		for i := 1; i <= n; i++ {
			k.exec(`INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by) VALUES ($1, $2, 't', 'operator', 'b', 'w', 'w', 'g', 'operator')`, p.ID, i)
		}
	}
	addCases(a, 5)
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	if x, _ = k.s.Get(k.ctx, 2); x.WaitsFor == nil || *x.WaitsFor != WaitsCases {
		t.Fatalf("with five cases: %+v", x.WaitsFor)
	}
	addCases(b, 5)
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	var suites int
	k.d.Server.QueryRow(k.ctx, `SELECT count(*) FROM eval_suites WHERE improvement = $1 AND purpose = 'improvement'`, x.ID).Scan(&suites)
	if suites != 2 {
		t.Fatalf("%d suites, one in each project with cases", suites)
	}
	if x, _ = k.s.Get(k.ctx, 2); x.Status != Testing || x.WaitsFor != nil {
		t.Fatalf("while its suites run: %+v", x)
	}
	// The suites end, each with its numbers: not worse.
	rows, _ := k.d.Server.Query(k.ctx, `SELECT id, project::text FROM eval_suites WHERE improvement = $1`, x.ID)
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
	for i, s := range list {
		k.exec(`UPDATE eval_suites SET state = 'done' WHERE id = $1`, s.id)
		base, change := side(5, true), side(5, true)
		// Case ids differ across projects.
		base.Runs = map[int64][]bool{int64(i*10 + 1): {true}, int64(i*10 + 2): {true}, int64(i*10 + 3): {true}, int64(i*10 + 4): {true}, int64(i*10 + 5): {false}}
		change.Runs = map[int64][]bool{int64(i*10 + 1): {true}, int64(i*10 + 2): {true}, int64(i*10 + 3): {true}, int64(i*10 + 4): {true}, int64(i*10 + 5): {true}}
		if err := k.s.RecordEvals(k.ctx, x.ID, s.project, s.id, base, change); err != nil {
			t.Fatal(err)
		}
	}
	// Crew's project is not named yet: it waits for it.
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	x, _ = k.s.Get(k.ctx, 2)
	var ev map[string]any
	json.Unmarshal(x.Evals, &ev)
	if x.Status != Passed || x.WaitsFor == nil || *x.WaitsFor != WaitsCrewProject || ev["projects"] != float64(2) || ev["cases"] != float64(10) || ev["verdict"] != "not_worse" {
		t.Fatalf("passed, waiting for Crew's project: %+v %v", x, ev)
	}
	k.doc["improvements"].(settings.Doc)["crew_project"] = crew.Name
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	x, _ = k.s.Get(k.ctx, 2)
	if x.Ticket == nil || *x.Ticket != "CR-1" || x.WaitsFor != nil || x.ReleaseFrom == nil || *x.ReleaseFrom != "1.1.0" {
		t.Fatalf("the ticket: %+v", x)
	}
	tk, err := k.b.Ticket(k.ctx, crew, "CR-1")
	if err != nil || !strings.HasPrefix(tk.Title, "Improvement i_0002") || len(tk.Checks) != 1 || !strings.Contains(tk.Checks[0].Spec, "sha256sum -c") ||
		strings.Contains(tk.Why+tk.What, "alpha") {
		t.Fatalf("the ticket: %+v %v", tk, err)
	}
	var note string
	k.d.Server.QueryRow(k.ctx, `SELECT body FROM records WHERE kind = 'note' AND author = 'improver'`).Scan(&note)
	if !strings.Contains(note, "Run the ticket's checks before you ask for a move.") || strings.Contains(note, "alpha") {
		t.Fatalf("the note: %s", note)
	}

	// A worse one is refused.
	y, _ := k.s.Propose(k.ctx, "", Proposal{Title: "Shorter build", Patch: map[string]string{"baseline/stages/in_progress.md": "Build it fast.\n"}, Measure: "fix_rounds_per_ticket"})
	k.exec(`INSERT INTO improvement_evals (improvement, project, suite, cases, result) SELECT $1, project, id, 5, '{}' FROM eval_suites WHERE improvement = $2`, y.ID, x.ID)
	for i, s := range list {
		k.exec(`INSERT INTO eval_suites (project, purpose, improvement, repeats, budget_usd, state, asked_by) VALUES ($1, 'improvement', $2, 1, 10, 'done', 'crew')`, s.project, y.ID)
		base, change := side(0, true), side(0, true)
		base.Runs, change.Runs = map[int64][]bool{}, map[int64][]bool{}
		for c := 1; c <= 5; c++ {
			base.Runs[int64(i*10+c)] = []bool{true}
			change.Runs[int64(i*10+c)] = []bool{c > 3}
		}
		if err := k.s.RecordEvals(k.ctx, y.ID, s.project, s.id, base, change); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.s.Step(k.ctx); err != nil {
		t.Fatal(err)
	}
	if y, _ = k.s.Get(k.ctx, y.Number); y.Status != Refused || !strings.Contains(*y.RefusedWhy, "evals were worse") {
		t.Fatalf("a worse one: %+v", y)
	}
}

// One of the prompt writer's rules waits for the operator; approve lets it go on as a passed one does,
// and take out ends one that is not installed (10.5, 7.2).
func TestTheOperatorsAnswer(t *testing.T) {
	k := newKit(t)
	crew := k.project("crew", "CR")
	k.doc["improvements"].(settings.Doc)["crew_project"] = crew.Name
	x, err := k.s.Propose(k.ctx, "", Proposal{Title: "Shorter rewrites", Patch: map[string]string{"baseline/agents/prompt-writer.md": "---\nname: prompt-writer\n---\nBe brief.\n"},
		Measure: measure.RewritesUsed})
	if err != nil || x.Status != Proposed || x.WaitsFor == nil || *x.WaitsFor != WaitsOperator {
		t.Fatalf("the prompt writer's: %+v %v", x, err)
	}
	if _, err := k.s.Decide(k.ctx, x.Number, "maybe", "operator"); err == nil {
		t.Fatal("a choice that is none")
	}
	x, err = k.s.Decide(k.ctx, x.Number, "approve", "operator")
	if err != nil || x.Status != Passed || x.Ticket == nil || x.WaitsFor != nil {
		t.Fatalf("approved: %+v %v", x, err)
	}
	if _, err := k.s.Decide(k.ctx, x.Number, "approve", "operator"); err == nil {
		t.Fatal("approved twice")
	}
	y, _ := k.s.Propose(k.ctx, "", Proposal{Title: "Template", Patch: map[string]string{"baseline/templates/task.md": "## Out of scope\n"}, Measure: "fix_rounds_per_ticket"})
	if y, err = k.s.Decide(k.ctx, y.Number, "take_out", "operator"); err != nil || y.Status != Removed {
		t.Fatalf("taken out: %+v %v", y, err)
	}
	// Taking out a passed one closes its ticket on Crew's project (review of M15).
	if x, err = k.s.Decide(k.ctx, x.Number, "take_out", "operator"); err != nil || x.Status != Removed {
		t.Fatalf("a passed one taken out: %+v %v", x, err)
	}
	if tk, err := k.b.Ticket(k.ctx, crew, *x.Ticket); err != nil || tk.Stage != "closed" {
		t.Fatalf("its ticket: %+v %v", tk, err)
	}
	if _, err := k.s.Get(k.ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatal("no such improvement")
	}
}

// An applied improvement is measured once enough tickets were done on its version, against those of the
// version before (10.5, step 6); a worse result takes it out by going back to the release before when
// nothing was installed since (step 7), with news; a release that also changed code makes the result
// inconclusive, and the change stays (21, "too few tasks to tell").
func TestAnAppliedImprovementIsMeasuredAndAWorseOneTakenOut(t *testing.T) {
	k := newKit(t)
	p := k.project("alpha", "AL")
	var asked []string
	k.s.Rollback = func(_ context.Context, v string) error { asked = append(asked, v); return nil }
	n := 0
	done := func(version string, failed bool) {
		n++
		var id int64
		if err := k.d.Server.QueryRow(k.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 't', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`, p.ID, 100+n, int64(n)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		var sub int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2, 1, 'build', 'builder') RETURNING id`, p.ID, id).Scan(&sub)
		k.exec(`INSERT INTO runs (id, subtask, number, cli, crew_version, image) VALUES (gen_random_uuid(), $1, 1, 'agy', $2, 'i')`, sub, version)
		k.exec(`INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, 'test', 'done', 'backend', now() + make_interval(secs => $2))`, id, n)
		if failed {
			k.exec(`INSERT INTO signals (project, ticket, kind) VALUES ($1, $2, 'check_round')`, p.ID, id)
		}
	}
	add := func(title, kind string) *Improvement {
		x, err := k.s.Propose(k.ctx, "", Proposal{Title: title, Patch: map[string]string{"baseline/guides/planning.md": title + "\n"}, Measure: "first_request_pass_rate"})
		if err != nil {
			t.Fatal(err)
		}
		k.exec(`UPDATE improvements SET status = 'applied', release_from = '1.0.0', release_to = '1.1.0', evals = '{"margin": 0.1}' WHERE id = $1`, x.ID)
		k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('1.1.0', 'c', 's', '{}', now(), $1, now())
			ON CONFLICT (version) DO UPDATE SET kind = EXCLUDED.kind`, kind)
		return x
	}
	x := add("Plan in small parts", "text")
	done("1.0.0", false)
	done("1.0.0", false)
	done("1.1.0", true)
	if err := k.s.Measure(k.ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.s.Get(k.ctx, x.Number); got.MeasuredAt != nil {
		t.Fatal("measured with one ticket after")
	}
	done("1.1.0", true)
	if err := k.s.Measure(k.ctx, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := k.s.Get(k.ctx, x.Number)
	var r measure.Result
	json.Unmarshal(got.Result, &r)
	if got.Status != RolledBack || r.Verdict != measure.Worse || r.Before != 1 || r.After != 0 || len(asked) != 1 || asked[0] != "1.0.0" {
		t.Fatalf("a worse one: %s %+v asked %v", got.Status, r, asked)
	}
	var news string
	k.d.Server.QueryRow(k.ctx, `SELECT detail->>'text' FROM signals WHERE kind = 'improvement'`).Scan(&news)
	if !strings.Contains(news, "measured worse, and Crew took it out by going back to 1.0.0") {
		t.Fatalf("the news: %q", news)
	}
	// The same with a release that changed code: inconclusive, and it stays.
	y := add("Plan with care", "code")
	if err := k.s.Measure(k.ctx, 2); err != nil {
		t.Fatal(err)
	}
	got, _ = k.s.Get(k.ctx, y.Number)
	json.Unmarshal(got.Result, &r)
	if got.Status != Applied || r.Verdict != measure.Inconclusive || !strings.Contains(r.Why, "changed code") {
		t.Fatalf("with code in the release: %s %+v", got.Status, r)
	}
	_ = time.Now
}

// Too few tickets to tell (21): an improvement measured with fewer tickets before it than it needs is
// inconclusive, with its numbers, and stays; the operator can still read it.
func TestTooFewTicketsToTellIsInconclusive(t *testing.T) {
	k := newKit(t)
	p := k.project("alpha", "AL")
	x, err := k.s.Propose(k.ctx, "", Proposal{Title: "Plan small", Patch: map[string]string{"baseline/guides/planning.md": "Plan small.\n"}, Measure: "first_request_pass_rate"})
	if err != nil {
		t.Fatal(err)
	}
	k.exec(`UPDATE improvements SET status = 'applied', release_from = '0.9.0', release_to = '1.1.0' WHERE id = $1`, x.ID)
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('1.1.0', 'c', 's', '{}', now(), 'text', now())`)
	for i := 1; i <= 3; i++ {
		var id int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position) VALUES ($1, $2, 't', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`,
			p.ID, 10+i, int64(i)).Scan(&id)
		var sub int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2, 1, 'build', 'builder') RETURNING id`, p.ID, id).Scan(&sub)
		k.exec(`INSERT INTO runs (id, subtask, number, cli, crew_version, image) VALUES (gen_random_uuid(), $1, 1, 'agy', '1.1.0', 'i')`, sub)
		k.exec(`INSERT INTO stage_moves (ticket, from_stage, to_stage, actor) VALUES ($1, 'test', 'done', 'backend')`, id)
	}
	if err := k.s.Measure(k.ctx, 3); err != nil {
		t.Fatal(err)
	}
	got, _ := k.s.Get(k.ctx, x.Number)
	var r measure.Result
	json.Unmarshal(got.Result, &r)
	if got.Status != Applied || r.Verdict != measure.Inconclusive || r.TicketsBefore != 0 || r.TicketsAfter != 3 || !strings.Contains(r.Why, "too few tickets") {
		t.Fatalf("too few to tell: %s %+v", got.Status, r)
	}
	var news string
	k.d.Server.QueryRow(k.ctx, `SELECT detail->>'text' FROM signals WHERE kind = 'improvement'`).Scan(&news)
	if !strings.Contains(news, "measured inconclusive") {
		t.Fatalf("the news: %q", news)
	}
}

// A worse improvement whose take-out fails is not counted as measured: the next pass tries again
// (review of M15).
func TestATakeOutThatFailsIsTriedAgain(t *testing.T) {
	k := newKit(t)
	p := k.project("alpha", "AL")
	x, err := k.s.Propose(k.ctx, "", Proposal{Title: "Build fast", Patch: map[string]string{"baseline/stages/in_progress.md": "Build fast.\n"}, Measure: "first_request_pass_rate"})
	if err != nil {
		t.Fatal(err)
	}
	k.exec(`UPDATE improvements SET status = 'applied', release_from = '1.0.0', release_to = '1.1.0' WHERE id = $1`, x.ID)
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('1.1.0', 'c', 's', '{}', now(), 'text', now())`)
	for i, v := range []string{"1.0.0", "1.1.0"} {
		var id int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position) VALUES ($1, $2, 't', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`,
			p.ID, 10+i, int64(i)).Scan(&id)
		var sub int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2, 1, 'build', 'builder') RETURNING id`, p.ID, id).Scan(&sub)
		k.exec(`INSERT INTO runs (id, subtask, number, cli, crew_version, image) VALUES (gen_random_uuid(), $1, 1, 'agy', $2, 'i')`, sub, v)
		k.exec(`INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, 'test', 'done', 'backend', now() + make_interval(secs => $2))`, id, i)
		if v == "1.1.0" {
			k.exec(`INSERT INTO signals (project, ticket, kind) VALUES ($1, $2, 'check_round')`, p.ID, id)
		}
	}
	// No updater and no ticket: the take-out cannot happen.
	if err := k.s.Measure(k.ctx, 1); err == nil {
		t.Fatal("a take-out that could not happen passed")
	}
	if got, _ := k.s.Get(k.ctx, x.Number); got.MeasuredAt != nil || got.Status != Applied {
		t.Fatalf("after a failed take-out: %+v", got)
	}
	var asked string
	k.s.Running, k.s.Rollback = "1.1.0", func(_ context.Context, v string) error { asked = v; return nil }
	if err := k.s.Measure(k.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.s.Get(k.ctx, x.Number); got.MeasuredAt == nil || got.Status != RolledBack || asked != "1.0.0" {
		t.Fatalf("tried again: %+v %q", got, asked)
	}
}

// The generic check refuses a patch when a cited ticket's files cannot be read (review of M15), and a
// proposal that looks like it holds a secret goes no further (security review of M15).
func TestTheChecksKeepOutWhatTheyCannotSee(t *testing.T) {
	k := newKit(t)
	p := k.project("alpha", "AL")
	tk, _ := k.b.AddTicket(k.ctx, p.Name, board.NewTicket{Title: "Lock", Why: "w", What: "w", Goal: "g", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	k.exec(`INSERT INTO check_runs (project, ticket, purpose, commit, state) SELECT $1, id, 'merge', $2, 'passed' FROM tickets WHERE project = $1 AND number = $3`,
		p.ID, strings.Repeat("ab", 20), tk.Number)
	patch := map[string]string{"baseline/stages/in_progress.md": "In general words.\n"}
	if err := Generic(k.ctx, k.d.Server, k.b.Git, patch, []string{"AL-1"}); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("a cited ticket whose merge is not in git: %v", err)
	}
	if err := Generic(k.ctx, k.d.Server, nil, patch, []string{"AL-1"}); err == nil {
		t.Fatal("with no git store")
	}
	if err := Generic(k.ctx, k.d.Server, k.b.Git, patch, []string{"AL-99", "ZZ-1"}); err != nil {
		t.Fatalf("keys that name no ticket: %v", err)
	}
	if _, err := k.s.Propose(k.ctx, "", Proposal{Title: "Keys", Patch: map[string]string{"baseline/stages/in_progress.md": "Use the token ghp_" + strings.Repeat("a1B2", 9) + " to clone.\n"},
		Measure: "fix_rounds_per_ticket"}); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a proposal with a secret: %v", err)
	}
}

// A passed improvement whose ticket the operator closed on the board is taken out all the same; a path
// that is no path of the baseline never reaches a check command; and a cited ticket that is Done with no
// merge Crew can find cannot be read, so the patch is refused (round 2 of the review of M15).
func TestRoundTwoOfTheReview(t *testing.T) {
	k := newKit(t)
	crew := k.project("crew", "CR")
	k.doc["improvements"].(settings.Doc)["crew_project"] = crew.Name
	x, _ := k.s.Propose(k.ctx, "", Proposal{Title: "Brief", Patch: map[string]string{"baseline/agents/prompt-writer.md": "---\nname: prompt-writer\n---\nBe brief.\n"}, Measure: measure.RewritesUsed})
	x, err := k.s.Decide(k.ctx, x.Number, "approve", "operator")
	if err != nil || x.Ticket == nil {
		t.Fatalf("approved: %+v %v", x, err)
	}
	if _, err := k.b.Discard(k.ctx, crew, *x.Ticket, "not now", board.Operator); err != nil {
		t.Fatal(err)
	}
	if x, err = k.s.Decide(k.ctx, x.Number, "take_out", "operator"); err != nil || x.Status != Removed {
		t.Fatalf("with its ticket closed already: %+v %v", x, err)
	}
	bad := &Improvement{Number: 9, Title: "x", Target: []string{"baseline/stages/x'; rm -rf /.md"}, Patch: map[string]string{"baseline/stages/x'; rm -rf /.md": "x"}}
	if err := k.s.ticket(k.ctx, bad); err == nil || !strings.Contains(err.Error(), "no path a check command takes") {
		t.Fatalf("a path that is no path of the baseline: %v", err)
	}
	k.exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position) VALUES ($1, 50, 't', 'feature', 'done', 'plan', 'lead', 50)`, crew.ID)
	if err := Generic(k.ctx, k.d.Server, k.b.Git, map[string]string{"baseline/stages/in_progress.md": "General words.\n"}, []string{"CR-50"}); err == nil {
		t.Fatal("a Done ticket with no merge was taken as read")
	}
}

// An improvement whose ticket is Done, in Crew's main but in no installed release, is taken out with a
// revert of its ticket (16.3, since 0.55): the revert ticket is made first, as Crew's own, then the
// improvement is removed; the releases that hold it wait for Install (versions' HoldTakenOut). One in an
// installed release is refused, since it is measured there (10.5); so is one while Crew's project is
// paused, and nothing changes. A revert that is open already, from a press that failed after it, is no
// error.
func TestAMergedImprovementIsTakenOutByARevert(t *testing.T) {
	k := newKit(t)
	crew := k.project("crew", "CR")
	k.doc["improvements"].(settings.Doc)["crew_project"] = crew.Name
	merged := func(title string) (*Improvement, string) {
		t.Helper()
		x, err := k.s.Propose(k.ctx, "", Proposal{Title: title, Patch: map[string]string{"baseline/guides/planning.md": title + "\n"}, Measure: "first_request_pass_rate"})
		if err != nil {
			t.Fatal(err)
		}
		tk, err := k.b.AddTicket(k.ctx, crew.Name, board.NewTicket{Title: title, Why: "w", What: "w", Goal: "g",
			Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f x"}}}, board.OriginAgent, "improver")
		if err != nil {
			t.Fatal(err)
		}
		var tid int64
		k.d.Server.QueryRow(k.ctx, `SELECT id FROM tickets WHERE project = $1 AND number = $2`, crew.ID, tk.Number).Scan(&tid)
		commit := k.commitOnMain(crew.Name, title)
		k.exec(`UPDATE tickets SET stage = 'done' WHERE id = $1`, tid)
		k.exec(`INSERT INTO check_runs (project, ticket, purpose, commit, state) VALUES ($1, $2, 'merge', $3, 'passed')`, crew.ID, tid, commit)
		k.exec(`UPDATE improvements SET status = 'passed', ticket = $2 WHERE id = $1`, x.ID, tid)
		x, _ = k.s.Get(k.ctx, x.Number)
		return x, commit
	}
	reverts := func() int {
		var n int
		k.d.Server.QueryRow(k.ctx, `SELECT count(*) FROM ticket_links WHERE kind = 'reverts'`).Scan(&n)
		return n
	}
	k.exec(`UPDATE projects SET state = 'running' WHERE id = $1`, crew.ID)

	// In an installed release: refused, nothing made.
	a, ca := merged("Plan in parts")
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('1.2.0', $1, 's', '{}', now(), 'text', now())`, ca)
	if _, err := k.s.Decide(k.ctx, a.Number, "take_out", "operator"); !isRefusal(err, "installed") {
		t.Fatalf("one in an installed release: %v", err)
	}
	// Not installed: a revert ticket, the improvement removed, and the releases held again.
	b, _ := merged("Plan with care")
	k.exec(`UPDATE projects SET state = 'paused' WHERE id = $1`, crew.ID)
	if _, err := k.s.Decide(k.ctx, b.Number, "take_out", "operator"); !isRefusal(err, "paused") {
		t.Fatalf("Crew's project paused: %v", err)
	}
	if got, _ := k.s.Get(k.ctx, b.Number); got.Status != Passed || reverts() != 0 {
		t.Fatalf("a refused take out changed something: %s, %d reverts", got.Status, reverts())
	}
	k.exec(`UPDATE projects SET state = 'running' WHERE id = $1`, crew.ID)
	got, err := k.s.Decide(k.ctx, b.Number, "take_out", "operator")
	if err != nil || got.Status != Removed || reverts() != 1 {
		t.Fatalf("taken out: %+v %v, %d reverts", got, err, reverts())
	}
	var kind, by string
	k.d.Server.QueryRow(k.ctx, `SELECT r.kind, r.created_by FROM tickets r JOIN ticket_links l ON l.from_ticket = r.id WHERE l.kind = 'reverts'`).Scan(&kind, &by)
	if kind != "revert" || got.DecidedBy == nil || *got.DecidedBy != "operator" {
		t.Fatalf("the revert ticket %s by %s, decided by %v", kind, by, got.DecidedBy)
	}
	// A press that made its revert and then failed is pressed again: the open revert is it.
	c, _ := merged("Plan with tests")
	if _, err := k.b.Revert(k.ctx, crew, *c.Ticket, "crew"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.s.Decide(k.ctx, c.Number, "take_out", "operator"); err != nil || got.Status != Removed || reverts() != 2 {
		t.Fatalf("with its revert open: %+v %v, %d reverts", got, err, reverts())
	}
	if got, _ := k.s.Get(k.ctx, a.Number); got.Status != Passed {
		t.Fatalf("the installed one: %s", got.Status)
	}
	// Refused, and nothing changed (security review and review of 1863a0e..f8d57b2): while a release
	// installs; for a ticket Done with no merge Crew recorded, so what it merged is not known; and where
	// the history of an installed release cannot be read.
	d, cd := merged("Plan with notes")
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state) VALUES ('1.3.0', $1, 's', '{}', now(), 'text', 'installing')`, cd)
	if _, err := k.s.Decide(k.ctx, d.Number, "take_out", "operator"); !isRefusal(err, "installs") {
		t.Fatalf("while a release installs: %v", err)
	}
	k.exec(`UPDATE releases SET state = 'kept' WHERE version = '1.3.0'`)
	k.exec(`DELETE FROM check_runs WHERE commit = $1`, cd)
	if _, err := k.s.Decide(k.ctx, d.Number, "take_out", "operator"); !isRefusal(err, "no merge") {
		t.Fatalf("a Done ticket with no merge recorded: %v", err)
	}
	e, _ := merged("Plan with lists")
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, installed_at) VALUES ('1.4.0', $1, 's', '{}', now(), 'text', now())`,
		strings.Repeat("e", 40))
	if _, err := k.s.Decide(k.ctx, e.Number, "take_out", "operator"); !isRefusal(err, "could not read whether release 1.4.0 holds") {
		t.Fatalf("a history that cannot be read: %v", err)
	}
	for _, x := range []*Improvement{d, e} {
		if got, _ := k.s.Get(k.ctx, x.Number); got.Status != Passed {
			t.Fatalf("a refused take out changed i_%04d: %s", x.Number, got.Status)
		}
	}
	if reverts() != 2 {
		t.Fatalf("%d reverts after the refusals", reverts())
	}
	// The take-out waits for the release clock's lock, and reads the improvement's status again under
	// its row: one that turned applied meanwhile is refused, with no revert made (round 2 of the security
	// review of 1863a0e..f8d57b2).
	k.exec(`DELETE FROM releases WHERE version = '1.4.0'`)
	f, _ := merged("Plan with maps")
	clock, err := k.d.Server.Begin(k.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer clock.Rollback(k.ctx)
	if _, err := clock.Exec(k.ctx, `SELECT pg_advisory_xact_lock(hashtext('crew.releases'))`); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(k.ctx, 300*time.Millisecond)
	_, err = k.s.Decide(short, f.Number, "take_out", "operator")
	cancel()
	if err == nil || reverts() != 2 {
		t.Fatalf("the take-out did not wait for the clock: %v, %d reverts", err, reverts())
	}
	clock.Rollback(k.ctx)
	installer, err := k.d.Server.Begin(k.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Rollback(k.ctx)
	if _, err := installer.Exec(k.ctx, `UPDATE improvements SET status = 'applied' WHERE id = $1`, f.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := k.s.Decide(k.ctx, f.Number, "take_out", "operator")
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if err := installer.Commit(k.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !isRefusal(err, "applied") || reverts() != 2 {
		t.Fatalf("one applied meanwhile: %v, %d reverts", err, reverts())
	}
}

// isRefusal says err is the improvements' refusal, and that its words hold what.
func isRefusal(err error, what string) bool {
	var r *Refusal
	return errors.As(err, &r) && strings.Contains(r.Msg, what)
}

// commitOnMain commits a file named after title on the project's main, as a merge moves it, and
// returns the commit.
func (k *kit) commitOnMain(project, title string) string {
	k.t.Helper()
	work := k.t.TempDir()
	git := func(args ...string) string {
		k.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", k.b.Git.Canonical(project), ".")
	if err := os.WriteFile(filepath.Join(work, strings.ReplaceAll(title, " ", "-")+".txt"), []byte(title), 0o644); err != nil {
		k.t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", title)
	git("push", "-q", "origin", "HEAD:main")
	return git("rev-parse", "HEAD")
}
