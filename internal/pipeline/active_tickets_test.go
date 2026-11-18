package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

func TestSortTicketsForReconcileOrdersByStageRightThenPriority(t *testing.T) {
	doc, err := settings.ProjectDefaults("shop", "SF")
	if err != nil {
		t.Fatal(err)
	}
	pipe := board.PipelineOf(doc)
	urgent := 0
	tickets := []*board.Ticket{
		{ID: 1, Key: "SF-1", Stage: "analysis", Priority: 2},
		{ID: 2, Key: "SF-2", Stage: "in_progress", Priority: 2},
		{ID: 3, Key: "SF-3", Stage: "review", Priority: 2},
		{ID: 4, Key: "SF-4", Stage: "in_progress", Priority: 1},
		{ID: 5, Key: "SF-5", Stage: "in_progress", Priority: 2, OperatorPriority: &urgent},
		{ID: 6, Key: "SF-6", Stage: "done", Priority: 0},
	}
	sortTicketsForReconcile(pipe, tickets)
	got := make([]string, len(tickets))
	for i, tk := range tickets {
		got[i] = tk.Key
	}
	want := "SF-3,SF-5,SF-4,SF-2,SF-1,SF-6"
	if strings.Join(got, ",") != want {
		t.Fatalf("sortTicketsForReconcile got %s, want %s", strings.Join(got, ","), want)
	}
}

func TestActiveTicketLimitsAndAlreadyActiveTickets(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{
		Pool: d.Server,
		Git:  &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		},
	}
	shop, err := b.CreateProject(ctx, board.NewProject{Name: "shop", Key: "SF", Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	ware, err := b.CreateProject(ctx, board.NewProject{Name: "warehouse", Key: "WH", Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}

	mkTicket := func(proj *board.Project, num int, title string) *board.Ticket {
		t.Helper()
		var id int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1::uuid, $2::int, $3, 'feature', 'in_progress', 'plan', 'lead', $2::float8) RETURNING id`, proj.ID, num, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return &board.Ticket{ID: id, Project: proj.ID, Number: num, Key: fmt.Sprintf("%s-%d", proj.Key, num), Title: title, Kind: "feature", Stage: "in_progress", Priority: 2}
	}
	t1 := mkTicket(shop, 1, "One")
	t2 := mkTicket(shop, 2, "Two")
	t3 := mkTicket(shop, 3, "Three")
	w1 := mkTicket(ware, 1, "Ware One")

	startRunOn := func(proj *board.Project, tk *board.Ticket, n int) {
		t.Helper()
		var sub int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
			VALUES ('ticket', $1, $2, $3, 'build', 'builder', 'running') RETURNING id`, proj.ID, tk.ID, n).Scan(&sub); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image)
			VALUES (gen_random_uuid(), $1, 1, 'working', 'agy', 'gemini-4-argon', 't', 't')`, sub); err != nil {
			t.Fatal(err)
		}
	}

	startRunOn(shop, t1, 1)
	startRunOn(shop, t2, 1)

	machineDoc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	shopDoc, err := b.Settings(ctx, d.Server, shop)
	if err != nil {
		t.Fatal(err)
	}
	wareDoc, err := b.Settings(ctx, d.Server, ware)
	if err != nil {
		t.Fatal(err)
	}

	pl := &Pipeline{Pool: d.Server, Board: b}
	jShop := &job{machine: machineDoc, doc: shopDoc, proj: shop}
	jWare := &job{machine: machineDoc, doc: wareDoc, proj: ware}

	// 0. Default limits are 12 across the machine and 4 per project.
	if sum, err := ActiveTicketsOf(ctx, d.Server, machineDoc); err != nil || sum.Active != 2 || sum.Max != 12 || sum.Set != 12 {
		t.Fatalf("expected default ActiveTicketsOf {Active:2 Max:12 Set:12}, got %+v (err %v)", sum, err)
	}
	if pAct, pMax, err := ProjectActiveTickets(ctx, d.Server, shop.ID, shopDoc); err != nil || pAct != 2 || pMax != 4 {
		t.Fatalf("expected default ProjectActiveTickets (2, 4), got (%d, %d) (err %v)", pAct, pMax, err)
	}

	// Lower shop's per-project limits.max_active_tickets to 2 to verify per-project cap enforcement.
	shopDoc["limits"].(settings.Doc)["max_active_tickets"] = 2

	// 1. Third ticket in shop is blocked by per-project limits.max_active_tickets (2 of 2 active).
	why, err := pl.checkActiveTicketLimit(ctx, jShop, t3, kindBuild)
	if err != nil || !strings.Contains(why, "waits for an active-ticket slot in shop (2 of 2 active)") {
		t.Fatalf("expected project active-ticket limit for t3, got %q (err %v)", why, err)
	}

	// 2. An already-active ticket (t1) starting a parallel part subtask or judge check is NOT blocked.
	if why, err := pl.checkActiveTicketLimit(ctx, jShop, t1, kindPart); err != nil || why != "" {
		t.Fatalf("expected already-active ticket t1 to start parallel part without waiting, got %q (err %v)", why, err)
	}

	// 3. Another project (warehouse) can still start work while machine active tickets (2) < machine max (6).
	if why, err := pl.checkActiveTicketLimit(ctx, jWare, w1, kindBuild); err != nil || why != "" {
		t.Fatalf("expected warehouse ticket w1 to start while machine active=2 < 6, got %q (err %v)", why, err)
	}

	// 4. Lower machine max_active_tickets to 4 and record quota >= slow_down_at (0.8) so effective machine max halves to 2.
	machineDoc["max_active_tickets"] = 4
	if err := models.RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.85}}, "login check"); err != nil {
		t.Fatal(err)
	}
	sum, err := ActiveTicketsOf(ctx, d.Server, machineDoc)
	if err != nil || sum.Active != 2 || sum.Max != 2 || sum.Set != 4 {
		t.Fatalf("expected ActiveTicketsOf {Active:2 Max:2 Set:4}, got %+v (err %v)", sum, err)
	}
	why, err = pl.checkActiveTicketLimit(ctx, jWare, w1, kindBuild)
	if err != nil || !strings.Contains(why, "waits for a machine active-ticket slot (2 of 2 active)") {
		t.Fatalf("expected machine active-ticket limit for w1, got %q (err %v)", why, err)
	}

	// 5. Summary runs on done tickets are never gated by active-ticket limits.
	if why, err := pl.checkActiveTicketLimit(ctx, jWare, w1, kindSummary); err != nil || why != "" {
		t.Fatalf("expected summary run to bypass active-ticket limit, got %q (err %v)", why, err)
	}
}
