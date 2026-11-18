package board

import (
	"regexp"
	"strings"

	"github.com/kesokaj/crew/internal/harness"
)

// CouldNotCheck is what an agent reads of a check run that ended as Crew's own error (13.9). It holds
// no error's text: an agent reads a ticket's waiting and stuck reasons through get_ticket, the chat
// reads check runs, the improver reads signals, and the error of a git archive, of Docker or of a
// sandbox's start can name the server's paths. The run keeps its detail for the operator's ticket
// page, and the server's log has it (the security review of session 57, finding 1).
const CouldNotCheck = "Crew's own error; it is in the check run on the ticket's page and in Crew's server log"

// readableSetup is the whole of an error of a project's own setup that says only how it ended, which
// the agent that fixes the setup needs: an exit code, or a time, with the mark the pipeline adds when
// the setup passes on main. It is matched whole, never as a beginning: an errored command's detail
// begins with the command, which an agent wrote. A setup the runtime could not run is Crew's error.
var readableSetup = regexp.MustCompile(`^` + regexp.QuoteMeta(harness.SetupError) +
	` (ended with -?[0-9]+|ran longer than [0-9][0-9.hmsµun]*)(; it passes on main, so this branch breaks it)?$`)

// PublicError is what an agent reads of a check command's error, or of an errored run's detail: a
// setup's own end as it is, anything else as CouldNotCheck; "" stays "".
func PublicError(s string) string {
	if s == "" || readableSetup.MatchString(s) {
		return s
	}
	return CouldNotCheck
}

// PublicDetail is what an agent reads of a check run's detail: an errored run's through PublicError,
// with CouldNotCheck when it has none, and so a failed run's whose setup failed, which the pipeline
// counts as a failure; any other run's as it is.
func PublicDetail(run *CheckRun) string {
	d := deref(run.Detail)
	if run.State != CheckError && !strings.HasPrefix(d, harness.SetupError) {
		return d
	}
	if s := PublicError(d); s != "" {
		return s
	}
	return CouldNotCheck
}

// Public is the run as an agent reads it: its detail and its commands' errors through PublicDetail
// and PublicError, on a copy.
func (run CheckRun) Public() CheckRun {
	d := PublicDetail(&run)
	run.Detail = &d
	if d == "" {
		run.Detail = nil
	}
	results := make([]CmdResult, len(run.Results))
	for i, r := range run.Results {
		r.Error = PublicError(r.Error)
		results[i] = r
	}
	run.Results = results
	return run
}
