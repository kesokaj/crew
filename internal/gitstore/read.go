package gitstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What the Git screen and a ticket's Git part read (design 6.7): branches, history, tags, and diffs.
// main and the tags are read from the canonical repository, which only Crew writes; the branches runs
// push are read from the quarantine one, where they are. A commit is looked for in the canonical
// repository first. Nothing here writes.

// Commit is one commit, as the screens show it. Its text is what its author wrote: data.
type Commit struct {
	ID      string    `json:"id"`
	Parents []string  `json:"parents"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	When    time.Time `json:"when"`
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
	// Ref is the branch a history of all branches reached the commit by.
	Ref string `json:"ref,omitempty"`
	// Cut says a field past its cut ends in "…": the subject, the body, the author, the email, or the
	// parents, of which the last whole ones are kept (6.1, since 1.09).
	Cut bool `json:"cut,omitempty"`
}

// FileDiff is what one file changed.
type FileDiff struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
	// Patch is the file's unified diff, cut at MaxPatch bytes; Cut says it was.
	Patch string `json:"patch"`
	Cut   bool   `json:"cut"`
	// Omitted, on a last entry with no path, is how many more files changed than are listed.
	Omitted int `json:"omitted,omitempty"`
}

// BranchInfo is a branch with how far it is ahead of main and behind it.
type BranchInfo struct {
	Name   string    `json:"name"`
	Head   string    `json:"head"`
	Ahead  int       `json:"ahead"`
	Behind int       `json:"behind"`
	When   time.Time `json:"when"`
}

// Tag is a tag and the commit it names.
type Tag struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// The most a diff shows of one file, and of all its files.
const (
	MaxPatch      = 64 << 10
	maxPatchTotal = 1 << 20
	MaxFiles      = 1000
)

var (
	tagName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	ownBranch = regexp.MustCompile(`^you/[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	// ErrNoRev is a branch, tag, or commit that is not there, or not one the screens may name.
	ErrNoRev = errors.New("no such branch, tag, or commit")
	// ErrSymlinkRefused indicates a symbolic link inside a protected config or agent prompt path.
	ErrSymlinkRefused = errors.New("symlink in protected config or prompt path refused")
)

// rev resolves what a screen names to a commit and the repository that has it: main, a branch Crew
// makes, a tag, or a commit's id. Anything else is refused before git sees it.
func (s *Store) rev(ctx context.Context, name, what string) (dir, commit string, err error) {
	canon, quar := s.Canonical(name), s.Quarantine.Path(name)
	switch {
	case what == "main":
		if c := s.ref(ctx, canon, "refs/heads/main"); c != "" {
			return canon, c, nil
		}
	case ValidBranch(what):
		if c := s.ref(ctx, quar, "refs/heads/"+what); c != "" {
			return quar, c, nil
		}
		if c := s.ref(ctx, canon, "refs/heads/"+what); c != "" {
			return canon, c, nil
		}
	case ownBranch.MatchString(what) && !strings.Contains(what, "..") && !strings.HasSuffix(what, ".lock"):
		// The operator's own branches, which the project terminal pushes in edit mode (13.11).
		if c := s.ref(ctx, quar, "refs/heads/"+what); c != "" {
			return quar, c, nil
		}
	case ValidCommit(what):
		for _, d := range []string{canon, quar} {
			if c := s.ref(ctx, d, what); c != "" {
				return d, c, nil
			}
		}
	case tagName.MatchString(what) && !strings.Contains(what, ".."):
		if c := s.ref(ctx, canon, "refs/tags/"+what); c != "" {
			return canon, c, nil
		}
	}
	return "", "", ErrNoRev
}

// Rev resolves what a screen names to its commit id in name, or "" when it is not there.
func (s *Store) Rev(ctx context.Context, name, what string) string {
	_, c, err := s.rev(ctx, name, what)
	if err != nil {
		return ""
	}
	return c
}

// Log lists the commits of what, newest first, at most limit; with since, only those since does not
// have.
func (s *Store) Log(ctx context.Context, name, what, since string, limit int) ([]Commit, error) {
	dir, head, err := s.rev(ctx, name, what)
	if err != nil {
		return nil, err
	}
	r := head
	if since != "" {
		_, base, err := s.rev(ctx, name, since)
		if err != nil {
			return nil, err
		}
		if !s.has(ctx, dir, base) {
			dir = s.Quarantine.Path(name)
		}
		r = base + ".." + head
	}
	return s.log(ctx, dir, r, limit)
}

// LogAll lists the commits of every branch, newest first, each with the branch it was reached by.
func (s *Store) LogAll(ctx context.Context, name string, limit int) ([]Commit, error) {
	m := newMarks()
	recs, err := s.readHistory(ctx, s.Quarantine.Path(name), m, slices.Concat(commitCuts, []int{maxName}), limit,
		"log", "--branches", "--source", "--topo-order", "-n", strconv.Itoa(limit),
		"--format="+m.format(slices.Concat(commitFields, []string{"%S"})...), "--")
	if err != nil {
		return nil, err
	}
	return commits(recs, 8), nil
}

// LogRange lists the commits from..to, oldest first: what one push brought. An empty from is a branch
// that shares no history with main: every commit of to.
func (s *Store) LogRange(ctx context.Context, name, from, to string) ([]Commit, error) {
	if !ValidCommit(to) || (from != "" && !ValidCommit(from)) {
		return nil, ErrNoRev
	}
	dir := s.Quarantine.Path(name)
	if !s.has(ctx, dir, to) {
		dir = s.Canonical(name)
	}
	r := to
	if from != "" {
		r = from + ".." + to
	}
	cs, err := s.log(ctx, dir, r, 500)
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	return cs, err
}

// CommitIDs are the ids of the commits from..to, newest first, at most the 500 LogRange finds, and
// nothing else of them: a view that keeps no more commits reads a push this way, whatever its messages
// hold (since 1.09).
func (s *Store) CommitIDs(ctx context.Context, name, from, to string) ([]string, error) {
	if !ValidCommit(to) || (from != "" && !ValidCommit(from)) {
		return nil, ErrNoRev
	}
	dir := s.Quarantine.Path(name)
	if !s.has(ctx, dir, to) {
		dir = s.Canonical(name)
	}
	r := to
	if from != "" {
		r = from + ".." + to
	}
	out, err := s.output(ctx, dir, "rev-list", "--max-count=500", r, "--")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (s *Store) has(ctx context.Context, dir, commit string) bool {
	_, err := s.output(ctx, dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func (s *Store) log(ctx context.Context, dir, r string, limit int) ([]Commit, error) {
	m := newMarks()
	args := []string{"log", "--format=" + m.format(commitFields...), "-n", strconv.Itoa(limit)}
	args = append(args, strings.Fields(r)...)
	args = append(args, "--")
	recs, err := s.readHistory(ctx, dir, m, commitCuts, limit, args...)
	if err != nil {
		return nil, err
	}
	return commits(recs, 7), nil
}

// Show is one commit and what it changed from its first parent.
func (s *Store) Show(ctx context.Context, name, commit string) (Commit, []FileDiff, error) {
	dir, c, err := s.rev(ctx, name, commit)
	if err != nil {
		return Commit{}, nil, err
	}
	cs, err := s.log(ctx, dir, c, 1)
	if errors.Is(err, ErrOutputTooLarge) {
		// Not ErrNoRev, which a reader takes for a commit with nothing to show yet (since 1.04).
		return Commit{}, nil, err
	}
	if err != nil || len(cs) == 0 {
		return Commit{}, nil, ErrNoRev
	}
	base := "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // the empty tree, for a first commit
	if len(cs[0].Parents) > 0 {
		base = cs[0].Parents[0]
	}
	files, err := s.diff(ctx, dir, base, c)
	return cs[0], files, err
}

// Compare is what to changed since it left from: the diff from their merge base, as a pull request
// shows it.
func (s *Store) Compare(ctx context.Context, name, from, to string) ([]FileDiff, error) {
	_, a, err := s.rev(ctx, name, from)
	if err != nil {
		return nil, err
	}
	dir, b, err := s.rev(ctx, name, to)
	if err != nil {
		return nil, err
	}
	if !s.has(ctx, dir, a) {
		dir = s.Quarantine.Path(name)
	}
	out, err := s.output(ctx, dir, "merge-base", a, b)
	if err != nil {
		return nil, fmt.Errorf("%s and %s have no common history", from, to)
	}
	return s.diff(ctx, dir, strings.TrimSpace(out), b)
}

// diff lists the files that changed from a to b with their counts and their patches: at most
// MaxFiles files, and a last entry that says how many more there are; the patches from one git diff,
// at most maxPatchTotal in all and MaxPatch a file.
func (s *Store) diff(ctx context.Context, dir, a, b string) ([]FileDiff, error) {
	out, err := s.output(ctx, dir, "diff", "--no-renames", "--numstat", "-z", a, b, "--", ":(exclude).crew")
	if err != nil {
		return nil, err
	}
	files := []FileDiff{}
	for _, rec := range strings.Split(out, "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		d := FileDiff{Path: f[2], Binary: f[0] == "-"}
		d.Added, _ = strconv.Atoi(f[0])
		d.Removed, _ = strconv.Atoi(f[1])
		files = append(files, d)
	}
	patch, cut, err := s.outputLimited(ctx, dir, maxPatchTotal, "diff", "--no-renames", "--no-color", "--no-ext-diff", a, b, "--", ":(exclude).crew")
	if err != nil {
		return nil, err
	}
	// The patch is one part a file, in the order of the counts; a part begins with "diff --git ",
	// which no line of a file's content can, as each of those begins with a space, + or -.
	parts := splitPatch(patch)
	for i := range files {
		switch {
		case files[i].Binary:
		case i >= len(parts) || (i == len(parts)-1 && cut):
			files[i].Cut = true
			if i < len(parts) {
				files[i].Patch = parts[i]
			}
		default:
			files[i].Patch = parts[i]
		}
		if len(files[i].Patch) > MaxPatch {
			files[i].Patch, files[i].Cut = files[i].Patch[:MaxPatch], true
		}
	}
	if n := len(files) - MaxFiles; n > 0 {
		files = append(files[:MaxFiles], FileDiff{Omitted: n})
	}
	return files, nil
}

func splitPatch(p string) []string {
	var parts []string
	for len(p) > 0 {
		next := strings.Index(p[1:], "\ndiff --git ")
		if next < 0 {
			parts = append(parts, p)
			break
		}
		parts = append(parts, p[:next+2])
		p = p[next+2:]
	}
	return parts
}

// Branches lists the branches: main, and every branch runs pushed or Crew cut, with how far each is
// ahead of main and behind it.
func (s *Store) Branches(ctx context.Context, name string) ([]BranchInfo, error) {
	main := s.Main(ctx, name)
	quar := s.Quarantine.Path(name)
	out, err := s.output(ctx, quar, "for-each-ref", "--format=%(refname:short)%1f%(objectname)%1f%(committerdate:iso-strict)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var bs []BranchInfo
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\x1f")
		if len(f) != 3 {
			continue
		}
		b := BranchInfo{Name: f[0], Head: f[1]}
		b.When, _ = time.Parse(time.RFC3339, f[2])
		if b.Name == "main" {
			b.Head = main
		} else if main != "" {
			counts, err := s.output(ctx, quar, "rev-list", "--left-right", "--count", main+"..."+b.Head)
			if err == nil {
				if f := strings.Fields(counts); len(f) == 2 {
					b.Behind, _ = strconv.Atoi(f[0])
					b.Ahead, _ = strconv.Atoi(f[1])
				}
			}
		}
		bs = append(bs, b)
	}
	if bs == nil {
		bs = []BranchInfo{}
	}
	return bs, nil
}

// Tags lists the tags of the canonical repository.
func (s *Store) Tags(ctx context.Context, name string) ([]Tag, error) {
	out, err := s.output(ctx, s.Canonical(name), "for-each-ref", "--format=%(refname:short)%1f%(objectname)", "refs/tags/")
	if err != nil {
		return nil, err
	}
	ts := []Tag{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Split(l, "\x1f"); len(f) == 2 {
			ts = append(ts, Tag{Name: f[0], Commit: f[1]})
		}
	}
	return ts, nil
}

// Touched lists every file that changed from where to left from to to, each with whether it was
// deleted, as git names them in full: no cut, unlike the patches of Compare. A check decides by it
// (7.1; review of M13: a deletion behind a cut patch or a binary file was not seen).
func (s *Store) Touched(ctx context.Context, name, from, to string) (map[string]bool, error) {
	_, a, err := s.rev(ctx, name, from)
	if err != nil {
		return nil, err
	}
	dir, b, err := s.rev(ctx, name, to)
	if err != nil {
		return nil, err
	}
	if !s.has(ctx, dir, a) {
		dir = s.Quarantine.Path(name)
	}
	base, err := s.output(ctx, dir, "merge-base", a, b)
	if err != nil {
		return nil, fmt.Errorf("%s and %s have no common history", from, to)
	}
	out, err := s.output(ctx, dir, "diff", "--no-renames", "--name-status", "-z", strings.TrimSpace(base), b, "--")
	if err != nil {
		return nil, err
	}
	touched := map[string]bool{}
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "" {
			break
		}
		touched[fields[i+1]] = fields[i] == "D"
	}
	return touched, nil
}

// TreeEntry is one file in a repository's tree at a branch, tag, or commit.
type TreeEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// BlobView is one file's content at a branch, tag, or commit, cut at MaxBlob bytes.
type BlobView struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Binary  bool   `json:"binary"`
	Content string `json:"content"`
	Cut     bool   `json:"cut"`
}

// MaxBlob is the largest file content Blob reads at once.
const MaxBlob = 256 << 10

// Tree lists the files in name at what (a branch, a tag, or a commit), up to 5000 entries.
func (s *Store) Tree(ctx context.Context, name, what string) ([]TreeEntry, error) {
	dir, c, err := s.rev(ctx, name, what)
	if err != nil {
		return nil, err
	}
	out, err := s.output(ctx, dir, "ls-tree", "-r", "-l", "-z", c, "--")
	if err != nil {
		return nil, err
	}
	entries := []TreeEntry{}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok || p == "" {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || f[1] != "blob" {
			continue
		}
		sz, _ := strconv.ParseInt(f[3], 10, 64)
		entries = append(entries, TreeEntry{Path: p, Size: sz})
		if len(entries) >= 5000 {
			break
		}
	}
	return entries, nil
}

// ReadBlob reads one file's content in name at what (a branch, a tag, or a commit), cut at MaxBlob bytes.
func (s *Store) ReadBlob(ctx context.Context, name, what, path string) (BlobView, error) {
	clean := filepath.ToSlash(filepath.Clean(path))
	if path == "" || clean == "." || clean == "/" || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || strings.Contains(path, "\x00") {
		return BlobView{}, ErrNoRev
	}
	dir, c, err := s.rev(ctx, name, what)
	if err != nil {
		return BlobView{}, err
	}
	spec := c + ":" + clean
	typ, err := s.output(ctx, dir, "cat-file", "-t", spec)
	if err != nil || strings.TrimSpace(typ) != "blob" {
		return BlobView{}, ErrNoRev
	}
	szStr, err := s.output(ctx, dir, "cat-file", "-s", spec)
	if err != nil {
		return BlobView{}, ErrNoRev
	}
	sz, _ := strconv.ParseInt(strings.TrimSpace(szStr), 10, 64)
	raw, cut, err := s.outputLimited(ctx, dir, MaxBlob, "cat-file", "blob", spec)
	if err != nil {
		return BlobView{}, err
	}
	if strings.IndexByte(raw, 0) >= 0 {
		return BlobView{Path: clean, Size: sz, Binary: true}, nil
	}
	return BlobView{Path: clean, Size: sz, Content: raw, Cut: cut}, nil
}

// CheckNoSymlinkChain fails closed with ErrSymlinkRefused if relPath or any parent directory at
// revision what is a git symlink (mode 120000). Missing paths return nil.
func (s *Store) CheckNoSymlinkChain(ctx context.Context, name, what, relPath string) error {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	if relPath == "" || clean == "." || clean == "/" || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || strings.Contains(relPath, "\x00") {
		return ErrNoRev
	}
	dir, c, err := s.rev(ctx, name, what)
	if err != nil {
		return err
	}
	parts := strings.Split(clean, "/")
	args := make([]string, 0, 3+len(parts))
	args = append(args, "ls-tree", c, "--")
	for i := range parts {
		args = append(args, strings.Join(parts[:i+1], "/"))
	}
	out, err := s.output(ctx, dir, args...)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		meta, entryPath, hasTab := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if len(fields) > 0 && fields[0] == "120000" {
			if !hasTab {
				entryPath = clean
			}
			return fmt.Errorf("%w: %s", ErrSymlinkRefused, entryPath)
		}
	}
	return nil
}

// ReadRegularBlob verifies no symlink exists anywhere along path at revision what before reading it.
func (s *Store) ReadRegularBlob(ctx context.Context, name, what, path string) (BlobView, error) {
	if err := s.CheckNoSymlinkChain(ctx, name, what, path); err != nil {
		return BlobView{}, err
	}
	return s.ReadBlob(ctx, name, what, path)
}
