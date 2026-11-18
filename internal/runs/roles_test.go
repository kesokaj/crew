package runs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runspec"
)

const testDraft = "d0000000-0000-4000-8000-0000000000aa"

func (k *kit) startRole(rr RoleRun) (string, int64) {
	k.t.Helper()
	if rr.CLI == "" {
		rr.CLI = "agy"
	}
	if len(rr.Parts) == 0 {
		rr.Parts = []PromptPart{{Part: "agent", Source: "baseline/agents/lead.md@test", Text: "You are the lead."},
			{Part: "operator", Source: "draft message 1", Text: "Plan a shop."}}
	}
	id, sub, err := k.d.StartRole(context.Background(), rr)
	if err != nil {
		k.t.Fatal(err)
	}
	return id, sub
}

func mcpRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestThePlanningAgentWorksOnItsDraftWithNoRepository(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id, repo) VALUES ($1, 'kesokaj/old-shop')`, testDraft); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft, Model: "claude-opus-5-5", Effort: "high", From: "machine"})
	v := k.wait(run, "working")
	s := started(k)[0]
	if s.Repo != "" || s.Branch != "" || s.MCP != "/mcp" || len(s.Repos) != 1 || s.Repos[0] != "kesokaj/old-shop" || !s.Minimal || s.Project != "" {
		t.Fatalf("the spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND scope = 'crew' AND draft = $2 AND project IS NULL AND role = 'planner'`, sub, testDraft); n != 1 {
		t.Fatal("the subtask does not belong to the draft")
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND model_from = 'machine' AND branch IS NULL`, run); n != 1 {
		t.Fatal("the run's model is not marked as the machine's, or it has a branch")
	}
	// The saved prompt names its parts with their sources (8.6).
	var parts []map[string]any
	var raw []byte
	k.db.Server.QueryRow(ctx, `SELECT parts FROM run_prompts WHERE run = $1`, run).Scan(&raw)
	json.Unmarshal(raw, &parts)
	if len(parts) != 2 || parts[0]["source"] != "baseline/agents/lead.md@test" || parts[1]["part"] != "operator" {
		t.Fatalf("the parts: %s", raw)
	}

	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Run != run || c.Role != "planner" || c.Draft != testDraft || c.Project != "" {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	if _, err := k.d.MCPCaller(mcpRequest("wrong")); err == nil {
		t.Fatal("a wrong token was a caller")
	}
	if _, err := k.d.GitAccess(mcpRequest("the-token"), "fixture"); err == nil {
		t.Fatal("a draft's run reached a repository")
	}

	// It continues in a new microVM with the same scope.
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
	run2, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "Add a part for payments."})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(run2, "working", "idle")
	s2 := started(k)[1]
	if s2.Repo != "" || s2.MCP != "/mcp" || len(s2.Repos) != 1 || !s2.Minimal {
		t.Fatalf("the spec of the continued run: %+v", s2)
	}
	// The token of the run that ended reaches nothing.
	if _, err := k.d.MCPCaller(mcpRequest("the-token")); err == nil {
		t.Fatal("the token of an ended run was a caller")
	}
}

func TestTheLeadWorksOnItsProjectsRepositoryOnly(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	const shop = "5b000000-0000-4000-8000-000000000001"
	for _, q := range []string{`INSERT INTO projects (id, name, key, repo_source) VALUES ($1, 'shop', 'SF', 'local')`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', $1)`} {
		if _, err := k.db.Server.Exec(ctx, q, shop); err != nil {
			t.Fatal(err)
		}
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "lead", Kind: "breakdown", Project: shop, ReadOnly: true})
	v := k.wait(run, "working")
	s := started(k)[0]
	// The lead reads main and pushes nothing (6.1).
	if s.Repo != "shop" || s.Branch != "main" || s.Base != "refs/heads/main" || !s.ReadOnly || s.MCP != "/mcp" || s.Project != shop || len(s.Repos) != 0 {
		t.Fatalf("the spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND scope = 'project' AND project = $2 AND draft IS NULL`, sub, shop); n != 1 {
		t.Fatal("the subtask does not belong to the project")
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Role != "lead" || c.Project != shop || c.Draft != "" {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	if _, err := k.d.GitAccess(mcpRequest("the-token"), "shop"); err != nil {
		t.Fatalf("its own repository: %v", err)
	}
	if _, err := k.d.GitAccess(mcpRequest("the-token"), "fixture"); err == nil {
		t.Fatal("the lead of shop reached the fixture's repository")
	}
}

func TestAFixtureRunHasNoCrewMCPYet(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	run := k.start(StartRequest{})
	k.wait(run, "idle", "ended")
	if s := started(k)[0]; s.MCP != "" {
		t.Fatalf("a builder's run got the Crew MCP before its tools exist: %+v", s)
	}
}

func TestARoleRunNamesOneProjectOrOneDraft(t *testing.T) {
	k := newKit(t)
	parts := []PromptPart{{Part: "x", Text: "x"}}
	for _, rr := range []RoleRun{
		{Role: "lead", Kind: "breakdown", CLI: "agy", Parts: parts},
		{Role: "lead", Kind: "breakdown", CLI: "agy", Parts: parts, Project: FixtureProject, Draft: testDraft},
		{Role: "lead", Kind: "breakdown", CLI: "agy", Project: FixtureProject},
	} {
		if _, _, err := k.d.StartRole(context.Background(), rr); err == nil {
			t.Errorf("%+v started", rr)
		}
	}
}

// What a turn left running in its sandbox cannot call the Crew MCP between turns.
func TestTheMCPAnswersARunOnlyWhileItsTurnRuns(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, testDraft); err != nil {
		t.Fatal(err)
	}
	run, _ := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft})
	v := k.wait(run, "idle")
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	if c, err := k.d.MCPCaller(mcpRequest("the-token")); err == nil {
		t.Fatalf("an idle run was a caller: %+v", c)
	}
}

