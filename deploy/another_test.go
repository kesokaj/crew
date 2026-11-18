package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// Another installation (16.12, D48): a card of a release, its images pulled from a registry with a
// token used once, checked by the config digest release.json names; or a whole folder, imported.

// fakeRegistryDocker is a docker on PATH that keeps its state in a file: the token its registry takes,
// the manifests it serves by reference, the id an image gets when pulled by a pin, and the tags. It
// writes its calls to the log, and the folder of each login to config-dirs.
const fakeRegistryDocker = `#!/usr/bin/env python3
import json, os, sys
d = os.environ["FAKE_DOCKER_DIR"]
args = sys.argv[1:]
with open(os.environ["FAKE_DOCKER_LOG"], "a") as f:
    f.write("docker " + " ".join(args) + "\n")
sp = os.path.join(d, "state.json")
st = json.load(open(sp))
def save():
    json.dump(st, open(sp, "w"))
cfg = None
if args[:1] == ["--config"]:
    cfg, args = args[1], args[2:]
if args[:1] == ["login"]:
    tok = sys.stdin.read().strip()
    with open(os.path.join(d, "config-dirs"), "a") as f:
        f.write(cfg + "\n")
    if tok != st["token"]:
        sys.exit("Error response from daemon: login attempt failed with status: 401 Unauthorized")
    c = json.load(open(os.path.join(cfg, "config.json")))
    # As docker does: with a credential helper installed, a config that holds no entry yet takes the
    # helper as its store; "store" is a docker that always does.
    if st.get("store") or (st.get("helper") and not c.get("auths")):
        c["credsStore"] = "pass"
    else:
        c.setdefault("auths", {})[args[1]] = {"auth": "eDp4"}
    json.dump(c, open(os.path.join(cfg, "config.json"), "w"))
elif args[:3] == ["buildx", "imagetools", "inspect"]:
    ref = args[-1]
    if not ref.startswith("docker.io/") and not os.path.exists(os.path.join(cfg or "/nonexistent", "config.json")):
        sys.exit("ERROR: unexpected status: 401 Unauthorized")
    if ref not in st["manifests"]:
        sys.exit("ERROR: " + ref + ": not found")
    sys.stdout.write(st["manifests"][ref])
elif args[:1] == ["pull"]:
    pin = args[-1]
    if pin not in st["pulled"]:
        sys.exit("not found: " + pin)
    st["refs"][pin] = st["pulled"][pin]
    save()
elif args[:1] == ["tag"]:
    st["tags"][args[2]] = st["refs"][args[1]]
    save()
elif args[:2] == ["image", "inspect"]:
    x = args[-1]
    i = st["tags"].get(x) or st["refs"].get(x)
    if not i:
        sys.exit("Error: No such image: " + x)
    print(i)
elif args[:2] == ["image", "rm"]:
    st["tags"].pop(args[-1], None)
    st["refs"].pop(args[-1], None)
    save()
`

type registryState struct {
	Token     string            `json:"token"`
	Store     bool              `json:"store"`
	Helper    bool              `json:"helper"`
	Manifests map[string]string `json:"manifests"`
	Pulled    map[string]string `json:"pulled"`
	Refs      map[string]string `json:"refs"`
	Tags      map[string]string `json:"tags"`
}

func sha(b string) string {
	s := sha256.Sum256([]byte(b))
	return "sha256:" + hex.EncodeToString(s[:])
}

// cardKit is an installed crew-ctl, a card of 0.3.0 brought from the main installation, a registry
// that serves each image of it, and a token file.
type cardKit struct {
	*ctl
	card, token, registry string
	ids                   map[string]string // the config digest release.json names
	pins                  map[string]string // the manifest each image is pulled by
	state                 registryState
}

const registryName = "registry.test:5000/owner"

