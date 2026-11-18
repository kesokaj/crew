package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// PollInterval is how often the ingress asks the server for the hosted routes, and how soon a failed
// load is tried again (design 19.4, plan M19 D33).
const PollInterval = 10 * time.Second

// hostLabel is a hosted project's name as its address takes it (plan M19 D10).
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// hostedRoute is one route of the server's answer on the ingress's door.
type hostedRoute struct {
	Project  string   `json:"project"`
	Host     string   `json:"host"`
	Upstream string   `json:"upstream"`
	Access   string   `json:"access"`
	Emails   []string `json:"emails"`
	RPS      int      `json:"rps"`
}

// sandboxNet is where the hosted bridges are (the machine's address pool, 172.16.0.0/12): an upstream
// anywhere else is not a hosted project's.
var sandboxNet = &net.IPNet{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)}

// HostedRoutes are Crew's route and the hosted ones the server names at url, asked with the ingress's
// token and no identity header. Until the server first answers, Crew's route is served alone; after,
// a failed ask keeps the routes of the last answer, so a server that restarts leaves the hosted
// addresses up. What the ingress would not write into a Caddyfile is left out, with a warning.
func HostedRoutes(crew App, url, token string, cfg GeneratorConfig, client *http.Client, log *slog.Logger) Routes {
	var mu sync.Mutex
	var last []App
	return func(ctx context.Context) ([]App, error) {
		hosted, err := askRoutes(ctx, url, token, cfg, client, log)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			log.Warn("asking the server for the hosted routes; serving the last ones", "error", err, "last", len(last))
		} else {
			last = hosted
		}
		return append([]App{crew}, last...), nil
	}
}

func askRoutes(ctx context.Context, url, token string, cfg GeneratorConfig, client *http.Client, log *slog.Logger) ([]App, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderIngress, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	var body struct {
		Routes []hostedRoute `json:"routes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("the server's answer: %w", err)
	}
	apps := []App{}
	for _, r := range body.Routes {
		a, why := hostedApp(r, cfg)
		if why != "" {
			log.Warn("a hosted route is left out", "project", r.Project, "why", why)
			continue
		}
		apps = append(apps, a)
	}
	return apps, nil
}

// hostedApp is a route of the server's answer as the generator takes it, or why it is not served.
func hostedApp(r hostedRoute, cfg GeneratorConfig) (App, string) {
	if !hostLabel.MatchString(r.Host) || r.Host == ServerRouteName || r.Host == "auth" {
		return App{}, "the host is not a name the ingress serves"
	}
	h, p, err := net.SplitHostPort(r.Upstream)
	port, perr := strconv.Atoi(p)
	ip := net.ParseIP(h)
	if err != nil || perr != nil || port < 1 || port > 65535 || ip == nil || ip.To4() == nil || !sandboxNet.Contains(ip) {
		return App{}, "the upstream is not on a hosted bridge"
	}
	a := App{Name: r.Host, Hostname: r.Host, ContainerIP: ip.String(), Port: port, Hosted: true, RPS: cfg.DefaultRPS}
	switch r.Access {
	case "private", "listed":
		a.Mode = ModeRoleGated
		a.Emails = []string{cfg.AdminEmail}
		if r.Access == "listed" {
			for _, e := range r.Emails {
				if list := parseEmailList(e); len(list) != 1 || list[0] != e {
					return App{}, "a listed address is not one mail address"
				}
				if !containsEmail(a.Emails, e) {
					a.Emails = append(a.Emails, e)
				}
			}
		}
	case "google":
		a.Mode = ModeAuthenticated
	case "public":
		a.Mode = ModePublic
		if r.RPS < 1 || r.RPS > 1000 {
			return App{}, "the requests per second are not from 1 to 1000"
		}
		a.RPS = r.RPS
	default:
		return App{}, "the access is not one of private, listed, google, public"
	}
	return a, ""
}
