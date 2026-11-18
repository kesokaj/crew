// Package updater is crew-updater (design 16.4, 16.5, 16.8): the small program, in its own container
// beside the server, that installs a release and rolls one back. It loads a release's images and
// checks their hashes, tries the stored state on a scratch copy, checks the new version beside the
// running one, switches, and watches; a version that fails before the switch changes nothing, and
// one that turns unhealthy after it is switched back. It needs neither the server nor the database
// to do so: its log is files on the machine, which the server copies into update_requests. It
// never replaces itself.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// Machine is what the updater does with Docker. The real one runs Docker Compose with the installed
// deploy files; a test's is a fake.
type Machine interface {
	// Load loads the images of a release that Verify passed, and checks that Docker has them with
	// the ids release.json names.
	Load(ctx context.Context, f *release.Folder) error
	// Snapshot dumps the database, without the events of ended months, into file, with the crew
	// admin of version (16.4, step 3).
	Snapshot(ctx context.Context, version, file string) error
	// Scratch makes the scratch database again, empty; DropScratch removes it.
	Scratch(ctx context.Context) error
	DropScratch(ctx context.Context) error
	// RestoreScratch reads snapshot into the scratch database and applies the release's schema to it,
	// with the release's crew admin.
	RestoreScratch(ctx context.Context, f *release.Folder, snapshot string) error
	// StartCheck starts the release's server in check mode on the scratch database, beside the running
	// one, from a spec the updater writes itself: no Docker socket, no key file, the scratch network,
	// and the image by this machine's id for the release's tag, once it is the image release.json names
	// by its config digest (16.4 step 3, since 0.51; D48). StopCheck removes it.
	StartCheck(ctx context.Context, f *release.Folder) error
	StopCheck(ctx context.Context) error
	// Health asks a server for its health: it answers, and reads and writes its database. Proof has
	// the server start one microVM from its runtime image and store an event it printed (16.8). Each
	// takes the server's container. Smoke is the check server's smoke run, which has no Docker socket:
	// the updater starts the microVM from the release's runtime image, and the check server stores the
	// event it printed (since 0.52).
	Health(ctx context.Context, container string) error
	Proof(ctx context.Context, container string) error
	Smoke(ctx context.Context, f *release.Folder) error
	// Selftest runs the selftest in the check server: one ticket from its OK to Done with the fake
	// harness. Browser runs the release's browser tests of the screens, which only read, against the
	// check server (16.2 step 5).
	Selftest(ctx context.Context) error
	Browser(ctx context.Context, f *release.Folder) error
	// StopServer stops the running server: new runs are held from here (16.7). StartServer starts
	// version's server and ingress.
	StopServer(ctx context.Context) error
	StartServer(ctx context.Context, version string) error
	// Migrate applies version's schema to the real database in one transaction (16.4, step 5).
	Migrate(ctx context.Context, version string) error
	// Restore reads snapshot back into the real database, with version's crew admin (16.6).
	Restore(ctx context.Context, version, snapshot string) error
	// Remove removes the images of a version that is no longer kept.
	Remove(ctx context.Context, version string) error
	// LoadImage loads an image file of a build, which must name tag and no other, and returns the id
	// Docker gives it; Untag takes a tag away again (16.2 step 4).
	LoadImage(ctx context.Context, path, tag string) (string, error)
	Untag(ctx context.Context, tag string) error
}

// The containers the updater checks.
const (
	ServerContainer = "crew-server"
	CheckContainer  = "crew-server-check"
)

// The actions of a request.
const (
	ActionInstall  = "install"
	ActionRollback = "rollback"
	// ActionGate is the gate's: steps 1 to 4, the selftest, and the browser tests, then gate.json with
	// the live part pending (16.2 step 5, since 0.52). ActionCheck is what an updater before 0.52 asked
	// the gate's check as, which its log may still hold.
	ActionGate  = "gate"
	ActionCheck = "check"
)

// The states of a request.
const (
	StateAsked        = "asked"
	StateRunning      = "running"
	StateDone         = "done"
	StateFailed       = "failed"
	StateSwitchedBack = "switched_back"
	StateStopped      = "stopped"
)

// The steps of 16.4.
var StepNames = []string{"", "Ask", "Load", "Try the state", "Check", "Switch", "Watch", "Keep"}

