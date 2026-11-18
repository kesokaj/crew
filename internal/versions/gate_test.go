package versions_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// fakeLive plays a gate's live steps: it records each round's start, and answers result once it is set.
type fakeLive struct {
	starts    []versions.LiveStart
	result    *updater.LiveAsk
	abandoned []string
	// abandonErr fails Abandon; stepErr fails Step for the versions it names.
	abandonErr error
	stepErr    map[string]error
}

func (f *fakeLive) Abandon(_ context.Context, version, _, why string) error {
	if f.abandonErr != nil {
		return f.abandonErr
	}
	f.abandoned = append(f.abandoned, version+": "+why)
	return nil
}

func (f *fakeLive) Step(_ context.Context, l versions.LiveStart) (*updater.LiveAsk, error) {
	f.starts = append(f.starts, l)
	if err := f.stepErr[l.Version]; err != nil {
		return nil, err
	}
	if f.result == nil {
		return nil, nil
	}
	r := *f.result
	return &r, nil
}

type gateKit struct {
	*buildKit
	live *fakeLive
}

func newGateKit(t *testing.T) *gateKit {
	return newGateKitOf(t, crewTree())
}

func newGateKitOf(t *testing.T, tree map[string]string) *gateKit {
	k := &gateKit{buildKit: newBuildKitOf(t, tree), live: &fakeLive{}}
	k.b.Gates, k.b.Live = k.s.Updater.(versions.GateUpdater), k.live
	return k
}

// round is one round of the gates, after the updater's request and the make check that run ended.
func (k *gateKit) round(installTest string) {
	k.t.Helper()
	k.u.Wait()
	k.b.Wait()
	st, err := k.s.Updater.Status(context.Background())
	if err != nil {
		k.t.Fatal(err)
	}
	if err := k.b.GateRound(context.Background(), st, "crew", installTest); err != nil {
		k.t.Fatal(err)
	}
	k.b.Wait()
	k.u.Wait()
}

func (k *gateKit) gateRow(v string) *versions.GateRow {
	k.t.Helper()
	g, err := k.b.GetGate(context.Background(), v)
	if err != nil || g == nil {
		k.t.Fatalf("%v %v", g, err)
	}
	return g
}

// sameRuntime makes the running release's runtime inputs the candidate's: only text differs.
func (k *gateKit) sameRuntime(v string) {
	k.t.Helper()
	f, _ := release.Read(filepath.Join(k.rel, v))
	run, _ := release.Read(filepath.Join(k.rel, "0.1.0"))
	if _, err := release.Write(run.Dir, release.Facts{Version: "0.1.0", BuiltAt: time.Now(), Images: run.Manifest.Images, Deploy: release.DeployFile,
		Schema: 51, Runtime: f.Manifest.Runtime}, run.Manifest.Files, nil); err != nil {
		k.t.Fatal(err)
	}
	k.gate("0.1.0")
}

