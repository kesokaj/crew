package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

// A server in its container whose network forwards IPv4 starts no sandbox (13.6; BUG-020): it joins
// every sandbox's bridge, so it would route between them. A sysctl it cannot read refuses too, and
// only CREW_ALLOW_IP_FORWARD=1 lets such a server through.
func TestAServerThatForwardsStartsNoSandbox(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{Check: true, RuntimeImage: "crew-runtime:test", SelfContainer: "crew-server", Runtime: "mvm"}
	dir := t.TempDir()
	write := func(v string) string {
		p := filepath.Join(dir, "ip_forward-"+strings.TrimSpace(v))
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	was := ipForwardProc
	t.Cleanup(func() { ipForwardProc = was })
	for _, c := range []struct {
		name, proc, allow string
		refused           bool
	}{
		{"forwarding on", write("1\n"), "", true},
		{"the sysctl unread", filepath.Join(dir, "absent"), "", true},
		{"forwarding on, the override set", write("1\n"), "1", false},
		{"forwarding off", write("0\n"), "", false},
	} {
		ipForwardProc = c.proc
		t.Setenv(config.EnvAllowIPForward, c.allow)
		m, err := openMachine(ctx, e, d.Server, log)
		forwards := err != nil && strings.Contains(err.Error(), "forwards")
		if forwards != c.refused || (c.refused && m.orch != nil) {
			t.Errorf("%s: refused %v, the orchestrator opened %v, err %v", c.name, forwards, m.orch != nil, err)
		}
	}
}
