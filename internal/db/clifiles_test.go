package db_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 77 removes the CLI's files stored in plain text before it; from it on every one is stored
// sealed with the key file, and a row written after the step stays (plan of 2026-10-26, D2).
func TestStep77RemovesThePlainCLIFiles(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 77 || steps[76].Name != "0077_cli_files_sealed.sql" {
		t.Fatalf("step 77 is not the CLI's files sealed")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:76]); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO conversations (id, purpose) VALUES ('00000000-0000-4000-8000-0000000000c1', 'run')`)
	exec(`INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until)
		VALUES ('00000000-0000-4000-8000-0000000000c1', 'plain', '\x1f8b', 'x', now() + interval '7 days')`)
	if _, err := db.MigrateSteps(ctx, conn, steps[:77]); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM cli_files`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("%d CLI files stored before step 77 are left", n)
	}
	exec(`INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until)
		VALUES ('00000000-0000-4000-8000-0000000000c1', 'sealed', '\x00', 'x', now() + interval '7 days')`)
	if _, err := db.MigrateSteps(ctx, conn, steps[:77]); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("a CLI file written after step 77 is gone (%d left)", n)
	}
}
