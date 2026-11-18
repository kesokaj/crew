package main

import (
	"fmt"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// own is an end of crew-run's own at a step: exit.json names the step, and gives its fixed sentence
// as the detail and the error apart, which the server logs with the run (13.9). An error here can
// name the sandbox's door, or carry git's text of files an agent committed.
func own(step, format string, args ...any) runspec.Exit {
	detail, _ := runspec.StepDetail(step)
	return runspec.Exit{Exit: -1, Reason: cli.ReasonError, Step: step, Detail: detail, Error: fmt.Sprintf(format, args...)}
}

// wipNotKept notes on a turn's end that its work in progress was not kept, with git's error apart.
func wipNotKept(ex *runspec.Exit, err error) {
	ex.Detail += runspec.WIPNotKept
	ex.WIPNotKept = true
	note := "the work in progress was not kept: " + err.Error()
	if ex.Error != "" {
		note = ex.Error + "; " + note
	}
	ex.Error = note
}
