package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/kesokaj/crew/internal/config"
)

// HostedSpec is a hosted project's microVM (19.3, plan M19 Interfaces): its own image, as Crew's uid
// with no capability, on the bridge CreateHostedNet made, at the app's fixed address there.
type HostedSpec struct {
	ID      string // host-<first 12 hex of the project's id>-<deploy number>
	Project string // the project's id, a label
	Image   string // the image's id, sha256:…
	Port    int
	// DataDir is the project's data folder on the machine, DataMount where the image wants it.
	DataDir, DataMount string
	// Env are the project's values, NAME=value; PORT is Crew's and added here.
	Env    []string
	Sizing Sizing
	Net    HostedNet
}

// HostedPids bounds the processes of a hosted microVM, and HostedTmp its /tmp.
const (
	HostedPids = 4096
	HostedTmp  = "size=256m"
)

// StartHosted starts a hosted project's microVM on its bridge (19.3). It runs the project's image as
// the uid and gid of the machine's user crew, never root, with every capability dropped and no new
// privilege, with no published port, so it is reached only on its bridge, where the ingress is. It
// starts again with Docker, and holds the data folder as its one bind. No CREW_ value reaches it.
// Nothing of Crew runs in it, so there is no guest to wait for: the health address says when it
// serves. A microVM that was made and does not start is removed, by its ID; its bridge is the
// caller's.
func (o *Orchestrator) StartHosted(ctx context.Context, spec HostedSpec) (sb Sandbox, err error) {
	if !sandboxIDRe.MatchString(spec.ID) || spec.Net.Name != SandboxName(spec.ID) || spec.Net.App == "" {
		return Sandbox{}, fmt.Errorf("the hosted sandbox %q has no bridge of its own", spec.ID)
	}
	if !strings.HasPrefix(spec.Image, "sha256:") || spec.Port < 1024 || spec.Port > 65535 || spec.DataDir == "" || !strings.HasPrefix(spec.DataMount, "/") {
		return Sandbox{}, fmt.Errorf("the hosted sandbox %s needs an image by its id, a port of 1024 or more, and a data folder", spec.ID)
	}
	env := []string{"PORT=" + strconv.Itoa(spec.Port)}
	for _, kv := range spec.Env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "PORT" || strings.HasPrefix(k, "CREW_") {
			return Sandbox{}, fmt.Errorf("the hosted sandbox %s: the value %q is not the operator's to give", spec.ID, k)
		}
		env = append(env, kv)
	}
	name := spec.Net.Name
	bridge, err := o.bridge(ctx, name)
	if err != nil {
		return Sandbox{}, err
	}
	mode := modeOf(bridge)
	if !slices.Contains(config.HostedModes, mode) {
		return Sandbox{}, fmt.Errorf("the bridge %s is no hosted bridge", name)
	}
	// The /24 the door counts as the guest's is the bridge's own, and the addresses the project was
	// given are that bridge's: a stored spec that drifted from it would enter another /24 (13.6).
	subnet := subnetOf(bridge)
	if subnet == nil {
		return Sandbox{}, fmt.Errorf("the bridge %s has no IPv4 subnet", name)
	}
	if want, aerr := hostedAddrs(subnet); aerr != nil || want.Server != spec.Net.Server || want.Ingress != spec.Net.Ingress || want.App != spec.Net.App || want.Probe != spec.Net.Probe {
		return Sandbox{}, fmt.Errorf("the hosted sandbox %s was given addresses that are not those of its bridge's subnet %s", spec.ID, subnet)
	}
	sb = Sandbox{ID: spec.ID, Kind: "hosted", Container: name, Network: name, BrokerIP: spec.Net.Server, IP: spec.Net.App, Mode: mode}
	// A create that failed made nothing to remove: the name may be another's (security review of b39f701).
	var made string
	defer func() {
		if err != nil && made != "" {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if rerr := o.forceRemove(rctx, made); rerr != nil {
				o.logger.Warn("removing a hosted microVM that did not start", "sandbox", spec.ID, "error", rerr)
			}
		}
	}()
	pids := int64(HostedPids)
	hc := &container.HostConfig{
		Runtime:       o.cfg.Runtime,
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		DNS:           []string{"8.8.8.8", "1.1.1.1"},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{NanoCPUs: spec.Sizing.NanoCPUs, Memory: spec.Sizing.MemBytes, PidsLimit: &pids},
		Binds:         []string{spec.DataDir + ":" + spec.DataMount},
		Tmpfs:         map[string]string{"/tmp": HostedTmp},
	}
	labels := o.ownLabels(spec.ID, "hosted")
	labels["crew.project"] = spec.Project
	// The bridge may have been made by an earlier start: its door and subnet are known before the app runs.
	o.doors.enter(spec.ID, spec.Net.Server, subnet)
	resp, err := o.docker.ContainerCreate(ctx,
		&container.Config{Image: spec.Image, Hostname: "app", User: strconv.Itoa(o.cfg.UID) + ":" + strconv.Itoa(o.cfg.GID), Env: env, Labels: labels},
		hc,
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			bridge.ID: {IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: spec.Net.App}},
		}},
		nil, name)
	if err != nil {
		return sb, fmt.Errorf("creating the hosted microVM: %w", err)
	}
	made = resp.ID
	if err := o.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		if ctx.Err() != nil {
			return sb, fmt.Errorf("starting the hosted microVM: %w", err)
		}
		why := cutWhy(err.Error())
		o.boot(false, why)
		return sb, fmt.Errorf("starting the hosted microVM: %w: %s", ErrNoBoot, why)
	}
	o.boot(true, "")
	o.doors.put(spec.ID, spec.Net.Server, spec.Net.App, mode)
	return sb, nil
}
