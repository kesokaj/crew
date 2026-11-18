// Package models keeps the models each agent CLI offers and chooses the CLI, the model, and the
// effort of every run (design 12.3), with the quota guard's state of each group of models (12.7).
// What differs between the CLIs is asked of their adapters.
package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/settings"
)

// ValidName is a name a CLI can be given as its model: letters, digits, and . _ : - [ ], beginning
// with a letter or a digit, so it never reads as a flag, such as claude-opus-5-5 or opus[1m].
var ValidName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,79}$`)

// FailuresForNews is how many reads of a CLI's list fail in a row before the operator gets news (12.3).
const FailuresForNews = 2

// Record keeps the list a CLI gave, as it gave it (12.3). A model it no longer lists stays, marked as
// gone, so old runs still show what they used; a name added by the operator is never marked. A read
// that failed keeps the last list and counts the failure.
func Record(ctx context.Context, pool *pgxpool.Pool, name string, list []cli.Model, readErr string) error {
	a, ok := cli.Get(name)
	if !ok {
		return fmt.Errorf("no CLI %q", name)
	}
	// A name of another shape is not kept: it would reach a command line (security review of M8).
	var kept []cli.Model
	for _, m := range list {
		if ValidName.MatchString(m.Name) && (m.Model == "" || ValidName.MatchString(m.Model)) {
			kept = append(kept, m)
		}
	}
	if len(list) > 0 && len(kept) == 0 {
		readErr = "the list held no name of a shape Crew takes"
	}
	list = kept
	hadPriorRead := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if len(list) == 0 {
			if readErr == "" {
				return nil
			}
			_, err := tx.Exec(ctx, `INSERT INTO cli_logins AS c (cli, models_failures, models_error, models_failed_at) VALUES ($1, 1, $2, now())
				ON CONFLICT (cli) DO UPDATE SET models_failures = c.models_failures + 1, models_error = $2, models_failed_at = now()`, name, readErr)
			return err
		}
		var prevRead *time.Time
		if err := tx.QueryRow(ctx, `SELECT models_read_at FROM cli_logins WHERE cli = $1`, name).Scan(&prevRead); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		hadPriorRead = prevRead != nil
		names := make([]string, len(list))
		for i, m := range list {
			names[i] = m.Name
		}
		if _, err := tx.Exec(ctx, `UPDATE models SET offered = false, gone_at = coalesce(gone_at, now())
			WHERE cli = $1 AND added_by = 'cli' AND NOT (name = ANY($2))`, name, names); err != nil {
			return err
		}
		for _, m := range list {
			efforts := m.Efforts
			if efforts == nil {
				efforts = []string{}
			}
			model := m.Model
			if model == "" {
				model = m.Name
			}
			if _, err := tx.Exec(ctx, `INSERT INTO models (cli, name, model, efforts, in_name, display, resolved, family, limit_group)
				VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, $9)
				ON CONFLICT (cli, name) DO UPDATE SET model = $3, efforts = $4, in_name = $5, display = nullif($6, ''),
					resolved = nullif($7, ''), family = $8, limit_group = $9, offered = true, gone_at = NULL, read_at = now()`,
				name, m.Name, model, efforts, m.InName, m.Display, m.Resolved, cli.Family(model), a.LimitGroup(model)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO cli_logins AS c (cli, models_read_at) VALUES ($1, now())
			ON CONFLICT (cli) DO UPDATE SET models_read_at = now(), models_failures = 0, models_error = NULL`, name)
		return err
	})
	if err != nil || readErr != "" || len(list) == 0 {
		return err
	}
	_, err = syncModels(ctx, pool, settings.Machine{}, "crew", hadPriorRead)
	return err
}

// Refused records that the CLI refused a name in a run (12.3, 21). The name is passed over for
// RetryAfter, then tried again: a run's report is its own account (12.4), so it never takes a model
// away from every project for good. The operator's Refresh offers it again at once.
func Refused(ctx context.Context, q Execer, cliName, name string) error {
	_, err := q.Exec(ctx, `UPDATE models SET refused = true, refused_at = now() WHERE cli = $1 AND name = $2`, cliName, name)
	return err
}

// ClearRefused offers again at once the names a CLI refused in a run: when the operator reads the
// lists again (12.3).
func ClearRefused(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `UPDATE models SET refused = false, refused_at = NULL WHERE refused`)
	return err
}

// Add keeps a name the operator added beside the ones a CLI lists, such as a full name to stay on
// one version (12.3, agents.extra_models). It is shown as added by the operator, and never marked
// gone by a read of the list.
func Add(ctx context.Context, pool *pgxpool.Pool, name, added string) error {
	a, ok := cli.Get(name)
	if !ok {
		return fmt.Errorf("no CLI %q", name)
	}
	m := a.Named(added)
	if m.Efforts == nil {
		m.Efforts = []string{}
	}
	_, err := pool.Exec(ctx, `INSERT INTO models (cli, name, model, efforts, in_name, family, limit_group, added_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'operator')
		ON CONFLICT (cli, name) DO UPDATE SET offered = true, refused = false, gone_at = NULL, added_by = 'operator'`,
		name, m.Name, m.Model, m.Efforts, m.InName, cli.Family(m.Model), a.LimitGroup(m.Model))
	return err
}

// Row is one name a CLI offers, or offered, as it is stored.
type Row struct {
	CLI, Name, Model  string
	Efforts           []string
	InName            bool
	Display, Resolved string
	Family, Group     string
	Offered, Refused  bool
	AddedBy           string
	FirstSeen, ReadAt time.Time
	GoneAt, RefusedAt *time.Time
}

// PassedOver says the CLI refused the name in a run within RetryAfter: it is not offered until then.
func (r Row) PassedOver(now time.Time) bool {
	return r.Refused && r.RefusedAt != nil && now.Sub(*r.RefusedAt) < RetryAfter
}

// Rows are every name of every CLI, offered or not, in the order of their CLI, model, and name.
func Rows(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]Row, error) {
	rows, err := q.Query(ctx, `SELECT cli, name, model, efforts, in_name, coalesce(display, ''), coalesce(resolved, ''),
			coalesce(family, ''), coalesce(limit_group, ''), offered, refused, added_by, first_seen, read_at, gone_at, refused_at
		FROM models ORDER BY cli, model, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.CLI, &r.Name, &r.Model, &r.Efforts, &r.InName, &r.Display, &r.Resolved, &r.Family, &r.Group,
			&r.Offered, &r.Refused, &r.AddedBy, &r.FirstSeen, &r.ReadAt, &r.GoneAt, &r.RefusedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