func newCardKit(t *testing.T) *cardKit {
	t.Helper()
	c := newCtl(t)
	c.install(good)
	k := &cardKit{ctl: c, registry: registryName, ids: map[string]string{}, pins: map[string]string{},
		state: registryState{Token: "made-up-token", Manifests: map[string]string{}, Pulled: map[string]string{}, Refs: map[string]string{}, Tags: map[string]string{}}}
	if err := os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte(fakeRegistryDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	// Root's owner for a kept folder: the tests run without root, so install takes no owner and chown
	// does nothing.
	for tool, script := range map[string]string{
		"install": "#!/bin/sh\nfor a; do shift; case \"$skip$a\" in 1*) skip= ;; -o|-g) skip=1 ;; *) set -- \"$@\" \"$a\" ;; esac; done\nexec /usr/bin/install \"$@\"\n",
		"chown":   "#!/bin/sh\necho \"chown $*\" >> " + c.log + "\n",
	} {
		if err := os.WriteFile(filepath.Join(c.root, "bin", tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c.env = append(c.env, "FAKE_DOCKER_DIR="+c.root, "FAKE_DOCKER_LOG="+c.log)
	k.token = filepath.Join(c.root, "token")
	if err := os.WriteFile(k.token, []byte("probe:made-up-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// PostgreSQL's image is an index on Docker Hub, which the deploy archive's versions.lock pins.
	pgManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + sha("postgres config") + `"}}`
	pgIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"digest":"` + sha("arm") + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}},` +
		`{"digest":"` + sha(pgManifest) + `","platform":{"os":"linux","architecture":"amd64"}}]}`
	k.state.Manifests["docker.io/library/postgres@"+sha(pgIndex)] = pgIndex
	k.state.Manifests["docker.io/library/postgres@"+sha(pgManifest)] = pgManifest

	dir := filepath.Join(c.root, "brought", "0.3.0")
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		_ = os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644)
		tag, id := n+":0.3.0", sha(n+" config")
		if n == "postgres" {
			tag, id = "postgres:18.6-trixie", sha("postgres config")
			// Pulled by the index the lock pins, which Compose names it by.
			k.pins[n] = "docker.io/library/postgres@" + sha(pgIndex)
		} else {
			m := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + id + `"}}`
			k.state.Manifests[registryName+"/"+n+":0.3.0"] = m
			k.pins[n] = registryName + "/" + n + "@" + sha(m)
		}
		imgs[n] = release.Image{Tag: tag, ID: id}
		k.ids[n] = id
		// The containerd store gives the manifest's digest, the classic one the config's.
		k.state.Pulled[k.pins[n]] = k.pins[n][strings.LastIndex(k.pins[n], "@")+1:]
		if n == "crew-caddy" {
			k.state.Pulled[k.pins[n]] = id
		}
	}
	writeDeployArchive(t, filepath.Join(dir, "deploy.tar.gz"), "POSTGRES_IMAGE=postgres:18.6-trixie@"+sha(pgIndex)+"\n")
	if _, err := release.Write(dir, release.Facts{Version: "0.3.0", Commit: "c", BuiltAt: time.Now(), Schema: 51, MachineSetup: 2,
		Images: imgs, Deploy: release.DeployFile}, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := release.Read(dir)
	if err := f.WriteGate(release.Gate{Passed: true, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// A card: the release folder without its images.
	if err := os.RemoveAll(filepath.Join(dir, "images")); err != nil {
		t.Fatal(err)
	}
	k.card = dir
	k.save()
	return k
}

func (k *cardKit) save() {
	k.t.Helper()
	b, _ := json.Marshal(k.state)
	if err := os.WriteFile(filepath.Join(k.root, "state.json"), b, 0o644); err != nil {
		k.t.Fatal(err)
	}
}

func (k *cardKit) load() registryState {
	k.t.Helper()
	b, _ := os.ReadFile(filepath.Join(k.root, "state.json"))
	var s registryState
	_ = json.Unmarshal(b, &s)
	return s
}

// loginsGone reports whether every folder a login used is removed again, its login with it.
func (k *cardKit) loginsGone() bool {
	b, _ := os.ReadFile(filepath.Join(k.root, "config-dirs"))
	for _, d := range strings.Fields(string(b)) {
		if _, err := os.Stat(d); err == nil {
			return false
		}
	}
	return len(b) > 0
}

func writeDeployArchive(t *testing.T, path, lock string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"versions.lock": lock, "deploy/crew-ctl": "#!/bin/sh\n"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (k *cardKit) pull() (string, int) {
	k.t.Helper()
	return k.run("release", "pull", k.card, "--registry", k.registry, "--registry-token-file", k.token)
}

// A release pull logs in once with the token on standard input, takes each image by the digest of a
// manifest whose config is the one release.json names, PostgreSQL's from Docker Hub by the deploy
// archive's lock, gives it its tag, and keeps the card as the release folder with pulled.json, which
// the updater's own check takes; the login is gone after it.
func TestAReleasePullTakesEachImageByItsConfig(t *testing.T) {
	k := newCardKit(t)
	out, code := k.pull()
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, k.calls())
	}
	dst := filepath.Join(k.root, "data", "releases", "0.3.0")
	f, err := release.Read(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	for _, n := range release.Images {
		p := f.Pulled[n]
		if p.Config != k.ids[n] || n != "postgres" && p.From != registryName+"/"+n+":0.3.0" || "@"+p.Manifest != k.pins[n][strings.LastIndex(k.pins[n], "@"):] {
			t.Errorf("%s: %+v", n, p)
		}
		if !strings.Contains(k.calls(), "pull --quiet "+k.pins[n]+"\n") {
			t.Errorf("%s was not pulled by its manifest's digest:\n%s", n, k.calls())
		}
	}
	st := k.load()
	// The registry's names are gone; PostgreSQL's by its digest stays, which Compose names it by.
	if st.Tags["crew-server:0.3.0"] != k.pins["crew-server"][strings.LastIndex(k.pins["crew-server"], "@")+1:] || st.Tags["crew-caddy:0.3.0"] != k.ids["crew-caddy"] ||
		st.Tags["postgres:18.6-trixie"] == "" || len(st.Refs) != 1 || st.Refs[k.pins["postgres"]] == "" {
		t.Fatalf("tags %v, refs %v", st.Tags, st.Refs)
	}
	if _, err := os.Stat(filepath.Join(dst, "images")); err == nil {
		t.Fatal("a pulled release has an images folder")
	}
	if strings.Contains(k.calls()+out, "made-up-token") || !strings.Contains(k.calls(), "login registry.test:5000 -u probe --password-stdin") {
		t.Fatalf("the token was in the calls or the output, or no login:\n%s\n%s", k.calls(), out)
	}
	if !k.loginsGone() {
		t.Fatal("the login's folder is still there")
	}
	if out, code := k.pull(); code == 0 || !strings.Contains(out, "is on this machine already") {
		t.Fatalf("a second pull: %d\n%s", code, out)
	}
}

// An image whose tag names an index, as a build with attestations pushes one, is pulled by the one
// linux/amd64 manifest that was checked, not by the index; a card that brought a pulled.json has it
// replaced by the pull's own.
func TestAPullTakesTheManifestItChecked(t *testing.T) {
	k := newCardKit(t)
	child := k.pins["crew-runtime"][strings.LastIndex(k.pins["crew-runtime"], "@")+1:]
	k.state.Manifests[k.pins["crew-runtime"]] = k.state.Manifests[registryName+"/crew-runtime:0.3.0"]
	k.state.Manifests[registryName+"/crew-runtime:0.3.0"] = `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"digest":"` + child + `","platform":{"os":"linux","architecture":"amd64"}},` +
		`{"digest":"` + sha("attestation") + `","platform":{"os":"unknown","architecture":"unknown"}}]}`
	k.save()
	_ = os.WriteFile(filepath.Join(k.card, "pulled.json"), []byte(`{"version": "0.3.0", "images": {}}`), 0o644)
	if out, code := k.pull(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(k.calls(), "pull --quiet "+k.pins["crew-runtime"]+"\n") {
		t.Fatalf("not pulled by the manifest:\n%s", k.calls())
	}
	f, err := release.Read(filepath.Join(k.root, "data", "releases", "0.3.0"))
	if err != nil || len(f.Pulled) != 5 || f.Pulled["crew-runtime"].Manifest != child {
		t.Fatalf("%v %+v", err, f)
	}
}

// A pull is refused, with what it tagged taken away and nothing kept, when the registry has another
// image under the tag, when an index has no one linux/amd64 manifest, when a tag names another image
// here already, when the lock pins another PostgreSQL than release.json names, or when the token is
// wrong; the login is gone after each.
func TestAReleasePullRefusesAnotherImage(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(k *cardKit)
		want   string
	}{
		"another config": {func(k *cardKit) {
			k.state.Manifests[registryName+"/crew-runtime:0.3.0"] = `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + sha("evil") + `"}}`
		}, "is the image " + sha("evil")[:19]},
		"a manifest of another hash": {func(k *cardKit) {
			for ref, body := range k.state.Manifests {
				if strings.HasPrefix(ref, "docker.io/library/postgres@") && strings.Contains(body, `"manifests"`) {
					k.state.Manifests[ref] = body + " "
				}
			}
		}, "the registry gave a manifest of another hash"},
		"no tag in the registry": {func(k *cardKit) { delete(k.state.Manifests, registryName+"/crew-updater:0.3.0") }, "not found"},
		// Docker's own choice of a platform could take a variant's entry, which was not checked.
		"an index with a variant": {func(k *cardKit) {
			child := k.pins["crew-runtime"][strings.LastIndex(k.pins["crew-runtime"], "@")+1:]
			k.state.Manifests[k.pins["crew-runtime"]] = k.state.Manifests[registryName+"/crew-runtime:0.3.0"]
			k.state.Manifests[registryName+"/crew-runtime:0.3.0"] = `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
				`{"digest":"` + child + `","platform":{"os":"linux","architecture":"amd64"}},` +
				`{"digest":"` + sha("evil") + `","platform":{"os":"linux","architecture":"amd64","variant":"v3"}}]}`
		}, "or another Docker could take for it"},
		"an index with one for x86_64": {func(k *cardKit) {
			child := k.pins["crew-runtime"][strings.LastIndex(k.pins["crew-runtime"], "@")+1:]
			k.state.Manifests[k.pins["crew-runtime"]] = k.state.Manifests[registryName+"/crew-runtime:0.3.0"]
			k.state.Manifests[registryName+"/crew-runtime:0.3.0"] = `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
				`{"digest":"` + child + `","platform":{"os":"linux","architecture":"amd64"}},` +
				`{"digest":"` + sha("evil") + `","platform":{"os":"linux","architecture":"x86_64"}}]}`
		}, "or another Docker could take for it"},
		"a tag of another image here": {func(k *cardKit) { k.state.Tags["crew-runtime:0.3.0"] = sha("running") }, "names another image on this machine already"},
		"another id after the pull":   {func(k *cardKit) { k.state.Pulled[k.pins["crew-runtime"]] = sha("other") }, "was pulled as " + sha("other")},
		"a wrong token":               {func(k *cardKit) { k.state.Token = "another" }, "refused the login"},
		"a changed deploy archive": {func(k *cardKit) {
			writeDeployArchive(k.t, filepath.Join(k.card, "deploy.tar.gz"), "POSTGRES_IMAGE=postgres:17.1@"+sha("x")+"\n")
		}, "the deploy archive does not have the hash release.json names"},
	} {
		t.Run(name, func(t *testing.T) {
			k := newCardKit(t)
			tc.change(k)
			k.save()
			out, code := k.pull()
			if code == 0 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want %q:\n%s", code, tc.want, out)
			}
			if _, err := os.Stat(filepath.Join(k.root, "data", "releases", "0.3.0")); err == nil {
				t.Fatal("a refused pull kept the release")
			}
			st := k.load()
			for tag := range st.Tags {
				if tag != "crew-runtime:0.3.0" || name != "a tag of another image here" {
					t.Errorf("the refused pull left the tag %s", tag)
				}
			}
			if !k.loginsGone() {
				t.Error("the login's folder is still there")
			}
		})
	}
}

// The token file is one line, USER:TOKEN; the registry is HOST[:PORT]/PATH; a pull needs an
// installed Crew; and install takes the token only with a card, on a machine with no Crew.
func TestTheTokenFileAndTheRegistryAreChecked(t *testing.T) {
	k := newCardKit(t)
	for body, want := range map[string]string{"": "the file is empty", "made-up-token\n": "holds one line, USER:TOKEN", ":tok\n": "USER:TOKEN", "probe:\n": "USER:TOKEN"} {
		_ = os.WriteFile(k.token, []byte(body), 0o600)
		if out, code := k.pull(); code == 0 || !strings.Contains(out, want) {
			t.Errorf("%q: exit %d\n%s", body, code, out)
		}
	}
	_ = os.WriteFile(k.token, []byte("probe:made-up-token\n"), 0o600)
	for _, reg := range []string{"", "Registry.test/o", "registry.test", "https://registry.test/o", "registry.test/o/../x"} {
		out, code := k.run("release", "pull", k.card, "--registry", reg, "--registry-token-file", k.token)
		if code == 0 || !strings.Contains(out, "--registry") {
			t.Errorf("%q: exit %d\n%s", reg, code, out)
		}
	}
	if out, code := k.run("release", "pull", k.card, "--registry", registryName, "--registry-token-file", filepath.Join(k.root, "none")); code == 0 || !strings.Contains(out, "no file this user can read") {
		t.Errorf("no file: %d\n%s", code, out)
	}
	// A link is refused as it was given, before realpath resolves it, and a file others may read is
	// refused (security review of M17's Close), and so is a file of another owner than the user or
	// SUDO_UID, such as root's /etc/hostname, which every user may read. Not tested here: a link in a
	// folder of the path, which realpath resolves and is not refused, and a file swapped between the
	// checks and the read, which one open of it with no link followed rules out.
	link := filepath.Join(k.root, "token-link")
	if err := os.Symlink(k.token, link); err != nil {
		t.Fatal(err)
	}
	if out, code := k.run("release", "pull", k.card, "--registry", registryName, "--registry-token-file", link); code == 0 || !strings.Contains(out, "is a link") {
		t.Errorf("a link: %d\n%s", code, out)
	}
	if os.Getuid() != 0 {
		env := k.env
		k.env = append(env, "SUDO_UID=")
		if out, code := k.run("release", "pull", k.card, "--registry", registryName, "--registry-token-file", "/etc/hostname"); code == 0 || !strings.Contains(out, "is not yours: its owner is uid 0") {
			t.Errorf("root's file: %d\n%s", code, out)
		}
		k.env = env
	}
	for _, mode := range []os.FileMode{0o640, 0o604} {
		if err := os.Chmod(k.token, mode); err != nil {
			t.Fatal(err)
		}
		if out, code := k.pull(); code == 0 || !strings.Contains(out, "chmod 600 it") {
			t.Errorf("mode %o: %d\n%s", mode, code, out)
		}
	}
	if err := os.Chmod(k.token, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := k.run("release", "pull", k.card, "--registry", registryName); code == 0 || !strings.Contains(out, "needs --registry-token-file") {
		t.Errorf("no token: %d\n%s", code, out)
	}
	if out, code := k.run("install", "--registry", registryName, "--registry-token-file", k.token, "--env-file", k.answers("a.env", good)); code == 0 || !strings.Contains(out, "install --from CARD") {
		t.Errorf("install without a card: %d\n%s", code, out)
	}
	if out, code := k.run("install", "--from", k.card, "--registry", registryName, "--registry-token-file", k.token); code == 0 || !strings.Contains(out, "release pull brings a later release") {
		t.Errorf("install on an installed machine: %d\n%s", code, out)
	}
	fresh := newCtl(t)
	if out, code := fresh.run("release", "pull", k.card, "--registry", registryName, "--registry-token-file", k.token); code == 0 || !strings.Contains(out, "Crew is not installed") {
		t.Errorf("not installed: %d\n%s", code, out)
	}
}

// Another machine's first install from a card pulls its images after the host setup, logs in with the
// token on standard input, keeps the card with pulled.json, and loads no image file; the dry run says
// so and names no token.
func TestAFirstInstallFromACardPullsTheImages(t *testing.T) {
	k := newCardKit(t)
	c := newCtl(t)
	c.inCheckout()
	c.env = append(c.env, "FAKE_DOCKER_DIR="+k.root, "FAKE_DOCKER_LOG="+c.log)
	// The dry run ends at the proof, which needs an installed Crew, as every dry install here does.
	out, _ := c.run("install", "--from", k.card, "--registry", registryName, "--registry-token-file", k.token, "--dry-run", "--env-file", c.answers("a.env", good))
	setup, login := strings.Index(out, "host-setup.sh"), strings.Index(out, "+ docker login registry.test:5000 as probe")
	if setup < 0 || login < setup || strings.Contains(out, "made-up-token") || strings.Contains(out, "docker load") {
		t.Fatalf("the dry run:\n%s", out)
	}
	for _, n := range release.Images {
		ref := registryName + "/" + n + ":0.3.0"
		if n == "postgres" {
			ref = "docker.io/library/postgres@"
		}
		if !strings.Contains(out, "+ docker pull "+ref) {
			t.Errorf("no pull of %s:\n%s", n, out)
		}
	}
	if !strings.Contains(out, "pulled.json") {
		t.Fatalf("the card is not kept with pulled.json:\n%s", out)
	}
	// Without a token, a card is no release folder to load.
	out, code := c.run("install", "--from", k.card, "--dry-run", "--env-file", c.answers("b.env", good))
	if code == 0 || !strings.Contains(out, "holds no images") {
		t.Fatalf("a card without a token: %d\n%s", code, out)
	}
}

// A card is the release folder without its images; one that is there already is not written again.
func TestACardHoldsNoImages(t *testing.T) {
	k := newCardKit(t)
	// The release on this machine, with its images and its deploy archive.
	src := filepath.Join(k.root, "data", "releases", "0.4.0")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-r", k.card, src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	m, _ := os.ReadFile(filepath.Join(src, "release.json"))
	_ = os.WriteFile(filepath.Join(src, "release.json"), bytes.ReplaceAll(m, []byte(`"0.3.0`), []byte(`"0.4.0`)), 0o644)
	_ = os.MkdirAll(filepath.Join(src, "images"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "images", "crew-server.tar"), []byte("x"), 0o644)
	out := filepath.Join(k.root, "cards")
	_ = os.MkdirAll(out, 0o755)
	got, code := k.run("release", "card", "0.4.0", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, got)
	}
	var names []string
	entries, _ := os.ReadDir(filepath.Join(out, "0.4.0"))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Contains(names, "release.json") || !slices.Contains(names, "gate.json") || !slices.Contains(names, "deploy.tar.gz") ||
		slices.Contains(names, "images") || slices.Contains(names, "pulled.json") || len(names) != 3 {
		t.Fatalf("the card holds %v", names)
	}
	// The card is written into a folder no one else may change.
	open := filepath.Join(k.root, "open")
	_ = os.MkdirAll(open, 0o755)
	_ = os.Chmod(open, 0o777)
	if got, code := k.run("release", "card", "0.4.0", "--out", open); code == 0 || !strings.Contains(got, "is not sticky") {
		t.Fatalf("a folder others may write: %d\n%s", code, got)
	}
	if left, _ := os.ReadDir(open); len(left) != 0 {
		t.Fatalf("a refused card left %v", left)
	}
	if got, code := k.run("release", "card", "0.4.0", "--out", out); code == 0 || !strings.Contains(got, "is there already") {
		t.Fatalf("a second card: %d\n%s", code, got)
	}
	if got, code := k.run("release", "card", "9.9.9", "--out", out); code == 0 || !strings.Contains(got, "no release 9.9.9") {
		t.Fatalf("no release: %d\n%s", code, got)
	}
}

// A whole folder copied over is imported as it is, checked by its hashes; a card that was pulled on
// another machine, or a folder whose image changed, is refused.
func TestAReleaseImportKeepsAWholeFolder(t *testing.T) {
	k := newCardKit(t)
	src := releaseFolder(t, filepath.Join(k.root, "copied"), "0.5.0", true)
	// A copy a run that was killed left is taken away first.
	stale := filepath.Join(k.root, "data", "releases", "0.5.0.partial", "images")
	_ = os.MkdirAll(stale, 0o755)
	_ = os.WriteFile(filepath.Join(stale, "crew-server.tar"), []byte("old"), 0o644)
	if out, code := k.run("release", "import", src); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(k.root, "data", "releases", "0.5.0", "images", "crew-server.tar")); err != nil {
		t.Fatal(err)
	}
	if out, code := k.run("release", "import", src); code == 0 || !strings.Contains(out, "is on this machine already") {
		t.Fatalf("a second import: %d\n%s", code, out)
	}
	changed := releaseFolder(t, filepath.Join(k.root, "changed"), "0.6.0", true)
	body, _ := os.ReadFile(filepath.Join(changed, "images", "crew-runtime.tar"))
	_ = os.WriteFile(filepath.Join(changed, "images", "crew-runtime.tar"), []byte("another"), 0o644)
	if out, code := k.run("release", "import", changed); code == 0 || !strings.Contains(out, "does not have the hash") {
		t.Fatalf("a changed image: %d\n%s", code, out)
	}
	// A refused import leaves nothing behind, and the folder put right is imported.
	if left, _ := filepath.Glob(filepath.Join(k.root, "data", "releases", "0.6.0*")); len(left) != 0 {
		t.Fatalf("a refused import left %v", left)
	}
	_ = os.WriteFile(filepath.Join(changed, "images", "crew-runtime.tar"), body, 0o644)
	if out, code := k.run("release", "import", changed); code != 0 {
		t.Fatalf("the folder put right: %d\n%s", code, out)
	}
	// A folder that changes while it is copied is checked as the copy: one that then holds a pulled.json
	// is not kept.
	moving := releaseFolder(t, filepath.Join(k.root, "moving"), "0.7.0", true)
	_ = os.WriteFile(filepath.Join(k.root, "bin", "cp"), []byte("#!/bin/sh\n/bin/cp \"$@\" || exit\nfor a; do last=$a; done\ntouch \"$last/pulled.json\"\n"), 0o755)
	if out, code := k.run("release", "import", moving); code == 0 || !strings.Contains(out, "changed while it was copied") {
		t.Fatalf("a folder that changed: %d\n%s", code, out)
	}
	if left, _ := filepath.Glob(filepath.Join(k.root, "data", "releases", "0.7.0*")); len(left) != 0 {
		t.Fatalf("a refused copy left %v", left)
	}
	// One whose image changes while it is copied: the copy's check refuses it, and the copy goes.
	_ = os.WriteFile(filepath.Join(k.root, "bin", "cp"), []byte("#!/bin/sh\n/bin/cp \"$@\" || exit\nfor a; do last=$a; done\necho x >> \"$last/images/crew-runtime.tar\"\n"), 0o755)
	if out, code := k.run("release", "import", moving); code == 0 || !strings.Contains(out, "does not check") {
		t.Fatalf("an image that changed: %d\n%s", code, out)
	}
	_ = os.Remove(filepath.Join(k.root, "bin", "cp"))
	if left, _ := filepath.Glob(filepath.Join(k.root, "data", "releases", "0.7.0*")); len(left) != 0 {
		t.Fatalf("a copy that did not check left %v", left)
	}
	_ = os.WriteFile(filepath.Join(k.card, "pulled.json"), []byte(`{"version": "0.3.0", "images": {}}`), 0o644)
	if out, code := k.run("release", "import", k.card); code == 0 || !strings.Contains(out, "holds a pulled.json") {
		t.Fatalf("a pulled card: %d\n%s", code, out)
	}
	if out, code := k.run("release", "nothing", k.card); code == 0 || !strings.Contains(out, "release card X.Y.Z") {
		t.Fatalf("no such release verb: %d\n%s", code, out)
	}
}

// crew-ctl's check of an image file and the updater's agree on the files docker save wrote on either
// store: the config digest and each manifest that names it, and a file of another image is refused.
func TestCrewCtlTakesTheIDsTheUpdaterTakes(t *testing.T) {
	c := newCtl(t)
	b, err := os.ReadFile("../internal/release/testdata/images/ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]struct {
		Config string            `json:"config"`
		Loaded map[string]string `json:"loaded"`
	}
	_ = json.Unmarshal(b, &fx)
	own := map[string]string{"classic.tar": "crew-idfixture:1", "containerd-resaved.tar": "crew-idfixture:1", "containerd-built.tar": "crew-idfixture:2"}
	for name, f := range fx {
		p, _ := filepath.Abs(filepath.Join("../internal/release/testdata/images", name))
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, f.Config, own[name])
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		got := strings.Fields(string(out))
		file, err := release.ReadImageFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, file.IDs) {
			t.Errorf("%s: crew-ctl takes %v, the updater %v", name, got, file.IDs)
		}
		for store, id := range f.Loaded {
			if !slices.Contains(got, id) {
				t.Errorf("%s: the %s store's id %s is not taken", name, store, id)
			}
		}
		cmd = exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, sha("another"), own[name])
		cmd.Env = c.env
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "and release.json names") {
			t.Errorf("%s: another image was taken: %s", name, out)
		}
		// A file that would give another tag too, the running version's, is refused, as the
		// updater's Load refuses one.
		cmd = exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, f.Config, "crew-server:0.3.0")
		cmd.Env = c.env
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "also names "+own[name]) {
			t.Errorf("%s: another tag was taken: %s", name, out)
		}
	}
	// A manifest whose bytes are not its digest, or that names another config, is taken by neither.
	classic := fx["classic.tar"]
	for what, change := range map[string]func(body []byte) []byte{
		"another hash": func(b []byte) []byte { return append(b, ' ') },
		"another config": func(b []byte) []byte {
			return bytes.Replace(b, []byte(classic.Config[7:]), []byte(strings.Repeat("e", 64)), 1)
		},
	} {
		p := forged(t, "../internal/release/testdata/images/classic.tar", "blobs/sha256/"+classic.Loaded["containerd"][7:], change)
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, classic.Config, "crew-idfixture:1")
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		file, ferr := release.ReadImageFile(p)
		if err != nil || ferr != nil || !slices.Equal(strings.Fields(string(out)), file.IDs) || slices.Contains(file.IDs, classic.Loaded["containerd"]) {
			t.Errorf("%s: crew-ctl %q %v, the updater %v %v", what, out, err, file.IDs, ferr)
		}
	}
}

