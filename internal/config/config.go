// Package config is what the orchestrator shares with the rest of Crew: a sandbox's label, its guest
// variables and bridge names, the microVM runtimes, and the check that the server forwards nothing.
package config

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LabelManaged marks containers crew owns; state recovery adopts everything
// carrying it, so labels are the state.
const LabelManaged = "crew.managed"

// Env keys passed into a sandbox (read by image/entrypoint.sh).
const (
	EnvUser       = "CREW_USER"
	EnvUID        = "CREW_UID"
	EnvGID        = "CREW_GID"
	EnvProject    = "CREW_PROJECT"
	EnvCredBroker = "CREW_CRED_BROKER" // credential broker URL on this bridge
)

// Config is what the orchestrator reads, which the server builds from its own environment
// (orchConfig in cmd/crew).
type Config struct {
	Addr     string // HTTP listen address
	Image    string // runtime image for sandboxes
	Runtime  string // docker runtime (mvm = Kata)
	UID, GID int    // host uid/gid the guest user must match

	// BrokerAddr: the credential broker's listener. Bound on all
	// interfaces so sandboxes reach it on their own bridge; NOT published to
	// the host, so it is a bridge-only, credential-only surface separate
	// from the ingress-gated main API. Empty disables the broker.
	BrokerAddr string

	// SelfContainer: when the server itself runs in a container, its name —
	// the orchestrator attaches it to each sandbox's network. Empty = host
	// process (reaches all bridges natively). A containerized server
	// starts no sandbox unless its netns forwards nothing (CheckIPForward in
	// openMachine; CREW_ALLOW_IP_FORWARD=1 is the deliberate override).
	SelfContainer string
	// IngressContainer is the ingress's container, which the server joins to each hosted project's
	// bridge (19.4, since 0.72); empty joins none.
	IngressContainer string
}

// CheckRuntime refuses a Docker runtime that is not a microVM, unless CREW_ALLOW_UNSAFE_RUNTIME=1
// says so deliberately. Every sandbox gets CAP_SYS_ADMIN and AppArmor off, which is containment only
// in a VM. The server checks it before it starts any sandbox, whichever way it was configured.
func CheckRuntime(runtime string) error {
	if vmRuntimes[runtime] {
		return nil
	}
	if os.Getenv("CREW_ALLOW_UNSAFE_RUNTIME") != "1" {
		return fmt.Errorf(
			"CREW_RUNTIME=%q is not a VM runtime (known: %s) — project containers "+
				"get CAP_SYS_ADMIN and apparmor=unconfined, which is containment only "+
				"when the runtime is a microVM; set CREW_ALLOW_UNSAFE_RUNTIME=1 to "+
				"override deliberately", runtime, strings.Join(knownVMRuntimes(), ", "))
	}
	// Deliberate override: say so loudly.
	fmt.Fprintf(os.Stderr,
		"crew: WARNING — CREW_RUNTIME=%q is not a VM runtime and "+
			"CREW_ALLOW_UNSAFE_RUNTIME=1 is set. Project containers get "+
			"CAP_SYS_ADMIN with AppArmor disabled; on a non-VM runtime that is "+
			"host root for every project user.\n", runtime)
	return nil
}

// IsVMRuntime says whether a Docker runtime is a microVM's, with no override: what must never run
// outside one, such as the check server of a release (16.4 step 3), asks this.
func IsVMRuntime(runtime string) bool { return vmRuntimes[runtime] }

// vmRuntimes are the Docker runtimes that put a project in its OWN kernel.
// "mvm" is this deployment's Kata runtime name (deploy/host-setup.sh registers
// it in daemon.json); the kata-* names are the upstream defaults.
var vmRuntimes = map[string]bool{
	"mvm":          true,
	"kata":         true,
	"kata-runtime": true,
	"kata-qemu":    true,
	"kata-clh":     true,
	"kata-fc":      true,
}

// knownVMRuntimes lists them in a stable order for the error message.
func knownVMRuntimes() []string {
	out := make([]string, 0, len(vmRuntimes))
	for r := range vmRuntimes {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// The network modes of a sandbox (13.6). Each is the prefix of its bridge's name, which the root
// firewall matches.
const (
	ModeInternet  = "internet"   // sdb-: the internet, and not the machine, the local network, or another sandbox
	ModeModelOnly = "model_only" // mdl-: only the server on its own bridge, which proxies the hosts on a short list
	ModeLAN       = "lan"        // lan-: whatever the machine reaches; no firewall rule matches it
	// ModeSources is the build of a release's (since 0.51): src-, the Model only chain, with the
	// proxy's list of package sources, sandbox.sources, in place of the model's hosts.
	ModeSources = "sources"
	// ModeHostedInternet and ModeHostedOffline are a hosted project's bridges (19.4, since 0.72):
	// app- and apo-, the firewall's CREW-APP chain, on which only the server and the ingress open a
	// connection; app- reaches the internet from inside, apo- nothing (hosting.internet).
	ModeHostedInternet = "hosted_internet"
	ModeHostedOffline  = "hosted_offline"
)

// HostedModes are the hosted projects' bridges, which no project, ticket, check or other sandbox may
// ask for: only the hosted start uses them.
var HostedModes = []string{ModeHostedInternet, ModeHostedOffline}

// Modes are the network modes a project or a ticket may choose, in the order the app shows them.
var Modes = []string{ModeInternet, ModeModelOnly, ModeLAN}

// SandboxModes are the modes a sandbox may start in: Modes, and Sources, which only a sandbox that runs
// no agent has, Crew's own build of a release and a project's check (since 0.53): it lets the package
// sources through and not the model's hosts. A project's checks may choose any of these.
var SandboxModes = []string{ModeInternet, ModeModelOnly, ModeLAN, ModeSources}

// BridgeName is the bridge interface name of a sandbox's network in a mode: the mode's prefix and
// the same eleven hex of the network's name in every mode. An unknown mode gets Model only's
// prefix, the one that lets the least through.
//
//	mdl-  MODEL ONLY. The firewall drops everything it sends to the machine and everything the
//	      machine would route for it, so it reaches the containers on its own bridge, which is the
//	      server alone.
//	src-  SOURCES. The same rules as mdl-; the proxy on the bridge lets through another list.
//	app-  A HOSTED PROJECT, with the internet; apo- without (19.4).
func BridgeName(netName, mode string) string {
	sum := sha1.Sum([]byte(netName))
	prefix := "mdl-"
	switch mode {
	case ModeInternet:
		prefix = "sdb-"
	case ModeLAN:
		prefix = "lan-"
	case ModeSources:
		prefix = "src-"
	case ModeHostedInternet:
		prefix = "app-"
	case ModeHostedOffline:
		prefix = "apo-"
	}
	return prefix + hex.EncodeToString(sum[:])[:11]
}
