package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// MaxAge is how long computed numbers are kept in metrics before they are computed again (since 0.49).
const MaxAge = 5 * time.Minute

// Cached returns the numbers kept under key when they are younger than MaxAge, and otherwise computes
// them and keeps them. project names the project they belong to, or is "" for the Crew level. A
// failure to keep them is no failure to answer.
//
// The numbers are kept under the Crew version that computed them, and keeping them forgets those of
// every other version: right after an install, and in the gate's copy of the database, the kept
// numbers are the version before's, computed by its code and in its shape.
func Cached[T any](ctx context.Context, q db.Querier, crewVersion, key, project string, now time.Time, compute func() (*T, error)) (*T, error) {
	key = crewVersion + "/" + key
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM metrics WHERE key = $1 AND computed_at > $2`, key, now.Add(-MaxAge)).Scan(&raw)
	if err == nil {
		var v T
		if json.Unmarshal(raw, &v) == nil {
			return &v, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	v, err := compute()
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(v); err == nil {
		q.Exec(ctx, `DELETE FROM metrics WHERE left(key, length($1) + 1) <> $1 || '/'`, crewVersion)
		q.Exec(ctx, `INSERT INTO metrics (key, project, value, computed_at) VALUES ($1, nullif($2, '')::uuid, $3, $4)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, computed_at = EXCLUDED.computed_at`, key, project, b, now)
	}
	return v, nil
}

// Forget removes the kept numbers, so the next read computes them again.
func Forget(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `DELETE FROM metrics`)
	return err
}

// ProjectCacheKey and CrewKey are the keys of the kept numbers.
func ProjectCacheKey(project string) string { return "project:" + project }

const CrewKey = "crew"