// A release Crew built is gated by the server: make check in a sandbox of its own on the exported
// tree, the updater's steps, which leave the gate pending, and, since its runtime image's inputs are
// the running release's, the live steps at once in the candidate's runtime image; their result goes to
// the updater as one request, and the release passed. Where the install test is set up, a gate Crew
// drives skips it, and the release does not install by itself (16.2 step 5, 16.3).
func TestAReleaseIsGatedAndItsLiveStepsRunAtOnceWhenOnlyTextDiffers(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	k.round("local")
	if len(k.h.MakeChecks) != 1 || len(k.h.MakeChecks[0].Tree) == 0 || k.gateRow("0.2.0").State != versions.GateAsking {
		t.Fatalf("make check: %d, gate %+v", len(k.h.MakeChecks), k.gateRow("0.2.0"))
	}
	k.round("local")
	// The updater's request may end within the round that asked it.
	if g := k.gateRow("0.2.0"); g.State != versions.GateAsked && g.State != versions.GateLive {
		t.Fatalf("%+v", g)
	}
	k.round("local")
	g := k.gateRow("0.2.0")
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	if g.State != versions.GateLive || g.SameRuntime == nil || !*g.SameRuntime || f.Pending == nil || f.Gate != nil {
		t.Fatalf("gate %+v, folder pending %v", g, f.Pending)
	}
	if len(k.live.starts) == 0 || len(k.live.starts) > 2 {
		t.Fatalf("the live steps: %+v", k.live.starts)
	}
	for _, s := range k.live.starts {
		if s.Image != f.Manifest.Images["crew-runtime"].ID {
			t.Fatalf("the live steps: %+v", k.live.starts)
		}
	}
	// The install test, which a gate Crew drives does not run, is said, and holds nothing.
	if f.Pending.Steps[0].Name != "make check at "+k.head[:12]+", in a sandbox of its own" || !f.Pending.Steps[0].OK ||
		len(f.Pending.Skipped) != 0 || !strings.Contains(f.Pending.InstallTestNote, "crew-ctl gate") {
		t.Fatalf("the pending gate %+v", f.Pending)
	}
	k.live.result = &updater.LiveAsk{Live: release.LiveSkipped, Steps: []release.GateStep{{Name: "the real ticket on crew-gate", OK: false}},
		Skipped: []string{"the real ticket on crew-gate: no agent CLI is signed in"}}
	k.round("local")
	f, _ = release.Read(filepath.Join(k.rel, "0.2.0"))
	if g := k.gateRow("0.2.0"); g.State != versions.GateDone || f.Gate == nil || f.Gate.InstallsBySelfAfterGate() {
		t.Fatalf("gate %+v, folder %+v", g, f.Gate)
	}
	// The release has a gate now, which skipped a step: the server gives notice, and the clock does not
	// install it.
	k.pass()
	var after *time.Time
	if err := k.d.Server.QueryRow(context.Background(), `SELECT install_after FROM releases WHERE version = '0.2.0'`).Scan(&after); err != nil || after != nil {
		t.Fatalf("a release whose gate skipped a step installs by itself at %v (%v)", after, err)
	}
	if _, err := k.u.Start(context.Background(), updater.Ask{Version: "0.2.0", AskedBy: versions.ByClock}, false); err == nil ||
		!strings.Contains(err.Error(), "does not install by itself") {
		t.Fatalf("the clock's install of a release whose gate skipped a step: %v", err)
	}
}

