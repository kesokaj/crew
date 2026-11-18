package gitendpoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A push whose commits or tip are more than MaxCommitBytes, their headers and their message, is
// refused before the server's program or a history reads them, an earlier commit of the push too, and one at the bound is
// taken (6.1, since 1.09): a message of hundreds of megabytes packs into a few hundred kilobytes.
func TestAPushOfACommitPastItsBoundIsRefused(t *testing.T) {
	e, srv := endpoint(t)
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "a"); err != nil {
		t.Fatal(out)
	}
	a := filepath.Join(dir, "a")
	before := e.Ref(ctx, "fixture", "refs/heads/task/1-a")
	run := func(args ...string) string {
		t.Helper()
		out, err := git(t, a, "", args...)
		if err != nil {
			t.Fatalf("git %v: %s", args[0], out)
		}
		return strings.TrimSpace(out)
	}
	// commit is a commit on parent of size bytes in all, its message filling what its headers leave.
	commit := func(parent string, size int) string {
		t.Helper()
		msg := filepath.Join(t.TempDir(), "message")
		write := func(n int) string {
			os.WriteFile(msg, []byte(strings.Repeat("m", n)), 0o644)
			return run("commit-tree", before+"^{tree}", "-p", parent, "-F", msg)
		}
		var headers int
		fmt.Sscan(run("cat-file", "-s", write(1)), &headers)
		c := write(size - headers + 1)
		if got := run("cat-file", "-s", c); got != fmt.Sprint(size) {
			t.Fatalf("a commit of %s bytes, not %d", got, size)
		}
		return c
	}
	past := commit(before, MaxCommitBytes+1)
	for name, c := range map[string]string{
		"past the bound": past,
		"an earlier one": commit(past, 300),
	} {
		out, err := git(t, a, "token-a", "push", "-q", "origin", c+":refs/heads/task/1-a")
		if says := "the commit " + past + " of refs/heads/task/1-a is 1048577 bytes, more than 1048576 that Crew takes"; err == nil || !strings.Contains(out, says) {
			t.Errorf("%s: %v %s", name, err, out)
		}
		if e.Ref(ctx, "fixture", "refs/heads/task/1-a") != before {
			t.Fatalf("%s landed", name)
		}
	}
	// A commit in another encoding than UTF-8 is refused, one that says UTF-8 is taken: git would print
	// the other up to three times as long as the hook measured.
	encoded := func(enc string) string {
		t.Helper()
		out, err := git(t, a, "", "-c", "i18n.commitEncoding="+enc, "commit-tree", before+"^{tree}", "-p", before, "-m", "ｱｲｳ")
		if err != nil {
			t.Fatal(out)
		}
		return strings.TrimSpace(out)
	}
	sjis := encoded("Shift_JIS")
	if out, err := git(t, a, "token-a", "push", "-q", "origin", sjis+":refs/heads/task/1-a"); err == nil || !strings.Contains(out, "the commit "+sjis+" of refs/heads/task/1-a is not in UTF-8") {
		t.Errorf("a commit in Shift_JIS: %v %s", err, out)
	}
	// git writes no header for UTF-8: this one is written by hand.
	tree := run("rev-parse", before+"^{tree}")
	hash := exec.Command("git", "hash-object", "-w", "-t", "commit", "--stdin")
	hash.Dir = a
	hash.Env = CleanEnv()
	hash.Stdin = strings.NewReader("tree " + tree + "\nparent " + before + "\nauthor t <t@t> 1700000000 +0000\ncommitter t <t@t> 1700000000 +0000\nencoding UTF-8\n\nutf-8\n")
	out, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	utf8 := strings.TrimSpace(string(out))
	if out, err := git(t, a, "token-a", "push", "-q", "origin", utf8+":refs/heads/task/1-a"); err != nil {
		t.Errorf("a commit that says UTF-8: %s", out)
	}
	at := commit(utf8, MaxCommitBytes)
	if out, err := git(t, a, "token-a", "push", "-q", "origin", at+":refs/heads/task/1-a"); err != nil {
		t.Fatalf("a commit at the bound: %s", out)
	}
	if e.Ref(ctx, "fixture", "refs/heads/task/1-a") != at {
		t.Fatal("a commit at the bound did not land")
	}
}

