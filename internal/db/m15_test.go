package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Deleting a project deletes everything of it (10.6): every table that refers to a project is named in
// the function that deletes one, or deletes its rows by itself (ON DELETE CASCADE or SET NULL), so a
// table added later cannot be forgotten there.
func TestEveryTableOfAProjectIsDeletedWithIt(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	var body string
	if err := d.Server.QueryRow(ctx, `SELECT pg_get_functiondef('crew_delete_project(uuid, text)'::regprocedure)`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Server.Query(ctx, `SELECT DISTINCT c.conrelid::regclass::text FROM pg_constraint c
		WHERE c.contype = 'f' AND c.confrelid = 'projects'::regclass AND c.confdeltype NOT IN ('c', 'n') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		n++
		if !strings.Contains(body, "FROM "+table+" WHERE") && !strings.Contains(body, "UPDATE "+table+" SET") {
			t.Errorf("crew_delete_project does not delete the rows of %s", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 20 {
		t.Fatalf("found %d tables that refer to a project; the query finds too few", n)
	}
}

// The runs of an eval and of the improver have scopes of their own (since 0.49).
func TestEvalAndImproverRunsHaveTheirScopes(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, scope := range []string{"eval", "improver"} {
		if _, err := d.Server.Exec(ctx, `INSERT INTO subtasks (scope, number, kind, role) VALUES ($1, 1, 'x', 'builder')`, scope); err != nil {
			t.Errorf("scope %s: %v", scope, err)
		}
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO subtasks (scope, number, kind, role) VALUES ('nowhere', 1, 'x', 'builder')`); err == nil {
		t.Error("a scope that does not exist was taken")
	}
}

// Step 52 takes the setting improvements.improver_network out of the machine's stored settings, and
// leaves the rest (since 0.49).
func TestTheImproversNetworkSettingIsTakenOut(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	steps, err := db.Steps()
	if err != nil {
		t.Fatal(err)
	}
	var update string
	for _, s := range steps {
		if s.Number == 52 {
			for _, line := range strings.Split(s.SQL, "\n") {
				if strings.HasPrefix(line, "UPDATE settings") {
					update = line
				}
			}
		}
	}
	if update == "" {
		t.Fatal("step 52 has no UPDATE of settings")
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"version": 1, "improvements": {"improver_network": "internet", "budget_usd_per_week": 3}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = EXCLUDED.doc`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, update); err != nil {
		t.Fatal(err)
	}
	var doc string
	if err := d.Server.QueryRow(ctx, `SELECT doc::text FROM settings WHERE project IS NULL`).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "improver_network") || !strings.Contains(doc, `"budget_usd_per_week": 3`) {
		t.Fatalf("settings after step 52: %s", doc)
	}
}
