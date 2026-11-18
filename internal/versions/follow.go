package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

// TakenOutHold is why a release of commit is held for improvements the operator took out before they
// were installed (16.3, since 0.55, the operator's answer 3 of report 10): it holds the merge of an
// improvement's ticket and not the merge of the Done revert ticket that takes it out. "" holds nothing.
// The improvement's ticket names its project, so the hold needs neither the builder nor the setting
// that names Crew's project; a history that cannot be read, or no Git to read it with, holds the
// release too, so an improvement taken out never installs by itself for want of a read (security
// review of 1863a0e..f8d57b2). The reason names i_NNNN only: the title is the improver's text.
func (s *Service) TakenOutHold(ctx context.Context, commit string) (string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT i.number, p.name,
			coalesce((SELECT c.commit FROM check_runs c WHERE c.ticket = i.ticket AND c.purpose = 'merge' AND c.state = 'passed'
				ORDER BY c.id DESC LIMIT 1), ''),
			coalesce((SELECT c.commit FROM ticket_links l JOIN tickets r ON r.id = l.from_ticket JOIN check_runs c ON c.ticket = r.id
				WHERE l.to_ticket = i.ticket AND l.kind = 'reverts' AND r.stage = 'done' AND c.purpose = 'merge' AND c.state = 'passed'
				ORDER BY c.id DESC LIMIT 1), '')
		FROM improvements i JOIN tickets t ON t.id = i.ticket JOIN projects p ON p.id = t.project
		WHERE i.status = 'removed' AND t.stage = 'done' ORDER BY i.number`)
	if err != nil {
		return "", err
	}
	type out struct {
		number                 int
		project, merge, revert string
	}
	var list []out
	for rows.Next() {
		var o out
		if err := rows.Scan(&o.number, &o.project, &o.merge, &o.revert); err != nil {
			rows.Close()
			return "", err
		}
		list = append(list, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	var holds, unread []string
	for _, o := range list {
		n := fmt.Sprintf("i_%04d", o.number)
		// No Git to read with, or a Done ticket with no merge recorded, whose change is not known: it
		// holds as unread (round 2 of the security review of 1863a0e..f8d57b2).
		if s.Git == nil || o.merge == "" {
			unread = append(unread, n)
			continue
		}
		has, err := s.Git.IsAncestor(ctx, o.project, o.merge, commit)
		if err == nil && has && o.revert != "" {
			var reverted bool
			if reverted, err = s.Git.IsAncestor(ctx, o.project, o.revert, commit); reverted {
				has = false
			}
		}
		switch {
		case err != nil:
			unread = append(unread, n)
		case has:
			holds = append(holds, n)
		}
	}
	var why []string
	if len(holds) > 0 {
		why = append(why, "it holds "+strings.Join(holds, ", ")+", taken out before it was installed, and not its revert")
	}
	if len(unread) > 0 {
		why = append(why, "Crew could not read whether it holds "+strings.Join(unread, ", ")+", taken out before it was installed")
	}
	return strings.Join(why, "; "), nil
}

// holdTakenOut holds a release that waits, for an improvement taken out, and records the hold.
func (s *Service) holdTakenOut(ctx context.Context, version, why string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE releases SET held = true, held_why = $2 WHERE version = $1 AND state = 'waiting' AND NOT held`, version, why)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return db.RecordChange(ctx, tx, "crew", "releases", version, map[string]any{"held": false}, map[string]any{"held": true, "heldWhy": why})
	})
}

// HoldTakenOut holds every release that waits and holds an improvement the operator took out, as its
// notice would now (16.3, since 0.55): the press of Take it out reaches the releases made before it.
func (s *Service) HoldTakenOut(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT version, commit FROM releases WHERE state = 'waiting' AND NOT held`)
	if err != nil {
		return err
	}
	type rel struct{ version, commit string }
	var list []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.version, &r.commit); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		why, err := s.TakenOutHold(ctx, r.commit)
		if err != nil {
			return err
		}
		if why == "" {
			continue
		}
		if err := s.holdTakenOut(ctx, r.version, why); err != nil {
			return err
		}
	}
	return nil
}

// Install asks the updater for a version for the operator: Install now, Install, Update, or Roll
// back (16.3, 16.5). It lifts a hold.
func (s *Service) Install(ctx context.Context, version, confirm string) (*updater.Request, error) {
	req, err := s.Updater.Install(ctx, updater.Ask{Version: version, AskedBy: ByOperator, Confirm: confirm})
	if err != nil {
		return req, err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE releases SET held = false, held_why = NULL, state = 'installing' WHERE version = $1`, version)
	return req, err
}