// The hook's check of a commit's size refuses the push when git gives no size, gives fewer than it
// was asked for, or the bound is not set: a check that errors refuses (BUG-012). It is run here as the
// hook runs it, with a git in front of the real one. The tip is checked too when the push brings no
// commit, as a ref set to one the repository holds; an awk that fails refuses; and a tip that is a tag
// of a commit, which a ref outside refs/heads/ could hold, is refused as no commit.
func TestACommitsSizeThatCannotBeReadRefuses(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	env := CleanEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "one"}} {
		cmd := exec.Command(real, args...)
		cmd.Dir, cmd.Env = work, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	rev := exec.Command(real, "-C", work, "rev-parse", "HEAD")
	rev.Env = env
	head, err := rev.Output()
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(t.TempDir(), "pre-receive")
	os.WriteFile(hook, []byte(preReceive), 0o755)
	realAwk, err := exec.LookPath("awk")
	if err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{
		"the real git":                   "",
		"no size":                        `[ "$1" = cat-file ] && [ "$2" != -t ] && exit 1`,
		"one fewer":                      `[ "$1" = cat-file ] && [ "$2" != -t ] && { "` + real + `" "$@" | sed '$d'; exit 0; }`,
		"no number of size":              `[ "$1" = cat-file ] && [ "$2" != -t ] && { "` + real + `" "$@" | sed 's/ [0-9]*$/ x/'; exit 0; }`,
		"an awk that fails":              "",
		"no encoding":                    `case "$*" in *%e*) exit 1 ;; esac`,
		"an awk of encodings that fails": "",
	}
	bound := (&Endpoint{}).treeBoundEnv()
	for name, fake := range fakes {
		for _, unset := range []bool{false, true} {
			bin := t.TempDir()
			os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+fake+"\nexec "+real+" \"$@\"\n"), 0o755)
			if name == "an awk that fails" {
				// Only the check of the sizes, which is the one awk given max.
				os.WriteFile(filepath.Join(bin, "awk"), []byte("#!/bin/sh\ncase \"$*\" in *max=*) exit 2 ;; esac\nexec "+realAwk+" \"$@\"\n"), 0o755)
			}
			if name == "an awk of encodings that fails" {
				os.WriteFile(filepath.Join(bin, "awk"), []byte("#!/bin/sh\ncase \"$*\" in *tolower*) exit 2 ;; esac\nexec "+realAwk+" \"$@\"\n"), 0o755)
			}
			cmd := exec.Command(hook, "crew-trees")
			cmd.Dir = work
			cmd.Env = append(append(env, "GIT_DIR="+filepath.Join(work, ".git"), "PATH="+bin+":"+os.Getenv("PATH")), bound...)
			if unset {
				cmd.Env = append(cmd.Env, "CREW_COMMIT_BYTES=")
			}
			cmd.Stdin = strings.NewReader(strings.Repeat("0", 40) + " " + strings.TrimSpace(string(head)) + " refs/heads/task/1-a\n")
			out, err := cmd.CombinedOutput()
			ok := name == "the real git" && !unset
			if ok != (err == nil) || (!ok && !strings.Contains(string(out), "the commits of refs/heads/task/1-a cannot be read")) {
				t.Errorf("%s, bound unset %v: %v %s", name, unset, err, out)
			}
		}
	}
	cmd := exec.Command(hook, "crew-trees")
	cmd.Dir = work
	cmd.Env = append(append(env, "GIT_DIR="+filepath.Join(work, ".git")), append(bound, "CREW_COMMIT_BYTES=10")...)
	cmd.Stdin = strings.NewReader(strings.Repeat("0", 40) + " " + strings.TrimSpace(string(head)) + " refs/heads/task/1-a\n")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "of refs/heads/task/1-a is ") || !strings.Contains(string(out), "more than 10 that Crew takes") {
		t.Errorf("a tip past the bound: %v %s", err, out)
	}
	tag := exec.Command(real, "tag", "-a", "-m", "a tag", "v1")
	tag.Dir, tag.Env = work, env
	if out, err := tag.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %s", out)
	}
	obj, err := exec.Command(real, "-C", work, "rev-parse", "refs/tags/v1").Output()
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(hook, "crew-trees")
	cmd.Dir = work
	cmd.Env = append(append(env, "GIT_DIR="+filepath.Join(work, ".git")), bound...)
	cmd.Stdin = strings.NewReader(strings.Repeat("0", 40) + " " + strings.TrimSpace(string(obj)) + " refs/crew/wip/run-a\n")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "refs/crew/wip/run-a is not a commit Crew can read") {
		t.Errorf("a tag as the tip: %v %s", err, out)
	}
}
