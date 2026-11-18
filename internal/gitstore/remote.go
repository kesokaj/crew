package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RemoteMain is where the remote's main is kept in the canonical repository after a fetch (6.6).
const RemoteMain = "refs/crew/remote/main"

// FetchMain fetches the remote's main into the canonical repository, under RemoteMain, and returns
// its commit: "" when the remote has no main yet. env carries how to reach the remote, as for a push.
// Only SSH is allowed, and what Protocols adds for tests.
func (s *Store) FetchMain(ctx context.Context, name, remote string, env []string) (string, error) {
	if strings.HasPrefix(remote, "-") || remote == "" {
		return "", errors.New("gitstore: a fetch names a remote")
	}
	protocols := "ssh"
	if s.Protocols != "" {
		protocols += ":" + s.Protocols
	}
	canon := s.Canonical(name)
	env = append(append([]string{}, env...), "GIT_ALLOW_PROTOCOL="+protocols)
	out, err := s.outputEnv(ctx, canon, env, "ls-remote", "--", remote, "refs/heads/main")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	if err := s.run(ctx, canon, env, "fetch", "-q", "--no-tags", "--", remote, "+refs/heads/main:"+RemoteMain); err != nil {
		return "", err
	}
	return s.ref(ctx, canon, RemoteMain), nil
}

// RemoteBranch is where a branch of the remote is kept in the canonical repository after a fetch.
func RemoteBranch(branch string) string { return "refs/crew/remote/branches/" + branch }

