package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

// The reviewer's list of package files (13.8): a manifest with its counts and diff, a lock file with
// its counts and no diff, a binary lock file said to be binary, and a file past the cap of what Crew
// compares with its counts said to be unknown, not +0 -0.
func TestThePackageFilesAReviewerIsShown(t *testing.T) {
	got := packageFilesText([]board.PackageFile{
		{FileDiff: gitstore.FileDiff{Path: "go.mod", Added: 1, Patch: "+require example.com/x v1.0.0\n"}},
		{FileDiff: gitstore.FileDiff{Path: "go.sum", Added: 2}, Lock: true},
		{FileDiff: gitstore.FileDiff{Path: "bun.lockb", Binary: true}, Lock: true},
		{FileDiff: gitstore.FileDiff{Path: "web/package.json", Cut: true}},
		{FileDiff: gitstore.FileDiff{Path: "old/requirements.txt", Removed: 3}, Deleted: true},
	})
	for _, want := range []string{
		"go.mod (manifest, changed, +1 -0)\n+require example.com/x v1.0.0\n",
		"go.sum (lock file, changed, +2 -0)\n\n",
		"bun.lockb (lock file, changed, binary)\n",
		"web/package.json (manifest, changed, counts not known)\n(Its diff is too long to show here, or cut: read it in your workspace.)\n",
		"old/requirements.txt (manifest, deleted, +0 -3)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the list does not hold %q:\n%s", want, got)
		}
	}
}

// The reviewer's context asks a word on each package file; it speaks of a lock file's diff only when a
// lock file is listed, and of a binary one only when a binary lock file is.
func TestWhatTheReviewersContextSaysOfPackageFiles(t *testing.T) {
	mod := board.PackageFile{FileDiff: gitstore.FileDiff{Path: "go.mod", Added: 1}}
	sum := board.PackageFile{FileDiff: gitstore.FileDiff{Path: "go.sum", Added: 2}, Lock: true}
	bin := board.PackageFile{FileDiff: gitstore.FileDiff{Path: "bun.lockb", Binary: true}, Lock: true}
	const lock, binary = "A lock file is listed without its diff", "A file listed as binary cannot be read by its diff"
	for _, c := range []struct {
		files        []board.PackageFile
		lock, binary bool
	}{
		{[]board.PackageFile{mod}, false, false},
		{[]board.PackageFile{mod, sum}, true, false},
		{[]board.PackageFile{mod, bin}, true, true},
	} {
		got := packageFilesContext(c.files)
		if !strings.Contains(got, fmt.Sprintf("The change touches %d package files", len(c.files))) ||
			strings.Contains(got, lock) != c.lock || strings.Contains(got, binary) != c.binary {
			t.Errorf("%d files (lock %v, binary %v): %s", len(c.files), c.lock, c.binary, got)
		}
	}
}
