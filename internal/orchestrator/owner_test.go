package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An action by a sandbox's name acts on the container the owner check read: one another server makes
// under the name between the check and the action is not touched (security review of 54f60eb, round
// 2, finding 2), as a command runs in it (BUG-015).
func TestAnActionByNameActsOnTheContainerTheCheckRead(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, method string
		act          func(*Orchestrator) func(context.Context, string) error
		// skip is how many calls of the method come before the action's own: the check's inspect.
		skip int
	}{
		{"stop", "ContainerStop", func(o *Orchestrator) func(context.Context, string) error { return o.StopHosted }, 0},
		{"restart", "ContainerRestart", func(o *Orchestrator) func(context.Context, string) error { return o.Restart }, 0},
		{"start again", "ContainerStart", func(o *Orchestrator) func(context.Context, string) error { return o.StartAgain }, 0},
		{"pause", "ContainerPause", pauses, 0},
		{"remove", "ContainerRemove", func(o *Orchestrator) func(context.Context, string) error { return o.RemoveSandbox }, 0},
		{"read its log", "ContainerLogs", readsLogs, 0},
		{"read its stats", "ContainerStatsOneShot", readsStats, 0},
		{"read whether it runs", "ContainerInspect", readsRunning, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := sandboxOrch()
			mine := func() *fakeContainer {
				return &fakeContainer{id: "id-mine", name: "crew-host-c", paused: true, running: true,
					labels: map[string]string{LabelSandbox: "host-c", LabelServer: "crew-server"}}
			}
			fd.containers["crew-host-c"] = mine()
			// Without the swap the action reaches this server's sandbox: the refusal below is the swap's.
			if err := tc.act(o)(ctx, "host-c"); err != nil || lastCall(fd, tc.method) == "" {
				t.Fatalf("%s of this server's sandbox: %v, %q", tc.name, err, lastCall(fd, tc.method))
			}
			fd.containers["crew-host-c"] = mine()
			theirs := &fakeContainer{id: "id-theirs", name: "crew-host-c", paused: true, running: true,
				labels: map[string]string{LabelSandbox: "host-c", LabelServer: "t19-server"}}
			seen := 0
			fd.onCall = func(m string) {
				if m != tc.method {
					return
				}
				if seen++; seen == tc.skip+1 {
					fd.mu.Lock()
					fd.containers["crew-host-c"] = theirs
					fd.mu.Unlock()
				}
			}
			_ = tc.act(o)(ctx, "host-c")
			fd.mu.Lock()
			defer fd.mu.Unlock()
			if fd.containers["crew-host-c"] != theirs || !theirs.running || !theirs.paused {
				t.Fatalf("%s reached the container made under the name after the check: %+v", tc.name, theirs)
			}
			if got := lastCallLocked(fd, tc.method); got != tc.method+"(id-mine)" {
				t.Fatalf("%s went by name: %q", tc.name, got)
			}
		})
	}
}

// pauses is Pause as an action that gives only its error.
func pauses(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		_, err := o.Pause(ctx, id)
		return err
	}
}

// readsStats is SandboxStats as an action that gives only its error.
func readsStats(o *Orchestrator) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		_, err := o.SandboxStats(ctx, id)
		return err
	}
}

// An unpause undoes the pause by the ID the pause's check read, with no check of its own: a container
// another server makes under the name after the pause stays paused, and a check that would error
// leaves no live app paused (security review of b39f701, finding 2).
func TestAnUnpauseUndoesThePauseByItsID(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	mine := &fakeContainer{id: "id-mine", name: "crew-host-c", running: true,
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "crew-server"}}
	fd.containers["crew-host-c"] = mine
	resume, err := o.Pause(ctx, "host-c")
	if err != nil || !mine.paused {
		t.Fatalf("the pause: %v", err)
	}
	theirs := &fakeContainer{id: "id-theirs", name: "crew-host-c", running: true, paused: true,
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "t19-server"}}
	fd.containers["crew-host-c"] = theirs
	fd.containers["crew-host-c-mine"] = mine
	fd.fail = map[string]error{"ContainerInspect": errors.New("the daemon is busy")}
	if err := resume(ctx); err != nil || mine.paused || !theirs.paused {
		t.Fatalf("the unpause: %v, this server's paused %v, the other's paused %v", err, mine.paused, theirs.paused)
	}
	if got := lastCall(fd, "ContainerUnpause"); got != "ContainerUnpause(id-mine)" {
		t.Fatalf("the unpause went by %q", got)
	}
}

// lastCall is the newest recorded call of a method.
func lastCall(fd *fakeDocker, method string) string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return lastCallLocked(fd, method)
}

func lastCallLocked(fd *fakeDocker, method string) string {
	for i := len(fd.calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(fd.calls[i], method+"(") {
			return fd.calls[i]
		}
	}
	return ""
}