// FetchBranch fetches a ticket's branch from the remote into the canonical repository, under
// RemoteBranch, and returns its commit: "" when the remote has no such branch (7.12, 21).
func (s *Store) FetchBranch(ctx context.Context, name, remote, branch string, env []string) (string, error) {
	if strings.HasPrefix(remote, "-") || remote == "" || !ValidBranch(branch) {
		return "", errors.New("gitstore: a fetch names a remote and a branch Crew makes")
	}
	protocols := "ssh"
	if s.Protocols != "" {
		protocols += ":" + s.Protocols
	}
	canon := s.Canonical(name)
	env = append(append([]string{}, env...), "GIT_ALLOW_PROTOCOL="+protocols)
	out, err := s.outputEnv(ctx, canon, env, "ls-remote", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	if err := s.run(ctx, canon, env, "fetch", "-q", "--no-tags", "--", remote, "+refs/heads/"+branch+":"+RemoteBranch(branch)); err != nil {
		return "", err
	}
	return s.ref(ctx, canon, RemoteBranch(branch)), nil
}

// Follows says commit b follows a: a is b or one of its ancestors, in the canonical repository.
func (s *Store) Follows(ctx context.Context, name, a, b string) bool {
	if !ValidCommit(a) || !ValidCommit(b) {
		return false
	}
	_, err := s.output(ctx, s.Canonical(name), "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// ChangedOnMain are the files that changed from from to to in the canonical repository.
func (s *Store) ChangedOnMain(ctx context.Context, name, from, to string) ([]string, error) {
	if !ValidCommit(from) || !ValidCommit(to) {
		return nil, ErrNoRev
	}
	out, err := s.output(ctx, s.Canonical(name), "diff", "--no-renames", "--name-only", "-z", from, to, "--")
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

// MergeMains merges theirs, the remote's main, into ours, Crew's, without a work tree (7.12): a main
// that went two ways. The merge of the same two commits is the same commit, dated as the later of
// them, so a check run on it is found again. Conflicted, it returns the files.
func (s *Store) MergeMains(ctx context.Context, name, ours, theirs, message string, id Identity) (string, []string, error) {
	if !ValidCommit(ours) || !ValidCommit(theirs) {
		return "", nil, ErrNoRev
	}
	canon := s.Canonical(name)
	out, conflicted, err := s.mergeTree(ctx, canon, ours, theirs)
	if err != nil {
		return "", nil, err
	}
	if conflicted {
		return "", out, nil
	}
	stamps, err := s.output(ctx, canon, "log", "--no-walk", "--format=%ct", ours, theirs)
	if err != nil {
		return "", nil, err
	}
	var when int64
	for _, f := range strings.Fields(stamps) {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil && n > when {
			when = n
		}
	}
	date := fmt.Sprintf("@%d +0000", when)
	env := append(id.env(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	commit, err := s.outputEnv(ctx, canon, env, "commit-tree", out[0], "-p", ours, "-p", theirs, "-m", message)
	if err != nil {
		return "", nil, err
	}
	c := strings.TrimSpace(commit)
	return c, nil, s.run(ctx, canon, nil, "update-ref", "refs/crew/remote/merge", c)
}

// ShareRemoteMain gives the quarantine repository the remote's main as the branch remote/main, which
// a builder that joins Crew's main with it reads (7.12).
func (s *Store) ShareRemoteMain(ctx context.Context, name string) error {
	return s.run(ctx, s.Canonical(name), nil, "push", "-q", "--", s.Quarantine.Path(name), "+"+RemoteMain+":refs/heads/remote/main")
}

// RemoteMainAt is the remote's main as the last fetch left it, or "".
func (s *Store) RemoteMainAt(ctx context.Context, name string) string {
	return s.ref(ctx, s.Canonical(name), RemoteMain)
}

// MergeBase is the newest commit a and b both follow, in the canonical repository.
func (s *Store) MergeBase(ctx context.Context, name, a, b string) (string, error) {
	if !ValidCommit(a) || !ValidCommit(b) {
		return "", ErrNoRev
	}
	out, err := s.output(ctx, s.Canonical(name), "merge-base", a, b)
	return strings.TrimSpace(out), err
}

// IsAncestor says commit b follows a, as Follows does, and says so only when git could read it: a
// commit the repository does not hold, or a history it cannot read, is an error, not a no (since 0.55).
func (s *Store) IsAncestor(ctx context.Context, name, a, b string) (bool, error) {
	if !ValidCommit(a) || !ValidCommit(b) {
		return false, ErrNoRev
	}
	_, errText, err := s.read(ctx, s.Canonical(name), nil, nil, "merge-base", "--is-ancestor", a, b)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base: %v: %s", err, errText)
}

// FollowsBranch says commit follows the ticket's branch as the quarantine repository holds it: the
// branch moved forward to it. A branch that is not there follows from nothing Crew knows, so no
// commit is taken as following it (security review of M13).
func (s *Store) FollowsBranch(ctx context.Context, name, branch, commit string) (bool, error) {
	if !ValidCommit(commit) {
		return false, ErrNoRev
	}
	head, err := s.Head(ctx, name, branch)
	if err != nil || head == "" {
		return false, err
	}
	canon := s.Canonical(name)
	if err := s.run(ctx, canon, nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+head+":refs/crew/quarantine/"+branch); err != nil {
		return false, err
	}
	_, err = s.output(ctx, canon, "merge-base", "--is-ancestor", head, commit)
	return err == nil, nil
}

// FileAt reads a file of a commit the canonical repository holds; nil when it has no such file. A
// file git cannot read, or one past the bound of a read, is an error, as File's is (since 1.04).
func (s *Store) FileAt(ctx context.Context, name, commit, path string) ([]byte, error) {
	if !ValidCommit(commit) || !safePath(path) {
		return nil, ErrNoRev
	}
	return s.fileOf(ctx, s.Canonical(name), commit, path)
}

// LsTree is `git ls-tree -r` of a commit of the canonical repository: each file with its mode and its
// hash, which a release lists (16.1).
func (s *Store) LsTree(ctx context.Context, name, commit string) (string, error) {
	if !ValidCommit(commit) {
		return "", ErrNoRev
	}
	return s.output(ctx, s.Canonical(name), "ls-tree", "-r", commit)
}

// FirstParentLog is FirstParents' line from to back to from, newest first, each commit with its
// parents and subject; at most limit of them.
func (s *Store) FirstParentLog(ctx context.Context, name, from, to string, limit int) ([]Commit, error) {
	if !ValidCommit(to) || (from != "" && !ValidCommit(from)) {
		return nil, ErrNoRev
	}
	r := "--first-parent " + to
	if from != "" {
		r = "--first-parent " + from + ".." + to
	}
	return s.log(ctx, s.Canonical(name), r, limit)
}

// FirstParents are the commits of main's own line from after from up to to, oldest first: each
// merge, and each commit pushed to main itself, without what a merge brought in. from "" is the
// whole line.
func (s *Store) FirstParents(ctx context.Context, name, from, to string) ([]string, error) {
	if !ValidCommit(to) || (from != "" && !ValidCommit(from)) {
		return nil, ErrNoRev
	}
	r := to
	if from != "" {
		r = from + ".." + to
	}
	out, err := s.output(ctx, s.Canonical(name), "rev-list", "--first-parent", "--reverse", r)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
