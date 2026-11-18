package gitstore_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
)

// An eval's branch starts at an old commit in the quarantine repository; the commit its checks run on is
// its head with the held-out files of another commit copied in; a file that commit lacks is refused; and
// the branch goes with what Crew made of it (9.6).
func TestAnEvalsBranchAndItsHeldOutFiles(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	base := s.Main(ctx, "shop")
	if err := s.EvalBranch(ctx, "shop", "task/SF-1-x", base); err == nil {
		t.Fatal("an eval's branch named like a ticket's")
	}
	// What the ticket merged, with its test, as the commit the held-out file comes from.
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"lock_test.go": "package lock\n", "lock.go": "package lock\n"})
	merged, _ := s.Head(ctx, "shop", "task/SF-1-x")
	if err := s.EvalBranch(ctx, "shop", "eval/9", base); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Head(ctx, "shop", "eval/9"); h != base {
		t.Fatalf("the eval's branch is at %s", h)
	}
	pushWork(t, s, "shop", "eval/9", map[string]string{"lock.go": "package lock // again\n"})
	head, _ := s.Head(ctx, "shop", "eval/9")
	if ok, err := s.StartsAt(ctx, "shop", head, base); err != nil || !ok {
		t.Fatalf("the eval's head after its builder's push starts at its base: %v %v", ok, err)
	}
	// A history rewritten without the base commit, as a run may push to its own branch.
	q := s.Quarantine.Path("shop")
	orphan := git(t, q, "commit-tree", head+"^{tree}", "-m", "rewritten")
	if ok, err := s.StartsAt(ctx, "shop", orphan, base); err != nil || ok {
		t.Fatalf("a head without the base commit: %v %v", ok, err)
	}
	// The ticket's work is fetched into the canonical repository, where the held-out file is read.
	git(t, s.Canonical("shop"), "fetch", "-q", s.Quarantine.Path("shop"), "+refs/heads/task/SF-1-x:refs/crew/heads/task/SF-1-x")
	commit, err := s.WithHeldOut(ctx, "shop", "eval/9", merged, []string{"lock_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Archive(ctx, "shop", commit, &buf); err != nil {
		t.Fatal(err)
	}
	if n := tarNames(t, buf.Bytes()); !slices.Contains(n, "lock_test.go") || !slices.Contains(n, "lock.go") || !slices.Contains(n, "a.txt") {
		t.Fatalf("the tree checked holds %v", n)
	}
	if _, err := s.WithHeldOut(ctx, "shop", "eval/9", merged, []string{"nothing_test.go"}); err == nil {
		t.Fatal("a held-out file the commit lacks")
	}
	if err := s.DropEvalBranch(ctx, "shop", "eval/9"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Head(ctx, "shop", "eval/9"); h != "" {
		t.Fatal("the branch is still there")
	}
	if p, err := s.FirstParent(ctx, "shop", commit); err != nil || len(p) < 40 {
		t.Fatalf("first parent %q %v", p, err)
	}
}

// A tag of a part or of Done is made once and never moved; a name Crew does not make is refused (6.6).
func TestATagIsMadeOnceAndNeverMoved(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	first := s.Main(ctx, "shop")
	if made, err := s.TagOnce(ctx, "shop", "part-sign-in", first); err != nil || !made {
		t.Fatalf("made %v %v", made, err)
	}
	pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"b.txt": "b\n"})
	other, _ := s.Head(ctx, "shop", "task/SF-1-x")
	git(t, s.Canonical("shop"), "fetch", "-q", s.Quarantine.Path("shop"), "+refs/heads/task/SF-1-x:refs/crew/heads/x")
	if made, err := s.TagOnce(ctx, "shop", "part-sign-in", other); err != nil || made {
		t.Fatalf("a second time: %v %v", made, err)
	}
	tags, _ := s.Tags(ctx, "shop")
	if len(tags) != 1 || tags[0].Commit != first {
		t.Fatalf("tags %+v", tags)
	}
	for _, bad := range []string{"v1.0.0", "part-", "done-x", "../main"} {
		if _, err := s.TagOnce(ctx, "shop", bad, first); err == nil {
			t.Errorf("the tag %q", bad)
		}
	}
}

// A builder's file or folder where a held-out file goes gives way to the held-out file: git refused to
// copy it in, and the case measured nothing (13.9, since 1.02).
func TestAHeldOutFileTakesItsPlaceInTheBuildersTree(t *testing.T) {
	ctx := context.Background()
	for name, work := range map[string]map[string]string{
		"a file where its folder goes": {"tests": "not a folder\n"},
		"a folder where it goes":       {"tests/count_test.go/x": "a folder\n"},
	} {
		s := shop(t)
		base := s.Main(ctx, "shop")
		pushWork(t, s, "shop", "task/SF-1-x", map[string]string{"tests/count_test.go": "package tests\n"})
		merged, _ := s.Head(ctx, "shop", "task/SF-1-x")
		git(t, s.Canonical("shop"), "fetch", "-q", s.Quarantine.Path("shop"), "+refs/heads/task/SF-1-x:refs/crew/heads/task/SF-1-x")
		if err := s.EvalBranch(ctx, "shop", "eval/9", base); err != nil {
			t.Fatal(err)
		}
		pushWork(t, s, "shop", "eval/9", work)
		commit, err := s.WithHeldOut(ctx, "shop", "eval/9", merged, []string{"tests/count_test.go"})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var buf bytes.Buffer
		if err := s.Archive(ctx, "shop", commit, &buf); err != nil {
			t.Fatal(err)
		}
		if n := tarNames(t, buf.Bytes()); !slices.Contains(n, "tests/count_test.go") || slices.Contains(n, "tests") || slices.Contains(n, "tests/count_test.go/x") {
			t.Fatalf("%s: the tree checked holds %v", name, n)
		}
	}
}
