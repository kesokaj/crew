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

// The guard on a remote's main follows a link of main that stands for a folder of code: a file where it
// leads is code, so a remote's main that changes it is held. A link out of the workspace holds it with
// every file it changes counted as code, so a remote's main that repairs it can be taken (13.8, 12.8;
// since 1.06).
func TestTheGuardOnARemotesMainFollowsAFolderLinkOfMain(t *testing.T) {
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
	os.MkdirAll(filepath.Join(work, "lib/plugins"), 0o755)
	os.Symlink("../lib/plugins", filepath.Join(work, ".agents/plugins"))
	os.WriteFile(filepath.Join(work, "lib/plugins/README.md"), []byte("plugins\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "plugins through a link")
	run("push", "-q", "origin", "main")
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	code, err := pl.codeIn(ctx, &job{proj: p}, []string{"README.md", "lib/plugins/p/hooks.json"})
	if err != nil || !slices.Equal(code, []string{"lib/plugins/p/hooks.json"}) {
		t.Fatalf("%v %v", code, err)
	}
	os.Remove(filepath.Join(work, ".agents/plugins"))
	os.Symlink("/opt/plugins", filepath.Join(work, ".agents/plugins"))
	run("add", "-A")
	run("commit", "-q", "-m", "out")
	run("push", "-q", "origin", "main")
	if code, err := pl.codeIn(ctx, &job{proj: p}, []string{"README.md", "src/a.go"}); err != nil || len(code) != 2 {
		t.Fatalf("a link out: %v %v", code, err)
	}
}
