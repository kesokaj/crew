package board_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

// An index file of main that is a link is followed within the tree by each guard that reads what it
// names as code or as instructions: the merge's check, a pull request's push and a branch that moved on
// GitHub. The file it leads to, and a folder that file names, are code; read as the link's own text,
// which names nothing, both would merge or come in by themselves (13.8; since 1.05).
func TestAnIndexFileOfMainThatIsALinkIsFollowedByTheCodeGuards(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	canon := e.git.Canonical("shop")
	seed := t.TempDir()
	git(t, seed, "clone", "-q", canon, ".")
	os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(seed, "cfg"), 0o755)
	os.Symlink("../cfg/p.json", filepath.Join(seed, ".agents/plugins.json"))
	os.WriteFile(filepath.Join(seed, "cfg/p.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "plugins through a link")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, canon, "push", "-q", e.git.Quarantine.Path("shop"), "main")
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	// Two heads, each in the canonical repository too: one that changes only README.md, as a head on
	// GitHub, and Crew's, which also changes the file the link leads to and a file in a folder it names.
	push := func(script func(dir string)) string {
		e.pushTree(p, *tk.Branch, func(dir string) {
			os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644)
			script(dir)
		})
		head, _ := e.b.Head(e.ctx, p, tk)
		if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
			t.Fatalf("follows: %v %v", ok, err)
		}
		return head
	}
	fetched := push(func(string) {})
	head := push(func(dir string) {
		os.WriteFile(filepath.Join(dir, "cfg/p.json"), []byte(`{"entries": [{"path": "tools"}, {"path": "src"}]}`), 0o644)
		os.MkdirAll(filepath.Join(dir, "tools/p"), 0o755)
		os.WriteFile(filepath.Join(dir, "tools/p/hooks.json"), []byte("{}"), 0o644)
	})
	for name, guard := range map[string]func() ([]string, error){
		"the merge's check": func() ([]string, error) { return e.b.CodeChanges(e.ctx, p, tk) },
		"a pull request's push": func() ([]string, error) {
			return board.CodeChangesAtForTest(e.ctx, e.git, p, head)
		},
		"a branch that moved on GitHub": func() ([]string, error) {
			return e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched)
		},
	} {
		t.Run(name, func(t *testing.T) {
			files, err := guard()
			slices.Sort(files)
			if err != nil || !slices.Equal(files, []string{"cfg/p.json", "tools/p/hooks.json"}) {
				t.Fatalf("%v %v", files, err)
			}
		})
	}
}

// A branch that moved on GitHub is judged with the index files of main as it is now too: a folder an
// index file names that came to main after the branch left it instructs agents, so the branch does not
// come in by itself (13.8, since 1.05).
func TestABranchThatMovedOnGitHubIsJudgedByMainAsItIsNow(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	push := func(script func(dir string)) string {
		e.pushTree(p, *tk.Branch, script)
		head, _ := e.b.Head(e.ctx, p, tk)
		if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
			t.Fatalf("follows: %v %v", ok, err)
		}
		return head
	}
	fetched := push(func(dir string) {
		os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
		os.WriteFile(filepath.Join(dir, "notes/a.md"), []byte("do this\n"), 0o644)
	})
	push(func(dir string) { os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644) })
	// main gets an index of rules that names notes, after the branch left it.
	seed := t.TempDir()
	git(t, seed, "clone", "-q", e.git.Canonical("shop"), ".")
	os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
	os.WriteFile(filepath.Join(seed, ".agents/rules.json"), []byte(`{"entries": [{"path": "notes"}]}`), 0o644)
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "rules")
	git(t, seed, "push", "-q", "origin", "main")
	if files, err := e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched); err != nil || !slices.Equal(files, []string{"notes/a.md"}) {
		t.Fatalf("%v %v", files, err)
	}
	// A git that cannot say where main is makes it an error, never a main with no index files.
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = 'refs/heads/main^{commit}' ] && exit 128; done\nexec "+real+" \"$@\"\n"), 0o755)
	e.git.Git = fake
	defer func() { e.git.Git = "" }()
	if files, err := e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched); err == nil {
		t.Fatalf("a git that cannot find main: %v", files)
	}
}
