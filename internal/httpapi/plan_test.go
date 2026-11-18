package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/rewrite"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// sandboxAuth takes a sandbox's id as its token, as the door's check would find it.
type sandboxAuth struct{}

func (sandboxAuth) Authorize(_ context.Context, _, _, token string) (sandboxes.Row, error) {
	if !strings.HasPrefix(token, "run-fake") {
		return sandboxes.Row{}, sandboxes.ErrRefused
	}
	return sandboxes.Row{ID: token}, nil
}

// fakeGitHub creates repositories, and git reaches them through a fake ssh on folders of the test.
type fakeGitHub struct{ root, ssh string }

func (f *fakeGitHub) CreateRepo(_ context.Context, name, _ string) (github.Repo, error) {
	if out, err := exec.Command("git", "init", "-q", "--bare", filepath.Join(f.root, "op", name+".git")).CombinedOutput(); err != nil {
		return github.Repo{}, fmt.Errorf("%v %s", err, out)
	}
	return github.Repo{FullName: "op/" + name, SSHURL: "git@github.com:op/" + name + ".git"}, nil
}

func (f *fakeGitHub) SSHEnv(context.Context) ([]string, func(), error) {
	return []string{"GIT_SSH_COMMAND=" + f.ssh, "FAKE_GITHUB=" + f.root}, func() {}, nil
}

const fakeSSH = `#!/bin/sh
for a; do last="$a"; done
cmd="${last%% *}"; path="${last#* }"; path="${path#\'}"; path="${path%\'}"
exec $cmd "$FAKE_GITHUB/$path"
`

// planKit is the server with the board, the run driver on a FakeHarness, the Crew MCP, and the
// planner: the way of 7.2 with every part real but the agents, which the harness plays. An agent's
// turn calls the Crew MCP over HTTP with its run's token, as a CLI would.
type planKit struct {
	t   *testing.T
	srv *httptest.Server
	// rw is the prompt writer.
	rw     *rewrite.Service
	mcpSrv *httptest.Server
	d      *dbtest.DB
	b      *board.Board
	f      *harness.Fake
	pl     *planning.Planner
	gh     *fakeGitHub

	mu    sync.Mutex
	calls []string // tool and whether it answered, as the agents saw them
	// silentLead makes the lead's first turn end with nothing written.
	silentLead bool
	// rewriter plays the prompt writer, a judge (7.2); rewrites are the prompts it was given.
	rewriter func(spec harness.RunSpec) harness.Script
	rewrites []harness.RunSpec
	// prompts are the prompts of every turn the agents were given, in order.
	prompts []string
	driver  *runs.Driver
}

func newPlanKit(t *testing.T) *planKit {
	t.Helper()
	k := &planKit{t: t, d: dbtest.New(t, false)}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	k.gh = &fakeGitHub{root: filepath.Join(dir, "github"), ssh: filepath.Join(dir, "ssh")}
	os.WriteFile(k.gh.ssh, []byte(fakeSSH), 0o755)
	g := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}}
	k.b = &board.Board{Pool: k.d.Server, Git: g, GitHub: k.gh, Log: log,
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		}}
	k.f = &harness.Fake{Next: k.agent}
	k.f.MakeSandbox = func(id string) error {
		_, err := k.d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ($1, 'run', $1, 'crew-runtime:test')`, id)
		return err
	}
	for _, c := range cli.Names() {
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ($1, 'signed_in', now(), '1.0')`, c); err != nil {
			t.Fatal(err)
		}
	}
	// agy has no short list to fall back to: its recorded list is what its agents run on.
	modelstest.Recorded(t, k.d.Server)
	driver := &runs.Driver{Pool: k.d.Server, Harness: k.f, Auth: sandboxAuth{}, MCP: "/mcp", Version: "1.0.0", Image: "crew-runtime:test", Log: log}
	t.Cleanup(driver.Close)
	k.driver = driver
	m, err := mcp.New(k.d.Server, driver.MCPCaller, log)
	if err != nil {
		t.Fatal(err)
	}
	k.pl = &planning.Planner{Pool: k.d.Server, Runs: driver, Version: "1.0.0", Log: log,
		Settings: func(ctx context.Context) (settings.Doc, error) {
			return settings.Effective(ctx, k.d.Server, settings.Machine{Version: "1.0.0"})
		}}
	if err := (&planning.Tools{Pool: k.d.Server, Board: k.b, Kick: k.pl.Kick}).Register(m); err != nil {
		t.Fatal(err)
	}
	k.mcpSrv = httptest.NewServer(m)
	t.Cleanup(k.mcpSrv.Close)
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: log}
	s.Pool.Store(k.d.Server)
	s.Board.Store(k.b)
	s.Drafts.Store(&planning.Drafts{Pool: k.d.Server, Kick: k.pl.Kick})
	s.Planner.Store(k.pl)
	k.rw = &rewrite.Service{Pool: k.d.Server, Runs: driver, Drafts: s.Drafts.Load(), Version: "1.0.0", Log: log,
		Poll: 20 * time.Millisecond,
		Machine: func(ctx context.Context) (settings.Doc, error) {
			return settings.Effective(ctx, k.d.Server, settings.Machine{Version: "1.0.0"})
		},
		Project: func(ctx context.Context, id string) (settings.Doc, error) {
			p, err := k.b.Project(ctx, id)
			if err != nil {
				return nil, err
			}
			return k.b.Settings(ctx, k.d.Server, p)
		}}
	s.Rewrites.Store(k.rw)
	k.srv = httptest.NewServer(s.Handler())
	t.Cleanup(k.srv.Close)
	return k
}

