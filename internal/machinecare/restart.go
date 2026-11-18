// Package machinecare is the server's half of looking after the machine (16.10): whether the machine
// may restart for the security updates it installed by itself, and the news of a certificate that
// runs out. crew-ctl, from a timer on the machine, only reads whether a restart is needed, asks
// `crew admin restart-check`, and restarts on a yes: the server decides (plan M17 D19).
package machinecare

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// WaitDays is how long a restart waits for quiet hours before Crew holds new runs for it (16.10).
const WaitDays = 7

// Answer is the server's answer to crew-ctl: Restart is the one yes, Why says why, either way.
type Answer struct {
	Restart bool
	Why     string
}

// Beginner opens the one transaction the answer is made in.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RestartCheck answers crew-ctl's question at now, read on the clock of loc (CREW_TIMEZONE). required
// is whether the machine says a restart is needed (/var/run/reboot-required).
//
// No restart needed clears what a past one left. A needed one is recorded the first time; it is a yes
// when nothing is going and now is in machine_care.restart_hours, or, once it waited WaitDays, when
// nothing is going at any hour: then the hold restart_wait has held new runs, so the running ones
// finish. A yes is written as news and holds new runs for hold.RestartingFor (restart_at) before it is answered, so
// there is no restart without its news; an error is never a yes.
func RestartCheck(ctx context.Context, pool Beginner, now time.Time, loc *time.Location, required bool) (Answer, error) {
	var a Answer
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var wanted, wait *time.Time
		err := tx.QueryRow(ctx, `SELECT reboot_wanted_at, restart_wait FROM crew_state WHERE id = 1 FOR UPDATE`).Scan(&wanted, &wait)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("crew_state has no row: the schema is not applied")
		}
		if err != nil {
			return err
		}
		if !required {
			a = Answer{Why: "the machine needs no restart"}
			if wanted == nil && wait == nil {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE crew_state SET reboot_wanted_at = NULL, restart_wait = NULL, restart_at = NULL WHERE id = 1`); err != nil {
				return err
			}
			return db.RecordChange(ctx, tx, "crew", "crew_state", "1", map[string]any{"rebootWantedAt": wanted, "restartWait": wait},
				map[string]any{"rebootWantedAt": nil, "restartWait": nil})
		}
		if wanted == nil {
			wanted = &now
			if _, err := tx.Exec(ctx, `UPDATE crew_state SET reboot_wanted_at = $1 WHERE id = 1`, now); err != nil {
				return err
			}
		}
		hours, err := settings.RestartHours(ctx, tx)
		if err != nil {
			return fmt.Errorf("machine_care.restart_hours: %w", err)
		}
		going, err := Going(ctx, tx, now)
		if err != nil {
			return err
		}
		overdue := now.Sub(*wanted) >= WaitDays*24*time.Hour
		if overdue && wait == nil {
			if _, err := tx.Exec(ctx, `UPDATE crew_state SET restart_wait = $1 WHERE id = 1`, now); err != nil {
				return err
			}
			if err := db.RecordChange(ctx, tx, "crew", "crew_state", "1", map[string]any{"restartWait": nil}, map[string]any{"restartWait": now}); err != nil {
				return err
			}
			if err := News(ctx, tx, "restart_wait", fmt.Sprintf("restart_wait:%d", wanted.Unix()), fmt.Sprintf(
				"A restart for security updates has waited %d days for quiet hours: Crew holds new runs, lets the running ones finish, and then the machine restarts", WaitDays)); err != nil {
				return err
			}
		}
		inHours := hours.Holds(now, loc)
		switch {
		case len(going) > 0:
			a.Why = "not quiet: " + strings.Join(going, ", ") + " going"
			return nil
		case !overdue && !inHours:
			a.Why = fmt.Sprintf("outside the hours %s (%s) of machine_care.restart_hours; the restart waits since %s",
				formatHours(hours), loc, wanted.In(loc).Format("Jan 2 15:04"))
			return nil
		}
		why := "the machine restarts for security updates, in the hours " + formatHours(hours)
		if !inHours {
			why = fmt.Sprintf("the machine restarts for security updates, after %d days of waiting", WaitDays)
		}
		// On the database's clock, as hold.Read reads it: the hold is of the real minutes the machine
		// takes to go down.
		if _, err := tx.Exec(ctx, `UPDATE crew_state SET restart_at = now() WHERE id = 1`); err != nil {
			return err
		}
		// One line for the restart that is wanted: a reboot that fails says yes at every tick, and its
		// unit fails each time, which shows it without a line of news every 15 minutes.
		if err := News(ctx, tx, "restart", fmt.Sprintf("restart:%d", wanted.Unix()), strings.ToUpper(why[:1])+why[1:]); err != nil {
			return err
		}
		a = Answer{Restart: true, Why: why}
		return nil
	})
	if err != nil {
		return Answer{Why: "no restart: " + err.Error()}, err
	}
	return a, nil
}

// Going lists what a restart would cut (16.10): every sandbox that is not a hosted project's (a run,
// a check, a build, a terminal, a chat, a login check), a build of a release, a gate that checks or
// runs its live steps, and an install the updater runs. A hosted project is started again after the
// restart, as Docker starts every service; the probe lives for seconds. An install that has run for
// six hours is not counted: the updater ends one long before that, and a row it left must not hold
// the restart for good.
func Going(ctx context.Context, q db.Querier, now time.Time) ([]string, error) {
	var out []string
	// A sandbox is going while it holds its token: Remove takes the token first, so a row whose
	// container Docker failed to remove has none, its work ended, and it must not hold the restart for
	// good (security review of M17's Close).
	rows, err := q.Query(ctx, `SELECT kind, count(*) FROM sandboxes s WHERE removed_at IS NULL AND kind NOT IN ('hosted', 'probe')
		AND EXISTS (SELECT 1 FROM sandbox_tokens t WHERE t.sandbox = s.id) GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if n == 1 {
			out = append(out, "a "+kind+" sandbox")
		} else {
			out = append(out, fmt.Sprintf("%d %s sandboxes", n, kind))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var builds, gates, installs int
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM builds WHERE state = 'running'),
			(SELECT count(*) FROM gates WHERE state IN ('checking', 'live')),
			(SELECT count(*) FROM update_requests WHERE state = 'running' AND asked_at > $1)`, now.Add(-6*time.Hour)).
		Scan(&builds, &gates, &installs); err != nil {
		return nil, err
	}
	for _, c := range []struct {
		n    int
		what string
	}{{builds, "a build of a release"}, {gates, "a gate"}, {installs, "an install"}} {
		if c.n > 0 {
			out = append(out, c.what)
		}
	}
	return out, nil
}

// News writes one line of news on the Crew level, once by its key: a key that is there already
// writes nothing, and is no error.
func News(ctx context.Context, q db.Querier, kind, key, text string) error {
	_, err := q.Exec(ctx, `INSERT INTO machine_news (kind, key, text) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`, kind, key, text)
	return err
}

func formatHours(h settings.Hours) string {
	f := func(d time.Duration) string { return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60) }
	return f(h.From) + "-" + f(h.To)
}
