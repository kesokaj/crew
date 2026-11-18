// Package diskguard looks after the disk of the data folder (16.10): at disk.clean_at percent full it
// removes what retention allows, and at disk.stop_at it holds Crew so no new run starts, until there
// is room again. It never removes a trajectory.
package diskguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/settings"
)

// Usage is how full a disk is, as df counts it: used of used and available, so the space only root
// may use is not counted as room.
type Usage struct {
	Used, Avail uint64
}

// Percent is how full the disk is.
func (u Usage) Percent() float64 {
	if u.Used+u.Avail == 0 {
		return 0
	}
	return float64(u.Used) * 100 / float64(u.Used+u.Avail)
}

// Guard is the disk guard. Stat and Prune are the machine's; tests give fakes.
type Guard struct {
	Pool *pgxpool.Pool
	Dir  string
	// Stat reads the disk that holds Dir.
	Stat func(dir string) (Usage, error)
	// Prune removes the images and the build cache retention allows, and says the bytes it freed.
	Prune    func(ctx context.Context) (uint64, error)
	Settings func(ctx context.Context) (settings.Doc, error)
	Log      *slog.Logger
	Now      func() time.Time
}

// Run checks the disk every period until ctx ends.
func (g *Guard) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := g.Check(ctx); err != nil {
			g.Log.Error("the disk guard", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Check is one round. A disk that cannot be read changes nothing: the guard keeps its last state and
// removes nothing on an error.
func (g *Guard) Check(ctx context.Context) error {
	u, err := g.Stat(g.Dir)
	if err != nil {
		return fmt.Errorf("reading the disk of %s: %w", g.Dir, err)
	}
	doc, err := g.Settings(ctx)
	if err != nil {
		return err
	}
	cleanAt, stopAt := number(doc, "clean_at", 85), number(doc, "stop_at", 95)
	if err := g.sample(ctx, u); err != nil {
		g.Log.Warn("keeping a reading of the disk", "error", err)
	}
	if u.Percent() >= cleanAt {
		freed, err := g.clean(ctx)
		if err != nil {
			g.Log.Error("the disk guard could not remove all it may", "error", err)
		}
		g.Log.Info("the disk guard cleaned", "percent", fmt.Sprintf("%.1f", u.Percent()), "freed", freed)
		if u, err = g.Stat(g.Dir); err != nil {
			return fmt.Errorf("reading the disk of %s again: %w", g.Dir, err)
		}
	}
	why := ""
	if p := u.Percent(); p >= stopAt {
		why = fmt.Sprintf("%.0f%% of the disk is used, %s free; Crew starts no new run from %.0f%%", p, bytes(u.Avail), stopAt)
	}
	st, err := hold.Read(ctx, g.Pool)
	if err != nil {
		return err
	}
	if st.DiskFull != why {
		if why != "" {
			g.Log.Warn("the disk is too full: no new run starts", "why", why)
		} else {
			g.Log.Info("the disk has room again")
		}
		return hold.SetDisk(ctx, g.Pool, why)
	}
	return nil
}

// clean removes what retention allows (16.10): a conversation's CLI files past their keep time, and
// the images and build cache Prune takes. A trajectory is never removed.
func (g *Guard) clean(ctx context.Context) (uint64, error) {
	var freed uint64
	tag, err := g.Pool.Exec(ctx, `DELETE FROM cli_files WHERE keep_until < $1`, g.now())
	if err != nil {
		return 0, err
	}
	if n := tag.RowsAffected(); n > 0 {
		g.Log.Info("removed CLI files past their keep time", "files", n)
	}
	if g.Prune != nil {
		n, err := g.Prune(ctx)
		freed += n
		if err != nil {
			return freed, err
		}
	}
	return freed, nil
}

// sample keeps one reading an hour, and a year of them.
func (g *Guard) sample(ctx context.Context, u Usage) error {
	now := g.now()
	_, err := g.Pool.Exec(ctx, `INSERT INTO disk_samples (at, used_bytes, total_bytes)
		SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM disk_samples WHERE at > $1::timestamptz - interval '1 hour')`,
		now, int64(u.Used), int64(u.Used+u.Avail))
	if err != nil {
		return err
	}
	_, err = g.Pool.Exec(ctx, `DELETE FROM disk_samples WHERE at < $1::timestamptz - interval '1 year'`, now)
	return err
}

func number(doc settings.Doc, key string, def float64) float64 {
	disk, _ := doc["disk"].(settings.Doc)
	switch v := disk[key].(type) {
	case int:
		return float64(v)
	case float64:
		return v
	}
	return def
}

func bytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}

// Room is what the Database screen says of the disk (16.10): how full it is, how fast it filled in
// the last 30 days of readings, and how many months are left before Crew stops at disk.stop_at. Grow
// and Months are nil until there are readings a day apart, or while the disk does not grow.
type Room struct {
	At      time.Time `json:"at"`
	Used    int64     `json:"usedBytes"`
	Total   int64     `json:"totalBytes"`
	Percent float64   `json:"percent"`
	// GrowPerMonth is in bytes; Months is until the stop.
	GrowPerMonth *float64 `json:"growPerMonth,omitempty"`
	Months       *float64 `json:"monthsLeft,omitempty"`
}

// ReadRoom reads the newest reading and the growth since the oldest of the last 30 days.
func ReadRoom(ctx context.Context, pool *pgxpool.Pool, stopAt float64) (*Room, error) {
	var r Room
	var firstAt time.Time
	var firstUsed int64
	err := pool.QueryRow(ctx, `SELECT n.at, n.used_bytes, n.total_bytes, o.at, o.used_bytes
		FROM (SELECT * FROM disk_samples ORDER BY at DESC LIMIT 1) n,
		     (SELECT * FROM disk_samples WHERE at > now() - interval '30 days' ORDER BY at LIMIT 1) o`).
		Scan(&r.At, &r.Used, &r.Total, &firstAt, &firstUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.Total > 0 {
		r.Percent = float64(r.Used) * 100 / float64(r.Total)
	}
	if days := r.At.Sub(firstAt).Hours() / 24; days >= 1 && r.Used > firstUsed {
		grow := float64(r.Used-firstUsed) / days * 30
		left := (stopAt/100*float64(r.Total) - float64(r.Used)) / grow
		r.GrowPerMonth, r.Months = &grow, &left
	}
	return &r, nil
}
