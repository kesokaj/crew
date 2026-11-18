package board_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
)

func TestTheBreakdownIsProposedInOneTransaction(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	a, b := ready("Sign-in"), ready("Lockout")
	b.DependsOn = []string{"@1"}
	part := "Sign-in"
	a.Part = &part
	a.Source = &board.Source{File: "plan/original.md", Heading: "Parts > Sign-in", Quote: "Customers can sign in."}
	got, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{a, b}, nil, nil, "lead · r_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "SF-1" || got[1].Key != "SF-2" || got[0].Stage != "planning" || got[0].Origin != "plan" ||
		got[0].Analysis.Status != "waiting" || got[0].CreatedBy != "lead · r_1" || got[0].Source == nil || *got[0].Part != "Sign-in" {
		t.Fatalf("%+v", got[0])
	}
	if l := got[1].Links; len(l) != 1 || l[0].Kind != "depends_on" || l[0].To != "SF-1" {
		t.Fatalf("SF-2's links: %+v", l)
	}
	// A bad ticket in the middle of a call leaves nothing of the call.
	bad := ready("No check")
	bad.Checks = nil
	before := e.changes(p)
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("One"), bad, ready("Three")}, nil, nil, "lead · r_1"); err == nil ||
		!strings.Contains(err.Error(), "ticket 2 (No check)") {
		t.Fatalf("a ticket with no check: %v", err)
	}
	all, _ := e.b.Tickets(e.ctx, p)
	if n := len(all); n != 2 || e.changes(p) != before {
		t.Fatalf("a refused call left %d tickets and %d rows of history", n, e.changes(p)-before)
	}
	for _, deps := range [][]string{{"@0"}, {"@2"}, {"@x"}} {
		n := ready("Bad dependency")
		n.DependsOn = deps
		if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("First"), n}, nil, nil, "lead · r_1"); err == nil {
			t.Errorf("dependsOn %v was taken", deps)
		}
	}
}

func TestTheLeadWithdrawsOnlyItsOwnTicketsInPlanning(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("A"), ready("B")}, nil, nil, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.AddTicket(e.ctx, "shop", ready("Mine"), board.OriginOperator, board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", nil, []board.Withdrawal{{Key: "SF-3", Reason: "not needed"}}, nil, "lead · r_2"); err == nil {
		t.Fatal("the lead withdrew the operator's ticket")
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", nil, []board.Withdrawal{{Key: "SF-1"}}, nil, "lead · r_2"); err == nil {
		t.Fatal("a withdrawal with no reason")
	}
	got, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("A, smaller")}, []board.Withdrawal{{Key: "SF-1", Reason: "too large"}}, nil, "lead · r_2")
	if err != nil || len(got) != 1 || got[0].Key != "SF-4" {
		t.Fatalf("%v %v", got, err)
	}
	if t1 := e.ticket(p, "SF-1"); t1.Stage != "closed" || *t1.ClosedReason != "discarded" || !strings.Contains(t1.Analysis.Reason, "too large") {
		t.Fatalf("SF-1 = %s %v %v", t1.Stage, t1.ClosedReason, t1.Analysis.Reason)
	}
	e.ok(p)
	if e.stage(p, "SF-1") != "closed" || e.stage(p, "SF-2") != "backlog" {
		t.Fatal("the OK moved a withdrawn ticket, or not the others")
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("Late")}, nil, nil, "lead · r_3"); err == nil {
		t.Fatal("a ticket of the breakdown after the OK")
	}
}

const draftID = "d0000000-0000-4000-8000-000000000042"

const draftPlan = `---
title: Shop
parts: [Sign-in]
stages: [review, test, verification]
budget_usd: 40
---

## Goal

Customers can sign in
with a password.

## Why

Support asks.
`