// An index's one manifest for linux/amd64, of no variant, must be the one that names the config, in
// crew-ctl as in the updater: an index with two, the unchecked one first, or one of a variant, gives
// no id of its own (security review of M17's Close).
func TestAnIndexWithAnotherManifestForThisPlatformIsNoID(t *testing.T) {
	c := newCtl(t)
	built := "../internal/release/testdata/images/containerd-built.tar"
	var fx map[string]struct {
		Config string            `json:"config"`
		Loaded map[string]string `json:"loaded"`
	}
	b, err := os.ReadFile("../internal/release/testdata/images/ids.json")
	if err != nil || json.Unmarshal(b, &fx) != nil {
		t.Fatal(err)
	}
	f := fx["containerd-built.tar"]
	ix := "blobs/sha256/" + f.Loaded["containerd"][7:]
	for what, from := range map[string][2]string{
		"two for linux/amd64":   {`"manifests": [`, `"manifests": [{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:` + strings.Repeat("b", 64) + `", "platform": {"architecture": "amd64", "os": "linux"}},`},
		"one of a variant":      {`"os": "linux"`, `"os": "linux", "variant": "v3"`},
		"one for x86_64 beside": {`"manifests": [`, `"manifests": [{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:` + strings.Repeat("b", 64) + `", "platform": {"architecture": "x86_64", "os": "linux"}},`},
	} {
		var body []byte
		p := forged(t, built, ix, func(b []byte) []byte {
			body = bytes.Replace(b, []byte(from[0]), []byte(from[1]), 1)
			return body
		})
		sum := sha256.Sum256(body)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		p = forged(t, p, ix, func([]byte) []byte { return body }, "blobs/sha256/"+digest[7:])
		p = forged(t, p, "index.json", func(b []byte) []byte { return bytes.ReplaceAll(b, []byte(f.Loaded["containerd"]), []byte(digest)) })
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, f.Config, "crew-idfixture:2")
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		file, ferr := release.ReadImageFile(p)
		if err != nil || ferr != nil || !slices.Equal(strings.Fields(string(out)), []string{f.Config}) || !slices.Equal(file.IDs, []string{f.Config}) {
			t.Errorf("%s: crew-ctl %q %v, the updater %v %v", what, out, err, file.IDs, ferr)
		}
	}
}

