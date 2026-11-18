package hosting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/orchestrator"
)

// fakeRuntime is the orchestrator as hosting uses it: bridges, hosted sandboxes and images in memory,
// recording what it was asked.
type fakeRuntime struct {
	mu       sync.Mutex
	joined   []string
	nets     map[string]orchestrator.HostedNet
	subnets  int
	running  map[string]bool // sandbox → it runs
	paused   map[string]bool
	started  []orchestrator.HostedSpec
	removed  []string
	images   map[string]string // tag → id
	removedI []string
	loads    int
	// loadID is the id a load gives; "" gives the cleaned config's, as Docker's overlay2 store does.
	loadID   string
	probeErr error
	startErr error
	pauseErr error
	restarts map[string]int
	again    []string
	calls    []string
	// volumes, ports and labels are what ImageConfig says of every image.
	volumes, ports, labels []string
	// logs is what every sandbox printed, as Docker gives it with its stamps; "" gives two lines.
	logs string
	// logsOf is what one sandbox printed, before logs.
	logsOf map[string]string
	// onProbe is called by each probe, unlocked.
	onProbe func()
	// onAgain is called by each StartAgain, unlocked, before the sandbox runs again.
	onAgain func(id string)
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{nets: map[string]orchestrator.HostedNet{}, running: map[string]bool{}, paused: map[string]bool{},
		images: map[string]string{}, restarts: map[string]int{}}
}

func (f *fakeRuntime) call(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeRuntime) JoinIngress(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joined = append(f.joined, id)
	return nil
}

func (f *fakeRuntime) CreateHostedNet(_ context.Context, id string, internet bool) (orchestrator.HostedNet, error) {
	f.call(fmt.Sprintf("net %s %v", id, internet))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subnets++
	p := fmt.Sprintf("172.20.%d.", f.subnets)
	n := orchestrator.HostedNet{Name: orchestrator.SandboxName(id), Server: p + "254", Ingress: p + "253", App: p + "252", Probe: p + "251"}
	f.nets[id] = n
	return n, nil
}

func (f *fakeRuntime) ProbeHosted(_ context.Context, id string, _ orchestrator.HostedNet) error {
	f.call("probe " + id)
	if f.onProbe != nil {
		f.onProbe()
	}
	return f.probeErr
}

func (f *fakeRuntime) StartHosted(_ context.Context, spec orchestrator.HostedSpec) (orchestrator.Sandbox, error) {
	f.call("start " + spec.ID)
	if f.startErr != nil {
		return orchestrator.Sandbox{}, f.startErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	f.running[spec.ID] = true
	return orchestrator.Sandbox{ID: spec.ID, IP: spec.Net.App}, nil
}

func (f *fakeRuntime) RemoveSandbox(_ context.Context, id string) error {
	f.call("remove " + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	delete(f.running, id)
	delete(f.nets, id)
	return nil
}

func (f *fakeRuntime) LoadImage(_ context.Context, r io.Reader) (string, error) {
	raw, _ := io.ReadAll(r)
	f.mu.Lock()
	f.loads++
	id := f.loadID
	f.mu.Unlock()
	if id == "" {
		img, err := CleanImage(bytes.NewReader(raw), io.Discard)
		if err != nil {
			return "", err
		}
		if bytes.Contains(raw, []byte("RepoTags\":[")) {
			return "", errors.New("the tar Docker was given names an image")
		}
		id = img.ID
	}
	return id, nil
}

func (f *fakeRuntime) TagImage(_ context.Context, id, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = id
	return nil
}

func (f *fakeRuntime) RemoveImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedI = append(f.removedI, ref)
	delete(f.images, ref)
	return nil
}

func (f *fakeRuntime) HasImage(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.images[ref]
	return ok, nil
}

func (f *fakeRuntime) ImageConfig(_ context.Context, ref string) ([]string, []string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes, f.ports, f.labels, nil
}

func (f *fakeRuntime) Pause(_ context.Context, id string) (func(context.Context) error, error) {
	f.call("pause " + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pauseErr != nil {
		return nil, f.pauseErr
	}
	f.paused[id] = true
	return func(context.Context) error {
		f.call("unpause " + id)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paused[id] = false
		return nil
	}, nil
}

func (f *fakeRuntime) Restart(_ context.Context, id string) error {
	f.call("restart " + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts[id]++
	return nil
}

func (f *fakeRuntime) StopHosted(_ context.Context, id string) error {
	f.call("stop " + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[id] = false
	return nil
}

func (f *fakeRuntime) StartAgain(_ context.Context, id string) error {
	f.call("start again " + id)
	if f.onAgain != nil {
		f.onAgain(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.again = append(f.again, id)
	f.running[id] = true
	return nil
}

func (f *fakeRuntime) HostedRunning(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[id], nil
}

func (f *fakeRuntime) HostedLogs(_ context.Context, id string, _ time.Time) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.logsOf[id]; ok {
		return []byte(l), nil
	}
	if f.logs != "" {
		return []byte(f.logs), nil
	}
	return []byte("listening on 8080\npanic: no database"), nil
}

// app is the address of a sandbox's app on its bridge, "" when it has no bridge.
func (f *fakeRuntime) app(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nets[id].App
}

// fakeHealth answers a health address by the sandbox at its host: 200 while it runs, unless answer
// says otherwise.
type fakeHealth struct {
	rt     *fakeRuntime
	answer func(sandbox string) int
}

func (h *fakeHealth) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	h.rt.mu.Lock()
	id := ""
	for sid, n := range h.rt.nets {
		if n.App == host {
			id = sid
		}
	}
	runs, paused := h.rt.running[id], h.rt.paused[id]
	h.rt.mu.Unlock()
	if id == "" || !runs || paused {
		return nil, errors.New("connect: no route to host")
	}
	code := 200
	if h.answer != nil {
		code = h.answer(id)
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

// fakeGit is a project's repository: main's head, the files of its commits, and its archive.
type fakeGit struct {
	mu    sync.Mutex
	head  string
	files map[string][]byte // path → body, at every commit
}

func (g *fakeGit) Main(context.Context, string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.head
}

func (g *fakeGit) FileAt(_ context.Context, _, _, path string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.files[path], nil
}

func (g *fakeGit) Archive(_ context.Context, _, commit string, w io.Writer) error {
	_, err := io.WriteString(w, "the tree of "+commit)
	return err
}
