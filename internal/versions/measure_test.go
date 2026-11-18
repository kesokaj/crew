package versions_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/versions"
)

// installed records a release as installed at at, in state, with the request of its install from
// from; "" records no request.
func (k *kit) installed(version, state, from string, at time.Time) {
	k.t.Helper()
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state, installed_at)
		VALUES ($1, 'c', 'm', '{}', $2, 'code', $3, $2)
		ON CONFLICT (version) DO UPDATE SET state = $3, installed_at = $2`, version, at, state)
	if from != "" {
		k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at, ended_at)
			VALUES ((SELECT coalesce(max(id), 0) + 1 FROM update_requests), $1, $2, 'install', 'operator', 'done', $3, $3)`, version, from, at)
	}
}

func (k *kit) exec(q string, args ...any) {
	k.t.Helper()
	if _, err := k.d.Server.Exec(context.Background(), q, args...); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
}

// done records n Done tickets of the project shop, each with one run on version; refused gives each a
// failed first request. hidden puts them in the gate's crew-gate.
func (k *kit) done(version string, n int, refused, hidden bool) {
	k.t.Helper()
	ctx := context.Background()
	name, key := "shop", "SF"
	if hidden {
		name, key = "crew-gate", "GATE"
	}
	var project string
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source, hidden)
		VALUES (gen_random_uuid(), $1, $2, 'running', 'local', $3) ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING id`,
		name, key, hidden).Scan(&project); err != nil {
		k.t.Fatal(err)
	}
	for range n {
		var ticket, sub int64
		if err := k.d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM tickets WHERE project = $1), 'T', 'feature', 'done', 'operator', 'lead', 1)
			RETURNING id`, project).Scan(&ticket); err != nil {
			k.t.Fatal(err)
		}
		if err := k.d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role)
			VALUES ('ticket', $1, $2, 1, 'build', 'builder') RETURNING id`, project, ticket).Scan(&sub); err != nil {
			k.t.Fatal(err)
		}
		k.exec(`INSERT INTO runs (id, subtask, number, cli, crew_version, image, state, started_at)
			VALUES (gen_random_uuid(), $1, 1, 'agy', $2, 'img', 'ended', now())`, sub, version)
		k.exec(`INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, at) VALUES ($1, 'test', 'done', 'backend', clock_timestamp())`, ticket)
		if refused {
			k.exec(`INSERT INTO signals (project, ticket, kind) VALUES ($1, $2, 'check_round')`, project, ticket)
		}
	}
}

// measured is a release's measure, nil while it has none.
func (k *kit) measured(version string) *versions.ReleaseMeasure {
	k.t.Helper()
	var raw []byte
	if err := k.d.Server.QueryRow(context.Background(), `SELECT measure FROM releases WHERE version = $1`, version).Scan(&raw); err != nil {
		k.t.Fatal(err)
	}
	if raw == nil {
		return nil
	}
	var m versions.ReleaseMeasure
	if err := json.Unmarshal(raw, &m); err != nil {
		k.t.Fatal(err)
	}
	return &m
}

func (k *kit) count(q string, args ...any) int {
	k.t.Helper()
	var n int
	if err := k.d.Server.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		k.t.Fatal(err)
	}
	return n
}

// Every release is compared with the one it replaced at its first install, on the six metrics of the
// tickets done wholly on each, 20 each (10.7, since 0.57; the operator's answers 5 to 8 of the
// thirteenth session): once 20 ran on it while it runs, and with fewer once it no longer runs. A
// release without an improvement is measured too. A worse one is kept as worse, once, however many
// passes, and nothing asks the updater for anything: Crew does not roll a release back by itself.
func TestAReleaseIsMeasuredAgainstTheOneItReplaced(t *testing.T) {
	k := newKit(t, "0.2.1")
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	k.installed("0.2.0", "kept", "0.1.9", t0)
	k.installed("0.2.1", "running", "0.2.0", t0.Add(24*time.Hour))
	k.done("0.2.0", 20, false, false)
	k.done("0.2.1", 19, true, false)
	k.done("0.2.1", 3, true, true) // the gate's, which count on no release
	requests := k.count(`SELECT count(*) FROM update_requests`)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.2.1"); m != nil {
		t.Fatalf("0.2.1 runs with 19 tickets on it, and was measured: %+v", m)
	}
	// 0.2.0 no longer runs: it is measured with what it has against 0.1.9, on which nothing ran.
	if m := k.measured("0.2.0"); m == nil || m.Verdict != measure.Inconclusive || m.Before != "0.1.9" || m.TicketsAfter != 20 || m.TicketsBefore != 0 ||
		m.Window != 20 || len(m.Results) != 6 {
		t.Fatalf("0.2.0: %+v", m)
	}
	k.done("0.2.1", 1, true, false)
	for range 3 {
		if err := k.s.Measure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	m := k.measured("0.2.1")
	if m == nil || m.Verdict != measure.Worse || m.Before != "0.2.0" || m.TicketsBefore != 20 || m.TicketsAfter != 20 || len(m.Results) != 6 {
		t.Fatalf("0.2.1: %+v", m)
	}
	names := map[string]string{}
	for _, r := range m.Results {
		names[r.Metric] = r.Verdict
		if r.Metric == measure.RewritesUsed {
			t.Fatal("a release is measured on the rewrites' metric")
		}
	}
	if names["first_request_pass_rate"] != measure.Worse || names["passed_first_time_rate"] != measure.Worse || names["fix_rounds_per_ticket"] != measure.Same ||
		!strings.Contains(m.Why, "Checks passed on the first request went from 100% to 0%") {
		t.Fatalf("the metrics: %v; why %q", names, m.Why)
	}
	// Measured once: a later pass changes nothing.
	at := func() time.Time {
		var t0 time.Time
		k.d.Server.QueryRow(ctx, `SELECT measured_at FROM releases WHERE version = '0.2.1'`).Scan(&t0)
		return t0
	}
	first := at()
	k.done("0.2.1", 5, false, false)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if !at().Equal(first) || k.measured("0.2.1").Verdict != measure.Worse {
		t.Fatal("a measured release was measured again")
	}
	if n := k.count(`SELECT count(*) FROM update_requests`); n != requests {
		t.Fatalf("the measure asked the updater: %d requests, %d before", n, requests)
	}
	if st, _ := k.u.Status(10); len(st.Requests) != 0 || st.Current != nil {
		t.Fatalf("the updater was asked: %+v", st)
	}
}

// The same tickets measure the same, and better ones better; the margins are 10 points of a share and
// 20% of a number per ticket.
func TestAReleaseMeasuresTheSameOrBetter(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		before, after bool
		want          string
	}{"the same": {false, false, measure.Same}, "better": {true, false, measure.Better}} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t, "0.2.2")
			t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			k.installed("0.2.1", "kept", "0.2.0", t0)
			k.installed("0.2.2", "running", "0.2.1", t0.Add(time.Hour))
			k.done("0.2.1", 20, c.before, false)
			k.done("0.2.2", 20, c.after, false)
			if err := k.s.Measure(ctx); err != nil {
				t.Fatal(err)
			}
			m := k.measured("0.2.2")
			if m == nil || m.Verdict != c.want {
				t.Fatalf("%+v", m)
			}
			for _, r := range m.Results {
				want := 0.20
				if r.Metric == "first_request_pass_rate" || r.Metric == "passed_first_time_rate" {
					want = 0.10
				}
				if r.Margin != want {
					t.Errorf("%s has the margin %v", r.Metric, r.Margin)
				}
			}
		})
	}
}

// A release with no release known before it is not compared: the machine's first install, and a
// request with no release it replaced or one naming the release itself. A later install of a measured
// release, and a rollback, change nothing.
func TestAReleaseWithNoReleaseBeforeIsNotCompared(t *testing.T) {
	k := newKit(t, "0.3.0")
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	k.installed("0.1.0", "kept", "", t0)
	k.installed("0.1.1", "kept", "0.1.1", t0.Add(time.Hour))
	k.installed("0.1.2", "kept", "", t0.Add(2*time.Hour))
	k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at) VALUES (90, '0.1.2', NULL, 'install', 'root', 'done', now())`)
	// 0.2.0 was installed from 0.1.2, later again from 0.3.0 after a rollback: the first is what it replaced.
	k.installed("0.2.0", "kept", "0.1.2", t0.Add(3*time.Hour))
	k.installed("0.3.0", "kept", "0.2.0", t0.Add(4*time.Hour))
	k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at) VALUES (99, '0.2.0', '0.3.0', 'rollback', 'operator', 'done', now())`)
	k.installed("0.2.0", "kept", "0.3.0", t0.Add(5*time.Hour))
	k.installed("0.3.0", "running", "0.2.0", t0.Add(6*time.Hour))
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		if m := k.measured(v); m == nil || m.Verdict != versions.NotCompared || m.Before != "" || len(m.Results) != 0 ||
			m.Why != "Crew does not know what ran before it" {
			t.Errorf("%s: %+v", v, m)
		}
	}
	if m := k.measured("0.2.0"); m == nil || m.Before != "0.1.2" {
		t.Fatalf("0.2.0: %+v", m)
	}
	if m := k.measured("0.3.0"); m != nil {
		t.Fatalf("0.3.0 runs with no ticket, and was measured: %+v", m)
	}
	// One that was never installed is not measured.
	k.exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state) VALUES ('0.4.0', 'c', 'm', '{}', now(), 'code', 'waiting')`)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.4.0"); m != nil {
		t.Fatalf("0.4.0: %+v", m)
	}
}

