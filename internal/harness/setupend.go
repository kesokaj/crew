package harness

// SetupCheck names the result a check with no command gets of a project's setup that failed: the
// setup's own, followed by its command, as the project's tests are named among a check's results.
const SetupCheck = "the project's setup: "

// setupEnded is what a check sandbox gives when the project's setup ended the check with end, before
// any command ran: each command gets the setup's end under its own name, and a check with no command
// gets one result of the setup's own, so the setup's end is read as it is with commands and never as
// a check that passed (13.9, since 1.11).
func setupEnded(spec CheckSpec, end CheckResult) []CheckResult {
	end.Passed = false
	if len(spec.Cmds) == 0 {
		end.Name = SetupCheck + spec.Setup
		return []CheckResult{end}
	}
	results := make([]CheckResult, 0, len(spec.Cmds))
	for _, c := range spec.Cmds {
		end.Name = c.Name
		results = append(results, end)
	}
	return results
}
