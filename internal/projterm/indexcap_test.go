package projterm_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// A main the terminal pushed whose index file of main names more than 1,024 folders, or one longer than
// 4,096 bytes, or that is not JSON Crew reads exactly, is held for the operator with every file it
// changes counted as code, as one that cannot be read is (13.8, 13.11; since 1.07). At the bounds it names its folders as before. The store's bound
// of 1 MiB comes to the terminal as an error of its reader, which the test of the read's bound holds.
func TestAMainWhoseIndexFileIsPastItsEntriesIsHeldWithEveryFileAsCode(t *testing.T) {
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
	entries := func(n int) string {
		return `{"entries": [` + strings.TrimSuffix(strings.Repeat(`{"path": "tools"},`, n), ",") + `]}`
	}
	long := func(n int) string {
		return `{"entries": [{"path": "` + strings.Repeat("./", (n-5)/2) + "tools" + strings.Repeat("/", (n-5)%2) + `"}]}`
	}
	for i, c := range []struct {
		name, body string
		held       bool
	}{
		{"1024 entries", entries(1024), false},
		{"1025 entries", entries(1025), true},
		{"an entry of 4096 bytes", long(4096), false},
		{"an entry of 4097 bytes", long(4097), true},
		{"a mistyped entry", `{"entries": [{"path": "tools"}, {"path": 7}]}`, true},
		{"a key in another case", `{"entries": [{"path": "tools"}], "Entries": []}`, true},
		{"a key Crew does not read", `{"entries": [{"path": "tools", "note": "x"}]}`, true},
	} {
		f.files = map[string]string{".agents/plugins.json": c.body}
		s.Pushed(acc, "refs/heads/main", "aaaaaaa", "bbbbbbb")
		want := []string{"tools/plug/hook.sh"}
		if c.held {
			want = f.changed
		}
		if code := codeOf(); len(f.held) != i+1 || !slices.Equal(code, want) {
			t.Errorf("%s: held %v, code %v", c.name, f.held, code)
		}
	}
}
