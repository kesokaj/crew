package models_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	. "github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

func readRecorded(t *testing.T, d *dbtest.DB, clis ...string) {
	t.Helper()
	modelstest.Recorded(t, d.Server, clis...)
}

func signIn(t *testing.T, d *dbtest.DB, state string, clis ...string) {
	t.Helper()
	for _, c := range clis {
		if _, err := d.Server.Exec(context.Background(), `INSERT INTO cli_logins (cli, state, checked_at) VALUES ($1, $2, now())
			ON CONFLICT (cli) DO UPDATE SET state = $2`, c, state); err != nil {
			t.Fatal(err)
		}
	}
}

func machine(t *testing.T, own settings.Doc) settings.Doc {
	t.Helper()
	def, err := settings.Defaults(settings.Machine{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return settings.Merge(def, own)
}

type want struct {
	cli, model, name, effort, from, effortFrom string
	flag, off                                  bool
}

func check(t *testing.T, what string, c Choice, err error, w want) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	got := want{c.CLI, c.Model, c.Name, c.Effort, c.ModelFrom, c.EffortFrom, c.Flag, c.Off}
	if got != w {
		t.Fatalf("%s:\n got %+v\nwant %+v", what, got, w)
	}
}

func TestEveryRunIsChosenFromWhatTheCLIsList(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	doc := machine(t, nil)
	choose := func(a Ask) (Choice, error) {
		if a.Machine == nil {
			a.Machine = doc
		}
		return Choose(ctx, d.Server, a)
	}

	// Nothing signed in: nothing runs.
	if _, err := choose(Ask{Role: "lead"}); err == nil {
		t.Fatal("a choice with no CLI signed in")
	}
	// Signed in, with its list never read: agy has no short list, so it runs nothing (12.3).
	signIn(t, d, "signed_in", "agy")
	if _, err := choose(Ask{Role: "lead"}); err == nil {
		t.Fatal("agy ran before its list was read")
	}

	readRecorded(t, d, "agy")
	// The lead's and the planner's first offered entry is Gemini 4 Argon, with the effort in the name.
	c, err := choose(Ask{Role: "lead"})
	check(t, "the lead", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromList, FromList, false, false})
	c, err = choose(Ask{Role: "planner"})
	check(t, "the planner", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromList, FromList, false, false})
	c, err = choose(Ask{Role: "builder"})
	check(t, "the builder", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromAutomatic, FromAutomatic, false, false})
	// Smaller builder tasks with distinct rules (Bracket: "flash") prefer the flash-bracket entry on the builder's list.
	c, err = choose(Ask{Role: "builder", Bracket: "flash"})
	check(t, "a smaller builder task with Bracket flash", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-high", "high", FromAutomatic, FromAutomatic, false, false})
	if c.Bracket != "flash" {
		t.Fatalf("smaller builder task bracket = %q, want flash", c.Bracket)
	}
	// agy offers no gemini-3.1-pro on these accounts: the builder skips a non-Gemini entry and runs on
	// its next Gemini entry (12.3, 0.33, 1.40).
	gone := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"builder": settings.Doc{"model": []any{"agy/gemini-3.1-pro", "agy/claude-sonnet-5-5", "agy/gemini-3.8-flash"}}}}})
	c, err = choose(Ask{Role: "builder", Machine: gone})
	check(t, "a builder whose first entry is gone", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-high", "high", FromAutomatic, FromAutomatic, false, false})
	// The tester: the name with its effort.
	c, err = choose(Ask{Role: "tester"})
	check(t, "the tester", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
	// An agent the operator set: Crew leaves it alone.
	set := machine(t, settings.Doc{"agents": settings.Doc{"auto": settings.Doc{"set_by_you": []any{"tester"}}}})
	c, err = choose(Ask{Role: "tester", Machine: set})
	check(t, "set by you", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromOperator, FromOperator, false, false})

	// A pin on the ticket comes first, then the project's setting, model and effort one by one.
	c, err = choose(Ask{Role: "tester", Pin: settings.Doc{"model": "agy/claude-sonnet-5-5"}, Project: settings.Doc{"agents": settings.Doc{"tester": settings.Doc{"effort": "low"}}}})
	check(t, "a pin and a project", c, err, want{"agy", "claude-sonnet-5-5", "claude-sonnet-5-5-low", "low", FromTicket, FromProject, false, false})
	c, err = choose(Ask{Role: "tester", Pin: settings.Doc{"effort": "high"}})
	check(t, "a pinned effort", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-high", "high", FromAutomatic, FromTicket, false, false})
	// An effort the model does not offer: the nearest below, else above; one with no levels runs with none.
	c, err = choose(Ask{Role: "tester", Pin: settings.Doc{"effort": "max"}})
	check(t, "the nearest effort", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-high", "high", FromAutomatic, FromNearest, false, false})
	// A name the operator added that ends in no effort is a model with no levels.
	if err := Add(ctx, d.Server, "agy", "gemini-exp-1206"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "summary", Pin: settings.Doc{"model": "agy/gemini-exp-1206"}})
	check(t, "a model with no levels", c, err, want{"agy", "gemini-exp-1206", "gemini-exp-1206", "", FromTicket, FromNearest, false, false})
	c, err = choose(Ask{Role: "tester", Pin: settings.Doc{"model": "agy/gpt-oss-120b", "effort": "low"}})
	check(t, "one fixed effort", c, err, want{"agy", "gpt-oss-120b", "gpt-oss-120b-medium", "medium", FromTicket, FromNearest, false, false})
	// A pin that names a model no CLI lists is ignored, and the machine's entry runs.
	c, err = choose(Ask{Role: "tester", Pin: settings.Doc{"model": "agy/gemini-3.1-pro"}})
	check(t, "a gone pin", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
	if !slices.Equal(c.Ignored, []string{"agy/gemini-3.1-pro"}) {
		t.Fatalf("the gone pin was not said: %v", c.Ignored)
	}
	// A CLI the installation does not use is not chosen: here one that names another CLI than agy.
	other := machine(t, settings.Doc{"agents": settings.Doc{"clis": []any{"other"}}})
	if c, err := choose(Ask{Role: "lead", Machine: other}); err == nil {
		t.Fatalf("a CLI the installation does not use was chosen: %+v", c)
	}
}

func TestARunMovesAtALimitOrALostSignIn(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	doc := machine(t, nil)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	choose := func(a Ask) (Choice, error) {
		if a.Machine == nil {
			a.Machine = doc
		}
		return Choose(ctx, d.Server, a)
	}
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	// agy's Claude and GPT group at its limit, as Crew's own login check read it: the judge moves from
	// Sonnet to its next entry in another group, Gemini 3.8 Flash (12.7).
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err := choose(Ask{Role: "judge"})
	check(t, "the judge at the Claude group's limit", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-low", "low", FromLimit, FromList, false, false})
	// With at_limit wait, it waits for the reset, and says until when.
	_, err = choose(Ask{Role: "judge", Project: settings.Doc{"agents": settings.Doc{"at_limit": "wait"}}})
	if w, ok := IsWait(err); !ok || !w.Until.Equal(reset) || !strings.Contains(w.Reason, "limit") {
		t.Fatalf("waiting: %v", err)
	}
	// A pin is the operator's choice: at a limit it is never moved, and its wait reason names the model
	// in its bracket that has room (12.7, rule 15).
	_, err = choose(Ask{Role: "lead", Pin: settings.Doc{"model": "agy/claude-sonnet-5-5"}})
	if w, ok := IsWait(err); !ok || !w.Until.Equal(reset) || !strings.Contains(w.Reason, WaitBracketHasRoom+"agy/gemini-3.8-flash") {
		t.Fatalf("a pin at a limit: %v", err)
	}

	// A reset that passed frees the group.
	d.Server.Exec(ctx, `UPDATE quota SET reached_until = now() - interval '1 second'`)
	c, err = choose(Ask{Role: "judge"})
	check(t, "after the reset", c, err, want{"agy", "claude-sonnet-5-5", "claude-sonnet-5-5-low", "low", FromList, FromList, false, false})

	// The Gemini group of agy at its limit: non-judge, non-planning roles stay on Gemini and wait for
	// its reset instead of moving onto Claude (12.3, 12.7, R130).
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	_, err = choose(Ask{Role: "tester"})
	if w, ok := IsWait(err); !ok || !w.Until.Equal(reset) || !strings.Contains(w.Reason, "Gemini") {
		t.Fatalf("the tester at Gemini's limit: %v", err)
	}
	// A review of a Sonnet build prefers Gemini, which is at its limit: instead of waiting, it falls
	// back to the pro bracket model in the builder's family that has room, with Off true so Crew records
	// a notice; with at_limit wait it waits for the reset.
	c, err = choose(Ask{Role: "reviewer", Avoid: "claude"})
	check(t, "a check whose other family is at its limit falls back", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromLimit, FromAutomatic, false, true})
	_, err = choose(Ask{Role: "reviewer", Avoid: "claude", Project: settings.Doc{"agents": settings.Doc{"at_limit": "wait"}}})
	if w, ok := IsWait(err); !ok || !strings.Contains(w.Reason, "another family") || !w.Until.Equal(reset) {
		t.Fatalf("a check whose other family is at its limit with at_limit wait: %v", err)
	}
	// A read that says the window is free again frees it.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.1}}, "login check"); err != nil {
		t.Fatal(err)
	}
	// The Gemini group close to its limit while 3p has room: the tester stays on Gemini and never moves
	// onto Sonnet (12.3, 12.7).
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "tester"})
	check(t, "close on Gemini, stays on Gemini", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
	// Both groups close to their limit: one run at a time works on Gemini, the others wait (12.7).
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "tester"})
	check(t, "close on both groups, with no run there", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
	var sub int64
	d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, number, kind, role) VALUES ('crew', 1, 'run', 'tester') RETURNING id`).Scan(&sub)
	d.Server.Exec(ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ('00000000-0000-4000-8000-000000000001', 'run', 'agy')`)
	if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image, conversation)
		VALUES ('00000000-0000-4000-8000-000000000001', $1, 1, 'working', 'agy', 'gemini-3.8-flash', 't', 't', '00000000-0000-4000-8000-000000000001')`, sub); err != nil {
		t.Fatal(err)
	}
	if _, err = choose(Ask{Role: "tester"}); err == nil || !strings.Contains(err.Error(), "close to their limit") {
		t.Fatalf("a second run on a group close to its limit: %v", err)
	}
	// Other groups with no working run are not held back.
	c, err = choose(Ask{Role: "judge"})
	check(t, "another group", c, err, want{"agy", "claude-sonnet-5-5", "claude-sonnet-5-5-low", "low", FromList, FromList, false, false})
	d.Server.Exec(ctx, `UPDATE runs SET state = 'ended'`)
	// agy signed out: nothing can run, and the tester waits.
	signIn(t, d, "signed_out", "agy")
	if _, err := choose(Ask{Role: "tester"}); err == nil {
		t.Fatal("a run was chosen with agy signed out")
	} else if _, ok := IsWait(err); !ok {
		t.Fatalf("agy signed out: %v", err)
	}
}

func TestACheckRunsOnAnotherFamilyThanTheBuild(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	doc := machine(t, nil)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	choose := func(a Ask) (Choice, error) {
		if a.Machine == nil {
			a.Machine = doc
		}
		return Choose(ctx, d.Server, a)
	}
	// A Sonnet build: the reviewer takes its list's first entry of another family, Gemini 4 Argon
	// (Q100, 0.41), and records that the rule moved it.
	c, err := choose(Ask{Role: "reviewer", Avoid: "claude"})
	check(t, "a review of a Sonnet build", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromCheckRule, FromAutomatic, false, false})
	// The operator's own choice for a project is moved by the rule too, and the run says so (12.3).
	own := settings.Doc{"agents": settings.Doc{"reviewer": settings.Doc{"model": "agy/claude-sonnet-5-5"}}}
	c, err = choose(Ask{Role: "reviewer", Avoid: "claude", Project: own})
	check(t, "the project's reviewer on the builder's family", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromCheckRule, FromAutomatic, false, false})
	// Unless the project switched the rule off.
	own["agents"].(settings.Doc)["checks_use_another_family"] = false
	c, err = choose(Ask{Role: "reviewer", Avoid: "claude", Project: own})
	check(t, "the rule off", c, err, want{"agy", "claude-sonnet-5-5", "claude-sonnet-5-5-high", "high", FromProject, FromAutomatic, false, false})
	// A Gemini build: the reviewer's first entry is of another family already.
	c, err = choose(Ask{Role: "reviewer", Avoid: "gemini"})
	check(t, "a review of a Gemini build", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromAutomatic, FromAutomatic, false, false})
	// The other family offered, at its limit: the check falls back to the builder's family with Off true,
	// and waits when at_limit wait is set or both groups are at their limit.
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "reviewer", Avoid: "claude"})
	check(t, "a check while the other family is at its limit falls back", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromLimit, FromAutomatic, false, true})
	if _, err := choose(Ask{Role: "reviewer", Avoid: "claude", Project: settings.Doc{"agents": settings.Doc{"at_limit": "wait"}}}); err == nil || !strings.Contains(err.Error(), "agy/gemini-4-argon") {
		t.Fatalf("a check while the other family is at its limit with at_limit wait: %v", err)
	}
	d.Server.Exec(ctx, `DELETE FROM quota`)
	// No entry of the reviewer offers another family: the check runs where it was, and says the rule
	// was not met.
	claudeOnly := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/claude-sonnet-5-5"}}}}})
	c, err = choose(Ask{Role: "reviewer", Avoid: "claude", Machine: claudeOnly})
	check(t, "no other family offered", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromAutomatic, FromAutomatic, false, true})
	// A tester runs commands: the rule does not apply to it.
	c, err = choose(Ask{Role: "tester", Avoid: "gemini"})
	check(t, "a tester", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
}

func TestTheRuleForChecksTakesNoFamilyCrewDoesNotKnowAsAnother(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	if err := Add(ctx, d.Server, "agy", "nova-1-high"); err != nil {
		t.Fatal(err)
	}
	// Moving off the builder's family skips a listed entry of an unknown family and takes the next
	// entry of a known family.
	withNova := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
		"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/nova-1", "agy/gemini-4-argon"}, "effort": "high"},
	}}})
	c, err := Choose(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Machine: withNova})
	check(t, "skip nova-1 when moving off claude", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromCheckRule, FromAutomatic, false, false})
	if cannot, err := RuleCannotBeMet(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Machine: withNova}); err != nil || cannot {
		t.Fatalf("RuleCannotBeMet with gemini-4-argon after nova-1: %v %v", cannot, err)
	}
	// With only the builder's family and an unknown family on the list, no other known family is
	// listed: RuleCannotBeMet is true and Choose keeps the first entry with Off true.
	claudeAndNova := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
		"reviewer": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/nova-1"}, "effort": "high"},
	}}})
	if cannot, err := RuleCannotBeMet(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Machine: claudeAndNova}); err != nil || !cannot {
		t.Fatalf("RuleCannotBeMet with only claude and nova-1: %v %v", cannot, err)
	}
	c, err = Choose(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Machine: claudeAndNova})
	check(t, "only claude and nova-1", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromAutomatic, FromAutomatic, false, true})
	// Prefer on an unknown family is refused for a check or a try, and taken for an operator comparison.
	def := machine(t, nil)
	c, err = Choose(ctx, d.Server, Ask{Role: "reviewer", Avoid: "gemini", Prefer: "agy/nova-1", PreferFrom: FromTry, Machine: def})
	check(t, "a check refuses Prefer on nova-1", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromAutomatic, FromAutomatic, false, false})
	c, err = Choose(ctx, d.Server, Ask{Role: "builder", Prefer: "agy/nova-1", PreferFrom: FromTry, Machine: def})
	check(t, "a builder refuses a try on nova-1", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromAutomatic, FromAutomatic, false, false})
	c, err = Choose(ctx, d.Server, Ask{Role: "builder", Prefer: "agy/nova-1", PreferFrom: FromComparison, Machine: def})
	check(t, "a builder takes an operator comparison on nova-1", c, err, want{"agy", "nova-1", "nova-1-high", "high", FromComparison, FromComparison, false, false})
	// At a limit, a check does not move onto an entry of an unknown family.
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	geminiAndNova := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
		"reviewer": settings.Doc{"model": []any{"agy/gemini-4-argon", "agy/nova-1"}, "effort": "high"},
	}}})
	if _, err := Choose(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Machine: geminiAndNova}); err == nil {
		t.Fatal("a check at Gemini's limit moved onto nova-1")
	}
	d.Server.Exec(ctx, `DELETE FROM quota`)
	// The operator's own pin on an unknown family is not moved away by Choose (the guard makes it wait).
	c, err = Choose(ctx, d.Server, Ask{Role: "reviewer", Avoid: "claude", Pin: settings.Doc{"model": "agy/nova-1"}, Machine: def})
	check(t, "a pin on nova-1 stays on nova-1", c, err, want{"agy", "nova-1", "nova-1-high", "high", FromTicket, FromAutomatic, false, false})
	// Agents reports waits on CheckRule when the builder or the check is on an unknown family.
	novaBuilder := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{
		"builder": settings.Doc{"model": []any{"agy/nova-1"}, "effort": "high"},
	}}})
	views, err := Agents(ctx, d.Server, novaBuilder)
	if err != nil {
		t.Fatal(err)
	}
	rev := views[slices.IndexFunc(views, func(v AgentView) bool { return v.Role == "reviewer" })]
	if !strings.HasPrefix(rev.CheckRule, "waits:") || !strings.Contains(rev.CheckRule, cli.UnknownFamily) {
		t.Fatalf("Agents CheckRule with builder on nova-1: %q", rev.CheckRule)
	}
}

// A list is kept as the CLI gave it: a model it stops listing is marked gone and kept, a read that
// fails keeps the last list and counts, and a refused name stays out until the lists are read again.
func TestTheListsAreKept(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	readRecorded(t, d, "agy")
	rows, err := Rows(ctx, d.Server)
	if err != nil || len(rows) != 13 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	gemini := rows[slices.IndexFunc(rows, func(r Row) bool { return r.Name == "gemini-3.8-flash-high" })]
	if gemini.Model != "gemini-3.8-flash" || gemini.Family != "gemini" || gemini.Group != "gemini" || !gemini.InName || gemini.Display == "" {
		t.Fatalf("%+v", gemini)
	}
	if err := Record(ctx, d.Server, "agy", []cli.Model{{Name: "gemini-3.8-flash-high", Model: "gemini-3.8-flash", Efforts: []string{"high"}, InName: true}}, ""); err != nil {
		t.Fatal(err)
	}
	var offered, gone int
	d.Server.QueryRow(ctx, `SELECT count(*) FILTER (WHERE offered), count(*) FILTER (WHERE gone_at IS NOT NULL) FROM models`).Scan(&offered, &gone)
	if offered != 1 || gone != 12 {
		t.Fatalf("offered %d, gone %d", offered, gone)
	}
	// Two failures in a row are counted; a good read starts the count again.
	for range FailuresForNews {
		if err := Record(ctx, d.Server, "agy", nil, "agy models failed"); err != nil {
			t.Fatal(err)
		}
	}
	var failures int
	d.Server.QueryRow(ctx, `SELECT models_failures FROM cli_logins WHERE cli = 'agy'`).Scan(&failures)
	d.Server.QueryRow(ctx, `SELECT count(*) FILTER (WHERE offered) FROM models`).Scan(&offered)
	if failures != 2 || offered != 1 {
		t.Fatalf("failures %d, offered %d", failures, offered)
	}
	readRecorded(t, d, "agy")
	d.Server.QueryRow(ctx, `SELECT models_failures FROM cli_logins WHERE cli = 'agy'`).Scan(&failures)
	d.Server.QueryRow(ctx, `SELECT count(*) FILTER (WHERE offered), count(*) FILTER (WHERE gone_at IS NOT NULL) FROM models`).Scan(&offered, &gone)
	if failures != 0 || offered != 13 || gone != 0 {
		t.Fatalf("after a good read: failures %d, offered %d, gone %d", failures, offered, gone)
	}
	// A name the CLI refused in a run is passed over for a while, then tried again; the operator's
	// Refresh offers it again at once. A run's report is its own account, so it never takes a model
	// away for good (security review of M8).
	signIn(t, d, "signed_in", "agy")
	doc := machine(t, nil)
	pin := Ask{Role: "tester", Machine: doc, Pin: settings.Doc{"model": "agy/gpt-oss-120b"}}
	if err := Refused(ctx, d.Server, "agy", "gpt-oss-120b-medium"); err != nil {
		t.Fatal(err)
	}
	readRecorded(t, d, "agy")
	if c, err := Choose(ctx, d.Server, pin); err != nil || c.Model == "gpt-oss-120b" || len(c.Ignored) != 1 {
		t.Fatalf("a refused name was chosen: %+v %v", c, err)
	}
	d.Server.Exec(ctx, `UPDATE models SET refused_at = now() - interval '16 minutes' WHERE refused`)
	if c, err := Choose(ctx, d.Server, pin); err != nil || c.Name != "gpt-oss-120b-medium" {
		t.Fatalf("a name refused long ago was not tried again: %+v %v", c, err)
	}
	if err := Refused(ctx, d.Server, "agy", "gpt-oss-120b-medium"); err != nil {
		t.Fatal(err)
	}
	if err := ClearRefused(ctx, d.Server); err != nil {
		t.Fatal(err)
	}
	if c, err := Choose(ctx, d.Server, pin); err != nil || c.Name != "gpt-oss-120b-medium" {
		t.Fatalf("a refresh did not offer the refused name again: %+v %v", c, err)
	}
	// A name the operator added stays when the list does not have it.
	var on bool
	if err := Add(ctx, d.Server, "agy", "gemini-3.1-pro-high"); err != nil {
		t.Fatal(err)
	}
	readRecorded(t, d, "agy")
	d.Server.QueryRow(ctx, `SELECT offered FROM models WHERE name = 'gemini-3.1-pro-high'`).Scan(&on)
	if !on {
		t.Fatal("a name the operator added was marked gone")
	}
	// And runs where it is named.
	if c, err := Choose(ctx, d.Server, Ask{Role: "lead", Machine: doc, Pin: settings.Doc{"model": "agy/gemini-3.1-pro"}}); err != nil || c.Name != "gemini-3.1-pro-high" || c.Effort != "high" || c.Short {
		t.Fatalf("the added name: %+v %v", c, err)
	}
}

// What a run says about the limits is its own account (12.4): a run's report, true or forged, holds a
// group for 15 minutes at most and makes no group close to its limit for longer; only Crew's own
// login check holds a group to its reset, and no reset is further than a week (security review of M8).
func TestARunsReportHoldsAGroupBriefly(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	far := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	agy, _ := cli.Get("agy")
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "weekly", Used: 1, Reached: true, ResetsAt: far}}, "run"); err != nil {
		t.Fatal(err)
	}
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.99, ResetsAt: far}}, "run"); err != nil {
		t.Fatal(err)
	}
	g, _ := LoadGuard(ctx, d.Server, 0.8)
	until, limited := g.Limited(agy, "claude-opus-5-5")
	if !limited || time.Until(until) > RetryAfter+time.Second || !g.Close(agy, "gemini-3.8-flash") {
		t.Fatalf("a run's report: limited %v until %v, close %v", limited, until, g.Close(agy, "gemini-3.8-flash"))
	}
	d.Server.Exec(ctx, `UPDATE quota SET read_at = now() - interval '16 minutes'`)
	g, _ = LoadGuard(ctx, d.Server, 0.8)
	if _, limited := g.Limited(agy, "claude-opus-5-5"); limited || g.Close(agy, "gemini-3.8-flash") || g.CLIClose("agy") {
		t.Fatal("a run's report held its group past 15 minutes")
	}
	// A subtask that ends at a limit waits no longer for a run's report than the report holds.
	d.Server.Exec(ctx, `UPDATE quota SET read_at = now()`)
	if wait, err := Reached(ctx, d.Server, "agy", "3p", "", time.Time{}); err != nil || time.Until(wait) > RetryAfter+time.Second {
		t.Fatalf("a run's report made a subtask wait until %v: %v", wait, err)
	}
	// The login check's read holds the group to its reset, a week at most.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: far}}, "login check"); err != nil {
		t.Fatal(err)
	}
	g, _ = LoadGuard(ctx, d.Server, 0.8)
	if until, limited := g.Limited(agy, "claude-sonnet-5-5"); !limited || until.After(time.Now().Add(MaxWindow)) || until.Before(time.Now().Add(MaxWindow-time.Minute)) {
		t.Fatalf("the login check's read: %v %v", limited, until)
	}
	// A run's report neither frees what the check holds nor turns it into a report of its own.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.1}}, "run"); err != nil {
		t.Fatal(err)
	}
	if _, err := Reached(ctx, d.Server, "agy", "3p", "5h", time.Time{}); err != nil {
		t.Fatal(err)
	}
	g, _ = LoadGuard(ctx, d.Server, 0.8)
	var source string
	d.Server.QueryRow(ctx, `SELECT source FROM quota WHERE cli = 'agy' AND grp = '3p' AND win = '5h'`).Scan(&source)
	if until, limited := g.Limited(agy, "claude-sonnet-5-5"); !limited || until.Before(time.Now().Add(MaxWindow-time.Minute)) || source != "login check" {
		t.Fatalf("a run's report changed the check's hold: %v %v %s", limited, until, source)
	}
}

// A name a CLI lists is kept only in a shape that cannot read as a flag (security review of M8).
func TestANameOfAnotherShapeIsNotKept(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	if err := Record(ctx, d.Server, "agy", []cli.Model{{Name: "gemini-3.8-flash-high", Model: "gemini-3.8-flash"}, {Name: "--dangerously", Model: "--dangerously"}, {Name: "a b", Model: "a b"}}, ""); err != nil {
		t.Fatal(err)
	}
	rows, _ := Rows(ctx, d.Server)
	if len(rows) != 1 || rows[0].Name != "gemini-3.8-flash-high" {
		t.Fatalf("%+v", rows)
	}
	if err := Record(ctx, d.Server, "agy", []cli.Model{{Name: "-x", Model: "-x"}}, ""); err != nil {
		t.Fatal(err)
	}
	var failures int
	d.Server.QueryRow(ctx, `SELECT models_failures FROM cli_logins WHERE cli = 'agy'`).Scan(&failures)
	if rows, _ = Rows(ctx, d.Server); len(rows) != 1 || !rows[0].Offered || failures != 1 {
		t.Fatalf("a list of no good name replaced the last: %+v, failures %d", rows, failures)
	}
}

// When the group of the model a run would take is at its limit or at or above quota.slow_down_at (0.8),
// Choose moves the run to a model of the same bracket in another limit group that has room; for a check,
// it prefers another family when that family can run, and falls back to the builder's family with Off
// true when the other family is at its limit (12.3, 12.7).
func TestARunMovesWithinItsBracket(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	doc := machine(t, nil)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	choose := func(a Ask) (Choice, error) {
		if a.Machine == nil {
			a.Machine = doc
		}
		return Choose(ctx, d.Server, a)
	}
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	// 1. Planner uses Gemini directly without touching 3p; with 3p at its limit, a reviewer of a Gemini build falls back to Gemini in pro.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err := choose(Ask{Role: "planner"})
	check(t, "planner uses Gemini directly", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromList, FromList, false, false})
	if c.Bracket != "pro" {
		t.Fatalf("planner bracket = %q, want pro", c.Bracket)
	}
	// A reviewer of a gemini-4-argon build with 3p at its limit falls back to gemini-4-argon in pro with Off true.
	c, err = choose(Ask{Role: "reviewer", Avoid: "gemini"})
	check(t, "reviewer of Gemini build with 3p at limit falls back", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromLimit, FromAutomatic, false, true})
	if cannot, err := RuleCannotBeMet(ctx, d.Server, Ask{Role: "reviewer", Avoid: "gemini", Machine: doc}); err != nil || !cannot {
		t.Fatalf("RuleCannotBeMet with 3p at limit: %v %v", cannot, err)
	}
	// With agents.at_limit: wait, a reviewer whose first entry is claude-opus-5-5 waits for 3p.
	if _, err := choose(Ask{Role: "reviewer", Avoid: "gemini", Project: settings.Doc{"agents": settings.Doc{"at_limit": "wait"}}}); err == nil {
		t.Fatal("at_limit wait moved the reviewer")
	}

	// 2. When 3p is at 0.85 (>= quota.slow_down_at) and Gemini has room, non-pinned checks fall back to
	// Gemini in the same bracket with FromLimit and Off true; when Gemini is also >= slow_down_at (no
	// model in the bracket has room), the reviewer keeps cross-family claude-opus-5-5 with Off false;
	// and an operator project pin on Claude stays on Claude even when Gemini has room (safety rule 15).
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "planner"})
	check(t, "planner with 3p at 0.85", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromList, FromList, false, false})
	c, err = choose(Ask{Role: "reviewer", Avoid: "gemini"})
	check(t, "reviewer of Gemini build with 3p at 0.85 falls back to Gemini with room", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromLimit, FromAutomatic, false, true})
	if cannot, err := RuleCannotBeMet(ctx, d.Server, Ask{Role: "reviewer", Avoid: "gemini", Machine: doc}); err != nil || !cannot {
		t.Fatalf("RuleCannotBeMet with 3p at 0.85 and Gemini free: %v %v", cannot, err)
	}
	c, err = choose(Ask{Role: "reviewer", Avoid: "gemini", Project: settings.Doc{"agents": settings.Doc{"reviewer": settings.Doc{"model": "agy/claude-opus-5-5"}}}})
	check(t, "project-pinned Claude reviewer stays on Claude at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromProject, FromAutomatic, false, false})
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "reviewer", Avoid: "gemini"})
	check(t, "reviewer keeps cross-family Claude when both 3p and Gemini are at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromAutomatic, FromAutomatic, false, false})
	if cannot, err := RuleCannotBeMet(ctx, d.Server, Ask{Role: "reviewer", Avoid: "gemini", Machine: doc}); err != nil || cannot {
		t.Fatalf("RuleCannotBeMet when both 3p and Gemini are at 0.85: %v %v", cannot, err)
	}

	// 3. With Gemini at 0.85, non-judge, non-planning roles stay on Gemini and never move onto Claude;
	// at a per-model limit they fall back along their Gemini entries, and at the Gemini group limit they wait.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "builder"})
	check(t, "builder with Gemini at 0.85 stays on Gemini", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromAutomatic, FromAutomatic, false, false})
	if c.Bracket != "pro" {
		t.Fatalf("builder bracket = %q, want pro", c.Bracket)
	}
	c, err = choose(Ask{Role: "tester"})
	check(t, "tester with Gemini at 0.85 stays on Gemini", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-medium", "medium", FromAutomatic, FromAutomatic, false, false})
	if c.Bracket != "flash" {
		t.Fatalf("tester bracket = %q, want flash", c.Bracket)
	}
	c, err = choose(Ask{Role: "summary"})
	check(t, "summary with Gemini at 0.85 stays on Gemini", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-low", "low", FromList, FromList, false, false})
	d.Server.Exec(ctx, `DELETE FROM quota`)
	for _, eff := range []string{"low", "medium", "high"} {
		if err := Refused(ctx, d.Server, "agy", "gemini-4-argon-"+eff); err != nil {
			t.Fatal(err)
		}
	}
	c, err = choose(Ask{Role: "builder"})
	check(t, "builder with gemini-4-argon refused moves to gemini-3.8-flash", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-high", "high", FromAutomatic, FromAutomatic, false, false})
	d.Server.Exec(ctx, `UPDATE models SET refused_at = NULL`)
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"lead", "builder", "tester", "e2e", "summary", "prompt", "assistant", "improver"} {
		if _, err := choose(Ask{Role: role}); err == nil {
			t.Fatalf("%s moved onto Claude at Gemini group limit", role)
		}
	}

	// 4. Even when the planner's own list has only one entry, it moves to the Gemini model in its bracket in agents.brackets.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	oneEntry := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"planner": settings.Doc{"model": []any{"agy/claude-opus-5-5"}, "effort": "high"}}}})
	c, err = choose(Ask{Role: "planner", Machine: oneEntry})
	check(t, "planner with one entry moves via agents.brackets", c, err, want{"agy", "gemini-4-argon", "gemini-4-argon-high", "high", FromLimit, FromList, false, false})
	// A judge runs on claude-sonnet-5-5 against a Gemini builder and falls back to gemini-3.8-flash when 3p is at its limit.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	c, err = choose(Ask{Role: "judge", Avoid: "gemini"})
	check(t, "judge against Gemini builder", c, err, want{"agy", "claude-sonnet-5-5", "claude-sonnet-5-5-low", "low", FromList, FromList, false, false})
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err = choose(Ask{Role: "judge", Avoid: "gemini"})
	check(t, "judge with 3p at limit falls back to Gemini flash", c, err, want{"agy", "gemini-3.8-flash", "gemini-3.8-flash-low", "low", FromLimit, FromList, false, true})

	// 5. gpt-oss-120b is in the single-entry oss bracket, and an unclassified model (nova-1) is in no bracket: neither moves to another model.
	d.Server.Exec(ctx, `DELETE FROM quota`)
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	gptRole := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"lead": settings.Doc{"model": []any{"agy/gpt-oss-120b"}, "effort": "medium"}}}})
	c, err = choose(Ask{Role: "lead", Machine: gptRole})
	check(t, "oss bracket model at 0.85", c, err, want{"agy", "gpt-oss-120b", "gpt-oss-120b-medium", "medium", FromList, FromList, false, false})
	if c.Bracket != "oss" {
		t.Fatalf("gpt-oss-120b Bracket = %q, want oss", c.Bracket)
	}
	if err := Add(ctx, d.Server, "agy", "nova-1-medium"); err != nil {
		t.Fatal(err)
	}
	novaRole := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"lead": settings.Doc{"model": []any{"agy/nova-1"}, "effort": "medium"}}}})
	c, err = choose(Ask{Role: "lead", Machine: novaRole})
	if err != nil || c.Bracket != "agy/nova-1" {
		t.Fatalf("unbracketed model = %+v, %v", c, err)
	}
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	if _, err := choose(Ask{Role: "lead", Machine: gptRole}); err == nil {
		t.Fatal("oss model with no peer moved at limit")
	}
}

// A pin on the ticket (FromTicket), a project's setting (FromProject), and an agent in set_by_you or
// with auto.on false (FromOperator) are never moved to another model at a limit or above slow_down_at;
// at their group's limit they wait, and when another model in their bracket has room the wait reason
// names it (12.3, 12.7, safety rule 15).
func TestTheOperatorsChoiceOfAModelIsNeverMoved(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	doc := machine(t, nil)
	signIn(t, d, "signed_in", "agy")
	readRecorded(t, d, "agy")
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	// Above slow_down_at (0.85), all three keep claude-opus-5-5 instead of moving to gemini-4-argon.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 0.85, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	c, err := Choose(ctx, d.Server, Ask{Role: "builder", Machine: doc, Pin: settings.Doc{"model": "agy/claude-opus-5-5"}})
	check(t, "pin at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromTicket, FromAutomatic, false, false})

	projSet := settings.Doc{"agents": settings.Doc{"builder": settings.Doc{"model": "agy/claude-opus-5-5"}}}
	c, err = Choose(ctx, d.Server, Ask{Role: "builder", Machine: doc, Project: projSet})
	check(t, "project setting at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromProject, FromAutomatic, false, false})

	setByYou := machine(t, settings.Doc{"agents": settings.Doc{
		"roles": settings.Doc{"builder": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}, "effort": "high"}},
		"auto":  settings.Doc{"set_by_you": []any{"builder"}},
	}})
	c, err = Choose(ctx, d.Server, Ask{Role: "builder", Machine: setByYou})
	check(t, "set_by_you at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromOperator, FromOperator, false, false})

	autoOff := machine(t, settings.Doc{"agents": settings.Doc{
		"roles": settings.Doc{"builder": settings.Doc{"model": []any{"agy/claude-opus-5-5", "agy/gemini-4-argon"}, "effort": "high"}},
		"auto":  settings.Doc{"on": false},
	}})
	c, err = Choose(ctx, d.Server, Ask{Role: "builder", Machine: autoOff})
	check(t, "auto.on false at 0.85", c, err, want{"agy", "claude-opus-5-5", "claude-opus-5-5-high", "high", FromOperator, FromOperator, false, false})

	// At the 3p limit while Gemini has room, all of them wait and their reason names agy/gemini-4-argon.
	if err := RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "3p", Window: "5h", Used: 1, Reached: true, ResetsAt: reset}}, "login check"); err != nil {
		t.Fatal(err)
	}
	for name, ask := range map[string]Ask{
		"pin":             {Role: "builder", Machine: doc, Pin: settings.Doc{"model": "agy/claude-opus-5-5"}},
		"project setting": {Role: "builder", Machine: doc, Project: projSet},
		"set_by_you":      {Role: "builder", Machine: setByYou},
		"auto.on false":   {Role: "builder", Machine: autoOff},
		"pinned reviewer": {Role: "reviewer", Avoid: "gemini", Machine: doc, Pin: settings.Doc{"model": "agy/claude-opus-5-5"}},
	} {
		_, err := Choose(ctx, d.Server, ask)
		w, ok := IsWait(err)
		if !ok || !w.Until.Equal(reset) || !strings.Contains(w.Reason, WaitBracketHasRoom+"agy/gemini-4-argon") {
			t.Fatalf("%s at limit: %v", name, err)
		}
	}
}
