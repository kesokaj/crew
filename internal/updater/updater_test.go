package updater_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

type kit struct {
	t   *testing.T
	u   *updater.Updater
	m   *fakeMachine
	rel string
}

// newKit is an updater on a machine that runs running, with a release folder for each version, each
// past the gate unless its name is in noGate. schema gives a version's schema and readBy.
func newKit(t *testing.T, running string, versions ...string) *kit {
	t.Helper()
	root := t.TempDir()
	k := &kit{t: t, m: newFake(running), rel: filepath.Join(root, "releases")}
	k.u = &updater.Updater{Machine: k.m, Releases: k.rel, Dir: filepath.Join(root, "updater"),
		MachineSetup: func() (int, error) { return 2, nil },
		WatchFor:     60 * time.Millisecond, WatchEvery: 10 * time.Millisecond, HealthFor: 50 * time.Millisecond,
		Keep: 5, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, v := range versions {
		k.release(v, 51, 0, true)
	}
	if err := k.u.Begin("0.1.0", running); err != nil {
		t.Fatal(err)
	}
	return k
}

// release writes a release folder.
func (k *kit) release(version string, schema, readBy int, gate bool) *release.Folder {
	k.t.Helper()
	dir := filepath.Join(k.rel, version)
	if err := os.MkdirAll(filepath.Join(dir, release.ImagesDir), 0o755); err != nil {
		k.t.Fatal(err)
	}
	imgs := map[string]release.Image{}
	for _, name := range release.Images {
		if err := os.WriteFile(filepath.Join(dir, release.ImagesDir, name+".tar"), []byte(name+version), 0o644); err != nil {
			k.t.Fatal(err)
		}
		imgs[name] = release.Image{Tag: name + ":" + version, ID: "sha256:" + name}
	}
	if _, err := release.Write(dir, release.Facts{Version: version, Commit: "c", BuiltAt: time.Now(), Schema: schema, ReadBy: readBy,
		MachineSetup: 2, Images: imgs}, map[string]string{}, nil); err != nil {
		k.t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		k.t.Fatal(err)
	}
	if gate {
		if err := f.WriteGate(release.Gate{Passed: true, At: time.Now(), InstallTest: "skipped"}); err != nil {
			k.t.Fatal(err)
		}
	}
	return f
}

// ask starts a request and waits for it to end.
func (k *kit) ask(a updater.Ask, root bool) (*updater.Request, error) {
	k.t.Helper()
	req, err := k.u.Start(context.Background(), a, root)
	if err != nil {
		return req, err
	}
	k.u.Wait()
	reqs, err := k.u.Requests(1)
	if err != nil || len(reqs) != 1 {
		k.t.Fatalf("the request's log: %v %v", reqs, err)
	}
	return reqs[0], nil
}

func (k *kit) running() string {
	st, err := k.u.State()
	if err != nil {
		k.t.Fatal(err)
	}
	return st.Running
}

func lastDetail(r *updater.Request) string {
	if len(r.Steps) == 0 {
		return ""
	}
	return r.Steps[len(r.Steps)-1].Detail
}

func TestAnUpdateChecksSwitchesWatchesAndKeeps(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	req, err := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if req.State != updater.StateDone || req.Action != updater.ActionInstall || req.From != "0.1.0" || req.Step != 7 {
		t.Fatalf("request %+v", req)
	}
	if k.running() != "0.2.0" || k.m.Running() != "0.2.0" {
		t.Fatalf("running %s on the machine %s", k.running(), k.m.Running())
	}
	// The order of 16.4: load, the snapshot of the running version on a scratch copy, the check
	// beside the running one, then the switch with the schema in one transaction, and the watch.
	want := []string{"Load 0.2.0", "Snapshot 0.1.0", "Scratch", "RestoreScratch 0.2.0 snapshot of 0.1.0", "StartCheck 0.2.0",
		"Health crew-server-check", "Smoke 0.2.0", "StopCheck", "DropScratch", "StopServer", "Migrate 0.2.0",
		"StartServer 0.2.0", "Health crew-server", "Proof crew-server"}
	got := k.m.Calls()
	if len(got) < len(want) || strings.Join(got[:len(want)], "|") != strings.Join(want, "|") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The watch says how long it watched.
	if req.Steps[5].Detail != "healthy for 60ms" {
		t.Fatalf("watch: %q", req.Steps[5].Detail)
	}
	for i, name := range []string{"Ask", "Load", "Try the state", "Check", "Switch", "Watch", "Keep"} {
		if req.Steps[i].Name != name || !req.Steps[i].OK {
			t.Fatalf("step %d: %+v", i+1, req.Steps[i])
		}
	}
	// The snapshot is kept with the version that was left.
	if _, err := os.Stat(filepath.Join(k.u.Dir, "snapshots", "0.1.0.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

// Row 53: a release that cannot start, cannot read the stored state, or cannot start a microVM is
// found in the check, beside the running version, and nothing was replaced.
func TestAReleaseThatFailsItsCheckChangesNothing(t *testing.T) {
	for _, c := range []struct{ fail, step string }{
		{"Load", "Load"},
		{"Snapshot", "Try the state"},
		{"RestoreScratch", "Try the state"},
		{"StartCheck", "Try the state"},
		{"Health crew-server-check", "Check"},
		{"Smoke 0.2.0", "Check"},
	} {
		t.Run(c.fail, func(t *testing.T) {
			k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
			k.m.fail[c.fail] = errors.New("it broke")
			req, err := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "clock"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if req.State != updater.StateFailed || !strings.HasPrefix(req.Error, c.step+": ") || !strings.Contains(req.Error, "it broke") {
				t.Fatalf("request %+v", req)
			}
			if k.m.called("StopServer") || k.m.called("Migrate") || k.m.called("StartServer") {
				t.Fatalf("the running version was touched: %v", k.m.Calls())
			}
			if k.running() != "0.1.0" || k.m.Running() != "0.1.0" {
				t.Fatal("the running version changed")
			}
			if c.fail != "Load" && c.fail != "Snapshot" && (!k.m.called("StopCheck") || !k.m.called("DropScratch")) {
				t.Fatalf("the check server or the scratch database was left: %v", k.m.Calls())
			}
		})
	}
}

// A schema change that fails at the switch is undone in its one transaction, and the old version
// starts again.
func TestASwitchWhoseSchemaFailsStartsTheOldVersionAgain(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	k.m.fail["Migrate 0.2.0"] = errors.New("step 52: no")
	req, _ := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false)
	if req.State != updater.StateFailed || !strings.Contains(req.Error, "undone") || k.m.Running() != "0.1.0" || k.running() != "0.1.0" {
		t.Fatalf("request %+v, machine runs %q", req, k.m.Running())
	}
}

// Row 54: a new version that turns unhealthy after the switch is switched back by the updater,
// without the operator.
func TestAVersionThatTurnsUnhealthyIsSwitchedBack(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	k.m.unhealthyAfter = 2
	req, _ := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "clock"}, false)
	if req.State != updater.StateSwitchedBack || !strings.Contains(req.Error, "switched back to 0.1.0") {
		t.Fatalf("request %+v", req)
	}
	if k.m.Running() != "0.1.0" || k.running() != "0.1.0" {
		t.Fatalf("machine runs %q, state %q", k.m.Running(), k.running())
	}
	// Its schema the version before reads: no snapshot is read back.
	if k.m.called("Restore") {
		t.Fatalf("a snapshot was read back: %v", k.m.Calls())
	}
	// A server that does not start at all after the switch is switched back too.
	k = newKit(t, "0.1.0", "0.1.0", "0.2.0")
	k.m.fail["StartServer 0.2.0"] = errors.New("exited 1")
	req, _ = k.ask(updater.Ask{Version: "0.2.0", AskedBy: "clock"}, false)
	if req.State != updater.StateSwitchedBack || k.m.Running() != "0.1.0" {
		t.Fatalf("request %+v", req)
	}
}

// Row 55: a schema the version before cannot read. Switching back in the watch reads the snapshot of
// the minutes before the switch back; a roll back later waits for the version's name, typed, and
// then reads back that version's own snapshot.
func TestASchemaTheVersionBeforeCannotRead(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	k.release("0.2.0", 53, 53, true)
	k.m.unhealthyAfter = 1
	req, _ := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false)
	if req.State != updater.StateSwitchedBack || !k.m.called("Restore 0.1.0 snapshot of 0.1.0") {
		t.Fatalf("request %+v, calls %v", req, k.m.Calls())
	}

	k = newKit(t, "0.1.0", "0.1.0")
	k.release("0.2.0", 53, 53, true)
	if req, _ := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); req.State != updater.StateDone {
		t.Fatalf("the update: %+v", req)
	}
	// Days later: back to 0.1.0, which cannot read schema 53.
	req, err := k.ask(updater.Ask{Version: "0.1.0", AskedBy: "operator"}, false)
	var refused *updater.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "everything written since is lost. Type 0.1.0") {
		t.Fatalf("a roll back that loses data went ahead: %+v %v", req, err)
	}
	if k.running() != "0.2.0" {
		t.Fatal("the refused roll back changed the running version")
	}
	req, err = k.ask(updater.Ask{Version: "0.1.0", AskedBy: "operator", Confirm: "0.1.0"}, false)
	if err != nil || req.State != updater.StateDone || req.Action != updater.ActionRollback || !req.Restores {
		t.Fatalf("roll back %+v %v", req, err)
	}
	// It was tried on its own snapshot, and that snapshot is what the real database got.
	if !k.m.called("RestoreScratch 0.1.0 snapshot of 0.1.0") || !k.m.called("Restore 0.1.0 snapshot of 0.1.0") || k.running() != "0.1.0" {
		t.Fatalf("calls %v", k.m.Calls())
	}
}

