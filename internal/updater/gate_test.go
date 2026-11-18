package updater_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

// realTicket is a live part's step that passed.
var realTicket = []release.GateStep{{Name: "the real ticket on crew-gate", OK: true}}

// gateAsk is a gate's request for the release folder f, with the caller's make check.
func gateAsk(f *release.Folder, makeCheck bool) updater.Ask {
	return updater.Ask{Version: f.Manifest.Version, AskedBy: "server", Action: updater.ActionGate, Gate: &updater.GateAsk{
		ManifestSHA256: f.ManifestSHA256, InstallTest: "skipped", InstallTestNote: "updates.install_test is off",
		Steps: []release.GateStep{{Name: "make check in a check sandbox", OK: makeCheck}}}}
}

func readFolder(t *testing.T, k *kit, v string) *release.Folder {
	t.Helper()
	f, err := release.Read(filepath.Join(k.rel, v))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A gate runs the release beside the running one in check mode, the smoke microVM, the selftest, and
// the browser tests, removes the check server, and writes gate.json with the caller's steps and its
// own, the live part pending: pending is no gate, and Install refuses it (16.2 step 5, since 0.52).
func TestAGateWritesItsStepsWithTheLivePartPending(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	f := k.release("0.2.0", 51, 0, false)
	req, err := k.ask(gateAsk(f, true), false)
	if err != nil || req.State != updater.StateDone || req.Action != updater.ActionGate {
		t.Fatalf("%+v %v", req, err)
	}
	calls := strings.Join(k.m.Calls(), "|")
	for _, want := range []string{"Load 0.2.0", "StartCheck 0.2.0", "Health crew-server-check", "Smoke 0.2.0", "Selftest", "Browser 0.2.0", "StopCheck", "DropScratch"} {
		if !strings.Contains(calls, want) {
			t.Errorf("the gate did not run %q: %s", want, calls)
		}
	}
	if k.m.called("StopServer") || k.m.called("Migrate") || k.m.called("Proof") {
		t.Fatalf("a gate touched the running server: %s", calls)
	}
	g := readFolder(t, k, "0.2.0")
	if g.Gate != nil || g.Pending == nil || g.Pending.Passed || g.Pending.Live != release.LivePending ||
		g.Pending.Steps[0].Name != "make check in a check sandbox" || len(g.Pending.Steps) < 5 || g.Pending.InstallTest != "skipped" {
		t.Fatalf("gate %+v pending %+v", g.Gate, g.Pending)
	}
	r, err := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false)
	if err == nil || !strings.Contains(r.Error, "gate of 0.2.0 is pending") {
		t.Fatalf("an install of a pending gate: %+v %v", r, err)
	}
	if _, err := k.u.Start(context.Background(), gateAsk(f, true), false); err == nil || !strings.Contains(err.Error(), "waits for its live steps") {
		t.Fatalf("a second gate of a pending one: %v", err)
	}
}

// A step the caller failed, or one of the updater's, fails the gate: gate.json says so, and a failed
// make check starts nothing of the release.
func TestAFailedStepFailsTheGate(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	f := k.release("0.2.0", 51, 0, false)
	req, _ := k.ask(gateAsk(f, false), false)
	g := readFolder(t, k, "0.2.0")
	if req.State != updater.StateFailed || k.m.called("Load") || k.m.called("StartCheck") || g.Gate != nil || g.Pending != nil {
		t.Fatalf("%+v %v %+v", req, k.m.Calls(), g)
	}
	k.m.fail["Browser"] = errors.New("screens.spec.ts: 1 failed")
	req, _ = k.ask(gateAsk(f, true), false)
	g = readFolder(t, k, "0.2.0")
	if req.State != updater.StateFailed || !strings.Contains(req.Error, "browser tests") || !k.m.called("DropScratch") || g.Gate != nil || g.Pending != nil {
		t.Fatalf("%+v %v %+v", req, k.m.Calls(), g)
	}
	for _, bad := range []func(a *updater.Ask){
		func(a *updater.Ask) { a.Gate.ManifestSHA256 = strings.Repeat("0", 64) },
		func(a *updater.Ask) { a.Gate = nil },
		func(a *updater.Ask) { a.Gate.InstallTest = "maybe" },
		func(a *updater.Ask) { a.Gate.Steps[0].Detail = strings.Repeat("x", 4000) },
	} {
		a := gateAsk(f, true)
		bad(&a)
		if _, err := k.u.Start(context.Background(), a, false); err == nil {
			t.Errorf("a gate's request was taken: %+v", a.Gate)
		}
	}
	passed := k.release("0.3.0", 51, 0, true)
	if _, err := k.u.Start(context.Background(), gateAsk(passed, true), false); err == nil || !strings.Contains(err.Error(), "passed its gate already") {
		t.Fatalf("a gate of a release that passed: %v", err)
	}
}

