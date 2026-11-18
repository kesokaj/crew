package egress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// modKit is a proxy at a test server whose door answers as the server's does, with one TLS server
// behind proxy.golang.org and Go's bucket, its certificate for those names, and a record of what it
// dialled and what that server was asked. These tests speak Go's module protocol with net/http, as
// read from Go's documentation: none drives Go's command itself or reaches the real services, which
// make check does not; the probe of a Sources check sandbox does (S15-D4).
type modKit struct {
	srv     *httptest.Server
	proxy   *Proxy
	box     string
	fromBox bool
	sources []string
	mu      sync.Mutex
	dialled []string
	asked   []string // host and path, as the server behind got them
	headers []http.Header
	logs    lockedBuf
}

// lockedBuf is a log the handlers write while a test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const zipBody = "PK the module's zip"

func newModKit(t *testing.T) *modKit {
	k := &modKit{box: "build-1", fromBox: true, sources: []string{"proxy.golang.org", "sum.golang.org"}}
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.asked = append(k.asked, r.Host+r.URL.Path)
		k.headers = append(k.headers, r.Header.Clone())
		k.mu.Unlock()
		bucket := "https://storage.googleapis.com/proxy-golang-org-prod/"
		switch r.Host + r.URL.Path {
		case "proxy.golang.org/example.com/m/@v/list":
			io.WriteString(w, "v1.0.0\n")
		case "proxy.golang.org/example.com/m/@v/v1.0.0.zip":
			http.Redirect(w, r, bucket+"0f1e-example.com-m-v1.0.0.zip?Expires=1&Signature=x", http.StatusFound)
		case "storage.googleapis.com/proxy-golang-org-prod/0f1e-example.com-m-v1.0.0.zip":
			io.WriteString(w, zipBody)
		case "proxy.golang.org/example.com/!upper/@v/v1.0.0.mod":
			io.WriteString(w, "module example.com/Upper\n")
		case "proxy.golang.org/example.com/otherbucket/@v/v1.0.0.zip":
			http.Redirect(w, r, "https://storage.googleapis.com/some-bucket/x.zip", http.StatusFound)
		case "proxy.golang.org/example.com/otherhost/@v/v1.0.0.zip":
			http.Redirect(w, r, "https://evil.example/proxy-golang-org-prod/x.zip", http.StatusFound)
		case "proxy.golang.org/example.com/plain/@v/v1.0.0.zip":
			http.Redirect(w, r, "http://storage.googleapis.com/proxy-golang-org-prod/x.zip", http.StatusFound)
		case "proxy.golang.org/example.com/twice/@v/v1.0.0.zip":
			http.Redirect(w, r, bucket+"twice", http.StatusFound)
		case "storage.googleapis.com/proxy-golang-org-prod/twice":
			http.Redirect(w, r, bucket+"0f1e-example.com-m-v1.0.0.zip", http.StatusFound)
		case "proxy.golang.org/example.com/up/@v/v1.0.0.zip":
			http.Redirect(w, r, bucket+"../some-bucket/x.zip", http.StatusFound)
		case "proxy.golang.org/example.com/gone/@v/list":
			http.Error(w, "gone", http.StatusGone)
		case "proxy.golang.org/example.com/broken/@v/list":
			http.Error(w, "broken", http.StatusInternalServerError)
		case "proxy.golang.org/example.com/huge/@v/v1.0.0.zip":
			w.Header().Set("Content-Length", fmt.Sprint(64<<20))
			chunk := []byte(strings.Repeat("x", 1<<20))
			for i := 0; i < 64; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		case "proxy.golang.org/example.com/big/@v/v1.0.0.zip":
			io.WriteString(w, strings.Repeat("x", 100)) // with its length: more than the test's cap
		case "proxy.golang.org/example.com/stream/@v/v1.0.0.zip":
			for i := 0; i < 100; i++ { // no length: it grows past the cap as it streams
				io.WriteString(w, "x")
				w.(http.Flusher).Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	cert, pool := certFor(t, "proxy.golang.org", "storage.googleapis.com", "evil.example")
	up.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	up.StartTLS()
	t.Cleanup(up.Close)
	addrs := map[string][]netip.Addr{
		"proxy.golang.org":       {netip.MustParseAddr("142.250.74.17")},
		"storage.googleapis.com": {netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("142.250.74.80")},
		"evil.example":           {netip.MustParseAddr("93.184.216.34")},
	}
	k.proxy = &Proxy{
		Sources:   func(context.Context) ([]string, error) { return k.sources, nil },
		IsSources: func(sandbox string) bool { return strings.HasPrefix(sandbox, "build-") },
		SandboxAt: func(local, remote string) (string, bool) { return k.box, k.fromBox },
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			return addrs[host], nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			k.mu.Lock()
			k.dialled = append(k.dialled, addr)
			k.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, "tcp", up.Listener.Addr().String())
		},
		RootCAs: pool,
		Log:     slog.New(slog.NewTextHandler(&k.logs, nil)),
	}
	mux := http.NewServeMux()
	mux.Handle(ModulesPath, http.HandlerFunc(k.proxy.ServeModules))
	k.srv = httptest.NewServer(mux)
	t.Cleanup(k.srv.Close)
	return k
}

