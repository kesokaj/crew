package analytics

import (
	"context"
	"time"

	"github.com/kesokaj/crew/internal/db"
)

// Bar is one bar of a chart: a label and its value.
type Bar struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Share is n of a whole, with its share; Share is 0 when the whole is.
type Share struct {
	N     int     `json:"n"`
	Of    int     `json:"of"`
	Share float64 `json:"share"`
}

func share(n, of int) Share {
	s := Share{N: n, Of: of}
	if of > 0 {
		s.Share = float64(n) / float64(of)
	}
	return s
}

// Project is the analytics of one project (9.7): flow, quality, and use (since 0.53 tokens and time,
// and no cost).
type Project struct {
	ComputedAt time.Time `json:"computedAt"`
	Done       int       `json:"done"`
	Tickets    int       `json:"tickets"`
	// PassedFirst are the Done tickets with no failed check on a request and no fix round.
	PassedFirst Share `json:"passedFirst"`
	// FirstRequest are the Done tickets whose checks passed on their first request to move.
	FirstRequest       Share   `json:"firstRequest"`
	FixRounds          int     `json:"fixRounds"`
	FixRoundsPerTicket float64 `json:"fixRoundsPerTicket"`
	TokensIn           int64   `json:"tokensIn"`
	TokensOut          int64   `json:"tokensOut"`
	// TokensPerTicket and MinutesPerTicket are the use of a Done ticket: its runs' tokens in and out,
	// and their time.
	TokensPerTicket  float64 `json:"tokensPerTicket"`
	MinutesPerTicket float64 `json:"minutesPerTicket"`
	DonePerDay       []Bar   `json:"donePerDay"`
	StageMinutes     []Bar   `json:"stageMinutes"`
	// TokensByRole, TokensByModel, and TokensByPart are the tokens in and out of the project's runs by
	// role, by model and effort, and by the part of the plan of their ticket.
	TokensByRole   []Bar   `json:"tokensByRole"`
	TokensByModel  []Bar   `json:"tokensByModel"`
	TokensByPart   []Bar   `json:"tokensByPart"`
	Created        Created `json:"created"`
	Docs           Share   `json:"docs"`
	Bugs           Bugs    `json:"bugs"`
	Quality        Quality `json:"quality"`
	FeedbackByRole []Bar   `json:"feedbackByRole"`
	Waiting        Waiting `json:"waiting"`
	// SandboxStartSeconds is the average time a sandbox took to start, clone, and set up.
	SandboxStartSeconds float64 `json:"sandboxStartSeconds"`
	// Evals are the suites that measured a case; EvalsUnmeasured counts those that ended having
	// measured none, which are no share of zero.
	Evals           []Bar `json:"evals"`
	EvalsUnmeasured int   `json:"evalsUnmeasured"`
}

// Created are the tickets agents created, by what their analysis decided; Added is how many count as
// work agents added, as the scope guard counts them, against Plan, the tickets of the approved plan
// (R32, 7.7).
type Created struct {
	Total     int   `json:"total"`
	Approved  int   `json:"approved"`
	Skipped   int   `json:"skipped"`
	Discarded int   `json:"discarded"`
	Notice    int   `json:"notice"`
	Added     Share `json:"added"`
	ByRole    []Bar `json:"byRole"`
}

// Bugs are the project's bugs by their state.
type Bugs struct {
	Open   int `json:"open"`
	Fixed  int `json:"fixed"`
	Closed int `json:"closed"`
}

// Quality are the counts of what went another way than planned.
type Quality struct {
	LookedAgain      int `json:"lookedAgain"`
	Cancelled        int `json:"cancelled"`
	WatchdogWarnings int `json:"watchdogWarnings"`
	WatchdogStops    int `json:"watchdogStops"`
	EscapedDefects   int `json:"escapedDefects"`
	// PagesBehind are the times a ticket changed code and not the page that covers it.
	PagesBehind int `json:"pagesBehind"`
}

// Waiting is what waits now: for the operator, for a lead slot, for room on the machine, for analysis.
type Waiting struct {
	Operator int `json:"operator"`
	LeadSlot int `json:"leadSlot"`
	Room     int `json:"room"`
	Analysis int `json:"analysis"`
}

// The SQL shared by both levels.
const (
	// projectRuns are the runs of a project: every subtask that names it.
	projectRuns = `runs r JOIN subtasks s ON s.id = r.subtask`
	// firstRequest is true for a ticket no check failed on a request to move (10.2, check rounds).
	firstRequest = `NOT EXISTS (SELECT 1 FROM signals g WHERE g.ticket = t.id AND g.kind = 'check_round')`
	// noFix is true for a ticket that had no fix round.
	noFix = `NOT EXISTS (SELECT 1 FROM signals g WHERE g.ticket = t.id AND g.kind = 'rework')`
)

