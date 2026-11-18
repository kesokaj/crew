package ingress

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// The one fixed route of the ingress: Crew's address, to the server, behind the
// Google sign-in, for the admin email only (design 19.4, 20.3), but for its
// health check and the Crew MCP of the operator's own tools. It comes from
// the ingress's environment, so nothing an agent builds can add a route.
const (
	// EnvServerAddr is the server's door for the ingress, as host:port on the
	// core services' network.
	EnvServerAddr = "CREW_SERVER_ADDR"
	// DefaultServerAddr is the server's name on crew-ingress alone, and its port: the server is also on
	// every hosted bridge, where its own container name could answer with that bridge's address (19.4).
	DefaultServerAddr = "crew-server-door:8080"
	// ServerRouteName names the route's policy and rate limit zone.
	ServerRouteName = "crew"
	// ServerHealthPath is the server's health check, served without the
	// sign-in, so that install can prove the server answers behind the ingress.
	ServerHealthPath = "/healthz"
	// ServerMCPPath is the Crew MCP for the operator's own tools (design 11.7):
	// a tool cannot do the browser sign-in, so this path is served without it,
	// and the server checks the personal token each request carries.
	ServerMCPPath = "/mcp"
)

// ServerRoute is the route to Crew's server at the base domain itself.
func ServerRoute(cfg GeneratorConfig, serverAddr string) (App, error) {
	host, portText, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return App{}, fmt.Errorf("%s=%q: %w", EnvServerAddr, serverAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return App{}, fmt.Errorf("%s=%q: the port is not a number from 1 to 65535", EnvServerAddr, serverAddr)
	}
	if host == "" || sanitizeHostname(host) != host {
		return App{}, fmt.Errorf("%s=%q: the host is not a plain name or address", EnvServerAddr, serverAddr)
	}
	admins := parseEmailList(cfg.AdminEmail)
	if len(admins) != 1 {
		return App{}, fmt.Errorf("%s=%q: not one mail address", EnvAdminEmail, cfg.AdminEmail)
	}
	return App{
		Name:        "crew-server",
		Hostname:    ServerRouteName,
		FQDN:        cfg.BaseDomain,
		ContainerIP: host,
		Port:        port,
		Mode:        ModeRoleGated,
		Admins:      admins,
		RPS:         cfg.DefaultRPS,
		OpenPaths:   []string{ServerHealthPath, ServerMCPPath},
		Sessions:    true,
	}, nil
}

// StaticRoutes serves the same routes on every scan.
func StaticRoutes(apps ...App) Routes {
	return func(context.Context) ([]App, error) { return apps, nil }
}
