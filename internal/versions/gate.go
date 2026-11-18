package versions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
)

// The gate of a release Crew built (16.2 step 5, since 0.52). The server drives it, a round at a time
// on the releases' timer, from the gates table, so a restart takes it up where it was: make check in a
// sandbox of the kind build on the exported tree; then the updater's steps over its socket, which end
// in gate.json with the live part pending; then the live part, at once when the runtime image's
// inputs are the running release's, else after Run the live gate; and the live result to the updater
// as one request. A gate crew-ctl asked for on the machine is taken up at its live part.

// GateUpdater is the updater as the gate reaches it.
type GateUpdater interface {
	Gate(ctx context.Context, a updater.Ask) (*updater.Request, error)
	Live(ctx context.Context, a updater.LiveAsk) (*release.Gate, error)
}

// LiveSteps are a gate's live steps on the running server: the real ticket on crew-gate and the eval
// suites, in the candidate's runtime image (16.2 step 5). Step is asked every round: it starts what is
// not started, and returns the result once every step ended, nil until then.
type LiveSteps interface {
	Step(ctx context.Context, l LiveStart) (*updater.LiveAsk, error)
	// Abandon ends what of a gate's live steps is on its way when the gate leaves its live part
	// another way, and takes the gate's image, image, from crew-gate.
	Abandon(ctx context.Context, version, image, why string) error
}

// LiveStart is what the live steps run on.
type LiveStart struct {
	Version string
	// Image is the candidate's runtime image, by its id.
	Image string
	// Agents is whether the release changes what agents see, which the eval suites run for.
	Agents bool
	// Project is Crew's project and Commit the release's commit in it, whose eval files a release's
	// suite lays over the baseline (16.2 step 5, since 0.52).
	Project, Commit string
	// SameRuntime is whether the candidate's runtime image's inputs are the running release's, so its
	// code is: otherwise what its runs did and said is other code's.
	SameRuntime bool
}

// The states of a gate.
const (
	GateChecking = "checking"
	GateAsking   = "asking"
	GateAsked    = "asked"
	GateWaiting  = "waiting"
	GateLive     = "live"
	GateDone     = "done"
	GateFailed   = "failed"
)

// GateRow is a gate as the database has it, with the inputs of the runtime image that differ from the
// running release's, which Run the live gate shows first.
type GateRow struct {
	Version         string             `json:"version"`
	State           string             `json:"state"`
	Steps           []release.GateStep `json:"steps"`
	Log             string             `json:"log"`
	Error           string             `json:"error,omitempty"`
	SameRuntime     *bool              `json:"sameRuntime,omitempty"`
	InstallWhenDone bool               `json:"installWhenDone,omitempty"`
	PressedBy       string             `json:"pressedBy,omitempty"`
	PressedAt       *time.Time         `json:"pressedAt,omitempty"`
	Live            json.RawMessage    `json:"live,omitempty"`
	StartedAt       time.Time          `json:"startedAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
	Diff            []string           `json:"diff,omitempty"`
	// NoDiff is why Diff is empty on a gate whose runtime is not the running release's, so the press
	// cannot be offered: NoDiffUnread, release.NoDiffNoInputs, or release.NoDiffBadHash.
	NoDiff string `json:"noDiff,omitempty"`
	// LivePart is where the live part is, while it runs and after: nil before it began.
	LivePart *LivePart `json:"livePart,omitempty"`
}

// LivePart is where a gate's live part is, as Versions shows it (16.2 step 6, since 0.52): the real
// ticket's key and its stage on crew-gate while it runs, then its step; each project of the eval
// suites, and the suites' step once it ended.
type LivePart struct {
	Ticket     string      `json:"ticket,omitempty"`
	Stage      string      `json:"stage,omitempty"`
	Real       *LiveStep   `json:"real,omitempty"`
	Suites     []LiveSuite `json:"suites,omitempty"`
	SuitesStep *LiveStep   `json:"suitesStep,omitempty"`
}

// LiveStep is a live step that ended: the step, why it was skipped, and why it holds the release.
type LiveStep struct {
	release.GateStep
	Skipped string `json:"skipped,omitempty"`
	Hold    string `json:"hold,omitempty"`
}

// LiveSuite is one project of a gate's eval suites, by its name: its release suite's state, the kind
// of what its next agent run waits for, and its verdict once decided; or why it is not compared.
type LiveSuite struct {
	Project string `json:"project"`
	Suite   int64  `json:"suite,omitempty"`
	State   string `json:"state,omitempty"`
	WaitFor string `json:"waitFor,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Note    string `json:"note,omitempty"`
}

