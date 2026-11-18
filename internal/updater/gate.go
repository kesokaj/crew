package updater

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/release"
)

// The gate (16.2 step 5, since 0.52). The server, or crew-ctl gate on the machine, runs make check and
// the install test, and asks the updater for the rest: the release in check mode beside the running
// one, the smoke microVM the updater starts, the selftest, and the browser tests. The updater writes
// the one gate.json, with the live part pending, and writes it again when the server sends the result
// of the live steps, which it ran on its own schedule. Pending is never passed.

// GateAsk is what a gate's caller ran before it asked: its steps, bound to the release.json whose hash
// it names, the install test, and the steps it skipped, each with why.
type GateAsk struct {
	ManifestSHA256  string             `json:"manifestSha256"`
	Steps           []release.GateStep `json:"steps"`
	InstallTest     string             `json:"installTest"`
	InstallTestNote string             `json:"installTestNote,omitempty"`
	Skipped         []string           `json:"skipped,omitempty"`
}

// LiveAsk is the server's result of the live steps of a gate, as one request (16.2 step 5).
type LiveAsk struct {
	Version        string             `json:"version"`
	ManifestSHA256 string             `json:"manifestSha256"`
	Live           string             `json:"live"`
	Steps          []release.GateStep `json:"steps"`
	Skipped        []string           `json:"skipped,omitempty"`
	Hold           string             `json:"hold,omitempty"`
}

// What a gate's text may hold: steps and notes are shown on Versions and written into the release's
// notes, so each is bounded.
const (
	maxGateSteps = 40
	maxGateText  = 2000
)

// askGate is step 1 of a gate: the caller's steps are bound to this release.json, and a release whose
// gate passed or waits for its live steps is not gated again.
func (u *Updater) askGate(req *Request, a Ask, f *release.Folder) error {
	g := a.Gate
	if g == nil {
		return refuse("a gate's request names its steps and the hash of the release.json they ran on")
	}
	if g.ManifestSHA256 != f.ManifestSHA256 {
		return refuse("the gate's steps ran on another release.json than %s's", a.Version)
	}
	switch {
	case f.Gate != nil:
		return refuse("%s passed its gate already", a.Version)
	case f.Pending != nil:
		return refuse("the gate of %s waits for its live steps", a.Version)
	case f.Manifest.ByHand:
		return refuse("%s is a build by hand, which root gives without the gate", a.Version)
	}
	if err := checkGateText(g.Steps, g.Skipped); err != nil {
		return err
	}
	switch g.InstallTest {
	case "passed", "failed", "skipped":
	default:
		return refuse("the install test is passed, failed, or skipped, not %q", g.InstallTest)
	}
	req.Action, req.Gate = ActionGate, g
	return nil
}

func checkGateText(steps []release.GateStep, notes []string) error {
	if len(steps) > maxGateSteps || len(notes) > maxGateSteps {
		return refuse("a gate has at most %d steps and %d notes", maxGateSteps, maxGateSteps)
	}
	for _, s := range steps {
		if s.Name == "" || !utf8.ValidString(s.Name+s.Detail) || len(s.Name) > maxGateText || len(s.Detail) > maxGateText {
			return refuse("a gate's step has a name, and each text is UTF-8 of at most %d bytes", maxGateText)
		}
	}
	for _, n := range notes {
		if n == "" || !utf8.ValidString(n) || len(n) > maxGateText {
			return refuse("a gate's note is UTF-8 of at most %d bytes", maxGateText)
		}
	}
	return nil
}

// plain is a text of a gate as it is written: what a terminal, a page, or a release's notes would read
// as more than text is taken out, which is every control, format, and direction-changing character and
// every separator but the space; a line break or a tab becomes a space.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r != ' ' && (unicode.Is(unicode.C, r) || unicode.Is(unicode.Z, r)):
			return -1
		}
		return r
	}, s)
}

func cleanSteps(steps []release.GateStep) []release.GateStep {
	out := make([]release.GateStep, len(steps))
	for i, s := range steps {
		out[i] = release.GateStep{Name: plain(s.Name), OK: s.OK, Detail: plain(s.Detail)}
	}
	return out
}

func cleanNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		out = append(out, plain(n))
	}
	return out
}

// gateStepsPassed reports whether every step the caller ran passed, the install test too.
func gateStepsPassed(g *GateAsk) bool {
	if g == nil || g.InstallTest == "failed" {
		return false
	}
	for _, s := range g.Steps {
		if !s.OK {
			return false
		}
	}
	return true
}

