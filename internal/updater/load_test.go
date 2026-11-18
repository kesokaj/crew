package updater_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

// build is the folder a build of version left under the updater's Builds, as the server copied it
// out of the check sandbox, and the ask that names its hashes.
func (k *kit) build(version string) updater.LoadAsk {
	k.t.Helper()
	k.u.Builds = filepath.Join(filepath.Dir(k.rel), "builds")
	k.u.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	dir := filepath.Join(k.u.Builds, "b-"+version)
	a := updater.LoadAsk{Version: version, Dir: dir, Hashes: map[string]string{}, Commit: strings.Repeat("c", 40),
		Files: map[string]string{"go.mod": strings.Repeat("1", 40)}, Runtime: map[string]string{"versions.lock": strings.Repeat("2", 64)},
		Schema: 55, ReadBy: 0, MachineSetup: 2, Pins: map[string]string{"agy": "1.4.0", "postgres": "18.6-trixie"}, Notes: "# Crew " + version + "\n"}
	files := []string{release.DeployFile, release.BrowserFile}
	for _, n := range release.Images {
		files = append(files, "images/"+n+".tar")
	}
	for _, name := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			k.t.Fatal(err)
		}
		body := []byte(name + " of " + version)
		if err := os.WriteFile(p, body, 0o640); err != nil {
			k.t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		a.Hashes[name] = hex.EncodeToString(sum[:])
	}
	return a
}

// A build's files become a release folder of the updater's (16.2 step 4): each copied and checked
// against the hash the server read out of the sandbox, each image loaded under its own tag, and
// release.json written from the facts the server gave and the hashes the updater made.
func TestALoadMakesTheReleaseFolder(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	a := k.build("0.2.0")
	got, err := k.u.Load(context.Background(), a)
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
	if got.ManifestSHA256 != f.ManifestSHA256 || m.Commit != a.Commit || m.Schema != 55 || m.MachineSetup != 2 ||
		m.RuntimeSHA256 != release.RuntimeSHA256(a.Runtime) || m.Deploy == nil || m.Deploy.SHA256 != a.Hashes[release.DeployFile] ||
		m.Images["crew-server"].Tag != "crew-server:0.2.0" || m.Images["postgres"].Tag != "postgres:18.6-trixie" || m.Pins["agy"] != "1.4.0" {
		t.Fatalf("%+v", m)
	}
	if notes, _ := os.ReadFile(filepath.Join(k.rel, "0.2.0", release.NotesFile)); string(notes) != a.Notes {
		t.Fatalf("notes %q", notes)
	}
	if f.Gate != nil {
		t.Fatal("a loaded build has a gate")
	}
	for _, n := range []string{"crew-server:0.2.0", "crew-caddy:0.2.0", "crew-runtime:0.2.0", "crew-updater:0.2.0", "postgres:18.6-trixie"} {
		if !slices.Contains(k.m.Calls(), "LoadImage "+n) {
			t.Errorf("%s was not loaded: %v", n, k.m.Calls())
		}
	}
	if _, err := k.u.Load(context.Background(), k.build("0.2.0")); err == nil || !strings.Contains(err.Error(), "built once") {
		t.Fatalf("a second load of 0.2.0: %v", err)
	}
}

