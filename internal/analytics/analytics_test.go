package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

// seed is two projects with what analytics reads, by SQL, at fixed times.
type seed struct {
	t    *testing.T
	d    *dbtest.DB
	ctx  context.Context
	now  time.Time
	shop string
	mail string
}

func newSeed(t *testing.T) *seed {
	s := &seed{t: t, d: dbtest.New(t, false), ctx: context.Background(), now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	s.shop = s.project("shop", "SF")
	s.mail = s.project("mail", "ML")
	return s
}

func (s *seed) exec(q string, args ...any) {
	s.t.Helper()
	if _, err := s.d.Server.Exec(s.ctx, q, args...); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
}

func (s *seed) id(q string, args ...any) int64 {
	s.t.Helper()
	var id int64
	if err := s.d.Server.QueryRow(s.ctx, q, args...).Scan(&id); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
	return id
}

func (s *seed) project(name, key string) string {
	s.t.Helper()
	var id string
	if err := s.d.Server.QueryRow(s.ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), $1, $2, 'running', 'local') RETURNING id`,
		name, key).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// ticket adds a ticket in a stage.
func (s *seed) ticket(project string, number int, stage, origin string) int64 {
	closed := any(nil)
	if stage == "closed" {
		closed = "discarded"
	}
	return s.id(`INSERT INTO tickets (project, number, title, kind, stage, closed_reason, origin, created_by, position)
		VALUES ($1, $2, 'T', 'feature', $3, $4, $5, 'lead', $6) RETURNING id`, project, number, stage, closed, origin, int64(number))
}

// run adds a run of a role on a ticket, on a Crew version, with its tokens in, of ten minutes that
// ended an hour before now.
func (s *seed) run(project string, ticket int64, role, version string, tokens int64) string {
	sub := s.id(`INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2,
		(SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), 'build', $3) RETURNING id`, project, ticket, role)
	var id string
	if err := s.d.Server.QueryRow(s.ctx, `INSERT INTO runs (id, subtask, number, cli, model, effort, crew_version, image, tokens_in, state, started_at, ended_at)
		VALUES (gen_random_uuid(), $1, 1, 'agy', 'gemini-3-pro', 'high', $2, 'img', $3, 'ended', $4::timestamptz - interval '70 minutes', $4::timestamptz - interval '1 hour')
		RETURNING id`, sub, version, tokens, s.now).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *seed) signal(project string, ticket int64, kind, detail string) {
	s.exec(`INSERT INTO signals (project, ticket, kind, detail, crew_version) VALUES ($1, nullif($2, 0), $3, $4::jsonb, '1.1.0')`, project, ticket, kind, detail)
}

func (s *seed) move(ticket int64, from, to string, at time.Time) {
	s.exec(`INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, $2, $3, 'backend', $4)`, ticket, from, to, at)
}

// all seeds the data both levels read.
func (s *seed) all() {
	t0 := s.now.Add(-2 * time.Hour)
	// SF-1 done on 1.0.0 with nothing failing; SF-2 done on 1.1.0 after a failed request; SF-3 done
	// on both versions after a fix round for the merge check; SF-4 in work; SF-5 created by an agent
	// and discarded.
	sf1 := s.ticket(s.shop, 1, "done", "plan")
	s.exec(`UPDATE tickets SET part = 'Sign-in' WHERE id = $1`, sf1)
	s.run(s.shop, sf1, "builder", "1.0.0", 1500)
	s.run(s.shop, sf1, "reviewer", "1.0.0", 500)
	s.move(sf1, "backlog", "analysis", t0)
	s.move(sf1, "analysis", "in_progress", t0.Add(10*time.Minute))
	s.move(sf1, "in_progress", "done", t0.Add(40*time.Minute))
	sf2 := s.ticket(s.shop, 2, "done", "plan")
	s.run(s.shop, sf2, "builder", "1.1.0", 2000)
	s.signal(s.shop, sf2, "check_round", `{"round": 1}`)
	s.move(sf2, "in_progress", "done", s.now.Add(-24*time.Hour))
	sf3 := s.ticket(s.shop, 3, "done", "plan")
	s.run(s.shop, sf3, "builder", "1.0.0", 1000)
	s.run(s.shop, sf3, "builder", "1.1.0", 1000)
	s.signal(s.shop, sf3, "rework", `{"reason": "merge", "stage": "verification"}`)
	sf4 := s.ticket(s.shop, 4, "in_progress", "plan")
	s.run(s.shop, sf4, "builder", "1.1.0", 250)
	sf5 := s.ticket(s.shop, 5, "closed", "agent")
	// Its analysis said ready before the operator discarded it, as on dave in M15.
	s.exec(`UPDATE tickets SET created_by = 'tester · 00000000-0000-4000-8000-000000000001', analysis = '{"status": "ready"}' WHERE id = $1`, sf5)
	s.signal(s.shop, sf5, "analysis_decision", `{"choice": "discard"}`)
	s.signal(s.shop, 0, "operator_intervention", `{"what": "breakdown_sent_back", "text": "Sent the breakdown back"}`)
	s.signal(s.shop, sf2, "operator_intervention", `{"what": "edited_before_ok", "text": "Edited SF-2 before the OK"}`)
	s.signal(s.shop, 0, "sandbox_startup", `{"seconds": 10}`)
	s.signal(s.shop, 0, "sandbox_startup", `{"seconds": 20}`)
	// ML-1 done on 1.1.0 after a fix round for the review; work merged outside Crew.
	ml1 := s.ticket(s.mail, 1, "done", "plan")
	s.run(s.mail, ml1, "builder", "1.1.0", 3000)
	s.signal(s.mail, ml1, "rework", `{"reason": "review", "stage": "review"}`)
	s.signal(s.mail, ml1, "merged_outside", `{"text": "ML-1 was merged on GitHub"}`)

	s.exec(`INSERT INTO doc_pages (project, path, title, kind, body, blob, last_commit, last_changed_at, outdated_since)
		VALUES ($1, 'docs/a.md', 'A', 'guide', '', 'b1', 'c', now(), NULL), ($1, 'docs/b.md', 'B', 'guide', '', 'b2', 'c', now(), now())`, s.shop)
	s.exec(`INSERT INTO bugs (project, number, title, severity, status, found_by, fix_ticket) VALUES ($1, 1, 'B', 'low', 'open', 'tester', $2), ($1, 2, 'C', 'low', 'fixed', 'tester', $3)`,
		s.shop, sf4, sf2)
	s.exec(`INSERT INTO feedback (project, number, from_role, about, kind, body) VALUES ($1, 1, 'reviewer', 'builder', 'correction', 'Test the failure paths.')`, s.shop)

	// A plan that started from an edited rewrite became shop; Improve text was used once and kept once.
	draft := s.id(`WITH d AS (INSERT INTO plan_drafts (id, state, project) VALUES (gen_random_uuid(), 'project', $1) RETURNING id)
		INSERT INTO prompt_rewrites (kind, draft, original, state, choice) SELECT 'first', id, 'make a shop', 'ready', 'edited' FROM d RETURNING id`, s.shop)
	_ = draft
	s.exec(`INSERT INTO prompt_rewrites (kind, project, original, state, choice) VALUES ('improve', $1, 'x', 'ready', 'use'), ('improve', $1, 'y', 'ready', 'keep')`, s.shop)
	s.exec(`INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'sent_back', 1, 'smaller', 'operator')`, s.shop)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A project's analytics are computed from its tickets, runs, moves, signals, pages, bugs, and
// feedback, and from nothing of another project (9.7).
func TestAProjectsAnalytics(t *testing.T) {
	s := newSeed(t)
	s.all()
	m, err := ForProject(s.ctx, s.d.Server, s.shop, s.now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Done != 3 || m.Tickets != 5 {
		t.Fatalf("done %d of %d", m.Done, m.Tickets)
	}
	if m.PassedFirst.N != 1 || !near(m.PassedFirst.Share, 1.0/3) || m.FirstRequest.N != 2 {
		t.Fatalf("passed first %+v, first request %+v", m.PassedFirst, m.FirstRequest)
	}
	if m.FixRounds != 1 || !near(m.FixRoundsPerTicket, 1.0/3) {
		t.Fatalf("fix rounds %d, %v", m.FixRounds, m.FixRoundsPerTicket)
	}
	// Use, since 0.53 in tokens and time, and no cost: a Done ticket's runs used 6000 tokens over three,
	// and five runs of ten minutes over three.
	if m.TokensIn != 6250 || !near(m.TokensPerTicket, 2000) || !near(m.MinutesPerTicket, 50.0/3) {
		t.Fatalf("tokens %d, %v per ticket, %v minutes", m.TokensIn, m.TokensPerTicket, m.MinutesPerTicket)
	}
	if len(m.TokensByRole) != 2 || m.TokensByRole[0] != (Bar{"builder", 5750}) || m.TokensByRole[1] != (Bar{"reviewer", 500}) {
		t.Fatalf("tokens by role %v", m.TokensByRole)
	}
	if len(m.TokensByModel) != 1 || m.TokensByModel[0] != (Bar{"gemini-3-pro · high", 6250}) || len(m.TokensByPart) != 1 || m.TokensByPart[0] != (Bar{"Sign-in", 2000}) {
		t.Fatalf("tokens by model %v, by part %v", m.TokensByModel, m.TokensByPart)
	}
	// SF-5, an agent's, was discarded: none added against the plan's four.
	if m.Created.Added.N != 0 || m.Created.Added.Of != 4 {
		t.Fatalf("added %+v", m.Created.Added)
	}
	if len(m.DonePerDay) != 14 || m.DonePerDay[13] != (Bar{"10-19", 1}) || m.DonePerDay[12] != (Bar{"10-18", 1}) {
		t.Fatalf("done per day %v", m.DonePerDay)
	}
	if len(m.StageMinutes) != 2 || m.StageMinutes[0] != (Bar{"analysis", 10}) || m.StageMinutes[1] != (Bar{"in_progress", 30}) {
		t.Fatalf("minutes in each stage %v", m.StageMinutes)
	}
	// A discarded ticket is not approved too, whatever its analysis said (M15 on dave).
	if m.Created.Total != 1 || m.Created.Discarded != 1 || m.Created.Approved != 0 || len(m.Created.ByRole) != 1 || m.Created.ByRole[0].Label != "tester" {
		t.Fatalf("created %+v", m.Created)
	}
	if m.Docs.N != 1 || m.Docs.Of != 2 || m.Bugs.Open != 1 || m.Bugs.Fixed != 1 {
		t.Fatalf("docs %+v, bugs %+v", m.Docs, m.Bugs)
	}
	if len(m.FeedbackByRole) != 1 || m.FeedbackByRole[0] != (Bar{"builder", 1}) || !near(m.SandboxStartSeconds, 15) {
		t.Fatalf("feedback %v, start-up %v", m.FeedbackByRole, m.SandboxStartSeconds)
	}
	// The other project's numbers are its own.
	o, err := ForProject(s.ctx, s.d.Server, s.mail, s.now)
	if err != nil {
		t.Fatal(err)
	}
	if o.Done != 1 || o.TokensIn != 3000 || o.FixRounds != 1 || o.PassedFirst.N != 0 || o.Docs.Of != 0 {
		t.Fatalf("mail %+v", o)
	}
}

// Analytics draws the share of goals met only for suites that measured a case; one that measured none
// is counted beside it, not drawn as 0% (M15 on dave).
func TestAProjectsEvalsLeaveOutWhatMeasuredNothing(t *testing.T) {
	s := newSeed(t)
	for i, result := range []string{`{"baseline": {"cases": 2, "passed": 0.5}}`, `{"baseline": {"cases": 0, "passed": 0}}`, `null`} {
		s.exec(`INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, result, ended_at)
			VALUES ($1, 'on_demand', 1, 10, 'done', 'operator', $2::jsonb, $3)`, s.shop, result, s.now.Add(-time.Duration(i)*time.Hour))
	}
	m, err := ForProject(s.ctx, s.d.Server, s.shop, s.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Evals) != 1 || !near(m.Evals[0].Value, 0.5) || m.EvalsUnmeasured != 2 {
		t.Fatalf("evals %v, unmeasured %d", m.Evals, m.EvalsUnmeasured)
	}
}

// The Crew level counts every project: the times the operator stepped in per 100 finished tickets,
// the signals by kind and project, the results by version where a ticket on two versions counts on
// neither, the projects side by side, and what followed each choice of a rewrite (10.7).
func TestTheCrewLevel(t *testing.T) {
	s := newSeed(t)
	s.all()
	c, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Done != 4 || c.Projects != 2 || c.Interventions != 3 || !near(c.InterventionsPer100, 75) {
		t.Fatalf("done %d, projects %d, interventions %d (%v per 100)", c.Done, c.Projects, c.Interventions, c.InterventionsPer100)
	}
	if len(c.Keys) != 2 || c.Keys[0].Key != "SF" || c.Keys[1].Key != "ML" {
		t.Fatalf("keys %v", c.Keys)
	}
	counts := map[string]KindCount{}
	for _, k := range c.Kinds {
		counts[k.Kind.Kind] = k
	}
	if k := counts["operator_intervention"]; k.All != 2 || k.ByProject["SF"] != 2 || k.Label != "Operator intervention" {
		t.Fatalf("interventions %+v", k)
	}
	if k := counts["rework"]; k.All != 2 || k.ByProject["SF"] != 1 || k.ByProject["ML"] != 1 {
		t.Fatalf("fix rounds %+v", k)
	}
	if k := counts[CreatedDiscarded]; k.All != 1 {
		t.Fatalf("created and discarded %+v", k)
	}
	if _, ok := counts["sandbox_startup"]; ok {
		t.Fatal("start-ups are not counted among the signals by kind")
	}
	// SF-3 ran on two versions: mixed. 1.1.0 has SF-2 and ML-1, 1.0.0 has SF-1.
	if c.Mixed != 1 || len(c.Versions) != 2 || c.Versions[0].Version != "1.1.0" || !c.Versions[0].Running || c.Versions[1].Version != "1.0.0" {
		t.Fatalf("versions %+v, mixed %d", c.Versions, c.Mixed)
	}
	v := c.Versions[0]
	if v.Tickets != 2 || v.FirstRequest.N != 1 || !near(v.FixRoundsPerTicket, 0.5) || v.FailedMerge.N != 0 || !near(v.TokensPerTicket, 2500) {
		t.Fatalf("1.1.0 %+v", v)
	}
	if c.FirstRequest != v.FirstRequest {
		t.Fatalf("the headline is not the running version's: %+v", c.FirstRequest)
	}
	if len(c.Comparison) != 2 || c.Comparison[0].Done != 3 || c.Comparison[0].Interventions != 2 || c.Comparison[1].Interventions != 1 || c.Comparison[0].CreatedDiscarded != 1 {
		t.Fatalf("comparison %+v", c.Comparison)
	}
	if len(c.Latest) == 0 || c.Latest[0].Kind != "merged_outside" || c.Latest[0].Project != "ML" || c.Latest[0].Ticket == nil || *c.Latest[0].Ticket != "ML-1" ||
		c.Latest[0].Text != "ML-1 was merged on GitHub" {
		t.Fatalf("latest %+v", c.Latest[0])
	}
	p := c.Prompts
	if len(p.Plans) != 3 || p.Plans[1].Choice != "edited" || p.Plans[1].Plans != 1 || p.Plans[1].SentBack != 1 || p.Plans[1].PassedFirst.Of != 3 ||
		p.Plans[1].PassedFirst.N != 2 || p.Plans[1].Enough || p.Plans[0].Plans != 0 {
		t.Fatalf("prompts %+v", p.Plans)
	}
	if p.Improve.Used != 1 || p.Improve.Refused != 1 || p.Improve.Edited != 0 {
		t.Fatalf("improve %+v", p.Improve)
	}
	if len(c.Use.ByRole) != 2 || c.Use.ByRole[0].Label != "builder" || c.Use.ByRole[0].Value != 8750 {
		t.Fatalf("use %+v", c.Use)
	}
}

// Every row of the Crew level's signals adds up: a signal of no project has a column of its own, and
// the fixture and an archived project, which have no column, are not counted (dave, 0.2.4: three
// choices of a rewrite counted under All and in no column).
func TestTheCrewLevelsRowsAddUp(t *testing.T) {
	s := newSeed(t)
	s.all()
	s.exec(`INSERT INTO signals (kind, detail) VALUES ('rewrite_choice', '{"choice": "edited"}')`)
	s.signal(fixture[1:len(fixture)-1], 0, "rework", `{"reason": "review"}`)
	s.signal(fixture[1:len(fixture)-1], 0, "operator_intervention", `{"what": "edited_before_ok", "text": "Edited FIX-1 before the OK"}`)
	old := s.project("old", "OL")
	s.signal(old, 0, "rework", `{"reason": "review"}`)
	s.exec(`UPDATE projects SET state = 'archived' WHERE id = $1`, old)
	c, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range c.Kinds {
		sum := k.NoProject
		for _, p := range c.Keys {
			sum += k.ByProject[p.Key]
		}
		if k.All != sum {
			t.Errorf("%s: all %d, the columns add up to %d (%+v)", k.Kind.Kind, k.All, sum, k)
		}
		switch k.Kind.Kind {
		case "rewrite_choice":
			if k.All != 1 || k.NoProject != 1 {
				t.Errorf("rewrite choice %+v", k)
			}
		case "rework":
			if k.All != 2 {
				t.Errorf("fix rounds of the fixture or an archived project counted: %+v", k)
			}
		}
	}
	// The headline leaves out the fixture, as its finished tickets do (review of these fixes).
	byWhat := 0.0
	for _, b := range c.InterventionsByWhat {
		byWhat += b.Value
	}
	if c.Interventions != 3 || byWhat != 3 {
		t.Errorf("interventions %d, by what %v: the fixture's counted", c.Interventions, c.InterventionsByWhat)
	}
}

// Latest signals leaves out the start-ups, a measurement Analytics shows (dave, 0.2.4: 21 of its 30
// lines).
func TestTheLatestSignalsLeaveOutStartUps(t *testing.T) {
	s := newSeed(t)
	s.all()
	s.signal(s.shop, 0, "sandbox_startup", `{"seconds": 30}`)
	c, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Latest) == 0 || c.Latest[0].Kind != "merged_outside" {
		t.Fatalf("latest %+v", c.Latest)
	}
	for _, l := range c.Latest {
		if l.Kind == "sandbox_startup" {
			t.Fatalf("a start-up among the latest signals: %+v", l)
		}
	}
}

// Latest signals leaves out the tokens and time of a ticket, a measurement its own card shows, and every
// other line says what its signal holds (dave, 0.2.5: 23 of the 30 lines repeated the signal's name).
func TestTheLatestSignalsSayWhatTheirDetailHolds(t *testing.T) {
	s := newSeed(t)
	ml := s.ticket(s.mail, 7, "done", "agent")
	decision := s.id(`INSERT INTO decisions (project, ticket, role, question, choice, reason) VALUES ($1, $2, 'builder', 'Which flag parser?', 'the standard library', 'no dependency') RETURNING id`,
		s.mail, ml)
	s.signal(s.mail, ml, "cost_and_time", `{"tokens": 1200, "minutes": 49, "outcome": "done"}`)
	s.signal(s.mail, ml, "analysis_decision", `{"choice": "approve", "origin": "agent"}`)
	s.signal(s.mail, ml, "check_rule_not_met", `{"role": "reviewer", "family": "claude"}`)
	s.signal(s.mail, ml, "decided_alone", fmt.Sprintf(`{"decision": %d}`, decision))
	s.signal(s.mail, 0, "rewrite_choice", `{"kind": "first", "round": 2, "state": "ready", "choice": "rewrite", "rewrite": 2}`)
	s.signal(s.mail, ml, "ticket_created", `{"ticket": "ML-8"}`)
	c, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"analysis_decision":  "Approved a ticket an agent created",
		"check_rule_not_met": "The reviewer ran on the claude family too: no CLI in use offers another",
		"decided_alone":      "Which flag parser? The builder chose: the standard library",
		"rewrite_choice":     "You sent Crew's rewrite of your first message",
		"ticket_created":     "An agent created ML-8",
	}
	for _, l := range c.Latest {
		if l.Kind == "cost_and_time" {
			t.Errorf("the tokens and time of a ticket among the latest signals: %+v", l)
		}
		if l.Text == l.Label {
			t.Errorf("%s: What repeats the signal's name", l.Kind)
		}
		if w, ok := want[l.Kind]; ok && l.Text != w {
			t.Errorf("%s: %q, want %q", l.Kind, l.Text, w)
		}
		delete(want, l.Kind)
	}
	if len(want) > 0 {
		t.Errorf("not among the latest signals: %v", want)
	}
}

// The numbers are kept for five minutes and computed again after that, or when they are forgotten.
func TestTheNumbersAreKeptForFiveMinutes(t *testing.T) {
	s := newSeed(t)
	calls := 0
	get := func(at time.Time) *Project {
		m, err := Cached(s.ctx, s.d.Server, "1.1.0", ProjectCacheKey(s.shop), s.shop, at, func() (*Project, error) {
			calls++
			return ForProject(s.ctx, s.d.Server, s.shop, at)
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if get(s.now).Tickets != 0 || calls != 1 {
		t.Fatal("first read")
	}
	s.ticket(s.shop, 1, "backlog", "plan")
	if get(s.now.Add(4*time.Minute)).Tickets != 0 || calls != 1 {
		t.Fatal("computed again within five minutes")
	}
	if get(s.now.Add(6*time.Minute)).Tickets != 1 || calls != 2 {
		t.Fatal("not computed again after five minutes")
	}
	s.ticket(s.shop, 2, "backlog", "plan")
	if err := Forget(s.ctx, s.d.Server); err != nil {
		t.Fatal(err)
	}
	if get(s.now.Add(7*time.Minute)).Tickets != 2 || calls != 3 {
		t.Fatal("not computed again after Forget")
	}
}

// Numbers kept by another version of Crew are computed again: right after an install, and in the
// gate's copy of the database, the kept numbers are the version before's (review of 0.2.5's fixes).
func TestNumbersKeptByAnotherVersionAreComputedAgain(t *testing.T) {
	s := newSeed(t)
	// A row kept by 0.2.4, which kept its numbers under the key alone.
	s.exec(`INSERT INTO metrics (key, value, computed_at) VALUES ('crew', '{}', $1)`, s.now)
	calls := 0
	get := func(v string) {
		t.Helper()
		if _, err := Cached(s.ctx, s.d.Server, v, ProjectCacheKey(s.shop), s.shop, s.now, func() (*Project, error) {
			calls++
			return ForProject(s.ctx, s.d.Server, s.shop, s.now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	get("0.2.4")
	get("0.2.4")
	if calls != 1 {
		t.Fatalf("computed %d times on one version", calls)
	}
	get("0.2.5")
	if calls != 2 {
		t.Fatal("0.2.5 was given the numbers 0.2.4 kept")
	}
	get("0.2.5")
	if calls != 2 {
		t.Fatal("0.2.5 did not keep its own numbers")
	}
	var others int
	if err := s.d.Server.QueryRow(s.ctx, `SELECT count(*) FROM metrics WHERE key NOT LIKE '0.2.5/%'`).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others != 0 {
		t.Fatalf("%d rows of other versions are still kept", others)
	}
}

// Signals are listed by kind, role, project, and time, newest first.
func TestSignalsAreListedByKindRoleProjectAndTime(t *testing.T) {
	s := newSeed(t)
	s.all()
	all, err := Signals(s.ctx, s.d.Server, Filter{})
	if err != nil || len(all) != 9 {
		t.Fatalf("%d signals, %v", len(all), err)
	}
	ml, _ := Signals(s.ctx, s.d.Server, Filter{Project: "ML"})
	if len(ml) != 2 || ml[0].Kind != "merged_outside" || ml[0].Label != "Work merged outside Crew" {
		t.Fatalf("mail %+v", ml)
	}
	d, _ := Signals(s.ctx, s.d.Server, Filter{Kind: CreatedDiscarded})
	if len(d) != 1 || d[0].Ticket == nil || *d[0].Ticket != "SF-5" {
		t.Fatalf("discarded %+v", d)
	}
	if late, _ := Signals(s.ctx, s.d.Server, Filter{Since: time.Now().Add(time.Hour)}); len(late) != 0 {
		t.Fatalf("since: %d", len(late))
	}
	if one, _ := Signals(s.ctx, s.d.Server, Filter{Limit: 1}); len(one) != 1 {
		t.Fatal("limit")
	}
}

// The gate's project crew-gate is Crew's own and hidden (16.2): its tickets, runs, and signals change
// none of the Crew level's numbers, and the improver's list of signals leaves it out, as it leaves out
// the fixture's numbers (M16's fourth phase).
func TestTheGatesProjectIsCountedNowhere(t *testing.T) {
	s := newSeed(t)
	s.all()
	before, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	signalsBefore, err := Signals(s.ctx, s.d.Server, Filter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	gate := s.project("crew-gate", "CGATE")
	s.exec(`UPDATE projects SET hidden = true WHERE id = $1`, gate)
	tk := s.ticket(gate, 1, "done", "operator")
	s.move(tk, "test", "done", s.now.Add(-time.Hour))
	s.run(gate, tk, "builder", "1.1.0", 1500)
	s.signal(gate, tk, "rework", `{"reason": "review"}`)
	s.signal(gate, tk, "operator_intervention", `{"what": "edited_before_ok", "text": "Edited CGATE-1"}`)
	s.signal(gate, tk, "cost_and_time", `{"minutes": 500, "tokens": 1500}`)
	after, err := ForCrew(s.ctx, s.d.Server, "1.1.0", s.now)
	if err != nil {
		t.Fatal(err)
	}
	after.ComputedAt, before.ComputedAt = time.Time{}, time.Time{}
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(before)
	if string(a) != string(b) {
		t.Errorf("the gate's project changed the Crew level:\nbefore %s\nafter  %s", b, a)
	}
	signalsAfter, err := Signals(s.ctx, s.d.Server, Filter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(signalsAfter) != len(signalsBefore) {
		t.Errorf("the gate's signals are listed: %d before, %d after", len(signalsBefore), len(signalsAfter))
	}
	if list, _ := Signals(s.ctx, s.d.Server, Filter{Project: "crew-gate"}); len(list) != 0 {
		t.Errorf("the gate's signals are listed by its name: %+v", list)
	}
}

// A release's suite measures a candidate in a gate, not the project as it runs: Analytics draws it
// nowhere and counts it nowhere (16.2 step 5, 9.6, since 0.52).
func TestAProjectsEvalsLeaveOutAGatesSuites(t *testing.T) {
	s := newSeed(t)
	var earlier int64
	if err := s.d.Server.QueryRow(s.ctx, `INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, result, ended_at)
		VALUES ($1, 'on_demand', 1, 10, 'done', 'operator', '{"baseline": {"cases": 2, "passed": 0.5}}', $2) RETURNING id`, s.shop, s.now.Add(-time.Hour)).Scan(&earlier); err != nil {
		t.Fatal(err)
	}
	for i, result := range []string{`{"baseline": {"cases": 2, "passed": 1}, "release": {"cases": 2, "passed": 0}}`, `{"baseline": {"cases": 0, "passed": 0}}`} {
		s.exec(`INSERT INTO eval_suites (project, purpose, repeats, budget_usd, state, asked_by, result, ended_at, gate, image, compare_to, layer)
			VALUES ($1, 'release', 1, 10, 'done', 'gate', $2::jsonb, $3, $5, 'sha256:`+strings.Repeat("a", 64)+`', $4, '{}')`, s.shop, result, s.now, earlier, fmt.Sprintf("0.3.%d", i))
	}
	m, err := ForProject(s.ctx, s.d.Server, s.shop, s.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Evals) != 1 || !near(m.Evals[0].Value, 0.5) || m.EvalsUnmeasured != 0 {
		t.Fatalf("evals %v, unmeasured %d", m.Evals, m.EvalsUnmeasured)
	}
}

// Added by agents counts as the scope guard does (R32, board.OverLimit): tickets of origin agent that
// Analysis approved, against the plan's; a ticket the backend made itself is none (review of 0.53).
func TestAddedByAgentsCountsAsTheScopeGuard(t *testing.T) {
	s := newSeed(t)
	s.ticket(s.mail, 1, "backlog", "plan")
	s.ticket(s.mail, 2, "backlog", "plan")
	added := s.ticket(s.mail, 3, "in_progress", "agent")
	backend := s.ticket(s.mail, 4, "in_progress", "agent")
	s.exec(`UPDATE tickets SET created_by = 'backend · the project''s setup' WHERE id = $1`, backend)
	m, err := ForProject(s.ctx, s.d.Server, s.mail, s.now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Created.Added.N != 1 || m.Created.Added.Of != 2 || !near(m.Created.Added.Share, 0.5) {
		t.Fatalf("added %+v (ticket %d)", m.Created.Added, added)
	}
}
