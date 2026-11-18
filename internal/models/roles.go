package models

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/settings"
)

// FillRoles seeds the machine's agents.roles from the baseline and syncs offered models into
// agents.brackets and agents.roles (12.3): a new agent gets the baseline's list and effort, an
// existing agent gets unseen baseline entries that were not removed by the operator, and syncModels
// ranks automatic roles by (family, bracket) while preserving Set by you, operator edits, and active
// Chooser decisions. It reports whether it changed the settings.
func FillRoles(ctx context.Context, pool *pgxpool.Pool, m settings.Machine, who string) (bool, error) {
	base, err := baseline.Roles()
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT role || ' ' || entry FROM baseline_entries`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			seen[k] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	names := make([]string, 0, len(base))
	for n := range base {
		names = append(names, n)
	}
	slices.Sort(names)
	changed := false
	hadStoredRoles := false
	// Under the settings' lock, from the settings as they are stored: an agent they do not have yet
	// gets the baseline's whole entry.
	err = settings.Update(ctx, pool, m, who, func(doc settings.Doc) error {
		own, err := settings.Load(ctx, pool)
		if err != nil {
			return err
		}
		stored, _ := dig(own, "agents", "roles").(settings.Doc)
		hadStoredRoles = len(stored) > 0
		roles := doc["agents"].(settings.Doc)["roles"].(settings.Doc)
		for _, name := range names {
			b := base[name]
			entry, ok := stored[name].(settings.Doc)
			if !ok {
				list := make([]any, len(b.Model))
				for i, e := range b.Model {
					list[i] = e
				}
				roles[name] = settings.Doc{"model": list, "effort": b.Effort}
				changed = true
				continue
			}
			list := strs(entry["model"])
			for _, e := range b.Model {
				if !seen[name+" "+e] && !slices.Contains(list, e) {
					list = append(list, e)
					changed = true
				}
			}
			out := make([]any, len(list))
			for i, e := range list {
				out[i] = e
			}
			effort := str(entry["effort"])
			if effort == "" {
				effort, changed = b.Effort, true
			}
			roles[name] = settings.Doc{"model": out, "effort": effort}
		}
		if !changed {
			return errUnchanged
		}
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		return false, err
	}
	for _, name := range names {
		for _, e := range base[name].Model {
			if _, err := pool.Exec(ctx, `INSERT INTO baseline_entries (role, entry) VALUES ($1, $2) ON CONFLICT DO NOTHING`, name, e); err != nil {
				return changed, err
			}
		}
	}
	synced, err := syncModels(ctx, pool, m, who, hadStoredRoles)
	return changed || synced, err
}

var errUnchanged = errors.New("nothing to fill")
