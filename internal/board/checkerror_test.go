package board_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// An agent reads a setup's own end as it is, and any other error of a check run as CouldNotCheck: a
// setup the runtime could not run is Crew's error too, also when it is counted as a failure, and a
// command an agent named as a setup's end is matched whole (13.9).
func TestPublicDetail(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		state  string
		detail *string
		want   string
	}{
		{board.CheckError, s("the tree to check: fatal: not a git repository: /var/lib/crew/canonical/shop"), board.CouldNotCheck},
		{board.CheckError, s(harness.SetupError + ": exec: /var/lib/docker/x: no such file"), board.CouldNotCheck},
		{board.CheckError, s(harness.SetupError + " ended with 2"), harness.SetupError + " ended with 2"},
		{board.CheckError, s(harness.SetupError + " ran longer than 30m0s"), harness.SetupError + " ran longer than 30m0s"},
		{board.CheckError, nil, board.CouldNotCheck},
		{board.CheckError, s(harness.SetupError + " ended with 2: the check sandbox: /var/lib/crew/sandboxes/x"), board.CouldNotCheck},
		{board.CheckError, s(harness.SetupError + " ended with 2: the check sandbox: exit"), board.CouldNotCheck},
		{board.CheckFailed, s(harness.SetupError + ": exec: /var/lib/docker/x: no such file"), board.CouldNotCheck},
		{board.CheckFailed, s(harness.SetupError + " ended with 2; it passes on main, so this branch breaks it"),
			harness.SetupError + " ended with 2; it passes on main, so this branch breaks it"},
		{board.CheckFailed, s("it failed once"), "it failed once"},
		{board.CheckPassed, nil, ""},
	} {
		run := board.CheckRun{State: c.state, Detail: c.detail, Results: []board.CmdResult{{Name: "x", Error: "the check sandbox: /run/y"}}}
		if got := board.PublicDetail(&run); got != c.want {
			d := "no detail"
			if c.detail != nil {
				d = *c.detail
			}
			t.Errorf("%s, %q: %q, not %q", c.state, d, got, c.want)
		}
		pub := run.Public()
		if pub.Results[0].Error != board.CouldNotCheck || run.Results[0].Error != "the check sandbox: /run/y" {
			t.Errorf("Public's results: %+v, the run's %+v", pub.Results, run.Results)
		}
	}
}

// FinishCheckRun logs Crew's own error of a run that ends, once: not a setup's own end, and not a run
// that had ended already (13.9).
func TestFinishCheckRunLogsCrewsError(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	tk := e.add(p, ready("A"), board.OriginPlan)
	var log bytes.Buffer
	e.b.Log = slog.New(slog.NewTextHandler(&log, nil))
	finish := func(commit, state, detail string) {
		t.Helper()
		id, err := e.b.StartCheckRunFor(e.ctx, p, tk, board.PurposeExit, commit, "", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := e.b.FinishCheckRun(e.ctx, id, state, nil, detail, false, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	finish(strings.Repeat("a", 40), board.CheckError, "the check sandbox: open /var/lib/crew/sandboxes/t37: permission denied")
	finish(strings.Repeat("b", 40), board.CheckError, harness.SetupError+" ended with 2")
	// The setup's own fix ticket counts a setup the runtime could not run as a failure (checks.go).
	finish(strings.Repeat("c", 40), board.CheckFailed, harness.SetupError+": exec: /var/lib/docker/t38: no such file")
	finish(strings.Repeat("d", 40), board.CheckFailed, harness.SetupError+" ended with 3")
	finish(strings.Repeat("e", 40), board.CheckFailed, "make test ended with 1")
	if l := log.String(); strings.Count(l, "/var/lib/crew/sandboxes/t37") != 1 || strings.Count(l, "/var/lib/docker/t38") != 1 ||
		strings.Contains(l, "ended with") {
		t.Fatalf("the log: %s", l)
	}
}
