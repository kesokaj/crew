// Package release is a release of Crew as a folder on the machine (design 16.1): the images as
// files, release.json, which names every image file by its content hash and every file of the
// commit by its git hash, and gate.json once the gate passed (16.2). It also decides which kind a
// release is, from the files it changes (16.3). The updater and the server both read it; neither
// trusts a folder whose files no longer have the hashes in its release.json.
package release

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The files of a release folder.
const (
	ManifestFile = "release.json"
	GateFile     = "gate.json"
	NotesFile    = "notes.md"
	ImagesDir    = "images"
	PulledFile   = "pulled.json"
)

// Images are the parts of a release that are images, in the order they are loaded (16.1).
var Images = []string{"crew-server", "crew-caddy", "crew-runtime", "crew-updater", "postgres"}

// versionName is a release's number, or the name of a build from a checkout at no tag (16.11).
var versionName = regexp.MustCompile(`^(\d+\.\d+\.\d+)(-dev\.[0-9a-f]{7,40})?$`)

// ValidVersion reports whether v is a version's full name. No moving tag such as latest is one.
func ValidVersion(v string) bool { return versionName.MatchString(v) }

// Image is one image of a release: its tag, its id, and the file docker save wrote, by its hash.
type Image struct {
	Tag    string `json:"tag"`
	ID     string `json:"id"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Manifest is release.json.
type Manifest struct {
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	BuiltAt time.Time `json:"builtAt"`
	// ByHand is a build by `crew-ctl build` that is given to the updater without the gate (16.10).
	ByHand bool `json:"byHand,omitempty"`
	// Schema is the last schema step of the release's server; ReadBy is the lowest schema whose
	// server reads a database at it (16.6).
	Schema       int               `json:"schema"`
	ReadBy       int               `json:"readBy"`
	MachineSetup int               `json:"machineSetup"`
	Images       map[string]Image  `json:"images"`
	Pins         map[string]string `json:"pins,omitempty"`
	// Files is every file of the commit with its git hash, and Agents the model and effort lines of
	// each agent file, so the running version decides the kind without a checkout (16.3).
	Files  map[string]string `json:"files"`
	Agents map[string]string `json:"agents,omitempty"`
	// Runtime is every input of the runtime image by its sha256, and RuntimeSHA256 one hash of them
	// all: the live steps of the gate run at once only when they are the running release's (16.2 step
	// 5, since 0.51). Deploy is the deploy archive, by its hash (16.12).
	Runtime       map[string]string `json:"runtime,omitempty"`
	RuntimeSHA256 string            `json:"runtimeSha256,omitempty"`
	Deploy        *File             `json:"deploy,omitempty"`
	// Browser is the gate's browser tests with their packages, which the build installed in its
	// sandbox, by its hash (16.2 step 5, since 0.52).
	Browser *File `json:"browser,omitempty"`
}

// File is a file of a release folder by its hash.
type File struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// DeployFile is the deploy archive's name in a release folder, and BrowserFile the gate's browser tests'.
const (
	DeployFile  = "deploy.tar.gz"
	BrowserFile = "browser.tar"
)

// Gate is gate.json: what the gate ran on the release and whether it passed. It names the hash of
// the release.json it ran on, so a folder changed after the gate has no gate. The updater writes it,
// once for its own steps and the caller's, with the live part pending, and once more when the
// server's live result is in (16.2 step 5, since 0.52).
type Gate struct {
	Version         string     `json:"version"`
	ManifestSHA256  string     `json:"manifestSha256"`
	Passed          bool       `json:"passed"`
	At              time.Time  `json:"at"`
	Steps           []GateStep `json:"steps"`
	InstallTest     string     `json:"installTest"` // passed, failed, or skipped
	InstallTestNote string     `json:"installTestNote,omitempty"`
	// Live is the live part: pending until its result is in, then passed, failed, or skipped. A gate
	// written before 0.52 has none, and is what its Passed says.
	Live      string     `json:"live,omitempty"`
	LiveSteps []GateStep `json:"liveSteps,omitempty"`
	// Skipped names each step that did not run, with why: a release with one never installs by itself,
	// whatever its kind (16.3). Hold is why a suite measured worse: the release waits for Install.
	Skipped []string `json:"skipped,omitempty"`
	Hold    string   `json:"hold,omitempty"`
	// A gate.json written before 0.53 may name what its live steps cost, costUsd, and its lines an
	// amount: the field is left, and the lines are shown without it (Unpriced).
}

// The lines of a gate that a server before 0.53 wrote with what its live steps cost, as it wrote them
// (M16): the real ticket that reached Done, the real ticket over its budget, and each suite's line. A
// gate is shown without the amount, since Crew shows none (16.2, the operator's choice of report 9).
var (
	pricedDone   = regexp.MustCompile(`^(.+ reached Done on its checks), for \$\d+\.\d\d$`)
	pricedSpent  = regexp.MustCompile(`^(\S+) spent \$\d+\.\d\d of its \$\d+\.\d\d$`)
	pricedSuite  = regexp.MustCompile(`(: (?:not )?(?:worse|compared): [^;]*?) \(\$\d+\.\d\d\)(;|$)`)
	pricedHold   = "its live steps ran in a candidate runtime image whose code is not the running release's, which reports what its runs cost: Install decides"
	unpricedHold = "its live steps ran in a candidate runtime image whose code is not the running release's, and the steps rest on what its runs did and said: Install decides"
)

// Unpriced is a gate's line without the amount a server before 0.53 wrote into it, in the words a
// server since writes: what is shown, never what is stored. Any other text is left as it is, a
// parenthesised amount in it too: a step's detail holds output of code agents wrote, which is not
// Crew's to change.
func Unpriced(s string) string {
	switch {
	case s == pricedHold:
		return unpricedHold
	case pricedDone.MatchString(s):
		return pricedDone.ReplaceAllString(s, "${1}")
	case pricedSpent.MatchString(s):
		return pricedSpent.ReplaceAllString(s, "${1} stopped at its budget (before 0.53)")
	}
	return pricedSuite.ReplaceAllString(s, "${1}${2}")
}

// Unpriced is a copy of the gate as Versions, the notes, and a release's page show it: its lines
// without the amounts of Unpriced. The gate itself, which the updater decides on and writes back, is
// not changed (security review of a9532aa).
func (g *Gate) Unpriced() *Gate {
	if g == nil {
		return nil
	}
	out := *g
	cut := func(steps []GateStep) []GateStep {
		if steps == nil {
			return nil
		}
		c := make([]GateStep, len(steps))
		for i, st := range steps {
			st.Detail = Unpriced(st.Detail)
			c[i] = st
		}
		return c
	}
	out.Steps, out.LiveSteps = cut(g.Steps), cut(g.LiveSteps)
	if g.Skipped != nil {
		out.Skipped = make([]string, len(g.Skipped))
		for i, s := range g.Skipped {
			out.Skipped[i] = Unpriced(s)
		}
	}
	out.Hold = Unpriced(g.Hold)
	return &out
}

// The states of a gate's live part.
const (
	LivePending = "pending"
	LivePassed  = "passed"
	LiveFailed  = "failed"
	LiveSkipped = "skipped"
)

// InstallsBySelfAfterGate reports whether the gate lets a release install by itself: not when a step
// was skipped, nor when a suite holds it (16.2 step 5, 16.3).
func (g *Gate) InstallsBySelfAfterGate() bool {
	return g != nil && len(g.Skipped) == 0 && g.Hold == ""
}

// GateStep is one test the gate ran.
type GateStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Folder is a release folder that was read and checked.
type Folder struct {
	Dir            string
	Manifest       Manifest
	ManifestSHA256 string
	// Gate is the folder's gate.json when it names this release.json and passed, else nil. Pending is
	// it when it names this release.json and its live part waits: a pending gate is never passed.
	Gate    *Gate
	Pending *Gate
	// Pulled are the images crew-ctl release pull took from a registry on another installation, by
	// pulled.json: they are in Docker, and the folder has no file of them (16.12, D48).
	Pulled map[string]Pulled
}

// Pulled is an image a release pull took from a registry: the config digest it checked, which is
// release.json's id, and the manifest digest it pulled by, which the containerd store gives as the id.
type Pulled struct {
	Config   string `json:"config"`
	Manifest string `json:"manifest"`
	From     string `json:"from"`
}

// pulledFile is pulled.json.
type pulledFile struct {
	Version string            `json:"version"`
	Images  map[string]Pulled `json:"images"`
}

// Read reads the release folder dir: its release.json and its gate.json. It does not hash the
// images; Verify does.
func Read(dir string) (*Folder, error) {
	f, gate, err := read(dir)
	if err != nil {
		return nil, err
	}
	switch {
	case gate == nil:
	case gate.Live == LivePending:
		f.Pending = gate
	case gate.Passed && (gate.Live == "" || gate.Live == LivePassed || gate.Live == LiveSkipped):
		f.Gate = gate
	}
	return f, nil
}

// ReadGate reads the gate.json of the release folder dir that names its release.json, whatever its
// state: pending, passed, or failed, which Read leaves out, so Versions shows a gate that failed
// (16.2 step 6, since 0.52). It is nil when there is none.
func ReadGate(dir string) (*Gate, error) {
	_, gate, err := read(dir)
	return gate, err
}

// read reads the release folder dir's release.json, and its gate.json when that names this
// release.json and version.
func read(dir string) (*Folder, *Gate, error) {
	body, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if !ValidVersion(m.Version) {
		return nil, nil, fmt.Errorf("%s: %q is not a version", ManifestFile, m.Version)
	}
	if filepath.Base(dir) != m.Version {
		return nil, nil, fmt.Errorf("%s names %s, and its folder is %s", ManifestFile, m.Version, filepath.Base(dir))
	}
	sum := sha256.Sum256(body)
	f := &Folder{Dir: dir, Manifest: m, ManifestSHA256: hex.EncodeToString(sum[:])}
	if p, err := os.ReadFile(filepath.Join(dir, PulledFile)); err == nil {
		var pf pulledFile
		if err := json.Unmarshal(p, &pf); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", PulledFile, err)
		}
		if pf.Version != m.Version {
			return nil, nil, fmt.Errorf("%s names %s, and release.json %s", PulledFile, pf.Version, m.Version)
		}
		f.Pulled = pf.Images
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	g, err := os.ReadFile(filepath.Join(dir, GateFile))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var gate Gate
	if json.Unmarshal(g, &gate) != nil || gate.ManifestSHA256 != f.ManifestSHA256 || gate.Version != m.Version {
		return f, nil, nil
	}
	return f, &gate, nil
}

// Verify checks that every image of the release is there, in the folder, with the hash release.json
// names (16.4, step 2). A folder that fails is not installed.
func (f *Folder) Verify() error {
	for _, name := range Images {
		img, ok := f.Manifest.Images[name]
		if !ok {
			return fmt.Errorf("release %s has no image %s", f.Manifest.Version, name)
		}
		// Each image is named for its part and its version, and by its config digest, which every image
		// store keeps (D48).
		want := name + ":" + f.Manifest.Version
		if name == "postgres" {
			want = ""
		}
		if (want != "" && img.Tag != want) || (want == "" && !strings.HasPrefix(img.Tag, "postgres:")) || !strings.HasPrefix(img.ID, "sha256:") {
			return fmt.Errorf("the image %s of %s is named %q with the id %q, not %s with its id", name, f.Manifest.Version, img.Tag, img.ID, orAny(want))
		}
		// A pulled image has no file: pulled.json names what the pull checked, which must be this. A
		// folder that holds the file too was not made by a pull, and its pulled.json turns no check off.
		if pl, ok := f.Pulled[name]; ok {
			if pl.Config != img.ID || !digestShape.MatchString(pl.Manifest) {
				return fmt.Errorf("the image %s of %s was pulled as %s by %q, and release.json names %s", name, f.Manifest.Version, short(pl.Config), pl.Manifest, short(img.ID))
			}
			if p, err := f.within(img.File); err != nil {
				return err
			} else if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("the image %s of %s is named pulled in %s, and the folder holds its file", name, f.Manifest.Version, PulledFile)
			}
			continue
		}
		p, err := f.within(img.File)
		if err != nil {
			return err
		}
		got, err := fileSHA256(p)
		if err != nil {
			return fmt.Errorf("the image %s: %w", name, err)
		}
		if got != img.SHA256 {
			return fmt.Errorf("the image %s of %s has changed since it was built: its hash is %s, not %s", name, f.Manifest.Version, got[:12], short(img.SHA256))
		}
	}
	for what, d := range map[string]*File{"the deploy archive": f.Manifest.Deploy, "the browser tests": f.Manifest.Browser} {
		if d == nil {
			continue
		}
		p, err := f.within(d.File)
		if err != nil {
			return err
		}
		got, err := fileSHA256(p)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if got != d.SHA256 {
			return fmt.Errorf("%s of %s has changed since it was built: its hash is %s, not %s", what, f.Manifest.Version, got[:12], short(d.SHA256))
		}
	}
	return nil
}

// BrowserPath is the release's browser tests, or an error when it has none (a release before 0.52).
func (f *Folder) BrowserPath() (string, error) {
	if f.Manifest.Browser == nil {
		return "", fmt.Errorf("the release %s has no browser tests of its gate", f.Manifest.Version)
	}
	return f.within(f.Manifest.Browser.File)
}

// DeployPath is the release's deploy archive, or an error when it has none (a release before 0.51).
func (f *Folder) DeployPath() (string, error) {
	if f.Manifest.Deploy == nil {
		return "", fmt.Errorf("the release %s has no deploy archive", f.Manifest.Version)
	}
	return f.within(f.Manifest.Deploy.File)
}

// ImagePath is the file of an image of the release.
func (f *Folder) ImagePath(name string) (string, error) {
	img, ok := f.Manifest.Images[name]
	if !ok {
		return "", fmt.Errorf("release %s has no image %s", f.Manifest.Version, name)
	}
	return f.within(img.File)
}

// within is a path the manifest names, refused when it leaves the folder.
func (f *Folder) within(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("%s names %q, which is not in its folder", ManifestFile, rel)
	}
	return filepath.Join(f.Dir, clean), nil
}

func orAny(want string) string {
	if want == "" {
		return "postgres:<version>"
	}
	return want
}

// Owned checks, on the machine, that the folder and every file of it is root's and that nobody else
// may write it: the gate's verdict in it is believed only so (16.8).
func (f *Folder) Owned() error {
	return filepath.WalkDir(f.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link: a release folder holds files only", path)
		}
		uid, ok := ownerOf(info)
		if !ok || uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is not root's alone: a release folder is root's, and nobody else may write it", path)
		}
		return nil
	})
}

// ownerOf is the user id that owns a file, from the system; a test stands in for it.
var ownerOf = func(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func fileSHA256(path string) (string, error) {
	r, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// List reads every release folder under root, oldest version first. A folder that cannot be read is
// returned in bad with the reason, and left out.
func List(root string) (folders []*Folder, bad map[string]error, err error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	bad = map[string]error{}
	for _, e := range entries {
		if !e.IsDir() || !ValidVersion(e.Name()) {
			continue
		}
		f, err := Read(filepath.Join(root, e.Name()))
		if err != nil {
			bad[e.Name()] = err
			continue
		}
		folders = append(folders, f)
	}
	sort.Slice(folders, func(i, j int) bool { return Less(folders[i].Manifest.Version, folders[j].Manifest.Version) })
	return folders, bad, nil
}

// Less orders versions by their numbers; a build from a checkout comes after the release of the same
// number, by name.
func Less(a, b string) bool {
	na, nb := numbers(a), numbers(b)
	for i := range na {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	return a < b
}

func numbers(v string) [3]int {
	var n [3]int
	m := versionName.FindStringSubmatch(v)
	if m == nil {
		return n
	}
	fmt.Sscanf(m[1], "%d.%d.%d", &n[0], &n[1], &n[2])
	return n
}

// Facts are what `crew-ctl build` gathered on the machine, which Write turns into release.json: the
// lines of `git ls-tree -r <commit>`, and the model and effort lines of the agent files.
type Facts struct {
	Version      string
	Commit       string
	BuiltAt      time.Time
	ByHand       bool
	Schema       int
	ReadBy       int
	MachineSetup int
	Images       map[string]Image // tag and id; Write hashes the files
	Pins         map[string]string
	// Runtime is the runtime image's inputs (RuntimeInputs), and Deploy the deploy archive's name in
	// the folder and Browser the browser tests', which Write hashes; "" is none.
	Runtime map[string]string
	Deploy  string
	Browser string
}

var lsTreeLine = regexp.MustCompile(`^[0-7]{6} (blob|commit|tree) ([0-9a-f]{40,64})\t(.+)$`)

// ParseTree reads the lines of `git ls-tree -r`: each file with its git hash.
func ParseTree(r io.Reader) (map[string]string, error) {
	files := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		m := lsTreeLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("not a line of git ls-tree: %q", line)
		}
		files[m[3]] = m[2]
	}
	return files, sc.Err()
}

// agentLine is a line of an agent file's front matter that sets its model or its effort.
var agentLine = regexp.MustCompile(`^(model|effort):`)

// AgentFile reports whether path is an agent definition, whose model and effort lines are settings
// (16.3, 16.8), not text.
func AgentFile(path string) bool {
	return strings.HasSuffix(path, ".md") && (strings.HasPrefix(path, "harness/agents/") || strings.HasPrefix(path, "baseline/agents/"))
}

// AgentSettings is the model and effort lines of an agent file's front matter, one string.
func AgentSettings(body []byte) string {
	var out []string
	lines := strings.Split(string(body), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if l == "---" {
			break
		}
		if agentLine.MatchString(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// Write writes release.json into dir from facts, the files of the commit, and the agents' lines,
// hashing each image file it names. It returns the manifest.
func Write(dir string, facts Facts, files, agents map[string]string) (*Manifest, error) {
	if filepath.Base(dir) != facts.Version {
		return nil, fmt.Errorf("the folder of %s must be named %s", facts.Version, facts.Version)
	}
	return WriteIn(dir, facts, files, agents)
}

// WriteIn is Write into a folder of another name, which is renamed to the version once it is whole:
// the updater's load (16.2 step 4).
func WriteIn(dir string, facts Facts, files, agents map[string]string) (*Manifest, error) {
	if !ValidVersion(facts.Version) {
		return nil, fmt.Errorf("%q is not a version", facts.Version)
	}
	m := Manifest{Version: facts.Version, Commit: facts.Commit, BuiltAt: facts.BuiltAt.UTC(), ByHand: facts.ByHand,
		Schema: facts.Schema, ReadBy: facts.ReadBy, MachineSetup: facts.MachineSetup, Images: map[string]Image{},
		Pins: facts.Pins, Files: files, Agents: agents}
	if facts.Runtime != nil {
		m.Runtime, m.RuntimeSHA256 = facts.Runtime, RuntimeSHA256(facts.Runtime)
	}
	f := &Folder{Dir: dir}
	for _, x := range []struct {
		name string
		to   **File
	}{{facts.Deploy, &m.Deploy}, {facts.Browser, &m.Browser}} {
		if x.name == "" {
			continue
		}
		p, err := f.within(x.name)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return nil, err
		}
		*x.to = &File{File: x.name, SHA256: sum, Bytes: st.Size()}
	}
	for _, name := range Images {
		img, ok := facts.Images[name]
		if !ok {
			return nil, fmt.Errorf("the image %s is missing", name)
		}
		if img.File == "" {
			img.File = ImagesDir + "/" + name + ".tar"
		}
		p, err := f.within(img.File)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if img.SHA256, err = fileSHA256(p); err != nil {
			return nil, err
		}
		img.Bytes = st.Size()
		m.Images[name] = img
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return &m, writeFile(filepath.Join(dir, ManifestFile), append(body, '\n'))
}

// WriteGate writes gate.json for the folder, bound to its release.json.
func (f *Folder) WriteGate(g Gate) error {
	g.Version, g.ManifestSHA256 = f.Manifest.Version, f.ManifestSHA256
	body, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(f.Dir, GateFile), append(body, '\n'))
}

// writeFile writes through a temporary file, so a reader never sees half of it.
func writeFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
