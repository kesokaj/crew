package versions_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// agyTarball is agy's tarball as the vendor's bucket serves it to a build, and agyLock its pin.
var (
	agyTarball = []byte("agy's tarball")
	agyLock    = "AGY_VERSION=1.4.5\nAGY_URL=https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.5-4952523841339392/linux-x64/cli_linux_x64.tar.gz\n" +
		"AGY_SHA512=" + sha512Of(agyTarball) + "\n"
)

func sha512Of(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

// crewTree is a commit of Crew's project, small, with what a build reads of it; its release.sh is the
// candidate's, which a build must not run.
func crewTree() map[string]string {
	return map[string]string{
		"go.mod":                            "module github.com/kesokaj/crew\n\ngo 1.27\n",
		"go.sum":                            "",
		"versions.lock":                     "POSTGRES_IMAGE=postgres:18.6-trixie@sha256:" + strings.Repeat("b", 64) + "\nMACHINE_SETUP=2\n" + agyLock,
		"image/Dockerfile":                  "FROM scratch\n",
		"cmd/crew-run/main.go":              "package main\n\nfunc main() {}\n",
		"cmd/crew-harnessbuild/main.go":     "package main\n\nfunc main() {}\n",
		"internal/db/migrations/0001_a.sql": "SELECT 1;\n",
		"harness/agents/builder.md":         "---\nmodel: agy/gemini-4-argon\neffort: high\n---\nBuild.\n",
		"deploy/crew-ctl":                   "#!/bin/sh\n",
		"deploy/release.sh":                 "#!/bin/sh\necho the candidate's own script\n",
		"deploy/host-setup.sh":              "#!/bin/sh\n",
		"deploy/proof.sh":                   "#!/bin/sh\n",
		"deploy/compose.yaml":               "services: {}\n",
		"deploy/db/10-roles.sh":             "#!/bin/sh\n",
		"deploy/lib/kata-config.sh":         "x=1\n",
		"deploy/firewall/crew-firewall.sh":  "#!/bin/sh\n",
	}
}

const runningScript = "#!/bin/sh\necho the running release's script\n"

type buildKit struct {
	*kit
	b    *versions.Builder
	h    *harness.Fake
	git  *gitstore.Store
	head string
	// vendor is what the vendor's bucket answers, and asked the addresses it was asked for.
	mu     sync.Mutex
	vendor []byte
	asked  []string
}

// newBuildKit is newKit with Crew's project in a repository, a running release whose deploy archive
// holds runningScript, and a builder on the fake harness that hands its builds to the updater.
func newBuildKit(t *testing.T) *buildKit {
	t.Helper()
	return newBuildKitOf(t, crewTree())
}

// newBuildKitOf is newBuildKit with Crew's project's main holding tree.
func newBuildKitOf(t *testing.T, tree map[string]string) *buildKit {
	t.Helper()
	k := &buildKit{kit: newKit(t, "0.1.0"), h: &harness.Fake{}}
	dir := t.TempDir()
	k.git = &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}, Protocols: "file"}
	ctx := context.Background()
	if err := k.git.CreateLocal(ctx, "crew", tree, "Crew", gitstore.Identity{Name: "a", Email: "a@x"}); err != nil {
		t.Fatal(err)
	}
	k.head = k.git.Main(ctx, "crew")
	// The running release, with its deploy archive.
	mfs := fstest.MapFS{}
	for p, body := range crewTree() {
		mfs[p] = &fstest.MapFile{Data: []byte(body), Mode: 0o755}
	}
	mfs["deploy/release.sh"] = &fstest.MapFile{Data: []byte(runningScript), Mode: 0o755}
	var arch bytes.Buffer
	if err := release.DeployArchive(mfs, &arch); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(k.rel, "0.1.0")
	if err := os.WriteFile(filepath.Join(run, release.DeployFile), arch.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := release.Read(run)
	f.Manifest.Deploy = nil
	imgs := f.Manifest.Images
	if _, err := release.Write(run, release.Facts{Version: "0.1.0", BuiltAt: time.Now(), Images: imgs, Deploy: release.DeployFile, Schema: 51}, f.Manifest.Files, nil); err != nil {
		t.Fatal(err)
	}
	k.gate("0.1.0")
	k.u.Builds = filepath.Join(dir, "builds")
	k.b = &versions.Builder{Pool: k.d.Server, Git: k.git, Sandbox: k.h, Loader: k.s.Updater.(versions.Loader),
		Releases: k.rel, Builds: k.u.Builds, Running: "0.1.0", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AgyClient: k.bucket(t)}
	free := harness.FreeSpace
	harness.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(func() { harness.FreeSpace = free })
	return k
}

