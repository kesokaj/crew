package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The server counts the orchestrator's boots in the row every start asks (21, D10): the second
// microVM in a row that did not boot holds Crew with its error and the machine check's line; a check
// that cannot ask Docker says so; a boot lifts it.
func TestTheServerCountsBootsWithTheMachineCheck(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	check := func(context.Context) (orchestrator.MachineCheck, error) {
		return orchestrator.MachineCheck{Docker: "29.1.3", Runtime: "mvm"}, nil
	}
	on := countBoot(d.Server, check, log)
	on(false, "kata: /dev/kvm is missing")
	if held := hold.Held(ctx, d.Server); held != "" {
		t.Fatalf("after one: %q", held)
	}
	on(false, "kata: /dev/kvm is missing")
	held := hold.Held(ctx, d.Server)
	if !strings.HasPrefix(held, "MicroVMs do not start: kata: /dev/kvm is missing; the machine check: Docker 29.1.3 has no runtime mvm") {
		t.Fatalf("after two: %q", held)
	}
	on(true, "")
	if held := hold.Held(ctx, d.Server); held != "" {
		t.Fatalf("after a boot: %q", held)
	}
	// A long Kata error keeps the machine check's line (round 2 of the review of 5d1b414).
	on(false, "kata: "+strings.Repeat("x", 3000))
	on(false, "kata: "+strings.Repeat("x", 3000))
	if held := hold.Held(ctx, d.Server); !strings.Contains(held, " …; the machine check: Docker 29.1.3 has no runtime mvm") {
		t.Fatalf("a long error: %q", held)
	}
	on(true, "")
	failing := func(context.Context) (orchestrator.MachineCheck, error) {
		return orchestrator.MachineCheck{}, errors.New("no socket")
	}
	if line := machineCheckLine(ctx, failing); line != "the machine check could not ask Docker: no socket" {
		t.Fatal(line)
	}
	ok := func(context.Context) (orchestrator.MachineCheck, error) {
		return orchestrator.MachineCheck{Docker: "29.1.3", Runtime: "mvm", RuntimeOK: true}, nil
	}
	if line := machineCheckLine(ctx, ok); !strings.Contains(line, "has the runtime mvm") {
		t.Fatal(line)
	}
}

// The server wires the count and the probe (round 1 of the review of 5d1b414): the orchestrator's
// OnBoot and the monitor's Probe are set; and its write probe writes the real row as the role crew,
// with its file in the backups folder, so two checks on a database that takes writes leave no file.
func TestTheServerWiresTheBootsAndTheWriteProbe(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &machine{orch: &orchestrator.Orchestrator{}, runs: &runs.Driver{Pool: d.Server}, sandboxes: &sandboxes.Manager{}}
	m.watchBoots(log)
	if m.orch.OnBoot == nil || m.runs.Probe == nil {
		t.Fatal("the boots are not counted, or no probe starts")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := writeProbe(d.Server, env{DataDir: dir}, log)
	if p.File != filepath.Join(dir, "backups", ".db-unwritable") {
		t.Fatalf("the file %s", p.File)
	}
	p.Check(ctx)
	p.Check(ctx)
	if _, err := os.Stat(p.File); !os.IsNotExist(err) {
		t.Fatalf("the probe's real write failed: %v", err)
	}
}
