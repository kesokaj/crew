package diskguard_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/diskguard"
	"github.com/kesokaj/crew/internal/settings"
)

// A trajectory is never removed by the disk guard (8.3, 16.10, 21 "The disk fills with
// trajectories"), not at clean_at and not at stop_at, and not with keep_trajectories_days set: no
// trajectory is marked done, since the marks of 8.3 are not built (D29), so the key removes nothing.
// Its events, its turns, and its run's conversation stay, a year old or a minute. No code reads the
// key yet: this holds the guard; a reader of the key elsewhere, when the marks come, needs its own.
func TestTheDiskGuardNeverRemovesATrajectory(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	fake := &disk{}
	machine, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	machine["keep_trajectories_days"] = 1
	g := &diskguard.Guard{Pool: d.Server, Dir: "/data", Stat: fake.stat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Prune:    func(context.Context) (uint64, error) { return 0, nil },
		Settings: func(context.Context) (settings.Doc, error) { return machine, nil }}
	old := time.Now().AddDate(-1, 0, 0)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if _, err := d.Server.Exec(ctx, "SELECT crew_ensure_events_month($1)", old); err != nil {
		t.Fatal(err)
	}
	conv := "c2222222-0000-4000-8000-000000000002"
	exec(`INSERT INTO conversations (id, purpose, cli, created_at, last_turn_at) VALUES ($1, 'run', 'agy', $2, $2)`, conv, old)
	exec(`INSERT INTO turns (conversation, number, speaker, text, at) VALUES ($1, 1, 'agent', 'old', $2), ($1, 2, 'agent', 'new', now())`, conv, old)
	exec(`INSERT INTO events (conversation, seq, at, type) VALUES ($1, 1, $2, 'status'), ($1, 2, now(), 'status')`, conv, old)
	count := func(q string) int {
		var n int
		if err := d.Server.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, pct := range []uint64{50, 90, 99} {
		fake.set(pct, nil)
		if err := g.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if e, tu, c := count(`SELECT count(*) FROM events`), count(`SELECT count(*) FROM turns`), count(`SELECT count(*) FROM conversations`); e != 2 || tu != 2 || c != 1 {
			t.Fatalf("at %d%%: events %d, turns %d, conversations %d", pct, e, tu, c)
		}
	}
}

// The disk's readings are kept for a year, one an hour (16.10, schema step 39): an older one goes at
// the next reading, and a second reading within the hour is not kept.
func TestTheDisksReadingsAreKeptAYear(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	fake := &disk{}
	fake.set(40, nil)
	machine, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	now := time.Now()
	g := &diskguard.Guard{Pool: d.Server, Dir: "/data", Stat: fake.stat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: func(context.Context) (settings.Doc, error) { return machine, nil }, Now: func() time.Time { return now }}
	if _, err := d.Server.Exec(ctx, `INSERT INTO disk_samples (at, used_bytes, total_bytes) VALUES
		($1::timestamptz - interval '1 year 1 hour', 1, 2), ($1::timestamptz - interval '364 days', 1, 2)`, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := g.Check(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n, older int
	d.Server.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE at < $1::timestamptz - interval '1 year') FROM disk_samples`, now).Scan(&n, &older)
	if n != 2 || older != 0 {
		t.Fatalf("%d readings, %d older than a year", n, older)
	}
}