// Each source of the tags a file gives is read: manifest.json, index.json, and repositories; and a
// metadata file named twice is refused.
func TestCrewCtlReadsEveryTagAFileGives(t *testing.T) {
	c := newCtl(t)
	classic := "../internal/release/testdata/images/classic.tar"
	config := "sha256:3470e023120a4ab9f26de8e0b4cca08435feefc051b605a5eff309d326b3f148"
	other := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte(`crew-idfixture`), []byte(`crew-server`)) }
	for what, p := range map[string]string{
		"repositories":  forged(t, classic, "repositories", other),
		"index.json":    forged(t, classic, "index.json", other),
		"manifest.json": forged(t, classic, "manifest.json", other),
		"twice":         forged(t, classic, "oci-layout", func([]byte) []byte { return tarEntry(t, classic, "manifest.json") }, "manifest.json"),
	} {
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && image_ids "$0" "$1" "$2"`, p, config, "crew-idfixture:1")
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		want := "also names crew-server:1"
		if what == "twice" {
			want = "holds manifest.json twice"
		}
		if err == nil || !strings.Contains(string(out), want) {
			t.Errorf("%s: %v %s", what, err, out)
		}
	}
}

// forged is a copy of the tar src with the entry name changed by change; with rename, that entry gets
// that name too, as a second copy of another file.
func forged(t *testing.T, src, name string, change func([]byte) []byte, rename ...string) string {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tr := tar.NewReader(in)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		if h.Name == name {
			b = change(b)
			if len(rename) > 0 {
				h.Name = rename[0]
			}
		}
		h.Size = int64(len(b))
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(b)
	}
	_ = tw.Close()
	p := filepath.Join(t.TempDir(), "forged.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A kept folder's pulled.json must name release.json's id for each image it says was pulled, and the
// same version.
func TestAPulledFolderIsCheckedByItsRecord(t *testing.T) {
	k := newCardKit(t)
	check := func(body string) (string, error) {
		_ = os.WriteFile(filepath.Join(k.card, "pulled.json"), []byte(body), 0o644)
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && check_release "$0"`, k.card)
		cmd.Env = k.env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	images := func(config string) string {
		var parts []string
		for _, n := range release.Images {
			c := k.ids[n]
			if n == "crew-runtime" {
				c = config
			}
			parts = append(parts, `"`+n+`": {"config": "`+c+`", "manifest": "`+sha(n)+`", "from": "x"}`)
		}
		return strings.Join(parts, ", ")
	}
	if out, err := check(`{"version": "0.3.0", "images": {` + images(k.ids["crew-runtime"]) + `}}`); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := check(`{"version": "0.3.0", "images": {` + images(sha("other")) + `}}`); err == nil || !strings.Contains(out, "crew-runtime: pulled as") {
		t.Fatalf("another config: %v\n%s", err, out)
	}
	if out, err := check(`{"version": "0.9.0", "images": {` + images(k.ids["crew-runtime"]) + `}}`); err == nil || !strings.Contains(out, "names another version") {
		t.Fatalf("another version: %v\n%s", err, out)
	}
}

