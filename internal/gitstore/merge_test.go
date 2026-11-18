package gitstore_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/gitstore"
)

func shop(t *testing.T) *gitstore.Store {
	t.Helper()
	s := newStore(t)
	if err := s.CreateLocal(context.Background(), "shop", map[string]string{"README.md": "# shop\n", "a.txt": "one\n"}, "SF: first", me); err != nil {
		t.Fatal(err)
	}
	return s
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	r := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// A ticket's branch merges with main into a candidate whose first parent is main; main moves to it
// only from where it was; the archive of the candidate holds both sides.
func TestAMergeCandidateIsMainWithTheBranch(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"b.txt": "two\n"})
	head, err := s.Head(ctx, "shop", "task/SF-1-x")
	if err != nil || !gitstore.ValidCommit(head) {
		t.Fatalf("head %q %v", head, err)
	}
	main := s.Main(ctx, "shop")
	c, err := s.MergeCandidate(ctx, "shop", "task/SF-1-x", "Merge SF-1", me, nil)
	if err != nil || c.Commit == "" || c.Main != main || c.Head != head || len(c.Conflicts) > 0 {
		t.Fatalf("%+v %v", c, err)
	}
	parents := git(t, s.Canonical("shop"), "log", "-1", "--format=%P %an %s", c.Commit)
	if parents != main+" "+head+" Op Erator Merge SF-1" {
		t.Fatalf("the candidate: %q", parents)
	}
	var buf bytes.Buffer
	if err := s.Archive(ctx, "shop", c.Commit, &buf); err != nil {
		t.Fatal(err)
	}
	if n := tarNames(t, buf.Bytes()); !slices.Contains(n, "b.txt") || !slices.Contains(n, "a.txt") {
		t.Fatalf("the archive holds %v", n)
	}
	// The branch head is archived from the quarantine repository, where the run pushed it.
	buf.Reset()
	if err := s.Archive(ctx, "shop", head, &buf); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceMain(ctx, "shop", head, c.Commit); !errors.Is(err, gitstore.ErrMainMoved) {
		t.Fatalf("main moved from a commit it was not at: %v", err)
	}
	if err := s.AdvanceMain(ctx, "shop", main, c.Commit); err != nil {
		t.Fatal(err)
	}
	if s.Main(ctx, "shop") != c.Commit || git(t, s.Quarantine.Path("shop"), "rev-parse", "refs/heads/main") != c.Commit {
		t.Fatal("main did not move in both repositories")
	}
	// Done twice, as a move that is tried again after it was written, is no error.
	if err := s.AdvanceMain(ctx, "shop", main, c.Commit); err != nil {
		t.Fatal(err)
	}
	commits, files, err := s.Changes(ctx, "shop", main, head)
	if err != nil || len(commits) != 1 || commits[0] != head || !slices.Equal(files, []string{"b.txt"}) {
		t.Fatalf("the changes: %v %v %v", commits, files, err)
	}
	// The branch is in main now: nothing more to merge.
	if c, err := s.MergeCandidate(ctx, "shop", "task/SF-1-x", "again", me, nil); err != nil || !c.UpToDate {
		t.Fatalf("%+v %v", c, err)
	}
}

// Two tickets that change the same line: the second conflicts, and says which file.
func TestAConflictNamesItsFiles(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"a.txt": "from one\n"})
	pushWork(t, s, "shop", "task/SF-2-y", map[string]string{"a.txt": "from two\n", "c.txt": "c\n"})
	c, err := s.MergeCandidate(ctx, "shop", "task/SF-1-x", "Merge SF-1", me, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceMain(ctx, "shop", c.Main, c.Commit); err != nil {
		t.Fatal(err)
	}
	c, err = s.MergeCandidate(ctx, "shop", "task/SF-2-y", "Merge SF-2", me, nil)
	if err != nil || c.Commit != "" || !slices.Equal(c.Conflicts, []string{"a.txt"}) {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := s.MergeCandidate(ctx, "shop", "task/SF-3-z", "x", me, nil); !errors.Is(err, gitstore.ErrNoHead) {
		t.Fatalf("a branch never pushed: %v", err)
	}
	if err := s.Archive(ctx, "shop", "--output=/tmp/x", io.Discard); err == nil {
		t.Fatal("an archive of an option")
	}
}

// The archive a check sandbox gets is the commit's whole tree: the commit's own .gitattributes cannot
// leave a file out (export-ignore) or change one (export-subst).
func TestTheArchiveIgnoresTheCommitsAttributes(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{".gitattributes": "secret_test.go export-ignore\nv.txt export-subst\n",
		"secret_test.go": "package x\n", "v.txt": "$Format:%H$\n"})
	head, _ := s.Head(ctx, "shop", "task/SF-1-x")
	var buf bytes.Buffer
	if err := s.Archive(ctx, "shop", head, &buf); err != nil {
		t.Fatal(err)
	}
	if n := tarNames(t, buf.Bytes()); !slices.Contains(n, "secret_test.go") {
		t.Fatalf("the archive left out a file the branch marked: %v", n)
	}
	if !bytes.Contains(buf.Bytes(), []byte("$Format:%H$")) {
		t.Fatal("the archive changed a file the branch marked")
	}
}

