//go:build hosting

// Package hostingtier is the Hosting tier (design 22, plan M19 E2E 1 to 6 and 8): real microVMs on the
// machine, through a throwaway stack (stack.sh) with an ingress of this checkout. make test-hosting
// runs it on the stack's network; it is not part of make check.
package hostingtier

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/kesokaj/crew/internal/httpapi"
)

const (
	server  = "http://t19-server:8080"
	ingress = "t19-caddy:443"
	admin   = "admin@example.test"
	apps    = "apps.test"
	app     = "https://t19app." + apps
)

var (
	token   = os.Getenv("T19_INGRESS_TOKEN")
	jwtKey  = os.Getenv("T19_JWT_KEY")
	project = os.Getenv("T19_PROJECT")
)

// view is what the Hosting screen reads, as much as the tier asks.
type view struct {
	State string `json:"state"`
	Live  *struct {
		Deploy  int    `json:"deploy"`
		Sandbox string `json:"sandbox"`
	} `json:"live"`
	Deploys []struct {
		Number         int    `json:"number"`
		State          string `json:"state"`
		Detail         string `json:"detail"`
		CanRollBack    bool   `json:"canRollBack"`
		CanRestoreData bool   `json:"canRestoreData"`
	} `json:"deploys"`
	Running int `json:"running"`
}

