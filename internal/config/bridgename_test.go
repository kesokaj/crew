package config

import (
	"slices"
	"strings"
	"testing"
)

// goldenNet is the network name the golden vector hashes: deploy/firewall/test.sh's
// `printf 'sdb-%s' "$uuid"`. The firewall matches only a bridge's prefix; the vector
// keeps the Go and the shell derivations of the eleven hex from drifting apart.
const goldenNet = "sdb-3e6a772c-b177-4cfc-b209-b14db514bac2"

// TestBridgeNameGoldenVector pins the interface-name derivation to the
// same vector deploy/firewall/test.sh asserts in shell
// (`printf 'sdb-%s' "$uuid" | sha1sum | cut -c1-11`). The shell side
// uses printf, never echo — echo's trailing newline changes the hash.
func TestBridgeNameGoldenVector(t *testing.T) {
	if got := BridgeName(goldenNet, ModeInternet); got != "sdb-e660d58459e" {
		t.Fatalf("BridgeName(%s, internet) = %q, want sdb-e660d58459e", goldenNet, got)
	}
	if got := BridgeName(goldenNet, ModeModelOnly); got != "mdl-e660d58459e" {
		t.Fatalf("model only bridge = %q, want mdl-e660d58459e", got)
	}
}

// TestBridgeNameModeIsThePrefix pins the whole mechanism: the mode is
// the four-character prefix and nothing else. The 11-hex suffix must be
// IDENTICAL across modes, because it hashes the network name only, and
// the kernel caps an interface name at 15 characters.
func TestBridgeNameModeIsThePrefix(t *testing.T) {
	sandbox, lan := BridgeName(goldenNet, ModeInternet), BridgeName(goldenNet, ModeLAN)
	if lan != "lan-e660d58459e" {
		t.Fatalf("lan bridge = %q, want lan-e660d58459e", lan)
	}
	if sandbox[4:] != lan[4:] {
		t.Fatalf("suffix must not depend on the mode: sandbox %q vs lan %q", sandbox, lan)
	}
	for _, m := range append(append([]string{}, SandboxModes...), HostedModes...) {
		if got := BridgeName(goldenNet, m); len(got) != 15 || got[4:] != sandbox[4:] {
			t.Fatalf("%s bridge %q: want 15 characters ending in %s", m, got, sandbox[4:])
		}
	}
}

// TestLanBridgeEscapesTheFirewallWildcard is the security property in test
// form. The root firewall jumps on `-i sdb-+`, so a LAN bridge must NOT carry
// that prefix — and, just as important, an Internet bridge must always carry it.
// If this test ever fails in the Internet direction, sandboxes that should be
// confined are silently on the LAN. An unknown mode gets Model only's prefix.
func TestLanBridgeEscapesTheFirewallWildcard(t *testing.T) {
	for _, net := range []string{
		goldenNet,
		"sdb-00000000-0000-4000-8000-000000000000",
		"crew-run-ffffffff",
	} {
		if got := BridgeName(net, ModeInternet); !strings.HasPrefix(got, "sdb-") {
			t.Fatalf("internet bridge %q for %s escapes the firewall wildcard — the sandbox would be unconfined", got, net)
		}
		if got := BridgeName(net, ModeLAN); strings.HasPrefix(got, "sdb-") {
			t.Fatalf("lan bridge %q for %s still matches `-i sdb-+` — LAN mode would do nothing", got, net)
		}
		if got := BridgeName(net, "no such mode"); !strings.HasPrefix(got, "mdl-") {
			t.Fatalf("an unknown mode gave %q, not Model only's prefix", got)
		}
	}
}

// A Sources sandbox, the build of a release (13.6, since 0.51), has a prefix of its own, src-, which
// the firewall sends to the Model only chain, and the same suffix as every mode.
func TestTheSourcesBridgeHasItsOwnPrefix(t *testing.T) {
	net := goldenNet
	if got := BridgeName(net, ModeSources); got != "src-e660d58459e" {
		t.Fatalf("sources bridge = %q, want src-e660d58459e", got)
	}
	if slices.Contains(Modes, ModeSources) {
		t.Fatal("Sources is among the modes a project or a ticket may choose: it is the build's alone")
	}
	if !slices.Contains(SandboxModes, ModeSources) || !slices.Contains(SandboxModes, ModeModelOnly) {
		t.Fatalf("SandboxModes = %v", SandboxModes)
	}
}

// A hosted project's bridge has a kind of its own (19.4, plan M19 D9): app- with the internet, apo-
// without, the same eleven hex as any mode, and neither mode is one a project, a ticket, a check or
// any other sandbox may ask for (D24).
func TestHostedBridgesHaveTheirOwnPrefixes(t *testing.T) {
	net := goldenNet
	if got := BridgeName(net, ModeHostedInternet); got != "app-e660d58459e" {
		t.Fatalf("with the internet: %q", got)
	}
	if got := BridgeName(net, ModeHostedOffline); got != "apo-e660d58459e" {
		t.Fatalf("without: %q", got)
	}
	for _, m := range HostedModes {
		if slices.Contains(Modes, m) || slices.Contains(SandboxModes, m) {
			t.Fatalf("%s is a mode a sandbox may ask for", m)
		}
	}
}
