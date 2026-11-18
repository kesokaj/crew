package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
)

// The guard on a remote's main follows a link of main's index file within the tree: the file it leads
// to, and a folder that file names, are code, so a remote's main that changes either is held, never
// taken by itself as one that changes no code (13.8, 12.8; since 1.05).
func TestTheGuardOnARemotesMainFollowsALinkOfAnIndexFile(t *testing.T) {
	ctx := context.Background()
	tools, p, _ := toolsKit(t)
	git := tools.Board.Git
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("clone", "-q", git.Canonical(p.Name), ".")
	os.MkdirAll(filepath.Join(work, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(work, "cfg"), 0o755)
	os.Symlink("../cfg/p.json", filepath.Join(work, ".agents/plugins.json"))
	os.WriteFile(filepath.Join(work, "cfg/p.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "plugins through a link")
	run("push", "-q", "origin", "main")
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	code, err := pl.codeIn(ctx, &job{proj: p}, []string{"README.md", "cfg/p.json", "tools/p/hooks.json"})
	if err != nil || !slices.Equal(code, []string{"cfg/p.json", "tools/p/hooks.json"}) {
		t.Fatalf("%v %v", code, err)
	}
	// A way through links that goes round never names nothing: every file of the change is code, so a
	// remote's main that repairs it waits for the operator, who can take it.
	os.Remove(filepath.Join(work, ".agents/plugins.json"))
	os.Symlink("plugins.json", filepath.Join(work, ".agents/plugins.json"))
	run("add", "-A")
	run("commit", "-q", "-m", "round")
	run("push", "-q", "origin", "main")
	if code, err := pl.codeIn(ctx, &job{proj: p}, []string{"README.md", ".agents/plugins.json"}); err != nil || len(code) != 2 {
		t.Fatalf("a link that goes round: %v %v", code, err)
	}
	// Any other failure to read main's index files holds the remote's main the same way.
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = ls-tree ] && exit 128; done\nexec "+real+" \"$@\"\n"), 0o755)
	git.Git = fake
	defer func() { git.Git = "" }()
	if code, err := pl.codeIn(ctx, &job{proj: p}, []string{"README.md"}); err != nil || len(code) != 1 {
		t.Fatalf("a git that fails: %v %v", code, err)
	}
}
