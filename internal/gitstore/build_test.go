package gitstore_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// FirstParents is main's own line, oldest first, without what a merge brought in; LsTree is every
// file of a commit with its hash, as a release lists them (16.1, 16.2 step 3).
func TestFirstParentsAndLsTree(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateLocal(ctx, "p", map[string]string{"a.txt": "a"}, "one", me); err != nil {
		t.Fatal(err)
	}
	c0 := s.Main(ctx, "p")
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("p"), ".")
	git(t, work, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(work, "b.txt"), []byte("b"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "side")
	side := strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))
	git(t, work, "checkout", "-q", "main")
	git(t, work, "merge", "-q", "--no-ff", "-m", "merge", "side")
	merge := strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))
	git(t, work, "push", "-q", "origin", "main")

	line, err := s.FirstParents(ctx, "p", c0, merge)
	if err != nil || !slices.Equal(line, []string{merge}) {
		t.Fatalf("since c0: %v %v", line, err)
	}
	if line, err := s.FirstParents(ctx, "p", "", merge); err != nil || !slices.Equal(line, []string{c0, merge}) || slices.Contains(line, side) {
		t.Fatalf("the whole line: %v %v", line, err)
	}
	if line, _ := s.FirstParents(ctx, "p", merge, merge); len(line) != 0 {
		t.Fatalf("an empty range: %v", line)
	}
	if _, err := s.FirstParents(ctx, "p", "", "main; rm -rf /"); err == nil {
		t.Fatal("a name that is no commit")
	}
	listed, err := s.LsTree(ctx, "p", merge)
	if err != nil || !strings.Contains(listed, "\ta.txt") || !strings.Contains(listed, "\tb.txt") || !strings.HasPrefix(listed, "100644 blob ") {
		t.Fatalf("ls-tree: %q %v", listed, err)
	}
	if _, err := s.LsTree(ctx, "p", "HEAD"); err == nil {
		t.Fatal("a name that is no commit")
	}
}
