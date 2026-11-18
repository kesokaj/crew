package measure

import (
	"context"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

// The tickets around two versions are every project's but a hidden one's: the gate's crew-gate runs
// a ticket on every release (16.2), which would measure the gate, not the release (M16's fourth
// phase, 10.5).
func TestAroundVersionsLeavesOutTheGatesProject(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	done := func(name, key, version string, hidden bool) {
		t.Helper()
		var project string
		if err := d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source, hidden)
			VALUES (gen_random_uuid(), $1, $2, 'running', 'local', $3) RETURNING id`, name, key, hidden).Scan(&project); err != nil {
			t.Fatal(err)
		}
		var ticket, sub int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, 1, 'T', 'feature', 'done', 'operator', 'lead', 1) RETURNING id`, project).Scan(&ticket); err != nil {
			t.Fatal(err)
		}
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role)
			VALUES ('ticket', $1, $2, 1, 'build', 'builder') RETURNING id`, project, ticket).Scan(&sub); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, cli, crew_version, image, cost_usd, state, started_at)
			VALUES (gen_random_uuid(), $1, 1, 'agy', $2, 'img', 2, 'ended', $3)`, sub, version, at); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, 'test', 'done', 'backend', $2)`,
			ticket, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	done("shop", "SF", "1.0.0", false)
	done("mail", "ML", "1.1.0", false)
	done("crew-gate", "CGATE", "1.1.0", true)
	before, after, err := AroundVersions(ctx, d.Server, "1.0.0", "1.1.0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("%d tickets before and %d after: the gate's ticket is counted", len(before), len(after))
	}
}
