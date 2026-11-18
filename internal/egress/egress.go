// Package egress is the proxy a Model only sandbox reaches its model through (13.6), and a Sources
// sandbox, the build of a release, its package sources. The sandbox's
// firewall lets it reach the server on its own bridge and nothing else, and its CLI is given the
// server's door as its HTTPS proxy. The proxy opens a tunnel to port 443 of a host on the short
// list, and to nothing else. It resolves the name itself, since the sandbox resolves none, and it
// never connects to an address that is not on the internet, whatever a name resolves to. For a
// Sources sandbox it is also Go's module proxy (modules.go, since 0.61), which the server asks
// itself.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy answers CONNECT at the sandboxes' door, and for a Sources sandbox Go's module proxy
// (ServeModules).
type Proxy struct {
	// Hosts is the list a tunnel may go to: network.model_only_hosts. Sources is the list of a
	// sandbox IsSources says is in Sources, the build of a release: sandbox.sources (13.6).
	Hosts     func(ctx context.Context) ([]string, error)
	Sources   func(ctx context.Context) ([]string, error)
	IsSources func(sandbox string) bool
	// SandboxAt says which sandbox a request came from: the server's address it reached and the
	// address it came from. A request from no sandbox is refused.
	SandboxAt func(local, remote string) (string, bool)
	// Resolve and Dial are the network; nil is the machine's own.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	// RootCAs are what the module proxy trusts for Go's proxy and its bucket; nil is the machine's.
	RootCAs *x509.CertPool
	Log     *slog.Logger

	modules modulesClient

	mu     sync.Mutex
	open   map[string]int // tunnels open, by sandbox
	listMu sync.Mutex     // apart from mu: a slow read of the list holds up no tunnel's end
	lists  map[string]cached
}

// cached is a list of hosts and when it was read.
type cached struct {
	hosts []string
	at    time.Time
}

// The limits of the tunnels (review of M10): a sandbox holds at most maxTunnels at once, a tunnel that
// carries nothing for idleTimeout ends, and the client's TLS handshake must come within helloTimeout.
var (
	maxTunnels   = 64
	idleTimeout  = 15 * time.Minute
	helloTimeout = 15 * time.Second
	listTTL      = 15 * time.Second
)

// hosts is the sandbox's list and the setting it comes from, read again when it is older than
// listTTL. A Sources sandbox gets the sources, and no proxy that knows none gives it the model's.
func (p *Proxy) hosts(ctx context.Context, sandbox string) ([]string, string, error) {
	name, read := "network.model_only_hosts", p.Hosts
	if p.IsSources != nil && p.IsSources(sandbox) {
		name, read = "sandbox.sources", p.Sources
		if read == nil {
			return nil, name, errors.New("the proxy has no list of sources")
		}
	}
	p.listMu.Lock()
	defer p.listMu.Unlock()
	if c, ok := p.lists[name]; ok && time.Since(c.at) < listTTL {
		return c.hosts, name, nil
	}
	list, err := read(ctx)
	if err != nil {
		return nil, name, err
	}
	c := cached{at: time.Now()}
	for _, h := range list {
		c.hosts = append(c.hosts, strings.TrimSuffix(strings.ToLower(h), "."))
	}
	if p.lists == nil {
		p.lists = map[string]cached{}
	}
	p.lists[name] = c
	return c.hosts, name, nil
}

// take counts a tunnel of the sandbox, and says whether it may have one more.
func (p *Proxy) take(sandbox string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open == nil {
		p.open = map[string]int{}
	}
	if p.open[sandbox] >= maxTunnels {
		return false
	}
	p.open[sandbox]++
	return true
}

func (p *Proxy) give(sandbox string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open[sandbox]--; p.open[sandbox] <= 0 {
		delete(p.open, sandbox)
	}
}

// dialTimeout bounds the name's lookup and the connections to the host, and addrTimeout one of the
// connections: an address that does not answer leaves time for the next.
const dialTimeout = 15 * time.Second