// A roll back to a kept version takes the same steps with its kept images, and nothing is rebuilt.
func TestARollBackToAKeptVersion(t *testing.T) {
	k := newKit(t, "0.2.0", "0.1.0", "0.2.0")
	req, err := k.ask(updater.Ask{Version: "0.1.0", AskedBy: "crew-ctl"}, true)
	if err != nil || req.State != updater.StateDone || req.Action != updater.ActionRollback || k.running() != "0.1.0" {
		t.Fatalf("%+v %v", req, err)
	}
	st, _ := k.u.State()
	if st.Previous != "0.2.0" {
		t.Fatalf("state %+v", st)
	}
}

// Step 1: what the updater refuses (16.4).
func TestTheUpdaterRefuses(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	k.release("0.3.0", 51, 0, false) // no gate
	f := k.release("0.4.0", 51, 0, true)
	p, _ := f.ImagePath("crew-server")
	if err := os.WriteFile(p, []byte("changed after the gate"), 0o644); err != nil {
		t.Fatal(err)
	}
	k.release("0.5.0", 51, 0, true)
	body, _ := os.ReadFile(filepath.Join(k.rel, "0.5.0", release.ManifestFile))
	body = []byte(strings.Replace(string(body), `"machineSetup": 2`, `"machineSetup": 3`, 1))
	_ = os.WriteFile(filepath.Join(k.rel, "0.5.0", release.ManifestFile), body, 0o644)
	// 0.5.0's gate was for the release.json before the edit: write one for this one.
	g, _ := release.Read(filepath.Join(k.rel, "0.5.0"))
	_ = g.WriteGate(release.Gate{Passed: true})
	for _, c := range []struct {
		ask  updater.Ask
		root bool
		why  string
	}{
		{updater.Ask{Version: "0.9.0", AskedBy: "operator"}, false, "there is no release 0.9.0"},
		{updater.Ask{Version: "latest", AskedBy: "operator"}, false, "is not a version"},
		{updater.Ask{Version: "0.3.0", AskedBy: "operator"}, false, "did not pass the gate"},
		{updater.Ask{Version: "0.3.0", AskedBy: "operator", ByHand: true}, false, "only by root"},
		{updater.Ask{Version: "0.4.0", AskedBy: "operator"}, false, "has changed since it was built"},
		{updater.Ask{Version: "0.5.0", AskedBy: "operator"}, false, "crew-ctl install --upgrade-runtime"},
		{updater.Ask{Version: "0.1.0", AskedBy: "operator"}, false, "is the version that runs"},
		{updater.Ask{Version: "0.2.0"}, false, "says who asks"},
	} {
		req, err := k.ask(c.ask, c.root)
		var refused *updater.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Reason, c.why) {
			t.Fatalf("%+v: %v, want %q", c.ask, err, c.why)
		}
		if req == nil || req.State != updater.StateFailed {
			t.Fatalf("a refusal is not in the log: %+v", req)
		}
	}
	if len(k.m.Calls()) != 0 || k.running() != "0.1.0" {
		t.Fatalf("a refused request did something: %v", k.m.Calls())
	}
	// Root gives a build by hand.
	req, err := k.ask(updater.Ask{Version: "0.3.0", AskedBy: "crew-ctl", ByHand: true}, true)
	if err != nil || req.State != updater.StateDone || !req.ByHand {
		t.Fatalf("by hand: %+v %v", req, err)
	}
}

