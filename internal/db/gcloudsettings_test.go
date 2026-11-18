package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 78 takes the retired settings of gcloud out of the stored settings, the machine's and every
// project's, and leaves every other key as it was (plan of 2026-10-26 on rule 8, D7).
func TestStep78TakesOutTheGcloudSettings(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 78 || steps[77].Name != "0078_gcloud_settings_retired.sql" {
		t.Fatalf("step 78 is not the gcloud settings retired")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:77]); err != nil {
		t.Fatal(err)
	}
	const shop = "c0000000-0000-4000-8000-0000000000d1"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"conversations": {"credentials": [], "idle_minutes": 45}}', 'operator')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + shop + `', '{"sandbox": {"credentials": "all", "test": "go test ./..."}}', 'operator')`,
		`INSERT INTO sandboxes (id, kind, container, image, gcloud) VALUES ('run-a', 'run', 'crew-run-a', 'i', true), ('terminal-b', 'terminal', 'crew-terminal-b', 'i', true)`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:78]); err != nil {
		t.Fatal(err)
	}
	doc := func(where string) map[string]map[string]any {
		t.Helper()
		var raw []byte
		if err := conn.QueryRow(ctx, `SELECT doc FROM settings WHERE `+where).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if c := doc("project IS NULL")["conversations"]; len(c) != 1 || c["idle_minutes"] != float64(45) {
		t.Fatalf("the machine's conversations after step 78: %v", c)
	}
	if sb := doc("project = '" + shop + "'")["sandbox"]; len(sb) != 1 || sb["test"] != "go test ./..." {
		t.Fatalf("the project's sandbox after step 78: %v", sb)
	}
	// A run's sandbox no longer says it holds gcloud; a terminal's still does.
	var run, term bool
	if err := conn.QueryRow(ctx, `SELECT (SELECT gcloud FROM sandboxes WHERE id = 'run-a'), (SELECT gcloud FROM sandboxes WHERE id = 'terminal-b')`).Scan(&run, &term); err != nil || run || !term {
		t.Fatalf("the sandboxes' gcloud after step 78: run %v, terminal %v (%v)", run, term, err)
	}
}
