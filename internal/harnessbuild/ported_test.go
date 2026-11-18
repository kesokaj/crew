// The tests below came with the code from styrman-harness's internal/harness
// (43faca0) and keep its behaviour; they are changed only where Crew's build
// differs from an install: one skeleton for both CLIs, no manifest.
package harnessbuild

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// planOf is Plan's files, the shape the ported tests were written against.
func planOf(src Source) ([]File, error) {
	sk, err := Plan(src)
	return sk.Files, err
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo builds a minimal harness checkout: two skills, one agent, two
// context files. Tests never touch the real $HOME.
func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("context/AGENTS.md", "# AGENTS.md\n\nBe honest.\n")
	write("context/dev-setup.md", "## Environment & Stack\n\n- GCP only.\n")
	write("skills/alpha/SKILL.md", "---\nname: alpha\ndescription: |\n  Alpha does things.\n  Use it for alpha work.\nmetadata:\n  version: \"1\"\n---\nbody\n")
	write("skills/alpha/references/notes.md", "notes\n")
	write("skills/beta/SKILL.md", "---\nname: beta\ndescription: \"Beta: slash only\"\ndisable-model-invocation: true\n---\nbody\n")
	write("skills/VENDORED.md", "not a skill\n")
	write("commands/deploy.md", "---\ndescription: Deploy the thing\n---\nSteps\n")
	write("rules/go-style.md", "# Go style\n\nUse slog.\n")
	write("agents/reviewer.md", "---\nname: reviewer\ndescription: Reviews diffs\ntools: Read, Grep, Bash\nmodel: opus\nskills: review-standards\neffort: high\nmaxTurns: 40\n---\nLoad ~/.claude/skills/review-standards first.\n")
	return repo
}

func mapSource(files map[string]string) Source {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body), Mode: 0o444}
	}
	return Source{FS: m}
}

func fsHarness() map[string]string {
	return map[string]string{
		"context/AGENTS.md":                "# AGENTS.md\n\nBe honest.\n",
		"context/dev-setup.md":             "## Environment\n",
		"skills/alpha/SKILL.md":            "---\nname: alpha\ndescription: \"Alpha does things\"\n---\nbody\n",
		"skills/alpha/references/notes.md": "notes\n",
		"skills/alpha/assets/.gitkeep":     "",
		"skills/VENDORED.md":               "not a skill\n",
		"skills/notaskill/README.md":       "no SKILL.md here\n",
		"agents/reviewer.md":               "---\nname: reviewer\ndescription: Reviews diffs\ntools: Read, Grep\n---\nbody\n",
		"commands/deploy.md":               "---\ndescription: Deploy the thing\n---\nSteps\n",
		"workflows/ship.md":                "# Ship it\n",
		"rules/go-style.md":                "# Go style\n",
	}
}

func TestParseFrontmatter(t *testing.T) {
	front := "name: x\ndescription: |\n  line one\n  line two\nquoted: \"a: b\"\nmetadata:\n  version: \"1.2\"\n  author: me\ntools: [Read, Grep]\nskills:\n  - alpha\n  - beta\nplain: first\n  second\nempty:\n"
	fm := Parse(front)
	want := map[string]string{
		"name":        "x",
		"description": "line one line two",
		"quoted":      "a: b",
		"tools":       "[Read, Grep]",
		"skills":      "[alpha, beta]",
		"plain":       "first second",
		"empty":       "",
	}
	for k, v := range want {
		if fm[k] != v {
			t.Errorf("%s = %q, want %q", k, fm[k], v)
		}
	}
	if _, ok := fm["metadata"]; ok {
		t.Errorf("nested map should be skipped, got %q", fm["metadata"])
	}
	if got := SplitList("[Read, Grep]"); strings.Join(got, ",") != "Read,Grep" {
		t.Errorf("SplitList brackets = %v", got)
	}
	if got := SplitList("Read Grep"); strings.Join(got, ",") != "Read,Grep" {
		t.Errorf("SplitList spaces = %v", got)
	}
}

func TestLintRejectsUnquotedComment(t *testing.T) {
	// A plain scalar with " #" is a YAML comment start: strict parsers
	// truncate it or, inside a flow sequence, fail the whole file. The
	// agy dropped two skills that way.
	bad := []string{
		"name: x\nargument-hint: [feature description or #issue]\n",
		"name: x\ndescription: fixes #1 and #2\n",
		"name: x\ndescription: #comment only\n",
		"name: x\ndescription: first line\n  wraps with #issue\n",
		"name: x\ndescription: value\t#tab-comment\n",
		"name: x\ndescription: \"quoted\" # trailing\n",
	}
	for _, front := range bad {
		if _, err := ParseStrict(front); err == nil {
			t.Errorf("Lint accepted %q", front)
		} else if !strings.Contains(err.Error(), "#") {
			t.Errorf("error should name the marker: %v", err)
		}
	}
	good := []string{
		"name: x\nargument-hint: \"[feature description or #issue]\"\n",
		"name: x\nargument-hint: '[#issue]'\n",
		"name: x\ndescription: |\n  a #b\n  c #d\nmetadata:\n  tag: \"#1\"\n",
		"name: x\ntools: [Read, Grep]\ncolor: \"#ff0000\"\n",
	}
	for _, front := range good {
		if _, err := ParseStrict(front); err != nil {
			t.Errorf("Lint rejected %q: %v", front, err)
		}
	}
}