// One request at a time, and one can be stopped before its switch.
func TestOneRequestAtATimeAndAStopBeforeTheSwitch(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	block := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(block) }) }
	t.Cleanup(unblock)
	k.m.blockLoad = block
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); !errors.Is(err, updater.ErrBusy) {
		t.Fatalf("a second request: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- k.u.Stop() }()
	time.Sleep(20 * time.Millisecond)
	unblock()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	reqs, _ := k.u.Requests(1)
	if reqs[0].State != updater.StateFailed || !strings.Contains(reqs[0].Error, "stopped by the operator") || k.m.called("StopServer") {
		t.Fatalf("%+v %v", reqs[0], k.m.Calls())
	}
}

// Five releases are kept by default (Updater.Keep), with their images and snapshots; never the running
// one or the one before.
func TestTheLastFiveAreKept(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", "0.6.0", "0.7.0")
	req, _ := k.ask(updater.Ask{Version: "0.7.0", AskedBy: "operator"}, false)
	if req.State != updater.StateDone {
		t.Fatalf("%+v", req)
	}
	fs, _, _ := release.List(k.rel)
	var got []string
	for _, f := range fs {
		got = append(got, f.Manifest.Version)
	}
	// 0.1.0 is the one before 0.7.0, so 0.2.0 and 0.3.0 go.
	if strings.Join(got, " ") != "0.1.0 0.4.0 0.5.0 0.6.0 0.7.0" || !k.m.called("Remove 0.2.0") || k.m.called("Remove 0.1.0") {
		t.Fatalf("kept %v, calls %v", got, k.m.Calls())
	}
}

// A request that ran when the updater stopped is recorded as stopped when it starts again.
func TestARequestCutOffByARestartIsRecorded(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	block := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(block) }) }
	t.Cleanup(unblock)
	k.m.blockLoad = block
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); err != nil {
		t.Fatal(err)
	}
	again := &updater.Updater{Machine: k.m, Releases: k.rel, Dir: k.u.Dir, Log: k.u.Log}
	if err := again.Begin("0.1.0", ""); err != nil {
		t.Fatal(err)
	}
	reqs, _ := again.Requests(1)
	if reqs[0].State != updater.StateStopped {
		t.Fatalf("%+v", reqs[0])
	}
	unblock()
	k.u.Wait()
}

