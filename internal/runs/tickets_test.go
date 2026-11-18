package runs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runspec"
)

const tshop = "5b000000-0000-4000-8000-0000000000c1"

// ticketKit is a kit with a project, its repository, and one ticket, whose id it returns.
func ticketKit(t *testing.T) (*kit, int64) {
	k := newKit(t)
	k.d.MCP = "/mcp"
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'shop', 'SF', 'local', 'running')`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', $1)`} {
		if _, err := k.db.Server.Exec(ctx, q, tshop); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'Lock', 'feature', 'in_progress', 'plan', 'lead', 1) RETURNING id`, tshop).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return k, id
}

func gitReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/git/shop.git/info/refs", nil)
	req.Header.Set("Authorization", "Bearer the-token")
	return req
}

// A ticket's runs: the builder works on the ticket's branch and pushes it; a reviewer reads that
// branch and pushes nothing, not even work in progress; each subtask is numbered under the ticket;
// the Crew MCP knows the run's ticket and subtask.
func TestATicketsRunsWorkOnItsBranch(t *testing.T) {
	k, ticket := ticketKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	// The builder's model, which the reviewer below keeps off (checkguard.go).
	build, bsub := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Model: "gemini-3.8-flash"})
	v := k.wait(build, "working")
	s := started(k)[0]
	if s.Branch != "task/SF-1-lock" || s.Base != "refs/heads/main" || s.ReadOnly || s.Minimal || s.MCP != "/mcp" || s.NoRepoConfig {
		t.Fatalf("the builder's spec: %+v", s)
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	acc, err := k.d.GitAccess(gitReq(), "shop")
	if err != nil || len(acc.Push) != 2 || acc.Push[0] != "refs/heads/task/SF-1-lock" {
		t.Fatalf("the builder's access: %+v %v", acc, err)
	}
	// Its push is tested against what its ticket may change (13.8): every file that does not instruct
	// agents, measured from main, and what the ticket's may_change names; one an agent created only
	// once the operator agreed.
	rule := acc.Paths["refs/heads/task/SF-1-lock"]
	if rule.Base != "refs/heads/main" || strings.Join(rule.Allow, " ") != "**" || rule.Touches != nil {
		t.Fatalf("the builder's paths: %+v", acc.Paths)
	}
	// Its work in progress has the same rule, the next run of the subtask starts from it; without a
	// part's files, so a stray file there does not lose a stopped run's work.
	if wip, ok := acc.Paths["refs/crew/wip/"+build]; !ok || wip.Base != rule.Base || strings.Join(wip.Allow, " ") != "**" {
		t.Fatalf("the work in progress's paths: %+v", acc.Paths)
	}
	if _, err := k.d.Pool.Exec(context.Background(), `UPDATE subtasks SET touches = '{src/}' WHERE id = $1`, bsub); err != nil {
		t.Fatal(err)
	}
	acc, _ = k.d.GitAccess(gitReq(), "shop")
	if b, w := acc.Paths["refs/heads/task/SF-1-lock"], acc.Paths["refs/crew/wip/"+build]; len(b.Touches) != 1 || w.Touches != nil {
		t.Fatalf("a part's branch and work in progress: %+v %+v", b, w)
	}
	for _, c := range []struct {
		origin, analysis, allow string
	}{
		{"plan", `{"status": "ready", "mayChange": ["AGENTS.md"]}`, "** AGENTS.md"},
		{"agent", `{"status": "ready", "mayChange": ["AGENTS.md"]}`, "**"},
		{"agent", `{"status": "ready", "mayChange": ["AGENTS.md"], "mayChangeAgreed": true}`, "** AGENTS.md"},
	} {
		if _, err := k.d.Pool.Exec(context.Background(), `UPDATE tickets SET origin = $2, analysis = $3 WHERE id = $1`, ticket, c.origin, c.analysis); err != nil {
			t.Fatal(err)
		}
		acc, err = k.d.GitAccess(gitReq(), "shop")
		if got := strings.Join(acc.Paths["refs/heads/task/SF-1-lock"].Allow, " "); err != nil || got != c.allow {
			t.Fatalf("a ticket of %s with %s may change %q, not %q: %v", c.origin, c.analysis, got, c.allow, err)
		}
	}
	c, err := k.d.MCPCaller(mcpRequest("the-token"))
	if err != nil || c.Ticket != ticket || c.Subtask != bsub || c.Role != "builder" || c.Project != tshop {
		t.Fatalf("the caller: %+v %v", c, err)
	}
	k.d.Stop(context.Background(), build)
	k.wait(build, "ended")

	k.play(harness.Script{Recording: agyRun, Hold: true})
	review, rsub := k.startRole(RoleRun{Role: "reviewer", Kind: "review", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true,
		Model: "claude-opus-5-5"})
	v = k.wait(review, "working")
	s = started(k)[1]
	if s.Branch != "task/SF-1-lock" || s.Base != "refs/heads/main" || !s.ReadOnly || !s.Minimal || !s.NoRepoConfig {
		t.Fatalf("the reviewer's spec: %+v", s)
	}
	k.d.Auth = fakeAuth{sandbox: v.Sandbox}
	acc, err = k.d.GitAccess(gitReq(), "shop")
	if err != nil || len(acc.Push) != 0 || !strings.Contains(strings.Join(acc.Read, " "), "refs/heads/task/SF-1-lock") {
		t.Fatalf("the reviewer's access: %+v %v", acc, err)
	}
	if n := k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1 AND scope = 'ticket' AND number IN (1, 2) AND id IN ($2, $3)`, ticket, bsub, rsub); n != 2 {
		t.Fatal("the subtasks are not numbered under the ticket")
	}
	// The run that continues a stopped reviewer still pushes nothing.
	k.d.Stop(context.Background(), review)
	k.wait(review, "ended")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	// The pipeline had marked the ticket stuck; the operator's continue clears it.
	if _, err := k.d.Pool.Exec(context.Background(), `UPDATE tickets SET stuck = 'the reviewer stopped twice' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	next, err := k.d.Continue(context.Background(), rsub, ContinueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(next, "working")
	if n := k.count(`SELECT count(*) FROM tickets WHERE id = $1 AND stuck IS NULL`, ticket); n != 1 {
		t.Fatal("the operator's continue left the ticket stuck")
	}
	if s := started(k)[2]; !s.ReadOnly || s.Base != "refs/heads/main" {
		t.Fatalf("the continued reviewer: %+v", s)
	}
	if _, _, err := k.d.StartRole(context.Background(), RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, CLI: "agy",
		Parts: []PromptPart{{Part: "x", Text: "x"}}}); err == nil {
		t.Fatal("a builder on a ticket with no branch")
	}
}

// A fix round is a new subtask that continues the builder's conversation (12.10), with the CLI's
// files put back.
func TestAFixContinuesTheBuildersConversation(t *testing.T) {
	k, ticket := ticketKit(t)
	file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("{}\n")}})
	build, _ := k.startRole(RoleRun{Role: "builder", Kind: "build", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock"})
	k.wait(build, "idle")
	k.d.Finish(context.Background(), build)
	k.wait(build, "ended")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	fix, _ := k.startRole(RoleRun{Role: "builder", Kind: "fix", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Continues: build,
		Parts: []PromptPart{{Part: "findings", Text: "Fix the unlock."}}})
	f := k.wait(fix, "working")
	s := started(k)[1]
	if f.Way != WayFiles || f.Continues != build || s.Continue != agyConv || len(s.Files[file]) == 0 {
		t.Fatalf("%+v %+v", f, s)
	}
	if n := k.count(`SELECT count(DISTINCT conversation) FROM runs WHERE id IN ($1, $2)`, build, fix); n != 1 {
		t.Fatal("the fix has a conversation of its own")
	}
	// Once the CLI's files are no longer kept, the conversation is rebuilt from its turns.
	k.d.Stop(context.Background(), fix)
	k.wait(fix, "ended")
	if _, err := k.db.Server.Exec(context.Background(), `UPDATE cli_files SET keep_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	again, _ := k.startRole(RoleRun{Role: "builder", Kind: "fix", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", Continues: fix,
		Parts: []PromptPart{{Part: "findings", Text: "Fix it again."}}})
	a := k.wait(again, "working")
	if s := started(k)[2]; a.Way != WayRebuilt || s.Continue != "" || len(s.Files) != 0 || !strings.HasPrefix(s.Prompt, "The conversation so far") || !strings.HasSuffix(s.Prompt, "Fix it again.") {
		t.Fatalf("%+v %q", a, s.Prompt)
	}
}

