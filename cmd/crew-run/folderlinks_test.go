package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A run that judges reads nothing through a link of a folder that instructs, main's or the branch's: the
// branch's link is taken out as itself, and main's is never written, so the folder it leads to is no
// skill of the judge's, whatever the branch put there (13.8, since 1.06).
func TestAJudgeReadsNothingThroughAFolderLink(t *testing.T) {
	k := agyKit(t, true)
	ws := k.env.Workspace
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	seed := t.TempDir()
	gitIn(t, seed, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(seed, "shared/skills/flows"), 0o755)
	os.Symlink("../shared/skills", filepath.Join(seed, ".agents/skills"))
	os.WriteFile(filepath.Join(seed, "shared/skills/flows/SKILL.md"), []byte("main's"), 0o644)
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "main")
	gitIn(t, seed, "push", "-q", origin, "main")
	os.MkdirAll(ws, 0o755)
	gitIn(t, ws, "init", "-q", "-b", "task/1-x")
	gitIn(t, ws, "remote", "add", "origin", origin)
	gitIn(t, ws, "fetch", "-q", "origin", "main")
	gitIn(t, ws, "reset", "-q", "--hard", "FETCH_HEAD")
	os.WriteFile(filepath.Join(ws, "shared/skills/flows/SKILL.md"), []byte("the branch's"), 0o644)
	os.MkdirAll(filepath.Join(ws, "notes"), 0o755)
	os.WriteFile(filepath.Join(ws, "notes/a.md"), []byte("the branch's rule"), 0o644)
	os.Symlink("../notes", filepath.Join(ws, ".agents/rules"))
	gitIn(t, ws, "add", "-A")
	gitIn(t, ws, "commit", "-q", "-m", "branch")
	if ex := k.turn(t); ex.Reason != "ok" {
		t.Fatalf("%+v", ex)
	}
	for _, link := range []string{".agents/skills", ".agents/rules"} {
		if _, err := os.Lstat(filepath.Join(ws, link)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is in the judge's workspace: %v", link, err)
		}
	}
	// What the links led to is the branch's work, as any other file, and nothing reads it as a skill.
	if b, _ := os.ReadFile(filepath.Join(ws, "shared/skills/flows/SKILL.md")); string(b) != "the branch's" {
		t.Fatalf("SKILL.md: %q", b)
	}
}
