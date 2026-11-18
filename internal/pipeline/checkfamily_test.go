package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

// familyKit is the setup mark kit with agy signed in, its models recorded, the machine's defaults, and
// runs that are recorded, not started.
func familyKit(t *testing.T) (*setupMarkKit, *recRuns) {
	t.Helper()
	k := newSetupMarkKit(t, nil)
	modelstest.Recorded(t, k.p.Pool)
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ('agy', 'signed_in', now(), '1.0')
		ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`); err != nil {
		t.Fatal(err)
	}
	machine, err := settings.Defaults(settings.Machine{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	k.j.machine = machine
	rr := &recRuns{}
	k.p.Runs = rr
	k.p.Settings = func(context.Context) (settings.Doc, error) { return machine, nil }
	return k, rr
}

// ran records a subtask of SF-1 of kind and role with one run on model, "" for none, started minutes
// ago, and gives the subtask's id.
func (k *setupMarkKit) ran(kind, role, model string, minutes int) int64 {
	k.t.Helper()
	var sub int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, $4, 'waiting') RETURNING id`,
		k.j.proj.ID, k.one.ID, kind, role).Scan(&sub); err != nil {
		k.t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, stop_reason, started_at)
		VALUES (gen_random_uuid(), $1, 1, 'ended', 'agy', nullif($2, ''), nullif($2, ''), 'test', 'img', 'quota', now() - $3::int * interval '1 minute')`,
		sub, model, minutes); err != nil {
		k.t.Fatal(err)
	}
	return sub
}

// The builder's last run is the newest of a build, a fix, or a part or a join by the builder: a ticket
// built only in parts has one, a tester's part is none, and a newest run with no model reads empty
// rather than an older run's (12.3; since 1.14 one read, where family() and NextChoice read two ways).
func TestTheBuildersLastRunCountsItsParts(t *testing.T) {
	for _, c := range []struct {
		name string
		runs [][3]string // kind, role, model, oldest first
		want string
	}{
		{"a build", [][3]string{{"build", "builder", "gemini-4-argon"}}, "gemini-4-argon"},
		{"only parts", [][3]string{{"analysis", "lead", "claude-opus-5-5"}, {"part", "builder", "gemini-4-argon"}}, "gemini-4-argon"},
		{"a join by a builder", [][3]string{{"join", "builder", "claude-sonnet-5-5"}}, "claude-sonnet-5-5"},
		{"a fix after the build", [][3]string{{"build", "builder", "gemini-4-argon"}, {"fix", "builder", "claude-sonnet-5-5"}}, "claude-sonnet-5-5"},
		{"a tester's part", [][3]string{{"part", "tester", "gemini-4-argon"}}, ""},
		{"nothing built", [][3]string{{"analysis", "lead", "claude-opus-5-5"}}, ""},
		{"the newest with no model", [][3]string{{"build", "builder", "gemini-4-argon"}, {"fix", "builder", ""}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, _ := familyKit(t)
			for i, r := range c.runs {
				k.ran(r[0], r[1], r[2], len(c.runs)-i)
			}
			if got, err := builderModel(k.ctx, k.p.Pool, k.one.ID); err != nil || got != c.want {
				t.Fatalf("%q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// A ticket's reviewer, verifier, own stage's agent and judge whose builder's family is not recorded
// wait, as at a limit, with the reason on the card, and start once it is, on another family; a tester,
// a reviewer's analysis of a ticket a lead created, which checks no build, and a check with the rule
// for checks off, start with none (12.3, safety rule 15; since 1.14, the
// operator's popup answer of session 81, "Wait as atLimit").
func TestACheckWithNoBuildersFamilyWaits(t *testing.T) {
	for _, c := range []struct {
		name string
		sp   startSpec
		off  bool
		wait bool
	}{
		{"a reviewer", startSpec{kind: kindReview, role: "reviewer", title: "Review the change"}, false, true},
		{"a verifier", startSpec{kind: kindVerify, role: "verifier", title: "Verify against the goal and the plan"}, false, true},
		{"a stage's agent", startSpec{kind: kindStage, role: "stage", modelRole: "reviewer", title: "Look at the change"}, false, true},
		{"a judge", startSpec{kind: kindJudge, role: "judge", title: "Judge the criteria", judge: true}, false, true},
		{"a tester", startSpec{kind: kindTest, role: "tester", title: "Test in a fresh sandbox"}, false, false},
		{"a reviewer's analysis", startSpec{kind: kindAnalysis, role: "reviewer", title: "Analyse the ticket"}, false, false},
		{"a reviewer with the rule off", startSpec{kind: kindReview, role: "reviewer", title: "Review the change"}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, rr := familyKit(t)
			if c.off {
				k.j.doc["agents"] = settings.Doc{"checks_use_another_family": false}
			}
			build := k.ran("build", "builder", "", 1)
			if _, err := k.p.start(k.ctx, k.j, k.one, c.sp); err != nil {
				t.Fatal(err)
			}
			if c.wait {
				if len(rr.started) != 0 || !strings.Contains(k.stuck(), "builder's model of the ticket is not recorded") {
					t.Fatalf("with no builder's model: %d runs started, stuck %q", len(rr.started), k.stuck())
				}
			} else if len(rr.started) != 1 {
				t.Fatalf("with no builder's model: %d runs started, want 1", len(rr.started))
			}
			if _, err := k.p.Pool.Exec(k.ctx, `UPDATE runs SET model = 'claude-opus-5-5' WHERE subtask = $1`, build); err != nil {
				t.Fatal(err)
			}
			rr.started = nil
			if _, err := k.p.start(k.ctx, k.j, k.one, c.sp); err != nil || len(rr.started) != 1 {
				t.Fatalf("with the builder's model: %v, %d runs started", err, len(rr.started))
			}
			if got := cli.Family(rr.started[0].Model); c.wait && got == "claude" {
				t.Fatalf("the %s runs on %s, the builder's family", c.sp.role, rr.started[0].Model)
			}
		})
	}
}

// A read of the builder's last run that fails starts no check and leaves the card as it was (BUG-012's
// rule; since 1.14).
func TestACheckWhoseBuildersRunCannotBeReadStartsNothing(t *testing.T) {
	k, rr := familyKit(t)
	k.ran("build", "builder", "gemini-4-argon", 1)
	k.failing("s.kind IN ('part', 'join') AND s.role = 'builder'")
	_, err := k.p.start(k.ctx, k.j, k.one, startSpec{kind: kindReview, role: "reviewer", title: "Review the change"})
	if !canceled(err) || len(rr.started) != 0 || k.stuck() != "" {
		t.Fatalf("%v, %d runs started, stuck %q", err, len(rr.started), k.stuck())
	}
}

// At a limit a ticket built in parts has its reviewer move off the builder's family, where it waited
// when only a build or a fix counted; a project's own stage's agent takes the reviewer's next choice
// with the reviewer's pin, where it looked for a role of its own and waited (12.7; since 1.14, the
// operator's popup answers of session 81, "Wait as atLimit" and "Fix here").
func TestAtALimitAPartedTicketsCheckAndAStageAgentMove(t *testing.T) {
	k, _ := familyKit(t)
	k.ran("part", "builder", "claude-sonnet-5-5", 3)
	models.Reached(k.ctx, k.p.Pool, "agy", "3p", "5h", time.Now().Add(time.Hour))
	review := k.ran("review", "reviewer", "claude-opus-5-5", 2)
	req, err := k.p.NextChoice(k.ctx, review)
	if err != nil || req == nil || cli.Family(req.Model) == "claude" || req.ModelFrom != models.FromLimit {
		t.Fatalf("a parted ticket's reviewer: %+v %v", req, err)
	}
	stage := k.ran(kindStage, "stage", "claude-opus-5-5", 1)
	if req, err = k.p.NextChoice(k.ctx, stage); err != nil || req == nil || req.Model != "gemini-4-argon" {
		t.Fatalf("a stage's agent: %+v %v", req, err)
	}
	if _, err := k.p.Board.EditTicket(k.ctx, k.j.proj, k.one.Key, board.Edit{Agents: map[string]any{"reviewer": map[string]any{"model": "agy/gemini-3.8-flash"}}}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if req, err = k.p.NextChoice(k.ctx, stage); err != nil || req == nil || req.Model != "gemini-3.8-flash" {
		t.Fatalf("a stage's agent with the reviewer's pin: %+v %v", req, err)
	}
}

// Each real caller of a check of the build, a review, a verification, an own stage's look and a judge,
// passes the kind that makes it wait with no builder's family, and starts off the builder's family once
// there is one (since 1.14, the review of 1.14's change, finding 3, and the operator's popup answer
// "Through all four").
func TestTheFourChecksOfABuildWaitWithNoBuildersFamily(t *testing.T) {
	for _, c := range []struct {
		role string
		act  func(k *setupMarkKit, head string) error
	}{
		{"reviewer", func(k *setupMarkKit, head string) error { return k.p.review(k.ctx, k.j, k.one, nil, head, "") }},
		{"verifier", func(k *setupMarkKit, head string) error { return k.p.verification(k.ctx, k.j, k.one, nil, head) }},
		{"stage", func(k *setupMarkKit, head string) error {
			return k.p.ownStage(k.ctx, k.j, k.one, nil, head, settings.Stage{ID: "docs", Agent: "agents/docs.md", Prompt: "Read the docs."})
		}},
		{"judge", func(k *setupMarkKit, head string) error {
			_, err := k.p.judgeOn(k.ctx, k.j, k.one, nil, head, []board.CheckSpec{{Kind: "judge", Spec: "it reads well"}}, board.PurposeJudge, "")
			return err
		}},
	} {
		t.Run(c.role, func(t *testing.T) {
			k, rr := familyKit(t)
			git := k.p.Board.Git
			if _, err := git.CommitPage(k.ctx, k.j.proj.Name, k.main, "agents/docs.md", []byte("# Docs\n\nRead the docs.\n"), "add the stage's agent",
				gitstore.Identity{Name: "Op", Email: "op@x"}); err != nil {
				t.Fatal(err)
			}
			k.main = git.Main(k.ctx, k.j.proj.Name)
			head, _ := k.branched()
			build := k.ran("build", "builder", "", 1)
			if err := c.act(k, head); err != nil || len(rr.started) != 0 || !strings.Contains(k.stuck(), "builder's model of the ticket is not recorded") {
				t.Fatalf("with no builder's model: %v, %d runs started, stuck %q", err, len(rr.started), k.stuck())
			}
			if _, err := k.p.Pool.Exec(k.ctx, `UPDATE runs SET model = 'claude-opus-5-5' WHERE subtask = $1`, build); err != nil {
				t.Fatal(err)
			}
			if err := c.act(k, head); err != nil || len(rr.started) != 1 || rr.started[0].Role != c.role || cli.Family(rr.started[0].Model) == "claude" {
				t.Fatalf("with the builder's model: %v, %+v", err, rr.started)
			}
		})
	}
}

// At a limit a ticket's check of its build with no builder's model waits, a review, a verification, a
// stage's look and a judge alike, while a reviewer's analysis of a lead's ticket, which checks no build,
// moves to its next choice (12.7; since 1.14, the reviews of 1.14's change and the operator's popup
// answers "Fix: one predicate + rows" and "Add rows for (1)").
func TestAtALimitACheckWaitsAndAReviewersAnalysisMoves(t *testing.T) {
	k, _ := familyKit(t)
	models.Reached(k.ctx, k.p.Pool, "agy", "3p", "5h", time.Now().Add(time.Hour))
	analysis := k.ran(kindAnalysis, "reviewer", "claude-opus-5-5", 3)
	if req, err := k.p.NextChoice(k.ctx, analysis); err != nil || req == nil || req.Model != "gemini-4-argon" {
		t.Fatalf("a reviewer's analysis with no build: %+v %v", req, err)
	}
	build := k.ran("build", "builder", "", 2)
	for kind, role := range map[string]string{kindReview: "reviewer", kindVerify: "verifier", kindStage: "stage", kindJudge: "judge"} {
		model := "claude-opus-5-5"
		if role == "judge" {
			model = "claude-sonnet-5-5"
		}
		if req, err := k.p.NextChoice(k.ctx, k.ran(kind, role, model, 1)); err != nil || req != nil {
			t.Fatalf("a %s with no builder's model: %+v %v", kind, req, err)
		}
	}
	// Even when the ticket has a builder's run with a model, an analysis does not check a build and sets
	// no family to avoid (register row 64).
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE runs SET model = 'gemini-4-argon' WHERE subtask = $1`, build); err != nil {
		t.Fatal(err)
	}
	if req, err := k.p.NextChoice(k.ctx, analysis); err != nil || req == nil || req.Model != "gemini-4-argon" || req.ModelFrom != models.FromLimit {
		t.Fatalf("a reviewer's analysis with a Gemini builder run: %+v %v", req, err)
	}
}