// Request is one request to the updater, and its log.
type Request struct {
	ID      int64      `json:"id"`
	Version string     `json:"version"`
	From    string     `json:"from,omitempty"`
	Action  string     `json:"action"`
	AskedBy string     `json:"askedBy"`
	ByHand  bool       `json:"byHand,omitempty"`
	Confirm string     `json:"confirm,omitempty"`
	State   string     `json:"state"`
	Step    int        `json:"step"`
	Steps   []Step     `json:"steps"`
	Error   string     `json:"error,omitempty"`
	AskedAt time.Time  `json:"askedAt"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
	// Restores is whether this request reads a snapshot back into the real database, which loses
	// what was written after it (16.6).
	Restores bool `json:"restores,omitempty"`
	// Gate is a gate's request: what its caller ran.
	Gate *GateAsk `json:"gate,omitempty"`
	// Keep is the number of releases its ask named, stored by its step Keep alone (D30).
	Keep int `json:"keep,omitempty"`
}

// Step is a line of a request's log.
type Step struct {
	Step   int       `json:"step"`
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail"`
}

// State is what the updater keeps between requests.
type State struct {
	// Running is the version the server runs; Previous the one before it.
	Running  string `json:"running"`
	Previous string `json:"previous,omitempty"`
	// Updater is the version of the updater that runs, written by the updater itself at start.
	Updater string `json:"updater,omitempty"`
	// Keep is the server's updates.keep_versions, from the last ask that named one (D30): an ask of
	// crew-ctl names none and keeps as many. Zero is Updater.Keep.
	Keep int `json:"keep,omitempty"`
}

// ByClock is who asks for a release that installs by itself (16.3): the updater refuses it a release
// whose gate skipped a step or holds it, whatever the server decided.
const ByClock = "clock"

// Ask is a request as it is sent over the socket.
type Ask struct {
	Version string `json:"version"`
	// Action is install, rollback, or check; install and rollback are the same steps, and which one
	// it is follows from the versions.
	Action  string `json:"action,omitempty"`
	AskedBy string `json:"askedBy"`
	// ByHand asks for a release that has no gate: only root on the machine may (16.10).
	ByHand bool `json:"byHand,omitempty"`
	// Confirm is the version's name, typed, for a roll back that restores the snapshot (16.6).
	Confirm string `json:"confirm,omitempty"`
	// Gate is what a gate's request carries: the steps its caller ran, make check and the install
	// test, on the release.json whose hash it names.
	Gate *GateAsk `json:"gate,omitempty"`
	// Keep is how many releases are kept, the server's updates.keep_versions, from 2 to 20; zero
	// keeps the number the updater was last given (D30).
	Keep int `json:"keep,omitempty"`
}

// Updater runs one request at a time.
type Updater struct {
	Machine Machine
	// Releases is the folder of release folders; Dir is the updater's own: its state, its requests,
	// and the snapshots. Builds is the server's folder of the builds it hands over to be loaded.
	Releases string
	Dir      string
	Builds   string
	// FreeSpace is the bytes free on the disk of a path; nil is the system's. A test stands in for it.
	FreeSpace func(path string) (uint64, error)
	// MachineSetup is the number of the setup this machine has (install.json).
	MachineSetup func() (int, error)
	Now          func() time.Time
	// WatchFor and WatchEvery are the watch after the switch: ten minutes, every 30 seconds (16.4).
	WatchFor, WatchEvery time.Duration
	// HealthFor is how long a server that was just started has to answer.
	HealthFor time.Duration
	// Keep is how many releases are kept with their images and snapshots.
	Keep int
	// RequireRoot takes a release folder only when root owns it and nobody else may write it: on the
	// machine, where the server's user could otherwise put a folder with a gate of its own (16.8).
	RequireRoot bool
	Log         *slog.Logger

	mu      sync.Mutex
	current *Request
	cancel  context.CancelFunc
	// starting is a request in its step 1.
	starting bool
	done     chan struct{}
	// cutOff are the requests a stop of the updater ended after their switch began (Begin).
	cutOff []*Request
	// loading is a load of a build that runs: no request starts meanwhile, and no load while one runs.
	loading bool
	// liveMu is held while a gate's live result is written.
	liveMu sync.Mutex
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// ErrBusy is the answer while a request runs.
var ErrBusy = errors.New("the updater is working on another request")

// Refusal is a request the updater will not start, with the reason (16.4, step 1).
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, a ...any) error { return &Refusal{Reason: fmt.Sprintf(format, a...)} }