// A draft's subtask that the project now counts keeps the draft's scope: a run of it gets neither the
// project's repository nor a token that names the project.
func TestADraftsRunNeverGetsTheProjectItBecame(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	const shop = "5b000000-0000-4000-8000-000000000002"
	for _, q := range []string{`INSERT INTO plan_drafts (id) VALUES ('` + testDraft + `')`,
		`INSERT INTO projects (id, name, key, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'local')`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', '` + shop + `')`} {
		if _, err := k.db.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	run, sub := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft})
	k.wait(run, "idle")
	// What takeDraft does when the project is made.
	k.db.Server.Exec(ctx, `UPDATE subtasks SET project = $1 WHERE id = $2`, shop, sub)
	k.d.Stop(ctx, run)
	k.wait(run, "ended")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run2, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	v := k.wait(run2, "working")
	if s := started(k)[1]; s.Repo != "" || s.Project != "" {
		t.Fatalf("the draft's run got the project: %+v", s)
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Project != "" || c.Draft != testDraft {
		t.Fatalf("the caller: %+v %v", c, err)
	}
}

// What a run takes is marked in the transaction that records it: when the mark fails, there is no
// run; when a turn is taken back, so is the mark.
func TestWhatARunTakesIsMarkedWithIt(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, testDraft); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, pgx.Tx, string) error { return errors.New("taken by another") }
	if _, _, err := k.d.StartRole(ctx, RoleRun{Role: "planner", Kind: "planning", Draft: testDraft, CLI: "agy",
		Parts: []PromptPart{{Part: "x", Text: "x"}}, Record: fail}); err == nil {
		t.Fatal("a run started whose mark failed")
	}
	if n := k.count(`SELECT count(*) FROM runs`); n != 0 || len(started(k)) != 0 {
		t.Fatalf("%d runs recorded, %d started", n, len(started(k)))
	}
	var marks []string
	mark := func(_ context.Context, tx pgx.Tx, run string) error { marks = append(marks, "+"+run); return nil }
	unmark := func(_ context.Context, tx pgx.Tx, run string) error { marks = append(marks, "-"+run); return nil }
	run, sub := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft, Record: mark})
	k.wait(run, "idle")
	k.f.ContinueErr = errors.New("no")
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "next", Record: mark, Unrecord: unmark}); err == nil {
		t.Fatal("no error")
	}
	if strings.Join(marks, " ") != "+"+run+" +"+run+" -"+run {
		t.Fatalf("marks %v", marks)
	}
	if _, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "next", Record: fail}); err == nil {
		t.Fatal("a turn started whose mark failed")
	}
	if n := k.count(`SELECT count(*) FROM turns WHERE text = 'next'`); n != 0 {
		t.Fatalf("%d turns hold the text", n)
	}
}

