package ingress

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testConfig returns a GeneratorConfig with all domain fields populated
// for consistent testing. Apps go to <hostname>.apps.example.com.
func testConfig() GeneratorConfig {
	return GeneratorConfig{
		BaseDomain:    "example.com",
		AdminEmail:    "admin@example.com",
		DefaultRPS:    100,
		AuthDomain:    "auth.example.com",
		AppsDomain:    "apps.example.com",
		CloudflareDNS: true,
	}
}

func TestGenerateCaddyfile_NoApps(t *testing.T) {
	cfg := testConfig()
	result := GenerateCaddyfile(nil, cfg)

	// Should still have global options and HTTP redirect
	assertContains(t, result, "email admin@example.com")
	assertContains(t, result, "acme_dns cloudflare")
	assertContains(t, result, "http:// {")
	assertContains(t, result, "redir https://{host}{uri} permanent")

	// Prometheus metrics always enabled
	assertContains(t, result, "metrics {")
	assertContains(t, result, "per_host")

	// Should NOT have security block or auth portal (no apps need OAuth)
	assertNotContains(t, result, "security {")
	assertNotContains(t, result, "authenticate with myportal")
}

func TestGenerateCaddyfile_NoCloudflare(t *testing.T) {
	cfg := testConfig()
	cfg.CloudflareDNS = false
	result := GenerateCaddyfile(nil, cfg)

	// Without a Cloudflare token Caddy falls back to HTTP-01 challenges.
	assertNotContains(t, result, "acme_dns")
	// Everything else is still present.
	assertContains(t, result, "email admin@example.com")
	assertContains(t, result, "auto_https disable_redirects")
}

func TestGenerateCaddyfile_PublicApp(t *testing.T) {
	apps := []App{
		{
			Name:        "landing",
			Hostname:    "landing",
			ContainerIP: "172.18.0.2",
			Port:        80,
			Mode:        ModePublic,
			RPS:         100,
		},
	}
	cfg := testConfig()
	result := GenerateCaddyfile(apps, cfg)

	// Public app should have reverse_proxy and rate_limit, but NO authorize
	assertContains(t, result, "landing.apps.example.com {")
	assertContains(t, result, "reverse_proxy 172.18.0.2:80")
	assertContains(t, result, "zone landing_rl")
	assertContains(t, result, "events 100")
	assertNotContains(t, result, "authorize with")
	assertContains(t, result, `X-Caddy-Role "public"`)
	// Public apps must STRIP client-supplied identity/ingress headers, and
	// must never SET a real identity (only the public role).
	assertContains(t, result, "request_header -X-Caddy-Email")
	assertContains(t, result, "request_header -X-Crew-Ingress")
	assertNotContains(t, result, `X-Caddy-Email "{`)
	// And no portal cookie, of any deployment, reaches a public app.
	assertContains(t, result, `request_header Cookie "`+PortalCookieStrip+`" ""`)

	// No auth portal needed (all apps are public)
	assertNotContains(t, result, "security {")
}

func TestGenerateCaddyfile_AuthenticatedApp(t *testing.T) {
	apps := []App{
		{
			Name:        "wiki",
			Hostname:    "wiki",
			ContainerIP: "172.18.0.3",
			Port:        3000,
			Mode:        ModeAuthenticated,
			RPS:         100,
		},
	}
	cfg := testConfig()
	result := GenerateCaddyfile(apps, cfg)

	// Authenticated app should have OAuth + authorize, but NO X-Caddy-Role
	assertContains(t, result, "security {")
	assertContains(t, result, "authentication portal myportal")
	assertContains(t, result, "auth.example.com {")
	assertContains(t, result, "wiki.apps.example.com {")
	assertContains(t, result, "authorize with wiki_policy")
	assertContains(t, result, "allow roles authp/user authp/guest")
	assertContains(t, result, "X-Caddy-Email")
	assertContains(t, result, `X-Caddy-Role "user"`)
}

func TestGenerateCaddyfile_RoleGatedApp(t *testing.T) {
	apps := []App{
		{
			Name:        "dashboard",
			Hostname:    "dashboard",
			ContainerIP: "172.18.0.4",
			Port:        8080,
			Mode:        ModeRoleGated,
			Admins:      []string{"alice@example.com"},
			Viewers:     []string{"bob@example.com"},
			RPS:         50,
		},
	}
	cfg := testConfig()
	cfg.SuperAdmins = []string{"boss@example.com"}
	result := GenerateCaddyfile(apps, cfg)

	// Should have transforms for superadmin + app admins + app viewers
	assertContains(t, result, "match email boss@example.com")
	assertContains(t, result, "action add role app/dashboard/superadmin")
	assertContains(t, result, "match email alice@example.com")
	assertContains(t, result, "action add role app/dashboard/admin")
	assertContains(t, result, "match email bob@example.com")
	assertContains(t, result, "action add role app/dashboard/viewer")

	// Authorization policy
	assertContains(t, result, "authorization policy dashboard_policy")
	assertContains(t, result, "allow roles app/dashboard/superadmin app/dashboard/admin app/dashboard/viewer")

	// Site block
	assertContains(t, result, "dashboard.apps.example.com {")
	assertContains(t, result, "authorize with dashboard_policy")
	assertContains(t, result, "events 50")
	assertContains(t, result, "X-Caddy-Role")
	assertContains(t, result, "X-Caddy-Email")
	assertContains(t, result, "reverse_proxy 172.18.0.4:8080")
}

