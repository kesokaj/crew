package updater_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// The files of ../release/testdata/images, which docker save wrote on Docker 29.7.2, with the ids
// each image store gave the image when it was loaded (D48).
type storeFixture struct {
	Config string            `json:"config"`
	Loaded map[string]string `json:"loaded"`
}

func storeFixtures(t *testing.T) map[string]storeFixture {
	t.Helper()
	b, err := os.ReadFile("../release/testdata/images/ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]storeFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// retagged writes the fixture file name to dst with the tag tag in place of its own, in manifest.json,
// index.json, and repositories, as docker save of that tag would name it; its blobs, and with them
// its digests, are the same.
func retagged(t *testing.T, name, dst, tag string) {
	t.Helper()
	f, err := os.Open(filepath.Join("../release/testdata/images", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	own := map[string]string{"classic.tar": "crew-idfixture:1", "containerd-resaved.tar": "crew-idfixture:1", "containerd-built.tar": "crew-idfixture:2"}[name]
	repo, ver, _ := strings.Cut(tag, ":")
	ownRepo, ownVer, _ := strings.Cut(own, ":")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		switch h.Name {
		case "manifest.json", "index.json":
			b = []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), own, tag), `"org.opencontainers.image.ref.name":"`+ownVer+`"`, `"org.opencontainers.image.ref.name":"`+ver+`"`))
		case "repositories":
			b = []byte(strings.Replace(string(b), `{"`+ownRepo+`":{"`+ownVer+`":`, `{"`+repo+`":{"`+ver+`":`, 1))
		}
		h.Size = int64(len(b))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeFolder is a release folder of 0.2.0 whose image files are real files docker save wrote: the
// runtime's built on the containerd store, with an attestation, the others' on the classic store, each
// under its own tag; release.json names each by its config digest.
func storeFolder(t *testing.T) (string, map[string]storeFixture) {
	t.Helper()
	fx := storeFixtures(t)
	dir := filepath.Join(t.TempDir(), "0.2.0")
	_ = os.MkdirAll(filepath.Join(dir, release.ImagesDir), 0o755)
	of := map[string]storeFixture{}
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		file, tag := "classic.tar", n+":0.2.0"
		if n == "crew-runtime" {
			file = "containerd-built.tar"
		}
		if n == "postgres" {
			tag = "postgres:18.6-trixie"
		}
		retagged(t, file, filepath.Join(dir, release.ImagesDir, n+".tar"), tag)
		of[n] = fx[file]
		imgs[n] = release.Image{Tag: tag, ID: fx[file].Config}
	}
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return dir, of
}

// A release's images load on either image store: Docker's id is the config digest release.json names
// on the classic store, a manifest's or an index's digest of the file on the containerd store (D48),
// and any other is refused.
func TestLoadingTakesTheIDOfEitherImageStore(t *testing.T) {
	dir, of := storeFolder(t)
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []string{"classic", "containerd"} {
		c, r := composeKit(t)
		for _, n := range release.Images {
			r.out["docker image inspect --format {{.Id}} "+f.Manifest.Images[n].Tag] = of[n].Loaded[store] + "\n"
		}
		if err := c.Load(context.Background(), f); err != nil {
			t.Fatalf("%s: %v", store, err)
		}
	}
	c, r := composeKit(t)
	for _, n := range release.Images {
		r.out["docker image inspect --format {{.Id}} "+f.Manifest.Images[n].Tag] = of[n].Loaded["containerd"] + "\n"
	}
	// The runtime's file is the classic image's: the containerd id of the other is not its own.
	r.out["docker image inspect --format {{.Id}} crew-runtime:0.2.0"] = of["crew-server"].Loaded["containerd"] + "\n"
	if err := c.Load(context.Background(), f); err == nil || !strings.Contains(err.Error(), "crew-runtime:0.2.0 loaded as "+of["crew-server"].Loaded["containerd"]) {
		t.Fatalf("another image's id: %v", err)
	}
}

// A file that holds another image than release.json names is refused before it is loaded.
func TestAFileOfAnotherImageIsNotLoaded(t *testing.T) {
	dir, of := storeFolder(t)
	retagged(t, "classic.tar", filepath.Join(dir, release.ImagesDir, "crew-runtime.tar"), "crew-runtime:0.2.0")
	// release.json is written again, so the file has the hash it names and only the image differs.
	m, _ := release.Read(dir)
	imgs := m.Manifest.Images
	img := imgs["crew-runtime"]
	img.ID = of["crew-runtime"].Config
	imgs["crew-runtime"] = img
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs}, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := release.Read(dir)
	c, r := composeKit(t)
	for _, n := range release.Images {
		r.out["docker image inspect --format {{.Id}} "+f.Manifest.Images[n].Tag] = of[n].Loaded["classic"] + "\n"
	}
	err := c.Load(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "holds the image "+storeFixtures(t)["classic.tar"].Config) {
		t.Fatalf("%v", err)
	}
	for _, x := range r.ran {
		if strings.Contains(x.cmd, "crew-runtime.tar") {
			t.Fatalf("the file was loaded: %s", x.cmd)
		}
	}
}

