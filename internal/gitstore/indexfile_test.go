package gitstore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An index file of main is read as the CLI opens it: through a link on its way, to the file the link
// leads to within the tree, with the way (13.8, since 1.05). A git that cannot list or read a part of
// the way is an error.
func TestAnIndexFileIsReadThroughTheLinksOnItsWay(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	os.MkdirAll(filepath.Join(work, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(work, "cfg"), 0o755)
	os.Symlink("../cfg/p.json", filepath.Join(work, ".agents/plugins.json"))
	os.WriteFile(filepath.Join(work, "cfg/p.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "a link")
	git(t, work, "push", "-q", "origin", "main")
	main := s.Main(ctx, "shop")
	read := map[string]func() ([]byte, []string, error){
		"IndexFile": func() ([]byte, []string, error) {
			b, way, err := s.IndexFile(ctx, "shop", ".agents/plugins.json")
			return b, way.Paths(), err
		},
		"IndexFileAt": func() ([]byte, []string, error) {
			b, way, err := s.IndexFileAt(ctx, "shop", main, ".agents/plugins.json")
			return b, way.Paths(), err
		},
	}
	for name, read := range read {
		t.Run(name, func(t *testing.T) {
			s.Git = ""
			b, paths, err := read()
			if string(b) != `{"entries": [{"path": "tools"}]}` || !slices.Equal(paths, []string{".agents/plugins.json/", "cfg/p.json/"}) || err != nil {
				t.Fatalf("%q %v %v", b, paths, err)
			}
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			for _, fails := range []string{"ls-tree", "cat-file"} {
				fake := filepath.Join(t.TempDir(), "git")
				os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = "+fails+" ] && exit 1; done\nexec "+real+" \"$@\"\n"), 0o755)
				s.Git = fake
				if b, _, err := read(); err == nil || !strings.Contains(err.Error(), "exit status 1") {
					t.Fatalf("a git that fails %s: %q %v", fails, b, err)
				}
			}
		})
	}
}

// A branch's file and its base's, which the check of VENDORED.md reads, are nil only when the branch or
// its base on main has no such file, or the branch no base on main; every other failure of git is an
// error, never a file that is not there (since 1.05).
func TestABranchsFileOrItsBasesGitCannotReadIsAnError(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	pushWork(t, s, "shop", "task/one", map[string]string{"one.txt": "one\n"})
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Quarantine.Path("shop"), ".")
	git(t, work, "checkout", "-q", "--orphan", "task/lone")
	git(t, work, "commit", "-q", "--allow-empty", "-m", "alone")
	git(t, work, "push", "-q", "origin", "task/lone")
	if b, err := s.BranchFile(ctx, "shop", "task/one", "one.txt"); string(b) != "one\n" || err != nil {
		t.Fatalf("BranchFile: %q %v", b, err)
	}
	if b, err := s.BaseFile(ctx, "shop", "task/one", "a.txt"); string(b) != "one\n" || err != nil {
		t.Fatalf("BaseFile: %q %v", b, err)
	}
	for name, read := range map[string]func() ([]byte, error){
		"a file the branch lacks": func() ([]byte, error) { return s.BranchFile(ctx, "shop", "task/one", "two.txt") },
		"a file its base lacks":   func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/one", "one.txt") },
		"a branch with no base":   func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/lone", "a.txt") },
	} {
		if b, err := read(); b != nil || err != nil {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		fails string
		read  func() ([]byte, error)
	}{
		"a branch's file git cannot list": {"ls-tree", func() ([]byte, error) { return s.BranchFile(ctx, "shop", "task/one", "one.txt") }},
		"a branch's file git cannot read": {"cat-file", func() ([]byte, error) { return s.BranchFile(ctx, "shop", "task/one", "one.txt") }},
		"a base git cannot find":          {"merge-base", func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/one", "a.txt") }},
		"a base's file git cannot list":   {"ls-tree", func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/one", "a.txt") }},
		"a base's file git cannot read":   {"cat-file", func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/one", "a.txt") }},
		// A git that fails as when it has no base, but says nothing of one, is an error too.
		"a branch git does not have": {"", func() ([]byte, error) { return s.BaseFile(ctx, "shop", "task/gone", "a.txt") }},
	} {
		t.Run(name, func(t *testing.T) {
			fake := filepath.Join(t.TempDir(), "git")
			// Exit status 128, as git's own failures are: its 1 is a merge base's "none".
			os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = \""+c.fails+"\" ] && exit 128; done\nexec "+real+" \"$@\"\n"), 0o755)
			s.Git = fake
			defer func() { s.Git = "" }()
			if b, err := c.read(); err == nil {
				t.Fatalf("%q", b)
			}
		})
	}
}

// IndexFile reads one commit of main for the whole way: main moving between two of its reads never
// stitches a way from two trees (since 1.05).
func TestAnIndexFileOfMainIsReadFromOneCommit(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	os.MkdirAll(filepath.Join(work, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(work, "cfg"), 0o755)
	os.Symlink("../cfg/p.json", filepath.Join(work, ".agents/plugins.json"))
	os.WriteFile(filepath.Join(work, "cfg/p.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "a link")
	git(t, work, "push", "-q", "origin", "main")
	// The next main holds plugins.json as a file of its own.
	os.Remove(filepath.Join(work, ".agents/plugins.json"))
	os.WriteFile(filepath.Join(work, ".agents/plugins.json"), []byte(`{"entries": [{"path": "elsewhere"}]}`), 0o644)
	git(t, work, "commit", "-q", "-am", "a file")
	next := git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", "origin", "HEAD:refs/crew/test/next")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A git that moves main to the next commit when it first lists a path.
	mark := filepath.Join(t.TempDir(), "moved")
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do if [ \"$a\" = ls-tree ] && [ ! -e "+mark+" ]; then touch "+mark+"; "+real+" update-ref refs/heads/main "+next+"; fi; done\nexec "+real+" \"$@\"\n"), 0o755)
	s.Git = fake
	b, way, err := s.IndexFile(ctx, "shop", ".agents/plugins.json")
	if _, merr := os.Stat(mark); merr != nil {
		t.Fatal("main was not moved")
	}
	if err != nil || string(b) != `{"entries": [{"path": "tools"}]}` || !slices.Equal(way.Links, []string{".agents/plugins.json"}) {
		t.Fatalf("%q %+v %v", b, way, err)
	}
}
