package db_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 75 applies on a database of step 74. A project builds, starts or checks one deploy at a time;
// a roll back and a settings deploy name the deploy whose image they take, and no other does; a
// deleted project takes its hosting and its deploys with it (19).
func TestStep75KeepsOneRunningDeploy(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 75 || steps[74].Name != "0075_hosting.sql" {
		t.Fatalf("step 75 is not the hosting")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:74]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:75]); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error {
		_, err := conn.Exec(ctx, sql, args...)
		return err
	}
	var p string
	if err := conn.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'shop', 'SH', 'running', 'local') RETURNING id`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	const c = "0123456789abcdef0123456789abcdef01234567"
	deploy := func(n int, state, why string, from any) (int64, error) {
		var id int64
		asked := map[string]string{"asked": "operator", "rollback": "operator", "follow_main": "follow_main", "hostable": "hostable", "settings": "settings"}[why]
		err := conn.QueryRow(ctx, `INSERT INTO deploys (project, number, commit, why, asked_by, from_deploy, state) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			p, n, c, why, asked, from, state).Scan(&id)
		return id, err
	}
	first, err := deploy(1, "building", "asked", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deploy(2, "checking", "asked", nil); err == nil {
		t.Fatal("a second running deploy of one project was taken")
	}
	if err := exec(`UPDATE deploys SET state = 'live' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy(2, "starting", "settings", first); err != nil {
		t.Fatalf("a deploy beside a live one: %v", err)
	}
	if _, err := deploy(3, "failed", "rollback", nil); err == nil {
		t.Fatal("a roll back that names no deploy was taken")
	}
	if _, err := deploy(3, "failed", "asked", first); err == nil {
		t.Fatal("a deploy asked for that names an image source was taken")
	}
	if _, err := deploy(2, "failed", "asked", nil); err == nil {
		t.Fatal("a deploy number was taken twice")
	}
	if err := exec(`INSERT INTO hosting (project, state, live_deploy) VALUES ($1, 'live', $2)`, p, first); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM projects WHERE id = $1`, p); err != nil {
		t.Fatalf("deleting the project: %v", err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting) + (SELECT count(*) FROM deploys)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left of a deleted project: %d, %v", n, err)
	}
}
