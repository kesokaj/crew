package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A Matcher says what Matches says, for every path of a few parts against patterns of each form: a
// name at any depth, a path, a folder at the root, a folder above one, and the whole workspace (since
// 1.06, after its security review's second round).
func TestAMatcherSaysWhatMatchesSays(t *testing.T) {
	parts := []string{"a", "b", "x", "AGENTS.md", ".agents", "skills", "tools", "c.md"}
	var paths []string
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if depth == 0 {
			return
		}
		for _, p := range parts {
			next := strings.TrimPrefix(prefix+"/"+p, "/")
			paths = append(paths, next)
			walk(next, depth-1)
		}
	}
	walk("", 4)
	for _, patterns := range [][]string{
		InstructionFiles(),
		{"AGENTS.md", ".agents/skills", "tools/", "a/b/", "x/AGENTS.md", "a/x/c.md/"},
		{"x", "a/b", "b/"},
		{"tools/", AllPaths},
		{},
	} {
		m := NewMatcher(patterns)
		for _, p := range paths {
			if got, want := m.Matches(p), Matches(p, patterns); got != want {
				t.Fatalf("%q against %q: matcher %v, Matches %v", p, patterns, got, want)
			}
		}
	}
}

// Ways that share one chain of links stand for its paths once, and the changed paths of a large tree
// are matched against them by their own folders, in time a push's hook has (since 1.06, after its
// security review's second round: 4,096 links into one chain made some 168,000 patterns, scanned for
// each of up to 200,000 changed paths). A listing with a record that does not read is an error.
func TestLinksThatShareAChainStandForItOnce(t *testing.T) {
	var listing strings.Builder
	rec := func(mode, kind, object, size, path string) {
		fmt.Fprintf(&listing, "%s %s %s %7s\t%s\x00", mode, kind, object, size, path)
	}
	rec(ModeFolder, "tree", "tc", "-", "c")
	for i := 1; i < 40; i++ {
		rec(ModeLink, "blob", fmt.Sprintf("l%d", i), "3", fmt.Sprintf("c/l%d", i))
	}
	rec("100644", "blob", "e", "2", "c/end")
	for i := range MaxLinked {
		rec(ModeFolder, "tree", "tp", "-", fmt.Sprintf("p%d", i))
		rec(ModeLink, "blob", "head", "7", fmt.Sprintf("p%d/AGENTS.md", i))
	}
	read := func(object string) (string, error) {
		if object == "head" {
			return "../c/l1", nil
		}
		var n int
		fmt.Sscanf(object, "l%d", &n)
		if n == 39 {
			return "end", nil
		}
		return fmt.Sprintf("l%d", n+1), nil
	}
	linked, _, err := LinkedTree([]byte(listing.String()), InstructionFiles(), read)
	if err != nil || len(linked) > MaxLinked+41 {
		t.Fatalf("%d paths: %v", len(linked), err)
	}
	m := NewMatcher(append(InstructionFiles(), linked...))
	start := time.Now()
	n := 0
	for i := range 200_000 {
		if m.Matches(fmt.Sprintf("src/pkg%d/file%d.go", i%500, i)) {
			n++
		}
	}
	if took := time.Since(start); n != 0 || took > 5*time.Second {
		t.Fatalf("%d matched in %v", n, took)
	}
	for _, bad := range []string{"120000 blob x\tl\x00", "120000 blob x 3\t\x00", "999999 blob x 3\tl\x00", "120000 blob x big\tl\x00", "120000 blob x 3 4\tl\x00"} {
		if _, _, err := LinkedTree([]byte(bad), []string{"l"}, read); !errors.Is(err, ErrListing) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
