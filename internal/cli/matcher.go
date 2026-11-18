package cli

import "strings"

// Matcher is Matches for one set of patterns, read once into sets, so a path is matched by its own
// folders and name, never by a scan of every pattern: the hook and the guards match every changed path
// of a tree against what the links of three trees stand for (since 1.06, after its security review's
// second round, the operator's popup answer of session 73, "Dedupe + index patterns").
type Matcher struct {
	all bool
	// names are the patterns with no slash, matched by a path's last part.
	names map[string]bool
	// at are the patterns with a slash, a trailing one taken off, matched by the path or a folder above it.
	at map[string]bool
	// above are the folders above a pattern, matched by the path itself (Above).
	above map[string]bool
}

// NewMatcher reads patterns as Matches does.
func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{names: map[string]bool{}, at: map[string]bool{}, above: map[string]bool{}}
	for _, p := range patterns {
		if p == AllPaths {
			m.all = true
		}
		if !strings.Contains(p, "/") {
			m.names[p] = true
			continue
		}
		m.at[strings.TrimSuffix(p, "/")] = true
		for i := range len(p) {
			if p[i] == '/' {
				m.above[p[:i]] = true
			}
		}
	}
	return m
}

// Matches is Matches(path, patterns) for the patterns the Matcher was made of.
func (m *Matcher) Matches(path string) bool {
	if m.all || m.above[path] || m.names[path[strings.LastIndex(path, "/")+1:]] || m.at[path] {
		return true
	}
	for i := range len(path) {
		if path[i] == '/' && m.at[path[:i]] {
			return true
		}
	}
	return false
}
