package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// RunLimits gives the run driver a subtask's watchdog numbers from its project's settings (8.2). A
// subtask of no project keeps the design's numbers. No limit is in dollars (7.7, since 0.53).
func (p *Pipeline) RunLimits(ctx context.Context, subtask int64) (runs.Limits, error) {
	l := runs.DefaultLimits()
	var project, name, key string
	err := p.Pool.QueryRow(ctx, `SELECT p.id::text, p.name, p.key FROM subtasks s JOIN projects p ON p.id = s.project WHERE s.id = $1`,
		subtask).Scan(&project, &name, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	doc, err := settings.EffectiveProject(ctx, p.Pool, project, name, key)
	if err != nil {
		return l, err
	}
	minutes := func(path string, def time.Duration) time.Duration {
		if v := num(doc, path, 0); v > 0 {
			return time.Duration(v * float64(time.Minute))
		}
		return def
	}
	count := func(path string, def int) int {
		if v := int(num(doc, path, 0)); v > 0 {
			return v
		}
		return def
	}
	l.SilentWarn = minutes("watchdog.silent_warn_minutes", l.SilentWarn)
	l.SilentStop = minutes("watchdog.silent_stop_minutes", l.SilentStop)
	l.FailWarn = count("watchdog.repeat_fail_warn", l.FailWarn)
	l.FailStop = count("watchdog.repeat_fail_stop", l.FailStop)
	return l, nil
}
