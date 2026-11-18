package mcp_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/mcp"
)

// The calls the operator's own tools made, with a personal token, are kept 180 days (6.5); the calls
// of a run are in its trajectory and are kept for good, however old, and a call with neither is not
// the operator's to lose (round 2 of the reviews of 1be8ea7).
func TestTheOperatorsOwnCallsAreKept180Days(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO personal_tokens (id, name, hash, expires_at) VALUES (1, 'laptop', 'h', now() + interval '1 day');
		INSERT INTO mcp_calls (at, role, tool, ok, result, token) VALUES
		(now() - interval '181 days', 'token', 'list_projects', true, 'old', 1),
		(now() - interval '179 days', 'token', 'list_projects', true, 'young', 1),
		(now() - interval '1 year', 'lead', 'get_project', true, 'neither', NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO subtasks (id, scope, project, number, kind, role) VALUES (9001, 'project', '` + fixture + `', 900, 'breakdown', 'lead')`,
		`INSERT INTO runs (id, subtask, number, cli, crew_version, image) VALUES ('00000000-0000-4000-8000-0000000000a1', 9001, 1, 'agy', 't', 't')`,
		`INSERT INTO mcp_calls (at, run, role, tool, ok, result) VALUES (now() - interval '2 years', '00000000-0000-4000-8000-0000000000a1', 'lead', 'get_project', true, 'run')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	n, err := mcp.PruneOwnCalls(ctx, d.Server)
	if err != nil || n != 1 {
		t.Fatalf("removed %d: %v", n, err)
	}
	var left string
	d.Server.QueryRow(ctx, `SELECT string_agg(result, ' ' ORDER BY result) FROM mcp_calls`).Scan(&left)
	if left != "neither run young" {
		t.Fatalf("left %q", left)
	}
}
