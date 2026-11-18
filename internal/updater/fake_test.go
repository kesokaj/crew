package updater_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/kesokaj/crew/internal/release"
)

// fakeMachine plays Docker for the updater: it records every call, and fails the ones a test names,
// as "Method" or "Method version" or "Method container".
type fakeMachine struct {
	mu      sync.Mutex
	calls   []string
	fail    map[string]error
	running string // the version the real server runs, "" while it is stopped
	schema  map[string]int
	// unhealthyAfter makes Health of crew-server fail after this many answers, for the watch.
	unhealthyAfter int
	healthy        int
	// blockLoad holds Load until it is closed; loadImage, when set, runs in each LoadImage.
	blockLoad chan struct{}
	loadImage func()
	// loadIDs is the id LoadImage gives a tag, as this machine's image store would (D48).
	loadIDs map[string]string
}

func newFake(running string) *fakeMachine {
	return &fakeMachine{fail: map[string]error{}, running: running, unhealthyAfter: -1}
}

func (m *fakeMachine) call(name string, args ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := strings.TrimSpace(name + " " + strings.Join(args, " "))
	m.calls = append(m.calls, c)
	if err, ok := m.fail[c]; ok {
		return err
	}
	if err, ok := m.fail[name]; ok {
		return err
	}
	return nil
}

func (m *fakeMachine) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *fakeMachine) called(name string) bool {
	for _, c := range m.Calls() {
		if c == name || strings.HasPrefix(c, name+" ") {
			return true
		}
	}
	return false
}

func (m *fakeMachine) Running() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *fakeMachine) Load(ctx context.Context, f *release.Folder) error {
	if m.blockLoad != nil {
		<-m.blockLoad
	}
	return m.call("Load", f.Manifest.Version)
}
func (m *fakeMachine) Snapshot(_ context.Context, version, file string) error {
	if err := m.call("Snapshot", version); err != nil {
		return err
	}
	return os.WriteFile(file, []byte("snapshot of "+version), 0o600)
}
func (m *fakeMachine) Scratch(context.Context) error     { return m.call("Scratch") }
func (m *fakeMachine) DropScratch(context.Context) error { return m.call("DropScratch") }
func (m *fakeMachine) RestoreScratch(_ context.Context, f *release.Folder, snapshot string) error {
	body, _ := os.ReadFile(snapshot)
	return m.call("RestoreScratch", f.Manifest.Version, string(body))
}
func (m *fakeMachine) StartCheck(_ context.Context, f *release.Folder) error {
	return m.call("StartCheck", f.Manifest.Version)
}
func (m *fakeMachine) Smoke(_ context.Context, f *release.Folder) error {
	return m.call("Smoke", f.Manifest.Version)
}
func (m *fakeMachine) Selftest(context.Context) error { return m.call("Selftest") }
func (m *fakeMachine) Browser(_ context.Context, f *release.Folder) error {
	return m.call("Browser", f.Manifest.Version)
}
func (m *fakeMachine) StopCheck(context.Context) error { return m.call("StopCheck") }
func (m *fakeMachine) Health(_ context.Context, container string) error {
	if err := m.call("Health", container); err != nil {
		return err
	}
	if container == "crew-server" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running == "" {
			return errors.New("no server runs")
		}
		m.healthy++
		if m.unhealthyAfter >= 0 && m.healthy > m.unhealthyAfter {
			return fmt.Errorf("%s answers 503", m.running)
		}
	}
	return nil
}
func (m *fakeMachine) Proof(_ context.Context, container string) error {
	return m.call("Proof", container)
}
func (m *fakeMachine) StopServer(context.Context) error {
	if err := m.call("StopServer"); err != nil {
		return err
	}
	m.mu.Lock()
	m.running = ""
	m.mu.Unlock()
	return nil
}
func (m *fakeMachine) StartServer(_ context.Context, version string) error {
	if err := m.call("StartServer", version); err != nil {
		return err
	}
	m.mu.Lock()
	m.running, m.healthy = version, 0
	m.mu.Unlock()
	return nil
}
func (m *fakeMachine) Migrate(_ context.Context, version string) error {
	return m.call("Migrate", version)
}
func (m *fakeMachine) Restore(_ context.Context, version, snapshot string) error {
	body, _ := os.ReadFile(snapshot)
	return m.call("Restore", version, string(body))
}
func (m *fakeMachine) Remove(_ context.Context, version string) error {
	return m.call("Remove", version)
}
func (m *fakeMachine) LoadImage(_ context.Context, path, tag string) (string, error) {
	if m.loadImage != nil {
		m.loadImage()
	}
	if err := m.call("LoadImage", tag); err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if id, ok := m.loadIDs[tag]; ok {
		return id, nil
	}
	return fmt.Sprintf("sha256:%064x", len(body)), nil
}
func (m *fakeMachine) Untag(_ context.Context, tag string) error { return m.call("Untag", tag) }
