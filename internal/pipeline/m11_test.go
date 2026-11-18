package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// A run is watched with its project's numbers (8.2): the watchdog. No limit is in dollars (7.7, since
// 0.53).
func TestRunLimitsAreTheProjectsSettings(t *testing.T) {
	ctx := context.Background()
	tools, p, c := toolsKit(t)
	doc, err := tools.Board.Settings(ctx, tools.Pool, p)
	if err != nil {
		t.Fatal(err)
	}
	doc["watchdog"].(settings.Doc)["silent_warn_minutes"] = 7
	doc["watchdog"].(settings.Doc)["repeat_fail_stop"] = 4
	// A document from before 0.53 still names budgets: they are taken out as it is saved (7.7).
	doc["limits"].(settings.Doc)["run_budget_usd"] = 2
	doc["limits"].(settings.Doc)["default_task_budget_usd"] = 3
	doc["budget_usd"] = 40
	if err := tools.Board.SaveSettings(ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	var own string
	if err := tools.Pool.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project = $1`, p.ID).Scan(&own); err != nil || strings.Contains(own, "budget") {
		t.Fatalf("the stored settings: %s %v", own, err)
	}
	var sub int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, 1, 'build', 'builder', 'running') RETURNING id`, p.ID, c.Ticket).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	l, err := pl.RunLimits(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if l.SilentWarn != 7*time.Minute || l.SilentStop != runs.SilentStop || l.FailStop != 4 {
		t.Fatalf("%+v", l)
	}
}

// A ticket in its stage three times the usual time there, the median of the project's past stays,
// is news once; with fewer than three past stays there is no usual time yet (8.2).
func TestATicketLongInItsStageIsNewsOnce(t *testing.T) {
	ctx := context.Background()
	tools, p, c := toolsKit(t)
	now := time.Now()
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	doc, _ := tools.Board.Settings(ctx, tools.Pool, p)
	j := &job{doc: doc, proj: p}
	// Past tickets stayed 10 minutes in Review.
	stay := func(n int) {
		var id int64
		if err := tools.Pool.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 'Past', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`, p.ID, 100+n, 100+n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(5+n) * time.Hour)
		if _, err := tools.Pool.Exec(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, at, actor) VALUES
			($1, 'in_progress', 'review', $2, 'backend'), ($1, 'review', 'done', $3, 'backend')`, id, at, at.Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	stay(1)
	stay(2)
	if _, err := tools.Pool.Exec(ctx, `UPDATE tickets SET stage = 'review', stage_at = $2 WHERE id = $1`, c.Ticket, now.Add(-40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		tools.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE ticket = $1 AND kind = 'slow_stage'`, c.Ticket).Scan(&n)
		return n
	}
	if err := pl.slowStages(ctx, j, now); err != nil || count() != 0 {
		t.Fatalf("with two past stays: %v, %d signals", err, count())
	}
	stay(3)
	if err := pl.slowStages(ctx, j, now); err != nil || count() != 1 {
		t.Fatalf("%v, %d signals, want 1", err, count())
	}
	if err := pl.slowStages(ctx, j, now.Add(time.Hour)); err != nil || count() != 1 {
		t.Fatalf("told again: %d signals", count())
	}
	var text string
	tools.Pool.QueryRow(ctx, `SELECT detail->>'text' FROM signals WHERE ticket = $1 AND kind = 'slow_stage'`, c.Ticket).Scan(&text)
	if !strings.HasPrefix(text, "SF-1 has been in Review for 40m0s, 4 times its usual 10m0s there") {
		t.Fatalf("%q", text)
	}
}

// fakeRuns records what the pipeline asks of the run driver.
type fakeRuns struct {
	mu    sync.Mutex
	warns []string
}

func (f *fakeRuns) StartRole(context.Context, runs.RoleRun) (string, int64, error) { return "", 0, nil }
func (f *fakeRuns) Continue(context.Context, int64, runs.ContinueRequest) (string, error) {
	return "", nil
}
func (f *fakeRuns) Finish(context.Context, string) error              { return nil }
func (f *fakeRuns) Stop(context.Context, string) error                { return nil }
func (f *fakeRuns) End(context.Context, string, string, string) error { return nil }
func (f *fakeRuns) EndWaited(context.Context, string, string, string) error {
	return nil
}
func (f *fakeRuns) Warn(_ context.Context, run, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warns = append(f.warns, run+": "+text)
}

// The third round of checks in one run is a warning on its card (8.2); the others are not.
func TestTheThirdRoundOfChecksWarns(t *testing.T) {
	ctx := context.Background()
	tools, p, c := toolsKit(t)
	f := &fakeRuns{}
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board, Runs: f}
	doc, _ := tools.Board.Settings(ctx, tools.Pool, p)
	j := &job{doc: doc, proj: p}
	tk := &board.Ticket{ID: c.Ticket}
	for round := 0; round < 4; round++ {
		if err := pl.round(ctx, j, tk, &sub{id: 1, kind: "build", role: "builder", run: "r1", goalRounds: round}, "again"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.warns) != 1 || f.warns[0] != "r1: 3 rounds of checks in this run" {
		t.Fatalf("%v", f.warns)
	}
}

// The look again's tools work only in the lead's run that looks again at its own ticket (7.7).
func TestTheLookAgainToolsRefuseAnyOtherRun(t *testing.T) {
	ctx := context.Background()
	tools, p, c := toolsKit(t)
	if _, err := tools.Board.OK(ctx, p.Name, board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Pool.Exec(ctx, `UPDATE tickets SET stage = 'in_progress' WHERE id = $1`, c.Ticket); err != nil {
		t.Fatal(err)
	}
	subtask := func(kind, role string) int64 {
		var id int64
		if err := tools.Pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
			VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, $4, 'running') RETURNING id`,
			p.ID, c.Ticket, kind, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	approach := json.RawMessage(`{"reason": "r", "approach": "a"}`)
	builder := c
	builder.Role, builder.Subtask = "builder", subtask(kindBuild, "builder")
	if _, err := tools.newApproach(ctx, builder, approach); err == nil || !strings.Contains(err.Error(), "looks again") {
		t.Fatalf("a builder's new_approach: %v", err)
	}
	analysis := c
	analysis.Role, analysis.Subtask = "lead", subtask(kindAnalysis, "lead")
	if _, err := tools.cancelAndReplace(ctx, analysis, json.RawMessage(`{"reason": "r"}`)); err == nil {
		t.Fatal("the lead's analysis cancelled its ticket")
	}
	lead := c
	lead.Role, lead.Subtask = "lead", subtask(kindLookAgain, "lead")
	if _, err := tools.cancelAndReplace(ctx, lead, json.RawMessage(`{"reason": "r"}`)); err == nil || !strings.Contains(err.Error(), "name the tickets") {
		t.Fatalf("cancel with no replacement: %v", err)
	}
	if _, err := tools.newApproach(ctx, lead, approach); err != nil {
		t.Fatal(err)
	}
	// write_report is only the report run's.
	report := json.RawMessage(`{"built": "b", "skipped": "s", "cancelled": "c", "missing": "m", "learned": "l"}`)
	if _, err := tools.writeReport(ctx, lead, report); err == nil || !strings.Contains(err.Error(), "final report") {
		t.Fatalf("a look again's write_report: %v", err)
	}
	reporter := c
	reporter.Role, reporter.Subtask = "lead", subtask(kindReport, "lead")
	if _, err := tools.writeReport(ctx, reporter, report); err != nil {
		t.Fatal(err)
	}
}

// add_e2e_result checks the role and the run's kind before it reads anything of the run's home: a
// builder's call, or a call from a run of another kind, is refused before the screenshots are looked
// at (security review of M10).
func TestAddE2EResultChecksTheRoleFirst(t *testing.T) {
	ctx := context.Background()
	tools, p, c := toolsKit(t)
	read := 0
	tools.Home = func(context.Context, string) (string, error) { read++; return t.TempDir(), nil }
	var sub int64
	if err := tools.Pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, 1, 'build', 'builder', 'running') RETURNING id`, p.ID, c.Ticket).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"result": "pass", "text": "t", "screenshots": ["../../etc/passwd"]}`)
	builder := c
	builder.Role, builder.Subtask = "builder", sub
	if _, err := tools.addE2EResult(ctx, builder, args); err == nil || !strings.Contains(err.Error(), "E2E tester's run") {
		t.Fatalf("a builder's call: %v", err)
	}
	e2e := c
	e2e.Role, e2e.Subtask = "e2e", sub
	if _, err := tools.addE2EResult(ctx, e2e, args); err == nil || !strings.Contains(err.Error(), "for an E2E run") {
		t.Fatalf("an e2e role in a build run: %v", err)
	}
	if read != 0 {
		t.Fatalf("the run's home was read %d times before the role was checked", read)
	}
}

// idleRuns records started role runs as idle runs in the database, and ends them when finished.
type idleRuns struct {
	recRuns
	pool *pgxpool.Pool
}

func (r *idleRuns) StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error) {
	sub := rr.Subtask
	if sub == 0 && rr.Ticket != 0 {
		if err := r.pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, title, branch, state)
			VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, $4, nullif($5, ''), nullif($6, ''), 'running') RETURNING id`,
			rr.Project, rr.Ticket, rr.Kind, rr.Role, rr.Title, rr.Branch).Scan(&sub); err != nil {
			return "", 0, err
		}
		rr.Subtask = sub
	}
	if _, _, err := r.recRuns.StartRole(ctx, rr); err != nil || sub == 0 {
		return "", 0, err
	}
	var id string
	if err := r.pool.QueryRow(ctx, `WITH c AS (
		INSERT INTO conversations (id, purpose, subtask, cli) VALUES (gen_random_uuid(), 'run', $1, 'agy') RETURNING id
	) INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, conversation, started_at)
		SELECT gen_random_uuid(), $1, 1, 'idle', 'agy', nullif($2, ''), nullif($2, ''), 'test', 'img', id, now() FROM c RETURNING id::text`,
		sub, rr.Model).Scan(&id); err != nil {
		return "", 0, err
	}
	if _, err := r.pool.Exec(ctx, `UPDATE subtasks SET state = 'running' WHERE id = $1`, sub); err != nil {
		return "", 0, err
	}
	if rr.Record != nil {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return "", 0, err
		}
		defer tx.Rollback(ctx)
		if err := rr.Record(ctx, tx, id); err != nil {
			return "", 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", 0, err
		}
	}
	return id, sub, nil
}

func (r *idleRuns) Finish(ctx context.Context, run string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE runs SET state = 'ended', stop_reason = 'ok', ended_at = now() WHERE id = $1`, run); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `UPDATE subtasks SET state = 'done', ended_at = now() WHERE id = (SELECT subtask FROM runs WHERE id = $1)`, run)
	return err
}

