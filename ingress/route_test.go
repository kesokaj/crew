package ingress

import (
	"context"
	"os"
	"strings"
	"testing"
)

func serverCaddyfile(t *testing.T, cfg GeneratorConfig) string {
	t.Helper()
	app, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := StaticRoutes(app)(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("StaticRoutes: %v %v", apps, err)
	}
	return GenerateCaddyfile(apps, cfg)
}

func crewConfig() GeneratorConfig {
	return GeneratorConfig{
		BaseDomain: "crew.example.com", AdminEmail: "admin@example.com", DefaultRPS: 100,
		AuthDomain: "auth.crew.example.com", AppsDomain: "crew.example.com",
		IngressToken: "tok123", TLS: TLSACME, AdminListen: DefaultCaddyAdmin,
	}
}

// Crew's one route: its own address, to the server, for the admin email only.
// The image build adapts the same Caddyfile with the real plugins
// (ingress/Dockerfile), through CREW_CADDYFILE_OUT.
func TestServerRouteCaddyfile(t *testing.T) {
	cfg := crewConfig()
	out := serverCaddyfile(t, cfg)
	for _, want := range []string{
		"\ncrew.example.com {\n",
		"\nauth.crew.example.com {\n",
		"trust login redirect uri domain exact crew.example.com path prefix /",
		"match email admin@example.com\n                action add role app/crew/admin",
		"allow roles app/crew/admin\n",
		"authorize with crew_policy",
		"reverse_proxy crew-server-door:8080",
		`request_header X-Crew-Ingress "tok123"`,
		"\ncrew.example.com {\n    header Strict-Transport-Security \"max-age=31536000\"\n",
		"\nauth.crew.example.com {\n    header Strict-Transport-Security \"max-age=31536000\"\n",
		"trust logout redirect uri domain exact crew.example.com path prefix /\n",
		`inject header "X-Crew-Auth-Time" from iat` + "\n",
		`inject header "X-Crew-Session" from jti` + "\n",
	} {
		assertContains(t, out, want)
	}
	assertNotContains(t, out, "crew.crew.example.com")
	assertNotContains(t, out, "authp/user")
	if path := os.Getenv("CREW_CADDYFILE_OUT"); path != "" {
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The health check and the Crew MCP of the operator's own tools are served
// without the sign-in, and nothing they receive can vouch for a user: every
// identity and ingress header and the portal's cookies are taken out, and the
// ingress token is not set there. No other path is open.
func TestServerRouteOpenPathCarriesNoIdentity(t *testing.T) {
	out := serverCaddyfile(t, crewConfig())
	a := strings.Index(out, "handle @crew_open {")
	if a < 0 {
		t.Fatalf("no open path block\n%s", out)
	}
	open := out[a : a+strings.Index(out[a:], "\n    }\n")]
	assertContains(t, out, "@crew_open path /healthz /mcp\n")
	for _, h := range []string{HeaderEmail, HeaderUser, HeaderRole, HeaderSubject, HeaderIngress, HeaderAuthTime, HeaderSession,
		UpstreamHeaderEmail, UpstreamHeaderName, UpstreamHeaderRoles, UpstreamHeaderSub} {
		assertContains(t, open, "request_header -"+h+"\n")
	}
	assertContains(t, open, `request_header Cookie "`+PortalCookieStrip+`" ""`)
	assertNotContains(t, open, "authorize")
	assertNotContains(t, open, "tok123")
	// Everything else goes through the sign-in.
	assertContains(t, out[a:], "    handle {\n    route {\n")
}

func TestTLSModes(t *testing.T) {
	cfg := crewConfig()
	cfg.CloudflareDNS = true
	acme := serverCaddyfile(t, cfg)
	assertNotContains(t, acme, "local_certs")
	assertNotContains(t, acme, "acme_ca")
	assertContains(t, acme, "acme_dns cloudflare")

	cfg.TLS = TLSStaging
	assertContains(t, serverCaddyfile(t, cfg), "acme_ca "+StagingCA+"\n")

	cfg.TLS = TLSInternal
	internal := serverCaddyfile(t, cfg)
	assertContains(t, internal, "    local_certs\n")
	assertNotContains(t, internal, "acme_dns")
}

func TestServerRouteNeedsOneAdmin(t *testing.T) {
	for _, email := range []string{"", "a@example.com,b@example.com", "not mail"} {
		cfg := crewConfig()
		cfg.AdminEmail = email
		if _, err := ServerRoute(cfg, DefaultServerAddr); err == nil {
			t.Errorf("admin email %q was accepted", email)
		}
	}
}

// The sign-in's time and the device's id reach Crew's server only, and only
// from the token: every route takes a client's copies out, on Crew's route
// before the authorizer sets them, since the plugin sets a header only when
// its claim is there (design 20.3; the probe of M17 phase 4).
func TestTheSessionHeadersComeOnlyFromTheToken(t *testing.T) {
	cfg := crewConfig()
	crew, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	other := App{Name: "shop", Hostname: "shop", ContainerIP: "172.18.0.5", Port: 3000, Mode: ModeRoleGated, Admins: []string{"admin@example.com"}, RPS: 100}
	open := App{Name: "site", Hostname: "site", ContainerIP: "172.18.0.6", Port: 80, Mode: ModePublic, RPS: 100}
	out := GenerateCaddyfile([]App{crew, other, open}, cfg)

	policy := func(name string) string {
		a := strings.Index(out, "authorization policy "+name+"_policy {")
		if a < 0 {
			t.Fatalf("no policy %s\n%s", name, out)
		}
		return out[a : a+strings.Index(out[a:], "\n        }\n")]
	}
	for _, h := range []string{HeaderAuthTime, HeaderSession} {
		if !strings.Contains(policy("crew"), `inject header "`+h+`"`) {
			t.Errorf("Crew's policy does not inject %s", h)
		}
		if strings.Contains(policy("shop"), h) {
			t.Errorf("another app's policy injects %s", h)
		}
	}

	site := func(fqdn string) string {
		a := strings.Index(out, "\n"+fqdn+" {\n")
		if a < 0 {
			t.Fatalf("no site %s\n%s", fqdn, out)
		}
		return out[a : a+strings.Index(out[a:], "\n}\n")]
	}
	for _, fqdn := range []string{"crew.example.com", "shop.crew.example.com", "site.crew.example.com"} {
		block := site(fqdn)
		for _, h := range []string{HeaderAuthTime, HeaderSession} {
			strip := strings.Index(block, "request_header -"+h+"\n")
			if strip < 0 {
				t.Errorf("%s does not take a client's %s out", fqdn, h)
				continue
			}
			if az := strings.Index(block, "authorize with"); az >= 0 {
				// The gated route: the strip is before the authorizer, so what it
				// sets is what passes. The open paths strip in their own block.
				route := block[strings.Index(block, "    handle {\n"):]
				rs, ra := strings.Index(route, "request_header -"+h+"\n"), strings.Index(route, "authorize with")
				if rs < 0 || rs > ra {
					t.Errorf("%s takes %s out after the authorizer, or not in its route", fqdn, h)
				}
			}
		}
	}
}

// No upstream of a signed-in route gets the portal's cookies, once the authorizer has read them: the
// token lasts 30 days and is sent to every host under the base domain (security review of M17 P4).
func TestNoUpstreamGetsThePortalsCookies(t *testing.T) {
	cfg := crewConfig()
	crew, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	app := App{Name: "wiki", Hostname: "wiki", ContainerIP: "172.18.0.7", Port: 3000, Mode: ModeAuthenticated, RPS: 100}
	out := GenerateCaddyfile([]App{crew, app}, cfg)
	for _, fqdn := range []string{"crew.example.com", "wiki.crew.example.com"} {
		a := strings.Index(out, "\n"+fqdn+" {\n")
		block := out[a : a+strings.Index(out[a:], "\n}\n")]
		route := block[strings.Index(block, "    handle {\n"):]
		strip := strings.Index(route, `request_header Cookie "`+PortalCookieStrip+`" ""`)
		az, proxy := strings.Index(route, "authorize with"), strings.Index(route, "reverse_proxy")
		if strip < 0 || strip < az || strip > proxy {
			t.Errorf("%s: the portal's cookies are not taken out between the authorizer and the upstream\n%s", fqdn, route)
		}
	}
}
