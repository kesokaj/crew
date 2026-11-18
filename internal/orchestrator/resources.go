package orchestrator

import (
	"context"
	"fmt"
	"time"
)

// A microVM's size. Every microVM has 1 vCPU and 2 GiB beside its
// container's limits, and the workload is capped at exactly the limit
// through the guest cgroup. With Kata 3.31 that was its base VM
// (configuration-clh.toml: default_vcpus = 1, default_memory = 2048), to
// which it hot-added the limits; since design 0.68 Kata 4's runtime-rs sizes
// the VM once, as the limits plus overhead_vcpus and overhead_memory, which
// Crew's drop-in sets to 1 and 2048 (deploy/lib/kata-config.sh). An UNSET
// limit is not "unlimited": it is the 1 vCPU / 2 GiB. So "unlimited" is
// spelled out as the host's own size, measured with 3.31 on the NUC
// 2026-10-05:
//   - vCPUs clamp at the host's count, and Docker refuses NanoCPUs above it
//     ("range of CPUs is from 0.01 to 16.00");
//   - memory does NOT clamp: base + limit, rounded UP to the 128 MiB hotplug
//     block, above host RAM fails the start ("The requested hotplug memory
//     addition is not a valid size").
// The install test's phase kata4 starts Kata 4 at the whole host's size: on
// an 8-CPU, 32 GiB VM, --cpus 8 and a 29952 MiB limit booted 9 vCPUs (the
// overhead's one above the host's count) and a guest of 31361 MiB.

// hostProbeTimeout bounds the one `docker info` call (plan Open row).
const hostProbeTimeout = 10 * time.Second

// Host is the machine's size as the Docker daemon reports it (`docker
// info`: NCPU, MemTotal). The daemon's count is the one it validates
// NanoCPUs against, and it is the same on every platform the server runs on.
type Host struct{ CPUs, MemMiB int }

// Sizing is what a container is, or would be, created with.
type Sizing struct {
	NanoCPUs int64 // HostConfig.NanoCPUs
	MemBytes int64 // HostConfig.Memory; 0 = no limit
}

// Host returns the machine's size, asking the daemon on first need and
// caching the answer only once it succeeds: a failure is returned and the
// next call asks again (a slow dockerd after a reboot must not leave the
// server sizing at zero forever, and must not block startup either).
func (o *Orchestrator) Host(ctx context.Context) (Host, error) {
	o.hostMu.Lock()
	defer o.hostMu.Unlock()
	if o.host.CPUs > 0 {
		return o.host, nil
	}
	ctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()
	info, err := o.docker.Info(ctx)
	if err != nil {
		return Host{}, fmt.Errorf("host size: docker info: %w", err)
	}
	if info.NCPU <= 0 || info.MemTotal <= 0 {
		return Host{}, fmt.Errorf("host size: docker info reported %d CPUs and %d bytes of memory", info.NCPU, info.MemTotal)
	}
	o.host = Host{CPUs: info.NCPU, MemMiB: int(info.MemTotal >> 20)}
	return o.host, nil
}
