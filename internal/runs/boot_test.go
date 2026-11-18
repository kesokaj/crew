package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// bootKit is a kit whose fake harness counts its boots in the database, as serve wires the
// orchestrator's OnBoot to hold.Boot (21, D10).
func bootKit(t *testing.T) *kit {
	k := newKit(t)
	k.f.Boot = func(ok bool, why string) {
		if _, err := hold.Boot(context.Background(), k.db.Server, ok, why); err != nil {
			t.Error(err)
		}
	}
	return k
}

func (k *kit) boots() (failures int, held string) {
	k.t.Helper()
	st, err := hold.Read(context.Background(), k.db.Server)
	if err != nil {
		k.t.Fatal(err)
	}
	return st.BootFailures, st.Held()
}

var noBoot = fmt.Errorf("starting the sandbox: %w: kata: /dev/kvm is missing", orchestrator.ErrNoBoot)

// Two microVMs in a row that do not boot hold new runs (21): the first is a run that ends sandbox, as
// before; the second holds Crew, so no run starts, with the error in the reason; a microVM that boots
// lifts it.
func TestTwoMicroVMsThatDoNotBootHoldNewRuns(t *testing.T) {
	k := bootKit(t)
	ctx := context.Background()
	k.f.StartErr = noBoot
	first := k.start(StartRequest{})
	if v := k.wait(first, "ended"); v.StopReason != ReasonSandbox {
		t.Fatalf("the first: %+v", v)
	}
	if n, held := k.boots(); n != 1 || held != "" {
		t.Fatalf("after one: %d failures, held %q", n, held)
	}
	k.wait(k.start(StartRequest{}), "ended")
	n, held := k.boots()
	if n != 2 || !strings.HasPrefix(held, "MicroVMs do not start: ") || !strings.Contains(held, "/dev/kvm is missing") {
		t.Fatalf("after two: %d failures, held %q", n, held)
	}
	if _, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "A third."}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a run started while microVMs do not boot: %v", err)
	}
	// The probe, or any microVM, that boots lifts the hold.
	if _, err := hold.Boot(ctx, k.db.Server, true, ""); err != nil {
		t.Fatal(err)
	}
	k.f.StartErr = nil
	if n, held := k.boots(); n != 0 || held != "" {
		t.Fatalf("after a boot: %d failures, held %q", n, held)
	}
	k.wait(k.start(StartRequest{Prompt: "After the boot."}), "idle")
}

// A boot between two failures sets the count to 0, so they are not two in a row; a start that fails
// for another reason than the boot counts nothing (D10).
func TestABootResetsTheCount(t *testing.T) {
	k := bootKit(t)
	k.f.StartErr = noBoot
	k.wait(k.start(StartRequest{}), "ended")
	k.f.StartErr = nil
	k.wait(k.start(StartRequest{}), "idle")
	k.f.StartErr = noBoot
	k.wait(k.start(StartRequest{}), "ended")
	if n, held := k.boots(); n != 1 || held != "" {
		t.Fatalf("%d failures, held %q", n, held)
	}
	k.f.StartErr = errors.New("the network of the sandbox: no IPv4 subnet")
	k.wait(k.start(StartRequest{}), "ended")
	k.wait(k.start(StartRequest{}), "ended")
	if n, held := k.boots(); n != 1 || held != "" {
		t.Fatalf("after starts that failed for no boot: %d failures, held %q", n, held)
	}
}

