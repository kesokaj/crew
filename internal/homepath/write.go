package homepath

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// MkdirAll creates rel and any missing parent folders inside home without following links. If any
// part of rel is a symlink or a non-directory file, ErrNotRegular is returned.
func MkdirAll(home, rel string, perm os.FileMode) error {
	if err := Valid(rel); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	return MkdirAllIn(root, rel, perm)
}

// MkdirAllIn creates rel and any missing parent folders inside root without following links.
func MkdirAllIn(root *os.Root, rel string, perm os.FileMode) error {
	if err := Valid(rel); err != nil {
		return err
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(cur)
		if err == nil {
			if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
				return fmt.Errorf("%s: %w", rel, ErrNotRegular)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := root.Mkdir(cur, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err = root.Lstat(cur)
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s: %w", rel, ErrNotRegular)
		}
	}
	return nil
}

// WriteFile writes data to rel inside home atomically at mode, through a temporary file beside it
// inside the pinned os.Root. No symlink in any parent folder is followed, and a symlink at rel is
// replaced rather than followed.
func WriteFile(home, rel string, data []byte, mode os.FileMode) error {
	if err := Valid(rel); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	return WriteFileIn(root, rel, data, mode)
}

// WriteFileIn writes data to rel inside root atomically at mode without following links.
func WriteFileIn(root *os.Root, rel string, data []byte, mode os.FileMode) error {
	if err := Valid(rel); err != nil {
		return err
	}
	dir := path.Dir(rel)
	if dir != "." {
		if err := MkdirAllIn(root, dir, 0o700); err != nil {
			return err
		}
	}
	var rb [8]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return err
	}
	tmpName := ".crew-" + path.Base(rel) + "-" + hex.EncodeToString(rb[:])
	tmpRel := tmpName
	if dir != "." {
		tmpRel = dir + "/" + tmpName
	}
	f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpRel) }()
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmpRel, rel)
}

// Remove deletes rel inside home without following links in any parent folder. Removing a missing
// file returns nil.
func Remove(home, rel string) error {
	if err := Valid(rel); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	parts := strings.Split(rel, "/")
	for i := range parts[:len(parts)-1] {
		fi, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s: %w", rel, ErrNotRegular)
		}
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
