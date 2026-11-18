package board_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// limitsProject is a running project whose approved plan has plan tickets, with the share of tickets
// agents may add given.
func limitsProject(e *env, plan int, share float64) *board.Project {
	e.t.Helper()
	p := e.project("shop", "SF")
	for i := 0; i < plan; i++ {
		e.add(p, ready(fmt.Sprintf("Part %d", i+1)), board.OriginPlan)
	}
	e.ok(p)
	doc, err := e.b.Settings(e.ctx, e.db.Server, p)
	if err != nil {
		e.t.Fatal(err)
	}
	doc["limits"].(settings.Doc)["max_added_share"] = share
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		e.t.Fatal(err)
	}
	return e.reload(p)
}

func (e *env) reload(p *board.Project) *board.Project {
	e.t.Helper()
	got, err := e.b.Project(e.ctx, p.Name)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// spend records a run of the project with this cost written on it, as runs before 0.53 had.
func (e *env) spend(p *board.Project, usd float64) {
	e.t.Helper()
	if _, err := e.db.Server.Exec(e.ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			VALUES ('project', $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE project = $1 AND ticket IS NULL), 'breakdown', 'lead') RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, cost_usd, cost_basis)
		SELECT gen_random_uuid(), id, 1, 'ended', 'agy', 'v', 'img', $2, 'estimated' FROM s`, p.ID, usd); err != nil {
		e.t.Fatal(err)
	}
}

// addTicket adds a ticket in Analysis with the origin and the analysis' status given.
func (e *env) addTicket(p *board.Project, origin, status string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Server.QueryRow(e.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, analysis)
		VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM tickets WHERE project = $1), 'x', 'feature', 'analysis', $2, 'lead', 99, jsonb_build_object('status', $3::text))
		RETURNING id`, p.ID, origin, status).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// No limit is in dollars (7.7, since 0.53): what a project's runs cost, as written on them before,
// pauses nothing.
func TestAProjectsSpendPausesNothing(t *testing.T) {
	e := newEnv(t)
	p := limitsProject(e, 4, 0.25)
	e.spend(p, 1e6)
	if did, err := e.b.HoldLimits(e.ctx, p); err != nil || did != "" {
		t.Fatalf("%q %v", did, err)
	}
	if over, err := e.b.OverLimit(e.ctx, e.db.Server, p); err != nil || over != "" {
		t.Fatalf("%q %v", over, err)
	}
}

// A project pauses when the tickets agents added pass their share of the tickets of the approved plan,
// with the reason in words; your Resume is refused while it is still passed; raising the share resumes
// it (R32, 7.7; 22, "Limits of a project").
func TestAProjectPausesPastItsShareAndRaisingItResumes(t *testing.T) {
	e := newEnv(t)
	p := limitsProject(e, 4, 0.25) // one ticket of four
	e.addTicket(p, "agent", board.StatusReady)
	if did, err := e.b.HoldLimits(e.ctx, p); err != nil || did != "" {
		t.Fatalf("one added: %q %v", did, err)
	}
	e.addTicket(p, "agent", board.StatusReady)
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "paused" {
		t.Fatalf("two added: %q %v", did, err)
	}
	p = e.reload(p)
	if p.State != "paused" || p.PauseReason == nil || *p.PauseReason != "Agents added 2 tickets, over 25% of the plan's 4" {
		t.Fatalf("%s %v", p.State, p.PauseReason)
	}
	if crew, _ := board.PausedByCrew(e.ctx, e.db.Server, p.ID); !crew {
		t.Fatal("the pause is not the backend's")
	}
	_, err := e.b.SetState(e.ctx, p.Name, "resume", "", board.Operator)
	if kindOf(err) != board.Refused || !strings.Contains(err.Error(), "Raise the limit") {
		t.Fatalf("Resume while over the share: %v", err)
	}
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["limits"].(settings.Doc)["max_added_share"] = 0.5
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "resumed" || e.reload(p).State != "running" {
		t.Fatalf("after raising the share: %q %v", did, err)
	}
}

