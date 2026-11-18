package db_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 79 marks the check runs stored before it whose setup failed, from the harness's own error in
// their results, and not a run whose command an agent named like a setup's end, whose detail begins
// the same (13.9).
func TestStep79MarksTheRunsWhoseSetupFailed(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 79 || steps[78].Name != "0079_check_runs_setup_failed.sql" {
		t.Fatalf("step 79 is not the check runs' setup mark")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:78]); err != nil {
		t.Fatal(err)
	}
	const p = "a0000000-0000-4000-8000-000000000079"
	run := func(id int, state, detail, results string) string {
		return `INSERT INTO check_runs (id, project, ticket, purpose, commit, state, detail, results) VALUES (` +
			string(rune('0'+id)) + `, '` + p + `', 1, 'exit', 'x', '` + state + `', ` + detail + `, '` + results + `')`
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, repo_source) VALUES ('` + p + `', 'p', 'P', 'local')`,
		`INSERT INTO tickets (id, project, number, title, kind, stage, origin, created_by, position)
			VALUES (1, '` + p + `', 1, 't', 'feature', 'in_progress', 'operator', 'operator', 1)`,
		run(1, "error", `'the project''s setup ended with 2'`,
			`[{"name": "make test", "code": 2, "passed": false, "error": "the project''s setup ended with 2", "output": "npm ERR!"}]`),
		run(2, "failed", `'the project''s setup: exec: no such file; it passes on main, so this branch breaks it'`,
			`[{"name": "make test", "code": -1, "passed": false, "error": "the project''s setup: exec: no such file"}]`),
		run(3, "error", `'the project''s setup ended with 1: it ran longer than 30m0s'`,
			`[{"name": "the project''s setup ended with 1", "code": 124, "passed": false, "error": "it ran longer than 30m0s"}]`),
		run(4, "passed", `NULL`, `[{"name": "make test", "code": 0, "passed": true, "output": "ok"}]`),
		run(5, "error", `'Crew restarted while it ran'`, `[]`),
		run(6, "failed", `'The ticket''s branch conflicts with main in: a.go'`, `{}`),
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:79]); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT id FROM check_runs WHERE setup_failed ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var marked []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		marked = append(marked, id)
	}
	if rows.Err() != nil || len(marked) != 2 || marked[0] != 1 || marked[1] != 2 {
		t.Fatalf("marked after step 79: %v (%v)", marked, rows.Err())
	}
	// A run stored after it is not marked unless the pipeline marks it.
	if _, err := conn.Exec(ctx, `INSERT INTO check_runs (id, project, ticket, purpose, commit) VALUES (7, '`+p+`', 1, 'exit', 'y')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE setup_failed`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d marked after a new run (%v)", n, err)
	}
}
