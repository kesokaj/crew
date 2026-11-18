package harnessbuild

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Source is where the harness's files are read from: context/, skills/,
// agents/, commands/, workflows/ and rules/, addressed with slash-separated
// paths.
type Source struct {
	FS fs.FS
	// Dir is the tree's path on disk, or "" for a tree in memory (the
	// tests). Only a tree on disk can hold a link, so the link refusals run
	// when Dir is set.
	Dir string
}

// DirSource reads the tree at dir.
func DirSource(dir string) Source {
	return Source{FS: os.DirFS(dir), Dir: dir}
}

// path names a file for a message: its disk path for a tree on disk, its
// path inside the source otherwise.
func (s Source) path(name string) string {
	if s.Dir == "" {
		return name
	}
	return filepath.Join(s.Dir, filepath.FromSlash(name))
}

// readRegular reads a file the source ships, refusing a link or anything
// that is not a regular file. checkManagedTree covers the managed directories
// and every entry in them; this guards the one read it cannot reach,
// skills/<name>/SKILL.md, because skills/<name> may itself be a link that
// os.DirFS follows.
func (s Source) readRegular(name string) ([]byte, error) {
	var info fs.FileInfo
	var err error
	if s.Dir != "" {
		info, err = os.Lstat(s.path(name))
	} else {
		info, err = fs.Stat(s.FS, name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink — the harness ships regular files only", s.path(name))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file — the harness ships regular files only", s.path(name))
	}
	return fs.ReadFile(s.FS, name)
}