// Start checks a request and starts it, and returns it at once; it runs on in the background.
// root is whether the caller is root on the machine.
func (u *Updater) Start(ctx context.Context, a Ask, root bool) (*Request, error) {
	req := &Request{Version: a.Version, AskedBy: a.AskedBy, ByHand: a.ByHand, Confirm: a.Confirm, Keep: a.Keep,
		State: StateRunning, AskedAt: u.now().UTC()}
	// Step 1 runs before the request is the current one: Status and Stop see it only whole, with its
	// cancel, once it was not refused.
	u.mu.Lock()
	if u.current != nil || u.starting || u.loading {
		u.mu.Unlock()
		return nil, ErrBusy
	}
	u.starting = true
	u.mu.Unlock()
	started := false
	defer func() {
		if !started {
			u.mu.Lock()
			u.starting = false
			u.mu.Unlock()
		}
	}()
	st, err := u.State()
	if err != nil {
		return nil, err
	}
	req.From = st.Running
	if req.ID, err = u.nextID(); err != nil {
		return nil, err
	}
	f, err := u.ask(req, a, st, root)
	if err != nil {
		var r *Refusal
		if errors.As(err, &r) {
			u.note(req, 1, false, r.Reason)
			u.end(req, StateFailed, r.Reason)
		}
		return u.copyOf(req), err
	}
	detail := fmt.Sprintf("%s %s from %s, asked by %s", req.Action, req.Version, orNone(req.From), req.AskedBy)
	// Install says which step of the gate did not run (16.2 step 5): the gate itself ran and passed,
	// and only the steps it names did not.
	if f.Gate != nil && len(f.Gate.Skipped) > 0 && req.Action != ActionGate {
		steps := "a step"
		if n := len(f.Gate.Skipped); n > 1 {
			steps = fmt.Sprintf("%d steps", n)
		}
		detail += fmt.Sprintf("; its gate passed, and %s did not run: %s", steps, strings.Join(f.Gate.Skipped, "; "))
	}
	u.note(req, 1, true, detail)
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	u.mu.Lock()
	u.current, u.cancel, u.done, u.starting = req, cancel, make(chan struct{}), false
	// The copy is taken before run can write to the request.
	c := u.copyOf(req)
	u.mu.Unlock()
	started = true
	go u.run(rctx, req, f, st)
	return c, nil
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

// ask is step 1: the release must be there, unchanged since it was built, past the gate unless root
// gives a build by hand, for this machine's setup, and a roll back that restores the snapshot must
// be confirmed with the version's name.
func (u *Updater) ask(req *Request, a Ask, st State, root bool) (*release.Folder, error) {
	if !release.ValidVersion(a.Version) {
		return nil, refuse("%q is not a version", a.Version)
	}
	if a.AskedBy == "" {
		return nil, refuse("a request says who asks")
	}
	if a.Keep != 0 && (a.Keep < 2 || a.Keep > 20) {
		return nil, refuse("keep must be from 2 to 20 releases, not %d", a.Keep)
	}
	f, err := release.Read(filepath.Join(u.Releases, a.Version))
	if err != nil {
		return nil, refuse("there is no release %s on this machine: %v", a.Version, err)
	}
	if err := f.Verify(); err != nil {
		return nil, refuse("%v", err)
	}
	if u.RequireRoot {
		if err := f.Owned(); err != nil {
			return nil, refuse("%v", err)
		}
	}
	if u.MachineSetup != nil {
		have, err := u.MachineSetup()
		if err != nil {
			return nil, refuse("the machine's setup cannot be read: %v", err)
		}
		if f.Manifest.MachineSetup > have {
			return nil, refuse("%s needs machine setup %d and this machine has %d: run crew-ctl install --upgrade-runtime on the machine",
				a.Version, f.Manifest.MachineSetup, have)
		}
	}
	if a.Action == ActionGate {
		return f, u.askGate(req, a, f)
	}
	switch {
	case f.Gate != nil && a.AskedBy == ByClock && !f.Gate.InstallsBySelfAfterGate():
		why := f.Gate.Hold
		if len(f.Gate.Skipped) > 0 {
			why = "its gate skipped " + strings.Join(f.Gate.Skipped, "; ")
		}
		return nil, refuse("%s does not install by itself: %s. It waits for Install", a.Version, why)
	case f.Gate != nil:
	case a.ByHand && root:
	case f.Pending != nil:
		return nil, refuse("the gate of %s is pending: its live steps have not run. Run the live gate on Versions first", a.Version)
	case a.ByHand:
		return nil, refuse("a release without the gate is given only by root on the machine (crew-ctl install --from)")
	case f.Manifest.ByHand:
		return nil, refuse("%s is a build by hand: only root on the machine gives it, with crew-ctl install --from", a.Version)
	default:
		return nil, refuse("%s did not pass the gate", a.Version)
	}
	switch a.Action {
	case "", ActionInstall, ActionRollback:
	default:
		return nil, refuse("%q is not an action", a.Action)
	}
	if a.Version == st.Running {
		return nil, refuse("%s is the version that runs", a.Version)
	}
	req.Action = ActionInstall
	if st.Running != "" && release.Less(a.Version, st.Running) {
		req.Action = ActionRollback
	}
	if st.Running != "" {
		cur, err := release.Read(filepath.Join(u.Releases, st.Running))
		if err != nil {
			return nil, refuse("the running release %s cannot be read: %v", st.Running, err)
		}
		if curMaj, nextMaj := postgresMajor(cur.Manifest.Images["postgres"].Tag), postgresMajor(f.Manifest.Images["postgres"].Tag); curMaj != "" && nextMaj != "" && curMaj != nextMaj {
			return nil, refuse("%s changes PostgreSQL's major version from %s to %s: upgrade PostgreSQL on the machine with crew-ctl first", a.Version, curMaj, nextMaj)
		}
		// The target cannot read the database as it is: its own snapshot is read back, and what was
		// written since is lost. Crew does not do that by itself (16.6).
		if f.Manifest.Schema < cur.Manifest.ReadBy {
			req.Restores = true
			if _, err := os.Stat(u.snapshotOf(a.Version)); err != nil {
				return nil, refuse("%s cannot read the database of %s, and there is no snapshot of when it last ran", a.Version, st.Running)
			}
			if a.Confirm != a.Version {
				return nil, refuse("%s cannot read the database of %s: its snapshot of %s is read back, and everything written since is lost. Type %s to go on",
					a.Version, st.Running, snapshotTime(u.snapshotOf(a.Version)), a.Version)
			}
		}
	}
	return f, nil
}

func snapshotTime(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "an unknown time"
	}
	return st.ModTime().UTC().Format(time.RFC3339)
}

