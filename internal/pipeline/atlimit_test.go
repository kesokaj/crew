package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

// A subtask stopped at a limit moves to its agent's next choice that can run, in another limit group of
// agy's, with the ticket's pin and the rule for checks as a new run would have them; it waits when
// nothing else can run, or with at_limit wait (12.7).
func TestASubtaskAtALimitMovesToItsNextChoice(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	p, err := b.CreateProject(ctx, board.NewProject{Name: "shop", Key: "SF", Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := b.AddTicket(ctx, p.Name, board.NewTicket{Title: "One", Why: "w", What: "w", Goal: "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	if err != nil {
		t.Fatal(err)
	}
	d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`)
	modelstest.Recorded(t, d.Server)
	own := settings.Doc{}
	pl := &Pipeline{Pool: d.Server, Board: b, Settings: func(context.Context) (settings.Doc, error) {
		def, err := settings.Defaults(settings.Machine{Version: "test"})
		return settings.Merge(def, own), err
	}}
	n := 0
	stopped := func(kind, role, cli, model string) int64 {
		t.Helper()
		n++
		var sub int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, wait_for)
			VALUES ('ticket', $1, $2, $3, $4, $5, 'waiting', 'quota:' || $6) RETURNING id`, p.ID, tk.ID, n, kind, role, cli).Scan(&sub); err != nil {
			t.Fatal(err)
		}
		conv := "00000000-0000-4000-8000-00000000000" + string(rune('0'+n))
		d.Server.Exec(ctx, `INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ($1, $2, $3, 'run', $4)`, conv, p.ID, tk.ID, cli)
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, conversation, stop_reason)
			VALUES ($1, $2, 1, 'ended', $3, $4, $4, 'test', 'test', $1::uuid, 'quota')`, conv, sub, cli, model); err != nil {
			t.Fatal(err)
		}
		return sub
	}
	reset := time.Now().Add(time.Hour)

	// The builder's Gemini 4 Argon refused by agy: it moves along its Gemini entries to Gemini 3.8 Flash;
	// at the limit of agy's Gemini group it stays on Gemini and waits rather than moving onto Claude (12.3, 12.7).
	for _, eff := range []string{"low", "medium", "high"} {
		if err := models.Refused(ctx, d.Server, "agy", "gemini-4-argon-"+eff); err != nil {
			t.Fatal(err)
		}
	}
	build := stopped("build", "builder", "agy", "gemini-4-argon")
	req, err := pl.NextChoice(ctx, build)
	if err != nil || req == nil || req.CLI != "agy" || req.Name != "gemini-3.8-flash-high" || req.ModelFrom != models.FromLimit {
		t.Fatalf("the builder with Argon refused: %+v %v", req, err)
	}
	if err := models.ClearRefused(ctx, d.Server); err != nil {
		t.Fatal(err)
	}
	models.Reached(ctx, d.Server, "agy", "gemini", "5h", reset)
	if req, err = pl.NextChoice(ctx, build); err != nil || req != nil {
		t.Fatalf("the builder at the Gemini group limit: %+v %v", req, err)
	}
	// The fix then ran on Sonnet, so the ticket's builder is of the Claude family.
	fix := stopped("fix", "builder", "agy", "claude-sonnet-5-5")
	// A reviewer whose Argon is at the Gemini limit falls back to Opus in the pro bracket.
	own["agents"] = settings.Doc{
		"roles":    settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/claude-opus-5-5", "agy/gemini-3.8-flash"}}},
		"brackets": settings.Doc{"pro": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}},
	}
	review := stopped("review", "reviewer", "agy", "gemini-4-argon")
	req, err = pl.NextChoice(ctx, review)
	if err != nil || req == nil || req.CLI != "agy" || req.Name != "claude-opus-5-5-high" || req.ModelFrom != models.FromLimit {
		t.Fatalf("the reviewer: %+v %v", req, err)
	}
	// The tester's pin on the ticket names Sonnet: it moves there from Gemini 3.8 Flash at the limit.
	if _, err := b.EditTicket(ctx, p, tk.Key, board.Edit{Agents: map[string]any{"tester": map[string]any{"model": "agy/claude-sonnet-5-5"}}}, board.Operator); err != nil {
		t.Fatal(err)
	}
	test := stopped("test", "tester", "agy", "gemini-3.8-flash")
	if req, err = pl.NextChoice(ctx, test); err != nil || req == nil || req.Name != "claude-sonnet-5-5-medium" || req.ModelFrom != models.FromLimit {
		t.Fatalf("the tester: %+v %v", req, err)
	}
	// With at_limit wait, nothing moves.
	own["agents"].(settings.Doc)["at_limit"] = "wait"
	if req, err = pl.NextChoice(ctx, review); err != nil || req != nil {
		t.Fatalf("at_limit wait: %+v %v", req, err)
	}
	delete(own["agents"].(settings.Doc), "at_limit")
	// Both groups at their limit: the fix has nothing that can run, so it waits.
	models.Reached(ctx, d.Server, "agy", "3p", "5h", reset)
	if req, err = pl.NextChoice(ctx, fix); err != nil || req != nil {
		t.Fatalf("the fix: %+v %v", req, err)
	}
}

// The project served least recently goes first, so projects share a window (7.9, R164).
func TestTheProjectServedLeastRecentlyGoesFirst(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	var ps []*board.Project
	for i, n := range []string{"first", "second", "third"} {
		p, err := b.CreateProject(ctx, board.NewProject{Name: n, Key: "P" + string(rune('A'+i)), Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
		if err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	run := func(p *board.Project, n int, ago time.Duration) {
		var sub int64
		d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, $2, 'run', 'lead') RETURNING id`, p.ID, n).Scan(&sub)
		id := "00000000-0000-4000-8000-00000000000" + string(rune('0'+n))
		d.Server.Exec(ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'run', 'agy')`, id, p.ID)
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation, started_at)
			VALUES ($1, $2, 1, 'ended', 'agy', 't', 't', $1::uuid, now() - $3::interval)`, id, sub, ago.String()); err != nil {
			t.Fatal(err)
		}
	}
	run(ps[0], 1, time.Minute)
	run(ps[1], 2, time.Hour)
	pl := &Pipeline{Pool: d.Server, Board: b}
	if err := pl.leastServedFirst(ctx, ps); err != nil {
		t.Fatal(err)
	}
	if ps[0].Name != "third" || ps[1].Name != "second" || ps[2].Name != "first" {
		t.Fatalf("%s %s %s", ps[0].Name, ps[1].Name, ps[2].Name)
	}
}

