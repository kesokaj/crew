package runs

import (
	"strings"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runspec"
)

// turnEnd is a turn's reason and end detail, read from its exit.json, which is the sandbox's word: it
// is in the agent's own home (12.4). A reason only the backend sets is not taken from it.
func turnEnd(end *harness.TurnEnd, setup bool) (reason, detail string) {
	if end.ByHarness && end.Reason == ReasonSandbox {
		// The harness's own end: the microVM died during the turn (12.4).
		return end.Reason, end.Detail
	}
	reason, detail = end.Reason, homeDetail(end)
	switch {
	case end.Step != "":
		reason = cli.ReasonError
	case !fromHome[reason] || (reason == cli.ReasonSetup && !setup):
		reason, detail = cli.ReasonError, "the turn gave a reason that is not one of its own; "+detail
	}
	return reason, detail
}

// homeDetail is a turn's end detail as the server takes it from exit.json. An end of crew-run's own,
// which names its step, gets the step's sentence from the server, whatever the home's detail says,
// and so does the note that the work in progress was not kept (13.9, since 1.00). Any other detail
// is the CLI's, as crew-run read it, and is read as data.
func homeDetail(end *harness.TurnEnd) string {
	detail := end.Detail
	if end.Step != "" {
		s, ok := runspec.StepDetail(end.Step)
		if !ok || end.Reason != cli.ReasonError {
			s = runspec.NoStep
		}
		detail = s
	}
	if end.WIPNotKept {
		detail = strings.TrimSuffix(detail, runspec.WIPNotKept) + runspec.WIPNotKept
	}
	return detail
}

// endStep is the step of crew-run's own that the server takes from a turn's exit.json: one on the
// list, with the reason error, the ends whose detail is the step's sentence; else "". The run keeps it
// in end_step, which an eval reads in place of the detail (13.9, since 1.01). It is still the home's
// word, which the sandbox writes.
func endStep(end *harness.TurnEnd) string {
	if _, ok := runspec.StepDetail(end.Step); !ok || end.Reason != cli.ReasonError {
		return ""
	}
	return end.Step
}

// logOwnError logs crew-run's own error of a turn with the run, where the operator reads it: the run's
// end detail says it is there. It is logged only for a step on the list or the mark that the work in
// progress was not kept, the ends whose sentence says so, and as the home's word, since exit.json is.
func (d *Driver) logOwnError(lr *liveRun, turn int, end *harness.TurnEnd) {
	if end.Error == "" || endStep(end) == "" && !end.WIPNotKept {
		return
	}
	d.Log.Warn("the turn's exit.json gives crew-run's error", "run", lr.id, "turn", turn, "step", trim(end.Step, 40),
		"error", trim(lr.redact.Text(end.Error), 2000))
}
