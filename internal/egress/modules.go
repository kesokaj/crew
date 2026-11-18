package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// ModulesPath is where the door answers Go's module proxy protocol for a Sources sandbox (13.6,
// since 0.61): the server sets its GOPROXY to the door with this path. proxy.golang.org answers the
// zip of a large module with a redirect into Go's bucket on storage.googleapis.com, which takes
// uploads to any bucket and is off sandbox.sources: the server follows that redirect itself, into
// that bucket's path and nowhere else, so the sandbox never reaches the host.
const ModulesPath = "/goproxy/"

// The module proxy's one way out, and its limits: a file at most maxModuleBytes, Go's own limit of a
// module's zip being 500 MiB, and moduleTimeout for one.
const (
	modulesOrigin = "https://proxy.golang.org"
	modulesBucket = "https://storage.googleapis.com/proxy-golang-org-prod/"
)

var (
	maxModuleBytes int64 = 512 << 20
	moduleTimeout        = 10 * time.Minute
)

// modulesClient is the server's own client to Go's proxy and its bucket: no proxy of the
// environment, only those two hosts at a public address on port 443, and one redirect, into the
// bucket's path.
type modulesClient struct {
	once sync.Once
	c    *http.Client
}

func (p *Proxy) modulesClient() *http.Client {
	p.modules.once.Do(func() {
		origin, _ := url.Parse(modulesOrigin)
		bucket, _ := url.Parse(modulesBucket)
		hosts := []string{origin.Host, bucket.Host}
		p.modules.c = &http.Client{
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					host, port, err := net.SplitHostPort(addr)
					if err != nil || port != "443" || !slices.Contains(hosts, host) {
						return nil, fmt.Errorf("the module proxy reaches only %v on port 443, not %s", hosts, addr)
					}
					return p.dial(ctx, host)
				},
				TLSClientConfig:       &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout:   dialTimeout,
				DisableCompression:    true, // what Go's proxy sends is handed on as it is
				ResponseHeaderTimeout: time.Minute,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       time.Minute,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 1 {
					return errors.New("a second redirect")
				}
				if req.Method != http.MethodGet || !inBucket(req.URL, bucket) {
					return fmt.Errorf("a redirect to %s, out of Go's bucket", req.URL.Redacted())
				}
				return nil
			},
		}
	})
	return p.modules.c
}

// inBucket says whether a redirect goes into Go's bucket: https, its host with no port or user, and
// a clean path under its prefix.
func inBucket(u, bucket *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && u.Host == bucket.Host && u.RawPath == "" &&
		strings.HasPrefix(u.Path, bucket.Path) && path.Clean(u.Path) == u.Path
}

// modulePath says whether p, the path after ModulesPath, is one Go's command asks a module proxy
// for: an escaped module path, then @v/list, @v/<version>.info, .mod or .zip, or @latest. Escaped,
// a path holds no capital, and it is no longer than Go's command makes one. The checksum database's
// paths are not answered, so Go's command asks sum.golang.org itself, through its tunnel.
func modulePath(p string) bool {
	if p == "" || len(p) > 1024 {
		return false
	}
	for _, r := range p {
		if !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || strings.ContainsRune("-._~!+/@", r)) {
			return false
		}
	}
	mod, rest, ok := strings.Cut(p, "/@")
	if !ok || mod == "" || strings.Contains(mod, "@") {
		return false
	}
	for _, seg := range strings.Split(mod, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	if rest == "latest" {
		return true
	}
	file, ok := strings.CutPrefix(rest, "v/")
	if !ok {
		return false
	}
	if file == "list" {
		return true
	}
	for _, ext := range []string{".info", ".mod", ".zip"} {
		if v, ok := strings.CutSuffix(file, ext); ok {
			return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/@")
		}
	}
	return false
}

