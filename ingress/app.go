package ingress

import (
	"regexp"
	"strings"
)

// AccessMode determines how authentication is handled for an app.
type AccessMode int

const (
	// ModePublic means no authentication required.
	ModePublic AccessMode = iota
	// ModeAuthenticated means any Google user can access.
	ModeAuthenticated
	// ModeRoleGated means only listed emails can access with assigned roles.
	ModeRoleGated
)

// App is one site the ingress serves. In Crew the routes come from the
// ingress's own environment, never from labels on a container (design 19.4).
type App struct {
	// Name is the service name (used for logging/zone names).
	Name string
	// Hostname is the subdomain (e.g., "grafana" → grafana.{APPS_DOMAIN}),
	// and the name of the site's policy and rate limit zone.
	Hostname string
	// FQDN, when set, is the site's full name in place of Hostname.AppsDomain.
	FQDN string
	// ContainerIP is the upstream's address or name on the core network
	// (e.g., "crew-server").
	ContainerIP string
	// Port is the container port to proxy to.
	Port int
	// Mode is the access mode for this app.
	Mode AccessMode
	// Admins is the list of admin email addresses (ModeRoleGated only).
	Admins []string
	// Viewers is the list of viewer email addresses (ModeRoleGated only).
	Viewers []string
	// SuperAdmins is the list of superadmin email addresses (ModeRoleGated only).
	SuperAdmins []string
	// RPS is the rate limit in requests per second per client IP.
	RPS int
	// OpenPaths are served without the sign-in, with every identity header
	// removed: Crew's server has one, its health check (design 16.11).
	OpenPaths []string
	// Sessions passes the sign-in's time and the device's id to the upstream
	// (HeaderAuthTime, HeaderSession), and the ingress's token (HeaderIngress):
	// Crew's server only (design 20.3).
	Sessions bool
	// Hosted is a hosted project's route (design 19.4): at <Hostname>.<AppsDomain>, signed in at the
	// apps domain's own portal, and allowed by Emails in its policy when role-gated (plan M19 D38).
	Hosted bool
	Emails []string
}

// parseEmailList splits a comma-separated email string into a deduplicated,
// trimmed slice. Empty strings are filtered out, and so is anything that is
// not email-shaped.
//
// The validation is the point. These values come from the admin email and
// from SUPERADMINS, and they are written VERBATIM into the generated Caddyfile
// as `match email <value>`, so an entry with Caddyfile-significant bytes could
// change the config. Both sources are the operator's; this is the same
// defence in depth that sanitizeHostname (below) gives the hostname, in an
// internet-facing process.
func parseEmailList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	seen := make(map[string]bool, len(parts))
	var result []string
	for _, p := range parts {
		email := strings.TrimSpace(p)
		if email == "" || seen[email] || !emailRe.MatchString(email) {
			continue
		}
		seen[email] = true
		result = append(result, email)
	}
	return result
}

// emailRe is the shape an entry must have to reach the generated Caddyfile.
// Deliberately strict: no whitespace, no braces, no newlines — the bytes that
// would end a `transform user` block early and let the rest of the value be
// read as Caddyfile directives.
var emailRe = regexp.MustCompile(`^[^@\s{}"'#]+@[^@\s{}"'#]+\.[^@\s{}"'#]+$`)

// sanitizeHostname converts a name into a valid subdomain: no leading
// slashes, underscores as hyphens, lower case.
func sanitizeHostname(name string) string {
	name = strings.TrimLeft(name, "/")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	// Keep only DNS-label-safe characters so a hostname value can never carry
	// Caddyfile-significant bytes (spaces, braces, newlines) into the generated
	// config — defense in depth, even though the routes are set by the server.
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			return r
		}
		return -1
	}, name)
}