// evalAtLimit is an eval run reviewing, whose builder ran on Gemini 4 Argon and whose reviewer's
// subtask waits at the limit of the Claude and GPT group, as on dave in M15: the eval's reviewer
// subtask is returned.
func evalAtLimit(t *testing.T) (*Pipeline, *Tools, int64, int64) {
	t.Helper()
	ctx := context.Background()
	p, tools, er := evalKit(t, "reviewing", "ok")
	modelstest.Recorded(t, tools.Pool)
	tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now()) ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`)
	p.Settings = func(context.Context) (settings.Doc, error) {
		return settings.Defaults(settings.Machine{Version: "test"})
	}
	branch := fmt.Sprintf("eval/%d", er)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon', model_name = 'gemini-4-argon' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	var sub int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state, wait_for, branch)
		VALUES ('eval', (SELECT project FROM eval_runs WHERE id = $1), 9, $2, 'reviewer', 'waiting', 'quota:agy', $3) RETURNING id`, er, kindEvalReview, branch).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	reviewer := "00000000-0000-4000-8000-0000000000e1"
	tools.Pool.Exec(ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ($1, 'run', 'agy')`, reviewer)
	if _, err := tools.Pool.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, conversation, stop_reason)
		VALUES ($1, $2, 1, 'ended', 'agy', 'claude-opus-5-5', 'claude-opus-5-5', 'test', 'test', $1::uuid, 'quota')`, reviewer, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Pool.Exec(ctx, `UPDATE eval_runs SET reviewer_run = $2::uuid, branch = $3, detail = jsonb_set(detail, '{runs}', jsonb_build_array(builder_run::text, $2::text))
		WHERE id = $1`, er, reviewer, branch); err != nil {
		t.Fatal(err)
	}
	models.Reached(ctx, tools.Pool, "agy", "3p", "5h", time.Now().Add(time.Hour))
	return p, tools, er, sub
}

