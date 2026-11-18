package gitendpoint

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bomb is a tree that lists fanout^depth leaves from depth+1 objects, a pack of a few hundred bytes:
// each leaf the one file body, or an empty tree when body is nil, and each name name bytes long, or
// its number when name is 0.
type bomb struct {
	body          []byte
	fanout, depth int
	name          int
}

// treeBomb makes, in the repository at dir, a commit on parent whose tree is b.
func treeBomb(t *testing.T, dir string, b bomb, parent string) string {
	t.Helper()
	run := func(stdin []byte, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = bytes.NewReader(stdin)
		cmd.Env = CleanEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args[0], err)
		}
		return strings.TrimSpace(string(out))
	}
	obj, mode, typ := run(nil, "mktree"), "040000", "tree"
	if b.body != nil {
		obj, mode, typ = run(b.body, "hash-object", "-w", "--stdin"), "100644", "blob"
	}
	for range b.depth {
		var list bytes.Buffer
		for i := range b.fanout {
			name := fmt.Sprintf("%x", i)
			if b.name > 0 {
				name += strings.Repeat("-", b.name-len(name))
			}
			fmt.Fprintf(&list, "%s %s %s\t%s\n", mode, typ, obj, name)
		}
		obj, mode, typ = run(list.Bytes(), "mktree"), "040000", "tree"
	}
	return run(nil, "commit-tree", obj, "-p", parent, "-m", "bomb")
}

// A push whose tree holds more paths, bytes of files, or bytes of a path or of its listing than Crew
// checks is refused before anything lists it whole, and so is one whose tree is an empty tree named
// millions of times, one that brings such a tree in an earlier commit, one that brings more commits
// than the hook bounds, and one that is no commit: a pack of a few hundred bytes would list millions of
// paths or gigabytes (6.1, since 1.03).
func TestAPushPastTheTreesBoundIsRefused(t *testing.T) {
	e, srv := endpoint(t)
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "a"); err != nil {
		t.Fatal(out)
	}
	a := filepath.Join(dir, "a")
	before := e.Ref(ctx, "fixture", "refs/heads/task/1-a")
	run := func(args ...string) string {
		out, err := git(t, a, "", args...)
		if err != nil {
			t.Fatalf("git %v: %s", args[0], out)
		}
		return strings.TrimSpace(out)
	}
	paths := treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 16, depth: 5}, before)
	chain := before
	for range 1001 {
		chain = run("commit-tree", before+"^{tree}", "-p", chain, "-m", "one more")
	}
	for name, c := range map[string]struct{ commit, says string }{
		"paths": {paths, "more than Crew checks"},
		"bytes": {treeBomb(t, a, bomb{body: make([]byte, 3<<20), fanout: 16, depth: 2}, before), "more than Crew checks"},
		// git's own check of the objects refuses a name this long before the hook runs.
		"a long path":    {treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 2, depth: 1, name: 4097}, before), ""},
		"its listing":    {treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 130, depth: 2, name: 2000}, before), "more than Crew checks"},
		"empty trees":    {treeBomb(t, a, bomb{fanout: 16, depth: 6}, before), "more than Crew checks"},
		"an earlier one": {run("commit-tree", before+"^{tree}", "-p", paths, "-m", "clean on top"), "more than Crew checks"},
		"many commits":   {chain, "brings more than 1000 commits"},
		"not a commit":   {"+" + run("hash-object", "-w", "README.md"), "is not a commit Crew can read"},
	} {
		out, err := git(t, a, "token-a", "push", "-q", "origin", c.commit+":refs/heads/task/1-a")
		if err == nil || !strings.Contains(out, c.says) {
			t.Errorf("%s: %v %s", name, err, out)
		}
		if e.Ref(ctx, "fixture", "refs/heads/task/1-a") != before {
			t.Fatalf("%s landed", name)
		}
	}
	// A tree within it is taken.
	small := treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 16, depth: 2}, before)
	if out, err := git(t, a, "token-a", "push", "-q", "origin", small+":refs/heads/task/1-a"); err != nil {
		t.Fatalf("a tree of 256 files: %s", out)
	}
}

// The hook's check of a push's trees runs within a time limit, and refuses a push it could not check
// in time; it lists each distinct tree once, so many commits of one tree cost one listing (6.1, since
// 1.03).
func TestAPushsTreesAreCheckedOnceAndInTime(t *testing.T) {
	for _, c := range []struct {
		name  string
		limit time.Duration
		big   bool
		ok    bool
	}{
		{"a large tree past a short limit", 50 * time.Millisecond, true, false},
		{"999 commits of one tree", time.Second, false, true},
	} {
		e, srv := endpoint(t, func(e *Endpoint) { e.TreeTime = c.limit })
		ctx := context.Background()
		dir := t.TempDir()
		if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "a"); err != nil {
			t.Fatal(out)
		}
		a := filepath.Join(dir, "a")
		before := e.Ref(ctx, "fixture", "refs/heads/task/1-a")
		tip := treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 16, depth: 4}, before)
		if !c.big {
			tip = treeBomb(t, a, bomb{body: []byte("x\n"), fanout: 16, depth: 2}, before)
			tree, _ := git(t, a, "", "rev-parse", tip+"^{tree}")
			for range 998 {
				out, err := git(t, a, "", "commit-tree", strings.TrimSpace(tree), "-p", tip, "-m", "again")
				if err != nil {
					t.Fatal(out)
				}
				tip = strings.TrimSpace(out)
			}
		}
		out, err := git(t, a, "token-a", "push", "-q", "origin", tip+":refs/heads/task/1-a")
		if c.ok != (err == nil) || (!c.ok && !strings.Contains(out, "could not be checked in")) {
			t.Errorf("%s: %v %s", c.name, err, out)
		}
	}
}
