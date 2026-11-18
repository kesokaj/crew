package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/runspec"
)

// ownEnd says a run of an eval that ended for reason ended for Crew's own reason, and measured nothing
// (9.6), or wait that it cannot be told yet. A sign-in counts only when Crew's own login check, recorded
// after the run ended, found the CLI signed out: until then the eval run waits. A run whose workspace
// crew-run could not make counts by the step the server took from its exit.json, never by its detail,
// which an end without a step gives in its own words (review of M15 after the live checks; 13.9, since
// 1.01). A limit and a model are the run's own word, which no check of Crew's confirms: they are the
// agent's end, as a turn's error is. A sandbox counts only when the microVM never started, before any
// of the agent's code ran: one that ended during a turn may be the agent's, which can be root in it. A
// setup counts only for the eval run's first builder's run, before any head was recorded or round
// spent, while its branch is still at the case's base commit: a builder can push its branch back to
// the base, and on the builder's work a setup end is the home's word, which the case's checks decide
// (13.9, since 1.02).
func (p *Pipeline) ownEnd(ctx context.Context, j *job, e *evalRun, c *board.EvalCase, run, reason string) (own, wait bool, err error) {
	switch {
	case crewEnded(reason):
		return true, false, nil
	case reason == cli.ReasonLogin:
		var state string
		var failures int
		err := p.Pool.QueryRow(ctx, `SELECT l.state, l.failures FROM runs r JOIN cli_logins l ON l.cli = r.cli
			WHERE r.id = $1 AND l.checked_at > coalesce(r.ended_at, '-infinity')`, run).Scan(&state, &failures)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, true, nil
		}
		// A check records each try: one refusal of two leaves the state as it was, with a failure
		// counted, and the next try tells (logins.RefusedInARow).
		return state == "signed_out", err == nil && (state == "unknown" || state == "signed_in" && failures > 0), err
	case reason == cli.ReasonError:
		var step *string
		err := p.Pool.QueryRow(ctx, `SELECT end_step FROM runs WHERE id = $1`, run).Scan(&step)
		return derefS(step) == runspec.StepWorkspace, false, err
	case reason == runs.ReasonSandbox:
		// The driver's own end of a run whose microVM did not start, a fixed sentence of the server's,
		// matched whole: the harness's end of one that ended during a turn has another.
		var detail *string
		err := p.Pool.QueryRow(ctx, `SELECT stop_detail FROM runs WHERE id = $1`, run).Scan(&detail)
		return derefS(detail) == runs.NoStart, false, err
	case reason == cli.ReasonSetup:
		if _, worked := e.detail["head"]; worked || e.rework > 0 || e.checks > 0 {
			return false, false, nil
		}
		head, err := p.Board.Git.Head(ctx, j.proj.Name, e.branch)
		return err == nil && head == c.BaseCommit, false, err
	}
	return false, false, nil
}

// toChecks says a reviewer's or a tester's run that ended for the project's setup on the builder's
// work, which ownEnd did not count as Crew's, goes on to the case's checks. The setup's reason is the
// home's word, which the agent can write during its own turn: the check sandbox runs the setup on the
// head itself, and its end decides (13.9, since 1.02).
func toChecks(reason string) bool { return reason == cli.ReasonSetup }

// crewWhy is the why of an eval run that measured nothing for an error of a step of Crew's own: a fixed
// sentence, which the eval's readers get (list_eval_results, the eval_result signal), while the error,
// which can name the server's paths or relay git's and Docker's text, goes to Crew's server log with
// the eval run (13.9, since 1.02).
func (p *Pipeline) crewWhy(e *evalRun, step string, err error) string {
	p.log().Error("an eval run measured nothing for Crew's own error", "evalRun", e.id, "step", step, "error", err)
	return step + "; the error is in Crew's server log"
}

// treeCap takes at most left bytes of a tree, and says when there was more: an eval's archive stops at
// what a check sandbox takes, before a branch's tree, small as a pack, fills the server's memory
// (since 1.02).
type treeCap struct {
	w    io.Writer
	left int
	over bool
}

// tooLarge says the archive that ended with err was of a tree larger than a check sandbox takes: past
// the cap, or past the bound of its paths or bytes that Archive's listing stops at (since 1.03).
func (c *treeCap) tooLarge(err error) bool {
	return c.over || errors.Is(err, gitstore.ErrTreeTooLarge)
}

func (c *treeCap) Write(b []byte) (int, error) {
	if len(b) > c.left {
		c.over = true
		return 0, errors.New("the tree is larger than a check sandbox takes")
	}
	c.left -= len(b)
	return c.w.Write(b)
}