// An image a release pull took is in Docker already: it is not loaded, and is checked by the config
// digest or the manifest digest pulled.json names; one that is not on the machine, or is another, is
// refused (16.12).
func TestAPulledImageIsCheckedInDocker(t *testing.T) {
	dir, of := storeFolder(t)
	_ = os.Remove(filepath.Join(dir, release.ImagesDir, "crew-runtime.tar"))
	manifest := of["crew-runtime"].Loaded["containerd"]
	pulled := `{"version": "0.2.0", "images": {"crew-runtime": {"config": "` + of["crew-runtime"].Config + `", "manifest": "` + manifest + `", "from": "r.test/o/crew-runtime:0.2.0"}}}`
	if err := os.WriteFile(filepath.Join(dir, release.PulledFile), []byte(pulled), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		manifest:                            "",
		of["crew-runtime"].Config:           "",
		of["crew-server"].Config:            "crew-runtime:0.2.0 is " + of["crew-server"].Config + " on this machine",
		"sha256:" + strings.Repeat("e", 64): "crew-runtime:0.2.0 is sha256:eeee",
	} {
		c, r := composeKit(t)
		for _, n := range release.Images {
			r.out["docker image inspect --format {{.Id}} "+f.Manifest.Images[n].Tag] = of[n].Loaded["classic"] + "\n"
		}
		r.out["docker image inspect --format {{.Id}} crew-runtime:0.2.0"] = got + "\n"
		err := c.Load(context.Background(), f)
		if (want == "" && err != nil) || (want != "" && (err == nil || !strings.Contains(err.Error(), want))) {
			t.Errorf("%s: %v", got, err)
		}
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker load") && strings.Contains(x.cmd, "crew-runtime") {
				t.Errorf("a pulled image was loaded: %s", x.cmd)
			}
		}
	}
	c, r := composeKit(t)
	for _, n := range release.Images {
		r.out["docker image inspect --format {{.Id}} "+f.Manifest.Images[n].Tag] = of[n].Loaded["classic"] + "\n"
	}
	r.fail = "inspect --format {{.Id}} crew-runtime:0.2.0"
	if err := c.Load(context.Background(), f); err == nil || !strings.Contains(err.Error(), "the pulled image crew-runtime:0.2.0 is not on this machine") {
		t.Fatalf("not on the machine: %v", err)
	}
}

