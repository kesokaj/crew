package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

// Markers that each match only the count of Crew's errors they are named for (grep each in its function).
const (
	errorsOnQuery      = "commit = $2 AND state = 'error' AND"
	mergeErrorsQuery   = "purpose = 'merge' AND head = $2"
	judgeErrorsQuery   = "coalesce(stage, '') = $4 AND state = 'error'"
	stageErrorsQuery   = "AND stage = $4 AND state = 'error'"
	outsideErrorsQuery = "purpose = 'outside' AND state = 'error'"
	baseErrorsQuery    = "purpose = 'base' AND state = 'error'"
	toldQuery          = "kind = 'setup_failed' AND detail->>'since'"
)

// branched gives SF-1 a pushed branch at main, and a subtask's branch one commit past it.
func (k *setupMarkKit) branched() (head, sub string) {
	k.t.Helper()
	git := k.p.Board.Git
	for _, b := range []string{"task/SF-1-one", "sub/SF-1-1"} {
		if err := git.AdvanceBranch(k.ctx, k.j.proj.Name, b, k.main); err != nil {
			k.t.Fatal(err)
		}
	}
	q := quarantineGit(k.t, &Tools{Pool: k.p.Pool, Board: k.p.Board}, k.j.proj.Name)
	sub = q("commit-tree", k.main+"^{tree}", "-p", k.main, "-m", "the part")
	q("update-ref", "refs/heads/sub/SF-1-1", sub)
	branch := "task/SF-1-one"
	k.one.Branch = &branch
	if _, err := k.p.Pool.Exec(k.ctx, `UPDATE tickets SET branch = $2 WHERE id = $1`, k.one.ID, branch); err != nil {
		k.t.Fatal(err)
	}
	k.p.Identity = k.p.Board.Identity
	return k.main, sub
}