func TestLoadSkillsRejectsUnquotedComment(t *testing.T) {
	repo := newRepo(t)
	p := filepath.Join(repo, "skills", "gamma", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: gamma\ndescription: Gamma\nargument-hint: [topic or #issue]\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSkills(DirSource(repo))
	if err == nil || !strings.Contains(err.Error(), "gamma") || !strings.Contains(err.Error(), "argument-hint") {
		t.Fatalf("LoadSkills should name the skill and the key, got %v", err)
	}
}

func TestSplitRejectsMissingFrontmatter(t *testing.T) {
	if _, _, err := Split("# no frontmatter\n"); err == nil {
		t.Fatal("expected an error")
	}
	if _, _, err := Split("---\nname: x\n"); err == nil {
		t.Fatal("expected an error for an unterminated block")
	}
}

func TestDeriveAgy(t *testing.T) {
	repo := newRepo(t)
	agents, err := LoadAgents(DirSource(repo))
	if err != nil {
		t.Fatal(err)
	}
	out, err := DeriveAgy(agents[0])
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"name: reviewer\n",
		"description: \"Reviews diffs\"\n",
		"subagent: true\n",
		"mainAgent: false\n",
		"model: pro\n",
		"tools: [view_file, grep_search, run_command]\n",
		"~/.gemini/config/skills/review-standards",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("derived agent missing %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"effort:", "skills:", "maxTurns:", "~/.claude/"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("derived agent should not contain %q:\n%s", unwanted, s)
		}
	}

	bad := agents[0]
	bad.Front = Frontmatter{"tools": "Read, WebSearch"}
	if _, err := DeriveAgy(bad); err == nil {
		t.Fatal("unmapped tool should be an error")
	}
}

func TestInventoryAndRender(t *testing.T) {
	repo := newRepo(t)
	skills, err := LoadSkills(DirSource(repo))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "alpha" || skills[1].Name != "beta" {
		t.Fatalf("skills = %+v", skills)
	}
	if !skills[1].SlashOnly || skills[0].SlashOnly {
		t.Errorf("SlashOnly wrong: %+v", skills)
	}
	inv, err := LoadInventory(DirSource(repo))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Commands) != 1 || inv.Commands[0].Name != "deploy" || inv.Commands[0].Description != "Deploy the thing" {
		t.Errorf("commands = %+v", inv.Commands)
	}
	if len(inv.Rules) != 1 || inv.Rules[0].Description != "Go style" {
		t.Errorf("rules = %+v", inv.Rules)
	}
	ctx, err := RenderContext(DirSource(repo), inv)
	if err != nil {
		t.Fatal(err)
	}
	s := string(ctx)
	if !strings.HasPrefix(s, Marker+"\n# AGENTS.md") {
		t.Errorf("context should start with the marker and AGENTS.md:\n%s", s[:80])
	}
	for _, want := range []string{"## Environment & Stack", "`alpha`", "`/beta`", "`reviewer` (opus)", "`/deploy`", "`go-style`"} {
		if !strings.Contains(s, want) {
			t.Errorf("context missing %q", want)
		}
	}
	if strings.Contains(s, "Alpha does things") {
		t.Errorf("deployed context should carry names only, not descriptions")
	}
	// The canary is the LAST line: a check that reads it proves the whole
	// file loaded. Anything after it would be unproven.
	if !strings.HasSuffix(s, "\n"+Canary+"\n") {
		t.Errorf("context should end with the canary line %q, ends with %q", Canary, s[max(0, len(s)-80):])
	}
}

func TestSlashNoteOnlyWhenSomeSkillIsSlashOnly(t *testing.T) {
	repo := newRepo(t)
	inv, err := LoadInventory(DirSource(repo))
	if err != nil {
		t.Fatal(err)
	}
	const marked = "Skills marked `/name` are typed by the user"
	if !strings.Contains(RenderInventoryCompact(inv), marked) {
		t.Errorf("fixture has a slash-only skill; the note should be printed")
	}
	for i := range inv.Skills {
		inv.Skills[i].SlashOnly = false
	}
	for _, out := range []string{RenderInventoryCompact(inv)} {
		if strings.Contains(out, marked) {
			t.Errorf("no slash-only skill; the note must not be printed:\n%s", out)
		}
		if !strings.Contains(out, "can also be typed as `/name`") {
			t.Errorf("expected the all-model-invocable sentence:\n%s", out)
		}
	}
}

// TestRenderContextRefusesSymlinks is the link refusal tested directly on
// RenderContext rather than only through Plan: a symlinked context directory
// or context file would let a tree outside the repo decide what every
// machine reads.
func TestRenderContextRefusesSymlinks(t *testing.T) {
	t.Run("context directory is a link", func(t *testing.T) {
		repo := newRepo(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("# hostile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(repo, "context")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, "context")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		inv, _ := LoadInventory(DirSource(repo))
		if _, err := RenderContext(DirSource(repo), inv); err == nil {
			t.Fatal("a symlinked context directory must be refused")
		} else if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("the error should name the symlink, got %v", err)
		}
	})

	t.Run("a file inside context is a link", func(t *testing.T) {
		repo := newRepo(t)
		outside := filepath.Join(t.TempDir(), "extra.md")
		if err := os.WriteFile(outside, []byte("# hostile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, "context", "extra.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		inv, _ := LoadInventory(DirSource(repo))
		if _, err := RenderContext(DirSource(repo), inv); err == nil {
			t.Fatal("a symlinked context file must be refused")
		} else if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("the error should name the symlink, got %v", err)
		}
	})
}

// TestLoadSkillsRefusesDuplicateNames: two directories whose frontmatter
// claims one name install to a single destination, so the walk order alone
// would decide which text every machine reads. That is what an interrupted
// `adopt` used to be able to leave behind (its staging now lives outside
// skills/), and what a hand-copied directory can still produce.
func TestLoadSkillsRefusesDuplicateNames(t *testing.T) {
	repo := newRepo(t)
	dup := filepath.Join(repo, "skills", "alpha-copy")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: alpha\ndescription: a second directory claiming the same name\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dup, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSkills(DirSource(repo))
	if err == nil {
		t.Fatal("two skills named alpha must be refused")
	}
	for _, want := range []string{"alpha", "alpha-copy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s, got %v", want, err)
		}
	}
}