// A ticket in In progress whose build stopped after pushing its head gets the lead's new approach as a
// build run: its builder calls request_move without pushing again, Crew runs In progress's exit command
// checks and judge on that head, and the ticket moves to Review; in a later stage a new approach stays
// a fix run (7.7, CREW-16).
func TestANewApproachInInProgressRunsItsExitChecks(t *testing.T) {
	k, _ := familyKit(t)
	ir := &idleRuns{pool: k.p.Pool}
	k.p.Runs = ir
	if _, err := k.p.Board.OK(k.ctx, k.j.proj.Name, board.Operator); err != nil {
		t.Fatal(err)
	}
	var err error
	if k.j.proj, err = k.p.Board.Project(k.ctx, k.j.proj.ID); err != nil {
		t.Fatal(err)
	}
	if k.j.doc, err = k.p.Board.Settings(k.ctx, k.p.Pool, k.j.proj); err != nil {
		t.Fatal(err)
	}
	_, head := k.branched()
	tools := &Tools{Pool: k.p.Pool, Board: k.p.Board}
	quarantineGit(t, tools, k.j.proj.Name)("update-ref", "refs/heads/task/SF-1-one", head)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'in_progress', docs_reason = 'none',
		source = '{"file": "PLAN.md", "heading": "Goal"}',
		analysis = '{"status": "ready", "decidedBy": "lead"}' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO checks (ticket, position, kind, spec) VALUES ($1, 2, 'judge', 'the approach is carried out')`, k.one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, body)
		VALUES ($1, $2, 'note', 'builder', 'Pushed the fix before stopping.')`, k.j.proj.ID, k.one.ID); err != nil {
		t.Fatal(err)
	}
	// A part from the first approach was joined, and the build run stopped on its pushed head.
	oldPart := k.ran(kindPart, "builder", "claude-sonnet-5-5", 3)
	oldBuild := k.ran(kindBuild, "builder", "claude-sonnet-5-5", 2)
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'done', joined_at = now() WHERE id = $1`, oldPart); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE subtasks SET state = 'stopped' WHERE id = $1`, oldBuild); err != nil {
		t.Fatal(err)
	}
	var lookSub int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, 3, $3, 'lead', 'queued') RETURNING id`, k.j.proj.ID, k.one.ID, kindLookAgain).Scan(&lookSub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ir.StartRole(k.ctx, runs.RoleRun{Subtask: lookSub, Role: "lead", Kind: kindLookAgain, Model: "claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	ir.started = nil
	if _, err := tools.newApproach(k.ctx, mcp.Caller{Project: k.j.proj.ID, Role: "lead", Subtask: lookSub, Ticket: k.one.ID},
		json.RawMessage(`{"reason": "stopped after pushing", "approach": "hand over the pushed head"}`)); err != nil {
		t.Fatal(err)
	}
	reload := func() []*sub {
		t.Helper()
		var err error
		if k.one, err = k.p.Board.Ticket(k.ctx, k.j.proj, "SF-1"); err != nil {
			t.Fatal(err)
		}
		subs, err := k.p.subtasks(k.ctx, k.one)
		if err != nil {
			t.Fatal(err)
		}
		return subs
	}
	reload()
	if err := k.p.ticket(k.ctx, k.j, k.one); err != nil {
		t.Fatal(err)
	}
	if len(ir.started) != 1 {
		t.Fatalf("after look_again: %d runs started, want 1", len(ir.started))
	}
	buildRun := ir.started[0]
	var prompt strings.Builder
	for _, p := range buildRun.Parts {
		prompt.WriteString(p.Part + ":" + p.Text + "\n")
	}
	if buildRun.Kind != kindBuild || buildRun.Continues != "" || buildRun.Branch != "task/SF-1-one" ||
		!strings.Contains(prompt.String(), "request_move") || !strings.Contains(prompt.String(), "plan:") ||
		!strings.Contains(prompt.String(), "The lead's new approach") {
		t.Fatalf("the new approach's builder run: kind=%q continues=%q branch=%q prompt=%q",
			buildRun.Kind, buildRun.Continues, buildRun.Branch, prompt.String())
	}
	// A retried afterLookAgain pass after NewApproach recorded ApproachAt starts no second builder.
	subs := reload()
	if err := k.p.afterLookAgain(k.ctx, k.j, k.one, subs, subs[2]); err != nil {
		t.Fatal(err)
	}
	if len(ir.started) != 1 {
		t.Fatalf("after a retried afterLookAgain pass: %d runs started, want 1", len(ir.started))
	}
	// The builder calls request_move without pushing a new commit.
	if _, err := tools.reporter(kindBuild, "builder", "request_move")(k.ctx, mcp.Caller{Project: k.j.proj.ID, Role: "builder", Subtask: buildRun.Subtask, Ticket: k.one.ID},
		json.RawMessage(`{}`)); err != nil {
		t.Fatalf("request_move on the new approach's build run: %v", err)
	}
	// Pass 1 of afterBuild runs In progress's exit command checks on the already-pushed head.
	reload()
	if err := k.p.ticket(k.ctx, k.j, k.one); err != nil {
		t.Fatal(err)
	}
	k.p.wg.Wait()
	if n := k.count(`SELECT count(*) FROM check_runs WHERE ticket = $1 AND purpose = 'exit' AND commit = $2 AND state = 'passed'`, k.one.ID, head); n != 1 {
		t.Fatalf("%d passed exit check runs on %s, want 1", n, head)
	}
	// Pass 2 of afterBuild starts the judge on that head.
	reload()
	if err := k.p.ticket(k.ctx, k.j, k.one); err != nil {
		t.Fatal(err)
	}
	if len(ir.started) != 2 || ir.started[1].Kind != kindJudge {
		t.Fatalf("after the exit command checks passed: %+v", ir.started)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO turns (conversation, number, run, speaker, text)
		SELECT conversation, 1, id, 'agent', '1. MET: carried out.' FROM runs WHERE subtask = $1`, ir.started[1].Subtask); err != nil {
		t.Fatal(err)
	}
	// Pass 3 finishes the judge; Pass 4 reconciles SF-1 into Review and finishes the builder's run.
	for range 2 {
		reload()
		if err := k.p.ticket(k.ctx, k.j, k.one); err != nil {
			t.Fatal(err)
		}
	}
	subs = reload()
	if k.one.Stage != settings.StageReview {
		t.Fatalf("SF-1 is in %s, want %s (waiting: %v)", k.one.Stage, settings.StageReview, k.one.Waiting)
	}
	// In a later stage (Review), a new approach stays a fix run.
	if _, err := k.p.approach(k.ctx, k.j, k.one, subs, &report{Approach: "fix what Review found"}); err != nil {
		t.Fatal(err)
	}
	if len(ir.started) != 3 || ir.started[2].Kind != kindFix {
		t.Fatalf("in Review, approach() started %+v", ir.started)
	}
}