// An eval's reviewer at a limit does not move to the builder's family: a run with no ticket keeps the
// rule of a ticket's check (rule 15). On dave in M15 it moved to Gemini 4 Argon, the builder's model.
func TestAnEvalsCheckAtALimitStaysOffTheBuildersFamily(t *testing.T) {
	p, _, _, sub := evalAtLimit(t)
	req, err := p.NextChoice(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if req != nil && cli.Family(req.Model) == cli.Family("gemini-4-argon") {
		t.Fatalf("the eval's reviewer moved to the builder's family: %+v", req)
	}
	// With the builder's model not to be read, it waits, where it would have moved to Gemini 4 Argon:
	// a family it cannot name is no family it may land on (security review of 0.50).
	ctx := context.Background()
	p, tools, er, sub := evalAtLimit(t)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = NULL WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	if req, err := p.NextChoice(ctx, sub); err != nil || req != nil {
		t.Fatalf("with no builder's model: %+v %v", req, err)
	}
	// An eval's tester checks nothing of the builder's family: it moves, as a ticket's does (review of
	// 0.2.4).
	tools.Pool.Exec(ctx, `UPDATE subtasks SET role = 'tester' WHERE id = $1`, sub)
	if req, err := p.NextChoice(ctx, sub); err != nil || req == nil {
		t.Fatalf("an eval's tester with no builder's model: %+v %v", req, err)
	}
	tools.Pool.Exec(ctx, `UPDATE subtasks SET role = 'reviewer' WHERE id = $1`, sub)
	// The builder's model again, but a subtask with no branch names no eval run (round 2 of the review).
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	tools.Pool.Exec(ctx, `UPDATE subtasks SET branch = NULL WHERE id = $1`, sub)
	if req, err := p.NextChoice(ctx, sub); err != nil || req != nil {
		t.Fatalf("with no branch: %+v %v", req, err)
	}
}

// An eval that ends, or whose suite stops, ends its subtasks that wait: nothing continues them after
// the eval's branch is gone, by a move at the limit or after the reset (M15 on dave).
func TestAnEndedEvalLeavesNoSubtaskWaiting(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"its reviewer ended at a limit", "its project paused", "it ended before, and its subtask was left"} {
		p, tools, er, sub := evalAtLimit(t)
		switch how {
		case "its project paused":
			tools.Pool.Exec(ctx, `UPDATE projects SET state = 'paused' WHERE id = (SELECT project FROM eval_runs WHERE id = $1)`, er)
		case "it ended before, and its subtask was left":
			// As an error at its end, or a release before 0.2.3, left the eval subtasks on dave (review
			// of 0.50).
			tools.Pool.Exec(ctx, `UPDATE eval_runs SET state = 'failed', ended_at = now() WHERE id = $1`, er)
			tools.Pool.Exec(ctx, `UPDATE subtasks SET state = 'stopped', wait_for = 'operator' WHERE id = $1`, sub)
		}
		if err := p.evals(ctx, settings.Doc{}); err != nil {
			t.Fatal(err)
		}
		var evalState, subState string
		tools.Pool.QueryRow(ctx, `SELECT state FROM eval_runs WHERE id = $1`, er).Scan(&evalState)
		tools.Pool.QueryRow(ctx, `SELECT state FROM subtasks WHERE id = $1`, sub).Scan(&subState)
		if evalState != "failed" && evalState != "stopped" {
			t.Fatalf("%s: the eval run is %s", how, evalState)
		}
		if subState == "waiting" || subState == "stopped" {
			t.Errorf("%s: the eval run is %s, and its reviewer's subtask is %s", how, evalState, subState)
		}
	}
}

