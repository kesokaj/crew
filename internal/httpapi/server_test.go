package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/settings"
)

// A shell like the one the UI's build writes: one inline script, and one by src.
const shellHTML = `<!DOCTYPE html><html><head><script class="$tsr">self.$R=1</script>` +
	`<script type="module" src="/assets/index.js"></script></head><body><p>crew</p></body></html>`

const (
	token = "ingress-secret-for-tests"
	admin = "admin@example.com"
	host  = "crew.example.com"
)

func newServer(t *testing.T, withDB bool) *httptest.Server {
	t.Helper()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "_shell.html"), []byte(shellHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &httpapi.Server{
		Machine:      settings.Machine{Version: "1.0.0", AdminEmail: admin},
		Hostname:     host,
		AdminEmail:   admin,
		IngressToken: []byte(token),
		WebDir:       web,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if withDB {
		s.Pool.Store(dbtest.New(t, false).Server)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, hdr map[string]string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// A request as the ingress passes it carries the time of its sign-in, here a fresh one, unless the
	// test names its own: an empty value sends none.
	if _, ok := hdr[httpapi.HeaderIngress]; ok {
		req.Header.Set(httpapi.HeaderAuthTime, strconv.FormatInt(time.Now().Unix(), 10))
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var signedIn = map[string]string{httpapi.HeaderIngress: token, httpapi.HeaderEmail: admin}

func with(extra map[string]string) map[string]string {
	h := map[string]string{}
	for k, v := range signedIn {
		h[k] = v
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func TestHealthSaysWhetherTheDatabaseAnswers(t *testing.T) {
	t.Parallel()
	code, body := do(t, newServer(t, true), "GET", "/healthz", nil, "")
	if code != 200 || !strings.Contains(body, `"database":"ok"`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do(t, newServer(t, false), "GET", "/healthz", nil, "")
	if code != 503 || !strings.Contains(body, `"database":"down"`) {
		t.Fatalf("without a database: %d %s", code, body)
	}
}

// Healthy means the database can be written too (16.8): a database that only reads is not healthy.
func TestHealthNeedsADatabaseThatTakesWrites(t *testing.T) {
	t.Parallel()
	d := dbtest.New(t, false)
	url, err := d.Conn(db.RoleServer).URL()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), WebDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Pool.Store(pool)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if code, body := do(t, srv, "GET", "/healthz", nil, ""); code != 503 || !strings.Contains(body, `"database":"down"`) {
		t.Fatalf("a database that only reads: %d %s", code, body)
	}
}

// Only the ingress, with the shared secret, for the admin email, gets in (20.3).
func TestOnlyTheOperatorThroughTheIngress(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	for name, c := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"no headers":           {nil, 401},
		"an email, no secret":  {map[string]string{httpapi.HeaderEmail: admin}, 401},
		"a wrong secret":       {map[string]string{httpapi.HeaderIngress: "guess", httpapi.HeaderEmail: admin}, 401},
		"another account":      {with(map[string]string{httpapi.HeaderEmail: "someone@example.com"}), 403},
		"no account":           {with(map[string]string{httpapi.HeaderEmail: ""}), 403},
		"the operator":         {signedIn, 200},
		"the operator in caps": {with(map[string]string{httpapi.HeaderEmail: "ADMIN@example.com"}), 200},
	} {
		for _, path := range []string{"/api/whoami", "/", "/board/x"} {
			if code, body := do(t, srv, "GET", path, c.hdr, ""); code != c.want {
				t.Errorf("%s, %s: %d %s, want %d", name, path, code, body, c.want)
			}
		}
	}
}

func TestTheUIFallsBackToItsIndex(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	code, body := do(t, srv, "GET", "/projects/x/board", signedIn, "")
	if code != 200 || !strings.Contains(body, "<p>crew</p>") {
		t.Fatalf("%d %q", code, body)
	}
	code, _ = do(t, srv, "GET", "/../../etc/passwd", signedIn, "")
	if code != 200 {
		t.Fatalf("a path out of the folder: %d", code)
	}
}

// A change must come from a page of Crew's own address; the settings are validated and stored.
func TestSettingsAPI(t *testing.T) {
	t.Parallel()
	srv := newServer(t, true)
	code, body := do(t, srv, "GET", "/api/settings/machine", signedIn, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	d["disk"].(map[string]any)["clean_at"] = 70
	changed, _ := json.Marshal(d)

	if code, _ := do(t, srv, "PUT", "/api/settings/machine", signedIn, string(changed)); code != 403 {
		t.Fatalf("a change with no origin: %d", code)
	}
	if code, _ := do(t, srv, "PUT", "/api/settings/machine", with(map[string]string{"Origin": "https://evil.example"}), string(changed)); code != 403 {
		t.Fatalf("a change from another site: %d", code)
	}
	own := with(map[string]string{"Origin": "https://" + host})
	if code, body := do(t, srv, "PUT", "/api/settings/machine", own, `{"version": 1, "disk": {"clean_at": "lots"}}`); code != 400 || !strings.Contains(body, "disk.clean_at") {
		t.Fatalf("an invalid set: %d %s", code, body)
	}
	code, body = do(t, srv, "PUT", "/api/settings/machine", own, string(changed))
	if code != 200 || !strings.Contains(body, `"clean_at":70`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestTheAPISaysWhenTheDatabaseIsNotThereYet(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	if code, body := do(t, srv, "GET", "/api/settings/machine", signedIn, ""); code != 503 || !strings.Contains(body, "UNAVAILABLE") {
		t.Fatalf("%d %s", code, body)
	}
}

// Every script element of the shell gets the response's nonce, the policy runs only scripts with it
// and what they load, and the nonce differs on each response.
func TestTheUIsPolicyRunsOnlyTheShellsScripts(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	get := func() (string, string) {
		req, _ := http.NewRequest("GET", srv.URL+"/projects/x", nil)
		for k, v := range signedIn {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.Header.Get("Content-Security-Policy"), string(b)
	}
	csp, body := get()
	m := regexp.MustCompile(`'nonce-([A-Za-z0-9_-]{24})' 'strict-dynamic'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("no nonce in the policy %q", csp)
	}
	if n := strings.Count(body, `<script nonce="`+m[1]+`"`); n != 2 {
		t.Fatalf("%d script elements carry the nonce, want 2:\n%s", n, body)
	}
	if !strings.Contains(csp, "style-src 'self' 'nonce-"+m[1]+"'") {
		t.Fatalf("the styles of the page's own code do not carry the nonce: %q", csp)
	}
	if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("the policy %q", csp)
	}
	if csp2, _ := get(); csp2 == csp {
		t.Fatal("two responses had the same nonce")
	}
}

// A request that arrives at the server's address on a sandbox's bridge is not let in at this door,
// with the ingress's secret or without (20.3, "Two doors").
func TestTheIngressDoorIsClosedToSandboxes(t *testing.T) {
	t.Parallel()
	var doorAddr string
	s := &httpapi.Server{
		Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SandboxDoor: func(local string) bool { return local == doorAddr },
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if code, _ := do(t, srv, "GET", "/api/whoami", signedIn, ""); code != http.StatusOK {
		t.Fatalf("at the ingress's door: %d", code)
	}
	doorAddr = strings.TrimPrefix(srv.URL, "http://") // the same address, now counted as a sandbox's
	for _, path := range []string{"/api/whoami", "/healthz", "/"} {
		if code, body := do(t, srv, "GET", path, signedIn, ""); code != http.StatusNotFound || strings.Contains(body, admin) {
			t.Fatalf("%s at a sandbox's door: %d %s", path, code, body)
		}
	}
}

// A sandbox's guest can route the server's address on another network through its own bridge, and the
// request then arrives at an address that is no sandbox's door (13.6): it is told by where it came
// from, and refused at every route, with the ingress's secret or without, and the outside MCP too.
func TestTheIngressDoorIsClosedToASandboxsOwnAddress(t *testing.T) {
	t.Parallel()
	var guest string
	s := &httpapi.Server{
		Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SandboxDoor:  func(string) bool { return false },
		SandboxGuest: func(remote string) bool { h, _, _ := net.SplitHostPort(remote); return h == guest },
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	guest = "192.0.2.7" // a guest elsewhere: the test's own address is not one
	if code, _ := do(t, srv, "GET", "/api/whoami", signedIn, ""); code != http.StatusOK {
		t.Fatalf("from an address of no sandbox: %d", code)
	}
	guest = "127.0.0.1" // the test's own address, now counted as a sandbox's
	for _, path := range []string{"/api/whoami", "/healthz", "/", "/mcp"} {
		if code, body := do(t, srv, "GET", path, signedIn, ""); code != http.StatusNotFound || strings.Contains(body, admin) {
			t.Fatalf("%s from a sandbox's own address: %d %s", path, code, body)
		}
	}
}

// The door answers only the ingress's network and the loopback (13.6, 20.3): a request from anywhere
// else is refused at every route, the health and the outside MCP too, with the ingress's secret or
// without, while no sandbox is known to the other checks.
func TestTheIngressDoorIsOpenOnlyToTheIngresssNetwork(t *testing.T) {
	t.Parallel()
	var ingress string
	s := &httpapi.Server{
		Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		FromIngress: func(remote string) bool { h, _, _ := net.SplitHostPort(remote); return h == ingress },
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ingress = "127.0.0.1" // the test's own address, counted as the ingress's
	if code, _ := do(t, srv, "GET", "/api/whoami", signedIn, ""); code != http.StatusOK {
		t.Fatalf("from the ingress's network: %d", code)
	}
	ingress = "192.0.2.7" // the ingress elsewhere: the test's own address is not on its network
	for _, path := range []string{"/api/whoami", "/healthz", "/", "/mcp"} {
		if code, body := do(t, srv, "GET", path, signedIn, ""); code != http.StatusNotFound || strings.Contains(body, admin) {
			t.Fatalf("%s from outside the ingress's network: %d %s", path, code, body)
		}
	}
}

// A terminal connection comes from a page of Crew's own address only (20.3), as a change does: a
// request with no Origin, one over http, and one of another site are refused before anything starts.
func TestATerminalIsOpenedOnlyFromCrewsOwnPages(t *testing.T) {
	t.Parallel()
	// With the database, which says how old a fresh sign-in may be, so the request reaches the check.
	srv := newServer(t, true)
	for name, h := range map[string]map[string]string{
		"no origin":    signedIn,
		"plain http":   with(map[string]string{"Origin": "http://" + host}),
		"another site": with(map[string]string{"Origin": "https://elsewhere.example"}),
		"a cross site": with(map[string]string{"Sec-Fetch-Site": "cross-site"}),
		"a lookalike":  with(map[string]string{"Origin": "https://" + host + ".elsewhere.example"}),
	} {
		if code, body := do(t, srv, "GET", "/api/projects/shop/terminal/ws", h, ""); code != 403 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// From Crew's own page it gets past the check, to the terminals, which this server has not.
	if code, _ := do(t, srv, "GET", "/api/projects/shop/terminal/ws", change, ""); code != 503 {
		t.Errorf("from Crew's own page: %d", code)
	}
}

// The Versions routes are the operator's: the ingress with the shared secret, for the admin email
// (16.3, 20.3).
func TestTheVersionsRoutesAreTheOperatorsOnly(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/crew/versions"}, {"POST", "/api/crew/versions/0.2.0/install"}, {"POST", "/api/crew/versions/0.2.0/hold"},
		{"DELETE", "/api/crew/versions/0.2.0/hold"}, {"POST", "/api/crew/versions/0.1.0/deploy"}, {"POST", "/api/crew/updater/stop"},
	} {
		if code, _ := do(t, srv, c.method, c.path, nil, ""); code != 401 {
			t.Errorf("%s %s without the ingress: %d", c.method, c.path, code)
		}
	}
	if code, body := do(t, srv, "GET", "/api/crew/versions", signedIn, ""); code != 503 || !strings.Contains(body, "releases are not set up") {
		t.Fatalf("%d %s", code, body)
	}
}

// A release in check mode only reads (16.4): every request that would change something is refused,
// before the sign-in is even asked, and the health check answers.
func TestCheckModeOnlyReads(t *testing.T) {
	t.Parallel()
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), WebDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ReadOnly: true}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if code, body := do(t, srv, m, "/api/crew/versions/0.2.0/install", with(map[string]string{"Origin": "https://" + host}), "{}"); code != 403 ||
			!strings.Contains(body, "only reads") {
			t.Errorf("%s: %d %s", m, code, body)
		}
	}
	if code, _ := do(t, srv, "POST", "/mcp", nil, "{}"); code != 403 {
		t.Errorf("the Crew MCP in check mode: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/api/whoami", signedIn, ""); code != 200 {
		t.Errorf("a read in check mode: %d", code)
	}
}
