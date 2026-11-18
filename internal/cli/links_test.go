package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A path of a tree is followed through links as an open of it in a workspace that is the tree does,
// and every path of a way through a link stands in for the index file (13.8, since 1.05).
func TestFollowReadsAPathThroughLinksAsAnOpenDoes(t *testing.T) {
	file := Entry{Mode: "100644", Object: "f"}
	folder := Entry{Mode: ModeFolder}
	link := func(to string) Entry { return Entry{Mode: ModeLink, Object: "link:" + to, Size: int64(len(to))} }
	loop := map[string]Entry{".agents": folder, ".agents/plugins.json": link("a"), ".agents/a": link("b"), ".agents/b": link("a")}
	deep := map[string]Entry{".agents": folder, ".agents/plugins.json": link(strings.Repeat("x/", MaxLinkSteps) + "p.json")}
	for i := 0; i <= MaxLinkSteps; i++ {
		deep[".agents/"+strings.TrimSuffix(strings.Repeat("x/", i+1), "/")] = folder
	}
	deep[".agents/"+strings.Repeat("x/", MaxLinkSteps)+"p.json"] = file
	// A chain of more links than an open follows, which leads to a file in fewer steps than the bound.
	chain := map[string]Entry{".agents": folder, "p.json": file, ".agents/plugins.json": link("l1")}
	for i := 1; i <= MaxLinks; i++ {
		chain[fmt.Sprintf(".agents/l%d", i)] = link(fmt.Sprintf("l%d", i+1))
	}
	chain[fmt.Sprintf(".agents/l%d", MaxLinks+1)] = link("../p.json")
	for name, c := range map[string]struct {
		tree         map[string]Entry
		file, end    string
		links, paths []string
		err          error
	}{
		"a file":                {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": file}, file: ".agents/plugins.json", end: ".agents/plugins.json"},
		"no file":               {tree: map[string]Entry{".agents": folder}, end: ".agents/plugins.json"},
		"a link up and over":    {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../cfg/p.json"), "cfg": folder, "cfg/p.json": file}, file: "cfg/p.json", end: "cfg/p.json", links: []string{".agents/plugins.json"}, paths: []string{".agents/plugins.json/", "cfg/p.json/"}},
		"a folder above a link": {tree: map[string]Entry{".agents": link("cfg"), "cfg": folder, "cfg/plugins.json": file}, file: "cfg/plugins.json", end: "cfg/plugins.json", links: []string{".agents"}, paths: []string{".agents/", "cfg/plugins.json/"}},
		"a link to a link":      {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("x"), ".agents/x": link("../p.json"), "p.json": file}, file: "p.json", end: "p.json", links: []string{".agents/plugins.json", ".agents/x"}},
		// ".." after a link goes up from where the link led, not from the link's own folder.
		"up from where a link led": {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("d/../p.json"), ".agents/d": link("../cfg/e"), "cfg": folder, "cfg/e": folder, "cfg/p.json": file}, file: "cfg/p.json", end: "cfg/p.json", links: []string{".agents/plugins.json", ".agents/d"}},
		"a link to nothing":        {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../cfg/p.json")}, end: "cfg", links: []string{".agents/plugins.json"}, paths: []string{".agents/plugins.json/", "cfg/"}},
		"a link to a folder":       {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../cfg/"), "cfg": folder}, end: "cfg", links: []string{".agents/plugins.json"}},
		"a link through a file":    {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../f/p.json"), "f": file}, end: "f", links: []string{".agents/plugins.json"}},
		"a link to a submodule":    {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../m"), "m": {Mode: "160000"}}, end: "m", links: []string{".agents/plugins.json"}},
		"a link out by its root":   {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/etc/p.json"), "etc": folder, "etc/p.json": file}, err: ErrLinkWay},
		"a link out by ..":         {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../../p.json"), "p.json": file}, err: ErrLinkWay},
		"a link through /proc":     {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/proc/self/cwd/cfg/p.json"), "cfg": folder, "cfg/p.json": file}, err: ErrLinkWay},
		"a link with a NUL":        {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../cfg\x00/p.json"), "cfg": folder}, err: ErrLinkWay},
		"a link that is empty":     {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("")}, links: []string{".agents/plugins.json"}},
		// The workspace is at /home/agent/work in every sandbox: a way out of the tree that reaches it
		// again is back in the tree.
		"a link into the workspace":      {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/home/agent/work/cfg/p.json"), "cfg": folder, "cfg/p.json": file}, file: "cfg/p.json", end: "cfg/p.json", links: []string{".agents/plugins.json"}},
		"a link back in by ..":           {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../../work/./cfg//p.json"), "cfg": folder, "cfg/p.json": file}, file: "cfg/p.json", end: "cfg/p.json", links: []string{".agents/plugins.json"}},
		"a link in and out again":        {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/home/agent/work/../x/p.json"), "x": folder, "x/p.json": file}, err: ErrLinkWay},
		"a link beside the workspace":    {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/home/agent/workx/p.json"), "p.json": file}, err: ErrLinkWay},
		"a link past the sandbox's root": {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("/../../home/agent/work/p.json"), "p.json": file}, file: "p.json", end: "p.json", links: []string{".agents/plugins.json"}},
		"links round":                    {tree: loop, err: ErrLinkWay},
		"a way of too many steps":        {tree: deep, err: ErrLinkWay},
		"too many links":                 {tree: chain, err: ErrLinkWay},
		// A link whose listing says its text is too long is never read.
		"a link's text too long":      {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": {Mode: ModeLink, Object: "unread", Size: MaxLinkText + 1}}, err: ErrLinkWay},
		"a text longer than its size": {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": {Mode: ModeLink, Object: "link:" + strings.Repeat("a", MaxLinkText+1)}}, err: ErrLinkWay},
		"a lookup that fails":         {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": link("../broken")}, err: errBroken},
		"a link's text that fails":    {tree: map[string]Entry{".agents": folder, ".agents/plugins.json": {Mode: ModeLink, Object: "bad"}}, err: errBroken},
	} {
		t.Run(name, func(t *testing.T) {
			lookup := func(p string) (Entry, error) {
				if p == "broken" {
					return Entry{}, errBroken
				}
				return c.tree[p], nil
			}
			text := func(e Entry) (string, error) {
				to, ok := strings.CutPrefix(e.Object, "link:")
				if !ok {
					return "", errBroken
				}
				return to, nil
			}
			way, err := Follow(".agents/plugins.json", lookup, text)
			if !errors.Is(err, c.err) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			if err != nil {
				return
			}
			if way.File != c.file || way.End != c.end || !slices.Equal(way.Links, c.links) {
				t.Errorf("way %+v", way)
			}
			if c.paths != nil && !slices.Equal(way.Paths(), c.paths) {
				t.Errorf("paths %v, want %v", way.Paths(), c.paths)
			}
			if c.file != "" && way.Entry != c.tree[c.file] {
				t.Errorf("entry %+v", way.Entry)
			}
		})
	}
}

var errBroken = errors.New("git could not read it")

// Every path of a way through a link counts as an index file of its CLI, code when the index file is
// code; a way with no link adds nothing, and what the file it leads to names counts as before.
func TestIndexedCountsTheWayThroughALink(t *testing.T) {
	code, instructions, err := Indexed(func(p string) ([]byte, Way) {
		switch p {
		case ".agents/plugins.json":
			return []byte(`{"entries": [{"path": "tools"}]}`), Way{File: "cfg/p.json", Links: []string{p}, End: "cfg/p.json"}
		case ".agents/rules.json":
			return nil, Way{Links: []string{p}, End: "cfg/r.json"}
		}
		return nil, Way{}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"cfg/p.json", ".agents/plugins.json", "tools/hook.sh"} {
		if !Matches(f, code) {
			t.Errorf("%s is not code: %v", f, code)
		}
	}
	if Matches("cfg/r.json", code) || !Matches("cfg/r.json", instructions) || Matches("cfg/other.json", instructions) {
		t.Errorf("code %v, instructions %v", code, instructions)
	}
	if fmt.Sprint(Way{File: "a"}.Paths()) != "[]" {
		t.Errorf("a way with no link: %v", Way{File: "a"}.Paths())
	}
}

// An entry of an index file is read from the workspace at /home/agent/work: one that leads outside it
// names nothing, and one that comes back into it names that folder of the tree (since 1.05).
func TestNamedCountsAnEntryThatComesBackIntoTheWorkspace(t *testing.T) {
	named, err := Named([]byte(`{"entries": [{"path": "/home/agent/work/a"}, {"path": "~/work/b"}, {"path": "../work/c/"},
		{"path": "/home/agent/workx"}, {"path": "~bob/work/d"}, {"path": "../../etc"}, {"path": "~/.agy/plugins"}],
		"inherits": [{"path": "./e/../f"}]}`))
	if err != nil || !slices.Equal(named, []string{"a/", "b/", "c/", "f/"}) {
		t.Fatalf("%v %v", named, err)
	}
	// The whole workspace names every path for Matches, and none for Below.
	for _, whole := range []string{".", "./", "~/work", "/home/agent/work/", "../work"} {
		named, err := Named([]byte(`{"entries": [{"path": "` + whole + `"}]}`))
		if err != nil || !slices.Equal(named, []string{AllPaths}) || !Matches("hooks.json", named) || !Matches("a/b", named) || Below("a/b", named) {
			t.Errorf("%s: %v %v", whole, named, err)
		}
	}
	// An entry through the sandbox's own links, which can lead back into the workspace, is an error.
	for _, out := range []string{"/proc/self/cwd/tools", "/proc", "../../../dev/fd/3", "~/../../proc/self/root/home/agent/work/t"} {
		if named, err := Named([]byte(`{"inherits": [{"path": "` + out + `"}]}`)); !errors.Is(err, ErrNamedOutside) {
			t.Errorf("%s: %v %v", out, named, err)
		}
	}
}

// An index file that names a folder through /proc or /dev makes Indexed an error, whatever the other
// index files name (since 1.05).
func TestIndexedIsAnErrorForAnEntryThroughProc(t *testing.T) {
	_, _, err := Indexed(func(p string) ([]byte, Way) {
		if p == ".agents/rules.json" {
			return []byte(`{"entries": [{"path": "/proc/self/cwd/notes"}]}`), Way{}
		}
		return nil, Way{}
	})
	if !errors.Is(err, ErrNamedOutside) {
		t.Fatal(err)
	}
}
