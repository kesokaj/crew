package httpapi_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// A check run that ends as Crew's own error keeps its detail for the operator, and an agent reads it
// neither in the ticket's waiting nor in its stuck: get_ticket shows CouldNotCheck there (13.9, the
// security review of session 57, finding 1). SF-1's checks on its built head end as an error that
// names a path on the machine, three times, and then it waits.
func TestAnErroredCheckRunsDetailReachesNoAgent(t *testing.T) {
	t.Parallel()
	const path = "/var/lib/crew/sandboxes/t37-sb/home/tree.tar"
	k := newTicketKit(t)
	k.builder(nil)
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		if slices.Contains(tarNames(spec.Tree), "1.txt") {
			return harness.CheckResult{Code: -1, Error: "the check sandbox: open " + path + ": permission denied"}
		}
		return checkSandbox(spec, c)
	}
	k.shop([]string{}, nil, "one")
	var made struct {
		Token string `json:"token"`
	}
	k.call("POST", "/api/crew/mcp/tokens", `{"name": "laptop", "projects": ["shop"]}`, 201, &made)
	defer k.run()()
	k.until("SF-1 to wait after three errors", func() bool {
		v := k.ticket("SF-1").Ticket
		for _, s := range []*string{v.Waiting, v.Stuck} {
			if strings.Contains(deref(s), path) {
				t.Fatalf("a reason holds the error: %s", deref(s))
			}
		}
		return strings.Contains(deref(v.Stuck), "three times")
	})
	_, text, refused := k.outsideCall(made.Token, "tools/call", map[string]any{"name": "get_ticket",
		"arguments": map[string]any{"project": "shop", "key": "SF-1"}})
	var got board.Ticket
	if refused || json.Unmarshal([]byte(text), &got) != nil {
		t.Fatalf("get_ticket: %s", text)
	}
	if strings.Contains(text, path) {
		t.Fatalf("get_ticket holds the error: %s", text)
	}
	for what, s := range map[string]*string{"waiting": got.Waiting, "stuck": got.Stuck} {
		if !strings.Contains(deref(s), board.CouldNotCheck) {
			t.Errorf("%s: %q, not %q", what, deref(s), board.CouldNotCheck)
		}
	}
	// The operator's ticket page has the error, in its check runs.
	if n := k.count(`SELECT count(*) FROM check_runs WHERE state = 'error' AND detail LIKE '%' || $1 || '%'`, path); n < 3 {
		t.Fatalf("%d check runs keep the error", n)
	}
}

// The signal of a decision whose checks could not be run on main holds CouldNotCheck, not Crew's
// error: the improver reads signals through list_signals (13.9).
func TestTheBaseCheckSignalHoldsNoCrewError(t *testing.T) {
	t.Parallel()
	const path = "/var/lib/crew/sandboxes/t37-sb/home"
	k := newTicketKit(t)
	k.builder(nil)
	k.f.CheckErr = errors.New("the check sandbox: open " + path + ": permission denied")
	k.shop([]string{}, nil, "one")
	defer k.run()()
	k.until("the signal that the checks on main were not run", func() bool {
		return k.count(`SELECT count(*) FROM signals WHERE kind = 'base_check_not_run'`) > 0
	})
	if n := k.count(`SELECT count(*) FROM signals WHERE kind = 'base_check_not_run' AND detail->>'detail' IS DISTINCT FROM $1`, board.CouldNotCheck); n != 0 {
		t.Fatalf("%d signals hold another detail", n)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE stop_detail LIKE '%' || $1 || '%'`, path); n != 0 {
		t.Fatalf("%d runs end with the error", n)
	}
}

// A setup the runtime could not run on main is Crew's error in the fix ticket Crew makes: its seen
// holds CouldNotCheck and the setup's output, not the runtime's text (13.9).
func TestTheSetupFixTicketHoldsNoCrewError(t *testing.T) {
	t.Parallel()
	const path = "/var/lib/docker/t37/rootfs"
	k := setupKit(t, func(has func(string) bool) bool { return !has("setup.fixed") }, true)
	check := k.f.Check
	k.f.Check = func(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
		r := check(spec, c)
		if strings.HasPrefix(r.Error, harness.SetupError) {
			r.Error = harness.SetupError + ": exec: " + path + ": no such file"
		}
		return r
	}
	var log syncBuffer
	k.pipe.Log = slog.New(slog.NewTextHandler(&log, nil))
	k.setupAgents(nil)
	k.shop([]string{}, nil, "One")
	k.withSetup()
	defer k.run()()
	k.until("the setup's fix ticket", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup'`) == 1
	})
	// The fix ticket's CouldNotCheck says the error is in Crew's server log: it is.
	if l := log.String(); !strings.Contains(l, "the project's setup on main ended as Crew's own error") || !strings.Contains(l, path) {
		t.Fatalf("the pipeline's log: %s", l)
	}
	if n := k.count(`SELECT count(*) FROM setup_checks WHERE state = 'failed' AND detail LIKE '%' || $1 || '%'`, path); n != 1 {
		t.Fatalf("%d checks of the setup on main failed with the runtime's error", n)
	}
	if n := k.count(`SELECT count(*) FROM tickets WHERE title = 'Fix the project''s setup' AND seen LIKE $1 || '%'
		AND seen LIKE '%No rule to make target%' AND seen NOT LIKE '%' || $2 || '%'`, board.CouldNotCheck, path); n != 1 {
		t.Fatal("the fix ticket's seen holds the runtime's error, or not the setup's output")
	}
}
