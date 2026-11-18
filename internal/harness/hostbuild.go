package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// ImageBuilder builds a hosted project's image (19.2 step 3): Kata in a microVM, Fake in a test.
type ImageBuilder interface {
	BuildImage(ctx context.Context, spec ImageSpec) (BuildResult, error)
}

// ImageSpec is one build of a project's image.
type ImageSpec struct {
	Project string // the project's id
	// Mode is the network mode of the project's checks (plan M19 D20, D41): its hostable ticket's
	// check built the image so.
	Mode string
	// Tree is main's commit, as git archive writes it.
	Tree []byte
	// Dockerfile is hosting.dockerfile, a path in the tree, which the build gets as an argument.
	Dockerfile string
	// Out is the file on the machine the image's tar is copied to; its folder must not exist yet.
	Out     string
	Timeout time.Duration
	Sizing  orchestrator.Sizing
	// Recorded is told once the sandbox's row exists, which the capacity check counts (13.10).
	Recorded func()
}

// ImageFile is the tar a project's build leaves in its out folder.
const ImageFile = "image.tar"

// DockerfilePath is what hosting.dockerfile may be (plan M19 D35): a path in the tree, of plain
// characters, never leading out of it.
var DockerfilePath = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// DockerfileProblem says why name cannot be a project's Dockerfile, or "".
func DockerfileProblem(name string) string {
	if !DockerfilePath.MatchString(name) || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "hosting.dockerfile is a path in the project's tree, of letters, digits, '.', '_', '-' and '/'"
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || part == "" {
			return "hosting.dockerfile is a path in the project's tree, with no '..', '.' or empty part"
		}
	}
	return ""
}

// hostBuildScript is Crew's own line of a project's build, the same whatever the tree holds: the
// Dockerfile is its $1, never a word of it. The sandbox's proxy, in a mode that has one, reaches the
// build as build arguments, by name.
const hostBuildScript = `set -euo pipefail
mkdir -p ~/work ~/out && tar -xf ~/` + buildTree + ` -C ~/work && rm -f ~/` + buildTree + `
cd ~/work
for _ in $(seq 120); do docker info >/dev/null 2>&1 && break; sleep 1; done
args=()
for v in HTTP_PROXY http_proxy HTTPS_PROXY https_proxy NO_PROXY no_proxy; do [ -n "${!v:-}" ] && args+=(--build-arg "$v"); done
docker build --progress=plain "${args[@]}" -f "$1" -t crew-host-build .
docker save crew-host-build -o ~/out/` + ImageFile

// BuildImage builds a project's image in a sandbox of the kind host-build, which runs no agent, has
// its own Docker daemon, and holds nothing of the operator's, in the mode of the project's checks; it
// copies the saved tar to spec.Out. Nothing else of the sandbox's home is read.
func (k *Kata) BuildImage(ctx context.Context, spec ImageSpec) (BuildResult, error) {
	if len(spec.Tree) == 0 || len(spec.Tree) > MaxTree || spec.Out == "" {
		return BuildResult{}, errors.New("a project's build needs a tree and where its image goes")
	}
	if why := DockerfileProblem(spec.Dockerfile); why != "" {
		return BuildResult{}, errors.New(why)
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 60 * time.Minute
	}
	row, sb, err := k.Sandboxes.Start(ctx, sandboxes.Spec{
		Kind: sandboxes.KindHostBuild, Project: spec.Project, Mode: spec.Mode, Sizing: spec.Sizing, Life: spec.Timeout + 30*time.Minute,
		Prepare: func(_, home, _ string) error {
			if spec.Recorded != nil {
				spec.Recorded()
			}
			return writeHome(home, buildTree, spec.Tree, 0o644)
		},
	})
	if err != nil {
		return BuildResult{}, fmt.Errorf("the build sandbox: %w", err)
	}
	defer k.remove(row.ID)
	res := BuildResult{Hashes: map[string]string{}}
	if err := k.buildStep(ctx, sb.Container, hostBuildScript, time.Now().Add(spec.Timeout), spec.Timeout, &res, "crew-host-build", spec.Dockerfile); err != nil {
		return res, err
	}
	h, err := copyImage(k.Sandboxes.Home(row.ID), spec.Out)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(spec.Out))
		return res, fmt.Errorf("the build's image: %w", err)
	}
	res.Hashes[ImageFile] = h
	return res, nil
}

// copyImage copies out/image.tar of a build's home to dst, making dst's folder, which must not be
// there yet. The home is the sandbox's to change, so the file is opened under a root, without
// following a link and without waiting, and checked once open: a plain file within MaxBuildFile, with
// DiskMargin left on the disk.
func copyImage(home, dst string) (string, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.OpenFile("out/"+ImageFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("it is not a plain file")
	}
	if st.Size() > MaxBuildFile {
		return "", fmt.Errorf("it is larger than %d bytes", int64(MaxBuildFile))
	}
	dir := filepath.Dir(dst)
	free, err := FreeSpace(filepath.Dir(dir))
	if err != nil {
		return "", err
	}
	if free < uint64(st.Size())+DiskMargin {
		return "", fmt.Errorf("the disk has %d bytes free, and the image needs %d and a margin of %d", free, st.Size(), int64(DiskMargin))
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return "", err
	}
	return copyHashed(f, dst, st.Size())
}