// A first install from a card that stopped half way, before its deploy files were in place, is run
// again as a repair; only an installed Crew sends the card to release pull (review of M17 phase 7).
func TestAFirstInstallFromACardThatStoppedIsRunAgain(t *testing.T) {
	k := newCardKit(t)
	c := newCtl(t)
	c.inCheckout()
	etc := filepath.Join(c.root, "etc", "crew")
	_ = os.MkdirAll(etc, 0o700)
	// What step 3 of the install wrote before it stopped: the data folder of the answers.
	_ = os.WriteFile(filepath.Join(etc, "install.json"), []byte(`{"data_dir": "/var/lib/crew"}`), 0o600)
	out, _ := c.run("install", "--from", k.card, "--registry", registryName, "--registry-token-file", k.token, "--dry-run", "--env-file", c.answers("a.env", good))
	if strings.Contains(out, "Crew is installed here") || !strings.Contains(out, "+ docker login registry.test:5000 as probe") {
		t.Fatalf("a half-done install is not run again:\n%s", out)
	}
	// One that stopped later, at its proof, has its deploy files and the card's version recorded.
	_ = os.WriteFile(filepath.Join(etc, "install.json"), []byte(`{"data_dir": "/var/lib/crew", "version": "0.3.0"}`), 0o600)
	_ = os.MkdirAll(filepath.Join(c.root, "lib", "crew"), 0o755)
	_ = os.WriteFile(filepath.Join(c.root, "lib", "crew", "compose.yaml"), []byte("name: crew\n"), 0o644)
	out, _ = c.run("install", "--from", k.card, "--registry", registryName, "--registry-token-file", k.token, "--dry-run", "--env-file", c.answers("b.env", good))
	if strings.Contains(out, "Crew is installed here") || !strings.Contains(out, "+ docker login registry.test:5000 as probe") {
		t.Fatalf("an install that stopped at its proof is not run again:\n%s", out)
	}
}

