package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// config.go — Single source of truth for all caddy-ingress constants,
// defaults, and env var keys. Values are grouped by lifecycle:
//   - Static:  baked into binary, never changes
//   - Default: sensible fallback, overridable via env var
//   - Dynamic: env var keys (required or optional)

// ─── STATIC: Baked into binary, never changes ─────────────────────────

// Clean identity headers injected into downstream requests.
const (
	HeaderEmail   = "X-Caddy-Email"
	HeaderUser    = "X-Caddy-User"
	HeaderRole    = "X-Caddy-Role"
	HeaderSubject = "X-Caddy-Subject"
	// HeaderIngress proves to the server that a request came through this
	// ingress (not from a container on a shared bridge spoofing X-Caddy-Email
	// directly). Set to the shared CREW_INGRESS_TOKEN when present, on Crew's
	// own route alone (App.Sessions); every route strips a client's copy.
	HeaderIngress = "X-Crew-Ingress"
	// HeaderAuthTime is the sign-in's time, the token's iat in decimal
	// seconds, and HeaderSession the device's id, the token's jti: on Crew's
	// route only, for the server's sessions and its fresh sign-in (20.3).
	HeaderAuthTime = "X-Crew-Auth-Time"
	HeaderSession  = "X-Crew-Session"
)

// clientHeaders are the headers only the ingress sets: every route removes a
// client's copy before anything else sees the request.
var clientHeaders = []string{HeaderEmail, HeaderUser, HeaderRole, HeaderSubject, HeaderIngress, HeaderAuthTime, HeaderSession}

// caddy-security upstream headers (stripped before reaching apps).
const (
	UpstreamHeaderEmail = "X-Token-User-Email"
	UpstreamHeaderName  = "X-Token-User-Name"
	UpstreamHeaderRoles = "X-Token-User-Roles"
	UpstreamHeaderSub   = "X-Token-Subject"
)

// CookiePrefix is this deployment's caddy-security cookie prefix: "AUTHP"
// plus the first 8 upper-case hex digits of sha256(base domain). The
// plugin's default prefix is the same "AUTHP" everywhere, so a browser
// holding a session for a deployment on a PARENT domain sent both
// deployments' same-named cookies to the child, and the child's authorizer
// found no valid one: an endless sign-in redirect. The portal
// names every cookie <prefix>_<NAME> (session id, access token, ...).
func CookiePrefix(baseDomain string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSuffix(baseDomain, "."))))
	return "AUTHP" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// AuthCookieName is this deployment's session (access token) cookie, the
// one every authorization policy reads.
func AuthCookieName(baseDomain string) string { return CookiePrefix(baseDomain) + "_ACCESS_TOKEN" }

// PortalCookieStrip matches EVERY deployment's portal cookies, the legacy
// unprefixed AUTHP_* names included: the public-app blocks and the open paths
// cut them all out before proxying. Stripping only this deployment's name
// would hand an app the plugin's default-named cookie, signed with the same
// pinned key, and a parent-domain deployment's cookie, either replayable under
// this deployment's derivable name. Raw: Caddy compiles it with Go's regexp.
const PortalCookieStrip = `(?:^|;\s*)AUTHP[0-9A-F]*_[A-Z_]+=[^;]*`

// Role strings used in X-Caddy-Role header values.
const (
	RolePublic     = "public"
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleViewer     = "viewer"
	RoleSuperAdmin = "superadmin"
)

// ─── DEFAULTS: Sensible fallback, overridable via env var ──────────────

const (
	// DefaultRPS is the per-client-IP rate limit of a route.
	// Override via DEFAULT_RPS env var.
	DefaultRPS = 100

	// DefaultCaddyAdmin is the Caddy admin API listen address. An IP literal,
	// not "localhost": isLoopbackAddr no longer accepts a NAME, because a name
	// is resolved by the OS at bind time and so cannot be verified here
	// (2026-09-01 review; changed 2026-09-02). Override via CADDY_ADMIN — the
	// override must also be a loopback IP literal with a port.
	DefaultCaddyAdmin = "127.0.0.1:2019"
)

// ─── DYNAMIC: Env var keys ─────────────────────────────────────────────

// Required env vars — program exits with a clear error if unset.
const (
	EnvBaseDomain         = "BASE_DOMAIN"
	EnvAdminEmail         = "ADMIN_EMAIL"
	EnvGoogleClientID     = "GOOGLE_CLIENT_ID"
	EnvGoogleClientSecret = "GOOGLE_CLIENT_SECRET"
)

// EnvAppsJWTKey is the apps portal's key, which the ingress derives from CREW_JWT_KEY at load and
// sets in its own environment, where the Caddyfile reads it (plan M19 D6).
const EnvAppsJWTKey = "CREW_APPS_JWT_KEY"

// Optional env vars — used if present, graceful if absent.
const (
	EnvSuperAdmins       = "SUPERADMINS" // Platform superadmins — get superadmin role on ALL apps + shell access
	EnvDefaultRPS        = "DEFAULT_RPS"
	EnvCaddyAdmin        = "CADDY_ADMIN"
	EnvTLS               = "CREW_TLS" // where certificates come from: acme (default), staging, or internal
	EnvCaddyfileBasePath = "CADDYFILE_BASE_PATH"
	EnvCloudflareToken   = "CLOUDFLARE_API_TOKEN"
	EnvAuthDomain        = "AUTH_DOMAIN"        // Absolute FQDN for auth portal (default: auth.<BASE_DOMAIN>)
	EnvAppsDomain        = "APPS_DOMAIN"        // Base of the hosted projects' addresses: no default, and none hosts nothing (19.4)
	EnvIngressToken      = "CREW_INGRESS_TOKEN" // shared secret set as X-Crew-Ingress on Crew's own route alone
	EnvJWTKey            = "CREW_JWT_KEY"       // stable HMAC key for caddy-security JWTs (survives restarts)
)

// IPDetectionURLs are the services used to detect the server's public IP.
// Order matters — we try each in sequence and return the first valid IPv4 response.
var IPDetectionURLs = []string{
	"https://ifconfig.me/ip",
	"https://api.ipify.org",
	"https://icanhazip.com",
}

// Where certificates come from (design 16.11, CREW_TLS).
const (
	// TLSACME is real certificates from the ACME service.
	TLSACME = "acme"
	// TLSStaging is the test certificates of the same service.
	TLSStaging = "staging"
	// TLSInternal is certificates the ingress makes itself, for a test machine.
	TLSInternal = "internal"
	// StagingCA is the directory of the ACME service's test certificates.
	StagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// The stand-in sign-in client of a test machine that has none (loadConfig).
const (
	NoClientID     = "no-sign-in-client.apps.googleusercontent.com"
	NoClientSecret = "no-sign-in-client"
)
