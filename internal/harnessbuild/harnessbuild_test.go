package harnessbuild

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// harnessDir is Crew's own harness, read by the tests that pin what it holds.
var harnessDir = filepath.Join("..", "..", "harness")

func fileByRel(t *testing.T, files []File, rel string) File {
	t.Helper()
	for _, f := range files {
		if f.Rel == filepath.FromSlash(rel) {
			return f
		}
	}
	t.Fatalf("%s is not planned", rel)
	return File{}
}

// agy cuts a rule file at 24,000 bytes and the context and rules share one
// block: the build refuses either, naming the file and the limit, and builds
// a harness just under the budget.
func TestPlanRefusesWhatAgyWouldCut(t *testing.T) {
	plan := func(rule string) (Skeleton, error) {
		files := fsHarness()
		files["rules/big.md"] = rule
		return Plan(mapSource(files))
	}
	pad := func(n int) string {
		return "---\ndescription: \"big\"\n---\n" + strings.Repeat("x", n-len("---\ndescription: \"big\"\n---\n"))
	}
	// The rule's name is in the context's inventory too, so the room is
	// measured with the rule there.
	base, err := plan(pad(100))
	if err != nil {
		t.Fatal(err)
	}
	room := ContextBudget - (base.AgyRulesBytes - 100)

	sk, err := plan(pad(room))
	if err != nil {
		t.Fatalf("a rule that fills the budget exactly was refused: %v", err)
	}
	if sk.AgyRulesBytes != ContextBudget {
		t.Errorf("agy rules block = %d, want %d", sk.AgyRulesBytes, ContextBudget)
	}
	if _, err := plan(pad(room + 1)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("a rule one byte over the budget: err = %v, want the budget refusal", err)
	}
	if _, err := plan(pad(AgyRuleFileLimit + 1)); err == nil || !strings.Contains(err.Error(), "big.md") || !strings.Contains(err.Error(), "cuts a rule file") {
		t.Errorf("a rule over agy's cut: err = %v, want the refusal naming big.md", err)
	}
	big := fsHarness()
	big["context/AGENTS.md"] = strings.Repeat("y", AgyRuleFileLimit)
	if _, err := Plan(mapSource(big)); err == nil || !strings.Contains(err.Error(), "rendered context") {
		t.Errorf("a context over agy's cut: err = %v, want the refusal naming the context", err)
	}
}

// Write puts every planned file in place with its planned mode, and every
// directory at 0755, whatever the umask; it refuses a directory that already
// holds files.
func TestWriteMakesExactlyThePlan(t *testing.T) {
	sk, err := Plan(mapSource(fsHarness()))
	if err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	out := filepath.Join(t.TempDir(), "skel")
	if err := Write(out, sk.Files); err != nil {
		t.Fatal(err)
	}
	want := map[string]File{}
	for _, f := range sk.Files {
		want[f.Rel] = f
	}
	seen := 0
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		if d.IsDir() {
			if info.Mode().Perm() != 0o755 {
				t.Errorf("directory %s is %o, want 755", rel, info.Mode().Perm())
			}
			return nil
		}
		f, ok := want[rel]
		if !ok {
			t.Errorf("%s was written but not planned", rel)
			return nil
		}
		seen++
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if string(data) != string(f.Data) {
			t.Errorf("%s holds other bytes than planned", rel)
		}
		if info.Mode().Perm() != f.Mode.Perm() {
			t.Errorf("%s is %o, want %o", rel, info.Mode().Perm(), f.Mode.Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != len(sk.Files) {
		t.Errorf("%d files written, %d planned", seen, len(sk.Files))
	}
	if err := Write(out, sk.Files); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a second write into the same directory: err = %v, want the not-empty refusal", err)
	}
	if err := Write(t.TempDir(), []File{{Rel: "../escape", Data: []byte("x"), Mode: 0o644}}); err == nil {
		t.Error("a file outside the skeleton was written")
	}
}

// What Crew's harness holds is design 17.3: the craft skills and find-skills,
// seven subagents (no dreamer), one rule, and none of the way-of-working or
// harness-itself skills, whose content moves into the stage prompts.
func TestHarnessHoldsWhatTheDesignKeeps(t *testing.T) {
	inv, err := LoadInventory(DirSource(harnessDir))
	if err != nil {
		t.Fatal(err)
	}
	var skills, agents, rules []string
	for _, s := range inv.Skills {
		skills = append(skills, s.Name)
	}
	for _, a := range inv.Agents {
		agents = append(agents, a.Name)
	}
	for _, r := range inv.Rules {
		rules = append(rules, r.Name)
	}
	wantSkills := []string{"api-auth-gcp", "bash", "cloud-run-deploy", "codebase-design", "data-gcp", "docker", "find-skills",
		"frontend", "frontend-ui-economist", "frontend-ui-modern", "gcloud-setup", "gke-basics", "golang", "google-catalog", "json-api-style", "mermaid-syntax", "operate-gcp",
		"review-standards", "terraform", "testing-strategy", "typescript"}
	wantAgents := []string{"bugs", "changelog", "documentation", "explorer", "reviewer", "security", "verifier"}
	for _, c := range []struct {
		what      string
		got, want []string
	}{{"skills", skills, wantSkills}, {"agents", agents, wantAgents}, {"rules", rules, []string{"code-modularity", "critical-thinking"}}} {
		sort.Strings(c.want)
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.what, c.got, c.want)
		}
	}
	if len(inv.Commands) != 0 {
		t.Errorf("commands = %v, want none", inv.Commands)
	}
}