// bucket is a client that reaches a server answering as the vendor's bucket, whatever the address.
func (k *buildKit) bucket(t *testing.T) *http.Client {
	k.vendor = agyTarball
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.asked = append(k.asked, r.Host+r.URL.Path)
		body := k.vendor
		k.mu.Unlock()
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	tr.TLSClientConfig.ServerName = "example.com"
	return &http.Client{Transport: tr}
}

func (k *buildKit) build(version string) *versions.Build {
	k.t.Helper()
	if _, err := k.b.Start(context.Background(), "crew", version, k.head, versions.ByOperator); err != nil {
		k.t.Fatal(err)
	}
	k.b.Wait()
	bd, err := k.b.Get(context.Background(), version)
	if err != nil || bd == nil {
		k.t.Fatalf("%v %v", bd, err)
	}
	return bd
}

// A build whose agy is not the commit's pin fails before its sandbox starts, says why, and leaves
// nothing on the machine (12.8, since 0.57).
func TestABuildWhoseAgyIsNotThePinFails(t *testing.T) {
	k := newBuildKit(t)
	k.mu.Lock()
	k.vendor = []byte("another tarball")
	k.mu.Unlock()
	bd := k.build("0.2.0")
	if bd.State != versions.BuildFailed || !strings.Contains(bd.Error, "sha512") || len(k.h.Builds) != 0 {
		t.Fatalf("%+v, %d builds", bd, len(k.h.Builds))
	}
	if entries, _ := os.ReadDir(k.u.Builds); len(entries) != 0 {
		t.Fatalf("the build's files are left: %v", entries)
	}
}

