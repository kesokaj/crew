package gitstore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
)

// What the Git screen reads (6.7): the branches with how far each is ahead, the history of a branch,
// a commit with its diff, a comparison of two points, and the tags; and nothing that is not a
// branch, a tag, or a commit reaches git.
func TestTheGitScreenReads(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	main := s.Main(ctx, "shop")
	git(t, s.Canonical("shop"), "tag", "v1", main)
	pushWork(t, s, "shop", "task/SF-1-a", map[string]string{"a.go": "package a\n", "README.md": "shop\nmore\n"})

	bs, err := s.Branches(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range bs {
		got = append(got, b.Name)
		if b.Name == "task/SF-1-a" && (b.Ahead != 1 || b.Behind != 0) {
			t.Errorf("the branch: %+v", b)
		}
		if b.Name == "main" && b.Head != main {
			t.Errorf("main is the canonical one's: %+v", b)
		}
	}
	if !slices.Equal(got, []string{"main", "task/SF-1-a"}) {
		t.Errorf("branches %v", got)
	}
	log, err := s.Log(ctx, "shop", "task/SF-1-a", "main", 10)
	if err != nil || len(log) != 1 || log[0].Subject != "work" || log[0].Parents[0] != main {
		t.Fatalf("the branch's own history: %+v %v", log, err)
	}
	c, files, err := s.Show(ctx, "shop", log[0].ID)
	if err != nil || c.ID != log[0].ID || len(files) != 2 {
		t.Fatalf("the commit: %+v %+v %v", c, files, err)
	}
	if f := files[0]; f.Path != "README.md" || f.Added != 1 || f.Removed != 0 || !strings.Contains(f.Patch, "+more") {
		t.Errorf("its first file: %+v", f)
	}
	_, first, err := s.Show(ctx, "shop", main)
	if err != nil || len(first) != 1 || first[0].Path != "README.md" {
		t.Errorf("a first commit is shown against nothing: %+v %v", first, err)
	}
	cmp, err := s.Compare(ctx, "shop", "v1", "task/SF-1-a")
	if err != nil || len(cmp) != 2 {
		t.Errorf("the comparison: %+v %v", cmp, err)
	}
	every, err := s.LogAll(ctx, "shop", 10)
	if err != nil || len(every) != 2 || every[0].Ref != "task/SF-1-a" || every[0].Subject != "work" || every[1].ID != main {
		t.Errorf("the history of all branches: %+v %v", every, err)
	}
	tags, err := s.Tags(ctx, "shop")
	if err != nil || len(tags) != 1 || tags[0].Name != "v1" || tags[0].Commit != main {
		t.Errorf("tags: %+v %v", tags, err)
	}
	pushed, err := s.LogRange(ctx, "shop", main, log[0].ID)
	if err != nil || len(pushed) != 1 || pushed[0].ID != log[0].ID {
		t.Errorf("what one push brought: %+v %v", pushed, err)
	}
	for _, bad := range []string{"--output=/tmp/x", "-p", "main..task/SF-1-a", "HEAD", "refs/heads/main", "task/../x", "nothing", ""} {
		if _, err := s.Log(ctx, "shop", bad, "", 10); !errors.Is(err, gitstore.ErrNoRev) {
			t.Errorf("%q: %v", bad, err)
		}
		if _, err := s.Compare(ctx, "shop", "main", bad); !errors.Is(err, gitstore.ErrNoRev) {
			t.Errorf("compare with %q: %v", bad, err)
		}
	}
	if _, err := s.LogRange(ctx, "shop", "--all", log[0].ID); !errors.Is(err, gitstore.ErrNoRev) {
		t.Errorf("a range of something else: %v", err)
	}
}

// A file's patch is cut at its limit, and says so.
func TestALargePatchIsCut(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	pushWork(t, s, "shop", "task/SF-1-big", map[string]string{"big.txt": strings.Repeat("a line of text\n", 10000)})
	files, err := s.Compare(ctx, "shop", "main", "task/SF-1-big")
	if err != nil || len(files) != 1 || !files[0].Cut || len(files[0].Patch) != gitstore.MaxPatch || files[0].Added != 10000 {
		t.Fatalf("%v %v", err, len(files))
	}
}

// A main pushed to the git endpoint from the terminal is held: Crew's main does not move, the endpoint
// serves Crew's main again, and the pushed commit is kept until the operator decides. Taken, it
// becomes main when it goes on from it; when main moved meanwhile it is not taken (13.11, M8).
func TestAPushedMainIsHeldAndTakenOnlyForward(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	main := s.Main(ctx, "shop")
	pushMain := func(file string) string {
		work := t.TempDir()
		git(t, work, "clone", "-q", s.Quarantine.Path("shop"), ".")
		git(t, work, "commit", "-q", "--allow-empty", "-m", file)
		git(t, work, "push", "-q", "origin", "HEAD:main")
		return git(t, work, "rev-parse", "HEAD")
	}
	ahead := pushMain("one")
	if err := s.HoldMain(ctx, "shop", ahead, "1"); err != nil {
		t.Fatal(err)
	}
	if s.Main(ctx, "shop") != main || s.Quarantine.Ref(ctx, "shop", "refs/heads/main") != main {
		t.Fatalf("a held main moved main: %s, the endpoint's %s", s.Main(ctx, "shop"), s.Quarantine.Ref(ctx, "shop", "refs/heads/main"))
	}
	if err := s.AdvanceMain(ctx, "shop", main, ahead); err != nil || s.Main(ctx, "shop") != ahead {
		t.Fatalf("taken: %v, main %s", err, s.Main(ctx, "shop"))
	}
	if err := s.DropHeld(ctx, "shop", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DropHeld(ctx, "shop", "../main"); err == nil {
		t.Fatal("a key that is not a number was taken")
	}
	// Crew's own merge moved main first: a push from the old main is not taken.
	again := pushMain("two")
	if err := s.HoldMain(ctx, "shop", again, "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceMain(ctx, "shop", main, again); !errors.Is(err, gitstore.ErrMainMoved) {
		t.Fatalf("a push from an old main: %v", err)
	}
	if s.Main(ctx, "shop") != ahead || s.Quarantine.Ref(ctx, "shop", "refs/heads/main") != ahead {
		t.Fatalf("main %s, the endpoint's %s", s.Main(ctx, "shop"), s.Quarantine.Ref(ctx, "shop", "refs/heads/main"))
	}
}

// What a run wrote cannot hide or shift a commit in the history (security review of M7): a message
// and an author that hold the separators of an earlier reading are read as they are.
func TestAMessageCannotHideACommit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Quarantine.Path("shop"), ".")
	git(t, work, "checkout", "-q", "-b", "task/SF-1-a")
	git(t, work, "commit", "-q", "--allow-empty", "--cleanup=verbatim", "--author=a\x1fb\x1ec <a@x>", "-m", "one\x1ftwo\x1ethree\n\nbody\x1f\x1e")
	git(t, work, "push", "-q", "origin", "task/SF-1-a")
	cs, err := s.Log(ctx, "shop", "task/SF-1-a", "main", 10)
	if err != nil || len(cs) != 1 || cs[0].Subject != "one\x1ftwo\x1ethree" || cs[0].Author != "a\x1fb\x1ec" || cs[0].Body != "body\x1f\x1e" {
		t.Fatalf("the commit: %+v %v", cs, err)
	}
	if c, _, err := s.Show(ctx, "shop", cs[0].ID); err != nil || c.ID != cs[0].ID {
		t.Errorf("its page: %+v %v", c, err)
	}
	all, err := s.LogAll(ctx, "shop", 10)
	if err != nil || len(all) != 2 || all[0].Ref != "task/SF-1-a" {
		t.Errorf("every branch: %+v %v", all, err)
	}
}

