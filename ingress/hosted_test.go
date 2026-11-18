package ingress

import (
	"os"
	"strings"
	"testing"
)

// hostedConfig is Crew's configuration with an apps domain of its own and the apps portal's key.
func hostedConfig() GeneratorConfig {
	cfg := crewConfig()
	cfg.AppsDomain = "apps.test"
	cfg.AppsKey = true
	cfg.JWTKey = "a-key-of-crew"
	return cfg
}

// hostedApps are Crew's route and one hosted project of each access choice (19.4).
func hostedApps(t *testing.T, cfg GeneratorConfig) []App {
	t.Helper()
	crew, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	return []App{crew,
		{Name: "shop", Hostname: "shop", ContainerIP: "172.20.7.252", Port: 8080, Mode: ModeRoleGated, Hosted: true, Emails: []string{"admin@example.com"}, RPS: 100},
		{Name: "club", Hostname: "club", ContainerIP: "172.20.8.252", Port: 8080, Mode: ModeRoleGated, Hosted: true, Emails: []string{"admin@example.com", "a@example.com"}, RPS: 100},
		{Name: "forum", Hostname: "forum", ContainerIP: "172.20.9.252", Port: 3000, Mode: ModeAuthenticated, Hosted: true, RPS: 100},
		{Name: "site", Hostname: "site", ContainerIP: "172.20.10.252", Port: 8080, Mode: ModePublic, Hosted: true, RPS: 20},
	}
}

// block returns the text from the line that starts with head to the next line that is "}" alone at
// the same depth: a site, or a policy.
func block(t *testing.T, out, head string) string {
	t.Helper()
	i := strings.Index(out, head)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", head, out)
	}
	indent := head[:len(head)-len(strings.TrimLeft(head, " \n"))]
	indent = strings.TrimLeft(indent, "\n")
	j := strings.Index(out[i+len(head):], "\n"+indent+"}\n")
	if j < 0 {
		t.Fatalf("%q does not end", head)
	}
	return out[i : i+len(head)+j+1]
}

