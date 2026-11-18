package cli

import (
	"slices"
	"sort"
	"strings"
)

// linkIndex finds the links of a tree that Below or Above can match for one pattern by the pattern's
// own prefix, folders and name, never by a scan of every link: Linked looked at every link for each
// pattern, which a push of a few MB, of 200,000 links and index files of some 300,000 entries, made some
// 6·10^10 comparisons that watched no time (since 1.07, the operator's popup answer of session 74,
// "Sorted links + sets"). Linked still decides each candidate by Below and Above.
type linkIndex struct {
	links []string
	// sorted are the places of links, by path: the links with one prefix are next to each other.
	sorted []int
	// at is a link's first place by its path, and names its places by its last part.
	at    map[string]int
	names map[string][]int
}

func newLinkIndex(links []string) *linkIndex {
	x := &linkIndex{links: links, sorted: make([]int, len(links)), at: make(map[string]int, len(links)), names: map[string][]int{}}
	for i, l := range links {
		x.sorted[i] = i
		if _, ok := x.at[l]; !ok {
			x.at[l] = i
		}
		name := l[strings.LastIndex(l, "/")+1:]
		x.names[name] = append(x.names[name], i)
	}
	slices.SortStableFunc(x.sorted, func(a, b int) int { return strings.Compare(links[a], links[b]) })
	return x
}

// candidates are the places of every link Below or Above matches for p, in the tree's order, each once:
// a name with no slash by a link's last part; a folder or a path by the links with its prefix and the
// link it is itself; and a folder above p (Above) by each of p's own folders.
func (x *linkIndex) candidates(p string) []int {
	if !strings.Contains(p, "/") {
		return x.names[p]
	}
	var out []int
	prefix := p
	if !strings.HasSuffix(p, "/") {
		prefix = p + "/"
	}
	from := sort.Search(len(x.sorted), func(i int) bool { return x.links[x.sorted[i]] >= prefix })
	for _, i := range x.sorted[from:] {
		if !strings.HasPrefix(x.links[i], prefix) {
			break
		}
		out = append(out, i)
	}
	if i, ok := x.at[strings.TrimSuffix(p, "/")]; ok {
		out = append(out, i)
	}
	for i := range len(p) {
		if p[i] == '/' {
			if at, ok := x.at[p[:i]]; ok {
				out = append(out, at)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
