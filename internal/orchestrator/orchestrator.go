// Package orchestrator starts, keeps apart, runs in and removes Crew's sandboxes: Kata microVMs
// through the Docker SDK, each on a bridge of its own (design 13.3, 13.6). The microVM's container
// configuration came from styrman-crew's orchestrator, which ported it from devbox's shell launcher:
// capabilities instead of --privileged, explicit DNS, apparmor unconfined.
package orchestrator

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/kesokaj/crew/internal/config"
)

// Orchestrator starts, adopts, and removes sandbox microVMs.
type Orchestrator struct {
	docker dockerAPI // *client.Client in production; a fake in tests (docker.go)
	cfg    *config.Config
	logger *slog.Logger

	// hostMu guards host, the machine's size, read from the daemon on first
	// need and kept once known (resources.go).
	hostMu sync.Mutex
	host   Host

	// OnBoot hears of every sandbox start that reached Docker's start (21, D10): false with why when
	// the microVM did not boot, true once its guest was ready. Nil hears nothing.
	OnBoot func(ok bool, why string)
	// readyTimeout is how long a guest has to become ready; zero is 90 seconds.
	readyTimeout time.Duration

	// doors are the server's addresses on sandbox bridges (sandbox_doors.go).
	doors sandboxDoors
}

// New creates an Orchestrator using the default Docker socket.
func New(cfg *config.Config, logger *slog.Logger) (*Orchestrator, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Orchestrator{docker: cli, cfg: cfg, logger: logger}, nil
}

// brokerIPForSubnet returns the fixed broker host address for a bridge:
// broadcast-1 (e.g. 172.19.255.254 for 172.19.0.0/16) — high in the subnet,
// clear of Docker's low-end dynamic allocations.
func brokerIPForSubnet(n *net.IPNet) net.IP {
	ip4 := n.IP.To4()
	if ip4 == nil {
		return nil
	}
	bcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bcast[i] = ip4[i] | ^n.Mask[i]
	}
	v := binary.BigEndian.Uint32(bcast) - 1
	out := make(net.IP, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

// bridgeSubnet returns the first IPv4 subnet of a sandbox's bridge.
func (o *Orchestrator) bridgeSubnet(ctx context.Context, netName string) *net.IPNet {
	insp, err := o.docker.NetworkInspect(ctx, netName, network.InspectOptions{})
	if err != nil {
		return nil
	}
	return subnetOf(insp)
}

// waitReady polls a sandbox for the entrypoint's ready marker, with Exec and its owner check.
func (o *Orchestrator) waitReady(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		code, _, err := o.Exec(ctx, containerID, ExecSpec{Cmd: []string{"test", "-f", "/run/crew/ready"}})
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout after %s", timeout)
}

// zombieRetryEvery is how often forceRemove retries a force-remove that
// Docker refused because the VM's process is exiting (a variable for tests).
var zombieRetryEvery = 2 * time.Second

// forceRemove force-removes a container, waiting out the window in which a
// Kata VM is exiting. A Kata container's PID is its cloud-hypervisor
// process; between that process exiting and the shim reaping it, Docker
// refuses the kill with "PID … is zombie and can not be killed" instead of
// waiting. A microVM sized to the whole host (styrman-crew's per-project VMs)
// outlasted Docker's own wait often enough that a delete failed two runs in
// three. Any other error is returned at once; the wait ends with ctx.
func (o *Orchestrator) forceRemove(ctx context.Context, name string) error {
	for {
		err := o.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
		if err == nil || errdefs.IsNotFound(err) {
			return nil
		}
		if !strings.Contains(err.Error(), "is zombie") {
			return err
		}
		o.logger.Info("container's VM is still exiting; retrying the remove", "container", name)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (gave up waiting for the VM to exit: %v)", err, ctx.Err())
		case <-time.After(zombieRetryEvery):
		}
	}
}

// Ping cheaply checks that the Docker daemon is reachable — used by the health
// probe, which is unauthenticated, so it must stay lightweight.
func (o *Orchestrator) Ping(ctx context.Context) error {
	_, err := o.docker.Ping(ctx)
	return err
}