// A build exports the commit, runs the running release's script on it in the build sandbox, never the
// candidate's own, and the updater makes the release folder: release.json lists the commit's files and
// agents as the canonical repository has them, the runtime image's inputs, and the deploy archive of
// the commit (16.2 step 4).
func TestABuildMakesTheReleaseFolder(t *testing.T) {
	k := newBuildKit(t)
	bd := k.build("0.2.0")
	if bd.State != versions.BuildDone || bd.Log != "built 0.2.0" {
		t.Fatalf("%+v", bd)
	}
	spec := k.h.Builds[0]
	if string(spec.Script) != runningScript {
		t.Fatalf("the sandbox ran %q, not the running release's script", spec.Script)
	}
	// agy's tarball, as the commit's versions.lock pins it, downloaded by the server and handed to the
	// build from a folder beside the build's own (12.8, since 0.57).
	if len(k.h.Agy) != 1 || string(k.h.Agy[0]) != string(agyTarball) || filepath.Base(spec.Agy) != "cli_linux_x64.tar.gz" ||
		filepath.Dir(filepath.Dir(spec.Agy)) != k.u.Builds || len(k.asked) != 1 || filepath.Base(filepath.Dir(spec.Agy)) == filepath.Base(spec.Out) ||
		k.asked[0] != "storage.googleapis.com/antigravity-public/antigravity-cli/1.4.5-4952523841339392/linux-x64/cli_linux_x64.tar.gz" {
		t.Fatalf("agy: %q from %v at %s", k.h.Agy, k.asked, spec.Agy)
	}
	tree, err := release.ReadTree(bytes.NewReader(spec.Tree))
	if err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(filepath.Join(k.rel, "0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	m := f.Manifest
	listed, _ := k.git.LsTree(context.Background(), "crew", k.head)
	files, _ := release.ParseTree(strings.NewReader(listed))
	if m.Commit != k.head || len(m.Files) != len(files) || m.Files["deploy/release.sh"] != files["deploy/release.sh"] ||
		m.Agents["harness/agents/builder.md"] != "model: agy/gemini-4-argon\neffort: high" ||
		m.RuntimeSHA256 != release.RuntimeSHA256(tree.Runtime) || m.Schema != 1 || m.Pins["postgres"] != "18.6-trixie" {
		t.Fatalf("%+v", m)
	}
	// The deploy archive is the candidate's, which carries its own release.sh to the next build.
	body, _ := os.ReadFile(filepath.Join(k.rel, "0.2.0", release.DeployFile))
	if !bytes.Equal(body, tree.Deploy) {
		t.Fatal("the deploy archive is not the commit's")
	}
	if entries, _ := os.ReadDir(k.u.Builds); len(entries) != 0 {
		t.Fatalf("the build's files are left: %v", entries)
	}
	if notes, _ := os.ReadFile(filepath.Join(k.rel, "0.2.0", release.NotesFile)); !strings.HasPrefix(string(notes), "# Crew 0.2.0: 1 commit from outside\n") {
		t.Fatalf("notes %q", notes)
	}
	if _, err := k.b.Start(context.Background(), "crew", "0.2.0", k.head, versions.ByOperator); err == nil {
		t.Fatal("0.2.0 was built twice")
	}
}

// One build at a time (16.2 step 3).
func TestTwoBuildsAtOnceAreRefused(t *testing.T) {
	k := newBuildKit(t)
	hold := make(chan struct{})
	k.h.BuildFile = func(spec harness.BuildSpec, name string) []byte {
		<-hold
		return []byte(name)
	}
	if _, err := k.b.Start(context.Background(), "crew", "0.2.0", k.head, versions.ByOperator); err != nil {
		t.Fatal(err)
	}
	if _, err := k.b.Start(context.Background(), "crew", "0.3.0", k.head, versions.ByOperator); !errors.Is(err, versions.ErrBuildRuns) {
		t.Fatalf("a second build: %v", err)
	}
	close(hold)
	k.b.Wait()
}

// A build that fails leaves no release folder and no files, with why on its row: the sandbox's own
// failure, and a running release that has no deploy archive to take the script from.
func TestAFailedBuildLeavesNoFolder(t *testing.T) {
	k := newBuildKit(t)
	k.h.BuildErr = errors.New("the build ended with 2")
	bd := k.build("0.2.0")
	if bd.State != versions.BuildFailed || !strings.Contains(bd.Error, "ended with 2") || bd.Log != "the build failed" {
		t.Fatalf("%+v", bd)
	}
	if _, err := os.Stat(filepath.Join(k.rel, "0.2.0")); err == nil {
		t.Fatal("a failed build left a release folder")
	}
	if entries, _ := os.ReadDir(k.u.Builds); len(entries) != 0 {
		t.Fatalf("a failed build left files: %v", entries)
	}
	// The number is free again.
	k.h.BuildErr = nil
	if bd := k.build("0.2.0"); bd.State != versions.BuildDone {
		t.Fatalf("%+v", bd)
	}
	// A running release from before 0.51 has no deploy archive.
	k2 := newBuildKit(t)
	run := filepath.Join(k2.rel, "0.1.0")
	f, _ := release.Read(run)
	if _, err := release.Write(run, release.Facts{Version: "0.1.0", BuiltAt: time.Now(), Images: f.Manifest.Images, Schema: 51}, f.Manifest.Files, nil); err != nil {
		t.Fatal(err)
	}
	bd = k2.build("0.2.0")
	if bd.State != versions.BuildFailed || !strings.Contains(bd.Error, "no deploy archive") || len(k2.h.Builds) != 0 {
		t.Fatalf("%+v %d", bd, len(k2.h.Builds))
	}
}

// A build the server's stop cut off is failed when it starts again, and its files are removed.
func TestABuildCutOffIsFailed(t *testing.T) {
	k := newBuildKit(t)
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by) VALUES ('0.2.0', $1, 'operator')`, k.head); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(k.u.Builds, "b1", "images"), 0o750)
	if err := k.b.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	bd, _ := k.b.Get(ctx, "0.2.0")
	if bd.State != versions.BuildFailed || !strings.Contains(bd.Error, "server stopped") {
		t.Fatalf("%+v", bd)
	}
	if _, err := os.Stat(filepath.Join(k.u.Builds, "b1")); err == nil {
		t.Fatal("its files are left")
	}
}

// A commit larger than a sandbox takes is refused while it is exported, before it is read again
// (review of M16 P3).
func TestATreeTooLargeIsRefusedAsItIsExported(t *testing.T) {
	k := newBuildKit(t)
	k.b.MaxTree = 1024
	bd := k.build("0.2.0")
	if bd.State != versions.BuildFailed || !strings.Contains(bd.Error, "larger than 1024 bytes") || len(k.h.Builds) != 0 {
		t.Fatalf("%+v", bd)
	}
}

// busyLoader is an updater that is busy for its first answers.
type busyLoader struct {
	versions.Loader
	busy int
}

func (l *busyLoader) Load(ctx context.Context, a updater.LoadAsk) (*updater.Loaded, error) {
	if l.busy > 0 {
		l.busy--
		return nil, updater.ErrBusy
	}
	return l.Loader.Load(ctx, a)
}

// A build whose updater is busy with an install waits for it, and is not thrown away (review of M16
// P3, round 2: a load and an install exclude each other).
func TestABuildWaitsForABusyUpdater(t *testing.T) {
	k := newBuildKit(t)
	l := &busyLoader{Loader: k.b.Loader, busy: 2}
	k.b.Loader, k.b.LoadWait = l, time.Millisecond
	if bd := k.build("0.2.0"); bd.State != versions.BuildDone || l.busy != 0 {
		t.Fatalf("%+v, %d busy answers left", bd, l.busy)
	}
}

// An update replaces the containers, not crew-ctl and its files (16.11, since 0.55). Versions compares
// what install recorded of them, in the releases folder, with the running release's deploy archive,
// and gives the command for root that puts the release's own in place: the same, nothing to do; one
// that differs, how many and the first; none recorded, an install before 0.55.
func TestVersionsSaysWhetherTheDeployFilesAreTheRunningReleases(t *testing.T) {
	k := newBuildKit(t)
	ctx := context.Background()
	// What the running release's archive holds: the deploy files of crewTree, its release.sh the
	// running one's.
	want := map[string]string{}
	for p, body := range crewTree() {
		if p == "deploy/release.sh" {
			body = runningScript
		}
		if strings.HasPrefix(p, "deploy/") || p == "versions.lock" {
			sum := sha256.Sum256([]byte(body))
			want[p] = hex.EncodeToString(sum[:])
		}
	}
	record := func(files map[string]string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"files": files})
		if err := os.WriteFile(filepath.Join(k.rel, release.DeployRecord), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	view := func() versions.DeployFilesView {
		t.Helper()
		v, err := k.s.View(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v.DeployFiles
	}
	archive := filepath.Join(k.rel, "0.1.0", release.DeployFile)
	f, _ := release.Read(filepath.Join(k.rel, "0.1.0"))
	// The command checks the archive against release.json's hash before root runs anything of it
	// (security review of 864fb57..97382bc).
	if d := view(); d.State != "unrecorded" || !strings.HasPrefix(d.Command, "echo '"+f.Manifest.Deploy.SHA256+"  "+archive+"' | sha256sum -c - && ") ||
		!strings.Contains(d.Command, "tar -xzf '"+archive+"'") || !strings.HasSuffix(d.Command, "install --deploy-files") ||
		!strings.Contains(d.Command, "echo '"+want["deploy/crew-ctl"]+`  '"$d/deploy/crew-ctl" | sha256sum -c - && "$d/deploy/crew-ctl" install`) {
		t.Fatalf("no record: %+v", d)
	}
	record(want)
	if d := view(); d.State != "same" || d.Command != "" {
		t.Fatalf("the release's own in place: %+v", d)
	}
	other := map[string]string{}
	for p, h := range want {
		other[p] = h
	}
	other["deploy/compose.yaml"] = strings.Repeat("0", 64)
	delete(other, "versions.lock")
	record(other)
	if d := view(); d.State != "differ" || d.Differ != 2 || len(d.Paths) != 2 || d.Paths[0] != "deploy/compose.yaml" || d.Paths[1] != "versions.lock" ||
		!strings.Contains(d.Command, archive) {
		t.Fatalf("two differ: %+v", d)
	}
	// An archive that is not the one release.json names is offered to no one.
	body, _ := os.ReadFile(archive)
	if err := os.WriteFile(archive, append(body, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := view(); d.Command != "" || !strings.Contains(d.Error, "not the one release.json names") {
		t.Fatalf("a changed archive: %+v", d)
	}
	if err := os.WriteFile(archive, body, 0o644); err != nil {
		t.Fatal(err)
	}
	// A record that cannot be read is no record of the same files.
	if err := os.WriteFile(filepath.Join(k.rel, release.DeployRecord), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := view(); d.State != "unrecorded" || d.Error == "" {
		t.Fatalf("a record that cannot be read: %+v", d)
	}
}

// An update is not held while crew-ctl and its files on the machine differ from the running
// release's (16.11, since 0.55): Versions says so, and a release of text still installs by itself after
// its notice (P5 of M16).
func TestAnUpdateIsNotHeldByDifferingDeployFiles(t *testing.T) {
	k := newBuildKit(t)
	b, _ := json.Marshal(map[string]any{"files": map[string]string{"deploy/crew-ctl": strings.Repeat("0", 64)}})
	if err := os.WriteFile(filepath.Join(k.rel, release.DeployRecord), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := k.s.View(context.Background()); err != nil || v.DeployFiles.State != "differ" {
		t.Fatalf("the deploy files: %+v %v", v.DeployFiles, err)
	}
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	if r := k.get("0.2.0"); r.State != "waiting" || r.Held || r.InstallAfter == nil {
		t.Fatalf("%+v", r)
	}
	k.now = k.now.Add(24 * time.Hour)
	k.pass()
	k.u.Wait()
	if k.running() != "0.2.0" {
		t.Fatalf("held by the deploy files: %s, %+v", k.running(), k.get("0.2.0"))
	}
}