// erroredOn records a check run of SF-1 for purpose and stage on commit that ended as Crew's error,
// not at the setup.
func (k *setupMarkKit) erroredOn(purpose, stage, commit string) {
	k.t.Helper()
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO check_runs (project, ticket, purpose, stage, commit, main, head, state, detail, started_at, ended_at)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $5, 'error', 'the check sandbox did not start', clock_timestamp(), clock_timestamp())`,
		k.j.proj.ID, k.one.ID, purpose, stage, commit, k.main); err != nil {
		k.t.Fatal(err)
	}
}

// A caller of a count of Crew's errors whose query fails returns the error, and starts nothing: no
// check sandbox, no judge, no merge; the step leaves the ticket's waiting reason as it was, and the merge
// queue writes CouldNotMerge on a merge's (TestAFailedMergeCountWaitsWithCouldNotMerge) (BUG-012's rule;
// the security review of 1.12's change, finding 2, and the operator's popup answer of session 80,
// "Return the error").
func TestACountOfCrewsErrorsThatFailsStartsNothing(t *testing.T) {
	for _, c := range []struct {
		name, marker, purpose, stage string
		act                          func(k *setupMarkKit, head, sub string) error
	}{
		{"afterBuild", errorsOnQuery, board.PurposeExit, "", func(k *setupMarkKit, head, _ string) error {
			k.one.Stage = settings.StageInProgress
			return k.p.afterBuild(k.ctx, k.j, k.one, nil, &sub{kind: kindBuild, reported: &report{Tool: "request_move"}})
		}},
		{"test", errorsOnQuery, board.PurposeTest, "", func(k *setupMarkKit, head, _ string) error {
			return k.p.test(k.ctx, k.j, k.one, []*sub{{kind: kindTest, commit: head, state: "done"}}, head)
		}},
		{"e2e", errorsOnQuery, board.PurposeE2E, "", func(k *setupMarkKit, head, _ string) error {
			return k.p.e2e(k.ctx, k.j, k.one, []*sub{{kind: kindE2E, commit: head, state: "done"}}, head, "make e2e")
		}},
		{"afterPart", errorsOnQuery, board.PurposePart, "", func(k *setupMarkKit, head, _ string) error {
			return k.p.afterPart(k.ctx, k.j, k.one, &sub{kind: kindPart, check: "true", reported: &report{}, branch: "task/SF-1-one"})
		}},
		{"join", errorsOnQuery, board.PurposeJoin, "", func(k *setupMarkKit, _, part string) error {
			s := &sub{kind: kindPart, number: 1, check: "true", branch: "sub/SF-1-1"}
			return k.p.join(k.ctx, k.j, k.one, []*sub{s}, s)
		}},
		{"afterJoins", errorsOnQuery, board.PurposeExit, "", func(k *setupMarkKit, head, _ string) error {
			return k.p.afterJoins(k.ctx, k.j, k.one, nil)
		}},
		{"revertStep", errorsOnQuery, board.PurposeExit, "", func(k *setupMarkKit, head, _ string) error {
			merge := k.main
			k.one.RevertOf = &merge
			return k.p.revertStep(k.ctx, k.j, k.one, nil)
		}},
		{"mergeOne's checks", errorsOnQuery, board.PurposeExit, "", func(k *setupMarkKit, head, _ string) error {
			k.one.Stage = settings.StageInProgress
			_, err := k.p.mergeOne(k.ctx, k.j, k.one)
			return err
		}},
		{"mergeOne's merge", mergeErrorsQuery, board.PurposeMerge, "", func(k *setupMarkKit, head, _ string) error {
			// In Verification, verified on its head, it waits only for its merge.
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, commit, result, body)
				VALUES ($1, $2, 'verify', 'verifier', $3, 'pass', 'It does what was asked.')`, k.j.proj.ID, k.one.ID, head); err != nil {
				k.t.Fatal(err)
			}
			k.one.Stage = settings.StageVerification
			_, err := k.p.mergeOne(k.ctx, k.j, k.one)
			return err
		}},
		{"judgeOn", judgeErrorsQuery, board.PurposeJudge, "", func(k *setupMarkKit, head, _ string) error {
			_, err := k.p.judgeOn(k.ctx, k.j, k.one, nil, head, []board.CheckSpec{{Kind: "judge", Spec: "it reads well"}}, board.PurposeJudge, "")
			return err
		}},
		{"ownStage", stageErrorsQuery, board.PurposeStage, "docs", func(k *setupMarkKit, head, _ string) error {
			if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO records (project, ticket, kind, author, commit, result, body, stage)
				VALUES ($1, $2, 'stage', 'stage', $3, 'pass', 'It reads well.', 'docs')`, k.j.proj.ID, k.one.ID, head); err != nil {
				k.t.Fatal(err)
			}
			st := settings.Stage{ID: "docs", Optional: true, Exit: []string{"cmd: make docs"}, Agent: "agents/docs.md"}
			subs := []*sub{{kind: kindStage, commit: head, state: "done", created: time.Now()}}
			return k.p.ownStage(k.ctx, k.j, k.one, subs, head, st)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			head, part := k.branched()
			on := head
			if c.purpose == board.PurposeJoin {
				on = part
			}
			k.erroredOn(c.purpose, c.stage, on)
			pool := k.p.Pool
			k.failing(c.marker)
			before := k.count(`SELECT count(*) FROM check_runs WHERE project = $1`, k.j.proj.ID)
			err := c.act(k, head, part)
			k.p.wg.Wait()
			if err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Fatalf("a count that could not be read: %v", err)
			}
			if n := k.count(`SELECT count(*) FROM check_runs WHERE project = $1`, k.j.proj.ID); n != before {
				t.Fatalf("%d check runs after a count that could not be read, want %d", n, before)
			}
			if s := k.stuck(); s != "" {
				t.Fatalf("the ticket waits: %q", s)
			}
			// Read, the one error is under three: a check starts again, but a merge and a judge, which
			// start no check run here (main has the branch; no model is listed).
			k.p.Pool = pool
			err = c.act(k, head, part)
			k.p.wg.Wait()
			want := before + 1
			if c.purpose == board.PurposeMerge || c.purpose == board.PurposeJudge {
				want = before
			}
			if n := k.count(`SELECT count(*) FROM check_runs WHERE project = $1`, k.j.proj.ID); err != nil || n != want {
				t.Fatalf("a count that was read: %v, %d check runs, want %d", err, n, want)
			}
		})
	}
}

// After a merge on GitHub, a count of the check's errors on main that fails starts no check there
// again, and the ticket does not wait (BUG-012's rule, since 1.13).
func TestAFailedCountOfAnOutsideCheckStartsNoCheck(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	last := k.marked(board.PurposeOutside, "")
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO pull_requests (ticket, project, repo, branch, state, outside, outside_check)
		VALUES ($1, $2, 'o/shop', 'sf-1', 'open', 'merged', $3)`, k.one.ID, k.j.proj.ID, last); err != nil {
		t.Fatal(err)
	}
	k.failing(outsideErrorsQuery)
	err := k.p.outsideMerges(k.ctx, k.j)
	var check *int64
	if err := k.p.Pool.QueryRow(context.Background(), `SELECT outside_check FROM pull_requests WHERE ticket = $1`, k.one.ID).Scan(&check); err != nil {
		t.Fatal(err)
	}
	if err == nil || !strings.Contains(err.Error(), "context canceled") || check == nil || *check != last || k.stuck() != "" {
		t.Fatalf("a count that could not be read: %v, check %v, stuck %q", err, check, k.stuck())
	}
}

