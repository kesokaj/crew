package hosting

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The tars in testdata are what `docker save` of Docker 29.7.2 (overlay2) wrote on dave for the two
// images of testdata/Dockerfile.plain and Dockerfile.volume, unchanged.
const (
	plainID  = "sha256:7eb5e1aadd14592c54cbaef225e2c5091871296364b8ac8039b7ab34de7b6dd8"
	volumeID = "sha256:2ac5617ad517d922a7666edcd519426ef4e80f0bc18dd2b04d0a09837953f668"
	// plainManifest is the manifest index.json names: the id containerd's image store gives the image
	// (Docker 29.0.0 in a throwaway dind on dave, .agents/scratch/s34/containerd-load.log).
	plainManifest = "sha256:617c18549d434516cb663b52b169371a78523bd30b96f5784f1b164e24004d77"
)

type entry struct {
	h    tar.Header
	body []byte
}

func readTar(t *testing.T, raw []byte) []entry {
	t.Helper()
	var out []entry
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out = append(out, entry{*h, body})
	}
}

func writeTar(t *testing.T, es []entry) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range es {
		h := e.h
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write(e.body)
	}
	tw.Close()
	return b.Bytes()
}

func saved(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func clean(t *testing.T, raw []byte) ([]byte, Image, error) {
	t.Helper()
	var out bytes.Buffer
	img, err := CleanImage(bytes.NewReader(raw), &out)
	return out.Bytes(), img, err
}

// What Docker saved is cleaned of every name (plan M19 D1): manifest.json has no RepoTags, there is no
// repositories, index.json names no image; every other entry is as Docker wrote it, and the id is the
// config's digest, which is what the overlay2 store loads it as.
func TestTheCleanerTakesEveryNameAndNothingElse(t *testing.T) {
	raw := saved(t, "save-plain.tar")
	out, img, err := clean(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if img.ID != plainID || img.Manifest != plainManifest || len(img.Volumes) != 0 || len(img.Ports) != 0 {
		t.Fatalf("the image: %+v", img)
	}
	before := map[string]entry{}
	for _, e := range readTar(t, raw) {
		before[e.h.Name] = e
	}
	after := readTar(t, out)
	if len(after) != len(before)-1 {
		t.Fatalf("%d entries of %d", len(after), len(before))
	}
	for _, e := range after {
		switch e.h.Name {
		case "repositories":
			t.Fatal("repositories was kept")
		case "manifest.json":
			var m []map[string]any
			json.Unmarshal(e.body, &m)
			if len(m) != 1 || m[0]["RepoTags"] != nil || m[0]["Config"] != "blobs/sha256/"+strings.TrimPrefix(plainID, "sha256:") || m[0]["Layers"] == nil {
				t.Fatalf("manifest.json: %s", e.body)
			}
		case "index.json":
			if bytes.Contains(e.body, []byte("t19-tiny")) || bytes.Contains(e.body, []byte("annotations")) || !bytes.Contains(e.body, []byte("manifests")) {
				t.Fatalf("index.json: %s", e.body)
			}
		default:
			if b := before[e.h.Name]; !bytes.Equal(b.body, e.body) || b.h.Mode != e.h.Mode || b.h.Typeflag != e.h.Typeflag {
				t.Fatalf("%s changed", e.h.Name)
			}
		}
	}
	if bytes.Contains(out, []byte("t19-tiny")) {
		t.Fatal("a name of the image is left in the tar")
	}
}

// An image's VOLUME elsewhere than hosting.data, and ports exposed without hosting.port, are refused
// with the reason (plan M19 D31).
func TestAnImagesConfigIsChecked(t *testing.T) {
	_, img, err := clean(t, saved(t, "save-volume.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if img.ID != volumeID || strings.Join(img.Volumes, ",") != "/cache" || strings.Join(img.Ports, ",") != "9999/tcp" {
		t.Fatalf("the image: %+v", img)
	}
	for _, tc := range []struct {
		port int
		data string
		want string
	}{{8080, "/data", "VOLUME /cache"}, {8080, "/cache/", "exposes 9999/tcp and not hosting.port 8080"}, {9999, "/cache", ""}} {
		if got := ConfigProblem(img, tc.port, tc.data); tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%d %s: %q", tc.port, tc.data, got)
		}
	}
	if got := ConfigProblem(Image{Labels: []string{"crew.system"}}, 8080, "/data"); !strings.Contains(got, "crew.system") {
		t.Fatalf("an image with Crew's labels: %q", got)
	}
	if got := ConfigProblem(Image{}, 8080, "/data"); got != "" {
		t.Fatalf("an image that asks nothing: %q", got)
	}
}

// The config's labels that are Crew's own are read, whatever their case, and no other (security review
// of M19, round 2).
func TestAConfigsCrewLabelsAreRead(t *testing.T) {
	img, err := readConfig([]byte(`{"config":{"Labels":{"Crew.System":"setup","crew.kind":"run","org.example":"x"}}}`))
	if err != nil || strings.Join(img.Labels, ",") != "Crew.System,crew.kind" {
		t.Fatalf("labels %v, %v", img.Labels, err)
	}
}

// What is not one image as Docker saves it is refused, and nothing of it reaches Docker.
func TestTheCleanerRefusesATarThatIsNotOneImage(t *testing.T) {
	base := readTar(t, saved(t, "save-plain.tar"))
	with := func(f func([]entry) []entry) []byte {
		es := append([]entry(nil), base...)
		return writeTar(t, f(es))
	}
	file := func(name string, typ byte, body string) entry {
		return entry{tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Linkname: "/etc/shadow"}, []byte(body)}
	}
	edit := func(name string, f func([]byte) []byte) func([]entry) []entry {
		return func(es []entry) []entry {
			for i := range es {
				if es[i].h.Name == name {
					es[i].body = f(es[i].body)
				}
			}
			return es
		}
	}
	many := func(es []entry) []entry {
		for i := len(es); i <= maxEntries; i++ {
			es = append(es, entry{tar.Header{Name: "blobs/sha256/" + strings.Repeat("0", 58) + strconv.Itoa(100000+i), Typeflag: tar.TypeReg, Mode: 0o644}, nil})
		}
		return es
	}
	for name, raw := range map[string][]byte{
		"a link": with(func(es []entry) []entry {
			return append(es, file("blobs/sha256/"+strings.Repeat("1", 64), tar.TypeSymlink, ""))
		}),
		"a hard link": with(func(es []entry) []entry {
			return append(es, file("blobs/sha256/"+strings.Repeat("1", 64), tar.TypeLink, ""))
		}),
		"a FIFO": with(func(es []entry) []entry {
			return append(es, file("blobs/sha256/"+strings.Repeat("1", 64), tar.TypeFifo, ""))
		}),
		"a device": with(func(es []entry) []entry {
			return append(es, file("blobs/sha256/"+strings.Repeat("1", 64), tar.TypeChar, ""))
		}),
		"a way out":        with(func(es []entry) []entry { return append(es, file("blobs/../../etc/cron.d/x", tar.TypeReg, "x")) }),
		"an absolute path": with(func(es []entry) []entry { return append(es, file("/etc/cron.d/x", tar.TypeReg, "x")) }),
		"a stranger":       with(func(es []entry) []entry { return append(es, file("run.sh", tar.TypeReg, "x")) }),
		"a name twice": with(func(es []entry) []entry {
			return append(es, file("blobs/sha256/e3dabc842c3636e10c57072a199a20c8d0c80635426f455d046d63828f029ad4", tar.TypeReg, "/etc/ another layer"))
		}),
		"too many entries": with(many),
		"two images": with(edit("manifest.json", func(b []byte) []byte {
			return append(append([]byte("["), b[1:len(b)-1]...), append([]byte(","), b[1:]...)...)
		})),
		"two in the index": with(edit("index.json", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"manifests":[`), []byte(`"manifests":[{"digest":"sha256:00"},`), 1)
		})),
		"a changed manifest": with(edit("blobs/sha256/617c18549d434516cb663b52b169371a78523bd30b96f5784f1b164e24004d77", func(b []byte) []byte {
			return bytes.Replace(b, []byte("schemaVersion"), []byte("schemaversion"), 1)
		})),
		"no manifest blob": with(func(es []entry) []entry {
			var out []entry
			for _, e := range es {
				if !strings.HasSuffix(e.h.Name, "617c18549d434516cb663b52b169371a78523bd30b96f5784f1b164e24004d77") {
					out = append(out, e)
				}
			}
			return out
		}),
		"a changed config": with(edit("blobs/sha256/"+strings.TrimPrefix(plainID, "sha256:"), func(b []byte) []byte { return bytes.Replace(b, []byte("amd64"), []byte("arm64"), 1) })),
		"too many layers": with(edit("manifest.json", func(b []byte) []byte {
			l := `"blobs/sha256/` + strings.Repeat("e", 64) + `"`
			return bytes.Replace(b, []byte(`"Layers":[`), []byte(`"Layers":[`+strings.Repeat(l+",", maxLayers)), 1)
		})),
		"no manifest": with(func(es []entry) []entry {
			var out []entry
			for _, e := range es {
				if e.h.Name != "manifest.json" {
					out = append(out, e)
				}
			}
			return out
		}),
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := clean(t, raw)
			if err == nil {
				t.Fatal("cleaned")
			}
			if len(out) > 0 && bytes.Contains(out, []byte("/etc/")) {
				t.Fatal("the bad entry was written")
			}
		})
	}
}
