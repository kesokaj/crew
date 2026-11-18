package models

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
)

// Numbers are the results of one agent on one entry and effort over a window (12.3): a CLI, the
// model as it really ran (the full name an alias pointed at, so a moved alias starts again), and the
// effort. For the builder a unit is a ticket; for an agent that checks it is a run. They hold no
// project content (10.6).
type Numbers struct {
	Role   string `json:"role"`
	CLI    string `json:"cli"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Kind is the ticket's kind, or "" for every kind.
	Kind  string `json:"kind,omitempty"`
	Units int    `json:"units"`
	// Passed counts the builder's tickets whose command checks passed at its first request for a
	// move, in Crew's own check sandbox (13.9). Nothing counts as passed for an agent that checks.
	Passed int `json:"passed"`
	// AskedFix counts the runs of an agent that checks that asked for a fix.
	AskedFix int `json:"askedFix"`
	// Escaped counts the defects that escaped the agent: bugs that Analysis approved in a ticket it
	// passed, that ticket confirmed by the analysis of the bug's fix or named by the operator (12.3, from
	// M12; since 0.54).
	Escaped   int     `json:"escaped"`
	FixRounds float64 `json:"fixRounds"`
	Seconds   float64 `json:"seconds"`
	// Checkers are the families of the models that checked the builder's tickets (12.3: what the
	// numbers rest on).
	Checkers []string `json:"checkers,omitempty"`
}

// PassedShare is the share of the units that passed, from 0 to 1.
func (n Numbers) PassedShare() float64 {
	if n.Units == 0 {
		return 0
	}
	return float64(n.Passed) / float64(n.Units)
}

// FixShare is the share of an agent that checks' runs that asked for a fix.
func (n Numbers) FixShare() float64 {
	if n.Units == 0 {
		return 0
	}
	return float64(n.AskedFix) / float64(n.Units)
}

// realModel is the model a run really ran on, for its numbers: the full name the CLI said for an
// alias, the listed model for a name that holds its effort.
const realModel = `CASE WHEN r.model_name IS NOT NULL AND r.model_name <> r.model THEN r.model ELSE coalesce(r.model_resolved, r.model) END`

// BuilderUnits are the builder's tickets since a time, each with the entry and effort of its first
// build, whether its first request for a move passed Crew's checks, and how long it took.
type BuilderUnit struct {
	Ticket             int64
	Kind               string
	CLI, Model, Effort string
	Alias, From        string
	Passed             bool
	FixRounds          int
	Seconds            float64
	Started            time.Time
	CheckFamily        string
}

// BuilderTickets are the builder's tickets whose first build started since a time and whose
// command checks ran at least once for a move.
func BuilderTickets(ctx context.Context, q db.Querier, since time.Time) ([]BuilderUnit, error) {
	rows, err := q.Query(ctx, `WITH first AS (
			SELECT DISTINCT ON (s.ticket) s.ticket, r.cli, `+realModel+` AS model, coalesce(r.effort, '') AS effort,
				coalesce(r.model, '') AS alias, coalesce(r.model_from, '') AS from_, r.started_at
			FROM subtasks s JOIN runs r ON r.subtask = s.id
			WHERE s.kind = 'build' AND s.ticket IS NOT NULL
			ORDER BY s.ticket, r.started_at)
		SELECT f.ticket, t.kind, f.cli, f.model, f.effort, f.alias, f.from_,
			(SELECT c.state = 'passed' FROM check_runs c WHERE c.ticket = f.ticket AND c.purpose = 'exit' ORDER BY c.id LIMIT 1),
			t.fix_rounds,
			coalesce((SELECT sum(extract(epoch FROM coalesce(r.ended_at, now()) - r.started_at))::float8 FROM runs r JOIN subtasks s ON s.id = r.subtask
				WHERE s.ticket = f.ticket AND s.kind IN ('build', 'fix')), 0),
			f.started_at,
			coalesce((SELECT `+realModel+` FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = f.ticket AND s.kind = 'review'
				ORDER BY r.started_at LIMIT 1), '')
		FROM first f JOIN tickets t ON t.id = f.ticket
		WHERE f.started_at >= $1 AND EXISTS (SELECT 1 FROM check_runs c WHERE c.ticket = f.ticket AND c.purpose = 'exit' AND c.state <> 'running')
		ORDER BY f.started_at`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (BuilderUnit, error) {
		var u BuilderUnit
		var passed *bool
		var checker string
		err := r.Scan(&u.Ticket, &u.Kind, &u.CLI, &u.Model, &u.Effort, &u.Alias, &u.From, &passed, &u.FixRounds, &u.Seconds,
			&u.Started, &checker)
		u.Passed = passed != nil && *passed
		if checker != "" {
			u.CheckFamily = familyOf(checker)
		}
		return u, err
	})
}

// CheckerRuns are the runs of an agent that checks since a time, with whether each asked for a fix,
// and the defects that escaped it: bugs Analysis approved in the ticket a run passed, that ticket
// confirmed (12.3, since 0.54).
type CheckerRun struct {
	Run                string
	Ticket             int64
	Escaped            int
	Kind               string
	CLI, Model, Effort string
	Alias, From        string
	AskedFix           bool
	Seconds            float64
	Started            time.Time
}

// CheckerRunsOf are the runs of a role that wrote its verdict: a reviewer's review, a tester's test
// entry, an E2E tester's entry, a verifier's verification. A defect escaped a run that passed its
// ticket when a bug is in that ticket and Analysis approved the bug's fix: its fix ticket is ready, or
// the bug is in work or fixed. A report alone does not count (12.3), and neither does the ticket its
// reporter named until the analysis of its fix confirmed it, or the operator named it (since 0.54).
func CheckerRunsOf(ctx context.Context, q db.Querier, role string, since time.Time) ([]CheckerRun, error) {
	rows, err := q.Query(ctx, `SELECT r.id::text, coalesce(s.ticket, 0), coalesce(t.kind, ''), r.cli, `+realModel+`, coalesce(r.effort, ''), coalesce(r.model, ''),
			coalesce(r.model_from, ''), coalesce(bool_or(rec.verdict IS NOT DISTINCT FROM 'changes_requested' OR rec.result IS NOT DISTINCT FROM 'fail'), false),
			extract(epoch FROM coalesce(r.ended_at, now()) - r.started_at)::float8, r.started_at
		FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN records rec ON rec.run = r.id AND rec.kind IN ('review', 'test', 'e2e', 'verify')
		LEFT JOIN tickets t ON t.id = s.ticket
		WHERE s.role = $1 AND r.started_at >= $2
		GROUP BY r.id, s.ticket, t.kind ORDER BY r.started_at`, role, since)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (CheckerRun, error) {
		var c CheckerRun
		err := r.Scan(&c.Run, &c.Ticket, &c.Kind, &c.CLI, &c.Model, &c.Effort, &c.Alias, &c.From, &c.AskedFix, &c.Seconds, &c.Started)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT b.in_ticket, count(*) FROM bugs b LEFT JOIN tickets f ON f.id = b.fix_ticket
		WHERE b.in_ticket IS NOT NULL AND b.in_confirmed AND b.status <> 'closed' AND (b.status IN ('in_work', 'fixed') OR f.analysis->>'status' = 'ready')
		GROUP BY b.in_ticket`)
	if err != nil {
		return nil, err
	}
	escaped := map[int64]int{}
	for rows.Next() {
		var t int64
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return nil, err
		}
		escaped[t] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A run counts one escape at most, however many bugs name the ticket it passed: one run of an agent
	// can report several, and the share of runs that let a defect escape is what decides (12.3,
	// security review of 0.53).
	for i := range out {
		if !out[i].AskedFix {
			out[i].Escaped = min(1, escaped[out[i].Ticket])
		}
	}
	return out, nil
}

