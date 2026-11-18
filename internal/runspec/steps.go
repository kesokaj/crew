package runspec

// The steps of crew-run's own ends of a turn, which exit.json names in Step. The run's end detail of
// such an end is the step's fixed sentence, which the server writes itself, and crew-run's error goes
// to the server, which logs it with the run: an error can name the sandbox's door and git's text of
// files an agent committed, and exit.json is in the agent's own home, so its words are the sandbox's
// (13.9, since 1.00).
const (
	StepRunFile     = "run.json"
	StepCLI         = "cli"
	StepPrompt      = "prompt"
	StepToken       = "token"
	StepBroker      = "broker"
	StepCredentials = "credentials"
	StepMCP         = "mcp"
	StepSettings    = "settings"
	StepWorkspace   = "workspace"
	StepSpool       = "spool"
	StepStderr      = "stderr"
	StepStart       = "start"
	StepSetupLog    = "setup-log"
)

const inLog = "; the error is in Crew's server log"

var steps = map[string]string{
	StepRunFile:     "crew-run could not read run.json" + inLog,
	StepCLI:         "run.json names no CLI crew-run knows" + inLog,
	StepPrompt:      "crew-run could not read the turn's prompt" + inLog,
	StepToken:       "crew-run could not read the run's token" + inLog,
	StepBroker:      "crew-run could not take the run's credentials from the broker" + inLog,
	StepCredentials: "crew-run could not put the run's credentials into its home" + inLog,
	StepMCP:         "Crew's MCP server does not answer" + inLog,
	StepSettings:    "crew-run could not write the CLI's settings" + inLog,
	StepWorkspace:   WorkspaceFailed + "crew-run could not make it" + inLog,
	StepSpool:       "crew-run could not open the turn's spool" + inLog,
	StepStderr:      "crew-run could not open the turn's standard error" + inLog,
	StepStart:       "the CLI did not start" + inLog,
	StepSetupLog:    "crew-run could not open the setup's log" + inLog,
}

// StepDetail is the fixed sentence of a step of crew-run's own, and whether it is one.
func StepDetail(step string) (string, bool) {
	s, ok := steps[step]
	return s, ok
}

// NoStep is the end detail of a turn whose exit.json names a step that is not one of crew-run's, or
// names one with a reason other than error. Its error is not logged: such an end is not crew-run's.
const NoStep = "the turn named a step that is not one of crew-run's"

// WIPNotKept follows the end detail of a turn whose work in progress crew-run could not keep. The
// server adds it itself, once, by WIPNotKept in exit.json, and logs git's error with the run.
const WIPNotKept = "; the work in progress was not kept" + inLog
