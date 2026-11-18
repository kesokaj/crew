package gitstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The scratch branch of an eval (9.6): eval/<run>, from the commit before a ticket was done, only in
// the quarantine repository, where its runs clone and push it. Nothing of it reaches main or a remote,
// and it is removed after the run.

func evalBranch(branch string) error {
	if !strings.HasPrefix(branch, "eval/") || !ValidBranch(branch) {
		return fmt.Errorf("%q is not an eval's branch", branch)
	}
	return nil
}

// EvalBranch puts an eval's branch at commit in the quarantine repository.
func (s *Store) EvalBranch(ctx context.Context, name, branch, commit string) error {
	if err := evalBranch(branch); err != nil {
		return err
	}
	if !ValidCommit(commit) {
		return errors.New("gitstore: an eval starts at a commit")
	}
	return s.AdvanceBranch(ctx, name, branch, commit)
}

// DropEvalBranch removes an eval's branch from the quarantine repository, and what Crew made of it in
// the canonical one.
func (s *Store) DropEvalBranch(ctx context.Context, name, branch string) error {
	if err := evalBranch(branch); err != nil {
		return err
	}
	var errs []error
	for _, d := range []struct{ dir, ref string }{
		{s.Quarantine.Path(name), "refs/heads/" + branch},
		{s.Canonical(name), "refs/crew/heads/" + branch},
		{s.Canonical(name), "refs/crew/held-out/" + branch},
	} {
		if s.ref(ctx, d.dir, d.ref) != "" {
			errs = append(errs, s.run(ctx, d.dir, nil, "update-ref", "-d", d.ref))
		}
	}
	return errors.Join(errs...)
}

// StartsAt says an eval's head still holds the case's base commit in its history, in the quarantine
// repository where its runs push. The endpoint lets a run rewrite its own branch; a builder that took
// the base commit out of it replayed nothing, and its tester could not read that commit's instructions
// (review of M15 after the live checks).
func (s *Store) StartsAt(ctx context.Context, name, head, base string) (bool, error) {
	if !ValidCommit(head) || !ValidCommit(base) {
		return false, errors.New("gitstore: an eval's head and base are commits")
	}
	dir := s.Quarantine.Path(name)
	if s.ref(ctx, dir, head) == "" || s.ref(ctx, dir, base) == "" {
		return false, fmt.Errorf("gitstore: %s or %s is not in the quarantine repository", head, base)
	}
	_, err := s.output(ctx, dir, "merge-base", "--is-ancestor", base, head)
	return err == nil, nil
}

// FirstParent is the first parent of a commit: for a ticket's merge, main before it.
func (s *Store) FirstParent(ctx context.Context, name, commit string) (string, error) {
	if !ValidCommit(commit) {
		return "", errors.New("gitstore: not a commit")
	}
	out, err := s.output(ctx, s.Canonical(name), "rev-parse", "--verify", "-q", commit+"^1")
	return strings.TrimSpace(out), err
}

// WithHeldOut is the commit an eval's checks run on: its branch's head as the quarantine repository
// holds it, with the held-out files as the commit from has them, copied in only now (9.6). It is made
// in the canonical repository, where Archive reads it. A file or a folder of the branch where a
// held-out file goes gives way to it: a builder's file in the place of a held-out folder made the copy
// fail, and its case measured nothing (13.9, since 1.02).
func (s *Store) WithHeldOut(ctx context.Context, name, branch, from string, paths []string) (string, error) {
	if err := evalBranch(branch); err != nil {
		return "", err
	}
	head, err := s.Head(ctx, name, branch)
	if err != nil {
		return "", err
	}
	if head == "" {
		return "", ErrNoHead
	}
	canon := s.Canonical(name)
	if err := s.run(ctx, canon, nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+"+head+":refs/crew/heads/"+branch); err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return head, nil
	}
	o := Overlay{}
	for _, p := range paths {
		if _, err := s.output(ctx, canon, "cat-file", "-e", from+":"+p); err != nil {
			return "", fmt.Errorf("gitstore: %s is not in %s", p, from)
		}
		o[p] = func(at []byte) []byte { return at }
	}
	tree, err := s.overlayTree(ctx, canon, head+"^{tree}", from, o, true)
	if err != nil {
		return "", err
	}
	commit, err := s.outputEnv(ctx, canon, Identity{Name: "Crew", Email: "crew@localhost"}.env(), "commit-tree", tree, "-p", head, "-m", "The held-out files of an eval")
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	return commit, s.run(ctx, canon, nil, "update-ref", "refs/crew/held-out/"+branch, commit)
}

var crewTag = regexp.MustCompile(`^(part-[a-z0-9][a-z0-9-]{0,59}|done(-[0-9]+)?)$`)

// TagOnce tags a commit of main in the canonical repository: when a part of the plan is complete and
// when the project is Done (6.6). A tag is never moved: one that is there already is left as it is,
// and TagOnce says whether it made it.
func (s *Store) TagOnce(ctx context.Context, name, tag, commit string) (bool, error) {
	if !crewTag.MatchString(tag) || !ValidCommit(commit) {
		return false, fmt.Errorf("gitstore: %q is not a tag Crew makes", tag)
	}
	if s.ref(ctx, s.Canonical(name), "refs/tags/"+tag) != "" {
		return false, nil
	}
	return true, s.run(ctx, s.Canonical(name), nil, "update-ref", "refs/tags/"+tag, commit, strings.Repeat("0", len(commit)))
}