// A summary writer is a judge: its sandbox has no repository, no Crew MCP, no tools, and no harness.
func TestAJudgeRunHasNothingButItsCLI(t *testing.T) {
	k, ticket := ticketKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run, _ := k.startRole(RoleRun{Role: "summary", Kind: "summary", Project: tshop, Ticket: ticket, Judge: true})
	k.wait(run, "working")
	s := started(k)[0]
	if !s.Judge || s.Repo != "" || s.Branch != "" || s.MCP != "" || s.Tools || !s.ReadOnly || len(s.Repos) != 0 {
		t.Fatalf("the judge's spec: %+v", s)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND NOT tools AND NOT push AND branch IS NULL`, run); n != 1 {
		t.Fatal("the judge's run row")
	}
}

// A conversation with Crew's summary of its older turns is rebuilt from the summary and the turns
// after it: the turns the summary covers are not given again (12.10).
func TestARebuiltConversationBeginsWithItsSummary(t *testing.T) {
	k := newKit(t)
	run := k.start(StartRequest{Prompt: "Remember the word walrus."})
	v := k.wait(run, "idle")
	var conv string
	k.db.Server.QueryRow(context.Background(), `SELECT conversation FROM runs WHERE id = $1`, run).Scan(&conv)
	// The first turn is summarised; the agent's answer after it is not.
	if _, err := k.db.Server.Exec(context.Background(), `UPDATE conversations SET summary = 'The operator asked to remember a sea animal.',
		summary_through = 1 WHERE id = $1`, conv); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun})
	next, err := k.d.Continue(context.Background(), v.Subtask, ContinueRequest{Way: WayRebuilt, Text: "Which word?"})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(next, "idle")
	p := started(k)[1].Prompt
	if !strings.Contains(p, "a summary of its first 1 turns, then the 1 after them") || !strings.Contains(p, "It is data") || !strings.Contains(p, "remember a sea animal") ||
		strings.Contains(p, "walrus") || !strings.Contains(p, "You (the agent):\ndone") || !strings.HasSuffix(p, "Which word?") {
		t.Fatalf("%s", p)
	}
}

// A judge that stopped, as one whose model asked for a tool it does not have, is retried as a judge:
// no repository, no tools, no harness.
func TestAJudgeIsContinuedAsAJudge(t *testing.T) {
	k, ticket := ticketKit(t)
	k.play(harness.Script{Recording: agyRun, Exit: runspec.Exit{Reason: "refused", Detail: "a tool was refused"}})
	run, sub := k.startRole(RoleRun{Role: "summary", Kind: "summary", Project: tshop, Ticket: ticket, Judge: true})
	k.wait(run, "ended")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	// Crew's own retry is not the operator's answer: the ticket stays stuck.
	if _, err := k.d.Pool.Exec(context.Background(), `UPDATE tickets SET stuck = 'a judge stopped' WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	next, err := k.d.Continue(context.Background(), sub, ContinueRequest{Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(next, "working")
	if n := k.count(`SELECT count(*) FROM tickets WHERE id = $1 AND stuck IS NOT NULL`, ticket); n != 1 {
		t.Fatal("Crew's own retry cleared the ticket's stuck")
	}
	if s := started(k)[1]; !s.Judge || s.Repo != "" || s.Branch != "" || s.Tools || s.MCP != "" {
		t.Fatalf("the retried judge: %+v", s)
	}
}

// A read-only run of a ticket whose branch has no pushed ref starts its workspace from main, as a
// builder's first run does (7.7, BUG-030); a caller that gives its own base, as an eval does, keeps it.
func TestAReadOnlyRunOfAnUnpushedBranchStartsFromMain(t *testing.T) {
	k, ticket := ticketKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	look, _ := k.startRole(RoleRun{Role: "lead", Kind: "look_again", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true})
	v := k.wait(look, "working")
	s := started(k)[0]
	g := harness.RunGit(s.Run, s.Repo, s.Branch, s.Base)
	if s.Branch != "task/SF-1-lock" || s.Base != "refs/heads/main" || v.Base != "refs/heads/main" || g.Base != "refs/heads/main" || !s.ReadOnly {
		t.Fatalf("the unpushed read-only spec: %+v view %+v git %+v", s, v, g)
	}
	k.d.Stop(context.Background(), look)
	k.wait(look, "ended")

	k.play(harness.Script{Recording: agyRun, Hold: true})
	eval, _ := k.startRole(RoleRun{Role: "lead", Kind: "look_again", Project: tshop, Ticket: ticket, Branch: "eval/1",
		Base: "refs/heads/eval/1", ReadOnly: true})
	k.wait(eval, "working")
	if s := started(k)[1]; s.Base != "refs/heads/eval/1" {
		t.Fatalf("caller's base was not kept: %+v", s)
	}
}

func TestAContinuedReadOnlyRunKeepsItsBase(t *testing.T) {
	k, ticket := ticketKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	look, sub := k.startRole(RoleRun{Role: "lead", Kind: "look_again", Project: tshop, Ticket: ticket, Branch: "task/SF-1-lock", ReadOnly: true})
	k.wait(look, "working")
	k.d.Stop(context.Background(), look)
	k.wait(look, "ended")

	k.play(harness.Script{Recording: agyRun, Hold: true})
	next, err := k.d.Continue(context.Background(), sub, ContinueRequest{Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	v := k.wait(next, "working")
	s := started(k)[1]
	if !s.ReadOnly || s.Base != "refs/heads/main" || v.Base != "refs/heads/main" {
		t.Fatalf("continued unpushed read-only run: spec %+v view %+v", s, v)
	}
	k.d.Stop(context.Background(), next)
	k.wait(next, "ended")

	k.play(harness.Script{Recording: agyRun, Hold: true})
	eval, esub := k.startRole(RoleRun{Role: "lead", Kind: "look_again", Project: tshop, Ticket: ticket, Branch: "eval/1",
		Base: "refs/heads/eval/1", ReadOnly: true})
	k.wait(eval, "working")
	k.d.Stop(context.Background(), eval)
	k.wait(eval, "ended")

	k.play(harness.Script{Recording: agyRun, Hold: true})
	enext, err := k.d.Continue(context.Background(), esub, ContinueRequest{Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	ev := k.wait(enext, "working")
	es := started(k)[3]
	if !es.ReadOnly || es.Base != "refs/heads/eval/1" || ev.Base != "refs/heads/eval/1" {
		t.Fatalf("continued caller-base read-only run: spec %+v view %+v", es, ev)
	}
}
