package release_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// folder writes a release folder for version with an image file of each part, and its release.json.
func folder(t *testing.T, root, version string) *release.Folder {
	t.Helper()
	dir := filepath.Join(root, version)
	if err := os.MkdirAll(filepath.Join(dir, release.ImagesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	imgs := map[string]release.Image{}
	for _, name := range release.Images {
		if err := os.WriteFile(filepath.Join(dir, release.ImagesDir, name+".tar"), []byte("image "+name+" "+version), 0o644); err != nil {
			t.Fatal(err)
		}
		imgs[name] = release.Image{Tag: name + ":" + version, ID: "sha256:" + strings.Repeat("a", 64)}
	}
	files, err := release.ParseTree(strings.NewReader("100644 blob " + strings.Repeat("1", 40) + "\tgo.mod\n" +
		"100644 blob " + strings.Repeat("2", 40) + "\tbaseline/agents/builder.md\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.Write(dir, release.Facts{Version: version, Commit: strings.Repeat("c", 40), BuiltAt: time.Now(),
		Schema: 51, MachineSetup: 2, Images: imgs}, files, map[string]string{"baseline/agents/builder.md": "model: x"}); err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAReleaseIsReadAndItsImagesHaveTheirHashes(t *testing.T) {
	f := folder(t, t.TempDir(), "0.1.0")
	if f.Manifest.Version != "0.1.0" || f.Manifest.Schema != 51 || len(f.Manifest.Files) != 2 || f.Gate != nil {
		t.Fatalf("read %+v", f.Manifest)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	// A file changed after the build is not what was tested.
	p, _ := f.ImagePath("crew-runtime")
	if err := os.WriteFile(p, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(); err == nil || !strings.Contains(err.Error(), "crew-runtime") {
		t.Fatalf("a changed image passed: %v", err)
	}
	os.Remove(p)
	if err := f.Verify(); err == nil {
		t.Fatal("a missing image passed")
	}
}

func TestTheGateHoldsOnlyForTheReleaseJSONItRanOn(t *testing.T) {
	root := t.TempDir()
	f := folder(t, root, "0.1.0")
	if err := f.WriteGate(release.Gate{Passed: true, At: time.Now(), InstallTest: "skipped"}); err != nil {
		t.Fatal(err)
	}
	g, err := release.Read(f.Dir)
	if err != nil || g.Gate == nil || g.Gate.ManifestSHA256 != f.ManifestSHA256 {
		t.Fatalf("the gate was not read: %+v %v", g, err)
	}
	// A failed gate is no gate.
	if err := f.WriteGate(release.Gate{Passed: false}); err != nil {
		t.Fatal(err)
	}
	if g, _ := release.Read(f.Dir); g.Gate != nil {
		t.Fatal("a failed gate counted")
	}
	// release.json changed after the gate: the gate was for another release.
	if err := f.WriteGate(release.Gate{Passed: true}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(f.Dir, release.ManifestFile))
	body = []byte(strings.Replace(string(body), `"schema": 51`, `"schema": 52`, 1))
	if err := os.WriteFile(filepath.Join(f.Dir, release.ManifestFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if g, _ := release.Read(f.Dir); g.Gate != nil {
		t.Fatal("a gate of another release.json counted")
	}
}

// A gate whose live part waits is pending, and pending is never passed, whatever its Passed says; a
// failed live part is no gate; a skipped one is a gate that names what did not run; a gate written
// before 0.52, which has no live part, is what its Passed says (16.2 step 5).
func TestAPendingGateIsNeverPassed(t *testing.T) {
	root := t.TempDir()
	f := folder(t, root, "1.2.3")
	for _, tc := range []struct {
		gate            release.Gate
		passed, pending bool
	}{
		{release.Gate{Passed: true, Live: release.LivePending}, false, true},
		{release.Gate{Passed: false, Live: release.LivePending}, false, true},
		{release.Gate{Passed: true, Live: release.LiveFailed}, false, false},
		{release.Gate{Passed: true, Live: release.LiveSkipped, Skipped: []string{"the real ticket: agy is signed out"}}, true, false},
		{release.Gate{Passed: true, Live: release.LivePassed}, true, false},
		{release.Gate{Passed: true}, true, false},
		{release.Gate{Passed: true, Live: "almost"}, false, false},
	} {
		if err := f.WriteGate(tc.gate); err != nil {
			t.Fatal(err)
		}
		g, err := release.Read(f.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if (g.Gate != nil) != tc.passed || (g.Pending != nil) != tc.pending {
			t.Errorf("%+v: passed %v, pending %v", tc.gate, g.Gate != nil, g.Pending != nil)
		}
	}
	if (&release.Gate{Skipped: []string{"x"}}).InstallsBySelfAfterGate() || (&release.Gate{Hold: "worse"}).InstallsBySelfAfterGate() ||
		!(&release.Gate{}).InstallsBySelfAfterGate() {
		t.Fatal("a skipped step or a hold lets a release install by itself")
	}
}

// ReadGate reads a folder's gate.json whatever its state, so Versions shows a gate that failed, which
// Read leaves out; a gate of another release.json, or of another version, is still no gate of this
// one (16.2 step 6).
func TestReadGateReadsAGateWhateverItsState(t *testing.T) {
	root := t.TempDir()
	f := folder(t, root, "1.2.3")
	if g, err := release.ReadGate(f.Dir); err != nil || g != nil {
		t.Fatalf("a folder with no gate.json: %+v %v", g, err)
	}
	for _, gate := range []release.Gate{
		{Passed: false, Steps: []release.GateStep{{Name: "make check", OK: false, Detail: "it ended with 2"}}},
		{Passed: true, Live: release.LiveFailed, LiveSteps: []release.GateStep{{Name: "the real ticket", OK: false}}},
		{Passed: true, Live: release.LivePending},
		{Passed: true, Live: release.LivePassed, Hold: "worse"},
	} {
		if err := f.WriteGate(gate); err != nil {
			t.Fatal(err)
		}
		g, err := release.ReadGate(f.Dir)
		if err != nil || g == nil || g.Passed != gate.Passed || g.Live != gate.Live || len(g.Steps) != len(gate.Steps) ||
			len(g.LiveSteps) != len(gate.LiveSteps) || g.Hold != gate.Hold || g.ManifestSHA256 != f.ManifestSHA256 {
			t.Fatalf("%+v: read %+v %v", gate, g, err)
		}
	}
	// A gate.json of another release.json, or that names another version, is not this release's.
	body, _ := os.ReadFile(filepath.Join(f.Dir, release.GateFile))
	other := strings.Replace(string(body), f.ManifestSHA256, strings.Repeat("0", 64), 1)
	if err := os.WriteFile(filepath.Join(f.Dir, release.GateFile), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, err := release.ReadGate(f.Dir); err != nil || g != nil {
		t.Fatalf("a gate of another release.json: %+v %v", g, err)
	}
	other = strings.Replace(string(body), `"version": "1.2.3"`, `"version": "1.2.4"`, 1)
	if err := os.WriteFile(filepath.Join(f.Dir, release.GateFile), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, err := release.ReadGate(f.Dir); err != nil || g != nil {
		t.Fatalf("a gate of another version: %+v %v", g, err)
	}
	// A folder that is no release is an error, as Read's.
	if _, err := release.ReadGate(filepath.Join(root, "9.9.9")); err == nil {
		t.Fatal("a folder that is not there was read")
	}
}

// The lines a server before 0.53 wrote into a gate named what its live steps cost. Since 0.53 Crew
// shows no amount (the operator's choice of report 9: Versions cuts it at display), so a gate is shown
// without one: Versions, the release's notes, the GitHub body. Reading it changes nothing: the updater
// decides on gate.json and writes it back as it reads it (security review of a9532aa).
func TestAGateWrittenBefore053IsShownWithoutItsAmounts(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"GATE-1 reached Done on its checks, for $1.13", "GATE-1 reached Done on its checks"},
		{"GATE-12 spent $2.41 of its $2.00", "GATE-12 stopped at its budget (before 0.53)"},
		{"tea: not worse: 2 of 2, after 2 of 2 ($0.84); shop: worse: 1 of 2, after 2 of 2 ($11.02); aaa: not compared: the project is paused",
			"tea: not worse: 2 of 2, after 2 of 2; shop: worse: 1 of 2, after 2 of 2; aaa: not compared: the project is paused"},
		{"its live steps ran in a candidate runtime image whose code is not the running release's, which reports what its runs cost: Install decides",
			"its live steps ran in a candidate runtime image whose code is not the running release's, and the steps rest on what its runs did and said: Install decides"},
		// Text that is no line of an old gate stays as it is: a step's own output is not Crew's to change.
		{"make check: $1 is the first argument", "make check: $1 is the first argument"},
		{"--- FAIL: TestTotal: got ($1.50), want ($1.25)", "--- FAIL: TestTotal: got ($1.50), want ($1.25)"},
		{"the eval suites: shop: not compared: too few cases ($0.10)", "the eval suites: shop: not compared: too few cases"},
		{"GATE-1 reached Done on its checks", "GATE-1 reached Done on its checks"},
		{"", ""},
	} {
		if got := release.Unpriced(tc.in); got != tc.want {
			t.Errorf("Unpriced(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	root := t.TempDir()
	f := folder(t, root, "1.2.3")
	old := map[string]any{"version": "1.2.3", "manifestSha256": f.ManifestSHA256, "passed": true, "at": time.Now(), "installTest": "skipped",
		"steps": []map[string]any{{"name": "make check", "ok": true}}, "live": "passed", "costUsd": 1.130918,
		"liveSteps": []map[string]any{{"name": "the real ticket on crew-gate", "ok": true, "detail": "GATE-1 reached Done on its checks, for $1.13"},
			{"name": "the eval suites", "ok": true, "detail": "tea: not worse: 2 of 2, after 2 of 2 ($0.84)"}},
		"skipped": []string{"the eval suites: shop: not compared: too few cases ($0.10)"},
		"hold":    "its live steps ran in a candidate runtime image whose code is not the running release's, which reports what its runs cost: Install decides"}
	body, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(f.Dir, release.GateFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := release.Read(f.Dir)
	if err != nil || read.Gate == nil {
		t.Fatalf("%+v %v", read, err)
	}
	any, err := release.ReadGate(f.Dir)
	if err != nil || any == nil {
		t.Fatalf("%+v %v", any, err)
	}
	for _, g := range []*release.Gate{read.Gate, any} {
		if g.LiveSteps[0].Detail != "GATE-1 reached Done on its checks, for $1.13" {
			t.Fatalf("reading changed the gate: %+v", g.LiveSteps[0])
		}
	}
	for _, g := range []*release.Gate{read.Gate.Unpriced(), any.Unpriced()} {
		out, _ := json.Marshal(g)
		if regexp.MustCompile(`\$\s*\d|costUsd`).Match(out) {
			t.Fatalf("the gate is read with an amount: %s", out)
		}
		if g.LiveSteps[0].Detail != "GATE-1 reached Done on its checks" || g.LiveSteps[1].Detail != "tea: not worse: 2 of 2, after 2 of 2" ||
			g.Skipped[0] != "the eval suites: shop: not compared: too few cases" || !strings.HasSuffix(g.Hold, "did and said: Install decides") {
			t.Fatalf("%s", out)
		}
	}
}

func TestAFolderMustBeWhatItSaysAndStayInside(t *testing.T) {
	root := t.TempDir()
	f := folder(t, root, "0.1.0")
	if err := os.Rename(f.Dir, filepath.Join(root, "0.2.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Read(filepath.Join(root, "0.2.0")); err == nil {
		t.Fatal("a folder named for another version was read")
	}
	g := folder(t, root, "0.3.0")
	img := g.Manifest.Images["postgres"]
	img.File = "../0.2.0/images/postgres.tar"
	g.Manifest.Images["postgres"] = img
	if err := g.Verify(); err == nil || !strings.Contains(err.Error(), "not in its folder") {
		t.Fatalf("an image outside the folder passed: %v", err)
	}
	for _, v := range []string{"latest", "1.2", "v1.2.3", "1.2.3-dev", "1.2.3-rc1"} {
		if release.ValidVersion(v) {
			t.Fatalf("%q passed as a version", v)
		}
	}
	for _, v := range []string{"0.1.0", "12.0.3", "0.0.0-dev.df75220abcde"} {
		if !release.ValidVersion(v) {
			t.Fatalf("%q did not pass as a version", v)
		}
	}
}

func TestReleasesAreListedByTheirNumbers(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"0.10.0", "0.2.0", "0.9.1"} {
		folder(t, root, v)
	}
	if err := os.MkdirAll(filepath.Join(root, "0.3.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, bad, err := release.List(root)
	if err != nil || len(fs) != 3 || bad["0.3.0"] == nil {
		t.Fatalf("%v %v %v", fs, bad, err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Manifest.Version)
	}
	if strings.Join(got, " ") != "0.2.0 0.9.1 0.10.0" {
		t.Fatalf("order %v", got)
	}
}

func TestTheAgentsModelAndEffortLinesAreRead(t *testing.T) {
	got := release.AgentSettings([]byte("---\nname: x\nmodel: sonnet\ndescription: \"model: not this\"\neffort: high\n---\nmodel: body\n"))
	if got != "model: sonnet\neffort: high" {
		t.Fatalf("got %q", got)
	}
	if release.AgentSettings([]byte("no front matter\nmodel: x\n")) != "" {
		t.Fatal("a file with no front matter has no settings")
	}
	if !release.AgentFile("harness/agents/reviewer.md") || !release.AgentFile("baseline/agents/builder.md") || release.AgentFile("harness/skills/a/SKILL.md") {
		t.Fatal("AgentFile")
	}
}

func TestParseTreeRefusesWhatGitDidNotWrite(t *testing.T) {
	if _, err := release.ParseTree(strings.NewReader("hello\n")); err == nil {
		t.Fatal("a line that is not git's was read")
	}
}

// Each image is named for its part and its version, with an id; a hand-made release.json that says
// less is refused (16.4, step 2).
func TestEachImageNamesItsPartAndItsID(t *testing.T) {
	f := folder(t, t.TempDir(), "0.1.0")
	img := f.Manifest.Images["crew-server"]
	img.Tag = "crew-server:0.0.9"
	f.Manifest.Images["crew-server"] = img
	if err := f.Verify(); err == nil || !strings.Contains(err.Error(), "not crew-server:0.1.0") {
		t.Fatalf("%v", err)
	}
	img.Tag, img.ID = "crew-server:0.1.0", ""
	f.Manifest.Images["crew-server"] = img
	if err := f.Verify(); err == nil {
		t.Fatal("an image with no id passed")
	}
}

// On the machine a release folder is root's alone; one the server's user could write is refused.
// A test runs as another user, so only the refusal is seen here; the install test sees the rest.
func TestAReleaseFolderIsRootsAlone(t *testing.T) {
	f := folder(t, t.TempDir(), "0.1.0")
	if os.Getuid() == 0 {
		t.Skip("run as root")
	}
	if err := f.Owned(); err == nil || !strings.Contains(err.Error(), "is not root's alone") {
		t.Fatalf("%v", err)
	}
}

// A release's deploy archive is found by the name release.json gives it, and Verify checks its hash
// with the images'; a release before 0.51 has none (16.12).
func TestTheDeployArchiveIsCheckedWithTheImages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "0.2.0")
	os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644)
		tag := n + ":0.2.0"
		if n == "postgres" {
			tag = "postgres:18"
		}
		imgs[n] = release.Image{Tag: tag, ID: "sha256:" + n}
	}
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs}, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := release.Read(dir)
	if _, err := f.DeployPath(); err == nil || !strings.Contains(err.Error(), "no deploy archive") {
		t.Fatalf("a release with none: %v", err)
	}
	os.WriteFile(filepath.Join(dir, release.DeployFile), []byte("the archive"), 0o644)
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs, Deploy: release.DeployFile}, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, _ = release.Read(dir)
	if p, err := f.DeployPath(); err != nil || p != filepath.Join(dir, release.DeployFile) {
		t.Fatalf("%q %v", p, err)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, release.DeployFile), []byte("another archive"), 0o644)
	if err := f.Verify(); err == nil || !strings.Contains(err.Error(), "deploy archive") {
		t.Fatalf("a changed archive: %v", err)
	}
}

// The deploy archive holds only names crew-ctl reads as root: one level under deploy/db, deploy/lib
// and deploy/firewall, of letters, digits, dots, dashes and underscores; any other fails the build
// rather than leaving a file out (round 2 of the security review of 864fb57..97382bc).
func TestTheDeployArchiveRefusesANameCrewCtlWouldNotRead(t *testing.T) {
	base := fstest.MapFS{}
	for _, p := range []string{"deploy/crew-ctl", "deploy/release.sh", "deploy/host-setup.sh", "deploy/proof.sh", "deploy/compose.yaml",
		"deploy/db/10-roles.sh", "deploy/lib/a.sh", "deploy/firewall/f.sh", "versions.lock"} {
		base[p] = &fstest.MapFile{Data: []byte("x\n"), Mode: 0o644}
	}
	if err := release.DeployArchive(base, io.Discard); err != nil {
		t.Fatalf("a plain tree: %v", err)
	}
	for _, bad := range []string{"deploy/lib/a b.sh", "deploy/lib/sub/a.sh", "deploy/firewall/\x1b[2J.sh"} {
		tree := fstest.MapFS{}
		for p, f := range base {
			tree[p] = f
		}
		tree[bad] = &fstest.MapFile{Data: []byte("x\n"), Mode: 0o644}
		if err := release.DeployArchive(tree, io.Discard); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
}