// A project's setup that fails runs in a guest that booted: it holds nothing, however often it fails
// (21, D6; the M13 fixture's setup fails by design).
func TestASetupThatFailsInABootedGuestHoldsNothing(t *testing.T) {
	k := bootKit(t)
	k.f.Setup = func(harness.RunSpec) (bool, string) { return false, "make: *** No rule to make target" }
	for i := range 3 {
		if _, err := k.f.StartRun(context.Background(), harness.RunSpec{Run: fmt.Sprintf("setup-%d", i), CLI: "agy", Setup: "make setup"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, held := k.boots(); n != 0 || held != "" {
		t.Fatalf("%d failures, held %q", n, held)
	}
}

// While microVMs do not boot, the monitor starts a probe every 5 minutes, one at a time (21, D11);
// a probe that boots lifts the hold, and no probe starts after that. Without the hold none starts.
func TestTheMonitorProbesWhileMicroVMsDoNotBoot(t *testing.T) {
	k := bootKit(t)
	ctx := context.Background()
	var probes int
	boots := false
	done := make(chan struct{}, 8)
	k.d.Probe = func(ctx context.Context) error {
		k.mu.Lock()
		probes++
		ok := boots
		k.mu.Unlock()
		k.f.Boot(ok, "kata: /dev/kvm is missing")
		done <- struct{}{}
		return nil
	}
	count := func() int { k.mu.Lock(); defer k.mu.Unlock(); return probes }
	k.d.Tick(ctx)
	select {
	case <-done:
		t.Fatal("a probe without a hold")
	case <-time.After(300 * time.Millisecond):
	}
	for range 2 {
		if _, err := hold.Boot(ctx, k.db.Server, false, "kata: /dev/kvm is missing"); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	k.mu.Lock()
	k.now = start
	k.mu.Unlock()
	k.d.Tick(ctx)
	<-done
	// The probe's goroutine lets go of it after it returned: a later Tick sees no probe running.
	time.Sleep(100 * time.Millisecond)
	k.d.Tick(ctx)
	select {
	case <-done:
		t.Fatal("a second probe within 5 minutes")
	case <-time.After(300 * time.Millisecond):
	}
	// Pause all starts no probe either (security review of 5d1b414).
	if _, err := k.d.PauseAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	k.now = start.Add(5 * time.Minute)
	k.mu.Unlock()
	k.d.Tick(ctx)
	select {
	case <-done:
		t.Fatal("a probe while Crew is paused")
	case <-time.After(300 * time.Millisecond):
	}
	if err := k.d.ResumeAll(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	// Nor a full disk.
	if err := hold.SetDisk(ctx, k.db.Server, "97% of the disk is used"); err != nil {
		t.Fatal(err)
	}
	k.d.Tick(ctx)
	select {
	case <-done:
		t.Fatal("a probe while the disk is full")
	case <-time.After(300 * time.Millisecond):
	}
	if err := hold.SetDisk(ctx, k.db.Server, ""); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	k.now, boots = start.Add(5*time.Minute), true
	k.mu.Unlock()
	k.d.Tick(ctx)
	<-done
	if n, held := k.boots(); count() != 2 || n != 0 || held != "" {
		t.Fatalf("after a probe that booted: %d probes, %d failures, held %q", count(), n, held)
	}
	k.mu.Lock()
	k.now = start.Add(20 * time.Minute)
	k.mu.Unlock()
	k.d.Tick(ctx)
	select {
	case <-done:
		t.Fatal("a probe after the hold was lifted")
	case <-time.After(300 * time.Millisecond):
	}
}

// One probe at a time: Run the check while a probe runs starts none (round 1 of the review of
// 5d1b414).
func TestOneProbeAtATime(t *testing.T) {
	k := bootKit(t)
	release := make(chan struct{})
	var once sync.Once
	unhold := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unhold)
	ran := make(chan struct{}, 4)
	k.d.Probe = func(context.Context) error { ran <- struct{}{}; <-release; return nil }
	if !k.d.ProbeNow(context.Background()) {
		t.Fatal("the first probe did not start")
	}
	<-ran
	if k.d.ProbeNow(context.Background()) {
		t.Fatal("a second probe started while one runs")
	}
	unhold()
	deadline := time.Now().Add(5 * time.Second)
	for !k.d.ProbeNow(context.Background()) {
		if time.Now().After(deadline) {
			t.Fatal("no probe after the first ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-ran
}