func postgresMajor(tag string) string {
	_, rest, ok := strings.Cut(tag, ":")
	if !ok || rest == "" {
		return ""
	}
	maj, _, _ := strings.Cut(rest, ".")
	maj, _, _ = strings.Cut(maj, "-")
	return maj
}

// run is steps 2 to 7.
func (u *Updater) run(ctx context.Context, req *Request, f *release.Folder, st State) {
	defer func() {
		u.mu.Lock()
		u.current, u.cancel = nil, nil
		close(u.done)
		u.mu.Unlock()
	}()
	fail := func(step int, err error) {
		u.note(req, step, false, err.Error())
		u.end(req, StateFailed, fmt.Sprintf("%s: %v", StepNames[step], err))
		if req.Action == ActionGate {
			u.gateFailed(req, f)
		}
	}
	if req.Action == ActionGate && !gateStepsPassed(req.Gate) {
		// What the caller ran failed: the gate failed, and nothing of the release is started.
		u.gateFailed(req, f)
		u.end(req, StateFailed, "the gate's own steps failed before the check")
		return
	}
	// 2. Load.
	if err := u.Machine.Load(ctx, f); err != nil {
		fail(2, err)
		return
	}
	u.note(req, 2, true, "the images are loaded, with the hashes the release names")

	// 3. Try the state: the snapshot of the running version, read into a scratch database, the new
	// schema applied to that copy, and the new server beside the running one, in check mode.
	if err := u.stopped(ctx); err != nil {
		fail(3, err)
		return
	}
	snapshot := ""
	if st.Running != "" {
		snapshot = u.snapshotOf(st.Running)
		if err := os.MkdirAll(filepath.Dir(snapshot), 0o700); err != nil {
			fail(3, err)
			return
		}
		if err := u.Machine.Snapshot(ctx, st.Running, snapshot); err != nil {
			fail(3, fmt.Errorf("the snapshot: %w", err))
			return
		}
	}
	tryOn := snapshot
	if req.Restores {
		tryOn = u.snapshotOf(req.Version)
	}
	cleanCheck := func() {
		c := context.WithoutCancel(ctx)
		_ = u.Machine.StopCheck(c)
		_ = u.Machine.DropScratch(c)
	}
	if err := u.Machine.Scratch(ctx); err != nil {
		cleanCheck()
		fail(3, fmt.Errorf("the scratch database: %w", err))
		return
	}
	if tryOn != "" {
		if err := u.Machine.RestoreScratch(ctx, f, tryOn); err != nil {
			cleanCheck()
			fail(3, fmt.Errorf("the stored state on a copy: %w", err))
			return
		}
	}
	if err := u.Machine.StartCheck(ctx, f); err != nil {
		cleanCheck()
		fail(3, fmt.Errorf("the new version beside the running one: %w", err))
		return
	}
	u.note(req, 3, true, "the stored state is read by "+req.Version+" on a scratch copy, beside the running version")

	// 4. Check: healthy, and a smoke run in a microVM from the new runtime image.
	if err := u.healthy(ctx, CheckContainer); err != nil {
		cleanCheck()
		fail(4, fmt.Errorf("the new version is not healthy: %w", err))
		return
	}
	if err := u.Machine.Smoke(ctx, f); err != nil {
		cleanCheck()
		fail(4, fmt.Errorf("the smoke run in a microVM: %w", err))
		return
	}
	u.note(req, 4, true, "healthy, and a microVM the updater started from the new runtime image printed an event the new version stored")
	if req.Action == ActionGate {
		u.gate(ctx, req, f, cleanCheck)
		return
	}
	cleanCheck()
	if err := u.stopped(ctx); err != nil {
		fail(4, err)
		return
	}

	// 5. Switch. From here a stop is not taken: the switch is seconds.
	ctx = context.WithoutCancel(ctx)
	if err := u.Machine.StopServer(ctx); err != nil {
		u.note(req, 5, false, "the old server did not stop: "+err.Error())
		_ = u.Machine.StartServer(ctx, st.Running)
		u.end(req, StateFailed, "Switch: the old server did not stop; it runs on")
		return
	}
	if req.Restores {
		if err := u.Machine.Restore(ctx, req.Version, u.snapshotOf(req.Version)); err != nil {
			u.note(req, 5, false, "the snapshot was not read back: "+err.Error())
			u.startOld(ctx, req, st)
			u.end(req, StateFailed, "Switch: the snapshot was not read back; the old version runs again")
			return
		}
	}
	// A snapshot that was read back has the version's schema already: Restore applied it.
	if !req.Restores {
		if err := u.Machine.Migrate(ctx, req.Version); err != nil {
			u.note(req, 5, false, "the schema was not changed: "+err.Error())
			u.startOld(ctx, req, st)
			u.end(req, StateFailed, "Switch: the schema change was undone, and the old version runs again")
			return
		}
	}
	if err := u.Machine.StartServer(ctx, req.Version); err != nil {
		u.note(req, 5, false, "the new server did not start: "+err.Error())
		u.switchBack(ctx, req, st, snapshot)
		return
	}
	u.note(req, 5, true, "switched: "+req.Version+" runs")

	// 6. Watch.
	if err := u.watch(ctx); err != nil {
		u.note(req, 6, false, err.Error())
		u.switchBack(ctx, req, st, snapshot)
		return
	}
	u.note(req, 6, true, fmt.Sprintf("healthy for %s", u.watchFor()))

	// 7. Keep. The number an ask named is stored here, by an install that kept, and by no gate.
	keep := st.Keep
	if req.Keep != 0 {
		keep = req.Keep
	}
	if err := u.saveState(State{Running: req.Version, Previous: st.Running, Updater: st.Updater, Keep: keep}); err != nil {
		u.note(req, 7, false, err.Error())
		u.end(req, StateFailed, "Keep: the state was not written: "+err.Error())
		return
	}
	removed := u.prune(ctx, req.Version, st.Running, keep)
	// The number is in the request's log, so a change of it can be seen (round 2 of the security review).
	detail := fmt.Sprintf("recorded as running; %d releases kept", u.keepOr(keep))
	if len(removed) > 0 {
		detail += "; no longer kept: " + strings.Join(removed, ", ")
	}
	u.note(req, 7, true, detail)
	u.end(req, StateDone, "")
}