func (e *env) draft(versions ...string) {
	e.t.Helper()
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO plan_drafts (id) VALUES ($1)`, draftID); err != nil {
		e.t.Fatal(err)
	}
	for i, v := range versions {
		if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO plan_draft_versions (draft, number, text) VALUES ($1, $2, $3)`, draftID, i+1, v); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestAProjectIsMadeFromItsDraftOnce(t *testing.T) {
	e := newEnv(t)
	e.draft("an old version", draftPlan)
	// A run of the draft: its cost goes to the project with it.
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO subtasks (scope, draft, number, kind, role) VALUES ('crew', $1, 1, 'planning', 'planner')`, draftID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: "mine", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator); err == nil {
		t.Fatal("a plan beside a draft was taken")
	}
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := e.b.Plan(e.ctx, p)
	if pl.Original != draftPlan || p.Goal != "Customers can sign in with a password." || p.Draft == nil || *p.Draft != draftID {
		t.Fatalf("plan %.40q, goal %q, draft %v", pl.Original, p.Goal, p.Draft)
	}
	st, err := e.b.Settings(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatal(err)
	}
	var on []string
	for _, s := range board.PipelineOf(st).Columns() {
		if s.Optional {
			on = append(on, s.ID)
		}
	}
	// A front matter that still names a budget, as plans before 0.53 did, gives none: a project has no
	// budget.
	if strings.Join(on, ",") != "review,test,verification" || st["budget_usd"] != nil {
		t.Fatalf("stages %v, budget %v", on, st["budget_usd"])
	}
	var state, project string
	e.db.Server.QueryRow(e.ctx, `SELECT state, project::text FROM plan_drafts WHERE id = $1`, draftID).Scan(&state, &project)
	var moved int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM subtasks WHERE draft = $1 AND project = $2`, draftID, p.ID).Scan(&moved)
	if state != "project" || project != p.ID || moved != 1 {
		t.Fatalf("the draft is %s of %s, %d subtasks moved", state, project, moved)
	}
	if _, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop2", Key: "SG", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator); err == nil {
		t.Fatal("a draft became a second project")
	}
}

func TestADraftWithNoPlanOrANewerVersionMakesNoProject(t *testing.T) {
	e := newEnv(t)
	e.draft()
	if _, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator); err == nil {
		t.Fatal("a draft with no plan made a project")
	}
	// The planning agent writes a version while the project is made: the plan taken is no longer the
	// newest, so nothing is made, and the repository is removed again.
	e.db.Server.Exec(e.ctx, `INSERT INTO plan_draft_versions (draft, number, text) VALUES ($1, 1, $2)`, draftID, draftPlan)
	e.b.Identity = func(ctx context.Context) (gitstore.Identity, error) {
		e.db.Server.Exec(ctx, `INSERT INTO plan_draft_versions (draft, number, text) VALUES ($1, 2, 'newer')`, draftID)
		return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
	}
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator)
	var be *board.Error
	if !errors.As(err, &be) || be.Kind != board.Conflict {
		t.Fatalf("a draft that changed: %v", err)
	}
	if e.git.Exists("shop") {
		t.Fatal("the repository of a project that was not made stayed")
	}
}

// fakeGitHub creates repositories as told, and reaches the remote through a fake ssh that runs git's
// receive-pack on a folder of the test.
type fakeGitHub struct {
	err     error
	created []string
	sshEnv  []string
	keyLeft bool
}

func (f *fakeGitHub) CreateRepo(_ context.Context, name, _ string) (github.Repo, error) {
	if f.err != nil {
		return github.Repo{}, f.err
	}
	f.created = append(f.created, name)
	return github.Repo{FullName: "op/" + name, SSHURL: "git@github.com:op/" + name + ".git"}, nil
}

func (f *fakeGitHub) SSHEnv(context.Context) ([]string, func(), error) {
	return f.sshEnv, func() {}, nil
}

// fakeSSH stands in for ssh: it runs the git command GitHub would run, on <root>/<owner>/<name>.git.
const fakeSSH = `#!/bin/sh
root="$FAKE_GITHUB"
for a; do last="$a"; done
cmd="${last%% *}"; path="${last#* }"; path="${path#\'}"; path="${path%\'}"
[ -d "$root/$path" ] || { echo "ERROR: Repository not found." >&2; exit 1; }
exec $cmd "$root/$path"
`

func (e *env) fakeGitHub() (*fakeGitHub, string) {
	e.t.Helper()
	root := e.t.TempDir()
	bin := filepath.Join(e.t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(fakeSSH), 0o755); err != nil {
		e.t.Fatal(err)
	}
	f := &fakeGitHub{sshEnv: []string{"GIT_SSH_COMMAND=" + bin, "FAKE_GITHUB=" + root}}
	e.b.GitHub = f
	return f, root
}

