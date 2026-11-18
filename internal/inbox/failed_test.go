package inbox_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

// A ticket Crew held at limits.max_ticket_failures appears in the inbox under KindCrewCannot until its
// hold is lifted or the ticket closes; a ticket the operator held by hand does not (7.7, 7.8).
func TestTicketHeldAtFailureLimitIsInInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const shop = "00000000-0000-4000-8000-000000000042"
	exec(`INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, shop)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, hold, stuck)
		VALUES ($1, 1, 'Broken migration', 'fix', 'in_progress', 'agent', 'curator', 1, true,
			'Held after 4 failures (limits.max_ticket_failures is 4): its subtask SF-1.2 Build stopped twice')`, shop)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, hold)
		VALUES ($1, 2, 'Paused by operator', 'feature', 'in_progress', 'plan', 'planner', 2, true)`, shop)
	exec(`INSERT INTO changes (project, who, tbl, row_key, after) VALUES
		($1::uuid, 'crew', 'tickets', 'SF-1', '{"hold": true}'),
		($1::uuid, 'operator', 'tickets', 'SF-2', '{"hold": true}')`, shop)

	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(items, func(it inbox.Item) bool { return it.ID == "failed/shop/SF-1" })
	if idx < 0 {
		t.Fatalf("expected failed/shop/SF-1 in inbox, got %+v", items)
	}
	it := items[idx]
	if it.Kind != inbox.KindCrewCannot || it.Project != "shop" || !strings.Contains(it.Body, "Held after 4 failures") {
		t.Fatalf("unexpected item: %+v", it)
	}
	if len(it.Alternatives) == 0 || it.Alternatives[0].Link != "/p/shop/tickets/SF-1" {
		t.Fatalf("unexpected alternatives: %+v", it.Alternatives)
	}
	if slices.ContainsFunc(items, func(it inbox.Item) bool { return it.ID == "failed/shop/SF-2" }) {
		t.Fatalf("operator-held SF-2 should not appear in inbox: %+v", items)
	}

	// Lifting the hold clears the inbox item.
	exec(`UPDATE tickets SET hold = false WHERE project = $1 AND number = 1`, shop)
	items, err = inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(items, func(it inbox.Item) bool { return strings.HasPrefix(it.ID, "failed/") }) {
		t.Fatalf("expected failed item cleared once unheld, got %+v", items)
	}
}
