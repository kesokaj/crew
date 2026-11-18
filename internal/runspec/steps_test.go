package runspec

import (
	"strings"
	"testing"
)

// Every step of crew-run's own has a sentence that says where its error is (13.9).
func TestEveryStepHasASentence(t *testing.T) {
	for _, step := range []string{StepRunFile, StepCLI, StepPrompt, StepToken, StepBroker, StepCredentials, StepMCP,
		StepSettings, StepWorkspace, StepSpool, StepStderr, StepStart, StepSetupLog} {
		s, ok := StepDetail(step)
		if !ok || !strings.HasSuffix(s, inLog) || len(s) <= len(inLog) {
			t.Errorf("step %q: %q, %v", step, s, ok)
		}
	}
	if len(steps) != 13 {
		t.Errorf("%d steps, not the 13 named above", len(steps))
	}
	if s, _ := StepDetail(StepWorkspace); !strings.HasPrefix(s, WorkspaceFailed) {
		t.Errorf("the workspace's sentence %q does not begin as an eval reads it", s)
	}
}