// A parallel subtask's branch joins its ticket's branch (7.11): a merge of both, or the subtask's head
// itself when the ticket's branch is behind it or not pushed yet; two subtasks that change one file
// conflict; a subtask joined already has nothing to join; the ticket's branch only goes forward.
func TestASubtasksBranchJoinsItsTicketsBranch(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"x.txt": "x\n"})
	pushWork(t, s, "shop", "sub/SF-1.2", map[string]string{"b.txt": "two\n"})
	pushWork(t, s, "shop", "sub/SF-1.3", map[string]string{"a.txt": "three\n"})
	pushWork(t, s, "shop", "sub/SF-1.4", map[string]string{"a.txt": "four\n"})
	base, _ := s.Head(ctx, "shop", "task/SF-1-x")
	head, _ := s.Head(ctx, "shop", "sub/SF-1.2")
	j, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-1.2", "Join SF-1.2", me)
	if err != nil || j.Commit == "" || j.Base != base || j.Head != head || j.UpToDate || len(j.Conflicts) > 0 {
		t.Fatalf("%+v %v", j, err)
	}
	if parents := git(t, s.Canonical("shop"), "log", "-1", "--format=%P %s", j.Commit); parents != base+" "+head+" Join SF-1.2" {
		t.Fatalf("the join: %q", parents)
	}
	// The same two commits join as the same commit, a second later too: a check run on it is found
	// again (review of M9).
	time.Sleep(1100 * time.Millisecond)
	if again, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-1.2", "Join SF-1.2", me); err != nil || again.Commit != j.Commit {
		t.Fatalf("the join moved from %s to %s: %v", j.Commit, again.Commit, err)
	}
	if err := s.AdvanceBranch(ctx, "shop", "task/SF-1-x", j.Commit); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.Head(ctx, "shop", "task/SF-1-x"); now != j.Commit {
		t.Fatalf("the ticket's branch is at %s, not the join", now)
	}
	// Joined, it has nothing more to join.
	if again, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-1.2", "Join SF-1.2", me); err != nil || !again.UpToDate || again.Commit != "" {
		t.Fatalf("joined twice: %+v %v", again, err)
	}
	// Two subtasks that changed one file: the second conflicts.
	first, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-1.3", "Join SF-1.3", me)
	if err != nil || first.Commit == "" {
		t.Fatalf("%+v %v", first, err)
	}
	if err := s.AdvanceBranch(ctx, "shop", "task/SF-1-x", first.Commit); err != nil {
		t.Fatal(err)
	}
	second, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-1.4", "Join SF-1.4", me)
	if err != nil || second.Commit != "" || !slices.Equal(second.Conflicts, []string{"a.txt"}) {
		t.Fatalf("the conflict: %+v %v", second, err)
	}
	// The branch does not go back: an older commit is refused.
	if err := s.AdvanceBranch(ctx, "shop", "task/SF-1-x", head); err == nil {
		t.Fatal("the ticket's branch went back")
	}
	// A ticket's branch that was never pushed takes the subtask's head, which left main.
	sub, _ := s.Head(ctx, "shop", "sub/SF-1.4")
	ff, err := s.JoinCandidate(ctx, "shop", "task/SF-2-y", "sub/SF-1.4", "Join", me)
	if err != nil || ff.Commit != sub || ff.Base != s.Main(ctx, "shop") {
		t.Fatalf("a branch not pushed yet: %+v %v", ff, err)
	}
	if err := s.AdvanceBranch(ctx, "shop", "task/SF-2-y", ff.Commit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinCandidate(ctx, "shop", "task/SF-1-x", "sub/SF-9.9", "Join", me); !errors.Is(err, gitstore.ErrNoHead) {
		t.Fatalf("a subtask that pushed nothing: %v", err)
	}
}

// A revert undoes one merge on main as it is now, even after later merges, and keeps what came after
// it; a revert that does not apply names the files that conflict (7.13).
func TestARevertUndoesOneMergeOnMainAsItIs(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	merge := func(branch string, files map[string]string) string {
		t.Helper()
		pushWork(t, s, "shop", branch, files)
		c, err := s.MergeCandidate(ctx, "shop", branch, "Merge "+branch, me, nil)
		if err != nil || c.Commit == "" {
			t.Fatalf("%+v %v", c, err)
		}
		if err := s.AdvanceMain(ctx, "shop", c.Main, c.Commit); err != nil {
			t.Fatal(err)
		}
		return c.Commit
	}
	one := merge("task/SF-1-x", map[string]string{"b.txt": "two\n"})
	merge("task/SF-2-y", map[string]string{"c.txt": "three\n"})
	main := s.Main(ctx, "shop")
	commit, conflicts, err := s.Revert(ctx, "shop", one, "Revert SF-1", me)
	if err != nil || len(conflicts) > 0 || !gitstore.ValidCommit(commit) {
		t.Fatalf("%q %v %v", commit, conflicts, err)
	}
	if parent := git(t, s.Canonical("shop"), "log", "-1", "--format=%P", commit); parent != main {
		t.Fatalf("the revert's parent %q, want main %q", parent, main)
	}
	files := git(t, s.Canonical("shop"), "ls-tree", "--name-only", commit)
	if strings.Contains(files, "b.txt") || !strings.Contains(files, "c.txt") || !strings.Contains(files, "a.txt") {
		t.Fatalf("the revert's tree: %q", files)
	}
	// A later change to a file the merge added: the revert does not apply.
	six := merge("task/SF-6-d", map[string]string{"d.txt": "first\n"})
	merge("task/SF-7-d", map[string]string{"d.txt": "changed later\n"})
	if _, conflicts, err := s.Revert(ctx, "shop", six, "Revert SF-6", me); err != nil || len(conflicts) != 1 || conflicts[0] != "d.txt" {
		t.Fatalf("a revert over a later change: %v %v", conflicts, err)
	}
}
