package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// LoadAsk is what the server hands the updater once a build ended (16.2 step 4): the folder the
// build's files are in, under Builds, the hash of each file as the server read it out of the check
// sandbox, and the facts of the commit, which the running server read from Crew's project itself.
// Nothing in it came from the candidate's own program.
type LoadAsk struct {
	Version      string            `json:"version"`
	Dir          string            `json:"dir"`
	Hashes       map[string]string `json:"hashes"`
	Commit       string            `json:"commit"`
	Files        map[string]string `json:"files"`
	Agents       map[string]string `json:"agents,omitempty"`
	Runtime      map[string]string `json:"runtime"`
	Schema       int               `json:"schema"`
	ReadBy       int               `json:"readBy"`
	MachineSetup int               `json:"machineSetup"`
	Pins         map[string]string `json:"pins,omitempty"`
	Notes        string            `json:"notes"`
}

// Loaded is the answer to a load: the release folder's release.json, by its hash.
type Loaded struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifestSha256"`
}

// buildFiles are the names a build's folder must hold, each a plain file.
func buildFiles() []string {
	names := []string{release.DeployFile, release.BrowserFile}
	for _, n := range release.Images {
		names = append(names, release.ImagesDir+"/"+n+".tar")
	}
	return names
}

// Load makes the release folder of a build Crew made: it copies each file of the build into a folder
// of root's, checks that the copy has the hash the server read out of the sandbox, loads each image,
// which must name only its own tag, and writes release.json from the facts it was given and the
// hashes it made itself. A release that is there already, a file that is missing, a link, or a hash
// that is not the build's makes no folder; nothing that was loaded stays tagged.
func (u *Updater) Load(ctx context.Context, a LoadAsk) (*Loaded, error) {
	if !release.ValidVersion(a.Version) || strings.Contains(a.Version, "-dev.") {
		return nil, refuse("%q is not the version of a release", a.Version)
	}
	if u.Builds == "" {
		return nil, refuse("this updater takes no build: it has no folder of builds")
	}
	pg := a.Pins["postgres"]
	if pg == "" || strings.ContainsAny(pg, ":@/ ") {
		return nil, refuse("the build names no version of postgres's image")
	}
	// One load at a time, and none while a request runs: both read and change Docker's tags.
	u.mu.Lock()
	if u.current != nil || u.starting || u.loading {
		u.mu.Unlock()
		return nil, ErrBusy
	}
	u.loading = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.loading = false
		u.mu.Unlock()
	}()
	src, err := within(u.Builds, a.Dir)
	if err != nil {
		return nil, refuse("%v", err)
	}
	dst := filepath.Join(u.Releases, a.Version)
	if _, err := os.Lstat(dst); err == nil {
		return nil, refuse("the release %s is on this machine already: a version is built once", a.Version)
	}
	tmp := filepath.Join(u.Releases, "."+a.Version+".load")
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	// Root's alone until it is whole and renamed: the server's user reads release folders.
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(tmp, release.ImagesDir), 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return nil, refuse("the build's folder: %v", err)
	}
	defer root.Close()
	// The folder opened is the one checked, not one put in its place since (security review of M16 P3).
	if checked, err := os.Lstat(src); err != nil {
		return nil, err
	} else if opened, err := root.Stat("."); err != nil || !os.SameFile(checked, opened) {
		return nil, refuse("the build's folder changed while it was opened")
	}
	var total int64
	for _, name := range buildFiles() {
		st, err := root.Lstat(name)
		if err != nil {
			return nil, refuse("the build's %s: %v", name, err)
		}
		total += st.Size()
	}
	free := u.FreeSpace
	if free == nil {
		free = freeSpace
	}
	if room, err := free(u.Releases); err != nil {
		return nil, err
	} else if room < uint64(total)+loadMargin {
		return nil, refuse("the disk has %d bytes free, and the build's files need %d and a margin of %d", room, total, int64(loadMargin))
	}
	for _, name := range buildFiles() {
		want := a.Hashes[name]
		if want == "" {
			return nil, refuse("the build names no hash for %s", name)
		}
		got, err := copyIn(root, name, filepath.Join(tmp, name))
		if err != nil {
			return nil, refuse("the build's %s: %v", name, err)
		}
		if got != want {
			return nil, refuse("the build's %s has the hash %s, not the build's %s", name, short(got), short(want))
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, release.NotesFile), []byte(a.Notes), 0o644); err != nil {
		return nil, err
	}
	facts := release.Facts{Version: a.Version, Commit: a.Commit, BuiltAt: u.now(), Schema: a.Schema, ReadBy: a.ReadBy,
		MachineSetup: a.MachineSetup, Images: map[string]release.Image{}, Pins: a.Pins, Runtime: a.Runtime,
		Deploy: release.DeployFile, Browser: release.BrowserFile}
	var loaded []string
	for _, name := range release.Images {
		tag := name + ":" + a.Version
		if name == "postgres" {
			tag = "postgres:" + pg
		}
		path := filepath.Join(tmp, release.ImagesDir, name+".tar")
		id, err := u.Machine.LoadImage(ctx, path, tag)
		if err != nil {
			u.unload(loaded)
			return nil, fmt.Errorf("loading %s: %w", tag, err)
		}
		loaded = append(loaded, tag)
		// release.json names the config digest, which every image store keeps, not the id this
		// machine's store gave (D48).
		file, err := release.ReadImageFile(path)
		if err != nil {
			u.unload(loaded)
			return nil, fmt.Errorf("the image file of %s: %w", tag, err)
		}
		if file.Config != "" {
			if !file.AcceptsID(file.Config, id) {
				u.unload(loaded)
				return nil, fmt.Errorf("%s loaded as %s, which is not the image its file holds, %s", tag, id, file.Config)
			}
			id = file.Config
		}
		facts.Images[name] = release.Image{Tag: tag, ID: id}
	}
	m, err := release.WriteIn(tmp, facts, a.Files, a.Agents)
	if err != nil {
		u.unload(loaded)
		return nil, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		u.unload(loaded)
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		u.unload(loaded)
		return nil, err
	}
	ok = true
	f, err := release.Read(dst)
	if err != nil {
		return nil, err
	}
	if u.Log != nil {
		u.Log.Info("a build is loaded as a release", "version", m.Version, "commit", m.Commit)
	}
	return &Loaded{Version: m.Version, ManifestSHA256: f.ManifestSHA256}, nil
}

// unload removes the tags a load that failed gave the images it loaded; postgres's tag is shared with
// every release, and stays.
func (u *Updater) unload(tags []string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, t := range tags {
		if strings.HasPrefix(t, "postgres:") {
			continue
		}
		if err := u.Machine.Untag(ctx, t); err != nil && u.Log != nil {
			u.Log.Warn("removing the tag of a load that failed", "tag", t, "error", err)
		}
	}
}

// within is dir, which must be a folder directly under root and no link.
func within(root, dir string) (string, error) {
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) != filepath.Clean(root) || filepath.Base(clean) == "." || filepath.Base(clean) == ".." {
		return "", fmt.Errorf("%s is not a build's folder", dir)
	}
	st, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}
	return clean, nil
}

// copyIn copies the plain file name of root to dst and returns the hash of what it wrote. A link, a
// folder, or a name that leaves root is refused.
func copyIn(root *os.Root, name, dst string) (string, error) {
	st, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("it is not a plain file")
	}
	in, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("it is not a plain file")
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(out, h), in)
	if err := errors.Join(cerr, out.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// loadMargin is what a load leaves free on the disk, which also holds the database (16.10).
const loadMargin = 10 << 30

func freeSpace(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
