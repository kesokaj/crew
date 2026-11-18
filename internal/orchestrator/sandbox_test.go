package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/system"

	"github.com/kesokaj/crew/internal/config"
)

func sandboxOrch() (*Orchestrator, *fakeDocker) {
	fd := newFakeDocker()
	fd.autoSubnet, fd.list = true, true
	cfg := &config.Config{SelfContainer: "crew-server", BrokerAddr: ":8091", Image: "crew-runtime:1.0.0", Runtime: "mvm", UID: 999, GID: 987}
	return &Orchestrator{docker: fd, cfg: cfg, logger: slog.New(slog.DiscardHandler)}, fd
}

func TestStartSandboxJoinsTheServerBeforeTheSandboxStarts(t *testing.T) {
	o, fd := sandboxOrch()
	var order []string
	fd.onCall = func(m string) {
		if m == "NetworkConnect" || m == "ContainerStart" {
			order = append(order, m)
		}
		// Its door and its bridge are known before the server joins the bridge, so no request of the
		// sandbox ever finds the ingress's door open.
		if m == "NetworkConnect" && (!o.IsSandboxDoor("10.9.1.254:8080") || !o.IsSandboxGuest("10.9.1.2:1")) {
			t.Error("the server joins the bridge before its door and its bridge are known")
		}
	}
	sb, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "setup-abc", Kind: "setup", Home: "/var/lib/crew/sandboxes/setup-abc/home", Minimal: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"NetworkConnect", "ContainerStart"}) {
		t.Fatalf("order = %v: the server must be on the bridge before the sandbox's first request", order)
	}
	if sb.BrokerIP != "10.9.1.254" || sb.Door != "10.9.1.254:8091" || sb.IP != "10.9.1.2" || sb.Container != "crew-setup-abc" {
		t.Fatalf("sandbox = %+v", sb)
	}
	n := fd.networks["crew-setup-abc"]
	if !strings.HasPrefix(n.bridge, "sdb-") {
		t.Fatalf("bridge %q: the firewall matches sdb- (13.6)", n.bridge)
	}
	c, _ := fd.byName("crew-setup-abc")
	for _, want := range []string{"CREW_USER=agent", "CREW_UID=999", "CREW_SANDBOX=setup-abc", "CREW_CRED_BROKER=http://10.9.1.254:8091", "CREW_MINIMAL=1"} {
		if !slices.Contains(c.env, want) {
			t.Errorf("env lacks %s: %v", want, c.env)
		}
	}
	for _, e := range c.env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY") {
			t.Fatal("ANTHROPIC_API_KEY in a sandbox")
		}
	}
	if c.restart != "no" || !slices.Equal(c.binds, []string{"/var/lib/crew/sandboxes/setup-abc/home:/home/agent"}) {
		t.Fatalf("restart %q, binds %v", c.restart, c.binds)
	}
	if c.labels[LabelSandbox] != "setup-abc" || c.labels[LabelKind] != "setup" {
		t.Fatalf("labels %v", c.labels)
	}

	// The broker knows the sandbox by the door it is called at and the address it calls from.
	if id, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:40000"); !ok || id != "setup-abc" {
		t.Fatalf("SandboxAt = %q, %v", id, ok)
	}
	if _, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.3:40000"); ok {
		t.Fatal("another address on the bridge counted as the sandbox")
	}
	if !o.IsSandboxDoor("10.9.1.254:8080") || o.IsSandboxDoor("172.18.0.5:8080") {
		t.Fatal("IsSandboxDoor")
	}
	// Every address of its bridge is the guest's, which can send from any of them, wherever a request
	// from it arrives; an address of no sandbox is not.
	for _, a := range []string{"10.9.1.2:40000", "10.9.1.2", "10.9.1.3:40000", "10.9.1.200:1"} {
		if !o.IsSandboxGuest(a) {
			t.Fatalf("%q, on the sandbox's bridge, is not counted as a guest's", a)
		}
	}
	for _, a := range []string{"10.9.2.3:40000", "172.18.0.5:443", "", ":0"} {
		if o.IsSandboxGuest(a) {
			t.Fatalf("%q counted as a guest", a)
		}
	}

	list, err := o.Sandboxes(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != "setup-abc" || list[0].Kind != "setup" {
		t.Fatalf("Sandboxes = %+v, %v", list, err)
	}

	if err := o.RemoveSandbox(context.Background(), "setup-abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fd.byName("crew-setup-abc"); ok {
		t.Fatal("the container is still there")
	}
	if _, ok := fd.networks["crew-setup-abc"]; ok {
		t.Fatal("the network is still there")
	}
	if _, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:40000"); ok {
		t.Fatal("a removed sandbox still has its door")
	}
	if o.IsSandboxGuest("10.9.1.2:40000") || o.IsSandboxGuest("10.9.1.3:40000") {
		t.Fatal("a removed sandbox's bridge still counts as a guest's")
	}
	if err := o.RemoveSandbox(context.Background(), "setup-abc"); err != nil {
		t.Fatalf("removing it twice: %v", err)
	}
}

