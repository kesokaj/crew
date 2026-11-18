package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// kit is a proxy at a test server, with a TLS server behind every host it may dial, and a record of
// what it dialled and of the requests that server got.
type kit struct {
	srv     *httptest.Server
	mu      sync.Mutex
	dialled []string
	hits    int
	addrs   map[string][]netip.Addr
	fromBox bool
	box     string
	proxy   *Proxy
}

func newKit(t *testing.T) *kit {
	k := &kit{fromBox: true, box: "judge-1", addrs: map[string][]netip.Addr{
		"daily-cloudcode-pa.googleapis.com": {netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("142.250.74.10")},
		"www.googleapis.com":                {netip.MustParseAddr("192.168.1.9")},
		"proxy.golang.org":                  {netip.MustParseAddr("142.250.74.17")},
	}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.hits++
		k.mu.Unlock()
		io.WriteString(w, "pong")
	}))
	t.Cleanup(up.Close)
	k.proxy = &Proxy{
		Hosts: func(context.Context) ([]string, error) {
			return []string{"daily-cloudcode-pa.googleapis.com", "www.googleapis.com"}, nil
		},
		SandboxAt: func(local, remote string) (string, bool) { return k.box, k.fromBox },
		Sources:   func(context.Context) ([]string, error) { return []string{"proxy.golang.org"}, nil },
		IsSources: func(sandbox string) bool { return strings.HasPrefix(sandbox, "build-") },
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			return k.addrs[host], nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			k.mu.Lock()
			k.dialled = append(k.dialled, addr)
			k.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, "tcp", up.Listener.Addr().String())
		},
		Log: slog.New(slog.DiscardHandler),
	}
	k.srv = httptest.NewServer(k.proxy)
	t.Cleanup(k.srv.Close)
	return k
}

// through speaks HTTPS through an open tunnel, naming sni in the handshake, and returns the body.
func through(c net.Conn, br *bufio.Reader, sni string) (string, error) {
	tc := tls.Client(&buffered{Conn: c, r: br}, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		return "", err
	}
	io.WriteString(tc, "GET / HTTP/1.1\r\nHost: "+sni+"\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// buffered reads what the status line's reader holds first.
type buffered struct {
	net.Conn
	r *bufio.Reader
}

func (b *buffered) Read(p []byte) (int, error) { return b.r.Read(p) }

// connect asks the proxy for a tunnel to target and returns the status line and the connection.
func (k *kit) connect(t *testing.T, target string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(k.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line), c, br
}

func TestATunnelGoesOnlyToAPublicAddressOfAListedHost(t *testing.T) {
	k := newKit(t)
	line, c, br := k.connect(t, "daily-cloudcode-pa.googleapis.com:443")
	if line != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status %q", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	body, err := through(c, br, "daily-cloudcode-pa.googleapis.com")
	if err != nil || body != "pong" {
		t.Fatalf("through the tunnel: %q, %v", body, err)
	}
	// The private address the name also has was skipped (13.6).
	if len(k.dialled) != 1 || k.dialled[0] != "142.250.74.10:443" {
		t.Fatalf("dialled %v", k.dialled)
	}
}

// The handshake inside a tunnel must name the host the tunnel was asked for: a tunnel to a listed
// host cannot reach another behind the same front (review of M10).
func TestATunnelWhoseHandshakeNamesAnotherHostIsClosed(t *testing.T) {
	k := newKit(t)
	_, c, br := k.connect(t, "daily-cloudcode-pa.googleapis.com:443")
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	if body, err := through(c, br, "storage.googleapis.com"); err == nil {
		t.Fatalf("a handshake for another host went through: %q", body)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.hits != 0 {
		t.Fatalf("the host behind got %d requests", k.hits)
	}
}

// A sandbox holds at most maxTunnels tunnels at once.
func TestASandboxHasFewTunnels(t *testing.T) {
	maxTunnels = 1
	defer func() { maxTunnels = 64 }()
	k := newKit(t)
	first, _, _ := k.connect(t, "daily-cloudcode-pa.googleapis.com:443")
	if first != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("the first: %q", first)
	}
	if second, _, _ := k.connect(t, "daily-cloudcode-pa.googleapis.com:443"); !strings.Contains(second, " 403 ") {
		t.Fatalf("the second at once: %q", second)
	}
}

func TestTheProxyRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		target  string
		fromBox bool
		want    string
	}{
		"a host not on the list":              {"api.anthropic.com:443", true, "403"},
		"another port of a listed host":       {"daily-cloudcode-pa.googleapis.com:80", true, "403"},
		"an address instead of a name":        {"142.250.74.10:443", true, "403"},
		"a listed host that is only local":    {"www.googleapis.com:443", true, "502"},
		"a request that is from no sandbox":   {"daily-cloudcode-pa.googleapis.com:443", false, "403"},
		"a listed host with a different case": {"DAILY-cloudcode-pa.googleapis.com.:443", true, "200"},
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			k.fromBox = tc.fromBox
			line, _, _ := k.connect(t, tc.target)
			if !strings.Contains(line, " "+tc.want+" ") {
				t.Fatalf("status %q, want %s", line, tc.want)
			}
			if tc.want != "200" && len(k.dialled) != 0 {
				t.Fatalf("dialled %v for a refused tunnel", k.dialled)
			}
		})
	}
	k := newKit(t)
	resp, err := http.Get(k.srv.URL + "/v1/creds")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("a GET got %d", resp.StatusCode)
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"142.250.74.10": true, "2a00:1450:4001:80b::200a": true,
		"127.0.0.1": false, "10.0.0.1": false, "172.17.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "198.18.0.1": false, "0.0.0.0": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false,
	} {
		a := netip.MustParseAddr(addr).Unmap()
		if got := Public(a); got != want {
			t.Errorf("Public(%s) = %v", addr, got)
		}
	}
}

