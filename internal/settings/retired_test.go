package settings_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/settings"
)

// The settings of gcloud are retired since 0.79 (AGENTS.md safety rule 8): a stored doc that still
// holds one, as an older server writes after a roll back, is read without it, and a save stores none
// (plan of 2026-10-26 on rule 8, D6).
func TestTheGcloudSettingsAreReadAndSavedWithout(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	const shop = "c0000000-0000-4000-8000-0000000000d1"
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source) VALUES ('` + shop + `', 'shop', 'SF', 'running', 'local')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"conversations": {"credentials": [], "idle_minutes": 45}}', 'operator')`,
		`INSERT INTO settings (project, doc, updated_by) VALUES ('` + shop + `', '{"sandbox": {"credentials": [], "test": "go test ./..."}}', 'operator')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	has := func(doc settings.Doc, section string) bool {
		s, _ := doc[section].(settings.Doc)
		_, ok := s["credentials"]
		return ok
	}
	own, err := settings.Load(ctx, d.Server)
	if err != nil || has(own, "conversations") || own["conversations"].(settings.Doc)["idle_minutes"] == nil {
		t.Fatalf("the machine's own settings: %v %v", own, err)
	}
	eff, err := settings.Effective(ctx, d.Server, machine)
	if err != nil || has(eff, "conversations") {
		t.Fatalf("the machine's effective settings: %v %v", eff["conversations"], err)
	}
	ownP, err := settings.LoadProject(ctx, d.Server, shop)
	if err != nil || has(ownP, "sandbox") || ownP["sandbox"].(settings.Doc)["test"] != "go test ./..." {
		t.Fatalf("the project's own settings: %v %v", ownP, err)
	}
	effP, err := settings.EffectiveProject(ctx, d.Server, shop, "shop", "SF")
	if err != nil || has(effP, "sandbox") {
		t.Fatalf("the project's effective settings: %v %v", effP["sandbox"], err)
	}
	// A file from before 0.79 saves, without them.
	eff["conversations"].(settings.Doc)["credentials"] = "all"
	if err := settings.Save(ctx, d.Server, machine, eff, "test"); err != nil {
		t.Fatalf("a file from before 0.79: %v", err)
	}
	var stored bool
	if err := d.Server.QueryRow(ctx, `SELECT doc #> '{conversations,credentials}' IS NOT NULL FROM settings WHERE project IS NULL`).Scan(&stored); err != nil || stored {
		t.Fatalf("a save stored the retired key: %v %v", stored, err)
	}
	// And a project's that changes a setting: a save that changes none writes nothing, and the stored
	// key it would leave is dropped by every read.
	effP["sandbox"].(settings.Doc)["credentials"] = []any{}
	effP["sandbox"].(settings.Doc)["test"] = "make test"
	tx, err := d.Server.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := settings.SaveProject(ctx, tx, shop, "shop", "SF", effP, "test"); err != nil {
		t.Fatalf("a project's file from before 0.79: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT doc #> '{sandbox,credentials}' IS NOT NULL FROM settings WHERE project = $1`, shop).Scan(&stored); err != nil || stored {
		t.Fatalf("a project's save stored the retired key: %v %v", stored, err)
	}
}