// startOld starts the version that ran before, after a switch that did not happen.
func (u *Updater) startOld(ctx context.Context, req *Request, st State) {
	if st.Running == "" {
		return
	}
	if err := u.Machine.StartServer(ctx, st.Running); err != nil {
		u.note(req, 5, false, "the old version did not start again: "+err.Error())
		return
	}
	// The request ends once the old server answers again, so it reads the end of the request.
	if err := u.healthy(ctx, ServerContainer); err != nil {
		u.note(req, 5, false, "the old version is not healthy again: "+err.Error())
	}
}

// switchBack goes back to the version before, after the switch: its snapshot is read back first when
// it cannot read what the new schema made of the database (16.5).
func (u *Updater) switchBack(ctx context.Context, req *Request, st State, snapshot string) {
	_ = u.Machine.StopServer(ctx)
	if st.Running == "" {
		u.end(req, StateFailed, "the new version is not healthy, and there is no version to go back to")
		return
	}
	old, err := release.Read(filepath.Join(u.Releases, st.Running))
	next, nerr := release.Read(filepath.Join(u.Releases, req.Version))
	if err == nil && nerr == nil && old.Manifest.Schema < next.Manifest.ReadBy && snapshot != "" {
		if err := u.Machine.Restore(ctx, st.Running, snapshot); err != nil {
			u.note(req, 6, false, "the snapshot was not read back: "+err.Error())
		} else {
			u.note(req, 6, true, "the snapshot of the minutes before the switch is read back")
		}
	}
	if err := u.Machine.StartServer(ctx, st.Running); err != nil {
		u.note(req, 6, false, "the version before did not start: "+err.Error())
		u.end(req, StateFailed, "switching back failed: "+err.Error())
		return
	}
	if err := u.healthy(ctx, ServerContainer); err != nil {
		u.note(req, 6, false, "the version before is not healthy either: "+err.Error())
	} else {
		u.note(req, 6, true, "switched back to "+st.Running)
	}
	u.end(req, StateSwitchedBack, "the new version turned unhealthy, and the updater switched back to "+st.Running)
}

