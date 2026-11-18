package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidCommit says c is a full commit id.
func ValidCommit(c string) bool { return commitID.MatchString(c) }

// Head returns the commit branch points at in the quarantine repository, where runs push it; "" when
// it is not there.
func (s *Store) Head(ctx context.Context, name, branch string) (string, error) {
	if !ValidBranch(branch) {
		return "", fmt.Errorf("%q is not a branch Crew makes", branch)
	}
	return s.ref(ctx, s.Quarantine.Path(name), "refs/heads/"+branch), nil
}

// Candidate is a merge candidate (7.10): the commit main moves to when its checks pass, with main as
// its first parent and the ticket's head as its second, made in the canonical repository where no
// sandbox reaches.
type Candidate struct {
	Commit    string   // "" when the merge conflicts
	Main      string   // main as it was
	Head      string   // the ticket's head
	Conflicts []string // the files that conflict
	// UpToDate says main has the head already: there is nothing to merge.
	UpToDate bool
	// FastForward says main is an ancestor of head: main has not moved since the branch started or
	// last merged main, so the merged code tree is identical to head's aside from Crew's overlay.
	FastForward bool
}

// ErrNoHead is returned for a branch that is not in the quarantine repository.
var ErrNoHead = errors.New("the branch is not pushed")

// MergeCandidate merges the ticket's branch as the quarantine repository holds it with main, without a
// work tree: the branch's commits are fetched into the canonical repository under refs/crew/, and git
// merge-tree makes the merged tree. The overlay is what Crew writes into the merge itself, the
// changelog's entry and the ticket's decision pages (9.9): a conflict only in those files is none.
func (s *Store) MergeCandidate(ctx context.Context, name, branch, message string, id Identity, overlay Overlay) (Candidate, error) {
	var c Candidate
	head, err := s.Head(ctx, name, branch)
	if err != nil {
		return c, err
	}
	if head == "" {
		return c, ErrNoHead
	}
	canon := s.Canonical(name)
	c.Main, c.Head = s.Main(ctx, name), head
	if c.Main == "" {
		return c, errors.New("the repository has no main")
	}
	keep := "refs/crew/heads/" + branch
	if err := s.run(ctx, canon, nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+head+":"+keep); err != nil {
		return c, err
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", head, c.Main); err == nil {
		c.UpToDate = true
		return c, nil
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", c.Main, head); err == nil {
		c.FastForward = true
	}
	tree, files, conflicted, err := s.mergeTreeOf(ctx, canon, c.Main, head)
	if err != nil {
		return c, err
	}
	if conflicted {
		for _, f := range files {
			if overlay[f] == nil {
				c.Conflicts = append(c.Conflicts, f)
			}
		}
		if len(c.Conflicts) > 0 {
			return c, nil
		}
	}
	if tree, err = s.overlayTree(ctx, canon, tree, c.Main, overlay, false); err != nil {
		return c, err
	}
	env := id.env()
	commit, err := s.outputEnv(ctx, canon, env, "commit-tree", tree, "-p", c.Main, "-p", head, "-m", message)
	if err != nil {
		return c, err
	}
	c.Commit = strings.TrimSpace(commit)
	return c, s.run(ctx, canon, nil, "update-ref", "refs/crew/merge/"+branch, c.Commit)
}

// mergeTree runs git merge-tree --write-tree. Merged, it returns the tree; conflicted, the files.
func (s *Store) mergeTree(ctx context.Context, dir, a, b string) ([]string, bool, error) {
	tree, files, conflicted, err := s.mergeTreeOf(ctx, dir, a, b)
	if conflicted || err != nil {
		return files, conflicted, err
	}
	return []string{tree}, false, nil
}

