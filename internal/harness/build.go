package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// Builder builds a release of Crew in a check sandbox (16.2 step 4), and runs the gate's make check
// on its tree in another (16.2 step 5): Kata in a microVM, Fake in a test.
type Builder interface {
	Build(ctx context.Context, spec BuildSpec) (BuildResult, error)
	// MakeCheck runs make check on the tree: only Version, Tree, Timeout, Sizing, and Recorded of the
	// spec count, and its result's Output is the end of what it printed.
	MakeCheck(ctx context.Context, spec BuildSpec) (BuildResult, error)
}

// BuildSpec is one build of a release.
type BuildSpec struct {
	Version string
	// Tree is the commit, as git archive writes it.
	Tree []byte
	// Script is deploy/release.sh of the running release's deploy archive, never the tree's own.
	Script []byte
	// Agy is agy's tarball on the machine, which the server downloaded and checked against the tree's
	// versions.lock: the build is given it, and its sandbox reaches no storage.googleapis.com (12.8,
	// since 0.57). MakeCheck reads none.
	Agy string
	// BaseRuntime is the running release's images/crew-runtime.tar on the machine when SameRuntime
	// holds between the running release (BaseVersion) and the candidate commit, so the build sandbox
	// can layer the new version stamp on top of it instead of rebuilding the runtime image from scratch.
	BaseRuntime string
	BaseVersion string
	// Out is the folder on the machine the build's files are copied to; it must not exist yet.
	Out     string
	Timeout time.Duration
	Sizing  orchestrator.Sizing
	// Recorded is told once the sandbox's row exists, which the capacity check counts, before its
	// microVM starts: the caller holds the check's lock until then (13.10).
	Recorded func()
}

// BuildResult is what a build left: the hash of each file copied to Out, by its name there, and the
// end of its output, which is the build's log.
type BuildResult struct {
	Hashes map[string]string
	Output string
}

// BuildFiles are what a build writes in its folder: the image files, and what the runtime image got.
var BuildFiles = append(append([]string(nil), RuntimeFiles...), OtherFiles...)

// RuntimeFiles are what the build's first step leaves: the runtime image, which gets the operator's
// login in the gate's live steps, and what it got of agy. OtherFiles are the second step's. The first
// step runs only image/Dockerfile, and its files are copied out before the second starts the
// candidate's other Dockerfiles, so an escape from one of those cannot change the runtime image
// (security review of M16 P3; the operator's answer of 2026-10-20).
var (
	RuntimeFiles = []string{"images/crew-runtime.tar", "pins.json"}
	OtherFiles   = []string{"images/crew-server.tar", "images/crew-caddy.tar", "images/crew-updater.tar", "images/postgres.tar", "browser.tar"}
)

// MaxBuildFile bounds one file a build leaves, and MaxBuildTotal all of them: the runtime image is the
// largest, a few GB. A copy leaves DiskMargin free on the disk, which also holds the database (16.10).
const (
	MaxBuildFile = 12 << 30
	DiskMargin   = 10 << 30
)

// MaxBuildTotal is a var so a test can lower it.
var MaxBuildTotal int64 = 20 << 30

const (
	buildTree        = "tree.tar"
	buildScript      = "release.sh"
	buildBaseRuntime = "base-runtime.tar"
	// buildAgy is the folder of agy's tarball in the home, which release.sh gives the runtime image's
	// build as its context agy.
	buildAgy = cli.AgyName
)

