package db_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// Step 74 applies on a database of step 73 and gives notify_state its one row. An item has one open
// episode at a time and one reminder for each; a news key and a digest's date are mailed once; a test
// message names no item and a reminder always its episode (18.1).
func TestStep74KeepsOneEpisodeAndOneReminder(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	if len(steps) < 74 || steps[73].Name != "0074_notifications.sql" {
		t.Fatalf("step 74 is not the notifications")
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:73]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateSteps(ctx, conn, steps[:74]); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM notify_state`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("notify_state: %d rows, %v", n, err)
	}
	insert := func(sql string, args ...any) error {
		_, err := conn.Exec(ctx, sql, args...)
		return err
	}
	var first int64
	if err := conn.QueryRow(ctx, `INSERT INTO notifications (kind, item, subject, body) VALUES ('needs_you', 'ok/shop', 's', 'b') RETURNING id`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO notifications (kind, item, subject, body) VALUES ('needs_you', 'ok/shop', 's', 'b')`); err == nil {
		t.Fatal("a second open episode of one item was taken")
	}
	if err := insert(`INSERT INTO notifications (kind, item, episode, subject, body) VALUES ('reminder', 'ok/shop', $1, 's', 'b')`, first); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO notifications (kind, item, episode, subject, body) VALUES ('reminder', 'ok/shop', $1, 's', 'b')`, first); err == nil {
		t.Fatal("a second reminder of one episode was taken")
	}
	if err := insert(`UPDATE notifications SET ended_at = now() WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO notifications (kind, item, subject, body) VALUES ('needs_you', 'ok/shop', 's', 'b')`); err != nil {
		t.Fatalf("a new episode after the first ended: %v", err)
	}
	for _, k := range []string{"news", "digest"} {
		if err := insert(`INSERT INTO notifications (kind, item, subject, body) VALUES ($1, $1 || '/1', 's', 'b')`, k); err != nil {
			t.Fatal(err)
		}
		if err := insert(`INSERT INTO notifications (kind, item, subject, body) VALUES ($1, $1 || '/1', 's', 'b')`, k); err == nil {
			t.Fatalf("a %s key was taken twice", k)
		}
	}
	if err := insert(`INSERT INTO notifications (kind, subject, body) VALUES ('test', 's', 'b')`); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO notifications (kind, subject, body) VALUES ('test', 's', 'b')`); err != nil {
		t.Fatalf("a second test message: %v", err)
	}
	if err := insert(`INSERT INTO notifications (kind, item, subject, body) VALUES ('reminder', 'ok/shop', 's', 'b')`); err == nil {
		t.Fatal("a reminder without its episode was taken")
	}
}
