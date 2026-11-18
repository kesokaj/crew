package gitstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
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
		cmd.Env = gitstore.CleanEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
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

// Archive stops at the first path past the bound, of paths, trees counted, of bytes of files, of a path
// or of the listing, before the tree is listed
// whole into the server's memory: a remote's main, or a tree stored before the git endpoint bounded
// it, never came through the endpoint's check (6.1, since 1.03).
func TestArchiveStopsAtTheTreesBound(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	main := s.Main(ctx, "shop")
	canon := s.Canonical("shop")
	for name, c := range map[string]struct {
		commit string
		err    error
	}{
		"paths":       {treeBomb(t, canon, bomb{body: []byte("x\n"), fanout: 16, depth: 5}, main), gitstore.ErrTreeTooLarge},
		"bytes":       {treeBomb(t, canon, bomb{body: make([]byte, 3<<20), fanout: 16, depth: 2}, main), gitstore.ErrTreeTooLarge},
		"a long path": {treeBomb(t, canon, bomb{body: []byte("x\n"), fanout: 2, depth: 1, name: 4097}, main), gitstore.ErrTreeTooLarge},
		// A name longer than listTree's buffer is never read whole.
		"a path past the buffer": {treeBomb(t, canon, bomb{body: []byte("x\n"), fanout: 2, depth: 1, name: 10000}, main), gitstore.ErrTreeTooLarge},
		"its listing":            {treeBomb(t, canon, bomb{body: []byte("x\n"), fanout: 130, depth: 2, name: 2000}, main), gitstore.ErrTreeTooLarge},
		"empty trees":            {treeBomb(t, canon, bomb{fanout: 16, depth: 6}, main), gitstore.ErrTreeTooLarge},
		"within":                 {treeBomb(t, canon, bomb{body: []byte("x\n"), fanout: 16, depth: 2}, main), nil},
	} {
		git(t, canon, "update-ref", "refs/crew/heads/"+strings.ReplaceAll(name, " ", "-"), c.commit)
		if err := s.Archive(ctx, "shop", c.commit, io.Discard); !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", name, err, c.err)
		}
	}
}