// The operator's own branches, which the terminal pushes in edit mode, are read like Crew's; a diff
// lists at most MaxFiles files and says how many more there are, each with its own patch.
func TestOwnBranchesAndLargeDiffs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for i := 0; i < gitstore.MaxFiles+5; i++ {
		files[fmt.Sprintf("f/%04d.txt", i)] = fmt.Sprintf("file %d\n", i)
	}
	pushWork(t, s, "shop", "you/try", files)
	if cs, err := s.Log(ctx, "shop", "you/try", "main", 10); err != nil || len(cs) != 1 {
		t.Fatalf("the operator's own branch: %+v %v", cs, err)
	}
	for _, bad := range []string{"you/", "you/../x", "you/a.lock", "yours/x"} {
		if _, err := s.Log(ctx, "shop", bad, "", 1); !errors.Is(err, gitstore.ErrNoRev) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	d, err := s.Compare(ctx, "shop", "main", "you/try")
	if err != nil || len(d) != gitstore.MaxFiles+1 || d[gitstore.MaxFiles].Omitted != 5 || d[gitstore.MaxFiles].Path != "" {
		t.Fatalf("%d files, last %+v, %v", len(d), d[len(d)-1], err)
	}
	for _, f := range d[:3] {
		if !strings.Contains(f.Patch, "+++ b/"+f.Path) || strings.Count(f.Patch, "diff --git ") != 1 {
			t.Errorf("the patch of %s: %q", f.Path, f.Patch)
		}
	}
}