// healthy waits for a server that was just started to answer, for HealthFor.
func (u *Updater) healthy(ctx context.Context, container string) error {
	deadline := u.now().Add(u.healthFor())
	var err error
	for {
		if err = u.Machine.Health(ctx, container); err == nil {
			return nil
		}
		if !u.now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(u.pause(2 * time.Second)):
		}
	}
}

// watchFor and watchEvery are the watch after the switch: ten minutes, every 30 seconds, unless set.
func (u *Updater) watchFor() time.Duration {
	if u.WatchFor > 0 {
		return u.WatchFor
	}
	return 10 * time.Minute
}

func (u *Updater) watchEvery() time.Duration {
	if u.WatchEvery > 0 {
		return u.WatchEvery
	}
	return 30 * time.Second
}

func (u *Updater) healthFor() time.Duration {
	if u.HealthFor > 0 {
		return u.HealthFor
	}
	return 2 * time.Minute
}

// pause is a wait between tries, shorter in a test whose clock is not the wall's.
func (u *Updater) pause(d time.Duration) time.Duration {
	if u.WatchEvery > 0 && u.WatchEvery < d {
		return u.WatchEvery
	}
	return d
}

// watch is step 6: the new server healthy at once and after every WatchEvery for WatchFor, and one
// smoke run at the start (16.4, 16.8).
func (u *Updater) watch(ctx context.Context) error {
	if err := u.healthy(ctx, ServerContainer); err != nil {
		return fmt.Errorf("the new version did not become healthy: %w", err)
	}
	if err := u.Machine.Proof(ctx, ServerContainer); err != nil {
		return fmt.Errorf("the smoke run after the switch: %w", err)
	}
	every, total := u.watchEvery(), u.watchFor()
	end := u.now().Add(total)
	for u.now().Before(end) {
		time.Sleep(every)
		if err := u.Machine.Health(ctx, ServerContainer); err != nil {
			// One more try before a version is called unhealthy: a single missed answer is not.
			time.Sleep(every)
			if err2 := u.Machine.Health(ctx, ServerContainer); err2 != nil {
				return fmt.Errorf("unhealthy in the watch: %w", err2)
			}
		}
	}
	return nil
}

