package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeDocker stands in for the Engine behind the dockerAPI seam (docker.go):
// an in-memory table of containers and networks, a per-method scripted
// error, and a call log. "Absent" answers are errdefs.NotFound, so the
// orchestrator's IsNotFound branches are the ones exercised. Every exec
// succeeds at once with execExit (waitReady's probe), unless the method is
// scripted to fail. fakeContainer fields are read and written under f.mu.
// Deliberately minimal: Remove ignores Force and endpoints, and Create
// overwrites a duplicate name — a test of a non-Force remove must extend the
// fake first, or it passes vacuously.
type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer // by name
	networks   map[string]*fakeNetwork   // by name
	imageID    string
	// loadAnswer is the body ImageLoad gives back.
	loadAnswer string
	fail       map[string]error
	execExit   int
	// execFor, when set, is the exit of each exec by its command, in place of execExit.
	execFor  func(cmd string) int
	execCmds map[string]string // exec id → its command
	execs    int
	// info is what Info returns: the host's size.
	info system.Info
	// list opts a test into ContainerList returning the table (filtered by
	// label); off, it returns nothing, as every older test assumes.
	list bool
	// autoSubnet gives each new network a /24 of its own and each container an address on it,
	// as Docker does; off, networks have no subnet, as every older test assumes.
	autoSubnet bool
	subnets    int
	// pool, when set, is the first two bytes of the networks' /24s, as the machine's pool 172.16.0.0/12
	// gives them; else 10.9.
	pool   string
	calls  []string
	onCall func(method string) // observe the orchestrator's state mid-call (not under f.mu)
}

type fakeContainer struct {
	id, name, image string
	// asked is the image ContainerCreate was asked for, by its name or id.
	asked   string
	running bool
	binds   []string
	// netName is the network it was created on, by its name whichever way it was named, and netID
	// that network's ID, which ContainerInspect gives as the endpoint's NetworkID.
	netName, netID string
	// ips: the container's address per network, as ContainerInspect reports
	// it.
	ips map[string]string
	// restart is the restart policy name ("unless-stopped", "no"), and
	// restartCount is what `docker inspect` would show after a restart.
	restart      string
	restartCount int
	// nanoCPUs, memory and env are what ContainerCreate was given and what
	// ContainerInspect reports back (HostConfig.Resources, Config.Env), so
	// a test can read what a sandbox was made with.
	nanoCPUs, memory int64
	env              []string
	dns              []string
	// labels and startedAt feed ContainerList's label filter and
	// ContainerInspect's State.StartedAt.
	labels    map[string]string
	startedAt string
	// cfg, hc and nc are what ContainerCreate was given, whole, for a test of a hosted microVM's spec.
	cfg    *container.Config
	hc     *container.HostConfig
	nc     *network.NetworkingConfig
	paused bool
}

type fakeNetwork struct {
	name, bridge string
	// id is the network's ID; empty is net-<name>, the ID NetworkCreate gives.
	id      string
	labels  map[string]string
	gateway string // IPAM gateway, as NetworkInspect reports it
	subnet  string // IPAM subnet (CIDR), the broker address derives from it
}

// ID is the network's ID.
func (n *fakeNetwork) ID() string {
	if n.id != "" {
		return n.id
	}
	return "net-" + n.name
}

// netLocked finds a network by its name or its ID, as Docker does.
func (f *fakeDocker) netLocked(ref string) (*fakeNetwork, bool) {
	if n, ok := f.networks[ref]; ok {
		return n, true
	}
	for _, n := range f.networks {
		if n.ID() == ref {
			return n, true
		}
	}
	return nil, false
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: map[string]*fakeContainer{},
		networks:   map[string]*fakeNetwork{},
		imageID:    "sha256:image-current",
		fail:       map[string]error{},
		info:       system.Info{NCPU: 16, MemTotal: 63853 << 20},
	}
}

func notFound(what string) error { return errdefs.NotFound(errors.New("no such " + what)) }

func (f *fakeDocker) record(call string) error {
	method, _, _ := strings.Cut(call, "(")
	if f.onCall != nil {
		f.onCall(method)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.fail[method]
}

func (f *fakeDocker) callsTo(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, method+"(") {
			n++
		}
	}
	return n
}

