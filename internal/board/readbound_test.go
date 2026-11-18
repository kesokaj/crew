package board_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

// An index file of main past the bound of a read is an error for each guard that reads what it names
// as code or as instructions: the merge's check, a pull request's push and a branch that moved on
// GitHub. Taken for an index file that names nothing, a file in the folder it names would merge or come
// in by itself (13.8; since 1.04).
func TestAnIndexFilePastTheBoundIsAnErrorForTheCodeGuards(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	// main's plugins.json names tools for agy's plugins, and is longer than the bound below.
	canon := e.git.Canonical("shop")
	seed := t.TempDir()
	git(t, seed, "clone", "-q", canon, ".")
	os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
	os.WriteFile(filepath.Join(seed, ".agents/plugins.json"), []byte(`{"entries": [{"path": "tools"}]`+strings.Repeat(" ", 32<<10)+"}"), 0o644)
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "plugins")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, canon, "push", "-q", e.git.Quarantine.Path("shop"), "main")
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	e.pushTree(p, *tk.Branch, func(dir string) {
		os.MkdirAll(filepath.Join(dir, "tools/p"), 0o755)
		os.WriteFile(filepath.Join(dir, "tools/p/hooks.json"), []byte("{}"), 0o644)
	})
	head, _ := e.b.Head(e.ctx, p, tk)
	// The head is in the canonical repository too, where a head fetched from GitHub is compared.
	if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
		t.Fatalf("follows: %v %v", ok, err)
	}
	guards := map[string]func() ([]string, error){
		"the merge's check": func() ([]string, error) { return e.b.CodeChanges(e.ctx, p, tk) },
		"a pull request's push": func() ([]string, error) {
			return board.CodeChangesAtForTest(e.ctx, e.git, p, head)
		},
		"a branch that moved on GitHub": func() ([]string, error) {
			return e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, head)
		},
	}
	for name, guard := range guards {
		t.Run(name, func(t *testing.T) {
			e.git.MaxOutput = 0
			if _, err := guard(); err != nil {
				t.Fatalf("within the bound: %v", err)
			}
			e.git.MaxOutput = 16 << 10
			defer func() { e.git.MaxOutput = 0 }()
			if files, err := guard(); !errors.Is(err, gitstore.ErrOutputTooLarge) {
				t.Fatalf("past the bound: %v %v", files, err)
			}
		})
	}
	// Within the bound, the merge's check sees the file plugins.json names as code.
	if code, err := e.b.CodeChanges(e.ctx, p, tk); err != nil || len(code) != 1 {
		t.Fatalf("code within the bound: %v %v", code, err)
	}
}
