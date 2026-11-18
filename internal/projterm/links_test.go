package projterm_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitendpoint"
)

// A main the terminal pushed is held with the files it changes that are code: also the file a link of
// main's index file leads to, and the folders that file names, as agy reads them (13.8, 13.11; since
// 1.05).
func TestAMainThatChangesWhereALinkOfAnIndexFileLeadsIsHeldWithIt(t *testing.T) {
	ctx := context.Background()
	s, f := setup(t)
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	acc := gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}
	f.files = map[string]string{".agents/plugins.json": `{"entries": [{"path": "tools/plug"}]}`}
	f.ways = map[string]cli.Way{".agents/plugins.json": {File: "cfg/p.json", Links: []string{".agents/plugins.json"}, End: "cfg/p.json"}}
	f.changed = []string{"README.md", "cfg/p.json", "tools/plug/hook.sh"}
	s.Pushed(acc, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	var code []string
	if err := f.d.Server.QueryRow(ctx, `SELECT code FROM terminal_mains ORDER BY id DESC LIMIT 1`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if len(f.held) != 1 || !slices.Equal(code, []string{"cfg/p.json", "tools/plug/hook.sh"}) {
		t.Errorf("held %v, code %v", f.held, code)
	}
}

func (f *fakes) Linked(ctx context.Context, name string, patterns []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	return cli.Linked(f.links, patterns, func(p string) (cli.Way, error) { return f.ways[p], nil })
}

// A main the terminal pushed is held with the files it changes where a link of main that stands for a
// folder of code leads, as agy reads them; a link that cannot be followed holds it with every file it
// changes counted as code (13.8, 13.11; since 1.06).
func TestAMainThatChangesWhereAFolderLinkOfMainLeadsIsHeldWithIt(t *testing.T) {
	ctx := context.Background()
	s, f := setup(t)
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	acc := gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}
	f.links = []string{".agents/skills"}
	f.ways = map[string]cli.Way{".agents/skills": {Links: []string{".agents/skills"}, End: "shared/skills"}}
	f.changed = []string{"README.md", "shared/skills/flows/SKILL.md"}
	code := func() []string {
		t.Helper()
		var code []string
		if err := f.d.Server.QueryRow(ctx, `SELECT code FROM terminal_mains ORDER BY id DESC LIMIT 1`).Scan(&code); err != nil {
			t.Fatal(err)
		}
		return code
	}
	s.Pushed(acc, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	if got := code(); len(f.held) != 1 || !slices.Equal(got, []string{"shared/skills/flows/SKILL.md"}) {
		t.Fatalf("held %v, code %v", f.held, got)
	}
	f.linkErr = errors.New("git ls-tree: exit status 128")
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ccccccc")
	if got := code(); len(f.held) != 2 || !slices.Equal(got, f.changed) {
		t.Fatalf("held %v, code %v", f.held, got)
	}
}
