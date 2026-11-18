package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// The tests of M16's first phase (design 0.51): an eval case's fit check, the kept output of a failed
// held-out check, and an eval that ends stopping its runs.

// fitCase is an eval case as GET /evals gives it.
type fitCase struct {
	Number   int
	Origin   string
	Status   string
	Revision int
	Fit      struct {
		State   string
		List    []string
		Why     string
		Builder string
		Judge   string
		Run     string
	}
}

func (k *ticketKit) cases() map[int]fitCase {
	var ev struct{ Cases []fitCase }
	k.call("GET", "/api/projects/shop/evals", "", 200, &ev)
	out := map[int]fitCase{}
	for _, c := range ev.Cases {
		out[c.Number] = c
	}
	return out
}

// needShop is shop with the tickets given Done, each pushing only its own n.txt with the lines given:
// a held-out file that needs what each "need: X" line names.
// maxFitHeldOut is the pipeline's bound of the held-out files a fit check's judge reads.
const maxFitHeldOut = 120_000

func (k *ticketKit) needShop(needs ...string) {
	k.t.Helper()
	titles := []string{"one", "two", "three"}[:len(needs)]
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind != "build" {
			return false
		}
		n := strings.TrimPrefix(key, "SF-")
		i := int(n[0] - '1')
		k.push(spec.Run, fmt.Sprintf("task/%s-%s", key, titles[i]), map[string]string{n + ".txt": needs[i]})
		k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
		k.tool(spec.Run, "request_move", map[string]any{})
		return true
	})
	k.shop([]string{}, nil, titles...)
	stop := k.run()
	k.until("every ticket Done", func() bool {
		for i := range titles {
			if k.stageOf(fmt.Sprintf("SF-%d", i+1)) != "done" {
				return false
			}
		}
		return true
	})
	stop()
}

// A proposed case waits for its fit check, whoever proposed it (9.6): the operator's, a fixed bug's,
// and the lead's each wait with what their held-out files need that their text does not state, read
// by a judge on the other family than the project's builder. Nothing waiting joins a suite. An edit
// of the text is checked again and, with nothing missing, the case is active; accept makes a waiting
// case active with its list; an active case's text is not changed.
func TestAProposedCaseWaitsForItsFitCheck(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: the type Counts\nneed: bufferSize\n", "need: Lines\n", "need: Words\n")
	ctx := context.Background()

	var c fitCase
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, &c)
	if c.Status != "waiting" || c.Fit.State != "pending" || c.Origin != "operator" {
		t.Fatalf("the operator's case: %+v", c)
	}
	var proj string
	k.d.Server.QueryRow(ctx, `SELECT id::text FROM projects WHERE name = 'shop'`).Scan(&proj)
	k.d.Server.Exec(ctx, `INSERT INTO bugs (project, number, title, severity, status, found_by, fix_ticket)
		VALUES ($1, 1, 'Lost', 'high', 'fixed', 'tester', (SELECT id FROM tickets WHERE project = $1 AND number = 2))`, proj)
	k.call("POST", "/api/projects/shop/evals/cases", `{"bug": 1, "heldOut": ["2.txt"], "why": "a bug that escaped"}`, 201, &c)
	if c.Status != "waiting" || c.Origin != "bug" {
		t.Fatalf("the bug's case: %+v", c)
	}
	// The lead's, as its propose_eval_case gives it to the board.
	p, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	lc, err := k.b.ProposeCase(ctx, p, board.NewCase{Ticket: "SF-3", HeldOut: []string{"3.txt"}, Why: "it needed rework"}, "lead · retrospective", "")
	if err != nil || lc.Status != "waiting" {
		t.Fatalf("the lead's case: %+v %v", lc, err)
	}
	k.call("POST", "/api/projects/shop/evals/run", "", 400, nil) // no case is active yet

	stop := k.run()
	k.until("three fit checks read", func() bool {
		for _, c := range k.cases() {
			if c.Fit.State != "checked" {
				return false
			}
		}
		return true
	})
	stop()
	want := map[int][]string{1: {"the type Counts", "bufferSize"}, 2: {"Lines"}, 3: {"Words"}}
	for n, c := range k.cases() {
		if c.Status != "waiting" || !slices.Equal(c.Fit.List, want[n]) {
			t.Fatalf("case %d: %s %q", n, c.Status, c.Fit.List)
		}
		// The judge is on another family than the builder (13.8), and is the run's own model.
		if c.Fit.Builder == "" || cli.Family(c.Fit.Builder) == cli.Family(c.Fit.Judge) {
			t.Fatalf("case %d: the builder %q and its judge %q", n, c.Fit.Builder, c.Fit.Judge)
		}
		var model string
		k.d.Server.QueryRow(ctx, `SELECT model FROM runs WHERE id::text = $1`, c.Fit.Run).Scan(&model)
		if cli.Family(model) != cli.Family(c.Fit.Judge) {
			t.Fatalf("case %d: the judge's run ran %q", n, model)
		}
	}
	k.mu.Lock()
	specs, prompts := append([]harness.RunSpec{}, k.specs["fit"]...), append([]string{}, k.prompts["fit"]...)
	k.mu.Unlock()
	if len(specs) != 3 {
		t.Fatalf("%d fit checks", len(specs))
	}
	for i, s := range specs {
		if !s.Judge || s.Tools || s.MCP != "" {
			t.Fatalf("a fit check's run: judge %v tools %v MCP %q", s.Judge, s.Tools, s.MCP)
		}
		if !strings.Contains(prompts[i], "need: ") || !strings.Contains(prompts[i], "The held-out test files") {
			t.Fatalf("a fit check without its held-out file: %s", prompts[i])
		}
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit' AND scope = 'eval'`); n != 3 {
		t.Fatalf("%d fit checks of the scope eval", n)
	}

	// The operator changes case 1's text: it is checked again, finds nothing missing, and is active.
	k.call("PUT", "/api/projects/shop/evals/cases/1", `{"title": "one", "why": "w",
		"what": "Return the type Counts, and read in blocks of bufferSize bytes.", "goal": "g"}`, 200, &c)
	if c.Status != "waiting" || c.Fit.State != "pending" || c.Revision != 2 {
		t.Fatalf("the changed case: %+v", c)
	}
	stop = k.run()
	k.until("case 1 checked again", func() bool { return k.cases()[1].Fit.State == "checked" })
	stop()
	if c := k.cases()[1]; c.Status != "active" || len(c.Fit.List) != 0 {
		t.Fatalf("case 1 after its edit: %+v", c)
	}
	k.mu.Lock()
	again := len(k.specs["fit"])
	k.mu.Unlock()
	if again != 4 {
		t.Fatalf("%d fit checks, not 4", again)
	}
	// Accept makes a waiting case active, with what its check found; an active case is not changed.
	k.call("POST", "/api/projects/shop/evals/cases/2/accept", "", 200, &c)
	if c.Status != "active" || !slices.Equal(c.Fit.List, []string{"Lines"}) {
		t.Fatalf("the accepted case: %+v", c)
	}
	k.call("POST", "/api/projects/shop/evals/cases/2/accept", "", 404, nil)
	k.call("PUT", "/api/projects/shop/evals/cases/1", `{"title": "one", "why": "w", "what": "w", "goal": "g"}`, 400, nil)
	k.call("PUT", "/api/projects/shop/evals/cases/3", `{"title": "", "why": "w", "what": "w", "goal": "g"}`, 400, nil)
	k.call("POST", "/api/projects/shop/evals/cases/3/retire", `{"why": "it measures guessing"}`, 200, nil)
	if n := k.count(`SELECT count(*) FROM changes WHERE tbl = 'eval_cases'`); n < 6 {
		t.Fatalf("%d changes of the cases recorded", n)
	}
	k.call("POST", "/api/projects/shop/evals/run", "", 202, nil)
	if n := k.count(`SELECT count(DISTINCT eval_case) FROM eval_runs`); n != 2 {
		t.Fatalf("the suite runs %d cases, not the 2 active", n)
	}
}

// While the project's builder's model cannot be read, a case waits as not checked, and no judge is
// started: with no family to avoid, it could be the builder's. The check runs once it can (9.6).
func TestAFitCheckWaitsForTheBuildersModel(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n")
	machine := k.pipe.Settings
	k.pipe.Settings = func(ctx context.Context) (settings.Doc, error) {
		doc, err := machine(ctx)
		if err != nil {
			return nil, err
		}
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		b := settings.Doc{}
		for key, v := range roles["builder"].(settings.Doc) {
			b[key] = v
		}
		b["model"] = []any{"agy/no-such-model"}
		roles["builder"] = b
		return doc, nil
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	stop := k.run()
	k.until("the case not checked", func() bool { return k.cases()[1].Fit.State == "unchecked" })
	stop()
	c := k.cases()[1]
	if c.Status != "waiting" || len(c.Fit.List) != 0 || !strings.HasPrefix(c.Fit.Why, "not checked: the project's builder's model cannot be read") {
		t.Fatalf("the case: %+v", c)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit'`); n != 0 {
		t.Fatalf("%d fit checks started with no builder's model", n)
	}
	k.pipe.Settings = machine
	stop = k.run()
	k.until("the case checked", func() bool { return k.cases()[1].Fit.State == "checked" })
	stop()
	if c := k.cases()[1]; c.Status != "waiting" || !slices.Equal(c.Fit.List, []string{"Counts"}) {
		t.Fatalf("the case once checked: %+v", c)
	}
}

// An answer not in its form is asked for again; after three, the case waits for the operator with why.
func TestAFitCheckThatCannotBeReadWaitsForTheOperator(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n")
	k.fit = func(string) string { return "The case looks fine to me." }
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	stop := k.run()
	k.until("the check given up", func() bool { return k.cases()[1].Fit.State == "error" })
	stop()
	c := k.cases()[1]
	if c.Status != "waiting" || !strings.Contains(c.Fit.Why, "Crew could not read the fit check 3 times") {
		t.Fatalf("the case: %+v", c)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit'`); n != 3 {
		t.Fatalf("%d tries", n)
	}
}

// A case whose held-out file cannot be read is not given to a judge as an empty file: it ends waiting
// for the operator with why, and the case behind it is checked meanwhile. A judge that read only part
// of the files, which are longer than Crew gives one, makes no case active by itself (security review
// of P1).
func TestAFitCheckThatCannotSeeItsFilesActivatesNothing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n", strings.Repeat("x", maxFitHeldOut+10)+"\n", "nothing needed\n")
	ctx := context.Background()
	for _, n := range []string{"1", "2", "3"} {
		k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-`+n+`", "heldOut": ["`+n+`.txt"], "why": "typical"}`, 201, nil)
	}
	k.d.Server.Exec(ctx, `UPDATE eval_cases SET held_out = '{gone.txt}' WHERE number = 1`)
	stop := k.run()
	k.until("every case's check ended", func() bool {
		c := k.cases()
		return c[1].Fit.State == "error" && c[2].Fit.State == "checked" && c[3].Fit.State == "checked"
	})
	stop()
	c := k.cases()
	if c[1].Status != "waiting" || !strings.Contains(c[1].Fit.Why, "the held-out file gone.txt cannot be read") {
		t.Fatalf("the case with no file: %+v", c[1])
	}
	if c[2].Status != "waiting" || len(c[2].Fit.List) != 0 || !strings.Contains(c[2].Fit.Why, "read only part of the held-out files") {
		t.Fatalf("the case with a long file: %+v", c[2])
	}
	if c[3].Status != "active" {
		t.Fatalf("the fit case: %+v", c[3])
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit'`); n != 2 {
		t.Fatalf("%d judges started, not the 2 of the cases whose files were read", n)
	}
}

