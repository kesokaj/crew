package httpapi_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
)

// taskBranch makes SF-1 of a new shop the ticket of a branch of n commits on main's first, the big-th
// with a message of 1 MiB (none at 0), and returns the commit of each number and that first commit.
func taskBranch(t *testing.T, n, big int) (*ticketKit, func(int) string, string) {
	t.Helper()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	work := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = gitstore.CleanEnv("GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@x",
			"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "clone", "-q", k.git.Quarantine.Path("shop"), ".")
	root := run("", "rev-list", "--max-parents=0", "origin/main")
	var stream strings.Builder
	for i := 1; i <= n; i++ {
		msg := fmt.Sprintf("c%04d", i)
		if i == big {
			msg += strings.Repeat("m", 1<<20)
		}
		fmt.Fprintf(&stream, "commit refs/heads/task/SF-1-one\nmark :%d\ncommitter agent <agent@x> 1700000000 +0000\ndata %d\n%s\n", i, len(msg), msg)
		if i == 1 {
			fmt.Fprintf(&stream, "from %s\n", root)
		} else {
			fmt.Fprintf(&stream, "from :%d\n", i-1)
		}
	}
	run(stream.String(), "fast-import", "--quiet")
	run("", "push", "-q", "origin", "task/SF-1-one")
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE tickets SET branch = 'task/SF-1-one' WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	return k, func(i int) string { return run("", "rev-parse", fmt.Sprintf("task/SF-1-one~%d", n-i)) }, root
}

// A ticket's Git part keeps at most 1,000 commits over its pushes and Other, the newest: a push read
// once the view is full is read for its ids alone, so a commit past the most is counted in omitted, not
// shown in Other as one no push brought, and a message in it that no history reads within its bound
// does not leave it out; a push that brought nothing is still a group (6.1, since 1.09).
func TestATicketsGitKeepsAThousandCommits(t *testing.T) {
	t.Parallel()
	// 1,300 commits on the branch in five recorded pushes, the last one of none; the 150th has a message
	// of 1 MiB, more than a history of the store's bound below reads.
	k, c, root := taskBranch(t, 1300, 150)
	k.b.Git.MaxOutput = 128 << 10
	ctx := context.Background()
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO ref_pushes (repo, who, ref, old, new) VALUES
			('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
			('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
			('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
			('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
			('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s')`, root, c(200), c(200), c(700), c(700), c(1200), c(1200), c(1300), c(1300), c(1300)),
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var g struct {
		Pushed bool
		Groups []struct {
			From, To string
			Commits  []struct{ ID, Subject string }
		}
		Other   []struct{ ID string }
		Omitted int
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g)
	var sizes []int
	for _, x := range g.Groups {
		sizes = append(sizes, len(x.Commits))
	}
	// Newest first: the last push's 100, the one before's 500, and the newest 400 of the second's; the
	// second's oldest 100 and the first push's 200, read for their ids, are past the most.
	if !g.Pushed || fmt.Sprint(sizes) != "[400 500 100 0]" || len(g.Other) != 0 || g.Omitted != 300 {
		t.Fatalf("groups %v, other %d, omitted %d", sizes, len(g.Other), g.Omitted)
	}
	if first := g.Groups[0].Commits[0].Subject; first != "c0301" {
		t.Fatalf("the oldest commit kept is %s", first)
	}
	if last := g.Groups[3]; last.From != c(1300) || last.To != c(1300) {
		t.Fatalf("the push that brought nothing: %+v", last)
	}
}

// Commits no push brought are kept newest first in Other, as many as the view has room for, the rest
// counted in omitted (6.1, since 1.09).
func TestATicketsGitKeepsTheNewestItHasNoPushFor(t *testing.T) {
	t.Parallel()
	k, c, root := taskBranch(t, 1100, 0)
	if _, err := k.d.Server.Exec(context.Background(), fmt.Sprintf(`INSERT INTO ref_pushes (repo, who, ref, old, new) VALUES
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s')`, root, c(400), c(400), c(900))); err != nil {
		t.Fatal(err)
	}
	var g struct {
		Other []struct {
			ID, Subject string
		}
		Omitted int
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g)
	// 900 kept of the pushes leave room for 100 of the 200 no push brought: the newest.
	if len(g.Other) != 100 || g.Omitted != 100 || g.Other[0].ID != c(1100) || g.Other[99].ID != c(1001) {
		t.Fatalf("other %d, omitted %d", len(g.Other), g.Omitted)
	}
}

// A push whose history is past the bound of a read is read for its ids, counted in omitted, and every
// older push after it too, so one view makes at most one read that fails and leaves no push out
// unsaid (6.1, since 1.09, the security review of 1.09's change, round two).
func TestATicketsGitReadsAPushPastTheBoundForItsIDs(t *testing.T) {
	t.Parallel()
	// The 300th of 1,100 commits has a message of 1 MiB, past eight times the store's bound below.
	k, c, root := taskBranch(t, 1100, 300)
	k.b.Git.MaxOutput = 128 << 10
	if _, err := k.d.Server.Exec(context.Background(), fmt.Sprintf(`INSERT INTO ref_pushes (repo, who, ref, old, new) VALUES
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s'),
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s')`, root, c(100), c(100), c(400), c(400), c(900))); err != nil {
		t.Fatal(err)
	}
	var g struct {
		Groups []struct {
			From    string
			Commits []struct{ ID string }
		}
		Other   []struct{ ID string }
		Omitted int
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g)
	// The newest push's 500 are shown; the one past the bound and the one before it are counted.
	if len(g.Groups) != 1 || g.Groups[0].From != c(400) || len(g.Groups[0].Commits) != 500 || g.Omitted != 400 || len(g.Other) != 200 {
		t.Fatalf("groups %d, omitted %d, other %d", len(g.Groups), g.Omitted, len(g.Other))
	}
}

// Repeated polls of a ticket's Git part while neither main, the branch, nor ref_pushes moved reuse the
// cached history and diff instead of running git log and git diff again; when a push row is added, the
// view reads the new history (register row 43).
func TestATicketsGitCachesRepeatedReadsUntilARefOrPushMoves(t *testing.T) {
	t.Parallel()
	k, c, root := taskBranch(t, 5, 0)
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, fmt.Sprintf(`INSERT INTO ref_pushes (repo, who, ref, old, new) VALUES
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s')`, root, c(3))); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "git")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nexec git \"$@\"\n", calls)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	k.b.Git.Git = wrapper
	var g1, g2, g3 struct {
		Groups []struct{ Commits []struct{ ID string } }
		Other  []struct{ ID string }
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g1)
	if len(g1.Groups) != 1 || len(g1.Groups[0].Commits) != 3 || len(g1.Other) != 2 {
		t.Fatalf("first read: %+v", g1)
	}
	os.Remove(calls)
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g2)
	b, _ := os.ReadFile(calls)
	if strings.Contains(string(b), "log ") || strings.Contains(string(b), "diff ") {
		t.Fatalf("cached read ran git log or diff:\n%s", b)
	}
	if len(g2.Groups) != 1 || len(g2.Groups[0].Commits) != 3 || len(g2.Other) != 2 {
		t.Fatalf("second read: %+v", g2)
	}
	if _, err := k.d.Server.Exec(ctx, fmt.Sprintf(`INSERT INTO ref_pushes (repo, who, ref, old, new) VALUES
		('shop', 'agent', 'refs/heads/task/SF-1-one', '%s', '%s')`, c(3), c(5))); err != nil {
		t.Fatal(err)
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g3)
	if len(g3.Groups) != 2 || len(g3.Other) != 0 {
		t.Fatalf("after new push: %+v", g3)
	}
}
