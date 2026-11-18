package board

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// testFolders are folders whose files are tests (7.1, since 0.47).
var testFolders = map[string]bool{"test": true, "tests": true, "__tests__": true, "e2e": true, "testdata": true}

// IsTestFile says a path is a test file (7.1, since 0.47): a file named *_test.go, test_*.py, *_test.py,
// *.test.*, or *.spec.*, a file under a folder test/, tests/, __tests__/, e2e/, or testdata/, and a path
// a command check of the ticket names.
func IsTestFile(p string, checks []CheckSpec) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if testFolders[dir] {
			return true
		}
	}
	return NamedByCheck(p, checks)
}

// NamedByCheck says a command check names the path: one of its words, or a flag's value after =, is
// the path, ./ and the path, or the path with a line after : or a test's name after :: (review of
// M13: a word that only ends with the path, such as out/report.txt for report.txt, names another
// file).
func NamedByCheck(p string, checks []CheckSpec) bool {
	for _, c := range checks {
		if c.Kind != "cmd" {
			continue
		}
		for _, w := range strings.FieldsFunc(c.Spec, func(r rune) bool { return strings.ContainsRune(" \t\n'\";&|()<>=", r) }) {
			w = strings.TrimPrefix(w, "./")
			if w == p || strings.HasPrefix(w, p+":") {
				return true
			}
		}
	}
	return false
}

// exitNames are the checks a ticket passes to leave its stage: the stage's exit checks; in In progress
// named_tests_kept when the ticket goes on without a review (7.1, since 0.47), except for a revert,
// which takes a change out by design, its tests with it, and whose checks prove they are gone (7.13);
// and before Done review_covers_head, outside Review (7.4, since 0.90).
func exitNames(pipe Pipeline, cur settings.Stage, t *Ticket) []string {
	names := slices.Clone(cur.Exit)
	if cur.ID == settings.StageInProgress && t.Kind != "revert" && !slices.Contains(names, "named_tests_kept") {
		if rv, ok := pipe.Stage(settings.StageReview); !ok || !rv.On || slices.Contains(t.Skips, settings.StageReview) {
			names = append(names, "named_tests_kept")
		}
	}
	if next, ok := pipe.Next(cur.ID, t.Skips); ok && next.ID == settings.StageDone && cur.ID != settings.StageReview {
		names = append(names, "review_covers_head")
	}
	return names
}

// namedTestsKept fails a branch that deletes a file a command check of its ticket names (7.1): with
// no review, nobody else reads why a test went.
func namedTestsKept(ctx context.Context, c *CheckContext) (Result, error) {
	if c.Ticket.Branch == nil {
		return Result{Passed: true, Detail: "it has no branch yet"}, nil
	}
	touched, err := c.Git.Touched(ctx, c.Project.Name, "main", *c.Ticket.Branch)
	if err != nil {
		return Result{Detail: "its branch cannot be compared with main"}, nil
	}
	var gone []string
	for path, deleted := range touched {
		if deleted && NamedByCheck(path, c.Ticket.Checks) {
			gone = append(gone, path)
		}
	}
	slices.Sort(gone)
	if len(gone) > 0 {
		return Result{Detail: "the branch deletes " + strings.Join(gone, ", ") + ", which a check of the ticket names: put it back, and change the code, not the test"}, nil
	}
	return Result{Passed: true, Detail: "no file a check names is deleted"}, nil
}

// TestFile is a test file a ticket's branch changes or deletes, with its diff against main.
type TestFile struct {
	gitstore.FileDiff
	Deleted bool
}

// TestFiles are the test files the ticket's branch at head changes or deletes, against where it left
// main (7.1): what its reviewer is shown, and what it gives a reason for. Which files they are comes
// from git's full list; only their diffs may be cut, or missing past the cut, and say so.
func (b *Board) TestFiles(ctx context.Context, p *Project, t *Ticket, head string) ([]TestFile, error) {
	touched, err := b.Git.Touched(ctx, p.Name, "main", head)
	if err != nil {
		return nil, err
	}
	diffs, err := b.Git.Compare(ctx, p.Name, "main", head)
	if err != nil {
		return nil, err
	}
	byPath := map[string]gitstore.FileDiff{}
	for _, d := range diffs {
		byPath[d.Path] = d
	}
	var out []TestFile
	for path, deleted := range touched {
		if !IsTestFile(path, t.Checks) {
			continue
		}
		d, ok := byPath[path]
		if !ok {
			d = gitstore.FileDiff{Path: path, Cut: true}
		}
		out = append(out, TestFile{FileDiff: d, Deleted: deleted})
	}
	slices.SortFunc(out, func(a, b TestFile) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}
