package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/httpapi"
)

// newHostingServer is the board's server with hosting, a vault, and an apps domain of its own;
// every log line is kept.
func newHostingServer(t *testing.T) (*boardServer, *syncBuffer, *credstore.Vault) {
	t.Helper()
	bs := newBoardServer(t)
	vault, err := credstore.NewVault(bs.b.Pool, bytes.Repeat([]byte{9}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bs.s.Log = log
	bs.s.Hosting.Store(&hosting.Service{Pool: bs.b.Pool, Vault: vault, AppsDomain: "crew-apps.example", CrewHost: host, Log: log})
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`, 201, nil)
	return bs, buf, vault
}

const hostedSecret = "s3cr3t-database-password"

// A hosted project's settings and secrets (19.3, R176, plan M19 D7): set as a whole behind a fresh
// sign-in, named in the hosting's answer and never shown, refused with a bad name, a name of Crew's,
// PORT, or too many; none of their values is in an answer or a log line. A key file lost makes them
// "lost": they are entered again (21).
func TestAHostedProjectsValues(t *testing.T) {
	t.Parallel()
	bs, buf, _ := newHostingServer(t)
	put := func(h map[string]string, body string) (int, string) {
		return do(t, bs.srv, "PUT", "/api/projects/shop/hosting/settings", h, body)
	}
	fresh := signedInAt(time.Minute, "")
	body := `{"values": {"DATABASE_PASSWORD": "` + hostedSecret + `", "MODE": "production"}}`
	if code, b := put(signedInAt(stale, ""), body); code != 401 || reason(b) != httpapi.ReasonFresh {
		t.Fatalf("stale: %d %s", code, b)
	}
	for _, bad := range []string{`{"values": {"lower": "x"}}`, `{"values": {"CREW_TOKEN": "x"}}`, `{"values": {"PORT": "9000"}}`,
		`{"values": {"A": "` + strings.Repeat("x", 4097) + `"}}`, `{"values": {"1A": "x"}}`} {
		if code, b := put(fresh, bad); code != 400 {
			t.Fatalf("%.60s: %d %s", bad, code, b)
		}
	}
	many := map[string]map[string]string{"values": {}}
	for i := 0; i < 51; i++ {
		many["values"][string(rune('A'+i/26))+string(rune('A'+i%26))] = "x"
	}
	mb, _ := json.Marshal(many)
	if code, b := put(fresh, string(mb)); code != 400 {
		t.Fatalf("51 values: %d %s", code, b)
	}
	code, b := put(fresh, body)
	if code != 200 || strings.Contains(b, hostedSecret) {
		t.Fatalf("fresh: %d %s", code, b)
	}
	var v struct {
		Values     []string `json:"values"`
		ValuesLost bool     `json:"valuesLost"`
		Refused    string   `json:"refused"`
	}
	code, b = do(t, bs.srv, "GET", "/api/projects/shop/hosting", signedIn, "")
	if code != 200 || strings.Contains(b, hostedSecret) {
		t.Fatalf("GET: %d %s", code, b)
	}
	json.Unmarshal([]byte(b), &v)
	if strings.Join(v.Values, ",") != "DATABASE_PASSWORD,MODE" || v.ValuesLost || v.Refused != "" {
		t.Fatalf("the hosting: %s", b)
	}
	// The whole set: a PUT without one takes it away; an empty set leaves none.
	if code, b := put(fresh, `{"values": {"MODE": "staging"}}`); code != 200 {
		t.Fatalf("one less: %d %s", code, b)
	}
	_, b = do(t, bs.srv, "GET", "/api/projects/shop/hosting", signedIn, "")
	json.Unmarshal([]byte(b), &v)
	if strings.Join(v.Values, ",") != "MODE" {
		t.Fatalf("after one less: %s", b)
	}
	if strings.Contains(buf.String(), hostedSecret) || strings.Contains(buf.String(), "staging") {
		t.Fatal("a value is in a log line")
	}
	// Sealed with another key file: lost, and entered again.
	other, err := credstore.NewVault(bs.b.Pool, bytes.Repeat([]byte{8}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	bs.s.Hosting.Store(&hosting.Service{Pool: bs.b.Pool, Vault: other, AppsDomain: "crew-apps.example", CrewHost: host, Log: slog.Default()})
	_, b = do(t, bs.srv, "GET", "/api/projects/shop/hosting", signedIn, "")
	json.Unmarshal([]byte(b), &v)
	if !v.ValuesLost || len(v.Values) != 0 {
		t.Fatalf("with another key file: %s", b)
	}
}

// With no apps domain of its own, the hosting says why it cannot host (plan M19 D23).
func TestTheHostingSaysWhyItCannotHost(t *testing.T) {
	t.Parallel()
	bs, _, vault := newHostingServer(t)
	bs.s.Hosting.Store(&hosting.Service{Pool: bs.b.Pool, Vault: vault, AppsDomain: "", CrewHost: host, Log: slog.Default()})
	var v struct {
		Refused string `json:"refused"`
	}
	code, b := do(t, bs.srv, "GET", "/api/projects/shop/hosting", signedIn, "")
	json.Unmarshal([]byte(b), &v)
	if code != 200 || !strings.Contains(v.Refused, "CREW_APPS_DOMAIN") {
		t.Fatalf("no apps domain: %d %s", code, b)
	}
}

// The ingress's door (19.4, plan M19 D5): the hosted routes, to the ingress alone, by the shared
// token and with no identity header, never from a sandbox's bridge; anything else is not found.
func TestTheIngressDoor(t *testing.T) {
	t.Parallel()
	bs, _, _ := newHostingServer(t)
	ctx := t.Context()
	var p string
	if err := bs.b.Pool.QueryRow(ctx, `SELECT id FROM projects WHERE name = 'shop'`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	var d int64
	if err := bs.b.Pool.QueryRow(ctx, `INSERT INTO deploys (project, number, commit, why, asked_by, state) VALUES ($1, 1, $2, 'asked', 'operator', 'live') RETURNING id`,
		p, strings.Repeat("a", 40)).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.b.Pool.Exec(ctx, `INSERT INTO hosting (project, state, live_deploy, sandbox, address) VALUES ($1, 'live', $2, 'host-x-1', '172.20.7.252')`, p, d); err != nil {
		t.Fatal(err)
	}
	fresh := signedInAt(time.Minute, "")
	set := func(body string) {
		if code, b := do(t, bs.srv, "PUT", "/api/projects/shop/settings", fresh, body); code != 200 {
			t.Fatalf("settings: %d %s", code, b)
		}
	}
	var doc map[string]any
	_, b := do(t, bs.srv, "GET", "/api/projects/shop/settings", signedIn, "")
	json.Unmarshal([]byte(b), &doc)
	h := doc["hosting"].(map[string]any)
	h["access"], h["allowed"], h["port"], h["public_rps"] = "listed", []any{"a@example.com"}, 3000, 7
	nb, _ := json.Marshal(doc)
	set(string(nb))
	door := func(headers map[string]string) (int, string) {
		return do(t, bs.srv, "GET", "/ingress/routes", headers, "")
	}
	code, b := door(map[string]string{"X-Crew-Ingress": token})
	want := `{"routes":[{"project":"shop","host":"shop","upstream":"172.20.7.252:3000","access":"listed","emails":["a@example.com"],"rps":7}]}`
	if code != 200 || strings.TrimSpace(b) != want {
		t.Fatalf("the door: %d %s", code, b)
	}
	for name, h := range map[string]map[string]string{
		"no token":        {},
		"another token":   {"X-Crew-Ingress": "not-the-token"},
		"through a route": {"X-Crew-Ingress": token, "X-Caddy-Email": admin},
	} {
		if code, b := door(h); code != 404 {
			t.Fatalf("%s: %d %s", name, code, b)
		}
	}
	// A hosted project that does not serve is no route; neither is one this machine cannot host.
	if _, err := bs.b.Pool.Exec(ctx, `UPDATE hosting SET state = 'stopped'`); err != nil {
		t.Fatal(err)
	}
	if _, b := door(map[string]string{"X-Crew-Ingress": token}); strings.TrimSpace(b) != `{"routes":[]}` {
		t.Fatalf("stopped: %s", b)
	}
	if _, err := bs.b.Pool.Exec(ctx, `UPDATE hosting SET state = 'failing'`); err != nil {
		t.Fatal(err)
	}
	vault := bs.s.Hosting.Load().Vault
	bs.s.Hosting.Store(&hosting.Service{Pool: bs.b.Pool, Vault: vault, AppsDomain: "", CrewHost: host, Log: slog.Default()})
	if _, b := door(map[string]string{"X-Crew-Ingress": token}); strings.TrimSpace(b) != `{"routes":[]}` {
		t.Fatalf("no apps domain: %s", b)
	}
	// From a sandbox's bridge the door is not there.
	bs.s.SandboxDoor = func(string) bool { return true }
	if code, _ := door(map[string]string{"X-Crew-Ingress": token}); code != 404 {
		t.Fatalf("from a sandbox: %d", code)
	}
}
