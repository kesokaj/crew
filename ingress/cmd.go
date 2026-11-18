package ingress

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
)

// init registers the "caddy ingress" subcommand. Required by the Caddy CLI plugin API —
// this is an approved exception to the "no init()" project convention.
func init() {
	caddycmd.RegisterCommand(caddycmd.Command{
		Name:  "ingress",
		Short: "Run Caddy as Crew's ingress, with one fixed route to the server",
		Long: `Generates a Caddyfile with HTTPS, Google OAuth, rate limiting, and
role-based access control for Crew's one fixed route, its address to the
server, and loads it. Configuration comes from the environment only.`,
		Func: cmdIngress,
	})
}

func cmdIngress(flags caddycmd.Flags) (int, error) {
	caddy.TrapSignals()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := loadConfig(logger)
	if err != nil {
		logger.Error("configuration error", "error", err)
		return 1, err
	}
	server, err := ServerRoute(cfg, getEnvOrDefault(EnvServerAddr, DefaultServerAddr))
	if err != nil {
		logger.Error("configuration error", "error", err)
		return 1, err
	}

	// Start Caddy with minimal config (admin API only). The bind address, the
	// reload URL (watcher), and the emitted `origins` line must all agree, so
	// every one of them derives from the single loopback-validated value in cfg
	// — never from a second env read that could drift.
	adminAddr := cfg.AdminListen
	err = caddy.Run(&caddy.Config{
		Admin: &caddy.AdminConfig{
			Listen: "tcp/" + adminAddr,
		},
	})
	if err != nil {
		logger.Error("failed to start caddy", "error", err)
		return 1, err
	}

	// Ensure Cloudflare DNS records are correct. A test machine with
	// certificates of its own has no names to write.
	if cfToken := os.Getenv(EnvCloudflareToken); cfToken != "" && cfg.TLS != TLSInternal {
		if err := syncDNS(cfToken, cfg, logger); err != nil {
			logger.Warn("cloudflare dns sync failed (continuing without it)", "error", err)
		}
	} else {
		logger.Warn("CLOUDFLARE_API_TOKEN not set, skipping DNS sync")
	}

	// Start the watcher
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	routes := StaticRoutes(server)
	if cfg.Hosting {
		addr := getEnvOrDefault(EnvServerAddr, DefaultServerAddr)
		routes = HostedRoutes(server, "http://"+addr+"/ingress/routes", cfg.IngressToken, cfg, &http.Client{Timeout: 5 * time.Second}, logger)
	}
	watcher := NewWatcher(routes, cfg, logger, adminAddr)
	if err := watcher.Start(ctx); err != nil {
		logger.Error("watcher failed to start", "error", err)
		return 1, err
	}

	logger.Info("caddy ingress running",
		"domain", cfg.BaseDomain,
		"admin_addr", adminAddr,
	)

	// Block until signal — or until the watcher declares itself unrecoverable
	// (admin API gone), where exiting nonzero hands recovery to the container
	// restart policy: a fresh process rebuilds the minimal admin config and the
	// initial scan + retry loop bring the site back (2026-09-01 boot outage).
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		return 0, nil
	case err := <-watcher.Fatal():
		logger.Error("watcher unrecoverable — exiting for the restart policy to recover", "error", err)
		return 1, err
	}
}

