package analytics

import (
	"context"
	"sort"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/release"
)

// Crew is the operator's one view across all projects (10.7). Agents in projects cannot see it.
type Crew struct {
	ComputedAt time.Time `json:"computedAt"`
	Version    string    `json:"version"`
	// VersionNote is what the running version changed, when an improvement shipped in it.
	VersionNote string `json:"versionNote"`
	Done        int    `json:"done"`
	Projects    int    `json:"projects"`
	// Interventions per 100 finished tickets is the headline number: how often the pipeline needed
	// the operator.
	Interventions       int     `json:"interventions"`
	InterventionsPer100 float64 `json:"interventionsPer100"`
	InterventionsByWhat []Bar   `json:"interventionsByWhat"`
	// FirstRequest is the share of tickets on the running version whose checks passed on the first
	// request.
	FirstRequest Share           `json:"firstRequest"`
	Keys         []ProjectKey    `json:"projectKeys"`
	Kinds        []KindCount     `json:"kinds"`
	Latest       []SignalLine    `json:"latest"`
	Versions     []VersionResult `json:"versions"`
	// Mixed are Done tickets whose runs used more than one version: they count on none (10.5).
	Mixed      int             `json:"mixed"`
	Comparison []ProjectResult `json:"comparison"`
	Use        CrewUse         `json:"use"`
	Prompts    Prompts         `json:"prompts"`
}

// ProjectKey is a project as a column of the Crew level.
type ProjectKey struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// KindCount is one kind of signal, counted in all projects and in each.
type KindCount struct {
	Kind
	All       int            `json:"all"`
	ByProject map[string]int `json:"byProject"`
	// NoProject are the signals of no project, such as a plan's choice of a rewrite, so that All is
	// the sum of the columns.
	NoProject int `json:"noProject"`
}

// SignalLine is one signal as the Crew level lists it.
type SignalLine struct {
	At      time.Time `json:"at"`
	Project string    `json:"project"`
	Kind    string    `json:"kind"`
	Label   string    `json:"label"`
	Text    string    `json:"text"`
	Ticket  *string   `json:"ticket"`
	Version string    `json:"version"`
}

// VersionResult is the results of the tickets Done on one Crew version, all projects (10.7).
type VersionResult struct {
	Version            string  `json:"version"`
	Running            bool    `json:"running"`
	Tickets            int     `json:"tickets"`
	FirstRequest       Share   `json:"firstRequest"`
	FixRoundsPerTicket float64 `json:"fixRoundsPerTicket"`
	FailedMerge        Share   `json:"failedMerge"`
	TokensPerTicket    float64 `json:"tokensPerTicket"`
}

// ProjectResult is one project's numbers beside the others (10.7, Comparison).
type ProjectResult struct {
	Key                string  `json:"key"`
	Name               string  `json:"name"`
	Done               int     `json:"done"`
	FirstRequest       Share   `json:"firstRequest"`
	FixRoundsPerTicket float64 `json:"fixRoundsPerTicket"`
	TokensPerTicket    float64 `json:"tokensPerTicket"`
	Interventions      int     `json:"interventions"`
	EscapedDefects     int     `json:"escapedDefects"`
	CreatedDiscarded   int     `json:"createdDiscarded"`
}

// CrewUse is the use of tokens and time per ticket, by role, stage, model and effort, and version
// (10.7; since 0.53 no cost).
type CrewUse struct {
	PerTicket      float64 `json:"perTicket"`
	MinutesPerTick float64 `json:"minutesPerTicket"`
	ByRole         []Bar   `json:"byRole"`
	ByStage        []Bar   `json:"byStage"`
	ByModel        []Bar   `json:"byModel"`
	ByVersion      []Bar   `json:"byVersion"`
}

// Prompts is what followed each choice of a rewrite (7.2, 10.7), with its count. Crew draws no
// conclusion from fewer than ten plans of a kind.
type Prompts struct {
	Plans   []PromptChoice `json:"plans"`
	Improve Improve        `json:"improve"`
}

// PromptChoice is what followed plans that started from one choice: the rewrite, an edited rewrite, or
// the operator's own text.
type PromptChoice struct {
	Choice string `json:"choice"`
	Plans  int    `json:"plans"`
	// Questions are the planning agent's messages in those drafts that asked something.
	Questions   int   `json:"questions"`
	SentBack    int   `json:"sentBack"`
	PassedFirst Share `json:"passedFirst"`
	Enough      bool  `json:"enough"`
}

