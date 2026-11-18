package planning

import (
	"context"
	"testing"
)

// The lead's breakdown reads its project's override of the breakdown guide, and names it (10.3).
func TestTheBreakdownReadsTheProjectsOverride(t *testing.T) {
	pl, _, d := newPlanner(t, nil)
	ctx := context.Background()
	var id string
	if err := d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, repo_source) VALUES (gen_random_uuid(), 'shop', 'SF', 'local') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO overrides (project, number, path, version, text, status, proposed_by, applied_at)
		VALUES ($1, 1, 'guides/breakdown.md', 3, 'Tickets of a day at most.', 'applied', 'operator', now())`, id); err != nil {
		t.Fatal(err)
	}
	for _, amendment := range []string{"", "Add receipts."} {
		parts := pl.breakdownPrompt(ctx, id, "shop", "SF", amendment)
		if parts[1].Part != "guide" || parts[1].Text != "Tickets of a day at most." || parts[1].Source != "override o_1 v3 of baseline/guides/breakdown.md@test" {
			t.Fatalf("the guide: %+v", parts[1])
		}
		if parts[0].Source != "baseline/agents/curator.md@test" || len(parts) != 4 {
			t.Fatalf("the parts: %+v", parts)
		}
	}
}