// With no model of another family than the project's builder offered to the judge, the fit check does
// not run on the builder's family: the case waits as not checked (rule 15; review of P1).
func TestAFitCheckRunsOnNoBuildersFamily(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.needShop("need: Counts\n")
	machine := k.pipe.Settings
	k.pipe.Settings = func(ctx context.Context) (settings.Doc, error) {
		doc, err := machine(ctx)
		if err != nil {
			return nil, err
		}
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		j := settings.Doc{}
		for key, v := range roles["judge"].(settings.Doc) {
			j[key] = v
		}
		// The builder's first model only: its list holds the other family too.
		j["model"] = []any{roles["builder"].(settings.Doc)["model"].([]any)[0]}
		roles["judge"] = j
		return doc, nil
	}
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	stop := k.run()
	k.until("the case not checked", func() bool { return k.cases()[1].Fit.State == "unchecked" })
	stop()
	if c := k.cases()[1]; c.Status != "waiting" || !strings.HasPrefix(c.Fit.Why, "not checked: no judge of another family than the project's builder") {
		t.Fatalf("the case: %+v", c)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'eval_fit'`); n != 0 {
		t.Fatalf("%d fit checks on the builder's family", n)
	}
}

// fitShop is shop with SF-1 Done, its case with 1.txt held out active after its fit check, and the
// knowledge settings and limits given.
func (k *ticketKit) fitShop(evals, limits map[string]any) {
	k.t.Helper()
	k.builder(nil)
	k.shop([]string{}, limits, "one")
	stop := k.run()
	k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
	k.call("POST", "/api/projects/shop/evals/cases", `{"ticket": "SF-1", "heldOut": ["1.txt"], "why": "typical"}`, 201, nil)
	k.until("the case active", func() bool { return k.cases()[1].Status == "active" })
	stop()
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	for key, v := range evals {
		doc["knowledge"].(map[string]any)["evals"].(map[string]any)[key] = v
	}
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)
}

// The output of a failed held-out check is kept on its eval run for the operator (9.6): each round's,
// redacted as a run's detail is, and cut to its last 64 KB. The builder learns only which commands
// failed.
func TestAFailedHeldOutCheckIsKeptForTheOperator(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.fitShop(map[string]any{"repeats": 1}, map[string]any{"max_fix_rounds": 1})
	k.evalScript(t)
	filler := strings.Repeat("x", 40<<10)
	k.f.Check = func(spec harness.CheckSpec, cmd harness.CheckCmd) harness.CheckResult {
		if slices.Contains(tarNames(spec.Tree), "eval-work.txt") {
			// A NUL and a rune the harness cut in two, which a text column refuses (review of P1).
			return harness.CheckResult{Code: 1, Output: "--- FAIL: TestCount\n    undefined: Counts\npassword: hunter2-but-longer\nbad: \xe2\x82\x00 end\n" + filler + "\nEND OF ROUND\n"}
		}
		return checkSandbox(spec, cmd)
	}
	k.call("POST", "/api/projects/shop/evals/run", "", 202, nil)
	stop := k.run()
	k.until("the suite done", func() bool { return k.count(`SELECT count(*) FROM eval_suites WHERE state = 'done'`) == 1 })
	stop()
	var suite int
	k.d.Server.QueryRow(context.Background(), `SELECT id FROM eval_suites`).Scan(&suite)
	var runs struct {
		Runs []struct {
			Case         int
			State        string
			GoalMet      *bool
			CheckRounds  int
			Why          string
			FailedOutput *string
		}
	}
	k.call("GET", fmt.Sprintf("/api/projects/shop/evals/runs?suite=%d", suite), "", 200, &runs)
	if len(runs.Runs) != 1 {
		t.Fatalf("the runs: %+v", runs)
	}
	r := runs.Runs[0]
	if r.State != "done" || r.GoalMet == nil || *r.GoalMet || r.FailedOutput == nil {
		t.Fatalf("the run: %+v", r)
	}
	out := *r.FailedOutput
	switch {
	case len(out) > 64<<10:
		t.Fatalf("%d bytes kept", len(out))
	case !strings.HasSuffix(out, "END OF ROUND\n") || !strings.Contains(out, "Check round 2:\n$ test -f 1.txt (exit 1)\n--- FAIL: TestCount"):
		t.Fatalf("the last round is not kept whole: …%s", out[max(0, len(out)-300):])
	case strings.Contains(out, "hunter2-but-longer") || !strings.Contains(out, "[redacted: secret assignment]"):
		t.Fatal("the kept output is not redacted")
	case !strings.Contains(out, "bad: \uFFFD end\n"):
		t.Fatal("the output's broken rune and NUL were not made storable")
	case strings.Contains(out, "Check round 1:"):
		t.Fatal("more than the last 64 KB is kept")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, p := range k.prompts["build"] {
		if strings.Contains(p, "undefined: Counts") {
			t.Fatal("the builder was shown the held-out check's output")
		}
	}
	k.call("GET", "/api/projects/shop/evals/runs?suite=99", "", 404, nil)
	k.call("GET", "/api/projects/shop/evals/runs", "", 400, nil)
}

// A suite that stops stops its eval's runs that still go, also one mid-turn, as a ticket's runs are
// stopped (9.6, since 0.51): the driver's End refused a run mid-turn, and it ran to its time limit.
func TestAStoppedSuiteLeavesNoRunGoing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.fitShop(map[string]any{"repeats": 1}, nil)
	prev := k.custom
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if strings.Contains(p, "this run is an eval of the project") {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl", Hold: true, Exit: runspec.Exit{Reason: "operator"}}, true
		}
		return prev(kind, spec, p)
	}
	// An override in testing gets its suite; the operator withdraws it while the eval's builder works.
	var id string
	k.d.Server.QueryRow(context.Background(), `SELECT id::text FROM projects WHERE name = 'shop'`).Scan(&id)
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO overrides (project, number, path, version, text, why, status, proposed_by)
		VALUES ($1, 1, 'stages/in_progress.md', 1, '# In progress: build one ticket', 'check rounds', 'testing', 'lead · run')`, id); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	defer stop()
	k.until("the eval's builder working", func() bool {
		return k.count(`SELECT count(*) FROM runs r JOIN eval_runs e ON e.builder_run = r.id WHERE r.state = 'working'`) == 1
	})
	k.d.Server.Exec(context.Background(), `UPDATE overrides SET status = 'withdrawn'`)
	k.until("the suite stopped and its builder ended", func() bool {
		return k.count(`SELECT count(*) FROM eval_suites WHERE state = 'stopped'`) == 1 &&
			k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.scope = 'eval' AND r.state <> 'ended'`) == 0
	})
	if n := k.count(`SELECT count(*) FROM runs r JOIN eval_runs e ON e.builder_run = r.id WHERE r.stop_reason = 'operator'`); n != 1 {
		t.Fatalf("%d builders stopped for the operator", n)
	}
}

// crewSource is kesokaj/crew on a fake GitHub, with a main of its own, and the server set to read it
// from there: Crew's project is imported from it with its history, and fetched from it over the fake
// SSH (16.2).
func (k *ticketKit) crewSource() string {
	k.t.Helper()
	return k.crewSourceWith(nil)
}

// crewSourceWith is crewSource with files, by their paths, in Crew's own history.
func (k *ticketKit) crewSourceWith(files map[string]string) string {
	k.t.Helper()
	dir := k.t.TempDir()
	gh := &fakeGitHub{root: filepath.Join(dir, "github"), ssh: filepath.Join(dir, "ssh")}
	os.WriteFile(gh.ssh, []byte(fakeSSH), 0o755)
	bare := filepath.Join(gh.root, "kesokaj", "crew.git")
	work := filepath.Join(dir, "work")
	for _, args := range [][]string{{"init", "-q", "--bare", bare}, {"init", "-q", "-b", "main", work}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			k.t.Fatalf("%v %s", err, out)
		}
	}
	os.WriteFile(filepath.Join(work, "README.md"), []byte("# Crew\n"), 0o644)
	os.WriteFile(filepath.Join(work, "go.mod"), []byte("module github.com/kesokaj/crew\n"), 0o644)
	for path, text := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, path)), 0o755)
		os.WriteFile(filepath.Join(work, path), []byte(text), 0o644)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "Crew's own history"}, {"push", "-q", bare, "main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=op", "GIT_AUTHOR_EMAIL=op@x", "GIT_COMMITTER_NAME=op", "GIT_COMMITTER_EMAIL=op@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	k.b.GitHub = gh
	k.git.Protocols = "file"
	k.api.CrewSource = func(repo string) board.CrewSource {
		return board.CrewSource{Repo: repo, Clone: "file://" + filepath.Join(gh.root, repo+".git"), Remote: settings.CrewRemote(repo)}
	}
	return bare
}

// setMachine changes the machine's settings through the API, and expects code.
func (k *ticketKit) setMachine(code int, change func(doc map[string]any)) {
	k.t.Helper()
	var doc map[string]any
	k.call("GET", "/api/settings/machine", "", 200, &doc)
	change(doc)
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/settings/machine", string(b), code, nil)
}

// Make Crew's project (16.2): it imports updates.source_repo with its history as a running project
// whose remote is that repository, with no breakdown, and sets improvements.crew_project; a second is
// refused, and so is a crew_project or a source_repo that would part the two. The gate's fixture
// project crew-gate is made with it and is in no list. A passed improvement that waited for Crew's
// project becomes its ticket.
func TestMakeCrewsProject(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	bare := k.crewSource()
	k.call("POST", "/api/crew/project", "", 400, nil) // no source repository yet
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO improvements (number, title, status, target, patch, measure, waits_for)
		VALUES (1, 'x', 'passed', '{baseline/stages/in_progress.md}', '{"baseline/stages/in_progress.md": "x"}', 'first_request_pass_rate', 'crew_project')`); err != nil {
		t.Fatal(err)
	}
	k.setMachine(400, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "not a repository" })
	k.setMachine(200, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew" })

	// A press that made the project and not the setting is finished by the next (review of M16 P2).
	if _, err := k.b.MakeCrewProject(ctx, k.api.CrewSource("kesokaj/crew"), "", "op"); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Name, Key, State string
		Remote           *string
		Hidden           bool
	}
	k.call("POST", "/api/crew/project", "", 201, &p)
	if p.Name != "crew" || p.Key != "CREW" || p.State != "running" || p.Remote == nil || *p.Remote != "git@github.com:kesokaj/crew.git" || p.Hidden {
		t.Fatalf("Crew's project: %+v", p)
	}
	if k.git.Main(ctx, "crew") != gitOut(t, bare, "rev-parse", "main") {
		t.Fatal("Crew's project is not the source repository's main")
	}
	var cp struct{ Project, SourceRepo string }
	k.call("GET", "/api/crew/project", "", 200, &cp)
	if cp.Project != "crew" || cp.SourceRepo != "kesokaj/crew" {
		t.Fatalf("GET /api/crew/project: %+v", cp)
	}
	k.call("POST", "/api/crew/project", "", 400, nil) // one only
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN projects p ON p.id = s.project WHERE p.name = 'crew' AND s.kind = 'breakdown'`); n != 0 {
		t.Fatalf("%d breakdowns of Crew's project", n)
	}

	// The gate's project: in no list, with In progress, Review, and Test, 30 turns a run, and no budget.
	for _, route := range []string{"/api/projects", "/api/frame", "/api/overview"} {
		var list struct{ Projects []struct{ Name string } }
		k.call("GET", route, "", 200, &list)
		var names []string
		for _, x := range list.Projects {
			names = append(names, x.Name)
		}
		if slices.Contains(names, "crew-gate") || !slices.Contains(names, "crew") {
			t.Fatalf("the projects %s lists: %v", route, names)
		}
	}
	g, err := k.b.Project(ctx, "crew-gate")
	if err != nil || !g.Hidden || g.State != "running" || g.Key != "GATE" {
		t.Fatalf("crew-gate: %+v %v", g, err)
	}
	var gs map[string]any
	k.call("GET", "/api/projects/crew-gate/settings", "", 200, &gs)
	limits := gs["limits"].(map[string]any)
	if _, budget := limits["default_task_budget_usd"]; budget || limits["run_max_turns"] != 30.0 {
		t.Fatalf("crew-gate's limits: %v", limits)
	}
	// No credentials key: no run gets gcloud since 0.79, and the setting is retired.
	if sb := gs["sandbox"].(map[string]any); sb["setup"] != "" || sb["test"] != "go test ./..." || sb["network"] != "model_only" ||
		sb["credentials"] != nil {
		t.Fatalf("crew-gate's sandbox: %v", sb)
	}
	if mod, _ := k.git.FileAt(ctx, "crew-gate", k.git.Main(ctx, "crew-gate"), "go.mod"); !strings.HasPrefix(string(mod), "module crewgate") {
		t.Fatalf("crew-gate's go.mod: %q", mod)
	}
	tk := board.GateTicket(3)
	if len(tk.Checks) != 1 || !strings.Contains(tk.Checks[0].Spec, "./g3/") || !strings.Contains(tk.What, "g3/reverse_test.go") {
		t.Fatalf("the gate's ticket: %+v", tk)
	}
	var models struct {
		Projects []struct{ Name string }
	}
	k.call("GET", "/api/crew/models", "", 200, &models)
	for _, x := range models.Projects {
		if x.Name == "crew-gate" {
			t.Fatal("the Models screen lists crew-gate")
		}
	}

	// Either setting saved apart from the other is refused.
	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "repository": {"kind": "local"}}`, 201, nil)
	k.setMachine(400, func(doc map[string]any) { doc["improvements"].(map[string]any)["crew_project"] = "shop" })
	k.setMachine(400, func(doc map[string]any) { doc["improvements"].(map[string]any)["crew_project"] = "crew-gate" })
	k.setMachine(400, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "someone/else" })
	k.setMachine(200, func(doc map[string]any) { doc["improvements"].(map[string]any)["run_every_days"] = 8 })
	// Crew's project is not archived while it is named; a save that changes neither setting is not held
	// by the two (review of M16 P2).
	k.call("POST", "/api/projects/crew/archive", "", 400, nil)
	k.d.Server.Exec(ctx, `UPDATE projects SET remote = 'git@github.com:someone/else.git' WHERE name = 'crew'`)
	k.setMachine(200, func(doc map[string]any) { doc["improvements"].(map[string]any)["run_every_days"] = 9 })
	k.d.Server.Exec(ctx, `UPDATE projects SET remote = 'git@github.com:kesokaj/crew.git' WHERE name = 'crew'`)

	// The improvement that waited becomes a ticket on Crew's project.
	svc := &improve.Service{Pool: k.d.Server, Board: k.b, Running: "0.0.0-test",
		Settings: func(ctx context.Context) (settings.Doc, error) { return k.pipe.Settings(ctx) }}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	x, _ := svc.Get(ctx, 1)
	if x.Ticket == nil || *x.Ticket != "CREW-1" || x.WaitsFor != nil {
		t.Fatalf("the improvement: ticket %v, waits for %v", x.Ticket, x.WaitsFor)
	}
}

