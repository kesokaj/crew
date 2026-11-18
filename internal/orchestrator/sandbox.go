package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/egress"
)

// A sandbox in Crew is one microVM for one purpose, removed afterwards (design 13.3): Setup, a login
// check, and from M3 a run. Each has a bridge of its own, named with the sdb- prefix that the
// firewall matches, and the server joins that bridge at a fixed address before the sandbox starts,
// so the broker answers its first request (17, "Sandboxes that cannot reach each other").

// Labels of a sandbox's container and network.
const (
	LabelSandbox = "crew.sandbox" // the sandbox's id
	LabelKind    = "crew.kind"    // setup, login-check, run, ...
	// LabelServer is the container of the server that made it: a second server on the same Docker,
	// such as a test's throwaway one, neither lists nor removes another's sandboxes (since 0.73).
	LabelServer = "crew.server"
)

// InstalledServer is the installed Crew's own container (deploy/compose.yaml): the sandboxes of a
// release before 0.73 carry no LabelServer, and are its.
const InstalledServer = "crew-server"

// ownLabels are the labels of a container or network this server makes for a sandbox.
func (o *Orchestrator) ownLabels(id, kind string) map[string]string {
	return map[string]string{config.LabelManaged: "true", LabelSandbox: id, LabelKind: kind, LabelServer: o.cfg.SelfContainer}
}

// ours says whether a sandbox's labels are this server's: its own name, or none on the installed one.
func (o *Orchestrator) ours(labels map[string]string) bool {
	by, ok := labels[LabelServer]
	if !ok {
		return o.cfg.SelfContainer == InstalledServer
	}
	return by == o.cfg.SelfContainer
}

// GuestUser is the user in every sandbox (AGENTS.md, "Fixed names").
const GuestUser = "agent"

// EnvSandbox names the sandbox to the image's scripts and to crew-run.
const EnvSandbox = "CREW_SANDBOX"

var sandboxIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}$`)

// SandboxSpec is what a sandbox is started with.
type SandboxSpec struct {
	ID   string // unique; the container and the network are named crew-<ID>
	Kind string
	// Home is the directory on the machine that becomes /home/agent. The caller makes it, and
	// removes it after the sandbox. Empty: the home is the image's own.
	Home string
	Env  []string
	// Minimal starts no Docker inside, for a sandbox that only signs in or checks a login.
	Minimal bool
	Labels  map[string]string
	Sizing  Sizing
	// Mode is the sandbox's network mode (13.6): config.ModeInternet, ModeModelOnly, or ModeLAN.
	// Empty is the internet. It is fixed when the sandbox starts.
	Mode string
	// Image is the runtime image it starts from, by its id; empty is the running release's. Only the
	// gate's project names another, the candidate's, while its live steps run (16.2 step 5).
	Image string
}

// Sandbox is a started sandbox.
type Sandbox struct {
	ID        string
	Kind      string
	Container string
	Network   string
	// BrokerIP is the server's address on the sandbox's bridge, where the broker answers it.
	BrokerIP string
	// Door is the server's address and port on the sandbox's bridge, host:port, where the broker and
	// proxy answer it.
	Door string
	// IP is the sandbox's own address on its bridge, the one its requests come from.
	IP string
	// Mode is its network mode.
	Mode string
}

// sandboxGwPriority keeps a sandbox's bridge from ever being the server's way out. Docker gives a
// container the default route of its network with the highest priority, and of equal ones the first
// by name: the sandboxes' networks, crew-…, sort before crew-out. The server then went out through
// the newest sandbox's bridge, where a Model only bridge's rules drop it (found on dave in M10).
const sandboxGwPriority = -1000

// SandboxName is the container's and the network's name for a sandbox id.
func SandboxName(id string) string { return "crew-" + id }

// ErrNoBoot is a microVM that did not boot: Docker could not start it, or its guest never became
// ready (21, D10). Nothing else is a failed boot: not the network, the image, a cancelled start, or
// the firewall's proof after the boot.
var ErrNoBoot = errors.New("the microVM did not boot")

// maxWhy bounds the text of a microVM that did not boot.
const maxWhy = 400

// cutWhy keeps at most maxWhy bytes of why, cut at a rune and marked.
func cutWhy(why string) string {
	if len(why) <= maxWhy {
		return why
	}
	cut := maxWhy
	for cut > 0 && !utf8.RuneStart(why[cut]) {
		cut--
	}
	return why[:cut] + " …"
}

// boot tells OnBoot of a start that reached Docker's.
func (o *Orchestrator) boot(ok bool, why string) {
	if o.OnBoot != nil {
		o.OnBoot(ok, why)
	}
}

// StartSandbox makes the sandbox's bridge, joins the server to it, starts the microVM, and waits
// until it is ready. A sandbox that fails to start is removed again.
func (o *Orchestrator) StartSandbox(ctx context.Context, spec SandboxSpec) (sb Sandbox, err error) {
	if !sandboxIDRe.MatchString(spec.ID) {
		return Sandbox{}, fmt.Errorf("sandbox id %q: lower-case letters, digits, and dashes", spec.ID)
	}
	if o.cfg.SelfContainer == "" {
		return Sandbox{}, errors.New("the server does not run in its container, so no sandbox can reach the broker")
	}
	mode := spec.Mode
	if mode == "" {
		mode = config.ModeInternet
	}
	image := o.cfg.Image
	if spec.Image != "" {
		image = spec.Image
	}
	if !slices.Contains(config.SandboxModes, mode) {
		return Sandbox{}, fmt.Errorf("sandbox %s: no network mode %q", spec.ID, mode)
	}
	// Model only and Sources reach only the server on their bridge, whose proxy lets through a list.
	proxied := mode == config.ModeModelOnly || mode == config.ModeSources
	name := SandboxName(spec.ID)
	sb = Sandbox{ID: spec.ID, Kind: spec.Kind, Container: name, Network: name, Mode: mode}
	// What the creates made, by their IDs, for a start that fails.
	var made, bridgeID string
	defer func() {
		if err != nil {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			o.clearFailedStart(rctx, spec.ID, made, bridgeID)
		}
	}()

	labels := o.ownLabels(spec.ID, spec.Kind)
	// The bridge is joined and the sandbox made on it by the ID Docker gave it here.
	bridge, err := o.docker.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:  "bridge",
		Options: map[string]string{"com.docker.network.bridge.name": config.BridgeName(name, mode)},
		Labels:  labels,
	})
	if err != nil {
		return sb, fmt.Errorf("the sandbox's network: %w", err)
	}
	bridgeID = bridge.ID
	subnet := o.bridgeSubnet(ctx, bridge.ID)
	if subnet == nil {
		return sb, fmt.Errorf("the sandbox's network %s has no IPv4 subnet", name)
	}
	sb.BrokerIP = brokerIPForSubnet(subnet).String()
	sb.Door = sb.BrokerIP + brokerPort(o.cfg.BrokerAddr)
	o.doors.enter(spec.ID, sb.BrokerIP, subnet)
	if err := o.docker.NetworkConnect(ctx, bridge.ID, o.cfg.SelfContainer, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: sb.BrokerIP}, GwPriority: sandboxGwPriority,
	}); err != nil {
		return sb, fmt.Errorf("joining the server to the sandbox's network: %w", err)
	}

	env := []string{
		config.EnvUser + "=" + GuestUser,
		config.EnvUID + "=" + strconv.Itoa(o.cfg.UID),
		config.EnvGID + "=" + strconv.Itoa(o.cfg.GID),
		config.EnvProject + "=" + spec.Kind,
		EnvSandbox + "=" + spec.ID,
		config.EnvCredBroker + "=http://" + sb.Door,
	}
	if spec.Minimal {
		env = append(env, "CREW_MINIMAL=1")
	}
	// Two public resolvers, as styrman-crew gave every sandbox. Model only resolves no name itself: its
	// one resolver is the server's address, where nothing answers on port 53, and the proxy resolves
	// the hosts on its list (13.6).
	dns := []string{"8.8.8.8", "1.1.1.1"}
	if proxied {
		dns = []string{sb.BrokerIP}
		proxy := "http://" + sb.Door
		noProxy := sb.BrokerIP + ",localhost,127.0.0.1"
		env = append(env, "HTTPS_PROXY="+proxy, "https_proxy="+proxy, "NO_PROXY="+noProxy, "no_proxy="+noProxy)
		// Go's modules come through the door, which follows proxy.golang.org's redirect into its bucket
		// itself; it answers a Sources sandbox only (13.6, since 0.61).
		if mode == config.ModeSources {
			env = append(env, "GOPROXY="+proxy+strings.TrimSuffix(egress.ModulesPath, "/"))
		}
	}
	env = append(env, spec.Env...)
	// A spec's labels add to the sandbox's own, which say whose it is and are not the caller's to change.
	for k, v := range spec.Labels {
		if _, own := labels[k]; !own {
			labels[k] = v
		}
	}
	hc := &container.HostConfig{
		Runtime:       o.cfg.Runtime,
		CapAdd:        []string{"SYS_ADMIN", "NET_ADMIN", "NET_RAW", "SYS_PTRACE"}, // 13.5
		DNS:           dns,
		SecurityOpt:   []string{"apparmor=unconfined"},
		RestartPolicy: container.RestartPolicy{Name: "no"},
		Resources:     container.Resources{NanoCPUs: spec.Sizing.NanoCPUs, Memory: spec.Sizing.MemBytes},
	}
	if spec.Home != "" {
		hc.Binds = []string{spec.Home + ":/home/" + GuestUser}
	}
	resp, err := o.docker.ContainerCreate(ctx,
		&container.Config{Image: image, Hostname: "crew-" + spec.Kind, Env: env, Labels: labels},
		hc,
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{bridge.ID: {}}},
		nil, name)
	if err != nil {
		return sb, fmt.Errorf("creating the sandbox: %w", err)
	}
	made = resp.ID
	if err := o.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		if ctx.Err() != nil {
			return sb, fmt.Errorf("starting the sandbox: %w", err)
		}
		// Kata's error can be a long stack: what reaches the run, the log, and the inbox is cut here,
		// where it is made (security review of 5d1b414, round 2).
		why := cutWhy(err.Error())
		o.boot(false, why)
		return sb, fmt.Errorf("starting the sandbox: %w: %s", ErrNoBoot, why)
	}
	insp, err := o.docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return sb, err
	}
	if insp.NetworkSettings != nil && insp.NetworkSettings.Networks[name] != nil {
		sb.IP = insp.NetworkSettings.Networks[name].IPAddress
	}
	if sb.IP == "" {
		return sb, fmt.Errorf("the sandbox %s has no address on its network", name)
	}
	o.doors.put(spec.ID, sb.BrokerIP, sb.IP, mode)
	ready := o.readyTimeout
	if ready == 0 {
		ready = 90 * time.Second
	}
	if err := o.waitReady(ctx, resp.ID, ready); err != nil {
		if ctx.Err() != nil {
			return sb, fmt.Errorf("the sandbox did not become ready: %w", err)
		}
		why := "the sandbox did not become ready: " + cutWhy(err.Error())
		o.boot(false, why)
		return sb, fmt.Errorf("%s: %w", why, ErrNoBoot)
	}
	o.boot(true, "")
	if proxied {
		if err := o.proveModelOnly(ctx, resp.ID, sb.Door, config.BridgeName(name, mode)[:4]); err != nil {
			return sb, err
		}
	}
	return sb, nil
}

// MachineCheck is what Setup's machine check says (20.1): Docker's version, and whether the runtime
// that makes a sandbox a microVM is registered with it.
type MachineCheck struct {
	Docker    string `json:"docker"`
	Runtime   string `json:"runtime"`
	RuntimeOK bool   `json:"runtimeOK"`
}

// CheckMachine asks Docker.
func (o *Orchestrator) CheckMachine(ctx context.Context) (MachineCheck, error) {
	info, err := o.docker.Info(ctx)
	if err != nil {
		return MachineCheck{}, err
	}
	_, ok := info.Runtimes[o.cfg.Runtime]
	return MachineCheck{Docker: info.ServerVersion, Runtime: o.cfg.Runtime, RuntimeOK: ok}, nil
}
