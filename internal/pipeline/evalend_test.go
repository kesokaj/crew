package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// A builder's run that ended for its sign-in measured nothing only when Crew's own login check, recorded
// after the run ended, found the CLI signed out; until a check tells, the eval run waits, and a check
// that found it signed in makes the end the agent's (13.9, since 1.01).
func TestAnEvalWaitsForCrewsLoginCheck(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, checks, want string
	}{
		{"no check", "", "building/<nil>"},
		{"a check before the end", "signed_out:-1 minute", "building/<nil>"},
		{"a check that refused once", "unknown:1 minute", "building/<nil>"},
		{"a signed-in CLI's check that refused once", "signed_in:1 minute:1", "building/<nil>"},
		{"signed out", "signed_out:1 minute", "failed/<nil>"},
		{"signed in", "signed_in:1 minute", "done/false"},
	} {
		p, tools, er := evalKit(t, "building", "login")
		tools.Pool.Exec(ctx, `UPDATE runs SET ended_at = now() WHERE id = (SELECT builder_run FROM eval_runs WHERE id = $1)`, er)
		if c.checks != "" {
			parts := strings.Split(c.checks+":0", ":")
			if _, err := tools.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, failures) VALUES ('agy', $1, now() + $2::interval, $3::int)
				ON CONFLICT (cli) DO UPDATE SET state = $1, checked_at = now() + $2::interval, failures = $3::int`, parts[0], parts[1], parts[2]); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.evals(ctx, settings.Doc{}); err != nil {
			t.Fatal(err)
		}
		var state string
		var met *bool
		tools.Pool.QueryRow(ctx, `SELECT state, goal_met FROM eval_runs WHERE id = $1`, er).Scan(&state, &met)
		got := state + "/<nil>"
		if met != nil {
			got = fmt.Sprintf("%s/%v", state, *met)
		}
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// A reviewer's run that ended at a limit is the agent's end: with no verdict its case measured nothing
// for that, not for a reason of Crew's (since 1.01).
func TestAnEvalsReviewerAtALimitEndedWithNoVerdict(t *testing.T) {
	ctx := context.Background()
	p, tools, er := evalKit(t, "reviewing", "quota")
	tools.Pool.Exec(ctx, `UPDATE eval_runs SET reviewer_run = builder_run, builder_run = NULL WHERE id = $1`, er)
	if err := p.evals(ctx, settings.Doc{}); err != nil {
		t.Fatal(err)
	}
	var state, why string
	tools.Pool.QueryRow(ctx, `SELECT state, detail->>'why' FROM eval_runs WHERE id = $1`, er).Scan(&state, &why)
	if state != "failed" || why != "the reviewer's run ended with no verdict: quota" {
		t.Fatalf("a reviewer at a limit: %s %q", state, why)
	}
}

// evalBranchAt puts an eval run's branch in the quarantine repository at the project's main, which
// becomes its case's base commit, and one commit on from it when the builder worked on it.
func evalBranchAt(t *testing.T, tools *Tools, er int64, worked bool) {
	t.Helper()
	ctx := context.Background()
	base := tools.Board.Git.Main(ctx, "shop")
	branch := fmt.Sprintf("eval/%d", er)
	if err := tools.Board.Git.EvalBranch(ctx, "shop", branch, base); err != nil {
		t.Fatal(err)
	}
	if worked {
		gitRun := quarantineGit(t, tools, "shop")
		gitRun("update-ref", "refs/heads/"+branch, gitRun("commit-tree", base+"^{tree}", "-p", base, "-m", "work"))
	}
	tools.Pool.Exec(ctx, `UPDATE eval_cases SET base_commit = $2 WHERE id = (SELECT eval_case FROM eval_runs WHERE id = $1)`, er, base)
	tools.Pool.Exec(ctx, `UPDATE eval_runs SET branch = $2 WHERE id = $1`, er, branch)
}

// evalNow is an eval run's state with its goal, and its why.
func evalNow(t *testing.T, tools *Tools, er int64) (string, string) {
	t.Helper()
	var state, why string
	var met *bool
	tools.Pool.QueryRow(context.Background(), `SELECT state, goal_met, coalesce(detail->>'why', '') FROM eval_runs WHERE id = $1`, er).Scan(&state, &met, &why)
	if met == nil {
		return state + "/<nil>", why
	}
	return fmt.Sprintf("%s/%v", state, *met), why
}

// A run of an eval that ended for its sandbox measured nothing only when its microVM never started,
// before any of the agent's code ran; one that ended during a turn may be the agent's, which can be
// root in it: a builder's is a goal not met, a reviewer's gave no verdict, and a tester's goes on to the
// case's checks (13.9, since 1.02).
func TestAnEvalCountsASandboxEndOnlyWhenItNeverStarted(t *testing.T) {
	ctx := context.Background()
	const midTurn = "the microVM ended during the turn"
	for _, c := range []struct {
		state, column, detail, want string
	}{
		{"building", "builder_run", runs.NoStart, "failed/<nil>"},
		{"building", "builder_run", midTurn, "done/false"},
		{"reviewing", "reviewer_run", runs.NoStart, "failed/<nil>"},
		{"reviewing", "reviewer_run", midTurn, "failed/<nil>"},
		{"testing", "tester_run", runs.NoStart, "failed/<nil>"},
		{"testing", "tester_run", midTurn, "done/true"},
	} {
		p, tools, er := evalKit(t, c.state, "sandbox")
		p.Checks, p.Log = checkAnswer{result: harness.CheckResult{Passed: true}}, slog.New(slog.NewTextHandler(io.Discard, nil))
		if c.column != "builder_run" {
			tools.Pool.Exec(ctx, `UPDATE eval_runs SET `+c.column+` = builder_run, builder_run = NULL WHERE id = $1`, er)
		}
		evalBranchAt(t, tools, er, true)
		tools.Pool.Exec(ctx, `UPDATE runs SET stop_detail = $2 WHERE id = (SELECT `+c.column+` FROM eval_runs WHERE id = $1)`, er, c.detail)
		if err := p.evals(ctx, settings.Doc{}); err != nil {
			t.Fatal(err)
		}
		p.wg.Wait()
		if got, why := evalNow(t, tools, er); got != c.want {
			t.Errorf("a %s run whose sandbox ended so, %q: %s %q, want %s", c.state, c.detail, got, why, c.want)
		}
	}
}

// A run of an eval that ended for the project's setup measured nothing only for the eval run's first
// builder's run, while its branch is at the case's base commit, the case's own code. A builder that
// pushed its branch back to the base after a round, or ended on its work, did not meet its goal. A
// reviewer's or a tester's setup end is the home's word: the case's checks, which run the setup on the
// head themselves, decide (13.9, since 1.02).
func TestAnEvalCountsASetupEndByItsBranch(t *testing.T) {
	ctx := context.Background()
	setupFails := checkAnswer{result: harness.CheckResult{Code: 2, Error: harness.SetupError + " ended with 2"}}
	passes := checkAnswer{result: harness.CheckResult{Passed: true}}
	for _, c := range []struct {
		state, column string
		worked        bool
		rework        int
		rounds        int
		checks        checkAnswer
		want, why     string
	}{
		{"building", "builder_run", false, 0, 0, passes, "failed/<nil>", "the builder's run ended for Crew's own reason: setup"},
		{"building", "builder_run", false, 1, 0, passes, "done/false", "the builder's run ended: setup"},
		{"building", "builder_run", false, 0, 1, passes, "done/false", "the builder's run ended: setup"},
		{"building", "builder_run", true, 0, 0, passes, "done/false", "the builder's run ended: setup"},
		{"reviewing", "reviewer_run", true, 0, 0, passes, "done/true", "the checks passed with the held-out files"},
		{"reviewing", "reviewer_run", true, 0, 0, setupFails, "fixing/<nil>", ""},
		{"testing", "tester_run", true, 0, 0, passes, "done/true", "the checks passed with the held-out files"},
		{"testing", "tester_run", true, 0, 0, setupFails, "fixing/<nil>", ""},
	} {
		p, tools, er := evalKit(t, c.state, "setup")
		p.Checks, p.Log = c.checks, slog.New(slog.NewTextHandler(io.Discard, nil))
		if c.column != "builder_run" {
			tools.Pool.Exec(ctx, `UPDATE eval_runs SET `+c.column+` = builder_run, builder_run = NULL WHERE id = $1`, er)
		}
		evalBranchAt(t, tools, er, c.worked)
		// With no command a check sandbox gives no result, and a setup that failed passes (13.9).
		if _, err := tools.Pool.Exec(ctx, `UPDATE eval_cases SET checks = '[{"kind": "cmd", "spec": "make test"}]' WHERE id = (SELECT eval_case FROM eval_runs WHERE id = $1)`, er); err != nil {
			t.Fatal(err)
		}
		tools.Pool.Exec(ctx, `UPDATE eval_runs SET rework = $2, check_rounds = $3 WHERE id = $1`, er, c.rework, c.rounds)
		if c.worked && c.column != "builder_run" {
			tools.Pool.Exec(ctx, `UPDATE eval_runs SET detail = detail || '{"head": "x"}' WHERE id = $1`, er)
		}
		if err := p.evals(ctx, settings.Doc{}); err != nil {
			t.Fatal(err)
		}
		p.wg.Wait()
		if got, why := evalNow(t, tools, er); got != c.want || why != c.why {
			t.Errorf("a %s run's setup end, worked %v, rework %d: %s %q, want %s %q", c.state, c.worked, c.rework, got, why, c.want, c.why)
		}
	}
}
