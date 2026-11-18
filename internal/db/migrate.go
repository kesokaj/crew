package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The schema changes in numbered steps (15.3). A step is applied once, in one transaction with its
// row in schema_steps, so a step that fails changes nothing. At an update's switch every step the
// release adds is applied in one transaction (16.4).
//
//go:embed migrations/*.sql
var migrations embed.FS

var stepName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// olderReads is the line by which a step says whether the version before it can still read the
// database after it (16.6, since 0.48): yes for a step that only adds, no for one that changes a form
// the version before reads.
var olderReads = regexp.MustCompile(`(?m)^-- older versions read: (yes|no)\s*$`)

// FirstStepThatSays is the first step that must carry the line. The steps before it were applied
// before there were releases, on the one installation.
const FirstStepThatSays = 51

// Step is one numbered change of the schema.
type Step struct {
	Number int
	Name   string
	SQL    string
	// OlderReads is whether the version before this step reads the database after it.
	OlderReads bool
}

// Steps returns every step in Crew's source, in order. Numbers are unique and start at 1 with no gap,
// so a step cannot be left out by mistake.
func Steps() ([]Step, error) { return StepsFS(migrations, "migrations") }

// StepsFS is Steps of the folder dir of fsys: the steps of another commit's tree, which the running
// release reads to write that release's release.json (16.6).
func StepsFS(fsys fs.FS, dir string) ([]Step, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var steps []Step
	for _, e := range entries {
		m := stepName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrations/%s: not named NNNN_words.sql", e.Name())
		}
		n, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		step := Step{Number: n, Name: e.Name(), SQL: string(body), OlderReads: true}
		if m := olderReads.FindAllStringSubmatch(step.SQL, -1); len(m) == 1 {
			step.OlderReads = m[0][1] == "yes"
		} else if n >= FirstStepThatSays {
			return nil, fmt.Errorf("migrations/%s: says %d times, not once, whether older versions read it (-- older versions read: yes|no)", e.Name(), len(m))
		}
		steps = append(steps, step)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Number < steps[j].Number })
	for i, s := range steps {
		if s.Number != i+1 {
			return nil, fmt.Errorf("migrations: step %d is %s; the numbers must run 1, 2, 3 with no gap", i+1, s.Name)
		}
	}
	return steps, nil
}

// ReadBy is the lowest schema whose server reads a database at the last of steps: the last step that
// the version before it cannot read, or 0 when every version reads it (16.6). A roll back to a
// release whose schema is below it restores the snapshot.
func ReadBy(steps []Step) int {
	n := 0
	for _, s := range steps {
		if !s.OlderReads {
			n = s.Number
		}
	}
	return n
}

// Migrate applies every step that is not applied yet, as the role that may change the schema, and
// returns the names it applied. It holds a lock for the length of the run, so two at once cannot
// apply a step twice.
func Migrate(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	steps, err := Steps()
	if err != nil {
		return nil, err
	}
	return migrate(ctx, conn, steps, false)
}

// MigrateAtOnce applies every step that is not applied yet in one transaction: they all apply, or
// none does. It is the switch of an update (16.4), after the same steps were tried on a copy.
func MigrateAtOnce(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	steps, err := Steps()
	if err != nil {
		return nil, err
	}
	return migrate(ctx, conn, steps, true)
}

// stepper is a connection or a transaction, on which a step is applied.
type stepper interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

func migrate(ctx context.Context, conn *pgx.Conn, steps []Step, atOnce bool) ([]string, error) {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_steps (
		number     integer PRIMARY KEY,
		name       text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("schema_steps: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('crew.migrate'))`); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('crew.migrate'))`) //nolint:errcheck
	var on stepper = conn
	var whole pgx.Tx
	if atOnce {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after the commit
		on, whole = tx, tx
	}
	var applied []string
	for _, s := range steps {
		var done bool
		if err := on.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_steps WHERE number = $1)`, s.Number).Scan(&done); err != nil {
			if atOnce {
				return nil, err
			}
			return applied, err
		}
		if done {
			continue
		}
		// Within the switch's transaction each step is a nested one, a savepoint, so the error
		// names the step; a failed one still undoes them all.
		err := pgx.BeginFunc(ctx, on, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, s.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_steps (number, name) VALUES ($1, $2)`, s.Number, s.Name)
			return err
		})
		if err != nil {
			if atOnce {
				return nil, fmt.Errorf("step %s: %w; no step was applied", s.Name, err)
			}
			return applied, fmt.Errorf("step %s: %w", s.Name, err)
		}
		applied = append(applied, s.Name)
	}
	if _, err := on.Exec(ctx, `SELECT crew_apply_grants()`); err != nil {
		if atOnce {
			return nil, fmt.Errorf("crew_apply_grants: %w; no step was applied", err)
		}
		return applied, fmt.Errorf("crew_apply_grants: %w", err)
	}
	if whole != nil {
		if err := whole.Commit(ctx); err != nil {
			return nil, err
		}
	}
	return applied, nil
}

// Applied returns the number of the last step applied, 0 for an empty database.
func Applied(ctx context.Context, q Querier) (int, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('schema_steps') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT coalesce(max(number), 0) FROM schema_steps`).Scan(&n)
	return n, err
}