// buildVersion is the number of a release Crew builds, which goes into the build's command line.
var buildVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// Build starts a build sandbox, which is Sources with its own Docker daemon and holds nothing of the
// operator's, runs the given script in it on the tree, and copies the files it made to spec.Out. A
// file that is not there, is no plain file, or is larger than MaxBuildFile fails the build; nothing
// of the sandbox's home but those files is read.
func (k *Kata) Build(ctx context.Context, spec BuildSpec) (BuildResult, error) {
	if len(spec.Tree) == 0 || len(spec.Tree) > MaxTree || len(spec.Script) == 0 || !buildVersion.MatchString(spec.Version) {
		return BuildResult{}, errors.New("a build needs a version X.Y.Z, a tree, and the script that builds it")
	}
	if fi, err := os.Lstat(spec.Agy); spec.Agy == "" || err != nil || !fi.Mode().IsRegular() {
		return BuildResult{}, errors.New("a build needs agy's tarball, which the server downloads and checks")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 90 * time.Minute
	}
	row, sb, err := k.Sandboxes.Start(ctx, sandboxes.Spec{
		Kind: sandboxes.KindBuild, Sizing: spec.Sizing, Life: spec.Timeout + 30*time.Minute,
		Prepare: func(_, home, _ string) error {
			if spec.Recorded != nil {
				spec.Recorded()
			}
			if err := writeHome(home, buildTree, spec.Tree, 0o644); err != nil {
				return err
			}
			agy, err := os.Open(spec.Agy)
			if err != nil {
				return err
			}
			defer agy.Close()
			// The home may be on another disk than the server's builds: it keeps DiskMargin free too.
			fi, err := agy.Stat()
			if err != nil {
				return err
			}
			free, err := FreeSpace(home)
			if err != nil {
				return err
			}
			if free < uint64(fi.Size())+DiskMargin {
				return fmt.Errorf("the disk of the sandbox's home has %d bytes free, and agy's tarball needs %d and a margin of %d", free, fi.Size(), int64(DiskMargin))
			}
			if err := writeHomeFrom(home, buildAgy+"/"+cli.AgyFile, agy, 0o644); err != nil {
				return err
			}
			if spec.BaseRuntime != "" && buildVersion.MatchString(spec.BaseVersion) {
				if baseFile, berr := os.Open(spec.BaseRuntime); berr == nil {
					defer baseFile.Close()
					if bfi, serr := baseFile.Stat(); serr == nil && bfi.Mode().IsRegular() {
						if bfree, ferr := FreeSpace(home); ferr == nil && bfree >= uint64(bfi.Size())+DiskMargin {
							_ = writeHomeFrom(home, buildBaseRuntime, baseFile, 0o644)
						}
					}
				}
			}
			return writeHome(home, buildScript, spec.Script, 0o755)
		},
	})
	if err != nil {
		return BuildResult{}, fmt.Errorf("the build sandbox: %w", err)
	}
	defer k.remove(row.ID)
	// One deadline for both steps; each exec gets what is left of it.
	deadline := time.Now().Add(spec.Timeout)
	res := BuildResult{Hashes: map[string]string{}}
	first := "set -e; mkdir -p ~/work && tar -xf ~/" + buildTree + " -C ~/work && rm -f ~/" + buildTree +
		" && exec bash ~/" + buildScript + " build-runtime --tree ~/work --version " + spec.Version + " --out ~/out --agy ~/" + buildAgy
	if spec.BaseRuntime != "" && buildVersion.MatchString(spec.BaseVersion) {
		first = "set -e; mkdir -p ~/work && tar -xf ~/" + buildTree + " -C ~/work && rm -f ~/" + buildTree +
			" && if [ -f ~/" + buildBaseRuntime + " ] && ! grep -q BASE_RUNTIME ~/" + buildScript + "; then " +
			"mkdir -p ~/out/images && docker load -q -i ~/" + buildBaseRuntime + " && rm -f ~/" + buildBaseRuntime + " && " +
			"printf 'FROM crew-runtime:" + spec.BaseVersion + "\\nUSER root\\nRUN printf \"%%s\\\\n\" \"" + spec.Version + "\" > /crew/VERSION && jq --arg v \"" + spec.Version + "\" \".crew = \\$v\" /crew/versions.json > /crew/versions.json.new && mv /crew/versions.json.new /crew/versions.json && chmod 0644 /crew/VERSION /crew/versions.json\\nLABEL crew.version=\"" + spec.Version + "\"\\nUSER agent\\n' > ~/out/.Dockerfile.fast && " +
			"docker build --progress=plain -f ~/out/.Dockerfile.fast -t crew-runtime:" + spec.Version + " ~/out && rm -f ~/out/.Dockerfile.fast && " +
			"docker save --output ~/out/images/crew-runtime.tar crew-runtime:" + spec.Version + " && " +
			"(docker run --rm --entrypoint cat crew-runtime:" + spec.Version + " /crew/versions.json > ~/out/pins.json 2>/dev/null || echo '{}' > ~/out/pins.json); " +
			"else BASE_RUNTIME=~/" + buildBaseRuntime + " BASE_VERSION=" + spec.BaseVersion + " exec bash ~/" + buildScript + " build-runtime --tree ~/work --version " + spec.Version + " --out ~/out --agy ~/" + buildAgy + "; fi"
	}
	if err := k.buildStep(ctx, sb.Container, first, deadline, spec.Timeout, &res); err != nil {
		return res, err
	}
	home := k.Sandboxes.Home(row.ID)
	var total int64
	if err := copyOut(home, spec.Out, RuntimeFiles, res.Hashes, &total); err != nil {
		_ = os.RemoveAll(spec.Out)
		return res, fmt.Errorf("the build's files: %w", err)
	}
	second := "set -e; exec bash ~/" + buildScript + " build-rest --tree ~/work --version " + spec.Version + " --out ~/out"
	if err := k.buildStep(ctx, sb.Container, second, deadline, spec.Timeout, &res); err != nil {
		_ = os.RemoveAll(spec.Out)
		return res, err
	}
	if err := copyOut(home, spec.Out, OtherFiles, res.Hashes, &total); err != nil {
		_ = os.RemoveAll(spec.Out)
		return res, fmt.Errorf("the build's files: %w", err)
	}
	return res, nil
}