// TestPlanRefusesSecretShapedFiles: a credential that slipped past
// .gitignore into a skill directory is invisible to review (git never shows
// it) and install is the one step that would copy it into both vendor
// directories on every machine.
func TestPlanRefusesSecretShapedFiles(t *testing.T) {
	for _, name := range []string{".env", ".env.local", "server.key", "cert.pem", "creds.tfvars", "application_default_credentials.json", "sa-key.json"} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			if err := os.WriteFile(filepath.Join(repo, "skills", "alpha", name), []byte("secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := planOf(DirSource(repo)); err == nil {
				t.Fatalf("%s must not be deployed", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("the error should name the file, got %v", err)
			}
		})
	}
	t.Run("an ordinary reference file still plans", func(t *testing.T) {
		repo := newRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "skills", "alpha", "keys-explained.md"), []byte("about keys\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := planOf(DirSource(repo))
		if err != nil {
			t.Fatalf("a normal file must still plan: %v", err)
		}
		var found bool
		for _, f := range files {
			if strings.HasSuffix(f.Rel, "keys-explained.md") {
				found = true
			}
		}
		if !found {
			t.Error("the reference file should have been planned")
		}
	})
}

// TestPlanDropsGroupAndOtherWrite: a skill file that is group-writable in
// the repo must not land group-writable in ~/.claude, where its contents are
// instructions every session reads.
func TestPlanDropsGroupAndOtherWrite(t *testing.T) {
	repo := newRepo(t)
	loose := filepath.Join(repo, "skills", "alpha", "loose.md")
	if err := os.WriteFile(loose, []byte("reference\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o666); err != nil {
		t.Fatal(err)
	}
	files, err := planOf(DirSource(repo))
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, f := range files {
		if f.Mode&0o022 != 0 {
			t.Errorf("%s would be deployed mode %o — group or other writable", f.Rel, f.Mode)
		}
		if strings.HasSuffix(f.Rel, "loose.md") {
			seen++
			if f.Mode.Perm() != 0o644 {
				t.Errorf("loose.md mode = %o, want 644", f.Mode.Perm())
			}
		}
	}
	if seen == 0 {
		t.Error("the loose file should still be planned")
	}
}

// TestAgentDescriptionRules: an agent's description is one line and names
// neither "dispatch prompt" nor "harness binary". Until now only inspection enforced it.
func TestAgentDescriptionRules(t *testing.T) {
	for name, front := range map[string]string{
		"two lines":       "name: x\ndescription: |\n  first line\n  second line\ntools: Read\n",
		"dispatch prompt": "name: x\ndescription: \"the dispatch prompt names the file\"\ntools: Read\n",
		"harness binary":  "name: x\ndescription: \"run by the harness binary\"\ntools: Read\n",
	} {
		repo := newRepo(t)
		os.WriteFile(filepath.Join(repo, "agents", "x.md"), []byte("---\n"+front+"---\nbody\n"), 0o644)
		if _, err := LoadAgents(DirSource(repo)); err == nil {
			t.Errorf("%s: LoadAgents accepted the description", name)
		}
	}
	// The fixture's own agent (one line, plain words) and the real agents pass.
	if _, err := LoadAgents(DirSource(newRepo(t))); err != nil {
		t.Errorf("the fixture's agents: %v", err)
	}
	if _, err := LoadAgents(DirSource(filepath.Join("..", "..", "harness"))); err != nil {
		t.Errorf("the checkout's agents: %v", err)
	}
}

// TestLintDescriptionLimit: a skill or agent description over 1,024
// characters, the Agent Skills spec's limit, is refused; a multi-line
// description is measured after its continuation lines are joined.
func TestLintDescriptionLimit(t *testing.T) {
	long := strings.Repeat("word ", 206) // 1,029 characters after the trailing space is trimmed
	if _, err := ParseStrict("name: x\ndescription: \"" + long + "\"\n"); err == nil {
		t.Error("a 1,025-character description passed Lint")
	}
	block := "name: x\ndescription: |\n"
	for i := 0; i < 11; i++ {
		block += "  " + strings.Repeat("word ", 20) + "\n" // 11 × 100 = 1,100 joined
	}
	if _, err := ParseStrict(block); err == nil {
		t.Error("a 1,100-character block description passed Lint")
	}
	if _, err := ParseStrict("name: x\ndescription: \"" + strings.Repeat("word ", 204) + "\"\n"); err != nil {
		t.Errorf("a 1,020-character description was refused: %v", err)
	}
	// A block of exactly 1,023 joined characters passes: the `|` indicator
	// and its space are not counted.
	block = "name: x\ndescription: |\n"
	for i := 0; i < 10; i++ {
		block += "  " + strings.Repeat("word ", 20) + "\n"
	}
	block += "  " + strings.Repeat("w", 13) + "\n" // 10 × 99 (joined by spaces) + 1 + 13 = 1,004 + 19 → under the limit
	if _, err := ParseStrict(block); err != nil {
		t.Errorf("a block description under the limit was refused: %v", err)
	}
}

// TestDeriveAgyRealReviewer: the derived Antigravity reviewer names the
// review-standards skill by its agy path, because its Claude body names the
// Claude path.
func TestDeriveAgyRealReviewer(t *testing.T) {
	agents, err := LoadAgents(DirSource(filepath.Join("..", "..", "harness")))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name != "reviewer" {
			continue
		}
		out, err := DeriveAgy(a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "~/.gemini/config/skills/review-standards/SKILL.md") {
			t.Errorf("the derived reviewer does not name the review-standards skill by its Antigravity path")
		}
		if strings.Contains(string(out), "~/.claude/") {
			t.Errorf("the derived reviewer still names a Claude path")
		}
		return
	}
	t.Fatal("no reviewer agent in the checkout")
}

