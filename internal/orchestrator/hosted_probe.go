package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/kesokaj/crew/internal/config"
)

// The probe's outcomes, from `timeout`: 0 connected, 124 silence, which is the firewall's drop.
const (
	probeConnected = 0
	probeSilent    = 124
	probeSeconds   = "3"
)

// probeTarget is one connection the probe tries, and what it must get: silence, any failure, or an
// answer.
type probeTarget struct {
	addr string // host/port
	want string // "silent", "fails", "answers"
}

// ProbeHosted proves a new hosted bridge's firewall before the app starts on it (plan M19 D22): a
// microVM of the runtime image at the probe's address, as crew's uid with no capability, tries the
// server's and the ingress's addresses, which only the firewall's drop leaves silent, the machine on
// the bridge, two private addresses, and on app- one address on the internet, which must answer; on
// apo- that must fail too. Any other outcome, or a probe that cannot run, is an error; the probe is
// removed either way.
func (o *Orchestrator) ProbeHosted(ctx context.Context, id string, n HostedNet) error {
	bridge, err := o.bridge(ctx, n.Name)
	if err != nil {
		return err
	}
	mode := modeOf(bridge)
	if !slices.Contains(config.HostedModes, mode) || n.Probe == "" {
		return fmt.Errorf("the bridge %s is no hosted bridge", n.Name)
	}
	pid := id + "-probe"
	if !sandboxIDRe.MatchString(pid) {
		return fmt.Errorf("sandbox id %q is not one", pid)
	}
	name := SandboxName(pid)
	// The probe is removed by the ID its create gave; a create that failed removes only a probe of
	// this server's left under the name from before, never one unlabelled or another's (BUG-018).
	var made string
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		cid := made
		if cid == "" {
			cid = o.leftContainer(rctx, name)
		}
		if cid == "" {
			return
		}
		if err := o.forceRemove(rctx, cid); err != nil {
			o.logger.Warn("removing a hosted bridge's probe", "sandbox", pid, "error", err)
		}
	}()
	resp, err := o.docker.ContainerCreate(ctx,
		&container.Config{Image: o.cfg.Image, Entrypoint: []string{"sleep", "300"}, Hostname: "probe",
			User:   strconv.Itoa(o.cfg.UID) + ":" + strconv.Itoa(o.cfg.GID),
			Labels: o.ownLabels(pid, "host-probe")},
		&container.HostConfig{Runtime: o.cfg.Runtime, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			DNS: []string{"8.8.8.8"}, RestartPolicy: container.RestartPolicy{Name: "no"}},
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			bridge.ID: {IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: n.Probe}},
		}},
		nil, name)
	if err != nil {
		return fmt.Errorf("the hosted bridge's probe: %w", err)
	}
	made = resp.ID
	if err := o.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("the hosted bridge's probe did not start: %s", cutWhy(err.Error()))
	}
	gw := n.Server[:strings.LastIndex(n.Server, ".")] + ".1"
	targets := []probeTarget{{gw + "/22", "silent"}, {n.Ingress + "/80", "silent"}, {n.Ingress + "/443", "silent"},
		{n.Server + "/80", "silent"}, {n.Server + "/443", "silent"}, {"192.168.0.1/80", "fails"}, {"10.0.0.1/80", "fails"}}
	for _, p := range []string{brokerPort(o.cfg.Addr), brokerPort(o.cfg.BrokerAddr)} {
		if p := strings.TrimPrefix(p, ":"); p != "" && !slices.ContainsFunc(targets, func(t probeTarget) bool { return t.addr == n.Server+"/"+p }) {
			targets = append(targets, probeTarget{n.Server + "/" + p, "silent"})
		}
	}
	internet := probeTarget{"1.1.1.1/443", "fails"}
	if mode == config.ModeHostedInternet {
		internet.want = "answers"
	}
	targets = append(targets, internet)
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out, err := o.Exec(ctx, resp.ID, ExecSpec{Cmd: []string{"timeout", probeSeconds, "bash", "-c", "exec 3<>/dev/tcp/" + t.addr}})
			at := strings.Replace(t.addr, "/", ":", 1)
			switch {
			case err != nil:
				errs[i] = fmt.Errorf("the probe of the hosted bridge could not run: %w", err)
			case code == 126 || code == 127:
				errs[i] = fmt.Errorf("the probe of the hosted bridge could not run (exit %d): %s", code, strings.TrimSpace(out))
			case t.want == "silent" && code != probeSilent:
				errs[i] = fmt.Errorf("%s answered the probe (exit %d)", at, code)
			case t.want == "fails" && code == probeConnected:
				errs[i] = fmt.Errorf("the probe reached %s", at)
			case t.want == "answers" && code != probeConnected:
				errs[i] = fmt.Errorf("the probe did not reach %s on the internet (exit %d)", at, code)
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("the firewall of the hosted bridge is not in place: %w", err)
	}
	return nil
}