func TestGenerateCaddyfile_AllThreeModes(t *testing.T) {
	apps := []App{
		{
			Name: "public-app", Hostname: "public-app",
			ContainerIP: "172.18.0.2", Port: 80,
			Mode: ModePublic, RPS: 100,
		},
		{
			Name: "wiki", Hostname: "wiki",
			ContainerIP: "172.18.0.3", Port: 3000,
			Mode: ModeAuthenticated, RPS: 100,
		},
		{
			Name: "dashboard", Hostname: "dashboard",
			ContainerIP: "172.18.0.4", Port: 8080,
			Mode:   ModeRoleGated,
			Admins: []string{"alice@example.com"}, Viewers: []string{"bob@example.com"},
			RPS: 50,
		},
	}
	cfg := testConfig()
	cfg.SuperAdmins = []string{"boss@example.com"}
	result := GenerateCaddyfile(apps, cfg)

	// All three site blocks present
	assertContains(t, result, "public-app.apps.example.com {")
	assertContains(t, result, "wiki.apps.example.com {")
	assertContains(t, result, "dashboard.apps.example.com {")

	// Auth portal present (non-public apps exist)
	assertContains(t, result, "auth.example.com {")

	// HTTP redirect always present
	assertContains(t, result, "http:// {")
}

func TestGenerateCaddyfile_SuperAdminDedup(t *testing.T) {
	apps := []App{
		{
			Name: "myapp", Hostname: "myapp",
			ContainerIP: "172.18.0.2", Port: 80,
			Mode:        ModeRoleGated,
			SuperAdmins: []string{"boss@example.com"}, // Same as SUPERADMINS
			RPS:         100,
		},
	}
	cfg := testConfig()
	cfg.SuperAdmins = []string{"boss@example.com"}
	result := GenerateCaddyfile(apps, cfg)

	// boss@example.com should appear only once (not duplicated by env + label)
	count := strings.Count(result, "match email boss@example.com")
	if count != 1 {
		t.Errorf("expected 1 transform for boss@example.com, got %d", count)
	}
}

func TestGenerateCaddyfile_WithBaseFile(t *testing.T) {
	// Create a temp Caddyfile.base
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "Caddyfile.base")
	baseContent := "example.com {\n    redir https://www.example.com{uri} permanent\n}\n"
	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("writing temp base file: %v", err)
	}

	cfg := testConfig()
	cfg.CaddyfileBasePath = basePath
	result := GenerateCaddyfile(nil, cfg)

	// Base content should be prepended
	assertContains(t, result, "FROM Caddyfile.base")
	assertContains(t, result, "example.com {")
	assertContains(t, result, "redir https://www.example.com{uri} permanent")

	// Generated marker should follow
	assertContains(t, result, "GENERATED by caddy-ingress")
}

func TestGenerateCaddyfile_NoBaseFile(t *testing.T) {
	cfg := testConfig()
	cfg.CaddyfileBasePath = "/nonexistent/path"
	result := GenerateCaddyfile(nil, cfg)

	// Should NOT have the base file marker
	assertNotContains(t, result, "FROM Caddyfile.base")
	// Should still have generated marker
	assertContains(t, result, "GENERATED by caddy-ingress")
}

func TestGenerateCaddyfile_DeterministicOrder(t *testing.T) {
	apps := []App{
		{Name: "zzz", Hostname: "zzz", ContainerIP: "172.18.0.2", Port: 80, Mode: ModePublic, RPS: 100},
		{Name: "aaa", Hostname: "aaa", ContainerIP: "172.18.0.3", Port: 80, Mode: ModePublic, RPS: 100},
		{Name: "mmm", Hostname: "mmm", ContainerIP: "172.18.0.4", Port: 80, Mode: ModePublic, RPS: 100},
	}
	cfg := testConfig()

	result1 := GenerateCaddyfile(apps, cfg)
	result2 := GenerateCaddyfile(apps, cfg)

	if result1 != result2 {
		t.Error("expected deterministic output, but got different results")
	}

	// Should be sorted: aaa, mmm, zzz
	aaaIdx := strings.Index(result1, "aaa.apps.example.com")
	mmmIdx := strings.Index(result1, "mmm.apps.example.com")
	zzzIdx := strings.Index(result1, "zzz.apps.example.com")
	if aaaIdx > mmmIdx || mmmIdx > zzzIdx {
		t.Error("expected alphabetical order: aaa, mmm, zzz")
	}
}

