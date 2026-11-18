package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/config"
)

type boots struct{ heard []string }

func (b *boots) on(ok bool, why string) {
	if ok {
		b.heard = append(b.heard, "ok")
		return
	}
	b.heard = append(b.heard, "failed: "+why)
}

// A boot is counted at one seam, StartSandbox (21, D10): Docker's start that fails and a guest that
// never becomes ready are a microVM that did not boot, ErrNoBoot, and OnBoot hears false; a guest that
// becomes ready is a boot, and OnBoot hears true, also when the firewall's proof fails after it.
// What fails before the start, the network, joining it, or a missing image, and a start whose context
// was cancelled, is no boot: OnBoot hears nothing and the error is no ErrNoBoot.
func TestABootIsCountedAtStartSandbox(t *testing.T) {
	start := func(set func(o *Orchestrator, fd *fakeDocker), spec SandboxSpec) (*boots, error) {
		t.Helper()
		o, fd := sandboxOrch()
		b := &boots{}
		o.OnBoot = b.on
		o.readyTimeout = 50 * time.Millisecond
		if set != nil {
			set(o, fd)
		}
		if spec.ID == "" {
			spec = SandboxSpec{ID: "run-abc", Kind: "run", Minimal: true}
		}
		_, err := o.StartSandbox(context.Background(), spec)
		return b, err
	}
	notReady := func(cmd string) int {
		if strings.Contains(cmd, "/run/crew/ready") {
			return 1
		}
		return 0
	}

	b, err := start(func(_ *Orchestrator, fd *fakeDocker) { fd.fail["ContainerStart"] = errors.New("kata: no /dev/kvm") }, SandboxSpec{})
	if !errors.Is(err, ErrNoBoot) || len(b.heard) != 1 || !strings.HasPrefix(b.heard[0], "failed: ") || !strings.Contains(b.heard[0], "no /dev/kvm") {
		t.Fatalf("a failed start: %v, heard %v", err, b.heard)
	}
	// A long error is cut where it is made: the run, the log and the inbox get the short form.
	b, err = start(func(_ *Orchestrator, fd *fakeDocker) {
		fd.fail["ContainerStart"] = errors.New(strings.Repeat("kata stack ", 500))
	}, SandboxSpec{})
	if !errors.Is(err, ErrNoBoot) || len(err.Error()) > 500 || len(b.heard) != 1 || len(b.heard[0]) > 500 || !strings.HasSuffix(err.Error(), " …") {
		t.Fatalf("a long error: %d bytes, heard %v", len(err.Error()), len(b.heard))
	}
	b, err = start(func(_ *Orchestrator, fd *fakeDocker) { fd.execFor = notReady }, SandboxSpec{})
	if !errors.Is(err, ErrNoBoot) || len(b.heard) != 1 || !strings.Contains(b.heard[0], "not become ready") {
		t.Fatalf("a guest never ready: %v, heard %v", err, b.heard)
	}
	b, err = start(nil, SandboxSpec{})
	if err != nil || strings.Join(b.heard, ",") != "ok" {
		t.Fatalf("a boot: %v, heard %v", err, b.heard)
	}
	// The firewall's proof fails after the boot: the boot was counted, the error is no ErrNoBoot.
	b, err = start(func(_ *Orchestrator, fd *fakeDocker) { fd.execFor = probeExits(124, "1.1.1.1/443") },
		SandboxSpec{ID: "judge-abc", Kind: "judge", Minimal: true, Mode: config.ModeModelOnly})
	if err == nil || errors.Is(err, ErrNoBoot) || strings.Join(b.heard, ",") != "ok" {
		t.Fatalf("the proof failed after a boot: %v, heard %v", err, b.heard)
	}
	for name, method := range map[string]string{"the network": "NetworkCreate", "joining it": "NetworkConnect", "a missing image": "ContainerCreate"} {
		b, err = start(func(_ *Orchestrator, fd *fakeDocker) { fd.fail[method] = errors.New("no") }, SandboxSpec{})
		if err == nil || errors.Is(err, ErrNoBoot) || len(b.heard) != 0 {
			t.Fatalf("%s: %v, heard %v", name, err, b.heard)
		}
	}
	// A context cancelled during Docker's start: its error is the cancel's, no boot and no failure.
	sctx, scancel := context.WithCancel(context.Background())
	so, sfd := sandboxOrch()
	sb := &boots{}
	so.OnBoot = sb.on
	sfd.fail["ContainerStart"] = errors.New("context canceled")
	sfd.onCall = func(m string) {
		if m == "ContainerStart" {
			scancel()
		}
	}
	if _, err := so.StartSandbox(sctx, SandboxSpec{ID: "run-abc", Kind: "run", Minimal: true}); err == nil || errors.Is(err, ErrNoBoot) || len(sb.heard) != 0 {
		t.Fatalf("cancelled at Docker's start: %v, heard %v", err, sb.heard)
	}
	// A context cancelled while the guest is waited for: no boot and no failure.
	ctx, cancel := context.WithCancel(context.Background())
	o, fd := sandboxOrch()
	bb := &boots{}
	o.OnBoot, o.readyTimeout = bb.on, time.Minute
	fd.execFor = func(cmd string) int {
		if strings.Contains(cmd, "/run/crew/ready") {
			cancel()
			return 1
		}
		return 0
	}
	if _, err := o.StartSandbox(ctx, SandboxSpec{ID: "run-abc", Kind: "run", Minimal: true}); err == nil || errors.Is(err, ErrNoBoot) || len(bb.heard) != 0 {
		t.Fatalf("a cancelled context: %v, heard %v", err, bb.heard)
	}
}