// The versions service's pass measures the releases, after it follows the updater, and Versions shows
// each release's measure with the number of tickets it waits for.
func TestThePassMeasuresAndVersionsShowsIt(t *testing.T) {
	k := newKit(t, "0.1.0")
	ctx := context.Background()
	k.installed("0.0.9", "kept", "", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err := k.s.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.0.9"); m == nil || m.Verdict != versions.NotCompared {
		t.Fatalf("the pass measured 0.0.9: %+v", m)
	}
	scr, err := k.s.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scr.MeasureAfter != 20 {
		t.Fatalf("the screen's window: %d", scr.MeasureAfter)
	}
	for _, r := range scr.Releases {
		if r.Version == "0.0.9" && (r.Measure == nil || r.Measure.Verdict != versions.NotCompared) {
			t.Fatalf("0.0.9 on the screen: %+v", r.Measure)
		}
	}
	// improvements.measure_after_tasks is the window of a release too.
	k.exec(`INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"measure_after_tasks": 5}}', 'test')`)
	k.installed("0.0.7", "kept", "0.0.6", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	k.installed("0.0.8", "kept", "0.0.7", time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	k.done("0.0.7", 5, false, false)
	k.done("0.0.8", 5, false, false)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.0.8"); m == nil || m.Verdict != measure.Same || m.Window != 5 || m.TicketsAfter != 5 {
		t.Fatalf("0.0.8 over a window of 5: %+v", m)
	}
	if scr, _ := k.s.View(ctx); scr.MeasureAfter != 5 {
		t.Fatalf("the screen's window: %d", scr.MeasureAfter)
	}
}

// A release that runs is never decided "not compared": its install's request is done only after the
// updater watched it for ten minutes, and the new server's first pass comes before that (0.2.26 on
// dave was stored as "not compared" at its start, though its request named 0.2.25). It waits, as one
// with too few tickets does, and is compared once the request is done.
func TestARunningReleaseWaitsForItsRequest(t *testing.T) {
	k := newKit(t, "0.3.1")
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	k.installed("0.3.0", "kept", "0.2.9", t0)
	k.installed("0.3.1", "running", "", t0.Add(time.Hour))
	k.exec(`INSERT INTO update_requests (id, version, from_version, action, asked_by, state, asked_at) VALUES (500, '0.3.1', '0.3.0', 'install', 'crew-ctl', 'running', now())`)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.3.1"); m != nil {
		t.Fatalf("0.3.1 runs, its request not done yet, and was measured: %+v", m)
	}
	k.exec(`UPDATE update_requests SET state = 'done', ended_at = now() WHERE id = 500`)
	k.done("0.3.0", 20, false, false)
	k.done("0.3.1", 20, false, false)
	if err := k.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k.measured("0.3.1"); m == nil || m.Before != "0.3.0" || m.Verdict != measure.Same {
		t.Fatalf("0.3.1 once its request is done: %+v", m)
	}
	// One that runs with no request at all, the machine's first install, waits too, and is not
	// compared once it no longer runs.
	k2 := newKit(t, "0.1.0")
	k2.installed("0.1.0", "running", "", t0)
	if err := k2.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k2.measured("0.1.0"); m != nil {
		t.Fatalf("the first install, running, was measured: %+v", m)
	}
	k2.exec(`UPDATE releases SET state = 'kept' WHERE version = '0.1.0'`)
	if err := k2.s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if m := k2.measured("0.1.0"); m == nil || m.Verdict != versions.NotCompared {
		t.Fatalf("the first install, no longer running: %+v", m)
	}
}
