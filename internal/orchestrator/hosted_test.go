package orchestrator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// errAlreadyConnected is Docker's answer to a join a container already has.
var errAlreadyConnected = errors.New("endpoint with name crew-caddy already exists in network crew-host-abc-1")

func hostedOrch() (*Orchestrator, *fakeDocker) {
	o, fd := sandboxOrch()
	o.cfg.IngressContainer = "crew-caddy"
	fd.pool = "172.20"
	return o, fd
}

// A hosted project's bridge (19.4, plan M19 D27, D39): app- with the internet, apo- without; the
// server at .254 and the ingress at .253 of its /24, which the firewall's rules name, the app at .252
// and the probe at .251; the server's address there is a sandbox's door, so nothing on that bridge
// reaches the operator's door.
func TestAHostedBridgeHasItsFixedAddresses(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "crew-host-abc-1" || n.Server != "172.20.1.254" || n.Ingress != "172.20.1.253" || n.App != "172.20.1.252" || n.Probe != "172.20.1.251" {
		t.Fatalf("the bridge: %+v", n)
	}
	if b := fd.networks["crew-host-abc-1"].bridge; !strings.HasPrefix(b, "app-") {
		t.Fatalf("the bridge's name: %s", b)
	}
	for _, want := range []string{"NetworkConnect(net-crew-host-abc-1,crew-server,ip=172.20.1.254,gw=", "NetworkConnect(net-crew-host-abc-1,crew-caddy,ip=172.20.1.253,gw="} {
		if !slices.ContainsFunc(fd.calls, func(c string) bool { return strings.HasPrefix(c, want) }) {
			t.Fatalf("no %s in %v", want, fd.calls)
		}
	}
	if !o.IsSandboxDoor("172.20.1.254:8080") {
		t.Fatal("the server's address on the hosted bridge is not a sandbox's door")
	}
	if !o.IsSandboxGuest("172.20.1.252:40000") || !o.IsSandboxGuest("172.20.1.17:1") || o.IsSandboxGuest("172.20.2.252:1") {
		t.Fatal("the hosted bridge's addresses are not all counted as its guest's, and only they")
	}
	off, err := o.CreateHostedNet(ctx, "host-abc-2", false)
	if err != nil || !strings.HasPrefix(fd.networks[off.Name].bridge, "apo-") {
		t.Fatalf("without the internet: %+v %v", off, err)
	}
}

// A bridge outside the pool's /24s would not be the firewall's rules': it is refused, and removed.
func TestAHostedBridgeOutsideThePoolIsRefused(t *testing.T) {
	o, fd := hostedOrch()
	fd.pool = ""
	if _, err := o.CreateHostedNet(context.Background(), "host-abc-1", true); err == nil || !strings.Contains(err.Error(), "172.16.0.0/12") {
		t.Fatalf("a bridge in 10.9.1.0/24: %v", err)
	}
	if _, ok := fd.networks["crew-host-abc-1"]; ok {
		t.Fatal("the refused bridge was left")
	}
}

// The ingress is joined again when it was recreated, and taken off a bridge before the bridge goes;
// a join it already has is no error (plan M19 D29).
func TestTheIngressIsJoinedAgainAndLeavesFirst(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	if _, err := o.CreateHostedNet(ctx, "host-abc-1", true); err != nil {
		t.Fatal(err)
	}
	fd.fail["NetworkConnect"] = errAlreadyConnected
	if err := o.JoinIngress(ctx, "host-abc-1"); err != nil {
		t.Fatalf("a join the ingress has: %v", err)
	}
	delete(fd.fail, "NetworkConnect")
	if err := o.JoinIngress(ctx, "host-abc-1"); err != nil {
		t.Fatal(err)
	}
	fd.calls = nil
	if err := o.RemoveSandbox(ctx, "host-abc-1"); err != nil {
		t.Fatal(err)
	}
	ingress := slices.Index(fd.calls, "NetworkDisconnect(net-crew-host-abc-1,crew-caddy)")
	remove := slices.Index(fd.calls, "NetworkRemove(net-crew-host-abc-1)")
	if ingress < 0 || remove < ingress {
		t.Fatalf("the ingress does not leave before the bridge goes: %v", fd.calls)
	}
}

func hostedSpec(n HostedNet) HostedSpec {
	return HostedSpec{ID: "host-abc-1", Project: "p1", Image: "sha256:" + strings.Repeat("a", 64), Port: 8080,
		DataDir: "/var/lib/crew/hosted/p1/data", DataMount: "/data", Env: []string{"DB_PASSWORD=x"},
		Sizing: Sizing{NanoCPUs: 1e9, MemBytes: 1 << 30}, Net: n}
}

