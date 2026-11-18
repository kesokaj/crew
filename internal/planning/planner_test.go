package planning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// fakeRuns records what the planner starts, and makes the rows a run would have, so the slots count.
type fakeRuns struct {
	t        *testing.T
	d        *dbtest.DB
	mu       sync.Mutex
	roles    []runs.RoleRun
	conts    []string
	ended    []string
	n        int
	failNext bool
}

func (f *fakeRuns) StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles = append(f.roles, rr)
	var sub int64
	scope, project, draft := "crew", any(nil), any(rr.Draft)
	if rr.Project != "" {
		scope, project, draft = "project", rr.Project, nil
	}
	if err := f.d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, draft, number, kind, role) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		scope, project, draft, len(f.roles), rr.Kind, rr.Role).Scan(&sub); err != nil {
		f.t.Fatal(err)
	}
	run, err := f.record(ctx, sub, rr.CLI, rr.Model, rr.Record)
	return run, sub, err
}

// record writes a run row and calls the hook in the same transaction, as the driver does.
func (f *fakeRuns) record(ctx context.Context, sub int64, cli, model string, hook func(context.Context, pgx.Tx, string) error) (string, error) {
	f.n++
	run := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.n)
	err := pgx.BeginFunc(ctx, f.d.Server, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image)
			VALUES ($1, $2, (SELECT coalesce(max(number), 0) + 1 FROM runs WHERE subtask = $2), 'working', $3, $4, 'test', 'test')`,
			run, sub, cli, model); err != nil {
			return err
		}
		if hook != nil {
			return hook(ctx, tx, run)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return run, nil
}

func (f *fakeRuns) Continue(ctx context.Context, subtask int64, req runs.ContinueRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return "", errors.New("the microVM did not start")
	}
	run, err := f.record(ctx, subtask, req.CLI, req.Model, req.Record)
	if err != nil {
		return "", err
	}
	f.conts = append(f.conts, fmt.Sprintf("%d:%s", subtask, req.Text))
	return run, nil
}

func (f *fakeRuns) Finish(_ context.Context, run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, "finish:"+run)
	return nil
}

func (f *fakeRuns) Stop(_ context.Context, run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, "stop:"+run)
	return nil
}

func newPlanner(t *testing.T, own settings.Doc) (*Planner, *fakeRuns, *dbtest.DB) {
	t.Helper()
	d := dbtest.New(t, false)
	f := &fakeRuns{t: t, d: d}
	modelstest.Recorded(t, d.Server) // the lead's models, as agy lists them
	pl := &Planner{Pool: d.Server, Runs: f, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: func(ctx context.Context) (settings.Doc, error) {
			def, err := settings.Defaults(settings.Machine{Version: "test"})
			return settings.Merge(def, own), err
		}}
	return pl, f, d
}

func signIn(t *testing.T, d *dbtest.DB, clis ...string) {
	t.Helper()
	for _, c := range clis {
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO cli_logins (cli, state, checked_at) VALUES ($1, 'signed_in', now())
			ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`, c); err != nil {
			t.Fatal(err)
		}
	}
}