// A release whose runtime image's inputs differ from the running release's waits for Run the live
// gate: no live step runs, so no run gets the login in its image, until the operator's press, and the
// gate's diff names what differs (16.2 step 5).
func TestDifferentRuntimeInputsWaitForThePress(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	for range 3 {
		k.round("off")
	}
	g := k.gateRow("0.2.0")
	if g.State != versions.GateWaiting || g.SameRuntime == nil || *g.SameRuntime || len(g.Diff) == 0 || len(k.live.starts) != 0 {
		t.Fatalf("gate %+v, live %+v", g, k.live.starts)
	}
	k.round("off")
	if len(k.live.starts) != 0 {
		t.Fatal("the live steps ran before the press")
	}
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	if err := k.b.RunLiveGate(context.Background(), "0.2.0", strings.Repeat("0", 64), "operator"); !errors.Is(err, versions.ErrStaleDiff) {
		t.Fatalf("a press on another diff: %v", err)
	}
	if err := k.b.RunLiveGate(context.Background(), "0.2.0", f.Manifest.RuntimeSHA256, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := k.b.RunLiveGate(context.Background(), "0.2.0", f.Manifest.RuntimeSHA256, "operator"); !errors.Is(err, versions.ErrNotWaitingGate) {
		t.Fatalf("a second press: %v", err)
	}
	k.round("off")
	// The live steps learn that the candidate's runtime image is not the running release's code (security
	// review of M16 P4d).
	if g := k.gateRow("0.2.0"); g.State != versions.GateLive || g.PressedBy != "operator" || len(k.live.starts) != 1 || !k.live.starts[0].Agents ||
		k.live.starts[0].SameRuntime {
		t.Fatalf("gate %+v, live %+v", g, k.live.starts)
	}
	f, _ = release.Read(filepath.Join(k.rel, "0.2.0"))
	if len(f.Pending.Skipped) != 0 || f.Pending.InstallTestNote != "updates.install_test is off" {
		t.Fatalf("with the install test off: %+v", f.Pending)
	}
}

// A runtime input whose path has a control character is never the same runtime, so its gate waits for
// the press even when the running release has the same file: the press is offered on a diff that names
// it, and not refused as one the running release cannot give (review of M16, F1). Without the running
// release, the diff is empty and says why.
func TestARuntimePathWithAControlCharacterIsOfferedThePress(t *testing.T) {
	tree := crewTree()
	tree["image/a\tb"] = "x\n"
	k := newGateKitOf(t, tree)
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	for range 3 {
		k.round("off")
	}
	g := k.gateRow("0.2.0")
	if g.State != versions.GateWaiting || len(g.Diff) != 1 || g.Diff[0] != "image/a\tb" || g.NoDiff != "" || len(k.live.starts) != 0 {
		t.Fatalf("gate %+v, live %+v", g, k.live.starts)
	}
	if err := os.Rename(filepath.Join(k.rel, "0.1.0"), filepath.Join(k.rel, "0.1.0.away")); err != nil {
		t.Fatal(err)
	}
	if g := k.gateRow("0.2.0"); len(g.Diff) != 0 || g.NoDiff != versions.NoDiffUnread {
		t.Fatalf("without the running release: %+v", g)
	}
}

// A running release whose recorded hash of its runtime inputs is not theirs is not the same runtime,
// with no path that differs: the gate says why no press is offered (review of M16, F1).
func TestAGateWithNoDiffSaysWhy(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	path := filepath.Join(k.rel, "0.1.0", release.ManifestFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := release.Read(filepath.Join(k.rel, "0.1.0"))
	body = []byte(strings.Replace(string(body), run.Manifest.RuntimeSHA256, strings.Repeat("0", 64), 1))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		k.round("off")
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting || len(g.Diff) != 0 || g.NoDiff != release.NoDiffBadHash || len(k.live.starts) != 0 {
		t.Fatalf("gate %+v, live %+v", g, k.live.starts)
	}
}

// A gate whose row failed while the updater's gate.json still waits for its live steps is ended: the
// updater writes its live part failed with the row's reason, so the release is not stuck pending and
// can be gated again; a gate asked again after that is taken up, not ended (review of M16, F2).
func TestAFailedGateRowEndsItsPendingGateAndTheReleaseCanBeGatedAgain(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	for range 3 {
		k.round("off")
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting {
		t.Fatalf("%+v", g)
	}
	// As a round that could not read the folder leaves it.
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE gates SET state = 'failed', error = 'the release folder cannot be read: EIO', updated_at = now()
		WHERE version = '0.2.0'`); err != nil {
		t.Fatal(err)
	}
	k.round("off")
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	gate, err := release.ReadGate(f.Dir)
	if err != nil || f.Pending != nil || gate == nil || gate.Passed || gate.Live != release.LiveFailed || len(gate.LiveSteps) != 1 ||
		!strings.Contains(gate.LiveSteps[0].Detail, "the release folder cannot be read: EIO") || len(k.live.starts) != 0 {
		t.Fatalf("pending %+v, gate %+v, %v, live %+v", f.Pending, gate, err, k.live.starts)
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateFailed {
		t.Fatalf("%+v", g)
	}
	// crew-ctl gate asks for it again, and the updater's steps leave it pending.
	if err := f.WriteGate(release.Gate{At: time.Now().Add(time.Minute), InstallTest: "skipped", Live: release.LivePending,
		Steps: []release.GateStep{{Name: "make check", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	k.round("off")
	f, _ = release.Read(filepath.Join(k.rel, "0.2.0"))
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting || g.Error != "" || f.Pending == nil {
		t.Fatalf("gated again: %+v, pending %+v", g, f.Pending)
	}
}

// gatesWith is the updater as the gate reaches it, whose Live answers liveErr once it is set.
type gatesWith struct {
	versions.GateUpdater
	liveErr error
}

func (g *gatesWith) Live(ctx context.Context, a updater.LiveAsk) (*release.Gate, error) {
	if g.liveErr != nil {
		return nil, g.liveErr
	}
	return g.GateUpdater.Live(ctx, a)
}

// failPending makes a waiting gate's row fail while its gate.json pends, with why.
func (k *gateKit) failPending(v, why string) {
	k.t.Helper()
	if _, err := k.d.Server.Exec(context.Background(), `UPDATE gates SET state = 'failed', error = $2, updated_at = now() WHERE version = $1`, v, why); err != nil {
		k.t.Fatal(err)
	}
}

// Ending a failed gate's pending gate.json is told in the log when the updater refuses it or cannot be
// reached, and holds back no other release's adoption; the next round asks again (round 2 of the
// reviews of M16's P8). A reason longer than a step's detail may be is cut, so the updater takes it.
func TestEndingAFailedGateHoldsNoOtherBack(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	for range 3 {
		k.round("off")
	}
	k.failPending("0.2.0", "the release folder cannot be read")
	// A gate crew-ctl gate asked for, after it, of another release.
	k.release("0.3.0", nil, false)
	f3, _ := release.Read(filepath.Join(k.rel, "0.3.0"))
	if err := f3.WriteGate(release.Gate{At: time.Now(), InstallTest: "skipped", Live: release.LivePending,
		Steps: []release.GateStep{{Name: "make check", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	g := &gatesWith{GateUpdater: k.b.Gates}
	k.b.Gates = g
	for _, e := range []error{errors.New("the updater's socket does not answer"), &updater.Refusal{Reason: "no"}, updater.ErrNoPendingGate} {
		g.liveErr = e
		k.u.Wait()
		st, err := k.s.Updater.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = k.b.GateRound(context.Background(), st, "crew", "off")
		f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
		if row := k.gateRow("0.2.0"); f.Pending == nil || row.State != versions.GateFailed {
			t.Fatalf("%v: pending %+v, row %+v", e, f.Pending, row)
		}
		if row, err := k.b.GetGate(context.Background(), "0.3.0"); err != nil || row == nil || row.State != versions.GateWaiting {
			t.Fatalf("%v: the other release's gate is not taken up: %+v %v", e, row, err)
		}
	}
	// A reason of 3000 bytes, with a rune of two bytes across the cut, is cut and taken.
	g.liveErr = nil
	k.failPending("0.2.0", strings.Repeat("a", 1499)+strings.Repeat("é", 800))
	k.round("off")
	gate, err := release.ReadGate(filepath.Join(k.rel, "0.2.0"))
	if err != nil || gate == nil || gate.Live != release.LiveFailed || len(gate.LiveSteps) != 1 || len(gate.LiveSteps[0].Detail) > 1500 ||
		!utf8.ValidString(gate.LiveSteps[0].Detail) || !strings.HasPrefix(gate.LiveSteps[0].Detail, strings.Repeat("a", 1499)) {
		t.Fatalf("%+v %v", gate, err)
	}
}

// A candidate whose recorded hash of its runtime inputs is not theirs is not pressed: the press binds
// to that hash, which would not be the hash of the inputs it showed (security review of ef7a82a..4a601e2).
func TestACandidateWhoseRuntimeHashIsNotItsInputsIsNotPressed(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	path := filepath.Join(k.rel, "0.2.0", release.ManifestFile)
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Repeat("0", 64)
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), f.Manifest.RuntimeSHA256, bad, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		k.round("off")
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting || len(g.Diff) != 0 || g.NoDiff != release.NoDiffBadHash {
		t.Fatalf("%+v", g)
	}
	if err := k.b.RunLiveGate(context.Background(), "0.2.0", bad, "operator"); !errors.Is(err, versions.ErrBadRuntimeHash) {
		t.Fatalf("pressed: %v", err)
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting {
		t.Fatalf("%+v", g)
	}
}

// A make check that fails fails the gate: the updater writes the failed gate.json, and nothing of the
// release is started, live or not.
func TestAFailedMakeCheckFailsTheGate(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.h.MakeCheckErr = errors.New("the build ended with 2")
	for range 3 {
		k.round("off")
	}
	g := k.gateRow("0.2.0")
	f, _ := release.Read(filepath.Join(k.rel, "0.2.0"))
	if g.State != versions.GateFailed || !strings.Contains(g.Error, "the updater's gate failed") || f.Gate != nil || f.Pending != nil ||
		len(k.live.starts) != 0 || len(k.m.started) != 0 {
		t.Fatalf("gate %+v, folder %+v %+v, live %+v", g, f.Gate, f.Pending, k.live.starts)
	}
}

// A pending gate crew-ctl gate asked for on the machine is taken up at its live part; a make check a
// stop of the server cut off runs again.
func TestAPendingGateFromTheMachineIsTakenUp(t *testing.T) {
	k := newGateKit(t)
	k.release("0.3.0", nil, false)
	f, _ := release.Read(filepath.Join(k.rel, "0.3.0"))
	if err := f.WriteGate(release.Gate{At: k.now, InstallTest: "skipped", Live: release.LivePending,
		Steps: []release.GateStep{{Name: "make check", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	k.round("off")
	if g := k.gateRow("0.3.0"); g.State != versions.GateWaiting {
		t.Fatalf("%+v", g)
	}
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO gates (version, state) VALUES ('0.4.0', 'checking')`); err != nil {
		t.Fatal(err)
	}
	if err := k.b.RecoverGates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g, _ := k.b.GetGate(context.Background(), "0.4.0"); g != nil {
		t.Fatalf("a cut-off make check is left: %+v", g)
	}
}

// A release of text whose gate skipped a step gets its notice and no clock, and one whose suite held it
// waits on hold with the reason: neither installs by itself, whatever its kind (16.2 step 5, 16.3).
func TestAGateThatSkippedOrHoldsGivesNoClock(t *testing.T) {
	k := newKit(t, "0.1.0")
	for v, g := range map[string]release.Gate{
		"0.2.0": {Passed: true, At: k.now, InstallTest: "skipped", Live: release.LiveSkipped, Skipped: []string{"the real ticket: agy is signed out"}},
		"0.2.1": {Passed: true, At: k.now, InstallTest: "skipped", Live: release.LivePassed, Hold: "shop's suite measured worse: 2 of 5 cases, after 4 of 5"},
	} {
		k.release(v, map[string]string{"baseline/stages/build.md": "new " + v}, false)
		f, _ := release.Read(filepath.Join(k.rel, v))
		if err := f.WriteGate(g); err != nil {
			t.Fatal(err)
		}
	}
	k.pass()
	// The newer one replaces the older as the next release, so each is read on its own notice.
	r := k.get("0.2.1")
	if r.Kind != release.KindText || r.InstallAfter != nil || !r.Held || r.HeldWhy == nil || !strings.Contains(*r.HeldWhy, "measured worse") {
		t.Fatalf("a held release: %+v", r)
	}
	k2 := newKit(t, "0.1.0")
	k2.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, false)
	f, _ := release.Read(filepath.Join(k2.rel, "0.2.0"))
	if err := f.WriteGate(release.Gate{Passed: true, At: k2.now, InstallTest: "skipped", Live: release.LiveSkipped, Skipped: []string{"the real ticket: agy is signed out"}}); err != nil {
		t.Fatal(err)
	}
	k2.pass()
	if r := k2.get("0.2.0"); r.Kind != release.KindText || r.State != "waiting" || r.InstallAfter != nil || r.Held {
		t.Fatalf("a release whose gate skipped a step: %+v", r)
	}
}

// The row follows the folder where an answer of the updater's was lost: a gate asked for whose
// gate.json waits already is decided, a live one whose gate passed is done, and an asked one whose
// request left the updater's status without a gate fails after two hours, so the next build is not
// held behind it (review of M16 P4b).
func TestTheGatesRowFollowsItsFolder(t *testing.T) {
	k := newGateKit(t)
	ctx := context.Background()
	k.release("0.3.0", nil, false)
	f, _ := release.Read(filepath.Join(k.rel, "0.3.0"))
	f.WriteGate(release.Gate{At: k.now, InstallTest: "skipped", Live: release.LivePending, Steps: []release.GateStep{{Name: "make check", OK: true}}})
	k.release("0.4.0", nil, true)
	for _, q := range []string{
		`INSERT INTO gates (version, state, steps) VALUES ('0.3.0', 'asking', '[{"name": "make check", "ok": true}]')`,
		`INSERT INTO gates (version, state, same_runtime) VALUES ('0.4.0', 'live', true)`,
		`INSERT INTO gates (version, state, request, updated_at) VALUES ('0.5.0', 'asked', 999, now() - interval '3 hours')`,
		`INSERT INTO gates (version, state, request, updated_at) VALUES ('0.6.0', 'asked', 998, now())`,
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"0.5.0", "0.6.0"} {
		k.release(v, nil, false)
	}
	k.round("off")
	for v, want := range map[string]string{"0.3.0": versions.GateWaiting, "0.4.0": versions.GateDone, "0.5.0": versions.GateFailed, "0.6.0": versions.GateAsked} {
		if g := k.gateRow(v); g.State != want {
			t.Errorf("%s is %s, not %s: %+v", v, g.State, want, g)
		}
	}
}

// A build whose folder was gated another way, by crew-ctl gate, gets a row that is done, and the next
// build is gated (review of M16 P4b).
func TestABuildGatedBeforeHoldsNoOther(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.gate("0.2.0")
	k.build("0.3.0")
	k.round("off")
	if g := k.gateRow("0.2.0"); g.State != versions.GateDone {
		t.Fatalf("%+v", g)
	}
	k.round("off")
	if g := k.gateRow("0.3.0"); g.State != versions.GateAsking || len(k.h.MakeChecks) != 1 {
		t.Fatalf("%+v %d", g, len(k.h.MakeChecks))
	}
}

// A gate that runs at once because its runtime image's inputs were the running release's is decided
// again against the release that runs now: once they differ it waits for the press, its ticket, if one
// started, ended (security review of M16 P4b, P4c).
func TestALiveGateIsDecidedAgainstTheReleaseThatRuns(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	for range 3 {
		k.round("off")
	}
	if g := k.gateRow("0.2.0"); g.State != versions.GateLive {
		t.Fatalf("%+v", g)
	}
	// The running release's inputs change, as when another release was installed meanwhile.
	run, _ := release.Read(filepath.Join(k.rel, "0.1.0"))
	if _, err := release.Write(run.Dir, release.Facts{Version: "0.1.0", BuiltAt: time.Now(), Images: run.Manifest.Images, Deploy: release.DeployFile,
		Schema: 51, Runtime: map[string]string{"versions.lock": strings.Repeat("9", 64)}}, run.Manifest.Files, nil); err != nil {
		t.Fatal(err)
	}
	k.gate("0.1.0")
	n := len(k.live.starts)
	k.round("off")
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting || len(k.live.starts) != n {
		t.Fatalf("%+v, live %d after %d", g, len(k.live.starts), n)
	}
}

// What agents see is a file under baseline/, harness/, image/, internal/cli/, or cmd/crew-run/,
// changed, added, or removed; the eval suites run for such a release only (16.2 step 5).
func TestWhatAgentsSee(t *testing.T) {
	run := release.Manifest{Files: map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "b", "web/x.ts": "c", "image/Dockerfile": "d"}}
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
	}{
		"server code only":     {map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "B", "web/x.ts": "C", "image/Dockerfile": "d"}, false},
		"a prompt":             {map[string]string{"baseline/stages/build.md": "A", "internal/harness/x.go": "b", "web/x.ts": "c", "image/Dockerfile": "d"}, true},
		"an image file gone":   {map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "b", "web/x.ts": "c"}, true},
		"a CLI adapter added":  {map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "b", "web/x.ts": "c", "image/Dockerfile": "d", "internal/cli/agy.go": "e"}, true},
		"crew-run":             {map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "b", "web/x.ts": "c", "image/Dockerfile": "d", "cmd/crew-run/main.go": "f"}, true},
		"a skill of a harness": {map[string]string{"baseline/stages/build.md": "a", "internal/harness/x.go": "b", "web/x.ts": "c", "image/Dockerfile": "d", "harness/skills/x/SKILL.md": "g"}, true},
	} {
		if got := versions.ChangesWhatAgentsSee(run, release.Manifest{Files: tc.files}); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// One real ticket runs at a time, since crew-gate names one image for its runs: a gate whose ticket has
// not started waits while another's runs. A live gate that leaves its live part another way, its
// folder no longer pending or its runtime inputs no longer the running release's, has its ticket
// ended; and the candidate's image is taken from crew-gate once no gate runs its live part (review of
// M16 P4c).
func TestLiveGatesRunOneTicketAtATime(t *testing.T) {
	k := newGateKit(t)
	ctx := context.Background()
	for _, v := range []string{"0.3.0", "0.4.0"} {
		k.release(v, nil, false)
		f, _ := release.Read(filepath.Join(k.rel, v))
		f.WriteGate(release.Gate{At: k.now, InstallTest: "skipped", Live: release.LivePending, Steps: []release.GateStep{{Name: "make check", OK: true}}})
	}
	for _, q := range []string{
		`INSERT INTO gates (version, state, pressed_at, ticket) VALUES ('0.3.0', 'live', now(), 'GATE-1')`,
		`INSERT INTO gates (version, state, pressed_at) VALUES ('0.4.0', 'live', now() - interval '1 hour')`,
	} {
		if _, err := k.d.Server.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	k.round("off")
	if len(k.live.starts) != 1 || k.live.starts[0].Version != "0.3.0" {
		t.Fatalf("the live steps of %+v", k.live.starts)
	}
	// 0.3.0's folder is no longer pending: its ticket is ended, and 0.4.0's may start.
	f, _ := release.Read(filepath.Join(k.rel, "0.3.0"))
	f.WriteGate(release.Gate{At: k.now, InstallTest: "skipped"})
	k.round("off")
	if g := k.gateRow("0.3.0"); g.State != versions.GateFailed || len(k.live.abandoned) != 1 || !strings.HasPrefix(k.live.abandoned[0], "0.3.0: ") {
		t.Fatalf("%+v %v", g, k.live.abandoned)
	}
	if n := len(k.live.starts); n != 2 || k.live.starts[1].Version != "0.4.0" {
		t.Fatalf("the live steps of %+v", k.live.starts)
	}
	// The image is a gate's only while one runs its live part.
	img := "sha256:" + strings.Repeat("e", 64)
	k.d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source, hidden, runtime_image)
		VALUES (gen_random_uuid(), 'crew-gate', 'GATE', 'running', 'local', true, $1)`, img)
	k.round("off")
	var named *string
	k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named)
	if named == nil {
		t.Fatal("the image was taken while a gate runs its live part")
	}
	k.d.Server.Exec(ctx, `UPDATE gates SET state = 'done'`)
	k.round("off")
	if k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named); named != nil {
		t.Fatalf("the image stays with no gate live: %s", *named)
	}
	k.d.Server.Exec(ctx, `UPDATE projects SET runtime_image = $1 WHERE name = 'crew-gate'`, img)
	if err := k.b.RecoverGates(ctx); err != nil {
		t.Fatal(err)
	}
	if k.d.Server.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE name = 'crew-gate'`).Scan(&named); named != nil {
		t.Fatal("a restart left the image")
	}
}