// A hosted microVM started on a bridge an earlier start made, which this server's doors do not hold,
// has its door and its bridge's /24 known before the app is created (13.6, 20.3).
func TestAHostedMicroVMOnAnEarlierBridgeHasItsDoorFirst(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	o.doors.drop("host-abc-1")
	fd.onCall = func(m string) {
		if m == "ContainerCreate" && (!o.IsSandboxDoor(n.Server+":8080") || !o.IsSandboxGuest("172.20.1.17:1")) {
			t.Error("the app is created before its bridge's door and subnet are known")
		}
	}
	if _, err := o.StartHosted(ctx, hostedSpec(n)); err != nil {
		t.Fatal(err)
	}
	if !o.IsSandboxGuest("172.20.1.17:1") || o.IsSandboxGuest("172.20.2.17:1") {
		t.Fatal("the hosted bridge's /24 is not the one counted as its guest's")
	}
}

// A hosted start whose stored addresses drifted from its bridge's subnet is refused before the app is
// made, and enters no door: the /24 counted as the guest's is the bridge's own (13.6).
func TestAHostedStartWhoseAddressesAreNotItsBridgesIsRefused(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	o.doors.drop("host-abc-1")
	drifted := n
	drifted.Server, drifted.Ingress, drifted.App, drifted.Probe = "172.20.7.254", "172.20.7.253", "172.20.7.252", "172.20.7.251"
	if _, err := o.StartHosted(ctx, hostedSpec(drifted)); err == nil {
		t.Fatal("a start with another /24's addresses was not refused")
	}
	if slices.ContainsFunc(fd.calls, func(c string) bool { return strings.HasPrefix(c, "ContainerCreate") }) {
		t.Fatal("the app was made")
	}
	if o.IsSandboxGuest("172.20.7.17:1") || o.IsSandboxDoor("172.20.7.254:8080") {
		t.Fatal("the drifted /24 was entered")
	}
}

// A hosted microVM (19.3, plan M19 D13, D31): the project's image by its id, as crew's uid and gid,
// every capability dropped, no new privilege, no AppArmor change, a bound on its processes, a /tmp
// of its own, no published port, started again with Docker, the data folder as its one bind, at the
// app's fixed address on its own bridge; PORT is Crew's, no CREW_ value, and its door is known.
func TestAHostedMicroVMRunsAsCrewWithNothingMore(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := o.StartHosted(ctx, hostedSpec(n))
	if err != nil {
		t.Fatal(err)
	}
	c := fd.containers["crew-host-abc-1"]
	if c == nil || !c.running || sb.IP != n.App || sb.Mode != "hosted_internet" {
		t.Fatalf("the microVM: %+v %+v", c, sb)
	}
	cfg, hc := c.cfg, c.hc
	if cfg.Image != hostedSpec(n).Image || cfg.User != "999:987" || cfg.Hostname != "app" || len(cfg.ExposedPorts) != 0 {
		t.Fatalf("the config: %+v", cfg)
	}
	if !slices.Equal(cfg.Env, []string{"PORT=8080", "DB_PASSWORD=x"}) {
		t.Fatalf("the environment: %v", cfg.Env)
	}
	if hc.Runtime != "mvm" || !slices.Equal(hc.CapDrop, []string{"ALL"}) || len(hc.CapAdd) != 0 || hc.Privileged ||
		!slices.Equal(hc.SecurityOpt, []string{"no-new-privileges"}) || hc.PidsLimit == nil || *hc.PidsLimit != HostedPids ||
		hc.Tmpfs["/tmp"] != HostedTmp || len(hc.PortBindings) != 0 || hc.PublishAllPorts || hc.NetworkMode != "" ||
		hc.RestartPolicy.Name != "unless-stopped" || !slices.Equal(hc.Binds, []string{"/var/lib/crew/hosted/p1/data:/data"}) ||
		len(hc.Mounts) != 0 || hc.Memory != 1<<30 {
		t.Fatalf("the host config: %+v", hc)
	}
	ep := c.nc.EndpointsConfig["net-crew-host-abc-1"]
	if len(c.nc.EndpointsConfig) != 1 || ep == nil || ep.IPAMConfig == nil || ep.IPAMConfig.IPv4Address != n.App {
		t.Fatalf("the network: %+v", c.nc.EndpointsConfig)
	}
	if !o.IsSandboxDoor(n.Server + ":8080") {
		t.Fatal("the hosted bridge's door is not known")
	}
	for _, bad := range [][]string{{"PORT=1"}, {"CREW_TOKEN=x"}, {"NOEQUALS"}} {
		s := hostedSpec(n)
		s.ID, s.Net.Name, s.Env = "host-abc-2", "crew-host-abc-2", bad
		fd.networks["crew-host-abc-2"] = fd.networks["crew-host-abc-1"]
		if _, err := o.StartHosted(ctx, s); err == nil {
			t.Fatalf("%v was given to a hosted microVM", bad)
		}
	}
	s := hostedSpec(n)
	s.Port = 80
	if _, err := o.StartHosted(ctx, s); err == nil {
		t.Fatal("a port below 1024, which crew's uid cannot open, was taken")
	}
	s = hostedSpec(n)
	s.Net.Name = "crew-other"
	if _, err := o.StartHosted(ctx, s); err == nil {
		t.Fatal("a hosted microVM on another's bridge was started")
	}
}