// A ticket's check at a limit whose builder's model Crew cannot read waits, as an eval's does, rather
// than choose with no family to avoid; a role that checks nothing still moves on a ticket with no build
// yet (the operator's answer to the fourth report of M15, item 4).
func TestATicketsCheckAtALimitWaitsWhenItsBuilderCannotBeRead(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	p, err := b.CreateProject(ctx, board.NewProject{Name: "shop", Key: "SF", Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := b.AddTicket(ctx, p.Name, board.NewTicket{Title: "One", Why: "w", What: "w", Goal: "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	if err != nil {
		t.Fatal(err)
	}
	d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`)
	modelstest.Recorded(t, d.Server)
	own := settings.Doc{"agents": settings.Doc{
		"roles": settings.Doc{
			"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/claude-opus-5-5", "agy/gemini-3.8-flash"}},
			"tester":   settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/gemini-3.8-flash"}},
		},
		"brackets": settings.Doc{"pro": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}},
	}}
	pl := &Pipeline{Pool: d.Server, Board: b, Settings: func(context.Context) (settings.Doc, error) {
		def, err := settings.Defaults(settings.Machine{Version: "test"})
		return settings.Merge(def, own), err
	}}
	n := 0
	stopped := func(kind, role, model, stop string) int64 {
		t.Helper()
		n++
		var sub int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, wait_for)
			VALUES ('ticket', $1, $2, $3, $4, $5, 'waiting', 'quota:agy') RETURNING id`, p.ID, tk.ID, n, kind, role).Scan(&sub); err != nil {
			t.Fatal(err)
		}
		conv := fmt.Sprintf("00000000-0000-4000-8000-0000000000%02d", n)
		d.Server.Exec(ctx, `INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ($1, $2, $3, 'run', 'agy')`, conv, p.ID, tk.ID)
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, conversation, stop_reason)
			VALUES ($1, $2, 1, 'ended', 'agy', nullif($3, ''), nullif($3, ''), 'test', 'test', $1::uuid, $4)`, conv, sub, model, stop); err != nil {
			t.Fatal(err)
		}
		return sub
	}
	for _, eff := range []string{"low", "medium", "high"} {
		if err := models.Refused(ctx, d.Server, "agy", "gemini-4-argon-"+eff); err != nil {
			t.Fatal(err)
		}
	}

	// No build has run on the ticket: the reviewer with Argon refused cannot tell the builder's family,
	// so it waits, where it would have moved to Opus.
	review := stopped("review", "reviewer", "gemini-4-argon", "quota")
	if req, err := pl.NextChoice(ctx, review); err != nil || req != nil {
		t.Fatalf("a reviewer with no build: %+v %v", req, err)
	}
	// With the rule for checks off, there is no family to keep the reviewer off: it moves to Opus.
	own["agents"].(settings.Doc)["checks_use_another_family"] = false
	if req, err := pl.NextChoice(ctx, review); err != nil || req == nil || req.Model != "claude-opus-5-5" {
		t.Fatalf("a reviewer with no build and the rule off: %+v %v", req, err)
	}
	delete(own["agents"].(settings.Doc), "checks_use_another_family")
	// A tester checks nothing of the builder's family (12.3): it moves to Gemini 3.8 Flash.
	test := stopped("test", "tester", "gemini-4-argon", "quota")
	if req, err := pl.NextChoice(ctx, test); err != nil || req == nil || req.Model != "gemini-3.8-flash" {
		t.Fatalf("a tester with no build: %+v %v", req, err)
	}
	// A build whose model was not recorded: the reviewer still waits.
	stopped("build", "builder", "", "done")
	if req, err := pl.NextChoice(ctx, review); err != nil || req != nil {
		t.Fatalf("a reviewer whose builder has no model: %+v %v", req, err)
	}
	// The build's model is read: Opus is the builder's family, so the reviewer moves past it to Gemini 3.8 Flash.
	d.Server.Exec(ctx, `UPDATE runs SET model = 'claude-sonnet-5-5' WHERE model IS NULL`)
	if req, err := pl.NextChoice(ctx, review); err != nil || req == nil || req.Name != "gemini-3.8-flash-high" {
		t.Fatalf("a reviewer whose builder ran on Sonnet: %+v %v", req, err)
	}
}

// A subtask stopped at a limit, or idle between turns when its group goes to its limit or above
// quota.slow_down_at (0.8), continues within its bracket on a model in another group that has room;
// a pinned subtask waits (12.7).
func TestAStoppedSubtaskContinuesWithinItsBracket(t *testing.T) {
	k, rr := familyKit(t)
	ctx := k.ctx
	reset := time.Now().Add(time.Hour)

	// 1. A lead subtask stopped at the 3p limit continues on gemini-4-argon in pro.
	models.Reached(ctx, k.p.Pool, "agy", "3p", "5h", reset)
	leadSub := k.ran("analysis", "lead", "claude-opus-5-5", 4)
	req, err := k.p.NextChoice(ctx, leadSub)
	if err != nil || req == nil || req.Model != "gemini-4-argon" || req.Name != "gemini-4-argon-high" || req.ModelFrom != models.FromLimit {
		t.Fatalf("lead stopped at 3p limit: %+v %v", req, err)
	}

	// 2. A reviewer subtask stopped at the 3p limit continues on gemini-4-argon when its builder ran on
	// Claude (both with the rule for checks on and with it off for the project).
	buildSub := k.ran("build", "builder", "claude-opus-5-5", 3)
	revSub := k.ran("review", "reviewer", "claude-opus-5-5", 2)
	agentsDoc := k.j.machine["agents"].(settings.Doc)
	agentsDoc["checks_use_another_family"] = false
	req, err = k.p.NextChoice(ctx, revSub)
	if err != nil || req == nil || req.Model != "gemini-4-argon" || req.ModelFrom != models.FromLimit {
		t.Fatalf("reviewer with rule off at 3p limit: %+v %v", req, err)
	}
	agentsDoc["checks_use_another_family"] = true
	// With the builder on gemini-4-argon and 3p at its limit, the reviewer falls back to gemini-4-argon,
	// and waits when at_limit is wait.
	if _, err := k.p.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon', model_name = 'gemini-4-argon' WHERE subtask = $1`, buildSub); err != nil {
		t.Fatal(err)
	}
	if req, err = k.p.NextChoice(ctx, revSub); err != nil || req == nil || req.Model != "gemini-4-argon" || req.ModelFrom != models.FromLimit {
		t.Fatalf("reviewer of Gemini build with 3p at limit falls back: %+v %v", req, err)
	}
	agentsDoc["at_limit"] = "wait"
	if req, err = k.p.NextChoice(ctx, revSub); err != nil || req != nil {
		t.Fatalf("reviewer of Gemini build with at_limit wait: %+v %v", req, err)
	}
	agentsDoc["at_limit"] = "move"

	// 3. A pinned subtask is never moved at a limit: it waits.
	if _, err := k.p.Board.EditTicket(ctx, k.j.proj, k.one.Key, board.Edit{Agents: map[string]any{"builder": map[string]any{"model": "agy/claude-opus-5-5"}}}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(ctx, `UPDATE runs SET model = 'claude-opus-5-5', model_name = 'claude-opus-5-5' WHERE subtask = $1`, buildSub); err != nil {
		t.Fatal(err)
	}
	if req, err = k.p.NextChoice(ctx, buildSub); err != nil || req != nil {
		t.Fatalf("pinned builder at 3p limit: %+v %v", req, err)
	}
	if _, err := k.p.Board.EditTicket(ctx, k.j.proj, k.one.Key, board.Edit{Agents: map[string]any{}}, board.Operator); err != nil {
		t.Fatal(err)
	}

	// 4. An idle builder subtask between turns (continueRun) with Gemini above slow_down_at (0.85)
	// stays on Gemini and never moves onto Claude; an idle reviewer on Opus with 3p above slow_down_at
	// moves within pro to gemini-4-argon (12.3, 12.7).
	k.p.Pool.Exec(ctx, `DELETE FROM quota`)
	if err := models.RecordLimits(ctx, k.p.Pool, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	idleBuild := k.ran("build", "builder", "gemini-4-argon", 1)
	if _, err := k.p.Pool.Exec(ctx, `UPDATE subtasks SET state = 'idle' WHERE id = $1`, idleBuild); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(ctx, `UPDATE runs SET state = 'idle', stop_reason = NULL WHERE subtask = $1`, idleBuild); err != nil {
		t.Fatal(err)
	}
	s := &sub{id: idleBuild, kind: kindBuild, role: "builder", state: "idle", runState: "idle", cli: "agy", model: "gemini-4-argon"}
	if err := k.p.continueRun(ctx, k.j, s, "Go on.", nil); err != nil {
		t.Fatal(err)
	}
	if len(rr.continued) != 1 || rr.continued[0].Model != "" {
		t.Fatalf("idle builder between turns with Gemini at 0.85 moved: %+v", rr.continued)
	}
	k.p.Pool.Exec(ctx, `DELETE FROM quota`)
	if err := models.RecordLimits(ctx, k.p.Pool, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	agentsDoc["checks_use_another_family"] = false
	if _, err := k.p.Pool.Exec(ctx, `UPDATE subtasks SET state = 'idle' WHERE id = $1`, revSub); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(ctx, `UPDATE runs SET state = 'idle', stop_reason = NULL WHERE subtask = $1`, revSub); err != nil {
		t.Fatal(err)
	}
	revIdle := &sub{id: revSub, kind: kindReview, role: "reviewer", state: "idle", runState: "idle", cli: "agy", model: "claude-opus-5-5"}
	if err := k.p.continueRun(ctx, k.j, revIdle, "Go on.", nil); err != nil {
		t.Fatal(err)
	}
	if len(rr.continued) != 2 || rr.continued[1].Model != "gemini-4-argon" || rr.continued[1].Name != "gemini-4-argon-high" || rr.continued[1].ModelFrom != models.FromLimit {
		t.Fatalf("idle reviewer between turns with 3p at 0.85: %+v", rr.continued)
	}
}