// A live gate the operator did not press, whose real ticket runs, is ended once the running
// release's runtime inputs are no longer its own, and waits for the press with no ticket.
func TestARunningTicketEndsWhenItsReleaseNoLongerRuns(t *testing.T) {
	k := newGateKit(t)
	k.build("0.2.0")
	k.sameRuntime("0.2.0")
	for range 3 {
		k.round("off")
	}
	// The live steps run on Crew's project and the release's commit, whose eval files a release's suite
	// lays over the baseline, in an image of the running release's code (16.2 step 5, since 0.52).
	if last := k.live.starts[len(k.live.starts)-1]; last.Project != "crew" || last.Commit != k.head || !last.SameRuntime {
		t.Fatalf("the live steps of %+v, on the commit %s", last, k.head)
	}
	k.d.Server.Exec(context.Background(), `UPDATE gates SET ticket = 'GATE-1', suites = '[]', suites_started_at = now(), suites_limit_since = now(),
		suites_limit_s = 60, suites_step = '{}' WHERE version = '0.2.0'`)
	run, _ := release.Read(filepath.Join(k.rel, "0.1.0"))
	release.Write(run.Dir, release.Facts{Version: "0.1.0", BuiltAt: time.Now(), Images: run.Manifest.Images, Deploy: release.DeployFile,
		Schema: 51, Runtime: map[string]string{"versions.lock": strings.Repeat("9", 64)}}, run.Manifest.Files, nil)
	k.gate("0.1.0")
	k.round("off")
	var ticket *string
	var suites int
	k.d.Server.QueryRow(context.Background(), `SELECT ticket, (suites IS NOT NULL)::int + (suites_started_at IS NOT NULL)::int +
		(suites_limit_since IS NOT NULL)::int + suites_limit_s + (suites_step IS NOT NULL)::int FROM gates WHERE version = '0.2.0'`).Scan(&ticket, &suites)
	if g := k.gateRow("0.2.0"); g.State != versions.GateWaiting || ticket != nil || suites != 0 || len(k.live.abandoned) != 1 {
		t.Fatalf("%+v %v, the suites' columns %d, %v", g, ticket, suites, k.live.abandoned)
	}
}