// The server's live result is written into the pending gate, bound to the same release.json: passed
// makes the gate, failed makes none, skipped names what did not run, which keeps the release from
// installing by itself; a result for another release.json, or for a gate that does not wait, is
// refused (16.2 step 5).
func TestTheLiveResultCompletesAPendingGate(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	for _, tc := range []struct {
		v       string
		live    string
		skipped []string
		passed  bool
		ask     string
	}{
		{"0.2.0", release.LivePassed, nil, true, ""},
		{"0.2.1", release.LiveFailed, nil, false, ""},
		{"0.2.2", release.LiveSkipped, []string{"the eval suites: no project could be compared"}, true,
			"; its gate passed, and a step did not run: the eval suites: no project could be compared"},
		{"0.2.4", release.LiveSkipped, []string{"the real ticket: agy is signed out", "the eval suites: agy is signed out"}, true,
			"; its gate passed, and 2 steps did not run: the real ticket: agy is signed out; the eval suites: agy is signed out"},
	} {
		v := tc.v
		f := k.release(v, 51, 0, false)
		if _, err := k.u.Live(updater.LiveAsk{Version: v, ManifestSHA256: f.ManifestSHA256, Live: release.LivePassed, Steps: realTicket}); !errors.Is(err, updater.ErrNoPendingGate) {
			t.Fatalf("a live result with no gate: %v", err)
		}
		if req, err := k.ask(gateAsk(f, true), false); err != nil || req.State != updater.StateDone {
			t.Fatalf("%+v %v", req, err)
		}
		if _, err := k.u.Live(updater.LiveAsk{Version: v, ManifestSHA256: strings.Repeat("0", 64), Live: release.LivePassed, Steps: realTicket}); err == nil {
			t.Fatal("a live result for another release.json was taken")
		}
		step := release.GateStep{Name: "the real ticket on crew-gate", OK: tc.live != release.LiveFailed}
		g, err := k.u.Live(updater.LiveAsk{Version: v, ManifestSHA256: f.ManifestSHA256, Live: tc.live, Steps: []release.GateStep{step},
			Skipped: tc.skipped})
		if err != nil {
			t.Fatal(err)
		}
		got := readFolder(t, k, v)
		if (got.Gate != nil) != tc.passed || got.Pending != nil || g.Live != tc.live {
			t.Fatalf("%s: gate %+v pending %+v", tc.live, got.Gate, got.Pending)
		}
		if tc.live == release.LiveSkipped {
			if got.Gate.InstallsBySelfAfterGate() {
				t.Fatal("a release whose live step was skipped installs by itself")
			}
			// Install says the gate passed and which step did not run, not that the gate did not run.
			req, err := k.ask(updater.Ask{Version: v, AskedBy: "operator"}, false)
			if err != nil || !strings.HasSuffix(req.Steps[0].Detail, tc.ask) {
				t.Fatalf("%s: %+v %v", v, req, err)
			}
		}
		if _, err := k.u.Live(updater.LiveAsk{Version: v, ManifestSHA256: f.ManifestSHA256, Live: release.LivePassed, Steps: realTicket}); !errors.Is(err, updater.ErrNoPendingGate) {
			t.Fatalf("a second live result: %v", err)
		}
	}
	f := k.release("0.2.3", 51, 0, false)
	k.ask(gateAsk(f, true), false)
	for _, bad := range []updater.LiveAsk{
		{Live: release.LiveSkipped},
		{Live: "fine"},
		{Live: release.LivePassed, Steps: []release.GateStep{{Name: "the real ticket", OK: false}}},
		{Live: release.LivePassed},
	} {
		bad.Version, bad.ManifestSHA256 = "0.2.3", f.ManifestSHA256
		if _, err := k.u.Live(bad); err == nil {
			t.Errorf("a live result was taken: %+v", bad)
		}
	}
}