// tool calls the Crew MCP as the run's CLI would, with the run's token.
func (k *planKit) tool(run, name string, args any) (string, bool) {
	if name == "analyse_ticket" {
		if m, ok := args.(map[string]any); ok && m["why"] == nil && (m["choice"] == "approve" || m["choice"] == "notice") {
			var empty bool
			_ = k.d.Server.QueryRow(context.Background(), `SELECT COALESCE(t.why = '', false) FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN tickets t ON t.id = s.ticket WHERE r.id = $1`, run).Scan(&empty)
			if empty {
				cp := make(map[string]any, len(m)+1)
				for key, v := range m {
					cp[key] = v
				}
				cp["why"] = "w"
				args = cp
			}
		}
	}
	var sandbox string
	if err := k.d.Server.QueryRow(context.Background(), `SELECT sandbox FROM runs WHERE id = $1`, run).Scan(&sandbox); err != nil {
		k.t.Errorf("the run's sandbox: %v", err)
		return "", false
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, k.mcpSrv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+sandbox)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Errorf("calling %s: %v", name, err)
		return "", false
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	text := ""
	if len(out.Result.Content) > 0 {
		text = out.Result.Content[0].Text
	}
	k.mu.Lock()
	k.calls = append(k.calls, fmt.Sprintf("%s:%v", name, !out.Result.IsError))
	k.mu.Unlock()
	if out.Result.IsError {
		k.t.Logf("%s refused: %s", name, text)
	}
	return text, !out.Result.IsError
}

const draftedPlan = `---
title: Shop sign-in
parts: [Sign-in]
stages: [review, test]
---

## Goal

Customers sign in with a password.

## Parts

### Sign-in
`

func ticket(title string, deps ...string) map[string]any {
	return map[string]any{"title": title, "kind": "feature", "part": "Sign-in", "why": "The plan says so.", "what": title + ", and no more.",
		"goal": title + " works.", "checks": []map[string]string{{"kind": "cmd", "spec": "go test ./..."}}, "dependsOn": deps,
		"source": map[string]string{"file": "plan/original.md", "heading": "Parts > Sign-in"}}
}