// Row 56: the socket answers without the server, and only root gives a build by hand.
func TestTheSocketTakesInstallStatusAndStop(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	k.release("0.3.0", 51, 0, false)
	h := k.u.Handler()
	do := func(method, path, body string, uid int, known bool) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if known {
			r = r.WithContext(updater.WithPeer(r.Context(), uid))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&out)
		return w.Code, out
	}
	if code, out := do("POST", "/install", `{"version":"0.3.0","askedBy":"crew-ctl","byHand":true}`, 1000, true); code != http.StatusUnprocessableEntity || !strings.Contains(out["error"].(string), "only by root") {
		t.Fatalf("by hand from the server's user: %d %v", code, out)
	}
	if code, out := do("POST", "/install", `{"version":"0.3.0","askedBy":"crew-ctl","byHand":true}`, 0, false); code != http.StatusUnprocessableEntity {
		t.Fatalf("by hand from an unknown caller: %d %v", code, out)
	}
	if code, out := do("POST", "/install", `{"version":"0.2.0","askedBy":"crew-ctl"}`, 0, true); code != http.StatusAccepted || out["version"] != "0.2.0" {
		t.Fatalf("install: %d %v", code, out)
	}
	k.u.Wait()
	code, out := do("GET", "/status", "", 0, true)
	if code != 200 || out["state"].(map[string]any)["running"] != "0.2.0" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := do("POST", "/stop", "", 0, true); code != http.StatusConflict {
		t.Fatalf("stop with nothing running: %d", code)
	}
	if code, _ := do("POST", "/install", `not json`, 0, true); code != http.StatusBadRequest {
		t.Fatalf("not JSON: %d", code)
	}
}