func draftWith(t *testing.T, d *dbtest.DB, text string) string {
	t.Helper()
	id, err := (&Drafts{Pool: d.Server}).Create(context.Background(), NewDraft{Message: text})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLeadRunsWaitForASlotOfTheirOwnCLI(t *testing.T) {
	pl, f, d := newPlanner(t, settings.Doc{"lead_slots": settings.Doc{"agy": 1}})
	signIn(t, d, "agy")
	ctx := context.Background()
	a := draftWith(t, d, "first")
	b := draftWith(t, d, "second")
	pl.Reconcile(ctx)
	if len(f.roles) != 1 || f.roles[0].Draft != a {
		t.Fatalf("with one slot of agy: %+v", f.roles)
	}
	if f.roles[0].CLI != "agy" {
		t.Fatalf("the lead run's CLI: %+v", f.roles[0])
	}
	// agy's one slot is taken: the second draft waits (7.9).
	pl.Reconcile(ctx)
	if len(f.roles) != 1 {
		t.Fatalf("a second run started without a slot: %+v", f.roles)
	}
	// The planning agent that waits for the operator holds no slot.
	d.Server.Exec(ctx, `UPDATE runs SET state = 'idle'`)
	pl.Reconcile(ctx)
	if len(f.roles) != 2 || f.roles[1].Draft != b {
		t.Fatalf("after the slot was given back: %+v", f.roles)
	}
	// Each message went to its run once.
	var waiting int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM draft_messages WHERE run IS NULL`).Scan(&waiting)
	if waiting != 0 {
		t.Fatalf("%d messages still wait", waiting)
	}
}

func TestADraftsMessageWaitsForTheTurnThatRuns(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	ctx := context.Background()
	id := draftWith(t, d, "first")
	pl.Reconcile(ctx)
	if err := (&Drafts{Pool: d.Server}).Message(ctx, id, "second"); err != nil {
		t.Fatal(err)
	}
	pl.Reconcile(ctx)
	if len(f.conts) != 0 || len(f.roles) != 1 {
		t.Fatalf("a message reached a turn that runs: %v", f.conts)
	}
	d.Server.Exec(ctx, `UPDATE runs SET state = 'idle'`)
	if err := (&Drafts{Pool: d.Server}).Message(ctx, id, "third"); err != nil {
		t.Fatal(err)
	}
	pl.Reconcile(ctx)
	if len(f.conts) != 1 || !strings.Contains(f.conts[0], "second\n\nthird") {
		t.Fatalf("the waiting messages were not given together: %v", f.conts)
	}
	prompt := runs.Join(f.roles[0].Parts)
	for _, want := range []string{"You are the curator", "Developing a plan with the operator", "The plan template", "This is plan draft " + id, "The operator's message:\n\nfirst"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the first prompt lacks %q", want)
		}
	}
	if f.roles[0].Parts[1].Source != "baseline/guides/planning.md@test" {
		t.Fatalf("the guide's source: %q", f.roles[0].Parts[1].Source)
	}
}

func TestADraftsMessagesAreCheckedAndAClosedDraftTakesNone(t *testing.T) {
	_, _, d := newPlanner(t, nil)
	ctx := context.Background()
	dr := &Drafts{Pool: d.Server}
	for _, n := range []NewDraft{{Message: " "}, {Message: "x", Repo: "not a repo"}, {Message: "x", Title: "two\nlines"},
		{Message: strings.Repeat("x", maxMessage+1)}} {
		if _, err := dr.Create(ctx, n); err == nil {
			t.Errorf("%+v was taken", n)
		}
	}
	id := draftWith(t, d, "first")
	d.Server.Exec(ctx, `UPDATE plan_drafts SET state = 'closed' WHERE id = $1`, id)
	if err := dr.Message(ctx, id, "more"); err == nil {
		t.Fatal("a closed draft took a message")
	}
	if err := dr.Message(ctx, "d0000000-0000-4000-8000-000000000099", "more"); err == nil {
		t.Fatal("a draft that does not exist took a message")
	}
}

// The tools of M5 register; that every tool of the baseline is served, by these and the pipeline's,
// is tested where the server registers both (cmd/crew).
func TestTheToolsOfThePlanRegister(t *testing.T) {
	d := dbtest.New(t, false)
	s, err := mcp.New(d.Server, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Tools{Pool: d.Server}).Register(s); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Names()); n != 9 {
		t.Fatalf("%d tools: %v", n, s.Names())
	}
}

// The planner starts nothing before the server has taken over the runs of the server before it.
func TestThePlannerWaitsUntilTheRunsAreTakenOver(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	draftWith(t, d, "first")
	ready := make(chan struct{})
	pl.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pl.Run(ctx, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	early := len(f.roles)
	f.mu.Unlock()
	if early != 0 {
		t.Fatal("the planner started a run before the runs were taken over")
	}
	close(ready)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.roles)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the planner started nothing once ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A turn that did not start leaves the operator's messages waiting, and they go with the next one.
func TestAMessageIsTakenOnlyWithTheTurnThatTakesIt(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	ctx := context.Background()
	id := draftWith(t, d, "first")
	pl.Reconcile(ctx)
	d.Server.Exec(ctx, `UPDATE runs SET state = 'idle'`)
	(&Drafts{Pool: d.Server}).Message(ctx, id, "second")
	f.failNext = true
	pl.Reconcile(ctx)
	var waiting int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM draft_messages WHERE run IS NULL`).Scan(&waiting)
	if waiting != 1 {
		t.Fatalf("after a turn that did not start, %d messages wait", waiting)
	}
	pl.Reconcile(ctx)
	if len(f.conts) != 1 || !strings.Contains(f.conts[0], "second") {
		t.Fatalf("the message did not go with the next turn: %v", f.conts)
	}
	// Given once: a pass after it gives nothing again.
	d.Server.Exec(ctx, `UPDATE runs SET state = 'idle'`)
	pl.Reconcile(ctx)
	if len(f.conts) != 1 {
		t.Fatalf("a message was given twice: %v", f.conts)
	}
}

