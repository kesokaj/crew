package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// theirsBridge is a hosted bridge another server on the same Docker made under a sandbox's name.
func theirsBridge(id string) *fakeNetwork {
	return &fakeNetwork{name: SandboxName(id), id: "net-theirs", bridge: "app-theirs", subnet: "172.20.9.0/24",
		labels: map[string]string{LabelSandbox: id, LabelServer: "t19-server"}}
}

// An action on a sandbox's bridge acts on the bridge the owner check read, by its ID: one another
// server makes under the name after the check is neither joined, left nor removed (security review of
// b39f701, finding 1).
func TestAnActionOnABridgeActsOnTheBridgeTheCheckRead(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, id, method string
		// start makes this server's sandbox; act is the action under test.
		start func(*Orchestrator) error
		act   func(*Orchestrator, string) error
	}{
		{"remove", "host-abc-1", "NetworkDisconnect",
			func(o *Orchestrator) error { _, err := o.CreateHostedNet(ctx, "host-abc-1", true); return err },
			func(o *Orchestrator, id string) error { return o.RemoveSandbox(ctx, id) }},
		{"cut", "run-1", "NetworkDisconnect",
			func(o *Orchestrator) error {
				_, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"})
				return err
			},
			func(o *Orchestrator, id string) error { return o.CutSandbox(ctx, id) }},
		{"join the ingress", "host-abc-1", "NetworkConnect",
			func(o *Orchestrator) error { _, err := o.CreateHostedNet(ctx, "host-abc-1", true); return err },
			func(o *Orchestrator, id string) error { return o.JoinIngress(ctx, id) }},
		{"adopt", "run-1", "NetworkConnect",
			func(o *Orchestrator) error {
				_, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"})
				o.doors.drop("run-1")
				return err
			},
			func(o *Orchestrator, id string) error { return o.AdoptSandbox(ctx, id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := hostedOrch()
			if err := tc.start(o); err != nil {
				t.Fatal(err)
			}
			theirs := theirsBridge(tc.id)
			fd.calls = nil
			fd.onCall = func(m string) {
				if m == tc.method {
					fd.mu.Lock()
					fd.networks[SandboxName(tc.id)] = theirs
					fd.mu.Unlock()
				}
			}
			_ = tc.act(o, tc.id)
			fd.onCall = nil
			if n, ok := fd.networks[SandboxName(tc.id)]; !ok || n != theirs {
				t.Fatalf("%s removed the bridge made under the name after the check", tc.name)
			}
			acted := 0
			for _, c := range fd.calls {
				if strings.HasPrefix(c, "Network") && !strings.HasPrefix(c, "NetworkInspect(") {
					acted++
					if !strings.Contains(c, "(net-"+SandboxName(tc.id)+",") && c != "NetworkRemove(net-"+SandboxName(tc.id)+")" {
						t.Errorf("%s went by name: %s", tc.name, c)
					}
				}
			}
			if acted == 0 {
				t.Fatalf("%s did nothing to the bridge: %v", tc.name, fd.calls)
			}
		})
	}
}

// Another server's bridge under the name of this server's sandbox is another's, though the container
// is this server's: it is not joined, adopted, started or probed on, and a removal of the sandbox
// leaves it.
func TestAnotherServersBridgeUnderThisServersSandboxIsLeft(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	theirs := theirsBridge("run-1")
	fd.networks["crew-run-1"] = theirs
	fd.calls = nil
	if err := o.JoinIngress(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("the ingress joined another server's bridge: %v", err)
	}
	if err := o.AdoptSandbox(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("an adoption on another server's bridge: %v", err)
	}
	if err := o.CutSandbox(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a cut on another server's bridge: %v", err)
	}
	if n := fd.callsTo("NetworkConnect"); n != 0 {
		t.Fatalf("%d joins of another server's bridge: %v", n, fd.calls)
	}
	if err := o.RemoveSandbox(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	if fd.containers["crew-run-1"] != nil || fd.networks["crew-run-1"] != theirs ||
		fd.callsTo("NetworkDisconnect")+fd.callsTo("NetworkRemove") != 0 {
		t.Fatalf("the removal: container left %v, the other's bridge %v, calls %v",
			fd.containers["crew-run-1"] != nil, fd.networks["crew-run-1"] != nil, fd.calls)
	}

	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	fd.networks["crew-host-abc-1"] = theirsBridge("host-abc-1")
	hostedProbeExits(fd, map[string]int{"1.1.1.1/443": 0}, 124)
	if err := o.ProbeHosted(ctx, "host-abc-1", n); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a probe on another server's bridge: %v", err)
	}
	if _, err := o.StartHosted(ctx, hostedSpec(n)); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a hosted microVM on another server's bridge: %v", err)
	}
	if fd.callsTo("ContainerCreate") != 0 {
		t.Fatalf("a microVM was made on another server's bridge: %v", fd.calls)
	}
}

// An adoption gives a door only to an address on the bridge the check read: an endpoint on another
// network of the name, as of a bridge removed and made again, is no door.
func TestAnAdoptionNeedsItsEndpointOnTheBridgeTheCheckRead(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	o.doors.drop("run-1")
	fd.containers["crew-run-1"].netID = "net-gone"
	if err := o.AdoptSandbox(ctx, "run-1"); err == nil {
		t.Fatal("an endpoint on another network was adopted")
	}
	if id, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.2:1"); ok {
		t.Fatalf("the broker answers an endpoint on another network: %q", id)
	}
}

// A sandbox and a hosted bridge are made on the bridge just created, by its ID.
func TestASandboxIsMadeOnItsBridgeByItsID(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	if ep := fd.containers["crew-run-1"].nc.EndpointsConfig; len(ep) != 1 || ep["net-crew-run-1"] == nil {
		t.Fatalf("the sandbox's endpoint: %v", ep)
	}
	if lastCall(fd, "NetworkConnect") != "NetworkConnect(net-crew-run-1,crew-server,ip=172.20.1.254,gw=-1000)" {
		t.Fatalf("the server's join: %q", lastCall(fd, "NetworkConnect"))
	}
}

// A hosted microVM that was not made removes none: one another server has under its name stays, and
// one that was made and did not start is removed by its ID.
func TestAHostedMicroVMThatDidNotStartRemovesOnlyItself(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	theirs := &fakeContainer{id: "id-theirs", name: "crew-host-abc-1", running: true,
		labels: map[string]string{LabelSandbox: "host-abc-1", LabelServer: "t19-server"}}
	fd.containers["crew-host-abc-1"] = theirs
	fd.fail["ContainerCreate"] = errors.New("Conflict. The container name \"/crew-host-abc-1\" is already in use")
	if _, err := o.StartHosted(ctx, hostedSpec(n)); err == nil {
		t.Fatal("a microVM that was not made started")
	}
	if fd.containers["crew-host-abc-1"] != theirs || fd.callsTo("ContainerRemove") != 0 {
		t.Fatalf("the other server's container under the name was removed: %v", fd.calls)
	}
	delete(fd.fail, "ContainerCreate")
	delete(fd.containers, "crew-host-abc-1")
	fd.fail["ContainerStart"] = errors.New("the microVM did not boot")
	if _, err := o.StartHosted(ctx, hostedSpec(n)); err == nil {
		t.Fatal("a microVM that did not boot started")
	}
	if got := lastCall(fd, "ContainerRemove"); got != "ContainerRemove(id-crew-host-abc-1)" {
		t.Fatalf("the microVM that did not start was removed by %q", got)
	}
}