// ServeModules answers GET ModulesPath<path> for a Sources sandbox: it asks proxy.golang.org for the
// path itself, follows a redirect only into Go's bucket, and hands on the answer within
// maxModuleBytes. Nothing the sandbox sent goes on but the path. Go's command checks every module it
// gets against go.sum and the checksum database, so the server checks no hash.
func (p *Proxy) ServeModules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "only GET", http.StatusMethodNotAllowed)
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
	origin, _ := url.Parse(modulesOrigin)
	// Refused as a sandbox, not as a host: the hosts refused are how a list is measured (13.6).
	if p.IsSources == nil || !p.IsSources(sandbox) {
		const why = "the module proxy answers only a Sources sandbox"
		p.Log.Info("the module proxy refused a sandbox", "sandbox", sandbox, "why", why)
		http.Error(w, "refused: "+why, http.StatusForbidden)
		return
	}
	list, setting, err := p.hosts(r.Context(), sandbox)
	if err != nil {
		p.Log.Error("the proxy cannot read its list of hosts", "list", setting, "error", err)
		http.Error(w, "the list of hosts cannot be read", http.StatusBadGateway)
		return
	}
	if !slices.Contains(list, origin.Host) {
		p.refuse(w, sandbox, origin.Host, "not on "+setting)
		return
	}
	mod := strings.TrimPrefix(r.URL.Path, ModulesPath)
	// Only the decoded path is checked and sent on: what the sandbox escaped is decoded first.
	if r.URL.RawQuery != "" || !modulePath(mod) {
		http.Error(w, "not a path of Go's module proxy", http.StatusNotFound)
		return
	}
	if !p.take(sandbox) {
		p.refuse(w, sandbox, origin.Host, fmt.Sprintf("more than %d tunnels at once", maxTunnels))
		return
	}
	defer p.give(sandbox)
	ctx, cancel := context.WithTimeout(r.Context(), moduleTimeout)
	defer cancel()
	// The bound holds for the writes to the sandbox too: one that stops reading gives back its tunnel.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(moduleTimeout)); err != nil {
		p.Log.Warn("the module proxy cannot bound its answer in time", "sandbox", sandbox, "error", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modulesOrigin+"/"+mod, nil)
	if err != nil {
		http.Error(w, "not a path of Go's module proxy", http.StatusNotFound)
		return
	}
	resp, err := p.modulesClient().Do(req)
	if err != nil {
		var ue *url.Error
		why := err.Error()
		if errors.As(err, &ue) {
			why = ue.Err.Error()
		}
		p.Log.Info("the module proxy could not get a module", "sandbox", sandbox, "path", mod, "why", why)
		http.Error(w, "the module cannot be fetched", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		// Go's command reads these as "no such module or version", and prints the text. Go's proxy
		// fetches a module it does not hold from wherever its path names, so a path can carry what the
		// sandbox holds to any host; such paths end here, and are logged (13.6).
		p.Log.Info("the module proxy found no such module", "sandbox", sandbox, "path", mod, "status", resp.StatusCode)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 4096))
		return
	default:
		p.Log.Info("the module proxy got an answer it does not hand on", "sandbox", sandbox, "path", mod, "status", resp.StatusCode)
		http.Error(w, fmt.Sprintf("Go's proxy answered %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	if resp.ContentLength > maxModuleBytes {
		p.Log.Info("the module proxy refused a module", "sandbox", sandbox, "path", mod, "why", fmt.Sprintf("%d bytes, more than %d", resp.ContentLength, maxModuleBytes))
		http.Error(w, "the module is too large", http.StatusBadGateway)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", fmt.Sprint(resp.ContentLength))
	}
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(w, io.LimitReader(resp.Body, maxModuleBytes))
	if err == nil && n == maxModuleBytes {
		if more, _ := io.ReadFull(resp.Body, make([]byte, 1)); more > 0 {
			err = fmt.Errorf("more than %d bytes", maxModuleBytes)
		}
	}
	if err != nil {
		// The answer is cut short: Go's command gets no whole file, and fails on it.
		p.Log.Info("the module proxy cut a module short", "sandbox", sandbox, "path", mod, "bytes", n, "error", err)
		panic(http.ErrAbortHandler)
	}
	p.Log.Debug("the module proxy handed on a module", "sandbox", sandbox, "path", mod, "bytes", n)
}
