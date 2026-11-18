package pipeline

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

// canceled says err is the error a failing pool gives its query.
func canceled(err error) bool { return err != nil && strings.Contains(err.Error(), "context canceled") }

// A merge whose count of errors cannot be read waits in the merge queue with its fixed reason, which
// holds no error's text, and no merge is tried (since 1.13; the review of 1.13's change, finding 1).
func TestAFailedMergeCountWaitsWithCouldNotMerge(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	head, _ := k.branched()
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET stage = 'verification' WHERE id = $1`, k.one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, commit, result, body)
		VALUES ($1, $2, 'verify', 'verifier', $3, 'pass', 'It does what was asked.')`, k.j.proj.ID, k.one.ID, head); err != nil {
		t.Fatal(err)
	}
	k.erroredOn(board.PurposeMerge, "", head)
	doc, err := k.p.Board.Settings(k.ctx, k.p.Pool, k.j.proj)
	if err != nil {
		t.Fatal(err)
	}
	k.j.pipe = board.PipelineOf(doc)
	k.failing(mergeErrorsQuery)
	err = k.p.mergeQueue(k.ctx, k.j)
	k.p.wg.Wait()
	if n := k.count(`SELECT count(*) FROM check_runs WHERE project = $1`, k.j.proj.ID); !canceled(err) || k.stuck() != CouldNotMerge || n != 1 {
		t.Fatalf("a merge count that could not be read: %v, stuck %q, %d check runs", err, k.stuck(), n)
	}
}

// An eval's reviewer whose builder's model reads empty waits, as at a limit, rather than run with no
// family to avoid; once the model is there it starts (12.3, safety rule 15; since 1.13, the security
// review of 1.13's change, finding 1).
func TestAnEvalsReviewerWaitsForItsBuildersModel(t *testing.T) {
	ctx := context.Background()
	p, tools, er := evalKit(t, "building", "ok")
	modelstest.Recorded(t, tools.Pool)
	tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ('agy', 'signed_in', now(), '1.0') ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`)
	rr := &recRuns{}
	p.Runs = rr
	evalBranchAt(t, tools, er, true)
	machine, _ := settings.Defaults(settings.Machine{Version: "test"})
	if err := p.evals(ctx, machine); err != nil || len(rr.started) != 0 {
		t.Fatalf("with no builder's model: %v, %d runs started", err, len(rr.started))
	}
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon-high' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	if err := p.evals(ctx, machine); err != nil || len(rr.started) != 1 || rr.started[0].Role != "reviewer" || strings.HasPrefix(rr.started[0].Model, "gemini-") {
		t.Fatalf("with the builder's model: %v, %+v", err, rr.started)
	}
}

// With no builder's run a reviewer waits too; a tester, which is no role that checks, starts on an
// empty model, and so does a reviewer when the rule for checks is off (12.3, since 1.13; the second
// round of the reviews of 1.13's change).
func TestAnEvalsRoleStartsByTheRuleForChecks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, kind, role string
		noBuilder, off   bool
		started          int
	}{
		{"a reviewer with no builder's run", kindEvalReview, "reviewer", true, false, 0},
		{"a tester with an empty model", kindEvalTest, "tester", false, false, 1},
		{"a reviewer with the rule off", kindEvalReview, "reviewer", false, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, tools, _ := evalKit(t, "building", "ok")
			modelstest.Recorded(t, tools.Pool)
			tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ('agy', 'signed_in', now(), '1.0') ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`)
			rr := &recRuns{}
			p.Runs = rr
			s, err := p.currentSuite(ctx)
			if err != nil || s == nil {
				t.Fatalf("the suite: %v", err)
			}
			e, err := p.nextEvalRun(ctx, s.id)
			if err != nil || e == nil {
				t.Fatalf("the eval run: %v", err)
			}
			evalBranchAt(t, tools, e.id, true)
			proj, err := tools.Board.Project(ctx, "shop")
			if err != nil {
				t.Fatal(err)
			}
			ec, err := tools.Board.Case(ctx, tools.Pool, proj.ID, e.caseID)
			if err != nil {
				t.Fatal(err)
			}
			if c.noBuilder {
				e.builder = ""
			}
			machine, _ := settings.Defaults(settings.Machine{Version: "test"})
			if c.off {
				machine["agents"].(settings.Doc)["checks_use_another_family"] = false
			}
			doc, err := tools.Board.Settings(ctx, tools.Pool, proj)
			if err != nil {
				t.Fatal(err)
			}
			j := &job{machine: machine, doc: doc, proj: proj, pipe: board.PipelineOf(doc)}
			head := tools.Board.Git.Main(ctx, "shop")
			if _, _, err := p.startEval(ctx, j, s, e, ec, c.kind, c.role, head, ""); err != nil || len(rr.started) != c.started {
				t.Fatalf("%v, %d runs started, want %d", err, len(rr.started), c.started)
			}
		})
	}
}