// Make Crew's project makes nothing when the gate's project cannot be made, for want of a git
// identity, and takes no project of the repository's name that is not that repository's (round 2 of the
// review of P2).
func TestMakeCrewsProjectRefusals(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.crewSource()
	k.setMachine(200, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew" })
	identity := k.b.Identity
	k.b.Identity = nil
	k.call("POST", "/api/crew/project", "", 400, nil)
	if n := k.count(`SELECT count(*) FROM projects WHERE name IN ('crew', 'crew-gate')`); n != 0 {
		t.Fatalf("%d projects made by a refused press", n)
	}
	k.b.Identity = identity
	k.call("POST", "/api/projects", `{"name": "crew", "key": "CR", "plan": "## Goal\n\nSomething else.\n", "repository": {"kind": "local"}}`, 201, nil)
	k.call("POST", "/api/crew/project", "", 409, nil)
	var crewProject *string
	k.d.Server.QueryRow(context.Background(), `SELECT doc->'improvements'->>'crew_project' FROM settings WHERE project IS NULL`).Scan(&crewProject)
	if crewProject != nil {
		t.Fatalf("crew_project is %q", *crewProject)
	}
}

// A push to Crew's source repository from outside, as the operator's coding agent makes until M20, is
// taken as Crew's project's main and checked, as any project's (16.2, section 21 row 11). Since 0.53
// Crew's project checks with make check, no setup, in a check sandbox in Sources, and its runs get no
// gcloud (the operator's choice of report 8).
func TestAnOutsidePushToCrewsProjectIsTakenAndChecked(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	bare := k.crewSource()
	k.setMachine(200, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew" })
	k.call("POST", "/api/crew/project", "", 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/crew/settings", "", 200, &doc)
	if sb, _ := doc["sandbox"].(map[string]any); sb["setup"] != "" || sb["test"] != "make check" || sb["check_network"] != "sources" ||
		sb["credentials"] != nil || sb["network"] != "inherit" {
		t.Fatalf("Crew's project's sandbox: %v", sb)
	}
	stop := k.run()
	pushed := outsidePush(t, bare, map[string]string{"NOTES.md": "from the operator's coding agent\n"})
	time.Sleep(300 * time.Millisecond)
	k.d.Server.Exec(ctx, `UPDATE projects SET fetched_at = NULL WHERE name = 'crew'`)
	k.pipe.Kick()
	k.until("Crew's project took the push and its tests passed", func() bool {
		return k.git.Main(ctx, "crew") == pushed && k.count(`SELECT count(*) FROM remote_mains WHERE remote = $1 AND kind = 'ahead' AND state = 'passed'`, pushed) == 1
	})
	stop()
	checks := append([]harness.CheckSpec(nil), k.f.Checks...)
	if len(checks) == 0 {
		t.Fatal("no check sandbox")
	}
	for _, c := range checks {
		if c.Mode != "sources" || c.Setup != "" || len(c.Cmds) != 1 || c.Cmds[0].Run != "make check" {
			t.Fatalf("Crew's project's check: mode %q, setup %q, %+v", c.Mode, c.Setup, c.Cmds)
		}
	}
}

// Build a release (16.2 step 3) is asked for on Versions, and a build's log is read with its version
// (16.2 step 6): a server with no sandboxes builds nothing, a bump is patch, minor, or major, and
// with no Crew's project there is nothing to build.
func TestABuildIsAskedForAndItsLogRead(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	vs := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1",
		Settings: func(context.Context) (settings.Doc, error) { return settings.Doc{}, nil }}
	k.api.Versions.Store(vs)
	k.call("POST", "/api/crew/versions/build", `{}`, 503, nil)
	vs.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1"}
	k.call("POST", "/api/crew/versions/build", `{"bump": "huge"}`, 400, nil)
	k.call("POST", "/api/crew/versions/build", `{"bump": "minor"}`, 422, nil)
	// A fault of the server's is no precondition of the operator's (review of M16 P3).
	vs.Settings = func(context.Context) (settings.Doc, error) { return nil, errors.New("the database is gone") }
	k.call("POST", "/api/crew/versions/build", `{}`, 500, nil)
	vs.Settings = func(context.Context) (settings.Doc, error) { return settings.Doc{}, nil }
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by, state, log, error, ended_at)
		VALUES ('0.2.2', $1, 'operator', 'failed', $2, 'the build ended with 2', now())`, strings.Repeat("c", 40), "step 1\nstep 2"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version string
		Build   *struct{ State, Log, Error, Commit string }
	}
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.Build == nil || got.Build.State != "failed" || got.Build.Log != "step 1\nstep 2" || got.Build.Error != "the build ended with 2" {
		t.Fatalf("%+v", got)
	}
	k.call("GET", "/api/crew/versions/0.9.9", "", 404, nil)
	k.call("GET", "/api/crew/versions/latest", "", 400, nil)
}

// Run the live gate (16.2 step 5) is the operator's press on a gate that waits for it, and the
// version's gate, with the runtime inputs that differ, is read with its build.
func TestRunTheLiveGateIsAPressOnAWaitingGate(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	vs := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1",
		Settings: func(context.Context) (settings.Doc, error) { return settings.Doc{}, nil }}
	k.api.Versions.Store(vs)
	k.call("POST", "/api/crew/versions/0.2.2/gate", "", 503, nil)
	vs.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1"}
	k.call("POST", "/api/crew/versions/latest/gate", "", 400, nil)
	k.call("POST", "/api/crew/versions/0.2.2/gate", `{}`, 409, nil)
	// The release, whose runtime image's inputs the press names by their hash.
	runtime := relFolder(t, dir, "0.2.2")
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by, state, ended_at) VALUES ('0.2.2', $1, 'operator', 'done', now())`,
		strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO gates (version, state, same_runtime) VALUES ('0.2.2', 'waiting', false)`); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Gate *struct{ State string } `json:"gate"`
	}
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.Gate == nil || got.Gate.State != "waiting" {
		t.Fatalf("%+v", got)
	}
	var g struct{ State, PressedBy string }
	k.call("POST", "/api/crew/versions/0.2.2/gate", `{"runtimeSha256": "`+strings.Repeat("0", 64)+`"}`, 409, nil)
	k.call("POST", "/api/crew/versions/0.2.2/gate", `{"runtimeSha256": "`+runtime+`"}`, 202, &g)
	if g.State != "live" || g.PressedBy != "operator" {
		t.Fatalf("%+v", g)
	}
	k.call("POST", "/api/crew/versions/0.2.2/gate", `{"runtimeSha256": "`+runtime+`"}`, 409, nil)
	// A release whose recorded hash of its runtime inputs is not theirs is refused, with why (security
	// review of ef7a82a..4a601e2).
	other := relFolder(t, dir, "0.2.3")
	path := filepath.Join(dir, "0.2.3", release.ManifestFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Repeat("e", 64)
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), other, bad, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO gates (version, state, same_runtime) VALUES ('0.2.3', 'waiting', false)`); err != nil {
		t.Fatal(err)
	}
	var refused struct {
		Error struct{ Message string } `json:"error"`
	}
	k.call("POST", "/api/crew/versions/0.2.3/gate", `{"runtimeSha256": "`+bad+`"}`, 409, &refused)
	if !strings.Contains(refused.Error.Message, "cannot be pressed") {
		t.Fatalf("%+v", refused)
	}
}

