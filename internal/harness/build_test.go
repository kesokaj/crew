package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kesokaj/crew/internal/sandboxes"
)

// writeOut plays release.sh build in the sandbox: the files it leaves in ~/out.
func writeOut(t *testing.T, home string) {
	t.Helper()
	for _, name := range BuildFiles {
		p := filepath.Join(home, "out", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("file "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// agyFile is agy's tarball as the server downloaded it for a build.
func agyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b1.agy", "cli_linux_x64.tar.gz")
	os.MkdirAll(filepath.Dir(p), 0o750)
	if err := os.WriteFile(p, []byte("agy's tarball"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

// A build runs in a sandbox of the kind build, which holds nothing of the operator's: no CLI, no
// repository for a token, no gcloud, nothing in its environment. It runs the script it is given, not
// one of the tree's, on the tree, and the files it left are copied out with their hashes (16.2 step 4).
func TestABuildRunsTheGivenScriptInASandboxThatHoldsNothing(t *testing.T) {
	defer roomy()()
	k, sb, rt := kata(t)
	var script, agy []byte
	recorded := 0
	rt.answer = func(line string) (int, string, error) {
		if recorded != 1 {
			t.Errorf("the build ran before the capacity check was told its sandbox's row exists (%d)", recorded)
		}
		home := sb.Home("run-0001")
		script, _ = os.ReadFile(filepath.Join(home, buildScript))
		agy, _ = os.ReadFile(filepath.Join(home, "agy", "cli_linux_x64.tar.gz"))
		writeOut(t, home)
		return 0, "built", nil
	}
	out := filepath.Join(t.TempDir(), "b1")
	res, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3", Tree: []byte("tree"), Agy: agyFile(t), Script: []byte("#!/bin/sh\necho the running release's\n"), Out: out,
		Recorded: func() { recorded++ }})
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != "#!/bin/sh\necho the running release's\n" {
		t.Fatalf("the sandbox ran %q", script)
	}
	// agy's tarball, which the server downloaded and checked, is in the home, beside the tree (12.8,
	// since 0.57).
	if string(agy) != "agy's tarball" {
		t.Fatalf("the sandbox's agy: %q", agy)
	}
	spec := sb.specs[0]
	if spec.Kind != sandboxes.KindBuild || spec.CLI != "" || spec.Repos != nil || spec.Gcloud || spec.Minimal || len(spec.Env) != 0 || spec.Mode != "" {
		t.Fatalf("the sandbox's spec: %+v", spec)
	}
	if len(rt.execs) != 2 {
		t.Fatalf("the build ran %d steps", len(rt.execs))
	}
	exec := rt.execs[0]
	line := exec.Cmd[len(exec.Cmd)-1]
	if exec.User != "agent" || !strings.Contains(line, "tar -xf ~/tree.tar -C ~/work") ||
		!strings.Contains(line, "bash ~/release.sh build-runtime --tree ~/work --version 1.2.3 --out ~/out --agy ~/agy") {
		t.Fatalf("the build ran %q as %q", line, exec.User)
	}
	exec = rt.execs[1]
	if line := exec.Cmd[len(exec.Cmd)-1]; exec.User != "agent" || strings.Contains(line, "tar -xf") ||
		!strings.Contains(line, "bash ~/release.sh build-rest --tree ~/work --version 1.2.3 --out ~/out") {
		t.Fatalf("the second step ran %q as %q", line, exec.User)
	}
	for _, name := range BuildFiles {
		body, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if res.Hashes[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: hash %q", name, res.Hashes[name])
		}
	}
	if res.Output != "builtbuilt" || len(sb.removed) != 1 {
		t.Fatalf("output %q, removed %v", res.Output, sb.removed)
	}
}

// What the sandbox leaves is the sandbox's: a link out of its folder is not followed, and a build
// that failed or left a link leaves no files on the machine.
func TestABuildCopiesOutOnlyPlainFiles(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.key")
	os.WriteFile(secret, []byte("the key"), 0o600)
	for name, tc := range map[string]struct {
		code int
		then func(home string)
		want string
	}{
		"a link out": {0, func(home string) {
			p := filepath.Join(home, "out", "images", "crew-server.tar")
			os.Remove(p)
			os.Symlink(secret, p)
		}, "images/crew-server.tar"},
		"a FIFO": {0, func(home string) {
			p := filepath.Join(home, "out", "pins.json")
			os.Remove(p)
			if err := syscall.Mkfifo(p, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "not a plain file"},
		"no room":         {0, func(string) { FreeSpace = func(string) (uint64, error) { return 1 << 30, nil } }, "bytes free"},
		"too much in all": {0, func(string) { MaxBuildTotal = 10 }, "more than 10"},
		"the folder a link": {0, func(home string) {
			os.RemoveAll(filepath.Join(home, "out"))
			os.Symlink(filepath.Dir(secret), filepath.Join(home, "out"))
		}, "build's files"},
		"a file missing": {0, func(home string) { os.Remove(filepath.Join(home, "out", "pins.json")) }, "pins.json"},
		"a failed build": {2, func(string) {}, "ended with 2"},
		"too long":       {124, func(string) {}, "ran longer than"},
	} {
		t.Run(name, func(t *testing.T) {
			defer roomy()()
			k, sb, rt := kata(t)
			rt.answer = func(line string) (int, string, error) {
				home := sb.Home("run-0001")
				writeOut(t, home)
				tc.then(home)
				return tc.code, "", nil
			}
			out := filepath.Join(t.TempDir(), "b1")
			_, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3", Tree: []byte("tree"), Agy: agyFile(t), Script: []byte("s"), Out: out})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(out); err == nil {
				t.Fatal("the build's folder is left")
			}
			if len(sb.removed) != 1 {
				t.Fatal("the sandbox is left")
			}
		})
	}
	k, sb, _ := kata(t)
	if _, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3; rm -rf ~", Tree: []byte("t"), Agy: agyFile(t), Script: []byte("s"), Out: t.TempDir() + "/x"}); err == nil {
		t.Fatal("a version that is no version was built")
	}
	// A build is given agy's tarball, or it starts no sandbox.
	for _, agy := range []string{"", filepath.Join(t.TempDir(), "none.tar.gz")} {
		if _, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3", Tree: []byte("t"), Agy: agy, Script: []byte("s"), Out: t.TempDir() + "/x"}); err == nil {
			t.Fatalf("a build with agy %q was built", agy)
		}
	}
	if len(sb.specs) != 0 {
		t.Fatalf("%d sandboxes started", len(sb.specs))
	}
}

// The runtime image, which gets the operator's login in the gate's live steps, is built first and
// copied out before the second step runs the candidate's other Dockerfiles: what the second step
// writes over it, as an escape from one of those builds could, never reaches the build's files
// (security review of M16 P3; the operator's answer of 2026-10-20).
func TestTheRuntimeImageIsCopiedOutBeforeTheOtherImagesAreBuilt(t *testing.T) {
	defer roomy()()
	k, sb, rt := kata(t)
	out := filepath.Join(t.TempDir(), "b1")
	step := 0
	rt.answer = func(line string) (int, string, error) {
		step++
		home := sb.Home("run-0001")
		switch step {
		case 1:
			for _, name := range RuntimeFiles {
				p := filepath.Join(home, "out", name)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte("built first "+name), 0o644)
			}
		case 2:
			if body, err := os.ReadFile(filepath.Join(out, "images", "crew-runtime.tar")); err != nil || string(body) != "built first images/crew-runtime.tar" {
				t.Errorf("the runtime image was not copied out before the second step: %q, %v", body, err)
			}
			writeOut(t, home)
			os.WriteFile(filepath.Join(home, "out", "images", "crew-runtime.tar"), []byte("changed by the second step"), 0o644)
			os.WriteFile(filepath.Join(home, "out", "pins.json"), []byte(`{"agy":"changed"}`), 0o644)
		}
		return 0, "", nil
	}
	res, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3", Tree: []byte("tree"), Agy: agyFile(t), Script: []byte("s"), Out: out})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range RuntimeFiles {
		body, _ := os.ReadFile(filepath.Join(out, name))
		sum := sha256.Sum256(body)
		if string(body) != "built first "+name || res.Hashes[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s is %q with the hash %q", name, body, res.Hashes[name])
		}
	}
	for _, name := range OtherFiles {
		if _, ok := res.Hashes[name]; !ok {
			t.Fatalf("%s was not copied", name)
		}
	}
	if len(res.Hashes) != len(BuildFiles) {
		t.Fatalf("hashes: %v", res.Hashes)
	}
}

// roomy is a disk with room for any build, until the function it returns puts the real one back.
func roomy() func() {
	f, n := FreeSpace, MaxBuildTotal
	FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	return func() { FreeSpace, MaxBuildTotal = f, n }
}

// A file that grows after it was checked is not copied past the size it had: the sandbox still runs
// while its files are copied out (review of M16 P3, round 2).
func TestAFileIsCopiedOnlyAsLongAsItWasChecked(t *testing.T) {
	defer roomy()()
	home := t.TempDir()
	writeOut(t, home)
	p := filepath.Join(home, "out", "images", "crew-runtime.tar")
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	g, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	g.WriteString(strings.Repeat("x", 4096))
	g.Close()
	if _, err := copyHashed(f, filepath.Join(t.TempDir(), "copy"), st.Size()); err == nil || !strings.Contains(err.Error(), "grew") {
		t.Fatalf("a file that grew: %v", err)
	}
}

// agy's tarball is copied into the sandbox's home only while that disk keeps DiskMargin free besides
// it, as a build's files are copied out (security review of 77f8ff9..f9d8ede): the home may be on
// another disk than the server's folder of builds.
func TestAgyIsCopiedIntoAHomeWithRoom(t *testing.T) {
	defer roomy()()
	k, sb, rt := kata(t)
	home := sb.Home("run-0001")
	FreeSpace = func(p string) (uint64, error) {
		if strings.HasPrefix(p, home) {
			return DiskMargin, nil
		}
		return 1 << 40, nil
	}
	_, err := k.Build(context.Background(), BuildSpec{Version: "1.2.3", Tree: []byte("tree"), Agy: agyFile(t), Script: []byte("s"), Out: filepath.Join(t.TempDir(), "b1")})
	if err == nil || !strings.Contains(err.Error(), "bytes free") || len(rt.execs) != 0 {
		t.Fatalf("%v, %d steps ran", err, len(rt.execs))
	}
	if _, err := os.Stat(filepath.Join(home, "agy", "cli_linux_x64.tar.gz")); err == nil {
		t.Fatal("the tarball was copied")
	}
}