// ForProject computes a project's analytics from its records, runs, moves, bugs, feedback, and signals.
func ForProject(ctx context.Context, q db.Querier, project string, now time.Time) (*Project, error) {
	out := &Project{ComputedAt: now}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE t.stage = 'done'), count(*),
			count(*) FILTER (WHERE t.stage = 'done' AND `+firstRequest+` AND `+noFix+`),
			count(*) FILTER (WHERE t.stage = 'done' AND `+firstRequest+`),
			count(*) FILTER (WHERE t.closed_reason = 'cancelled')
		FROM tickets t WHERE t.project = $1`, project).
		Scan(&out.Done, &out.Tickets, &out.PassedFirst.N, &out.FirstRequest.N, &out.Quality.Cancelled); err != nil {
		return nil, err
	}
	out.PassedFirst = share(out.PassedFirst.N, out.Done)
	out.FirstRequest = share(out.FirstRequest.N, out.Done)

	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'rework'),
			count(*) FILTER (WHERE kind = 'look_again'),
			count(*) FILTER (WHERE kind = 'watchdog_warning'),
			count(*) FILTER (WHERE kind = 'watchdog_stop'),
			count(*) FILTER (WHERE kind = 'escaped_defect'),
			count(*) FILTER (WHERE kind = 'docs_behind'),
			coalesce(avg((detail->>'seconds')::float8) FILTER (WHERE kind = 'sandbox_startup'), 0)
		FROM signals WHERE project = $1`, project).
		Scan(&out.FixRounds, &out.Quality.LookedAgain, &out.Quality.WatchdogWarnings, &out.Quality.WatchdogStops,
			&out.Quality.EscapedDefects, &out.Quality.PagesBehind, &out.SandboxStartSeconds); err != nil {
		return nil, err
	}
	if out.Done > 0 {
		out.FixRoundsPerTicket = float64(out.FixRounds) / float64(out.Done)
	}

	var minutes float64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(r.tokens_in), 0)::bigint, coalesce(sum(r.tokens_out), 0)::bigint,
			coalesce(sum(extract(epoch FROM coalesce(r.ended_at, $2::timestamptz) - r.started_at)) FILTER (WHERE t.stage = 'done'), 0)::float8 / 60
		FROM `+projectRuns+` LEFT JOIN tickets t ON t.id = s.ticket WHERE s.project = $1`, project, now).Scan(&out.TokensIn, &out.TokensOut, &minutes); err != nil {
		return nil, err
	}
	if out.Done > 0 {
		var done int64
		if err := q.QueryRow(ctx, `SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint FROM `+projectRuns+` JOIN tickets t ON t.id = s.ticket
			WHERE s.project = $1 AND t.stage = 'done'`, project).Scan(&done); err != nil {
			return nil, err
		}
		out.TokensPerTicket, out.MinutesPerTicket = float64(done)/float64(out.Done), minutes/float64(out.Done)
	}
	var err error
	if out.TokensByRole, err = bars(ctx, q, `SELECT s.role, coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+`
		WHERE s.project = $1 GROUP BY s.role HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`, project); err != nil {
		return nil, err
	}
	if out.TokensByModel, err = bars(ctx, q, `SELECT coalesce(r.model, r.cli) || coalesce(' · ' || r.effort, ''), coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8
		FROM `+projectRuns+` WHERE s.project = $1 GROUP BY 1 HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`, project); err != nil {
		return nil, err
	}
	if out.TokensByPart, err = bars(ctx, q, `SELECT t.part, coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8
		FROM `+projectRuns+` JOIN tickets t ON t.id = s.ticket WHERE s.project = $1 AND coalesce(t.part, '') <> ''
		GROUP BY 1 HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`, project); err != nil {
		return nil, err
	}
	if out.DonePerDay, err = bars(ctx, q, `SELECT to_char(d, 'MM-DD'), (SELECT count(*) FROM stage_moves m JOIN tickets t ON t.id = m.ticket
			WHERE t.project = $1 AND m.to_stage = 'done' AND m.at >= d AND m.at < d + interval '1 day')::float8
		FROM generate_series(date_trunc('day', $2::timestamptz) - interval '13 days', date_trunc('day', $2::timestamptz), interval '1 day') d ORDER BY d`, project, now); err != nil {
		return nil, err
	}
	// The time in a stage is from the move into it to the move out of it; a ticket still in it counts to now.
	if out.StageMinutes, err = bars(ctx, q, `WITH m AS (
			SELECT m.to_stage AS stage, m.at, lead(m.at) OVER (PARTITION BY m.ticket ORDER BY m.id) AS left_at
			FROM stage_moves m JOIN tickets t ON t.id = m.ticket WHERE t.project = $1)
		SELECT stage, avg(extract(epoch FROM coalesce(left_at, $2::timestamptz) - at) / 60)::float8 FROM m
		WHERE stage NOT IN ('done', 'closed') GROUP BY stage ORDER BY min(at)`, project, now); err != nil {
		return nil, err
	}

	if err := q.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE (t.analysis->>'status' = 'ready' OR t.stage NOT IN ('planning', 'backlog', 'analysis', 'closed'))
				AND t.closed_reason IS DISTINCT FROM 'discarded'),
			count(*) FILTER (WHERE t.analysis->>'status' = 'skipped'),
			count(*) FILTER (WHERE t.closed_reason = 'discarded'),
			count(*) FILTER (WHERE t.analysis->>'status' = 'new_feature_notice')
		FROM tickets t WHERE t.project = $1 AND t.origin = 'agent'`, project).
		Scan(&out.Created.Total, &out.Created.Approved, &out.Created.Skipped, &out.Created.Discarded, &out.Created.Notice); err != nil {
		return nil, err
	}
	// Work agents added, as the scope guard counts it (board.OverLimit), against the plan's tickets.
	var added, plan int
	if err := q.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.origin = 'agent'
				AND split_part(t.created_by, ' · ', 1) <> 'backend'
				AND NOT EXISTS (SELECT 1 FROM ticket_links l WHERE l.from_ticket = t.id AND l.kind = 'replaces')
				AND (t.closed_reason IS DISTINCT FROM 'discarded')
				AND (t.analysis->>'status' IN ('ready', 'new_feature_notice')
					OR t.stage NOT IN ('planning', 'backlog', 'analysis', 'closed')
					OR t.closed_reason = 'cancelled')),
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.origin = 'plan')`, project).Scan(&added, &plan); err != nil {
		return nil, err
	}
	out.Created.Added = share(added, plan)
	if out.Created.ByRole, err = bars(ctx, q, `SELECT coalesce(nullif(split_part(t.created_by, ' · ', 1), ''), t.created_by), count(*)::float8
		FROM tickets t WHERE t.project = $1 AND t.origin = 'agent' GROUP BY 1 ORDER BY 2 DESC`, project); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE outdated_since IS NULL), count(*) FROM doc_pages WHERE project = $1`, project).
		Scan(&out.Docs.N, &out.Docs.Of); err != nil {
		return nil, err
	}
	out.Docs = share(out.Docs.N, out.Docs.Of)
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('open', 'in_work')), count(*) FILTER (WHERE status = 'fixed'),
			count(*) FILTER (WHERE status = 'closed') FROM bugs WHERE project = $1`, project).
		Scan(&out.Bugs.Open, &out.Bugs.Fixed, &out.Bugs.Closed); err != nil {
		return nil, err
	}
	if out.FeedbackByRole, err = bars(ctx, q, `SELECT about, count(*)::float8 FROM feedback WHERE project = $1 GROUP BY about ORDER BY 2 DESC, 1`, project); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.stage = 'analysis' AND t.analysis->>'status' IN ('waits_for_operator', 'new_feature_notice')),
			(SELECT count(*) FROM lead_waits w WHERE w.project = $1),
			(SELECT count(*) FROM room_waits w WHERE w.project = $1),
			(SELECT count(*) FROM tickets t WHERE t.project = $1 AND t.stage IN ('backlog', 'analysis') AND coalesce(t.analysis->>'status', '') IN ('', 'proposed', 'waiting'))`, project).
		Scan(&out.Waiting.Operator, &out.Waiting.LeadSlot, &out.Waiting.Room, &out.Waiting.Analysis); err != nil {
		return nil, err
	}
	// A release's suite measures a candidate in a gate, not the project as it runs (16.2 step 5).
	if out.Evals, err = bars(ctx, q, `SELECT to_char(e.ended_at, 'YYYY-MM-DD HH24:MI'), coalesce((e.result->'baseline'->>'passed')::float8, 0)
		FROM eval_suites e WHERE e.project = $1 AND e.state = 'done' AND e.purpose <> 'release' AND coalesce((e.result->'baseline'->>'cases')::int, 0) > 0
		ORDER BY e.ended_at DESC LIMIT 20`, project); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM eval_suites e WHERE e.project = $1 AND e.state = 'done' AND e.purpose <> 'release'
		AND coalesce((e.result->'baseline'->>'cases')::int, 0) = 0`, project).Scan(&out.EvalsUnmeasured); err != nil {
		return nil, err
	}
	return out, nil
}

// bars reads rows of a label and a value.
func bars(ctx context.Context, q db.Querier, sql string, args ...any) ([]Bar, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bar{}
	for rows.Next() {
		var b Bar
		if err := rows.Scan(&b.Label, &b.Value); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
