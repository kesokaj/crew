package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The limits of a conversation's work folder, both ways (11.8, plan M20 D17): its files' bytes before
// gzip, and its entries.
const (
	FolderMaxBytes   = 200 << 20
	FolderMaxEntries = 20000
)

// FolderFile is the file beside a sandbox's home where the harness writes the work folder after a
// turn: outside the home, so the sandbox cannot reach it.
const FolderFile = "folder.tar.gz"

// ErrFolderLimit is returned for a work folder over its limits.
var ErrFolderLimit = errors.New("the work folder is over its limits")

// checked is called between the folder's check and its open; a test swaps the folder there.
var checked = func(home, rel string) {}

// SaveFolder writes a gzip tar of the folder rel of home to out, a home a sandbox wrote and may still
// be writing, so nothing in it is trusted (D17). It walks through os.Root, takes each entry by Lstat,
// keeps folders and regular files only, opens a file without following a link and without waiting,
// checks it is regular on the open handle, and writes modes as 0644 or 0755. A link, a FIFO, a device
// or a socket is left out and counted in skipped. Over FolderMaxBytes or FolderMaxEntries nothing is
// written and the error is ErrFolderLimit. A folder that is not there is an empty one.
func SaveFolder(home, rel, out string) (files int, size int64, skipped int, err error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return 0, 0, 0, err
	}
	defer root.Close()
	fi, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, 0, 0, emptyTar(out)
	case err != nil:
		return 0, 0, 0, err
	case !fi.IsDir():
		return 0, 0, 1, emptyTar(out)
	}
	checked(home, rel)
	dir, err := root.OpenRoot(rel)
	if err != nil {
		return 0, 0, 0, err
	}
	defer dir.Close()
	// What is opened is what was checked: a link to another folder of the home swapped in between, such
	// as the CLI's own, is refused, and the folder saved before stays (os.Root keeps it in the home).
	if st, err := dir.Stat("."); err != nil || !os.SameFile(fi, st) {
		return 0, 0, 0, errors.New("the work folder changed while it was read")
	}
	f, err := os.OpenFile(out+".part", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(out + ".part")
		}
	}()
	// The fastest level: a folder of 200 MB is written after every turn.
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	entries := 0
	err = fs.WalkDir(dir.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == "." {
			return nil
		}
		entries++
		if entries > FolderMaxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrFolderLimit, FolderMaxEntries)
		}
		st, err := dir.Lstat(p)
		if err != nil {
			return err
		}
		// A folder swapped for a link after it was listed is not walked into.
		if d.IsDir() && !st.IsDir() {
			skipped++
			return fs.SkipDir
		}
		switch {
		case st.Mode().IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: p + "/", Mode: 0o755, ModTime: st.ModTime().UTC().Truncate(time.Second)})
		case !st.Mode().IsRegular():
			skipped++
			return nil
		}
		fh, hs, err := openRegular(dir, p)
		if errors.Is(err, errNotRegular) {
			skipped++
			return nil
		}
		if err != nil {
			return err
		}
		defer fh.Close()
		if size+hs.Size() > FolderMaxBytes {
			return fmt.Errorf("%w: more than %d MB of files", ErrFolderLimit, FolderMaxBytes>>20)
		}
		mode := int64(0o644)
		if hs.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: p, Mode: mode, Size: hs.Size(),
			ModTime: hs.ModTime().UTC().Truncate(time.Second)}); err != nil {
			return err
		}
		// The file may grow or shrink after its Fstat: exactly its size is written, padded when it
		// shrank, so the tar stays whole and the limit holds.
		n, err := io.Copy(tw, io.LimitReader(fh, hs.Size()))
		if err != nil {
			return err
		}
		if n < hs.Size() {
			if _, err := io.CopyN(tw, zeros{}, hs.Size()-n); err != nil {
				return err
			}
		}
		size += hs.Size()
		files++
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if err = tw.Close(); err != nil {
		return 0, 0, 0, err
	}
	if err = gz.Close(); err != nil {
		return 0, 0, 0, err
	}
	if err = f.Close(); err != nil {
		return 0, 0, 0, err
	}
	return files, size, skipped, os.Rename(out+".part", out)
}

// keepFolder saves the folder rel of home after a turn into FolderFile beside the home, where the
// sandbox cannot reach it, and says where, or why it was not saved, or what was left out (D17).
func keepFolder(home, rel string) (file, note string) {
	out := filepath.Join(filepath.Dir(home), FolderFile)
	_, _, skipped, err := SaveFolder(home, rel, out)
	switch {
	case err != nil:
		return "", "the work folder of the last turn was not saved: " + err.Error()
	case skipped == 1:
		return out, "one link or special file was left out of the work folder"
	case skipped > 1:
		return out, fmt.Sprintf("%d links or special files were left out of the work folder", skipped)
	}
	return out, ""
}

// errNotRegular is an entry that is not a regular file when it is opened.
var errNotRegular = errors.New("not a regular file")

// openRegular opens p in dir for reading once, without following a link and without waiting, and
// checks on the open handle that it is a regular file: an entry swapped for a link or a FIFO after
// it was listed is refused, never followed or waited on (D17).
func openRegular(dir *os.Root, p string) (*os.File, fs.FileInfo, error) {
	f, err := dir.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		// A link is refused by the root when it leads out and by O_NOFOLLOW when it does not: either
		// way it is no regular file, and it was not followed.
		if st, lerr := dir.Lstat(p); errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENXIO) || (lerr == nil && !st.Mode().IsRegular()) {
			return nil, nil, errNotRegular
		}
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, errNotRegular
	}
	return f, st, nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// emptyTar writes the gzip tar of an empty folder.
func emptyTar(out string) error {
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = errors.Join(tw.Close(), gz.Close(), f.Close())
	return err
}

// PutFolder unpacks the gzip tar gz into the folder rel of home, which it makes: folders and regular
// files only, no name that leaves the folder, modes 0644 or 0755, and FolderMaxBytes and
// FolderMaxEntries counted while it writes (D17). Any other entry, a name that leaves, or a limit
// passed refuses the whole archive, and what was written of it is removed.
func PutFolder(home, rel string, gz []byte) (err error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	dir, err := root.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer dir.Close()
	if len(gz) == 0 {
		return nil
	}
	defer func() {
		if err != nil {
			dir.Close()
			root.RemoveAll(rel)
			root.MkdirAll(rel, 0o755)
		}
	}()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	var total int64
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		entries++
		if entries > FolderMaxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrFolderLimit, FolderMaxEntries)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || !fs.ValidPath(name) || filepath.IsAbs(name) || path.Clean(name) != name {
			return fmt.Errorf("the work folder names %q, which leaves it", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := dir.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size < 0 || total+h.Size > FolderMaxBytes {
				return fmt.Errorf("%w: more than %d MB of files", ErrFolderLimit, FolderMaxBytes>>20)
			}
			if d := path.Dir(name); d != "." {
				if err := dir.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if err := dir.Chmod(name, mode); err != nil {
				return err
			}
			total += n
		default:
			return fmt.Errorf("the work folder holds %q, which is not a folder or a regular file", h.Name)
		}
	}
}
