package hosting

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Recover is run once at the server's start, after the sandboxes were reconciled (plan M19 D29): a
// deploy that ran is failed, "interrupted by a restart of Crew", with its sandbox and its build's
// folder; a sandbox still to retire goes; a project with nothing live is stopped. The live sandboxes
// were adopted with every other; the ingress is joined to them by Rejoin. A live sandbox whose
// microVM is gone is started again from its deploy's image.
func (s *Service) Recover(ctx context.Context) error {
	// The sandboxes that do not serve go first: a cut-off deploy's new app may still run, and must not
	// write to a data folder a roll back puts back (review of M19, round 2).
	rows, err := s.Pool.Query(ctx, `SELECT x.id FROM sandboxes x LEFT JOIN hosting h ON h.project = x.project
		WHERE x.kind = 'hosted' AND x.removed_at IS NULL AND (h.sandbox IS NULL OR x.id <> h.sandbox)`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		s.removeSandbox(ctx, id)
	}
	if err := s.recoverRollBacks(ctx); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET state = 'failed', detail = 'interrupted by a restart of Crew', ended_at = now()
		WHERE state IN ('building', 'starting', 'checking')`); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET retiring = NULL WHERE retiring IS NOT NULL`); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE hosting SET state = 'stopped', updated_at = now() WHERE state = 'deploying' AND live_deploy IS NULL`); err != nil {
		return err
	}
	if s.Builds != "" {
		if entries, err := os.ReadDir(s.Builds); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "host-") {
					_ = os.RemoveAll(filepath.Join(s.Builds, e.Name()))
				}
			}
		}
	}
	// A live sandbox Reconcile found gone: its row is closed. It comes back as a settings deploy of its
	// own image, as a change of settings would start it.
	gone, err := s.Pool.Query(ctx, `SELECT h.project, d.number FROM hosting h JOIN deploys d ON d.id = h.live_deploy
		JOIN sandboxes x ON x.id = h.sandbox WHERE h.state IN ('live', 'failing') AND x.removed_at IS NOT NULL`)
	if err != nil {
		return err
	}
	type back struct {
		project string
		number  int
	}
	var again []back
	for gone.Next() {
		var b back
		if err := gone.Scan(&b.project, &b.number); err != nil {
			gone.Close()
			return err
		}
		again = append(again, b)
	}
	gone.Close()
	for _, b := range again {
		if _, err := s.Deploy(ctx, b.project, Ask{Why: "settings", By: "settings", From: b.number}); err != nil {
			s.Log.Warn("starting again a hosted project whose microVM is gone", "project", b.project, "error", err)
		}
	}
	return gone.Err()
}

// recoverRollBacks finishes a roll back with data that a restart cut off after it stopped what served
// (review of M19, finding 5): the data it copied first is put back, and what served starts again, as a
// roll back that fails does. A roll back with data names the copy it puts back before it stops what
// serves; one without data is never taken here (round 2).
func (s *Service) recoverRollBacks(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT d.id, d.project, d.data_copy, coalesce(h.sandbox, ''), coalesce(l.number, 0) FROM deploys d
		JOIN hosting h ON h.project = d.project LEFT JOIN deploys l ON l.id = h.live_deploy
		WHERE d.state IN ('building', 'starting', 'checking') AND d.why = 'rollback' AND d.data_copy IS NOT NULL
		AND d.restored IS NOT NULL`)
	if err != nil {
		return err
	}
	type cut struct {
		id                     int64
		project, copy, sandbox string
		live                   int
	}
	var all []cut
	for rows.Next() {
		var c cut
		if err := rows.Scan(&c.id, &c.project, &c.copy, &c.sandbox, &c.live); err != nil {
			rows.Close()
			return err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range all {
		if c.sandbox == "" {
			continue
		}
		if runs, err := s.Runtime.HostedRunning(ctx, c.sandbox); err != nil || runs {
			continue
		}
		back := []string{"interrupted by a restart of Crew"}
		if err := replaceData(s.dataDir(c.project), s.copyDir(c.project, c.copy)); err != nil {
			back = append(back, "the data could not be put back: "+err.Error())
		} else {
			back = append(back, "the data was put back")
		}
		if err := s.Runtime.StartAgain(ctx, c.sandbox); err != nil {
			back = append(back, fmt.Sprintf("deploy %d could not be started again: %v", c.live, err))
		} else {
			back = append(back, fmt.Sprintf("deploy %d serves again", c.live))
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE deploys SET state = 'failed', detail = $2, ended_at = now() WHERE id = $1`, c.id, strings.Join(back, "; ")); err != nil {
			return err
		}
	}
	return nil
}
