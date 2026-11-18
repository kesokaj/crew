package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A force-remove that lands while a Kata VM is exiting is refused by Docker
// with "PID … is zombie and can not be killed": the container's PID is the
// cloud-hypervisor process, and the shim has not reaped it yet. A sandbox's
// removal must wait that window out, not fail.
const zombieErr = `cannot remove container "crew-x": could not kill container: container c93 PID 1312994 is zombie and can not be killed. Use the --init option when creating containers to run an init inside the container that forwards signals and reaps processes`

// zombieSandbox is a started sandbox whose removes Docker refuses as a zombie's.
func zombieSandbox(t *testing.T) (*Orchestrator, *fakeDocker) {
	t.Helper()
	o, fd := sandboxOrch()
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	every := zombieRetryEvery
	zombieRetryEvery = time.Millisecond
	t.Cleanup(func() { zombieRetryEvery = every })
	fd.fail["ContainerRemove"] = errors.New(zombieErr)
	return o, fd
}

func TestARemovalRetriesASandboxWhoseVMIsStillExiting(t *testing.T) {
	o, fd := zombieSandbox(t)
	removes := 0
	fd.onCall = func(method string) {
		if method == "ContainerRemove" {
			removes++
			if removes == 3 {
				fd.mu.Lock()
				delete(fd.fail, "ContainerRemove")
				fd.mu.Unlock()
			}
		}
	}
	if err := o.RemoveSandbox(context.Background(), "run-1"); err != nil {
		t.Fatalf("RemoveSandbox: %v", err)
	}
	if removes != 3 {
		t.Fatalf("ContainerRemove called %d times, want 3 (two zombie refusals, then success)", removes)
	}
	if _, ok := fd.byName(SandboxName("run-1")); ok {
		t.Fatal("container still there")
	}
}

// Any other failure is returned at once.
func TestARemovalReturnsOtherRemoveErrorsAtOnce(t *testing.T) {
	o, fd := zombieSandbox(t)
	fd.fail["ContainerRemove"] = errors.New("permission denied")
	if err := o.RemoveSandbox(context.Background(), "run-1"); err == nil {
		t.Fatal("RemoveSandbox succeeded past a remove error")
	}
	if n := fd.callsTo("ContainerRemove"); n != 1 {
		t.Fatalf("ContainerRemove called %d times, want 1", n)
	}
}

// The wait is bounded by the caller's context.
func TestARemovalGivesUpWhenTheContextEnds(t *testing.T) {
	o, _ := zombieSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := o.RemoveSandbox(ctx, "run-1"); err == nil {
		t.Fatal("RemoveSandbox succeeded while every remove was refused")
	}
}