// The detail of a version (16.2 step 6, since 0.52) has its gate.json whatever its state, so a gate
// that failed shows, and where the gate's live part is: the real ticket's key and its stage on
// crew-gate, then its step; each project of the eval suites by its name, with its suite's state, what
// its next run waits for, and its verdict, or why it is not compared; and the suites' step.
func TestTheGateDetailShowsAFailedGateAndTheLivePart(t *testing.T) {
	t.Parallel()
	k, live, img := gateKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	vs := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1",
		Settings: func(context.Context) (settings.Doc, error) { return settings.Doc{}, nil }}
	vs.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1"}
	k.api.Versions.Store(vs)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := k.d.Server.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	type step struct {
		Name, Detail, Skipped, Hold string
		OK                          bool
	}
	type detail struct {
		Gate *struct {
			State    string
			LivePart *struct {
				Ticket, Stage string
				Real          *step
				Suites        []struct {
					Project, State, WaitFor, Verdict, Note string
					Suite                                  int64
				}
				SuitesStep *step
			}
		}
		GateFile *struct {
			Passed bool
			Live   string
			Steps  []step
		}
	}
	// A gate whose make check failed: Read leaves its gate.json out, the detail has it.
	relFolder(t, dir, "0.2.2")
	f, err := release.Read(filepath.Join(dir, "0.2.2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteGate(release.Gate{At: time.Now(), InstallTest: "skipped",
		Steps: []release.GateStep{{Name: "make check", OK: false, Detail: "it ended with 2"}}}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO gates (version, state, error) VALUES ('0.2.2', 'failed', 'the updater''s gate failed')`)
	var got detail
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.GateFile == nil || got.GateFile.Passed || len(got.GateFile.Steps) != 1 || got.GateFile.Steps[0].OK ||
		got.GateFile.Steps[0].Detail != "it ended with 2" || got.Gate == nil || got.Gate.LivePart != nil {
		t.Fatalf("%+v %+v", got.Gate, got.GateFile)
	}
	// 0.3.0's live part: its real ticket is on its way, at its stage on crew-gate.
	relFolder(t, dir, "0.3.0")
	if res, err := live.Step(ctx, versions.LiveStart{Version: "0.3.0", Image: img}); err != nil || res != nil {
		t.Fatalf("%+v %v", res, err)
	}
	got = detail{}
	k.call("GET", "/api/crew/versions/0.3.0", "", 200, &got)
	if got.Gate == nil || got.Gate.LivePart == nil || got.Gate.LivePart.Ticket != "GATE-1" || got.Gate.LivePart.Stage == "" ||
		got.Gate.LivePart.Stage != k.gateStage("GATE-1") || got.Gate.LivePart.Real != nil || got.Gate.LivePart.Suites != nil || got.GateFile != nil {
		t.Fatalf("%+v %+v", got.Gate, got.GateFile)
	}
	// Its real ticket passed; one project's suite runs and waits for a model, one is decided, one is
	// not compared.
	shop, tea, aaa := k.caseProject("shop", "running", 1), k.caseProject("tea", "running", 1), k.caseProject("aaa", "paused", 1)
	running := k.suiteOn(shop, "running", "0.2.1", "baseline", true, 1)
	exec(`UPDATE eval_suites SET wait_for = 'model' WHERE id = $1`, running)
	done := k.suiteOn(tea, "done", "0.2.1", "baseline", true, 1)
	exec(`UPDATE eval_suites SET result = '{"decision": {"verdict": "not_worse", "why": "1 of 1, after 1 of 1"}}' WHERE id = $1`, done)
	suites, _ := json.Marshal([]map[string]any{{"project": shop, "name": "shop", "suite": running}, {"project": tea, "name": "tea", "suite": done},
		{"project": aaa, "name": "aaa", "note": "the project is paused"}})
	exec(`UPDATE gates SET real_step = '{"step": {"name": "the real ticket", "ok": true, "detail": "GATE-1 reached Done on its checks"}, "costUsd": 1.5}',
		suites = $1 WHERE version = '0.3.0'`, suites)
	got = detail{}
	k.call("GET", "/api/crew/versions/0.3.0", "", 200, &got)
	lp := got.Gate.LivePart
	if lp == nil || lp.Real == nil || !lp.Real.OK || lp.Real.Name != "the real ticket" || lp.Real.Detail != "GATE-1 reached Done on its checks" ||
		len(lp.Suites) != 3 || lp.SuitesStep != nil {
		t.Fatalf("%+v", lp)
	}
	if s := lp.Suites[0]; s.Project != "shop" || s.Suite != running || s.State != "running" || s.WaitFor != "model" || s.Verdict != "" || s.Note != "" {
		t.Fatalf("%+v", s)
	}
	if s := lp.Suites[1]; s.Project != "tea" || s.Suite != done || s.State != "done" || s.Verdict != "not_worse" || s.WaitFor != "" {
		t.Fatalf("%+v", s)
	}
	if s := lp.Suites[2]; s.Project != "aaa" || s.Suite != 0 || s.State != "" || s.Note != "the project is paused" {
		t.Fatalf("%+v", s)
	}
	// The suites' step ended, and the gate file the updater wrote is pending until the result is in.
	exec(`UPDATE gates SET suites_step = '{"step": {"name": "the eval suites", "ok": true, "detail": "tea: not worse"}, "hold": "", "costUsd": 2}'
		WHERE version = '0.3.0'`)
	f, _ = release.Read(filepath.Join(dir, "0.3.0"))
	if err := f.WriteGate(release.Gate{At: time.Now(), InstallTest: "skipped", Live: release.LivePending,
		Steps: []release.GateStep{{Name: "make check", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	got = detail{}
	k.call("GET", "/api/crew/versions/0.3.0", "", 200, &got)
	if lp := got.Gate.LivePart; lp == nil || lp.SuitesStep == nil || !lp.SuitesStep.OK || lp.SuitesStep.Name != "the eval suites" ||
		got.GateFile == nil || got.GateFile.Live != release.LivePending {
		t.Fatalf("%+v %+v", got.Gate.LivePart, got.GateFile)
	}
}

// A gate a server before 0.53 recorded names what its live steps cost, in its row and in its folder's
// gate.json. Versions shows no amount since 0.53 (the operator's choice of report 9: Versions cuts it):
// neither the screen nor a version's detail carries one, nor costUsd, and the lines say what happened.
func TestVersionsShowNoAmountOfAGateRecordedBefore053(t *testing.T) {
	t.Parallel()
	k, _, _ := gateKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	vs := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Updater: silentUpdater{},
		Settings: func(context.Context) (settings.Doc, error) { return settings.Doc{}, nil }}
	vs.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1"}
	k.api.Versions.Store(vs)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := k.d.Server.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	relFolder(t, dir, "0.2.1")
	relFolder(t, dir, "0.2.2")
	f, err := release.Read(filepath.Join(dir, "0.2.2"))
	if err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(map[string]any{"version": "0.2.2", "manifestSha256": f.ManifestSHA256, "passed": true, "at": time.Now(),
		"installTest": "skipped", "steps": []map[string]any{{"name": "make check", "ok": true}}, "live": "passed", "costUsd": 1.13,
		"liveSteps": []map[string]any{{"name": "the real ticket on crew-gate", "ok": true, "detail": "GATE-1 reached Done on its checks, for $1.13"},
			{"name": "the eval suites", "ok": true, "detail": "tea: not worse: 2 of 2, after 2 of 2 ($0.84)"}}})
	if err := os.WriteFile(filepath.Join(f.Dir, release.GateFile), old, 0o644); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO gates (version, state, ticket, real_step, suites_step, live) VALUES ('0.2.2', 'done', 'GATE-1',
		'{"step": {"name": "the real ticket on crew-gate", "ok": false, "detail": "GATE-1 spent $2.41 of its $2.00"}, "costUsd": 2.41}',
		'{"step": {"name": "the eval suites", "ok": true, "detail": "tea: not worse: 2 of 2, after 2 of 2 ($0.84)"}, "costUsd": 0.84}',
		'{"live": "passed", "steps": [{"name": "the real ticket on crew-gate", "ok": true, "detail": "GATE-1 reached Done on its checks, for $1.13"}], "costUsd": 1.13, "version": "0.2.2"}')`)
	// The screen lists 0.2.2 with its gate, as the service records a folder (so the check below is not
	// of an empty list).
	if err := vs.Scan(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var screen struct {
		Releases []struct {
			Version string
			Gate    *struct{ LiveSteps []struct{ Detail string } }
		}
	}
	k.call("GET", "/api/crew/versions", "", 200, &screen)
	listed := false
	for _, r := range screen.Releases {
		if r.Version == "0.2.2" && r.Gate != nil && len(r.Gate.LiveSteps) == 2 && r.Gate.LiveSteps[0].Detail == "GATE-1 reached Done on its checks" {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("the screen does not list 0.2.2 with its gate: %+v", screen)
	}
	// A hold a server before 0.53 stored is shown in today's words, on Versions and in the inbox (round 2
	// of the review of a9532aa).
	exec(`UPDATE releases SET state = 'waiting', held = true, held_why = 'its live steps ran in a candidate runtime image whose code is not the running release''s, which reports what its runs cost: Install decides' WHERE version = '0.2.2'`)
	for _, path := range []string{"/api/crew/versions", "/api/inbox"} {
		var raw json.RawMessage
		k.call("GET", path, "", 200, &raw)
		if strings.Contains(string(raw), "what its runs cost") || !strings.Contains(string(raw), "what its runs did and said") {
			t.Fatalf("%s shows the hold as stored: %s", path, raw)
		}
	}
	amount := regexp.MustCompile(`\$\s*\d|costUsd`)
	for _, path := range []string{"/api/crew/versions/0.2.2", "/api/crew/versions"} {
		var raw json.RawMessage
		k.call("GET", path, "", 200, &raw)
		if m := amount.Find(raw); m != nil {
			t.Fatalf("%s shows %q: %s", path, m, raw)
		}
	}
	var got struct {
		Gate struct {
			LivePart struct{ Real, SuitesStep struct{ Detail string } }
			Live     struct{ Steps []struct{ Detail string } }
		}
		GateFile struct{ LiveSteps []struct{ Detail string } }
	}
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.Gate.LivePart.Real.Detail != "GATE-1 stopped at its budget (before 0.53)" || got.Gate.LivePart.SuitesStep.Detail != "tea: not worse: 2 of 2, after 2 of 2" ||
		len(got.Gate.Live.Steps) != 1 || got.Gate.Live.Steps[0].Detail != "GATE-1 reached Done on its checks" ||
		len(got.GateFile.LiveSteps) != 2 || got.GateFile.LiveSteps[0].Detail != "GATE-1 reached Done on its checks" {
		t.Fatalf("%+v", got)
	}
}

// A version's detail says what it holds (16.2 step 6, since 0.54): the tickets and the commits from
// outside since the release before, read from Crew's project for the commit of its folder or of its
// build; and its kind against the running release. The screen says whether a build runs, and whether
// this server builds at all, for Build a release.
func TestTheDetailSaysWhatAVersionHoldsAndTheScreenWhetherABuildRuns(t *testing.T) {
	t.Parallel()
	k, _, _ := gateKit(t)
	ctx := context.Background()
	dir := t.TempDir()
	crew := func(context.Context) (settings.Doc, error) {
		return settings.Doc{"improvements": settings.Doc{"crew_project": "crew"}}, nil
	}
	vs := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Updater: silentUpdater{}, Settings: crew}
	vs.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Git: k.git}
	k.api.Versions.Store(vs)
	relFolder(t, dir, "0.2.1")
	head := k.git.Main(ctx, "crew")
	d := filepath.Join(dir, "0.2.2")
	os.MkdirAll(filepath.Join(d, "images"), 0o755)
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(d, "images", n+".tar"), []byte(n), 0o644)
		imgs[n] = release.Image{Tag: n + ":0.2.2", ID: "sha256:" + n}
	}
	if _, err := release.Write(d, release.Facts{Version: "0.2.2", Commit: head, BuiltAt: time.Now(), Images: imgs}, map[string]string{"go.mod": strings.Repeat("1", 64)}, nil); err != nil {
		t.Fatal(err)
	}
	type changes struct {
		Since   *struct{ Version, Commit string }
		Tickets []struct{ Key, Title, Commit string }
		Outside []struct{ Commit, Subject string }
		Note    string
	}
	var got struct {
		Changes *changes
		Kind    *struct {
			Kind      string
			Changed   int
			Protected []string
		}
	}
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.Changes == nil || got.Changes.Note != "" || got.Changes.Since != nil || len(got.Changes.Tickets) != 0 || len(got.Changes.Outside) != 1 ||
		got.Changes.Outside[0].Commit != head || got.Changes.Outside[0].Subject != "Crew's own history" {
		t.Fatalf("changes %+v", got.Changes)
	}
	if got.Kind == nil || got.Kind.Kind != "code" || got.Kind.Changed != 1 || len(got.Kind.Protected) == 0 {
		t.Fatalf("kind %+v", got.Kind)
	}
	// A build with no folder yet: what it holds, from its commit, since 0.2.2, whose commit it is; no kind.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by, state) VALUES ('0.2.3', $1, 'operator', 'running')`, head); err != nil {
		t.Fatal(err)
	}
	got.Changes, got.Kind = nil, nil
	k.call("GET", "/api/crew/versions/0.2.3", "", 200, &got)
	if got.Changes == nil || got.Changes.Since == nil || got.Changes.Since.Version != "0.2.2" || len(got.Changes.Outside) != 0 || got.Kind != nil {
		t.Fatalf("a build's detail: %+v %+v", got.Changes, got.Kind)
	}
	var screen struct {
		Building string
		CanBuild bool
	}
	k.call("GET", "/api/crew/versions", "", 200, &screen)
	if screen.Building != "0.2.3" || !screen.CanBuild {
		t.Fatalf("screen %+v", screen)
	}
	// What it holds that cannot be read is a note, not a failed detail: the gate and the press stay
	// (reviews of 95d116d).
	broken := &versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Updater: silentUpdater{},
		Settings: func(context.Context) (settings.Doc, error) { return nil, errors.New("the settings are gone") }}
	broken.Builder = &versions.Builder{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Git: k.git}
	k.api.Versions.Store(broken)
	got.Changes, got.Kind = nil, nil
	k.call("GET", "/api/crew/versions/0.2.2", "", 200, &got)
	if got.Changes == nil || !strings.Contains(got.Changes.Note, "cannot be read") || got.Kind == nil {
		t.Fatalf("a detail whose history cannot be read: %+v %+v", got.Changes, got.Kind)
	}
	k.api.Versions.Store(vs)
	// A server with no builder, the gate's check server among them, builds nothing.
	k.api.Versions.Store(&versions.Service{Pool: k.d.Server, Releases: dir, Running: "0.2.1", Updater: silentUpdater{}, Settings: crew})
	screen.CanBuild = true
	k.call("GET", "/api/crew/versions", "", 200, &screen)
	if screen.CanBuild {
		t.Fatal("a server with no builder offers a build")
	}
}

