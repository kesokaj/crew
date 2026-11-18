package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// A file of main past the bound of a read is never read as one that names nothing by the pipeline's
// readers of main's files: the guard on a remote's main counts every file of the change as code, so the
// operator decides (13.8; since 1.05, an error before), and a project's own stage, which would wait for
// a definition main does have, has an error (since 1.04).
func TestAFileOfMainPastTheBoundIsAnErrorInThePipeline(t *testing.T) {
	ctx := context.Background()
	tools, p, _ := toolsKit(t)
	git := tools.Board.Git
	id := gitstore.Identity{Name: "Op", Email: "op@x"}
	for path, body := range map[string]string{
		".agents/plugins.json": `{"entries": [{"path": "tools"}]` + strings.Repeat(" ", 32<<10) + "}",
		"agents/judge.md":      "# The judge\n\n" + strings.Repeat("Judge it. ", 4<<10),
	} {
		if _, err := git.CommitPage(ctx, p.Name, git.Main(ctx, p.Name), path, []byte(body), "add "+path, id); err != nil {
			t.Fatal(err)
		}
	}
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	j := &job{proj: p}
	if code, err := pl.codeIn(ctx, j, []string{"tools/p/hooks.json"}); err != nil || len(code) != 1 {
		t.Fatalf("within the bound: %v %v", code, err)
	}
	git.MaxOutput = 16 << 10
	if code, err := pl.codeIn(ctx, j, []string{"README.md", "tools/p/hooks.json"}); err != nil || len(code) != 2 {
		t.Fatalf("a remote's main past the bound: %v %v", code, err)
	}
	tk, err := tools.Board.Ticket(ctx, p, "SF-1")
	if err != nil {
		t.Fatal(err)
	}
	st := settings.Stage{ID: "judged", Agent: "agents/judge.md"}
	if err := pl.ownStage(ctx, j, tk, nil, git.Main(ctx, p.Name), st); !errors.Is(err, gitstore.ErrOutputTooLarge) {
		t.Fatalf("a stage's agent past the bound: %v", err)
	}
	if tk, _ = tools.Board.Ticket(ctx, p, "SF-1"); tk.Waiting != nil {
		t.Fatalf("the ticket waits: %s", *tk.Waiting)
	}
}

// A remote's main is held for the operator, with every file it changes counted as code, when main's
// index file is past the bound of a read, both when it follows Crew's main and when it went another
// way: it is not taken or merged by itself, and the operator can take one that repairs the index file
// (13.8, 12.8; since 1.05, where 1.04 made it an error each pass, which no remote's main could end).
func TestARemotesMainIsHeldPastTheBoundOfAnIndexFile(t *testing.T) {
	ctx := context.Background()
	tools, p, _ := toolsKit(t)
	git := tools.Board.Git
	first := git.Main(ctx, p.Name)
	local, err := git.CommitPage(ctx, p.Name, first, ".agents/plugins.json",
		[]byte(`{"entries": [{"path": "tools"}]`+strings.Repeat(" ", 32<<10)+"}"), "plugins", gitstore.Identity{Name: "Op", Email: "op@x"})
	if err != nil {
		t.Fatal(err)
	}
	// Two remote mains, in the canonical repository as a fetch leaves them: one on Crew's main, one
	// on the commit before it.
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remoteOn := func(base string) string {
		run("checkout", "-q", "--detach", base)
		os.MkdirAll(filepath.Join(work, "tools/p"), 0o755)
		os.WriteFile(filepath.Join(work, "tools/p/hooks.json"), []byte("{}\n"), 0o644)
		run("add", "-A")
		run("commit", "-q", "-m", "a hook from outside")
		c := run("rev-parse", "HEAD")
		run("push", "-q", git.Canonical(p.Name), c+":refs/crew/test/"+c)
		return c
	}
	run("clone", "-q", git.Canonical(p.Name), ".")
	ahead, diverged := remoteOn(local), remoteOn(first)
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	j := &job{proj: p}
	git.MaxOutput = 16 << 10
	if err := pl.takeRemote(ctx, j, local, ahead); err != nil {
		t.Fatalf("a remote's main ahead: %v", err)
	}
	if err := pl.mergeRemote(ctx, j, local, diverged, false); err != nil {
		t.Fatalf("a remote's main that went another way: %v", err)
	}
	var rows int
	if err := tools.Pool.QueryRow(ctx, `SELECT count(*) FROM remote_mains WHERE state = 'held' AND 'tools/p/hooks.json' = ANY(files)`).Scan(&rows); err != nil || rows != 2 || git.Main(ctx, p.Name) != local {
		t.Fatalf("held: %d rows, main %s, %v", rows, git.Main(ctx, p.Name), err)
	}
	if _, err := tools.Pool.Exec(ctx, `DELETE FROM remote_mains`); err != nil {
		t.Fatal(err)
	}
	// Within the bound, the hook it brings is code that main's index file names: the main is held.
	git.MaxOutput = 0
	if err := pl.takeRemote(ctx, j, local, ahead); err != nil {
		t.Fatal(err)
	}
	if err := tools.Pool.QueryRow(ctx, `SELECT count(*) FROM remote_mains WHERE state = 'held' AND files = '{tools/p/hooks.json}'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("within the bound: %d held, %v", rows, err)
	}
}
