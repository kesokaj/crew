package pins_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/pins"
)

// The publishers' answers as they were on 2026-10-22 (testdata/pins), served by one server, which a
// test changes where it needs another answer. The registries answer a HEAD with a digest by tag.
type publishers struct {
	t       *testing.T
	answers map[string]string
	digests map[string]string
	// hits are the requests the server answered; two looks at once ask it at once.
	mu   sync.Mutex
	hits []string
}

// asked is what the server was asked, in order.
func (p *publishers) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hits...)
}

func recorded(t *testing.T) *publishers {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pins", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return &publishers{t: t, answers: map[string]string{
		"/dl/":                          read("go-dl.json"),
		"/dist/index.json":              read("node-index.json"),
		"/dist/v22.23.4/SHASUMS256.txt": read("node-v22.23.4-SHASUMS256.txt"),
		"/chrome-devtools-mcp/latest":   read("npm-chrome-devtools-mcp.json"),
		"/manifests/linux_amd64.json":   read("agy-manifest.json"),
		"/packages/dists/stable/main/binary-amd64/Packages": read("gh-Packages"),
		"/token": `{"token":"a.b-c_d"}`,
	}, digests: map[string]string{}}
}

func (p *publishers) serve() *pins.Sources {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.hits = append(p.hits, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v2/") {
			if r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v2/library/") && r.Header.Get("Authorization") != "Bearer a.b-c_d" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			d, ok := p.digests[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", d)
			return
		}
		a, ok := p.answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(a))
	}))
	p.t.Cleanup(srv.Close)
	return &pins.Sources{GoDL: srv.URL, Node: srv.URL, NPM: srv.URL, DockerAuth: srv.URL, DockerHub: srv.URL, MCR: srv.URL,
		GH: srv.URL, Agy: srv.URL + "/manifests/linux_amd64.json", AgyTarball: cli.AgyTarball}
}

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

// The pins of versions.lock on 2026-10-22, with agy at 1.4.5 and gh at 2.103.0.
func lock() map[string]string {
	return map[string]string{
		"GO_VERSION": "1.27.3", "RUNTIME_GO_VERSION": "1.27.3", "RUNTIME_GO_SHA256": strings.Repeat("6", 64),
		"GO_IMAGE":             "golang:1.27.3-trixie@" + digest('1'),
		"RUNTIME_NODE_VERSION": "22.23.3", "RUNTIME_NODE_SHA256": strings.Repeat("d", 64), "WEB_NODE_VERSION": "22.23.3",
		"WEB_NODE_IMAGE":              "node:22.23.3-trixie-slim@" + digest('2'),
		"SERVER_BASE_IMAGE":           "debian:trixie-slim@" + digest('3'),
		"PLAYWRIGHT_IMAGE":            "mcr.microsoft.com/playwright:v1.64.0-noble@" + digest('4'),
		"CHROME_DEVTOOLS_MCP_VERSION": "1.10.1", "CHROME_DEVTOOLS_MCP_INTEGRITY": "sha512-" + strings.Repeat("K", 86) + "==",
		"AGY_VERSION": "1.4.5", "AGY_URL": "https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.5-1/linux-x64/cli_linux_x64.tar.gz",
		"AGY_SHA512": strings.Repeat("f", 128), "RUNTIME_GH_VERSION": "2.103.0",
	}
}

// Every image as pinned: nothing moved.
func (p *publishers) asPinned() {
	p.digests["/v2/library/golang/manifests/1.27.3-trixie"] = digest('1')
	p.digests["/v2/library/node/manifests/22.23.3-trixie-slim"] = digest('2')
	p.digests["/v2/library/debian/manifests/trixie-slim"] = digest('3')
	p.digests["/v2/playwright/manifests/v1.64.0-noble"] = digest('4')
}

func keys(found []pins.Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.Keys...)
	}
	return out
}