// An address that does not answer leaves time for the next, and IPv4 is tried first.
func TestAnAddressThatHangsLeavesTimeForTheNext(t *testing.T) {
	addrTimeout = 50 * time.Millisecond
	defer func() { addrTimeout = 5 * time.Second }()
	var tried []string
	p := &Proxy{
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("2a00:1450:4001:80b::200a"), netip.MustParseAddr("34.54.84.110"), netip.MustParseAddr("142.250.74.10")}, nil
		},
		Dial: func(ctx context.Context, _, addr string) (net.Conn, error) {
			tried = append(tried, addr)
			if addr == "34.54.84.110:443" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}
	c, err := p.dial(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if strings.Join(tried, " ") != "34.54.84.110:443 142.250.74.10:443" {
		t.Fatalf("tried %v", tried)
	}
}

// A tunnel ends when nothing passed either way for idleTimeout, not when one way is quiet while the
// other carries a long answer (round 2 of M10).
func TestATunnelEndsOnlyWhenBothWaysAreQuiet(t *testing.T) {
	idleTimeout = 200 * time.Millisecond
	defer func() { idleTimeout = 15 * time.Minute }()
	clientSide, proxyDown := net.Pipe()
	proxyUp, serverSide := net.Pipe()
	done := make(chan struct{})
	go func() { splice(proxyDown, strings.NewReader(""), proxyUp); close(done) }()
	// The server streams for three times the idle time; the client sends nothing.
	go func() {
		for i := 0; i < 12; i++ {
			serverSide.Write([]byte("x"))
			time.Sleep(50 * time.Millisecond)
		}
	}()
	got := 0
	buf := make([]byte, 1)
	for got < 12 {
		clientSide.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := clientSide.Read(buf); err != nil {
			t.Fatalf("the tunnel ended after %d of 12 bytes: %v", got, err)
		}
		got++
	}
	// Now both ways are quiet: it ends.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a quiet tunnel did not end")
	}
}

// A Sources sandbox reaches the hosts of sandbox.sources and not the model's; a Model only one the
// model's and not the sources (13.6, since 0.51).
func TestEachModeHasItsOwnList(t *testing.T) {
	k := newKit(t)
	if line, _, _ := k.connect(t, "proxy.golang.org:443"); !strings.Contains(line, " 403 ") {
		t.Fatalf("a Model only sandbox reached a source: %q", line)
	}
	k.box = "build-1"
	if line, _, _ := k.connect(t, "proxy.golang.org:443"); line != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("a Sources sandbox did not reach its source: %q", line)
	}
	if line, _, _ := k.connect(t, "daily-cloudcode-pa.googleapis.com:443"); !strings.Contains(line, " 403 ") {
		t.Fatalf("a Sources sandbox reached the model: %q", line)
	}
}