func TestStartSandboxThatFailsIsRemoved(t *testing.T) {
	o, fd := sandboxOrch()
	fd.fail["ContainerStart"] = errors.New("kata said no")
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "check-x1", Kind: "login-check"}); err == nil {
		t.Fatal("a sandbox that did not start was reported started")
	}
	if _, ok := fd.byName("crew-check-x1"); ok {
		t.Fatal("the failed container was left")
	}
	if _, ok := fd.networks["crew-check-x1"]; ok {
		t.Fatal("the failed network was left")
	}
	if o.IsSandboxDoor("10.9.1.254:8091") || o.IsSandboxGuest("10.9.1.3:1") {
		t.Fatal("the failed sandbox kept a door or its bridge")
	}
}

func TestStartSandboxRefusesWhatCannotWork(t *testing.T) {
	o, _ := sandboxOrch()
	for _, id := range []string{"", "A-upper", "../x", "x"} {
		if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: id, Kind: "setup"}); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
	o.cfg.SelfContainer = ""
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "setup-1", Kind: "setup"}); err == nil {
		t.Fatal("a sandbox started that could not reach the broker")
	}
}

func TestAdoptSandboxGivesItsDoorBack(t *testing.T) {
	o, fd := sandboxOrch()
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	o.doors.drop("run-1") // as after a restart of the server
	// A running guest is refused at the ingress's door from the moment the server joins its bridge.
	fd.onCall = func(m string) {
		if m == "NetworkConnect" && (!o.IsSandboxDoor("10.9.1.254:8080") || !o.IsSandboxGuest("10.9.1.3:1")) {
			t.Error("the server joins the bridge of a sandbox it takes up before its door and bridge are known")
		}
	}
	if err := o.AdoptSandbox(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if id, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:1"); !ok || id != "run-1" {
		t.Fatalf("after adopt: %q %v", id, ok)
	}
}

func TestCheckMachine(t *testing.T) {
	o, fd := sandboxOrch()
	fd.info.ServerVersion = "29.1.0"
	fd.info.Runtimes = map[string]system.RuntimeWithStatus{"mvm": {}}
	m, err := o.CheckMachine(context.Background())
	if err != nil || m.Docker != "29.1.0" || !m.RuntimeOK || m.Runtime != "mvm" {
		t.Fatalf("%+v %v", m, err)
	}
	fd.info.Runtimes = nil
	if m, _ := o.CheckMachine(context.Background()); m.RuntimeOK {
		t.Fatal("a missing runtime was reported there")
	}
}

// probeExits answers waitReady's probe with 0, the probe of the sandbox's own door with 0, and each
// other probe of the Model only test with code, or with 0 for open, the one address that the
// firewall would let through.
func probeExits(code int, open string) func(string) int {
	return func(cmd string) int {
		if !strings.HasPrefix(cmd, "timeout 4 bash -c exec 3<>/dev/tcp/") || strings.HasSuffix(cmd, "/dev/tcp/10.9.1.254/8091") {
			return 0
		}
		if open != "" && strings.HasSuffix(cmd, "/dev/tcp/"+open) {
			return 0
		}
		return code
	}
}

