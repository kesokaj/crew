package runs

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
)

// The improver's run (10.4) belongs to no project and no draft: its subtask has the scope improver, its
// sandbox is the improver's, Model only whatever any setting says, with no repository and the Crew MCP;
// its token's caller is the Crew level's. A project's run never is, and no other role starts as one.
func TestTheImproversRunIsTheCrewLevels(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	if _, _, err := k.d.StartRole(ctx, RoleRun{Role: "improver", Kind: "improve", Improver: true, Project: "f1c50000-0000-4000-8000-000000000001", CLI: "agy",
		Parts: []PromptPart{{Part: "agent", Text: "x"}}}); err == nil {
		t.Fatal("an improver's run on a project started")
	}
	if _, _, err := k.d.StartRole(ctx, RoleRun{Role: "improver", Kind: "improve", Draft: testDraft, CLI: "agy", Parts: []PromptPart{{Part: "agent", Text: "x"}}}); err == nil {
		t.Fatal("the improver's role started without being the improver's run")
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "improver", Kind: "improve", Improver: true})
	v := k.wait(run, "working")
	s := started(k)[0]
	if !s.Improver || s.Repo != "" || s.Branch != "" || s.MCP != "/mcp" || len(s.Repos) != 0 || s.Project != "" || s.Mode != "model_only" {
		t.Fatalf("the spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND scope = 'improver' AND project IS NULL AND draft IS NULL AND role = 'improver'`, sub); n != 1 {
		t.Fatal("the subtask is not the improver's")
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || !c.Crew || c.Role != "improver" || c.Project != "" || c.Draft != "" {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	if _, err := k.d.GitAccess(mcpRequest("the-token"), "fixture"); err == nil {
		t.Fatal("the improver reached a repository")
	}
	k.d.Stop(ctx, run)
	k.wait(run, "ended")

	// A project's run's caller is not the Crew level's, whatever its role is called.
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run2 := k.start(StartRequest{})
	v2 := k.wait(run2, "working")
	k.d.Auth = fakeAuth{sandbox: v2.Sandbox}
	if c, err := k.d.MCPCaller(mcpRequest("the-token")); err != nil || c.Crew {
		t.Fatalf("a project's caller: %+v %v", c, err)
	}
	k.d.Stop(ctx, run2)
	k.wait(run2, "ended")
}

// A run of an eval reads its own branch only, in its workspace too: its spec says so, with no Crew MCP
// and not main's instructions (9.6; the live check of M15 on dave found the workspace fetching main).
func TestAnEvalRunsSpecReadsItsBranchOnly(t *testing.T) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	evalOn(t, k, "eval/7", "b45e0000")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, sub := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: "f1c50000-0000-4000-8000-000000000001", Branch: "eval/7", Base: "refs/heads/eval/7", Eval: true})
	k.wait(run, "working")
	s := started(k)[0]
	if !s.OwnBranchOnly || s.MCP != "" || s.MainInstructions || s.Branch != "eval/7" {
		t.Fatalf("an eval run's spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE id = $1 AND scope = 'eval'`, sub); n != 1 {
		t.Fatal("the subtask is not an eval's")
	}
	k.d.Stop(context.Background(), run)
	k.wait(run, "ended")
}

// evalOn records an eval run on branch, of a case whose base commit is base.
func evalOn(t *testing.T, k *kit, branch, base string) {
	t.Helper()
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	ctx := context.Background()
	var c, s int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO eval_cases (project, number, title, origin, base_commit, why, what, goal, proposed_by)
		VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM eval_cases WHERE project = $1), 'A case', 'operator', $2, 'why', 'what', 'goal', 'operator')
		RETURNING id`, fixture, base).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, asked_by) VALUES ($1, 'on_demand', 1, 1, 'operator')
		RETURNING id`, fixture).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO eval_runs (suite, project, eval_case, repeat, side, branch, crew_version)
		VALUES ($1, $2, $3, 1, 'baseline', $4, 'dev')`, s, fixture, c, branch); err != nil {
		t.Fatal(err)
	}
}

// An eval's reviewer is read-only on its branch with tools, no Crew MCP, and the instructions of its
// case's base commit, as the tester (design 0.50).
func TestAnEvalsReviewerHasToolsOnItsBranch(t *testing.T) {
	k := newKit(t)
	evalOn(t, k, "eval/8", "b45e0001")
	// Its builder's run, whose family it keeps off (checkguard.go).
	build, _ := k.startRole(RoleRun{Role: "builder", Kind: "eval_build", Project: "f1c50000-0000-4000-8000-000000000001", Branch: "eval/8", Eval: true,
		Model: "gemini-3.8-flash"})
	k.wait(build, "idle", "ended")
	if _, err := k.db.Server.Exec(context.Background(), `UPDATE eval_runs SET builder_run = $1 WHERE branch = 'eval/8'`, build); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "reviewer", Kind: "eval_review", Project: "f1c50000-0000-4000-8000-000000000001", Branch: "eval/8", ReadOnly: true, Eval: true,
		Model: "claude-opus-5-5"})
	k.wait(run, "working")
	s := started(k)[1]
	if s.Judge || !s.Tools || !s.ReadOnly || s.MCP != "" || s.Branch != "eval/8" || !s.OwnBranchOnly || s.MainInstructions || s.InstructionsAt != "b45e0001" {
		t.Fatalf("an eval reviewer's spec: %+v", s)
	}
}

// The tester of an eval takes its instructions from its case's base commit, and a restarted server
// follows it with the same spec: its branch only, and that commit (review of M15 after the live
// checks found the resumed spec reading main again). A run of an eval on a branch no eval run names
// does not start.
func TestAnEvalsTesterTakesTheBaseCommitsInstructionsAlsoAfterARestart(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	evalOn(t, k, "eval/8", "b45e0001")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "tester", Kind: "eval_test", Project: "f1c50000-0000-4000-8000-000000000001", Branch: "eval/8", ReadOnly: true, Eval: true})
	k.wait(run, "working")
	s := started(k)[0]
	if !s.OwnBranchOnly || !s.NoRepoConfig || s.MainInstructions || s.InstructionsAt != "b45e0001" {
		t.Fatalf("an eval tester's spec: %+v", s)
	}
	k.d.Close()
	d2 := k.driver()
	d2.Reconcile(ctx)
	defer d2.Close()
	if len(k.f.Resumes) != 1 || !k.f.Resumes[0].Spec.Git.OwnOnly || k.f.Resumes[0].Spec.Git.InstructionsAt != "b45e0001" || !k.f.Resumes[0].Spec.NoRepoConfig {
		t.Fatalf("the spec followed again: %+v", k.f.Resumes)
	}
	if _, _, err := d2.StartRole(ctx, RoleRun{Role: "tester", Kind: "eval_test", Project: "f1c50000-0000-4000-8000-000000000001", Branch: "eval/none", ReadOnly: true, Eval: true,
		CLI: "agy", Parts: []PromptPart{{Part: "agent", Source: "baseline/agents/tester.md@test", Text: "You are the tester."}}}); err == nil || !strings.Contains(err.Error(), "the eval case of branch eval/none") {
		t.Fatalf("a run of an eval on a branch no eval run names: %v", err)
	}
}
