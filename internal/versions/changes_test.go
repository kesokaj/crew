package versions_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/versions"
)

// remoteMerge merges a commit of the remote's own into the fixture's main, as Crew merges a remote main
// that went another way: a merge no ticket made, which brings a commit from outside.
func (k *buildKit) remoteMerge() (merge, commit string) {
	k.t.Helper()
	return k.remoteMergeOf("the remote's own commit")
}

// remoteMergeOf is remoteMerge with the remote's commit's subject given.
func (k *buildKit) remoteMergeOf(subject string) (merge, commit string) {
	k.t.Helper()
	work := k.t.TempDir()
	git := func(args ...string) string {
		k.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitstore.CleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("clone", "-q", k.git.Canonical("fixture"), ".")
	git("checkout", "-q", "-b", "remote")
	os.WriteFile(filepath.Join(work, "remote.txt"), []byte("r"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", subject)
	commit = git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "Merge the remote's main into main", "remote")
	merge = git("rev-parse", "HEAD")
	git("push", "-q", "origin", "main")
	return merge, commit
}

// What a release holds (16.2 step 6, since 0.54): the tickets whose merges are on Crew's project's main
// since the release before it, the newest release folder whose commit the repository holds and is an
// ancestor of its own; and the commits from outside: the operator's own, and those a merge no ticket
// made brought in. A ticket's branch commits are its own, not from outside. Each list is newest first,
// screened as a title is, and cut at its bound.
func TestWhatAReleaseHoldsIsItsTicketsAndTheCommitsFromOutside(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	merge, remote := k.remoteMerge()
	long := strings.Repeat("x", 130)
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET title = CASE number WHEN 901 THEN 'Cut the total`+"‮"+`' WHEN 905 THEN '`+long+`' ELSE 'Ticket ' || number END
		WHERE number BETWEEN 901 AND 905`); err != nil {
		t.Fatal(err)
	}
	ch, err := k.b.Changes(ctx, "fixture", "0.1.1", merge)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Note != "" || ch.Since == nil || ch.Since.Version != "0.1.0" || ch.Since.Commit != h.c0 || ch.Cut.Tickets || ch.Cut.Outside {
		t.Fatalf("%+v", ch)
	}
	var keys []string
	for _, tk := range ch.Tickets {
		keys = append(keys, tk.Key+" "+tk.Title)
	}
	if want := []string{"FIX-905 " + long[:120], "FIX-904 Ticket 904", "FIX-903 Ticket 903", "FIX-902 Ticket 902", "FIX-901 Cut the total"}; strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("tickets %q", keys)
	}
	if ch.Tickets[0].Commit != h.m4 {
		t.Fatalf("the newest ticket's commit %s, not %s", ch.Tickets[0].Commit, h.m4)
	}
	var outside []string
	for _, c := range ch.Outside {
		outside = append(outside, c.Commit+" "+c.Subject)
	}
	if want := []string{remote + " the remote's own commit", h.o2 + " another push", h.o1 + " the operator's own push"}; strings.Join(outside, "|") != strings.Join(want, "|") {
		t.Fatalf("outside %q", outside)
	}
	// The bound keeps the newest of each, and says so.
	defer versions.SetMaxChanges(2)()
	ch, _ = k.b.Changes(ctx, "fixture", "0.1.1", merge)
	if len(ch.Tickets) != 2 || ch.Tickets[0].Key != "FIX-905" || len(ch.Outside) != 2 || ch.Outside[0].Commit != remote || !ch.Cut.Tickets || !ch.Cut.Outside {
		t.Fatalf("cut: %+v", ch)
	}
	// At most maxMerges merges no ticket made are read for what they brought in; past that the list is
	// cut, though it has room (round 2 of the reviews of 95d116d).
	versions.SetMaxChanges(200)
	defer versions.SetMaxMerges(0)()
	ch, _ = k.b.Changes(ctx, "fixture", "0.1.1", merge)
	if len(ch.Outside) != 2 || ch.Outside[0].Commit != h.o2 || !ch.Cut.Outside || ch.Cut.Tickets {
		t.Fatalf("no merge read: %+v", ch)
	}
	// What cannot be read says why, with nothing listed.
	for project, commit := range map[string]string{"": merge, "fixture": strings.Repeat("a", 40)} {
		ch, err := k.b.Changes(ctx, project, "0.1.1", commit)
		if err != nil || ch.Note == "" || len(ch.Tickets) != 0 || len(ch.Outside) != 0 || ch.Since != nil {
			t.Fatalf("%q at %s: %+v %v", project, commit, ch, err)
		}
	}
}

// A commit from outside is screened as a title is (16.2 step 6): its subject, which anyone who pushes
// wrote, loses its control, invisible, and direction-changing characters and is cut at 120, before the
// view and the notes, whose line keeps the commit's hash after it (P5 of M16).
func TestACommitFromOutsideIsScreenedAsATitleIs(t *testing.T) {
	k := newBuildKit(t)
	k.history()
	subject := "Drop the cap\u202e\u200b" + strings.Repeat("y", 130)
	merge, remote := k.remoteMergeOf(subject)
	ch, err := k.b.Changes(context.Background(), "fixture", "0.1.1", merge)
	if err != nil || len(ch.Outside) == 0 || ch.Outside[0].Commit != remote {
		t.Fatalf("%+v %v", ch, err)
	}
	got := ch.Outside[0].Subject
	if want := ("Drop the cap" + strings.Repeat("y", 130))[:120]; got != want {
		t.Fatalf("the subject %q, not %q", got, want)
	}
}

// A server build's notes are written from what it holds (16.9, since 0.54): its tickets, then the
// commits from outside, each one screened line; the first is its title.
func TestABuildsNotesAreItsTicketsAndTheCommitsFromOutside(t *testing.T) {
	k := newBuildKit(t)
	h := k.history()
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET title = 'Ticket ' || number WHERE number BETWEEN 901 AND 905`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.b.Start(ctx, "fixture", "0.1.1", h.o2, versions.ByOperator); err != nil {
		t.Fatal(err)
	}
	k.b.Wait()
	notes, err := os.ReadFile(filepath.Join(k.rel, "0.1.1", release.NotesFile))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Crew 0.1.1: 5 tickets and 2 commits from outside\n\nSince 0.1.0 (" + h.c0[:12] + "):\n\nTickets:\n\n- FIX-905: Ticket 905\n- FIX-904: Ticket 904\n- FIX-903: Ticket 903\n" +
		"- FIX-902: Ticket 902\n- FIX-901: Ticket 901\n\nFrom outside:\n\n- another push (" + h.o2[:7] + ")\n- the operator's own push (" + h.o1[:7] + ")\n"
	if string(notes) != want {
		t.Fatalf("notes:\n%s\nwant:\n%s", notes, want)
	}
	// Its title is Crew's own words, not an agent's ticket title (security review of 95d116d).
	if title := k.s.Title("0.1.1"); title != "5 tickets and 2 commits from outside" {
		t.Fatalf("title %q", title)
	}
	// What a release with a folder holds does not change: it is read from git once, however often the
	// panel asks (reviews of 95d116d).
	counted := &countingGit{BuildGit: k.b.Git}
	k.b.Git = counted
	for i := 0; i < 3; i++ {
		if ch, err := k.b.Changes(ctx, "fixture", "0.1.1", h.o2); err != nil || len(ch.Tickets) != 5 {
			t.Fatalf("%+v %v", ch, err)
		}
	}
	if counted.lines != 1 {
		t.Fatalf("the line was read %d times", counted.lines)
	}
}

// countingGit counts the reads of main's line.
type countingGit struct {
	versions.BuildGit
	lines int
}

func (c *countingGit) FirstParentLog(ctx context.Context, name, from, to string, limit int) ([]gitstore.Commit, error) {
	c.lines++
	return c.BuildGit.FirstParentLog(ctx, name, from, to, limit)
}
