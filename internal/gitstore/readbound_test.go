package gitstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

// readBound is the bound the reads of these tests have: their fixtures are past it, and within the
// default.
const readBound = 16 << 10

// bigShop is the shop with what each reading of git's output can be past readBound by:
//   - task/big: 400 new files of long names and big.txt of 32 KiB, in a commit of a 32 KiB message;
//   - main: merge, the merge of task/big with a message as long, and after it a commit that changes the
//     400 files;
//   - task/two: cut from that main, with one new file;
//   - task/conf: cut from the first main, with the 400 paths of other content, so it conflicts with main.
type bigShop struct {
	s                        *gitstore.Store
	first, head, merge, main string
	two, conf, blob, bigMain string
}

func newBigShop(t *testing.T) bigShop {
	t.Helper()
	ctx := context.Background()
	s := shop(t)
	b := bigShop{s: s, first: s.Main(ctx, "shop")}
	canon, quar := s.Canonical("shop"), s.Quarantine.Path("shop")
	msg := filepath.Join(t.TempDir(), "message")
	os.WriteFile(msg, []byte(strings.Repeat("A long message. ", 2<<10)), 0o644)
	files := func(dir, body string) {
		os.MkdirAll(filepath.Join(dir, "f"), 0o755)
		for i := range 400 {
			os.WriteFile(filepath.Join(dir, "f", fmt.Sprintf("%03d-%s", i, strings.Repeat("n", 54))), []byte(body), 0o644)
		}
	}
	work := t.TempDir()
	git(t, work, "clone", "-q", canon, ".")
	git(t, work, "checkout", "-q", "-b", "task/big")
	files(work, "x\n")
	os.WriteFile(filepath.Join(work, "big.txt"), bytes.Repeat([]byte("big\n"), 8<<10), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-F", msg)
	b.head = git(t, work, "rev-parse", "HEAD")
	b.blob = git(t, work, "rev-parse", "HEAD:big.txt")
	git(t, work, "push", "-q", quar, "task/big")
	git(t, work, "checkout", "-q", "main")
	git(t, work, "merge", "-q", "--no-ff", "-F", msg, "task/big")
	b.merge = git(t, work, "rev-parse", "HEAD")
	files(work, "y\n")
	git(t, work, "commit", "-q", "-am", "change them")
	b.main = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", "origin", "main")
	if err := s.GiveBackMain(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	git(t, work, "checkout", "-q", "-b", "task/two")
	os.WriteFile(filepath.Join(work, "two.txt"), []byte("two\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "two")
	b.two = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", quar, "task/two")
	git(t, work, "checkout", "-q", "-b", "task/conf", b.first)
	files(work, "z\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "conf")
	b.conf = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", quar, "task/conf")
	return b
}

// Every reading of git's output that a tree, a diff, a file or a merge can make longer than the bound
// stops at it with ErrOutputTooLarge, never ErrNoRev and never a list cut short, on a branch a run
// pushed, on main, and in a merge (6.1, since 1.04); within the bound, each is read as before. A
// history's fields are cut instead, each to its own cut, and only what it keeps of them is bounded so
// (since 1.09, historycut_test.go): a pack of 285 KB carried a message of 300 MB on dave.
func TestEveryReadOfGitsOutputStopsAtTheBound(t *testing.T) {
	ctx := context.Background()
	b := newBigShop(t)
	s := b.s
	all := func(string) bool { return true }
	// A conflict list or an overlay read in part would read as a merge with fewer conflicts, or a file
	// of main as none.
	keep := gitstore.Overlay{"big.txt": func(old []byte) []byte { return old }}
	reads := map[string]func() error{
		"Show":          func() error { _, _, err := s.Show(ctx, "shop", b.head); return err },
		"LastChange":    func() error { _, _, _, err := s.LastChange(ctx, "shop", b.main, "big.txt"); return err },
		"Files":         func() error { _, err := s.Files(ctx, "shop", b.main, all, 1000); return err },
		"LsTree":        func() error { _, err := s.LsTree(ctx, "shop", b.main); return err },
		"Compare":       func() error { _, err := s.Compare(ctx, "shop", "main", "task/conf"); return err },
		"Touched":       func() error { _, err := s.Touched(ctx, "shop", "main", "task/conf"); return err },
		"ReadBranch":    func() error { _, err := s.ReadBranch(ctx, "shop", "task/conf"); return err },
		"ChangedSince":  func() error { _, err := s.ChangedSince(ctx, "shop", b.conf); return err },
		"ChangedFiles":  func() error { _, err := s.ChangedFiles(ctx, "shop", b.first, b.conf); return err },
		"ChangedOnMain": func() error { _, err := s.ChangedOnMain(ctx, "shop", b.first, b.main); return err },
		"Changes":       func() error { _, _, err := s.Changes(ctx, "shop", b.first, b.main); return err },
		"File":          func() error { _, err := s.File(ctx, "shop", "big.txt"); return err },
		"FileAt":        func() error { _, err := s.FileAt(ctx, "shop", b.main, "big.txt"); return err },
		"IndexFile":     func() error { _, _, err := s.IndexFile(ctx, "shop", "big.txt"); return err },
		"IndexFileAt":   func() error { _, _, err := s.IndexFileAt(ctx, "shop", b.main, "big.txt"); return err },
		"BranchFile":    func() error { _, err := s.BranchFile(ctx, "shop", "task/big", "big.txt"); return err },
		"BaseFile":      func() error { _, err := s.BaseFile(ctx, "shop", "task/two", "big.txt"); return err },
		"Blob":          func() error { _, err := s.Blob(ctx, "shop", b.blob, 1<<20); return err },
		"an overlay of main's file": func() error {
			_, err := s.MergeCandidate(ctx, "shop", "task/two", "m", me, keep)
			return err
		},
		"a merge's conflicts": func() error {
			c, err := s.MergeCandidate(ctx, "shop", "task/conf", "m", me, nil)
			if err == nil && len(c.Conflicts) != 400 {
				return fmt.Errorf("%d conflicts", len(c.Conflicts))
			}
			return err
		},
		"a revert's conflicts": func() error {
			_, files, err := s.Revert(ctx, "shop", b.merge, "r", me)
			if err == nil && len(files) < 400 {
				return fmt.Errorf("%d conflicts", len(files))
			}
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			s.MaxOutput = 0
			if err := read(); err != nil {
				t.Fatalf("within the bound: %v", err)
			}
			s.MaxOutput = readBound
			defer func() { s.MaxOutput = 0 }()
			if err := read(); !errors.Is(err, gitstore.ErrOutputTooLarge) || errors.Is(err, gitstore.ErrNoRev) {
				t.Fatalf("past the bound: %v", err)
			}
		})
	}
}

// The bound is the listing's bound of a tree, 64 MiB: every tree a push brings lists within it. A
// commit's message past it is read cut (since 1.09, historycut_test.go).
func TestTheBoundOfAReadIsATreesListing(t *testing.T) {
	if gitstore.MaxOutput != gitendpoint.MaxListBytes {
		t.Fatalf("the bound is %d, the listing's %d", gitstore.MaxOutput, gitendpoint.MaxListBytes)
	}
	if gitstore.MaxOutput != 64<<20 {
		t.Fatalf("the bound is %d", gitstore.MaxOutput)
	}
}

// git that goes on writing past the bound is stopped, not read to its end; and what git says on its
// error side is kept to its start, for a read and for a command whose output only an error shows.
func TestGitPastTheBoundIsStoppedAndItsErrorTextIsCut(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	main := s.Main(ctx, "shop")
	dir := t.TempDir()
	fake := func(name, script string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755)
		return p
	}
	// It writes without end, and is not stopped by a pipe that closes: only a kill ends it.
	s.Git = fake("endless", `trap '' PIPE; while :; do printf '%01024d' 0 2>/dev/null; done`)
	s.MaxOutput = readBound
	done := make(chan error, 1)
	go func() { _, err := s.LsTree(ctx, "shop", main); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, gitstore.ErrOutputTooLarge) {
			t.Fatalf("an endless answer: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("git was not stopped past the bound")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Loud only for the commands each case is about, so the bundle's own git starts.
	s.Git = fake("loud", `for a; do case "$a" in ls-tree|push|bundle) head -c 1048576 /dev/zero | tr '\0' e >&2; exit 1;; esac; done; exec `+real+` "$@"`)
	cut := func(what string, err error) {
		t.Helper()
		if err == nil || len(err.Error()) > 65<<10 {
			t.Errorf("%s's error text: %d bytes", what, len(fmt.Sprint(err)))
		}
	}
	_, err = s.LsTree(ctx, "shop", main)
	cut("a read", err)
	cut("a command", s.GiveBackMain(ctx, "shop"))
	cut("the listing of a tree", s.Archive(ctx, "shop", main, io.Discard))
	cut("a bundle", s.Bundle(ctx, "shop", io.Discard))
}

// A file git cannot read is an error for File, FileAt and the overlay of a merge, as one past the bound
// is; only a path the commit does not have, or one that is no file, is nil (since 1.04).
func TestAFileGitCannotReadIsAnError(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/one", map[string]string{"one.txt": "one\n", "dir/x.txt": "x\n"})
	main := s.Main(ctx, "shop")
	// main holds the same files, so a path that is not one of them is no file of main's either.
	if _, err := s.CommitPage(ctx, "shop", main, "dir/x.txt", []byte("x\n"), "x", me); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitPage(ctx, "shop", s.Main(ctx, "shop"), "one.txt", []byte("one\n"), "one", me); err != nil {
		t.Fatal(err)
	}
	main = s.Main(ctx, "shop")
	if b, err := s.FileAt(ctx, "shop", main, "dir/x.txt"); string(b) != "x\n" || err != nil {
		t.Fatalf("a nested file: %q %v", b, err)
	}
	for _, p := range []string{"missing.txt", "dir", "dir/", "dir/../one.txt", "one.txt/x"} {
		if b, err := s.File(ctx, "shop", p); b != nil || err != nil {
			t.Fatalf("File of %s: %q %v", p, b, err)
		}
		// FileAt refuses a path that is not one Crew reads before git sees it.
		if b, err := s.FileAt(ctx, "shop", main, p); b != nil || (err != nil && !errors.Is(err, gitstore.ErrNoRev)) {
			t.Fatalf("FileAt of %s: %q %v", p, b, err)
		}
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A git that cannot read a blob, as when it is killed or runs out of time.
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = cat-file ] && exit 1; done\nexec "+real+" \"$@\"\n"), 0o755)
	s.Git = fake
	if b, err := s.File(ctx, "shop", "README.md"); err == nil || !strings.Contains(err.Error(), "git cat-file") {
		t.Fatalf("File: %q", b)
	}
	if b, err := s.FileAt(ctx, "shop", main, "README.md"); err == nil || !strings.Contains(err.Error(), "git cat-file") {
		t.Fatalf("FileAt: %q", b)
	}
	keep := gitstore.Overlay{"README.md": func(old []byte) []byte { return old }}
	if c, err := s.MergeCandidate(ctx, "shop", "task/one", "m", me, keep); err == nil || !strings.Contains(err.Error(), "git cat-file") {
		t.Fatalf("an overlay of a file git cannot read: %+v", c)
	}
	// A git that cannot list the path is an error too, not a file that is not there.
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = ls-tree ] && exit 1; done\nexec "+real+" \"$@\"\n"), 0o755)
	if b, err := s.File(ctx, "shop", "README.md"); err == nil || !strings.Contains(err.Error(), "git --literal-pathspecs") {
		t.Fatalf("File of a path git cannot list: %q %v", b, err)
	}
}

