package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// What the documentation of a project needs of its repository (9.9): the pages on main with their
// blobs, the commit that last changed each, the files Crew writes itself into a merge commit, and the
// operator's edit of a page as a commit of its own. Everything is read and written in the canonical
// repository, without a work tree.

// TreeFile is a file of a commit: its path and its blob.
type TreeFile struct {
	Path string
	Blob string
	Size int64
}

// Files lists the files of a commit that match, at most max of them, in the order of their paths.
func (s *Store) Files(ctx context.Context, name, commit string, match func(path string) bool, max int) ([]TreeFile, error) {
	if !ValidCommit(commit) {
		return nil, ErrNoRev
	}
	out, err := s.output(ctx, s.Canonical(name), "ls-tree", "-r", "-z", "--long", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	var files []TreeFile
	for _, e := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 || f[1] != "blob" || !match(path) {
			continue
		}
		var size int64
		fmt.Sscan(f[3], &size)
		files = append(files, TreeFile{Path: path, Blob: f[2], Size: size})
		if len(files) >= max {
			break
		}
	}
	return files, nil
}

var blobID = commitID

// fileOf reads path of rev in dir: nil when rev has no such file, as its listing says before the file
// is read. Any failure of git, the bound of a read among them, is an error, never a file that is not
// there (since 1.04).
func (s *Store) fileOf(ctx context.Context, dir, rev, path string) ([]byte, error) {
	entry, err := s.output(ctx, dir, "--literal-pathspecs", "ls-tree", "-z", rev, "--", path)
	if err != nil {
		return nil, err
	}
	meta, listed, ok := strings.Cut(entry, "\t")
	f := strings.Fields(meta)
	// Only the entry of the path itself: "dir/" lists dir's files, and git names a path anew.
	if !ok || len(f) != 3 || f[1] != "blob" || strings.TrimSuffix(listed, "\x00") != path {
		return nil, nil
	}
	out, err := s.output(ctx, dir, "cat-file", "blob", f[2])
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Blob reads a blob of the canonical repository, refused past limit bytes.
func (s *Store) Blob(ctx context.Context, name, blob string, limit int64) ([]byte, error) {
	if !blobID.MatchString(blob) {
		return nil, ErrNoRev
	}
	size, err := s.output(ctx, s.Canonical(name), "cat-file", "-s", blob)
	if err != nil {
		return nil, err
	}
	var n int64
	if _, err := fmt.Sscan(strings.TrimSpace(size), &n); err != nil || n > limit {
		return nil, fmt.Errorf("the blob is %s bytes, more than %d", strings.TrimSpace(size), limit)
	}
	out, err := s.output(ctx, s.Canonical(name), "cat-file", "blob", blob)
	return []byte(out), err
}

// LastChange is the newest commit on main's own line up to commit that changed path, with its time
// and message: a merge Crew made, or a commit of the operator's, never a commit of an agent's branch,
// whose message the agent wrote (security review of M12). The path is read literally, not as a pattern.
// The message is cut at MaxMessage, the most a push brings, and then ends in "…" (since 1.09).
func (s *Store) LastChange(ctx context.Context, name, commit, path string) (id string, at time.Time, message string, err error) {
	if !ValidCommit(commit) {
		return "", at, "", ErrNoRev
	}
	m := newMarks()
	recs, err := s.readHistory(ctx, s.Canonical(name), m, []int{maxName, maxName, MaxMessage}, 1,
		"--literal-pathspecs", "log", "-1", "--first-parent", "--format="+m.format("%H", "%cI", "%B"), commit, "--", path)
	if err != nil {
		return "", at, "", err
	}
	if len(recs) != 1 || len(recs[0].fields) != 3 {
		return "", at, "", fmt.Errorf("no commit changed %s", path)
	}
	f := recs[0].fields
	at, err = time.Parse(time.RFC3339, strings.TrimSpace(f[1]))
	message = strings.TrimSpace(f[2])
	if recs[0].cut[2] {
		message += "…"
	}
	return strings.TrimSpace(f[0]), at, message, err
}

// Overlay is what Crew writes into a tree itself: for each path, its content from what main holds
// there (nil when main has none). A conflict in such a path is Crew's to resolve, so it is none.
type Overlay map[string]func(onMain []byte) []byte

// overlayTree writes the overlay over tree, with main's files as what each function reads, and returns
// the new tree. It uses an index file of its own, so no work tree and no other index is touched. With
// replace, a file or a folder of tree where an overlaid path goes gives way to it, which git refuses
// otherwise.
func (s *Store) overlayTree(ctx context.Context, dir, tree, main string, o Overlay, replace bool) (string, error) {
	if len(o) == 0 {
		return tree, nil
	}
	tmp, err := os.MkdirTemp("", "crew-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := s.outputEnv(ctx, dir, env, "read-tree", tree); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(o))
	for p := range o {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !safePath(p) {
			return "", fmt.Errorf("gitstore: %q is not a path Crew writes", p)
		}
		// A file of main that cannot be read is an error: read as no file, main's content would be
		// written over (since 1.04).
		old, err := s.fileOf(ctx, dir, main, p)
		if err != nil {
			return "", err
		}
		blob, err := s.hashObject(ctx, dir, o[p](old))
		if err != nil {
			return "", err
		}
		args := []string{"update-index", "--add"}
		if replace {
			args = append(args, "--replace")
		}
		if _, err := s.outputEnv(ctx, dir, env, append(args, "--cacheinfo", "100644,"+blob+","+p)...); err != nil {
			return "", err
		}
	}
	out, err := s.outputEnv(ctx, dir, env, "write-tree")
	return strings.TrimSpace(out), err
}

// safePath is a relative path inside the repository, with no part that leaves it or names git's own.
func safePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func (s *Store) hashObject(ctx context.Context, dir string, content []byte) (string, error) {
	out, errText, err := s.read(ctx, dir, nil, bytes.NewReader(content), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("git hash-object: %v: %s", err, errText)
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrPageChanged is returned when a page changed on main since the commit an edit started from.
var ErrPageChanged = errors.New("the page changed on main since it was opened")

// CommitPage writes one file on main as a commit of its own, by id, and moves main to it (9.9): the
// operator's edit of a page. base is the commit the edit started from; when the file changed on main
// since, nothing is written and ErrPageChanged says so. It returns the new main.
func (s *Store) CommitPage(ctx context.Context, name, base, path string, content []byte, message string, id Identity) (string, error) {
	if !ValidCommit(base) || !safePath(path) {
		return "", ErrNoRev
	}
	dir := s.Canonical(name)
	main := s.Main(ctx, name)
	if main == "" {
		return "", errors.New("the repository has no main")
	}
	if main != base {
		if _, err := s.output(ctx, dir, "merge-base", "--is-ancestor", base, main); err != nil {
			return "", ErrPageChanged
		}
		changed, err := s.output(ctx, dir, "--literal-pathspecs", "diff", "--name-only", "-z", base, main, "--", path)
		if err != nil {
			return "", err
		}
		if changed != "" {
			return "", ErrPageChanged
		}
	}
	tree, err := s.overlayTree(ctx, dir, main+"^{tree}", main, Overlay{path: func([]byte) []byte { return content }}, false)
	if err != nil {
		return "", err
	}
	commit, err := s.outputEnv(ctx, dir, id.env(), "commit-tree", tree, "-p", main, "-m", message)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	return commit, s.AdvanceMain(ctx, name, main, commit)
}

// CommitOverlay writes the overlay onto main as a commit of its own when the tree changes, and moves
// main to it. When main holds those files already, it returns main with no new commit.
func (s *Store) CommitOverlay(ctx context.Context, name string, o Overlay, message string, id Identity) (string, error) {
	dir := s.Canonical(name)
	main := s.Main(ctx, name)
	if main == "" {
		return "", errors.New("the repository has no main")
	}
	if len(o) == 0 {
		return main, nil
	}
	tree, err := s.overlayTree(ctx, dir, main+"^{tree}", main, o, true)
	if err != nil {
		return "", err
	}
	mainTree, err := s.output(ctx, dir, "rev-parse", main+"^{tree}")
	if err == nil && strings.TrimSpace(mainTree) == tree {
		return main, nil
	}
	commit, err := s.outputEnv(ctx, dir, id.env(), "commit-tree", tree, "-p", main, "-m", message)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	return commit, s.AdvanceMain(ctx, name, main, commit)
}

// DeletePage removes one file on main as a commit of its own, by id, and moves main to it.
func (s *Store) DeletePage(ctx context.Context, name, path, message string, id Identity) (string, error) {
	if !safePath(path) {
		return "", ErrNoRev
	}
	dir := s.Canonical(name)
	main := s.Main(ctx, name)
	if main == "" {
		return "", errors.New("the repository has no main")
	}
	tmp, err := os.MkdirTemp("", "crew-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := s.outputEnv(ctx, dir, env, "read-tree", main+"^{tree}"); err != nil {
		return "", err
	}
	if _, errText, err := s.read(ctx, dir, env, strings.NewReader("0 0000000000000000000000000000000000000000\t"+path+"\n"), "update-index", "--index-info"); err != nil {
		return "", fmt.Errorf("git update-index: %v: %s", err, errText)
	}
	tree, err := s.outputEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	mainTree, err := s.output(ctx, dir, "rev-parse", main+"^{tree}")
	if err == nil && strings.TrimSpace(mainTree) == tree {
		return main, nil
	}
	commit, err := s.outputEnv(ctx, dir, id.env(), "commit-tree", tree, "-p", main, "-m", message)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	return commit, s.AdvanceMain(ctx, name, main, commit)
}

// CommitBranchOverlay writes the overlay onto main's tree as a commit on branch, and publishes the
// branch to the quarantine repository so agents and the Git screen can read it.
func (s *Store) CommitBranchOverlay(ctx context.Context, name, branch string, o Overlay, message string, id Identity) (string, error) {
	if !ValidBranch(branch) {
		return "", fmt.Errorf("%q is not a branch Crew makes", branch)
	}
	dir := s.Canonical(name)
	main := s.Main(ctx, name)
	if main == "" {
		return "", errors.New("the repository has no main")
	}
	tree, err := s.overlayTree(ctx, dir, main+"^{tree}", main, o, true)
	if err != nil {
		return "", err
	}
	commit, err := s.outputEnv(ctx, dir, id.env(), "commit-tree", tree, "-p", main, "-m", message)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	ref := "refs/heads/" + branch
	if err := s.run(ctx, dir, nil, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, s.publish(ctx, name, []string{ref})
}

// BranchFile reads a file of a ticket's branch as the quarantine repository holds it, through the links
// on its way as the CLI's open of it would (cli.Follow, since 1.06); nil when the branch has no such
// file. Every failure of git, and a way past the bounds or out of the workspace, is an error, as for
// File (since 1.05).
func (s *Store) BranchFile(ctx context.Context, name, branch, path string) ([]byte, error) {
	if !ValidBranch(branch) || !safePath(path) {
		return nil, ErrNoRev
	}
	b, _, err := s.indexFile(ctx, s.Quarantine.Path(name), "refs/heads/"+branch, path)
	return b, err
}

// BaseFile reads a file as it is where a ticket's branch left main, the merge base of the two in the
// quarantine repository, through the links on its way as BranchFile does; nil when it had no such file
// there, or when the branch has no base on main, which git says by its exit status 1 alone. Every other
// failure of git is an error (since 1.05).
func (s *Store) BaseFile(ctx context.Context, name, branch, path string) ([]byte, error) {
	if !ValidBranch(branch) || !safePath(path) {
		return nil, ErrNoRev
	}
	dir := s.Quarantine.Path(name)
	out, errText, err := s.read(ctx, dir, nil, nil, "merge-base", "refs/heads/main", "refs/heads/"+branch)
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee) && ee.ExitCode() == 1 && len(bytes.TrimSpace(out)) == 0:
		return nil, nil // a branch with no base on main has nothing to keep
	default:
		return nil, fmt.Errorf("git merge-base: %v: %s", err, errText)
	}
	b, _, err := s.indexFile(ctx, dir, string(bytes.TrimSpace(out)), path)
	return b, err
}
