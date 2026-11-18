package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/runspec"
)

// fakeChat stands in for agy in a chat's turn: it notes the marker of the workspace's commit, the
// files agy would load by itself, and the other branches it can read, then leaves a file behind.
const fakeChat = `#!/bin/sh
if [ "$1" = models ]; then printf 'gemini-3.8-flash\tGemini 3.8 Flash\n'; exit 0; fi
cat > /dev/null
echo "marker $(cat marker.txt)" >> "$HOME/seen.txt"
[ -e .agents/hooks.json ] && echo "present .agents/hooks.json" >> "$HOME/seen.txt"
git rev-parse -q --verify refs/remotes/origin/sub/1.2 >/dev/null && echo "also $(git show refs/remotes/origin/sub/1.2:sub.txt)" >> "$HOME/seen.txt"
[ -e stray.txt ] && echo "stray left" >> "$HOME/seen.txt"
echo stray > stray.txt
cat "$FAKE_AGY_RECORDING"
`

// pushTo commits files on top of ref in the endpoint's repository and pushes the commit to ref.
func pushTo(t *testing.T, k *turnKit, ref string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "clone", "-q", k.ep.Path("fixture"), ".")
	if out := gitIn(t, dir, "ls-remote", "origin", ref); strings.TrimSpace(out) != "" {
		gitIn(t, dir, "fetch", "-q", "origin", ref)
		gitIn(t, dir, "checkout", "-q", "FETCH_HEAD")
	}
	for p, s := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "x")
	gitIn(t, dir, "push", "-q", "origin", "HEAD:"+ref)
	return strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
}

// turnN runs turn n of the kit's run.
func (k *turnKit) turnN(t *testing.T, n int) {
	t.Helper()
	os.MkdirAll(filepath.Join(k.home, runspec.TurnDir(n)), 0o700)
	os.WriteFile(filepath.Join(k.home, runspec.TurnDir(n), runspec.PromptFile), []byte("What changed?"), 0o644)
	var out, errb bytes.Buffer
	if code := turn(k.env, []string{filepath.Base(runspec.TurnDir(n))}, &out, &errb); code != 0 {
		t.Fatalf("turn %d: exit %d: %s", n, code, errb.String())
	}
}

func chatKit(t *testing.T) *turnKit {
	t.Helper()
	k := newTurnKit(t, true)
	bin := strings.Split(os.Getenv("PATH"), ":")[0]
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(fakeChat), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, _ := filepath.Abs("../../testdata/agy/normal-run-flash-low.jsonl")
	t.Setenv("FAKE_AGY_RECORDING", rec)
	auth := k.ep.Auth
	k.ep.Auth = func(r *http.Request, repo string) (gitendpoint.Access, error) {
		acc, err := auth(r, repo)
		acc.Read = append(acc.Read, "refs/heads/sub/1.2")
		acc.Push = nil
		return acc, err
	}
	return k
}

func seenOf(t *testing.T, k *turnKit) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(k.home, "seen.txt"))
	os.Remove(filepath.Join(k.home, "seen.txt"))
	return string(b)
}

// A chat's workspace is fetched again at every turn (11.9, D7): a commit pushed between two turns is
// in the second, the files agy loads by itself that the branch added meanwhile are taken out before
// the CLI starts (13.8, rule 13), what a turn left in the workspace is gone, and the branches of its
// subtasks are fetched too.
func TestAChatsWorkspaceIsFetchedAgainAtEveryTurn(t *testing.T) {
	k := chatKit(t)
	pushTo(t, k, "refs/heads/task/1-x", map[string]string{"marker.txt": "one"})
	pushTo(t, k, "refs/heads/sub/1.2", map[string]string{"sub.txt": "part"})
	k.writeRun(t, runspec.Run{Run: "r1", Subtask: 1, CLI: "agy", Model: "gemini-3.8-flash", Tools: true, NoRepoConfig: true,
		Limits: runspec.Limits{TimeoutSeconds: 60, MaxTurns: 50},
		Git: runspec.Git{Path: "/git/fixture.git", Branch: "task/1-x", Base: "refs/heads/task/1-x", Refresh: true,
			Also: []string{"sub/1.2"}}})
	k.turnN(t, 1)
	if seen := seenOf(t, k); !strings.Contains(seen, "marker one") || !strings.Contains(seen, "also part") || strings.Contains(seen, "present") {
		t.Fatalf("turn 1 saw %q", seen)
	}
	pushTo(t, k, "refs/heads/task/1-x", map[string]string{"marker.txt": "two", ".agents/hooks.json": `{"x":{}}`})
	k.turnN(t, 2)
	if seen := seenOf(t, k); !strings.Contains(seen, "marker two") || strings.Contains(seen, "present .agents/hooks.json") ||
		strings.Contains(seen, "stray left") {
		t.Fatalf("turn 2 saw %q", seen)
	}
}

// Without Refresh, a workspace that exists is the one the turn before left (12.4): the read-only runs
// of a ticket's stages are not fetched again.
func TestAWorkspaceWithoutRefreshIsLeftAsItIs(t *testing.T) {
	k := chatKit(t)
	pushTo(t, k, "refs/heads/task/1-x", map[string]string{"marker.txt": "one"})
	k.writeRun(t, runspec.Run{Run: "r1", Subtask: 1, CLI: "agy", Model: "gemini-3.8-flash", Tools: true, NoRepoConfig: true,
		Limits: runspec.Limits{TimeoutSeconds: 60, MaxTurns: 50},
		Git:    runspec.Git{Path: "/git/fixture.git", Branch: "task/1-x", Base: "refs/heads/task/1-x"}})
	k.turnN(t, 1)
	seenOf(t, k)
	pushTo(t, k, "refs/heads/task/1-x", map[string]string{"marker.txt": "two"})
	k.turnN(t, 2)
	if seen := seenOf(t, k); !strings.Contains(seen, "marker one") || !strings.Contains(seen, "stray left") {
		t.Fatalf("turn 2 saw %q", seen)
	}
}

// A chat of a Done ticket reads main at its merge commit, not main's head (11.9).
func TestAChatOfADoneTicketReadsItsMergeCommit(t *testing.T) {
	k := chatKit(t)
	merge := pushTo(t, k, "refs/heads/main", map[string]string{"marker.txt": "merged"})
	pushTo(t, k, "refs/heads/main", map[string]string{"marker.txt": "later"})
	k.writeRun(t, runspec.Run{Run: "r1", Subtask: 1, CLI: "agy", Model: "gemini-3.8-flash", Tools: true, NoRepoConfig: true,
		Limits: runspec.Limits{TimeoutSeconds: 60, MaxTurns: 50},
		Git:    runspec.Git{Path: "/git/fixture.git", Branch: "main", Base: "refs/heads/main", Refresh: true, Commit: merge}})
	for n := 1; n <= 2; n++ {
		k.turnN(t, n)
		if seen := seenOf(t, k); !strings.Contains(seen, "marker merged") {
			t.Fatalf("turn %d saw %q", n, seen)
		}
	}
}