// A header's value that git repeats is taken out of the error, also its start where the error text is
// cut short (since 1.04).
func TestAHeaderCutShortInAnErrorIsRedacted(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s3cr", 10)
	header := "Authorization: Bearer " + token
	dir := t.TempDir()
	for name, before := range map[string]int{"whole": 100, "cut short": 64<<10 - 30} {
		t.Run(name, func(t *testing.T) {
			fake := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			os.WriteFile(fake, []byte(fmt.Sprintf("#!/bin/sh\nfor a; do [ \"$a\" = fetch ] && { head -c %d /dev/zero | tr '\\0' e; printf '%%s' \"$GIT_CONFIG_VALUE_0\"; exit 1; }; done\nexec %s \"$@\"\n", before, real)), 0o755)
			s := newStore(t)
			s.Git = fake
			err := s.CreateFrom(context.Background(), "shop", "file:///nowhere", header)
			if err == nil || strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("%.200q", fmt.Sprint(err)[max(0, len(fmt.Sprint(err))-200):])
			}
		})
	}
}

// The cut read of a diff's patches is never longer than the bound of a read: a caller's longer limit
// is cut to it (since 1.04).
func TestADiffsPatchesAreCutAtTheBound(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/long", map[string]string{"long.txt": strings.Repeat("a long line of the patch\n", 1600)})
	for bound, cut := range map[int64]bool{0: false, readBound: true} {
		s.MaxOutput = bound
		files, err := s.Compare(ctx, "shop", "main", "task/long")
		if err != nil || len(files) != 1 || files[0].Cut != cut || int64(len(files[0].Patch)) > max(bound, gitstore.MaxPatch) {
			t.Fatalf("bound %d: %d files, cut %v, %d bytes, %v", bound, len(files), len(files) == 1 && files[0].Cut, len(fmt.Sprint(files)), err)
		}
	}
}
