package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// A start that failed removes what its creates made, by their IDs, and nothing under the name that it
// did not make and whose labels do not name this server: an unlabelled container or bridge is left
// (security review of the release of 0.3.3, finding 1; BUG-018).
func TestAFailedStartLeavesWhatItDidNotMake(t *testing.T) {
	ctx := context.Background()
	refused := errors.New("Conflict. The name is already in use")
	for _, tc := range []struct {
		name string
		// net or ctr is the name an unlabelled bridge or container holds; fail is the create that meets it.
		net, ctr, fail string
		start          func(*Orchestrator) error
	}{
		{"a sandbox whose bridge was not made", "crew-run-1", "", "NetworkCreate",
			func(o *Orchestrator) error {
				_, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"})
				return err
			}},
		{"a sandbox whose container was not made", "", "crew-run-1", "ContainerCreate",
			func(o *Orchestrator) error {
				_, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"})
				return err
			}},
		{"a hosted bridge that was not made", "crew-host-abc-1", "", "NetworkCreate",
			func(o *Orchestrator) error { _, err := o.CreateHostedNet(ctx, "host-abc-1", true); return err }},
		{"a probe that was not made", "", "crew-host-abc-1-probe", "ContainerCreate",
			func(o *Orchestrator) error {
				n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
				if err != nil {
					return err
				}
				hostedProbeExits(o.docker.(*fakeDocker), map[string]int{"1.1.1.1/443": 0}, 124)
				return o.ProbeHosted(ctx, "host-abc-1", n)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := hostedOrch()
			unlabelled := &fakeContainer{id: "id-unlabelled", name: tc.ctr, running: true}
			unlabelledNet := &fakeNetwork{name: tc.net + tc.ctr, id: "net-unlabelled", bridge: "app-x", subnet: "172.20.9.0/24"}
			if tc.net != "" {
				fd.networks[tc.net] = unlabelledNet
			} else {
				fd.containers[tc.ctr] = unlabelled
			}
			fd.onCall = func(m string) {
				if m != tc.fail {
					return
				}
				fd.mu.Lock()
				defer fd.mu.Unlock()
				fd.fail[tc.fail] = refused
				// The start's own bridge is kept by its ID while an unlabelled one takes its name: the
				// clean-up must remove it by the ID its create gave.
				if ours, ok := fd.networks["crew-run-1"]; ok && tc.ctr == "crew-run-1" {
					fd.networks["crew-run-1 (by its id)"] = ours
					fd.networks["crew-run-1"] = unlabelledNet
				}
			}
			if err := tc.start(o); err == nil {
				t.Fatal("a start whose create was refused started")
			}
			if tc.net != "" && (fd.networks[tc.net] != unlabelledNet || fd.callsTo("NetworkRemove") != 0) {
				t.Fatalf("the unlabelled bridge under the name was removed: %v", fd.calls)
			}
			if tc.ctr != "" && (fd.containers[tc.ctr] != unlabelled || fd.callsTo("ContainerRemove") != 0) {
				t.Fatalf("the unlabelled container under the name was removed: %v", fd.calls)
			}
			if tc.name == "a sandbox whose container was not made" {
				_, kept := fd.networks["crew-run-1 (by its id)"]
				if kept || fd.networks["crew-run-1"] != unlabelledNet || lastCall(fd, "NetworkRemove") != "NetworkRemove(net-crew-run-1)" {
					t.Fatalf("the bridge the start made was not removed by its ID: %v", fd.calls)
				}
			}
		})
	}
}

// A bridge of this server's left under the name from before is removed when a start's create meets it,
// by the ID its labels were read with, so the next start can make it.
func TestAFailedStartRemovesThisServersLeftover(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	fd.networks["crew-run-1"] = &fakeNetwork{name: "crew-run-1", id: "net-left", bridge: "mdl-x",
		labels: map[string]string{LabelSandbox: "run-1", LabelServer: "crew-server"}}
	fd.fail["NetworkCreate"] = errors.New("network with name crew-run-1 already exists")
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err == nil {
		t.Fatal("a start whose bridge was refused started")
	}
	if _, ok := fd.networks["crew-run-1"]; ok || lastCall(fd, "NetworkRemove") != "NetworkRemove(net-left)" {
		t.Fatalf("this server's leftover bridge: %v", fd.calls)
	}
}

// A container or bridge under a sandbox's name with no sandbox label is no sandbox of this server's to
// remove or to join: every sandbox Crew makes has its labels (since M2), so a removal, which a failed
// start's caller makes too, leaves one without, and the ingress joins no such bridge (security review
// of the release of 0.3.3, findings 1 and 3).
func TestAnUnlabelledContainerOrBridgeIsNotThisServers(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	unlabelled := &fakeContainer{id: "id-unlabelled", name: "crew-run-1", running: true}
	fd.containers["crew-run-1"] = unlabelled
	if err := o.RemoveSandbox(ctx, "run-1"); err != nil || fd.containers["crew-run-1"] != unlabelled {
		t.Fatalf("a removal of an unlabelled container: %v", err)
	}
	delete(fd.containers, "crew-run-1")
	bridge := &fakeNetwork{name: "crew-host-abc-1", id: "net-unlabelled", bridge: "app-x", subnet: "172.20.9.0/24"}
	fd.networks["crew-host-abc-1"] = bridge
	if err := o.JoinIngress(ctx, "host-abc-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("the ingress joined an unlabelled bridge: %v", err)
	}
	if err := o.RemoveSandbox(ctx, "host-abc-1"); err != nil || fd.networks["crew-host-abc-1"] != bridge || fd.callsTo("NetworkRemove") != 0 {
		t.Fatalf("a removal of an unlabelled bridge: %v %v", err, fd.calls)
	}
}