// probeExits plays the firewall for the probe: the exit of each connection by its target.
func hostedProbeExits(fd *fakeDocker, exits map[string]int, other int) {
	fd.execFor = func(cmd string) int {
		for target, code := range exits {
			if strings.HasSuffix(cmd, "/dev/tcp/"+target) {
				return code
			}
		}
		return other
	}
}

// The probe of a new hosted bridge (plan M19 D22): with the firewall in place the server, the ingress
// and the machine are silent, private addresses fail, and on app- the internet answers; anything else
// fails the deploy with the firewall's reason. The probe is removed either way.
func TestTheProbeProvesTheHostedBridgesFirewall(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		internet bool
		exits    map[string]int
		fail     string
	}{
		{"in place, app-", true, map[string]int{"1.1.1.1/443": 0, "192.168.0.1/80": 1, "10.0.0.1/80": 1}, ""},
		{"in place, apo-", false, map[string]int{"1.1.1.1/443": 124, "192.168.0.1/80": 124, "10.0.0.1/80": 124}, ""},
		{"the server answers", true, map[string]int{"1.1.1.1/443": 0, "172.20.1.254/8091": 0}, "172.20.1.254:8091 answered"},
		{"the server refuses", true, map[string]int{"1.1.1.1/443": 0, "172.20.1.254/80": 1}, "172.20.1.254:80 answered"},
		{"the ingress answers", true, map[string]int{"1.1.1.1/443": 0, "172.20.1.253/443": 0}, "172.20.1.253:443 answered"},
		{"the machine refuses", true, map[string]int{"1.1.1.1/443": 0, "172.20.1.1/22": 1}, "172.20.1.1:22 answered"},
		{"a private address", true, map[string]int{"1.1.1.1/443": 0, "10.0.0.1/80": 0}, "reached 10.0.0.1:80"},
		{"apo- reaches out", false, map[string]int{"1.1.1.1/443": 0}, "reached 1.1.1.1:443"},
		{"app- reaches nothing", true, map[string]int{}, "did not reach 1.1.1.1:443"},
		{"no bash", true, map[string]int{"1.1.1.1/443": 0, "172.20.1.253/80": 127}, "could not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := hostedOrch()
			n, err := o.CreateHostedNet(ctx, "host-abc-1", tc.internet)
			if err != nil {
				t.Fatal(err)
			}
			hostedProbeExits(fd, tc.exits, 124)
			err = o.ProbeHosted(ctx, "host-abc-1", n)
			switch {
			case tc.fail == "" && err != nil:
				t.Fatalf("a firewall in place failed: %v", err)
			case tc.fail != "" && (err == nil || !strings.Contains(err.Error(), tc.fail) || !strings.Contains(err.Error(), "the firewall of the hosted bridge is not in place")):
				t.Fatalf("want %q, got %v", tc.fail, err)
			}
			if _, ok := fd.containers["crew-host-abc-1-probe"]; ok {
				t.Fatal("the probe was left")
			}
			if fd.callsTo("ContainerCreate") != 1 {
				t.Fatal("no probe was made")
			}
		})
	}
}