// A fit check's judge keeps off the family of the project's builder: it moves off a project's judge on
// that family, is not checked when no other family is listed, waits when the other family is at its
// limit, and a builder that cannot be chosen keeps no error's text with the case (rule 15; since 1.14,
// the security review of 1.14's change, finding 6).
func TestAFitChecksJudgeKeepsOffTheBuildersFamily(t *testing.T) {
	k, _ := familyKit(t)
	k.j.doc["agents"] = settings.Doc{"judge": settings.Doc{"model": "agy/gemini-3.8-flash"}}
	builder, judge, why, wait, err := k.p.fitModels(k.ctx, k.j)
	if err != nil || why != "" || wait || cli.Family(builder.Model) != "gemini" || cli.Family(judge.Model) == "gemini" {
		t.Fatalf("moved off the builder's family: %v %q %v %s %s", err, why, wait, builder.Model, judge.Model)
	}
	models.Reached(k.ctx, k.p.Pool, "agy", "3p", "5h", time.Now().Add(time.Hour))
	if _, _, why, wait, err := k.p.fitModels(k.ctx, k.j); err != nil || why != "" || !wait {
		t.Fatalf("the other family at its limit: %v %q %v", err, why, wait)
	}
	k.j.machine["agents"].(settings.Doc)["roles"].(settings.Doc)["judge"] = settings.Doc{"model": []any{"agy/gemini-3.8-flash"}}
	if _, _, why, wait, err := k.p.fitModels(k.ctx, k.j); err != nil || wait || !strings.Contains(why, "no judge of another family") {
		t.Fatalf("no other family listed: %v %q %v", err, why, wait)
	}
	gone, cancel := context.WithCancel(k.ctx)
	cancel()
	if _, _, why, _, err := k.p.fitModels(gone, k.j); err != nil || why != "not checked: the project's builder's model cannot be read" {
		t.Fatalf("a builder that cannot be chosen: %v %q", err, why)
	}
}

