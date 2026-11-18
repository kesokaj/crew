package harness

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// A check sandbox is of the kind check, gets the tree in its home before it starts, runs the setup
// and then each command as the sandbox's user under its time limit, tells a failure from a command
// that ran past its time, and goes when the check ends.
func TestACheckSandboxRunsEachCommandOnTheTree(t *testing.T) {
	k, sb, rt := kata(t)
	var seenTree []byte
	rt.answer = func(line string) (int, string, error) {
		switch {
		case strings.Contains(line, "tar -xf"):
			seenTree, _ = os.ReadFile(filepath.Join(sb.Home("run-0001"), checkTree))
			return 0, "", nil
		case strings.HasSuffix(line, "make setup"):
			return 0, "set up", nil
		case strings.HasSuffix(line, "go test ./..."):
			return 1, "FAIL: TestCount", nil
		case strings.HasSuffix(line, "sleep 99"):
			return 124, "", nil
		}
		return 0, "ok", nil
	}
	res, err := k.ExecFresh(t.Context(), CheckSpec{Project: "p1", Repos: []string{"o/r"}, Tree: []byte("a tar"), Setup: "make setup",
		Cmds: []CheckCmd{{"wc", "./wc testdata"}, {"tests", "go test ./..."}, {"slow", "sleep 99"}}, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if string(seenTree) != "a tar" {
		t.Fatalf("the tree in the home: %q", seenTree)
	}
	if s := sb.specs[0]; s.Kind != sandboxes.KindCheck || s.CLI != "" || s.Project != "p1" || !slices.Equal(s.Repos, []string{"o/r"}) {
		t.Fatalf("the sandbox: %+v", s)
	}
	want := []CheckResult{{Name: "wc", Passed: true, Output: "ok"}, {Name: "tests", Code: 1, Output: "FAIL: TestCount"},
		{Name: "slow", Code: 124, Error: "it ran longer than 1m0s"}}
	if !slices.Equal(res, want) {
		t.Fatalf("%+v", res)
	}
	for _, e := range rt.execs {
		if e.User != orchestrator.GuestUser || e.Cmd[0] != "timeout" || e.Cmd[3] != "bash" {
			t.Fatalf("a command not run as the sandbox's user under its limit: %+v", e)
		}
	}
	if !slices.Equal(sb.removed, []string{"run-0001"}) {
		t.Fatalf("the sandbox was not removed: %v", sb.removed)
	}
}

// A setup that fails makes every command an error, which is Crew's trouble and no failure (13.9).
func TestASetupThatFailsIsAnError(t *testing.T) {
	k, _, rt := kata(t)
	rt.answer = func(line string) (int, string, error) {
		if strings.HasSuffix(line, "make setup") {
			return 2, "no rule", nil
		}
		return 0, "", nil
	}
	res, err := k.ExecFresh(t.Context(), CheckSpec{Tree: []byte("x"), Setup: "make setup", Cmds: []CheckCmd{{"a", "true"}}})
	if err != nil || len(res) != 1 || res[0].Passed || res[0].Error != "the project's setup ended with 2" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := k.ExecFresh(t.Context(), CheckSpec{Cmds: []CheckCmd{{"a", "true"}}}); err == nil {
		t.Fatal("a check with no tree")
	}
}

func tarArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An eval's held-out check (9.6, 13.6, 13.9, register row 22) unpacks the branch's tree and runs the
// project's setup first, cuts the sandbox's route from outside the microVM and proves from inside that
// the former door and a public address fail, and only then writes the held-out files into the home,
// removes what the branch has at their paths, unpacks them, and runs the case's commands. A cut, a
// proof, or a placement that fails gives an error and no result, with no command run.
func TestAHeldOutCheckCutsItsRouteBeforeTheHeldOutFiles(t *testing.T) {
	tree := tarArchive(t, map[string]string{"count.go": "branch\n", "internal/count_test.go": "branch test\n"})
	heldOut := tarArchive(t, map[string]string{"internal/count_test.go": "held-out test\n", "testdata/cases.txt": "cases\n"})

	heldOutInHome := func(home string) []byte {
		entries, _ := os.ReadDir(home)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "held-out-") {
				b, _ := os.ReadFile(filepath.Join(home, e.Name()))
				return b
			}
		}
		return nil
	}

	k, sb, rt := kata(t)
	var steps, probes []string
	var seenHeldOut []byte
	sb.onCut = func(id string) {
		if heldOutInHome(sb.Home(id)) != nil {
			t.Error("the held-out files were in the home before the route was cut")
		}
		steps = append(steps, "cut")
	}
	rt.answer = func(line string) (int, string, error) {
		home := sb.Home("run-0001")
		switch {
		case strings.Contains(line, "tar -xf ~/"+checkTree):
			if len(sb.cut) != 0 || heldOutInHome(home) != nil {
				t.Errorf("at unpack: cut=%v heldOut=%v", sb.cut, heldOutInHome(home) != nil)
			}
			steps = append(steps, "unpack")
			return 0, "", nil
		case strings.HasSuffix(line, "make setup"):
			if len(sb.cut) != 0 || heldOutInHome(home) != nil {
				t.Errorf("at setup: cut=%v heldOut=%v", sb.cut, heldOutInHome(home) != nil)
			}
			steps = append(steps, "setup")
			return 0, "set up", nil
		case strings.HasPrefix(line, "exec 3<>/dev/tcp/"):
			if len(sb.cut) != 1 || heldOutInHome(home) != nil {
				t.Errorf("at probe %q: cut=%v heldOut=%v", line, sb.cut, heldOutInHome(home) != nil)
			}
			probes = append(probes, strings.TrimPrefix(line, "exec 3<>/dev/tcp/"))
			return 1, "", nil
		case strings.Contains(line, "tar -xf ~/held-out-"):
			seenHeldOut = heldOutInHome(home)
			for _, want := range []string{"internal/count_test.go", "testdata/cases.txt", "rm -rf", "rm -f ~/held-out-"} {
				if !strings.Contains(line, want) {
					t.Errorf("placement command %q lacks %q", line, want)
				}
			}
			steps = append(steps, "place")
			return 0, "", nil
		case strings.HasSuffix(line, "go test ./..."):
			steps = append(steps, "cmd")
			return 0, "PASS", nil
		}
		return 0, "", nil
	}
	res, err := k.ExecFresh(t.Context(), CheckSpec{
		Project: "p1", Mode: "sources", Tree: tree, HeldOut: heldOut, Cut: true,
		Setup: "make setup", Cmds: []CheckCmd{{"tests", "go test ./..."}}, Timeout: time.Minute,
	})
	if err != nil || len(res) != 1 || !res[0].Passed {
		t.Fatalf("ExecFresh = %+v, %v", res, err)
	}
	if !slices.Equal(steps, []string{"unpack", "setup", "cut", "place", "cmd"}) {
		t.Fatalf("order = %v, want [unpack setup cut place cmd]", steps)
	}
	if !slices.Equal(probes, []string{"10.9.1.254/18091", "1.1.1.1/443"}) {
		t.Fatalf("probes = %v", probes)
	}
	for _, ex := range rt.execs {
		if strings.HasPrefix(ex.Cmd[len(ex.Cmd)-1], "exec 3<>/dev/tcp/") {
			if !slices.Equal(ex.Cmd[:len(ex.Cmd)-1], []string{"timeout", "4", "bash", "-c"}) {
				t.Fatalf("probe cmd = %v, want non-login bash -c", ex.Cmd)
			}
		}
	}
	if !bytes.Equal(seenHeldOut, heldOut) {
		t.Fatal("the held-out archive in the home did not match")
	}

	// A cut, a proof, or a placement that fails returns an error and no result, and runs no command.
	for name, tc := range map[string]struct {
		noDoor    bool
		cutErr    error
		probeExit int
		placeExit int
	}{
		"the door is not known": {noDoor: true, probeExit: 1},
		"the cut fails":         {cutErr: errors.New("docker is busy"), probeExit: 1},
		"the route stays up":    {probeExit: 0},
		"the placement fails":   {probeExit: 1, placeExit: 1},
	} {
		t.Run(name, func(t *testing.T) {
			k, sb, rt := kata(t)
			if tc.noDoor {
				sb.door = ""
			}
			sb.cutErr = tc.cutErr
			ranCmd := false
			rt.answer = func(line string) (int, string, error) {
				switch {
				case strings.HasPrefix(line, "exec 3<>/dev/tcp/"):
					return tc.probeExit, "", nil
				case strings.Contains(line, "tar -xf ~/held-out-"):
					return tc.placeExit, "cannot unpack", nil
				case strings.HasSuffix(line, "go test ./..."):
					ranCmd = true
				}
				return 0, "", nil
			}
			res, err := k.ExecFresh(t.Context(), CheckSpec{
				Project: "p1", Mode: "sources", Tree: tree, HeldOut: heldOut, Cut: true,
				Setup: "make setup", Cmds: []CheckCmd{{"tests", "go test ./..."}}, Timeout: time.Minute,
			})
			if err == nil || len(res) != 0 || ranCmd {
				t.Fatalf("%s: res=%+v err=%v ranCmd=%v", name, res, err, ranCmd)
			}
			if tc.placeExit == 0 && heldOutInHome(sb.Home("run-0001")) != nil {
				t.Fatalf("%s: the held-out files were written into the home before the cut held", name)
			}
		})
	}
}