// A folder brought to this machine with a pulled.json is refused on every way in: only a release pull
// writes one, into the copy it keeps (security review of M17 phase 7).
func TestABroughtPulledFileIsRefused(t *testing.T) {
	k := newCardKit(t)
	src := releaseFolder(t, filepath.Join(k.root, "copied"), "0.5.0", true)
	_ = os.WriteFile(filepath.Join(src, "pulled.json"), []byte(`{"version": "0.5.0", "images": {}}`), 0o644)
	// On a running Crew: a build by hand, and an import.
	for _, args := range [][]string{{"install", "--from", src, "--confirm", "install"}, {"release", "import", src}} {
		if out, code := k.run(args...); code == 0 || !strings.Contains(out, "holds a pulled.json") {
			t.Errorf("%v: %d\n%s", args, code, out)
		}
	}
	// On a fresh machine: install --from loads no folder that holds one.
	c := newCtl(t)
	c.inCheckout()
	if out, _ := c.run("install", "--from", src, "--dry-run", "--env-file", c.answers("a.env", good)); !strings.Contains(out, "holds a pulled.json") || strings.Contains(out, "docker load") {
		t.Errorf("a fresh install:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(k.root, "data", "releases", "0.5.0")); err == nil {
		t.Fatal("a refused folder was kept")
	}
}

// A card that names other parts than a release's, or a part under another tag, is refused before
// anything is pulled.
func TestACardOfOtherImagesIsRefusedBeforeAPull(t *testing.T) {
	for what, change := range map[string]func(m map[string]any){
		"an extra part": func(m map[string]any) {
			m["images"].(map[string]any)["evil"] = m["images"].(map[string]any)["crew-caddy"]
		},
		"another tag": func(m map[string]any) {
			m["images"].(map[string]any)["crew-caddy"].(map[string]any)["tag"] = "crew-caddy:0.1.0"
		},
		"a missing part": func(m map[string]any) { delete(m["images"].(map[string]any), "crew-updater") },
		"not postgres's": func(m map[string]any) { m["images"].(map[string]any)["postgres"].(map[string]any)["tag"] = "redis:7" },
	} {
		t.Run(what, func(t *testing.T) {
			k := newCardKit(t)
			p := filepath.Join(k.card, "release.json")
			b, _ := os.ReadFile(p)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			change(m)
			b, _ = json.Marshal(m)
			_ = os.WriteFile(p, b, 0o644)
			out, code := k.pull()
			if code == 0 || !strings.Contains(out, "cannot be pulled") || strings.Contains(k.calls(), " pull ") || strings.Contains(k.calls(), "imagetools") {
				t.Fatalf("exit %d\n%s\n%s", code, out, k.calls())
			}
		})
	}
}

// The login is kept in its own folder in /run and nowhere else: a folder a killed run left is removed
// first, and a docker that keeps the login in a credential store refuses the pull (security review of
// M17 phase 7).
func TestTheLoginStaysInItsFolder(t *testing.T) {
	k := newCardKit(t)
	left := filepath.Join(k.root, "crew-login.abc123")
	_ = os.MkdirAll(left, 0o700)
	_ = os.WriteFile(filepath.Join(left, "config.json"), []byte(`{"auths": {"x": {"auth": "eDp4"}}}`), 0o600)
	if out, code := k.pull(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(left); err == nil {
		t.Fatal("the login a killed run left is still there")
	}
	b, _ := os.ReadFile(filepath.Join(k.root, "config-dirs"))
	if !strings.HasPrefix(strings.TrimSpace(string(b)), filepath.Join(k.root, "crew-login.")) {
		t.Fatalf("the login's folder is %q", b)
	}
	// With a credential helper on the machine, the login still goes into the folder.
	k3 := newCardKit(t)
	k3.state.Helper = true
	k3.save()
	if out, code := k3.pull(); code != 0 {
		t.Fatalf("a machine with a credential helper: %d\n%s", code, out)
	}
	k2 := newCardKit(t)
	k2.state.Store = true
	k2.save()
	if out, code := k2.pull(); code == 0 || !strings.Contains(out, "kept the login of registry.test:5000 somewhere else") {
		t.Fatalf("a credential store: %d\n%s", code, out)
	}
	if !k2.loginsGone() {
		t.Fatal("the refused login's folder is still there")
	}
}

// tarEntry is the body of the entry name of the tar src.
func tarEntry(t *testing.T, src, name string) []byte {
	t.Helper()
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("no %s in %s", name, src)
		}
		if h.Name == name {
			b, _ := io.ReadAll(tr)
			return b
		}
	}
}

