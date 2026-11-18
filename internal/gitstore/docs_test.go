package gitstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
)

// What Crew writes into a merge itself (9.9): the changelog on top of main's file, also when the
// branch changed it too, which is then no conflict; and a decision page. A conflict in another file is
// still one.
func TestAMergeCarriesWhatCrewWritesItself(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"CHANGELOG.md": "# Changelog\n\nthe builder's line\n", "b.txt": "b\n"})
	overlay := gitstore.Overlay{
		"CHANGELOG.md": func(onMain []byte) []byte { return append([]byte("## SF-1\n\n"), onMain...) },
		"docs/decisions/0001-cents.md": func(onMain []byte) []byte {
			if onMain != nil {
				t.Errorf("main has no page yet: %q", onMain)
			}
			return []byte("# 0001. Cents\n")
		},
	}
	// main changed its changelog meanwhile, so the two conflict in it.
	pushWork(t, s, "shop", "task/SF-0-main", map[string]string{"CHANGELOG.md": "# Changelog\n\nmain's line\n"})
	c0, err := s.MergeCandidate(ctx, "shop", "task/SF-0-main", "Merge SF-0", me, nil)
	if err != nil || s.AdvanceMain(ctx, "shop", c0.Main, c0.Commit) != nil {
		t.Fatalf("%+v %v", c0, err)
	}
	c, err := s.MergeCandidate(ctx, "shop", "task/SF-1-x", "Merge SF-1", me, overlay)
	if err != nil || c.Commit == "" || len(c.Conflicts) > 0 {
		t.Fatalf("%+v %v", c, err)
	}
	canon := s.Canonical("shop")
	if got := git(t, canon, "show", c.Commit+":CHANGELOG.md"); got != "## SF-1\n\n# Changelog\n\nmain's line" {
		t.Fatalf("the changelog: %q", got)
	}
	if got := git(t, canon, "show", c.Commit+":docs/decisions/0001-cents.md"); got != "# 0001. Cents" {
		t.Fatalf("the page: %q", got)
	}
	if got := git(t, canon, "show", c.Commit+":b.txt"); got != "b" {
		t.Fatalf("the branch's work: %q", got)
	}
	// A conflict outside the overlay is one.
	pushWork(t, s, "shop", "task/SF-2-y", map[string]string{"a.txt": "two\n"})
	pushWork(t, s, "shop", "task/SF-3-z", map[string]string{"a.txt": "three\n"})
	c2, _ := s.MergeCandidate(ctx, "shop", "task/SF-2-y", "Merge SF-2", me, overlay)
	if err := s.AdvanceMain(ctx, "shop", c2.Main, c2.Commit); err != nil {
		t.Fatal(err)
	}
	c3, err := s.MergeCandidate(ctx, "shop", "task/SF-3-z", "Merge SF-3", me, overlay)
	if err != nil || !slices.Equal(c3.Conflicts, []string{"a.txt"}) {
		t.Fatalf("%+v %v", c3, err)
	}
	pushWork(t, s, "shop", "task/SF-4-w", map[string]string{"d.txt": "d\n"})
	if _, err := s.MergeCandidate(ctx, "shop", "task/SF-4-w", "x", me, gitstore.Overlay{"../x": func([]byte) []byte { return nil }}); err == nil {
		t.Fatal("an overlay path outside the repository was written")
	}
}

// The operator's edit of a page is a commit of its own on main, by the operator; an edit from a main
// on which the page has changed since is refused, and one where only other files changed is not.
func TestAnEditOfAPageIsACommitOnMain(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	base := s.Main(ctx, "shop")
	commit, err := s.CommitPage(ctx, "shop", base, "docs/setup.md", []byte("# Setup\n"), "Edit docs/setup.md", me)
	if err != nil || s.Main(ctx, "shop") != commit {
		t.Fatalf("%s %v", commit, err)
	}
	canon := s.Canonical("shop")
	if got := git(t, canon, "log", "-1", "--format=%P %an", commit); got != base+" Op Erator" {
		t.Fatalf("the commit: %q", got)
	}
	if git(t, s.Quarantine.Path("shop"), "rev-parse", "refs/heads/main") != commit {
		t.Fatal("the quarantine repository has another main")
	}
	// An edit from the older main of the same page is refused; of another page it is made.
	if _, err := s.CommitPage(ctx, "shop", base, "docs/setup.md", []byte("# Mine\n"), "x", me); !errors.Is(err, gitstore.ErrPageChanged) {
		t.Fatalf("an edit over a newer page: %v", err)
	}
	if _, err := s.CommitPage(ctx, "shop", base, "README.md", []byte("# shop\n\nMore.\n"), "x", me); err != nil {
		t.Fatalf("an edit of a page that did not change: %v", err)
	}
	if _, err := s.CommitPage(ctx, "shop", s.Main(ctx, "shop"), ".git/config", []byte("x"), "x", me); err == nil {
		t.Fatal("an edit of git's own file")
	}
	files, err := s.Files(ctx, "shop", s.Main(ctx, "shop"), func(p string) bool { return strings.HasSuffix(p, ".md") }, 10)
	if err != nil || len(files) != 2 || files[0].Path != "README.md" || files[1].Path != "docs/setup.md" {
		t.Fatalf("files %+v %v", files, err)
	}
	if b, err := s.Blob(ctx, "shop", files[1].Blob, 100); err != nil || string(b) != "# Setup\n" {
		t.Fatalf("blob %q %v", b, err)
	}
	if _, err := s.Blob(ctx, "shop", files[1].Blob, 3); err == nil {
		t.Fatal("a blob past its limit was read")
	}
}

// Who last changed a file is read from main's own line: the merge Crew made, whose message Crew
// wrote, never the branch commit whose message an agent wrote; and a path is read as it is, not as a
// pattern (security review of M12).
func TestTheLastChangeIsMainsOwnCommit(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Quarantine.Path("shop"), ".")
	git(t, work, "checkout", "-q", "-b", "task/SF-1-x")
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(work, "docs", "a.md"), []byte("# A\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "Change docs\n\nTicket: SF-9")
	git(t, work, "push", "-q", "origin", "task/SF-1-x")
	c, err := s.MergeCandidate(ctx, "shop", "task/SF-1-x", "SF-1: x\n\nTicket: SF-1\n", me, nil)
	if err != nil || s.AdvanceMain(ctx, "shop", c.Main, c.Commit) != nil {
		t.Fatalf("%+v %v", c, err)
	}
	id, _, msg, err := s.LastChange(ctx, "shop", s.Main(ctx, "shop"), "docs/a.md")
	if err != nil || id != c.Commit || !strings.Contains(msg, "Ticket: SF-1") || strings.Contains(msg, "SF-9") {
		t.Fatalf("%s %q %v", id, msg, err)
	}
	if _, _, _, err := s.LastChange(ctx, "shop", s.Main(ctx, "shop"), "docs/*"); err == nil {
		t.Fatal("a pattern was read as one")
	}
}