// TestLintRefusesAllowedTools: a skill's frontmatter is a permission surface;
// `allowed-tools` lets every named tool run unprompted while the skill is
// active, so a harness skill never sets it — the operator's permission mode
// decides.
func TestLintRefusesAllowedTools(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(repo, "skills", "x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: a skill\nallowed-tools: Bash\n---\nbody\n"), 0o644)
	if _, err := LoadSkills(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "allowed-tools") {
		t.Errorf("a skill with allowed-tools passed: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: a skill\n---\nbody\n"), 0o644)
	if _, err := LoadSkills(DirSource(repo)); err != nil {
		t.Errorf("the same skill without the key was refused: %v", err)
	}
	if _, err := LoadSkills(DirSource(filepath.Join("..", "..", "harness"))); err != nil {
		t.Errorf("the checkout's skills: %v", err)
	}
}

// TestLintPermissionKeysByKind: the permission-granting frontmatter keys are
// refused per file kind, and a top-level line the installer's key grammar
// cannot read is refused rather than skipped — a quoted or spaced key is the
// same key to a YAML parser.
func TestLintPermissionKeysByKind(t *testing.T) {
	skill := func(front string) error {
		repo := newRepo(t)
		dir := filepath.Join(repo, "skills", "x")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: a skill\n"+front+"---\nbody\n"), 0o644)
		_, err := LoadSkills(DirSource(repo))
		return err
	}
	for name, front := range map[string]string{
		"double-quoted key": "\"allowed-tools\": Bash\n",
		"single-quoted key": "'allowed-tools': Bash\n",
		"spaced colon":      "allowed-tools : Bash\n",
		"block list":        "allowed-tools:\n  - Bash\n",
		"hooks":             "hooks:\n  PreToolUse: []\n",
	} {
		if err := skill(front); err == nil {
			t.Errorf("skill with %s loaded", name)
		}
	}
	for name, front := range map[string]string{
		"metadata map":  "metadata:\n  version: \"1.0\"\n",
		"license":       "license: Apache-2.0\n",
		"comment line":  "# a comment\n",
		"column-0 list": "compatibility:\n- claude\n- agy\n",
	} {
		if err := skill(front); err != nil {
			t.Errorf("skill with %s refused: %v", name, err)
		}
	}
	agent := func(front string) error {
		repo := newRepo(t)
		os.WriteFile(filepath.Join(repo, "agents", "x.md"), []byte("---\nname: x\ndescription: an agent\ntools: Read\n"+front+"---\nbody\n"), 0o644)
		_, err := LoadAgents(DirSource(repo))
		return err
	}
	// mcpServers: an inline MCP server is a command line Claude Code starts
	// when the agent is used, outside the permission mode.
	// An agent key outside the allow-list is refused, whatever it does:
	// `memory` and `initialPrompt` passed a deny-list, and a vendor's next
	// key would too.
	for _, front := range []string{"permissionMode: bypassPermissions\n", "hooks:\n  Stop: []\n", "mcpServers:\n  x:\n    command: sh\n", "mcpServers: {x: {command: sh}}\n",
		"memory: project\n", "initialPrompt: run the tests\n", "\"memory\": user\n", "isolation: worktree\n", "futureKey: x\n",
		// agy's derived agent drops it, so an agent relying on it would be
		// unrestricted there.
		"disallowedTools: Write\n"} {
		if err := agent(front); err == nil {
			t.Errorf("agent with %q loaded", front)
		}
	}
	for _, front := range []string{"model: sonnet\n", "maxTurns: 10\n", "skills: [golang]\n", "effort: high\n", "color: blue\n"} {
		if err := agent(front); err != nil {
			t.Errorf("agent with %q refused: %v", front, err)
		}
	}
	repo := newRepo(t)
	os.MkdirAll(filepath.Join(repo, "commands"), 0o755)
	os.WriteFile(filepath.Join(repo, "commands", "x.md"), []byte("---\ndescription: a command\nallowed-tools: Bash\n---\nbody\n"), 0o644)
	if _, err := LoadInventory(DirSource(repo)); err == nil {
		t.Error("a command with allowed-tools loaded")
	}
	if _, err := LoadInventory(DirSource(filepath.Join("..", "..", "harness"))); err != nil {
		t.Errorf("the checkout: %v", err)
	}
}