// silentUpdater is an updater that does not answer, as the gate's check server has none.
type silentUpdater struct{}

func (silentUpdater) Status(context.Context) (updater.Status, error) {
	return updater.Status{}, errors.New("the updater does not answer")
}
func (silentUpdater) Install(context.Context, updater.Ask) (*updater.Request, error) {
	return nil, errors.New("the updater does not answer")
}
func (silentUpdater) Stop(context.Context) error { return errors.New("the updater does not answer") }

// relFolder writes a release folder of version under dir with runtime inputs, and returns their hash.
func relFolder(t *testing.T, dir, version string) string {
	t.Helper()
	d := filepath.Join(dir, version)
	os.MkdirAll(filepath.Join(d, "images"), 0o755)
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(d, "images", n+".tar"), []byte(n), 0o644)
		imgs[n] = release.Image{Tag: n + ":" + version, ID: "sha256:" + n}
	}
	m, err := release.Write(d, release.Facts{Version: version, BuiltAt: time.Now(), Images: imgs, Runtime: map[string]string{"versions.lock": strings.Repeat("2", 64)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m.RuntimeSHA256
}

// gateKit is a ticket kit with Crew's project and the gate's crew-gate made, a gate whose live part
// runs for 0.3.0, and the live steps on the kit's board and pipeline.
func gateKit(t *testing.T) (*ticketKit, *versions.LiveRunner, string) {
	return gateKitWith(t, nil)
}

// gateKitWith is gateKit with files in Crew's own history.
func gateKitWith(t *testing.T, files map[string]string) (*ticketKit, *versions.LiveRunner, string) {
	k := newTicketKit(t)
	k.crewSourceWith(files)
	k.setMachine(200, func(doc map[string]any) { doc["updates"].(map[string]any)["source_repo"] = "kesokaj/crew" })
	k.call("POST", "/api/crew/project", "", 201, nil)
	// No setup step in the fake check sandbox, as for the kit's other projects.
	var doc map[string]any
	k.call("GET", "/api/projects/crew-gate/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = ""
	body, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/crew-gate/settings", string(body), 200, nil)
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO gates (version, state, same_runtime) VALUES ('0.3.0', 'live', true)`); err != nil {
		t.Fatal(err)
	}
	live := &versions.LiveRunner{Pool: k.d.Server}
	live.Use(versions.LiveDeps{Board: k.b, Kick: func() { k.b.Kick(); k.pipe.Kick() },
		SignedIn: func(context.Context) []string { return []string{"agy"} }})
	return k, live, "sha256:" + strings.Repeat("c", 64)
}

func (k *ticketKit) gateStage(key string) string {
	var stage string
	k.d.Server.QueryRow(context.Background(), `SELECT t.stage FROM tickets t JOIN projects p ON p.id = t.project
		WHERE p.name = 'crew-gate' AND p.key || '-' || t.number = $1`, key).Scan(&stage)
	return stage
}

// The gate's real ticket (16.2 step 5): it is added to crew-gate, whose sandboxes start from the
// candidate's runtime image while it runs, goes through the board's stages with the agents of any
// project, and passes when the board moves it to Done on its checks; the image is taken away again,
// and a release that changes nothing agents see runs no suite.
func TestTheGatesRealTicketPassesOnDone(t *testing.T) {
	t.Parallel()
	k, live, img := gateKit(t)
	ctx := context.Background()
	var gateID string
	k.d.Server.QueryRow(ctx, `SELECT id::text FROM projects WHERE name = 'crew-gate'`).Scan(&gateID)
	k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
		if spec.Project != gateID {
			return harness.Script{}, false
		}
		switch kind {
		case "analysis", "analysis-again":
			k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "approach": "Add g1/reverse.go and its test.",
				"checks": []map[string]any{{"kind": "cmd", "spec": "test -f g1/reverse.go"}}, "docsReason": "The fixture has no pages."})
		case "build", "build-again", "fix":
			k.pushIn("crew-gate", spec.Run, spec.Branch, map[string]string{"g1/reverse.go": "package g1\n", "g1/reverse_test.go": "package g1\n"})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Added Reverse and its table test."})
			k.tool(spec.Run, "request_move", map[string]any{})
		case "review":
			k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": board.VerdictApprove, "text": "Reverse and its test are there."}))
		case "test":
			k.tool(spec.Run, "add_test_result", map[string]any{"result": "pass", "text": "go test ./g1/ passes."})
		default:
			return harness.Script{}, false
		}
		return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}, true
	}
	l := versions.LiveStart{Version: "0.3.0", Image: img}
	if res, err := live.Step(ctx, l); err != nil || res != nil {
		t.Fatalf("the first step: %+v %v", res, err)
	}
	var named *string
	var ticket string
	k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named)
	k.d.Server.QueryRow(ctx, `SELECT coalesce(ticket, '') FROM gates WHERE version = '0.3.0'`).Scan(&ticket)
	if named == nil || *named != img || ticket != "GATE-1" {
		t.Fatalf("crew-gate's image %v, the ticket %q", named, ticket)
	}
	stop := k.run()
	k.until("GATE-1 Done", func() bool { return k.gateStage("GATE-1") == "done" })
	// The summary follows Done; waited for, so the runs read below are the same on every machine.
	k.until("GATE-1's summary", func() bool {
		return k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project
			WHERE p.name = 'crew-gate' AND s.kind = 'summary' AND r.state = 'ended'`) > 0
	})
	stop()
	res, err := live.Step(ctx, l)
	if err != nil || res == nil || res.Live != release.LivePassed || len(res.Steps) != 2 || !res.Steps[0].OK ||
		!strings.Contains(res.Steps[0].Detail, "GATE-1 reached Done on its checks") || res.Steps[1].Detail != "no eval: nothing the agents see changed" {
		t.Fatalf("%+v %v", res, err)
	}
	k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named)
	if named != nil {
		t.Fatalf("crew-gate keeps the candidate's image: %s", *named)
	}
	// Every agent run of the ticket ran in the candidate's image; its summary, a run of the judge kind,
	// in the running release's, as its sandbox does (13.4, S14-D6).
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project
		WHERE p.name = 'crew-gate' AND s.ticket IS NOT NULL AND s.kind NOT IN ('summary', 'judge') AND r.image <> $1`, img); n != 0 ||
		k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project
		WHERE p.name = 'crew-gate' AND s.ticket IS NOT NULL AND s.kind NOT IN ('summary', 'judge')`) == 0 {
		var other []string
		rows, _ := k.d.Server.Query(ctx, `SELECT s.kind || '/' || s.role || ' ' || r.state || ' ' || r.image || ' ' || r.started_at::text
			FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project
			WHERE p.name = 'crew-gate' AND s.ticket IS NOT NULL AND s.kind NOT IN ('summary', 'judge') AND r.image <> $1`, img)
		for rows != nil && rows.Next() {
			var l string
			rows.Scan(&l)
			other = append(other, l)
		}
		t.Fatalf("%d runs of GATE-1 in another image: %v", n, other)
	}
	if n := k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project
		WHERE p.name = 'crew-gate' AND s.kind = 'summary' AND r.image <> 'crew-runtime:test'`); n != 0 {
		t.Fatalf("%d summary runs of GATE-1 recorded another image than the running release's", n)
	}
	// Once ended, the step is its result, round after round.
	if again, err := live.Step(ctx, l); err != nil || again.Live != release.LivePassed {
		t.Fatalf("%+v %v", again, err)
	}
}

// The real ticket fails on a stop, or no Done in its time, and is cancelled so nothing of it runs on;
// it is skipped when no agent CLI is signed in, or after six hours at a limit; each time the
// candidate's image is taken from crew-gate (16.2 step 5). Since 0.53 it has no budget.
func TestTheGatesRealTicketFailsOrIsSkipped(t *testing.T) {
	t.Parallel()
	type state struct {
		live, detail, skipped string
		cancelled             bool
	}
	for name, tc := range map[string]struct {
		signedOut bool
		after     func(k *ticketKit, ticket int64)
		want      state
	}{
		"signed out before": {signedOut: true, want: state{live: release.LiveSkipped, skipped: "no agent CLI is signed in"}},
		"stopped": {after: func(k *ticketKit, ticket int64) { gateRun(k, ticket, "stopped", "operator", 0.1) },
			want: state{live: release.LiveFailed, detail: "a run of GATE-1 was stopped", cancelled: true}},
		"no Done in its time": {after: func(k *ticketKit, ticket int64) {
			k.d.Server.Exec(context.Background(), `UPDATE gates SET live_started_at = now() - interval '9 hours'`)
		}, want: state{live: release.LiveFailed, detail: "GATE-1 did not reach Done in 8h0m0s", cancelled: true}},
		"six hours at a limit": {after: func(k *ticketKit, ticket int64) {
			gateRun(k, ticket, "waiting", "quota:agy", 0.1)
			k.d.Server.Exec(context.Background(), `UPDATE gates SET limit_since = now() - interval '7 hours'`)
		}, want: state{live: release.LiveSkipped, skipped: "it waited at a limit for 6h0m0s", cancelled: true}},
	} {
		t.Run(name, func(t *testing.T) {
			k, live, img := gateKit(t)
			ctx := context.Background()
			if tc.signedOut {
				live.Use(versions.LiveDeps{Board: k.b, SignedIn: func(context.Context) []string { return nil }})
			}
			l := versions.LiveStart{Version: "0.3.0", Image: img, Agents: true}
			res, err := live.Step(ctx, l)
			if tc.after != nil {
				var ticket int64
				if err := k.d.Server.QueryRow(ctx, `SELECT t.id FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'crew-gate'`).Scan(&ticket); err != nil {
					t.Fatal(err)
				}
				tc.after(k, ticket)
				// A limit is first seen, then waited out.
				if res, err = live.Step(ctx, l); res == nil && err == nil {
					res, err = live.Step(ctx, l)
				}
			}
			if err != nil || res == nil || res.Live != tc.want.live {
				t.Fatalf("%+v %v", res, err)
			}
			if tc.want.detail != "" && res.Steps[0].Detail != tc.want.detail {
				t.Fatalf("detail %q", res.Steps[0].Detail)
			}
			if tc.want.skipped != "" && (len(res.Skipped) != 2 || res.Skipped[0] != "the real ticket on crew-gate: "+tc.want.skipped ||
				res.Skipped[1] != "the eval suites: "+tc.want.skipped) {
				t.Fatalf("skipped %q", res.Skipped)
			}
			if got := k.gateStage("GATE-1") == "closed"; got != tc.want.cancelled {
				t.Fatalf("GATE-1 is %q", k.gateStage("GATE-1"))
			}
			var named *string
			k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named)
			if named != nil {
				t.Fatalf("crew-gate keeps the candidate's image: %s", *named)
			}
		})
	}
}

// The real ticket has no budget (16.2 step 5, since 0.53): a run of it with a cost written on it, as
// runs before 0.53 had, past the $2 it had then, fails nothing; the ticket runs on.
func TestTheGatesRealTicketHasNoBudget(t *testing.T) {
	t.Parallel()
	k, live, img := gateKit(t)
	ctx := context.Background()
	l := versions.LiveStart{Version: "0.3.0", Image: img, Agents: true}
	if _, err := live.Step(ctx, l); err != nil {
		t.Fatal(err)
	}
	var ticket int64
	if err := k.d.Server.QueryRow(ctx, `SELECT t.id FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'crew-gate'`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	gateRun(k, ticket, "ended", "", 5)
	for i := 0; i < 2; i++ {
		if res, err := live.Step(ctx, l); err != nil || res != nil {
			t.Fatalf("a run's cost ended the step: %+v %v", res, err)
		}
	}
	if k.gateStage("GATE-1") == "closed" {
		t.Fatal("GATE-1 was cancelled")
	}
}

// gateRun is a run of the gate's ticket that ended as state says, with a cost written on it as runs
// had before 0.53.
func gateRun(k *ticketKit, ticket int64, state, waitFor string, cost float64) {
	k.t.Helper()
	ctx := context.Background()
	var sub int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, wait_for)
		SELECT 'ticket', t.project, t.id, 90, 'build', 'builder', $2, nullif($3, '') FROM tickets t WHERE t.id = $1 RETURNING id`,
		ticket, state, waitFor).Scan(&sub); err != nil {
		k.t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, cli, crew_version, image, cost_usd, state)
		VALUES (gen_random_uuid(), $1, 1, 'agy', '0.2.0', 'img', $2, 'ended')`, sub, cost); err != nil {
		k.t.Fatal(err)
	}
}

// More of the real ticket's ways (review of M16 P4c): a ticket that is stuck, waiting for a lead slot,
// goes on; a ticket whose key the gate recorded before a stop, unadded, is added once; a ticket the
// board closes fails the step; one whose CLI is signed out on its way is skipped; a limit that ends
// forgets its clock; the image of another gate is not this gate's to take; and with no board the
// live steps say so.
func TestTheGatesRealTicketsOtherWays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gateTicket := func(k *ticketKit) (id int64) {
		k.d.Server.QueryRow(ctx, `SELECT t.id FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'crew-gate' ORDER BY t.id DESC LIMIT 1`).Scan(&id)
		return id
	}
	t.Run("stuck goes on", func(t *testing.T) {
		k, live, img := gateKit(t)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		live.Step(ctx, l)
		k.d.Server.Exec(ctx, `UPDATE tickets SET stuck = 'its analysis waits for a lead slot of agy' WHERE id = $1`, gateTicket(k))
		if res, err := live.Step(ctx, l); err != nil || res != nil || k.gateStage("GATE-1") == "closed" {
			t.Fatalf("%+v %v %s", res, err, k.gateStage("GATE-1"))
		}
	})
	t.Run("recorded, not added", func(t *testing.T) {
		k, live, img := gateKit(t)
		k.d.Server.Exec(ctx, `UPDATE gates SET ticket = 'GATE-1', live_started_at = now() WHERE version = '0.3.0'`)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		if res, err := live.Step(ctx, l); err != nil || res != nil {
			t.Fatalf("%+v %v", res, err)
		}
		live.Step(ctx, l)
		if n := k.count(`SELECT count(*) FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'crew-gate'`); n != 1 || k.gateStage("GATE-1") == "" {
			t.Fatalf("%d tickets", n)
		}
	})
	t.Run("closed by the board", func(t *testing.T) {
		k, live, img := gateKit(t)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		live.Step(ctx, l)
		p, _ := k.b.Project(ctx, "crew-gate")
		if _, err := k.b.Cancel(ctx, p, "GATE-1", "the lead's look again stopped twice", "backend"); err != nil {
			t.Fatal(err)
		}
		if res, err := live.Step(ctx, l); err != nil || res == nil || res.Live != release.LiveFailed || res.Steps[0].Detail != "GATE-1 was closed: cancelled" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("signed out on its way", func(t *testing.T) {
		k, live, img := gateKit(t)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		live.Step(ctx, l)
		live.Use(versions.LiveDeps{Board: k.b, SignedIn: func(context.Context) []string { return nil }})
		if res, err := live.Step(ctx, l); err != nil || res == nil || res.Live != release.LiveSkipped || k.gateStage("GATE-1") != "closed" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("a limit that ends", func(t *testing.T) {
		k, live, img := gateKit(t)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		live.Step(ctx, l)
		gateRun(k, gateTicket(k), "waiting", "quota:agy", 0.1)
		live.Step(ctx, l)
		var since *time.Time
		k.d.Server.QueryRow(ctx, `SELECT limit_since FROM gates WHERE version = '0.3.0'`).Scan(&since)
		if since == nil {
			t.Fatal("the limit was not seen")
		}
		k.d.Server.Exec(ctx, `UPDATE subtasks SET state = 'done', wait_for = NULL WHERE ticket = $1`, gateTicket(k))
		live.Step(ctx, l)
		if k.d.Server.QueryRow(ctx, `SELECT limit_since FROM gates WHERE version = '0.3.0'`).Scan(&since); since != nil {
			t.Fatal("a limit that ended keeps its clock")
		}
	})
	t.Run("another gate's image", func(t *testing.T) {
		k, live, img := gateKit(t)
		l := versions.LiveStart{Version: "0.3.0", Image: img}
		live.Step(ctx, l)
		other := "sha256:" + strings.Repeat("d", 64)
		k.d.Server.Exec(ctx, `UPDATE projects SET runtime_image = $1 WHERE name = 'crew-gate'`, other)
		k.d.Server.Exec(ctx, `UPDATE gates SET live_started_at = now() - interval '9 hours'`)
		if res, _ := live.Step(ctx, l); res == nil || res.Live != release.LiveFailed {
			t.Fatalf("%+v", res)
		}
		var named *string
		if k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named); named == nil || *named != other {
			t.Fatalf("another gate's image was taken: %v", named)
		}
	})
	t.Run("no board", func(t *testing.T) {
		k, _, img := gateKit(t)
		bare := &versions.LiveRunner{Pool: k.d.Server}
		if _, err := bare.Step(ctx, versions.LiveStart{Version: "0.3.0", Image: img}); err == nil || !strings.Contains(err.Error(), "no board") {
			t.Fatalf("%v", err)
		}
	})
}

// Ending a gate's real ticket from outside its step cancels it and every other open ticket of
// crew-gate, and takes the gate's image; a ticket of crew-gate the gate did not add is not its real
// ticket, whatever its key (security review of M16 P4c, round 2).
func TestAbandonEndsTheGatesTickets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k, live, img := gateKit(t)
	l := versions.LiveStart{Version: "0.3.0", Image: img}
	live.Step(ctx, l)
	if _, err := k.b.AddTicket(ctx, "crew-gate", board.GateTicket(7), board.OriginAgent, "lead"); err != nil {
		t.Fatal(err)
	}
	if err := live.Abandon(ctx, "0.3.0", img, "its gate no longer waits for its live steps"); err != nil {
		t.Fatal(err)
	}
	if k.gateStage("GATE-1") != "closed" || k.gateStage("GATE-2") != "closed" {
		t.Fatalf("GATE-1 %s, GATE-2 %s", k.gateStage("GATE-1"), k.gateStage("GATE-2"))
	}
	var named *string
	if k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named); named != nil {
		t.Fatalf("the image stays: %s", *named)
	}

	k2, live2, img2 := gateKit(t)
	if _, err := k2.b.AddTicket(ctx, "crew-gate", board.GateTicket(1), board.OriginAgent, "lead"); err != nil {
		t.Fatal(err)
	}
	k2.d.Server.Exec(ctx, `UPDATE gates SET ticket = 'GATE-1', live_started_at = now() WHERE version = '0.3.0'`)
	live2.Step(ctx, versions.LiveStart{Version: "0.3.0", Image: img2})
	var ticket string
	k2.d.Server.QueryRow(ctx, `SELECT ticket FROM gates WHERE version = '0.3.0'`).Scan(&ticket)
	if ticket != "GATE-2" || k2.count(`SELECT count(*) FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = 'crew-gate' AND t.created_by = 'gate'`) != 1 {
		t.Fatalf("the gate's ticket is %q", ticket)
	}
}

// crew-gate, hidden, is Done after each gate's real ticket: it gets no retrospective and no final
// report, lead runs outside the gate's budget and report that would also propose eval cases on a
// project with no screen (gate 0.2.15 on dave: $0.58 after GATE-1). Its housekeeping stays (9.8): with
// feedback given after its Done, the pass that starts housekeeping has passed the retrospective and
// the report of that Done by, which is what the hidden project is read at. A project that is not
// hidden, the same way, gets both: the setup reaches them (reviews of the fix: no watch by time).
func TestAHiddenProjectGetsNoRetrospectiveAndNoFinalReport(t *testing.T) {
	t.Parallel()
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden=%v", hidden), func(t *testing.T) {
			ctx := context.Background()
			k := newTicketKit(t)
			k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
				if !strings.HasPrefix(kind, "report") {
					return false
				}
				k.tool(spec.Run, "write_report", map[string]any{"built": "One file.", "skipped": "Nothing.", "cancelled": "Nothing.",
					"missing": "Nothing.", "learned": "Nothing."})
				return true
			})
			k.shop([]string{}, nil, "one")
			if hidden {
				if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET hidden = true WHERE name = 'shop'`); err != nil {
					t.Fatal(err)
				}
			}
			stop := k.run()
			defer stop()
			k.until("the project is Done", func() bool {
				return k.count(`SELECT count(*) FROM projects WHERE name = 'shop' AND state = 'done'`) == 1
			})
			if !hidden {
				k.until("the retrospective and the final report", func() bool {
					return k.count(`SELECT count(*) FROM reports`) == 1 && k.count(`SELECT count(*) FROM retrospectives WHERE part IS NULL AND state = 'done'`) == 1
				})
				return
			}
			if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'housekeeping'`); n != 0 {
				t.Fatalf("%d housekeeping before the feedback", n)
			}
			if _, err := k.d.Server.Exec(ctx, `INSERT INTO feedback (project, number, from_role, about, kind, body)
				SELECT id, 1, 'reviewer', 'builder', 'correction', 'Test the failures.' FROM projects WHERE name = 'shop'`); err != nil {
				t.Fatal(err)
			}
			k.until("the hidden project's housekeeping", func() bool { return k.count(`SELECT count(*) FROM subtasks WHERE kind = 'housekeeping'`) == 1 })
			if n := k.count(`SELECT (SELECT count(*) FROM retrospectives) + (SELECT count(*) FROM reports) +
				(SELECT count(*) FROM subtasks WHERE kind IN ('retrospective', 'report'))`); n != 0 {
				t.Fatalf("a hidden project got %d rows of a retrospective or a final report", n)
			}
		})
	}
}

// A builder whose turn asked to move, and whose run then ended while it waited for Crew, is taken up
// as if its turn had just ended (since 0.55, the operator's answer 1 of report 10): its checks and its
// judge start, and the ticket goes on with no other turn of the builder. Two ways end such a run: its
// project paused before Crew acted on the turn, after which nothing started the judge (L4's CREW-1 on
// dave, S10-D11); and its idle time, past which the monitor ends a run while its checks still run. A
// criterion not met after the take-up is one more round, in a new microVM; at the round limit the run
// ends for its rounds, as one that waits would, and its subtask is retried once.
func TestABuilderWhoseRunEndedWhileItWaitedIsTakenUp(t *testing.T) {
	t.Parallel()
	for _, way := range []string{"pause", "long check", "not met", "round limit"} {
		t.Run(way, func(t *testing.T) {
			ctx := context.Background()
			k := newTicketKit(t)
			long := way != "pause"
			answers := []string{"1. MET: 1.txt says SF-1."}
			if way == "not met" || way == "round limit" {
				answers = []string{"1. NOT MET: 1.txt names nothing.", "1. MET: 1.txt names SF-1."}
			}
			var builds atomic.Int64
			// The monitor's clock, ahead by later: past a run's idle time without waiting for it.
			var later atomic.Int64
			k.driver.Now = func() time.Time { return time.Now().Add(time.Duration(later.Load())) }
			k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
				switch {
				case kind == "analysis":
					k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "docsReason": "none", "checks": []map[string]any{
						{"kind": "cmd", "spec": "test -f 1.txt"}, {"kind": "judge", "spec": "1.txt names its ticket."}}})
					return true
				case kind == "build" && way == "pause":
					k.pushFromRoot("task/SF-1-one", map[string]string{"1.txt": "SF-1"})
					k.tool(spec.Run, "add_note", map[string]any{"text": "Built SF-1"})
					k.tool(spec.Run, "request_move", map[string]any{})
					// The operator pauses the project before Crew acts on the turn.
					if _, err := k.b.SetState(ctx, "shop", "pause", "paused by the test", board.Operator); err != nil {
						t.Error(err)
					}
					return true
				case strings.HasPrefix(kind, "build") && builds.Add(1) > 1:
					// The round after the judge, or the retry after the round limit: the builder fixes it.
					k.pushMerged("task/SF-1-one", map[string]string{"1.txt": "SF-1 named"})
					k.tool(spec.Run, "add_note", map[string]any{"text": "1.txt names its ticket."})
					k.tool(spec.Run, "request_move", map[string]any{})
					return true
				}
				return false
			})
			inner := k.custom
			k.custom = func(kind string, spec harness.RunSpec, p string) (harness.Script, bool) {
				if kind == "judge" {
					k.mu.Lock()
					a := answers[0]
					if len(answers) > 1 {
						answers = answers[1:]
					}
					k.mu.Unlock()
					return harness.Script{Lines: answerLines(spec, a)}, true
				}
				return inner(kind, spec, p)
			}
			// The long check: the first check of the built head holds until the builder's run ended.
			release := make(chan struct{})
			var once sync.Once
			unhold := func() { once.Do(func() { close(release) }) }
			var holding atomic.Bool
			k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
				if long && slices.Contains(tarNames(spec.Tree), "1.txt") && holding.CompareAndSwap(false, true) {
					<-release
				}
				return checkSandbox(spec, c)
			}
			var limits map[string]any
			if way == "round limit" {
				limits = map[string]any{"max_goal_rounds": 1}
			}
			k.shop([]string{}, limits, "one")
			stop := k.run()
			defer stop()
			// Let go of the check on every way out, before stop waits for it (BUG-028's review).
			defer unhold()
			builder := `FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'build'`
			if way == "pause" {
				k.until("the builder's run idles in the paused project", func() bool {
					return k.count(`SELECT count(*) `+builder+` AND r.state = 'idle'`) == 1 &&
						k.count(`SELECT count(*) FROM projects WHERE name = 'shop' AND state = 'paused'`) == 1
				})
			} else {
				k.until("the check holds and the builder's run idles", func() bool {
					return holding.Load() && k.count(`SELECT count(*) `+builder+` AND r.state = 'idle'`) == 1
				})
			}
			later.Store(int64(11 * time.Minute))
			k.until("the monitor ended the builder's run", func() bool {
				return k.count(`SELECT count(*) `+builder+` AND r.state = 'ended' AND r.stop_detail = 'no next turn came'`) == 1
			})
			later.Store(0)
			if way == "pause" {
				if _, err := k.b.SetState(ctx, "shop", "resume", "", board.Operator); err != nil {
					t.Fatal(err)
				}
			} else {
				unhold()
			}
			k.until("SF-1 Done", func() bool { return k.stageOf("SF-1") == "done" })
			var judged string
			k.d.Server.QueryRow(ctx, `SELECT string_agg(state, ' ' ORDER BY id) FROM check_runs WHERE purpose = 'judge'`).Scan(&judged)
			switch way {
			case "pause", "long check":
				if n := k.count(`SELECT count(*) ` + builder); n != 1 || judged != "passed" {
					t.Fatalf("the builder had %d runs, not its one; the judge: %s", n, judged)
				}
				// Taken up once: its run says so, and is not taken up again while its ticket waits.
				// Wait for the mark: the board can move SF-1 to Done before the pipeline's next pass runs markMovedOn (BUG-028, internal/pipeline/takeup.go).
				k.until("the run taken up is marked so", func() bool {
					return k.count(`SELECT count(*) `+builder+` AND r.stop_detail = 'no next turn came; Crew took up its turn after'`) == 1
				})
			case "not met":
				k.mu.Lock()
				again := strings.Join(k.prompts["build-again"], "\n")
				k.mu.Unlock()
				if n := k.count(`SELECT count(*) ` + builder); n != 2 || judged != "failed passed" || !strings.Contains(again, "1.txt names nothing.") {
					t.Fatalf("the builder had %d runs, the judge %s; its round:\n%s", n, judged, again)
				}
			case "round limit":
				if n := k.count(`SELECT count(*) ` + builder + ` AND r.stop_reason = 'rounds' AND r.stop_detail = '1 rounds of checks failed'`); n != 1 {
					t.Fatalf("%d runs ended for their rounds", n)
				}
				if n := k.count(`SELECT count(*) FROM subtasks WHERE kind = 'build' AND stops = 1`); n != 1 || judged != "failed passed" {
					t.Fatalf("%d builders with one stop; the judge: %s", n, judged)
				}
			}
		})
	}
}

// Crew's project is never Done (since 0.55, the operator's answer 2 of report 10): it has no plan, and
// its board takes tickets for as long as Crew runs. Its last ticket's Done runs that ticket's own steps
// and no project's: no retrospective, no final report, and no tag done; it stays running, and the
// operator can pause it. On dave its one ticket made it Done, which ran all three, pushed the tag to
// GitHub, and left it unable to pause (S10-D13). Another project, the same way, is Done
// (TestAHiddenProjectGetsNoRetrospectiveAndNoFinalReport).
func TestCrewsProjectIsNeverDone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"crew_project": "shop"}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = settings.doc || EXCLUDED.doc`); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	k.until("SF-1 Done and its summary written", func() bool {
		return k.stageOf("SF-1") == "done" && k.count(`SELECT count(*) FROM subtasks WHERE kind = 'summary' AND state = 'done'`) == 1
	})
	// Passes go on after it: a Done project would be marked in the next.
	time.Sleep(time.Second)
	stop()
	if n := k.count(`SELECT count(*) FROM projects WHERE name = 'shop' AND state = 'running'`); n != 1 {
		var state string
		k.d.Server.QueryRow(ctx, `SELECT state FROM projects WHERE name = 'shop'`).Scan(&state)
		t.Fatalf("Crew's project is %s, not running", state)
	}
	if n := k.count(`SELECT (SELECT count(*) FROM retrospectives) + (SELECT count(*) FROM reports) +
		(SELECT count(*) FROM subtasks WHERE kind IN ('retrospective', 'report'))`); n != 0 {
		t.Fatalf("Crew's project got %d rows of a retrospective or a final report", n)
	}
	if tags, err := k.git.Tags(ctx, "shop"); err != nil || len(tags) != 0 {
		t.Fatalf("Crew's project was tagged: %v %v", tags, err)
	}
	k.call("POST", "/api/projects/shop/pause", `{}`, 200, nil)
}