func bareRepo(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", "--bare", path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

func TestANewRepositoryOnGitHubGetsMainOverSSH(t *testing.T) {
	e := newEnv(t)
	f, root := e.fakeGitHub()
	bareRepo(t, filepath.Join(root, "op/shop-code.git"))
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan, Repo: board.Repo{Kind: "github", Name: "shop-code"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0] != "shop-code" || p.Remote == nil || *p.Remote != "git@github.com:op/shop-code.git" ||
		p.RepoSource != "github git@github.com:op/shop-code.git" {
		t.Fatalf("created %v, project %+v", f.created, p)
	}
	ps, _ := e.b.Pushes(e.ctx, p)
	if len(ps) != 1 || ps[0].State != "done" || ps[0].Tries != 1 {
		t.Fatalf("pushes %+v", ps)
	}
	// main on GitHub is main on the machine.
	out, err := exec.Command("git", "-C", filepath.Join(root, "op/shop-code.git"), "rev-parse", "refs/heads/main").Output()
	if err != nil || strings.TrimSpace(string(out)) != e.git.Main(e.ctx, "shop") {
		t.Fatalf("main on the remote %q, on the machine %q: %v", out, e.git.Main(e.ctx, "shop"), err)
	}
}

func TestGitHubRefusesTheRepositoryAndNothingIsMade(t *testing.T) {
	e := newEnv(t)
	f, _ := e.fakeGitHub()
	f.err = &github.Error{Msg: "GitHub says a repository named shop exists already on this account"}
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan, Repo: board.Repo{Kind: "github"}}, board.Operator)
	if err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("%v", err)
	}
	if ps, _ := e.b.Projects(e.ctx); len(ps) != 1 || e.git.Exists("shop") {
		t.Fatalf("a project or a repository was made: %d projects", len(ps))
	}
}

