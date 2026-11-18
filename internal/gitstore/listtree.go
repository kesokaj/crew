package gitstore

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// ErrTreeTooLarge is a tree past the bound a push may bring and a check sandbox takes: more than
// gitendpoint.MaxTreePaths paths, trees counted, gitendpoint.MaxTreeBytes bytes of files, a path of
// gitendpoint.MaxPathBytes, or gitendpoint.MaxListBytes of listing (6.1, since 1.03). Its text is
// fixed: it is a check run's detail.
var ErrTreeTooLarge = fmt.Errorf("the tree holds more than %d paths, %d bytes of files or a path of %d bytes, more than a check sandbox takes",
	gitendpoint.MaxTreePaths, gitendpoint.MaxTreeBytes, gitendpoint.MaxPathBytes)

// treeFile is a file of a tree, as Archive writes it.
type treeFile struct{ mode, sha, path string }

// listTree lists the files of commit's tree, streamed, and stops at the first past the bound: a tree
// that did not come through the git endpoint, a remote's main, or one stored before the endpoint
// bounded it, is never listed whole into the server's memory (since 1.03). A tree and a submodule
// count as paths, and are no files; a record is read through a buffer of its own bound, so one long
// name is never read whole.
func (s *Store) listTree(ctx context.Context, dir, commit string) ([]treeFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), "ls-tree", "-r", "-t", "-z", "-l", "--full-tree", commit)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(nil)
	stderr := &gitendpoint.LimitedText{Max: maxErrText}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func(err error) ([]treeFile, error) {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	// A record is its mode, type, id and size, a tab, the path and a NUL: past the buffer, its path is
	// past the bound.
	r := bufio.NewReaderSize(out, gitendpoint.MaxPathBytes+256)
	var files []treeFile
	var paths int
	var size, listed int64
	for {
		rec, err := r.ReadSlice(0)
		if errors.Is(err, io.EOF) && len(rec) == 0 {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return stop(ErrTreeTooLarge)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return stop(err)
		}
		paths++
		listed += int64(len(rec))
		meta, p, ok := strings.Cut(strings.TrimSuffix(string(rec), "\x00"), "\t")
		f := strings.Fields(meta)
		if ok && len(f) == 4 && f[1] == "blob" {
			n, _ := strconv.ParseInt(f[3], 10, 64)
			size += n
			files = append(files, treeFile{mode: f[0], sha: f[2], path: p})
		}
		if paths > gitendpoint.MaxTreePaths || size > gitendpoint.MaxTreeBytes || len(p) > gitendpoint.MaxPathBytes || listed > gitendpoint.MaxListBytes {
			return stop(ErrTreeTooLarge)
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git ls-tree: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return files, nil
}

// Archive writes the tree of commit as a tar archive to w: the code a check sandbox gets (13.9). The
// commit is read from the quarantine repository, where the runs pushed it, or from the canonical one,
// where a merge candidate is made. The archive is written here from the tree's objects, not by git
// archive, which obeys the commit's own .gitattributes: export-ignore and export-subst would let a
// branch check another tree than the one that merges. A tree past the bound is ErrTreeTooLarge,
// before it is listed whole (since 1.03).
func (s *Store) Archive(ctx context.Context, name, commit string, w io.Writer) error {
	if !ValidCommit(commit) {
		return fmt.Errorf("gitstore: %q is not a commit", commit)
	}
	dir := s.Canonical(name)
	if s.ref(ctx, dir, commit) == "" {
		dir = s.Quarantine.Path(name)
	}
	entries, err := s.listTree(ctx, dir, commit)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), "cat-file", "--batch")...)
	cmd.Dir = dir
	cmd.Env = gitEnv(nil)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for _, e := range entries {
			fmt.Fprintln(in, e.sha)
		}
		in.Close()
	}()
	r := bufio.NewReader(out)
	tw := tar.NewWriter(w)
	werr := func() error {
		for _, e := range entries {
			head, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			f := strings.Fields(head)
			if len(f) != 3 || f[0] != e.sha {
				return fmt.Errorf("git cat-file: %q", strings.TrimSpace(head))
			}
			size, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil {
				return err
			}
			if e.mode == "120000" {
				if size > 4096 {
					return fmt.Errorf("a link of %d bytes", size)
				}
				target := make([]byte, size)
				if _, err := io.ReadFull(r, target); err != nil {
					return err
				}
				if err := tw.WriteHeader(&tar.Header{Name: e.path, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: string(target), Format: tar.FormatPAX}); err != nil {
					return err
				}
			} else {
				h := &tar.Header{Name: e.path, Mode: 0o644, Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
				if e.mode == "100755" {
					h.Mode = 0o755
				}
				if err := tw.WriteHeader(h); err != nil {
					return err
				}
				// Streamed, so a large file is not held in memory whole.
				if _, err := io.CopyN(tw, r, size); err != nil {
					return err
				}
			}
			if _, err := r.Discard(1); err != nil {
				return err
			}
		}
		return tw.Close()
	}()
	if werr != nil {
		// git may be blocked writing what nobody reads now: it is stopped, not waited for.
		cancel()
	}
	if err := cmd.Wait(); err != nil && werr == nil {
		werr = fmt.Errorf("git cat-file: %v", err)
	}
	return werr
}
