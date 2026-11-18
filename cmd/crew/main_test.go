package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

func useDB(t *testing.T, empty bool) *dbtest.DB {
	t.Helper()
	d := dbtest.New(t, empty)
	t.Setenv("CREW_DB_HOST", os.Getenv(dbtest.EnvHost))
	t.Setenv("CREW_DB_NAME", d.Name)
	t.Setenv("CREW_ADMIN_EMAIL", "admin@example.com")
	return d
}

func crew(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(stdin), &out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return out.String(), err
}

// What crew-ctl passes on, against a database of an installation: migrate on an empty one, then the
// install proof, then the settings out and back in (16.11).
func TestAdminVerbs(t *testing.T) {
	useDB(t, true)
	out, err := crew(t, "", "admin", "migrate")
	if err != nil || !strings.Contains(out, "applied 0001_first.sql") {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	if out, err := crew(t, "", "admin", "migrate"); err != nil || strings.Contains(out, "applied") {
		t.Fatalf("a second migrate: %v\n%s", err, out)
	}
	if out, err := crew(t, "", "admin", "proof"); err != nil || !strings.Contains(out, "written and read back") {
		t.Fatalf("proof: %v\n%s", err, out)
	}

	exported, err := crew(t, "", "admin", "settings", "export")
	if err != nil || !strings.Contains(exported, "clean_at: 85") {
		t.Fatalf("export: %v\n%s", err, exported)
	}
	file := filepath.Join(t.TempDir(), "crew.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(exported, "clean_at: 85", "clean_at: 80", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := crew(t, "", "admin", "settings", "import", file); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if again, _ := crew(t, "", "admin", "settings", "export"); !strings.Contains(again, "clean_at: 80") {
		t.Fatalf("the import did not take:\n%s", again)
	}
	if _, err := crew(t, "version: 1\ndisk: {clean_at: 101}\n", "admin", "settings", "import", "-"); err == nil {
		t.Fatal("an invalid file was imported")
	}
	if again, _ := crew(t, "", "admin", "settings", "export"); !strings.Contains(again, "clean_at: 80") {
		t.Fatalf("a refused import changed the settings:\n%s", again)
	}
}

// The check server's smoke run stores the event the microVM the updater started printed, read from
// stdin, and reads it back; what is not one line of JSON of at most 4096 bytes is refused (16.4 step 4).
func TestAdminProofStoreStoresTheMicroVMsEvent(t *testing.T) {
	d := useDB(t, false)
	line := `{"type":"crew_proof","kernel":"6.12-kata"}`
	if out, err := crew(t, line+"\n", "admin", "proof", "store"); err != nil || !strings.Contains(out, "stored and read back: "+line) {
		t.Fatalf("%v\n%s", err, out)
	}
	var raw string
	if err := d.Server.QueryRow(context.Background(), `SELECT e.raw FROM events e JOIN conversations c ON c.id = e.conversation WHERE c.purpose = 'proof'`).Scan(&raw); err != nil || raw != line {
		t.Fatalf("%q %v", raw, err)
	}
	// 4097 bytes of valid JSON: one more than a line may be.
	long := `{"x":"` + strings.Repeat("y", 4097-len(`{"x":""}`)) + `"}`
	for _, bad := range []string{"", "not json", long} {
		if _, err := crew(t, bad, "admin", "proof", "store"); err == nil {
			t.Errorf("%.20q was stored", bad)
		}
	}
}

func TestBackupGoesWhereTheSettingsSay(t *testing.T) {
	useDB(t, false)
	data := t.TempDir()
	t.Setenv("CREW_DATA_DIR", data)
	dir := filepath.Join(data, "backups")
	file := filepath.Join(t.TempDir(), "crew.yaml")
	if err := os.WriteFile(file, []byte("version: 1\nupdates: {backup_path: /etc, backup_keep: 2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := crew(t, "", "admin", "settings", "import", file); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := crew(t, "", "admin", "backup"); err == nil || !strings.Contains(err.Error(), "not under") {
		t.Fatalf("a backup outside the backups folder: %v", err)
	}
	if err := os.WriteFile(file, []byte("version: 1\nupdates: {backup_path: "+dir+", backup_keep: 2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := crew(t, "", "admin", "settings", "import", file); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := crew(t, "", "admin", "backup")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), dir+"/crew-") {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if out, err := crew(t, "", "admin", "restore", strings.TrimSpace(out)); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	// The Database screen lists them by name, with the folder they are in.
	pool, err := db.Open(context.Background(), db.ConnFromEnv(db.RoleServer))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	got, files, err := listBackups(context.Background(), pool, loadEnv())
	if err != nil || got != dir || len(files) != 1 || files[0] != filepath.Base(strings.TrimSpace(out)) {
		t.Fatalf("listed %s %v %v", got, files, err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"nonsense"}, {"admin"}, {"admin", "nonsense"}, {"admin", "settings"}, {"admin", "restore"}} {
		if _, err := crew(t, "", args...); err == nil {
			t.Errorf("%v: no error", args)
		} else if _, ok := err.(usageError); !ok {
			t.Errorf("%v: %v is not a usage error", args, err)
		}
	}
}

// crew serve answers its health check once the database answers, makes the month partitions, and
// stops cleanly; crew health reads it as Docker's health check does.
func TestServeAndHealth(t *testing.T) {
	d := useDB(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREW_HOSTNAME", "crew.example.com")
	t.Setenv("CREW_LISTEN", addr)
	t.Setenv("CREW_INGRESS_TOKEN_FILE", tokenFile)
	t.Setenv("CREW_WEB_DIR", t.TempDir())
	t.Setenv("CREW_DOOR_NAME", "127.0.0.1") // the loopback stands in for the ingress's network

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, strings.NewReader(""), io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	var out string
	for i := 0; i < 100; i++ {
		if out, err = crew(t, "", "health"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !strings.Contains(out, `"database":"ok"`) {
		t.Fatalf("health: %v %s", err, out)
	}
	// Three months of event partitions exist ahead. serve makes them beside answering, so they may come
	// a moment after health does: on a busy machine, or in a check sandbox's microVM, they did.
	var n int
	for i := 0; ; i++ {
		err := d.Server.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_class WHERE relname LIKE 'events_____\__%' AND relkind = 'r'`).Scan(&n)
		if err == nil && n >= 3 {
			break
		}
		if i == 100 {
			t.Fatalf("%d month partitions, want at least 3 (%v)", n, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	if _, err := crew(t, "", "health"); err == nil {
		t.Fatal("health passes with no server")
	}
}

func TestServeNeedsItsAnswers(t *testing.T) {
	t.Setenv("CREW_HOSTNAME", "")
	if _, err := crew(t, "", "serve"); err == nil || !strings.Contains(err.Error(), "CREW_HOSTNAME") {
		t.Fatalf("serve without a hostname: %v", err)
	}
}

// crew-ctl pause and resume, when the app cannot be opened (7.9): crew admin writes the row every
// start asks, with its row in the history.
func TestAdminPauseAndResume(t *testing.T) {
	d := useDB(t, false)
	ctx := context.Background()
	if out, err := crew(t, "", "admin", "pause"); err != nil || !strings.Contains(out, "Crew is paused") {
		t.Fatalf("pause: %v\n%s", err, out)
	}
	var why string
	if err := d.Server.QueryRow(ctx, `SELECT coalesce(why, '') FROM crew_state`).Scan(&why); err != nil || why != "pause" {
		t.Fatalf("after pause: %q %v", why, err)
	}
	if out, err := crew(t, "", "admin", "resume"); err != nil || !strings.Contains(out, "Crew is resumed after") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	var n int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM crew_state WHERE paused_at IS NULL`).Scan(&n)
	var changes int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM changes WHERE tbl = 'crew_state'`).Scan(&changes)
	if n != 1 || changes != 2 {
		t.Fatalf("after resume: %d rows not paused, %d rows of history", n, changes)
	}
}