// build --out writes a release this machine does not take, as the install test's phase second does for
// another machine: into the folder given, which must be no one else's to change, and nothing of it
// under the data folder's releases, which the updater and Versions read.
func TestABuildOutIsNoReleaseOfThisMachine(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	ctlBody, _ := os.ReadFile("crew-ctl")
	repo := gitRepo(t, map[string]string{"deploy/crew-ctl": string(ctlBody), "deploy/release.sh": "#!/bin/sh\n",
		"deploy/compose.yaml": "services: {}\n", "versions.lock": "MACHINE_SETUP=2\n" + agyPin("1.4.5", agyURL("1.4.5"), strings.Repeat("ab", 64))})
	out := filepath.Join(c.root, "built")
	_ = os.MkdirAll(out, 0o755)
	build := func(dir string) (string, error) {
		cmd := exec.Command("bash", filepath.Join(repo, "deploy", "crew-ctl"), "build", "--version", "0.99.1", "--out", dir, "--dry-run")
		cmd.Env = c.env
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	got, err := build(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if !strings.Contains(got, "release.sh build --tree "+out+"/.0.99.1.tree --version 0.99.1 --out "+out+"/.0.99.1.partial") ||
		!strings.Contains(got, "+ write the release folder "+out+"/0.99.1 ") || strings.Contains(got, filepath.Join(c.root, "data", "releases")) {
		t.Fatalf("not into %s alone:\n%s", out, got)
	}
	open := filepath.Join(c.root, "open")
	_ = os.MkdirAll(open, 0o755)
	_ = os.Chmod(open, 0o777)
	if got, err := build(open); err == nil || !strings.Contains(got, "is not sticky") || strings.Contains(got, "release.sh build") {
		t.Fatalf("a folder others may write: %v\n%s", err, got)
	}
}
