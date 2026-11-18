package hosting

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/errdefs"

	"github.com/kesokaj/crew/internal/harness"
)

// errTooLong is a copy of the data that ran past its bound.
var errTooLong = errors.New("the copy ran too long")

// copyData copies a project's data folder before a deploy switches (19.3, R171, plan M19 D18), with
// the sandbox that serves paused, so the copy is one point in time, as a crash would leave it. It
// gives why it could not; the sandbox is unpaused whatever happened.
func (s *Service) copyData(ctx context.Context, projectID, sandbox, name string) string {
	// A sandbox that does not run, or is gone, writes nothing: its data is copied as it is.
	if resume, err := s.Runtime.Pause(ctx, sandbox); err != nil && !errdefs.IsNotFound(err) && !strings.Contains(err.Error(), "is not running") {
		return "the data could not be held still for its copy: " + err.Error()
	} else if err == nil {
		defer func() {
			if err := resume(context.WithoutCancel(ctx)); err != nil {
				s.Log.Error("unpausing a hosted sandbox after the copy of its data", "sandbox", sandbox, "error", err)
			}
		}()
	}
	dst := s.copyDir(projectID, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "the copy of the data: " + err.Error()
	}
	// The disk holds the database too: a copy needs the data's size and the margin a build keeps
	// (review of M19, finding 9; security review).
	size, err := treeSize(s.dataDir(projectID))
	if err != nil {
		return "the copy of the data: " + err.Error()
	}
	if free, err := harness.FreeSpace(filepath.Dir(dst)); err != nil || free < uint64(size)+harness.DiskMargin {
		return fmt.Sprintf("the copy of the data needs %d bytes and a margin of %d, and the disk has %d free", size, int64(harness.DiskMargin), free)
	}
	var notes []string
	err = copyTree(s.dataDir(projectID), dst, time.Now().Add(CopyTimeout), &notes)
	for _, n := range notes {
		s.logLine(projectID, "the copy of the data left out %s", n)
	}
	if err != nil {
		_ = os.RemoveAll(dst)
		if errors.Is(err, errTooLong) {
			return fmt.Sprintf("the copy of the data did not finish in %s", CopyTimeout)
		}
		return "the copy of the data: " + err.Error()
	}
	return ""
}

// copyTree copies the folder src into dst, which it makes when it is not there, through roots, so
// no link is followed out of either: a link is copied as a link, a hard link as a file of its own, a
// FIFO, a device or a socket is left out with a note. Past deadline it stops with errTooLong.
func copyTree(src, dst string, deadline time.Time, notes *[]string) error {
	sr, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer sr.Close()
	if err := os.Mkdir(dst, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	dr, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer dr.Close()
	var walk func(rel string) error
	walk = func(rel string) error {
		d, err := sr.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if time.Now().After(deadline) {
				return errTooLong
			}
			p := path.Join(rel, e.Name())
			fi, err := sr.Lstat(p)
			if err != nil {
				return err
			}
			switch m := fi.Mode(); {
			case m.IsDir():
				if err := dr.Mkdir(p, m.Perm()|0o700); err != nil {
					return err
				}
				if err := walk(p); err != nil {
					return err
				}
			case m&fs.ModeSymlink != 0:
				target, err := sr.Readlink(p)
				if err != nil {
					return err
				}
				if err := dr.Symlink(target, p); err != nil {
					return err
				}
			case m.IsRegular():
				if err := copyFile(sr, dr, p, m.Perm(), deadline); err != nil {
					return err
				}
			default:
				*notes = append(*notes, p+" ("+m.Type().String()+")")
			}
		}
		return nil
	}
	return walk(".")
}

// copyFile copies one plain file between roots, opened without following a link or waiting.
func copyFile(sr, dr *os.Root, p string, perm fs.FileMode, deadline time.Time) error {
	in, err := sr.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is no plain file", p)
	}
	out, err := dr.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, &untilDeadline{r: in, deadline: deadline})
	return errors.Join(err, out.Close())
}

// untilDeadline reads until its deadline, then fails: a large file is no way past the bound.
type untilDeadline struct {
	r        io.Reader
	deadline time.Time
}

func (u *untilDeadline) Read(p []byte) (int, error) {
	if time.Now().After(u.deadline) {
		return 0, errTooLong
	}
	return u.r.Read(p)
}

// replaceData empties the data folder, through a root, removing links and never following them,
// and copies a kept copy into it (plan M19 D15, D18).
func replaceData(data, copy string) error {
	if fi, err := os.Lstat(copy); err != nil || !fi.IsDir() {
		return fmt.Errorf("the copy %s is gone", filepath.Base(copy))
	}
	r, err := os.OpenRoot(data)
	if err != nil {
		return err
	}
	d, err := r.Open(".")
	if err != nil {
		r.Close()
		return err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	for _, n := range names {
		if err == nil {
			err = r.RemoveAll(n)
		}
	}
	r.Close()
	if err != nil {
		return err
	}
	var notes []string
	return copyTree(copy, data, time.Now().Add(CopyTimeout), &notes)
}

// pruneCopies keeps the newest keep copies of a project's data, and protect, which a roll back reads.
func (s *Service) pruneCopies(ctx context.Context, projectID string, keep int, protect string) {
	if keep <= 0 {
		keep = 3
	}
	dir := filepath.Join(s.projectDir(projectID), "copies")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var numbers []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() && strconv.Itoa(n) == e.Name() {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	slices.Reverse(numbers)
	for i, n := range numbers {
		name := strconv.Itoa(n)
		if i < keep || name == protect {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			s.Log.Warn("removing a copy of a hosted project's data", "project", projectID, "copy", name, "error", err)
		}
	}
}

// treeSize is the size of a folder's plain files, walked through a root.
func treeSize(dir string) (int64, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var n int64
	err = fs.WalkDir(root.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n, err
}
