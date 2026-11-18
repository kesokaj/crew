package gitstore_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/gitstore"
)

// commitAs writes, in the repository at dir, a commit of parent's tree on parent by author with the
// message, and returns it.
func commitAs(t *testing.T, dir, parent, author, message string) string {
	t.Helper()
	msg := filepath.Join(t.TempDir(), "message")
	os.WriteFile(msg, []byte(message), 0o644)
	cmd := exec.Command("git", "commit-tree", parent+"^{tree}", "-p", parent, "-F", msg)
	cmd.Dir = dir
	cmd.Env = gitstore.CleanEnv("GIT_AUTHOR_NAME="+author, "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit-tree: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// Every history keeps a commit's subject to MaxSubject, its body to MaxBody and its author to its cut,
// each cut one ending in "…" on a whole rune, and says the commit was cut; a commit within them is read
// whole and is not cut (6.1, since 1.09).
func TestAHistorysFieldsAreCutAndMarked(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	canon, main := s.Canonical("shop"), s.Main(ctx, "shop")
	// One byte before the runes, so the cut falls inside one.
	subject := "x" + strings.Repeat("é", gitstore.MaxSubject)
	body := strings.Repeat("body ", gitstore.MaxBody)
	author := strings.Repeat("n", 2<<10)
	long := commitAs(t, canon, main, author, subject+"\n\n"+body+"\n")
	short := commitAs(t, canon, long, "a", "short subject\n\nshort body\n")
	git(t, canon, "push", "-q", s.Quarantine.Path("shop"), short+":refs/heads/task/cut")
	reads := map[string]func() ([]gitstore.Commit, error){
		"Log":            func() ([]gitstore.Commit, error) { return s.Log(ctx, "shop", short, "", 10) },
		"LogRange":       func() ([]gitstore.Commit, error) { return s.LogRange(ctx, "shop", main, short) },
		"FirstParentLog": func() ([]gitstore.Commit, error) { return s.FirstParentLog(ctx, "shop", main, short, 10) },
		"LogAll":         func() ([]gitstore.Commit, error) { return s.LogAll(ctx, "shop", 10) },
		"Show": func() ([]gitstore.Commit, error) {
			a, _, err := s.Show(ctx, "shop", long)
			if err != nil {
				return nil, err
			}
			b, _, err := s.Show(ctx, "shop", short)
			return []gitstore.Commit{a, b}, err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			cs, err := read()
			if err != nil {
				t.Fatal(err)
			}
			by := map[string]gitstore.Commit{}
			for _, c := range cs {
				by[c.ID] = c
			}
			l, sh := by[long], by[short]
			if !l.Cut || !strings.HasSuffix(l.Subject, "…") || len(l.Subject) > gitstore.MaxSubject+len("…") || !utf8.ValidString(l.Subject) ||
				!strings.HasPrefix(subject, strings.TrimSuffix(l.Subject, "…")) || len(l.Subject) < gitstore.MaxSubject-utf8.UTFMax {
				t.Errorf("the long subject: %v %d %q", l.Cut, len(l.Subject), l.Subject[max(0, len(l.Subject)-12):])
			}
			if !strings.HasSuffix(l.Body, "…") || len(l.Body) > gitstore.MaxBody+len("…") || len(l.Body) < gitstore.MaxBody-8 ||
				!strings.HasPrefix(body, strings.TrimSuffix(l.Body, "…")) {
				t.Errorf("the long body: %d", len(l.Body))
			}
			if !strings.HasSuffix(l.Author, "…") || len(l.Author) > 1<<10+len("…") || len(l.Author) < 1<<10 {
				t.Errorf("the long author: %d", len(l.Author))
			}
			if l.Email != "a@x" || len(l.Parents) != 1 || l.Parents[0] != main {
				t.Errorf("the long commit's other fields: %+v", l.Parents)
			}
			if sh.Cut || sh.Subject != "short subject" || sh.Body != "short body" || sh.Author != "a" || len(sh.Parents) != 1 || sh.Parents[0] != long {
				t.Errorf("the short commit: %+v", sh)
			}
		})
	}
}

// A commit's message one byte past the bound of a read, which a history read whole before 1.09 and
// then refused, is read cut, and the read holds no more than its cuts: it allocates a small part of
// the message, not the message (6.1, since 1.09).
func TestAMessagePastTheBoundIsReadCutAndNotHeld(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	canon := s.Canonical("shop")
	long := commitAs(t, canon, s.Main(ctx, "shop"), "a", strings.Repeat("a", gitstore.MaxOutput+1))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	cs, err := s.LogRange(ctx, "shop", "", long)
	runtime.ReadMemStats(&after)
	if err != nil || len(cs) != 2 || cs[1].ID != long {
		t.Fatalf("%v %d", err, len(cs))
	}
	if c := cs[1]; !c.Cut || c.Subject != strings.Repeat("a", gitstore.MaxSubject)+"…" || c.Body != "" {
		t.Fatalf("%v %d %q", c.Cut, len(c.Subject), c.Body)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Fatalf("the read allocated %d bytes", n)
	}
}

// The message of the last change of a path, which a page and a skill read for their ticket's trailer,
// is cut at MaxMessage, the most a push brings, so the trailer of every message a push brings is
// found, and one past it ends in "…" (6.1, since 1.09).
func TestTheLastChangesMessageIsCutAtThePushBound(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	change := func(message string) string {
		t.Helper()
		msg := filepath.Join(t.TempDir(), "message")
		os.WriteFile(msg, []byte(message), 0o644)
		os.WriteFile(filepath.Join(work, "page.md"), []byte(message[:16]), 0o644)
		git(t, work, "add", "-A")
		git(t, work, "commit", "-q", "-F", msg)
		git(t, work, "push", "-q", "origin", "main")
		if err := s.GiveBackMain(ctx, "shop"); err != nil {
			t.Fatal(err)
		}
		_, _, m, err := s.LastChange(ctx, "shop", s.Main(ctx, "shop"), "page.md")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	trailer := "\n\nTicket: SHOP-7\n"
	within := strings.Repeat("a line of a long message\n", 200<<10/25)
	if m := change(within + trailer); !strings.HasSuffix(m, "Ticket: SHOP-7") || len(m) < len(within) {
		t.Fatalf("a message within the bound: %d %q", len(m), m[max(0, len(m)-20):])
	}
	past := strings.Repeat("b line of a long message\n", gitstore.MaxMessage/25+4)
	m := change(past + trailer)
	if !strings.HasSuffix(m, "…") || len(m) > gitstore.MaxMessage+len("…") || len(m) < gitstore.MaxMessage-len(trailer) || strings.Contains(m, "SHOP-7") {
		t.Fatalf("a message past the bound: %d %q", len(m), m[max(0, len(m)-20):])
	}
}

// A commit of more parents than their cut keeps the whole ones in their order, the first included,
// and says it was cut; no id is left cut short (6.1, since 1.09).
func TestAHistoryKeepsTheWholeParentsOfItsCut(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	canon, main := s.Canonical("shop"), s.Main(ctx, "shop")
	var parents []string
	args := []string{"commit-tree", main + "^{tree}"}
	for i := range 120 {
		c := commitAs(t, canon, main, "a", "parent "+strings.Repeat("p", i)+"\n")
		parents = append(parents, c)
		args = append(args, "-p", c)
	}
	octopus := git(t, canon, append(args, "-m", "octopus")...)
	cs, err := s.Log(ctx, "shop", octopus, "", 1)
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %d", err, len(cs))
	}
	got := cs[0].Parents
	if !cs[0].Cut || len(got) < 90 || len(got) >= len(parents) {
		t.Fatalf("cut %v, %d parents", cs[0].Cut, len(got))
	}
	for i, p := range got {
		if p != parents[i] {
			t.Fatalf("parent %d is %q, not %s", i, p, parents[i])
		}
	}
}

// A history stops at eight times the bound of a read of git's output, past which it is an error and
// never a list cut short, also when what it keeps is far less (6.1, since 1.09).
func TestAHistoryStopsPastItsRawBound(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	s.MaxOutput = 1 << 20
	canon, main := s.Canonical("shop"), s.Main(ctx, "shop")
	within := commitAs(t, canon, main, "a", strings.Repeat("w", 7<<20))
	if cs, err := s.LogRange(ctx, "shop", main, within); err != nil || len(cs) != 1 || !cs[0].Cut {
		t.Fatalf("within eight times the bound: %v %d", err, len(cs))
	}
	past := commitAs(t, canon, main, "a", strings.Repeat("p", 9<<20))
	if _, err := s.LogRange(ctx, "shop", main, past); !errors.Is(err, gitstore.ErrOutputTooLarge) {
		t.Fatalf("past eight times the bound: %v", err)
	}
}

// CommitIDs are the ids of what LogRange lists, newest first and at most 500 of them, also of a
// commit whose message no history keeps whole.
func TestCommitIDsAreTheIDsOfARange(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	canon, main := s.Canonical("shop"), s.Main(ctx, "shop")
	a := commitAs(t, canon, main, "a", strings.Repeat("a", 2<<20))
	b := commitAs(t, canon, a, "a", "b\n")
	ids, err := s.CommitIDs(ctx, "shop", main, b)
	if err != nil || strings.Join(ids, " ") != b+" "+a {
		t.Fatalf("%v %q", err, ids)
	}
	tip := b
	for range 510 {
		tip = commitAs(t, canon, tip, "a", "more\n")
	}
	if ids, err := s.CommitIDs(ctx, "shop", main, tip); err != nil || len(ids) != 500 || ids[0] != tip {
		t.Fatalf("a range of 512: %v %d", err, len(ids))
	}
	if _, err := s.CommitIDs(ctx, "shop", "main", b); !errors.Is(err, gitstore.ErrNoRev) {
		t.Fatalf("a range by a name: %v", err)
	}
}