// An adoption learns the doors of the container the owner check read: one another server makes
// under the name after the check gives the broker no door to answer.
func TestAnAdoptionReadsTheContainerTheCheckRead(t *testing.T) {
	ctx := context.Background()
	o, fd := sandboxOrch()
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-1", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	o.doors.drop("run-1") // as after a restart of the server
	fd.mu.Lock()
	mine := fd.containers["crew-run-1"]
	theirs := *mine
	fd.mu.Unlock()
	theirs.id = "id-theirs"
	theirs.labels = map[string]string{LabelSandbox: "run-1", LabelServer: "t19-server"}
	theirs.ips = map[string]string{"crew-run-1": "10.9.1.3"}
	inspects := 0
	fd.onCall = func(m string) {
		if m == "ContainerInspect" {
			if inspects++; inspects == 2 {
				fd.mu.Lock()
				fd.containers["crew-run-1"] = &theirs
				fd.mu.Unlock()
			}
		}
	}
	_ = o.AdoptSandbox(ctx, "run-1")
	if id, ok := o.SandboxAt("10.9.1.254:8091", "10.9.1.3:1"); ok {
		t.Fatalf("the broker answers the container made under the name after the check: %q", id)
	}
	if got := lastCall(fd, "ContainerInspect"); got != "ContainerInspect("+mine.id+")" {
		t.Fatalf("the adoption read its address by name: %q", got)
	}
}

// The probe's removal removes the probe the check read: one another server makes under its name after
// the check is left (security review of b39f701, finding 3).
func TestTheProbeRemovesTheProbeTheCheckRead(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	n, err := o.CreateHostedNet(ctx, "host-abc-1", true)
	if err != nil {
		t.Fatal(err)
	}
	// A probe of this server's left from before: the new one cannot be made, and the removal takes it.
	fd.containers["crew-host-abc-1-probe"] = &fakeContainer{id: "id-probe", name: "crew-host-abc-1-probe", running: true,
		labels: map[string]string{LabelSandbox: "host-abc-1-probe", LabelServer: "crew-server"}}
	fd.fail = map[string]error{"ContainerCreate": errors.New("Conflict. The container name is already in use")}
	theirs := &fakeContainer{id: "id-theirs", name: "crew-host-abc-1-probe", running: true,
		labels: map[string]string{LabelSandbox: "host-abc-1-probe", LabelServer: "t19-server"}}
	fd.onCall = func(m string) {
		if m == "ContainerRemove" {
			fd.mu.Lock()
			fd.containers["crew-host-abc-1-probe"] = theirs
			fd.mu.Unlock()
		}
	}
	_ = o.ProbeHosted(ctx, "host-abc-1", n)
	if fd.containers["crew-host-abc-1-probe"] != theirs || lastCall(fd, "ContainerRemove") != "ContainerRemove(id-probe)" {
		t.Fatalf("the probe's removal reached the container made under its name after the check: %q", lastCall(fd, "ContainerRemove"))
	}
}

// A removal that finds no container removes none by name: one another server makes under the name
// after the check is left, and only the bridge goes (security review of b39f701, finding 3).
func TestARemovalThatFoundNoContainerRemovesNone(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	fd.networks["crew-host-c"] = &fakeNetwork{name: "crew-host-c", bridge: "app-c", subnet: "172.20.9.0/24", gateway: "172.20.9.1",
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "crew-server"}}
	theirs := &fakeContainer{id: "id-theirs", name: "crew-host-c", running: true,
		labels: map[string]string{LabelSandbox: "host-c", LabelServer: "t19-server"}}
	fd.onCall = func(m string) {
		// The check's read of the bridge comes after it found no container.
		if m == "NetworkInspect" {
			fd.mu.Lock()
			fd.containers["crew-host-c"] = theirs
			fd.mu.Unlock()
		}
	}
	err := o.RemoveSandbox(ctx, "host-c")
	if fd.containers["crew-host-c"] != theirs || fd.callsTo("ContainerRemove") != 0 {
		t.Fatalf("a removal that found no container removed one: %d removes", fd.callsTo("ContainerRemove"))
	}
	if _, ok := fd.networks["crew-host-c"]; err != nil || ok || lastCall(fd, "NetworkRemove") != "NetworkRemove(net-crew-host-c)" {
		t.Fatalf("the bridge of this server's sandbox: %v, left %v, %q", err, ok, lastCall(fd, "NetworkRemove"))
	}
}

// A sandbox whose container is gone is stopped, and does not run: a roll back's stop and the start
// of a lost live sandbox read it so (internal/hosting deploy.go, recover.go; review of b39f701).
func TestAGoneSandboxIsStoppedAndDoesNotRun(t *testing.T) {
	ctx := context.Background()
	o, _ := hostedOrch()
	if err := o.StopHosted(ctx, "host-c"); err != nil {
		t.Fatalf("stopping a gone sandbox: %v", err)
	}
	if running, err := o.HostedRunning(ctx, "host-c"); err != nil || running {
		t.Fatalf("whether a gone sandbox runs: %v %v", running, err)
	}
}

// A gone container whose bridge cannot be read is not taken for a sandbox that is simply gone: the
// check gives the bridge's error, so a command, a terminal and a stop act on nothing by name
// (security review of the range 6d8a71f..5034f55, finding 1).
func TestAGoneSandboxWhoseBridgeCannotBeReadGivesTheError(t *testing.T) {
	ctx := context.Background()
	o, fd := hostedOrch()
	fd.fail["NetworkInspect"] = errors.New("the daemon is busy")
	for name, act := range map[string]func(context.Context, string) error{"exec": execs(o),
		"start a terminal": startsInteractive(o), "stop": o.StopHosted} {
		if err := act(ctx, "run-1"); err == nil || !strings.Contains(err.Error(), "reading whose the bridge") {
			t.Fatalf("%s of a gone sandbox whose bridge cannot be read: %v", name, err)
		}
	}
	if n := fd.callsTo("ContainerExecCreate") + fd.callsTo("ContainerStop"); n != 0 {
		t.Fatalf("%d actions on a gone sandbox whose bridge cannot be read", n)
	}
}
