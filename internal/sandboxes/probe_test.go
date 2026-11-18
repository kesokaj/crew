package sandboxes_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The probe (21, D11) is a sandbox of its own kind that is given nothing, no login, no git identity,
// no token and no gcloud, runs Model only and Minimal at Kata's base size, and is removed as soon as
// it started: its boot is what it is for. A server that starts again removes one it left.
func TestTheProbeIsGivenNothingAndRemovedAtOnce(t *testing.T) {
	m, f := manager(t)
	if g := sandboxes.GrantFor(sandboxes.KindProbe, "agy", []string{"agy"}, true); g.CLIs != nil || g.GitIdentity || g.GitHubToken || g.Gcloud {
		t.Fatalf("the probe's grant: %+v", g)
	}
	if mode := sandboxes.ModeFor(sandboxes.KindProbe, config.ModeInternet); mode != config.ModeModelOnly {
		t.Fatalf("the probe's mode: %s", mode)
	}
	if err := m.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.specs) != 1 || f.specs[0].Kind != sandboxes.KindProbe || !f.specs[0].Minimal || f.specs[0].Mode != config.ModeModelOnly ||
		f.specs[0].Sizing.NanoCPUs != 0 || f.specs[0].Sizing.MemBytes != 0 || len(f.removed) != 1 || f.removed[0] != f.specs[0].ID {
		t.Fatalf("started %+v, removed %v", f.specs, f.removed)
	}
	var open int
	m.Pool.QueryRow(context.Background(), `SELECT count(*) FROM sandboxes WHERE kind = 'probe' AND removed_at IS NULL`).Scan(&open)
	if open != 0 {
		t.Fatal("the probe's row stays open")
	}
	f.failOn = sandboxes.KindProbe
	if err := m.Probe(context.Background()); err == nil {
		t.Fatal("a probe that did not start was no error")
	}
	if !sandboxes.ShortLived(sandboxes.KindProbe) {
		t.Fatal("a probe left at a restart is not removed")
	}
}
