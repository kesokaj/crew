package baseline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestTheFilesOfM5AreThere(t *testing.T) {
	for _, p := range []string{"agents/curator.md", "guides/planning.md", "guides/breakdown.md", "templates/plan.md", "templates/task.md", "tools.yaml"} {
		s, err := Text(p)
		if err != nil || strings.TrimSpace(s) == "" {
			t.Errorf("%s: %v, %d bytes", p, err, len(s))
		}
	}
}

// The prompt writer's agent file and its two stage prompts (7.2, M7): the rules, and the fixed forms
// the answer is read in.
func TestThePromptWritersFilesAreThere(t *testing.T) {
	agent := Body(MustText("agents/prompt-writer.md"))
	for _, rule := range []string{"Keep every fact", "Add no wish of your own", "At most three questions", "language the operator wrote in",
		"good as it is", "You do not answer it"} {
		if !strings.Contains(agent, rule) {
			t.Errorf("the agent file lacks %q", rule)
		}
	}
	if s := MustText("stages/rewrite.md"); !strings.Contains(s, "GOOD AS IT IS") || !strings.Contains(s, "## Rewrite\n\n## Changes\n\n## Assumptions\n\n## Questions") {
		t.Errorf("the rewrite's form: %s", s)
	}
	if s := MustText("stages/review-text.md"); !strings.Contains(s, "## Notes") || !strings.Contains(s, "at most five notes") {
		t.Errorf("the review's form: %s", s)
	}
}

// Since design 0.53 Crew sets no budget and names no amount of money, in its prompts as on its screens.
// The Run screen marks a saved prompt as a record (data-free-text), so the gate's browser check no
// longer reads it: this keeps Crew's own prompt files free of what that check looked for.
func TestNoPromptFileNamesAnAmountOrABudget(t *testing.T) {
	money := regexp.MustCompile(`(?i)\$\s*\d|€\s*\d|\d\s*€|\bUSD\b|\bEUR\b|\bdollars?\b|\bbudgets?\b`)
	for _, p := range Paths() {
		if m := money.FindString(MustText(p)); m != "" {
			t.Errorf("%s says %q", p, m)
		}
	}
}

// The rest of what Crew writes into a prompt, a run's title, or a stop text is built in Go, and the
// Run and ticket screens mark it with the agents' words (data-free-text). So no string in Crew's Go
// code names or formats an amount of money, by the patterns of the gate's browser check
// (tools/e2e/browser/gate/nodollars.ts) and Go's verbs after a dollar sign: no "$3", "$%.2f", "3 €",
// USD, EUR or dollars. Comments and struct tags are not read. A SQL text's placeholders ("= $1",
// "($2", ", $3", or a text with an upper-case SQL word), a quoted "$1" of a shell, and a regexp
// replacement that is "$1" alone are not amounts.
func TestNoGoTextNamesAnAmount(t *testing.T) {
	money := regexp.MustCompile(`(?i)\$\s*\d|\$\s*%[-+ #0-9.]*[dfgv]|€\s*\d|\d\s*€|\bUSD\b|\bEUR\b|\bdollars?\b`)
	sql := regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE|WHERE|VALUES|RETURNING|FROM|SET|JOIN|AND|OR|IN|ORDER|LIMIT|OFFSET)\b|=\s*\$[\d%]|\(\$\d|,\s*\$\d`)
	placeholder := regexp.MustCompile(`(["(=,/@\x{200b}])\$\d+`)
	alone := regexp.MustCompile(`^\$\d+$`)
	read := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// spike holds the reference recordings and code of the spike, which Crew does not run.
			case ".git", ".agents", ".check", "node_modules", "testdata", "web", "docs", "spike":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		read++
		tags := map[*ast.BasicLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.Field); ok && fd.Tag != nil {
				tags[fd.Tag] = true
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || tags[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			if alone.MatchString(s) {
				return true
			}
			if sql.MatchString(s) {
				s = strings.ReplaceAll(s, "$", "")
			}
			if m := money.FindString(placeholder.ReplaceAllString(s, "$1")); m != "" {
				t.Errorf("%s: %q says %q", path, s, m)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A walk from the wrong folder would read nothing and pass on nothing: Crew has some 250 Go files.
	if read < 200 {
		t.Fatalf("read %d Go files", read)
	}
}

func TestBodyLeavesOutTheFrontMatter(t *testing.T) {
	b := Body(MustText("agents/curator.md"))
	if strings.Contains(b, "description:") || !strings.HasPrefix(b, "You are the curator") {
		t.Fatalf("Body = %.80q", b)
	}
	if Body("no front matter") != "no front matter" {
		t.Fatal("a text without front matter changed")
	}
}

func TestEveryToolHasWordsAndRoles(t *testing.T) {
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 9 {
		t.Fatalf("tools.yaml has %d tools", len(tools))
	}
	for name, tl := range tools {
		if tl.Group == "" {
			t.Errorf("%s has no group", name)
		}
	}
}

// The files a project may replace are the baseline's markdown, and the checking roles' and the prompt
// writer's wait for the operator (9.6, 7.2).
func TestWhichFilesAProjectMayReplace(t *testing.T) {
	for path, want := range map[string]bool{"stages/in_progress.md": true, "agents/reviewer.md": true, "guides/breakdown.md": true,
		"templates/task.md": true, "tools.yaml": false, "roles.yaml": false, "agents/nobody.md": false, "../AGENTS.md": false} {
		if Overridable(path) != want {
			t.Errorf("Overridable(%s) = %v", path, !want)
		}
	}
	for _, p := range append(append([]string{}, checking...), promptWriter...) {
		if !Overridable(p) {
			t.Errorf("%s is named but is no file of the baseline", p)
		}
		if !WaitsForOperator(p) {
			t.Errorf("%s does not wait", p)
		}
	}
	if WaitsForOperator("stages/in_progress.md") || WaitsForOperator("agents/builder.md") {
		t.Error("a builder's file waits")
	}
}