// Hosted projects sign in at the apps domain's own portal, with its own cookie and key, which the
// Caddyfile names by its environment variable and never holds; who may open one is in its policy, by
// mail address, so a change holds at the next reload; a hosted app gets no ingress token, no portal
// cookie, and sets no cookie for the whole apps domain (19.4, R172, plan M19 D6, D26, D38).
func TestHostedProjectsUseTheAppsPortal(t *testing.T) {
	cfg := hostedConfig()
	out := GenerateCaddyfile(hostedApps(t, cfg), cfg)
	portal := block(t, out, "        authentication portal appsportal {")
	for _, want := range []string{"cookie domain apps.test\n", "set cookie name prefix " + CookiePrefix("apps.test") + "\n",
		"crypto key sign-verify {env." + EnvAppsJWTKey + "}\n", "trust login redirect uri domain suffix .apps.test path prefix /\n"} {
		assertContains(t, portal, want)
	}
	assertNotContains(t, portal, "transform user")
	assertContains(t, out, "\nauth.apps.test {\n    header Strict-Transport-Security \"max-age=31536000\"\n    authenticate with appsportal\n}\n")
	crewPortal := block(t, out, "        authentication portal myportal {")
	assertNotContains(t, crewPortal, "apps.test")
	for name, allow := range map[string]string{
		"shop":  "allow email admin@example.com\n",
		"club":  "allow email admin@example.com a@example.com\n",
		"forum": "allow roles authp/user authp/guest\n",
	} {
		p := block(t, out, "        authorization policy "+name+"_policy {")
		for _, want := range []string{"set auth url https://auth.apps.test/oauth2/google/\n", "set access_token cookie name " + AuthCookieName("apps.test") + "\n",
			"crypto key verify {env." + EnvAppsJWTKey + "}\n", allow} {
			assertContains(t, p, want)
		}
		assertNotContains(t, p, "inject header \"X-Crew")
	}
	assertNotContains(t, out, "site_policy")
	assertNotContains(t, out, "app/shop/")
	assertNotContains(t, out, "app/club/")
	for _, host := range []string{"shop", "club", "forum", "site"} {
		site := block(t, out, "\n"+host+".apps.test {")
		assertNotContains(t, site, "request_header X-Crew-Ingress \"")
		assertContains(t, site, "request_header -X-Crew-Ingress\n")
		assertContains(t, site, `request_header Cookie "`+PortalCookieStrip+`" ""`)
		assertContains(t, site, `header_down Set-Cookie "(?i);\s*domain=[^;]*" ""`)
	}
	assertContains(t, block(t, out, "\nshop.apps.test {"), "request_header X-Caddy-Role \"user\"\n")
	assertNotContains(t, out, cfg.JWTKey)
	if path := os.Getenv("CREW_CADDYFILE_HOSTED_OUT"); path != "" {
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Crew's own route refuses the ingress's door itself, so a request through the ingress never reaches
// it, signed in or not (plan M19 D5).
func TestCrewsRouteRefusesTheIngressDoor(t *testing.T) {
	out := serverCaddyfile(t, crewConfig())
	site := block(t, out, "\ncrew.example.com {")
	assertContains(t, site, "@crew_door path /ingress /ingress/*\n")
	assertContains(t, site, "handle @crew_door {\n        respond 404\n    }\n")
}

// With a DNS token, the hosted projects are one site of the wildcard, a handle for each, and any other
// name under the apps domain is not found (plan M19 D33).
func TestHostedProjectsShareTheWildcardSite(t *testing.T) {
	cfg := hostedConfig()
	cfg.CloudflareDNS = true
	out := GenerateCaddyfile(hostedApps(t, cfg), cfg)
	site := block(t, out, "\n*.apps.test {")
	for _, host := range []string{"shop", "club", "forum", "site"} {
		assertContains(t, site, "@"+host+"_host host "+host+".apps.test\n")
		assertContains(t, site, "handle @"+host+"_host {\n")
		assertNotContains(t, out, "\n"+host+".apps.test {")
	}
	if !strings.HasSuffix(site, "    handle {\n        respond 404\n    }\n") {
		t.Fatalf("the wildcard does not end in a 404 for another name:\n%s", site)
	}
	assertContains(t, out, "\nauth.apps.test {\n")
	assertContains(t, out, "\ncrew.example.com {\n")
	if path := os.Getenv("CREW_CADDYFILE_WILDCARD_OUT"); path != "" {
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Without the apps portal's key, a hosted route that needs a sign-in is not served at all, and Crew's
// route is as it is without hosting.
func TestNoAppsKeyServesNoSignedInHostedRoute(t *testing.T) {
	cfg := hostedConfig()
	cfg.AppsKey = false
	out := GenerateCaddyfile(hostedApps(t, cfg), cfg)
	assertNotContains(t, out, "appsportal")
	assertNotContains(t, out, "\nshop.apps.test {")
	assertNotContains(t, out, "forum_policy")
}

// A change one hosted project's page asks of another is refused before the sign-in: every hosted
// project is the same site, with one sign-in cookie (security review of M19). A public project has no
// sign-in to ride; Crew's own route keeps its own checks.
func TestAChangeFromAnotherSiteIsRefused(t *testing.T) {
	cfg := hostedConfig()
	out := GenerateCaddyfile(hostedApps(t, cfg), cfg)
	for _, host := range []string{"shop", "club", "forum"} {
		site := block(t, out, "\n"+host+".apps.test {")
		assertContains(t, site, "    @"+host+"_xsite {\n        not method GET HEAD OPTIONS\n        header_regexp Sec-Fetch-Site ^(same-site|cross-site)$\n    }\n")
		assertContains(t, site, "    handle @"+host+"_xsite {\n        respond 403\n    }\n")
		// A WebSocket from another site, and a change from another Origin by a browser that sends no
		// Sec-Fetch-Site (round 2).
		assertContains(t, site, "    @"+host+"_xws {\n        header Upgrade websocket\n        header_regexp Sec-Fetch-Site ^(same-site|cross-site)$\n    }\n")
		assertContains(t, site, "    handle @"+host+"_xws {\n        respond 403\n    }\n")
		assertContains(t, site, "    @"+host+"_xorigin {\n        not method GET HEAD OPTIONS\n        header !Sec-Fetch-Site\n        header Origin *\n        not header Origin https://"+host+".apps.test\n    }\n")
		assertContains(t, site, "    handle @"+host+"_xorigin {\n        respond 403\n    }\n")
		// A WebSocket from another Origin, whatever Sec-Fetch-Site says: a browser that sends none still
		// sends Origin on every handshake (security review of the release of M20).
		assertContains(t, site, "    @"+host+"_xwsorigin {\n        header Upgrade websocket\n        header Origin *\n        not header Origin https://"+host+".apps.test\n    }\n")
		assertContains(t, site, "    handle @"+host+"_xwsorigin {\n        respond 403\n    }\n")
		for _, m := range []string{"_xsite {", "_xws {", "_xorigin {", "_xwsorigin {"} {
			if strings.Index(site, m) > strings.Index(site, "authorize with") {
				t.Fatalf("%s: the refusal %s comes after the sign-in", host, m)
			}
		}
	}
	for _, m := range []string{"_xsite", "_xws", "_xorigin", "_xwsorigin"} {
		assertNotContains(t, block(t, out, "\nsite.apps.test {"), m)
	}
}
