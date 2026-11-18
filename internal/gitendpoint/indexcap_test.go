package gitendpoint

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An index file of the push, of its base or of main as it is now that names more than 1,024 folders, or
// one longer than 4,096 bytes, refuses the push with a fixed text, before its links are matched against
// what it names (13.8, since 1.07); at the bound what it names instructs, as before.
func TestAPushWithAnIndexFilePastItsEntriesIsRefused(t *testing.T) {
	entries := func(n int) string {
		return `{"entries": [` + strings.TrimSuffix(strings.Repeat(`{"path": "tools"},`, n), ",") + `]}`
	}
	// An entry of n bytes that names tools/ ("./" and a "/" at its end are cleaned away).
	long := func(n int) string {
		return `{"entries": [{"path": "` + strings.Repeat("./", (n-5)/2) + "tools" + strings.Repeat("/", (n-5)%2) + `"}]}`
	}
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/"}}
	for name, c := range map[string]struct {
		pushed, main string
		refused      bool
	}{
		"1024 entries pushed":                {pushed: entries(1024)},
		"1025 entries pushed":                {pushed: entries(1025), refused: true},
		"an entry of 4096 bytes pushed":      {pushed: long(4096)},
		"an entry of 4097 bytes pushed":      {pushed: long(4097), refused: true},
		"1025 entries on main as it is now":  {main: entries(1025), refused: true},
		"an entry of 4097 bytes on main now": {main: long(4097), refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			work, commit := linkRepo(t, map[string]string{"README.md": "x\n"})
			files := map[string]string{"tools/a.sh": "x\n"}
			if c.pushed != "" {
				files[".agents/rules.json"] = c.pushed
			}
			head := commit(files)
			if c.main != "" {
				// main moves on after the branch left it.
				git(t, work, "", "checkout", "-q", "main")
				commit(map[string]string{".agents/rules.json": c.main})
				git(t, work, "", "checkout", "-q", "task/1-a")
			}
			refused, err := testPush(context.Background(), rule, head, within)
			if c.refused {
				if !errors.Is(err, errIndexEntries) || !strings.Contains(err.Error(), "names more than 1024 folders, or a folder longer than 4096 bytes") {
					t.Fatalf("%v %v", refused, err)
				}
				return
			}
			// Within the bound, the folder it names instructs agents, and its file is refused for it.
			if err != nil || len(refused) != 1 || !strings.Contains(refused[0], "tools/a.sh instructs agents") {
				t.Fatalf("%v %v", refused, err)
			}
		})
	}
}

// An index file of the push or of main as it is now that is not JSON Crew reads exactly refuses the push
// with a fixed text, never names nothing: agy's reader may take from it what Go's does not (13.8, since
// 1.07, after its security review).
func TestAPushWithAnIndexFileCrewCannotReadExactlyIsRefused(t *testing.T) {
	bad := `{"entries":[{"path":"tools"},{"path":7}]}`
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/"}}
	for name, c := range map[string]struct{ pushed, main string }{
		"pushed":                                  {pushed: bad},
		"on main as it is now":                    {main: `{"entries":[{"path":"tools"}],"Entries":[]}`},
		"a path with white space, pushed":         {pushed: `{"entries":[{"path":" tools"}]}`},
		"a key Crew does not read in an entry":    {pushed: `{"entries":[{"path":"tools","note":"x"}]}`},
		"a key Crew does not read at the top":     {pushed: `{"entries":[{"path":"tools"}],"plugins":[]}`},
		"a key Crew does not read on main as now": {main: `{"entries":[{"path":"tools","note":"x"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			work, commit := linkRepo(t, map[string]string{"README.md": "x\n"})
			files := map[string]string{"tools/a.sh": "x\n"}
			if c.pushed != "" {
				files[".agents/plugins.json"] = c.pushed
			}
			head := commit(files)
			if c.main != "" {
				git(t, work, "", "checkout", "-q", "main")
				commit(map[string]string{".agents/plugins.json": c.main})
				git(t, work, "", "checkout", "-q", "task/1-a")
			}
			refused, err := testPush(context.Background(), rule, head, within)
			if !errors.Is(err, errIndexParse) || !strings.Contains(err.Error(), "is not JSON Crew reads") || !strings.Contains(err.Error(), "key Crew does not read") {
				t.Fatalf("%v %v", refused, err)
			}
		})
	}
}