// gate is the rest of a gate's request after step 4: the selftest and the browser tests in the check
// server, which is then removed, and gate.json with the live part pending.
func (u *Updater) gate(ctx context.Context, req *Request, f *release.Folder, clean func()) {
	defer clean()
	if err := u.Machine.Selftest(ctx); err != nil {
		u.note(req, 4, false, "the selftest: "+err.Error())
		u.gateFailed(req, f)
		u.end(req, StateFailed, "Check: the selftest: "+err.Error())
		return
	}
	u.note(req, 4, true, "the selftest: one ticket from its OK to Done on the copy, with the fake harness")
	if err := u.Machine.Browser(ctx, f); err != nil {
		u.note(req, 4, false, "the browser tests: "+err.Error())
		u.gateFailed(req, f)
		u.end(req, StateFailed, "Check: the browser tests: "+err.Error())
		return
	}
	u.note(req, 4, true, "the browser tests of the screens, which only read")
	if err := u.writeGate(req, f, true); err != nil {
		u.note(req, 4, false, "gate.json was not written: "+err.Error())
		u.end(req, StateFailed, "Check: gate.json was not written: "+err.Error())
		return
	}
	u.end(req, StateDone, "")
}

// gateFailed writes the gate of a request that failed at a step: failed, with the steps that ran.
func (u *Updater) gateFailed(req *Request, f *release.Folder) {
	if err := u.writeGate(req, f, false); err != nil && u.Log != nil {
		u.Log.Error("writing a failed gate", "version", req.Version, "error", err)
	}
}

// writeGate writes gate.json from the caller's steps and the request's own: pending when every one
// passed, which is never passed, else failed.
func (u *Updater) writeGate(req *Request, f *release.Folder, ok bool) error {
	g := req.Gate
	steps := append([]release.GateStep(nil), g.Steps...)
	for _, s := range req.Steps {
		if s.Step < 2 {
			continue
		}
		steps = append(steps, release.GateStep{Name: StepNames[s.Step], OK: s.OK, Detail: s.Detail})
	}
	gate := release.Gate{At: u.now().UTC(), Steps: cleanSteps(steps), InstallTest: g.InstallTest, InstallTestNote: plain(g.InstallTestNote),
		Skipped: cleanNotes(g.Skipped)}
	if ok && gateStepsPassed(g) {
		gate.Live = release.LivePending
	}
	return f.WriteGate(gate)
}

// ErrNoPendingGate is a live result for a release whose gate does not wait for one.
var ErrNoPendingGate = errors.New("the release's gate does not wait for its live steps")

// Live writes the server's result of the live steps into the release's pending gate.json: bound to
// the same release.json, passed unless the live part failed, and with the steps it skipped, each of
// which keeps the release from installing by itself (16.2 step 5, 16.3).
func (u *Updater) Live(a LiveAsk) (*release.Gate, error) {
	if !release.ValidVersion(a.Version) {
		return nil, refuse("%q is not a version", a.Version)
	}
	switch a.Live {
	case release.LivePassed:
		// Checks decide: a passed live part names the steps that passed.
		if len(a.Steps) == 0 {
			return nil, refuse("a passed live part names the steps it ran")
		}
	case release.LiveFailed:
	case release.LiveSkipped:
		if len(a.Skipped) == 0 {
			return nil, refuse("a skipped live part names what it skipped, and why")
		}
	default:
		return nil, refuse("the live part is passed, failed, or skipped, not %q", a.Live)
	}
	if err := checkGateText(a.Steps, a.Skipped); err != nil {
		return nil, err
	}
	if a.Hold != "" && (!utf8.ValidString(a.Hold) || len(a.Hold) > maxGateText) {
		return nil, refuse("a hold is UTF-8 of at most %d bytes", maxGateText)
	}
	// Two results for one gate are written one after the other: the second finds it no longer pending.
	u.liveMu.Lock()
	defer u.liveMu.Unlock()
	f, err := release.Read(filepath.Join(u.Releases, a.Version))
	if err != nil {
		return nil, refuse("there is no release %s on this machine: %v", a.Version, err)
	}
	if u.RequireRoot {
		if err := f.Owned(); err != nil {
			return nil, refuse("%v", err)
		}
	}
	if a.ManifestSHA256 != f.ManifestSHA256 {
		return nil, refuse("the live steps ran on another release.json than %s's", a.Version)
	}
	if f.Pending == nil {
		return nil, ErrNoPendingGate
	}
	g := *f.Pending
	g.At = u.now().UTC()
	g.Live, g.LiveSteps, g.Hold = a.Live, cleanSteps(a.Steps), plain(a.Hold)
	g.Skipped = append(append([]string(nil), g.Skipped...), cleanNotes(a.Skipped)...)
	g.Passed = a.Live != release.LiveFailed
	// A passed live part passed every step; a skipped one names the step that did not run.
	for _, s := range a.Steps {
		if !s.OK && a.Live == release.LivePassed {
			return nil, refuse("a live part with a failed step is not passed: %s", strings.TrimSpace(s.Name))
		}
	}
	if err := f.WriteGate(g); err != nil {
		return nil, err
	}
	return &g, nil
}