// The probe runs as crew's uid with no capability, at the probe's address, in a microVM.
func TestTheProbeHasNothing(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, _ := o.CreateHostedNet(ctx, "host-abc-1", true)
	// The probe's connections run at once: the first one's look at the probe is taken once.
	var seen *fakeContainer
	var once sync.Once
	fd.onCall = func(m string) {
		if m == "ContainerExecCreate" {
			once.Do(func() { seen, _ = fd.byName("crew-host-abc-1-probe") })
		}
	}
	hostedProbeExits(fd, map[string]int{"1.1.1.1/443": 0}, 124)
	if err := o.ProbeHosted(ctx, "host-abc-1", n); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.cfg.User != "999:987" || !slices.Equal(seen.hc.CapDrop, []string{"ALL"}) || seen.hc.Runtime != "mvm" ||
		len(seen.hc.Binds) != 0 {
		t.Fatalf("the probe: %+v", seen)
	}
	if ep := seen.nc.EndpointsConfig["net-"+n.Name]; ep == nil || ep.IPAMConfig == nil || ep.IPAMConfig.IPv4Address != n.Probe {
		t.Fatalf("the probe's endpoint: %+v", seen.nc.EndpointsConfig)
	}
}

// A load is known by the id Docker says it loaded; a load by a name, two images, an error, or no id
// is refused.
func TestLoadImageReadsTheID(t *testing.T) {
	ctx := context.Background()
	id := "sha256:" + strings.Repeat("b", 64)
	for answer, want := range map[string]string{
		`{"stream":"Loaded image ID: ` + id + `\n"}`:                              id,
		`{"stream":"Loaded image: shop:latest\n"}`:                                "",
		`{"stream":"Loaded image ID: ` + id + `\nLoaded image ID: ` + id + `\n"}`: "",
		`{"error":"no space left on device"}`:                                     "",
		`{"stream":"nothing\n"}`:                                                  "",
	} {
		o, fd := hostedOrch()
		fd.loadAnswer = answer
		got, err := o.LoadImage(ctx, strings.NewReader("tar"))
		if want != "" && (err != nil || got != want) || want == "" && err == nil {
			t.Errorf("%s: %q %v", answer, got, err)
		}
	}
}