// loadConfig reads environment variables into a GeneratorConfig.
// BASE_DOMAIN, ADMIN_EMAIL, GOOGLE_CLIENT_ID, and GOOGLE_CLIENT_SECRET
// are required — set them in .env.
func loadConfig(log *slog.Logger) (GeneratorConfig, error) {
	// A test machine, with certificates of its own, may have no sign-in client (design 16.11). The
	// ingress then starts with a stand-in client that Google refuses, so nobody can sign in: only the
	// open health check reaches the server.
	if os.Getenv(EnvTLS) == TLSInternal && os.Getenv(EnvGoogleClientID) == "" && os.Getenv(EnvGoogleClientSecret) == "" {
		log.Warn("CREW_TLS=internal and no sign-in client: nobody can sign in on this machine")
		os.Setenv(EnvGoogleClientID, NoClientID)
		os.Setenv(EnvGoogleClientSecret, NoClientSecret)
	}
	// Validate all required env vars upfront so failures are caught at startup.
	for _, key := range []string{EnvBaseDomain, EnvAdminEmail, EnvGoogleClientID, EnvGoogleClientSecret} {
		if os.Getenv(key) == "" {
			return GeneratorConfig{}, fmt.Errorf("%s is required but not set — add it to your .env file", key)
		}
	}

	baseDomain := os.Getenv(EnvBaseDomain)
	adminEmail := os.Getenv(EnvAdminEmail)
	// Both are written into the generated Caddyfile as they are, so each must have its form: one
	// mail address, and host names.
	if list := parseEmailList(adminEmail); len(list) != 1 || list[0] != adminEmail {
		return GeneratorConfig{}, fmt.Errorf("%s=%q: not one mail address", EnvAdminEmail, adminEmail)
	}
	for _, key := range []string{EnvBaseDomain, EnvAuthDomain, EnvAppsDomain} {
		if v := os.Getenv(key); v != "" && (sanitizeHostname(v) != v || !strings.Contains(v, ".")) {
			return GeneratorConfig{}, fmt.Errorf("%s=%q: not a host name", key, v)
		}
	}

	cfg := GeneratorConfig{
		BaseDomain:        baseDomain,
		AdminEmail:        adminEmail,
		CaddyfileBasePath: os.Getenv(EnvCaddyfileBasePath),
		AuthDomain:        getEnvOrDefault(EnvAuthDomain, "auth."+baseDomain),
		AppsDomain:        os.Getenv(EnvAppsDomain),
		IngressToken:      os.Getenv(EnvIngressToken),
		JWTKey:            os.Getenv(EnvJWTKey),
		CloudflareDNS:     os.Getenv(EnvCloudflareToken) != "",
		TLS:               getEnvOrDefault(EnvTLS, TLSACME),
		AdminListen:       getEnvOrDefault(EnvCaddyAdmin, DefaultCaddyAdmin),
	}

	// The admin API can replace the whole ingress config, so a non-loopback
	// bind hands the sign-in in front of Crew away. Refuse to start rather
	// than trust a default.
	if !isLoopbackAddr(cfg.AdminListen) {
		return GeneratorConfig{}, fmt.Errorf(
			"%s=%q must bind a loopback address (127.0.0.1/::1) — the admin API reconfigures the whole ingress",
			EnvCaddyAdmin, cfg.AdminListen)
	}

	if cfg.TLS != TLSACME && cfg.TLS != TLSStaging && cfg.TLS != TLSInternal {
		return GeneratorConfig{}, fmt.Errorf("%s=%q: must be %s, %s, or %s", EnvTLS, cfg.TLS, TLSACME, TLSStaging, TLSInternal)
	}

	if superAdmins := os.Getenv(EnvSuperAdmins); superAdmins != "" {
		cfg.SuperAdmins = parseEmailList(superAdmins)
	}

	rps := getEnvOrDefault(EnvDefaultRPS, strconv.Itoa(DefaultRPS))
	parsed, err := strconv.Atoi(rps)
	if err != nil {
		log.Warn("invalid "+EnvDefaultRPS+", using default", "value", rps, "default", DefaultRPS)
		parsed = DefaultRPS
	}
	cfg.DefaultRPS = parsed

	// Hosting needs an apps domain of its own, another registered domain than Crew's (R172), and
	// Crew's key, from which the apps portal's is derived.
	if why := hostingProblem(cfg); why != "" {
		log.Warn("hosted projects are not served", "why", why)
	} else {
		key, err := hkdf.Key(sha256.New, []byte(cfg.JWTKey), nil, "crew apps portal", 32)
		if err != nil {
			return GeneratorConfig{}, fmt.Errorf("the apps portal's key: %w", err)
		}
		if err := os.Setenv(EnvAppsJWTKey, base64.StdEncoding.EncodeToString(key)); err != nil {
			return GeneratorConfig{}, err
		}
		cfg.Hosting, cfg.AppsKey = true, true
	}

	log.Info("config loaded",
		"base_domain", cfg.BaseDomain,
		"auth_domain", cfg.AuthDomain,
		"apps_domain", cfg.AppsDomain,
		"admin_email", cfg.AdminEmail,
		"super_admins", strings.Join(cfg.SuperAdmins, ","),
		"default_rps", cfg.DefaultRPS,
		"tls", cfg.TLS,
		"hosting", cfg.Hosting,
	)

	return cfg, nil
}

// hostingProblem says why the ingress serves no hosted project, or "": the same guard as the
// server's, as crew-ctl checks at install that the two registered domains differ (plan M19 D23).
func hostingProblem(cfg GeneratorConfig) string {
	a, c := strings.ToLower(cfg.AppsDomain), strings.ToLower(cfg.BaseDomain)
	switch {
	case a == "":
		return "no apps domain (CREW_APPS_DOMAIN)"
	case a == c, strings.HasSuffix(c, "."+a), strings.HasSuffix(a, "."+c):
		return "the apps domain is Crew's own address or holds it"
	case cfg.JWTKey == "":
		return "no key for the sign-in (CREW_JWT_KEY)"
	}
	return ""
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// syncDNS detects the machine's public IP and ensures the Cloudflare DNS
// records of Crew's address and of its sign-in page point to it, and the apps domain's.
func syncDNS(token string, cfg GeneratorConfig, logger *slog.Logger) error {
	ip, err := DetectPublicIP()
	if err != nil {
		return fmt.Errorf("detecting public IP: %w", err)
	}
	logger.Info("detected public ip", "ip", ip)

	return syncZones(ip, cfg, func(domain string) (*CloudflareClient, error) {
		return NewCloudflareClient(token, domain, logger)
	})
}

// syncZones points Crew's address and its sign-in at ip in Crew's zone, and with hosting the apps
// domain's wildcard and its sign-in in the apps domain's zone, another registered domain (19.4).
func syncZones(ip string, cfg GeneratorConfig, client func(domain string) (*CloudflareClient, error)) error {
	cf, err := client(cfg.BaseDomain)
	if err != nil {
		return fmt.Errorf("creating cloudflare client: %w", err)
	}
	if err := cf.EnsureDNS(ip, cfg.BaseDomain, cfg.AuthDomain); err != nil {
		return err
	}
	if !cfg.Hosting {
		return nil
	}
	apps, err := client(cfg.AppsDomain)
	if err != nil {
		return fmt.Errorf("the apps domain's zone: %w", err)
	}
	return apps.EnsureDNS(ip, "*."+cfg.AppsDomain, "auth."+cfg.AppsDomain)
}
