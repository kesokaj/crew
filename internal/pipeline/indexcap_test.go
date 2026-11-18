package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

// An index file of main past the bounds Crew reads one within, more than 1,024 entries, an entry longer
// than 4,096 bytes or more than 1 MiB, holds a remote's main for the operator with every file it changes
// counted as code, as any index file of main that cannot be read does, and so does one that is not JSON
// Crew reads exactly (13.8, 12.8; since 1.07). At the bounds it names its folders as before.
func TestTheGuardOnARemotesMainHoldsAnIndexFilePastItsBounds(t *testing.T) {
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
	entries := func(n int) string {
		return `{"entries": [` + strings.TrimSuffix(strings.Repeat(`{"path": "tools"},`, n), ",") + `]}`
	}
	long := func(n int) string {
		return `{"entries": [{"path": "` + strings.Repeat("./", (n-5)/2) + "tools" + strings.Repeat("/", (n-5)%2) + `"}]}`
	}
	sized := func(n int) string {
		head := `{"entries": [{"path": "tools"}]`
		return head + strings.Repeat(" ", n-len(head)-1) + "}"
	}
	pl := &Pipeline{Pool: tools.Pool, Board: tools.Board}
	files := []string{"README.md", "tools/p/hooks.json"}
	for _, c := range []struct {
		name, file, body string
		held             bool
	}{
		{"1024 entries", ".agents/plugins.json", entries(1024), false},
		{"1025 entries", ".agents/plugins.json", entries(1025), true},
		{"an entry of 4096 bytes", ".agents/plugins.json", long(4096), false},
		{"an entry of 4097 bytes", ".agents/plugins.json", long(4097), true},
		{"a file of 1 MiB", ".agents/plugins.json", sized(cli.MaxIndexBytes), false},
		{"a file of 1 MiB and a byte", ".agents/plugins.json", sized(cli.MaxIndexBytes + 1), true},
		{"a mistyped entry", ".agents/plugins.json", `{"entries": [{"path": "tools"}, {"path": 7}]}`, true},
		{"a key in another case", ".agents/plugins.json", `{"entries": [{"path": "tools"}], "Entries": []}`, true},
		{"a key Crew does not read in an entry", ".agents/plugins.json", `{"entries": [{"path": "tools", "note": "x"}]}`, true},
		{"a top-level key plugins", ".agents/plugins.json", `{"entries": [{"path": "tools"}], "plugins": []}`, true},
		{"a key Crew does not read in rules.json entry", ".agents/rules.json", `{"entries": [{"path": "tools", "note": "x"}]}`, true},
		{"a top-level key plugins in rules.json", ".agents/rules.json", `{"entries": [{"path": "tools"}], "plugins": []}`, true},
	} {
		os.Remove(filepath.Join(work, ".agents/plugins.json"))
		os.Remove(filepath.Join(work, ".agents/rules.json"))
		os.WriteFile(filepath.Join(work, c.file), []byte(c.body), 0o644)
		run("add", "-A")
		run("commit", "-q", "-m", c.name)
		run("push", "-q", "origin", "main")
		want := []string{"tools/p/hooks.json"}
		if c.held {
			want = files
		}
		if code, err := pl.codeIn(ctx, &job{proj: p}, files); err != nil || !slices.Equal(code, want) {
			t.Errorf("%s: %v %v", c.name, code, err)
		}
	}
}
