package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/backup"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// monthKit is a database with events in an ended month, 2026-08, and in the month of now, 2026-10.
func monthKit(t *testing.T) (*dbtest.DB, time.Time) {
	t.Helper()
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, m := range []string{"2026-08-01", "2026-10-01"} {
		if _, err := d.Server.Exec(ctx, `SELECT crew_ensure_events_month($1::date)`, m); err != nil {
			t.Fatal(err)
		}
	}
	for i, at := range []string{"2026-08-05T10:00:00Z", "2026-08-20T10:00:00Z", "2026-10-02T10:00:00Z"} {
		if _, err := d.Server.Exec(ctx, `INSERT INTO events (run, seq, at, type, payload)
			VALUES (gen_random_uuid(), $1, $2, 'note', jsonb_build_object('marker', $3::text))`, i, at, "marker-"+at[:7]+"-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	return d, time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
}

func countEvents(t *testing.T, d *dbtest.DB, month string) int {
	t.Helper()
	var n int
	if err := d.Server.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE to_char(at, 'YYYY-MM') = $1`, month).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The events of an ended month are in a month file, written once, and not in the day's file; a
// restore reads them back from it, in the same transaction (16.9).
func TestEndedMonthsAreInMonthFiles(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if months, _ := backup.ListMonths(dir); strings.Join(months, " ") != "2026-08" {
		t.Fatalf("month files %v", months)
	}
	entries, err := entriesOf(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(entries["months.txt"]) != "dir: "+dir+"\n2026-08\n" {
		t.Fatalf("months.txt %q", entries["months.txt"])
	}
	dump := string(entries["crew.sql"])
	if strings.Contains(dump, "marker-2026-08") || !strings.Contains(dump, "marker-2026-10") {
		t.Fatal("the day's file holds the ended month, or not this month")
	}
	// The month file is written once: the next night leaves it.
	mf := filepath.Join(dir, backup.MonthName("2026-08"))
	before, _ := os.Stat(mf)
	time.Sleep(10 * time.Millisecond)
	if _, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(mf); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the month file was written again with nothing deleted")
	}
	// Everything gone, and back from the day's file and the month file.
	if _, err := d.Server.Exec(ctx, `DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err != nil {
		t.Fatal(err)
	}
	if countEvents(t, d, "2026-08") != 2 || countEvents(t, d, "2026-10") != 1 {
		t.Fatalf("after the restore: %d in August, %d in October", countEvents(t, d, "2026-08"), countEvents(t, d, "2026-10"))
	}
	// The scratch copy of an update leaves the months out.
	if err := backup.RestoreWith(ctx, d.Conn(db.RoleMigrate), file, backup.Options{NoMonths: true}); err != nil {
		t.Fatal(err)
	}
	if countEvents(t, d, "2026-08") != 0 || countEvents(t, d, "2026-10") != 1 {
		t.Fatal("NoMonths read the month file")
	}
}

// A deletion from an ended month, of a project or a conversation, writes its month file again
// without it (16.9).
func TestADeletionWritesTheMonthFileAgain(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	if _, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `DELETE FROM events WHERE payload->>'marker' LIKE 'marker-2026-08-a'`); err != nil {
		t.Fatal(err)
	}
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, d, "2026-08"); n != 1 {
		t.Fatalf("%d events of August came back, want the one that was not deleted", n)
	}
}

// A backup whose month file is gone does not restore, and changes nothing.
func TestARestoreNeedsItsMonthFiles(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, backup.MonthName("2026-08"))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `DELETE FROM events WHERE to_char(at, 'YYYY-MM') = '2026-10'`); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err == nil || !strings.Contains(err.Error(), "month file of 2026-08") {
		t.Fatalf("restore: %v", err)
	}
	if countEvents(t, d, "2026-10") != 0 || countEvents(t, d, "2026-08") != 2 {
		t.Fatal("the failed restore changed the database")
	}
}

// A month file that holds a psql meta-command is refused, as a dump is.
func TestAMonthFileWithAMetaCommandIsRefused(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	mf := filepath.Join(dir, backup.MonthName("2026-08"))
	marker := filepath.Join(dir, "ran")
	if err := gzLine(mf, "\\! touch "+marker+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err == nil || !strings.Contains(err.Error(), "meta-command") {
		t.Fatalf("restore: %v", err)
	}
	// After SQL on the same line, psql's restricted mode refuses it.
	if err := gzLine(mf, "SELECT 1; \\! touch "+marker+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err == nil {
		t.Fatal("a month file with a command after SQL restored")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}

// A backup names the folder of its month files; a restore reads them only under the folder it is
// given, whatever the file says.
func TestABackupDoesNotChooseWhereItsMonthsAreRead(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	err = backup.RestoreWith(ctx, d.Conn(db.RoleMigrate), file, backup.Options{MonthsRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "which is not under") {
		t.Fatalf("%v", err)
	}
	if err := backup.RestoreWith(ctx, d.Conn(db.RoleMigrate), file, backup.Options{MonthsRoot: filepath.Dir(dir)}); err != nil {
		t.Fatal(err)
	}
}

// The repositories of projects with no remote are in the day's file, and a restore puts back each
// one that is missing, never over one that is there.
func TestTheRepositoriesWithNoRemoteComeBack(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	src := filepath.Join(t.TempDir(), "local.git")
	if err := os.MkdirAll(filepath.Join(src, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now, Repos: map[string]string{"local": src}})
	if err != nil {
		t.Fatal(err)
	}
	canonical := t.TempDir()
	// Another repository that is there is left as it is.
	if err := os.MkdirAll(filepath.Join(canonical, "other.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backup.RestoreWith(ctx, d.Conn(db.RoleMigrate), file, backup.Options{Canonical: canonical}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(canonical, "local.git", "HEAD"))
	if err != nil || string(body) != "ref: refs/heads/main\n" {
		t.Fatalf("the repository: %q %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "local.git", "HEAD"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backup.RestoreWith(ctx, d.Conn(db.RoleMigrate), file, backup.Options{Canonical: canonical}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(canonical, "local.git", "HEAD")); string(body) != "changed\n" {
		t.Fatal("a repository that was there was replaced")
	}
}

// The updater's snapshot is the database without the months whose files are current, and reads back
// into a scratch copy without them.
func TestASnapshotLeavesOutTheMonthsInFiles(t *testing.T) {
	ctx := context.Background()
	d, now := monthKit(t)
	dir := t.TempDir()
	if _, err := backup.Daily(ctx, backup.Plan{Conn: d.Conn(db.RoleRead), Dir: dir, Keep: 14, Now: now}); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "0.1.0.tar.gz")
	if err := backup.Snapshot(ctx, d.Conn(db.RoleRead), dir, snap); err != nil {
		t.Fatal(err)
	}
	entries, err := entriesOf(snap)
	if err != nil {
		t.Fatal(err)
	}
	if string(entries["months.txt"]) != "dir: "+dir+"\n2026-08\n" || strings.Contains(string(entries["crew.sql"]), "marker-2026-08") {
		t.Fatalf("months %q", entries["months.txt"])
	}
	// Kept elsewhere, it finds the month files in the folder it names.
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), snap); err != nil {
		t.Fatal(err)
	}
	if countEvents(t, d, "2026-08") != 2 {
		t.Fatal("a snapshot read back with its month files lost the ended month")
	}
}