// askedFor is how long a gate waits for its request to show in the updater's status before it fails.
const askedFor = 2 * time.Hour

// ErrNotWaitingGate is Run the live gate for a release whose gate does not wait for it.
var ErrNotWaitingGate = errors.New("the release's gate does not wait for Run the live gate")

// Notes of a gate's install test, which a gate Crew drives does not run (16.11): it needs a virtual
// machine started on the machine, which runs only by crew-ctl gate, with the operator. The release
// says so, and is not held for it.
const (
	installTestOff   = "updates.install_test is off"
	installTestByCtl = "the install test runs by crew-ctl gate, with the operator: a gate Crew drives does not start a virtual machine"
)

// agentPaths are the paths whose change changes what agents see: a release that changes one runs the
// eval suites (16.2 step 5).
var agentPaths = []string{"baseline/", "harness/", "image/", "internal/cli/", "cmd/crew-run/"}

// ChangesWhatAgentsSee reports whether next changes a file under agentPaths from running.
func ChangesWhatAgentsSee(running, next release.Manifest) bool {
	under := func(p string) bool {
		for _, a := range agentPaths {
			if strings.HasPrefix(p, a) {
				return true
			}
		}
		return false
	}
	for p, h := range next.Files {
		if under(p) && running.Files[p] != h {
			return true
		}
	}
	for p := range running.Files {
		if _, ok := next.Files[p]; !ok && under(p) {
			return true
		}
	}
	return false
}

// GateRound is one round of the gates: it starts the gate of a build that is done, asks the updater
// for the steps of one whose make check ended, reads the end of the updater's request, takes up a
// pending gate it did not ask for, and steps the live part of each that runs it. project is Crew's
// project, installTest the setting updates.install_test.
func (b *Builder) GateRound(ctx context.Context, st updater.Status, project, installTest string) error {
	if b.Gates == nil {
		return nil
	}
	var errs []error
	if project != "" {
		errs = append(errs, b.startGate(ctx, project))
	}
	errs = append(errs, b.askGates(ctx, installTest), b.followGates(ctx, st), b.adoptPending(ctx), b.stepLive(ctx, project))
	return errors.Join(errs...)
}

