package httpapi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
)

// The paths the reviews of M10 fixed with no test of their own (the M10 handoff's named test debt).

// A fetch of the remote's main that times out holds up nothing: it ends at its time, says so, is not
// tried on every pass, and Crew's merges go on (review of M10, finding 2).
func TestAFetchThatTimesOutHoldsUpNothing(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	_, _ = k.withRemote()
	dir := t.TempDir()
	hang := filepath.Join(dir, "ssh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	k.b.GitHub = &fakeGitHub{root: filepath.Join(dir, "github"), ssh: hang}
	k.b.PushTimeout = 300 * time.Millisecond
	stop := k.run()
	k.until("SF-1 Done while the remote hangs", func() bool { return k.stageOf("SF-1") == "done" })
	stop()
	var detail string
	k.d.Server.QueryRow(context.Background(), `SELECT coalesce(fetch_detail, '') FROM projects WHERE name = 'shop'`).Scan(&detail)
	if detail != "the remote did not answer in time" {
		t.Fatalf("the fetch's detail: %q", detail)
	}
}

// Only GitHub's own address of a pull request is kept, so the page never links elsewhere (security
// review of M10).
func TestAPullRequestsAddressIsKeptOnlyWhenItIsGitHubs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		url  string
		kept bool
	}{{"https://github.com/op/shop/pull/7", true}, {"https://evil.example/op/shop/pull/7", false}, {"javascript:alert(1)", false}} {
		k := newTicketKit(t)
		k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
			if kind == "review" {
				k.tool(spec.Run, "write_review", reviewed(p, map[string]any{"verdict": "approve", "text": "It does what " + key + " asks."}))
				return true
			}
			return false
		})
		k.shop([]string{"review"}, nil, "one")
		pulls, _ := k.withRemote()
		pulls.url = c.url
		stop := k.run()
		k.until("the pull request opened", func() bool { return k.count(`SELECT count(*) FROM pull_requests WHERE number IS NOT NULL`) == 1 })
		stop()
		var url string
		k.d.Server.QueryRow(context.Background(), `SELECT coalesce(url, '') FROM pull_requests`).Scan(&url)
		if (url == c.url) != c.kept || (!c.kept && url != "") {
			t.Errorf("%s: kept %q", c.url, url)
		}
	}
}

// A remote main that failed its check, or did not join, gets its ticket on a later pass when it
// could not be made then (round 2 of M10, finding 6).
func TestAMissingTicketOfARemoteMainIsMadeLater(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	k.withRemote()
	ctx := context.Background()
	main := k.git.Main(ctx, "shop")
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET hold = true`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET fetched_at = now() WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO remote_mains (project, remote, base, kind, commit, state, results)
		SELECT id, $1, $1, 'ahead', $1, 'failed', '[{"name": "make test", "passed": false, "output": "FAIL"}]' FROM projects WHERE name = 'shop'`, main); err != nil {
		t.Fatal(err)
	}
	stop := k.run()
	k.until("the missing ticket", func() bool {
		return k.count(`SELECT count(*) FROM remote_mains m JOIN tickets t ON t.id = m.ticket WHERE t.title = 'Fix main from the remote: its tests fail'`) == 1
	})
	stop()
}

// A head that waits for the operator cancels the branch's waiting pushes, and the pull request no
// longer says its branch is pushed, so the head is pushed again once allowed (round 2 of M10, finding 4).
func TestAHeldHeadCancelsAPushAndClearsPushed(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	k.withRemote()
	ctx := context.Background()
	proj, err := k.b.Project(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	k.pushFromRoot("task/SF-1-one", map[string]string{"1.txt": "one"})
	head := gitOut(t, k.git.Quarantine.Path("shop"), "rev-parse", "refs/heads/task/SF-1-one")
	for _, q := range []string{
		`UPDATE tickets SET branch = 'task/SF-1-one' WHERE number = 1`,
		`INSERT INTO pull_requests (ticket, project, repo, branch, state, pushed) SELECT id, project, 'op/shop', 'task/SF-1-one', 'open', '` + head + `' FROM tickets WHERE number = 1`,
		`INSERT INTO pushes (project, remote, refs, who, source, ticket, commit) SELECT project, 'git@github.com:op/shop.git', '{refs/heads/task/SF-1-one}', 'backend', 'quarantine', id, '` + head + `' FROM tickets WHERE number = 1`,
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	k.pushFromRoot("task/SF-1-one", map[string]string{".github/workflows/ci.yml": "on: push\n"})
	if err := k.b.SyncPulls(ctx, proj.ID); err != nil {
		t.Fatal(err)
	}
	if n := k.count(`SELECT count(*) FROM pushes WHERE detail LIKE 'held back%'`); n != 1 {
		t.Fatalf("%d waiting pushes cancelled, want 1", n)
	}
	if n := k.count(`SELECT count(*) FROM pull_requests WHERE held IS NOT NULL AND pushed IS NULL`); n != 1 {
		t.Fatal("the held pull request still says its branch is pushed")
	}
}

// With the gh login run out (21, row 6), no pull request is made, and the ticket merges into main with
// a merge commit; main still goes to the remote.
func TestWithGhSignedOutATicketMergesWithAMergeCommit(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one")
	pulls, remote := k.withRemote()
	pulls.mu.Lock()
	pulls.signedIn = false
	pulls.mu.Unlock()
	stop := k.run()
	k.until("SF-1 Done, and main on the remote", func() bool {
		return k.stageOf("SF-1") == "done" && gitOut(t, remote, "rev-parse", "main") == k.git.Main(context.Background(), "shop")
	})
	stop()
	if n := k.count(`SELECT count(*) FROM pull_requests`); n != 0 {
		t.Fatalf("%d pull requests with gh signed out", n)
	}
	if parents := gitOut(t, remote, "log", "-1", "--format=%P", "main"); len(strings.Fields(parents)) != 2 {
		t.Fatalf("main's newest commit is no merge commit: parents %q", parents)
	}
}

// When a ticket with a held workflow leaves In progress before another ticket (`created_at` earlier)
// and opens its pull request on GitHub only after the operator allows it (`opened_at` and `number`
// newer), PullRequests and GET /api/projects/{p}/git still order the later-opened pull request first.
func TestAPullRequestHeldAndOpenedLaterSortsNewestFirst(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.builder(nil)
	k.shop([]string{}, nil, "one", "two")
	k.withRemote()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO pull_requests (ticket, project, repo, branch, number, state, created_at, opened_at)
			SELECT id, project, 'op/shop', 'task/SF-2-two', 8, 'merged', now() - interval '2 minutes', now() - interval '10 seconds'
			FROM tickets WHERE number = 2`,
		`INSERT INTO pull_requests (ticket, project, repo, branch, number, state, created_at, opened_at)
			SELECT id, project, 'op/shop', 'task/SF-1-one', 7, 'merged', now() - interval '1 minute', now() - interval '50 seconds'
			FROM tickets WHERE number = 1`,
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var git struct {
		UsesPulls bool `json:"usesPulls"`
		Pulls     []struct {
			Number int
			Ticket string
			State  string
		}
	}
	k.call("GET", "/api/projects/shop/git", "", 200, &git)
	if !git.UsesPulls || len(git.Pulls) != 2 || git.Pulls[0].Ticket != "SF-2" || git.Pulls[0].Number != 8 || git.Pulls[1].Ticket != "SF-1" || git.Pulls[1].Number != 7 {
		t.Fatalf("the Git screen's pull requests: %+v", git)
	}
}
