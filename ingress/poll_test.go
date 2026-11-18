package ingress

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// routesServer is the server's door: it answers body to the ingress's token, and 404 to anything
// else, or 503 while down is set.
func routesServer(t *testing.T, body *atomic.Value, down *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/ingress/routes" || r.Header.Get(HeaderIngress) != "tok123" || r.Header.Get(HeaderEmail) != "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hostnames(apps []App) []string {
	var out []string
	for _, a := range apps {
		out = append(out, a.Hostname)
	}
	return out
}

// The ingress asks the server for the hosted routes (19.4): each access choice becomes its mode, the
// admin email is the ingress's own beside the listed ones, and a route the ingress would not write
// into a Caddyfile is left out. Until the first answer Crew's route is served alone; a failed poll
// after keeps the last table (plan M19 D33).
func TestHostedRoutesFromTheServer(t *testing.T) {
	cfg := hostedConfig()
	crew, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	var body atomic.Value
	var down atomic.Bool
	body.Store(`{"routes": [
		{"project": "shop", "host": "shop", "upstream": "172.20.7.252:8080", "access": "private", "emails": [], "rps": 20},
		{"project": "club", "host": "club", "upstream": "172.20.8.252:8080", "access": "listed", "emails": ["a@example.com"], "rps": 20},
		{"project": "forum", "host": "forum", "upstream": "172.20.9.252:3000", "access": "google", "emails": [], "rps": 20},
		{"project": "site", "host": "site", "upstream": "172.20.10.252:8080", "access": "public", "emails": [], "rps": 7},
		{"project": "crew", "host": "crew", "upstream": "172.20.11.252:8080", "access": "public", "emails": [], "rps": 7},
		{"project": "x", "host": "x}", "upstream": "172.20.12.252:8080", "access": "public", "emails": [], "rps": 7},
		{"project": "out", "host": "out", "upstream": "8.8.8.8:80", "access": "public", "emails": [], "rps": 7},
		{"project": "odd", "host": "odd", "upstream": "172.20.13.252:8080", "access": "everyone", "emails": [], "rps": 7},
		{"project": "bad", "host": "bad", "upstream": "172.20.14.252:8080", "access": "listed", "emails": ["not a mail }"], "rps": 7}
	]}`)
	down.Store(true)
	srv := routesServer(t, &body, &down)
	routes := HostedRoutes(crew, srv.URL+"/ingress/routes", "tok123", cfg, srv.Client(), quiet())
	ctx := context.Background()
	apps, err := routes(ctx)
	if err != nil || len(apps) != 1 || apps[0].Hostname != crew.Hostname {
		t.Fatalf("before the first answer: %v %v", hostnames(apps), err)
	}
	down.Store(false)
	apps, err = routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := hostnames(apps); !slices.Equal(got, []string{"crew", "shop", "club", "forum", "site"}) {
		t.Fatalf("the routes: %v", got)
	}
	byName := map[string]App{}
	for _, a := range apps {
		byName[a.Hostname] = a
	}
	for name, want := range map[string]App{
		"shop":  {Mode: ModeRoleGated, Emails: []string{"admin@example.com"}, ContainerIP: "172.20.7.252", Port: 8080, RPS: cfg.DefaultRPS},
		"club":  {Mode: ModeRoleGated, Emails: []string{"admin@example.com", "a@example.com"}, ContainerIP: "172.20.8.252", Port: 8080, RPS: cfg.DefaultRPS},
		"forum": {Mode: ModeAuthenticated, ContainerIP: "172.20.9.252", Port: 3000, RPS: cfg.DefaultRPS},
		"site":  {Mode: ModePublic, ContainerIP: "172.20.10.252", Port: 8080, RPS: 7},
	} {
		a := byName[name]
		if !a.Hosted || a.Sessions || a.FQDN != "" || a.Mode != want.Mode || !slices.Equal(a.Emails, want.Emails) ||
			a.ContainerIP != want.ContainerIP || a.Port != want.Port || a.RPS != want.RPS {
			t.Errorf("%s: %+v", name, a)
		}
	}
	down.Store(true)
	again, err := routes(ctx)
	if err != nil || len(again) != len(apps) {
		t.Fatalf("a failed poll after: %v %v", hostnames(again), err)
	}
}

// The watcher asks every tick and loads only a Caddyfile that changed (plan M19 D33).
func TestTheWatcherPollsAndLoadsOnlyAChange(t *testing.T) {
	var mu sync.Mutex
	var loads int
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		loads++
		mu.Unlock()
	}))
	defer admin.Close()
	var n atomic.Int32
	routes := func(context.Context) ([]App, error) {
		apps := []App{{Hostname: "crew", FQDN: "crew.example.com", ContainerIP: "crew-server", Port: 8080, Mode: ModePublic, RPS: 10}}
		if n.Add(1) >= 4 {
			apps = append(apps, App{Hostname: "shop", ContainerIP: "172.20.7.252", Port: 8080, Mode: ModePublic, RPS: 10, Hosted: true})
		}
		return apps, nil
	}
	w := NewWatcher(routes, hostedConfig(), quiet(), admin.Listener.Addr().String())
	w.tick = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if n.Load() < 8 || loads != 2 {
		t.Fatalf("%d polls, %d loads; want 8 or more polls and 2 loads", n.Load(), loads)
	}
}

// Hosting needs an apps domain of its own and Crew's key: the apps portal's key is derived from it
// with HKDF into the ingress's own environment; without either, nothing is hosted (R172, D6, D23).
func TestLoadConfigHostsOnlyWithAnAppsDomainAndAKey(t *testing.T) {
	base := func() {
		t.Setenv(EnvBaseDomain, "crew.example.com")
		t.Setenv(EnvAdminEmail, "admin@example.com")
		t.Setenv(EnvTLS, TLSInternal)
		t.Setenv(EnvGoogleClientID, "")
		t.Setenv(EnvGoogleClientSecret, "")
		t.Setenv(EnvJWTKey, "a-key-of-crew")
		t.Setenv(EnvAppsJWTKey, "")
		os.Unsetenv(EnvAppsJWTKey)
	}
	for name, apps := range map[string]string{"none": "", "Crew's own": "crew.example.com", "under Crew's": "apps.crew.example.com", "over Crew's": "example.com"} {
		base()
		t.Setenv(EnvAppsDomain, apps)
		cfg, err := loadConfig(quiet())
		if err != nil || cfg.Hosting || cfg.AppsKey || os.Getenv(EnvAppsJWTKey) != "" {
			t.Errorf("%s: hosting %v, key %v, %v", name, cfg.Hosting, cfg.AppsKey, err)
		}
	}
	base()
	t.Setenv(EnvAppsDomain, "crew-apps.example")
	t.Setenv(EnvJWTKey, "")
	if cfg, err := loadConfig(quiet()); err != nil || cfg.Hosting {
		t.Errorf("no key: hosting %v, %v", cfg.Hosting, err)
	}
	base()
	t.Setenv(EnvAppsDomain, "crew-apps.example")
	cfg, err := loadConfig(quiet())
	if err != nil || !cfg.Hosting || !cfg.AppsKey || cfg.AppsDomain != "crew-apps.example" {
		t.Fatalf("an apps domain of its own: %+v %v", cfg, err)
	}
	k, _ := hkdf.Key(sha256.New, []byte("a-key-of-crew"), nil, "crew apps portal", 32)
	if got := os.Getenv(EnvAppsJWTKey); got != base64.StdEncoding.EncodeToString(k) {
		t.Fatalf("the apps portal's key: %q", got)
	}
}
