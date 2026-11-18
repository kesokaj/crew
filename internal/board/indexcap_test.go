package board_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

// An index file of main past the bounds Crew reads one within, more than 1,024 entries, an entry longer
// than 4,096 bytes or more than 1 MiB, is an error for each guard that reads what it names, the merge's
// check, a pull request's push and a branch that moved on GitHub, never a file read whole or named in
// part, and so is one that is not JSON Crew reads exactly (13.8, since 1.07). At the bounds it names
// its folders as before.
func TestAnIndexFileOfMainPastItsBoundsIsAnErrorForTheCodeGuards(t *testing.T) {
	entries := func(n int) string {
		return `{"entries": [` + strings.TrimSuffix(strings.Repeat(`{"path": "tools"},`, n), ",") + `]}`
	}
	long := func(n int) string {
		return `{"entries": [{"path": "` + strings.Repeat("./", (n-5)/2) + "tools" + strings.Repeat("/", (n-5)%2) + `"}]}`
	}
	sized := func(n int) string {
		head := `{"entries": [{"path": "tools"}]`
		return head + strings.Repeat(" ", n-len(head)-1) + "}"
	}
	for name, c := range map[string]struct {
		file, body string
		err        error
	}{
		"1024 entries":             {file: ".agents/plugins.json", body: entries(1024)},
		"1025 entries":             {file: ".agents/plugins.json", body: entries(1025), err: cli.ErrIndexEntries},
		"an entry of 4096 bytes":   {file: ".agents/plugins.json", body: long(4096)},
		"an entry of 4097 bytes":   {file: ".agents/rules.json", body: long(4097), err: cli.ErrIndexEntries},
		"a file of 1 MiB":          {file: ".agents/plugins.json", body: sized(cli.MaxIndexBytes)},
		"a file of 1 MiB, and 1":   {file: ".agents/plugins.json", body: sized(cli.MaxIndexBytes + 1), err: gitstore.ErrIndexFileTooLarge},
		"rules of 1 MiB, and one":  {file: ".agents/rules.json", body: sized(cli.MaxIndexBytes + 1), err: gitstore.ErrIndexFileTooLarge},
		"a mistyped entry":         {file: ".agents/plugins.json", body: `{"entries": [{"path": "tools"}, {"path": 7}]}`, err: cli.ErrIndexParse},
		"a key in another case":    {file: ".agents/rules.json", body: `{"entries": [{"path": "tools"}], "Entries": []}`, err: cli.ErrIndexParse},
		"a key Crew does not read": {file: ".agents/rules.json", body: `{"entries": [{"path": "tools", "note": "x"}]}`, err: cli.ErrIndexParse},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.project("shop", "SF", []string{}...)
			mainWithLinks(t, e, p, map[string]string{c.file: c.body})
			e.add(p, ready("One"), board.OriginPlan)
			e.ok(p)
			e.reconcile(p)
			e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
			e.reconcile(p)
			tk := e.ticket(p, "SF-1")
			push := func(script func(dir string)) string {
				e.pushTree(p, *tk.Branch, script)
				head, _ := e.b.Head(e.ctx, p, tk)
				if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
					t.Fatalf("follows: %v %v", ok, err)
				}
				return head
			}
			fetched := push(func(dir string) { os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644) })
			head := push(func(dir string) {
				os.MkdirAll(filepath.Join(dir, "tools/p"), 0o755)
				os.WriteFile(filepath.Join(dir, "tools/p/hooks.json"), []byte("{}"), 0o644)
			})
			for guard, read := range map[string]func() ([]string, error){
				"the merge's check":     func() ([]string, error) { return e.b.CodeChanges(e.ctx, p, tk) },
				"a pull request's push": func() ([]string, error) { return board.CodeChangesAtForTest(e.ctx, e.git, p, head) },
				"a branch that moved on GitHub": func() ([]string, error) {
					return e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched)
				},
			} {
				files, err := read()
				if c.err != nil {
					if !errors.Is(err, c.err) {
						t.Errorf("%s: %v %v", guard, files, err)
					}
					continue
				}
				// At the bounds, the folder it names is code (or instructions, for the branch that moved).
				if err != nil || len(files) != 1 || files[0] != "tools/p/hooks.json" {
					t.Errorf("%s: %v %v", guard, files, err)
				}
			}
		})
	}
}

// The check of .agents/skills/VENDORED.md follows main's file through the store's reader of index files,
// and so reads it within the same 1 MiB: past it, the check is an error of its own text and runs again,
// never a pass (7.1, since 1.07).
func TestTheCheckOfVendoredMDOfMainPast1MiBIsAnError(t *testing.T) {
	rows := "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | 6654f6b | MIT |\n"
	for _, c := range []struct {
		size int
		over bool
	}{{cli.MaxIndexBytes, false}, {cli.MaxIndexBytes + 1, true}} {
		e := newEnv(t)
		p := e.running("shop", "SF")
		e.reconcile(p)
		e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{"**"}})
		e.reconcile(p)
		e.b.Checks = scripted()
		e.push(p, *e.ticket(p, "SF-1").Branch, map[string]string{".agents/skills/flows/SKILL.md": "# flows\n"})
		mainWithLinks(t, e, p, map[string]string{".agents/skills/VENDORED.md": rows + "\n" + strings.Repeat("x", c.size-len(rows)-2) + "\n"})
		tk := e.ticket(p, "SF-1")
		r, err := board.SkillsVendoredForTest(e.ctx, &board.CheckContext{Project: p, Ticket: tk, Git: e.git})
		if c.over != (err != nil && err.Error() == "main's .agents/skills/VENDORED.md is longer than 1048576 bytes, more than Crew reads") || (!c.over && err != nil) {
			t.Errorf("%d bytes: %+v %v", c.size, r, err)
		}
	}
}
