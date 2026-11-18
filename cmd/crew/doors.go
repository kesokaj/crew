package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// sandboxNet is what the ingress's door asks of the sandboxes: whether an address of the server is on
// a sandbox's bridge, and whether an address a request came from is on one.
type sandboxNet interface {
	IsSandboxDoor(local string) bool
	IsSandboxGuest(remote string) bool
}

// doorName is the name the ingress reaches the server by, which is on the ingress's network alone:
// CREW_DOOR_NAME, crew-server-door in Compose. The server's own name is not one, since Docker answers
// it with the bridge the server joined last (19.4). The name is rooted, with a final dot, so a name
// Docker does not hold is never asked again under the machine's search domain, of the local
// network's resolver. A check server (16.4) runs in a microVM, which Docker's name server does not
// answer, on its scratch network alone and joins no bridge: its hostname, which Docker writes into
// its /etc/hosts with that network's address, is the name there, unrooted, since a rooted name is
// not looked up in that file. It is read here, not with the rest of the environment, so what the
// door trusts is in a protected file (16.8).
func doorName(check bool, getenv func(string) string, hostname func() (string, error)) (string, error) {
	if n := getenv("CREW_DOOR_NAME"); n != "" {
		return rooted(n), nil
	}
	if check {
		h, err := hostname()
		if err != nil || h == "" {
			return "", fmt.Errorf("the ingress's network: a check server's hostname: %v", err)
		}
		return h, nil
	}
	return rooted("crew-server-door"), nil
}

// rooted is name with a final dot; an address is left as it is.
func rooted(name string) string {
	if net.ParseIP(name) != nil || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// openIngressDoor is the server at the ingress's door, once it knows the ingress's network (13.6,
// 20.3). A server that cannot learn it in a minute does not serve: a door that answered everyone
// would answer a sandbox's guest too.
func openIngressDoor(ctx context.Context, e env, token []byte, orch *atomic.Pointer[orchestrator.Orchestrator], log *slog.Logger) (*httpapi.Server, error) {
	name, err := doorName(e.Check, os.Getenv, os.Hostname)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	door, err := waitForDoorNet(ctx, name, net.DefaultResolver.LookupHost, net.InterfaceAddrs)
	if err != nil {
		return nil, err
	}
	log.Info("the ingress's door answers only the ingress's network and the loopback", "name", name, "network", door.String())
	return ingressServer(e, token, door, func() sandboxNet {
		if o := orch.Load(); o != nil {
			return o
		}
		return nil
	}, log), nil
}

// waitForDoorNet asks doorNet until it answers or ctx ends: Docker's DNS may not answer the first
// instant a container runs.
func waitForDoorNet(ctx context.Context, name string, lookup func(context.Context, string) ([]string, error), addrs func() ([]net.Addr, error)) (*net.IPNet, error) {
	for {
		door, err := doorNet(ctx, name, lookup, addrs)
		if err == nil {
			return door, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(time.Second):
		}
	}
}

// doorNet is the ingress's network as the server sees it: the subnet of the server's own address
// there, which name resolves to. An address name gives that is not the server's, such as another
// container's under the same alias while one is replaced, is passed over; the server's own must be
// there, and all on one network.
func doorNet(ctx context.Context, name string, lookup func(context.Context, string) ([]string, error), addrs func() ([]net.Addr, error)) (*net.IPNet, error) {
	ips, err := lookup(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("the ingress's network: %s: %w", name, err)
	}
	own, err := addrs()
	if err != nil {
		return nil, fmt.Errorf("the ingress's network: this server's addresses: %w", err)
	}
	var door *net.IPNet
	for _, s := range ips {
		ip := net.ParseIP(s).To4()
		if ip == nil {
			continue
		}
		n := ownSubnet(ip, own)
		if n == nil {
			continue
		}
		if door != nil && door.String() != n.String() {
			return nil, fmt.Errorf("the ingress's network: %s gives addresses of this server's on %s and on %s", name, door, n)
		}
		door = n
	}
	if door == nil {
		return nil, fmt.Errorf("the ingress's network: %s gives %v, no IPv4 address of this server's", name, ips)
	}
	return door, nil
}

// ownSubnet is the IPv4 subnet of the server's address ip, or nil when ip is not one of own.
func ownSubnet(ip net.IP, own []net.Addr) *net.IPNet {
	for _, a := range own {
		in, ok := a.(*net.IPNet)
		if !ok || !in.IP.Equal(ip) {
			continue
		}
		if ones, bits := in.Mask.Size(); bits == 32 {
			m := net.CIDRMask(ones, 32)
			return &net.IPNet{IP: ip.Mask(m), Mask: m}
		}
	}
	return nil
}

// fromIngress lets through a request from door, the ingress's network, or from the loopback, where
// Docker's health check and crew admin ask in the server's own container; a guest cannot send from
// either and have the answer come back to it. A nil door is the loopback alone.
func fromIngress(door *net.IPNet) func(remote string) bool {
	return func(remote string) bool {
		h, _, err := net.SplitHostPort(remote)
		if err != nil {
			return false
		}
		ip := net.ParseIP(h)
		return ip != nil && (ip.IsLoopback() || (door != nil && door.Contains(ip)))
	}
}

// ingressServer is the server at the ingress's door (20.3, "Two doors"). It answers a request only
// from door, the ingress's network, or the loopback, also before the orchestrator is open, which
// closes the door to a guest on a bridge the sandboxes do not know: of a server that started again
// and has not taken them up, of a sandbox being removed, and a hosted project's after a restart
// (13.6). Under that, a request that arrives at the server's address on a sandbox's bridge, or that
// comes from any address on a sandbox's or a hosted project's bridge, is not for this door either.
// sandboxes gives nil until the orchestrator is open.
func ingressServer(e env, token []byte, door *net.IPNet, sandboxes func() sandboxNet, log *slog.Logger) *httpapi.Server {
	return &httpapi.Server{
		FromIngress: fromIngress(door),
		SandboxDoor: func(local string) bool {
			n := sandboxes()
			return n != nil && n.IsSandboxDoor(local)
		},
		SandboxGuest: func(remote string) bool {
			n := sandboxes()
			return n != nil && n.IsSandboxGuest(remote)
		},
		Machine:      e.machine(),
		Hostname:     e.Hostname,
		AppsDomain:   e.AppsDomain,
		AdminEmail:   e.AdminEmail,
		IngressToken: []byte(strings.TrimSpace(string(token))),
		WebDir:       e.WebDir,
		Log:          log,
		ReadOnly:     e.Check,
	}
}