// startGate starts the gate of the oldest build that is done and has none, one gate's make check at a
// time: its sandbox is one of the size build (since 0.60).
func (b *Builder) startGate(ctx context.Context, project string) error {
	var busy bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates WHERE state IN ('checking', 'asking', 'asked'))`).Scan(&busy); err != nil || busy {
		return err
	}
	var version, commit string
	err := b.Pool.QueryRow(ctx, `SELECT b.version, b.commit FROM builds b WHERE b.state = 'done'
		AND NOT EXISTS (SELECT 1 FROM gates g WHERE g.version = b.version) ORDER BY b.id LIMIT 1`).Scan(&version, &commit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := release.Read(filepath.Join(b.Releases, version))
	if err != nil {
		// The build's folder is gone, or never came: there is nothing to gate.
		_, err = b.Pool.Exec(ctx, `INSERT INTO gates (version, state, error) VALUES ($1, 'failed', $2) ON CONFLICT DO NOTHING`,
			version, "the release folder cannot be read: "+err.Error())
		return err
	}
	// A folder whose gate passed, or waits for its live part, was gated another way, by crew-ctl gate:
	// its row says so, and the next build is not held behind it (review of M16 P4b).
	switch {
	case f.Gate != nil:
		_, err := b.Pool.Exec(ctx, `INSERT INTO gates (version, state) VALUES ($1, 'done') ON CONFLICT DO NOTHING`, version)
		return err
	case f.Pending != nil:
		return b.adopt(ctx, f)
	}
	tag, err := b.Pool.Exec(ctx, `INSERT INTO gates (version, state) VALUES ($1, 'checking') ON CONFLICT DO NOTHING`, version)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.makeCheck(context.WithoutCancel(ctx), project, version, commit)
	}()
	return nil
}

// makeCheck runs make check on the commit in a sandbox of the kind build, and records its step. When
// the commit already passed Crew's merge or outside check run on Crew's own project, that check run's
// result is reused instead of running a duplicate make check sandbox.
func (b *Builder) makeCheck(ctx context.Context, project, version, commit string) {
	if b.passedCheckOnCommit(ctx, project, commit) {
		step := release.GateStep{Name: "make check at " + abbrev(commit, 12) + ", in a sandbox of its own", OK: true,
			Detail: "passed in the commit's check run on main"}
		steps, _ := json.Marshal([]release.GateStep{step})
		if _, uerr := b.Pool.Exec(ctx, `UPDATE gates SET state = 'asking', steps = $2, log = $3, updated_at = now() WHERE version = $1 AND state = 'checking'`,
			version, steps, "make check already passed on "+abbrev(commit, 12)+" in its check sandbox."); uerr != nil && b.Log != nil {
			b.Log.Error("recording a gate's make check", "version", version, "error", uerr)
		}
		return
	}
	out, err := b.runMakeCheck(ctx, project, version, commit)
	step := release.GateStep{Name: "make check at " + abbrev(commit, 12) + ", in a sandbox of its own", OK: err == nil}
	if err != nil {
		// One plain line of at most 120 characters; the log holds the rest.
		step.Detail = plain(err.Error())
	}
	steps, _ := json.Marshal([]release.GateStep{step})
	if _, uerr := b.Pool.Exec(ctx, `UPDATE gates SET state = 'asking', steps = $2, log = $3, updated_at = now() WHERE version = $1 AND state = 'checking'`,
		version, steps, storable(out)); uerr != nil && b.Log != nil {
		b.Log.Error("recording a gate's make check", "version", version, "error", uerr)
	}
}

func (b *Builder) passedCheckOnCommit(ctx context.Context, project, commit string) bool {
	var ok bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM check_runs c
		JOIN projects p ON p.id = c.project
		WHERE (p.name = $1 OR p.id::text = $1)
		  AND c.commit = $2
		  AND c.purpose IN ('merge', 'outside')
		  AND c.state = 'passed'
	)`, project, commit).Scan(&ok); err != nil {
		return false
	}
	return ok
}

func (b *Builder) runMakeCheck(ctx context.Context, project, version, commit string) (string, error) {
	var tree bytes.Buffer
	limit := b.MaxTree
	if limit <= 0 {
		limit = harness.MaxTree
	}
	if err := b.Git.Archive(ctx, project, commit, &capped{w: &tree, left: limit, limit: limit}); err != nil {
		return "", fmt.Errorf("exporting %s: %w", abbrev(commit, 12), err)
	}
	sizing, recorded, err := b.room(ctx, "gate:"+version, "the gate's make check of Crew "+version)
	if err != nil {
		return "", fmt.Errorf("room for the check's sandbox: %w", err)
	}
	defer recorded()
	res, err := b.Sandbox.MakeCheck(ctx, harness.BuildSpec{Version: version, Tree: tree.Bytes(), Timeout: b.Timeout, Sizing: sizing, Recorded: recorded})
	return res.Output, err
}

