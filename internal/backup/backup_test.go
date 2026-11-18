package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/backup"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// A dump made by the role that can only read restores into the database, and a project's rows and
// the history come back as they were (design 22, "Backup and restore").
func TestADumpRestores(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO jobs (kind, key) VALUES ('x', 'before')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO personal_tokens (name, hash, expires_at) VALUES ('mine', 'h', now() + interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file, err := backup.Dump(ctx, d.Conn(db.RoleRead), dir, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the backup file: %v %v", fi, err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO jobs (kind, key) VALUES ('x', 'after')`); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), file); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, err := d.Server.Query(ctx, `SELECT key FROM jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != "before" {
		t.Fatalf("after the restore jobs holds %v", keys)
	}
	// A personal token made before the backup is ended by the restore (16.11).
	var live int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM personal_tokens WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("%d personal tokens live after a restore (%v)", live, err)
	}
	// The server's role works as before: the grants came back with the tables.
	if _, err := d.Server.Exec(ctx, `INSERT INTO jobs (kind, key) VALUES ('x', 'again')`); err != nil {
		t.Fatalf("the server cannot write after a restore: %v", err)
	}
	if _, err := d.Server.Exec(ctx, `DELETE FROM changes`); err == nil {
		t.Fatal("after a restore the server may remove history")
	}
}

func TestAFailedRestoreChangesNothing(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO jobs (kind, key) VALUES ('x', 'kept')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file, err := backup.Dump(ctx, d.Conn(db.RoleRead), dir, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The server's role may not change the schema, so a restore as it fails part way.
	if err := backup.Restore(ctx, d.Conn(db.RoleServer), file); err == nil {
		t.Fatal("a restore as the server's role succeeded")
	}
	var n int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE key = 'kept'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the failed restore changed the data: %d, %v", n, err)
	}
}

func TestOnlyTheNewestAreKept(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := backup.Dump(ctx, d.Conn(db.RoleRead), dir, 2, start.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := backup.List(dir)
	if len(names) != 2 || filepath.Base(names[0]) != backup.Name(start.AddDate(0, 0, 2)) {
		t.Fatalf("kept %v", names)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a file that is not a backup was removed")
	}
}

func TestADumpThatFailsLeavesNoFile(t *testing.T) {
	d := dbtest.New(t, false)
	dir := t.TempDir()
	c := d.Conn(db.RoleRead)
	c.Name = "no_such_database"
	if _, err := backup.Dump(context.Background(), c, dir, 2, time.Now()); err == nil {
		t.Fatal("a dump of a missing database succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

// A file in the backup folder that holds a psql meta-command is refused before anything runs: the
// server's role can write that folder, and a restore runs as the schema's owner.
func TestARestoreRefusesMetaCommands(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if _, err := d.Server.Exec(ctx, `INSERT INTO jobs (kind, key) VALUES ('x', 'kept')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	good, err := backup.Dump(ctx, d.Conn(db.RoleRead), dir, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	key := regexp.MustCompile(`\\restrict ([A-Za-z0-9]+)`).FindSubmatch(must(dumpOf(good)))
	if key == nil {
		t.Fatal("the dump does not open with \\restrict")
	}
	for name, inject := range map[string]string{
		"a shell command":               "\\! touch " + marker + "\n",
		"an include":                    "  \\i /etc/passwd\n",
		"a shell command after SQL":     "SELECT 1; \\! touch " + marker + "\n",
		"its unrestrict before its end": "\\unrestrict " + string(key[1]) + "\n\\! touch " + marker + "\n",
		"an unrestrict after SQL":       "SELECT 1; \\unrestrict " + string(key[1]) + "\nSELECT 2; \\! touch " + marker + "\n",
	} {
		bad := filepath.Join(dir, "crew-20260101T000000Z.sql.gz")
		if err := withLine(good, bad, inject); err != nil {
			t.Fatal(err)
		}
		// The filter refuses it, or psql does in its restricted mode: either way it does not run.
		if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), bad); err == nil ||
			!(strings.Contains(err.Error(), "meta-command") || strings.Contains(err.Error(), "unrestrict") || strings.Contains(err.Error(), "restricted")) {
			t.Errorf("%s: restore gave %v", name, err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: the command ran", name)
		}
	}
	// A file that does not open with \restrict is no dump pg_dump wrote.
	plain := filepath.Join(dir, "crew-20260101T000001Z.sql.gz")
	if err := gzWrite(plain, "SELECT 1;\n"); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), plain); err == nil || !strings.Contains(err.Error(), "does not open with") {
		t.Errorf("no restrict: %v", err)
	}
	// A dump Crew made, with its \restrict lines and its COPY data, still restores.
	if err := backup.Restore(ctx, d.Conn(db.RoleMigrate), good); err != nil {
		t.Fatalf("a good dump: %v", err)
	}
	var n int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE key = 'kept'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after the refused restores: %d, %v", n, err)
	}
}

// withLine writes the dump of the backup src as a plain dump dst, with one line put in after its
// first line.
func withLine(src, dst, line string) error {
	body, err := dumpOf(src)
	if err != nil {
		return err
	}
	i := afterRestrict(body)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := zw.Write(append(append(append([]byte{}, body[:i]...), line...), body[i:]...)); err != nil {
		return err
	}
	return zw.Close()
}

// dumpOf is crew.sql of a backup file.
func dumpOf(file string) ([]byte, error) {
	entries, err := entriesOf(file)
	if err != nil {
		return nil, err
	}
	return entries["crew.sql"], nil
}

// entriesOf reads every regular file of a backup's tar.gz.
func entriesOf(file string) (map[string][]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	var order []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		order = append(order, hdr.Name)
		if hdr.Typeflag == tar.TypeReg {
			if out[hdr.Name], err = io.ReadAll(tr); err != nil {
				return nil, err
			}
		}
	}
	if len(order) < 2 || order[0] != "months.txt" || order[1] != "crew.sql" {
		return nil, fmt.Errorf("the entries are %v", order)
	}
	return out, nil
}

// gzLine puts a line after the first line of a gzipped file, in place.
func gzLine(path, line string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return err
	}
	body, err := io.ReadAll(zr)
	f.Close()
	if err != nil {
		return err
	}
	i := afterRestrict(body)
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := zw.Write(append(append(append([]byte{}, body[:i]...), line...), body[i:]...)); err != nil {
		return err
	}
	return zw.Close()
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func gzWrite(path, body string) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := zw.Write([]byte(body)); err != nil {
		return err
	}
	return zw.Close()
}

// afterRestrict is where the line after pg_dump's \restrict starts, so a line put there is read in
// psql's restricted mode.
func afterRestrict(body []byte) int {
	i := strings.Index(string(body), "\\restrict ")
	if i < 0 {
		return strings.Index(string(body), "\n") + 1
	}
	return i + strings.Index(string(body[i:]), "\n") + 1
}