// TestLintRefusesUnreadableFrontmatterInDocs: a command or rule whose first
// line opens a frontmatter block that Split cannot read was once treated as
// having none, so the permission-key lint never saw it and the file deployed
// as written. A file with no frontmatter still loads.
func TestLintRefusesUnreadableFrontmatterInDocs(t *testing.T) {
	load := func(kind, text string) error {
		repo := newRepo(t)
		os.MkdirAll(filepath.Join(repo, kind), 0o755)
		os.WriteFile(filepath.Join(repo, kind, "x.md"), []byte(text), 0o644)
		_, err := LoadInventory(DirSource(repo))
		return err
	}
	for _, kind := range []string{"commands", "rules"} {
		for name, text := range map[string]string{
			"a byte-order mark":           "\ufeff---\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"spaces after the opening":    "---  \ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a tab after the opening":     "---\t\ndescription: d\nhooks:\n  Stop: []\n---\nbody\n",
			"an unterminated block":       "---\ndescription: d\nallowed-tools: Bash\nbody with no closing line\n",
			"a carriage return and space": "--- \r\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			// Spellings a lenient reader could still take for a fence
			//.
			"a no-break space after":   "---\u00a0\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a form feed after":        "---\f\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"an ideographic space":     "---\u3000\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"two byte-order marks":     "\ufeff\ufeff---\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a leading blank line":     "\n---\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a tag on the fence":       "---yaml\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a comment on the fence":   "--- # c\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
			"a zero-width space first": "\u200b---\ndescription: d\nallowed-tools: Bash\n---\nbody\n",
		} {
			err := load(kind, text)
			if err == nil || !strings.Contains(err.Error(), "frontmatter starts but cannot be read") {
				t.Errorf("%s with %s: want the unreadable-frontmatter refusal, got %v", kind, name, err)
			}
		}
		for name, text := range map[string]string{
			"no frontmatter":             "# A title\n\nbody\n",
			"a rule line further down":   "A first line.\n\n---\n\nmore\n",
			"a readable block":           "---\ndescription: d\n---\nbody\n",
			"four dashes (a rule, text)": "----\nbody\n",
		} {
			if err := load(kind, text); err != nil {
				t.Errorf("%s with %s refused: %v", kind, name, err)
			}
		}
	}
}

// TestSplitFenceIsOneLine: a fence is `---` and at most one carriage return.
// A line of `---` and two carriage returns once closed the block, so a key
// written after it (and before a real closing line a lenient reader would
// take) was never checked (security read of a62eb9d..768efce).
func TestSplitFenceIsOneLine(t *testing.T) {
	if _, _, err := Split("---\r\nname: x\r\n---\r\nbody\r\n"); err != nil {
		t.Errorf("a CRLF file is not read: %v", err)
	}
	front, _, err := Split("---\nname: x\n---\r\r\nallowed-tools: Bash\n---\nbody\n")
	if err == nil && !strings.Contains(front, "allowed-tools") {
		t.Errorf("the block closed at a fence with two carriage returns; front = %q", front)
	}
	if _, _, err := Split("---\r\r\nname: x\n---\nbody\n"); err == nil {
		t.Error("an opening fence with two carriage returns was accepted")
	}
	repo := newRepo(t)
	dir := filepath.Join(repo, "skills", "x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: a skill\n---\r\r\nallowed-tools: Bash\n---\nbody\n"), 0o644)
	if _, err := LoadSkills(DirSource(repo)); err == nil {
		t.Error("a skill with a permission key behind a two-carriage-return fence loaded")
	} else if !strings.Contains(err.Error(), "line break") || strings.Contains(err.Error(), "quoted or spaced key") {
		// The refusal names the real problem (a line break that is not a
		// newline), not a key that cannot be read.
		t.Errorf("the refusal blames the wrong thing: %v", err)
	}
	// A `---` with a trailing space is not a fence to Split either; inside
	// the block it is refused as a fence that is not one.
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: a skill\n--- \nallowed-tools: Bash\n---\nbody\n"), 0o644)
	if _, err := LoadSkills(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "not a fence") {
		t.Errorf("a skill with a spaced fence inside its block: want the not-a-fence refusal, got %v", err)
	}
}

