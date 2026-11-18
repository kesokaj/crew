package projterm_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

// A main the terminal pushed, whose index file of main cannot be read, past the bound of a read,
// reached through more links than Crew follows or by any failure of git, is held for the operator with
// every file it changes counted as code, so a main that repairs main can be confirmed (13.8, 13.11;
// since 1.05, where 1.04 did not take it). A diff that cannot be read is still not taken.
func TestAMainWhoseIndexFileIsPastTheBoundIsHeldWithEveryFileAsCode(t *testing.T) {
	ctx := context.Background()
	s, f := setup(t)
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	acc := gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}
	f.changed = []string{"README.md", "tools/plug/hook.sh"}
	codeOf := func() []string {
		var code []string
		if err := f.d.Server.QueryRow(ctx, `SELECT code FROM terminal_mains ORDER BY id DESC LIMIT 1`).Scan(&code); err != nil {
			t.Fatal(err)
		}
		return code
	}
	s.Pushed(acc, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	if len(f.held) != 1 || len(codeOf()) != 0 {
		t.Fatalf("within the bound: held %v, code %v", f.held, codeOf())
	}
	for i, past := range []error{gitstore.ErrOutputTooLarge, cli.ErrLinkWay, errors.New("git cat-file: signal: killed")} {
		f.fileErr = past
		s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ccccccc")
		if code := codeOf(); len(f.held) != 2+i || !slices.Equal(code, f.changed) {
			t.Errorf("%v: held %v, code %v", past, f.held, code)
		}
	}
	// An index file of main that names a folder through /proc is held the same way.
	f.fileErr, f.files = nil, map[string]string{".agents/rules.json": `{"entries": [{"path": "/proc/self/cwd/notes"}]}`}
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ccccccc")
	if code := codeOf(); len(f.held) != 5 || !slices.Equal(code, f.changed) {
		t.Errorf("an entry through /proc: held %v, code %v", f.held, code)
	}
	f.files, f.changedErr = nil, errors.New("no such commit")
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ddddddd")
	if h := history(t, f); len(f.held) != 5 || !strings.Contains(h[len(h)-1], `"notTaken": "ddddddd"`) ||
		!strings.Contains(h[len(h)-1], "what it changed could not be read") {
		t.Errorf("a diff that cannot be read: held %v, %q", f.held, h[len(h)-1])
	}
}
