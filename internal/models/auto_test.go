package models_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

// autoKit holds a database with agy signed in and its recorded list, one project, and a machine whose
// builder runs on agy/gemini-4-argon first, then agy/gemini-3.8-flash.
type autoKit struct {
	t       *testing.T
	d       *dbtest.DB
	ctx     context.Context
	c       *models.Chooser
	project string
	n       int
	now     time.Time
}

const testProject = "00000000-0000-4000-8000-0000000000f1"

func newAutoKit(t *testing.T, own settings.Doc) *autoKit {
	t.Helper()
	k := &autoKit{t: t, d: dbtest.New(t, false), ctx: context.Background(), project: testProject, now: time.Now()}
	k.d.Server.Exec(k.ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`)
	modelstest.Recorded(t, k.d.Server)
	if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, k.project); err != nil {
		t.Fatal(err)
	}
	m := settings.Machine{Version: "test"}
	def, _ := settings.Defaults(m)
	base := settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"builder": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/gemini-3.8-flash"}, "effort": "high"}}}}
	if err := settings.Save(k.ctx, k.d.Server, m, settings.Merge(settings.Merge(def, base), own), "test"); err != nil {
		t.Fatal(err)
	}
	k.c = &models.Chooser{Pool: k.d.Server, Machine: m, Now: func() time.Time { return k.now }}
	return k
}

// units records n builder tickets on a pick, of which passed passed Crew's checks at the first
// request for a move, each costing cost, the first build started at.
func (k *autoKit) units(n, passed int, cliName, model, effort, from string, cost float64, at time.Time) {
	k.t.Helper()
	for i := 0; i < n; i++ {
		k.n++
		var ticket, sub int64
		if err := k.d.Server.QueryRow(k.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 't', 'feature', 'done', 'plan', 'lead', $3) RETURNING id`, k.project, k.n, int64(k.n)).Scan(&ticket); err != nil {
			k.t.Fatal(err)
		}
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES ('ticket', $1, $2, 1, 'build', 'builder', 'done')
			RETURNING id`, k.project, ticket).Scan(&sub)
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", k.n)
		k.d.Server.Exec(k.ctx, `INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ($1, $2, $3, 'run', $4)`, id, k.project, ticket, cliName)
		name := model + "-" + effort // agy's name holds the effort
		if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, effort, model_from,
				crew_version, image, conversation, cost_usd, started_at, ended_at)
			VALUES ($1, $2, 1, 'ended', $3, $4, $5, $6, $7, 't', 't', $1::uuid, $8, $9::timestamptz, $9::timestamptz + interval '10 minutes')`,
			id, sub, cliName, model, name, effort, from, cost, at); err != nil {
			k.t.Fatal(err)
		}
		state := "failed"
		if i < passed {
			state = "passed"
		}
		k.d.Server.Exec(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, ended_at) VALUES ($1, $2, 'exit', 'c', $3, now())`,
			k.project, ticket, state)
	}
}

func (k *autoKit) builder() ([]string, string) {
	k.t.Helper()
	doc, err := settings.Effective(k.ctx, k.d.Server, settings.Machine{Version: "test"})
	if err != nil {
		k.t.Fatal(err)
	}
	b := doc["agents"].(settings.Doc)["roles"].(settings.Doc)["builder"].(settings.Doc)
	var list []string
	for _, e := range b["model"].([]any) {
		list = append(list, e.(string))
	}
	return list, b["effort"].(string)
}

func (k *autoKit) evaluate() {
	k.t.Helper()
	if err := k.c.Evaluate(k.ctx, "builder"); err != nil {
		k.t.Fatal(err)
	}
}

func (k *autoKit) choices() []models.ChoiceRow {
	k.t.Helper()
	ch, err := models.Choices(k.ctx, k.d.Server, "builder")
	if err != nil {
		k.t.Fatal(err)
	}
	return ch
}

// The rules of 12.3 with scripted numbers: no change below the minimum of runs or where the difference
// is not clear; better by 8 points changes the builder by itself, watched, and once in 7 days; worse
// watched runs take it back, and that entry is not chosen again for 30 days.
func TestCrewChangesTheBuilderByItsNumbers(t *testing.T) {
	k := newAutoKit(t, nil)
	ago := k.now.Add(-48 * time.Hour)
	// 19 tickets on Flash at high: too few, though all passed.
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, ago)
	k.units(19, 19, "agy", "gemini-3.8-flash", "high", "try", 1.0, ago)
	// 20 on Flash at medium, 55% against Argon's 50%: not a clear difference.
	k.units(20, 11, "agy", "gemini-3.8-flash", "medium", "try", 1.0, ago)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" || len(k.choices()) != 0 {
		t.Fatalf("a change below the minimum or with no clear difference: %v", list)
	}
	// The 20th on Flash at high: 100% against 50% is better.
	k.units(1, 1, "agy", "gemini-3.8-flash", "high", "try", 1.0, ago)
	k.evaluate()
	list, effort := k.builder()
	if list[0] != "agy/gemini-3.8-flash" || list[1] != "agy/gemini-4-argon" || effort != "high" {
		t.Fatalf("no change to a better entry: %v %s", list, effort)
	}
	ch := k.choices()
	if len(ch) != 1 || ch[0].Kind != "change" || ch[0].Reason != "better" || ch[0].State != "watching" ||
		ch[0].Numbers.FromStats.Units != 20 || ch[0].Numbers.ToStats.Passed != 20 {
		t.Fatalf("%+v", ch)
	}
	// While it is watched, nothing else changes, and nothing is tried.
	if p, from, _ := k.c.Steer(k.ctx, "builder", "every-ticket", false); from != "" {
		t.Fatalf("a try while a change is watched: %v", p)
	}
	// The next 20 tickets on Flash pass 40%: worse than the 50% Argon had by 8 points. Crew goes back.
	k.units(20, 8, "agy", "gemini-3.8-flash", "high", "automatic", 1.0, k.now.Add(time.Hour))
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("no going back: %v", list)
	}
	ch = k.choices()
	if len(ch) != 2 || ch[0].Kind != "back" || ch[0].Reason != "worse" || ch[0].From.Entry != "agy/gemini-3.8-flash" || ch[1].State != "undone" {
		t.Fatalf("%+v", ch)
	}
	// Flash is not chosen again for 30 days, and not tried: the numbers above would choose it.
	k.now = k.now.Add(8 * 24 * time.Hour)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("an entry gone back from was chosen again: %v", list)
	}
	for i := 0; i < 200; i++ {
		if p, _, _ := k.c.Steer(k.ctx, "builder", fmt.Sprint("t", i), false); p.Entry == "agy/gemini-3.8-flash" {
			t.Fatal("an entry gone back from was tried")
		}
	}
}

// An agent the operator set is never changed: Crew recommends an entry that did better, Not now hides
// it, and Use it takes it and leaves the agent set by the operator. Since 0.53 only better counts.
func TestCrewRecommendsForAnAgentTheOperatorSet(t *testing.T) {
	k := newAutoKit(t, settings.Doc{"agents": settings.Doc{"auto": settings.Doc{"set_by_you": []any{"builder"}}}})
	ago := k.now.Add(-48 * time.Hour)
	k.units(20, 14, "agy", "gemini-4-argon", "high", "operator", 2.0, ago)
	k.units(20, 18, "agy", "gemini-3.8-flash", "high", "limit", 1.0, ago)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("an agent the operator set was changed: %v", list)
	}
	ch := k.choices()
	if len(ch) != 1 || ch[0].Kind != "recommend" || ch[0].Reason != "better" || ch[0].State != "open" {
		t.Fatalf("%+v", ch)
	}
	k.evaluate()
	if len(k.choices()) != 1 {
		t.Fatal("the same recommendation twice")
	}
	if err := k.c.NotNow(k.ctx, ch[0].ID, "op"); err != nil {
		t.Fatal(err)
	}
	k.evaluate()
	if ch = k.choices(); len(ch) != 1 || ch[0].State != "dismissed" {
		t.Fatalf("a hidden recommendation came back: %+v", ch)
	}
	k.now = k.now.Add(31 * 24 * time.Hour)
	k.units(1, 0, "agy", "gemini-4-argon", "high", "operator", 2.0, k.now.Add(-time.Hour))
	k.evaluate()
	if ch = k.choices(); len(ch) != 2 || ch[0].State != "open" {
		t.Fatalf("after Not now's days: %+v", ch)
	}
	if err := k.c.UseIt(k.ctx, ch[0].ID, "op"); err != nil {
		t.Fatal(err)
	}
	doc, _ := settings.Effective(k.ctx, k.d.Server, settings.Machine{Version: "test"})
	if list, _ := k.builder(); list[0] != "agy/gemini-3.8-flash" || !slices.Contains(models.RulesOf(doc).SetByYou, "builder") {
		t.Fatalf("Use it: %v, set by you %v", list, models.RulesOf(doc).SetByYou)
	}
}

// One ticket in ten of an Automatic builder is a try, in turn another entry and the own entry at a
// lower effort, the same way for the same ticket, and never with a pin or with agents.auto off. A
// comparison sends every second ticket to the other pick, and ends at its count.
func TestTriesAndComparisons(t *testing.T) {
	k := newAutoKit(t, nil)
	tries := 0
	for i := 0; i < 1000; i++ {
		p, from, err := k.c.Steer(k.ctx, "builder", fmt.Sprint("SF-", i), false)
		if err != nil {
			t.Fatal(err)
		}
		if from == models.FromTry {
			tries++
			if p.Entry != "agy/gemini-3.8-flash" || p.Effort != "high" {
				t.Fatalf("the first try: %v", p)
			}
		}
	}
	if tries < 70 || tries > 130 {
		t.Fatalf("%d tries in 1,000 tickets", tries)
	}
	var tried string
	for i := 0; tried == ""; i++ {
		if _, from, _ := k.c.Steer(k.ctx, "builder", fmt.Sprint("SF-", i), false); from == models.FromTry {
			tried = fmt.Sprint("SF-", i)
		}
	}
	if _, from, _ := k.c.Steer(k.ctx, "builder", tried, true); from != "" {
		t.Fatal("a try on a pinned ticket")
	}
	if _, from, _ := k.c.Steer(k.ctx, "builder", tried, false); from != models.FromTry {
		t.Fatal("the same ticket was steered another way")
	}
	// The second try, after one: the own entry at the next lower effort.
	k.units(1, 1, "agy", "gemini-3.8-flash", "high", "try", 1.0, k.now.Add(-time.Hour))
	if p, _, _ := k.c.Steer(k.ctx, "builder", tried, false); p.Entry != "agy/gemini-4-argon" || p.Effort != "medium" {
		t.Fatalf("the second try: %v", p)
	}
	off := newAutoKit(t, settings.Doc{"agents": settings.Doc{"auto": settings.Doc{"on": false}}})
	if _, from, _ := off.c.Steer(off.ctx, "builder", tried, false); from != "" {
		t.Fatal("a try with agents.auto off")
	}

	// A comparison: every second ticket goes to the other pick.
	id, err := k.c.StartComparison(k.ctx, "builder", models.Pick{Entry: "agy/gemini-3.8-flash", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.c.StartComparison(k.ctx, "builder", models.Pick{Entry: "agy/gemini-4-argon", Effort: "high"}); err == nil {
		t.Fatal("a comparison of the agent's own pick, or a second at once")
	}
	if p, from, _ := k.c.Steer(k.ctx, "builder", "a", false); from != models.FromComparison || p.Effort != "low" {
		t.Fatalf("the first ticket of a comparison: %v %s", p, from)
	}
	start := k.now.Add(time.Minute)
	k.units(1, 1, "agy", "gemini-3.8-flash", "low", "comparison", 0.5, start)
	if _, from, _ := k.c.Steer(k.ctx, "builder", "b", false); from != "" {
		t.Fatalf("the second ticket of a comparison: %s", from)
	}
	// It ends when each pick has 20 tickets in it; its numbers decide by the same rules: Flash at low
	// passed 19 of 20, against 15 of 20.
	cmps, _ := models.Comparisons(k.ctx, k.d.Server)
	k.d.Server.Exec(k.ctx, `UPDATE comparisons SET started_at = $1 WHERE id = $2`, start.Add(-time.Second), cmps[0].ID)
	k.units(19, 18, "agy", "gemini-3.8-flash", "low", "comparison", 0.5, start)
	k.units(20, 15, "agy", "gemini-4-argon", "high", "automatic", 1.0, start)
	k.evaluate()
	cmps, _ = models.Comparisons(k.ctx, k.d.Server)
	if cmps[0].ID != id || cmps[0].State != "ended" || cmps[0].Numbers.ToStats.Units != 20 || cmps[0].Numbers.FromStats.Units != 20 {
		t.Fatalf("%+v", cmps[0])
	}
	list, effort := k.builder()
	if list[0] != "agy/gemini-3.8-flash" || effort != "low" {
		t.Fatalf("the comparison's numbers did not change the builder: %v %s", list, effort)
	}
	// A comparison can be stopped.
	if err := k.c.StopComparison(k.ctx, id); err == nil {
		t.Fatal("an ended comparison was stopped")
	}
}

// Only the window counts, and a change waits 7 days after the last.
func TestTheWindowAndTheDaysBetweenChanges(t *testing.T) {
	k := newAutoKit(t, nil)
	old := k.now.AddDate(0, 0, -100)
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, k.now.Add(-time.Hour))
	k.units(20, 20, "agy", "gemini-3.8-flash", "high", "try", 1.0, old)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("runs older than the window were counted: %v", list)
	}
	k.units(20, 18, "agy", "gemini-3.8-flash", "medium", "try", 1.0, k.now.Add(-time.Hour))
	k.evaluate()
	if list, effort := k.builder(); list[0] != "agy/gemini-3.8-flash" || effort != "medium" {
		t.Fatalf("no change: %v %s", list, effort)
	}
	k.d.Server.Exec(k.ctx, `UPDATE model_choices SET state = 'kept'`)
	// A better one the next day waits for the 7 days.
	k.units(20, 20, "agy", "gemini-4-argon", "low", "try", 0.1, k.now.Add(-time.Hour))
	k.now = k.now.Add(24 * time.Hour)
	k.evaluate()
	if list, effort := k.builder(); list[0] != "agy/gemini-3.8-flash" || effort != "medium" {
		t.Fatalf("a second change within 7 days: %v %s", list, effort)
	}
	k.now = k.now.Add(7 * 24 * time.Hour)
	k.evaluate()
	if list, effort := k.builder(); list[0] != "agy/gemini-4-argon" || effort != "low" {
		t.Fatalf("after 7 days: %v %s", list, effort)
	}
}

// A checking agent's results count its runs that asked for a fix: an approving review is a run that
// did not, and a test entry that failed is one that did (12.3). Found on dave: an approval read as
// nothing at all.
func TestACheckersResultsCountTheRunsThatAskedForAFix(t *testing.T) {
	k := newAutoKit(t, nil)
	k.units(1, 1, "agy", "gemini-4-argon", "high", "automatic", 1.0, k.now.Add(-time.Hour))
	var ticket int64
	k.d.Server.QueryRow(k.ctx, `SELECT id FROM tickets LIMIT 1`).Scan(&ticket)
	for i, verdict := range []string{"approve", "changes_requested", "approve"} {
		var sub int64
		k.d.Server.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2, $3, 'review', 'reviewer')
			RETURNING id`, k.project, ticket, i+2).Scan(&sub)
		id := fmt.Sprintf("00000000-0000-4000-8000-1000000000%02d", i)
		k.d.Server.Exec(k.ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ($1, 'run', 'agy')`, id)
		if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, effort, crew_version, image, conversation)
			VALUES ($1, $2, 1, 'ended', 'agy', 'gemini-3.8-flash', 'gemini-3.8-flash-high', 'high', 't', 't', $1::uuid)`, id, sub); err != nil {
			t.Fatal(err)
		}
		if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, run, subtask, verdict, body) VALUES ($1, $2, 'review', 'reviewer', $3, $4, $5, 'x')`,
			k.project, ticket, id, sub, verdict); err != nil {
			t.Fatal(err)
		}
	}
	n, err := models.Results(k.ctx, k.d.Server, "reviewer", k.now.AddDate(0, 0, -1))
	if err != nil || len(n) == 0 || n[0].Units != 3 || n[0].AskedFix != 1 {
		t.Fatalf("%+v %v", n, err)
	}
}

// An agent the operator sets while Crew watches a change of its own is the operator's: Crew stops
// watching and never goes back on it, also when the watched tickets did worse (AGENTS.md rule 15,
// security review of M8).
func TestCrewDoesNotGoBackOnAnAgentTheOperatorSetMeanwhile(t *testing.T) {
	k := newAutoKit(t, nil)
	ago := k.now.Add(-48 * time.Hour)
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, ago)
	k.units(20, 20, "agy", "gemini-3.8-flash", "high", "try", 1.0, ago)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-3.8-flash" {
		t.Fatalf("no change: %v", list)
	}
	m := settings.Machine{Version: "test"}
	if err := settings.Update(k.ctx, k.d.Server, m, "op", func(doc settings.Doc) error {
		doc["agents"].(settings.Doc)["auto"].(settings.Doc)["set_by_you"] = []any{"builder"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.units(20, 2, "agy", "gemini-3.8-flash", "high", "automatic", 1.0, k.now.Add(time.Hour))
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-3.8-flash" {
		t.Fatalf("Crew went back on an agent the operator set: %v", list)
	}
	if ch := k.choices(); len(ch) != 1 || ch[0].State != "superseded" {
		t.Fatalf("%+v", ch)
	}
}

// Pause all holds Crew's own changes of a model too (7.9): numbers that would change the builder
// change nothing while Crew is paused, and do after Resume.
func TestPauseAllHoldsCrewsChoices(t *testing.T) {
	k := newAutoKit(t, nil)
	ago := k.now.Add(-48 * time.Hour)
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, ago)
	k.units(20, 20, "agy", "gemini-3.8-flash", "high", "try", 1.0, ago)
	if _, err := k.d.Server.Exec(k.ctx, `UPDATE crew_state SET paused_at = now(), paused_by = 'operator', why = 'pause' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" || len(k.choices()) != 0 {
		t.Fatalf("Crew changed the builder while paused: %v", list)
	}
	if _, err := k.d.Server.Exec(k.ctx, `UPDATE crew_state SET paused_at = NULL, paused_by = NULL, why = NULL WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-3.8-flash" {
		t.Fatalf("no change after Resume: %v", list)
	}
}

// A change made before 0.53 for "as well, for less" keeps its own mirror while it is watched (12.3):
// it goes back when the watched tickets pass more than same_points less than the replaced entry did,
// though that is within the 8 points that would keep a change made for better.
func TestAChangeForLessMadeBefore053KeepsItsMirror(t *testing.T) {
	k := newAutoKit(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"builder": settings.Doc{
		"model": []any{"agy/gemini-3.8-flash", "agy/gemini-4-argon"}, "effort": "high"}}}})
	numbers := `{"from": {"entry": "agy/gemini-4-argon", "effort": "high"}, "to": {"entry": "agy/gemini-3.8-flash", "effort": "high"},
		"fromStats": {"units": 20, "passed": 16}, "toStats": {"units": 20, "passed": 15}}`
	if _, err := k.d.Server.Exec(k.ctx, `INSERT INTO model_choices (role, kind, reason, from_entry, from_effort, to_entry, to_effort, numbers, state, at)
		VALUES ('builder', 'change', 'cheaper', 'agy/gemini-4-argon', 'high', 'agy/gemini-3.8-flash', 'high', $1::jsonb, 'watching', $2)`,
		numbers, k.now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 37 of 50 since: 74%, against the 80% Argon had.
	k.units(50, 37, "agy", "gemini-3.8-flash", "high", "automatic", 1.0, k.now.Add(-time.Hour))
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("no going back by the mirror of as well, for less: %v", list)
	}
	if ch := k.choices(); len(ch) != 2 || ch[0].Kind != "back" || ch[0].Reason != "worse" {
		t.Fatalf("%+v", ch)
	}
}

// Automatic switches and 1-in-10 tries never pick a non-Gemini model for a non-judge role or
// claude-sonnet-5-5 for a judge (12.3, R130).
func TestAutoAndTriesNeverPickDisallowedModels(t *testing.T) {
	k := newAutoKit(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
		"builder":  settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/claude-opus-5-5", "agy/claude-sonnet-5-5"}, "effort": "high"},
		"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/claude-sonnet-5-5", "agy/gemini-4-argon"}, "effort": "high"},
	}}})
	ago := k.now.Add(-48 * time.Hour)
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, ago)
	k.units(20, 20, "agy", "claude-opus-5-5", "high", "try", 1.0, ago)
	k.units(20, 20, "agy", "claude-sonnet-5-5", "high", "try", 1.0, ago)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" || len(k.choices()) != 0 {
		t.Fatalf("builder switched to a Claude model: %v, choices %+v", list, k.choices())
	}
	for i := 0; i < 200; i++ {
		if p, from, _ := k.c.Steer(k.ctx, "builder", fmt.Sprint("SF-", i), false); from == models.FromTry && p.Entry != "agy/gemini-4-argon" {
			t.Fatalf("builder tried a non-Gemini entry: %+v", p)
		}
		if p, from, _ := k.c.Steer(k.ctx, "reviewer", fmt.Sprint("SF-", i), false); from == models.FromTry && p.Entry == "agy/claude-sonnet-5-5" {
			t.Fatalf("reviewer tried claude-sonnet-5-5: %+v", p)
		}
	}
	// A comparison started by the operator on a model reserved for operator choice recommends it when it
	// wins instead of auto-switching an automatic role onto a model Choose refuses to run.
	if _, err := k.c.StartComparison(k.ctx, "builder", models.Pick{Entry: "agy/claude-sonnet-5-5", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	start := k.now.Add(time.Minute)
	cmps, _ := models.Comparisons(k.ctx, k.d.Server)
	k.d.Server.Exec(k.ctx, `UPDATE comparisons SET started_at = $1 WHERE id = $2`, start.Add(-time.Second), cmps[0].ID)
	k.units(20, 19, "agy", "claude-sonnet-5-5", "low", "comparison", 0.5, start)
	k.units(20, 10, "agy", "gemini-4-argon", "high", "automatic", 1.0, start)
	k.evaluate()
	if list, _ := k.builder(); list[0] != "agy/gemini-4-argon" {
		t.Fatalf("comparison auto-switched builder onto claude-sonnet-5-5: %v", list)
	}
	if ch := k.choices(); len(ch) != 1 || ch[0].Kind != "recommend" || ch[0].To.Entry != "agy/claude-sonnet-5-5" {
		t.Fatalf("expected recommendation for operator-only comparison winner: %+v", ch)
	}
}
