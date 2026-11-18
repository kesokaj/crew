package hosting

import (
	"strings"
	"testing"
	"time"
)

// clock is a time a test moves.
type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) pass(d time.Duration) { c.t = c.t.Add(d) }

// Health (plan M19 D11): three misses restart the sandbox, then 60 s of grace; three more make the
// project failing, once per episode, and it is restarted every 10 minutes while failing; an answer
// ends the episode.
func TestHealthRestartsThenFails(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	c := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	h.s.Now = c.now
	sb := SandboxID(h.id, 1)
	down := false
	h.h.answer = func(string) int {
		if down {
			return 500
		}
		return 200
	}
	round := func() {
		t.Helper()
		if err := h.s.HealthRound(h.ctx); err != nil {
			t.Fatal(err)
		}
		c.pass(HealthEvery)
	}
	state := func() (string, int, *time.Time) {
		var s string
		var n int
		var since *time.Time
		h.db.Server.QueryRow(h.ctx, `SELECT state, restarts, down_since FROM hosting WHERE project = $1`, h.id).Scan(&s, &n, &since)
		return s, n, since
	}
	round()
	down = true
	round()
	round()
	if h.rt.restarts[sb] != 0 {
		t.Fatal("restarted after two misses")
	}
	round()
	if s, n, _ := state(); h.rt.restarts[sb] != 1 || n != 1 || s != "live" {
		t.Fatalf("after three misses: %s, %d restarts", s, n)
	}
	for range 4 { // the grace: 60 s, four rounds of 15 s
		round()
	}
	if s, _, _ := state(); s != "live" {
		t.Fatalf("failing within the grace: %s", s)
	}
	round()
	round()
	round()
	s, _, since := state()
	if s != "failing" || since == nil {
		t.Fatalf("after three more misses: %s %v", s, since)
	}
	first := *since
	for range 10 {
		round()
	}
	if _, _, since := state(); !since.Equal(first) || h.rt.restarts[sb] != 1 {
		t.Fatalf("a second episode within one: %v, %d restarts", since, h.rt.restarts[sb])
	}
	c.pass(FailingRestartEvery)
	round()
	if h.rt.restarts[sb] != 2 {
		t.Fatalf("no restart after 10 minutes failing: %d", h.rt.restarts[sb])
	}
	c.pass(HealthGrace)
	down = false
	round()
	if s, _, since := state(); s != "live" || since != nil {
		t.Fatalf("an answer did not end the episode: %s %v", s, since)
	}
	logs := h.logs()
	for _, want := range []string{"did not answer: restarted", "stopped answering", "answers again"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the log has no %q:\n%s", want, logs)
		}
	}
}

// A sandbox that answers again after its restart starts a new count: one miss is not a failure.
func TestAnAnswerAfterARestartStartsAgain(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	c := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	h.s.Now = c.now
	answers := []int{500, 500, 500, 200, 500, 500, 200}
	i := 0
	h.h.answer = func(string) int { return answers[min(i, len(answers)-1)] }
	for i = range answers {
		h.s.HealthRound(h.ctx)
		c.pass(HealthGrace)
	}
	var s string
	h.db.Server.QueryRow(h.ctx, `SELECT state FROM hosting WHERE project = $1`, h.id).Scan(&s)
	if s != "live" || h.rt.restarts[SandboxID(h.id, 1)] != 1 {
		t.Fatalf("%s, %d restarts", s, h.rt.restarts[SandboxID(h.id, 1)])
	}
}

// While a deploy runs, health asks nothing and restarts nothing: the deploy may have paused or
// stopped what serves (review of M19, finding 2).
func TestHealthLeavesAProjectWhoseDeployRuns(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	c := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	h.s.Now = c.now
	h.h.answer = func(string) int { return 500 }
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO deploys (project, number, commit, why, asked_by, state) VALUES ($1, 2, $2, 'asked', 'operator', 'starting')`, h.id, head1); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		h.s.HealthRound(h.ctx)
		c.pass(HealthEvery)
	}
	var s string
	h.db.Server.QueryRow(h.ctx, `SELECT state FROM hosting WHERE project = $1`, h.id).Scan(&s)
	if h.rt.restarts[SandboxID(h.id, 1)] != 0 || s != "live" {
		t.Fatalf("health acted during a deploy: %d restarts, %s", h.rt.restarts[SandboxID(h.id, 1)], s)
	}
}