// crewEnded says a run ended for a reason of Crew's, not of the agent's work: a restart, the operator,
// or a CLI that refused to run. A sandbox and a setup are Crew's only as ownEnd reads them.
func crewEnded(reason string) bool {
	switch reason {
	case runs.ReasonRestart, runs.ReasonOperator, cli.ReasonRefused:
		return true
	}
	return false
}

// checkEnd is how an eval reads its case's check (9.6, 13.9; since 1.02). Each command's error came
// after the project's setup began on the builder's tree, in a check sandbox where the agent's code runs
// and can be root: a setup that failed or ran past its time, a command past its time or ending with
// 124, and a sandbox that ended under them are the branch's, a failed round as a failed check is. So is
// a tree larger than a check sandbox takes, which the server measures first. A check sandbox that gave
// no result could not start or take the tree, before any code of the branch ran: that measured nothing.
type checkEnd struct {
	passed, measured bool
	// failed are the commands that failed, by name; setup, that the project's setup failed first;
	// large, that the tree is larger than a check sandbox takes.
	failed       []string
	setup, large bool
}

// readCheck reads a check that ran, as execChecks gives it.
func readCheck(state string, results []board.CmdResult, setup bool) checkEnd {
	if state == board.CheckPassed {
		return checkEnd{passed: true, measured: true}
	}
	end := checkEnd{setup: setup && state == board.CheckError}
	for _, r := range results {
		if !r.Passed {
			end.failed = append(end.failed, r.Name)
		}
	}
	end.measured = state == board.CheckFailed || (state == board.CheckError && len(end.failed) > 0)
	return end
}

// maxCheckTree is the largest tree a check sandbox takes, harness.MaxTree; a test lowers it.
var maxCheckTree = harness.MaxTree

// tooLarge is the end of a check whose tree, with the held-out files, is larger than maxCheckTree.
var tooLarge = checkEnd{measured: true, large: true}

// fix is what the builder is told of a failed round: which commands failed, never their output, which
// would show the held-out tests it is measured by (since 0.51), nor the setup's, which runs over them.
func (c checkEnd) fix() string {
	const then = " Fix the work so the case's goal is met, run the checks, commit and push to your branch, and end your turn."
	switch {
	case c.large:
		return fmt.Sprintf("Crew could not check your branch: with the case's own tests its tree is larger than %d MB, the most a check sandbox takes.", maxCheckTree>>20) + then
	case c.setup:
		return "Crew ran the project's setup on your branch, with tests of its own, before the case's checks, and the setup failed." + then
	}
	return "Crew ran the case's checks on your branch, with tests of its own, and these failed: " + strings.Join(c.failed, "; ") + "." + then
}

// why is the eval run's why when its last round failed.
func (c checkEnd) why() string {
	switch {
	case c.large:
		return "the branch's tree is larger than a check sandbox takes"
	case c.setup:
		return "the project's setup failed on the branch"
	}
	return "the checks failed: " + strings.Join(c.failed, ", ")
}

// addUnmeasured adds to each side of a suite its runs that ended without measuring their case, the
// complement of what sides reads: a comparison counts the change's as goals not met before it lets the
// change pass (measure.NotWorse, since 1.10).
func (p *Pipeline) addUnmeasured(ctx context.Context, suite int64, sides map[string]measure.Side) error {
	rows, err := p.Pool.Query(ctx, `SELECT side, eval_case, count(*) FROM eval_runs
		WHERE suite = $1 AND NOT (state = 'done' AND goal_met IS NOT NULL) GROUP BY 1, 2`, suite)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var side string
		var c int64
		var n int
		if err := rows.Scan(&side, &c, &n); err != nil {
			return err
		}
		s := sides[side]
		if s.Runs == nil {
			s.Runs = map[int64][]bool{}
		}
		if s.Unmeasured == nil {
			s.Unmeasured = map[int64]int{}
		}
		s.Unmeasured[c] = n
		sides[side] = s
	}
	return rows.Err()
}

// unmeasured reads the runs of one side of a suite that ended without measuring their case, the
// complement of what measured reads, for a comparison across suites (measure.Common, since 1.10).
func (p *Pipeline) unmeasured(ctx context.Context, suite int64, side string) ([]measure.Run, error) {
	rows, err := p.Pool.Query(ctx, `SELECT eval_case, repeat FROM eval_runs WHERE suite = $1 AND side = $2 AND goal_met IS NULL ORDER BY repeat, id`, suite, side)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (measure.Run, error) {
		m := measure.Run{Unmeasured: true}
		err := r.Scan(&m.Case, &m.Repeat)
		return m, err
	})
}
