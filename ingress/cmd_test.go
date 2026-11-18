package ingress

import (
	"io"
	"log/slog"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A test machine with certificates of its own needs no sign-in client (design 16.11); a real one does.
func TestLoadConfigWithoutASignInClient(t *testing.T) {
	t.Setenv(EnvBaseDomain, "crew.install.test")
	t.Setenv(EnvAdminEmail, "admin@example.com")
	t.Setenv(EnvGoogleClientID, "")
	t.Setenv(EnvGoogleClientSecret, "")

	t.Setenv(EnvTLS, TLSACME)
	if _, err := loadConfig(quiet()); err == nil {
		t.Fatal("real certificates without a sign-in client were accepted")
	}

	t.Setenv(EnvTLS, TLSInternal)
	cfg, err := loadConfig(quiet())
	if err != nil {
		t.Fatalf("a test machine without a sign-in client: %v", err)
	}
	if cfg.TLS != TLSInternal {
		t.Fatalf("TLS = %q", cfg.TLS)
	}
	app, err := ServerRoute(cfg, DefaultServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	out := GenerateCaddyfile([]App{app}, cfg)
	assertContains(t, out, "@crew_open path /healthz /mcp")
	assertContains(t, out, "authorize with crew_policy")
}

// The admin email and the names are written into the Caddyfile as they are: each must have its form.
func TestLoadConfigRefusesWhatWouldReachTheCaddyfile(t *testing.T) {
	for name, env := range map[string][2]string{
		"two addresses":         {EnvAdminEmail, "a@example.com,b@example.com"},
		"an address with junk":  {EnvAdminEmail, "a@example.com, }"},
		"a brace in the domain": {EnvBaseDomain, "crew.example.com}"},
		"a space in the domain": {EnvBaseDomain, "crew example.com"},
		"no dot":                {EnvAuthDomain, "auth"},
	} {
		t.Setenv(EnvBaseDomain, "crew.example.com")
		t.Setenv(EnvAdminEmail, "admin@example.com")
		t.Setenv(EnvAuthDomain, "")
		t.Setenv(EnvTLS, TLSInternal)
		t.Setenv(EnvGoogleClientID, "")
		t.Setenv(EnvGoogleClientSecret, "")
		t.Setenv(env[0], env[1])
		if _, err := loadConfig(quiet()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
