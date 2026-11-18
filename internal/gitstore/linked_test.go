package gitstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
)

// A link that stands for a folder that instructs is followed on main, at a commit and on a ticket's
// branch, and a file of the branch or of its base is read through the links on its way, as the CLI
// opens it (13.8, since 1.06). A link out of the workspace, and a git that cannot list the tree, is an
// error.
func TestALinkOfAFolderIsFollowedOnMainAndOnABranch(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	commit := func(dir, msg string, files map[string]string) {
		t.Helper()
		for p, body := range files {
			full := filepath.Join(dir, p)
			os.RemoveAll(full)
			os.MkdirAll(filepath.Dir(full), 0o755)
			if to, ok := cutLink(body); ok {
				os.Symlink(to, full)
				continue
			}
			os.WriteFile(full, []byte(body), 0o644)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", msg)
	}
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	commit(work, "skills through a link", map[string]string{".agents/skills": "-> ../shared/skills", "shared/skills/VENDORED.md": "rows\n"})
	git(t, work, "push", "-q", "origin", "main")
	main := s.Main(ctx, "shop")
	want := []string{".agents/skills/", "shared/skills/"}
	pats := []string{".agents/skills"}
	for name, read := range map[string]func() ([]string, error){
		"Linked":   func() ([]string, error) { return s.Linked(ctx, "shop", pats) },
		"LinkedAt": func() ([]string, error) { return s.LinkedAt(ctx, "shop", main, pats) },
	} {
		if got, err := read(); err != nil || !slices.Equal(got, want) {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	branch := t.TempDir()
	git(t, branch, "clone", "-q", s.Quarantine.Path("shop"), ".")
	git(t, branch, "fetch", "-q", s.Canonical("shop"), "main")
	git(t, branch, "checkout", "-q", "-b", "task/one", "FETCH_HEAD")
	commit(branch, "more", map[string]string{"shared/skills/VENDORED.md": "more rows\n"})
	git(t, branch, "push", "-q", "origin", "task/one")
	git(t, s.Canonical("shop"), "push", "-q", s.Quarantine.Path("shop"), "main")
	if got, err := s.BranchLinked(ctx, "shop", "task/one", pats); err != nil || !slices.Equal(got, want) {
		t.Fatalf("BranchLinked: %v %v", got, err)
	}
	if b, err := s.BranchFile(ctx, "shop", "task/one", ".agents/skills/VENDORED.md"); string(b) != "more rows\n" || err != nil {
		t.Fatalf("BranchFile: %q %v", b, err)
	}
	if b, err := s.BaseFile(ctx, "shop", "task/one", ".agents/skills/VENDORED.md"); string(b) != "rows\n" || err != nil {
		t.Fatalf("BaseFile: %q %v", b, err)
	}
	// A way out of the workspace is an error, for each of them.
	commit(branch, "out", map[string]string{".agents/skills": "-> /opt/skills"})
	git(t, branch, "push", "-q", "origin", "task/one")
	if got, err := s.BranchLinked(ctx, "shop", "task/one", pats); !errors.Is(err, cli.ErrLinkWay) {
		t.Fatalf("BranchLinked out: %v %v", got, err)
	}
	if b, err := s.BranchFile(ctx, "shop", "task/one", ".agents/skills/VENDORED.md"); !errors.Is(err, cli.ErrLinkWay) {
		t.Fatalf("BranchFile out: %q %v", b, err)
	}
	// A git that cannot list the tree is an error, never a tree with no links.
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a; do [ \"$a\" = --full-tree ] && exit 1; done\nexec "+real+" \"$@\"\n"), 0o755)
	s.Git = fake
	defer func() { s.Git = "" }()
	if got, err := s.Linked(ctx, "shop", pats); err == nil {
		t.Fatalf("a git that cannot list: %v", got)
	}
}

func cutLink(body string) (string, bool) {
	if len(body) > 3 && body[:3] == "-> " {
		return body[3:], true
	}
	return "", false
}

// The store reads every lookup of a way from one listing of the tree, and each link's text once, as the
// hook does (since 1.06, after its security review).
func TestTheStoreListsATreeOnceForItsLinks(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	deep := filepath.Join(strings.Repeat("d/", 100), "x.md")
	os.MkdirAll(filepath.Join(work, filepath.Dir(deep)), 0o755)
	os.WriteFile(filepath.Join(work, deep), []byte("x\n"), 0o644)
	for i := range 200 {
		os.MkdirAll(filepath.Join(work, fmt.Sprintf("p%d", i)), 0o755)
		os.Symlink("../"+deep, filepath.Join(work, fmt.Sprintf("p%d/AGENTS.md", i)))
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "links")
	git(t, work, "push", "-q", "origin", "main")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	fake := filepath.Join(dir, "git")
	os.WriteFile(fake, []byte("#!/bin/sh\necho >> "+count+"\nexec "+real+" \"$@\"\n"), 0o755)
	s.Git = fake
	defer func() { s.Git = "" }()
	linked, err := s.Linked(ctx, "shop", []string{"AGENTS.md"})
	calls, _ := os.ReadFile(count)
	if err != nil || !slices.Contains(linked, deep+"/") || strings.Count(string(calls), "\n") > 10 {
		t.Fatalf("%d linked, %d git calls: %v", len(linked), strings.Count(string(calls), "\n"), err)
	}
}