// Two servers on one Docker (a test's throwaway beside the installed one) list only their own
// sandboxes, so neither's Reconcile removes the other's: the installed server's are its own name's,
// or have no name, from a release before 0.73 (review of M19, finding 1).
func TestAServerListsOnlyItsOwnSandboxes(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	for name, labels := range map[string]map[string]string{
		"crew-run-a":  {LabelSandbox: "run-a", LabelServer: "crew-server"},
		"crew-run-b":  {LabelSandbox: "run-b"},
		"crew-host-c": {LabelSandbox: "host-c", LabelServer: "t19-server"},
	} {
		fd.containers[name] = &fakeContainer{id: "id-" + name, name: name, labels: labels, running: true}
	}
	ids := func(o *Orchestrator) []string {
		list, err := o.Sandboxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range list {
			out = append(out, s.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(o); !slices.Equal(got, []string{"run-a", "run-b"}) {
		t.Fatalf("the installed server lists %v", got)
	}
	o.cfg.SelfContainer = "t19-server"
	if got := ids(o); !slices.Equal(got, []string{"host-c"}) {
		t.Fatalf("the throwaway server lists %v", got)
	}
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "setup-x", Kind: "setup", Minimal: true}); err != nil {
		t.Fatal(err)
	}
	if fd.containers["crew-setup-x"].labels[LabelServer] != "t19-server" {
		t.Fatalf("a sandbox's labels: %v", fd.containers["crew-setup-x"].labels)
	}
	// By name too: a database restored from the installed one names its sandboxes, and the throwaway
	// server neither removes nor stops nor starts them (security review of M19, round 2).
	if err := o.RemoveSandbox(ctx, "run-a"); err != nil || fd.containers["crew-run-a"] == nil {
		t.Fatalf("the throwaway server removed the installed server's sandbox (%v)", err)
	}
	for name, change := range map[string]func(context.Context, string) error{"stop": o.StopHosted, "restart": o.Restart,
		"start again": o.StartAgain, "pause": pauses(o), "join the ingress": o.JoinIngress, "adopt": o.AdoptSandbox, "cut": o.CutSandbox,
		"read whether it runs": readsRunning(o), "read its log": readsLogs(o),
		"exec": execs(o), "exec detached": execsDetached(o), "start a terminal": startsInteractive(o), "read whether its container runs": readsContainer(o)} {
		if err := change(ctx, "run-a"); !errors.Is(err, ErrAnothers) {
			t.Fatalf("%s of the installed server's sandbox: %v", name, err)
		}
	}
	if err := o.RemoveSandbox(ctx, "host-c"); err != nil || fd.containers["crew-host-c"] != nil {
		t.Fatalf("the throwaway server did not remove its own sandbox (%v)", err)
	}
}

// A check of whose a sandbox is that errors keeps the sandbox: only "no such container" lets a server
// go on by name (security review of the release of M20, L2).
func TestASandboxWhoseOwnerCannotBeReadIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	o.cfg.SelfContainer = "t19-server"
	fd.containers["crew-host-c"] = &fakeContainer{id: "id-host-c", name: "crew-host-c",
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "t19-server"}, running: true}
	o.doors.put("host-c", "172.20.9.2", "172.20.9.3", "")
	fd.fail = map[string]error{"ContainerInspect": errors.New("the daemon is busy")}
	if err := o.RemoveSandbox(ctx, "host-c"); err == nil || fd.containers["crew-host-c"] == nil {
		t.Fatalf("a sandbox whose owner could not be read was removed (%v)", err)
	}
	// It runs on, so the broker still knows it (review of 5ddd59a).
	if id, ok := o.SandboxAt("172.20.9.2:8080", "172.20.9.3:41000"); !ok || id != "host-c" {
		t.Fatalf("a removal that did not happen dropped the sandbox's doors: %q %v", id, ok)
	}
	for name, change := range map[string]func(context.Context, string) error{"stop": o.StopHosted, "restart": o.Restart,
		"start again": o.StartAgain, "pause": pauses(o), "join the ingress": o.JoinIngress, "adopt": o.AdoptSandbox, "cut": o.CutSandbox,
		// Whether it runs is not here: its own inspect fails with the check's, so only the table of
		// another server's sandbox above pins its guard (review of 1e00ae4).
		"read its log": readsLogs(o), "exec": execs(o), "exec detached": execsDetached(o), "start a terminal": startsInteractive(o)} {
		if err := change(ctx, "host-c"); err == nil || !strings.Contains(err.Error(), "the daemon is busy") {
			t.Fatalf("%s of a sandbox whose owner could not be read: %v", name, err)
		}
		if id, ok := o.SandboxAt("172.20.9.2:8080", "172.20.9.3:41000"); !ok || id != "host-c" {
			t.Fatalf("%s that could not read the owner dropped the sandbox's doors: %q %v", name, id, ok)
		}
	}
	// An unpause, by the ID its pause gave, has no check to wait for (TestAnUnpauseUndoesThePauseByItsID).
	// Gone is not another's: a removal of a sandbox Docker no longer has is done.
	delete(fd.fail, "ContainerInspect")
	delete(fd.containers, "crew-host-c")
	if err := o.RemoveSandbox(ctx, "host-c"); err != nil {
		t.Fatalf("a gone sandbox: %v", err)
	}
}

// A command runs in the container the owner check read: one another server makes under the name
// between the check and the exec gets none (security review of 54f60eb, finding 1).
func TestACommandRunsInTheContainerTheCheckRead(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(*Orchestrator) func(context.Context, string) error{
		"exec": execs, "exec detached": execsDetached, "start a terminal": startsInteractive} {
		t.Run(name, func(t *testing.T) {
			o, fd := sandboxOrch()
			fd.containers["crew-run-a"] = &fakeContainer{id: "id-mine", name: "crew-run-a",
				labels: map[string]string{LabelSandbox: "run-a", LabelServer: "crew-server"}, running: true}
			// Without the swap the command runs: the refusal below is the swap's.
			if err := run(o)(ctx, "run-a"); err != nil || fd.execs != 1 {
				t.Fatalf("a command in this server's sandbox: %v, %d execs", err, fd.execs)
			}
			fd.execs = 0
			fd.onCall = func(m string) {
				if m == "ContainerExecCreate" {
					fd.mu.Lock()
					fd.containers["crew-run-a"] = &fakeContainer{id: "id-theirs", name: "crew-run-a",
						labels: map[string]string{LabelSandbox: "run-a", LabelServer: "t19-server"}, running: true}
					fd.mu.Unlock()
				}
			}
			creates := fd.callsTo("ContainerExecCreate")
			if err := run(o)(ctx, "run-a"); err == nil || fd.execs != 0 || fd.callsTo("ContainerExecCreate") != creates+1 {
				t.Fatalf("a command reached the container made under the name after the check: %v, %d execs", err, fd.execs)
			}
		})
	}
}