// Results are the numbers of a role on each entry and effort it ran on since a time, for every kind
// of work and for each kind.
func Results(ctx context.Context, q db.Querier, role string, since time.Time) ([]Numbers, error) {
	type key struct{ cli, model, effort, kind string }
	agg := map[key]*Numbers{}
	var order []key
	add := func(k key) *Numbers {
		n, ok := agg[k]
		if !ok {
			n = &Numbers{Role: role, CLI: k.cli, Model: k.model, Effort: k.effort, Kind: k.kind}
			agg[k] = n
			order = append(order, k)
		}
		return n
	}
	switch role {
	case "builder":
		units, err := BuilderTickets(ctx, q, since)
		if err != nil {
			return nil, err
		}
		for _, u := range units {
			for _, kind := range []string{"", u.Kind} {
				n := add(key{u.CLI, u.Model, u.Effort, kind})
				n.Units++
				if u.Passed {
					n.Passed++
				}
				n.FixRounds += float64(u.FixRounds)
				n.Seconds += u.Seconds
				if u.CheckFamily != "" && !contains(n.Checkers, u.CheckFamily) {
					n.Checkers = append(n.Checkers, u.CheckFamily)
				}
			}
		}
	default:
		runs, err := CheckerRunsOf(ctx, q, role, since)
		if err != nil {
			return nil, err
		}
		for _, c := range runs {
			for _, kind := range []string{"", c.Kind} {
				n := add(key{c.CLI, c.Model, c.Effort, kind})
				n.Units++
				if c.AskedFix {
					n.AskedFix++
				}
				n.Escaped += c.Escaped
				n.Seconds += c.Seconds
			}
		}
	}
	out := make([]Numbers, 0, len(order))
	for _, k := range order {
		n := *agg[k]
		if n.Units > 0 {
			n.FixRounds /= float64(n.Units)
			n.Seconds /= float64(n.Units)
		}
		out = append(out, n)
	}
	return out, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// familyOf is a model's family, by its name (12.3).
func familyOf(model string) string { return cli.Family(model) }
