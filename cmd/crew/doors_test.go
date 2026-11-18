package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
)

type fakeSandboxNet struct{ door, guest string }

func (f fakeSandboxNet) IsSandboxDoor(local string) bool {
	h, _, _ := net.SplitHostPort(local)
	return h == f.door
}

func (f fakeSandboxNet) IsSandboxGuest(remote string) bool {
	h, _, _ := net.SplitHostPort(remote)
	return h == f.guest
}

// The server serve starts at the ingress's door asks the sandboxes both ways (13.6, 20.3): a request
// at the server's address on a sandbox's bridge, and one from an address on a sandbox's bridge at any
// address of the server, find nothing there; before the orchestrator is open, and from anywhere else, the door
// answers.
func TestTheIngressServerAsksTheSandboxesBothWays(t *testing.T) {
	var sn sandboxNet
	s := ingressServer(env{Hostname: "crew.example.com", AdminEmail: "admin@example.com"}, []byte("t"), nil,
		func() sandboxNet { return sn }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func() int {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// The health answers without a database, as degraded: not the 404 of a closed door.
	if code := get(); code == http.StatusNotFound {
		t.Fatalf("before the orchestrator is open: %d", code)
	}
	sn = fakeSandboxNet{door: "192.0.2.1", guest: "192.0.2.2"}
	if code := get(); code == http.StatusNotFound {
		t.Fatalf("from an address of no sandbox: %d", code)
	}
	sn = fakeSandboxNet{door: "127.0.0.1"}
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("at a sandbox's door: %d", code)
	}
	sn = fakeSandboxNet{guest: "127.0.0.1"}
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("from a sandbox's bridge: %d", code)
	}
}

// The ingress's door answers only the ingress's network and the loopback (13.6, 20.3), so a guest on a
// bridge the sandboxes do not know is refused: of a server that started again and has not taken its
// sandboxes up, of a sandbox whose doors a removal dropped, and a hosted project's after a restart.
func TestTheIngressDoorAnswersOnlyTheIngresssNetwork(t *testing.T) {
	_, door, _ := net.ParseCIDR("172.16.1.0/24")
	var sn sandboxNet // no sandbox known, as each window leaves it
	s := ingressServer(env{Hostname: "crew.example.com", AdminEmail: "admin@example.com"}, []byte("t"), door,
		func() sandboxNet { return sn }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()
	get := func(local, remote string) int {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		la, err := net.ResolveTCPAddr("tcp", local)
		if err != nil {
			t.Fatal(err)
		}
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, la))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		name, local, remote string
		open                bool
	}{
		{"the ingress", "172.16.1.2:8080", "172.16.1.3:40000", true},
		{"Docker's health check and crew admin", "127.0.0.1:8080", "127.0.0.1:40000", true},
		{"a guest of a sandbox a restarted server has not taken up, at its own door", "172.16.6.1:8080", "172.16.6.2:40000", false},
		{"the same guest at the server's address on the ingress's network", "172.16.1.2:8080", "172.16.6.2:40000", false},
		{"a guest of a sandbox being removed", "172.16.1.2:8080", "172.16.7.5:40000", false},
		{"a hosted app after a restart", "172.16.1.2:8080", "172.16.9.252:40000", false},
		{"a link-local IPv6 address", "[fe80::2]:8080", "[fe80::1]:40000", false},
	} {
		if code := get(c.local, c.remote); (code != http.StatusNotFound) != c.open {
			t.Errorf("%s: %d", c.name, code)
		}
	}
	// With the sandboxes known, a request from the ingress's network still answers.
	sn = fakeSandboxNet{door: "172.16.6.1", guest: "172.16.6.2"}
	if code := get("172.16.1.2:8080", "172.16.1.3:40000"); code == http.StatusNotFound {
		t.Errorf("the ingress, with sandboxes known: %d", code)
	}
}

// A door that does not know the ingress's network answers the loopback alone.
func TestAnIngressDoorWithNoNetworkAnswersTheLoopbackAlone(t *testing.T) {
	f := fromIngress(nil)
	if !f("127.0.0.1:1") || !f("[::1]:1") || f("172.16.1.3:1") || f("bad") {
		t.Fatal("a door with no network")
	}
}