// A sandbox whose microVM is gone is known by its bridge: another server's bridge is neither joined,
// adopted nor removed (security review of the release of M20, round 2).
func TestAnotherServersBridgeIsLeftWhenItsSandboxIsGone(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	o.cfg.SelfContainer = "t19-server"
	fd.networks["crew-host-c"] = &fakeNetwork{name: "crew-host-c", bridge: "app-c", subnet: "172.20.9.0/24", gateway: "172.20.9.1",
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "crew-server"}}
	for name, change := range map[string]func(context.Context, string) error{"join the ingress": o.JoinIngress, "adopt": o.AdoptSandbox} {
		if err := change(ctx, "host-c"); !errors.Is(err, ErrAnothers) {
			t.Fatalf("%s of another server's bridge: %v", name, err)
		}
	}
	if err := o.RemoveSandbox(ctx, "host-c"); err != nil || fd.networks["crew-host-c"] == nil {
		t.Fatalf("another server's bridge was removed (%v)", err)
	}
	fd.networks["crew-host-c"].labels[LabelServer] = "t19-server"
	if err := o.RemoveSandbox(ctx, "host-c"); err != nil || fd.networks["crew-host-c"] != nil {
		t.Fatalf("its own bridge was not removed (%v)", err)
	}
}

// The probe's removal leaves a probe of the same name another server started, and one whose owner
// Docker cannot say (review of the release of M20, round 2).
func TestTheProbeLeavesAnotherServersProbe(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		server  string
		inspect error
	}{
		{"another server's", "t19-server", nil},
		{"an owner Docker cannot say", "crew-server", errors.New("the daemon is busy")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := hostedOrch()
			n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
			if err != nil {
				t.Fatal(err)
			}
			fd.containers["crew-host-abc-1-probe"] = &fakeContainer{id: "id-probe", name: "crew-host-abc-1-probe", running: true,
				labels: map[string]string{LabelSandbox: "host-abc-1-probe", LabelServer: tc.server}}
			// Docker refuses a second container of a name in use.
			fd.fail = map[string]error{"ContainerCreate": errors.New("Conflict. The container name is already in use")}
			if tc.inspect != nil {
				fd.fail["ContainerInspect"] = tc.inspect
			}
			if err := o.ProbeHosted(ctx, "host-abc-1", n); err == nil {
				t.Fatal("a probe that could not be made passed")
			}
			if fd.containers["crew-host-abc-1-probe"] == nil {
				t.Fatal("the probe's removal removed a probe that is not this server's")
			}
		})
	}
}

// readsRunning and readsLogs are the reads by name as changes, for the tables of the guards above.
func readsRunning(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error { _, err := o.HostedRunning(ctx, id); return err }
}

// readsContainerRunning is the harness's read of whether a sandbox's container runs.
func readsContainerRunning(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error { _, err := o.Running(ctx, SandboxName(id)); return err }
}

func readsLogs(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error { _, err := o.HostedLogs(ctx, id, time.Time{}); return err }
}

// The commands run in a sandbox by its name: the harness's turns, the probes, and the terminals
// (review of 1e00ae4, finding 1).
func execs(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		_, _, err := o.Exec(ctx, SandboxName(id), ExecSpec{Cmd: []string{"true"}})
		return err
	}
}

func execsDetached(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		_, err := o.ExecDetached(ctx, SandboxName(id), ExecSpec{Cmd: []string{"true"}})
		return err
	}
}

func startsInteractive(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		ex, err := o.StartInteractive(ctx, SandboxName(id), ExecSpec{Cmd: []string{"bash"}}, 80, 24)
		if ex != nil {
			ex.Close()
		}
		return err
	}
}

func readsContainer(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error { _, err := o.Running(ctx, SandboxName(id)); return err }
}

func TestStartHostedRefusesABridgeWithNoIPv4Subnet(t *testing.T) {
	o, fd := hostedOrch()
	ctx := context.Background()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", false)
	if err != nil {
		t.Fatal(err)
	}
	fd.networks[n.Name].subnet = ""
	if _, err := o.StartHosted(ctx, HostedSpec{ID: "host-abc-1", Net: n, Image: "sha256:" + strings.Repeat("a", 64), Port: 8080, DataDir: "/var/lib/crew/projects/shop/data", DataMount: "/data"}); err == nil ||
		!strings.Contains(err.Error(), "has no IPv4 subnet") {
		t.Fatalf("StartHosted on a bridge with no IPv4 subnet: %v", err)
	}
	if _, ok := fd.byName("crew-host-abc-1"); ok {
		t.Fatal("a container was created on a bridge with no IPv4 subnet")
	}
}
