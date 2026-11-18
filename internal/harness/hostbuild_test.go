package harness

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/kesokaj/crew/internal/sandboxes"
)

// A project's build (19.2 step 3, plan M19 D20, D35): a sandbox of the kind host-build, which holds
// nothing of the operator's, in the mode of the project's checks, runs Crew's own line whatever the
// tree holds; the Dockerfile reaches it as an argument, never as a word of the line, and the image's
// tar is copied out with its hash.
func TestAProjectsBuildRunsCrewsLineWithTheDockerfileAsAnArgument(t *testing.T) {
	defer roomy()()
	k, sb, rt := kata(t)
	recorded := 0
	rt.answer = func(string) (int, string, error) {
		home := sb.Home("run-0001")
		os.MkdirAll(filepath.Join(home, "out"), 0o755)
		os.WriteFile(filepath.Join(home, "out", ImageFile), []byte("the image"), 0o644)
		return 0, "built", nil
	}
	out := filepath.Join(t.TempDir(), "host-p1-1", ImageFile)
	res, err := k.BuildImage(context.Background(), ImageSpec{Project: "p1", Mode: "sources", Tree: []byte("tree"),
		Dockerfile: "deploy/Dockerfile", Out: out, Recorded: func() { recorded++ }})
	if err != nil {
		t.Fatal(err)
	}
	spec := sb.specs[0]
	if spec.Kind != sandboxes.KindHostBuild || spec.Project != "p1" || spec.Mode != "sources" || spec.CLI != "" || spec.Repos != nil ||
		spec.Gcloud || spec.Minimal || len(spec.Env) != 0 || recorded != 1 {
		t.Fatalf("the sandbox's spec: %+v (recorded %d)", spec, recorded)
	}
	cmd := rt.execs[0].Cmd
	at := slices.Index(cmd, "-lc")
	if at < 0 || cmd[at+1] != hostBuildScript || !slices.Equal(cmd[at+2:], []string{"crew-host-build", "deploy/Dockerfile"}) ||
		rt.execs[0].User != "agent" {
		t.Fatalf("the build ran %q", cmd)
	}
	if !strings.Contains(hostBuildScript, `-f "$1"`) || strings.Contains(hostBuildScript, "deploy/Dockerfile") {
		t.Fatal("the Dockerfile is not the line's $1")
	}
	if body, _ := os.ReadFile(out); string(body) != "the image" || res.Hashes[ImageFile] == "" || len(sb.removed) != 1 {
		t.Fatalf("the image %q, %v, removed %v", body, res.Hashes, sb.removed)
	}
}

// A Dockerfile's name that could be read as more than a path is refused before a sandbox starts.
func TestADockerfileNameIsAPathInTheTree(t *testing.T) {
	for name, ok := range map[string]bool{
		"Dockerfile": true, "deploy/Dockerfile.app": true, "a_b-c/D": true,
		"": false, "/etc/passwd": false, "../Dockerfile": false, "a/../b": false, "./Dockerfile": false, "a//b": false,
		"Docker file": false, `Dockerfile"`: false, "$(id)": false, "`id`": false, "a;b": false, "a\nb": false, "dir/": false,
		strings.Repeat("a", 201): false,
	} {
		if got := DockerfileProblem(name) == ""; got != ok {
			t.Errorf("%q: allowed %v", name, got)
		}
	}
	k, sb, _ := kata(t)
	if _, err := k.BuildImage(context.Background(), ImageSpec{Tree: []byte("t"), Dockerfile: "$(id)", Out: filepath.Join(t.TempDir(), "x", ImageFile)}); err == nil || len(sb.specs) != 0 {
		t.Fatalf("a bad name was built: %v", err)
	}
}

// What the build leaves is the sandbox's: a link, a folder, or a build that failed leaves no image on
// the machine.
func TestAProjectsBuildCopiesOnlyAPlainImage(t *testing.T) {
	defer roomy()()
	secret := filepath.Join(t.TempDir(), "secret.key")
	os.WriteFile(secret, []byte("the key"), 0o600)
	for name, tc := range map[string]struct {
		code int
		then func(out string)
	}{
		"a link out":  {0, func(out string) { os.Symlink(secret, filepath.Join(out, ImageFile)) }},
		"a folder":    {0, func(out string) { os.Mkdir(filepath.Join(out, ImageFile), 0o755) }},
		"a FIFO":      {0, func(out string) { syscall.Mkfifo(filepath.Join(out, ImageFile), 0o644) }},
		"none":        {0, func(string) {}},
		"it failed":   {1, func(out string) { os.WriteFile(filepath.Join(out, ImageFile), []byte("x"), 0o644) }},
		"a link home": {0, func(out string) { os.RemoveAll(out); os.Symlink(filepath.Dir(secret), out) }},
	} {
		t.Run(name, func(t *testing.T) {
			k, sb, rt := kata(t)
			rt.answer = func(string) (int, string, error) {
				out := filepath.Join(sb.Home("run-0001"), "out")
				os.MkdirAll(out, 0o755)
				tc.then(out)
				return tc.code, "", nil
			}
			out := filepath.Join(t.TempDir(), "host-p1-1", ImageFile)
			if _, err := k.BuildImage(context.Background(), ImageSpec{Tree: []byte("t"), Dockerfile: "Dockerfile", Out: out}); err == nil {
				t.Fatal("the build passed")
			}
			if _, err := os.Lstat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Fatalf("the build left %s", filepath.Dir(out))
			}
		})
	}
}
