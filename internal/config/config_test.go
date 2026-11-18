package config

import (
	"strings"
	"testing"
)

// Every sandbox is created with CAP_SYS_ADMIN, NET_ADMIN, NET_RAW and
// apparmor=unconfined. That is containment ONLY when the runtime is a
// microVM; under runc the same flags are host root for every user in it.
// The server checks the runtime itself before a sandbox starts, so it must
// refuse a non-VM runtime rather than assume one.
func TestCheckRuntimeRefusesANonVMRuntime(t *testing.T) {
	// Case and whitespace variants ARE refused: they are typos, not runtimes.
	for _, rt := range []string{"", "runc", "crun", "MVM", "kata ", "runsc", "docker-runc"} {
		t.Run("refuses "+rt, func(t *testing.T) {
			t.Setenv("CREW_ALLOW_UNSAFE_RUNTIME", "")
			if err := CheckRuntime(rt); err == nil {
				t.Fatalf("CREW_RUNTIME=%q was accepted — escape-capable caps on a non-VM runtime", rt)
			} else if !strings.Contains(err.Error(), "not a VM runtime") {
				t.Errorf("unhelpful error for %q: %v", rt, err)
			}
		})
	}
	for _, rt := range []string{"mvm", "kata", "kata-qemu", "kata-clh"} {
		t.Run("accepts "+rt, func(t *testing.T) {
			t.Setenv("CREW_ALLOW_UNSAFE_RUNTIME", "")
			if err := CheckRuntime(rt); err != nil {
				t.Errorf("VM runtime %q was refused: %v", rt, err)
			}
		})
	}
	// The override exists so a deliberate operator is not locked out, but it
	// has to be explicit — not a typo-friendly default.
	t.Run("explicit override is honoured", func(t *testing.T) {
		t.Setenv("CREW_ALLOW_UNSAFE_RUNTIME", "1")
		if err := CheckRuntime("runc"); err != nil {
			t.Errorf("explicit override still refused: %v", err)
		}
	})
}
