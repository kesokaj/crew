package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing/fstest"
	"time"
	"unicode"

	"github.com/kesokaj/crew/internal/db"
)

// Tree is what the running release reads from the commit of a release itself (16.2, 16.6, 16.12):
// the inputs of its runtime image, its schema, and its deploy archive. A release built in a check
// sandbox says nothing of these about itself: what came out of the sandbox is images, and the
// candidate's own program never writes its release.json.
type Tree struct {
	// Runtime is every file the runtime image is built from, by its sha256 (16.2 step 5).
	Runtime map[string]string
	// Schema is the last schema step, and ReadBy the lowest schema whose server reads a database
	// at it (16.6).
	Schema, ReadBy int
	// MachineSetup is the number of the machine setup it needs, from its versions.lock (16.4), and
	// Lock every KEY=VALUE of that file.
	MachineSetup int
	Lock         map[string]string
	// Deploy is the deploy archive (16.12).
	Deploy []byte
}

// MaxTreeFile is the largest file a tree may hold; Crew's largest is a few megabytes.
const MaxTreeFile = 64 << 20

// ReadTree reads a commit's tree, as git archive writes it, and what the release needs of it.
func ReadTree(r io.Reader) (*Tree, error) {
	fsys, err := tarFS(r)
	if err != nil {
		return nil, err
	}
	return TreeOf(fsys)
}

// TreeOf is ReadTree of a tree already read.
func TreeOf(fsys fs.FS) (*Tree, error) {
	rt, err := RuntimeInputs(fsys)
	if err != nil {
		return nil, fmt.Errorf("the inputs of the runtime image: %w", err)
	}
	steps, err := db.StepsFS(fsys, "internal/db/migrations")
	if err != nil {
		return nil, fmt.Errorf("the schema: %w", err)
	}
	var arch bytes.Buffer
	if err := DeployArchive(fsys, &arch); err != nil {
		return nil, fmt.Errorf("the deploy archive: %w", err)
	}
	body, err := fs.ReadFile(fsys, "versions.lock")
	if err != nil {
		return nil, err
	}
	lock := ParseLock(body)
	setup, err := strconv.Atoi(lock["MACHINE_SETUP"])
	if err != nil || setup < 1 {
		return nil, errors.New("versions.lock names no MACHINE_SETUP")
	}
	return &Tree{Runtime: rt, Schema: len(steps), ReadBy: db.ReadBy(steps), MachineSetup: setup, Lock: lock, Deploy: arch.Bytes()}, nil
}

// ParseLock is every KEY=VALUE of a versions.lock: the last line of a key wins, as for a shell and for
// Compose.
func ParseLock(body []byte) map[string]string {
	lock := map[string]string{}
	for _, l := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && !strings.HasPrefix(k, "#") {
			lock[k] = v
		}
	}
	return lock
}

// tarFS is a tree from a tar archive: its regular files, with their modes. Anything else, or a path
// that leaves the tree, is refused: git archive writes neither for Crew's repository.
func tarFS(r io.Reader) (fs.FS, error) {
	out := fstest.MapFS{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("the tree: %w", err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("the tree holds %s, which is not a file", name)
		}
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("the tree holds the path %q", h.Name)
		}
		if h.Size > MaxTreeFile {
			return nil, fmt.Errorf("the tree's file %s is larger than %d bytes", name, MaxTreeFile)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxTreeFile+1))
		if err != nil {
			return nil, err
		}
		out[name] = &fstest.MapFile{Data: body, Mode: fs.FileMode(h.Mode) & 0o777}
	}
}

// runtimeMains are the programs the runtime image builds from Go (image/Dockerfile): crew-run, and
// crew-harnessbuild, which turns harness/ into the image's home.
var runtimeMains = []string{"cmd/crew-run", "cmd/crew-harnessbuild"}