// TestLintSeesEveryKeyAYAMLReaderSees: three shapes hid a permission key
// from the lint while a YAML reader still read it, or may: a block whose lines are all
// indented (YAML reads a top-level map; the key grammar took the lines for
// continuations of nothing), a line break the lint does not split on inside a
// key line (a lone carriage return, NEL, the Unicode line and paragraph
// separators), and a line that starts with a tab (no YAML indentation: a
// reader errors, nests it, or reads a top-level key). Refused in every kind,
// whatever else the file gets right.
func TestLintSeesEveryKeyAYAMLReaderSees(t *testing.T) {
	const hidden = "allowed-tools: Bash(*)"
	// An agent sets only the keys of its allow-list, so its
	// keep-loading shapes use them; none of them holds a map.
	other := [3]string{"license", "compatibility", "metadata"}
	kinds := map[string]struct {
		keys  []string  // the kind's own required lines
		extra [3]string // a key the kind loads with a value, a list and a map ("" for none)
		load  func(front string) error
	}{
		"skill": {[]string{"name: x", "description: d"}, other, func(front string) error {
			repo := newRepo(t)
			dir := filepath.Join(repo, "skills", "x")
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n"+front+"---\nbody\n"), 0o644)
			_, err := LoadSkills(DirSource(repo))
			return err
		}},
		"agent": {[]string{"name: x", "description: d", "tools: Read"}, [3]string{"color", "skills", ""}, func(front string) error {
			repo := newRepo(t)
			os.WriteFile(filepath.Join(repo, "agents", "x.md"), []byte("---\n"+front+"---\nbody\n"), 0o644)
			_, err := LoadAgents(DirSource(repo))
			return err
		}},
		"command": {[]string{"description: d"}, other, func(front string) error {
			repo := newRepo(t)
			os.MkdirAll(filepath.Join(repo, "commands"), 0o755)
			os.WriteFile(filepath.Join(repo, "commands", "x.md"), []byte("---\n"+front+"---\nbody\n"), 0o644)
			_, err := LoadInventory(DirSource(repo))
			return err
		}},
		"rule": {[]string{"description: d"}, other, func(front string) error {
			repo := newRepo(t)
			os.MkdirAll(filepath.Join(repo, "rules"), 0o755)
			os.WriteFile(filepath.Join(repo, "rules", "x.md"), []byte("---\n"+front+"---\nbody\n"), 0o644)
			_, err := LoadInventory(DirSource(repo))
			return err
		}},
	}
	for kind, k := range kinds {
		plain := strings.Join(k.keys, "\n") + "\n"
		refused := map[string]string{
			"an indented block":      "  " + strings.Join(k.keys, "\n  ") + "\n  " + hidden + "\n",
			"an indented first line": "  " + hidden + "\n" + plain,
			// A tab is no YAML indentation: a strict reader errors on the
			// line, a lenient one nests it, an older one reads a top-level
			// key.
			"a tab-indented first line":        "\t" + hidden + "\n" + plain,
			"a tab-led key after a key":        plain + "\t" + hidden + "\n",
			"a tab-led key after a nested map": plain + "metadata:\n  version: \"1.0\"\n\t" + hidden + "\n",
			"a tab-led key after a list":       plain + "compatibility:\n  - claude\n\t" + hidden + "\n",
			"a tab-led key after an empty key": plain + "metadata:\n\t" + hidden + "\n",
		}
		for name, sep := range map[string]string{
			"a lone carriage return":         "\r",
			"a next-line character (U+0085)": "\u0085",
			"a line separator (U+2028)":      "\u2028",
			"a paragraph separator (U+2029)": "\u2029",
		} {
			refused[name] = strings.TrimSuffix(plain, "\n") + sep + hidden + "\n"
		}
		for name, front := range refused {
			err := k.load(front)
			if err == nil {
				t.Errorf("%s with %s loaded", kind, name)
				continue
			}
			// The refusal is the lint's, not a side effect such as a
			// missing description.
			if msg := err.Error(); !strings.Contains(msg, "indented") && !strings.Contains(msg, "line break") && !strings.Contains(msg, "starts with a tab") {
				t.Errorf("%s with %s refused for another reason: %v", kind, name, err)
			}
		}
		// What must keep loading: a nested map, a list, a CRLF file.
		value, list, nested := k.extra[0], k.extra[1], k.extra[2]
		for name, front := range map[string]string{
			"a nested map":     plain + nested + ":\n  version: \"1.0\"\n",
			"an indented list": plain + list + ":\n  - claude\n",
			"a CRLF file":      strings.ReplaceAll(plain, "\n", "\r\n") + value + ": MIT\r\n",
			"a comment first":  "# a comment\n" + plain,
			// A tab inside a value, and a line that is only a tab, are not
			// tab indentation.
			"a tab inside a value": plain + value + ": MIT\tor Apache\n",
			"a tab-only line":      plain + "\t\n" + value + ": MIT\n",
		} {
			if nested == "" && name == "a nested map" {
				continue
			}
			if err := k.load(front); err != nil {
				t.Errorf("%s with %s refused: %v", kind, name, err)
			}
		}
	}
	// A folded value under its key is a continuation, not a hidden key
	// (skills allow one; agents want a one-line description).
	if err := kinds["skill"].load("name: x\ndescription: >-\n  a description\n  over two lines\n"); err != nil {
		t.Errorf("a skill with a folded description refused: %v", err)
	}
}

func TestPlanFromFSSource(t *testing.T) {
	files, err := planOf(mapSource(fsHarness()))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var got []string
	byRel := map[string]File{}
	for _, f := range files {
		got = append(got, f.Rel)
		byRel[f.Rel] = f
	}
	want := []string{
		".gemini/config/agents/reviewer/agent.md",
		".gemini/config/rules/AGENTS.md",
		".gemini/config/rules/go-style.md",
		".gemini/config/skills/alpha/SKILL.md",
		".gemini/config/skills/alpha/assets/.gitkeep",
		".gemini/config/skills/alpha/references/notes.md",
		".gemini/config/workflows/deploy.md",
		".gemini/config/workflows/ship.md",
	}
	for i := range want {
		want[i] = filepath.FromSlash(want[i])
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("planned files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	notes := byRel[filepath.FromSlash(".gemini/config/skills/alpha/references/notes.md")]
	if string(notes.Data) != "notes\n" {
		t.Errorf("notes.md data = %q", notes.Data)
	}
	// embed reports every file 0444; the deployed copy is owner-writable, the
	// same 0644 a checkout's 0644 file deploys as.
	if notes.Mode != 0o644 {
		t.Errorf("notes.md mode = %v, want 0644", notes.Mode)
	}
	ctx := string(byRel[filepath.FromSlash(".gemini/config/rules/AGENTS.md")].Data)
	for _, part := range []string{Marker, "Be honest.", "## Environment", "Skills (1): `alpha`", "`reviewer` (inherit)", "`/deploy`", "`go-style`"} {
		if !strings.Contains(ctx, part) {
			t.Errorf("rendered context lacks %q", part)
		}
	}
}

// Every refusal a checkout install makes, an install from an FS source makes
// too, naming the file by its path inside the source.
func TestFSSourceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change map[string]string // "" deletes the file
		want   []string
	}{
		{"no context", map[string]string{"context/AGENTS.md": ""}, []string{"reading context"}},
		{"skill without description", map[string]string{"skills/alpha/SKILL.md": "---\nname: alpha\n---\nbody\n"}, []string{"alpha", "no description"}},
		{"secret-shaped file", map[string]string{"skills/alpha/.env": "TOKEN=x\n"}, []string{"skills/alpha/.env", "secret-shaped"}},
		{"duplicate skill name", map[string]string{"skills/alpha-copy/SKILL.md": "---\nname: alpha\ndescription: \"again\"\n---\nbody\n"}, []string{"alpha-copy", "two skills"}},
		{"bad agent name", map[string]string{"agents/reviewer.md": "---\nname: ../x\ndescription: \"x\"\n---\nbody\n"}, []string{"reviewer.md", "single local path segment"}},
		{"unquoted hash", map[string]string{"commands/deploy.md": "---\ndescription: deploy #now\n---\nbody\n"}, []string{"quote the value"}},
		{"unmapped tool", map[string]string{"agents/reviewer.md": "---\nname: reviewer\ndescription: \"x\"\ntools: Teleport\n---\nbody\n"}, []string{"Teleport"}},
		{"command in both dirs", map[string]string{"workflows/deploy.md": "# again\n"}, []string{"commands/deploy.md", "workflows/deploy.md"}},
		{"rule named AGENTS", map[string]string{"rules/AGENTS.md": "# no\n"}, []string{"AGENTS.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fsHarness()
			for k, v := range tc.change {
				if v == "" {
					delete(files, k)
				} else {
					files[k] = v
				}
			}
			_, err := planOf(mapSource(files))
			if err == nil {
				t.Fatalf("Plan succeeded, want an error naming %q", tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %v, want it to contain %q", err, w)
				}
			}
		})
	}
}