// A load that is refused makes no release folder and leaves no image tagged: a file whose hash is not
// the build's, a link, a folder that is not a build's, a version that is no release's, and an image
// that does not load.
func TestALoadRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(k *kit, a *updater.LoadAsk)
		want   string
	}{
		"another hash": {func(k *kit, a *updater.LoadAsk) {
			os.WriteFile(filepath.Join(a.Dir, "images", "crew-runtime.tar"), []byte("another image"), 0o640)
		}, "not the build's"},
		"a link": {func(k *kit, a *updater.LoadAsk) {
			secret := filepath.Join(k.t.TempDir(), "secret.key")
			os.WriteFile(secret, []byte("images/crew-server.tar of 0.2.0"), 0o600)
			p := filepath.Join(a.Dir, "images", "crew-server.tar")
			os.Remove(p)
			os.Symlink(secret, p)
		}, "not a plain file"},
		"no hash":         {func(k *kit, a *updater.LoadAsk) { delete(a.Hashes, release.DeployFile) }, "no hash"},
		"another folder":  {func(k *kit, a *updater.LoadAsk) { a.Dir = filepath.Dir(k.rel) }, "not a build's folder"},
		"a folder deeper": {func(k *kit, a *updater.LoadAsk) { a.Dir = filepath.Join(a.Dir, "images") }, "not a build's folder"},
		"a dev version":   {func(k *kit, a *updater.LoadAsk) { a.Version = "0.2.0-dev.abcdef1" }, "not the version"},
		"no load":         {func(k *kit, a *updater.LoadAsk) { k.m.fail["LoadImage crew-runtime:0.2.0"] = os.ErrInvalid }, "loading crew-runtime"},
		"no postgres pin": {func(k *kit, a *updater.LoadAsk) { delete(a.Pins, "postgres") }, "postgres"},
		"no room": {func(k *kit, a *updater.LoadAsk) {
			k.u.FreeSpace = func(string) (uint64, error) { return 1 << 30, nil }
		}, "bytes free"},
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t, "0.1.0", "0.1.0")
			a := k.build("0.2.0")
			tc.change(k, &a)
			if _, err := k.u.Load(context.Background(), a); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			entries, _ := os.ReadDir(k.rel)
			for _, e := range entries {
				if e.Name() != "0.1.0" {
					t.Fatalf("the load left %s", e.Name())
				}
			}
			if name == "no load" && !slices.Contains(k.m.Calls(), "Untag crew-server:0.2.0") {
				t.Fatalf("the images loaded before are still tagged: %v", k.m.Calls())
			}
		})
	}
}

// The socket takes a load from the server (16.11): 200 with the release's hash, 422 when refused.
func TestTheSocketTakesALoad(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	a := k.build("0.2.0")
	srv := httptest.NewServer(k.u.Handler())
	defer srv.Close()
	c := &updater.Client{Base: srv.URL, HTTP: srv.Client()}
	got, err := c.Load(context.Background(), a)
	if err != nil || got.Version != "0.2.0" || got.ManifestSHA256 == "" {
		t.Fatalf("%+v %v", got, err)
	}
	var refused *updater.Refusal
	if _, err := c.Load(context.Background(), a); err == nil || !errors.As(err, &refused) {
		t.Fatalf("a second load: %v", err)
	}
	if code := post(t, srv.URL+"/load", "not json"); code != http.StatusBadRequest {
		t.Fatalf("not JSON: %d", code)
	}
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A load and a request of the updater exclude each other: both read and change Docker's tags (review
// of M16 P3, round 2).
func TestALoadWaitsForARequest(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	blockLoad := make(chan struct{})
	var blockOnce sync.Once
	unblockLoad := func() { blockOnce.Do(func() { close(blockLoad) }) }
	t.Cleanup(unblockLoad)
	k.m.blockLoad = blockLoad
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := k.u.Load(context.Background(), k.build("0.3.0")); !errors.Is(err, updater.ErrBusy) {
		t.Fatalf("a load during an install: %v", err)
	}
	unblockLoad()
	k.u.Wait()
	k.m.blockLoad = nil
	release := make(chan struct{})
	var releaseOnce sync.Once
	unhold := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unhold)
	// One signal is kept until the test reads it: the load can reach LoadImage first (BUG-024).
	entered := make(chan struct{}, 1)
	k.m.loadImage = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	done := make(chan error)
	go func() {
		_, err := k.u.Load(context.Background(), k.build("0.3.0"))
		done <- err
	}()
	<-entered
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.1.0", AskedBy: "operator"}, false); !errors.Is(err, updater.ErrBusy) {
		t.Fatalf("an install during a load: %v", err)
	}
	unhold()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