// RuntimeInputs is every file the runtime image is built from, by its sha256 (16.2 step 5): image/,
// versions.lock, go.mod and go.sum, the root .dockerignore, what harness/ holds that is not text for
// agents, and every Go file, assembly, header, and .syso file, and embedded file, of the packages of
// this module that runtimeMains are built from,
// for linux on amd64 without cgo, as the image builds them. The packages from outside the module
// are fixed by go.sum, and Go itself by versions.lock. Text under harness/ is not an input: it is
// data, and only text may differ for the live steps to run at once.
func RuntimeInputs(fsys fs.FS) (map[string]string, error) {
	out := map[string]string{}
	add := func(p string) error {
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[p] = hex.EncodeToString(sum[:])
		return nil
	}
	for _, p := range []string{"versions.lock", "go.mod", "go.sum"} {
		if err := add(p); err != nil {
			return nil, err
		}
	}
	// The runtime image warms /crew/cache from the lockfiles of ingress/, web/, and tools/e2e/browser/
	// so native sandbox checks (CREW_SANDBOX=1) run offline without fetching modules or packages.
	for _, p := range []string{"ingress/go.mod", "ingress/go.sum", "web/package.json", "web/package-lock.json",
		"tools/e2e/browser/package.json", "tools/e2e/browser/package-lock.json"} {
		if _, err := fs.Stat(fsys, p); err == nil {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	}
	// The build context is the tree's root, whose ignore file decides what the COPY lines take.
	if _, err := fs.Stat(fsys, ".dockerignore"); err == nil {
		if err := add(".dockerignore"); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{"image", "harness"} {
		err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || (dir == "harness" && TextFile(p)) {
				return err
			}
			return add(p)
		})
		if err != nil {
			return nil, err
		}
	}
	mod, err := modulePath(fsys)
	if err != nil {
		return nil, err
	}
	ctxt := treeContext(fsys)
	seen := map[string]bool{}
	todo := append([]string(nil), runtimeMains...)
	for len(todo) > 0 {
		dir := todo[0]
		todo = todo[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		pkg, err := ctxt.ImportDir("/"+dir, 0)
		if err != nil {
			return nil, fmt.Errorf("the package %s: %w", dir, err)
		}
		// The image builds without cgo: a file go build would refuse, or link only with cgo, is no input
		// to leave out quietly.
		if n := len(pkg.CFiles) + len(pkg.CXXFiles) + len(pkg.MFiles) + len(pkg.FFiles) + len(pkg.SwigFiles) + len(pkg.SwigCXXFiles); n > 0 {
			return nil, fmt.Errorf("the package %s holds C, C++, Objective-C, Fortran, or SWIG files, which the runtime image does not build", dir)
		}
		files := append(append(append(append([]string(nil), pkg.GoFiles...), pkg.SFiles...), pkg.HFiles...), pkg.SysoFiles...)
		for _, f := range files {
			if err := add(path.Join(dir, f)); err != nil {
				return nil, err
			}
		}
		for _, pat := range pkg.EmbedPatterns {
			matched, err := embedded(fsys, dir, pat)
			if err != nil {
				return nil, fmt.Errorf("the package %s embeds %s: %w", dir, pat, err)
			}
			for _, p := range matched {
				if err := add(p); err != nil {
					return nil, err
				}
			}
		}
		for _, imp := range pkg.Imports {
			if rest, ok := strings.CutPrefix(imp, mod+"/"); ok {
				todo = append(todo, rest)
			}
		}
	}
	return out, nil
}

var moduleLine = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

func modulePath(fsys fs.FS) (string, error) {
	body, err := fs.ReadFile(fsys, "go.mod")
	if err != nil {
		return "", err
	}
	m := moduleLine.FindSubmatch(body)
	if m == nil {
		return "", errors.New("go.mod names no module")
	}
	return string(m[1]), nil
}

// treeContext is go/build on the tree, at the paths "/dir": no GOROOT, no GOPATH, and no go command,
// so it reads one directory at a time and resolves no import itself.
func treeContext(fsys fs.FS) *build.Context {
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled, ctxt.GOROOT, ctxt.GOPATH = "linux", "amd64", false, "", ""
	rel := func(p string) string {
		p = strings.TrimPrefix(path.Clean(p), "/")
		if p == "" {
			return "."
		}
		return p
	}
	ctxt.JoinPath = func(elem ...string) string { return path.Join(elem...) }
	ctxt.IsAbsPath = func(p string) bool { return strings.HasPrefix(p, "/") }
	ctxt.SplitPathList = func(string) []string { return nil }
	ctxt.HasSubdir = func(root, dir string) (string, bool) { return "", false }
	ctxt.IsDir = func(p string) bool {
		st, err := fs.Stat(fsys, rel(p))
		return err == nil && st.IsDir()
	}
	ctxt.ReadDir = func(dir string) ([]fs.FileInfo, error) {
		ents, err := fs.ReadDir(fsys, rel(dir))
		if err != nil {
			return nil, err
		}
		var out []fs.FileInfo
		for _, e := range ents {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			out = append(out, info)
		}
		return out, nil
	}
	ctxt.OpenFile = func(p string) (io.ReadCloser, error) { return fsys.Open(rel(p)) }
	return &ctxt
}

// embedded is what a //go:embed pattern of the package in dir names: each file, and every file below
// a directory it names, as go:embed takes them (a directory leaves out names that begin with . or _,
// unless the pattern begins with all:).
func embedded(fsys fs.FS, dir, pat string) ([]string, error) {
	all := strings.HasPrefix(pat, "all:")
	pat = strings.TrimPrefix(pat, "all:")
	matches, err := fs.Glob(fsys, path.Join(dir, pat))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errors.New("it matches no file")
	}
	var out []string
	for _, m := range matches {
		err := fs.WalkDir(fsys, m, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != m && !all && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RuntimeSHA256 is one hash of the runtime image's inputs, one line for each path: equal inputs give
// an equal hash, and so do only equal inputs while no path holds a newline, which SameRuntime checks.
func RuntimeSHA256(inputs map[string]string) string {
	paths := make([]string, 0, len(inputs))
	for p := range inputs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s  %s\n", inputs[p], p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SameRuntime reports whether next's runtime image is built from what running's was (16.2 step 5).
// A release that records no inputs, such as one built before 0.51, is never the same: the hash of
// no inputs is never "". Nor is one with a control character in a path: RuntimeSHA256 writes one line
// for each path, so a path with a newline could make other files hash as the running release's (security
// review of M16 P5).
func SameRuntime(running, next Manifest) bool {
	for p := range next.Runtime {
		if oddPath(p) {
			return false
		}
	}
	return running.RuntimeSHA256 == next.RuntimeSHA256 && RuntimeSHA256(next.Runtime) == next.RuntimeSHA256
}

// oddPath is a path that holds a control character, or one that ends a line or a paragraph.
func oddPath(p string) bool {
	return strings.ContainsFunc(p, func(r rune) bool {
		return unicode.Is(unicode.C, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	})
}

// RuntimeDiff is every input that was added, changed, or removed, for the diff Run the live gate
// shows first, and every input whose path is never the same runtime, whose hash may be the running
// release's: without it, such a release would wait for a press that is never offered (review of M16, F1).
func RuntimeDiff(running, next Manifest) []string {
	var out []string
	for p, h := range next.Runtime {
		if running.Runtime[p] != h || oddPath(p) {
			out = append(out, p)
		}
	}
	for p := range running.Runtime {
		if _, ok := next.Runtime[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Why a gate's diff is empty while the runtime is not the same, for the screen (review of M16, F1).
const (
	// NoDiffNoInputs is a release that records no runtime inputs, one built before 0.51.
	NoDiffNoInputs = "noInputs"
	// NoDiffBadHash is a release whose hash of its runtime inputs is not the hash of the inputs it records.
	NoDiffBadHash = "badHash"
)

// NoDiff is why no diff is shown to press on although SameRuntime is false, or "": a candidate whose
// recorded hash is not its inputs' has none, whatever its inputs, since the press binds to that hash
// (security review of ef7a82a..4a601e2); else an empty RuntimeDiff, where with equal inputs and no odd
// path only a recorded hash that is not its inputs' makes them not the same.
func NoDiff(running, next Manifest) string {
	if len(next.Runtime) > 0 && RuntimeSHA256(next.Runtime) != next.RuntimeSHA256 {
		return NoDiffBadHash
	}
	if SameRuntime(running, next) || len(RuntimeDiff(running, next)) > 0 {
		return ""
	}
	if len(next.Runtime) == 0 {
		return NoDiffNoInputs
	}
	return NoDiffBadHash
}

// DeployFiles are the deploy files of a release (16.11, 16.12): crew-ctl and what it needs once it
// is installed, with the script that builds a release. The archive holds them at their paths in the
// repository, so crew-ctl unpacked from it finds them as from a checkout. It holds nothing else of
// Crew's source.
var DeployFiles = []string{"deploy/crew-ctl", "deploy/release.sh", "deploy/host-setup.sh", "deploy/proof.sh",
	"deploy/compose.yaml", "deploy/db/", "deploy/lib/", "deploy/firewall/", "versions.lock"}

// DeployHashes are the hashes of the files of a deploy archive, by their paths in the repository: what
// the deploy files on the machine are compared with (16.11, since 0.55). Only regular files count; an
// archive that cannot be read is an error.
func DeployHashes(r io.Reader) (map[string]string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, io.LimitReader(tr, 16<<20)); err != nil {
			return nil, err
		}
		out[h.Name] = hex.EncodeToString(sum.Sum(nil))
	}
}

// DeployRecord is the file in the releases folder where crew-ctl's install records the hash of each
// deploy file it put in place, by its path in the repository (16.11, since 0.55).
const DeployRecord = "deploy-files.json"

// deployName is a name crew-ctl reads under deploy/db, deploy/lib, and deploy/firewall.
var deployName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DeployArchive writes the deploy archive of a tree: a tar.gz of DeployFiles in the order of their
// names, each file root's, with the mode 0755 when the tree has it executable and 0644 otherwise,
// and no time. The same tree gives the same bytes, from a checkout or from git archive.
func DeployArchive(fsys fs.FS, w io.Writer) error {
	var names []string
	for _, p := range DeployFiles {
		if !strings.HasSuffix(p, "/") {
			names = append(names, p)
			continue
		}
		err := fs.WalkDir(fsys, strings.TrimSuffix(p, "/"), func(q string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				// crew-ctl reads, as root, one plain name under each folder: any other fails the build
				// rather than being left out of what crew-ctl puts in place (round 2 of the security
				// review of 864fb57..97382bc).
				if rest := strings.TrimPrefix(q, p); strings.Contains(rest, "/") || !deployName.MatchString(rest) {
					return fmt.Errorf("%s: crew-ctl reads only plain names one level under %s", q, p)
				}
				names = append(names, q)
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	sort.Strings(names)
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, n := range names {
		st, err := fs.Stat(fsys, n)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file", n)
		}
		body, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
