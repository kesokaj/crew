package main

import "testing"

// Only a terminal may hold gcloud: no run and no conversation gets it (AGENTS.md safety rule 8, design
// 0.79), so crew admin sandbox start refuses --gcloud for any other kind, before it opens the machine.
func TestOnlyATerminalTakesGcloud(t *testing.T) {
	for _, kind := range []string{"run", "conversation", "judge", "check"} {
		if _, err := startSpec([]string{kind, "--gcloud"}); err == nil {
			t.Errorf("%s with --gcloud: no error", kind)
		} else if _, ok := err.(usageError); !ok {
			t.Errorf("%s with --gcloud: %v is not a usage error", kind, err)
		}
	}
	if s, err := startSpec([]string{"terminal", "--gcloud"}); err != nil || !s.Gcloud || s.Kind != "terminal" {
		t.Fatalf("a terminal with --gcloud: %+v %v", s, err)
	}
	if s, err := startSpec([]string{"run", "--cli", "agy", "--repo", "o/r"}); err != nil || s.Gcloud || s.CLI != "agy" || len(s.Repos) != 1 || !s.Minimal {
		t.Fatalf("a run without it: %+v %v", s, err)
	}
}
