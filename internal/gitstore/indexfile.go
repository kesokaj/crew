package gitstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kesokaj/crew/internal/cli"
)

// IndexFile reads an index file of the agent CLI on main as the CLI opens it (13.8): a link on the way
// is followed within the tree (cli.Follow), and the way comes with what it leads to, nil when that is
// no file. Every failure of git, the bound of a read among them, is an error (since 1.05), and so is a
// file past cli.MaxIndexBytes (since 1.07).
func (s *Store) IndexFile(ctx context.Context, name, path string) ([]byte, cli.Way, error) {
	// One commit for the whole way: main can move between two of its reads.
	main, err := s.output(ctx, s.Canonical(name), "rev-parse", "--verify", "refs/heads/main^{commit}")
	if err != nil {
		return nil, cli.Way{}, err
	}
	return s.indexFile(ctx, s.Canonical(name), strings.TrimSpace(main), path)
}

// IndexFileAt is IndexFile at a commit of the canonical repository.
func (s *Store) IndexFileAt(ctx context.Context, name, commit, path string) ([]byte, cli.Way, error) {
	if !ValidCommit(commit) {
		return nil, cli.Way{}, ErrNoRev
	}
	return s.indexFile(ctx, s.Canonical(name), commit, path)
}

// ErrIndexFileTooLarge is an index file longer than the hook reads one, cli.MaxIndexBytes, by its size
// in the tree before it is read: a guard parsed up to the bound of a read, 64 MiB (since 1.07).
var ErrIndexFileTooLarge = fmt.Errorf("an index file of the agent CLI is longer than %d bytes, more than Crew reads", cli.MaxIndexBytes)

func (s *Store) indexFile(ctx context.Context, dir, rev, path string) ([]byte, cli.Way, error) {
	way, err := s.follow(ctx, dir, rev, path)
	if err != nil || way.File == "" {
		return nil, way, err
	}
	if way.Entry.Size > cli.MaxIndexBytes {
		return nil, way, ErrIndexFileTooLarge
	}
	out, err := s.output(ctx, dir, "cat-file", "blob", way.Entry.Object)
	if err != nil {
		return nil, way, err
	}
	return []byte(out), way, nil
}

// follow reads a path of rev as the CLI opens it (cli.Follow).
func (s *Store) follow(ctx context.Context, dir, rev, path string) (cli.Way, error) {
	lookup := func(p string) (cli.Entry, error) {
		entry, err := s.output(ctx, dir, "--literal-pathspecs", "ls-tree", "-l", "-z", rev, "--", p)
		if err != nil {
			return cli.Entry{}, err
		}
		meta, listed, ok := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		// Only the entry of the path itself: git lists a folder's files for "dir/".
		if !ok || len(f) != 4 || strings.TrimSuffix(listed, "\x00") != p {
			return cli.Entry{}, nil
		}
		size, _ := strconv.ParseInt(f[3], 10, 64) // "-" for a folder or a submodule
		return cli.Entry{Mode: f[0], Object: f[2], Size: size}, nil
	}
	read := func(e cli.Entry) (string, error) {
		return s.output(ctx, dir, "cat-file", "blob", e.Object)
	}
	return cli.Follow(path, lookup, read)
}

// Linked are the paths of main that stand for a path of patterns through a link (cli.Linked), read at
// one commit: what a link of main at, above or below a file or a folder that instructs, or that is code,
// leads to counts as it does (13.8, since 1.06). Every failure of git, the bound of a read among them, is
// an error.
func (s *Store) Linked(ctx context.Context, name string, patterns []string) ([]string, error) {
	main, err := s.output(ctx, s.Canonical(name), "rev-parse", "--verify", "refs/heads/main^{commit}")
	if err != nil {
		return nil, err
	}
	return s.linked(ctx, s.Canonical(name), strings.TrimSpace(main), patterns)
}

// LinkedAt is Linked at a commit of the canonical repository.
func (s *Store) LinkedAt(ctx context.Context, name, commit string, patterns []string) ([]string, error) {
	if !ValidCommit(commit) {
		return nil, ErrNoRev
	}
	return s.linked(ctx, s.Canonical(name), commit, patterns)
}

// BranchLinked is Linked on a ticket's branch, as the quarantine repository holds it.
func (s *Store) BranchLinked(ctx context.Context, name, branch string, patterns []string) ([]string, error) {
	if !ValidBranch(branch) {
		return nil, ErrNoRev
	}
	return s.linked(ctx, s.Quarantine.Path(name), "refs/heads/"+branch, patterns)
}

// linked lists rev's tree once, within the bound of every read of git's output, and reads every lookup
// of a way from the listing (cli.LinkedTree, since 1.06, after its security review).
func (s *Store) linked(ctx context.Context, dir, rev string, patterns []string) ([]string, error) {
	listing, err := s.output(ctx, dir, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	linked, _, err := cli.LinkedTree([]byte(listing), patterns, func(object string) (string, error) {
		return s.output(ctx, dir, "cat-file", "blob", object)
	})
	return linked, err
}
