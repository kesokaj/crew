package gitendpoint

import (
	"strconv"
	"time"
)

// The largest tree a push may bring, and the largest Crew lists or checks (6.1, since 1.03): at most
// MaxTreePaths paths, trees counted, MaxTreeBytes bytes of files, the most a check sandbox takes, a
// path of MaxPathBytes, and MaxListBytes of the listing's text in all. A pack of a few hundred bytes,
// of trees that point at each other, lists millions of paths, and one of long names lists gigabytes:
// listed whole, it filled the server's memory and git's. A push brings at most MaxNewCommits commits,
// each of whose trees is bounded, and each distinct tree is listed once, within MaxTreeTime; the test of
// the files it changes (13.8) runs within MaxTreeTime of its own (since 1.07). Each of those commits, and
// each ref's tip, is at most MaxCommitBytes, its headers and its message: a pack of a few hundred
// kilobytes carries a message of hundreds of megabytes, which git and every history read whole (since
// 1.09).
const (
	MaxTreePaths  = 200_000
	MaxTreeBytes  = 512 << 20
	MaxPathBytes  = 4096
	MaxListBytes  = 64 << 20
	MaxNewCommits = 1000
	MaxTreeTime   = 2 * time.Minute
	// MaxCommitBytes is also the cut of a message the server reads of one commit (gitstore).
	MaxCommitBytes = 1 << 20
)

// treeBoundEnv is the bound as the hook reads it.
func (e *Endpoint) treeBoundEnv() []string {
	limit := MaxTreeTime
	if e.TreeTime > 0 {
		limit = e.TreeTime
	}
	return []string{"CREW_TREE_PATHS=" + strconv.Itoa(MaxTreePaths), "CREW_TREE_BYTES=" + strconv.Itoa(MaxTreeBytes),
		"CREW_PATH_BYTES=" + strconv.Itoa(MaxPathBytes), "CREW_LIST_BYTES=" + strconv.Itoa(MaxListBytes),
		"CREW_NEW_COMMITS=" + strconv.Itoa(MaxNewCommits), "CREW_COMMIT_BYTES=" + strconv.Itoa(MaxCommitBytes),
		"CREW_TREE_SECONDS=" + strconv.FormatFloat(limit.Seconds(), 'f', -1, 64)}
}
