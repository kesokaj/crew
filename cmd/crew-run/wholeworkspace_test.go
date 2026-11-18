package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An index file of main that names the whole workspace counts every path for the guards only (13.8,
// since 1.05): a run that judges takes no more from main for it, and git still sees the branch's own
// files, none of them marked to be skipped.
func TestAJudgeTakesNothingMoreForAnIndexFileThatNamesTheWorkspace(t *testing.T) {
	k := agyKit(t, true)
	ws := k.env.Workspace
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	seed := t.TempDir()
	gitIn(t, seed, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(seed, ".agents"), 0o755)
	os.WriteFile(filepath.Join(seed, ".agents/rules.json"), []byte(`{"entries": [{"path": "."}]}`), 0o644)
	os.WriteFile(filepath.Join(seed, "app.go"), []byte("main's"), 0o644)
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "main")
	gitIn(t, seed, "push", "-q", origin, "main")
	os.MkdirAll(ws, 0o755)
	gitIn(t, ws, "init", "-q", "-b", "task/1-x")
	gitIn(t, ws, "remote", "add", "origin", origin)
	gitIn(t, ws, "fetch", "-q", "origin", "main")
	gitIn(t, ws, "reset", "-q", "--hard", "FETCH_HEAD")
	os.WriteFile(filepath.Join(ws, "app.go"), []byte("the branch's"), 0o644)
	gitIn(t, ws, "commit", "-q", "-am", "branch")
	if ex := k.turn(t); ex.Reason != "ok" {
		t.Fatalf("%+v", ex)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "app.go")); string(b) != "the branch's" {
		t.Fatalf("app.go: %q", b)
	}
	if flags := gitIn(t, ws, "ls-files", "-v", "app.go"); !strings.HasPrefix(flags, "H ") {
		t.Fatalf("app.go is marked: %q", flags)
	}
}