// The last answer of a run that cannot be read is an error, never no answer: an eval's reviewer is not
// read as one that asked for nothing, a tester's answer is not kept empty, and a fit check spends no try
// (since 1.13; the security review of 1.13's change, finding 2).
func TestAFailedReadOfAnAnswerIsNotNoAnswer(t *testing.T) {
	ctx := context.Background()
	const marker = "SELECT text FROM turns WHERE run = $1 AND speaker = 'agent'"
	// An idle run is not finished before its answer is read: the next pass reads it as idle again.
	for _, c := range []struct {
		state, column, run string
	}{{"reviewing", "reviewer_run", "ended"}, {"testing", "tester_run", "ended"}, {"reviewing", "reviewer_run", "idle"}, {"testing", "tester_run", "idle"}} {
		t.Run(c.state+" "+c.run, func(t *testing.T) {
			p, tools, er := evalKit(t, c.state, "ok")
			p.Checks, p.Log = checkAnswer{result: harness.CheckResult{Passed: true}}, slog.New(slog.NewTextHandler(io.Discard, nil))
			finished := &finishRuns{}
			p.Runs = finished
			var run string
			if err := tools.Pool.QueryRow(ctx, `UPDATE eval_runs SET `+c.column+` = builder_run, builder_run = NULL WHERE id = $1
				RETURNING `+c.column+`::text`, er).Scan(&run); err != nil {
				t.Fatal(err)
			}
			tools.Pool.Exec(ctx, `UPDATE runs SET state = $2 WHERE id = $1`, run, c.run)
			evalBranchAt(t, tools, er, true)
			p.Pool = failingPoolFor(t, p.Pool, marker, run)
			err := p.evals(ctx, settings.Doc{})
			p.wg.Wait()
			if got, why := evalNow(t, tools, er); !canceled(err) || got != c.state+"/<nil>" || len(finished.finished) != 0 {
				t.Fatalf("an answer that could not be read: %v, %s %q, finished %v", err, got, why, finished.finished)
			}
		})
	}
	t.Run("fit", func(t *testing.T) {
		tools, _, c := toolsKit(t)
		liveRun(t, tools, c, "judge")
		tools.Pool.Exec(ctx, `UPDATE runs SET state = 'idle' WHERE id = $1`, c.Run)
		p := &Pipeline{Pool: failingPoolFor(t, tools.Pool, marker, c.Run), Board: tools.Board, Runs: &fakeRuns{}}
		ec := &board.EvalCase{Fit: board.Fit{State: board.FitRunning, Run: c.Run}}
		if still, err := p.readFit(ctx, &job{}, ec); !canceled(err) || still {
			t.Fatalf("a fit check's answer that could not be read: %v, still %v", err, still)
		}
	})
}

// finishRuns is a run driver that records the runs it was asked to finish.
type finishRuns struct {
	fakeRuns
	finished []string
}

func (r *finishRuns) Finish(_ context.Context, run string) error {
	r.finished = append(r.finished, run)
	return nil
}

// A lookup of a fix ticket made on an earlier pass that fails makes no second ticket: the setup's
// and the one after a merge on GitHub (since 1.13; the security review of 1.13's change, finding 3).
func TestAFailedLookupOfAFixTicketMakesNone(t *testing.T) {
	for _, c := range []struct {
		name, marker string
		act          func(k *setupMarkKit) error
	}{
		{"setup", "t.created_by = $2 AND t.stage NOT IN ('done', 'closed')", func(k *setupMarkKit) error {
			var id int64
			if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, detail, decided_at)
				VALUES ($1, $2, now(), 'failed', $3, now()) RETURNING id`, k.j.proj.ID, k.main, harness.SetupError+" ended with 2\nno make").Scan(&id); err != nil {
				k.t.Fatal(err)
			}
			k.failing("t.created_by = $2 AND t.stage NOT IN ('done', 'closed')")
			return k.p.actOnSetup(k.ctx, k.j, id, "failed")
		}},
		{"outside", "title = $2 AND created_by = $3", func(k *setupMarkKit) error {
			k.failing("title = $2 AND created_by = $3")
			return k.p.outsideFix(k.ctx, k.j, k.one, &board.CheckRun{State: board.CheckFailed, Purpose: board.PurposeOutside})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			before := k.count(`SELECT count(*) FROM tickets WHERE project = $1`, k.j.proj.ID)
			err := c.act(k)
			if n := k.count(`SELECT count(*) FROM tickets WHERE project = $1`, k.j.proj.ID); !canceled(err) || n != before {
				t.Fatalf("a lookup that could not be read: %v, %d tickets, want %d", err, n, before)
			}
		})
	}
}

// A lead's turn whose ticket cannot be read takes no lead slot, which would be in the wrong class
// (since 1.13; the security review of 1.13's change, finding 4).
func TestAFailedReadOfALeadTurnsTicketTakesNoSlot(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	s := &sub{role: "lead", cli: "agy"}
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, 1, 'analysis', 'lead', 'idle') RETURNING id`, k.j.proj.ID, k.one.ID).Scan(&s.id); err != nil {
		t.Fatal(err)
	}
	k.p.Runs = &fakeRuns{}
	k.failing("coalesce(t.operator_priority, $2), t.priority")
	err := k.p.continueRun(k.ctx, k.j, s, "Go on.", nil)
	if n := k.count(`SELECT count(*) FROM lead_waits`); !canceled(err) || n != 0 {
		t.Fatalf("a lead's turn whose ticket could not be read: %v, %d waiting for a slot", err, n)
	}
}