func value(f pins.Finding, key string) string {
	for _, kv := range f.Set {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

// The recorded day: Node 22.23.4 with its tag, chrome-devtools-mcp 1.11.0; Go, agy and gh as pinned;
// no image moved. Node's ticket waits while node:22.23.4-trixie-slim is not there.
func TestTheRecordedDay(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	s := p.serve()
	found, lines := s.Compare(context.Background(), lock())
	if got := keys(found); !slices.Equal(got, []string{"chrome-devtools-mcp@1.11.0"}) {
		t.Fatalf("without Node's image: %v\n%v", got, lines)
	}
	if !slices.ContainsFunc(lines, func(l pins.Line) bool {
		return l.Source == "nodejs.org" && strings.Contains(l.Text, "node:22.23.4-trixie-slim is not to be had yet")
	}) {
		t.Fatalf("lines %v", lines)
	}
	p.digests["/v2/library/node/manifests/22.23.4-trixie-slim"] = digest('5')
	found, lines = s.Compare(context.Background(), lock())
	if got := keys(found); !slices.Equal(got, []string{"node@22.23.4", "chrome-devtools-mcp@1.11.0"}) {
		t.Fatalf("keys %v\n%v", got, lines)
	}
	node := found[0]
	want := map[string]string{"RUNTIME_NODE_VERSION": "22.23.4", "WEB_NODE_VERSION": "22.23.4", "WEB_NODE_IMAGE": "node:22.23.4-trixie-slim@" + digest('5')}
	for k, v := range want {
		if value(node, k) != v {
			t.Fatalf("%s=%q, want %q", k, value(node, k), v)
		}
	}
	// The sum of the recorded SHASUMS256.txt's linux-x64 tarball.
	if sum := value(node, "RUNTIME_NODE_SHA256"); !regexpSHA256(sum) || !strings.Contains(recordedFile(t, "node-v22.23.4-SHASUMS256.txt"), sum+"  node-v22.23.4-linux-x64.tar.xz") {
		t.Fatalf("sum %q", sum)
	}
	if value(found[1], "CHROME_DEVTOOLS_MCP_INTEGRITY") == "" || !slices.Equal(found[1].Places, []string{"image/Dockerfile"}) {
		t.Fatalf("npm %+v", found[1])
	}
	for _, l := range lines {
		if !l.OK {
			t.Fatalf("a source failed: %v", l)
		}
	}
}

func recordedFile(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pins", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func regexpSHA256(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// A newer Go is a ticket only with its image's tag there; Go's version comes with its sum, and the
// image keeps its tag's tail.
func TestANewerGoWithItsImage(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	p.answers["/dl/"] = strings.ReplaceAll(p.answers["/dl/"], "1.27.3", "1.27.4")
	s := p.serve()
	found, lines := s.Compare(context.Background(), lock())
	if slices.Contains(keys(found), "go@1.27.4") {
		t.Fatalf("Go without golang:1.27.4-trixie: %v", lines)
	}
	p.digests["/v2/library/golang/manifests/1.27.4-trixie"] = digest('9')
	// The old tag moved too: Go's ticket moves the image, and the images' ticket leaves it.
	p.digests["/v2/library/golang/manifests/1.27.3-trixie"] = digest('8')
	found, _ = s.Compare(context.Background(), lock())
	if got := keys(found); !slices.Equal(got, []string{"go@1.27.4", "chrome-devtools-mcp@1.11.0"}) {
		t.Fatalf("keys %v", got)
	}
	if value(found[0], "GO_IMAGE") != "golang:1.27.4-trixie@"+digest('9') || value(found[0], "RUNTIME_GO_VERSION") != "1.27.4" ||
		!regexpSHA256(value(found[0], "RUNTIME_GO_SHA256")) {
		t.Fatalf("Go %+v", found[0])
	}
	if !slices.Equal(found[0].Places, []string{"go.mod", "ingress/go.mod", "deploy/Dockerfile.server", "ingress/Dockerfile", "image/Dockerfile"}) {
		t.Fatalf("places %v", found[0].Places)
	}
}

// The images whose tag's digest moved are one finding, a key each; one not read gives its line.
func TestTheImagesThatMoved(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	p.digests["/v2/library/debian/manifests/trixie-slim"] = digest('a')
	p.digests["/v2/playwright/manifests/v1.64.0-noble"] = digest('b')
	delete(p.digests, "/v2/library/golang/manifests/1.27.3-trixie")
	s := p.serve()
	found, lines := s.Compare(context.Background(), lock())
	var img *pins.Finding
	for i := range found {
		if found[i].Tool == "the images" {
			img = &found[i]
		}
	}
	if img == nil || !slices.Equal(img.Keys, []string{"image:PLAYWRIGHT_IMAGE@" + digest('b'), "image:SERVER_BASE_IMAGE@" + digest('a')}) {
		t.Fatalf("images %+v", img)
	}
	if value(*img, "SERVER_BASE_IMAGE") != "debian:trixie-slim@"+digest('a') || value(*img, "PLAYWRIGHT_IMAGE") != "mcr.microsoft.com/playwright:v1.64.0-noble@"+digest('b') {
		t.Fatalf("set %v", img.Set)
	}
	if !slices.ContainsFunc(lines, func(l pins.Line) bool {
		return l.Source == "GO_IMAGE" && !l.OK && l.Text == "not read: it answered 404"
	}) {
		t.Fatalf("lines %v", lines)
	}
}

// A newer agy, gh, or package is one finding each, with every value of its pin.
func TestNewerAgyAndGh(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	p.answers["/manifests/linux_amd64.json"] = strings.ReplaceAll(p.answers["/manifests/linux_amd64.json"], "1.4.5", "1.4.6")
	p.answers["/packages/dists/stable/main/binary-amd64/Packages"] += "\nPackage: gh\nVersion: 2.103.1\nArchitecture: amd64\n\nPackage: gh\nVersion: 2.104.0\nArchitecture: arm64\n"
	found, _ := p.serve().Compare(context.Background(), lock())
	got := keys(found)
	if !slices.Contains(got, "agy@1.4.6") || !slices.Contains(got, "gh@2.103.1") {
		t.Fatalf("keys %v", got)
	}
	for _, f := range found {
		if f.Tool == "agy" && (!strings.Contains(value(f, "AGY_URL"), "/1.4.6-") || len(value(f, "AGY_SHA512")) != 128) {
			t.Fatalf("agy %+v", f)
		}
	}
}

// An answer out of shape gives nothing of its source, whatever it holds, and the others go on.
func TestHostileAnswersGiveNothing(t *testing.T) {
	cases := []struct {
		name, path, old, new, source string
		// skipped: the source lists many, and one out of shape is passed over, not taken.
		skipped bool
	}{
		{"a Go version with a quote", "/dl/", `go1.27.3`, `go1.27.9'`, "go.dev", false},
		{"a Go version with a command", "/dl/", `go1.27.3`, `go1.27.9$(id)`, "go.dev", false},
		{"a Go sum with a space", "/dl/", `"65efbc80`, `"65efbc8 `, "go.dev", false},
		{"a Node version with a line break", "/dist/index.json", `"v22.23.4"`, `"v22.23.9\nRUN x"`, "nodejs.org", true},
		{"an integrity too long", "/chrome-devtools-mcp/latest", `"sha512-`, `"sha512-AAAA`, "npm", false},
		{"an npm version with a semicolon", "/chrome-devtools-mcp/latest", `"1.11.0"`, `"1.11.0;x"`, "npm", false},
		{"agy's tarball of another host", "/manifests/linux_amd64.json", "storage.googleapis.com", "example.com", "agy's manifest", false},
		{"agy's tarball of another bucket", "/manifests/linux_amd64.json", "antigravity-public", "someone-else", "agy's manifest", false},
		{"agy's tarball of another version", "/manifests/linux_amd64.json", `"1.4.5"`, `"1.4.6"`, "agy's manifest", false},
		{"agy's sha512 with a quote", "/manifests/linux_amd64.json", `"fd30`, `"'d30`, "agy's manifest", false},
		{"gh's version with a dollar", "/packages/dists/stable/main/binary-amd64/Packages", "Version: 2.103.0", "Version: 2.104.0$", "gh's apt index", false},
		{"no JSON at all", "/dl/", "[", "<html>", "go.dev", false},
		{"gh's version of a part too long", "/packages/dists/stable/main/binary-amd64/Packages", "Version: 2.103.0", "Version: 2.1030000000.0", "gh's apt index", false},
		{"an npm version with a leading zero", "/chrome-devtools-mcp/latest", `"1.11.0"`, `"1.011.0"`, "npm", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := recorded(t)
			p.asPinned()
			if !strings.Contains(p.answers[c.path], c.old) {
				t.Fatalf("the recording has no %q", c.old)
			}
			// Everywhere: a Go version is in its files' names too, which would refuse it otherwise.
			p.answers[c.path] = strings.ReplaceAll(p.answers[c.path], c.old, c.new)
			found, lines := p.serve().Compare(context.Background(), lock())
			for _, f := range found {
				for _, kv := range f.Set {
					if strings.ContainsAny(kv.Value, "'\"$ ;\n") || strings.Contains(kv.Value, "example.com") || strings.Contains(kv.Value, "someone-else") {
						t.Fatalf("a hostile value was taken: %v", kv)
					}
				}
			}
			i := slices.IndexFunc(lines, func(l pins.Line) bool { return l.Source == c.source })
			if i < 0 || (!c.skipped && (lines[i].OK || !strings.HasPrefix(lines[i].Text, "not read:"))) {
				t.Fatalf("lines %v", lines)
			}
			// The line is Crew's words: none of the answer is in it.
			if strings.Contains(lines[i].Text, strings.Trim(c.new, `"`)) {
				t.Fatalf("the answer is in the line: %q", lines[i].Text)
			}
			if !slices.Contains(keys(found), "chrome-devtools-mcp@1.11.0") && c.source != "npm" {
				t.Fatalf("another source stopped too: %v", keys(found))
			}
		})
	}
}

// A version comes with its checksum, or not at all.
func TestAVersionWithoutItsChecksumGivesNothing(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	p.digests["/v2/library/node/manifests/22.23.4-trixie-slim"] = digest('5')
	delete(p.answers, "/dist/v22.23.4/SHASUMS256.txt")
	p.answers["/dl/"] = strings.ReplaceAll(strings.ReplaceAll(p.answers["/dl/"], "1.27.3", "1.27.4"), `"linux"`, `"plan9"`)
	p.digests["/v2/library/golang/manifests/1.27.4-trixie"] = digest('9')
	found, lines := p.serve().Compare(context.Background(), lock())
	if got := keys(found); slices.Contains(got, "node@22.23.4") || slices.Contains(got, "go@1.27.4") {
		t.Fatalf("keys %v\n%v", got, lines)
	}
}

// An older version than the pin, or the same, is no finding.
func TestAnOlderVersionIsNone(t *testing.T) {
	p := recorded(t)
	p.asPinned()
	l := lock()
	l["CHROME_DEVTOOLS_MCP_VERSION"] = "1.12.0"
	l["RUNTIME_GH_VERSION"] = "2.110.0"
	found, _ := p.serve().Compare(context.Background(), l)
	if len(found) != 0 {
		t.Fatalf("found %v", keys(found))
	}
	for _, c := range [][2]string{{"1.27.10", "1.27.9"}, {"2.0", "1.99.99"}, {"1.4.5.1", "1.4.5"}} {
		if !pins.Newer(c[0], c[1]) || pins.Newer(c[1], c[0]) || pins.Newer(c[0], c[0]) {
			t.Fatalf("Newer %v", c)
		}
	}
	if pins.Newer("1.2.3'", "1.0") || pins.Newer("1.0", "x") {
		t.Fatal("a version out of shape is newer")
	}
}

// A redirect is not followed, to an address of the machine's network too; an answer over the cap,
// and an answer that is not 200, give nothing.
func TestRedirectsAndLargeAnswers(t *testing.T) {
	inner := 0
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inner++ }))
	t.Cleanup(private.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl/":
			http.Redirect(w, r, private.URL+"/latest", http.StatusFound)
		case "/dist/index.json":
			_, _ = w.Write([]byte("[" + strings.Repeat(" ", 5<<20) + "]"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	s := &pins.Sources{GoDL: srv.URL, Node: srv.URL, NPM: srv.URL, GH: srv.URL, Agy: srv.URL + "/m", AgyTarball: cli.AgyTarball}
	if _, err := s.GoRelease(context.Background()); err == nil || err.Error() != "answered with a redirect, which the look does not follow" || inner != 0 {
		t.Fatalf("redirect: %v, %d reached", err, inner)
	}
	if _, err := s.NodeRelease(context.Background(), "22"); err == nil || err.Error() != "answered more than 4 MiB" {
		t.Fatalf("large: %v", err)
	}
	if _, err := s.GHVersion(context.Background()); err == nil || err.Error() != "answered 500" {
		t.Fatalf("500: %v", err)
	}
	// A registry the look does not read is not asked.
	if _, err := s.Digest(context.Background(), "ghcr.io/x/y", "1"); err == nil || err.Error() != "is a registry the look does not read" {
		t.Fatalf("ghcr: %v", err)
	}
}