// A project you paused yourself is never resumed by the backend, and your Resume of it works.
func TestAProjectYouPausedStaysPaused(t *testing.T) {
	e := newEnv(t)
	p := limitsProject(e, 4, 0.25)
	if _, err := e.b.SetState(e.ctx, p.Name, "pause", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "" || e.reload(p).State != "paused" {
		t.Fatalf("%q %v", did, err)
	}
	if _, err := e.b.SetState(e.ctx, p.Name, "resume", "", board.Operator); err != nil {
		t.Fatal(err)
	}
}

// Work agents added counts each ticket of origin agent that Analysis approved; a replacement, a
// discarded ticket, one not approved yet, one the backend made itself, and the operator's own, also
// through a token or a conversation, do not count. A project with no tickets of its plan has no share
// to pass (R32, 7.7).
func TestWorkAgentsAddedCountsTicketsOfAgents(t *testing.T) {
	e := newEnv(t)
	p := limitsProject(e, 2, 0.25) // half a ticket of two
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.db.Server.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	replacement := e.addTicket(p, "agent", board.StatusReady)
	exec(`INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, (SELECT min(id) FROM tickets WHERE project = $2), 'replaces')`, replacement, p.ID)
	discarded := e.addTicket(p, "agent", board.StatusReady)
	exec(`UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE id = $1`, discarded)
	e.addTicket(p, "agent", board.StatusAnalysing)
	e.addTicket(p, "token", board.StatusReady)
	e.addTicket(p, "conversation", board.StatusReady)
	e.addTicket(p, "operator", board.StatusReady)
	// The backend's own ticket, a fix of the project's setup, has origin agent and is no agent's work.
	setup := e.addTicket(p, "agent", board.StatusReady)
	exec(`UPDATE tickets SET created_by = 'backend · the project''s setup' WHERE id = $1`, setup)
	if over, err := e.b.OverLimit(e.ctx, e.db.Server, p); err != nil || over != "" {
		t.Fatalf("none counts: %q %v", over, err)
	}
	e.addTicket(p, "agent", board.StatusNotice)
	over, err := e.b.OverLimit(e.ctx, e.db.Server, p)
	if err != nil || over != "Agents added 1 ticket, over 25% of the plan's 2" {
		t.Fatalf("%q %v", over, err)
	}
	if did, _ := e.b.HoldLimits(e.ctx, p); did != "paused" {
		t.Fatalf("%q", did)
	}
	// With no ticket of a plan, as Crew's own project has, nothing agents add passes a share.
	none := e.project("crewish", "CR")
	exec(`UPDATE projects SET state = 'running' WHERE id = $1`, none.ID)
	for i := 0; i < 5; i++ {
		e.addTicket(none, "agent", board.StatusReady)
	}
	if over, err := e.b.OverLimit(e.ctx, e.db.Server, e.reload(none)); err != nil || over != "" {
		t.Fatalf("a project with no plan's tickets: %q %v", over, err)
	}
}

// On a project with no plan tickets, limits.max_added_tickets bounds how many open approved agent
// tickets may accumulate at once; closing one, raising the limit, or setting it to 0 resumes the
// project, and replacements and backend tickets do not count (7.7).
func TestAProjectWithNoPlanPausesPastMaxAddedTickets(t *testing.T) {
	e := newEnv(t)
	p := e.project("crewish", "CR")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.db.Server.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`UPDATE projects SET state = 'running' WHERE id = $1`, p.ID)
	doc, err := e.b.Settings(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatal(err)
	}
	doc["limits"].(settings.Doc)["max_added_tickets"] = 2
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	first := e.addTicket(p, "agent", board.StatusReady)
	e.addTicket(p, "agent", board.StatusNotice)
	// Replacements and backend tickets do not count toward max_added_tickets.
	rep := e.addTicket(p, "agent", board.StatusReady)
	exec(`INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, 'replaces')`, rep, first)
	setup := e.addTicket(p, "agent", board.StatusReady)
	exec(`UPDATE tickets SET created_by = 'backend · the project''s setup' WHERE id = $1`, setup)
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "" {
		t.Fatalf("at limit 2: %q %v", did, err)
	}
	e.addTicket(p, "agent", board.StatusReady)
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "paused" {
		t.Fatalf("past limit 2: %q %v", did, err)
	}
	p = e.reload(p)
	if p.PauseReason == nil || *p.PauseReason != "Agents have 3 open added tickets, over the limit of 2" {
		t.Fatalf("pause reason = %v", p.PauseReason)
	}
	// Closing one open ticket brings open added back to 2 and resumes the project.
	exec(`UPDATE tickets SET stage = 'done' WHERE id = $1`, first)
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "resumed" || e.reload(p).State != "running" {
		t.Fatalf("after one reached Done: %q %v", did, err)
	}
	// Exceeding again and setting max_added_tickets to 0 disables the cap and resumes.
	e.addTicket(p, "agent", board.StatusReady)
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "paused" {
		t.Fatalf("paused again: %q %v", did, err)
	}
	doc, _ = e.b.Settings(e.ctx, e.db.Server, p)
	doc["limits"].(settings.Doc)["max_added_tickets"] = 0
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	if did, err := e.b.HoldLimits(e.ctx, e.reload(p)); err != nil || did != "resumed" || e.reload(p).State != "running" {
		t.Fatalf("after disabling with 0: %q %v", did, err)
	}
}