// The door serve opens knows the ingress's network by its name before anything else, and no sandbox
// before the orchestrator is open (20.3).
func TestTheDoorServeOpensKnowsTheIngresssNetworkFirst(t *testing.T) {
	var orch atomic.Pointer[orchestrator.Orchestrator]
	t.Setenv("CREW_DOOR_NAME", "127.0.0.1")
	e := env{Hostname: "crew.example.com", AdminEmail: "admin@example.com"}
	s, err := openIngressDoor(context.Background(), e, []byte("t"), &orch, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if s.FromIngress == nil || !s.FromIngress("127.0.0.1:1") || s.FromIngress("192.0.2.1:1") {
		t.Fatal("the door does not answer the loopback's network alone")
	}
	if s.SandboxDoor("192.0.2.1:8091") || s.SandboxGuest("192.0.2.2:1") {
		t.Fatal("the door knows a sandbox before the orchestrator is open")
	}
}

// A check server's door, which serve opens with check mode on and no CREW_DOOR_NAME, is named by its
// hostname (16.4): the name shows in the error when it does not resolve here, or in the log line when
// it does.
func TestACheckServersDoorIsNamedByItsHostname(t *testing.T) {
	t.Setenv("CREW_DOOR_NAME", "")
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skipf("no hostname here: %v", err)
	}
	var logged bytes.Buffer
	var orch atomic.Pointer[orchestrator.Orchestrator]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e := env{Hostname: "crew.example.com", AdminEmail: "admin@example.com", Check: true}
	_, err = openIngressDoor(ctx, e, []byte("t"), &orch, slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil && !strings.Contains(err.Error(), host) {
		t.Fatalf("the door was not named by the hostname %s: %v", host, err)
	}
	if err == nil && !strings.Contains(logged.String(), "name="+host) {
		t.Fatalf("the door was not named by the hostname %s: %s", host, logged.String())
	}
}

func lookupOf(m map[string][]string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, name string) ([]string, error) {
		if ips, ok := m[name]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
}

func ownAddrs(cidrs ...string) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) {
		var out []net.Addr
		for _, c := range cidrs {
			ip, n, err := net.ParseCIDR(c)
			if err != nil {
				return nil, err
			}
			out = append(out, &net.IPNet{IP: ip, Mask: n.Mask})
		}
		return out, nil
	}
}

// The ingress's network is the subnet of the server's own address that the door's name gives; a name
// that gives no address of the server's, an IPv6 one, two networks, or none, is no network (13.6).
func TestTheIngressNetworkIsTheSubnetOfTheServersAddressByItsName(t *testing.T) {
	own := ownAddrs("127.0.0.1/8", "172.16.1.2/24", "172.16.2.2/24", "172.16.6.254/24", "fe80::2/64")
	ctx := context.Background()
	n, err := doorNet(ctx, "crew-server-door", lookupOf(map[string][]string{"crew-server-door": {"172.16.1.2"}}), own)
	if err != nil || n.String() != "172.16.1.0/24" {
		t.Fatalf("the ingress's network: %v %v", n, err)
	}
	// Another container under the same alias, while one is replaced, is passed over.
	n, err = doorNet(ctx, "crew-server-door", lookupOf(map[string][]string{"crew-server-door": {"172.16.1.9", "172.16.1.2"}}), own)
	if err != nil || n.String() != "172.16.1.0/24" {
		t.Fatalf("the ingress's network beside another container's address: %v %v", n, err)
	}
	for name, ips := range map[string][]string{
		"not the server's": {"172.16.1.3"},
		"IPv6":             {"fe80::2"},
		"two networks":     {"172.16.1.2", "172.16.2.2"},
		"none":             {},
	} {
		if n, err := doorNet(ctx, "x", lookupOf(map[string][]string{"x": ips}), own); err == nil {
			t.Errorf("%s: %v", name, n)
		}
	}
	if _, err := doorNet(ctx, "missing", lookupOf(nil), own); err == nil {
		t.Error("a name that does not resolve")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := waitForDoorNet(ctx, "missing", lookupOf(nil), own); err == nil {
		t.Error("waiting for a name that does not resolve ended without an error")
	}
}

// The door's name is CREW_DOOR_NAME, else crew-server-door, Compose's alias on crew-ingress, rooted so
// no search domain is tried; a check server, in a microVM on its scratch network alone, is reached by
// its hostname, as its /etc/hosts holds it (16.4).
func TestTheDoorsName(t *testing.T) {
	host := func() (string, error) { return "9b34d236d250", nil }
	for _, c := range []struct {
		check bool
		set   string
		want  string
	}{
		{false, "", "crew-server-door."},
		{true, "", "9b34d236d250"},
		{true, "t19-server-door", "t19-server-door."},
		{false, "t20-server-door.", "t20-server-door."},
		{false, "127.0.0.1", "127.0.0.1"},
	} {
		getenv := func(k string) string {
			if k == "CREW_DOOR_NAME" {
				return c.set
			}
			return ""
		}
		if got, err := doorName(c.check, getenv, host); err != nil || got != c.want {
			t.Errorf("check %v, CREW_DOOR_NAME %q: %s %v", c.check, c.set, got, err)
		}
	}
	if _, err := doorName(true, func(string) string { return "" }, func() (string, error) { return "", errors.New("no name") }); err == nil {
		t.Error("a check server with no hostname has a door name")
	}
}

func TestOpenIngressDoorFailsWhenTheNameIsNotTheServers(t *testing.T) {
	t.Setenv("CREW_DOOR_NAME", "192.0.2.1")
	var orch atomic.Pointer[orchestrator.Orchestrator]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := env{Hostname: "crew.example.com", AdminEmail: "admin@example.com"}
	s, err := openIngressDoor(ctx, e, []byte("t"), &orch, slog.New(slog.DiscardHandler))
	if err == nil || s != nil || !strings.Contains(err.Error(), "192.0.2.1") {
		t.Fatalf("openIngressDoor = %v, %v", s, err)
	}
}