// A turn that did not start because its run ended meanwhile takes back its mark and its turn, so the
// next way to continue gives what it was given.
func TestATurnWhoseRunEndedMeanwhileTakesBackItsMark(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, testDraft); err != nil {
		t.Fatal(err)
	}
	taken := ""
	mark := func(_ context.Context, tx pgx.Tx, run string) error {
		if taken != "" {
			return ErrBusy // as the planner's mark finds its messages taken
		}
		taken = run
		return nil
	}
	unmark := func(_ context.Context, tx pgx.Tx, run string) error { taken = ""; return nil }
	run, sub := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft})
	k.wait(run, "idle")
	// The microVM dies, and the run is ended, while the turn is being given.
	k.f.ContinueHook = func() { k.db.Server.Exec(ctx, `UPDATE runs SET state = 'ended' WHERE id = $1`, run) }
	k.f.ContinueErr = harness.ErrNoRun
	run2, err := k.d.Continue(ctx, sub, ContinueRequest{Text: "next", Record: mark, Unrecord: unmark})
	if err != nil || run2 == run || taken != run2 {
		t.Fatalf("the next way did not take the turn: %q %v, taken by %q", run2, err, taken)
	}
	if n := k.count(`SELECT count(*) FROM turns WHERE text = 'next' AND run = $1`, run); n != 0 {
		t.Fatalf("the turn that never ran stayed: %d", n)
	}
}

// A draft's run that a restarted server follows again keeps no repository and keeps the Crew MCP.
func TestARestartFollowsADraftsRunAsItWas(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, testDraft); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft})
	k.wait(run, "working")
	k.d.Close()
	d2 := k.driver()
	d2.MCP = "/mcp"
	d2.Reconcile(ctx)
	defer d2.Close()
	if len(k.f.Resumes) != 1 {
		t.Fatalf("%d runs followed again", len(k.f.Resumes))
	}
	if s := k.f.Resumes[0].Spec; !reflect.DeepEqual(s.Git, runspec.Git{}) || s.MCP != "/mcp" || s.Run != run {
		t.Fatalf("the spec followed again: %+v", s)
	}
}

// A project's lead run that a restarted server follows again keeps its repository and its branch.
func TestARestartFollowsAProjectsRunWithItsRepository(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	const shop = "5b000000-0000-4000-8000-000000000003"
	for _, q := range []string{`INSERT INTO projects (id, name, key, repo_source) VALUES ($1, 'shop', 'SF', 'local')`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', $1)`} {
		if _, err := k.db.Server.Exec(ctx, q, shop); err != nil {
			t.Fatal(err)
		}
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "lead", Kind: "breakdown", Project: shop, ReadOnly: true})
	k.wait(run, "working")
	k.d.Close()
	d2 := k.driver()
	d2.MCP = "/mcp"
	d2.Reconcile(ctx)
	defer d2.Close()
	want := runspec.Git{Path: "/git/shop.git", Branch: "main", Base: "refs/heads/main"}
	if len(k.f.Resumes) != 1 || !reflect.DeepEqual(k.f.Resumes[0].Spec.Git, want) || k.f.Resumes[0].Spec.MCP != "/mcp" {
		t.Fatalf("the spec followed again: %+v", k.f.Resumes)
	}
}

// A run that does not build, such as the lead's, gets a workspace without the repository's own CLI
// configuration; the fixture's builder keeps it, also after a restart.
func TestOnlyARunThatBuildsLoadsTheRepositorysConfiguration(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, testDraft); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	lead, _ := k.startRole(RoleRun{Role: "planner", Kind: "planning", Draft: testDraft})
	k.wait(lead, "working")
	k.start(StartRequest{})
	if s := started(k); !s[0].NoRepoConfig || s[1].NoRepoConfig {
		t.Fatalf("planner %v, builder %v", s[0].NoRepoConfig, s[1].NoRepoConfig)
	}
	k.d.Close()
	d2 := k.driver()
	d2.Reconcile(ctx)
	defer d2.Close()
	found := false
	for _, r := range k.f.Resumes {
		if r.Run == lead {
			found = true
			if !r.Spec.NoRepoConfig {
				t.Fatal("the planner's run followed again loads the repository's configuration")
			}
		}
	}
	if !found {
		t.Fatal("the planner's run was not followed again")
	}
}