func (u *Updater) stopped(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("stopped by the operator before the switch")
	}
	return nil
}

// Stop ends the request that runs, before its switch. A request past step 4 cannot be stopped, but a
// gate's, which never switches.
func (u *Updater) Stop() error {
	u.mu.Lock()
	cur, cancel, done := u.current, u.cancel, u.done
	u.mu.Unlock()
	if cur == nil {
		return refuse("no request runs")
	}
	u.mu.Lock()
	step, action := cur.Step, cur.Action
	u.mu.Unlock()
	if step >= 4 && action != ActionGate {
		return refuse("the request is past its check, and is not stopped")
	}
	cancel()
	<-done
	return nil
}

// Wait waits for the request that runs to end.
func (u *Updater) Wait() {
	u.mu.Lock()
	done := u.done
	u.mu.Unlock()
	if done != nil {
		<-done
	}
}

// keepOr is keep, or Updater.Keep when it is 0, or five.
func (u *Updater) keepOr(keep int) int {
	if keep <= 0 {
		keep = u.Keep
	}
	if keep <= 0 {
		keep = 5
	}
	return keep
}

// prune removes the releases beyond keep, or Updater.Keep when it is 0, oldest first, never the
// running one, the one before it, or one newer than the running one, which waits to be installed.
func (u *Updater) prune(ctx context.Context, running, previous string, keep int) []string {
	keep = u.keepOr(keep)
	fs, _, err := release.List(u.Releases)
	if err != nil {
		return nil
	}
	var removed []string
	for i := 0; len(fs)-len(removed) > keep && i < len(fs); i++ {
		v := fs[i].Manifest.Version
		if v == running || v == previous || release.Less(running, v) {
			continue
		}
		if err := u.Machine.Remove(ctx, v); err != nil {
			u.Log.Warn("removing the images of a release", "version", v, "error", err)
			continue
		}
		if err := os.RemoveAll(fs[i].Dir); err != nil {
			u.Log.Warn("removing a release folder", "version", v, "error", err)
			continue
		}
		_ = os.Remove(u.snapshotOf(v))
		removed = append(removed, v)
	}
	return removed
}

// snapshotOf is the snapshot taken when version was last left: the stored state it ran on.
func (u *Updater) snapshotOf(version string) string {
	return filepath.Join(u.Dir, "snapshots", version+".tar.gz")
}

// note adds a line to the request's log and saves it.
func (u *Updater) note(req *Request, step int, ok bool, detail string) {
	u.mu.Lock()
	req.Step = step
	req.Steps = append(req.Steps, Step{Step: step, Name: StepNames[step], At: u.now().UTC(), OK: ok, Detail: detail})
	u.mu.Unlock()
	if u.Log != nil {
		u.Log.Info("update", "request", req.ID, "version", req.Version, "step", StepNames[step], "ok", ok, "detail", detail)
	}
	if err := u.save(req); err != nil && u.Log != nil {
		u.Log.Error("saving a request", "request", req.ID, "error", err)
	}
}

func (u *Updater) end(req *Request, state, why string) {
	u.mu.Lock()
	req.State, req.Error = state, why
	t := u.now().UTC()
	req.EndedAt = &t
	u.mu.Unlock()
	if err := u.save(req); err != nil && u.Log != nil {
		u.Log.Error("saving a request", "request", req.ID, "error", err)
	}
}

func (u *Updater) copyOf(req *Request) *Request {
	c := *req
	c.Steps = append([]Step(nil), req.Steps...)
	return &c
}

// Status is the updater's answer to "where is it" (16.11).
type Status struct {
	State   State    `json:"state"`
	Current *Request `json:"current,omitempty"`
	// Checking is the version whose check server an updater before 0.52 held for the gate; this one
	// holds none, and the server still reads it of an older updater.
	Checking string     `json:"checking,omitempty"`
	Requests []*Request `json:"requests"`
	// MachineSetup is the number of the setup this machine has, 0 when it cannot be read: the server
	// needs it to tell a release that needs a newer one (16.3).
	MachineSetup int `json:"machineSetup"`
}