func TestGenerateCaddyfile_HTTPSRedirectAlwaysPresent(t *testing.T) {
	cfg := testConfig()

	// Even with no apps, HTTP redirect should be present
	result := GenerateCaddyfile(nil, cfg)
	assertContains(t, result, "http:// {\n    redir https://{host}{uri} permanent\n}")
}

func TestGenerateCaddyfile_SuperAdminsOnAuthenticatedApps(t *testing.T) {
	apps := []App{
		{
			Name: "wiki", Hostname: "wiki",
			ContainerIP: "172.18.0.2", Port: 80,
			Mode: ModeAuthenticated, RPS: 100,
		},
	}
	cfg := testConfig()
	cfg.SuperAdmins = []string{"boss@example.com"}
	result := GenerateCaddyfile(apps, cfg)

	// Superadmin should get a role transform even on authenticated apps
	assertContains(t, result, "match email boss@example.com")
	assertContains(t, result, "action add role app/wiki/superadmin")
	// Policy should allow both superadmin role and authp/user
	assertContains(t, result, "allow roles app/wiki/superadmin authp/user authp/guest")
}

// --- helpers ---

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q, but it didn't.\nFull output:\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected output NOT to contain %q, but it did.\nFull output:\n%s", needle, haystack)
	}
}

func TestIngressTokenHeaderInjected(t *testing.T) {
	apps := []App{{Hostname: "app", ContainerIP: "172.18.0.4", Port: 8080, Mode: ModeAuthenticated, Sessions: true}}
	cfg := GeneratorConfig{BaseDomain: "crew.xyz", AdminEmail: "a@b.c", IngressToken: "s3cr3t-tok"}
	result := GenerateCaddyfile(apps, cfg)
	assertContains(t, result, `request_header X-Crew-Ingress "s3cr3t-tok"`)
	// The client-supplied header is stripped even when a token IS set (the
	// strip precedes the inject, so a forged value can never survive).
	assertContains(t, result, "request_header -X-Crew-Ingress")
	// INJECTION absent when unset — but the defensive strip must remain, so a
	// token-less deploy still can't relay a forged header upstream.
	cfg.IngressToken = ""
	out := GenerateCaddyfile(apps, cfg)
	if strings.Contains(out, `request_header X-Crew-Ingress`) {
		t.Fatal("token header must not be injected when IngressToken is empty")
	}
	assertContains(t, out, "request_header -X-Crew-Ingress")
}

// A sign-in lasts 30 days on a device (design 20.3): the token the portal
// issues, and the cookie that holds it, which without a lifetime of its own
// ends when the browser closes. A missing directive means the plugin's 900 s
// default, or a session cookie, is back.
func TestGenerateCaddyfile_SessionLifetimeIsExplicit(t *testing.T) {
	apps := []App{{Name: "wiki", Hostname: "wiki", ContainerIP: "172.18.0.3", Port: 3000, Mode: ModeAuthenticated, RPS: 100}}
	result := GenerateCaddyfile(apps, testConfig())
	assertContains(t, result, "authentication portal myportal")
	assertContains(t, result, "crypto default token lifetime 2592000\n")
	assertContains(t, result, "cookie lifetime 2592000\n")
	if SessionLifetimeSeconds != 30*24*60*60 {
		t.Errorf("SessionLifetimeSeconds = %d, want 30 days (2592000)", SessionLifetimeSeconds)
	}
}