// hasParts ignores parts that are cancelled, superseded, or already done and joined before a new
// approach's build, while keeping parts after the build, parts with no build, and older parts that
// still owe a join (7.7, 7.11, CREW-16).
func TestHasParts(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		subs []*sub
		want bool
	}{
		{"old part done and joined", []*sub{{id: 1, kind: kindPart, state: "done", joined: &now}, {id: 2, kind: kindBuild, state: "running"}}, false},
		{"old part superseded", []*sub{{id: 1, kind: kindPart, state: "superseded"}, {id: 2, kind: kindBuild, state: "running"}}, false},
		{"old part cancelled", []*sub{{id: 1, kind: kindPart, state: "cancelled"}, {id: 2, kind: kindBuild, state: "running"}}, false},
		{"old part done but not joined", []*sub{{id: 1, kind: kindPart, state: "done"}, {id: 2, kind: kindBuild, state: "running"}}, true},
		{"old part running", []*sub{{id: 1, kind: kindPart, state: "running"}, {id: 2, kind: kindBuild, state: "running"}}, true},
		{"part after the build", []*sub{{id: 1, kind: kindBuild, state: "done"}, {id: 2, kind: kindPart, state: "done", joined: &now}}, true},
		{"no build with a part", []*sub{{id: 1, kind: kindPart, state: "queued"}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hasParts(c.subs); got != c.want {
				t.Fatalf("hasParts() = %v, want %v", got, c.want)
			}
		})
	}
}