// A failed start leaves a bridge under the name that is another server's, and one whose labels could
// not be read (review of f6855da, finding 2).
func TestAFailedStartLeavesAnotherServersOrAnUnreadBridge(t *testing.T) {
	ctx := context.Background()
	for name, inspect := range map[string]error{"another server's": nil, "one whose labels could not be read": errors.New("the daemon is busy")} {
		t.Run(name, func(t *testing.T) {
			o, fd := hostedOrch()
			left := &fakeNetwork{name: "crew-run-1", id: "net-left", bridge: "mdl-x",
				labels: map[string]string{LabelSandbox: "run-1", LabelServer: "t19-server"}}
			if inspect != nil {
				left.labels[LabelServer] = "crew-server"
			}
			fd.networks["crew-run-1"] = left
			fd.fail["NetworkCreate"] = errors.New("network with name crew-run-1 already exists")
			fd.onCall = func(m string) {
				if m == "ContainerInspect" && inspect != nil {
					fd.mu.Lock()
					fd.fail["NetworkInspect"] = inspect
					fd.mu.Unlock()
				}
			}
			if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err == nil {
				t.Fatal("a start whose bridge was refused started")
			}
			if fd.networks["crew-run-1"] != left || fd.callsTo("NetworkRemove")+fd.callsTo("NetworkDisconnect") != 0 {
				t.Fatalf("the bridge under the name: %v", fd.calls)
			}
		})
	}
}

// The actions on a sandbox leave an unlabelled container under its name too (security review of the
// release of 0.3.3, the extension over ff1e7fd, finding 2). A command runs nothing in it, its
// keystrokes included, and the harness's read of whether it runs is ErrAnothers (the extension over
// 325ff81, finding 1); since 0.84 no command of the orchestrator counts one as this server's.
func TestTheActionsOnASandboxLeaveAnUnlabelledContainer(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	fd.containers["crew-run-1"] = &fakeContainer{id: "id-unlabelled", name: "crew-run-1", running: true}
	for name, act := range map[string]func(context.Context, string) error{"restart": o.Restart, "stop": o.StopHosted,
		"start again": o.StartAgain, "pause": pauses(o), "adopt": o.AdoptSandbox, "cut": o.CutSandbox, "join the ingress": o.JoinIngress,
		"read its log": readsLogs(o), "read whether it runs": readsRunning(o), "exec": execs(o),
		"exec detached": execsDetached(o), "start a terminal": startsInteractive(o),
		"read whether its container runs": readsContainerRunning(o)} {
		if err := act(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
			t.Fatalf("%s of an unlabelled container: %v", name, err)
		}
	}
	if n := fd.callsTo("ContainerExecCreate"); n != 0 {
		t.Fatalf("%d commands made in an unlabelled container", n)
	}
}

// Every use of a sandbox's bridge leaves an unlabelled bridge under its name, this server's container
// on it or not: a removal takes the container and leaves the bridge, and an adoption, a hosted start
// and a probe use none (review of ff1e7fd, findings 1 and 2).
func TestEveryUseOfASandboxsBridgeLeavesAnUnlabelledOne(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	unlabelled := &fakeNetwork{name: "crew-run-1", id: "net-unlabelled", bridge: "mdl-x", subnet: "172.20.9.0/24"}
	fd.networks["crew-run-1"] = unlabelled
	if err := o.AdoptSandbox(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("an adoption on an unlabelled bridge: %v", err)
	}
	if err := o.CutSandbox(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a cut on an unlabelled bridge: %v", err)
	}
	if err := o.RemoveSandbox(ctx, "run-1"); err != nil || fd.containers["crew-run-1"] != nil ||
		fd.networks["crew-run-1"] != unlabelled || fd.callsTo("NetworkRemove") != 0 {
		t.Fatalf("the removal: %v, container left %v, calls %v", err, fd.containers["crew-run-1"] != nil, fd.calls)
	}
	// The container gone, the unlabelled bridge under its name is no gone sandbox of this server's.
	if err := o.StopHosted(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a stop of a gone sandbox with an unlabelled bridge: %v", err)
	}
	if _, err := o.HostedRunning(ctx, "run-1"); !errors.Is(err, ErrAnothers) {
		t.Fatalf("whether a gone sandbox with an unlabelled bridge runs: %v", err)
	}
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	fd.networks["crew-host-abc-1"] = &fakeNetwork{name: "crew-host-abc-1", id: "net-unlabelled-2", bridge: "app-x", subnet: "172.20.9.0/24"}
	hostedProbeExits(fd, map[string]int{"1.1.1.1/443": 0}, 124)
	if err := o.ProbeHosted(ctx, "host-abc-1", n); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a probe on an unlabelled bridge: %v", err)
	}
	if _, err := o.StartHosted(ctx, hostedSpec(n)); !errors.Is(err, ErrAnothers) {
		t.Fatalf("a hosted microVM on an unlabelled bridge: %v", err)
	}
}

// A sandbox's own labels are not the caller's to change: a spec's labels cannot make it another
// server's, nor another sandbox (security review of the release of 0.3.3, the extension over ff1e7fd,
// finding 3).
func TestASpecsLabelsDoNotChangeWhoseTheSandboxIs(t *testing.T) {
	o, fd := sandboxOrch()
	spec := SandboxSpec{ID: "run-1", Kind: "run", Labels: map[string]string{LabelServer: "t19-server", LabelSandbox: "run-2", "crew.extra": "x"}}
	if _, err := o.StartSandbox(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	l := fd.containers["crew-run-1"].labels
	if l[LabelServer] != "crew-server" || l[LabelSandbox] != "run-1" || l["crew.extra"] != "x" {
		t.Fatalf("the sandbox's labels: %v", l)
	}
}