// A count of a decision's errored checks on main that fails neither asks again nor ends the lead's run
// (BUG-012's rule, since 1.13).
func TestAFailedCountOfABaseCheckNeitherAsksAgainNorEnds(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	ended := &endRuns{}
	k.p.Runs = ended
	s := &sub{run: "00000000-0000-4000-8000-0000000000d1", created: time.Now().Add(-time.Minute)}
	var last int64
	for range 3 {
		last = k.marked(board.PurposeBase, "")
	}
	k.setupErrors(3)
	k.failing(baseErrorsQuery)
	r := &report{BaseRun: last}
	_, _, err := k.p.baseCheck(k.ctx, k.j, k.one, s, r, []board.CheckSpec{{Kind: "cmd", Spec: "true"}})
	if err == nil || !strings.Contains(err.Error(), "context canceled") || r.BaseRun != last || len(ended.ended) != 0 || k.stuck() != "" {
		t.Fatalf("a count that could not be read: %v, base run %d, ended %v, stuck %q", err, r.BaseRun, ended.ended, k.stuck())
	}
}

// A count of the news that Crew stops trying the setup on main that fails sends no news (BUG-012's
// rule, since 1.13).
func TestAFailedCountOfTheNewsSendsNone(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	k.setupErrors(3)
	k.failing(toldQuery)
	err := k.p.watchSetup(k.ctx, k.j)
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'setup_failed'`); err == nil || !strings.Contains(err.Error(), "context canceled") || n != 0 {
		t.Fatalf("a count of the news that could not be read: %v, %d news", err, n)
	}
}

// An eval's reviewer starts only once its builder's model was read: one that cannot be read gives no
// family to avoid, and a reviewer could run on the builder's (12.3, safety rule 15; the operator's
// popup answer of session 80, "Seven + eval's model read").
func TestAFailedReadOfAnEvalBuildersModelStartsNoReviewer(t *testing.T) {
	ctx := context.Background()
	p, tools, er := evalKit(t, "building", "ok")
	modelstest.Recorded(t, tools.Pool)
	tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ('agy', 'signed_in', now(), '1.0') ON CONFLICT (cli) DO UPDATE SET state = 'signed_in'`)
	rr := &recRuns{}
	p.Runs = rr
	base := tools.Board.Git.Main(ctx, "shop")
	branch := fmt.Sprintf("eval/%d", er)
	if err := tools.Board.Git.EvalBranch(ctx, "shop", branch, base); err != nil {
		t.Fatal(err)
	}
	gitRun := quarantineGit(t, tools, "shop")
	gitRun("update-ref", "refs/heads/"+branch, gitRun("commit-tree", base+"^{tree}", "-p", base, "-m", "the work"))
	tools.Pool.Exec(ctx, `UPDATE eval_cases SET base_commit = $2 WHERE id = (SELECT eval_case FROM eval_runs WHERE id = $1)`, er, base)
	tools.Pool.Exec(ctx, `UPDATE eval_runs SET branch = $2 WHERE id = $1`, er, branch)
	tools.Pool.Exec(ctx, `UPDATE runs SET model = 'gemini-4-argon-high' WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
	p.Pool = failingPool(t, p.Pool, "SELECT coalesce(model, '') FROM runs WHERE id = $1")
	machine, _ := settings.Defaults(settings.Machine{Version: "test"})
	err := p.evals(ctx, machine)
	var state string
	if err := tools.Pool.QueryRow(ctx, `SELECT state FROM eval_runs WHERE id = $1`, er).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err == nil || !strings.Contains(err.Error(), "context canceled") || len(rr.started) != 0 || state != "building" {
		t.Fatalf("a builder's model that could not be read: %d runs started, eval run %s (%v)", len(rr.started), state, err)
	}
}
