package release_test

import (
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// familyGuards are the functions that keep a check off the builder's family, by package (safety rule
// 15). In internal/pipeline: the read of the ticket's builder's last run, whether a role checks, a
// ticket's check's family to avoid with its wait, a subtask's next choice at a limit, a fit check's two
// models, and an eval's start (since 1.14: the operator's popup answer of session 81, "New
// checkfamily.go + atlimit.go"). In internal/runs: the guard every run passes, at its start and at
// Crew's move of it, with its reads, the news it records, and the two functions that call it; and the
// test of internal/models it asks (since 1.15: the operator's popup answer of session 82,
// "runs/checkguard.go"). Each is protected (16.8), wherever a split puts it.
var familyGuards = map[string][]string{
	"pipeline": {"builderModel", "checksBuild", "checksTicketBuild", "avoidBuilder", "Pipeline.NextChoice", "Pipeline.fitModels",
		"Pipeline.startEval"},
	"runs": {"BuilderModel", "EvalBuilderModel", "checkModelRole", "Driver.checkFamily", "Driver.checkMove", "ruleNotMet",
		"Driver.StartRole", "Driver.continueNew"},
	"models": {"RuleCannotBeMet", "state.otherFamilyListed", "CheckRule"},
}

func TestEveryFamilyGuardIsInAProtectedFile(t *testing.T) {
	for pkg, guards := range familyGuards {
		where := definedIn(t, pkg)
		for _, name := range guards {
			path, ok := where[name]
			if !ok {
				t.Errorf("%s is defined in no file of internal/%s", name, pkg)
				continue
			}
			running := release.Manifest{Files: map[string]string{path: "a"}}
			next := release.Manifest{Files: map[string]string{path: "b"}}
			if k := release.Classify(running, next, 2); len(k.Protected) == 0 {
				t.Errorf("%s is in %s, which is not protected", name, path)
			}
		}
	}
}