// A directory named SKILL.md is not a regular file, from an FS source as from
// a checkout.
func TestFSSourceSkillMdMustBeRegular(t *testing.T) {
	m := fstest.MapFS{}
	for name, body := range fsHarness() {
		m[name] = &fstest.MapFile{Data: []byte(body), Mode: 0o444}
	}
	delete(m, "skills/alpha/SKILL.md")
	m["skills/alpha/SKILL.md/inner"] = &fstest.MapFile{Data: []byte("x"), Mode: 0o444}
	_, err := LoadSkills(Source{FS: m})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("LoadSkills err = %v, want a not-a-regular-file refusal", err)
	}
}

// DirSource reads the tree through os.DirFS and keeps the path for the link
// checks, so the embedded and on-disk readers are one code path.
func TestDirSourceReadsTheSameAsFS(t *testing.T) {
	repo := t.TempDir()
	for name, body := range fsHarness() {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	fromDir, err := planOf(DirSource(repo))
	if err != nil {
		t.Fatalf("Plan(DirSource): %v", err)
	}
	fromFS, err := planOf(mapSource(fsHarness()))
	if err != nil {
		t.Fatalf("Plan(map): %v", err)
	}
	if len(fromDir) != len(fromFS) {
		t.Fatalf("dir source plans %d files, fs source %d", len(fromDir), len(fromFS))
	}
	for i := range fromDir {
		a, b := fromDir[i], fromFS[i]
		if a.Rel != b.Rel || string(a.Data) != string(b.Data) || a.Mode != b.Mode {
			t.Errorf("file %d: dir %s %v, fs %s %v", i, a.Rel, a.Mode, b.Rel, b.Mode)
		}
	}
	var _ fs.FS = DirSource(repo).FS
}

// skills/<name> may be a link — `npx skills add -g` leaves one — and a
// DirSource follows it to the skill's files, as the path-based reader did by
// resolving it; a link that points nowhere is refused, never skipped.
func TestDirSourceFollowsLinkedSkillDir(t *testing.T) {
	repo := t.TempDir()
	for name, body := range fsHarness() {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	outside := filepath.Join(t.TempDir(), "gamma")
	writeFile(t, filepath.Join(outside, "SKILL.md"), "---\nname: gamma\ndescription: \"linked\"\n---\nbody\n")
	writeFile(t, filepath.Join(outside, "references", "g.md"), "g\n")
	if err := os.Symlink(outside, filepath.Join(repo, "skills", "gamma")); err != nil {
		t.Skip("symlinks unavailable here")
	}
	files, err := planOf(DirSource(repo))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, f := range files {
		if f.Rel == filepath.Join(AgyRoot, "skills", "gamma", "references", "g.md") && string(f.Data) == "g\n" {
			found = true
		}
	}
	if !found {
		t.Errorf("the linked skill's references/g.md is not planned")
	}

	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(repo, "skills", "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSkills(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Errorf("LoadSkills err = %v, want a refusal naming the dangling link", err)
	}
}

// A frontmatter `name:` becomes a path component under ~/.claude and the agy
// mirror (plan.go); the parse boundary refuses anything that is not one
// local segment, naming the file, so `../x` or `a/b` never reaches
// filepath.Join.
func TestSkillAndAgentNamesMustBeOneLocalSegment(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  bool
	}{
		{"ok-name", false},
		{"../evil", true},
		{"a/b", true},
		{"/abs", true},
		{"..", true},
		{`a\b`, true},
		{"a b", true},
		{"a`b", true},
		{"café", true},
		{".hidden", true},
		{"v1.2_x-y", false},
	} {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, "skills", "dir", "SKILL.md"),
			"---\nname: "+tc.name+"\ndescription: \"a skill\"\n---\nbody\n")
		writeFile(t, filepath.Join(repo, "agents", "file.md"),
			"---\nname: "+tc.name+"\ndescription: \"an agent\"\n---\nbody\n")
		_, serr := LoadSkills(DirSource(repo))
		_, aerr := LoadAgents(DirSource(repo))
		if tc.bad {
			if serr == nil || !strings.Contains(serr.Error(), "dir/SKILL.md") {
				t.Errorf("name %q: skills loader err = %v, want a refusal naming dir/SKILL.md", tc.name, serr)
			}
			if aerr == nil || !strings.Contains(aerr.Error(), "file.md") {
				t.Errorf("name %q: agents loader err = %v, want a refusal naming file.md", tc.name, aerr)
			}
			continue
		}
		if serr != nil || aerr != nil {
			t.Errorf("name %q: skills err = %v, agents err = %v, want none", tc.name, serr, aerr)
		}
	}
}