// agent plays the planning agent and the lead: each turn calls the tools its prompt asks for.
func (k *planKit) agent(spec harness.RunSpec, turn int) harness.Script {
	k.mu.Lock()
	k.prompts = append(k.prompts, spec.Prompt)
	k.mu.Unlock()
	if spec.Judge {
		k.mu.Lock()
		k.rewrites = append(k.rewrites, spec)
		play := k.rewriter
		k.mu.Unlock()
		if play != nil {
			return play(spec)
		}
	}
	switch {
	case spec.Project == "" && strings.Contains(spec.Prompt, "Add verification"):
		k.tool(spec.Run, "write_plan_draft", map[string]any{"plan": strings.Replace(draftedPlan, "[review, test]", "[review, test, verification]", 1)})
	case spec.Project == "":
		k.tool(spec.Run, "get_plan_draft", map[string]any{})
		k.tool(spec.Run, "write_plan_draft", map[string]any{"plan": draftedPlan, "title": "Shop sign-in"})
		// A draft's token reaches no project tool.
		if _, ok := k.tool(spec.Run, "get_project", map[string]any{}); ok {
			k.t.Error("the planning agent reached a project tool")
		}
	case strings.Contains(spec.Prompt, "The operator sent the breakdown back with this comment"):
		k.tool(spec.Run, "get_plan", map[string]any{})
		// SF-3 is changed in place, keeping its number, and its second half is a new ticket after it.
		part1 := ticket("Lockout, part one", "SF-1")
		part1["key"] = "SF-3"
		k.tool(spec.Run, "propose_tickets", map[string]any{"tickets": []any{ticket("Lockout, part two", "SF-3")},
			"revise": []any{part1}})
		k.tool(spec.Run, "write_breakdown", map[string]any{"text": "## Sign-in\n\nSF-1, SF-2, SF-3, SF-4."})
	case k.silentLead && !strings.Contains(spec.Prompt, "You ended your turn without writing the breakdown"):
		// A lead that ends its turn with nothing written.
		k.tool(spec.Run, "get_plan", map[string]any{})
	default:
		k.tool(spec.Run, "get_project", map[string]any{})
		k.tool(spec.Run, "get_plan", map[string]any{})
		k.tool(spec.Run, "list_tickets", map[string]any{})
		k.tool(spec.Run, "write_breakdown", map[string]any{"text": "## Sign-in\n\nSF-1, SF-2, SF-3."})
		k.tool(spec.Run, "propose_tickets", map[string]any{"tickets": []any{ticket("Password sign-in"), ticket("Sign-out", "@1"), ticket("Lockout", "@1")}})
		// The lead cannot write the draft's plan, which is no longer its to change.
		if _, ok := k.tool(spec.Run, "write_plan_draft", map[string]any{"plan": "x"}); ok {
			k.t.Error("the lead wrote a plan draft")
		}
	}
	return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}
}

func (k *planKit) call(method, path, body string, code int, out any) {
	k.t.Helper()
	h := signedIn
	if method != "GET" {
		h = change
	}
	got, resp := do(k.t, k.srv, method, path, h, body)
	if got != code {
		k.t.Fatalf("%s %s: %d %s", method, path, got, resp)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(resp), out); err != nil {
			k.t.Fatalf("%s %s: %v: %s", method, path, err, resp)
		}
	}
}

func (k *planKit) waitFor(what string, ok func() bool) {
	k.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			k.t.Fatalf("waited for %s", what)
		}
		k.pl.Reconcile(context.Background())
		time.Sleep(50 * time.Millisecond)
	}
}