// A release that measured worse than the one it replaced is one line of news on the Crew level, read
// from its row, so it comes once (10.7, since 0.57); one whose measure is no news, as the past is at
// schema step 67, and one that measured the same make none, and a project's activity has none.
func TestAWorseReleaseIsNewsOnce(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	worse := `{"before": "%s", "verdict": "worse", "why": "Checks passed on the first request went from 90%% to 40%%, past the margin of 10 points", "window": 20, "ticketsBefore": 20, "ticketsAfter": 20}`
	for _, r := range []struct {
		version, measure string
		news             bool
	}{
		{"0.2.0", fmt.Sprintf(worse, "0.1.9"), false},
		{"0.2.1", fmt.Sprintf(worse, "0.2.0"), true},
		{"0.2.2", `{"before": "0.2.1", "verdict": "same", "why": "the same as 0.2.1 over 20 tickets each", "window": 20}`, true},
	} {
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state, installed_at, measure, measured_at, measure_news)
			VALUES ($1, 'c', 'm', '{}', now(), 'code', 'kept', now(), $2, now(), $3)`, r.version, r.measure, r.news); err != nil {
			t.Fatal(err)
		}
	}
	var feed struct{ Lines []struct{ Kind, Text string } }
	k.call("GET", "/api/activity", "", 200, &feed)
	var news []string
	for _, l := range feed.Lines {
		if l.Kind == "news" && strings.Contains(l.Text, "measured") {
			news = append(news, l.Text)
		}
	}
	if len(news) != 1 || news[0] != "Crew 0.2.1 measured worse than 0.2.0 over 20 tickets each: Checks passed on the first request went from 90% to 40%, past the margin of 10 points. Crew takes out only an improvement by itself; this release's numbers are on Versions" {
		t.Fatalf("the news of the measures: %q", news)
	}
	k.shop([]string{}, nil, "one")
	k.call("GET", "/api/projects/shop/activity", "", 200, &feed)
	for _, l := range feed.Lines {
		if strings.Contains(l.Text, "measured worse") {
			t.Fatalf("a project's activity has the release's news: %q", l.Text)
		}
	}
}
