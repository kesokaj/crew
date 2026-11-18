package runs

// What a run's end detail says when Crew's own error ended it (13.9). The lead reads NoStart when it
// looks again; it leaves out a restart's end, so the other two reach only the operator's pages. The
// error of Docker, of a microVM's start, or of the database can name the server's paths: the error
// goes to the server's log, with the run, and the detail holds none of it.
const (
	// NoStart ends a run whose microVM did not start.
	NoStart = "the microVM did not start; the error is in Crew's server log"
	// NoScopeAfterRestart ends a run whose subtask could not be read after a restart.
	NoScopeAfterRestart = "its subtask could not be read after the restart; the error is in Crew's server log"
	// NotFollowedAfterRestart ends a run the harness could not follow again after a restart.
	NotFollowedAfterRestart = "it could not be followed again after the restart; the error is in Crew's server log"
)
