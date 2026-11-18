package release_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// amd64 is the platform of an index's entry that Docker runs here.
const amd64 = `"platform":{"os":"linux","architecture":"amd64"}`

// fixture is one file of testdata/images, which docker save wrote on Docker 29.7.2, with the ids each
// image store gave the image when it was loaded (D48).
type fixture struct {
	Config string            `json:"config"`
	Loaded map[string]string `json:"loaded"`
}

func fixtures(t *testing.T) map[string]fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/images/ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx) != 3 {
		t.Fatalf("%d fixtures", len(fx))
	}
	return fx
}

// A file docker save wrote on either image store gives the config digest, and accepts the id each
// store gives the image it holds: the config digest on the classic store, a manifest's or an index's
// digest on the containerd store, an attestation's index followed one level.
func TestAnImageFileAcceptsTheIDEveryStoreGives(t *testing.T) {
	for name, fx := range fixtures(t) {
		f, err := release.ReadImageFile(filepath.Join("testdata/images", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.Config != fx.Config {
			t.Errorf("%s: config %s, want %s", name, f.Config, fx.Config)
		}
		for store, id := range fx.Loaded {
			if !f.AcceptsID(fx.Config, id) {
				t.Errorf("%s: the id %s of the %s store is refused (%v)", name, id, store, f.IDs)
			}
		}
		// The ids of another image, and another image's config, are refused.
		if f.AcceptsID(fx.Config, "sha256:"+strings.Repeat("e", 64)) || f.AcceptsID("sha256:"+strings.Repeat("e", 64), fx.Loaded["containerd"]) {
			t.Errorf("%s: accepts an id of another image", name)
		}
	}
}

// entries reads a tar into its names and bodies, in order.
func entries(t *testing.T, path string) ([]string, map[string][]byte) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	bodies := map[string][]byte{}
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
		names = append(names, h.Name)
		bodies[h.Name] = b
	}
	return names, bodies
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// rewrite writes a tar of the names and bodies.
func rewrite(t *testing.T, path string, names []string, bodies map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range names {
		typ := byte(tar.TypeReg)
		if strings.HasSuffix(n, "/") {
			typ = tar.TypeDir
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(bodies[n])), Typeflag: typ}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bodies[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A manifest is believed only by its bytes: one whose blob does not have the digest index.json names,
// or that names another config, gives no id; a file of two images, or a config path of no digest, is
// refused; a file that is no tar says nothing, and only the id itself is taken.
func TestAnImageFileIsNotTakenAtItsWord(t *testing.T) {
	fx := fixtures(t)["classic.tar"]
	manifest := "blobs/sha256/" + strings.TrimPrefix(fx.Loaded["containerd"], "sha256:")
	dir := t.TempDir()
	read := func(names []string, bodies map[string][]byte) (release.ImageFile, error) {
		p := filepath.Join(dir, "x.tar")
		rewrite(t, p, names, bodies)
		return release.ReadImageFile(p)
	}

	names, bodies := entries(t, "testdata/images/classic.tar")
	bodies[manifest] = append(bodies[manifest], ' ')
	if f, err := read(names, bodies); err != nil || f.AcceptsID(fx.Config, fx.Loaded["containerd"]) || !f.AcceptsID(fx.Config, fx.Config) {
		t.Fatalf("a manifest of another hash: %+v %v", f, err)
	}

	names, bodies = entries(t, "testdata/images/classic.tar")
	other := strings.Replace(string(bodies[manifest]), strings.TrimPrefix(fx.Config, "sha256:"), strings.Repeat("e", 64), 1)
	bodies["blobs/sha256/"+sha(other)] = []byte(other)
	names = append(names, "blobs/sha256/"+sha(other))
	bodies["index.json"] = []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + sha(other) + `"}]}`)
	if f, err := read(names, bodies); err != nil || f.AcceptsID(fx.Config, "sha256:"+sha(other)) {
		t.Fatalf("a manifest of another config: %+v %v", f, err)
	}

	names, bodies = entries(t, "testdata/images/classic.tar")
	var m []map[string]any
	_ = json.Unmarshal(bodies["manifest.json"], &m)
	two, _ := json.Marshal(append(m, m[0]))
	bodies["manifest.json"] = two
	if _, err := read(names, bodies); err == nil || !strings.Contains(err.Error(), "holds 2 images") {
		t.Fatalf("two images: %v", err)
	}

	m[0]["Config"] = "config.json"
	one, _ := json.Marshal(m)
	bodies["manifest.json"] = one
	if _, err := read(names, bodies); err == nil || !strings.Contains(err.Error(), "names the config") {
		t.Fatalf("a config of no digest: %v", err)
	}

	// Before Docker 25, docker save named the config HEX.json.
	m[0]["Config"] = strings.TrimPrefix(fx.Config, "sha256:") + ".json"
	old, _ := json.Marshal(m)
	bodies["manifest.json"] = old
	if f, err := read(names, bodies); err != nil || f.Config != fx.Config {
		t.Fatalf("the old form: %+v %v", f, err)
	}

	// An index is followed one level, as an image with its attestations has it: one deeper is no file
	// docker save writes.
	names, bodies = entries(t, "testdata/images/classic.tar")
	inner := `{"schemaVersion":2,"manifests":[{"digest":"sha256:` + strings.TrimPrefix(fx.Loaded["containerd"], "sha256:") + `",` + amd64 + `}]}`
	middle := `{"schemaVersion":2,"manifests":[{"digest":"sha256:` + sha(inner) + `",` + amd64 + `}]}`
	for _, b := range []string{inner, middle} {
		bodies["blobs/sha256/"+sha(b)] = []byte(b)
		names = append(names, "blobs/sha256/"+sha(b))
	}
	bodies["index.json"] = []byte(`{"schemaVersion":2,"manifests":[{"digest":"sha256:` + sha(middle) + `"},{"digest":"sha256:` + sha(inner) + `"}]}`)
	if f, err := read(names, bodies); err != nil || f.AcceptsID(fx.Config, "sha256:"+sha(middle)) || !f.AcceptsID(fx.Config, "sha256:"+sha(inner)) {
		t.Fatalf("an index two levels deep: %+v %v", f, err)
	}

	// An index's one manifest for linux/amd64, of no variant, must be the one that names the config:
	// Docker runs that one (security review of M17's Close). Beside it an attestation is no platform's.
	attest := `{"digest":"sha256:` + strings.Repeat("a", 64) + `","platform":{"os":"unknown","architecture":"unknown"}}`
	unchecked := `{"digest":"sha256:` + strings.Repeat("b", 64) + `",` + amd64 + `}`
	named := `{"digest":"sha256:` + strings.TrimPrefix(fx.Loaded["containerd"], "sha256:") + `",`
	for _, c := range []struct {
		why, manifests string
		takes          bool
	}{
		{"one for linux/amd64 and an attestation", named + amd64 + `},` + attest, true},
		{"two for linux/amd64, the unchecked one first", unchecked + `,` + named + amd64 + `}`, false},
		{"one of a variant", named + `"platform":{"os":"linux","architecture":"amd64","variant":"v3"}}`, false},
		{"one of no platform", named[:len(named)-1] + `}`, false},
		{"one for linux/arm64", named + `"platform":{"os":"linux","architecture":"arm64"}}`, false},
		// Docker normalises a platform before it matches it: these it could take for linux/amd64
		// (round 2), while another platform beside, as PostgreSQL's index has, is no choice of its.
		{"one for x86_64 beside", named + amd64 + `},{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"os":"linux","architecture":"x86_64"}}`, false},
		{"one for LINUX/AMD64 beside", named + amd64 + `},{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"os":"Linux","architecture":"AMD64"}}`, false},
		{"one of no os beside", named + amd64 + `},{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"architecture":"amd64"}}`, false},
		{"one for linux/arm64 beside", named + amd64 + `},{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}},` + attest, true},
	} {
		names, bodies = entries(t, "testdata/images/classic.tar")
		ix := `{"schemaVersion":2,"manifests":[` + c.manifests + `]}`
		bodies["blobs/sha256/"+sha(ix)] = []byte(ix)
		names = append(names, "blobs/sha256/"+sha(ix))
		bodies["index.json"] = []byte(`{"schemaVersion":2,"manifests":[{"digest":"sha256:` + sha(ix) + `"}]}`)
		if f, err := read(names, bodies); err != nil || f.AcceptsID(fx.Config, "sha256:"+sha(ix)) != c.takes {
			t.Errorf("an index with %s: %+v %v", c.why, f, err)
		}
	}

	p := filepath.Join(dir, "no.tar")
	_ = os.WriteFile(p, []byte("not a tar"), 0o644)
	if f, err := release.ReadImageFile(p); err != nil || f.Config != "" || !f.AcceptsID(fx.Config, fx.Config) || f.AcceptsID(fx.Config, fx.Loaded["containerd"]) {
		t.Fatalf("no tar: %+v %v", f, err)
	}
}

// A pulled image has no file in its folder: pulled.json names the config digest the pull checked,
// which must be release.json's id, and the manifest it was pulled by (16.12, D48).
func TestAPulledImageIsVerifiedByWhatThePullChecked(t *testing.T) {
	f := folder(t, t.TempDir(), "0.4.0")
	p, _ := f.ImagePath("crew-runtime")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(); err == nil {
		t.Fatal("a folder without an image file passed")
	}
	id := f.Manifest.Images["crew-runtime"].ID
	pulled := func(body string) (*release.Folder, error) {
		if err := os.WriteFile(filepath.Join(f.Dir, release.PulledFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return release.Read(f.Dir)
	}
	good := `{"version": "0.4.0", "images": {"crew-runtime": {"config": "` + id + `", "manifest": "sha256:` + strings.Repeat("d", 64) + `", "from": "r/crew-runtime:0.4.0"}}}`
	g, err := pulled(good)
	if err != nil || g.Pulled["crew-runtime"].Manifest != "sha256:"+strings.Repeat("d", 64) {
		t.Fatalf("%+v %v", g, err)
	}
	if err := g.Verify(); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]string{
		strings.Replace(good, id, "sha256:"+strings.Repeat("e", 64), 1):    "was pulled as",
		strings.Replace(good, `"manifest": "sha256:`, `"manifest": "x`, 1): "was pulled as",
	} {
		g, err := pulled(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Verify(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
	if _, err := pulled(strings.Replace(good, `"0.4.0", "images"`, `"0.5.0", "images"`, 1)); err == nil || !strings.Contains(err.Error(), "pulled.json names 0.5.0") {
		t.Errorf("another version: %v", err)
	}
	if _, err := pulled("{"); err == nil {
		t.Error("a broken pulled.json was read")
	}
	// A folder that holds the file of an image pulled.json names was not made by a pull.
	if err := os.WriteFile(p, []byte("image crew-runtime 0.4.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, _ := pulled(good); g.Verify() == nil || !strings.Contains(g.Verify().Error(), "and the folder holds its file") {
		t.Errorf("a pulled image with its file: %v", g.Verify())
	}
}