func TestAModelOnlySandboxHasItsBridgeNoResolverAndTheProxy(t *testing.T) {
	o, fd := sandboxOrch()
	fd.execFor = probeExits(124, "")
	sb, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "judge-abc", Kind: "judge", Minimal: true, Mode: config.ModeModelOnly})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Mode != config.ModeModelOnly {
		t.Fatalf("mode = %q", sb.Mode)
	}
	if n := fd.networks["crew-judge-abc"]; !strings.HasPrefix(n.bridge, "mdl-") {
		t.Fatalf("bridge %q: the firewall's Model only chain matches mdl- (13.6)", n.bridge)
	}
	c, _ := fd.byName("crew-judge-abc")
	if !slices.Equal(c.dns, []string{"10.9.1.254"}) {
		t.Fatalf("dns = %v: a Model only sandbox resolves no name itself", c.dns)
	}
	for _, want := range []string{"HTTPS_PROXY=http://10.9.1.254:8091", "https_proxy=http://10.9.1.254:8091", "NO_PROXY=10.9.1.254,localhost,127.0.0.1"} {
		if !slices.Contains(c.env, want) {
			t.Errorf("env lacks %s: %v", want, c.env)
		}
	}
	// The module proxy answers a Sources sandbox only, and a Model only one is not pointed at it.
	for _, e := range c.env {
		if strings.HasPrefix(e, "GOPROXY=") {
			t.Errorf("a Model only sandbox has %s", e)
		}
	}
	// The start-up test ran from inside the sandbox: the internet, and the machine on the bridge.
	for _, want := range []string{"/dev/tcp/1.1.1.1/443", "/dev/tcp/8.8.8.8/53", "/dev/tcp/10.9.1.1/22"} {
		found := false
		for _, call := range fd.calls {
			if strings.Contains(call, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no probe of %s: %v", want, fd.calls)
		}
	}
}

func TestAModelOnlySandboxThatReachesOutIsRemoved(t *testing.T) {
	for name, tc := range map[string]struct {
		exits func(string) int
		want  string
	}{
		"the internet":             {probeExits(124, "1.1.1.1/443"), "reached 1.1.1.1:443"},
		"the machine":              {probeExits(124, "10.9.1.1/22"), "10.9.1.1:22 answered"},
		"the machine that refuses": {probeExits(1, ""), "10.9.1.1:22 answered (exit 1)"},
		"no bash":                  {probeExits(127, ""), "could not run"},
		"a probe that reaches none": {func(cmd string) int {
			if strings.Contains(cmd, "/dev/tcp/") {
				return 1
			}
			return 0
		}, "did not reach its own door"},
	} {
		t.Run(name, func(t *testing.T) {
			o, fd := sandboxOrch()
			fd.execFor = tc.exits
			_, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "judge-abc", Kind: "judge", Minimal: true, Mode: config.ModeModelOnly})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if _, ok := fd.byName("crew-judge-abc"); ok {
				t.Fatal("the sandbox that failed the test is still there")
			}
			if _, ok := fd.networks["crew-judge-abc"]; ok {
				t.Fatal("its network is still there")
			}
		})
	}
}

func TestTheOtherModesHaveTheirPrefixAndNoProbe(t *testing.T) {
	for mode, prefix := range map[string]string{"": "sdb-", config.ModeInternet: "sdb-", config.ModeLAN: "lan-"} {
		o, fd := sandboxOrch()
		if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "run-abc", Kind: "run", Mode: mode}); err != nil {
			t.Fatal(mode, err)
		}
		if n := fd.networks["crew-run-abc"]; !strings.HasPrefix(n.bridge, prefix) {
			t.Fatalf("mode %q: bridge %q", mode, n.bridge)
		}
		c, _ := fd.byName("crew-run-abc")
		if !slices.Equal(c.dns, []string{"8.8.8.8", "1.1.1.1"}) {
			t.Fatalf("mode %q: dns %v", mode, c.dns)
		}
		for _, e := range c.env {
			if strings.HasPrefix(strings.ToUpper(e), "HTTPS_PROXY=") || strings.HasPrefix(e, "GOPROXY=") {
				t.Fatalf("mode %q has a proxy: %s", mode, e)
			}
		}
		for _, call := range fd.calls {
			if strings.Contains(call, "/dev/tcp/") {
				t.Fatalf("mode %q ran the Model only test: %s", mode, call)
			}
		}
	}
	o, _ := sandboxOrch()
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "run-abc", Kind: "run", Mode: "open"}); err == nil {
		t.Fatal("an unknown mode started")
	} // A hosted project's bridge is the hosted start's alone (plan M19 D24).
	for _, m := range config.HostedModes {
		if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "check-abc", Kind: "check", Mode: m}); err == nil {
			t.Fatalf("a check started on %s", m)
		}
	}
}

