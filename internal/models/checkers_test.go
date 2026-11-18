package models_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

// reviews records n reviewer runs on a pick, each on a Done ticket of its own: asked of them asked for
// changes, and escaped of the others passed a ticket that a bug Analysis approved is in (12.3).
func (k *autoKit) reviews(n, asked, escaped int, model, effort, from string, cost float64, at time.Time) {
	k.t.Helper()
	for i := 0; i < n; i++ {
		k.n++
		var ticket, sub int64
		if err := k.d.Server.QueryRow(k.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 't', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`, k.project, k.n, int64(k.n)).Scan(&ticket); err != nil {
			k.t.Fatal(err)
		}
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES ('ticket', $1, $2, 1, 'review', 'reviewer', 'done')
			RETURNING id`, k.project, ticket).Scan(&sub)
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", k.n)
		k.d.Server.Exec(k.ctx, `INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ($1, $2, $3, 'run', 'agy')`, id, k.project, ticket)
		if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, effort, model_from,
				crew_version, image, conversation, cost_usd, started_at, ended_at)
			VALUES ($1, $2, 1, 'ended', 'agy', $3, $4, $5, $6, 't', 't', $1::uuid, $7, $8::timestamptz, $8::timestamptz + interval '5 minutes')`,
			id, sub, model, model+"-"+effort, effort, from, cost, at); err != nil {
			k.t.Fatal(err)
		}
		verdict := "approve"
		if i < asked {
			verdict = "changes_requested"
		}
		if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, run, subtask, verdict, body) VALUES ($1, $2, 'review', 'reviewer', $3, $4, $5, 'x')`,
			k.project, ticket, id, sub, verdict); err != nil {
			k.t.Fatal(err)
		}
		if i >= asked && i < asked+escaped {
			// A bug in the ticket, approved, its ticket confirmed by the analysis of its fix: its fix is in
			// work. A report alone would not count.
			if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO bugs (project, number, title, severity, status, found_by, in_ticket, in_confirmed)
				VALUES ($1, $2, 'b', 'low', 'in_work', 'tester', $3, true)`, k.project, k.n, ticket); err != nil {
				k.t.Fatal(err)
			}
		}
	}
}

func (k *autoKit) reviewer() string {
	k.t.Helper()
	doc, _ := settings.Effective(k.ctx, k.d.Server, settings.Machine{Version: "test"})
	r := doc["agents"].(settings.Doc)["roles"].(settings.Doc)["reviewer"].(settings.Doc)
	return r["model"].([]any)[0].(string) + " · " + r["effort"].(string)
}

var reviewerOnSonnet = settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{
	"model": []any{"agy/claude-sonnet-5-5", "agy/gemini-3.8-flash"}, "effort": "high"}}}}

// An agent that checks joins Crew's choice once bugs are recorded (12.3, M12), and since 0.53 by its
// results alone: an entry whose share of runs that let a defect escape is at least better_points lower
// and whose share of runs that asked for a fix is within five points is taken; one that let as many or
// more escape, or one fewer in twenty, which is noise (security review of 0.53), or that asks for fixes
// far less or far more, is not, and a cost written on its runs, as runs had before, decides nothing.
// Escaped defects count only bugs Analysis approved, in a ticket the run passed.
func TestAnAgentThatChecksIsChosenByTheDefectsThatEscapedIt(t *testing.T) {
	ago := time.Now().Add(-48 * time.Hour)
	for _, c := range []struct {
		name           string
		asked, escaped int
		cost           float64
		changes        bool
	}{
		{"fewer escaped, as many fixes asked", 5, 0, 1.0, true},
		{"one fewer escaped of twenty", 5, 1, 1.0, false},
		{"as many escaped, at half the cost", 5, 2, 0.5, false},
		{"more escaped", 5, 3, 1.0, false},
		{"lenient", 1, 0, 1.0, false},
		{"strict", 9, 0, 1.0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newAutoKit(t, reviewerOnSonnet)
			k.reviews(20, 5, 2, "claude-sonnet-5-5", "high", "automatic", 1.0, ago)
			k.reviews(20, c.asked, c.escaped, "gemini-3.8-flash", "high", "comparison", c.cost, ago)
			if err := k.c.Evaluate(k.ctx, "reviewer"); err != nil {
				t.Fatal(err)
			}
			got := k.reviewer()
			if want := map[bool]string{true: "agy/gemini-3.8-flash · high", false: "agy/claude-sonnet-5-5 · high"}[c.changes]; got != want {
				t.Fatalf("the reviewer runs on %s", got)
			}
			n, err := models.Results(k.ctx, k.d.Server, "reviewer", ago.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range n {
				if x.Model == "gemini-3.8-flash-high" || x.Model == "gemini-3.8-flash" {
					if x.Kind == "" && x.Escaped != c.escaped {
						t.Fatalf("escaped %+v", x)
					}
				}
			}
		})
	}
}

// A change of an agent that checks is watched: when the runs after it let more defects escape, Crew
// goes back.
func TestCrewGoesBackWhenMoreDefectsEscapeAChecker(t *testing.T) {
	k := newAutoKit(t, reviewerOnSonnet)
	ago := k.now.Add(-72 * time.Hour)
	k.reviews(20, 5, 2, "claude-sonnet-5-5", "high", "automatic", 1.0, ago)
	k.reviews(20, 5, 0, "gemini-3.8-flash", "high", "comparison", 1.0, ago)
	if err := k.c.Evaluate(k.ctx, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if k.reviewer() != "agy/gemini-3.8-flash · high" {
		t.Fatalf("no change: %s", k.reviewer())
	}
	k.now = k.now.Add(time.Hour)
	k.reviews(20, 5, 3, "gemini-3.8-flash", "high", "automatic", 0.5, k.now)
	k.now = k.now.Add(time.Hour)
	if err := k.c.Evaluate(k.ctx, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if k.reviewer() != "agy/claude-sonnet-5-5 · high" {
		t.Fatalf("not back: %s", k.reviewer())
	}
	chs, _ := models.Choices(k.ctx, k.d.Server, "reviewer")
	if len(chs) != 2 || chs[0].Kind != "back" || chs[0].Reason != "worse" {
		t.Fatalf("choices %+v", chs)
	}
}

// A bug the operator closed is no defect that escaped: an approved fix ticket of a bug closed as not
// reproducible does not count against the agents that passed its ticket (review of M12).
func TestAClosedBugIsNoEscapedDefect(t *testing.T) {
	k := newAutoKit(t, reviewerOnSonnet)
	ago := time.Now().Add(-48 * time.Hour)
	k.reviews(1, 0, 1, "gemini-3.8-flash", "high", "automatic", 1.0, ago)
	count := func() int {
		n, err := models.Results(k.ctx, k.d.Server, "reviewer", ago.Add(-time.Hour))
		if err != nil || len(n) == 0 {
			t.Fatalf("%+v %v", n, err)
		}
		return n[0].Escaped
	}
	if count() != 1 {
		t.Fatal("the approved bug did not count")
	}
	k.d.Server.Exec(k.ctx, `UPDATE bugs SET status = 'closed', closed_reason = 'not_reproducible'`)
	if n := count(); n != 0 {
		t.Fatalf("a closed bug counts: %d", n)
	}
}

// The ticket a bug names is its reporter's word until the analysis of its fix ticket confirmed it
// (design 0.54, the operator's choice of report 9): an approved fix of a bug whose ticket is not
// confirmed lets no defect escape the agents that passed that ticket.
func TestABugsTicketCountsOnceAnalysisConfirmedIt(t *testing.T) {
	k := newAutoKit(t, reviewerOnSonnet)
	ago := time.Now().Add(-48 * time.Hour)
	k.reviews(1, 0, 1, "gemini-3.8-flash", "high", "automatic", 1.0, ago)
	count := func() int {
		n, err := models.Results(k.ctx, k.d.Server, "reviewer", ago.Add(-time.Hour))
		if err != nil || len(n) == 0 {
			t.Fatalf("%+v %v", n, err)
		}
		return n[0].Escaped
	}
	if count() != 1 {
		t.Fatal("the confirmed bug did not count")
	}
	k.d.Server.Exec(k.ctx, `UPDATE bugs SET in_confirmed = false`)
	if n := count(); n != 0 {
		t.Fatalf("a bug whose ticket is its reporter's word counts: %d", n)
	}
}

// A run of an agent that checks counts one escape at most, however many bugs name the ticket it passed
// (security review of 0.53): two reports on one ticket, which one run can make, are one escape.
func TestARunOfAnAgentThatChecksCountsOneEscapeAtMost(t *testing.T) {
	k := newAutoKit(t, reviewerOnSonnet)
	ago := time.Now().Add(-48 * time.Hour)
	k.reviews(1, 0, 1, "gemini-3.8-flash", "high", "automatic", 1.0, ago)
	if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO bugs (project, number, title, severity, status, found_by, in_ticket, in_confirmed, fix_ticket)
		SELECT project, number + 100, 'the same ticket again', severity, status, found_by, in_ticket, in_confirmed, fix_ticket FROM bugs`); err != nil {
		t.Fatal(err)
	}
	n, err := models.Results(k.ctx, k.d.Server, "reviewer", ago.Add(-time.Hour))
	if err != nil || len(n) == 0 || n[0].Escaped != 1 {
		t.Fatalf("%+v %v", n, err)
	}
}
