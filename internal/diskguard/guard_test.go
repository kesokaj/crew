package diskguard_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/diskguard"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/settings"
)

type disk struct {
	mu     sync.Mutex
	pct    uint64
	err    error
	pruned int
}

func (d *disk) stat(string) (diskguard.Usage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return diskguard.Usage{Used: d.pct << 30, Avail: (100 - d.pct) << 30}, d.err
}

func (d *disk) set(pct uint64, err error) { d.mu.Lock(); d.pct, d.err = pct, err; d.mu.Unlock() }

// The disk guard (16.10; 22, "Pause all, and the disk guard"): below 85% it does nothing; at 85% it
// removes the CLI files past their keep time and the images and build cache retention allows, and no
// trajectory; at 95% no new run starts and the inbox says why with the numbers; with room again
// that clears by itself. A disk that cannot be read changes nothing and removes nothing.
func TestTheDiskGuardCleansAndStops(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	fake := &disk{}
	machine, _ := settings.Defaults(settings.Machine{Version: "1.0.0"})
	g := &diskguard.Guard{Pool: d.Server, Dir: "/data", Stat: fake.stat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Prune: func(context.Context) (uint64, error) {
			fake.mu.Lock()
			fake.pruned++
			fake.mu.Unlock()
			return 1 << 30, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return machine, nil }}
	exec := func(sql string) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO conversations (id, purpose, cli) VALUES ('c1111111-0000-4000-8000-000000000001', 'run', 'agy')`)
	exec(`INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until) VALUES
		('c1111111-0000-4000-8000-000000000001', 'old', '\x00', 'a', now() - interval '1 day'),
		('c1111111-0000-4000-8000-000000000001', 'new', '\x00', 'b', now() + interval '1 day')`)
	count := func(q string) int {
		var n int
		d.Server.QueryRow(ctx, q).Scan(&n)
		return n
	}
	held := func() string { return hold.Held(ctx, d.Server) }

	fake.set(50, nil)
	if err := g.Check(ctx); err != nil || fake.pruned != 0 || count(`SELECT count(*) FROM cli_files`) != 2 || held() != "" {
		t.Fatalf("at 50%%: %v, pruned %d, held %q", err, fake.pruned, held())
	}
	if count(`SELECT count(*) FROM disk_samples`) != 1 {
		t.Fatal("no reading kept")
	}
	fake.set(90, nil)
	if err := g.Check(ctx); err != nil || fake.pruned != 1 || held() != "" {
		t.Fatalf("at 90%%: %v, pruned %d, held %q", err, fake.pruned, held())
	}
	if count(`SELECT count(*) FROM cli_files WHERE path = 'new'`) != 1 || count(`SELECT count(*) FROM cli_files`) != 1 {
		t.Fatal("the CLI files: the old one stays, or the new one went")
	}
	fake.set(97, nil)
	if err := g.Check(ctx); err != nil || !strings.HasPrefix(held(), "The disk is too full: 97% of the disk is used, 3.0 GB free") {
		t.Fatalf("at 97%%: %v, held %q", err, held())
	}
	diskItem := func() *inbox.Item {
		items, err := inbox.List(ctx, d.Server, machine)
		if err != nil {
			t.Fatal(err)
		}
		for i := range items {
			if items[i].Kind == inbox.KindDiskFull {
				return &items[i]
			}
		}
		return nil
	}
	if it := diskItem(); it == nil || !strings.Contains(it.Body, "97% of the disk is used") {
		t.Fatalf("the inbox: %+v", it)
	}
	// A disk that cannot be read: the guard keeps its state and removes nothing.
	fake.set(40, errors.New("statfs failed"))
	if err := g.Check(ctx); err == nil || held() == "" || fake.pruned != 2 {
		t.Fatalf("an unreadable disk: %v, held %q, pruned %d", err, held(), fake.pruned)
	}
	fake.set(60, nil)
	if err := g.Check(ctx); err != nil || held() != "" {
		t.Fatalf("with room again: %v, held %q", err, held())
	}
	if it := diskItem(); it != nil {
		t.Fatalf("the inbox with room again: %+v", it)
	}
}

// The Database screen's room: the growth over the readings of the last 30 days, a month at that
// rate, and the months left before the stop.
func TestRoomSaysTheGrowthAndTheMonthsLeft(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	if r, err := diskguard.ReadRoom(ctx, d.Server, 95); err != nil || r != nil {
		t.Fatalf("no readings: %+v %v", r, err)
	}
	now := time.Now()
	const gb = int64(1) << 30
	if _, err := d.Server.Exec(ctx, `INSERT INTO disk_samples (at, used_bytes, total_bytes) VALUES ($1, $2, $4), ($3, $5, $4)`,
		now.Add(-10*24*time.Hour), 100*gb, now, 1000*gb, 110*gb); err != nil {
		t.Fatal(err)
	}
	r, err := diskguard.ReadRoom(ctx, d.Server, 95)
	if err != nil || r == nil || r.GrowPerMonth == nil || r.Months == nil {
		t.Fatalf("%+v %v", r, err)
	}
	// 10 GB in 10 days is 30 GB a month; 950 - 110 GB is 28 months.
	if g := *r.GrowPerMonth / float64(gb); g < 29.9 || g > 30.1 {
		t.Fatalf("growth %.2f GB a month", g)
	}
	if m := *r.Months; m < 27.9 || m > 28.1 {
		t.Fatalf("%.2f months left", m)
	}
}
