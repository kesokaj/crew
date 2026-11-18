package homepath

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// ErrNotRegular is returned for a path that is a link in any of its parts, or is not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is returned for a file over the limit.
var ErrTooLarge = errors.New("larger than the limit")

// ReadFile reads rel from home, a home a sandbox wrote and may still be writing: nothing in it is
// trusted. Every folder on the way is checked not to be a link, and the file is opened once, without
// following a link and without waiting, then checked on the open handle. So a file swapped for a
// link or a FIFO after a check is refused, not followed or waited on. At most max bytes are read.
func ReadFile(home, rel string, max int64) ([]byte, error) {
	f, err := Open(home, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: %w (%d bytes)", rel, ErrTooLarge, max)
	}
	return data, nil
}

// Open opens rel in home for reading with the checks of ReadFile: no link in any part of its path,
// opened once without following a link or waiting, and a regular file on the open handle. The
// caller reads it as it grows, such as a spool (12.4), and closes it.
func Open(home, rel string) (*os.File, error) {
	if err := Valid(rel); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parts := strings.Split(rel, "/")
	for i := range parts[:len(parts)-1] {
		fi, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return nil, fmt.Errorf("%s: %w", rel, ErrNotRegular)
		}
	}
	// A link is named as one; the open below refuses one that appears after this check.
	if fi, err := root.Lstat(rel); err != nil {
		return nil, err
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %w", rel, ErrNotRegular)
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s: %w", rel, ErrNotRegular)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s: %w", rel, ErrNotRegular)
	}
	return f, nil
}