func (f *fakeDocker) byName(name string) (*fakeContainer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[name]; ok {
		return c, true
	}
	for _, c := range f.containers {
		if c.id == name {
			return c, true
		}
	}
	return nil, false
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	if err := f.record("ContainerInspect(" + id + ")"); err != nil {
		return container.InspectResponse{}, err
	}
	c, ok := f.byName(id)
	if !ok {
		return container.InspectResponse{}, notFound("container")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
		ID: c.id, Image: c.image, State: &container.State{Running: c.running, StartedAt: c.startedAt}, RestartCount: c.restartCount,
		HostConfig: &container.HostConfig{Binds: append([]string(nil), c.binds...),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(c.restart)},
			Resources:     container.Resources{NanoCPUs: c.nanoCPUs, Memory: c.memory}},
	}, Config: &container.Config{Env: append([]string(nil), c.env...), Labels: maps.Clone(c.labels)}}
	if len(c.ips) > 0 {
		resp.NetworkSettings = &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}}
		for n, ip := range c.ips {
			resp.NetworkSettings.Networks[n] = &network.EndpointSettings{IPAddress: ip, Gateway: ip[:strings.LastIndex(ip, ".")+1] + "1"}
			if n == c.netName {
				resp.NetworkSettings.Networks[n].NetworkID = c.netID
			}
		}
	}
	return resp, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, hc *container.HostConfig, nc *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	if err := f.record("ContainerCreate(" + name + ")"); err != nil {
		return container.CreateResponse{}, err
	}
	c := &fakeContainer{id: "id-" + name, name: name, image: f.imageID, cfg: cfg, hc: hc, nc: nc}
	if cfg != nil && cfg.Image != "" {
		c.image, c.asked = f.imageID, cfg.Image
	}
	if hc != nil {
		c.binds = hc.Binds
		c.restart = string(hc.RestartPolicy.Name)
		c.nanoCPUs, c.memory = hc.NanoCPUs, hc.Memory
		c.dns = hc.DNS
	}
	if cfg != nil {
		c.env = append([]string(nil), cfg.Env...)
		c.labels = cfg.Labels
	}
	if nc != nil {
		for n := range nc.EndpointsConfig {
			c.netName = n
		}
	}
	f.mu.Lock()
	// Docker keys a container's networks by their names, whether it was given a name or an ID.
	if n, ok := f.netLocked(c.netName); ok {
		c.netName, c.netID = n.name, n.ID()
		if f.autoSubnet && n.subnet != "" {
			c.ips = map[string]string{c.netName: strings.TrimSuffix(n.subnet, "0/24") + "2"}
		}
	}
	f.containers[name] = c
	f.mu.Unlock()
	return container.CreateResponse{ID: c.id}, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	if err := f.record("ContainerStart(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	c.running = true
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	if err := f.record("ContainerStop(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	c.running = false
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	if err := f.record("ContainerRemove(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	delete(f.containers, c.name)
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	if err := f.record("ContainerList()"); err != nil {
		return nil, err
	}
	if !f.list {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []container.Summary
next:
	for _, c := range f.containers {
		for _, kv := range opts.Filters.Get("label") {
			k, v, hasValue := strings.Cut(kv, "=")
			if got, ok := c.labels[k]; !ok || hasValue && got != v {
				continue next
			}
		}
		state := "exited"
		if c.running {
			state = "running"
		}
		out = append(out, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Labels: c.labels, State: state})
	}
	return out, nil
}

func (f *fakeDocker) ContainerLogs(_ context.Context, id string, _ container.LogsOptions) (io.ReadCloser, error) {
	if err := f.record("ContainerLogs(" + id + ")"); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeDocker) ContainerExecCreate(_ context.Context, id string, opts container.ExecOptions) (container.ExecCreateResponse, error) {
	cmd := strings.Join(opts.Cmd, " ")
	if err := f.record("ContainerExecCreate(" + cmd + ")"); err != nil {
		return container.ExecCreateResponse{}, err
	}
	if _, ok := f.byName(id); !ok {
		return container.ExecCreateResponse{}, notFound("container")
	}
	f.mu.Lock()
	f.execs++
	eid := fmt.Sprintf("exec-%d", f.execs)
	if f.execCmds == nil {
		f.execCmds = map[string]string{}
	}
	f.execCmds[eid] = cmd
	f.mu.Unlock()
	return container.ExecCreateResponse{ID: eid}, nil
}

func (f *fakeDocker) ContainerExecAttach(_ context.Context, id string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	if err := f.record("ContainerExecAttach(" + id + ")"); err != nil {
		return types.HijackedResponse{}, err
	}
	// An already-finished command: the peer end closes at once, so the
	// stdcopy demux sees EOF and Exec proceeds to the exit-code inspect.
	ours, theirs := net.Pipe()
	_ = theirs.Close()
	return types.HijackedResponse{Conn: ours, Reader: bufio.NewReader(ours)}, nil
}

func (f *fakeDocker) ContainerExecInspect(_ context.Context, id string) (container.ExecInspect, error) {
	if err := f.record("ContainerExecInspect(" + id + ")"); err != nil {
		return container.ExecInspect{}, err
	}
	code := f.execExit
	f.mu.Lock()
	cmd := f.execCmds[id]
	f.mu.Unlock()
	if f.execFor != nil {
		code = f.execFor(cmd)
	}
	return container.ExecInspect{ExecID: id, ExitCode: code, Running: false}, nil
}

func (f *fakeDocker) ContainerExecStart(_ context.Context, id string, _ container.ExecStartOptions) error {
	return nil
}

func (f *fakeDocker) ContainerExecResize(_ context.Context, id string, _ container.ResizeOptions) error {
	return f.record("ContainerExecResize(" + id + ")")
}

func (f *fakeDocker) NetworkCreate(_ context.Context, name string, opts network.CreateOptions) (network.CreateResponse, error) {
	if err := f.record("NetworkCreate(" + name + ")"); err != nil {
		return network.CreateResponse{}, err
	}
	f.mu.Lock()
	n := &fakeNetwork{name: name, bridge: opts.Options["com.docker.network.bridge.name"], labels: maps.Clone(opts.Labels)}
	if f.autoSubnet {
		f.subnets++
		pool := f.pool
		if pool == "" {
			pool = "10.9"
		}
		n.subnet = fmt.Sprintf("%s.%d.0/24", pool, f.subnets)
	}
	f.networks[name] = n
	f.mu.Unlock()
	return network.CreateResponse{ID: "net-" + name}, nil
}

func (f *fakeDocker) NetworkInspect(_ context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
	if err := f.record("NetworkInspect(" + name + ")"); err != nil {
		return network.Inspect{}, err
	}
	f.mu.Lock()
	n, ok := f.netLocked(name)
	f.mu.Unlock()
	if !ok {
		return network.Inspect{}, notFound("network")
	}
	insp := network.Inspect{Name: n.name, ID: n.ID(), Options: map[string]string{"com.docker.network.bridge.name": n.bridge}, Labels: maps.Clone(n.labels)}
	if n.gateway != "" || n.subnet != "" {
		insp.IPAM.Config = []network.IPAMConfig{{Subnet: n.subnet, Gateway: n.gateway}}
	}
	return insp, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	if err := f.record("NetworkRemove(" + name + ")"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.netLocked(name)
	if !ok {
		return notFound("network")
	}
	// The entry found, which a test may keep under another key than its name.
	for k, v := range f.networks {
		if v == n {
			delete(f.networks, k)
		}
	}
	return nil
}

// NetworkConnect records the static address when the caller asked for one
// (`NetworkConnect(net,id,ip=A.B.C.D)`), else the two-field form — callsTo
// counts both, since record splits on the first "(".
func (f *fakeDocker) NetworkConnect(_ context.Context, name, id string, ep *network.EndpointSettings) error {
	if ep != nil && ep.IPAMConfig != nil && ep.IPAMConfig.IPv4Address != "" {
		if ep.GwPriority != 0 {
			return f.record(fmt.Sprintf("NetworkConnect(%s,%s,ip=%s,gw=%d)", name, id, ep.IPAMConfig.IPv4Address, ep.GwPriority))
		}
		return f.record(fmt.Sprintf("NetworkConnect(%s,%s,ip=%s)", name, id, ep.IPAMConfig.IPv4Address))
	}
	return f.record(fmt.Sprintf("NetworkConnect(%s,%s)", name, id))
}

func (f *fakeDocker) NetworkDisconnect(_ context.Context, name, id string, _ bool) error {
	return f.record(fmt.Sprintf("NetworkDisconnect(%s,%s)", name, id))
}

func (f *fakeDocker) ImageInspect(_ context.Context, id string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if err := f.record("ImageInspect(" + id + ")"); err != nil {
		return image.InspectResponse{}, err
	}
	return image.InspectResponse{ID: f.imageID}, nil
}

func (f *fakeDocker) Ping(_ context.Context) (types.Ping, error) {
	if err := f.record("Ping()"); err != nil {
		return types.Ping{}, err
	}
	return types.Ping{APIVersion: "1.47"}, nil
}

func (f *fakeDocker) Info(_ context.Context) (system.Info, error) {
	if err := f.record("Info()"); err != nil {
		return system.Info{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, nil
}

func (f *fakeDocker) ImagesPrune(_ context.Context, args filters.Args) (image.PruneReport, error) {
	if err := f.record("ImagesPrune(" + strings.Join(args.Get("until"), ",") + ")"); err != nil {
		return image.PruneReport{}, err
	}
	return image.PruneReport{SpaceReclaimed: 1 << 20}, nil
}

func (f *fakeDocker) BuildCachePrune(_ context.Context, opts build.CachePruneOptions) (*build.CachePruneReport, error) {
	if err := f.record("BuildCachePrune(" + strings.Join(opts.Filters.Get("until"), ",") + ")"); err != nil {
		return nil, err
	}
	return &build.CachePruneReport{SpaceReclaimed: 2 << 20}, nil
}

var _ dockerAPI = (*fakeDocker)(nil)

// loaded is what ImageLoad answers: Docker's stream for a load, one line per message.
func (f *fakeDocker) ImageLoad(_ context.Context, r io.Reader, _ ...client.ImageLoadOption) (image.LoadResponse, error) {
	if err := f.record("ImageLoad()"); err != nil {
		return image.LoadResponse{}, err
	}
	_, _ = io.Copy(io.Discard, r)
	f.mu.Lock()
	body := f.loadAnswer
	f.mu.Unlock()
	return image.LoadResponse{Body: io.NopCloser(strings.NewReader(body)), JSON: true}, nil
}

func (f *fakeDocker) ImageTag(_ context.Context, source, target string) error {
	return f.record("ImageTag(" + source + "," + target + ")")
}

func (f *fakeDocker) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	return nil, f.record("ImageRemove(" + id + ")")
}

func (f *fakeDocker) ContainerPause(_ context.Context, id string) error {
	if err := f.record("ContainerPause(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	c.paused = true
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerUnpause(_ context.Context, id string) error {
	if err := f.record("ContainerUnpause(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	c.paused = false
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerRestart(_ context.Context, id string, _ container.StopOptions) error {
	if err := f.record("ContainerRestart(" + id + ")"); err != nil {
		return err
	}
	c, ok := f.byName(id)
	if !ok {
		return notFound("container")
	}
	f.mu.Lock()
	c.running = true
	c.restartCount++
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) ContainerStatsOneShot(_ context.Context, id string) (container.StatsResponseReader, error) {
	if err := f.record("ContainerStatsOneShot(" + id + ")"); err != nil {
		return container.StatsResponseReader{}, err
	}
	if _, ok := f.byName(id); !ok {
		return container.StatsResponseReader{}, notFound("container")
	}
	body := `{"cpu_stats":{"cpu_usage":{"total_usage":200000000},"system_cpu_usage":1000000000,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":100000000},"system_cpu_usage":800000000},"memory_stats":{"usage":536870912,"limit":4294967296}}`
	return container.StatsResponseReader{Body: io.NopCloser(strings.NewReader(body))}, nil
}
