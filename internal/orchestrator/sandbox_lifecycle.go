package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// RemoveSandbox removes a sandbox's microVM and its network. Removing one that is gone is not an
// error, and one another server on the same Docker started is gone for this one: it is left as it is
// (security review of M19, round 2). One whose owner the check cannot read keeps its doors with it, so
// the broker still answers a sandbox of this server's that runs on (review of 5ddd59a).
func (o *Orchestrator) RemoveSandbox(ctx context.Context, id string) error {
	name := SandboxName(id)
	cid, err := o.sandboxTarget(ctx, name)
	switch {
	case errors.Is(err, ErrAnothers):
		o.doors.drop(id)
		o.logger.Warn("a sandbox of another server has this name; it is left as it is", "sandbox", id)
		return nil
	case errdefs.IsNotFound(err):
	case err != nil:
		return err
	}
	o.doors.drop(id)
	if cid != "" {
		if err := o.forceRemove(ctx, cid); err != nil {
			return err
		}
	}
	bridge, err := o.bridge(ctx, name)
	switch {
	case errdefs.IsNotFound(err):
		return nil
	case errors.Is(err, ErrAnothers):
		o.logger.Warn("a bridge of another server has the sandbox's name; it is left as it is", "sandbox", id)
		return nil
	case err != nil:
		return fmt.Errorf("removing the sandbox's network: %w", err)
	}
	return o.dropBridge(ctx, id, bridge.ID)
}

// CutSandbox disconnects a sandbox's microVM from its bridge and drops its door, so it has no route
// out and the broker and the proxy refuse it (9.6, 13.6).
func (o *Orchestrator) CutSandbox(ctx context.Context, id string) error {
	name := SandboxName(id)
	cid, err := o.sandboxTarget(ctx, name)
	if err != nil {
		return err
	}
	bridge, err := o.bridge(ctx, name)
	if err != nil {
		return err
	}
	o.doors.drop(id)
	return o.docker.NetworkDisconnect(ctx, bridge.ID, cid, true)
}

// SandboxInfo is a sandbox Docker still has.
type SandboxInfo struct {
	ID, Kind, Container string
	Running             bool
}

// Sandboxes lists the sandboxes Docker has of this server, running or not: another server's on the
// same Docker are not listed, so Reconcile never takes them for its own orphans.
func (o *Orchestrator) Sandboxes(ctx context.Context) ([]SandboxInfo, error) {
	list, err := o.docker.ContainerList(ctx, container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", LabelSandbox)),
	})
	if err != nil {
		return nil, err
	}
	var out []SandboxInfo
	for _, c := range list {
		if !o.ours(c.Labels) {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, SandboxInfo{ID: c.Labels[LabelSandbox], Kind: c.Labels[LabelKind], Container: name, Running: c.State == "running"})
	}
	return out, nil
}

// AdoptSandbox learns the doors of a sandbox that runs from before the server started, so the
// broker answers it again. Its address counts only on the bridge the check read.
func (o *Orchestrator) AdoptSandbox(ctx context.Context, id string) error {
	name := SandboxName(id)
	cid, err := o.sandboxTarget(ctx, name)
	if err != nil {
		return err
	}
	bridge, err := o.bridge(ctx, name)
	if err != nil {
		return err
	}
	subnet := subnetOf(bridge)
	if subnet == nil {
		return fmt.Errorf("the sandbox %s has no network", id)
	}
	brokerIP := brokerIPForSubnet(subnet).String()
	o.doors.enter(id, brokerIP, subnet)
	err = o.docker.NetworkConnect(ctx, bridge.ID, o.cfg.SelfContainer, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: brokerIP}, GwPriority: sandboxGwPriority,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	insp, err := o.docker.ContainerInspect(ctx, cid)
	if err != nil {
		return err
	}
	if insp.NetworkSettings == nil || insp.NetworkSettings.Networks[name] == nil || insp.NetworkSettings.Networks[name].NetworkID != bridge.ID {
		return fmt.Errorf("the sandbox %s has no address on its network", id)
	}
	o.doors.put(id, brokerIP, insp.NetworkSettings.Networks[name].IPAddress, modeOf(bridge))
	return nil
}
