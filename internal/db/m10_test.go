package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 30 lets a check run be a judge's, and puts task_judge_checks_pass after task_cmd_checks_pass
// in the In progress stage of every project that stores its stages block; a block without the
// stage, and a project that stores none, are left alone. A second pass changes nothing.
func TestStep30AddsTheJudgesExitCheck(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if steps[29].Name != "0030_judge_checks.sql" {
		t.Fatalf("step 30 is %s", steps[29].Name)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:29]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, repo_source) VALUES
			('a0000000-0000-4000-8000-000000000001', 'stored', 'ST', 'local'),
			('a0000000-0000-4000-8000-000000000002', 'plain', 'PL', 'local'),
			('a0000000-0000-4000-8000-000000000003', 'odd', 'OD', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES
			('a0000000-0000-4000-8000-000000000001', '{"stages": [{"id": "planning"}, {"id": "in_progress", "role": "builder",
				"exit": ["branch_pushed", "has_commits", "note_exists", "task_cmd_checks_pass", "docs_done"]}, {"id": "done"}]}', 'test'),
			('a0000000-0000-4000-8000-000000000002', '{"budget_usd": 5}', 'test'),
			('a0000000-0000-4000-8000-000000000003', '{"stages": [{"id": "in_progress", "exit": ["docs_done"]}]}', 'test')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:30]); err != nil {
		t.Fatal(err)
	}
	exit := func(key string) string {
		var s string
		if err := conn.QueryRow(ctx, `SELECT coalesce((SELECT e->'exit' FROM jsonb_array_elements(s.doc->'stages') e WHERE e->>'id' = 'in_progress')::text, '-')
			FROM settings s JOIN projects p ON p.id = s.project WHERE p.key = $1`, key).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := exit("ST"); got != `["branch_pushed", "has_commits", "note_exists", "task_cmd_checks_pass", "task_judge_checks_pass", "docs_done"]` {
		t.Fatalf("the stored In progress exit: %s", got)
	}
	if got := exit("OD"); got != `["docs_done", "task_judge_checks_pass"]` {
		t.Fatalf("an exit without task_cmd_checks_pass: %s", got)
	}
	var plain, stages string
	conn.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project = 'a0000000-0000-4000-8000-000000000002'`).Scan(&plain)
	if plain != `{"budget_usd": 5}` {
		t.Fatalf("a project that stores no stages: %s", plain)
	}
	conn.QueryRow(ctx, `SELECT doc->'stages'->>0 FROM settings WHERE project = 'a0000000-0000-4000-8000-000000000001'`).Scan(&stages)
	if stages != `{"id": "planning"}` {
		t.Fatalf("the other stages were changed: %s", stages)
	}
	var def string
	conn.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'check_runs_purpose_check'`).Scan(&def)
	if !strings.Contains(def, "'judge'") || !strings.Contains(def, "'join'") {
		t.Fatalf("the purposes: %s", def)
	}
}

// Step 31 lets an entry be an E2E tester's or a verifier's, each with a result, one of each a run,
// and keeps images with an entry: only a PNG or a JPEG of at most 2 MB, under a plain name.
func TestStep31KeepsE2EAndVerificationEntries(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if steps[30].Name != "0031_e2e_and_verification.sql" {
		t.Fatalf("step 31 is %s", steps[30].Name)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:31]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, repo_source) VALUES ('a0000000-0000-4000-8000-000000000001', 'p', 'P', 'local')`,
		`INSERT INTO tickets (id, project, number, title, kind, stage, origin, created_by, position)
			VALUES (1, 'a0000000-0000-4000-8000-000000000001', 1, 't', 'feature', 'e2e', 'operator', 'operator', 1)`,
		`INSERT INTO records (id, project, ticket, kind, author, result, body) VALUES
			(1, 'a0000000-0000-4000-8000-000000000001', 1, 'e2e', 'e2e', 'pass', 'ok'),
			(2, 'a0000000-0000-4000-8000-000000000001', 1, 'verify', 'verifier', 'fail', 'no')`,
		`INSERT INTO record_files (record, name, type, data) VALUES (1, 'login.png', 'image/png', '\x89504e47')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for name, q := range map[string]string{
		"an e2e entry with no result": `INSERT INTO records (project, ticket, kind, author, body) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'e2e', 'e2e', 'x')`,
		"a note with a result":        `INSERT INTO records (project, ticket, kind, author, result, body) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'note', 'builder', 'pass', 'x')`,
		"a page kept as an image":     `INSERT INTO record_files (record, name, type, data) VALUES (1, 'a.html', 'text/html', 'x')`,
		"a name with a path":          `INSERT INTO record_files (record, name, type, data) VALUES (1, '../a.png', 'image/png', 'x')`,
		"an image over 2 MB":          `INSERT INTO record_files (record, name, type, data) VALUES (1, 'big.png', 'image/png', decode(repeat('00', 2097153), 'hex'))`,
		"the same name twice":         `INSERT INTO record_files (record, name, type, data) VALUES (1, 'login.png', 'image/png', 'x')`,
		"a check run of no purpose":   `INSERT INTO check_runs (project, ticket, purpose, commit) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'other', 'x')`,
	} {
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'e2e', 'x')`); err != nil {
		t.Fatalf("an e2e check run: %v", err)
	}
}

// Step 32 lets an entry be the verdict of a project's own stage, which names its stage, one a run,
// and lets a check run be a stage's commands or a judge's answer to a stage's criteria.
func TestStep32KeepsAStagesVerdict(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if steps[31].Name != "0032_own_stages.sql" {
		t.Fatalf("step 32 is %s", steps[31].Name)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:32]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, repo_source) VALUES ('a0000000-0000-4000-8000-000000000001', 'p', 'P', 'local')`,
		`INSERT INTO tickets (id, project, number, title, kind, stage, origin, created_by, position)
			VALUES (1, 'a0000000-0000-4000-8000-000000000001', 1, 't', 'feature', 'security', 'operator', 'operator', 1)`,
		`INSERT INTO records (project, ticket, kind, author, result, body, stage) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'stage', 'stage', 'pass', 'ok', 'security')`,
		`INSERT INTO check_runs (project, ticket, purpose, commit) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'stage', 'x'),
			('a0000000-0000-4000-8000-000000000001', 1, 'stage_judge', 'x')`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for name, q := range map[string]string{
		"a verdict with no stage":  `INSERT INTO records (project, ticket, kind, author, result, body) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'stage', 'stage', 'pass', 'x')`,
		"a verdict with no result": `INSERT INTO records (project, ticket, kind, author, body, stage) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'stage', 'stage', 'x', 's')`,
		"a note with a stage":      `INSERT INTO records (project, ticket, kind, author, body, stage) VALUES ('a0000000-0000-4000-8000-000000000001', 1, 'note', 'builder', 'x', 's')`,
	} {
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
}