// Status returns the state, the request that runs, and the last requests, newest first.
func (u *Updater) Status(last int) (Status, error) {
	st, err := u.State()
	if err != nil {
		return Status{}, err
	}
	reqs, err := u.Requests(last)
	if err != nil {
		return Status{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s := Status{State: st, Requests: reqs}
	if u.MachineSetup != nil {
		s.MachineSetup, _ = u.MachineSetup()
	}
	if u.current != nil {
		s.Current = u.copyOf(u.current)
	}
	return s, nil
}

// Requests reads the last n requests from the updater's folder, newest first.
func (u *Updater) Requests(n int) ([]*Request, error) {
	dir := filepath.Join(u.Dir, "requests")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []*Request{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, e := range entries {
		if id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".json"), 10, 64); err == nil && strings.HasSuffix(e.Name(), ".json") {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if n > 0 && len(ids) > n {
		ids = ids[:n]
	}
	out := []*Request{}
	for _, id := range ids {
		body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.json", id)))
		if err != nil {
			continue
		}
		var r Request
		if json.Unmarshal(body, &r) == nil {
			out = append(out, &r)
		}
	}
	return out, nil
}

// State reads the updater's state.
func (u *Updater) State() (State, error) {
	var st State
	body, err := os.ReadFile(filepath.Join(u.Dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(body, &st)
}

func (u *Updater) saveState(st State) error {
	return writeJSON(filepath.Join(u.Dir, "state.json"), st)
}

// Begin records the version of the updater that runs and, on a machine whose updater runs for the
// first time, the version the server runs: crew-ctl install writes the first.
func (u *Updater) Begin(own, running string) error {
	st, err := u.State()
	if err != nil {
		return err
	}
	st.Updater = own
	if st.Running == "" {
		st.Running = running
	}
	// A request that ran when the updater stopped did not end: it is recorded as stopped.
	reqs, _ := u.Requests(0)
	for _, r := range reqs {
		if r.State == StateRunning {
			r.State, r.Error = StateStopped, "the updater stopped while it ran"
			if r.Step >= 5 && r.Action != ActionCheck && r.Action != ActionGate {
				r.Error = fmt.Sprintf("the updater stopped while it ran, at its step %d: %s, which ran before, is started again unless it runs", r.Step, orNone(st.Running))
				u.cutOff = append(u.cutOff, r)
			}
			t := u.now().UTC()
			r.EndedAt = &t
			_ = writeJSON(u.requestPath(r.ID), r)
		}
	}
	return u.saveState(st)
}

// Recover starts the version that ran before when a request was cut off after its switch began
// and no server is healthy: the old server was stopped, and maybe nothing started again (16.4).
func (u *Updater) Recover(ctx context.Context) error {
	cut := u.cutOff
	u.cutOff = nil
	if len(cut) == 0 {
		return nil
	}
	st, err := u.State()
	if err != nil || st.Running == "" {
		return err
	}
	// After a restart of the machine the server starts with the updater, and needs its time.
	if u.healthy(ctx, ServerContainer) == nil {
		return nil
	}
	// A request that had switched started its own version, with its schema applied: that one goes on.
	// One cut off before the switch was done leaves the version before.
	r := cut[len(cut)-1]
	v := st.Running
	for _, s := range r.Steps {
		if s.Step == 5 && s.OK {
			v = r.Version
		}
	}
	if err := u.Machine.StartServer(ctx, v); err != nil {
		return fmt.Errorf("starting %s again after a request was cut off: %w", v, err)
	}
	if v != st.Running {
		if err := u.saveState(State{Running: v, Previous: st.Running, Updater: st.Updater, Keep: st.Keep}); err != nil {
			return err
		}
	}
	if u.Log != nil {
		u.Log.Warn("a request was cut off after its switch began: a version is started again", "version", v)
	}
	return nil
}

// nextID is one more than the newest request's. Start runs one request at a time, so two cannot
// take the same number.
func (u *Updater) nextID() (int64, error) {
	reqs, err := u.Requests(1)
	if err != nil {
		return 0, err
	}
	if len(reqs) == 0 {
		return 1, nil
	}
	return reqs[0].ID + 1, nil
}

func (u *Updater) requestPath(id int64) string {
	return filepath.Join(u.Dir, "requests", fmt.Sprintf("%d.json", id))
}

func (u *Updater) save(req *Request) error {
	u.mu.Lock()
	c := u.copyOf(req)
	u.mu.Unlock()
	return writeJSON(u.requestPath(c.ID), c)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