// api calls the server as the ingress passes the operator's request on, signed in just now.
func api(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, server+path, strings.NewReader(body))
	req.Header.Set(httpapi.HeaderIngress, token)
	req.Header.Set(httpapi.HeaderEmail, admin)
	req.Header.Set(httpapi.HeaderAuthTime, strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("Origin", "https://crew.test")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func hosting(t *testing.T) view {
	t.Helper()
	code, body := api(t, "GET", "/api/projects/t19app/hosting", "")
	if code != 200 {
		t.Fatalf("hosting: %d %s", code, body)
	}
	var v view
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// deploy asks for a deploy and gives its number.
func deploy(t *testing.T, path, body string) int {
	t.Helper()
	code, out := api(t, "POST", path, body)
	if code != 202 {
		t.Fatalf("%s: %d %s", path, code, out)
	}
	var d struct{ Number int }
	json.Unmarshal([]byte(out), &d)
	return d.Number
}

// ended waits until deploy n is live or failed, at most limit, and gives its state and detail.
func ended(t *testing.T, n int, limit time.Duration) (string, string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		for _, d := range hosting(t).Deploys {
			if d.Number == n && (d.State == "live" || d.State == "failed") {
				return d.State, d.Detail
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("deploy %d did not end in %s", n, limit)
	return "", ""
}

// settings stores the project's own settings, the whole document.
func settings(t *testing.T, hostingDoc string) {
	t.Helper()
	doc := `{"version": 1, "project": "t19app", "key": "TA", "hosting": ` + hostingDoc + `}`
	if code, body := api(t, "PUT", "/api/projects/t19app/settings", doc); code != 200 {
		t.Fatalf("settings: %d %s", code, body)
	}
}

// browser reaches the hosted addresses through the stack's ingress, with its internal certificate,
// following no redirect.
var browser = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, ingress)
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       15 * time.Second,
}

// appsKey is the apps portal's key as the ingress derives it from CREW_JWT_KEY (plan M19 D6).
func appsKey(t *testing.T) []byte {
	k, err := hkdf.Key(sha256.New, []byte(jwtKey), nil, "crew apps portal", 32)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(k))
}

// cookieName is the apps portal's access token cookie (ingress.AuthCookieName of the apps domain).
func cookieName() string {
	sum := sha256.Sum256([]byte(apps))
	return "AUTHP" + strings.ToUpper(hex.EncodeToString(sum[:4])) + "_ACCESS_TOKEN"
}

// signed is an apps portal token for email with roles, as the portal writes one.
func signed(t *testing.T, email string, roles ...string) string {
	now := time.Now().Unix()
	b64 := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	body := b64(map[string]any{"alg": "HS512", "typ": "JWT"}) + "." + b64(map[string]any{"sub": email, "email": email, "name": "Test",
		"roles": roles, "iss": "https://auth." + apps + "/oauth2/google/", "iat": now, "nbf": now - 60, "exp": now + 3600})
	m := hmac.New(sha512.New, appsKey(t))
	m.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// open is a request to the hosted fixture, with a token of email when it is not "". A request that
// gets no answer, such as before the ingress took up the route, is status 0 with the error as its body.
func open(t *testing.T, path, email string, roles []string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", app+path, nil)
	if email != "" {
		req.AddCookie(&http.Cookie{Name: cookieName(), Value: signed(t, email, roles...)})
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := browser.Do(req)
	if err != nil {
		return &http.Response{StatusCode: 0, Header: http.Header{}}, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// within asks f until it is true, at most limit: the ingress takes up the server's routes every 10 s.
func within(t *testing.T, limit time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, limit)
		}
		time.Sleep(2 * time.Second)
	}
}

func docker(t *testing.T) *client.Client {
	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// inApp runs a command in the live sandbox and gives its exit code.
func inApp(t *testing.T, sandbox string, cmd ...string) int {
	t.Helper()
	ctx := context.Background()
	d := docker(t)
	ex, err := d.ContainerExecCreate(ctx, "crew-"+sandbox, container.ExecOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	att, err := d.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, att.Reader)
	att.Close()
	insp, err := d.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		t.Fatal(err)
	}
	return insp.ExitCode
}

func TestTheHostingTier(t *testing.T) {
	if token == "" || jwtKey == "" || project == "" {
		t.Fatal("run by make test-hosting, which gives the stack's values")
	}
	var first int
	t.Run("E2E 1: Host locally serves the fixture to the admin, behind the sign-in", func(t *testing.T) {
		first = deploy(t, "/api/projects/t19app/hosting/deploys", "")
		if state, detail := ended(t, first, 10*time.Minute); state != "live" {
			t.Fatalf("deploy %d: %s %s", first, state, detail)
		}
		within(t, 60*time.Second, "the route", func() bool {
			resp, body := open(t, "/", admin, nil, nil)
			return resp.StatusCode == 200 && strings.Contains(body, "the fixture of the Hosting tier")
		})
		if resp, body := open(t, "/cgi-bin/who", admin, nil, map[string]string{"X-Caddy-Email": "mallory@example.test"}); resp.StatusCode != 200 ||
			!strings.Contains(body, "email="+admin+"\n") || strings.Contains(body, "mallory") {
			t.Fatalf("who: %d %s", resp.StatusCode, body)
		}
		if resp, _ := open(t, "/", "", nil, nil); resp.StatusCode < 300 || resp.StatusCode > 399 || !strings.Contains(resp.Header.Get("Location"), "auth."+apps) {
			t.Fatalf("no token: %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		req, _ := http.NewRequest("GET", "https://crew.test/healthz", nil)
		if resp, err := browser.Do(req); err != nil || resp.StatusCode != 200 {
			t.Fatalf("Crew's route: %v %v", resp, err)
		}
	})
	t.Run("E2E 6: no Set-Cookie with a Domain, no portal cookie at the app", func(t *testing.T) {
		resp, body := open(t, "/cgi-bin/who", admin, nil, nil)
		for _, c := range resp.Header.Values("Set-Cookie") {
			if strings.Contains(strings.ToLower(c), "domain=") {
				t.Fatalf("a Set-Cookie with a Domain reached the client: %s", c)
			}
		}
		if strings.Contains(body, "AUTHP") {
			t.Fatalf("the app got a portal cookie: %s", body)
		}
		// A change another hosted project's page asks is refused before the sign-in; the project's own
		// page and a read pass (security review of M19).
		ask := func(method string, headers map[string]string) int {
			req, _ := http.NewRequest(method, app+"/cgi-bin/who", strings.NewReader("x"))
			req.AddCookie(&http.Cookie{Name: cookieName(), Value: signed(t, admin)})
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := browser.Do(req)
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		post := func(site string) int { return ask("POST", map[string]string{"Sec-Fetch-Site": site}) }
		if code := post("same-site"); code != 403 {
			t.Fatalf("a change from another hosted project: %d", code)
		}
		if code := post("same-origin"); code == 403 || code == 0 {
			t.Fatalf("a change from the project's own page: %d", code)
		}
		// A WebSocket from another site, and a change from another Origin by a browser that sends no
		// Sec-Fetch-Site, are refused; the project's own Origin passes (round 2).
		if code := ask("GET", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-Fetch-Site": "same-site"}); code != 403 {
			t.Fatalf("a WebSocket from another hosted project: %d", code)
		}
		if code := ask("POST", map[string]string{"Origin": "https://other." + apps}); code != 403 {
			t.Fatalf("a change from another Origin with no Sec-Fetch-Site: %d", code)
		}
		if code := ask("POST", map[string]string{"Origin": app}); code == 403 || code == 0 {
			t.Fatalf("a change from the project's own Origin with no Sec-Fetch-Site: %d", code)
		}
		// A WebSocket from another Origin by a browser that sends no Sec-Fetch-Site is refused too; from
		// the project's own Origin it passes (security review of the release of M20).
		if code := ask("GET", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Origin": "https://other." + apps}); code != 403 {
			t.Fatalf("a WebSocket from another Origin with no Sec-Fetch-Site: %d", code)
		}
		if code := ask("GET", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Origin": app}); code == 403 || code == 0 {
			t.Fatalf("a WebSocket from the project's own Origin with no Sec-Fetch-Site: %d", code)
		}
	})
	t.Run("E2E 5: the app opens nothing on its bridge, nor a private address", func(t *testing.T) {
		v := hosting(t)
		sb := v.Live.Sandbox
		var addr string
		insp, err := docker(t).ContainerInspect(context.Background(), "crew-"+sb)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range insp.NetworkSettings.Networks {
			addr = n.IPAddress
		}
		bridge := addr[:strings.LastIndex(addr, ".")]
		for _, target := range []string{bridge + ".254:8080", bridge + ".253:443", bridge + ".1:22", "10.0.0.1:80", "192.168.0.1:80"} {
			if code := inApp(t, sb, "sh", "-c", "nc -w 3 "+strings.Replace(target, ":", " ", 1)+" </dev/null"); code == 0 {
				t.Errorf("the app reached %s", target)
			}
		}
		if code := inApp(t, sb, "sh", "-c", "nc -w 6 1.1.1.1 443 </dev/null"); code != 0 {
			t.Errorf("the app did not reach the internet: %d", code)
		}
	})
	t.Run("E2E 4: the four access choices", func(t *testing.T) {
		settings(t, `{"public_rps": 2, "access": "listed", "allowed": ["friend@example.test"]}`)
		within(t, 30*time.Second, "listed", func() bool {
			r1, _ := open(t, "/", "friend@example.test", nil, nil)
			r2, _ := open(t, "/", "stranger@example.test", []string{"authp/user"}, nil)
			return r1.StatusCode == 200 && r2.StatusCode == 403
		})
		settings(t, `{"public_rps": 2, "access": "google"}`)
		within(t, 30*time.Second, "google", func() bool {
			r, _ := open(t, "/", "stranger@example.test", []string{"authp/user"}, nil)
			return r.StatusCode == 200
		})
		settings(t, `{"public_rps": 2, "access": "public"}`)
		within(t, 30*time.Second, "public", func() bool {
			r, _ := open(t, "/", "", nil, nil)
			return r.StatusCode == 200
		})
		limited := false
		for range 12 {
			if r, _ := open(t, "/", "", nil, nil); r.StatusCode == 429 {
				limited = true
			}
		}
		if !limited {
			t.Fatal("no 429 above public_rps")
		}
		settings(t, `{"public_rps": 2}`)
		within(t, 30*time.Second, "private", func() bool {
			r1, _ := open(t, "/", "friend@example.test", nil, nil)
			r2, _ := open(t, "/", admin, nil, nil)
			return r1.StatusCode == 403 && r2.StatusCode == 200
		})
	})
	t.Run("E2E 2: a deploy whose health never answers fails, and the one before serves on", func(t *testing.T) {
		before := hosting(t).Live.Deploy
		settings(t, `{"public_rps": 2, "health": "/not-there"}`)
		n := hosting(t).Running
		if n == 0 {
			t.Fatal("a change of the health address started no deploy")
		}
		if state, detail := ended(t, n, 6*time.Minute); state != "failed" || !strings.Contains(detail, "/not-there did not answer") {
			t.Fatalf("deploy %d: %s %s", n, state, detail)
		}
		if v := hosting(t); v.Live.Deploy != before {
			t.Fatalf("serving %d, not %d", v.Live.Deploy, before)
		}
		if resp, _ := open(t, "/", admin, nil, nil); resp.StatusCode != 200 {
			t.Fatalf("the address: %d", resp.StatusCode)
		}
		settings(t, `{"public_rps": 2}`)
		if n := hosting(t).Running; n > 0 {
			if state, detail := ended(t, n, 6*time.Minute); state != "live" {
				t.Fatalf("deploy %d: %s %s", n, state, detail)
			}
		}
	})
	t.Run("E2E 3: a roll back with the data puts back the data of before", func(t *testing.T) {
		v := hosting(t)
		k := v.Live.Deploy
		open(t, "/cgi-bin/data?write=before", admin, nil, nil)
		n := deploy(t, "/api/projects/t19app/hosting/deploys", "")
		if state, detail := ended(t, n, 10*time.Minute); state != "live" {
			t.Fatalf("deploy %d: %s %s", n, state, detail)
		}
		within(t, 40*time.Second, "the new deploy's route", func() bool {
			_, body := open(t, "/cgi-bin/data?write=after", admin, nil, nil)
			return strings.Contains(body, "after")
		})
		r := deploy(t, fmt.Sprintf("/api/projects/t19app/hosting/deploys/%d/rollback", k), `{"restoreData": true}`)
		if state, detail := ended(t, r, 6*time.Minute); state != "live" {
			t.Fatalf("the roll back %d: %s %s", r, state, detail)
		}
		within(t, 40*time.Second, "the data of before", func() bool {
			_, body := open(t, "/cgi-bin/data", admin, nil, nil)
			return strings.Contains(body, "before") && !strings.Contains(body, "after")
		})
	})
	t.Run("E2E 8: a restart of Crew during a deploy fails it, and the next one runs", func(t *testing.T) {
		before := hosting(t).Live.Deploy
		n := deploy(t, "/api/projects/t19app/hosting/deploys", "")
		time.Sleep(5 * time.Second)
		timeout := 10
		if err := docker(t).ContainerRestart(context.Background(), "t19-server", container.StopOptions{Timeout: &timeout}); err != nil {
			t.Fatal(err)
		}
		// The server answers /healthz before its hosting is in place: the route itself is waited for.
		within(t, 90*time.Second, "the server's hosting", func() bool {
			code, _ := api(t, "GET", "/api/projects/t19app/hosting", "")
			return code == 200
		})
		if state, detail := ended(t, n, 2*time.Minute); state != "failed" || detail != "interrupted by a restart of Crew" {
			t.Fatalf("deploy %d: %s %q", n, state, detail)
		}
		if v := hosting(t); v.Live.Deploy != before {
			t.Fatalf("serving %d, not %d", v.Live.Deploy, before)
		}
		within(t, 70*time.Second, "the route after the restart", func() bool {
			resp, _ := open(t, "/", admin, nil, nil)
			return resp.StatusCode == 200
		})
		m := deploy(t, "/api/projects/t19app/hosting/deploys", "")
		if state, detail := ended(t, m, 10*time.Minute); state != "live" {
			t.Fatalf("deploy %d: %s %s", m, state, detail)
		}
	})
	t.Run("a restart of the hosted microVM: it answers again at its address", func(t *testing.T) {
		sb := hosting(t).Live.Sandbox
		timeout := 10
		if err := docker(t).ContainerRestart(context.Background(), "crew-"+sb, container.StopOptions{Timeout: &timeout}); err != nil {
			t.Fatal(err)
		}
		within(t, 90*time.Second, "the restarted microVM", func() bool {
			resp, _ := open(t, "/", admin, nil, nil)
			return resp.StatusCode == 200
		})
	})
	t.Run("Stop hosting: the address stops answering", func(t *testing.T) {
		// A name of the apps domain with no route is what t19app's is once its route is gone: it must
		// read as stopped, so the TLS branch below is checked on every run, not only when a probe misses
		// the 502 of the removed microVM.
		req, _ := http.NewRequest("GET", "https://nosuch."+apps+"/", nil)
		if resp, err := browser.Do(req); err == nil {
			resp.Body.Close()
			if !stopped(resp.StatusCode, "") {
				t.Fatalf("a name with no route answers %d", resp.StatusCode)
			}
		} else if !stopped(0, err.Error()) {
			t.Fatalf("a name with no route fails otherwise: %v", err)
		}
		// t19app answers until it is stopped, so a refusal after Stop is the Stop's, not the ingress's.
		if resp, body := open(t, "/", admin, nil, nil); resp.StatusCode != 200 {
			t.Fatalf("before Stop: %d %.120s", resp.StatusCode, body)
		}
		if code, body := api(t, "DELETE", "/api/projects/t19app/hosting", ""); code != 200 {
			t.Fatalf("stop: %d %s", code, body)
		}
		// Stopped is any of the ways this ingress stops answering a name: 5xx while the route still
		// points at the removed microVM, 404 where a wildcard site holds the name, and, with internal
		// certificates as on this stack, a TLS refusal once the route and the name's site are gone
		// (ingress/generator.go). Each attempt is logged, so a failure says what it saw (BUG-009).
		within(t, 30*time.Second, "no route", func() bool {
			resp, body := open(t, "/", admin, nil, nil)
			t.Logf("after Stop: %d %.120s", resp.StatusCode, body)
			return stopped(resp.StatusCode, body)
		})
	})
}

// stopped says an answer of the ingress is one of the ways it stops answering a name (BUG-009).
func stopped(status int, body string) bool {
	return status == 404 || status >= 500 || (status == 0 && strings.Contains(body, "tls:"))
}