var addrTimeout = 5 * time.Second // a var only so a test can shorten it

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
		return
	}
	local := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		local = a.String()
	}
	sandbox, ok := p.SandboxAt(local, r.RemoteAddr)
	if !ok {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if err != nil || port != "443" || host == "" {
		p.refuse(w, sandbox, r.Host, "only port 443 of a host by its name")
		return
	}
	list, setting, err := p.hosts(r.Context(), sandbox)
	if err != nil {
		p.Log.Error("the proxy cannot read its list of hosts", "list", setting, "error", err)
		http.Error(w, "the list of hosts cannot be read", http.StatusBadGateway)
		return
	}
	if !slices.Contains(list, host) {
		p.refuse(w, sandbox, host, "not on "+setting)
		return
	}
	if !p.take(sandbox) {
		p.refuse(w, sandbox, host, fmt.Sprintf("more than %d tunnels at once", maxTunnels))
		return
	}
	defer p.give(sandbox)
	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	defer cancel()
	up, err := p.dial(ctx, host)
	if err != nil {
		p.Log.Warn("the proxy could not reach a host on its list", "sandbox", sandbox, "host", host, "error", err)
		http.Error(w, "the host cannot be reached", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "no tunnel", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		up.Close()
		down.Close()
		return
	}
	// The TLS handshake inside the tunnel names the host it is for: one that names another, or a
	// tunnel that is no TLS, is refused (review of M10). What travels inside TLS is not seen: a Host
	// header for another name behind the same front still passes, and the design says so (13.8).
	hello, sni, err := peekHello(down, buf.Reader)
	if err != nil || strings.TrimSuffix(strings.ToLower(sni), ".") != host {
		p.Log.Info("the proxy refused a host", "sandbox", sandbox, "host", host, "why", "the TLS handshake names "+strconv.Quote(sni))
		up.Close()
		down.Close()
		return
	}
	p.Log.Debug("the proxy opened a tunnel", "sandbox", sandbox, "host", host)
	splice(down, io.MultiReader(bytes.NewReader(hello), buf.Reader), up)
}

// errPeeked ends a handshake once the client's hello is read: the proxy answers nothing of it.
var errPeeked = errors.New("peeked")

// peekHello reads the client's TLS ClientHello from the tunnel without answering it, and returns the
// bytes it read, to be sent on, and the server name it asks for.
func peekHello(down net.Conn, buffered *bufio.Reader) ([]byte, string, error) {
	down.SetReadDeadline(time.Now().Add(helloTimeout))
	defer down.SetReadDeadline(time.Time{})
	rec := &recorder{conn: down, r: io.MultiReader(io.LimitReader(buffered, int64(buffered.Buffered())), down)}
	sni := ""
	err := tls.Server(rec, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni = h.ServerName
		return nil, errPeeked
	}}).Handshake()
	if sni == "" {
		if err == nil || errors.Is(err, errPeeked) {
			err = errors.New("the handshake names no server")
		}
		return nil, "", err
	}
	return rec.read.Bytes(), sni, nil
}

// recorder is the client's side of a tunnel for peekHello: it keeps what is read, and writes nothing.
type recorder struct {
	conn net.Conn
	r    io.Reader
	read bytes.Buffer
}

func (c *recorder) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.read.Write(b[:n])
	return n, err
}
func (c *recorder) Write(b []byte) (int, error)        { return len(b), nil }
func (c *recorder) Close() error                       { return nil }
func (c *recorder) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *recorder) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *recorder) SetDeadline(t time.Time) error      { return nil }
func (c *recorder) SetReadDeadline(t time.Time) error  { return nil }
func (c *recorder) SetWriteDeadline(t time.Time) error { return nil }

// refuse answers 403 and logs what was asked for: the hosts a CLI asks for and does not get are
// what a shorter list is measured by (spike item 4).
func (p *Proxy) refuse(w http.ResponseWriter, sandbox, host, why string) {
	p.Log.Info("the proxy refused a host", "sandbox", sandbox, "host", host, "why", why)
	http.Error(w, "refused: "+why, http.StatusForbidden)
}

// dial resolves the host and connects to the first of its addresses that is on the internet.
func (p *Proxy) dial(ctx context.Context, host string) (net.Conn, error) {
	resolve := p.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	dial := p.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	addrs, err := resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	// IPv4 first: the server's networks have no IPv6, and an IPv6 address would only time out.
	slices.SortStableFunc(addrs, func(a, b netip.Addr) int {
		switch {
		case a.Unmap().Is4() && !b.Unmap().Is4():
			return -1
		case !a.Unmap().Is4() && b.Unmap().Is4():
			return 1
		}
		return 0
	})
	var last error = errors.New("the name has no address on the internet")
	for _, a := range addrs {
		a = a.Unmap()
		// The server's networks have no IPv6: an IPv6 address is never dialled (review of M10).
		if !a.Is4() || !Public(a) {
			continue
		}
		actx, cancel := context.WithTimeout(ctx, addrTimeout)
		c, err := dial(actx, "tcp", netip.AddrPortFrom(a, 443).String())
		cancel()
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// notPublic are the ranges a tunnel never goes to, beside what netip names itself: the carrier
// range, the benchmark range, and the ranges for documentation and other special uses.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Public says whether an address is on the internet: not the machine, not a local network, not a
// cloud's metadata address, and not a range set aside.
func Public(a netip.Addr) bool {
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// splice copies both ways until either side ends, or nothing passes either way for idleTimeout, then
// closes both. first is what the client sent that was read already.
func splice(down net.Conn, first io.Reader, up net.Conn) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			down.Close()
			up.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		io.Copy(up, idle{r: io.MultiReader(first, down), c: down, last: &last})
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		io.Copy(down, idle{r: up, c: up, last: &last})
	}()
	wg.Wait()
}

// idle reads r, the reading side of c, with a deadline: the tunnel ends when nothing passed either
// way for idleTimeout, so a long answer to a client that sends nothing goes on (round 2 of M10).
type idle struct {
	r    io.Reader
	c    net.Conn
	last *atomic.Int64 // when anything last passed, either way
}

func (i idle) Read(b []byte) (int, error) {
	for {
		i.c.SetReadDeadline(time.Now().Add(idleTimeout))
		n, err := i.r.Read(b)
		if n > 0 {
			i.last.Store(time.Now().UnixNano())
		}
		var ne net.Error
		if n == 0 && errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, i.last.Load())) < idleTimeout {
			continue
		}
		return n, err
	}
}
