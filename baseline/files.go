package baseline

import (
	"slices"
	"strings"
)

// checking are the files of the roles that check: the reviewer, the tester, the E2E tester, the
// verifier, and the judge (AGENTS.md rule 15). A change to one waits for the operator, in an override
// or an improvement (9.6, 10.4).
var checking = []string{
	"agents/reviewer.md", "agents/security.md", "agents/tester.md", "agents/e2e.md", "agents/verifier.md", "agents/judge.md",
	"stages/review.md", "stages/test.md", "stages/e2e.md", "stages/verification.md", "stages/judge.md",
}

// measurable are the files an eval's runs read that a change may make without the operator: the
// builder's. An eval gives the builder, the reviewer, and the tester their stage and agent files, and the
// other two are the roles that check (security review of M15).
var measurable = []string{"agents/builder.md", "stages/in_progress.md"}

// Measurable says whether the evals measure a change of a file of the baseline: an eval's runs read it.
func Measurable(path string) bool { return slices.Contains(measurable, path) }

// promptWriter are the files of the prompt writer's rules: no eval case measures a rewrite, so a change
// to one waits for the operator too (7.2).
var promptWriter = []string{"agents/prompt-writer.md", "stages/rewrite.md", "stages/review-text.md"}

// Checking says whether a file of the baseline is one of a role that checks.
func Checking(path string) bool { return slices.Contains(checking, path) }

// PromptWriter says whether a file of the baseline holds the prompt writer's rules.
func PromptWriter(path string) bool { return slices.Contains(promptWriter, path) }

// WaitsForOperator says whether a change to a file of the baseline waits for the operator: a checking
// role's, the prompt writer's, and every file the evals cannot measure, since no eval runs it.
func WaitsForOperator(path string) bool {
	return Checking(path) || PromptWriter(path) || !Measurable(path)
}

// evalRead are the files an eval's runs read (9.6): the builder's, the reviewer's, and the tester's
// stage and agent files. A release's suite lays the candidate's over the baseline (16.2 step 5, since
// 0.52).
var evalRead = []string{"stages/in_progress.md", "stages/review.md", "stages/test.md", "agents/builder.md", "agents/reviewer.md", "agents/tester.md"}

// EvalFiles lists the files of the baseline an eval's runs read.
func EvalFiles() []string { return slices.Clone(evalRead) }

// Overridable says whether a project may replace a file of the baseline with its own (10.3): the
// markdown of the agents, the stages, the guides, and the templates. tools.yaml and roles.yaml are
// not: they are the tools' words and the models, which no project changes.
func Overridable(path string) bool {
	if !strings.HasSuffix(path, ".md") {
		return false
	}
	dir, _, _ := strings.Cut(path, "/")
	if !slices.Contains([]string{"agents", "stages", "guides", "templates"}, dir) {
		return false
	}
	_, err := Text(path)
	return err == nil
}
