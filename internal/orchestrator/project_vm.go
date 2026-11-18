package orchestrator

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/docker/docker/errdefs"
)

// ContainerStats holds a sandbox container's live CPU and memory usage (design 1.38, 15.4).
type ContainerStats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsedMB  int64   `json:"memUsedMB"`
	MemLimitMB int64   `json:"memLimitMB"`
}

// PauseSandbox suspends a project's warm Kata microVM (`ContainerPause`, 0% CPU) after verifying
// that the container belongs to this server (design 1.37, 13.3).
func (o *Orchestrator) PauseSandbox(ctx context.Context, id string) error {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return err
	}
	return o.docker.ContainerPause(ctx, cid)
}

// UnpauseSandbox resumes a suspended project Kata microVM (`ContainerUnpause`) after verifying
// that the container belongs to this server (design 1.37, 13.3).
func (o *Orchestrator) UnpauseSandbox(ctx context.Context, id string) error {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return err
	}
	return o.docker.ContainerUnpause(ctx, cid)
}

// SandboxPaused reports whether a project's Kata microVM is currently paused in Docker.
func (o *Orchestrator) SandboxPaused(ctx context.Context, id string) (bool, error) {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	insp, err := o.docker.ContainerInspect(ctx, cid)
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return insp.State != nil && insp.State.Paused, nil
}

// SandboxStats reads a sandbox container's one-shot CPU and memory stats from Docker after
// verifying that the container belongs to this server (design 1.38, 15.4).
func (o *Orchestrator) SandboxStats(ctx context.Context, idOrContainer string) (ContainerStats, error) {
	name := idOrContainer
	if !strings.HasPrefix(name, "crew-") {
		name = SandboxName(name)
	}
	cid, err := o.sandboxTarget(ctx, name)
	if err != nil {
		return ContainerStats{}, err
	}
	sr, err := o.docker.ContainerStatsOneShot(ctx, cid)
	if err != nil {
		return ContainerStats{}, err
	}
	defer func() { _ = sr.Body.Close() }()
	var raw struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Limit uint64            `json:"limit"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(sr.Body).Decode(&raw); err != nil {
		return ContainerStats{}, err
	}
	var cpuPct float64
	cpuDelta := float64(raw.CPUStats.CPUUsage.TotalUsage) - float64(raw.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(raw.CPUStats.SystemUsage) - float64(raw.PreCPUStats.SystemUsage)
	cpus := float64(raw.CPUStats.OnlineCPUs)
	if cpus <= 0 {
		cpus = 1
	}
	if sysDelta > 0 && cpuDelta > 0 {
		cpuPct = math.Round((cpuDelta/sysDelta)*cpus*100.0*10) / 10
	}
	usage := raw.MemoryStats.Usage
	if inactive := raw.MemoryStats.Stats["inactive_file"]; inactive < usage {
		usage -= inactive
	}
	return ContainerStats{
		CPUPercent: cpuPct,
		MemUsedMB:  int64(usage) / (1024 * 1024),
		MemLimitMB: int64(raw.MemoryStats.Limit) / (1024 * 1024),
	}, nil
}