// ErrNotWaiting is a hold of a release that does not wait.
var ErrNotWaiting = errors.New("only a release that waits to be installed can be held")

// Hold keeps a release of text from installing by itself until Install (16.3); held is false to lift
// the hold, which starts no clock again: a lifted hold waits for Install, as code does.
func (s *Service) Hold(ctx context.Context, version string, held bool) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var state string
		var was bool
		if err := tx.QueryRow(ctx, `SELECT state, held FROM releases WHERE version = $1 FOR UPDATE`, version).Scan(&state, &was); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotWaiting
			}
			return err
		}
		if state != "waiting" && state != "failed" {
			return ErrNotWaiting
		}
		why := "held by you"
		if !held {
			why = ""
		}
		if _, err := tx.Exec(ctx, `UPDATE releases SET held = $2, held_why = nullif($3, ''),
				install_after = CASE WHEN $2 THEN install_after ELSE NULL END WHERE version = $1`, version, held, why); err != nil {
			return err
		}
		return db.RecordChange(ctx, tx, ByOperator, "releases", version, map[string]any{"held": was}, map[string]any{"held": held})
	})
}

// Clock installs the next release of text when its notice has passed (16.3, step 3): not while one
// is held, a request runs, or Crew is paused.
func (s *Service) Clock(ctx context.Context, st updater.Status) error {
	if st.Current != nil || st.Checking != "" {
		return nil
	}
	if h, err := hold.Read(ctx, s.Pool); err != nil || h.PausedAt != nil {
		return err
	}
	// One at a time with Take it out of an improvement, which takes the same lock (16.3, since 0.55): a
	// take-out is done before the clock reads the hold, or waits until the release is installing (round
	// 2 of the security review of 1863a0e..f8d57b2).
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('crew.releases'))`); err != nil {
			return err
		}
		var v, commit string
		var installWhenDone bool
		err := tx.QueryRow(ctx, `SELECT r.version, r.commit,
				EXISTS (SELECT 1 FROM gates g WHERE g.version = r.version AND g.install_when_done)
			FROM releases r
			WHERE r.state = 'waiting' AND r.gate IS NOT NULL AND r.replaced_by IS NULL
			  AND NOT r.held
			  AND (
				  (r.install_after IS NOT NULL AND r.install_after <= $1)
				  OR EXISTS (SELECT 1 FROM gates g WHERE g.version = r.version AND g.install_when_done)
			  )
			ORDER BY coalesce(r.install_after, $1) LIMIT 1`, s.now()).Scan(&v, &commit, &installWhenDone)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// The hold read again for this release: an improvement taken out since the round held what
		// waited is not installed with it (16.3, since 0.55; security review of 1863a0e..f8d57b2).
		why, err := s.TakenOutHold(ctx, commit)
		if err != nil {
			return err
		}
		if why != "" {
			return s.holdTakenOut(ctx, v, why)
		}
		by := ByClock
		if installWhenDone {
			by = ByOperator
			if _, err := tx.Exec(ctx, `UPDATE gates SET install_when_done = false WHERE version = $1`, v); err != nil {
				return err
			}
		}
		_, err = s.Updater.Install(ctx, updater.Ask{Version: v, AskedBy: by})
		var refused *updater.Refusal
		switch {
		case errors.As(err, &refused):
			// Nothing was replaced: the release is held, and the inbox says why (21).
			return s.failed(ctx, v, by, refused.Reason)
		case err != nil:
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE releases SET state = 'installing' WHERE version = $1`, v)
		return err
	})
}

// failed records a request for v that did not install it. One the clock asked for is held.
func (s *Service) failed(ctx context.Context, v, by, why string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return failedTx(ctx, tx, v, by, why) })
}