// mergeTreeOf runs git merge-tree --write-tree, and returns the tree, also when it conflicts, with the
// files that conflict.
func (s *Store) mergeTreeOf(ctx context.Context, dir, a, b string) (string, []string, bool, error) {
	out, errText, err := s.read(ctx, dir, nil, nil, "merge-tree", "--write-tree", "--name-only", "-z", a, b)
	var ee *exec.ExitError
	switch {
	case errors.Is(err, ErrOutputTooLarge):
		// A list of conflicts cut short would read as fewer conflicts.
		return "", nil, false, err
	case err == nil:
		tree, _, _ := strings.Cut(string(out), "\x00")
		return strings.TrimSpace(tree), nil, false, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		// The tree, then the conflicted files, then an empty field and the messages.
		parts := strings.Split(string(out), "\x00")
		var files []string
		for _, p := range parts[1:] {
			if p == "" {
				break
			}
			files = append(files, p)
		}
		return strings.TrimSpace(parts[0]), files, true, nil
	}
	return "", nil, false, fmt.Errorf("git merge-tree: %v: %s", err, errText)
}

// ErrMainMoved is returned when main is no longer the commit a change was made against.
var ErrMainMoved = errors.New("main moved meanwhile")

// AdvanceMain moves main from from to to in the canonical repository, only if it is still from, and
// then gives the quarantine repository the same main (6.1: canonical first). to must have from as an
// ancestor: main is never rewritten.
func (s *Store) AdvanceMain(ctx context.Context, name, from, to string) error {
	if !ValidCommit(from) || !ValidCommit(to) {
		return errors.New("gitstore: main moves between two commits")
	}
	canon := s.Canonical(name)
	if cur := s.Main(ctx, name); cur != from {
		if cur == to {
			return s.publish(ctx, name, []string{"refs/heads/main"})
		}
		return ErrMainMoved
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", from, to); err != nil {
		return fmt.Errorf("gitstore: %s does not follow main", to)
	}
	if err := s.run(ctx, canon, nil, "update-ref", "refs/heads/main", to, from); err != nil {
		return err
	}
	return s.publish(ctx, name, []string{"refs/heads/main"})
}

// Changes lists the commits of head that base does not have, newest first, and the files they
// change, as the canonical repository holds them: the facts of a merged ticket (8.4).
func (s *Store) Changes(ctx context.Context, name, base, head string) (commits, files []string, err error) {
	if !ValidCommit(base) || !ValidCommit(head) {
		return nil, nil, errors.New("gitstore: changes between two commits")
	}
	canon := s.Canonical(name)
	out, err := s.output(ctx, canon, "rev-list", base+".."+head)
	if err != nil {
		return nil, nil, err
	}
	commits = strings.Fields(out)
	out, err = s.output(ctx, canon, "diff", "--name-only", "-z", base+"..."+head)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return commits, files, nil
}

// File is a file of main in the canonical repository, or nil when it is not there. A file git cannot
// read, or one past the bound of a read, is an error, never nil: a guard that reads what main's index
// files name refuses then, and does not take the file for one that is not there (since 1.04).
func (s *Store) File(ctx context.Context, name, path string) ([]byte, error) {
	if strings.HasPrefix(path, "-") || strings.Contains(path, ":") {
		return nil, nil
	}
	return s.fileOf(ctx, s.Canonical(name), "refs/heads/main", path)
}

// HoldMain keeps a main pushed to the quarantine repository from outside, the operator's project
// terminal in edit mode, in the canonical repository under refs/crew/held/<key>, without moving main:
// it waits for the operator's confirmation (13.11). The quarantine repository gets Crew's main back.
func (s *Store) HoldMain(ctx context.Context, name, to, key string) error {
	if !ValidCommit(to) || !safeKey.MatchString(key) {
		return errors.New("gitstore: a held main needs a commit and a key")
	}
	err := s.run(ctx, s.Canonical(name), nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+to+":refs/crew/held/"+key)
	if perr := s.GiveBackMain(ctx, name); perr != nil {
		if err != nil {
			return fmt.Errorf("%w; and main could not be given back to the git endpoint: %v", err, perr)
		}
		return fmt.Errorf("main could not be given back to the git endpoint: %w", perr)
	}
	return err
}