// MakeCheck runs the gate's make check on the tree in a sandbox of the kind build (16.2 step 5): Sources,
// its own Docker daemon for the tests' database, and nothing of the operator's. The tree's Makefile and
// tests are code agents wrote, and run only there.
func (k *Kata) MakeCheck(ctx context.Context, spec BuildSpec) (BuildResult, error) {
	if len(spec.Tree) == 0 || len(spec.Tree) > MaxTree || !buildVersion.MatchString(spec.Version) {
		return BuildResult{}, errors.New("a check needs a version X.Y.Z and a tree")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 90 * time.Minute
	}
	row, sb, err := k.Sandboxes.Start(ctx, sandboxes.Spec{
		Kind: sandboxes.KindBuild, Sizing: spec.Sizing, Life: spec.Timeout + 30*time.Minute,
		Prepare: func(_, home, _ string) error {
			if spec.Recorded != nil {
				spec.Recorded()
			}
			return writeHome(home, buildTree, spec.Tree, 0o644)
		},
	})
	if err != nil {
		return BuildResult{}, fmt.Errorf("the check sandbox: %w", err)
	}
	defer k.remove(row.ID)
	res := BuildResult{}
	line := "set -e; mkdir -p ~/work && tar -xf ~/" + buildTree + " -C ~/work && rm -f ~/" + buildTree + " && exec make -C ~/work check"
	err = k.buildStep(ctx, sb.Container, line, time.Now().Add(spec.Timeout), spec.Timeout, &res)
	return res, err
}

// buildStep runs one line of a build in its sandbox, within what is left until deadline, and adds the
// end of its output to the result's. args are the line's $0, $1, …: a value given so is never part of
// the line bash reads.
func (k *Kata) buildStep(ctx context.Context, container, line string, deadline time.Time, timeout time.Duration, res *BuildResult, args ...string) error {
	left := time.Until(deadline)
	if left < time.Second {
		return fmt.Errorf("the build ran longer than %s", timeout)
	}
	ectx, cancel := context.WithTimeout(ctx, left+time.Minute)
	defer cancel()
	code, out, err := k.Runtime.Exec(ectx, container, orchestrator.ExecSpec{
		Cmd:  append([]string{"timeout", "--kill-after=30s", fmt.Sprintf("%ds", int(left.Seconds())), "bash", "-lc", line}, args...),
		User: orchestrator.GuestUser, Env: []string{"HOME=/home/" + orchestrator.GuestUser},
	})
	res.Output = tail(res.Output + out)
	switch {
	case err != nil:
		return fmt.Errorf("the build sandbox: %w", err)
	case code == 124:
		return fmt.Errorf("the build ran longer than %s", timeout)
	case code != 0:
		return fmt.Errorf("the build ended with %d", code)
	}
	return nil
}

// copyOut copies the named files from the out folder of the sandbox's home to dst, which it makes
// when it is not there yet, and adds each one's hash to hashes as it is written, and its size to
// total. The home is the sandbox's to change, and the sandbox still runs, so it is opened as a
// root, where a name that leads out of it is refused; each file is opened without following a link
// and without waiting, and is checked once it is open: a plain file, within MaxBuildFile, and all of
// a build's within MaxBuildTotal and the room the disk has (security review of M16 P3).
func copyOut(home, dst string, names []string, hashes map[string]string, total *int64) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	var size int64
	for _, name := range names {
		f, err := root.OpenFile(path.Join("out", name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files[name] = f
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a plain file", name)
		}
		if st.Size() > MaxBuildFile {
			return fmt.Errorf("%s is larger than %d bytes", name, int64(MaxBuildFile))
		}
		size += st.Size()
	}
	if *total+size > MaxBuildTotal {
		return fmt.Errorf("the build's files are %d bytes, more than %d", *total+size, MaxBuildTotal)
	}
	// What an earlier step copied is on the disk already, so only this step's files need room.
	free, err := FreeSpace(filepath.Dir(dst))
	if err != nil {
		return err
	}
	if free < uint64(size)+DiskMargin {
		return fmt.Errorf("the disk has %d bytes free, and the build's files need %d and a margin of %d", free, size, int64(DiskMargin))
	}
	// The first step makes the folder, which must not be there yet; the second adds to it.
	if len(hashes) == 0 {
		if err := os.Mkdir(dst, 0o750); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(dst, "images"), 0o750); err != nil {
			return err
		}
	}
	for _, name := range names {
		st, _ := files[name].Stat()
		h, err := copyHashed(files[name], filepath.Join(dst, name), st.Size())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		hashes[name] = h
	}
	*total += size
	return nil
}

// FreeSpace is how many bytes the disk of path has free for the server's user; a test stands in for
// it.
var FreeSpace = func(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// copyHashed copies in to a new file at path, at most size bytes, the size the file had when it was
// checked: a file that grew since, in a sandbox that still runs, is refused.
func copyHashed(in io.Reader, path string, size int64) (string, error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, size+1))
	if err := errors.Join(cerr, out.Close()); err != nil {
		return "", err
	}
	if n > size {
		return "", errors.New("it grew while it was copied")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