// The portal's redirect trust is label-bounded (a bare suffix would trust
// "evil<base>"), the session cookie is SameSite=Lax, and a public app's block
// cuts the portal's cookies out of the header before proxying to the app (the
// app's own cookies survive).
func TestGenerateCaddyfile_PortalRedirectSuffixCookieAndCookieStrip(t *testing.T) {
	apps := []App{{Name: "crew", Hostname: "crew", ContainerIP: "172.18.0.2", Port: 8080, Mode: ModeAuthenticated, RPS: 100}}
	cfg := testConfig()
	result := GenerateCaddyfile(apps, cfg)
	assertContains(t, result, "trust login redirect uri domain suffix ."+cfg.BaseDomain+" path prefix /")
	assertNotContains(t, result, "domain suffix "+cfg.BaseDomain+" path")
	assertContains(t, result, "cookie samesite lax")
	// Caddy compiles that pattern with Go's regexp: it must compile here too,
	// and cut every portal cookie — this deployment's, the legacy unprefixed
	// one and another deployment's — out of a header that carries
	// others, which survive.
	re := regexp.MustCompile(PortalCookieStrip)
	own, foreign := AuthCookieName(cfg.BaseDomain), AuthCookieName("other.example.org")
	hdr := "a=1; " + own + "=tok.en; app_session=x; AUTHP_ACCESS_TOKEN=old; " + foreign + "=f; " +
		CookiePrefix(cfg.BaseDomain) + "_SESSION_ID=s; b=2"
	if got := re.ReplaceAllString(hdr, ""); got != "a=1; app_session=x; b=2" {
		t.Fatalf("cookie strip regex: %q", got)
	}
	if got := re.ReplaceAllString(own+"=tok", ""); got != "" {
		t.Fatalf("cookie strip regex (only cookie): %q", got)
	}
}

func TestServerRouteRefusesAHostThatIsNotPlain(t *testing.T) {
	for _, addr := range []string{"___:8080", "crew server:8080", "crew-server{}:8080", ":8080", "crew-server", "crew-server:0", "crew-server:x"} {
		if _, err := ServerRoute(testConfig(), addr); err == nil {
			t.Errorf("%q was accepted", addr)
		}
	}
}

// Two deployments never share a cookie name, or a browser signed in to one on
// a parent domain loops on the other's sign-in: the portal writes this
// deployment's prefix, and every authorization policy reads exactly its
// access-token cookie (the plugin's default list names only AUTHP_*).
func TestCookieNamesArePerDeployment(t *testing.T) {
	a, b := CookiePrefix("crew.xyz"), CookiePrefix("gcp.crew.xyz")
	if a == b || !regexp.MustCompile(`^AUTHP[0-9A-F]{8}$`).MatchString(a) {
		t.Fatalf("prefixes %q / %q", a, b)
	}
	if CookiePrefix("Crew.XYZ.") != a {
		t.Fatal("the prefix depends on the domain's case or trailing dot")
	}
	if !regexp.MustCompile(PortalCookieStrip).MatchString(AuthCookieName("crew.xyz") + "=x") {
		t.Fatal("the strip misses this deployment's cookie")
	}
	apps := []App{{Name: "crew", Hostname: "crew", ContainerIP: "172.18.0.2", Port: 8080, Mode: ModeAuthenticated, RPS: 100}}
	cfg := testConfig()
	result := GenerateCaddyfile(apps, cfg)
	assertContains(t, result, "set cookie name prefix "+CookiePrefix(cfg.BaseDomain)+"\n")
	if n, m := strings.Count(result, "authorization policy "), strings.Count(result, "set access_token cookie name "+AuthCookieName(cfg.BaseDomain)+"\n"); n == 0 || n != m {
		t.Fatalf("%d policies, %d read this deployment's cookie", n, m)
	}
}

// The ingress's token goes to Crew's own route alone: with it the server takes the admin's mail and
// a sign-in's time, so a hosted app that got it could call Crew's API as a fresh operator (security
// review of M17's Close). Every other app gets the client's copy stripped and none set.
func TestTheIngressTokenGoesOnlyToCrewsRoute(t *testing.T) {
	crew := App{Hostname: "crew", ContainerIP: "172.18.0.2", Port: 8080, Mode: ModeRoleGated, Admins: []string{"a@b.c"}, Sessions: true}
	for _, mode := range []AccessMode{ModeAuthenticated, ModeRoleGated} {
		app := App{Hostname: "app", ContainerIP: "172.18.0.4", Port: 8080, Mode: mode, Admins: []string{"a@b.c"}}
		out := GenerateCaddyfile([]App{crew, app}, GeneratorConfig{BaseDomain: "crew.xyz", AdminEmail: "a@b.c", IngressToken: "s3cr3t-tok"})
		if n := strings.Count(out, `request_header X-Crew-Ingress "s3cr3t-tok"`); n != 1 {
			t.Fatalf("%v: the token is set %d times, want once, for Crew's route:\n%s", mode, n, out)
		}
		rest := out[strings.Index(out, `request_header X-Crew-Ingress "s3cr3t-tok"`):]
		if !strings.HasPrefix(rest[strings.Index(rest, "reverse_proxy"):], "reverse_proxy 172.18.0.2:8080") {
			t.Fatalf("%v: the token is set on the app's route, not Crew's:\n%s", mode, out)
		}
		if n := strings.Count(out, "request_header -X-Crew-Ingress"); n < 2 {
			t.Fatalf("%v: the client's copy is not stripped on both routes (%d):\n%s", mode, n, out)
		}
	}
}
