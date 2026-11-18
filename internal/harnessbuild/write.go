package harnessbuild

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write puts the skeleton's files under dir. dir is new or empty: the build
// writes a whole home skeleton, never into one that already holds files,
// so what is there afterwards is exactly the plan. Every file gets its
// planned mode and every directory 0755, whatever the caller's umask, so
// two builds of one harness give the same tree.
func Write(dir string, files []File) error {
	switch entries, err := os.ReadDir(dir); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%s is not empty: the skeleton is written into a new or empty directory", dir)
	}
	if err := mkdirs(dir); err != nil {
		return err
	}
	for _, f := range files {
		if !filepath.IsLocal(f.Rel) {
			return fmt.Errorf("%s leaves the skeleton", f.Rel)
		}
		dest := filepath.Join(dir, f.Rel)
		if err := mkdirs(filepath.Dir(dest)); err != nil {
			return err
		}
		if err := writeNew(dest, f); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}
	return nil
}

// mkdirs makes dir and every missing parent with mode 0755.
func mkdirs(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	if err := mkdirs(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return os.Chmod(dir, 0o755)
}

// writeNew creates dest, which must not exist yet: O_EXCL, so nothing at the
// path is written through.
func writeNew(dest string, f File) error {
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.Write(f.Data); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(f.Mode.Perm()); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