// The socket takes a gate and its live result, as the server sends them.
func TestTheSocketTakesAGateAndItsLiveResult(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	f := k.release("0.2.0", 51, 0, false)
	srv := httptest.NewServer(k.u.Handler())
	defer srv.Close()
	c := &updater.Client{HTTP: srv.Client(), Base: srv.URL}
	req, err := c.Gate(context.Background(), gateAsk(f, true))
	if err != nil || req.Action != updater.ActionGate {
		t.Fatalf("%+v %v", req, err)
	}
	k.u.Wait()
	g, err := c.Live(context.Background(), updater.LiveAsk{Version: "0.2.0", ManifestSHA256: f.ManifestSHA256, Live: release.LivePassed, Steps: realTicket})
	if err != nil || !g.Passed || g.Live != release.LivePassed {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := c.Live(context.Background(), updater.LiveAsk{Version: "0.2.0", ManifestSHA256: f.ManifestSHA256, Live: release.LivePassed, Steps: realTicket}); !errors.Is(err, updater.ErrNoPendingGate) {
		t.Fatalf("a second result: %v", err)
	}
	body, _ := json.Marshal(updater.Ask{Version: "0.2.0", AskedBy: "x", Action: updater.ActionInstall})
	resp, err := http.Post(srv.URL+"/gate", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /gate with no steps: %v %v", resp, err)
	}
}

// Every text a gate writes is one plain line: no control, format, or direction-changing character and
// no separator but the space, whether the caller sent it or the live result did, since crew-ctl prints
// it to root's terminal and the screens show it (security review of M16 P4a).
func TestAGatesTextsArePlain(t *testing.T) {
	k := newKit(t, "0.1.0", "0.1.0")
	f := k.release("0.2.0", 51, 0, false)
	dirty := "a\x1b[2J\r\nb\x00c‮d e​f\tg"
	a := gateAsk(f, true)
	a.Gate.Steps = append(a.Gate.Steps, release.GateStep{Name: "step " + dirty, OK: true, Detail: "detail " + dirty})
	a.Gate.Skipped = []string{"note " + dirty}
	if req, err := k.ask(a, false); err != nil || req.State != updater.StateDone {
		t.Fatalf("%+v %v", req, err)
	}
	g, err := k.u.Live(updater.LiveAsk{Version: "0.2.0", ManifestSHA256: f.ManifestSHA256, Live: release.LiveSkipped,
		Steps: []release.GateStep{{Name: "live " + dirty, OK: false, Detail: dirty}}, Skipped: []string{"skipped " + dirty}, Hold: "hold " + dirty})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, s := range append(append([]release.GateStep(nil), g.Steps...), g.LiveSteps...) {
		texts = append(texts, s.Name, s.Detail)
	}
	texts = append(append(texts, g.Skipped...), g.Hold)
	for _, x := range texts {
		for _, r := range x {
			if r != ' ' && (unicode.Is(unicode.C, r) || unicode.Is(unicode.Z, r)) {
				t.Fatalf("%q holds %U", x, r)
			}
		}
	}
	if g.Hold != "hold a[2J bcdef g" {
		t.Fatalf("the hold: %q", g.Hold)
	}
}

// An error of a command the updater ran names its arguments but not the value of an -e NAME=VALUE:
// the install's own values, such as its admin email, are no part of a gate's report.
func TestAnErrorKeepsNoValueOfTheEnvironment(t *testing.T) {
	_, err := updater.ExecRun(context.Background(), nil, nil, "false", "-e", "CREW_ADMIN_EMAIL=admin@example.org")
	if err == nil || strings.Contains(err.Error(), "admin@example.org") || !strings.Contains(err.Error(), "-e CREW_ADMIN_EMAIL=…") {
		t.Fatalf("%v", err)
	}
}

// The clock installs no release whose gate skipped a step or holds it, whatever the server decided;
// the operator's Install does (16.2 step 5, 16.3).
func TestTheClockInstallsNoReleaseWhoseGateSkippedOrHolds(t *testing.T) {
	for name, g := range map[string]release.Gate{
		"skipped": {Passed: true, Live: release.LiveSkipped, InstallTest: "skipped", Skipped: []string{"the real ticket: no agent CLI is signed in"}},
		"held":    {Passed: true, Live: release.LivePassed, InstallTest: "skipped", Hold: "shop's suite measured worse"},
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t, "0.1.0", "0.1.0")
			f := k.release("0.2.0", 51, 0, false)
			if err := f.WriteGate(g); err != nil {
				t.Fatal(err)
			}
			if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: updater.ByClock}, false); err == nil ||
				!strings.Contains(err.Error(), "does not install by itself") {
				t.Fatalf("the clock: %v", err)
			}
			if req, err := k.ask(updater.Ask{Version: "0.2.0", AskedBy: "operator"}, false); err != nil || req.State != updater.StateDone {
				t.Fatalf("the operator's Install: %+v %v", req, err)
			}
		})
	}
}
