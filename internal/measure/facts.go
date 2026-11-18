package measure

import (
	"context"
	"time"

	"github.com/kesokaj/crew/internal/db"
)

// doneTickets are the Done tickets with when they were done, when their runs started, the versions they
// ran on, and their facts. The SQL that uses it filters and orders.
const doneTickets = `WITH d AS (
	SELECT t.id, t.project,
		(SELECT max(m.at) FROM stage_moves m WHERE m.ticket = t.id AND m.to_stage = 'done') AS done_at,
		(SELECT min(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id) AS first_run,
		(SELECT max(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id) AS last_run,
		(SELECT CASE WHEN count(DISTINCT r.crew_version) = 1 THEN min(r.crew_version) ELSE 'mixed' END
			FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id) AS version,
		NOT EXISTS (SELECT 1 FROM signals g WHERE g.ticket = t.id AND g.kind = 'check_round') AS first_request,
		(SELECT count(*) FROM signals g WHERE g.ticket = t.id AND g.kind = 'rework') AS fix_rounds,
		(SELECT count(*) FROM signals g WHERE g.ticket = t.id AND g.kind IN ('operator_intervention', 'merged_outside')) AS interventions,
		(SELECT count(*) FROM signals g WHERE g.ticket = t.id AND g.kind = 'escaped_defect') AS escaped,
		(SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id) AS tokens,
		EXISTS (SELECT 1 FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = t.id
			AND EXISTS (SELECT 1 FROM events e WHERE e.run = r.id AND e.type = 'status' AND e.payload->>'status' = 'ended' AND (e.payload->>'tokens_reported') = 'false')) AS no_tokens
	FROM tickets t WHERE t.stage = 'done')
SELECT first_request, first_request AND fix_rounds = 0, fix_rounds, interventions, escaped, tokens, no_tokens FROM d `

func facts(ctx context.Context, q db.Querier, where string, args ...any) ([]Facts, error) {
	rows, err := q.Query(ctx, doneTickets+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Facts
	for rows.Next() {
		var f Facts
		if err := rows.Scan(&f.FirstRequest, &f.PassedFirst, &f.FixRounds, &f.Interventions, &f.Escaped, &f.Tokens, &f.NoTokens); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AroundOverride are a project's tickets before an override was applied, every run of which started
// before it, the last n oldest first; and those after, every run of which started after it, the first n.
func AroundOverride(ctx context.Context, q db.Querier, project string, applied time.Time, n int) (before, after []Facts, err error) {
	before, err = facts(ctx, q, `WHERE project = $1 AND last_run < $2 ORDER BY done_at DESC LIMIT $3`, project, applied, n)
	if err != nil {
		return nil, nil, err
	}
	reverse(before)
	after, err = facts(ctx, q, `WHERE project = $1 AND first_run >= $2 ORDER BY done_at LIMIT $3`, project, applied, n)
	return before, after, err
}

// AroundVersions are the tickets of every project done on the version before, every run of which used
// it, the last n oldest first; and those on the version after, the first n (10.5, step 6). A hidden
// project's are not counted: the gate's crew-gate runs one on every release (16.2).
func AroundVersions(ctx context.Context, q db.Querier, from, to string, n int) (before, after []Facts, err error) {
	const shown = `project NOT IN (SELECT id FROM projects WHERE hidden)`
	before, err = facts(ctx, q, `WHERE version = $1 AND `+shown+` ORDER BY done_at DESC LIMIT $2`, from, n)
	if err != nil {
		return nil, nil, err
	}
	reverse(before)
	after, err = facts(ctx, q, `WHERE version = $1 AND `+shown+` ORDER BY done_at LIMIT $2`, to, n)
	return before, after, err
}

func reverse(fs []Facts) {
	for i, j := 0, len(fs)-1; i < j; i, j = i+1, j-1 {
		fs[i], fs[j] = fs[j], fs[i]
	}
}

// AroundRewrites are the operator's choices of rewrites of first messages made on the version before,
// the last n oldest first, and on the version after, the first n (7.2): a rewrite counts on the version
// its prompt writer's run used.
func AroundRewrites(ctx context.Context, q db.Querier, from, to string, n int) (before, after []Facts, err error) {
	read := func(v, order string) ([]Facts, error) {
		rows, err := q.Query(ctx, `SELECT w.choice = 'rewrite' FROM prompt_rewrites w JOIN runs r ON r.id = w.run
			WHERE w.kind = 'first' AND w.choice IN ('rewrite', 'edited', 'mine') AND r.crew_version = $1 ORDER BY w.chosen_at `+order+` LIMIT $2`, v, n)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Facts
		for rows.Next() {
			var used bool
			if err := rows.Scan(&used); err != nil {
				return nil, err
			}
			out = append(out, Facts{UsedAsIs: used})
		}
		return out, rows.Err()
	}
	if before, err = read(from, "DESC"); err != nil {
		return nil, nil, err
	}
	reverse(before)
	after, err = read(to, "ASC")
	return before, after, err
}
