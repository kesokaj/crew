package models

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
)

// RetryAfter is how long a group at its limit waits when no reset time is known: Crew then tries it
// again (12.7). It is also the most a run's own report holds a group.
const RetryAfter = 15 * time.Minute

// MaxWindow is the longest window of any limit, a week: no reset is believed further away.
const MaxWindow = 7 * 24 * time.Hour

// checkKept is the condition under which a write may change a window: always for Crew's own check;
// for a run's report ($7 = 'run'), only a window the check did not read lately and does not hold.
const checkKept = `($7 <> 'run' OR q.source = 'run' OR (q.read_at < now() - interval '30 minutes' AND coalesce(q.reached_until, now()) <= now()))`

// heldUntil is the reset a window holds its group to, as the guard counts it: a run's report holds
// RetryAfter at most from when it was read.
const heldUntil = `CASE WHEN source = 'run' THEN least(reached_until, read_at + interval '15 minutes') ELSE reached_until END`

// RecordLimits keeps what a CLI said about its limits (12.7): the share used of each window and when
// it resets. A window the CLI says is reached is held until its reset. A window that is no longer
// reached frees its group at once. A run's report (source "run") changes no window that Crew's own
// check read in the last CheckFresh, and never frees one that check holds (reviews of M8).
func RecordLimits(ctx context.Context, pool *pgxpool.Pool, name string, limits []cli.Limit, source string) error {
	for _, l := range limits {
		var resets *time.Time
		if !l.ResetsAt.IsZero() {
			t := l.ResetsAt
			resets = &t
		}
		var until *time.Time
		if l.Reached {
			t := time.Now().Add(RetryAfter)
			if resets != nil && resets.After(time.Now()) {
				t = *resets
			}
			// No window is longer than a week.
			if ceiling := time.Now().Add(MaxWindow); t.After(ceiling) {
				t = ceiling
			}
			until = &t
		}
		if _, err := pool.Exec(ctx, `INSERT INTO quota AS q (cli, grp, win, used, resets_at, reached_until, reached_at, source)
			VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6::timestamptz IS NULL THEN NULL ELSE now() END, $7)
			ON CONFLICT (cli, grp, win) DO UPDATE SET used = $4, resets_at = $5, reached_until = $6,
				reached_at = CASE WHEN $6::timestamptz IS NULL THEN q.reached_at WHEN q.reached_until > now() THEN q.reached_at ELSE now() END,
				source = $7, read_at = now()
			WHERE `+checkKept+``,
			name, l.Group, l.Window, l.Used, resets, until, source); err != nil {
			return err
		}
	}
	if source != "run" {
		if _, err := pool.Exec(ctx, `DELETE FROM quota WHERE cli = $1 AND win = 'limit' AND coalesce(reached_until, now()) <= now()`, name); err != nil {
			return err
		}
	}
	return nil
}

// Reached marks one group, or one model (model:<name>), of a CLI as at its limit, when a run ended at
// it (12.7): until resets, or RetryAfter from now when the reset is not known. The window is the
// one the CLI named, or "limit" when it named none.
func Reached(ctx context.Context, q Execer, name, group, window string, resets time.Time) (time.Time, error) {
	until := time.Now().Add(RetryAfter)
	if resets.After(time.Now()) {
		until = resets
	}
	if window == "" {
		window = "limit"
	}
	// A window of the group the CLI said is reached already may know a later reset: the subtask waits
	// for the later of the two.
	var known *time.Time
	if err := q.QueryRow(ctx, `SELECT max(`+heldUntil+`) FROM quota WHERE cli = $1 AND grp = $2 AND `+heldUntil+` > now()`,
		name, group).Scan(&known); err != nil {
		return until, err
	}
	wait := until
	if known != nil && known.After(wait) {
		wait = *known
	}
	_, err := q.Exec(ctx, `INSERT INTO quota AS q (cli, grp, win, used, resets_at, reached_until, reached_at, source)
		VALUES ($1, $2, $3, 1, $4, $4, now(), $5)
		ON CONFLICT (cli, grp, win) DO UPDATE SET used = 1, reached_until = greatest(coalesce(q.reached_until, $4), $4),
			resets_at = $4, reached_at = CASE WHEN q.reached_until > now() THEN q.reached_at ELSE now() END,
			source = 'run', read_at = now()
		WHERE ($5 <> 'run' OR q.source = 'run' OR (q.read_at < now() - interval '30 minutes' AND coalesce(q.reached_until, now()) <= now()))`,
		name, group, window, until, "run")
	return wait, err
}

