package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/settings"
)

// Sessions and the fresh sign-in (design 20.3, plan M17 phase 4): the ingress passes the time of a
// sign-in and the device's id; the server ends a session after sessions.days, refuses a device signed
// out, and asks a fresh sign-in on the routes and the keys 20.3 names.

// signedInAt is a request of device jti as the ingress passes it, signed in age ago: a change, from
// Crew's own page.
func signedInAt(age time.Duration, jti string) map[string]string {
	h := with(map[string]string{"Origin": "https://" + host, httpapi.HeaderAuthTime: strconv.FormatInt(time.Now().Add(-age).Unix(), 10)})
	if jti != "" {
		h[httpapi.HeaderSession] = jti
	}
	return h
}

const stale = 20 * time.Minute

func reason(body string) string {
	var e struct {
		Error struct {
			Details []struct {
				Reason string   `json:"reason"`
				SignIn string   `json:"signIn"`
				Keys   []string `json:"keys"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) != nil || len(e.Error.Details) == 0 {
		return ""
	}
	return e.Error.Details[0].Reason
}

// The route list (D14): every pattern the API registers is in freshRoutes or plainRoutes, and in one
// only, and every pattern the lists name is registered, so a new route cannot be added without the
// choice.
func TestEveryRouteIsFreshOrPlain(t *testing.T) {
	t.Parallel()
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	got := s.Patterns()
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("%s is registered twice", p)
		}
		seen[p] = true
		fresh, plain := slices.Contains(httpapi.FreshRoutes, p), slices.Contains(httpapi.PlainRoutes, p)
		switch {
		case fresh && plain:
			t.Errorf("%s is in both lists", p)
		case !fresh && !plain:
			t.Errorf("%s is in neither list: put it in freshRoutes or plainRoutes (20.3)", p)
		}
	}
	for _, list := range [][]string{httpapi.FreshRoutes, httpapi.PlainRoutes} {
		for _, p := range list {
			if !seen[p] {
				t.Errorf("%s is listed but no route has it", p)
			}
		}
	}
	// What 20.3 names is fresh; the pause and the stop, the brakes, are not.
	for _, p := range []string{"POST /api/projects/{p}/terminal", "PUT /api/projects/{p}/terminal/mode", "GET /api/projects/{p}/terminal/ws",
		"GET /api/setup/terminal", "POST /api/crew/mcp/tokens", "POST /api/crew/versions/{v}/install", "POST /api/crew/resume",
		"POST /api/machine/boot/resume", "POST /api/sessions/{id}/signout", "POST /api/projects/{p}/sandbox/build-image"} {
		if !slices.Contains(httpapi.FreshRoutes, p) {
			t.Errorf("%s does not ask for a fresh sign-in", p)
		}
	}
	for _, p := range []string{"POST /api/crew/pause", "POST /api/crew/stop", "POST /api/crew/mcp/tokens/{id}/revoke"} {
		if slices.Contains(httpapi.FreshRoutes, p) {
			t.Errorf("%s, which only takes away, asks for a fresh sign-in", p)
		}
	}
}

// path is a request's path for a pattern, with a value for each of its wildcards.
func path(pattern string) (method, p string) {
	method, p, ok := strings.Cut(pattern, " ")
	if !ok {
		return "GET", "/api/nothing"
	}
	for k, v := range map[string]string{"{p}": "shop", "{t}": "SF-1", "{v}": "0.0.1", "{nonce}": "x", "{cli}": "agy", "{model}": "m"} {
		p = strings.ReplaceAll(p, k, v)
	}
	for strings.Contains(p, "{") {
		a := strings.Index(p, "{")
		p = p[:a] + "1" + p[a+strings.Index(p[a:], "}")+1:]
	}
	return method, p
}

// Each fresh route refuses a sign-in older than sessions.fresh_minutes with FRESH_SIGN_IN, and lets a
// fresh one reach its handler; a plain route takes a stale one.
func TestEachFreshRouteAsksForAFreshSignIn(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	for _, pattern := range httpapi.FreshRoutes {
		method, p := path(pattern)
		code, body := do(t, bs.srv, method, p, signedInAt(stale, ""), "{}")
		if code != 401 || reason(body) != httpapi.ReasonFresh {
			t.Errorf("%s, stale: %d %s", pattern, code, body)
		}
		code, body = do(t, bs.srv, method, p, signedInAt(time.Minute, ""), "{}")
		if reason(body) == httpapi.ReasonFresh {
			t.Errorf("%s, fresh: %d %s", pattern, code, body)
		}
	}
	// No time of the sign-in at all is not fresh either.
	if code, body := do(t, bs.srv, "POST", "/api/crew/resume", with(map[string]string{"Origin": "https://" + host, httpapi.HeaderAuthTime: ""}), ""); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Errorf("no time: %d %s", code, body)
	}
	for _, pattern := range []string{"GET /api/frame", "POST /api/crew/pause", "GET /api/projects/{p}/board"} {
		method, p := path(pattern)
		if code, body := do(t, bs.srv, method, p, signedInAt(stale, ""), ""); code == 401 {
			t.Errorf("%s, a plain route, stale: %d %s", pattern, code, body)
		}
	}
	// Taking a held main (rule 13) and a project's files written out whole ask for one (security
	// review of M17's Close).
	for _, pattern := range []string{"POST /api/projects/{p}/remote/mains/{id}/take", "POST /api/projects/{p}/terminal/mains/{id}/take", "POST /api/projects/{p}/export"} {
		method, p := path(pattern)
		if code, body := do(t, bs.srv, method, p, signedInAt(stale, ""), "{}"); code != 401 || reason(body) != httpapi.ReasonFresh {
			t.Errorf("%s, stale: %d %s", pattern, code, body)
		}
	}
	// The answer names the ingress's sign-in, asking which account.
	_, body := do(t, bs.srv, "POST", "/api/crew/resume", signedInAt(stale, ""), "")
	if !strings.Contains(body, `"signIn":"https://auth.`+host+`/oauth2/google/?prompt=select_account"`) {
		t.Errorf("the sign-in: %s", body)
	}
}

// sessionServer is the server on a test database, with the database's pool to read the devices.
func sessionServer(t *testing.T, readOnly bool) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	d := dbtest.New(t, false)
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), WebDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ReadOnly: readOnly}
	s.Pool.Store(d.Server)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, d.Server
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type devicesView struct {
	Devices []struct {
		ID        int64     `json:"id"`
		AuthAt    time.Time `json:"authAt"`
		FirstAt   time.Time `json:"firstAt"`
		UserAgent string    `json:"userAgent"`
		This      bool      `json:"this"`
	} `json:"devices"`
	This struct {
		ID    int64 `json:"id"`
		Fresh bool  `json:"fresh"`
	} `json:"this"`
}

func devices(t *testing.T, srv *httptest.Server, h map[string]string) devicesView {
	t.Helper()
	code, body := do(t, srv, "GET", "/api/sessions", h, "")
	if code != 200 {
		t.Fatalf("the devices: %d %s", code, body)
	}
	var v devicesView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A device is made at its first request and listed, this one marked; signed out on another device,
// every request of it is refused with SIGNED_OUT, which names the ingress's logout, and the other
// device goes on.
func TestADeviceSignedOutIsRefused(t *testing.T) {
	t.Parallel()
	srv, pool := sessionServer(t, false)
	laptop, phone := signedInAt(time.Minute, "laptop-session-id"), signedInAt(time.Minute, "phone-session-id")
	phone["User-Agent"] = "a phone"
	if code, body := do(t, srv, "GET", "/api/frame", phone, ""); code == 401 {
		t.Fatalf("the phone: %d %s", code, body)
	}
	v := devices(t, srv, laptop)
	if len(v.Devices) != 2 || !v.Devices[0].This || v.Devices[1].This || v.Devices[1].UserAgent != "a phone" || !v.This.Fresh {
		t.Fatalf("two devices, this one first: %+v", v)
	}
	phoneID := v.Devices[1].ID
	if code, body := do(t, srv, "POST", "/api/sessions/"+strconv.FormatInt(phoneID, 10)+"/signout", laptop, ""); code != 200 {
		t.Fatalf("sign out: %d %s", code, body)
	}
	code, body := do(t, srv, "GET", "/api/frame", phone, "")
	if code != 401 || reason(body) != httpapi.ReasonSignedOut || !strings.Contains(body, "This device was signed out") ||
		!strings.Contains(body, `/oauth2/google/logout?redirect_uri=https%3A%2F%2F`+host+`%2F`) {
		t.Fatalf("the phone after: %d %s", code, body)
	}
	// Its id without a time is refused too (security review of M17 P4).
	noTime := signedInAt(0, "phone-session-id")
	noTime[httpapi.HeaderAuthTime] = ""
	if code, body := do(t, srv, "GET", "/api/frame", noTime, ""); code != 401 || reason(body) != httpapi.ReasonSignedOut {
		t.Fatalf("the phone without a time: %d %s", code, body)
	}
	if code, _ := do(t, srv, "GET", "/api/frame", laptop, ""); code != 200 {
		t.Fatalf("the laptop after: %d", code)
	}
	if v := devices(t, srv, laptop); len(v.Devices) != 1 || v.Devices[0].ID == phoneID {
		t.Fatalf("the list after: %+v", v)
	}
	if n := count(t, pool, `SELECT count(*) FROM changes WHERE tbl = 'sessions' AND row_key = $1`, strconv.FormatInt(phoneID, 10)); n != 1 {
		t.Fatalf("the sign-out is a change of its own: %d", n)
	}
	// Signed out once is for good: a second sign-out finds no device.
	if code, _ := do(t, srv, "POST", "/api/sessions/"+strconv.FormatInt(phoneID, 10)+"/signout", laptop, ""); code != 404 {
		t.Fatalf("a second sign-out: %d", code)
	}
	// Signing out asks for a fresh sign-in.
	if code, body := do(t, srv, "POST", "/api/sessions/1/signout", signedInAt(stale, "laptop-session-id"), ""); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("a stale sign-out: %d %s", code, body)
	}
}

// A sign-in older than sessions.days is over, on every route, with SESSION_OVER; the key shortens
// the ingress's 30 days.
func TestASessionOlderThanSessionsDaysIsOver(t *testing.T) {
	t.Parallel()
	srv, _ := sessionServer(t, false)
	h := signedInAt(31*24*time.Hour, "old")
	if code, body := do(t, srv, "GET", "/api/frame", h, ""); code != 401 || reason(body) != httpapi.ReasonOver {
		t.Fatalf("31 days: %d %s", code, body)
	}
	h = signedInAt(3*24*time.Hour, "three")
	if code, _ := do(t, srv, "GET", "/api/frame", h, ""); code != 200 {
		t.Fatalf("3 days: %d", code)
	}
	fresh := signedInAt(time.Minute, "fresh")
	var d map[string]any
	_, body := do(t, srv, "GET", "/api/settings/machine", fresh, "")
	json.Unmarshal([]byte(body), &d)
	d["sessions"].(map[string]any)["days"] = 2
	b, _ := json.Marshal(d)
	if code, body := do(t, srv, "PUT", "/api/settings/machine", fresh, string(b)); code != 200 {
		t.Fatalf("days 2: %d %s", code, body)
	}
	if code, body := do(t, srv, "GET", "/api/frame", h, ""); code != 401 || reason(body) != httpapi.ReasonOver {
		t.Fatalf("3 days with days 2: %d %s", code, body)
	}
	// A time the ingress would never pass is no sign-in.
	if code, body := do(t, srv, "GET", "/api/frame", with(map[string]string{httpapi.HeaderAuthTime: "1.7e+09"}), ""); code != 401 || reason(body) != httpapi.ReasonOver {
		t.Fatalf("an unreadable time: %d %s", code, body)
	}
}

// A device's last time is written at most once a minute (D21), and a release in check mode writes
// no device at all.
func TestADevicesLastTimeIsWrittenOnceAMinute(t *testing.T) {
	t.Parallel()
	srv, pool := sessionServer(t, false)
	h := signedInAt(time.Minute, "dev")
	for range 3 {
		do(t, srv, "GET", "/api/frame", h, "")
	}
	if n := count(t, pool, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("one device: %d", n)
	}
	var first time.Time
	pool.QueryRow(context.Background(), `SELECT last_at FROM sessions`).Scan(&first)
	time.Sleep(50 * time.Millisecond)
	do(t, srv, "GET", "/api/frame", h, "")
	var last time.Time
	pool.QueryRow(context.Background(), `SELECT last_at FROM sessions`).Scan(&last)
	if !last.Equal(first) {
		t.Fatalf("written again within the minute: %v %v", first, last)
	}
	// A newer sign-in of the same device is written at once.
	do(t, srv, "GET", "/api/frame", signedInAt(0, "dev"), "")
	pool.QueryRow(context.Background(), `SELECT last_at FROM sessions`).Scan(&last)
	if last.Equal(first) {
		t.Fatal("a newer sign-in was not written")
	}

	check, cpool := sessionServer(t, true)
	if code, _ := do(t, check, "GET", "/api/frame", signedInAt(time.Minute, "dev"), ""); code != 200 {
		t.Fatalf("check mode: %d", code)
	}
	if n := count(t, cpool, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("check mode wrote a device: %d", n)
	}
}

// A fresh sign-in after the browser restarted brings a new id: the handover moves it onto the device
// that asked, keeping when it was first seen (D13); the row the new id made at the page's first
// request goes. A nonce is good once, for ten minutes, for the same email.
func TestAFreshSignInIsHandedOverToTheDevice(t *testing.T) {
	t.Parallel()
	srv, pool := sessionServer(t, false)
	old := signedInAt(stale, "before-restart")
	do(t, srv, "GET", "/api/frame", old, "")
	before := devices(t, srv, old)
	code, body := do(t, srv, "POST", "/api/handovers", old, "")
	if code != 200 {
		t.Fatalf("handover: %d %s", code, body)
	}
	var n struct{ Nonce string }
	json.Unmarshal([]byte(body), &n)
	if len(n.Nonce) < 40 {
		t.Fatalf("nonce: %s", body)
	}
	if got := count(t, pool, `SELECT count(*) FROM session_handovers WHERE nonce_sha256 = encode(sha256($1::bytea), 'hex')`, n.Nonce); got != 1 {
		t.Fatalf("the nonce's hash is kept, not the nonce: %d", got)
	}
	// Back from Google with a new id: the page's first requests make a device of it.
	fresh := signedInAt(0, "after-restart")
	do(t, srv, "GET", "/api/frame", fresh, "")
	if got := count(t, pool, `SELECT count(*) FROM sessions`); got != 2 {
		t.Fatalf("before the handover: %d devices", got)
	}
	code, body = do(t, srv, "POST", "/api/handovers/"+n.Nonce, fresh, "")
	if code != 200 || !strings.Contains(body, `"moved":true`) {
		t.Fatalf("take over: %d %s", code, body)
	}
	after := devices(t, srv, fresh)
	if len(after.Devices) != 1 || after.Devices[0].ID != before.Devices[0].ID || !after.Devices[0].This ||
		!after.Devices[0].FirstAt.Equal(before.Devices[0].FirstAt) || !after.Devices[0].AuthAt.After(before.Devices[0].AuthAt) {
		t.Fatalf("one device, the old one, signed in anew: %+v then %+v", before, after)
	}
	// Both ids are the device's: signed out, neither passes.
	if got := count(t, pool, `SELECT count(*) FROM session_ids WHERE session = $1`, before.Devices[0].ID); got != 2 {
		t.Fatalf("the device's ids: %d", got)
	}
	// Used once.
	if _, body := do(t, srv, "POST", "/api/handovers/"+n.Nonce, signedInAt(0, "third"), ""); !strings.Contains(body, `"moved":false`) {
		t.Fatalf("a used nonce: %s", body)
	}
	// Run out, or another email's sign-in: refused, and the new id stays a device of its own.
	_, body = do(t, srv, "POST", "/api/handovers", fresh, "")
	json.Unmarshal([]byte(body), &n)
	// The new device was seen before, so no new row's clean-up takes the late nonce first.
	do(t, srv, "GET", "/api/frame", signedInAt(0, "fourth"), "")
	if _, err := pool.Exec(context.Background(), `UPDATE session_handovers SET until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, body := do(t, srv, "POST", "/api/handovers/"+n.Nonce, signedInAt(0, "fourth"), ""); !strings.Contains(body, `"moved":false`) {
		t.Fatalf("a late nonce: %s", body)
	}
	if got := count(t, pool, `SELECT count(*) FROM session_ids WHERE jti = 'fourth' AND session <> $1`, before.Devices[0].ID); got != 1 {
		t.Fatalf("the late sign-in is a device of its own: %d", got)
	}
}

// A change of a key 20.3 names, without a fresh sign-in, is refused with FRESH_SIGN_IN naming the
// key, and nothing of the request is stored; a fresh one is stored; a change of any other key needs
// none. Put back is held the same way, a group key's too (D15).
func TestAFreshKeyNeedsAFreshSignIn(t *testing.T) {
	t.Parallel()
	srv, pool := sessionServer(t, false)
	put := func(h map[string]string, edit func(d map[string]any)) (int, string) {
		var d map[string]any
		_, body := do(t, srv, "GET", "/api/settings/machine", h, "")
		json.Unmarshal([]byte(body), &d)
		edit(d)
		b, _ := json.Marshal(d)
		return do(t, srv, "PUT", "/api/settings/machine", h, string(b))
	}
	sec := func(d map[string]any, k string) map[string]any { return d[k].(map[string]any) }
	stored := func() int { return count(t, pool, `SELECT count(*) FROM changes WHERE tbl = 'settings'`) }

	// The keys of 20.3 and of the plan's D15, and those the security reviews of M17 P4 and of M18 added.
	want := []string{"updates.auto_install", "updates.install_after_hours", "updates.install_test", "updates.source_repo", "updates.publish",
		"notifications.mail_to", "network.mode", "network.model_only_hosts", "sandbox.sources", "sessions.days", "sessions.fresh_minutes",
		"terminal.edit_mode", "hosting.default_access", "notifications.mail_smtp", "notifications.chat_webhook", "updates.backup_copy_to",
		"github.visibility", "improvements.approval", "agents.checks_use_another_family",
		"notifications.needs_you", "notifications.news"}
	if got := slices.Sorted(slices.Values(httpapi.FreshKeys)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("the fresh keys: %v", got)
	}
	old := signedInAt(stale, "")
	for _, k := range httpapi.FreshKeys {
		sect, key, _ := strings.Cut(k, ".")
		code, body := put(old, func(d map[string]any) {
			s := sec(d, sect)
			switch v := s[key].(type) {
			case float64:
				s[key] = v - 1
			case bool:
				s[key] = !v
			case []any:
				s[key] = append(v, "example.org")
			default:
				// Another word than the one set.
				for _, w := range map[string][]string{"updates.auto_install": {"unprotected", "never"}, "updates.install_test": {"local", "off"},
					"updates.publish": {"github", "off"}, "network.mode": {"lan", "internet"}, "hosting.default_access": {"public", "private"},
					"notifications.mail_to": {"other@example.com"}, "updates.source_repo": {"kesokaj/other"},
					"notifications.mail_smtp": {"smtp.example.org:587"}, "notifications.chat_webhook": {"https://chat.example.org/hook"},
					"updates.backup_copy_to": {"/mnt/elsewhere"}, "github.visibility": {"public", "private"}, "improvements.approval": {"operator", "auto"},
					"notifications.news": {"off", "digest"}}[k] {
					if s[key] != w {
						s[key] = w
						break
					}
				}
			}
			sec(d, "disk")["clean_at"] = 80
		})
		if code != 401 || reason(body) != httpapi.ReasonFresh || !strings.Contains(body, `"keys":["`+k+`"]`) {
			t.Errorf("%s, stale: %d %s", k, code, body)
		}
	}
	if n := stored(); n != 0 {
		t.Fatalf("a refused change stored %d", n)
	}
	if code, body := put(old, func(d map[string]any) { sec(d, "disk")["clean_at"] = 80 }); code != 200 {
		t.Fatalf("another key, stale: %d %s", code, body)
	}
	if code, body := put(signedInAt(time.Minute, ""), func(d map[string]any) { sec(d, "updates")["install_after_hours"] = 12 }); code != 200 {
		t.Fatalf("a fresh change: %d %s", code, body)
	}
	var seq int64
	pool.QueryRow(context.Background(), `SELECT max(seq) FROM changes WHERE tbl = 'settings'`).Scan(&seq)
	for _, key := range []string{"updates.install_after_hours", "updates"} {
		code, body := do(t, srv, "POST", "/api/settings/machine/putback", old, `{"seq": `+strconv.FormatInt(seq, 10)+`, "key": "`+key+`"}`)
		if code != 401 || reason(body) != httpapi.ReasonFresh {
			t.Fatalf("put back %s, stale: %d %s", key, code, body)
		}
	}
	if code, body := do(t, srv, "POST", "/api/settings/machine/putback", signedInAt(time.Minute, ""), `{"seq": `+strconv.FormatInt(seq, 10)+`, "key": "updates"}`); code != 200 {
		t.Fatalf("put back, fresh: %d %s", code, body)
	}
}

// A project's network mode that becomes or leaves LAN, as it applies with the machine's mode, and
// hosting made public, need a fresh sign-in, through the project's settings, its Put back, and the
// Network card's PUT (D15).
func TestAProjectsLANAndPublicNeedAFreshSignIn(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	old, fresh := signedInAt(stale, ""), signedInAt(time.Minute, "")
	network := func(h map[string]string, mode string) (int, string) {
		return do(t, bs.srv, "PUT", "/api/projects/shop/network", h, `{"mode": "`+mode+`"}`)
	}
	if code, body := network(old, "lan"); code != 401 || reason(body) != httpapi.ReasonFresh || !strings.Contains(body, "sandbox.network") {
		t.Fatalf("to lan, stale: %d %s", code, body)
	}
	if code, body := network(old, "internet"); code != 200 {
		t.Fatalf("to internet, stale: %d %s", code, body)
	}
	if code, body := network(fresh, "lan"); code != 200 {
		t.Fatalf("to lan, fresh: %d %s", code, body)
	}
	if code, body := network(old, "model_only"); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("from lan, stale: %d %s", code, body)
	}
	if code, body := network(fresh, "inherit"); code != 200 {
		t.Fatalf("to inherit, fresh: %d %s", code, body)
	}
	settingsPut := func(h map[string]string, edit func(d map[string]any)) (int, string) {
		var d map[string]any
		_, body := do(t, bs.srv, "GET", "/api/projects/shop/settings", h, "")
		json.Unmarshal([]byte(body), &d)
		edit(d)
		b, _ := json.Marshal(d)
		return do(t, bs.srv, "PUT", "/api/projects/shop/settings", h, string(b))
	}
	sec := func(d map[string]any, k string) map[string]any { return d[k].(map[string]any) }
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "sandbox")["check_network"] = "lan" }); code != 401 || !strings.Contains(body, "sandbox.check_network") {
		t.Fatalf("checks to lan, stale: %d %s", code, body)
	}
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["access"] = "public" }); code != 401 || !strings.Contains(body, "hosting.access") {
		t.Fatalf("public, stale: %d %s", code, body)
	}
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["access"] = "google" }); code != 401 || !strings.Contains(body, "hosting.access") {
		t.Fatalf("google, stale: %d %s", code, body)
	}
	if code, body := settingsPut(fresh, func(d map[string]any) { sec(d, "hosting")["access"] = "google" }); code != 200 {
		t.Fatalf("google, fresh: %d %s", code, body)
	}
	// Checks on the builder's family need a fresh sign-in; the rule back on needs none (rule 15).
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "agents")["checks_use_another_family"] = false }); code != 401 || !strings.Contains(body, "agents.checks_use_another_family") {
		t.Fatalf("checks on the same family, stale: %d %s", code, body)
	}
	if code, body := settingsPut(fresh, func(d map[string]any) { sec(d, "agents")["checks_use_another_family"] = false }); code != 200 {
		t.Fatalf("checks on the same family, fresh: %d %s", code, body)
	}
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "agents")["checks_use_another_family"] = true }); code != 200 {
		t.Fatalf("the rule back on, stale: %d %s", code, body)
	}
	// Fewer who may reach it needs none.
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["access"] = "listed" }); code != 200 {
		t.Fatalf("listed from google, stale: %d %s", code, body)
	}
	// Listed, an address added is one more who may reach it; one taken away is not (round 2).
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["allowed"] = []any{"a@example.com"} }); code != 401 || !strings.Contains(body, "hosting.allowed") {
		t.Fatalf("an address added, stale: %d %s", code, body)
	}
	if code, body := settingsPut(fresh, func(d map[string]any) { sec(d, "hosting")["allowed"] = []any{"a@example.com", "b@example.com"} }); code != 200 {
		t.Fatalf("addresses added, fresh: %d %s", code, body)
	}
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["allowed"] = []any{"b@example.com"} }); code != 200 {
		t.Fatalf("an address taken away, stale: %d %s", code, body)
	}
	// The internet from inside a hosted project: off narrows it, on widens it (plan M19 D12).
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["internet"] = false }); code != 200 {
		t.Fatalf("the internet off, stale: %d %s", code, body)
	}
	if code, body := settingsPut(old, func(d map[string]any) { sec(d, "hosting")["internet"] = true }); code != 401 || !strings.Contains(body, "hosting.internet") {
		t.Fatalf("the internet on, stale: %d %s", code, body)
	}
	if code, body := settingsPut(fresh, func(d map[string]any) { sec(d, "hosting")["internet"] = true }); code != 200 {
		t.Fatalf("the internet on, fresh: %d %s", code, body)
	}
	// A ticket's own mode is guarded the same way (review of M17 P4, finding 1).
	bs.call(t, "POST", "/api/projects/shop/tasks", `{"title": "A ticket", "why": "w", "what": "w", "goal": "g"}`, 201, nil)
	ticket := func(h map[string]string, body string) (int, string) {
		return do(t, bs.srv, "PATCH", "/api/projects/shop/tasks/SF-1", h, body)
	}
	if code, body := ticket(old, `{"network": "lan"}`); code != 401 || reason(body) != httpapi.ReasonFresh || !strings.Contains(body, `"keys":["network"]`) {
		t.Fatalf("a ticket to lan, stale: %d %s", code, body)
	}
	if code, body := ticket(old, `{"network": "model_only", "title": "Renamed"}`); code != 200 {
		t.Fatalf("a ticket to model only, stale: %d %s", code, body)
	}
	if code, body := ticket(fresh, `{"network": "lan"}`); code != 200 {
		t.Fatalf("a ticket to lan, fresh: %d %s", code, body)
	}
	if code, body := ticket(old, `{"network": "inherit"}`); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("a ticket from lan, stale: %d %s", code, body)
	}
	if code, body := ticket(fresh, `{"network": "inherit"}`); code != 200 {
		t.Fatalf("a ticket from lan, fresh: %d %s", code, body)
	}
	// The machine's mode is LAN: a project that inherits it becomes LAN, which is guarded too.
	var m map[string]any
	_, body := do(t, bs.srv, "GET", "/api/settings/machine", fresh, "")
	json.Unmarshal([]byte(body), &m)
	sec(m, "network")["mode"] = "lan"
	b, _ := json.Marshal(m)
	if code, body := do(t, bs.srv, "PUT", "/api/settings/machine", fresh, string(b)); code != 200 {
		t.Fatalf("the machine to lan: %d %s", code, body)
	}
	if code, body := network(fresh, "internet"); code != 200 {
		t.Fatalf("to internet, fresh: %d %s", code, body)
	}
	if code, body := network(old, "inherit"); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("to inherit of a LAN machine, stale: %d %s", code, body)
	}
	// Put back of that last change is inherit again, LAN on this machine: stale, refused.
	var seq int64
	bs.b.Pool.QueryRow(context.Background(), `SELECT max(seq) FROM changes WHERE tbl = 'settings'`).Scan(&seq)
	putBack := `{"seq": ` + strconv.FormatInt(seq, 10) + `, "key": "sandbox.network"}`
	if code, body := do(t, bs.srv, "POST", "/api/projects/shop/settings/putback", old, putBack); code != 401 || reason(body) != httpapi.ReasonFresh {
		t.Fatalf("put back, stale: %d %s", code, body)
	}
	if code, body := do(t, bs.srv, "POST", "/api/projects/shop/settings/putback", fresh, putBack); code != 200 {
		t.Fatalf("put back, fresh: %d %s", code, body)
	}
}