// The server joins a sandbox's network as its door, never as its way out: with the lowest priority
// for the default route, at start and when it adopts the sandbox again.
func TestTheServerNeverGoesOutThroughASandbox(t *testing.T) {
	o, fd := sandboxOrch()
	fd.execFor = probeExits(124, "")
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "judge-abc", Kind: "judge", Minimal: true, Mode: config.ModeModelOnly}); err != nil {
		t.Fatal(err)
	}
	if err := o.AdoptSandbox(context.Background(), "judge-abc"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range fd.calls {
		if strings.HasPrefix(c, "NetworkConnect(net-crew-judge-abc,crew-server,") {
			n++
			if !strings.HasSuffix(c, ",gw=-1000)") {
				t.Errorf("%s: the sandbox's bridge could become the server's default route", c)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d joins, want 2: %v", n, fd.calls)
	}
}

// A Sources sandbox, the build of a release (13.6, since 0.51), is as a Model only one but for its
// prefix: no resolver of its own, the server's door as its proxy, and the start-up test of the rules
// before it is used. It has a Docker daemon inside.
func TestASourcesSandboxHasItsBridgeNoResolverAndTheProxy(t *testing.T) {
	o, fd := sandboxOrch()
	fd.execFor = probeExits(124, "")
	sb, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "build-abc", Kind: "build", Mode: config.ModeSources})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Mode != config.ModeSources || o.SandboxMode("build-abc") != config.ModeSources {
		t.Fatalf("mode = %q, %q", sb.Mode, o.SandboxMode("build-abc"))
	}
	if n := fd.networks["crew-build-abc"]; !strings.HasPrefix(n.bridge, "src-") {
		t.Fatalf("bridge %q: the firewall matches src- (13.6)", n.bridge)
	}
	c, _ := fd.byName("crew-build-abc")
	if !slices.Equal(c.dns, []string{"10.9.1.254"}) {
		t.Fatalf("dns = %v: a Sources sandbox resolves no name itself", c.dns)
	}
	// Go's modules come through the server's door, which follows proxy.golang.org's redirect into its
	// bucket (13.6, since 0.61).
	for _, want := range []string{"HTTPS_PROXY=http://10.9.1.254:8091", "https_proxy=http://10.9.1.254:8091", "GOPROXY=http://10.9.1.254:8091/goproxy"} {
		if !slices.Contains(c.env, want) {
			t.Errorf("env lacks %s: %v", want, c.env)
		}
	}
	if slices.Contains(c.env, "CREW_MINIMAL=1") {
		t.Error("a build has no Docker daemon")
	}
	found := false
	for _, call := range fd.calls {
		found = found || strings.Contains(call, "/dev/tcp/1.1.1.1/443")
	}
	if !found {
		t.Errorf("no start-up test: %v", fd.calls)
	}
	o, fd = sandboxOrch()
	fd.execFor = probeExits(124, "1.1.1.1/443")
	if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: "build-def", Kind: "build", Mode: config.ModeSources}); err == nil ||
		!strings.Contains(err.Error(), "src- rules are missing") {
		t.Fatalf("a Sources sandbox that reached the internet was used, or the message names another prefix: %v", err)
	}
}

// A sandbox starts from the running release's runtime image, or from the image its spec names by its
// id: the candidate's, for a run of the gate's crew-gate while a gate's live steps run (16.2 step 5).
func TestASandboxStartsFromTheImageItsSpecNames(t *testing.T) {
	o, fd := sandboxOrch()
	img := "sha256:" + strings.Repeat("c", 64)
	for id, image := range map[string]string{"run-abc": img, "run-def": ""} {
		if _, err := o.StartSandbox(context.Background(), SandboxSpec{ID: id, Kind: "run", Home: "/var/lib/crew/sandboxes/" + id + "/home", Image: image}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := fd.byName("crew-run-abc")
	d, _ := fd.byName("crew-run-def")
	if a.asked != img || d.asked != "crew-runtime:1.0.0" {
		t.Fatalf("images %q and %q", a.asked, d.asked)
	}
}

func TestCutSandboxDisconnectsTheBridgeAndDropsTheDoor(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	fd.execFor = probeExits(124, "")
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "check-abc", Kind: "check", Mode: config.ModeSources}); err != nil {
		t.Fatal(err)
	}
	if _, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:40000"); !ok {
		t.Fatal("the sandbox is not at its door before CutSandbox")
	}
	fd.calls = nil
	if err := o.CutSandbox(ctx, "check-abc"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fd.calls, "NetworkDisconnect(net-crew-check-abc,id-crew-check-abc)") {
		t.Fatalf("CutSandbox calls = %v", fd.calls)
	}
	if _, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:40000"); ok {
		t.Fatal("a cut sandbox still has its door at the broker")
	}
	if o.IsSandboxDoor("10.9.1.254:8091") || o.SandboxMode("check-abc") != "" || o.IsSandboxGuest("10.9.1.2:40000") {
		t.Fatalf("a cut sandbox kept its door (%v), mode (%q), or guest bridge (%v)",
			o.IsSandboxDoor("10.9.1.254:8091"), o.SandboxMode("check-abc"), o.IsSandboxGuest("10.9.1.2:40000"))
	}
}