// A build the updater loads on a machine whose store is containerd's gets release.json's id from the
// file, its config digest, not the manifest digest the load gave; a load that gives the id of another
// image is refused, and its tags taken away (D48).
func TestALoadedBuildNamesItsImagesByTheirConfig(t *testing.T) {
	fx := storeFixtures(t)["containerd-built.tar"]
	k := newKit(t, "0.1.0", "0.1.0")
	a := k.build("0.2.0")
	p := filepath.Join(a.Dir, release.ImagesDir, "crew-runtime.tar")
	retagged(t, "containerd-built.tar", p, "crew-runtime:0.2.0")
	body, _ := os.ReadFile(p)
	sum := sha256.Sum256(body)
	a.Hashes["images/crew-runtime.tar"] = hex.EncodeToString(sum[:])
	k.m.loadIDs = map[string]string{"crew-runtime:0.2.0": fx.Loaded["containerd"]}
	if _, err := k.u.Load(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(filepath.Join(k.rel, "0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Manifest.Images["crew-runtime"].ID; got != fx.Config {
		t.Fatalf("release.json names %s, want the config %s", got, fx.Config)
	}

	b := k.build("0.3.0")
	p = filepath.Join(b.Dir, release.ImagesDir, "crew-runtime.tar")
	retagged(t, "containerd-built.tar", p, "crew-runtime:0.3.0")
	body, _ = os.ReadFile(p)
	sum = sha256.Sum256(body)
	b.Hashes["images/crew-runtime.tar"] = hex.EncodeToString(sum[:])
	k.m.loadIDs = map[string]string{"crew-runtime:0.3.0": "sha256:" + strings.Repeat("e", 64)}
	if _, err := k.u.Load(context.Background(), b); err == nil || !strings.Contains(err.Error(), "which is not the image its file holds") {
		t.Fatalf("another image's id: %v", err)
	}
	if _, err := os.Stat(filepath.Join(k.rel, "0.3.0")); err == nil {
		t.Fatal("a refused load made a release folder")
	}
	untagged := false
	for _, c := range k.m.Calls() {
		untagged = untagged || c == "Untag crew-runtime:0.3.0"
	}
	if !untagged {
		t.Fatalf("the refused load's tags stay: %v", k.m.Calls())
	}
}

// On the containerd image store a config digest names no image, so the check server and the smoke
// microVM start from the id this machine gives the release's tag, once it is the image release.json
// names; one that is not is refused, and nothing starts (D48).
func TestTheCheckStartsTheImageByThisMachinesID(t *testing.T) {
	dir, of := storeFolder(t)
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, r := composeKit(t)
	installFile(t, c)
	r.out[serverID] = of["crew-server"].Loaded["containerd"] + "\n"
	r.out[runtimeID] = of["crew-runtime"].Loaded["containerd"] + "\n"
	r.out["docker run --rm --name crew-gate-smoke"] = "{\"type\":\"crew_proof\",\"kernel\":\"6.12-kata\"}\n"
	if err := c.StartCheck(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if err := c.Smoke(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	var check, smoke string
	for _, x := range r.ran {
		switch {
		case strings.HasPrefix(x.cmd, "docker run -d --name crew-server-check"):
			check = x.cmd
		case strings.HasPrefix(x.cmd, "docker run --rm --name crew-gate-smoke"):
			smoke = x.cmd
		}
	}
	if !strings.Contains(check, " "+of["crew-server"].Loaded["containerd"]+" serve --check") || !strings.Contains(smoke, " "+of["crew-runtime"].Loaded["containerd"]+" proof") {
		t.Fatalf("check %q\nsmoke %q", check, smoke)
	}
	for _, lookup := range []string{serverID, runtimeID} {
		c, r := composeKit(t)
		installFile(t, c)
		r.out[serverID] = of["crew-server"].Loaded["containerd"] + "\n"
		r.out[runtimeID] = of["crew-runtime"].Loaded["containerd"] + "\n"
		r.out[lookup] = "sha256:" + strings.Repeat("e", 64) + "\n"
		errCheck, errSmoke := c.StartCheck(context.Background(), f), c.Smoke(context.Background(), f)
		if (lookup == serverID && (errCheck == nil || !strings.Contains(errCheck.Error(), "and release.json names"))) ||
			(lookup == runtimeID && (errSmoke == nil || !strings.Contains(errSmoke.Error(), "and release.json names"))) {
			t.Fatalf("%s: %v %v", lookup, errCheck, errSmoke)
		}
		for _, x := range r.ran {
			if strings.Contains(x.cmd, "sha256:"+strings.Repeat("e", 64)) {
				t.Fatalf("%s: started another image: %s", lookup, x.cmd)
			}
		}
	}
}

// A pulled release has no image files: the check server, the release's crew admin, and the smoke
// microVM start from the manifest pulled.json names when this machine's tag is it, and from no other
// (review of M17 phase 7).
func TestAPulledReleaseStartsByThePulledManifest(t *testing.T) {
	dir, of := storeFolder(t)
	for _, n := range []string{"crew-server", "crew-runtime"} {
		_ = os.Remove(filepath.Join(dir, release.ImagesDir, n+".tar"))
	}
	server, runtime := sha("server manifest"), sha("runtime manifest")
	pulled := `{"version": "0.2.0", "images": {"crew-server": {"config": "` + of["crew-server"].Config + `", "manifest": "` + server + `", "from": "r"}, ` +
		`"crew-runtime": {"config": "` + of["crew-runtime"].Config + `", "manifest": "` + runtime + `", "from": "r"}}}`
	_ = os.WriteFile(filepath.Join(dir, release.PulledFile), []byte(pulled), 0o644)
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := func(serverGot, runtimeGot string) (*recorder, error, error, error) {
		c, r := composeKit(t)
		installFile(t, c)
		r.out[serverID], r.out[runtimeID] = serverGot+"\n", runtimeGot+"\n"
		r.out["docker run --rm --name crew-gate-smoke"] = "{\"type\":\"crew_proof\",\"kernel\":\"6.12-kata\"}\n"
		return r, c.StartCheck(context.Background(), f), c.Smoke(context.Background(), f), c.RestoreScratch(context.Background(), f, writeSnapshot(t, c))
	}
	r, e1, e2, e3 := run(server, runtime)
	if e1 != nil || e2 != nil || e3 != nil {
		t.Fatalf("%v %v %v", e1, e2, e3)
	}
	starts := 0
	for _, x := range r.ran {
		if strings.HasPrefix(x.cmd, "docker run") {
			starts++
			if !strings.Contains(x.cmd, " "+server+" ") && !strings.Contains(x.cmd, " "+runtime+" ") {
				t.Errorf("not started by the pulled manifest: %s", x.cmd)
			}
		}
	}
	if starts != 4 {
		t.Fatalf("%d starts: %+v", starts, r.ran)
	}
	r, e1, e2, e3 = run(sha("other"), sha("other"))
	for _, e := range []error{e1, e2, e3} {
		if e == nil || !strings.Contains(e.Error(), "and release.json names") {
			t.Errorf("another image: %v", e)
		}
	}
	for _, x := range r.ran {
		if strings.HasPrefix(x.cmd, "docker run") {
			t.Fatalf("another image started: %s", x.cmd)
		}
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
