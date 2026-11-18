package gitendpoint

import (
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestMain lets the test program be the hook's program too, as the server is: started with
// HookCommand, it tests a push's paths.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HookCommand {
		os.Exit(PreReceive(os.Stdin, os.Stderr))
	}
	os.Exit(m.Run())
}

// A push is tested against the paths its ticket may change (13.8, 7.11): a file that instructs agents
// only when may_change names it, also one an index file of the pushed tree names, and a file that
// does not only under may_change; a parallel subtask's push only inside what it said it touches. The
// change is measured from where the branch leaves its base, so a branch that merged main in is not
// charged with main's files, and a file changed and changed back is no change. A part's work in
// progress keeps a stray file and refuses an instruction. A request with rules and no program to
// test them is refused.
func TestAPushChangesOnlyWhatItsTicketMayChange(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	rules := map[string]PathRule{}
	e, srv := endpoint(t, func(e *Endpoint) {
		e.Self = self
		auth := e.Auth
		e.Auth = func(r *http.Request, repo string) (Access, error) {
			a, err := auth(r, repo)
			if err == nil && a.Who == "run-a" {
				mu.Lock()
				a.Paths = maps.Clone(rules)
				mu.Unlock()
			}
			return a, err
		}
	})
	set := func(r PathRule) { mu.Lock(); rules["refs/heads/task/1-a"] = r; mu.Unlock() }
	work := t.TempDir()
	if out, err := git(t, work, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "c"); err != nil {
		t.Fatal(out)
	}
	c := filepath.Join(work, "c")
	commit := func(files map[string]string) {
		t.Helper()
		for f, body := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(c, f)), 0o755)
			if body == "" {
				os.Remove(filepath.Join(c, f))
				continue
			}
			os.WriteFile(filepath.Join(c, f), []byte(body), 0o644)
		}
		git(t, c, "", "add", "-A")
		if out, err := git(t, c, "", "commit", "-qm", "work"); err != nil {
			t.Fatal(out)
		}
	}
	push := func() (string, error) {
		return git(t, c, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a")
	}
	reset := func() {
		t.Helper()
		if out, err := git(t, c, "token-a", "fetch", "-q", "origin", "task/1-a"); err != nil {
			t.Fatal(out)
		}
		git(t, c, "", "reset", "-q", "--hard", "FETCH_HEAD")
	}
	ref := "refs/heads/task/1-a"
	set(PathRule{Base: "refs/heads/main", Allow: []string{Everything}})

	commit(map[string]string{"src/a.go": "package a\n"})
	if out, err := push(); err != nil {
		t.Fatalf("an ordinary file under **: %s", out)
	}
	for _, f := range []string{"AGENTS.md", "docs/AGENTS.md", ".agents/hooks.json", ".agents/skills/x/SKILL.md", ".agents/rules/r.md",
		".github/workflows/ci.yml", ".agents"} {
		commit(map[string]string{f: "x\n"})
		out, err := push()
		if err == nil || !strings.Contains(out, f+" instructs agents") {
			t.Fatalf("%s was pushed under ** alone: %v %s", f, err, out)
		}
		reset()
	}
	// A folder an index file of the pushed tree names instructs too.
	commit(map[string]string{".agents/rules.json": "", "tools/rules/r.md": "Obey.\n"})
	reset()
	commit(map[string]string{"tools/rules/r.md": "Obey.\n"})
	if out, err := push(); err != nil {
		t.Fatalf("a folder no index names yet: %s", out)
	}
	set(PathRule{Base: "refs/heads/main", Allow: []string{Everything, "AGENTS.md", "tools/rules/r.md"}})
	commit(map[string]string{".agents/skills.json": `{"entries": [{"path": "tools/skills"}]}`, "tools/skills/s.md": "x\n"})
	if out, err := push(); err == nil || !strings.Contains(out, "tools/skills/s.md instructs agents") || !strings.Contains(out, ".agents/skills.json instructs agents") {
		t.Fatalf("a folder the pushed index names: %v %s", err, out)
	}
	reset()
	// Named in may_change, it goes; changed and changed back, nothing is charged.
	commit(map[string]string{"AGENTS.md": "Be brief.\n", "sub/AGENTS.md": "Be brief here too.\n"})
	if out, err := push(); err != nil {
		t.Fatalf("AGENTS.md named in may_change: %s", out)
	}
	set(PathRule{Base: "refs/heads/main", Allow: []string{Everything}})
	commit(map[string]string{".agents/mcp_config.json": "{}\n"})
	commit(map[string]string{".agents/mcp_config.json": ""})
	if out, err := push(); err == nil || !strings.Contains(out, "AGENTS.md instructs agents") || strings.Contains(out, "mcp_config.json") {
		t.Fatalf("the branch's change as a whole: %v %s", err, out)
	}
	reset()
	// A may_change with no ** keeps ordinary files to its list.
	set(PathRule{Base: "refs/heads/main", Allow: []string{"src/", "AGENTS.md"}})
	commit(map[string]string{"web/x.ts": "x\n"})
	if out, err := push(); err == nil || !strings.Contains(out, "web/x.ts is outside the ticket's may_change") {
		t.Fatalf("outside a narrow may_change: %v %s", err, out)
	}
	reset()
	// A subtask's push stays inside its touches, measured from the ticket's branch it was cut from.
	if err := e.run(context.Background(), e.Path("fixture"), nil, "update-ref", "refs/heads/ticket", e.Ref(context.Background(), "fixture", ref)); err != nil {
		t.Fatal(err)
	}
	set(PathRule{Base: "refs/heads/ticket", Allow: []string{Everything, "AGENTS.md"}, Touches: []string{"src/"}})
	commit(map[string]string{"src/b.go": "package a\n"})
	if out, err := push(); err != nil {
		t.Fatalf("inside its touches: %s", out)
	}
	commit(map[string]string{"web/y.ts": "y\n"})
	if out, err := push(); err == nil || !strings.Contains(out, "web/y.ts is outside the files this subtask said it touches") {
		t.Fatalf("outside its touches: %v %s", err, out)
	}
	reset()
	// A ref with no rule, the run's work in progress, is tested for its name only.
	commit(map[string]string{".agents/hooks.json": "{}\n"})
	if out, err := git(t, c, "token-a", "push", "-q", "origin", "HEAD:refs/crew/wip/run-a"); err != nil {
		t.Fatalf("the work in progress: %s", out)
	}
	reset()
	// A part's work in progress has its branch's rule without the part's files: a stray file is kept,
	// a file that instructs agents is not (review of M9, round 2).
	mu.Lock()
	rules["refs/crew/wip/run-a"] = PathRule{Base: "refs/heads/ticket", Allow: []string{Everything, "AGENTS.md"}}
	mu.Unlock()
	commit(map[string]string{"web/z.ts": "z\n"})
	if out, err := git(t, c, "token-a", "push", "-q", "origin", "+HEAD:refs/crew/wip/run-a"); err != nil {
		t.Fatalf("a stray file in the work in progress: %s", out)
	}
	commit(map[string]string{".agents/hooks.json": "{}\n"})
	if out, err := git(t, c, "token-a", "push", "-q", "origin", "+HEAD:refs/crew/wip/run-a"); err == nil || !strings.Contains(out, ".agents/hooks.json instructs agents") {
		t.Fatalf("an instruction in the work in progress: %v %s", err, out)
	}
}

// A request with path rules and no program to test them is refused.
func TestAPushWithRulesAndNoProgramIsRefused(t *testing.T) {
	_, srv := endpoint(t, func(e *Endpoint) {
		auth := e.Auth
		e.Auth = func(r *http.Request, repo string) (Access, error) {
			a, err := auth(r, repo)
			a.Paths = map[string]PathRule{"refs/heads/task/1-a": {Base: "refs/heads/main", Allow: []string{Everything}}}
			return a, err
		}
	})
	work := t.TempDir()
	if out, err := git(t, work, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "c"); err != nil {
		t.Fatal(out)
	}
	c := filepath.Join(work, "c")
	os.WriteFile(filepath.Join(c, "a.go"), []byte("package a\n"), 0o644)
	git(t, c, "", "add", "-A")
	git(t, c, "", "commit", "-qm", "work")
	if out, err := git(t, c, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a"); err == nil {
		t.Fatalf("pushed with no program to test it: %s", out)
	}
}