// capModules sets the cap for a test, and puts it back after the test's servers closed, which wait
// for their handlers.
func capModules(t *testing.T, n int64) {
	t.Cleanup(func() { maxModuleBytes = 512 << 20 })
	maxModuleBytes = n
}

// certFor is a certificate for the names, and a pool that trusts it.
func certFor(t *testing.T, names ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// get asks the door for a path, as Go's command asks its GOPROXY, and returns the status and body.
func (k *modKit) get(t *testing.T, method, path string, hdr http.Header) (int, string, error) {
	t.Helper()
	req, err := http.NewRequest(method, k.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range hdr {
		req.Header[name] = v
	}
	resp, err := k.srv.Client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// A Sources sandbox gets Go's modules through the server (13.6, since 0.61): the server asks
// proxy.golang.org itself and follows its redirect into Go's bucket, which the sandbox's list does
// not hold.
func TestASourcesSandboxGetsAModuleThroughTheServer(t *testing.T) {
	k := newModKit(t)
	code, body, err := k.get(t, "GET", "/goproxy/example.com/m/@v/v1.0.0.zip", http.Header{"Authorization": {"Bearer x"}, "Cookie": {"a=b"}})
	if err != nil || code != 200 || body != zipBody {
		t.Fatalf("%d %q %v", code, body, err)
	}
	if code, body, _ := k.get(t, "GET", "/goproxy/example.com/m/@v/list", nil); code != 200 || body != "v1.0.0\n" {
		t.Fatalf("the list: %d %q", code, body)
	}
	if code, body, _ := k.get(t, "GET", "/goproxy/example.com/!upper/@v/v1.0.0.mod", nil); code != 200 || !strings.Contains(body, "Upper") {
		t.Fatalf("an escaped path: %d %q", code, body)
	}
	if code, body, _ := k.get(t, "GET", "/goproxy/example.com/%21upper/@v/v1.0.0.mod", nil); code != 200 || !strings.Contains(body, "Upper") {
		t.Fatalf("an escaped path, its ! escaped again: %d %q", code, body)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	want := []string{"proxy.golang.org/example.com/m/@v/v1.0.0.zip", "storage.googleapis.com/proxy-golang-org-prod/0f1e-example.com-m-v1.0.0.zip",
		"proxy.golang.org/example.com/m/@v/list", "proxy.golang.org/example.com/!upper/@v/v1.0.0.mod", "proxy.golang.org/example.com/!upper/@v/v1.0.0.mod"}
	if strings.Join(k.asked, " ") != strings.Join(want, " ") {
		t.Fatalf("asked %v", k.asked)
	}
	// Only the public address of the bucket's name was dialled, and only port 443.
	for _, d := range k.dialled {
		if d != "142.250.74.17:443" && d != "142.250.74.80:443" {
			t.Fatalf("dialled %v", k.dialled)
		}
	}
	// Nothing the sandbox sent goes on.
	for _, h := range k.headers {
		if h.Get("Authorization") != "" || h.Get("Cookie") != "" {
			t.Fatalf("the sandbox's headers went on: %v", h)
		}
	}
}

func TestTheModuleProxyRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path string
		box          string
		fromBox      bool
		sources      []string
		want         int
		asks         int // requests the server behind gets
	}{
		"a Model only sandbox":                {"GET", "/goproxy/example.com/m/@v/list", "judge-1", true, nil, 403, 0},
		"a request from no sandbox":           {"GET", "/goproxy/example.com/m/@v/list", "build-1", false, nil, 403, 0},
		"a method that is not GET":            {"POST", "/goproxy/example.com/m/@v/list", "build-1", true, nil, 405, 0},
		"proxy.golang.org off the list":       {"GET", "/goproxy/example.com/m/@v/list", "build-1", true, []string{"sum.golang.org"}, 403, 0},
		"the checksum database":               {"GET", "/goproxy/sumdb/sum.golang.org/supported", "build-1", true, nil, 404, 0},
		"a path that is not a module's":       {"GET", "/goproxy/example.com/m/@v/v1.0.0.tar", "build-1", true, nil, 404, 0},
		"a path with a capital":               {"GET", "/goproxy/example.com/M/@v/list", "build-1", true, nil, 404, 0},
		"no path":                             {"GET", "/goproxy/", "build-1", true, nil, 404, 0},
		"a query":                             {"GET", "/goproxy/example.com/m/@v/list?go-get=1", "build-1", true, nil, 404, 0},
		"a redirect to another bucket":        {"GET", "/goproxy/example.com/otherbucket/@v/v1.0.0.zip", "build-1", true, nil, 502, 1},
		"a redirect to another host":          {"GET", "/goproxy/example.com/otherhost/@v/v1.0.0.zip", "build-1", true, nil, 502, 1},
		"a redirect over plain HTTP":          {"GET", "/goproxy/example.com/plain/@v/v1.0.0.zip", "build-1", true, nil, 502, 1},
		"a second redirect":                   {"GET", "/goproxy/example.com/twice/@v/v1.0.0.zip", "build-1", true, nil, 502, 2},
		"a redirect out of the bucket's path": {"GET", "/goproxy/example.com/up/@v/v1.0.0.zip", "build-1", true, nil, 502, 1},
		"an answer of another kind":           {"GET", "/goproxy/example.com/broken/@v/list", "build-1", true, nil, 502, 1},
		"a module that is gone":               {"GET", "/goproxy/example.com/gone/@v/list", "build-1", true, nil, 410, 1},
		"a module that is not there":          {"GET", "/goproxy/example.com/none/@v/list", "build-1", true, nil, 404, 1},
		"a module larger than the cap":        {"GET", "/goproxy/example.com/big/@v/v1.0.0.zip", "build-1", true, nil, 502, 1},
	} {
		t.Run(name, func(t *testing.T) {
			capModules(t, 50)
			k := newModKit(t)
			k.box, k.fromBox = tc.box, tc.fromBox
			if tc.sources != nil {
				k.sources = tc.sources
			}
			code, body, _ := k.get(t, tc.method, tc.path, nil)
			if code != tc.want {
				t.Fatalf("status %d %q, want %d", code, body, tc.want)
			}
			k.mu.Lock()
			defer k.mu.Unlock()
			if len(k.asked) != tc.asks {
				t.Fatalf("the server behind was asked %v, want %d", k.asked, tc.asks)
			}
			for _, d := range k.dialled {
				if d == "93.184.216.34:443" {
					t.Fatal("a host off the redirect's rule was dialled")
				}
			}
		})
	}
}

// The paths of Go's module proxy protocol, escaped as Go's command escapes them, and nothing else.
func TestModulePaths(t *testing.T) {
	for p, want := range map[string]bool{
		"example.com/m/@v/list": true, "example.com/m/@v/v1.0.0.info": true, "example.com/m/@v/v1.0.0.mod": true,
		"example.com/m/@v/v1.0.0.zip": true, "example.com/m/@latest": true, "github.com/!burnt!sushi/toml/@v/v1.4.0.zip": true,
		"golang.org/x/net/@v/v0.0.0-20260101000000-0123456789ab.mod": true, "example.com/m/v2/@v/v2.0.0+incompatible.zip": true,
		"": false, "example.com/m": false, "example.com/m/@v/": false, "example.com/m/@v/v1.0.0.tar": false,
		"example.com/m/@v/v1.0.0/x.zip": false, "example.com/M/@v/list": false, "example.com/./m/@v/list": false,
		"example.com/../m/@v/list": false, "example.com//m/@v/list": false, "/example.com/m/@v/list": false,
		"example.com/m%2f/@v/list": false, "example.com/m/@v/list?x=1": false, "example.com/m@v1/@v/list": false,
		"sumdb/sum.golang.org/supported": false, "sumdb/sum.golang.org/lookup/example.com/m@v1.0.0": false,
		"@v/list": false, "example.com/m/@v/.zip": false, "example.com/m/@latest/x": false,
		"example.com/" + strings.Repeat("a", 1100) + "/@v/list": false,
	} {
		if got := modulePath(p); got != want {
			t.Errorf("modulePath(%q) = %v", p, got)
		}
	}
}

// An answer with no length that grows past the cap is cut: Go's command gets no whole file.
func TestAModuleThatStreamsPastTheCapIsCut(t *testing.T) {
	capModules(t, 50)
	k := newModKit(t)
	_, body, err := k.get(t, "GET", "/goproxy/example.com/stream/@v/v1.0.0.zip", nil)
	if err == nil || len(body) > 50 {
		t.Fatalf("got %d bytes, err %v", len(body), err)
	}
}

// A sandbox's requests to the module proxy count among its tunnels.
func TestTheModuleProxyCountsAmongTheTunnels(t *testing.T) {
	t.Cleanup(func() { maxTunnels = 64 })
	maxTunnels = 0
	k := newModKit(t)
	if code, _, _ := k.get(t, "GET", "/goproxy/example.com/m/@v/list", nil); code != 403 {
		t.Fatalf("status %d", code)
	}
}

// The server's client to Go's proxy dials only it and its bucket, on port 443, whatever it is asked.
func TestTheModuleClientDialsOnlyGosProxyAndItsBucket(t *testing.T) {
	k := newModKit(t)
	for _, u := range []string{"https://evil.example/x", "https://proxy.golang.org:8443/x", "https://www.googleapis.com/x"} {
		if resp, err := k.proxy.modulesClient().Get(u); err == nil {
			resp.Body.Close()
			t.Errorf("%s was reached", u)
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.dialled) != 0 {
		t.Fatalf("dialled %v", k.dialled)
	}
}

// A redirect goes only into Go's bucket: https, its host alone, and a clean path under its prefix.
func TestARedirectGoesOnlyIntoGosBucket(t *testing.T) {
	bucket, _ := url.Parse(modulesBucket)
	for raw, want := range map[string]bool{
		"https://storage.googleapis.com/proxy-golang-org-prod/abc-m.zip?Expires=1&Signature=x": true,
		"http://storage.googleapis.com/proxy-golang-org-prod/abc":                              false,
		"https://storage.googleapis.com:443/proxy-golang-org-prod/abc":                         false,
		"https://u@storage.googleapis.com/proxy-golang-org-prod/abc":                           false,
		"https://storage.googleapis.com/proxy-golang-org-prod":                                 false,
		"https://storage.googleapis.com/proxy-golang-org-prod-2/abc":                           false,
		"https://storage.googleapis.com/proxy-golang-org-prod/../other/abc":                    false,
		"https://storage.googleapis.com/proxy-golang-org-prod/a//b":                            false,
		"https://storage.googleapis.com/proxy-golang-org-prod/a%2F..%2Fb":                      false,
		"https://evil.example/proxy-golang-org-prod/abc":                                       false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := inBucket(u, bucket); got != want {
			t.Errorf("inBucket(%s) = %v", raw, got)
		}
	}
}

// A sandbox that stops reading a module ends its request within moduleTimeout, and gives back its
// tunnel: the bound holds for what is written to the sandbox, not only for what is read from Go's
// proxy (the security review of 1416306..4b04b02).
func TestASandboxThatStopsReadingGivesBackItsTunnel(t *testing.T) {
	t.Cleanup(func() { moduleTimeout = 10 * time.Minute })
	moduleTimeout = 300 * time.Millisecond
	k := newModKit(t)
	c, err := net.Dial("tcp", strings.TrimPrefix(k.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET /goproxy/example.com/huge/@v/v1.0.0.zip HTTP/1.1\r\nHost: door\r\n\r\n")
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.Contains(line, " 200 ") {
		t.Fatalf("%q %v", line, err)
	}
	// It reads no more, and keeps its connection open.
	open := func() int {
		k.proxy.mu.Lock()
		defer k.proxy.mu.Unlock()
		return k.proxy.open["build-1"]
	}
	deadline := time.Now().Add(5 * time.Second)
	for open() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the request still holds its tunnel after %s", 5*time.Second)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A sandbox that is not in Sources is refused as a sandbox, not as a host: the hosts the proxy
// refuses are how network.model_only_hosts is measured (13.6), and proxy.golang.org is no host a
// Model only CLI asked for.
func TestARefusedSandboxIsNoRefusedHost(t *testing.T) {
	k := newModKit(t)
	k.box = "judge-1"
	if code, _, _ := k.get(t, "GET", "/goproxy/example.com/m/@v/list", nil); code != 403 {
		t.Fatalf("status %d", code)
	}
	if l := k.logs.String(); strings.Contains(l, "the proxy refused a host") || !strings.Contains(l, "the module proxy refused a sandbox") {
		t.Fatalf("the log: %s", l)
	}
}

// proxy.golang.org fetches a module it does not hold from wherever its path names, so a module path
// can carry what a Sources sandbox holds to any host, as the tunnel to proxy.golang.org could. The
// answers of no such module are where such paths end, and each is logged with its sandbox and path
// (13.6; security review of M16's close).
func TestANoSuchModuleAnswerIsLogged(t *testing.T) {
	k := newModKit(t)
	for path, want := range map[string]int{"example.com/gone/@v/list": 410, "example.com/nope/@v/list": 404} {
		if code, _, _ := k.get(t, "GET", "/goproxy/"+path, nil); code != want {
			t.Fatalf("%s: status %d", path, code)
		}
		if l := k.logs.String(); !strings.Contains(l, `msg="the module proxy found no such module" sandbox=build-1 path=`+path+` status=`+fmt.Sprint(want)) {
			t.Fatalf("%s: the log: %s", path, l)
		}
	}
}

// Go's proxy that cannot be reached is a 502 with its line, as Go's command reads any error, and no
// fallback (21, since 0.61).
func TestGosProxyThatCannotBeReachedIsA502(t *testing.T) {
	k := newModKit(t)
	k.proxy.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("no route to host") }
	if code, _, _ := k.get(t, "GET", "/goproxy/example.com/m/@v/list", nil); code != 502 {
		t.Fatalf("status %d", code)
	}
	if l := k.logs.String(); !strings.Contains(l, `msg="the module proxy could not get a module" sandbox=build-1 path=example.com/m/@v/list`) || !strings.Contains(l, "no route to host") {
		t.Fatalf("the log: %s", l)
	}
}