// failFrom is a query tracer that ends, before it is sent, every query whose text holds marker from its
// from-th on, so one call of a function that asks the query twice can fail alone.
type failFrom struct {
	marker string
	from   int32
	seen   *atomic.Int32
}

func (f failFrom) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, f.marker) && f.seen.Add(1) >= f.from {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}

func (failFrom) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A fit check whose judge cannot be chosen, after its builder was, returns the error and starts nothing
// (BUG-012's rule; since 1.14).
func TestAFitChecksJudgeThatCannotBeChosenIsAnError(t *testing.T) {
	k, _ := familyKit(t)
	cfg := k.p.Pool.Config()
	cfg.ConnConfig.Tracer = failFrom{marker: "FROM models ORDER BY cli, model, name", from: 2, seen: &atomic.Int32{}}
	failing, err := pgxpool.NewWithConfig(k.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(failing.Close)
	k.p.Pool = failing
	if builder, _, why, wait, err := k.p.fitModels(k.ctx, k.j); !canceled(err) || why != "" || wait || builder.Model == "" {
		t.Fatalf("%v %q %v, builder %q", err, why, wait, builder.Model)
	}
}

// A check whose rule cannot be met, since no model of another family is listed, starts on the builder's
// family; the news of it, with the family, is recorded with the run by the guard every run passes
// (12.3; internal/runs/checkguard.go and its TestACheckOnTheBuildersFamilyRunsOnlyWhenTheRuleIsOffOrCannotBeMet,
// since 1.15).
func TestACheckWhoseRuleCannotBeMetStartsOnTheBuildersFamily(t *testing.T) {
	k, rr := familyKit(t)
	k.j.machine["agents"].(settings.Doc)["roles"].(settings.Doc)["reviewer"] = settings.Doc{"model": []any{"agy/claude-opus-5-5"}, "effort": "high"}
	k.ran("build", "builder", "claude-sonnet-5-5", 1)
	if _, err := k.p.start(k.ctx, k.j, k.one, startSpec{kind: kindReview, role: "reviewer", title: "Review the change"}); err != nil || len(rr.started) != 1 ||
		rr.started[0].Model != "claude-opus-5-5" {
		t.Fatalf("%v, %+v", err, rr.started)
	}
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'check_rule_not_met'`); n != 0 {
		t.Fatalf("%d news from the pipeline", n)
	}
}

// A docs or chore ticket's review uses the flash bracket (claude-sonnet-5-5 against a Gemini build,
// moving to gemini-3.8-flash at a 3p limit), while a custom stage check on the same ticket stays on pro
// (claude-opus-5-5, moving to gemini-4-argon at a 3p limit).
func TestDocsAndChoreReviewsUseFlashWhileStageStaysPro(t *testing.T) {
	for _, kind := range []string{"docs", "chore"} {
		t.Run(kind, func(t *testing.T) {
			k, rr := familyKit(t)
			if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET kind = $1 WHERE id = $2`, kind, k.one.ID); err != nil {
				t.Fatal(err)
			}
			k.one.Kind = kind
			k.ran("build", "builder", "gemini-3.8-flash", 3)

			// 1. Review starts on claude-sonnet-5-5 (flash), while stage starts on claude-opus-5-5 (pro).
			if _, err := k.p.start(k.ctx, k.j, k.one, startSpec{kind: kindReview, role: "reviewer", title: "Review the change"}); err != nil || len(rr.started) != 1 || rr.started[0].Model != "claude-sonnet-5-5" {
				t.Fatalf("review start = %+v, %v; want claude-sonnet-5-5", rr.started, err)
			}
			rr.started = nil
			if _, err := k.p.start(k.ctx, k.j, k.one, startSpec{kind: kindStage, role: "stage", modelRole: "reviewer", title: "Stage look"}); err != nil || len(rr.started) != 1 || rr.started[0].Model != "claude-opus-5-5" {
				t.Fatalf("stage start = %+v, %v; want claude-opus-5-5", rr.started, err)
			}

			// 2. At 3p limit, NextChoice moves review to gemini-3.8-flash (flash) and stage to gemini-4-argon (pro).
			models.Reached(k.ctx, k.p.Pool, "agy", "3p", "5h", time.Now().Add(time.Hour))
			revSub := k.ran(kindReview, "reviewer", "claude-sonnet-5-5", 2)
			req, err := k.p.NextChoice(k.ctx, revSub)
			if err != nil || req == nil || req.Model != "gemini-3.8-flash" {
				t.Fatalf("NextChoice review at 3p limit = %+v, %v; want gemini-3.8-flash", req, err)
			}
			stageSub := k.ran(kindStage, "stage", "claude-opus-5-5", 1)
			req, err = k.p.NextChoice(k.ctx, stageSub)
			if err != nil || req == nil || req.Model != "gemini-4-argon" {
				t.Fatalf("NextChoice stage at 3p limit = %+v, %v; want gemini-4-argon", req, err)
			}
		})
	}
}