// A real ticket that cannot be ended leaves its gate's row as it was, and the next round tries again;
// a gate whose step fails before it added a ticket holds no slot (review of M16 P4c, round 2).
func TestALiveGateWaitsForItsTicketToEnd(t *testing.T) {
	k := newGateKit(t)
	ctx := context.Background()
	k.release("0.3.0", nil, false)
	f, _ := release.Read(filepath.Join(k.rel, "0.3.0"))
	f.WriteGate(release.Gate{At: k.now, InstallTest: "skipped"})
	k.d.Server.Exec(ctx, `INSERT INTO gates (version, state, pressed_at, ticket) VALUES ('0.3.0', 'live', now(), 'GATE-1')`)
	k.live.abandonErr = errors.New("the live steps have no board yet")
	k.round("off")
	if g := k.gateRow("0.3.0"); g.State != versions.GateLive {
		t.Fatalf("a gate whose ticket was not ended moved on: %+v", g)
	}
	k.live.abandonErr = nil
	k.round("off")
	if g := k.gateRow("0.3.0"); g.State != versions.GateFailed || len(k.live.abandoned) != 1 {
		t.Fatalf("%+v %v", g, k.live.abandoned)
	}
	for _, v := range []string{"0.4.0", "0.5.0"} {
		k.release(v, nil, false)
		g, _ := release.Read(filepath.Join(k.rel, v))
		g.WriteGate(release.Gate{At: k.now, InstallTest: "skipped", Live: release.LivePending, Steps: []release.GateStep{{Name: "make check", OK: true}}})
	}
	k.d.Server.Exec(ctx, `INSERT INTO gates (version, state, pressed_at, updated_at) VALUES ('0.4.0', 'live', now(), now() - interval '1 hour'), ('0.5.0', 'live', now(), now())`)
	k.live.stepErr = map[string]error{"0.4.0": errors.New("the candidate's runtime image is named oddly")}
	n := len(k.live.starts)
	k.round("off")
	if got := k.live.starts[n:]; len(got) != 2 || got[0].Version != "0.4.0" || got[1].Version != "0.5.0" {
		t.Fatalf("the live steps of %+v", got)
	}
}