// A symlink inside a skill directory is refused by name, never followed:
// os.ReadFile would deploy the target's bytes under the link's mode, and a
// link's 0777 becomes a world-writable file in ~/.claude.
func TestSkillFilesRefuseSymlinks(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "skills", "s")
	writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), "# ctx\n")
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: s\ndescription: \"x\"\n---\nbody\n")
	outside := filepath.Join(t.TempDir(), "secret")
	writeFile(t, outside, "not for ~/.claude\n")
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Skip("symlinks unavailable here")
	}
	if _, err := planOf(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Plan err = %v, want a symlink refusal", err)
	}
}

// context/, agents/, rules/, commands/ and workflows/ are read with
// os.ReadFile, which follows a link and would deploy the TARGET'S bytes into
// every consumer home — and for context/ into the AGENTS.md both CLIs send to
// the model. The skill walk and adopt's copy already refuse links; these
// readers must too.
func TestContextAgentsAndDocsRefuseSymlinks(t *testing.T) {
	valid := map[string]string{
		"context":   "# extra\n\nbytes that must not be deployed\n",
		"agents":    "---\nname: shadow\ndescription: \"an agent\"\n---\nbody\n",
		"rules":     "---\ndescription: \"a rule\"\n---\nbody\n",
		"commands":  "---\ndescription: \"a command\"\n---\nbody\n",
		"workflows": "---\ndescription: \"a workflow\"\n---\nbody\n",
	}
	for _, tc := range []struct{ dir, file string }{
		{"context", "extra.md"},
		{"context", "AGENTS.md"},
		{"agents", "shadow.md"},
		{"rules", "rule.md"},
		{"commands", "cmd.md"},
		{"workflows", "flow.md"},
	} {
		t.Run(tc.dir+"/"+tc.file, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), "# ctx\n")
			writeFile(t, filepath.Join(repo, "skills", "s", "SKILL.md"), "---\nname: s\ndescription: \"x\"\n---\nbody\n")
			outside := filepath.Join(t.TempDir(), "target.md")
			writeFile(t, outside, valid[tc.dir])
			link := filepath.Join(repo, tc.dir, tc.file)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			os.Remove(link)
			if err := os.Symlink(outside, link); err != nil {
				t.Skip("symlinks unavailable here")
			}
			_, err := planOf(DirSource(repo))
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("Plan err = %v, want a symlink refusal naming %s", err, link)
			}
		})
	}
}

// A symlinked skills/<name>/SKILL.md is refused where it is read: plan.go's
// walk only visits skills LoadSkills returned, so swallowing the read error
// would drop the skill silently instead of refusing the link.
func TestSymlinkedSkillMdIsRefused(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), "# ctx\n")
	dir := filepath.Join(repo, "skills", "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	writeFile(t, outside, "---\nname: s\ndescription: \"x\"\n---\nbody\n")
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skip("symlinks unavailable here")
	}
	if _, err := LoadSkills(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("LoadSkills err = %v, want a symlink refusal", err)
	}
	if _, err := planOf(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("Plan err = %v, want a symlink refusal", err)
	}
}

// A directory under skills/ with no SKILL.md is not a skill and is skipped,
// which the refusal above must not turn into an error.
func TestSkillDirWithoutSkillMdIsSkipped(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), "# ctx\n")
	writeFile(t, filepath.Join(repo, "skills", "real", "SKILL.md"), "---\nname: real\ndescription: \"x\"\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(repo, "skills", "notaskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	skills, err := LoadSkills(DirSource(repo))
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "real" {
		t.Errorf("skills = %+v, want only real", skills)
	}
}

// A managed directory that is itself a symlink is refused too: os.DirFS, like
// os.ReadDir and filepath.Glob before it, follows a directory link, so guarding the leaf files alone let
// the same payload through one path segment up.
func TestManagedDirsRefuseSymlinks(t *testing.T) {
	skillMD := "---\nname: s\ndescription: \"x\"\n---\nbody\n"
	content := map[string]string{
		"context":   "# ctx\n",
		"agents":    "---\nname: shadow\ndescription: \"an agent\"\n---\nbody\n",
		"rules":     "---\ndescription: \"a rule\"\n---\nbody\n",
		"commands":  "---\ndescription: \"a command\"\n---\nbody\n",
		"workflows": "---\ndescription: \"a workflow\"\n---\nbody\n",
	}
	for _, dir := range []string{"context", "agents", "rules", "commands", "workflows", "skills"} {
		t.Run(dir, func(t *testing.T) {
			repo := t.TempDir()
			outside := t.TempDir()
			switch dir {
			case "context":
				writeFile(t, filepath.Join(outside, "AGENTS.md"), content["context"])
				writeFile(t, filepath.Join(repo, "skills", "s", "SKILL.md"), skillMD)
			case "skills":
				writeFile(t, filepath.Join(outside, "s", "SKILL.md"), skillMD)
				writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), content["context"])
			default:
				writeFile(t, filepath.Join(outside, "x.md"), content[dir])
				writeFile(t, filepath.Join(repo, "context", "AGENTS.md"), content["context"])
				writeFile(t, filepath.Join(repo, "skills", "s", "SKILL.md"), skillMD)
			}
			if err := os.Symlink(outside, filepath.Join(repo, dir)); err != nil {
				t.Skip("symlinks unavailable here")
			}
			if _, err := planOf(DirSource(repo)); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("Plan err = %v, want a symlink refusal naming %s", err, filepath.Join(repo, dir))
			}
		})
	}
}
