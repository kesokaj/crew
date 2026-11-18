package cli

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// scanLinked is Linked as 1.06 had it, every link looked at for each pattern: what Linked must still
// say.
func scanLinked(links, patterns []string, follow func(path string) (Way, error)) ([]string, error) {
	var out []string
	have := map[string]bool{}
	add := func(p string) {
		if !have[p] {
			have[p] = true
			out = append(out, p)
		}
	}
	followed := map[string]bool{}
	queued := map[string]bool{}
	queue := slices.Clone(patterns)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, l := range links {
			at := l
			switch {
			case Below(l, []string{p}):
			case Above(l, []string{p}):
				at = strings.TrimSuffix(p, "/")
			default:
				continue
			}
			if followed[at] {
				continue
			}
			if followed[at] = true; len(followed) > MaxLinked {
				return nil, ErrLinkWay
			}
			way, err := follow(at)
			if err != nil {
				return nil, err
			}
			for _, p := range way.Paths() {
				add(p)
			}
			if way.File == "" && way.End == "" && len(way.Links) > 0 {
				add(AllPaths)
			}
			if end := way.End; end != "" && !queued[end] {
				queued[end] = true
				queue = append(queue, end+"/")
			}
		}
	}
	return out, nil
}

// Linked looks only at the links a pattern's own prefix, folders and name can match, and says what the
// scan of every link said, in the same order, its errors included: for names, folders, paths, the whole
// workspace and odd patterns, against random trees of links whose ways lead on to more (since 1.07).
func TestLinkedSaysWhatTheScanOfEveryLinkSaid(t *testing.T) {
	parts := []string{"a", "b", ".agents", "skills", "s.md", "ab"}
	r := rand.New(rand.NewPCG(1, 7))
	path := func() string {
		var p []string
		for range 1 + r.IntN(4) {
			p = append(p, parts[r.IntN(len(parts))])
		}
		return strings.Join(p, "/")
	}
	pattern := func() string {
		switch r.IntN(7) {
		case 0:
			return parts[r.IntN(len(parts))]
		case 1:
			return AllPaths
		case 2:
			return "/" + path()
		case 3:
			return path()
		case 4:
			return ""
		}
		return path() + "/"
	}
	for i := range 3000 {
		var links, patterns []string
		for range r.IntN(25) {
			links = append(links, path())
		}
		for range 1 + r.IntN(6) {
			patterns = append(patterns, pattern())
		}
		ends := []string{"", "a", "b/skills", "ab/a/s.md", ".agents"}
		follow := func(at string) (Way, error) {
			h := fnv.New32a()
			h.Write([]byte(at))
			switch n := h.Sum32() % 11; {
			case n == 0:
				return Way{}, ErrLinkWay
			case n == 1:
				return Way{Links: []string{at}}, nil // to the root
			case n < 5:
				return Way{Links: []string{at}, File: at + "/f", End: at + "/f"}, nil
			default:
				return Way{Links: []string{at}, End: ends[n%uint32(len(ends))]}, nil
			}
		}
		want, werr := scanLinked(links, patterns, follow)
		got, gerr := Linked(links, patterns, follow)
		if !slices.Equal(got, want) || !errors.Is(gerr, werr) || (werr == nil) != (gerr == nil) {
			t.Fatalf("case %d: links %q, patterns %q:\n got %q %v\nwant %q %v", i, links, patterns, got, gerr, want, werr)
		}
	}
}

// The links of a pushed tree, up to its 200,000 paths, against what the index files of three trees can
// name, 4 × 1,024 entries each, are matched in seconds, where the scan of every link took about a minute
// (since 1.07).
func TestLinkedMatches200000LinksAgainst12288PatternsInSeconds(t *testing.T) {
	var links, patterns []string
	for i := range 200_000 {
		links = append(links, fmt.Sprintf("d%d/l%d", i%500, i))
	}
	for i := range 4 * MaxIndexEntries * 3 {
		patterns = append(patterns, fmt.Sprintf("p%d/q%d/", i%300, i))
	}
	// And a few that each match one link, which leads nowhere further.
	patterns = append(patterns, "d7/l7", "d8/", "l9")
	followed := 0
	start := time.Now()
	out, err := Linked(links, patterns, func(at string) (Way, error) {
		followed++
		return Way{Links: []string{at}, End: "nowhere"}, nil
	})
	if took := time.Since(start); err != nil || took > 5*time.Second || followed != 1+400+1 || len(out) != followed+1 {
		t.Fatalf("%v, %d followed, %d paths, %v", took, followed, len(out), err)
	}
}