func projectInPlanning(t *testing.T, d *dbtest.DB) string {
	t.Helper()
	const id = "5b000000-0000-4000-8000-0000000000c1"
	if _, err := d.Server.Exec(context.Background(), `INSERT INTO projects (id, name, key, repo_source) VALUES ($1, 'shop', 'SF', 'local')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A breakdown run that ended with nothing written gets one reminder, as an idle one does.
func TestALeadRunThatEndedWithNothingWrittenIsRemindedOnce(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	ctx := context.Background()
	projectInPlanning(t, d)
	pl.Reconcile(ctx)
	if len(f.roles) != 1 || f.roles[0].Kind != "breakdown" {
		t.Fatalf("the breakdown did not start: %+v", f.roles)
	}
	d.Server.Exec(ctx, `UPDATE runs SET state = 'ended', stop_reason = 'ok'`)
	d.Server.Exec(ctx, `UPDATE subtasks SET state = 'done'`)
	pl.Reconcile(ctx)
	if len(f.conts) != 1 || !strings.Contains(f.conts[0], "You ended your turn without writing the breakdown") {
		t.Fatalf("no reminder: %v", f.conts)
	}
	d.Server.Exec(ctx, `UPDATE runs SET state = 'ended', stop_reason = 'ok'`)
	pl.Reconcile(ctx)
	if len(f.conts) != 1 {
		t.Fatalf("a second reminder: %v", f.conts)
	}
}

// The runs of a draft that became a project end: an idle one is finished, a working one stopped.
func TestTheRunsOfADraftThatBecameAProjectEnd(t *testing.T) {
	pl, f, d := newPlanner(t, settings.Doc{"lead_slots": settings.Doc{"agy": 2}}) // both drafts' runs at once
	signIn(t, d, "agy")
	ctx := context.Background()
	a := draftWith(t, d, "first")
	b := draftWith(t, d, "second")
	pl.Reconcile(ctx)
	if len(f.roles) != 2 {
		t.Fatalf("%d runs", len(f.roles))
	}
	d.Server.Exec(ctx, `UPDATE runs r SET state = 'idle' FROM subtasks s WHERE s.id = r.subtask AND s.draft = $1`, a)
	project := projectInPlanning(t, d)
	d.Server.Exec(ctx, `UPDATE plan_drafts SET state = 'project', project = $2 WHERE id = $1`, a, project)
	d.Server.Exec(ctx, `UPDATE plan_drafts SET state = 'project', project = $2 WHERE id = $1`, b, project)
	d.Server.Exec(ctx, `UPDATE projects SET state = 'running'`)
	pl.Reconcile(ctx)
	got := strings.Join(f.ended, " ")
	if len(f.ended) != 2 || !strings.Contains(got, "finish:") || !strings.Contains(got, "stop:") {
		t.Fatalf("ended %v", f.ended)
	}
}

// A lead run the operator stopped, or the driver gave up on, gets no reminder: the stop holds.
func TestAStoppedLeadRunIsNotReminded(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	ctx := context.Background()
	projectInPlanning(t, d)
	pl.Reconcile(ctx)
	for _, sub := range []string{`state = 'waiting', wait_for = 'operator'`, `state = 'stopped', wait_for = 'operator'`, `state = 'waiting', wait_for = 'retry'`} {
		d.Server.Exec(ctx, `UPDATE runs SET state = 'ended', stop_reason = 'operator'`)
		d.Server.Exec(ctx, `UPDATE subtasks SET `+sub)
		pl.Reconcile(ctx)
		if len(f.conts) != 0 {
			t.Fatalf("a run whose subtask is %s was reminded: %v", sub, f.conts)
		}
	}
}

// A comment the breakdown is sent back with reaches the lead with the way to revise in place.
func TestARevisionAsksForChangesInPlace(t *testing.T) {
	pl, f, d := newPlanner(t, nil)
	signIn(t, d, "agy")
	ctx := context.Background()
	project := projectInPlanning(t, d)
	pl.Reconcile(ctx)
	d.Server.Exec(ctx, `UPDATE runs SET state = 'idle'`)
	d.Server.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'breakdown', 1, 'b', 'lead'), ($1, 'sent_back', 1, 'SF-3 is too large.', 'operator')`, project)
	pl.Reconcile(ctx)
	if len(f.conts) != 1 || !strings.Contains(f.conts[0], "SF-3 is too large.") || !strings.Contains(f.conts[0], "revise a ticket to change it in place") ||
		strings.Contains(f.conts[0], "propose their replacements") {
		t.Fatalf("%v", f.conts)
	}
}