// The done-when of M5, through the API, with the agents played: a plan developed with the planning
// agent, a project with a repository on GitHub made from it, the plan added, the lead's breakdown
// into tickets in Planning, sent back and revised, and the OK.
func TestFromAPlanToTheOK(t *testing.T) {
	t.Parallel()
	k := newPlanKit(t)
	var d planning.Draft
	k.call("POST", "/api/drafts", `{"message":"I want customers to sign in to the shop.","repo":"op/old-shop"}`, 201, &d)
	k.waitFor("the first version of the plan", func() bool {
		k.call("GET", "/api/drafts/"+d.ID, "", 200, &d)
		return d.Version == 1 && d.Agent != nil && d.Agent.State == "idle"
	})
	if d.Title != "Shop sign-in" || len(d.Waiting) != 0 || *d.Agent.Model != "gemini-4-argon" || d.Agent.CLI != "agy" {
		t.Fatalf("the draft: %+v", d)
	}
	first := k.f.Started[0]
	if first.Repo != "" || first.MCP != "/mcp" || len(first.Repos) != 1 || first.Repos[0] != "op/old-shop" ||
		!strings.Contains(first.Prompt, "Developing a plan with the operator") || !strings.Contains(first.Prompt, "I want customers to sign in") {
		t.Fatalf("the planning agent's run: %+v", first)
	}

	// The operator's next message continues the conversation in the same microVM.
	k.call("POST", "/api/drafts/"+d.ID+"/messages", `{"text":"Add verification."}`, 202, nil)
	k.waitFor("the second version", func() bool {
		k.call("GET", "/api/drafts/"+d.ID, "", 200, &d)
		return d.Version == 2 && len(d.Waiting) == 0 && d.Agent.State == "idle"
	})
	if len(k.f.Started) != 1 || len(k.f.Continued) != 1 {
		t.Fatalf("a message to an idle planning agent started %d runs, continued %d", len(k.f.Started), len(k.f.Continued))
	}

	// The project, with a new repository on GitHub, and the draft's plan as its original plan.
	var p board.Project
	k.call("POST", "/api/projects", `{"name":"shop","key":"SF","draft":"`+d.ID+`","repository":{"kind":"github"}}`, 201, &p)
	if p.State != "planning" || p.Remote == nil || *p.Remote != "git@github.com:op/shop.git" || p.Goal != "Customers sign in with a password." {
		t.Fatalf("the project: %+v", p)
	}
	var gitView struct {
		Main   string       `json:"main"`
		Pushes []board.Push `json:"pushes"`
	}
	k.call("GET", "/api/projects/shop/git", "", 200, &gitView)
	remoteMain, _ := exec.Command("git", "-C", filepath.Join(k.gh.root, "op/shop.git"), "rev-parse", "main").Output()
	if len(gitView.Pushes) != 1 || gitView.Pushes[0].State != "done" || strings.TrimSpace(string(remoteMain)) != gitView.Main {
		t.Fatalf("the push: %+v, main on GitHub %q", gitView, remoteMain)
	}
	var stages map[string]any
	k.call("GET", "/api/projects/shop/stages", "", 200, &stages)
	if !strings.Contains(fmt.Sprint(stages), "verification") {
		t.Fatalf("the stages of the draft's last version: %v", stages)
	}
	k.call("POST", "/api/drafts/"+d.ID+"/messages", `{"text":"One more thing."}`, 400, nil)

	// The lead breaks the plan down, and its run ends once the breakdown is written.
	var brd struct {
		Columns []struct {
			ID      string `json:"id"`
			Tickets []struct {
				Key string `json:"key"`
			} `json:"tickets"`
		} `json:"columns"`
	}
	planning := func() []string {
		k.call("GET", "/api/projects/shop/board", "", 200, &brd)
		var keys []string
		for _, c := range brd.Columns {
			if c.ID == "planning" {
				for _, x := range c.Tickets {
					keys = append(keys, x.Key)
				}
			}
		}
		return keys
	}
	k.waitFor("the breakdown's tickets", func() bool { return strings.Join(planning(), ",") == "SF-1,SF-2,SF-3" })
	lead := k.f.Started[1]
	if lead.Repo != "shop" || lead.MCP != "/mcp" || lead.Project != p.ID || !strings.Contains(lead.Prompt, "Breaking a plan down") {
		t.Fatalf("the lead's run: %+v", lead)
	}
	var pl board.Plan
	k.waitFor("the lead's run to end", func() bool {
		var n int
		k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE state <> 'ended' AND id = $1`, lead.Run).Scan(&n)
		return n == 0
	})
	k.call("GET", "/api/projects/shop/plan", "", 200, &pl)
	if pl.Original != strings.Replace(draftedPlan, "[review, test]", "[review, test, verification]", 1) || pl.Breakdown == nil {
		t.Fatalf("the plan: %+v", pl)
	}

	// Sent back with a comment: the lead revises in a new run of its conversation.
	k.call("POST", "/api/projects/shop/ok", `{"sendBack":"SF-3 is too large: split it."}`, 200, nil)
	k.waitFor("the revised breakdown", func() bool { return strings.Join(planning(), ",") == "SF-1,SF-2,SF-3,SF-4" })
	if len(k.f.Started) != 3 || len(k.f.Continued) != 1 || !strings.Contains(k.f.Started[2].Prompt, "SF-3 is too large") {
		t.Fatalf("the revision's run: %d runs, %d continued", len(k.f.Started), len(k.f.Continued))
	}
	k.mu.Lock()
	refused := 0
	for _, c := range k.calls {
		if strings.HasSuffix(c, ":false") {
			refused++
		}
	}
	k.mu.Unlock()
	if refused != 2 {
		t.Fatalf("calls %v: only the two that test a token's reach are refused", k.calls)
	}
	k.call("GET", "/api/projects/shop/plan", "", 200, &pl)
	var t3 struct {
		Ticket struct{ Title string } `json:"ticket"`
	}
	k.call("GET", "/api/projects/shop/tasks/SF-3", "", 200, &t3)
	if t3.Ticket.Title != "Lockout, part one" {
		t.Fatalf("SF-3 was not revised in place: %q", t3.Ticket.Title)
	}
	if !strings.Contains(*pl.Breakdown, "SF-3, SF-4") {
		t.Fatalf("the breakdown was not written again: %q", *pl.Breakdown)
	}

	// The tickets that depend on SF-1 are its links back (20.2).
	var t1 struct {
		LinkedFrom []struct{ Kind, To string } `json:"linkedFrom"`
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1", "", 200, &t1)
	if len(t1.LinkedFrom) != 2 || t1.LinkedFrom[0].Kind != "depends_on" || t1.LinkedFrom[0].To != "SF-2" || t1.LinkedFrom[1].To != "SF-3" {
		t.Errorf("SF-1's links back: %+v", t1.LinkedFrom)
	}
	// The breakdown waits for the OK: the inbox says so, with the OK as its main alternative, and the
	// menu counts it for the project (7.8).
	var in struct {
		Items []struct {
			ID           string
			Alternatives []struct {
				Action string
				Main   bool
			}
		}
	}
	// It waits while the lead's run of the revision goes on, and is there once it has ended.
	var okIdx int
	k.waitFor("the inbox item of the OK", func() bool {
		k.call("GET", "/api/inbox", "", 200, &in)
		okIdx = -1
		for i, it := range in.Items {
			if it.ID == "ok/shop" {
				okIdx = i
			}
		}
		return okIdx >= 0
	})
	if in.Items[okIdx].ID != "ok/shop" || in.Items[okIdx].Alternatives[2].Action != "/api/projects/shop/ok" {
		t.Errorf("the inbox before the OK: %+v", in)
	}
	var f struct {
		Inbox    int
		Projects []struct {
			Name     string
			NeedsYou int
		}
	}
	k.call("GET", "/api/frame", "", 200, &f)
	if f.Inbox != 1 || len(f.Projects) == 0 || f.Projects[len(f.Projects)-1].NeedsYou != 1 {
		t.Errorf("the frame before the OK: %+v", f)
	}

	// The OK.
	k.call("POST", "/api/projects/shop/ok", "", 200, &p)
	k.call("GET", "/api/inbox", "", 200, &in)
	if len(in.Items) != 0 {
		t.Errorf("the inbox after the OK: %+v", in)
	}
	if p.State != "running" || len(planning()) != 0 {
		t.Fatalf("after the OK: %s, still in Planning %v", p.State, planning())
	}

	// Every call is recorded, with the draft's under the project made from it.
	var calls struct {
		Calls []struct {
			Role, Tool string
			OK         bool
		} `json:"calls"`
	}
	k.call("GET", "/api/projects/shop/mcp/calls", "", 200, &calls)
	tools := map[string]bool{}
	for _, c := range calls.Calls {
		tools[c.Role+"/"+c.Tool+fmt.Sprint(c.OK)] = true
	}
	for _, want := range []string{"planner/write_plan_drafttrue", "planner/get_projectfalse", "planner/propose_ticketstrue",
		"planner/write_breakdowntrue", "planner/write_plan_draftfalse"} {
		if !tools[want] {
			t.Errorf("no call %s in %v", want, tools)
		}
	}
	// No call was made after the OK, and nothing is queued: the planner starts nothing more.
	before := len(k.f.Started)
	k.pl.Reconcile(context.Background())
	if len(k.f.Started) != before {
		t.Fatal("the planner started a run after the OK")
	}
}

// A lead whose turn ends with nothing written gets one reminder, and then writes it.
func TestALeadThatWroteNothingIsRemindedOnce(t *testing.T) {
	t.Parallel()
	k := newPlanKit(t)
	k.silentLead = true
	k.call("POST", "/api/projects", `{"name":"shop","key":"SF","plan":"## Goal\n\nA shop.\n","repository":{"kind":"local"}}`, 201, nil)
	k.waitFor("the reminded lead's tickets", func() bool {
		var n int
		k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM tickets WHERE stage = 'planning'`).Scan(&n)
		return n == 3
	})
	if len(k.f.Started) != 1 || len(k.f.Continued) != 1 {
		t.Fatalf("%d runs started, %d continued", len(k.f.Started), len(k.f.Continued))
	}
	// Once only: a reminder is a job done, which the next pass does not repeat.
	var jobs int
	k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE key LIKE 'nudge:%'`).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("%d reminders", jobs)
	}
}
