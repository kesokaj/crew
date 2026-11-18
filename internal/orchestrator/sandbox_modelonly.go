package orchestrator

import (
	"context"
	"fmt"
	"strings"
)

// modelOnlyProbes are what a Model only sandbox must not reach: an address on the internet, and the
// machine itself at its address on the bridge. Each is a connection from inside the microVM, so it
// tests the rules that hold for this sandbox, not a record of them.
var modelOnlyProbes = []string{"1.1.1.1/443", "8.8.8.8/53"}

// proveModelOnly is the start-up test of the rules (13.6): no Model only sandbox is used while the
// firewall lets it reach the internet or the machine. A probe that connects is a failure; one that
// times out or is refused is what the rules do. A probe that cannot be run at all fails too.
func (o *Orchestrator) proveModelOnly(ctx context.Context, containerID, door, prefix string) error {
	probe := func(p string) (int, string, error) {
		return o.Exec(ctx, containerID, ExecSpec{Cmd: []string{"timeout", "4", "bash", "-c", "exec 3<>/dev/tcp/" + p}})
	}
	// The probe itself works: it reaches the sandbox's own door on the server, the one place Model
	// only lets it reach. A probe that cannot connect anywhere proves nothing (review of M10).
	host, port, _ := strings.Cut(door, ":")
	if code, out, err := probe(host + "/" + port); err != nil || code != 0 {
		return fmt.Errorf("the start-up test of Model only could not run: the sandbox did not reach its own door (exit %d %v): %s", code, err, strings.TrimSpace(out))
	}
	for _, p := range modelOnlyProbes {
		code, out, err := probe(p)
		if err != nil {
			return fmt.Errorf("the start-up test of Model only could not run: %w", err)
		}
		if code == 0 {
			return fmt.Errorf("the start-up test of Model only failed: the sandbox reached %s, so the firewall's %s rules are missing", strings.Replace(p, "/", ":", 1), prefix)
		}
		if code == 126 || code == 127 {
			return fmt.Errorf("the start-up test of Model only could not run (exit %d): %s", code, strings.TrimSpace(out))
		}
	}
	// The machine itself: only silence is the firewall's drop. A refusal came from the machine, which
	// the packet reached (review of M10).
	if gw := o.bridgeGateway(ctx, containerID); gw != "" {
		code, _, err := probe(gw + "/22")
		if err != nil {
			return fmt.Errorf("the start-up test of Model only could not run: %w", err)
		}
		if code != 124 {
			return fmt.Errorf("the start-up test of Model only failed: the machine at %s:22 answered (exit %d), so the firewall's %s rules are missing", gw, code, prefix)
		}
	}
	return nil
}

// bridgeGateway is the machine's address on the sandbox's bridge, or "" when Docker does not say.
func (o *Orchestrator) bridgeGateway(ctx context.Context, containerID string) string {
	insp, err := o.docker.ContainerInspect(ctx, containerID)
	if err != nil || insp.NetworkSettings == nil {
		return ""
	}
	for _, n := range insp.NetworkSettings.Networks {
		if n != nil && n.Gateway != "" {
			return n.Gateway
		}
	}
	return ""
}
