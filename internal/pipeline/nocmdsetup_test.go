package pipeline

import (
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// failedSetup is the project's setup that ends a check before its commands, as the fake plays it.
func failedSetup(harness.CheckSpec) harness.CheckResult {
	return harness.CheckResult{Code: 2, Error: harness.SetupError + " ended with 2", Output: "npm ERR!"}
}

// noCmdRun runs a check with no command of SF-1 for purpose on the fake check sandbox, whose setup
// ends with setup, and reads how it ended.
func (k *setupMarkKit) noCmdRun(purpose string, setup func(harness.CheckSpec) harness.CheckResult) (string, bool, []board.CmdResult) {
	k.t.Helper()
	k.p.Checks = &harness.Fake{CheckSetup: setup}
	id, err := k.p.runChecks(k.ctx, k.j, k.one, purpose, k.main, k.main, k.main, checkCmds(nil, ""))
	if err != nil || id == 0 {
		k.t.Fatalf("the check run %d: %v", id, err)
	}
	k.p.wg.Wait()
	var state string
	var marked bool
	var results []board.CmdResult
	if err := k.p.Pool.QueryRow(k.ctx, `SELECT state, setup_failed, results FROM check_runs WHERE id = $1`, id).Scan(&state, &marked, &results); err != nil {
		k.t.Fatal(err)
	}
	return state, marked, results
}

// A ticket's check with no command, such as the merge of a ticket with judge checks only in a project
// with no test command, whose setup fails, is the setup's end as it is with commands: an error with
// the setup's mark and the setup's own result, never a pass; two in a row have the setup run on main
// (13.5, 13.9, since 1.11).
func TestACheckWithNoCommandReadsItsSetupsEnd(t *testing.T) {
	for _, purpose := range []string{board.PurposeMerge, board.PurposeTest, board.PurposeOutside, board.PurposeExit} {
		t.Run(purpose, func(t *testing.T) {
			k := newSetupMarkKit(t, nil)
			state, marked, results := k.noCmdRun(purpose, failedSetup)
			if state != board.CheckError || !marked {
				t.Fatalf("the check run: %s, marked %v; not an error with the setup's mark", state, marked)
			}
			want := board.CmdResult{Name: harness.SetupCheck + "make setup", Code: 2, Error: harness.SetupError + " ended with 2", Output: "npm ERR!"}
			if len(results) != 1 || results[0] != want {
				t.Fatalf("the results: %+v", results)
			}
			if purpose != board.PurposeMerge {
				return
			}
			k.noCmdRun(purpose, failedSetup)
			if err := k.p.watchSetup(k.ctx, k.j); err != nil {
				t.Fatal(err)
			}
			k.p.wg.Wait()
			if n := k.count(`SELECT count(*) FROM setup_checks WHERE project = $1`, k.j.proj.ID); n != 1 {
				t.Fatalf("%d checks of the setup on main after two runs with no command whose setup failed", n)
			}
		})
	}
}

// The setup's own fix ticket fails its check with no command where its setup still fails; a setup
// that passes with no command passes, with no result (13.5, 13.9, since 1.11).
func TestACheckWithNoCommandOfTheSetupsFixTicketFails(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	if _, err := k.p.Pool.Exec(k.ctx, `INSERT INTO setup_checks (project, commit, through, state, decided_at, acted_at, ticket)
		VALUES ($1, $2, now(), 'failed', now(), now(), $3)`, k.j.proj.ID, k.main, k.one.ID); err != nil {
		t.Fatal(err)
	}
	if state, marked, _ := k.noCmdRun(board.PurposeMerge, failedSetup); state != board.CheckFailed || !marked {
		t.Fatalf("the fix ticket's check run: %s, marked %v; not failed with the setup's mark", state, marked)
	}
	passes := func(harness.CheckSpec) harness.CheckResult { return harness.CheckResult{} }
	if state, marked, results := k.noCmdRun(board.PurposeMerge, passes); state != board.CheckPassed || marked || len(results) != 0 {
		t.Fatalf("a setup that passes: %s, marked %v, %+v", state, marked, results)
	}
}