// Improve is how often Improve text was used, edited, or refused.
type Improve struct {
	Used    int `json:"used"`
	Edited  int `json:"edited"`
	Refused int `json:"refused"`
}

// interventionKinds are the signals that count as the operator stepping in.
const interventionKinds = `('operator_intervention', 'merged_outside')`

// fixture is Crew's own project, which no view counts. ownProjects are it and the hidden ones, the
// gate's crew-gate (16.2), whose tickets, runs, and signals no view counts either.
const (
	fixture     = `'f1c50000-0000-4000-8000-000000000001'`
	ownProjects = `(SELECT id FROM projects WHERE hidden OR id = ` + fixture + `)`
)

// ticketVersion gives each Done ticket the version every run of it used, or mixed (10.5).
const ticketVersion = `tv AS (SELECT t.id, t.project,
		CASE WHEN count(DISTINCT r.crew_version) = 1 THEN min(r.crew_version) ELSE 'mixed' END AS v,
		coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 AS tokens
	FROM tickets t JOIN subtasks s ON s.ticket = t.id JOIN runs r ON r.subtask = s.id
	WHERE t.stage = 'done' AND t.project NOT IN ` + ownProjects + ` GROUP BY t.id, t.project)`

// ForCrew computes the Crew level's numbers. version is the running Crew version.
func ForCrew(ctx context.Context, q db.Querier, version string, now time.Time) (*Crew, error) {
	out := &Crew{ComputedAt: now, Version: version}
	if err := q.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tickets t WHERE t.stage = 'done' AND t.project NOT IN `+ownProjects+`),
			(SELECT count(*) FROM projects WHERE id <> `+fixture+` AND NOT hidden AND state <> 'archived'),
			(SELECT count(*) FROM signals WHERE kind IN `+interventionKinds+` AND (project IS NULL OR project NOT IN `+ownProjects+`)),
			coalesce((SELECT title FROM improvements WHERE release_to = $1 ORDER BY id DESC LIMIT 1), '')`, version).
		Scan(&out.Done, &out.Projects, &out.Interventions, &out.VersionNote); err != nil {
		return nil, err
	}
	if out.Done > 0 {
		out.InterventionsPer100 = float64(out.Interventions) * 100 / float64(out.Done)
	}
	var err error
	if out.InterventionsByWhat, err = bars(ctx, q, `SELECT CASE WHEN kind = 'merged_outside' THEN 'Work merged outside Crew' ELSE coalesce(detail->>'text', detail->>'what') END, count(*)::float8
		FROM signals WHERE kind IN `+interventionKinds+` AND (project IS NULL OR project NOT IN `+ownProjects+`) GROUP BY 1 ORDER BY 2 DESC, 1`); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `SELECT key, name FROM projects WHERE id <> `+fixture+` AND NOT hidden AND state <> 'archived' ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	out.Keys = []ProjectKey{}
	for rows.Next() {
		var k ProjectKey
		if err := rows.Scan(&k.Key, &k.Name); err != nil {
			rows.Close()
			return nil, err
		}
		out.Keys = append(out.Keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts := map[string]map[string]int{}
	// A signal of no project has a column of its own; the fixture, a hidden project, and an archived one have none, so
	// they are not counted, and every row adds up.
	rows, err = q.Query(ctx, `SELECT `+displayKind+`, coalesce(p.key, ''), count(*) FROM signals g LEFT JOIN projects p ON p.id = g.project
		WHERE g.project IS NULL OR (p.id <> `+fixture+` AND NOT p.hidden AND p.state <> 'archived') GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, key string
		var n int
		if err := rows.Scan(&kind, &key, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if counts[kind] == nil {
			counts[kind] = map[string]int{}
		}
		counts[kind][key] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, k := range Kinds {
		if !k.Counted {
			continue
		}
		kc := KindCount{Kind: k, ByProject: map[string]int{}}
		for key, n := range counts[k.Kind] {
			kc.All += n
			if key == "" {
				kc.NoProject = n
			} else {
				kc.ByProject[key] = n
			}
		}
		out.Kinds = append(out.Kinds, kc)
	}

	if out.Latest, err = latest(ctx, q, 30); err != nil {
		return nil, err
	}

	rows, err = q.Query(ctx, `WITH `+ticketVersion+`
		SELECT tv.v, count(*),
			count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM signals g WHERE g.ticket = tv.id AND g.kind = 'check_round')),
			coalesce(sum((SELECT count(*) FROM signals g WHERE g.ticket = tv.id AND g.kind = 'rework')), 0),
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM signals g WHERE g.ticket = tv.id AND g.kind = 'rework' AND g.detail->>'reason' = 'merge')),
			coalesce(sum(tv.tokens), 0)::float8
		FROM tv GROUP BY tv.v`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v VersionResult
		var first, rework, merge int
		var tokens float64
		if err := rows.Scan(&v.Version, &v.Tickets, &first, &rework, &merge, &tokens); err != nil {
			rows.Close()
			return nil, err
		}
		if v.Version == "mixed" {
			out.Mixed = v.Tickets
			continue
		}
		v.Running = v.Version == version
		v.FirstRequest, v.FailedMerge = share(first, v.Tickets), share(merge, v.Tickets)
		v.FixRoundsPerTicket, v.TokensPerTicket = float64(rework)/float64(v.Tickets), tokens/float64(v.Tickets)
		out.Versions = append(out.Versions, v)
		if v.Running {
			out.FirstRequest = v.FirstRequest
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortVersions(out.Versions)

	if out.Comparison, err = comparison(ctx, q); err != nil {
		return nil, err
	}
	if out.Use, err = crewUse(ctx, q, out.Done); err != nil {
		return nil, err
	}
	if out.Prompts, err = prompts(ctx, q); err != nil {
		return nil, err
	}
	return out, nil
}

// latest are the newest signals of every project, each with what it holds (What). A sandbox's
// start-up and a ticket's tokens and time (the kind cost_and_time, a name kept in stored rows) are left
// out: they are measurements, which Analytics and the Use card show, and one comes with every run or
// every ticket. An open choice an agent
// decided is told by its question and its choice, from the decision it names.
func latest(ctx context.Context, q db.Querier, limit int) ([]SignalLine, error) {
	rows, err := q.Query(ctx, `SELECT g.at, coalesce(p.key, ''), `+displayKind+`,
			g.detail || CASE WHEN g.kind = 'decided_alone' THEN coalesce((SELECT jsonb_build_object('question', d.question, 'choice', d.choice, 'role', d.role)
				FROM decisions d WHERE d.id = (g.detail->>'decision')::bigint), '{}') ELSE '{}' END,
			CASE WHEN t.id IS NULL THEN NULL ELSE p.key || '-' || t.number END, coalesce(g.crew_version, '')
		FROM signals g LEFT JOIN projects p ON p.id = g.project LEFT JOIN tickets t ON t.id = g.ticket
		WHERE g.kind NOT IN ('sandbox_startup', 'cost_and_time') AND p.hidden IS NOT TRUE
		ORDER BY g.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SignalLine{}
	for rows.Next() {
		var l SignalLine
		var detail map[string]any
		if err := rows.Scan(&l.At, &l.Project, &l.Kind, &detail, &l.Ticket, &l.Version); err != nil {
			return nil, err
		}
		l.Label, l.Text = KindOf(l.Kind).Label, What(l.Kind, detail)
		if l.Text == "" {
			l.Text = l.Label
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func comparison(ctx context.Context, q db.Querier) ([]ProjectResult, error) {
	rows, err := q.Query(ctx, `SELECT p.key, p.name,
			(SELECT count(*) FROM tickets t WHERE t.project = p.id AND t.stage = 'done'),
			(SELECT count(*) FROM tickets t WHERE t.project = p.id AND t.stage = 'done' AND `+firstRequest+`),
			(SELECT count(*) FROM signals g WHERE g.project = p.id AND g.kind = 'rework'),
			(SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+` WHERE s.project = p.id),
			(SELECT count(*) FROM signals g WHERE g.project = p.id AND g.kind IN `+interventionKinds+`),
			(SELECT count(*) FROM signals g WHERE g.project = p.id AND g.kind = 'escaped_defect'),
			(SELECT count(*) FROM signals g WHERE g.project = p.id AND g.kind = 'analysis_decision' AND g.detail->>'choice' = 'discard')
		FROM projects p WHERE p.id <> `+fixture+` AND NOT p.hidden AND p.state <> 'archived' ORDER BY p.created_at, p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectResult{}
	for rows.Next() {
		var r ProjectResult
		var first, rework int
		var tokens float64
		if err := rows.Scan(&r.Key, &r.Name, &r.Done, &first, &rework, &tokens, &r.Interventions, &r.EscapedDefects, &r.CreatedDiscarded); err != nil {
			return nil, err
		}
		r.FirstRequest = share(first, r.Done)
		if r.Done > 0 {
			r.FixRoundsPerTicket, r.TokensPerTicket = float64(rework)/float64(r.Done), tokens/float64(r.Done)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func crewUse(ctx context.Context, q db.Querier, done int) (CrewUse, error) {
	var c CrewUse
	var total float64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+` WHERE (s.project IS NULL OR s.project NOT IN `+ownProjects+`)`).Scan(&total); err != nil {
		return c, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(avg((g.detail->>'minutes')::float8), 0) FROM signals g WHERE g.kind = 'cost_and_time'
		AND (g.project IS NULL OR g.project NOT IN `+ownProjects+`)`).Scan(&c.MinutesPerTick); err != nil {
		return c, err
	}
	if done > 0 {
		c.PerTicket = total / float64(done)
	}
	var err error
	where := `WHERE (s.project IS NULL OR s.project NOT IN ` + ownProjects + `)`
	if c.ByRole, err = bars(ctx, q, `SELECT s.role, coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+` `+where+` GROUP BY 1 HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`); err != nil {
		return c, err
	}
	if c.ByStage, err = bars(ctx, q, `SELECT s.kind, coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+` `+where+` GROUP BY 1 HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`); err != nil {
		return c, err
	}
	if c.ByModel, err = bars(ctx, q, `SELECT coalesce(r.model, r.cli) || coalesce(' · ' || r.effort, ''), coalesce(sum(r.tokens_in + r.tokens_out), 0)::float8 FROM `+projectRuns+` `+where+`
		GROUP BY 1 HAVING sum(r.tokens_in + r.tokens_out) > 0 ORDER BY 2 DESC`); err != nil {
		return c, err
	}
	if c.ByVersion, err = bars(ctx, q, `WITH `+ticketVersion+` SELECT v, avg(tokens)::float8 FROM tv WHERE v <> 'mixed' GROUP BY v ORDER BY v`); err != nil {
		return c, err
	}
	return c, nil
}

// prompts follows each plan from the choice of its first message to its project (7.2).
func prompts(ctx context.Context, q db.Querier) (Prompts, error) {
	var out Prompts
	rows, err := q.Query(ctx, `WITH first AS (
			SELECT DISTINCT ON (w.draft) w.draft, w.choice FROM prompt_rewrites w
			WHERE w.kind = 'first' AND w.choice IN ('rewrite', 'edited', 'mine') ORDER BY w.draft, w.id DESC),
		plan AS (SELECT f.choice, f.draft, d.project FROM first f JOIN plan_drafts d ON d.id = f.draft)
		SELECT c.choice,
			(SELECT count(*) FROM plan WHERE plan.choice = c.choice),
			(SELECT count(*) FROM plan JOIN conversations v ON v.draft = plan.draft JOIN turns u ON u.conversation = v.id
				WHERE plan.choice = c.choice AND u.speaker = 'agent' AND u.text LIKE '%?%'),
			(SELECT count(*) FROM plan JOIN plans b ON b.project = plan.project AND b.kind = 'sent_back' WHERE plan.choice = c.choice),
			(SELECT count(*) FROM plan JOIN tickets t ON t.project = plan.project AND t.stage = 'done' WHERE plan.choice = c.choice),
			(SELECT count(*) FROM plan JOIN tickets t ON t.project = plan.project AND t.stage = 'done' WHERE plan.choice = c.choice AND `+firstRequest+`)
		FROM (VALUES ('rewrite', 1), ('edited', 2), ('mine', 3)) c (choice, o) ORDER BY c.o`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PromptChoice
		var done, first int
		if err := rows.Scan(&p.Choice, &p.Plans, &p.Questions, &p.SentBack, &done, &first); err != nil {
			return out, err
		}
		p.PassedFirst, p.Enough = share(first, done), p.Plans >= 10
		out.Plans = append(out.Plans, p)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	err = q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE choice = 'use'), count(*) FILTER (WHERE choice = 'edited'), count(*) FILTER (WHERE choice = 'keep')
		FROM prompt_rewrites WHERE kind = 'improve'`).Scan(&out.Improve.Used, &out.Improve.Edited, &out.Improve.Refused)
	return out, err
}

// sortVersions puts the newest version first.
func sortVersions(vs []VersionResult) {
	sort.Slice(vs, func(i, j int) bool { return release.Less(vs[j].Version, vs[i].Version) })
}
