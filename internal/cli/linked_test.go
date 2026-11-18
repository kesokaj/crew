package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A link of a tree at, above or below a file or a folder that instructs, or that is code, stands for
// it: every path of its way counts as the pattern does, and where it ends is read again for more links
// (13.8, since 1.06).
func TestLinkedFollowsALinkThatStandsForAPattern(t *testing.T) {
	file := Entry{Mode: "100644", Object: "f"}
	folder := Entry{Mode: ModeFolder}
	link := func(to string) Entry { return Entry{Mode: ModeLink, Object: "link:" + to, Size: int64(len(to))} }
	many := map[string]Entry{".agents": folder, ".agents/skills": folder}
	for i := 0; i <= MaxLinked; i++ {
		many[fmt.Sprintf(".agents/skills/s%d", i)] = link(fmt.Sprintf("../../shared/s%d", i))
	}
	patterns := []string{".agents/skills", ".agents/rules", "AGENTS.md", "tools/"}
	for name, c := range map[string]struct {
		tree     map[string]Entry
		patterns []string
		want     []string
		err      error
	}{
		"a folder that is a link": {tree: map[string]Entry{".agents": folder, ".agents/skills": link("../shared/skills"), "shared": folder, "shared/skills": folder},
			want: []string{".agents/skills/", "shared/skills/"}},
		"a folder above one": {tree: map[string]Entry{".agents": link("cfg"), "cfg": folder, "cfg/skills": folder},
			want: []string{".agents/", "cfg/skills/", "cfg/rules/"}},
		"a skill's folder that is a link": {tree: map[string]Entry{".agents": folder, ".agents/skills": folder, ".agents/skills/flows": link("../../vendor/flows"), "vendor": folder, "vendor/flows": folder},
			want: []string{".agents/skills/flows/", "vendor/flows/"}},
		"a file at any depth": {tree: map[string]Entry{"sub": folder, "sub/AGENTS.md": link("../docs/agents.md"), "docs": folder, "docs/agents.md": file},
			want: []string{"sub/AGENTS.md/", "docs/agents.md/"}},
		"a folder an index file names": {tree: map[string]Entry{"tools": link("lib/tools"), "lib": folder, "lib/tools": folder},
			want: []string{"tools/", "lib/tools/"}},
		// Where a way ends is read again: a link in the folder a link leads to stands for it too.
		"a link where a link leads": {tree: map[string]Entry{".agents": folder, ".agents/skills": link("../shared"), "shared": folder, "shared/flows": link("../vendor/flows"), "vendor": folder, "vendor/flows": folder},
			want: []string{".agents/skills/", "shared/", "shared/flows/", "vendor/flows/"}},
		"a link to the root":   {tree: map[string]Entry{".agents": folder, ".agents/rules": link("..")}, want: []string{".agents/rules/", AllPaths}},
		"a link that is empty": {tree: map[string]Entry{".agents": folder, ".agents/rules": link("")}, want: []string{".agents/rules/", AllPaths}},
		"another link":         {tree: map[string]Entry{"docs": link("site/docs"), "site": folder, "site/docs": folder, ".agents": folder}},
		// The whole workspace stands for every path already, so no link is followed for it.
		"the whole workspace": {tree: map[string]Entry{"x": link("/proc/self/cwd")}, patterns: []string{AllPaths}},
		"a link out":          {tree: map[string]Entry{".agents": folder, ".agents/skills": link("/opt/skills")}, err: ErrLinkWay},
		"a link through /proc": {tree: map[string]Entry{".agents": folder, ".agents/skills": link("/proc/self/cwd/shared")},
			err: ErrLinkWay},
		"too many links":   {tree: many, err: ErrLinkWay},
		"a way that fails": {tree: map[string]Entry{".agents": folder, ".agents/skills": {Mode: ModeLink, Object: "bad"}}, err: errBroken},
	} {
		t.Run(name, func(t *testing.T) {
			var links []string
			for p, e := range c.tree {
				if e.Mode == ModeLink {
					links = append(links, p)
				}
			}
			slices.Sort(links)
			follow := func(p string) (Way, error) {
				return Follow(p, func(p string) (Entry, error) { return c.tree[p], nil }, func(e Entry) (string, error) {
					to, ok := strings.CutPrefix(e.Object, "link:")
					if !ok {
						return "", errBroken
					}
					return to, nil
				})
			}
			pats := patterns
			if c.patterns != nil {
				pats = c.patterns
			}
			got, err := Linked(links, pats, follow)
			if !errors.Is(err, c.err) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("linked %q, want %q", got, c.want)
			}
		})
	}
}

// LinkedTree reads every lookup of a way from the tree's listing and each link's text once for its
// object, at most MaxLinkTexts of them (since 1.06, after its security review).
func TestLinkedTreeReadsEachLinksTextOnce(t *testing.T) {
	var listing strings.Builder
	rec := func(mode, kind, object, size, path string) {
		fmt.Fprintf(&listing, "%s %s %s %7s\t%s\x00", mode, kind, object, size, path)
	}
	rec(ModeFolder, "tree", "t1", "-", "docs")
	rec("100644", "blob", "agents", "5", "docs/AGENTS.md")
	for i := range 300 {
		rec(ModeFolder, "tree", fmt.Sprintf("t%d", i+2), "-", fmt.Sprintf("p%d", i))
		rec(ModeLink, "blob", "same", "17", fmt.Sprintf("p%d/GEMINI.md", i))
	}
	reads := 0
	read := func(object string) (string, error) {
		reads++
		return "../docs/AGENTS.md", nil
	}
	linked, links, err := LinkedTree([]byte(listing.String()), []string{"GEMINI.md"}, read)
	if err != nil || reads != 1 || len(links) != 300 || !slices.Contains(linked, "docs/AGENTS.md/") || !slices.Contains(linked, "p299/GEMINI.md/") {
		t.Fatalf("reads %d, links %d, linked %d %v", reads, len(links), len(linked), err)
	}
	// Past MaxLinkTexts distinct texts the way is not followed, though fewer than MaxLinked links stand
	// for a pattern: each of these goes through a second link of its own.
	listing.Reset()
	n := MaxLinkTexts/2 + 52
	for i := range n {
		rec(ModeFolder, "tree", fmt.Sprintf("ts%d", i), "-", fmt.Sprintf("s%d", i))
		rec(ModeLink, "blob", fmt.Sprintf("a%d", i), "9", fmt.Sprintf("s%d/AGENTS.md", i))
		rec(ModeLink, "blob", fmt.Sprintf("b%d", i), "4", fmt.Sprintf("m%d", i))
	}
	second := func(object string) (string, error) {
		if i, ok := strings.CutPrefix(object, "a"); ok {
			return "../m" + i, nil
		}
		return "x.md", nil
	}
	if _, _, err := LinkedTree([]byte(listing.String()), []string{"AGENTS.md"}, second); !errors.Is(err, ErrLinkWay) {
		t.Fatalf("past the texts: %v", err)
	}
}