// sh-agent is not used in Crew (design 12.1): no file of the harness names it,
// and none names the trail records of the repository it came from.
func TestHarnessNamesNoShAgent(t *testing.T) {
	err := filepath.WalkDir(harnessDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.Contains(p, filepath.Join("google-catalog", "catalog")+string(filepath.Separator)) && !strings.HasSuffix(p, "MANIFEST.md") {
			return nil // vendored text, byte for byte at its pins
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, banned := range []string{"sh-agent", "audit-fix", ".agents/docs/plans/20"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s names %q", p, banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The whole block agy builds from its rules directory stays under the budget,
// and the context's hand-written part under its own target: every run reads
// all of it (design 12.2, 12.9), so a rule that does not fit is paid for by
// a deletion or moves into a skill, and the budget is never raised to fit.
func TestHarnessContextFitsAgy(t *testing.T) {
	sk, err := Plan(DirSource(harnessDir))
	if err != nil {
		t.Fatal(err)
	}
	if sk.AgyRulesBytes > ContextBudget {
		t.Fatalf("agy rules block %d bytes, budget %d", sk.AgyRulesBytes, ContextBudget)
	}
	ctx := string(fileByRel(t, sk.Files, ".gemini/config/rules/AGENTS.md").Data)
	if !strings.HasSuffix(ctx, "\n"+Canary+"\n") {
		t.Error("the context does not end with the canary")
	}
	written := ctx
	if i := strings.Index(written, "\n## Installed"); i >= 0 {
		written = written[:i]
	}
	const target = 15000
	if len(written) > target {
		t.Errorf("hand-written context is %d bytes, target %d: move a whole rule into the skill or agent that acts on it", len(written), target)
	}
	t.Logf("context %d bytes (hand-written %d), agy rules block %d of %d", sk.ContextBytes, len(written), sk.AgyRulesBytes, ContextBudget)
}

// claude shows the descriptions of about 19,800 bytes of skills at session
// start and bare names beyond that (measured on 2026-10-01 with 173 skills),
// so a skill past the budget has nothing that tells the model when to load
// it. agy's budget is larger.
func TestHarnessSkillListingBudget(t *testing.T) {
	const budget = 19500
	skills, err := LoadSkills(DirSource(harnessDir))
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	var sizes []string
	for _, s := range skills {
		if s.SlashOnly {
			continue
		}
		sum += len(s.Name) + len(s.Description)
		sizes = append(sizes, fmt.Sprintf("%s (%d)", s.Name, len(s.Description)))
	}
	if sum > budget {
		t.Errorf("skill listing is %d bytes of names and descriptions, budget %d: %s", sum, budget, strings.Join(sizes, ", "))
	}
}
