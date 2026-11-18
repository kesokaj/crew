package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
)

func TestNextDaily(t *testing.T) {
	sto, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, sto)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for now, want := range map[string]string{
		"2026-10-13 20:00": "2026-10-14 03:00", // in the evening: tonight
		"2026-10-14 02:59": "2026-10-14 03:00", // just before
		"2026-10-14 03:00": "2026-10-15 03:00", // at the hour: the next day, never twice
		"2026-10-24 12:00": "2026-10-25 03:00", // the night summer time ends: a day of 25 hours
		"2026-03-28 12:00": "2026-03-29 03:00", // the night it starts: 02:00 to 03:00 does not exist
	} {
		if got := nextDaily(at(now), sto); !got.Equal(at(want)) {
			t.Errorf("after %s: %s, want %s", now, got.In(sto).Format("2006-01-02 15:04 MST"), want)
		}
	}
	// In UTC, the same hour.
	if got := nextDaily(time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC), time.UTC); !got.Equal(time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("UTC: %s", got)
	}
}

// In check mode the server gets no key file (16.8): it makes a key of its own, which opens no
// credential of the copy it runs on. Out of it, a missing key file stops the machine.
func TestCheckModeHasAKeyOfItsOwn(t *testing.T) {
	e := env{SecretKeyFile: filepath.Join(t.TempDir(), "no-such-key"), Check: true}
	a, err := checkKey(e)
	if err != nil || len(a) != credstore.KeySize {
		t.Fatalf("%d bytes, %v", len(a), err)
	}
	b, _ := checkKey(e)
	if string(a) == string(b) {
		t.Fatal("two check servers got the same key")
	}
	e.Check = false
	if _, err := checkKey(e); err == nil {
		t.Fatal("a server out of check mode started without its key file")
	}
}

// A server mails what needs the operator, and a check server, which starts no notifier, writes no
// message (16.4, 18): the same database with a CLI signed out gets an episode from the one and none
// from the other.
func TestOnlyAServerThatIsNotCheckingNotifies(t *testing.T) {
	for _, check := range []bool{true, false} {
		d := useDB(t, false)
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO cli_logins (cli, state, result) VALUES ('agy', 'signed_out', 'refused')`); err != nil {
			t.Fatal(err)
		}
		const proj = "00000000-0000-4000-8000-000000000009"
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1::uuid, 'shop', 'SF', 'running', 'local')`, proj); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO tickets (project, number, title, kind, why, what, goal, stage, origin, created_by, priority, position)
			VALUES ($1::uuid, 1, 'One', 'feature', 'w', 'w', 'g', 'backlog', 'operator', 'you', 2, 1)`, proj); err != nil {
			t.Fatal(err)
		}
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
		t.Setenv("CREW_SECRET_KEY_FILE", filepath.Join(t.TempDir(), "no-key"))
		if check {
			t.Setenv("CREW_CHECK", "1")
		} else {
			t.Setenv("CREW_CHECK", "")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, []string{"serve"}, strings.NewReader(""), io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil)))
		}()
		count := func() int {
			var n int
			d.Server.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE item = 'signin/agy'`).Scan(&n)
			return n
		}
		deadline := time.Now().Add(10 * time.Second)
		for count() == 0 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if check {
			if out, err := crew(t, "", "admin", "api", "GET", "/api/projects/shop/tasks/SF-1/chat"); err != nil || !strings.Contains(out, `"ticket":"SF-1"`) {
				t.Fatalf("check mode ticket chat: %v\n%s", err, out)
			}
		}
		got := count()
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("serve did not stop")
		}
		if check && got != 0 {
			t.Fatalf("a check server wrote %d messages", got)
		}
		if !check && got != 1 {
			t.Fatalf("a server wrote %d messages for one episode", got)
		}
	}
}