func TestAPushThatFailsIsTriedAgainAndNeverForced(t *testing.T) {
	e := newEnv(t)
	_, root := e.fakeGitHub()
	// The operator's own empty repository, which does not exist yet: the first push fails.
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan,
		Repo: board.Repo{Kind: "github", URL: "git@github.com:op/mine.git"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := e.b.Pushes(e.ctx, p)
	if len(ps) != 1 || ps[0].State != "pending" || ps[0].Tries != 1 || ps[0].Detail != "the remote has no such repository" {
		t.Fatalf("pushes %+v", ps)
	}
	// Not before its time.
	bareRepo(t, filepath.Join(root, "op/mine.git"))
	e.b.PushPending(e.ctx, "")
	if ps, _ = e.b.Pushes(e.ctx, p); ps[0].State != "pending" {
		t.Fatal("a push was tried again before its time")
	}
	e.db.Server.Exec(e.ctx, `UPDATE pushes SET not_before = now()`)
	e.b.PushPending(e.ctx, "")
	if ps, _ = e.b.Pushes(e.ctx, p); ps[0].State != "done" || ps[0].Tries != 2 {
		t.Fatalf("pushes %+v", ps)
	}
	// A remote whose main went another way refuses the push, and it waits: nothing is forced.
	other := filepath.Join(t.TempDir(), "other")
	for _, args := range [][]string{{"init", "-q", "-b", "main", other}, {"-C", other, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-q", "--allow-empty", "-m", "elsewhere"},
		{"-C", other, "push", "-q", "-f", filepath.Join(root, "op/mine.git"), "main:main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	before, _ := exec.Command("git", "-C", filepath.Join(root, "op/mine.git"), "rev-parse", "main").Output()
	e.db.Server.Exec(e.ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, 'git@github.com:op/mine.git', '{refs/heads/main}', 'test')`, p.ID)
	e.b.PushPending(e.ctx, p.ID)
	after, _ := exec.Command("git", "-C", filepath.Join(root, "op/mine.git"), "rev-parse", "main").Output()
	if ps, _ = e.b.Pushes(e.ctx, p); ps[0].State != "pending" || string(before) != string(after) {
		t.Fatalf("a push over another main: %+v", ps[0])
	}
}

func TestARepositoryOnGitHubIsNamedOneWay(t *testing.T) {
	e := newEnv(t)
	e.fakeGitHub()
	for _, r := range []board.Repo{
		{Kind: "github", URL: "https://github.com/op/shop.git"},
		{Kind: "github", URL: "git@evil.example:op/shop.git"},
		{Kind: "github", URL: "git@github.com:op/shop.git", Name: "shop"},
		{Kind: "github", Name: "a b"},
		{Kind: "local", Name: "shop"},
	} {
		if _, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan, Repo: r}, board.Operator); err == nil {
			t.Errorf("%+v was taken", r)
		}
	}
}

// A repository Crew made on GitHub for a project that was then not made is named in the error, so
// the operator can use it.
func TestARepositoryMadeOnGitHubIsNamedWhenTheProjectIsNotMade(t *testing.T) {
	e := newEnv(t)
	e.fakeGitHub()
	// The machine's repository cannot be made: its folder is a file.
	os.MkdirAll(filepath.Dir(e.git.Dir), 0o755)
	os.WriteFile(e.git.Dir, []byte("not a folder"), 0o644)
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan, Repo: board.Repo{Kind: "github"}}, board.Operator)
	if err == nil || !strings.Contains(err.Error(), "git@github.com:op/shop.git on GitHub was made and is empty") {
		t.Fatalf("%v", err)
	}
	// The operator's own repository is theirs: it is not named as one Crew made.
	_, err = e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan,
		Repo: board.Repo{Kind: "github", URL: "git@github.com:op/mine.git"}}, board.Operator)
	if err == nil || strings.Contains(err.Error(), "was made") {
		t.Fatalf("%v", err)
	}
}

// The plan's front matter is the planning agent's text: it may switch optional stages on, never
// off, and a budget it still names sets nothing.
func TestTheDraftsFrontMatterOnlyAddsStages(t *testing.T) {
	e := newEnv(t)
	e.draft(strings.Replace(draftPlan, "stages: [review, test, verification]\nbudget_usd: 40", "stages: [e2e]\nbudget_usd: .inf", 1))
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := e.b.Settings(e.ctx, e.db.Server, p)
	var on []string
	for _, s := range board.PipelineOf(st).Columns() {
		if s.Optional {
			on = append(on, s.ID)
		}
	}
	if strings.Join(on, ",") != "review,test,e2e" || fmt.Sprint(st["budget_usd"]) == "+Inf" {
		t.Fatalf("stages %v, budget %v", on, st["budget_usd"])
	}
	empty := strings.Replace(draftPlan, "stages: [review, test, verification]", "stages: []", 1)
	e2 := newEnv(t)
	e2.draft(empty)
	p2, err := e2.b.CreateProject(e2.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := e2.b.Settings(e2.ctx, e2.db.Server, p2)
	if cols := fmt.Sprint(board.PipelineOf(st2).Columns()); !strings.Contains(cols, "review") || !strings.Contains(cols, "test") {
		t.Fatalf("an empty list of stages switched Review or Test off: %s", cols)
	}
}

func TestTheBreakdownChangesOnlyBeforeTheOKAndHasALimit(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	for i := 0; i < 100; i++ {
		if err := e.b.SetBreakdown(e.ctx, p, fmt.Sprintf("version %d", i), "lead · r1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.b.SetBreakdown(e.ctx, p, "one more", "lead · r1"); err == nil {
		t.Fatal("a breakdown past the limit")
	}
	e.add(p, ready("Mine"), board.OriginOperator)
	e.ok(p)
	p, _ = e.b.Project(e.ctx, "shop")
	for _, kind := range []func() error{
		func() error { return e.b.SetBreakdown(e.ctx, p, "late", "lead · r1") },
		func() error { return e.b.SendBack(e.ctx, "shop", "late", board.Operator) },
	} {
		if err := kind(); err == nil || !strings.Contains(err.Error(), "running") {
			t.Fatalf("after the OK: %v", err)
		}
	}
	// The state is read under the project's lock: a project row read before the OK is no pass.
	stale := *p
	stale.State = "planning"
	if err := e.b.SetBreakdown(e.ctx, &stale, "late", "lead · r1"); err == nil {
		t.Fatal("a breakdown landed after the OK")
	}
}

func TestATicketsTextsHaveTheirSizes(t *testing.T) {
	e := newEnv(t)
	e.project("shop", "SF")
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("First")}, nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	long, two := strings.Repeat("x", 5000), "a\nb"
	for name, change := range map[string]func(*board.NewTicket){
		"part":       func(n *board.NewTicket) { n.Part = &two },
		"source":     func(n *board.NewTicket) { n.Source = &board.Source{File: "plan/original.md", Quote: long} },
		"heading":    func(n *board.NewTicket) { n.Source = &board.Source{File: "plan/original.md", Heading: two} },
		"docsReason": func(n *board.NewTicket) { n.DocsReason = &long },
		"dependsOn": func(n *board.NewTicket) {
			for i := 0; i < 51; i++ {
				n.DependsOn = append(n.DependsOn, "SF-1")
			}
		},
	} {
		n := ready("Sized")
		change(&n)
		if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{n}, nil, nil, "lead · r1"); err == nil {
			t.Errorf("%s past its size was taken", name)
		}
	}
	many := make([]board.NewTicket, 60)
	for i := range many {
		many[i] = ready(fmt.Sprintf("T%d", i))
	}
	for i := 0; i < 4; i++ {
		if _, err := e.b.ProposeTickets(e.ctx, "shop", many, nil, nil, "lead · r1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", many[:59], nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", many[:1], nil, nil, "lead · r1"); err == nil {
		t.Fatal("a breakdown past 300 tickets")
	}
}

// A push that stalls ends at its time, says so in Crew's words, and holds up no board.
func TestAStalledPushEndsAndHoldsNothingUp(t *testing.T) {
	e := newEnv(t)
	f, _ := e.fakeGitHub()
	stall := filepath.Join(t.TempDir(), "ssh")
	os.WriteFile(stall, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)
	f.sshEnv = []string{"GIT_SSH_COMMAND=" + stall}
	e.b.PushTimeout = time.Second
	start := time.Now()
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan,
		Repo: board.Repo{Kind: "github", URL: "git@github.com:op/mine.git"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the request waited %v", time.Since(start))
	}
	ps, _ := e.b.Pushes(e.ctx, p)
	if ps[0].State != "pending" || ps[0].Detail != "the remote did not answer in time" {
		t.Fatalf("%+v", ps[0])
	}
	// While a push stalls in the board's loop, the board still moves: an OK given meanwhile takes
	// its ticket to Analysis.
	e.b.PushTimeout = 20 * time.Second
	e.db.Server.Exec(e.ctx, `UPDATE pushes SET not_before = now()`)
	e.add(p, ready("A"), board.OriginOperator)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	go e.b.Run(ctx, 50*time.Millisecond)
	time.Sleep(500 * time.Millisecond) // the push has begun, and stalls
	e.ok(p)
	e.b.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for e.stage(p, "SF-1") != "analysis" {
		if time.Now().After(deadline) {
			t.Fatal("a stalled push held the board up")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAPushFailureIsToldInCrewsWords(t *testing.T) {
	e := newEnv(t)
	e.fakeGitHub()
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Plan: plan,
		Repo: board.Repo{Kind: "github", URL: "git@github.com:op/absent.git"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := e.b.Pushes(e.ctx, p)
	if ps[0].Detail != "the remote has no such repository" {
		t.Fatalf("the detail holds %q", ps[0].Detail)
	}
}

// A goal cut at its size is cut at a character, so the project is made with a goal in any language.
func TestAGoalFromTheDraftIsCutAtACharacter(t *testing.T) {
	e := newEnv(t)
	e.draft("## Goal\n\n" + strings.Repeat("x", 1999) + "åäö\n")
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Draft: draftID, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(p.Goal) || len(p.Goal) > 2000 {
		t.Fatalf("the goal: %d bytes, valid %v", len(p.Goal), utf8.ValidString(p.Goal))
	}
}

// The lead changes a ticket of its breakdown in place: the number, the history, and the tickets that
// depend on it stay; only its own tickets in Planning, and all of a call or nothing.
func TestTheLeadRevisesATicketInPlace(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	a, b, c := ready("Sign-in"), ready("Sign-out"), ready("Lockout")
	b.DependsOn, c.DependsOn = []string{"@1"}, []string{"@1"}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{a, b, c}, nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.AddTicket(e.ctx, "shop", ready("Mine"), board.OriginOperator, board.Operator); err != nil {
		t.Fatal(err)
	}
	r1 := board.Revision{Key: "SF-1", NewTicket: ready("Sign-in with a password")}
	r1.Checks = []board.CheckSpec{{Kind: "cmd", Spec: "go test ./auth/..."}}
	r3 := board.Revision{Key: "SF-3", NewTicket: ready("Lockout, part one")}
	r3.DependsOn = []string{"SF-2", "@1"}
	before := e.changes(p)
	got, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("Lockout, part two")}, nil, []board.Revision{r1, r3}, "lead · r2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Key != "SF-5" || got[1].Key != "SF-1" || got[2].Key != "SF-3" {
		t.Fatalf("%v", got)
	}
	t1, t2, t3 := e.ticket(p, "SF-1"), e.ticket(p, "SF-2"), e.ticket(p, "SF-3")
	if t1.Title != "Sign-in with a password" || len(t1.Checks) != 1 || t1.Checks[0].Spec != "go test ./auth/..." || t1.Stage != "planning" {
		t.Fatalf("SF-1 = %+v", t1)
	}
	if l := t2.Links; len(l) != 1 || l[0].To != "SF-1" {
		t.Fatalf("what depends on SF-1 changed: %+v", l)
	}
	if links := fmt.Sprint(t3.Links); t3.Title != "Lockout, part one" || !strings.Contains(links, "SF-2") || !strings.Contains(links, "SF-5") || strings.Contains(links, "SF-1") {
		t.Fatalf("SF-3 = %s %v", t3.Title, t3.Links)
	}
	h, _ := e.b.History(e.ctx, t1)
	if last := h[len(h)-1]; last.Who != "lead · r2" || !strings.Contains(string(last.Before), `"Sign-in"`) || !strings.Contains(string(last.After), "with a password") ||
		strings.Contains(string(last.After), `"why"`) || strings.Contains(string(last.After), `"origin"`) {
		t.Fatalf("the history of SF-1 holds more than what changed: %s / %s", last.Before, last.After)
	}
	if e.changes(p) != before+3 {
		t.Fatalf("%d rows of history for one ticket added and two revised", e.changes(p)-before)
	}
	for name, call := range map[string]func() error{
		"the operator's ticket": func() error {
			_, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-4", NewTicket: ready("x")}}, "lead · r3")
			return err
		},
		"a ticket withdrawn and revised": func() error {
			_, err := e.b.ProposeTickets(e.ctx, "shop", nil, []board.Withdrawal{{Key: "SF-2", Reason: "r"}}, []board.Revision{{Key: "SF-2", NewTicket: ready("x")}}, "lead · r3")
			return err
		},
		"a revision with no check": func() error {
			n := ready("x")
			n.Checks = nil
			_, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-2", NewTicket: n}}, "lead · r3")
			return err
		},
		"a circle": func() error {
			n := ready("x")
			n.DependsOn = []string{"SF-2"}
			_, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-1", NewTicket: n}}, "lead · r3")
			return err
		},
		"a ticket that is not there, after a good one": func() error {
			_, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{ready("Extra")}, nil, []board.Revision{{Key: "SF-99", NewTicket: ready("x")}}, "lead · r3")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s was revised", name)
		}
	}
	if all, _ := e.b.Tickets(e.ctx, p); len(all) != 5 {
		t.Fatalf("a refused call left a ticket: %d", len(all))
	}
	// A ticket the lead created as an agent, not in its breakdown, is no ticket of the breakdown.
	if _, err := e.b.AddTicket(e.ctx, "shop", ready("Seen while working"), board.OriginAgent, "lead · r9"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-6", NewTicket: ready("x")}}, "lead · r9"); err == nil {
		t.Fatal("a ticket of origin agent was revised as one of the breakdown")
	}
	e.ok(p)
	if _, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-1", NewTicket: ready("late")}}, "lead · r4"); err == nil {
		t.Fatal("a revision after the OK")
	}
}

// The operator's change of a ticket wins over the lead's revision; a priority a revision leaves out
// stays.
func TestARevisionKeepsTheOperatorsChangeAndWhatItLeavesOut(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	one := 1
	a := ready("Sign-in")
	a.Priority = &one
	if _, err := e.b.ProposeTickets(e.ctx, "shop", []board.NewTicket{a, ready("Sign-out")}, nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-1", NewTicket: ready("Sign-in, again")}}, "lead · r2"); err != nil {
		t.Fatal(err)
	}
	if t1 := e.ticket(p, "SF-1"); t1.Priority != 1 {
		t.Fatalf("the revision reset what it left out: priority %d", t1.Priority)
	}
	title := "The operator's title"
	if _, err := e.b.EditTicket(e.ctx, p, "SF-2", board.Edit{Title: &title}, board.Operator); err != nil {
		t.Fatal(err)
	}
	_, err := e.b.ProposeTickets(e.ctx, "shop", nil, nil, []board.Revision{{Key: "SF-2", NewTicket: ready("The lead's title")}}, "lead · r3")
	if err == nil || !strings.Contains(err.Error(), "the operator changed SF-2") || e.ticket(p, "SF-2").Title != title {
		t.Fatalf("%v; title %q", err, e.ticket(p, "SF-2").Title)
	}
}
