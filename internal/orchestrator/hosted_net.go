package orchestrator

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"

	"github.com/kesokaj/crew/internal/config"
)

// HostedNet is a hosted project's bridge and the fixed addresses on it (19.4, plan M19 D27, D39): the
// firewall lets the server's and the ingress's open a connection there, and nothing else.
type HostedNet struct {
	Name    string // the network's and the container's name, crew-<id>
	Server  string // .254 of the /24
	Ingress string // .253
	App     string // .252
	Probe   string // .251
}

// sandboxPool is the machine's pool of bridges (the host setup's default-address-pools): the
// firewall's rules for a hosted bridge name the server and the ingress by their place in its /24s.
var sandboxPool = &net.IPNet{IP: net.IPv4(172, 16, 0, 0).To4(), Mask: net.CIDRMask(12, 32)}

// hostedAddrs are the fixed addresses of a hosted bridge's subnet, which must be a /24 of the pool.
func hostedAddrs(subnet *net.IPNet) (HostedNet, error) {
	ones, bits := subnet.Mask.Size()
	ip := subnet.IP.To4()
	if ip == nil || ones != 24 || bits != 32 || !sandboxPool.Contains(ip) {
		return HostedNet{}, fmt.Errorf("the bridge's subnet %s is not a /24 of 172.16.0.0/12, which the firewall's rules for a hosted bridge need", subnet)
	}
	at := func(last byte) string {
		v := binary.BigEndian.Uint32(ip)&0xffffff00 | uint32(last)
		out := make(net.IP, 4)
		binary.BigEndian.PutUint32(out, v)
		return out.String()
	}
	return HostedNet{Server: at(254), Ingress: at(253), App: at(252), Probe: at(251)}, nil
}

// CreateHostedNet makes a hosted project's bridge, app- with the internet or apo- without, checks its
// subnet, joins the server and the ingress at their fixed addresses, and makes the server's address
// there a sandbox's door, so a request that arrives on it is not the operator's (20.3). A bridge that
// cannot be made so is removed again.
func (o *Orchestrator) CreateHostedNet(ctx context.Context, id string, internet bool) (n HostedNet, err error) {
	if !sandboxIDRe.MatchString(id) {
		return HostedNet{}, fmt.Errorf("sandbox id %q is not one", id)
	}
	if o.cfg.SelfContainer == "" || o.cfg.IngressContainer == "" {
		return HostedNet{}, fmt.Errorf("a hosted bridge needs the server's and the ingress's containers")
	}
	mode := config.ModeHostedOffline
	if internet {
		mode = config.ModeHostedInternet
	}
	name := SandboxName(id)
	var bridgeID string
	defer func() {
		if err != nil {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			o.clearFailedStart(rctx, id, "", bridgeID)
		}
	}()
	labels := o.ownLabels(id, "hosted")
	bridge, err := o.docker.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:  "bridge",
		Options: map[string]string{"com.docker.network.bridge.name": config.BridgeName(name, mode)},
		Labels:  labels,
	})
	if err != nil {
		return HostedNet{}, fmt.Errorf("the hosted bridge: %w", err)
	}
	bridgeID = bridge.ID
	subnet := o.bridgeSubnet(ctx, bridge.ID)
	if subnet == nil {
		return HostedNet{}, fmt.Errorf("the hosted bridge %s has no IPv4 subnet", name)
	}
	n, err = hostedAddrs(subnet)
	if err != nil {
		return HostedNet{}, err
	}
	n.Name = name
	o.doors.enter(id, n.Server, subnet)
	if err := o.docker.NetworkConnect(ctx, bridge.ID, o.cfg.SelfContainer, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: n.Server}, GwPriority: sandboxGwPriority,
	}); err != nil {
		return HostedNet{}, fmt.Errorf("joining the server to the hosted bridge: %w", err)
	}
	o.doors.put(id, n.Server, n.App, mode)
	if err := o.joinIngress(ctx, bridge.ID, n.Ingress); err != nil {
		return HostedNet{}, err
	}
	return n, nil
}

// JoinIngress joins the ingress to a hosted bridge at its fixed address, unless it is there: the
// ingress is a new container after an install, and Docker does not join it again (plan M19 D29).
func (o *Orchestrator) JoinIngress(ctx context.Context, id string) error {
	name := SandboxName(id)
	// Another server's sandbox is not joined (review of the release of M20), by its container's labels
	// and its bridge's.
	if _, err := o.sandboxTarget(ctx, name); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	bridge, err := o.bridge(ctx, name)
	if err != nil {
		return err
	}
	subnet := subnetOf(bridge)
	if subnet == nil {
		return fmt.Errorf("the hosted bridge %s has no IPv4 subnet", name)
	}
	n, err := hostedAddrs(subnet)
	if err != nil {
		return err
	}
	return o.joinIngress(ctx, bridge.ID, n.Ingress)
}

// joinIngress joins the ingress to a bridge by its ID.
func (o *Orchestrator) joinIngress(ctx context.Context, bridgeID, addr string) error {
	err := o.docker.NetworkConnect(ctx, bridgeID, o.cfg.IngressContainer, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: addr}, GwPriority: sandboxGwPriority,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("joining the ingress to the hosted bridge: %w", err)
	}
	return nil
}