// Only a new approach's stopped build counts as a fix round alongside a fix subtask in fixedAfter,
// fixedSince and fixedAfterRun (7.7, register row 82).
func TestOnlyANewApproachsStoppedBuildCountsAsAFixRound(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	after := base.Add(time.Minute)
	approachAt := base.Add(2 * time.Minute)
	afterApproach := base.Add(3 * time.Minute)
	mark := &sub{id: 1, created: base}
	run := &board.CheckRun{EndedAt: &base}
	for _, tc := range []struct {
		name string
		tk   *board.Ticket
		s    *sub
		want bool
	}{
		{"a fix subtask", &board.Ticket{}, &sub{id: 2, kind: kindFix, state: "running", created: after}, true},
		{"a stopped build without a new approach", &board.Ticket{}, &sub{id: 2, kind: kindBuild, state: "stopped", created: after}, false},
		{"a stopped build before the new approach", &board.Ticket{ApproachAt: &approachAt}, &sub{id: 2, kind: kindBuild, state: "stopped", created: after}, false},
		{"a running build of the new approach", &board.Ticket{ApproachAt: &approachAt}, &sub{id: 2, kind: kindBuild, state: "running", created: afterApproach}, false},
		{"the stopped build of the new approach", &board.Ticket{ApproachAt: &approachAt}, &sub{id: 2, kind: kindBuild, state: "stopped", created: afterApproach}, true},
	} {
		subs := []*sub{mark, tc.s}
		if got := fixedAfter(tc.tk, subs, mark); got != tc.want {
			t.Errorf("%s: fixedAfter = %v, want %v", tc.name, got, tc.want)
		}
		if got := fixedSince(tc.tk, subs, base); got != tc.want {
			t.Errorf("%s: fixedSince = %v, want %v", tc.name, got, tc.want)
		}
		if got := fixedAfterRun(tc.tk, subs, run); got != tc.want {
			t.Errorf("%s: fixedAfterRun = %v, want %v", tc.name, got, tc.want)
		}
	}
}
