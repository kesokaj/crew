package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/credbroker"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/egress"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/logins"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The sandboxes' door answers Go's module proxy protocol with the egress proxy's module proxy (13.6,
// since 0.61), which answers only a Sources sandbox, and says so when the server has no proxy.
func TestTheDoorAnswersTheModuleProxy(t *testing.T) {
	m := &machine{brokerSvc: &credbroker.Broker{}, git: &gitendpoint.Endpoint{}, proxy: &egress.Proxy{
		SandboxAt: func(local, remote string) (string, bool) { return "judge-1", true },
		IsSources: func(string) bool { return false },
		Sources:   func(context.Context) ([]string, error) { return []string{"proxy.golang.org"}, nil },
		Log:       slog.New(slog.DiscardHandler),
	}}
	ask := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		m.sandboxDoor().ServeHTTP(w, httptest.NewRequest("GET", "/goproxy/example.com/m/@v/list", nil))
		return w
	}
	if w := ask(); w.Code != http.StatusForbidden || w.Body.String() != "refused: the module proxy answers only a Sources sandbox\n" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	m.proxy = nil
	if w := ask(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("with no proxy: %d %q", w.Code, w.Body)
	}
}

// The sandboxes' door, its handler, its listener and its server, and what is removed of a run's
// secrets are wired in machine.go alone, which 16.8 protects: a release that adds a route, swaps the
// handler, or drops the removal changes a protected file (round 2 of the security review of M16's
// close).
func TestTheDoorAndTheSecretsAreWiredInMachineGo(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "machine.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"sandboxDoor", "newBroker", ".secrets(", "net.Listen("} {
			if strings.Contains(string(body), s) {
				t.Errorf("%s has %q: the door and the secrets are machine.go's", f, s)
			}
		}
	}
}

// newBroker wires sandboxes.Manager.NoGitHubToken onto the credential broker, so a sandbox that
// gets no GitHub token writes a no_github_token signal (21).
func TestNewBrokerWiresNoGitHubToken(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	log := slog.New(slog.DiscardHandler)
	sb := &sandboxes.Manager{Pool: d.Server, Log: log}
	m := &machine{sandboxes: sb, logins: &logins.Service{Pool: d.Server, Log: log}}
	b := m.newBroker(log)
	if b.NoToken == nil {
		t.Fatal("newBroker left NoToken nil")
	}
	b.NoToken(ctx, sandboxes.Row{ID: "run-1"}, errors.New("github: 503 Service Unavailable"))
	var why string
	if err := d.Server.QueryRow(ctx, `SELECT detail->>'error' FROM signals WHERE kind = 'no_github_token' AND detail->>'sandbox' = 'run-1'`).Scan(&why); err != nil {
		t.Fatalf("no_github_token signal: %v", err)
	}
}