// A Stop or a Status while step 1 still checks the release finds no request that runs: step 1 is not
// published half made, and nothing panics or races.
func TestAStopDuringStepOne(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	inAsk, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unhold := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unhold)
	var asked atomic.Bool
	k.u.MachineSetup = func() (int, error) {
		// Step 1 asks first, and waits; Status asks too, and goes on.
		if asked.CompareAndSwap(false, true) {
			close(inAsk)
			<-release
		}
		return 2, nil
	}
	started := make(chan error, 1)
	go func() {
		_, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false)
		started <- err
	}()
	<-inAsk
	if err := k.u.Stop(); err == nil || !strings.Contains(err.Error(), "no request runs") {
		t.Fatalf("stop in step 1: %v", err)
	}
	if st, err := k.u.Status(5); err != nil || st.Current != nil {
		t.Fatalf("status in step 1: %+v %v", st, err)
	}
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); !errors.Is(err, updater.ErrBusy) {
		t.Fatalf("a second request in step 1: %v", err)
	}
	unhold()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	k.u.Wait()
	if k.running() != "0.2.0" {
		t.Fatal("the request did not go on")
	}
}

// A request cut off after its switch began, by a stop of the updater, starts the version that ran
// before again when no server is healthy, and leaves a healthy one alone.
func TestARequestCutOffAfterItsSwitchIsRecovered(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
		cut := updater.Request{ID: 7, Version: "0.2.0", From: "0.1.0", Action: updater.ActionInstall, AskedBy: "clock",
			State: updater.StateRunning, Step: 5, AskedAt: time.Now()}
		body, _ := json.Marshal(cut)
		if err := os.MkdirAll(filepath.Join(k.u.Dir, "requests"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(k.u.Dir, "requests", "7.json"), body, 0o640); err != nil {
			t.Fatal(err)
		}
		if !healthy {
			k.m.running = ""
		}
		again := &updater.Updater{Machine: k.m, Releases: k.rel, Dir: k.u.Dir, Log: k.u.Log, HealthFor: 30 * time.Millisecond, WatchEvery: 5 * time.Millisecond}
		if err := again.Begin("0.1.0", ""); err != nil {
			t.Fatal(err)
		}
		if err := again.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		reqs, _ := again.Requests(1)
		if reqs[0].State != updater.StateStopped || !strings.Contains(reqs[0].Error, "at its step 5") {
			t.Fatalf("%+v", reqs[0])
		}
		if k.m.called("StartServer 0.1.0") == healthy {
			t.Fatalf("healthy %v: calls %v", healthy, k.m.Calls())
		}
	}
}

// A request cut off after its switch was done goes on with its own version, whose schema is applied;
// a server that becomes healthy while the updater waits is left alone.
func TestRecoveryKnowsWhetherTheSwitchWasDone(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0", "0.2.0")
	cut := updater.Request{ID: 8, Version: "0.2.0", From: "0.1.0", Action: updater.ActionInstall, AskedBy: "clock",
		State: updater.StateRunning, Step: 6, AskedAt: time.Now(),
		Steps: []updater.Step{{Step: 5, Name: "Switch", OK: true, Detail: "switched: 0.2.0 runs"}}}
	body, _ := json.Marshal(cut)
	_ = os.MkdirAll(filepath.Join(k.u.Dir, "requests"), 0o750)
	if err := os.WriteFile(filepath.Join(k.u.Dir, "requests", "8.json"), body, 0o640); err != nil {
		t.Fatal(err)
	}
	k.m.running = ""
	// The number of releases kept, from an install before, outlives the recovery (D30).
	if err := os.WriteFile(filepath.Join(k.u.Dir, "state.json"), []byte(`{"running":"0.1.0","keep":3}`), 0o640); err != nil {
		t.Fatal(err)
	}
	again := &updater.Updater{Machine: k.m, Releases: k.rel, Dir: k.u.Dir, Log: k.u.Log, HealthFor: 30 * time.Millisecond, WatchEvery: 5 * time.Millisecond}
	if err := again.Begin("0.1.0", ""); err != nil {
		t.Fatal(err)
	}
	if err := again.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !k.m.called("StartServer 0.2.0") || k.m.called("StartServer 0.1.0") {
		t.Fatalf("%v", k.m.Calls())
	}
	if st, _ := again.State(); st.Running != "0.2.0" || st.Previous != "0.1.0" || st.Keep != 3 {
		t.Fatalf("%+v", st)
	}
}

// The updater's lock is never a link (16.11, security review of 0.53): a link at its place, as the
// user crew could put into a folder it writes, is refused, and nothing is locked through it.
func TestTheUpdatersLockIsNoLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "updater.lock")); err != nil {
		t.Fatal(err)
	}
	if f, err := updater.Exclusive(dir); err == nil {
		f.Close()
		t.Fatal("a lock through a link was taken")
	}
}
