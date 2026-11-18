package orchestrator

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/network"

	"github.com/kesokaj/crew/internal/config"
)

// A sandbox's bridge has its name and its labels: what is done to it is done by the ID its check read
// (security review of b39f701, finding 1).

// bridge is a sandbox's bridge, read with the check of whose it is: another server's, or one with no
// sandbox label, which every sandbox's bridge has (since M2), is ErrAnothers, one Docker does not have the inspect's not-found error, and a check that errors its error. What is
// done to it is done by the ID read here, so a bridge made under the name after the check is not
// touched.
func (o *Orchestrator) bridge(ctx context.Context, name string) (network.Inspect, error) {
	n, err := o.docker.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return network.Inspect{}, fmt.Errorf("reading whose the bridge %s is: %w", name, err)
	}
	if o.notOurs(n.Labels) {
		return network.Inspect{}, ErrAnothers
	}
	return n, nil
}

// subnetOf is the first IPv4 subnet of a bridge read.
func subnetOf(insp network.Inspect) *net.IPNet {
	for _, cfg := range insp.IPAM.Config {
		if _, subnet, perr := net.ParseCIDR(cfg.Subnet); perr == nil && subnet.IP.To4() != nil {
			return subnet
		}
	}
	return nil
}

// modeOf is the network mode of a sandbox's bridge read, from the prefix of its name; a bridge with
// no prefix Crew gives is Model only's, the mode that lets the least through.
func modeOf(insp network.Inspect) string {
	for _, m := range append(slices.Clone(config.SandboxModes), config.HostedModes...) {
		if bridge := insp.Options["com.docker.network.bridge.name"]; strings.HasPrefix(bridge, config.BridgeName(insp.Name, m)[:4]) {
			return m
		}
	}
	return config.ModeModelOnly
}
