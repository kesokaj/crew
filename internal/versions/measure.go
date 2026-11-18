package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/settings"
)

// NotCompared is the verdict of a release Crew knows no release before: the machine's first install,
// or history a restore of an older snapshot forgot.
const NotCompared = "not_compared"

// The margins of a release's measure (the operator's answer 5 of the thirteenth session): a share is
// worse past 10 points, a number per ticket past 20% of its value before. A release has no evals to
// take a margin from, as an improvement does.
const (
	shareMargin     = 0.10
	perTicketMargin = 0.20
)

// ReleaseMeasure is a release compared with the release it replaced at its first install, on the
// tickets done wholly on each (10.7, since 0.57). Results has the six metrics of the tickets, each with
// its numbers before and after; Why says what decided the verdict, in Crew's words.
type ReleaseMeasure struct {
	Before        string           `json:"before,omitempty"`
	Verdict       string           `json:"verdict"`
	Why           string           `json:"why"`
	TicketsBefore int              `json:"ticketsBefore"`
	TicketsAfter  int              `json:"ticketsAfter"`
	Window        int              `json:"window"`
	Results       []measure.Result `json:"results,omitempty"`
}

// Measure measures each release that was installed and has no measure yet, once (10.7, since 0.57):
// against the release its first install replaced, when 20 tickets (improvements.measure_after_tasks)
// were done wholly on it, or with fewer once it no longer runs. A worse release is news, which
// Activity reads from its row; nothing here rolls back, holds, or asks the updater for anything: only
// an improvement is taken out by Crew (10.5).
func (s *Service) Measure(ctx context.Context) error {
	n := 20
	if s.Settings != nil {
		d, err := s.Settings(ctx)
		if err != nil {
			return err
		}
		n = measureAfter(d)
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.version, r.state = 'running',
			(SELECT q.from_version FROM update_requests q WHERE q.version = r.version AND q.action = 'install' AND q.state = 'done'
				ORDER BY q.id LIMIT 1)
		FROM releases r WHERE r.installed_at IS NOT NULL AND r.measured_at IS NULL ORDER BY r.installed_at`)
	if err != nil {
		return err
	}
	type todo struct {
		version string
		running bool
		before  *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (todo, error) {
		var t todo
		return t, r.Scan(&t.version, &t.running, &t.before)
	})
	if err != nil {
		return err
	}
	for _, t := range list {
		m, err := s.measureOne(ctx, t.version, t.running, t.before, n)
		if err != nil || m == nil {
			if err != nil {
				return fmt.Errorf("measuring %s: %w", t.version, err)
			}
			continue
		}
		body, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE releases SET measure = $2, measured_at = now() WHERE version = $1 AND measured_at IS NULL`,
			t.version, body); err != nil {
			return err
		}
	}
	return nil
}

// measureOne is a release's measure, or nil while it runs with fewer than n tickets on it, or with no
// release known before it: its install's request is done only after the updater watched it, and the
// new server's first pass comes before that (0.2.26 on dave was stored as "not compared" so).
func (s *Service) measureOne(ctx context.Context, version string, running bool, before *string, n int) (*ReleaseMeasure, error) {
	if before == nil || *before == "" || *before == version {
		if running {
			return nil, nil
		}
		return &ReleaseMeasure{Verdict: NotCompared, Why: "Crew does not know what ran before it", Window: n}, nil
	}
	from, to, err := measure.AroundVersions(ctx, s.Pool, *before, version, n)
	if err != nil {
		return nil, err
	}
	if len(to) < n && running {
		return nil, nil
	}
	m := &ReleaseMeasure{Before: *before, TicketsBefore: min(len(from), n), TicketsAfter: min(len(to), n), Window: n}
	var worse []string
	better := false
	for _, metric := range measure.Metrics {
		if metric.Name == measure.RewritesUsed {
			continue
		}
		margin := perTicketMargin
		if metric.Rate {
			margin = shareMargin
		}
		r := measure.Compare(metric, from, to, n, margin)
		m.Results = append(m.Results, r)
		switch r.Verdict {
		case measure.Worse:
			worse = append(worse, r.Why)
		case measure.Better:
			better = true
		}
	}
	switch {
	case m.TicketsBefore < n || m.TicketsAfter < n:
		m.Verdict = measure.Inconclusive
		m.Why = fmt.Sprintf("too few tickets against %s: %d on it and %d on %s, of %d each", *before, m.TicketsAfter, m.TicketsBefore, *before, n)
	case len(worse) > 0:
		m.Verdict, m.Why = measure.Worse, strings.Join(worse, "; ")
	case better:
		m.Verdict, m.Why = measure.Better, fmt.Sprintf("better than %s over %d tickets each", *before, n)
	default:
		m.Verdict, m.Why = measure.Same, fmt.Sprintf("the same as %s over %d tickets each", *before, n)
	}
	return m, nil
}

// measureAfter is improvements.measure_after_tasks, the tickets an improvement and a release are
// measured over: 20 when it is not set.
func measureAfter(d settings.Doc) int {
	imp, _ := d["improvements"].(settings.Doc)
	switch v := imp["measure_after_tasks"].(type) {
	case int:
		if v >= 1 {
			return v
		}
	case float64:
		if v >= 1 {
			return int(v)
		}
	}
	return 20
}
