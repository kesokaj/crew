package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// IPForwardProc is the kernel's IPv4 forwarding switch as the server
// container sees it.
const IPForwardProc = "/proc/sys/net/ipv4/ip_forward"

// IPForwardOff reports whether IPv4 forwarding is off in the network
// namespace this process runs in, reading the sysctl at procPath (the
// IPForwardProc constant in production; a test hands it a file).
//
// Why the server cares: it joins every sandbox's bridge at the door's
// address, so with forwarding on its netns could route between two bridges,
// and out of a Model only one, and void the isolation of 13.6. The compose
// deploy starts the server's container with `net.ipv4.ip_forward=0`, and
// openMachine (cmd/crew/machine.go) starts no sandbox when CheckIPForward
// refuses. Only a containerized server is checked: one that is not starts no
// sandbox at all.
func IPForwardOff(procPath string) (bool, error) {
	b, err := os.ReadFile(procPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", procPath, err)
	}
	return strings.TrimSpace(string(b)) == "0", nil
}

// IPForwardMustRefuse reports whether a CONTAINERIZED server must refuse to
// start, given what the sysctl read returned and whether the operator set the
// override. It exists as its own function because the rule is a security
// decision and main() is not reachable from a test.
//
// Any error refuses, including fs.ErrNotExist. Until 2026-09-22 a missing
// file warned and continued, on the reasoning that an exotic kernel should
// not take the service down; that made the one isolation guard
// report "fine" in the single case where it had measured nothing: a fail-open.
// Inside a Linux container that file is effectively
// always present, so its absence is a reason to look, not to proceed. The
// operator keeps a documented way through: CREW_ALLOW_IP_FORWARD=1.
func IPForwardMustRefuse(off bool, err error, override bool) bool {
	if override {
		return false
	}
	if err != nil {
		return true
	}
	return !off
}

// EnvAllowIPForward set to 1 is the operator's deliberate override of CheckIPForward.
const EnvAllowIPForward = "CREW_ALLOW_IP_FORWARD"

// CheckIPForward is the guard a containerized server passes before it can start a sandbox (13.6):
// an error when the sysctl at procPath is not "0", or cannot be read, unless override is set.
func CheckIPForward(procPath string, override bool) error {
	off, err := IPForwardOff(procPath)
	if !IPForwardMustRefuse(off, err, override) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the server cannot tell whether its network forwards, so no sandbox can start: %w", err)
	}
	return errors.New("the server's network forwards IPv4 (net.ipv4.ip_forward is not 0), so no sandbox can start: it would route between the sandboxes' bridges")
}