// DropHeld forgets a held main.
func (s *Store) DropHeld(ctx context.Context, name, key string) error {
	if !safeKey.MatchString(key) {
		return errors.New("gitstore: no such held main")
	}
	return s.run(ctx, s.Canonical(name), nil, "update-ref", "-d", "refs/crew/held/"+key)
}

var safeKey = regexp.MustCompile(`^[0-9]{1,18}$`)

// GiveBackMain gives the quarantine repository the canonical main, by force: a main pushed there
// that Crew did not take is ahead of it.
func (s *Store) GiveBackMain(ctx context.Context, name string) error {
	return s.run(ctx, s.Canonical(name), nil, "push", "-q", "--", s.Quarantine.Path(name), "+refs/heads/main:refs/heads/main")
}

// ChangedFiles are the files that changed from from to to in the quarantine repository, where a push
// from outside lands.
func (s *Store) ChangedFiles(ctx context.Context, name, from, to string) ([]string, error) {
	if !ValidCommit(from) || !ValidCommit(to) {
		return nil, ErrNoRev
	}
	out, err := s.output(ctx, s.Quarantine.Path(name), "diff", "--no-renames", "--name-only", "-z", from, to, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Join is the merge of a parallel subtask's branch into its ticket's branch (7.11): the commit the
// ticket's branch moves to when the join's checks pass.
type Join struct {
	Commit    string   // "" when the merge conflicts or there is nothing to join
	Base      string   // the ticket's branch as it was, or main when it was not pushed yet
	Head      string   // the subtask's head
	Conflicts []string // the files that conflict
	// UpToDate says the ticket's branch has the subtask's head already.
	UpToDate bool
}

// JoinCandidate merges source, a subtask's branch, into target, its ticket's branch, as the
// quarantine repository holds them, in the canonical repository and without a work tree. A target
// that only follows the source is moved to it without a merge commit.
func (s *Store) JoinCandidate(ctx context.Context, name, target, source, message string, id Identity) (Join, error) {
	var j Join
	head, err := s.Head(ctx, name, source)
	if err != nil {
		return j, err
	}
	if head == "" {
		return j, ErrNoHead
	}
	base, err := s.Head(ctx, name, target)
	if err != nil {
		return j, err
	}
	if base == "" {
		base = s.Main(ctx, name)
	}
	if base == "" {
		return j, errors.New("the repository has no main")
	}
	j.Base, j.Head = base, head
	canon := s.Canonical(name)
	if err := s.run(ctx, canon, nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+head+":refs/crew/heads/"+source,
		"+"+base+":refs/crew/join-base/"+source); err != nil {
		return j, err
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", head, base); err == nil {
		j.UpToDate = true
		return j, nil
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", base, head); err == nil {
		j.Commit = head
		return j, s.run(ctx, canon, nil, "update-ref", "refs/crew/join/"+source, head)
	}
	out, conflicted, err := s.mergeTree(ctx, canon, base, head)
	if err != nil {
		return j, err
	}
	if conflicted {
		j.Conflicts = out
		return j, nil
	}
	// The join of the same two commits is the same commit: its dates are the later parent's, so a
	// check run on it is found again on the next pass (review of M9).
	stamps, err := s.output(ctx, canon, "log", "--no-walk", "--format=%ct", base, head)
	if err != nil {
		return j, err
	}
	var when int64
	for _, f := range strings.Fields(stamps) {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil && n > when {
			when = n
		}
	}
	date := fmt.Sprintf("@%d +0000", when)
	env := append(id.env(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	commit, err := s.outputEnv(ctx, canon, env, "commit-tree", out[0], "-p", base, "-p", head, "-m", message)
	if err != nil {
		return j, err
	}
	j.Commit = strings.TrimSpace(commit)
	return j, s.run(ctx, canon, nil, "update-ref", "refs/crew/join/"+source, j.Commit)
}

// JoinMain merges main, as the canonical repository has it, into a ticket's branch as the quarantine
// repository holds it, without a work tree: the branch of a ticket that waited for the project's setup
// to be fixed on main (13.5). A branch main already follows is moved to main; one that has main is up
// to date. The caller moves the branch with AdvanceBranch.
func (s *Store) JoinMain(ctx context.Context, name, branch, message string, id Identity) (Join, error) {
	var j Join
	base, err := s.Head(ctx, name, branch)
	if err != nil {
		return j, err
	}
	if base == "" {
		return j, ErrNoHead
	}
	head := s.Main(ctx, name)
	if head == "" {
		return j, errors.New("the repository has no main")
	}
	j.Base, j.Head = base, head
	canon := s.Canonical(name)
	if err := s.run(ctx, canon, nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+base+":refs/crew/join-base/main-into/"+branch); err != nil {
		return j, err
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", head, base); err == nil {
		j.UpToDate = true
		return j, nil
	}
	if _, err := s.output(ctx, canon, "merge-base", "--is-ancestor", base, head); err == nil {
		j.Commit = head
		return j, nil
	}
	out, conflicted, err := s.mergeTree(ctx, canon, base, head)
	if err != nil {
		return j, err
	}
	if conflicted {
		j.Conflicts = out
		return j, nil
	}
	commit, err := s.outputEnv(ctx, canon, id.env(), "commit-tree", out[0], "-p", base, "-p", head, "-m", message)
	if err != nil {
		return j, err
	}
	j.Commit = strings.TrimSpace(commit)
	return j, nil
}

// AdvanceBranch moves a ticket's branch in the quarantine repository to a join that follows it, with a
// push that only goes forward: a branch a run moved meanwhile is not overwritten, and the push fails.
func (s *Store) AdvanceBranch(ctx context.Context, name, branch, to string) error {
	if !ValidBranch(branch) || !ValidCommit(to) {
		return errors.New("gitstore: a branch moves to a commit")
	}
	return s.run(ctx, s.Canonical(name), nil, "push", "-q", "--", s.Quarantine.Path(name), to+":refs/heads/"+branch)
}

// Revert makes the commit that undoes a merge on main as it is now, as git revert -m 1 would (7.13):
// the tree of main with the merge's own change taken out, a three-way merge of main and the merge's
// first parent over the merge itself. It is made in the canonical repository with no work tree, and
// says the files that conflict instead when it does not apply.
func (s *Store) Revert(ctx context.Context, name, merge, message string, id Identity) (string, []string, error) {
	if !ValidCommit(merge) {
		return "", nil, errors.New("gitstore: a revert names a commit")
	}
	main := s.Main(ctx, name)
	if main == "" {
		return "", nil, errors.New("the repository has no main")
	}
	canon := s.Canonical(name)
	parent, err := s.output(ctx, canon, "rev-parse", "--verify", "-q", merge+"^1^{commit}")
	if err != nil {
		return "", nil, fmt.Errorf("the merge %s has no first parent: %w", merge[:12], err)
	}
	out, errText, err := s.read(ctx, canon, nil, nil, "merge-tree", "--write-tree", "--name-only", "-z",
		"--merge-base="+merge, main, strings.TrimSpace(parent))
	var ee *exec.ExitError
	switch {
	case errors.Is(err, ErrOutputTooLarge):
		return "", nil, err
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		parts := strings.Split(string(out), "\x00")
		var files []string
		for _, p := range parts[1:] {
			if p == "" {
				break
			}
			files = append(files, p)
		}
		return "", files, nil
	case err != nil:
		return "", nil, fmt.Errorf("git merge-tree: %v: %s", err, errText)
	}
	tree, _, _ := strings.Cut(string(out), "\x00")
	commit, err := s.outputEnv(ctx, canon, id.env(), "commit-tree", strings.TrimSpace(tree), "-p", main, "-m", message)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(commit), nil, nil
}
