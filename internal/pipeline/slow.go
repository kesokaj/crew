package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kesokaj/crew/internal/board"
)

// minStays is how many past stays in a stage make its usual time: fewer say nothing yet.
const minStays = 3

// slowStages sends news of a ticket that has been in its stage watchdog.slow_stage_factor times the
// usual time of that stage in its project, the median of its past stays there (8.2). It stops
// nothing; each stay is told once, and counted as a signal.
func (p *Pipeline) slowStages(ctx context.Context, j *job, now time.Time) error {
	factor := num(j.doc, "watchdog.slow_stage_factor", 3)
	if factor <= 0 {
		return nil
	}
	rows, err := p.Pool.Query(ctx, `WITH m AS (
			SELECT sm.from_stage AS stage, sm.at - lag(sm.at) OVER (PARTITION BY sm.ticket ORDER BY sm.id) AS stay
			FROM stage_moves sm JOIN tickets t ON t.id = sm.ticket WHERE t.project = $1),
		usual AS (SELECT stage, percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM stay)) AS secs
			FROM m WHERE stay IS NOT NULL GROUP BY stage HAVING count(*) >= $2)
		SELECT t.id, p.key || '-' || t.number, t.stage, t.stage_at, u.secs FROM tickets t
		JOIN projects p ON p.id = t.project JOIN usual u ON u.stage = t.stage
		WHERE t.project = $1 AND t.stage NOT IN ('planning', 'backlog', 'done', 'closed') AND u.secs > 0
		  AND t.stage_at < $3::timestamptz - make_interval(secs => u.secs * $4)
		  AND NOT EXISTS (SELECT 1 FROM signals g WHERE g.ticket = t.id AND g.kind = 'slow_stage'
			AND g.detail->>'stage' = t.stage AND (g.detail->>'since')::timestamptz = t.stage_at)`,
		j.proj.ID, minStays, now, factor)
	if err != nil {
		return err
	}
	type slow struct {
		id         int64
		key, stage string
		since      time.Time
		usual      float64
	}
	var list []slow
	for rows.Next() {
		var s slow
		if err := rows.Scan(&s.id, &s.key, &s.stage, &s.since, &s.usual); err != nil {
			rows.Close()
			return err
		}
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range list {
		in := now.Sub(s.since)
		usual := time.Duration(s.usual * float64(time.Second))
		text := fmt.Sprintf("%s has been in %s for %s, %.0f times its usual %s there", s.key, board.StageName(s.stage),
			in.Round(time.Minute), in.Seconds()/s.usual, usual.Round(time.Minute))
		detail, _ := json.Marshal(map[string]any{"stage": s.stage, "since": s.since, "usualSeconds": s.usual, "text": text})
		if _, err := p.Pool.Exec(ctx, `INSERT INTO signals (project, ticket, kind, detail) VALUES ($1, $2, 'slow_stage', $3)`,
			j.proj.ID, s.id, detail); err != nil {
			return err
		}
	}
	return nil
}