// Execer is a pool or a transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Window is the state of one window of a group of models of a CLI.
type Window struct {
	CLI, Group, Window string
	Used               float64
	ResetsAt           *time.Time
	ReachedUntil       *time.Time
	ReachedAt          *time.Time
	Source             string
	ReadAt             time.Time
}

// Windows are every window the quota guard knows.
func Windows(ctx context.Context, q db.Querier) ([]Window, error) {
	rows, err := q.Query(ctx, `SELECT cli, grp, win, used, resets_at, reached_until, reached_at, source, read_at
		FROM quota WHERE win <> 'limit' OR coalesce(reached_until, now()) > now() ORDER BY cli, grp, win`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Window
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.CLI, &w.Group, &w.Window, &w.Used, &w.ResetsAt, &w.ReachedUntil, &w.ReachedAt, &w.Source, &w.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Guard is the quota guard's view of now: which groups and models are at their limit, until when,
// and which are close to it (12.7).
type Guard struct {
	limited map[string]time.Time
	close   map[string]bool
}

// LoadGuard reads the guard's state; slowDownAt is quota.slow_down_at.
func LoadGuard(ctx context.Context, pool db.Querier, slowDownAt float64) (Guard, error) {
	g := Guard{limited: map[string]time.Time{}, close: map[string]bool{}}
	ws, err := Windows(ctx, pool)
	if err != nil {
		return g, err
	}
	now := time.Now()
	for _, w := range ws {
		k := w.CLI + "/" + w.Group
		until := w.ReachedUntil
		// What a run said is its own account (12.4): it holds the group for RetryAfter at most, and
		// only what Crew read itself, in a login check, holds it to the reset (security review of M8).
		hint := w.Source == "run"
		if hint && until != nil && until.After(w.ReadAt.Add(RetryAfter)) {
			t := w.ReadAt.Add(RetryAfter)
			until = &t
		}
		if until != nil {
			if until.After(now) {
				if until.After(g.limited[k]) {
					g.limited[k] = *until
				}
			}
			continue
		}
		// A share read before its window reset says nothing about the window that runs now.
		if hint && now.Sub(w.ReadAt) > RetryAfter {
			continue
		}
		if slowDownAt > 0 && w.Used >= slowDownAt && (w.ResetsAt == nil || w.ResetsAt.After(now)) {
			g.close[k] = true
		}
	}
	return g, nil
}

// keys are the guard's keys a model of a CLI falls under: its group, and the model's own limit.
func keys(a cli.Adapter, model string) []string {
	out := []string{a.Name() + "/" + a.LimitGroup(model)}
	if m := a.ModelLimit(model); m != "" {
		out = append(out, a.Name()+"/model:"+m)
	}
	return out
}

// Limited says a model of a CLI is at a limit now, and until when.
func (g Guard) Limited(a cli.Adapter, model string) (time.Time, bool) {
	var until time.Time
	for _, k := range keys(a, model) {
		if t, ok := g.limited[k]; ok && t.After(until) {
			until = t
		}
	}
	return until, !until.IsZero()
}

// Close says a model's group or the model itself is above slow_down_at (12.7).
func (g Guard) Close(a cli.Adapter, model string) bool {
	for _, k := range keys(a, model) {
		if g.close[k] {
			return true
		}
	}
	return false
}

// CLIClose says some group of a CLI is close to its limit: the guard then lowers the CLI's lead slots.
func (g Guard) CLIClose(name string) bool {
	for k := range g.close {
		if len(k) > len(name) && k[:len(name)+1] == name+"/" {
			return true
		}
	}
	return false
}