// failedTx records the failure on a release that waits or was being installed: one that runs or is
// kept since is not touched by a failure the table had forgotten.
func failedTx(ctx context.Context, tx pgx.Tx, v, by, why string) error {
	held := by == ByClock
	heldWhy := ""
	if held {
		heldWhy = "it was installing by itself, and the check stopped it: " + why
	}
	tag, err := tx.Exec(ctx, `UPDATE releases SET state = 'failed', failure = $2, held = held OR $3,
			held_why = CASE WHEN $3 THEN $4 ELSE held_why END WHERE version = $1 AND state IN ('waiting', 'installing', 'failed')`, v, why, held, heldWhy)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return db.RecordChange(ctx, tx, "crew", "releases", v, nil, map[string]any{"state": "failed", "failure": why, "held": held})
}

// Follow copies the updater's requests into update_requests, and acts once on each that ended:
// the release that runs, the one that failed and why, and the news of it (16.3, step 4).
func (s *Service) Follow(ctx context.Context, st updater.Status) error {
	reqs := append([]*updater.Request{}, st.Requests...)
	if st.Current != nil {
		reqs = append(reqs, st.Current)
	}
	for _, r := range reqs {
		steps, err := json.Marshal(r.Steps)
		if err != nil {
			return err
		}
		var was *string
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT state FROM update_requests WHERE id = $1 FOR UPDATE`, r.ID).Scan(&was); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO update_requests (id, version, from_version, action, asked_by, state, step, steps, error, asked_at, ended_at)
				VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, nullif($9, ''), $10, $11)
				ON CONFLICT (id) DO UPDATE SET state = $6, step = $7, steps = $8, error = nullif($9, ''), ended_at = $11`,
				r.ID, r.Version, r.From, actionOf(r), r.AskedBy, r.State, r.Step, steps, r.Error, r.AskedAt, r.EndedAt); err != nil {
				return err
			}
			ended := r.State != updater.StateRunning && r.State != updater.StateAsked
			// A gate's request installs nothing: its result is the release's gate.json.
			if !ended || (was != nil && *was == r.State) || r.Action == updater.ActionCheck || r.Action == updater.ActionGate {
				return nil
			}
			switch r.State {
			case updater.StateDone:
				// The news is this server's own: a request that installed another version is history
				// that a restore of an older snapshot made the table forget.
				if r.Version != s.Running {
					return nil
				}
				// Once per request: the history names the request whose news it made.
				var told bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM changes WHERE tbl = 'releases' AND row_key = $1
					AND after->>'request' = $2)`, r.Version, fmt.Sprint(r.ID)).Scan(&told); err != nil || told {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'running', installed_at = $2, failure = NULL, held = false WHERE version = $1`, r.Version, s.now()); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE releases SET state = 'kept' WHERE state = 'running' AND version <> $1`, r.Version); err != nil {
					return err
				}
				return db.RecordChange(ctx, tx, "updater", "releases", r.Version, map[string]any{"version": r.From},
					map[string]any{"state": "running", "action": r.Action, "request": fmt.Sprint(r.ID), "news": fmt.Sprintf("Crew is now on %s", r.Version)})
			default:
				// A request that did not install its version left the running one as it was; it is
				// told, and held when the clock asked, in the transaction that copies it, so it is
				// acted on once.
				return failedTx(ctx, tx, r.Version, r.AskedBy, r.Error)
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func actionOf(r *updater.Request) string {
	if r.Action == "" {
		return updater.ActionInstall
	}
	return r.Action
}

// replaceUpdater installs the updater of this version once the server runs on it and no request
// runs: the server replaces the updater, the updater never itself (16.8). One try per version.
func (s *Service) replaceUpdater(ctx context.Context, st updater.Status) error {
	if s.Replacer == nil || st.Current != nil || st.Checking != "" || st.State.Running != s.Running || st.State.Updater == s.Running || st.State.Updater == "" {
		return nil
	}
	f, err := release.Read(filepath.Join(s.Releases, s.Running))
	if err != nil || f.Gate == nil && !f.Manifest.ByHand {
		// A build from a checkout is installed by crew-ctl, which starts its updater itself.
		return nil
	}
	var tried bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM changes WHERE tbl = 'updater' AND row_key = $1)`, s.Running).Scan(&tried); err != nil || tried {
		return err
	}
	rerr := s.Replacer.ReplaceUpdater(ctx, s.Running)
	result := map[string]any{"from": st.State.Updater, "to": s.Running}
	if rerr != nil {
		result["error"] = rerr.Error()
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return db.RecordChange(ctx, tx, "crew", "updater", s.Running, nil, result)
	})
}
