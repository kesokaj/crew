package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// clearFailedStart removes what a start that failed left under a sandbox's name: what its creates
// made, by the IDs they gave, and a container or bridge from before only when its labels name this
// server, so a start removes nothing unlabelled or another's under the name (BUG-018; security review
// of the release of 0.3.3, finding 1). Its doors go with it.
func (o *Orchestrator) clearFailedStart(ctx context.Context, id, cid, bridgeID string) {
	name := SandboxName(id)
	o.doors.drop(id)
	if cid == "" {
		cid = o.leftContainer(ctx, name)
	}
	if cid != "" {
		if err := o.forceRemove(ctx, cid); err != nil {
			o.logger.Warn("removing a sandbox that did not start", "sandbox", id, "error", err)
		}
	}
	if bridgeID == "" {
		if n, err := o.docker.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil && o.labelledOurs(n.Labels) {
			bridgeID = n.ID
		}
	}
	if bridgeID != "" {
		if err := o.dropBridge(ctx, id, bridgeID); err != nil {
			o.logger.Warn("removing the network of a sandbox that did not start", "sandbox", id, "error", err)
		}
	}
}

// leftContainer is the ID of the container under a name whose labels name this server, else "".
func (o *Orchestrator) leftContainer(ctx context.Context, name string) string {
	insp, err := o.docker.ContainerInspect(ctx, name)
	if err != nil || insp.Config == nil || !o.labelledOurs(insp.Config.Labels) {
		return ""
	}
	return insp.ID
}

// dropBridge takes the server, and the ingress, which a hosted project's bridge has too (19.4), off
// a sandbox's bridge and removes it, by its ID: Docker removes no network a container is still on.
func (o *Orchestrator) dropBridge(ctx context.Context, id, bridgeID string) error {
	for _, c := range []string{o.cfg.SelfContainer, o.cfg.IngressContainer} {
		if c == "" {
			continue
		}
		if err := o.docker.NetworkDisconnect(ctx, bridgeID, c, true); err != nil && !errdefs.IsNotFound(err) &&
			!strings.Contains(err.Error(), "is not connected") {
			o.logger.Warn("leaving a sandbox's network", "sandbox", id, "container", c, "error", err)
		}
	}
	if err := o.docker.NetworkRemove(ctx, bridgeID); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("removing the sandbox's network: %w", err)
	}
	return nil
}