// askGates asks the updater for the steps of each gate whose make check ended, with the install test,
// which a gate Crew drives skips: where it is set up, the skip keeps the release from installing by
// itself.
func (b *Builder) askGates(ctx context.Context, installTest string) error {
	rows, err := b.Pool.Query(ctx, `SELECT version, steps FROM gates WHERE state = 'asking' ORDER BY started_at`)
	if err != nil {
		return err
	}
	type asking struct {
		version string
		steps   []release.GateStep
	}
	var list []asking
	for rows.Next() {
		var a asking
		if err := rows.Scan(&a.version, &a.steps); err != nil {
			rows.Close()
			return err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range list {
		f, err := release.Read(filepath.Join(b.Releases, a.version))
		if err != nil {
			if err := b.failGate(ctx, a.version, "the release folder cannot be read: "+err.Error()); err != nil {
				return err
			}
			continue
		}
		// The folder may be ahead of the row: an answer of the updater's that was lost (review of M16
		// P4b).
		if f.Pending != nil {
			if err := b.pending(ctx, f); err != nil {
				return err
			}
			continue
		}
		if f.Gate != nil {
			if _, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'done', updated_at = now() WHERE version = $1`, a.version); err != nil {
				return err
			}
			continue
		}
		g := &updater.GateAsk{ManifestSHA256: f.ManifestSHA256, Steps: a.steps, InstallTest: "skipped", InstallTestNote: installTestOff}
		if installTest != "off" {
			g.InstallTestNote = installTestByCtl
		}
		req, err := b.Gates.Gate(ctx, updater.Ask{Version: a.version, AskedBy: "server", Gate: g})
		var refused *updater.Refusal
		switch {
		case errors.Is(err, updater.ErrBusy):
			// The updater runs another request: the next round asks again.
			continue
		case errors.As(err, &refused):
			if err := b.failGate(ctx, a.version, "the updater refused the gate: "+refused.Reason); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'asked', request = $2, updated_at = now() WHERE version = $1`, a.version, req.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Builder) failGate(ctx context.Context, version, why string) error {
	_, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'failed', error = $2, updated_at = now() WHERE version = $1`, version, storable(why))
	return err
}

// followGates reads the end of each updater's request a gate waits for: the release's gate is
// pending, and its live part runs or waits, or the gate failed.
func (b *Builder) followGates(ctx context.Context, st updater.Status) error {
	rows, err := b.Pool.Query(ctx, `SELECT version, request, updated_at FROM gates WHERE state = 'asked'`)
	if err != nil {
		return err
	}
	ids, asked := map[string]int64{}, map[string]time.Time{}
	for rows.Next() {
		var v string
		var id *int64
		var at time.Time
		if err := rows.Scan(&v, &id, &at); err != nil {
			rows.Close()
			return err
		}
		ids[v], asked[v] = 0, at
		if id != nil {
			ids[v] = *id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for v, id := range ids {
		// The status was read before this round asked: a request is decided on only once the status
		// shows it ended, or its gate is pending already.
		var ended *updater.Request
		for _, r := range st.Requests {
			if r.ID == id && r.State != updater.StateRunning && r.State != updater.StateAsked && (st.Current == nil || st.Current.ID != id) {
				ended = r
			}
		}
		listed := ended != nil || (st.Current != nil && st.Current.ID == id)
		for _, r := range st.Requests {
			listed = listed || r.ID == id
		}
		f, err := release.Read(filepath.Join(b.Releases, v))
		switch {
		case err != nil:
			err = b.failGate(ctx, v, "the release folder cannot be read: "+err.Error())
		case f.Pending != nil:
			err = b.pending(ctx, f)
		case f.Gate != nil:
			_, err = b.Pool.Exec(ctx, `UPDATE gates SET state = 'done', updated_at = now() WHERE version = $1`, v)
		case ended == nil && !listed && asked[v].Before(b.now().Add(-askedFor)):
			// The request is in the status no more, and its gate never came: it is failed, so the
			// next build is not held behind it (review of M16 P4b).
			err = b.failGate(ctx, v, "the updater's gate request is no longer in its status, and it left no gate that waits")
		case ended == nil:
			continue
		case ended.Error != "":
			err = b.failGate(ctx, v, "the updater's gate failed: "+ended.Error)
		default:
			err = b.failGate(ctx, v, "the updater's gate ended with no gate.json that waits for its live steps")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// adoptPending takes up a release whose gate is pending and that has no gate row: crew-ctl gate asked
// the updater for it on the machine, and the live part is the running server's.
func (b *Builder) adoptPending(ctx context.Context) error {
	folders, _, err := release.List(b.Releases)
	if err != nil {
		return err
	}
	// Ending a failed gate needs the updater, adopting does not: one release's ending that fails holds
	// back no other's adoption (round 2 of the reviews of M16's P8).
	var errs []error
	for _, f := range folders {
		if f.Pending == nil {
			continue
		}
		if err := b.endFailed(ctx, f); err != nil {
			errs = append(errs, fmt.Errorf("ending the failed gate of %s: %w", f.Manifest.Version, err))
		}
		if err := b.adopt(ctx, f); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// endFailed ends a pending gate whose row failed after the updater wrote it: the server gave that gate
// up, so the updater writes its live part failed with the row's reason, and the release is no longer
// held pending and can be gated again. A gate the updater wrote after the row failed is another one,
// crew-ctl gate's again: its failed row goes, and adopt takes it up (review of M16, F2). A refusal is
// told in the log, and the next round asks again.
func (b *Builder) endFailed(ctx context.Context, f *release.Folder) error {
	v := f.Manifest.Version
	var why string
	var at time.Time
	err := b.Pool.QueryRow(ctx, `SELECT coalesce(error, ''), updated_at FROM gates WHERE version = $1 AND state = 'failed'`, v).Scan(&why, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.Pending.At.After(at) {
		_, err := b.Pool.Exec(ctx, `DELETE FROM gates WHERE version = $1 AND state = 'failed' AND updated_at = $2`, v, at)
		return err
	}
	if why == "" {
		why = "no reason was kept"
	}
	if len(why) > maxLiveDetail {
		why = strings.ToValidUTF8(why[:maxLiveDetail], "")
	}
	_, err = b.Gates.Live(ctx, updater.LiveAsk{Version: v, ManifestSHA256: f.ManifestSHA256, Live: release.LiveFailed,
		Steps: []release.GateStep{{Name: "The gate on Crew's server", OK: false, Detail: why}}})
	var refused *updater.Refusal
	switch {
	case err == nil, errors.Is(err, updater.ErrNoPendingGate):
		return nil
	case errors.As(err, &refused):
		if b.Log != nil {
			b.Log.Warn("ending a failed gate's pending gate.json", "version", v, "refused", refused.Reason)
		}
		return nil
	}
	return err
}

// maxLiveDetail is the most of a failed row's reason a live step's detail holds: the updater takes at
// most 2000 bytes.
const maxLiveDetail = 1500

// adopt makes the row of a pending gate it has none of, decided in the one statement.
func (b *Builder) adopt(ctx context.Context, f *release.Folder) error {
	state, same := b.decide(f)
	_, err := b.Pool.Exec(ctx, `INSERT INTO gates (version, state, same_runtime) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		f.Manifest.Version, state, same)
	return err
}

// decide is the live part's state for a pending gate: live when the runtime image's inputs are the
// running release's, else waiting for the press.
func (b *Builder) decide(f *release.Folder) (string, bool) {
	state, same, _ := b.decideErr(f)
	return state, same
}

// decideErr is decide, with the error of reading the running release: a gate already live is not
// decided again on a folder that could not be read (review of M16 P4c).
func (b *Builder) decideErr(f *release.Folder) (string, bool, error) {
	running, err := release.Read(filepath.Join(b.Releases, b.Running))
	if err != nil {
		return GateWaiting, false, err
	}
	if release.SameRuntime(running.Manifest, f.Manifest) {
		return GateLive, true, nil
	}
	return GateWaiting, false, nil
}

// pending decides the live part of a pending gate (16.2 step 5, 16.8): it runs at once when the
// runtime image's inputs are the running release's, since then only text differs and text is data;
// otherwise the operator's login waits for Run the live gate, which shows the diff first.
func (b *Builder) pending(ctx context.Context, f *release.Folder) error {
	state, same := b.decide(f)
	_, err := b.Pool.Exec(ctx, `UPDATE gates SET state = $2, same_runtime = $3, updated_at = now() WHERE version = $1`, f.Manifest.Version, state, same)
	return err
}

// NoDiffUnread is a gate's NoDiff when the running release or the candidate's folder cannot be read.
const NoDiffUnread = "unread"

// ErrStaleDiff is Run the live gate pressed on a diff that is not the release's: the screen showed
// another runtime image's inputs than the release has.
var ErrStaleDiff = errors.New("the release's runtime image is not the one whose diff was shown: read the diff again")

// ErrBadRuntimeHash is Run the live gate pressed on a release whose recorded hash of its runtime inputs
// is not their hash: the press would bind to another hash than that of the inputs it showed.
var ErrBadRuntimeHash = errors.New("the release's hash of its runtime inputs is not the hash of the inputs it records: it cannot be pressed")

// RunLiveGate is Run the live gate on Versions: a gate that waits for it runs its live part. runtime
// is the hash of the runtime image's inputs whose diff the screen showed, which must be the release's
// (security review of M16 P4b).
func (b *Builder) RunLiveGate(ctx context.Context, version, runtime, by string) error {
	f, err := release.Read(filepath.Join(b.Releases, version))
	if err != nil {
		return ErrNotWaitingGate
	}
	if runtime == "" || runtime != f.Manifest.RuntimeSHA256 {
		return ErrStaleDiff
	}
	if release.RuntimeSHA256(f.Manifest.Runtime) != f.Manifest.RuntimeSHA256 {
		return ErrBadRuntimeHash
	}
	tag, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'live', pressed_by = $2, pressed_at = now(), updated_at = now()
		WHERE version = $1 AND state = 'waiting'`, version, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotWaitingGate
	}
	return nil
}

// SetInstallWhenDone sets whether a release's gate installs the release automatically once it passes.
func (b *Builder) SetInstallWhenDone(ctx context.Context, version string, install bool) error {
	tag, err := b.Pool.Exec(ctx, `UPDATE gates SET install_when_done = $2, updated_at = now() WHERE version = $1`, version, install)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotWaitingGate
	}
	return nil
}

// stepLive steps the live part of the gates that run it, and sends a finished result to the updater.
// One real ticket runs at a time, since crew-gate names one image for its sandboxes: a gate whose
// ticket has not started waits while another's runs (review of M16 P4c).
func (b *Builder) stepLive(ctx context.Context, project string) error {
	if err := b.clearGateImage(ctx); err != nil {
		return err
	}
	if b.Live == nil {
		return nil
	}
	rows, err := b.Pool.Query(ctx, `SELECT version, ticket IS NOT NULL AND real_step IS NULL, pressed_at IS NOT NULL, coalesce(same_runtime, false) FROM gates
		WHERE state = 'live' ORDER BY (ticket IS NOT NULL AND real_step IS NULL) DESC, real_step IS NOT NULL DESC, updated_at`)
	if err != nil {
		return err
	}
	type liveGate struct {
		version                string
		running, pressed, same bool
	}
	gates, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (liveGate, error) {
		var g liveGate
		return g, r.Scan(&g.version, &g.running, &g.pressed, &g.same)
	})
	if err != nil {
		return err
	}
	busy := false
	for _, g := range gates {
		v := g.version
		f, err := release.Read(filepath.Join(b.Releases, v))
		img := ""
		if err == nil {
			img = f.Manifest.Images["crew-runtime"].ID
		}
		// abandon ends the gate's real ticket; when it cannot, the row stays as it is and the next
		// round tries again, so no ticket is left on its way with its key forgotten (review of P4c).
		abandon := func(why string) bool {
			if aerr := b.Live.Abandon(ctx, v, img, why); aerr != nil {
				if b.Log != nil {
					b.Log.Warn("ending a gate's real ticket", "version", v, "error", aerr)
				}
				return false
			}
			return true
		}
		switch {
		case err == nil && f.Gate != nil:
			// The result reached the updater, and the row did not hear of it (review of M16 P4b).
			if !abandon("its gate passed") {
				continue
			}
			if _, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'done', updated_at = now() WHERE version = $1`, v); err != nil {
				return err
			}
			continue
		case err != nil || f.Pending == nil:
			why := "its gate no longer waits for its live steps"
			if err != nil {
				why = "the release folder cannot be read: " + err.Error()
			}
			if !abandon(why) {
				continue
			}
			if err := b.failGate(ctx, v, why); err != nil {
				return err
			}
			continue
		}
		// A gate that runs at once because its inputs were the running release's is decided again
		// against the release that runs now; one the operator pressed is not. A ticket of it that is
		// on its way is ended: its image no longer has the running release's inputs (security review
		// of M16 P4b, P4c).
		if state, _, derr := b.decideErr(f); derr == nil && !g.pressed && state != GateLive {
			if !abandon("the release that runs no longer has its runtime image's inputs") {
				continue
			}
			// Its eval suites start again after the press too: the pipeline stops any that ran (16.2 step 5,
			// since 0.52).
			if _, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'waiting', same_runtime = false, ticket = NULL, live_started_at = NULL,
				limit_since = NULL, real_step = NULL, suites = NULL, suites_started_at = NULL, suites_limit_since = NULL, suites_limit_s = 0,
				suites_step = NULL, updated_at = now() WHERE version = $1 AND state = 'live'`, v); err != nil {
				return err
			}
			continue
		}
		// One real ticket at a time: a gate whose ticket has not started waits for the one that runs.
		var started, ended bool
		if err := b.Pool.QueryRow(ctx, `SELECT ticket IS NOT NULL, real_step IS NOT NULL FROM gates WHERE version = $1`, v).Scan(&started, &ended); err != nil {
			return err
		}
		if !started && busy {
			continue
		}
		res, err := b.Live.Step(ctx, LiveStart{Version: v, Image: img, Agents: b.agentsSee(f), Project: project, Commit: f.Manifest.Commit, SameRuntime: g.same})
		// The slot is the gate's only while its ticket is on its way: a gate whose step fails before
		// it added one holds nothing (review of M16 P4c).
		if !ended {
			var running bool
			if qerr := b.Pool.QueryRow(ctx, `SELECT ticket IS NOT NULL AND real_step IS NULL FROM gates WHERE version = $1`, v).Scan(&running); qerr == nil && running {
				busy = true
			}
		}
		if err != nil {
			if b.Log != nil {
				b.Log.Warn("a gate's live steps", "version", v, "error", err)
			}
			continue
		}
		if res == nil {
			continue
		}
		res.Version, res.ManifestSHA256 = v, f.ManifestSHA256
		if _, err := b.Gates.Live(ctx, *res); err != nil && !errors.Is(err, updater.ErrNoPendingGate) {
			var refused *updater.Refusal
			if errors.As(err, &refused) {
				if err := b.failGate(ctx, v, "the updater refused the live result: "+refused.Reason); err != nil {
					return err
				}
				continue
			}
			return err
		}
		body, _ := json.Marshal(res)
		if _, err := b.Pool.Exec(ctx, `UPDATE gates SET state = 'done', live = $2, updated_at = now() WHERE version = $1`, v, body); err != nil {
			return err
		}
		busy = false
	}
	return nil
}

// agentsSee is whether the release changes what agents see against the running release; with no
// running release to compare, it does.
func (b *Builder) agentsSee(f *release.Folder) bool {
	running, err := release.Read(filepath.Join(b.Releases, b.Running))
	return err != nil || ChangesWhatAgentsSee(running.Manifest, f.Manifest)
}

// RecoverGates takes up the gates a stop of the server cut off: a make check that ran is run again.
func (b *Builder) RecoverGates(ctx context.Context) error {
	if _, err := b.Pool.Exec(ctx, `DELETE FROM gates WHERE state = 'checking'`); err != nil {
		return err
	}
	return b.clearGateImage(ctx)
}

// clearGateImage takes the candidate's runtime image from crew-gate when no gate runs its live part:
// the image is a gate's only while it runs, whichever way it ended (16.2 step 5).
func (b *Builder) clearGateImage(ctx context.Context) error {
	_, err := b.Pool.Exec(ctx, `UPDATE projects SET runtime_image = NULL WHERE runtime_image IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM gates WHERE state = 'live')`)
	return err
}

// GetGate is a version's gate, or nil, with the runtime inputs that differ from the running release's.
func (b *Builder) GetGate(ctx context.Context, version string) (*GateRow, error) {
	var g GateRow
	var why, by *string
	var live, real, suites, suitesStep []byte
	var ticket string
	err := b.Pool.QueryRow(ctx, `SELECT version, state, steps, log, error, same_runtime, install_when_done, pressed_by, pressed_at, live, started_at, updated_at,
			coalesce(ticket, ''), real_step, suites, suites_step
		FROM gates WHERE version = $1`, version).Scan(&g.Version, &g.State, &g.Steps, &g.Log, &why, &g.SameRuntime, &g.InstallWhenDone, &by, &g.PressedAt, &live,
		&g.StartedAt, &g.UpdatedAt, &ticket, &real, &suites, &suitesStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if g.LivePart, err = b.livePart(ctx, ticket, real, suites, suitesStep); err != nil {
		return nil, err
	}
	if why != nil {
		g.Error = *why
	}
	if by != nil {
		g.PressedBy = *by
	}
	if g.Live, err = unpricedLive(live); err != nil {
		return nil, err
	}
	g.NoDiff = NoDiffUnread
	if running, err := release.Read(filepath.Join(b.Releases, b.Running)); err == nil {
		if f, err := release.Read(filepath.Join(b.Releases, version)); err == nil {
			g.Diff, g.NoDiff = release.RuntimeDiff(running.Manifest, f.Manifest), release.NoDiff(running.Manifest, f.Manifest)
			if g.NoDiff != "" {
				g.Diff = nil
			}
		}
	}
	return &g, nil
}

// livePart is where a gate's live part is, from its row's columns: the real ticket's stage while it
// runs, read from crew-gate; each project's release suite's state, wait, and verdict. nil before the
// live part began.
func (b *Builder) livePart(ctx context.Context, ticket string, real, suites, suitesStep []byte) (*LivePart, error) {
	if ticket == "" && real == nil && suites == nil && suitesStep == nil {
		return nil, nil
	}
	lp := &LivePart{Ticket: ticket}
	var err error
	if lp.Real, err = liveStep(real); err != nil {
		return nil, err
	}
	if lp.SuitesStep, err = liveStep(suitesStep); err != nil {
		return nil, err
	}
	if ticket != "" && lp.Real == nil {
		// A key the gate recorded before its ticket was added has no stage yet.
		err := b.Pool.QueryRow(ctx, `SELECT t.stage FROM tickets t JOIN projects p ON p.id = t.project
			WHERE p.name = $1 AND p.key || '-' || t.number = $2 AND t.created_by = 'gate'`, board.GateProject, ticket).Scan(&lp.Stage)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if suites == nil {
		return lp, nil
	}
	var entries []suiteEntry
	if err := json.Unmarshal(suites, &entries); err != nil {
		return nil, err
	}
	lp.Suites = []LiveSuite{}
	for _, e := range entries {
		s := LiveSuite{Project: e.Name, Suite: e.Suite, Note: e.Note}
		if e.Suite != 0 {
			var waitFor, verdict *string
			err := b.Pool.QueryRow(ctx, `SELECT state, wait_for, result->'decision'->>'verdict' FROM eval_suites WHERE id = $1`, e.Suite).
				Scan(&s.State, &waitFor, &verdict)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if waitFor != nil {
				s.WaitFor = *waitFor
			}
			if verdict != nil {
				s.Verdict = *verdict
			}
		}
		lp.Suites = append(lp.Suites, s)
	}
	return lp, nil
}

// liveStep reads a live step as the gate's row keeps it, realResult's or suitesResult's shape; nil
// when it has not ended.
func liveStep(body []byte) (*LiveStep, error) {
	if body == nil {
		return nil, nil
	}
	var s suitesResult
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	s.Step.Detail = release.Unpriced(s.Step.Detail)
	return &LiveStep{GateStep: s.Step, Skipped: release.Unpriced(s.Skipped), Hold: release.Unpriced(s.Hold)}, nil
}

// unpricedLive is a gate's live result as its row keeps it, without what a server before 0.53 wrote
// of its cost: its costUsd and the amounts in its lines (release.Unpriced). A body that is not such a
// result is not shown.
func unpricedLive(body []byte) (json.RawMessage, error) {
	if body == nil {
		return nil, nil
	}
	var l updater.LiveAsk
	if err := json.Unmarshal(body, &l); err != nil {
		return nil, err
	}
	for i := range l.Steps {
		l.Steps[i].Detail = release.Unpriced(l.Steps[i].Detail)
	}
	for i := range l.Skipped {
		l.Skipped[i] = release.Unpriced(l.Skipped[i])
	}
	l.Hold = release.Unpriced(l.Hold)
	return json.Marshal(l)
}

// installTestSetting is updates.install_test of the settings, local unless it is off; YAML reads an
// unquoted off as false.
func installTestSetting(d settings.Doc) string {
	u, _ := d["updates"].(settings.Doc)
	switch v := u["install_test"].(type) {
	case string:
		if v == "off" {
			return "off"
		}
	case bool:
		if !v {
			return "off"
		}
	}
	return "local"
}