// The patches of a diff are at most 1 MB in all: the file the limit falls in is cut, and the files
// after it have no patch and are cut, each still with its counts.
func TestADiffsPatchesHaveALimitInAll(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("f/%02d.txt", i)] = strings.Repeat(fmt.Sprintf("line of file %d\n", i), 3000) // about 50 KB a file
	}
	pushWork(t, s, "shop", "task/SF-1-many", files)
	d, err := s.Compare(ctx, "shop", "main", "task/SF-1-many")
	if err != nil || len(d) != 40 {
		t.Fatalf("%d files, %v", len(d), err)
	}
	total, cut, empty := 0, 0, 0
	for i, f := range d {
		total += len(f.Patch)
		if f.Cut {
			cut++
		}
		if f.Patch == "" {
			empty++
			if !f.Cut || f.Added != 3000 {
				t.Errorf("file %d past the limit: %+v", i, f)
			}
		} else if !strings.Contains(f.Patch, "+++ b/"+f.Path) {
			t.Errorf("file %d has another file's patch", i)
		}
	}
	if total > 1<<20 || empty == 0 || cut < empty+1 {
		t.Errorf("%d bytes of patches, %d cut, %d with none", total, cut, empty)
	}
}

// Touched names every file a branch changed, with whether it was deleted, past the cut of the patches:
// a deletion behind a large patch, and a deleted binary file, are seen (review of M13). FollowsBranch
// takes no commit as following a branch that is not there.
func TestTouchedSeesPastTheCutAndAMissingBranchFollowsNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "shop\n", "z_test.go": "package z\n", "zz.bin": "\x00\x01\x02"}, "First", me); err != nil {
		t.Fatal(err)
	}
	main := s.Main(ctx, "shop")
	pushWork(t, s, "shop", "task/SF-1-a", map[string]string{"a-large.txt": strings.Repeat("x\n", 600<<10)})
	work := t.TempDir()
	git(t, work, "clone", "-q", "-b", "task/SF-1-a", s.Quarantine.Path("shop"), ".")
	git(t, work, "rm", "-q", "z_test.go", "zz.bin")
	git(t, work, "-c", "user.name=a", "-c", "user.email=a@x", "commit", "-q", "-m", "Remove")
	git(t, work, "push", "-q", "origin", "task/SF-1-a")
	diffs, err := s.Compare(ctx, "shop", "main", "task/SF-1-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		if d.Path == "z_test.go" && strings.Contains(d.Patch, "deleted file mode") {
			t.Fatal("the patch of z_test.go was not cut: the test shows nothing")
		}
	}
	touched, err := s.Touched(ctx, "shop", "main", "task/SF-1-a")
	if err != nil {
		t.Fatal(err)
	}
	if !touched["z_test.go"] || !touched["zz.bin"] || touched["a-large.txt"] || len(touched) != 3 {
		t.Fatalf("touched: %v", touched)
	}
	if ok, err := s.FollowsBranch(ctx, "shop", "task/SF-9-gone", main); err != nil || ok {
		t.Fatalf("a commit follows a branch that is not there: %v %v", ok, err)
	}
}
