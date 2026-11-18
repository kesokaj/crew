package versions_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// machine is an updater's machine on which every step works, except what fail names.
type machine struct {
	mu   sync.Mutex
	fail map[string]bool
	// started are the versions StartCheck started.
	started []string
}

func (m *machine) err(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail[name] {
		return errors.New(name + " failed")
	}
	return nil
}
func (m *machine) Load(context.Context, *release.Folder) error { return m.err("Load") }
func (m *machine) Snapshot(_ context.Context, _, file string) error {
	return os.WriteFile(file, []byte("s"), 0o600)
}
func (m *machine) Scratch(context.Context) error                                 { return nil }
func (m *machine) DropScratch(context.Context) error                             { return nil }
func (m *machine) RestoreScratch(context.Context, *release.Folder, string) error { return nil }
func (m *machine) StartCheck(_ context.Context, f *release.Folder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = append(m.started, f.Manifest.Version)
	return nil
}
func (m *machine) Smoke(_ context.Context, f *release.Folder) error { return m.err("Smoke") }
func (m *machine) Selftest(context.Context) error                   { return m.err("Selftest") }
func (m *machine) Browser(context.Context, *release.Folder) error   { return m.err("Browser") }
func (m *machine) StopCheck(context.Context) error                  { return nil }
func (m *machine) Health(context.Context, string) error             { return nil }
func (m *machine) Proof(_ context.Context, c string) error          { return m.err("Proof " + c) }
func (m *machine) StopServer(context.Context) error                 { return nil }
func (m *machine) StartServer(context.Context, string) error        { return nil }
func (m *machine) Migrate(context.Context, string) error            { return m.err("Migrate") }
func (m *machine) Restore(context.Context, string, string) error    { return nil }
func (m *machine) Remove(context.Context, string) error             { return nil }

type replacer struct{ to []string }

func (r *replacer) ReplaceUpdater(_ context.Context, v string) error {
	r.to = append(r.to, v)
	return nil
}

type kit struct {
	t   *testing.T
	d   *dbtest.DB
	s   *versions.Service
	u   *updater.Updater
	m   *machine
	r   *replacer
	now time.Time
	rel string
}

var base = map[string]string{
	"baseline/stages/build.md": "a", "harness/skills/x/SKILL.md": "b", "internal/board/board.go": "c", "deploy/crew-ctl": "d",
}

// newKit is a server on running, an updater on the machine, and the release folders a test adds.
func newKit(t *testing.T, running string) *kit {
	t.Helper()
	d := dbtest.New(t, false)
	root := t.TempDir()
	k := &kit{t: t, d: d, m: &machine{fail: map[string]bool{}}, r: &replacer{}, rel: filepath.Join(root, "releases"),
		now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	k.u = &updater.Updater{Machine: k.m, Releases: k.rel, Dir: filepath.Join(root, "updater"),
		MachineSetup: func() (int, error) { return 2, nil },
		FreeSpace:    func(string) (uint64, error) { return 1 << 40, nil },
		WatchFor:     20 * time.Millisecond, WatchEvery: 5 * time.Millisecond, HealthFor: 10 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := k.u.Begin("0.1.0", running); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(k.u.Handler())
	t.Cleanup(srv.Close)
	k.s = &versions.Service{Pool: d.Server, Releases: k.rel, Running: running,
		Updater:  &updater.Client{Base: srv.URL, HTTP: srv.Client()},
		Replacer: k.r,
		Settings: func(ctx context.Context) (settings.Doc, error) {
			return settings.Effective(ctx, d.Server, settings.Machine{Version: running, AdminEmail: "a@example.com", SourceRepo: "Op/crew"})
		},
		Now: func() time.Time { return k.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	k.release(running, nil, true)
	return k
}

// release writes a release folder whose files are base with change laid over it.
func (k *kit) release(version string, change map[string]string, gated bool) {
	k.t.Helper()
	files := map[string]string{}
	for p, h := range base {
		files[p] = h
	}
	for p, h := range change {
		files[p] = h
	}
	dir := filepath.Join(k.rel, version)
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		k.t.Fatal(err)
	}
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		if err := os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644); err != nil {
			k.t.Fatal(err)
		}
		imgs[n] = release.Image{Tag: n + ":" + version, ID: "sha256:" + n}
	}
	imgs["postgres"] = release.Image{Tag: "postgres:18.6-trixie", ID: "sha256:postgres"}
	if _, err := release.Write(dir, release.Facts{Version: version, Commit: "c" + version, BuiltAt: k.now, Schema: 51, MachineSetup: 2, Images: imgs}, files, nil); err != nil {
		k.t.Fatal(err)
	}
	if gated {
		k.gate(version)
	}
}

func (k *kit) gate(version string) {
	k.t.Helper()
	f, err := release.Read(filepath.Join(k.rel, version))
	if err != nil {
		k.t.Fatal(err)
	}
	if err := f.WriteGate(release.Gate{Passed: true, At: k.now, InstallTest: "skipped"}); err != nil {
		k.t.Fatal(err)
	}
}

// pass is one round of the server's, after the updater's request ended.
func (k *kit) pass() {
	k.t.Helper()
	k.u.Wait()
	if err := k.s.Pass(context.Background()); err != nil {
		k.t.Fatal(err)
	}
	k.u.Wait()
}

type rel struct {
	State, Kind  string
	Held         bool
	HeldWhy      *string
	InstallAfter *time.Time
	ReplacedBy   *string
	Failure      *string
}

func (k *kit) get(v string) rel {
	k.t.Helper()
	var r rel
	err := k.d.Server.QueryRow(context.Background(), `SELECT state, kind, held, held_why, install_after, replaced_by, failure FROM releases WHERE version = $1`, v).
		Scan(&r.State, &r.Kind, &r.Held, &r.HeldWhy, &r.InstallAfter, &r.ReplacedBy, &r.Failure)
	if err != nil {
		k.t.Fatalf("release %s: %v", v, err)
	}
	return r
}

func (k *kit) running() string {
	st, _ := k.u.State()
	return st.Running
}

// A release of text gets its notice and installs by itself 24 hours later; a later release replaces
// one that waits (16.3).
func TestAReleaseOfTextInstallsByItselfAfterItsNotice(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	r := k.get("0.2.0")
	if r.State != "waiting" || r.Kind != release.KindText || r.InstallAfter == nil || !r.InstallAfter.Equal(k.now.Add(24*time.Hour)) {
		t.Fatalf("%+v", r)
	}
	if k.get("0.1.0").State != "running" {
		t.Fatal("the running version is not marked")
	}
	// 23 hours later nothing happened.
	k.now = k.now.Add(23 * time.Hour)
	k.pass()
	if k.running() != "0.1.0" {
		t.Fatal("installed before its notice passed")
	}
	k.now = k.now.Add(time.Hour)
	k.pass()
	k.u.Wait()
	// The new server runs, and records it.
	k.s.Running = "0.2.0"
	k.pass()
	if k.running() != "0.2.0" || k.get("0.2.0").State != "running" {
		t.Fatalf("not installed after its notice: %s, %+v", k.running(), k.get("0.2.0"))
	}
	var asked string
	_ = k.d.Server.QueryRow(context.Background(), `SELECT asked_by FROM update_requests ORDER BY id DESC LIMIT 1`).Scan(&asked)
	if asked != versions.ByClock {
		t.Fatalf("asked by %q", asked)
	}
	var news string
	_ = k.d.Server.QueryRow(context.Background(), `SELECT after->>'news' FROM changes WHERE tbl = 'releases' AND row_key = '0.2.0' ORDER BY seq DESC LIMIT 1`).Scan(&news)
	if news != "Crew is now on 0.2.0" {
		t.Fatalf("news %q", news)
	}
}

// On another installation, which names no repository it builds from, a release of text waits for
// Install too: that machine holds no source to tell text from code by (16.12).
func TestOnAnotherInstallationEveryReleaseWaitsForInstall(t *testing.T) {
	for _, repo := range []any{nil, "", "<owner>/crew"} {
		k := newKit(t, "0.1.0")
		effective := k.s.Settings
		k.s.Settings = func(ctx context.Context) (settings.Doc, error) {
			d, err := effective(ctx)
			if err == nil {
				d["updates"].(settings.Doc)["source_repo"] = repo
			}
			return d, err
		}
		k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
		k.pass()
		if r := k.get("0.2.0"); r.State != "waiting" || r.Kind != release.KindText || r.InstallAfter != nil {
			t.Fatalf("%v: %+v", repo, r)
		}
		k.now = k.now.Add(30 * 24 * time.Hour)
		k.pass()
		if k.running() != "0.1.0" {
			t.Fatalf("%v: text installed by itself on another installation", repo)
		}
	}
}

// Row 67: a release that changes code waits for Install, whatever time passes; Install installs it.
func TestAReleaseThatChangesCodeWaitsForInstall(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"internal/board/board.go": "new", "baseline/stages/build.md": "new"}, true)
	k.pass()
	if r := k.get("0.2.0"); r.State != "waiting" || r.Kind != release.KindCode || r.InstallAfter != nil {
		t.Fatalf("%+v", r)
	}
	k.now = k.now.Add(30 * 24 * time.Hour)
	k.pass()
	if k.running() != "0.1.0" {
		t.Fatal("code installed by itself")
	}
	if _, err := k.s.Install(context.Background(), "0.2.0", ""); err != nil {
		t.Fatal(err)
	}
	k.u.Wait()
	k.s.Running = "0.2.0"
	k.pass()
	if k.running() != "0.2.0" || k.get("0.2.0").State != "running" {
		t.Fatalf("Install: %s %+v", k.running(), k.get("0.2.0"))
	}
}

// Hold keeps a release of text until Install; lifting the hold starts no clock again. A later
// release replaces a held one.
func TestHoldAndALaterReleaseReplacesAHeldOne(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"harness/skills/x/SKILL.md": "new"}, true)
	k.pass()
	if err := k.s.Hold(context.Background(), "0.2.0", true); err != nil {
		t.Fatal(err)
	}
	k.now = k.now.Add(48 * time.Hour)
	k.pass()
	if k.running() != "0.1.0" || !k.get("0.2.0").Held {
		t.Fatal("a held release installed")
	}
	if err := k.s.Hold(context.Background(), "0.2.0", false); err != nil {
		t.Fatal(err)
	}
	k.pass()
	if k.running() != "0.1.0" || k.get("0.2.0").InstallAfter != nil {
		t.Fatal("lifting the hold started the clock again")
	}
	if err := k.s.Hold(context.Background(), "0.2.0", true); err != nil {
		t.Fatal(err)
	}
	k.release("0.3.0", map[string]string{"harness/skills/x/SKILL.md": "newer"}, true)
	k.pass()
	if r := k.get("0.2.0"); r.ReplacedBy == nil || *r.ReplacedBy != "0.3.0" || r.State != "kept" {
		t.Fatalf("0.2.0: %+v", r)
	}
	if r := k.get("0.3.0"); r.State != "waiting" || r.Held {
		t.Fatalf("0.3.0: %+v", r)
	}
	if err := k.s.Hold(context.Background(), "0.1.0", true); !errors.Is(err, versions.ErrNotWaiting) {
		t.Fatalf("holding the running version: %v", err)
	}
}

// Row 70: a release that installs by itself and fails its check replaced nothing; it is held, and
// the reason is kept for the inbox.
func TestAReleaseThatInstallsByItselfAndFailsItsCheckIsHeld(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.m.fail["Smoke"] = true
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	k.now = k.now.Add(25 * time.Hour)
	k.pass()
	k.pass()
	r := k.get("0.2.0")
	if k.running() != "0.1.0" || r.State != "failed" || !r.Held || r.HeldWhy == nil || !strings.Contains(*r.HeldWhy, "the check stopped it") ||
		r.Failure == nil || !strings.Contains(*r.Failure, "smoke run") {
		t.Fatalf("%s %+v", k.running(), r)
	}
	// The clock does not try it again.
	k.now = k.now.Add(25 * time.Hour)
	k.pass()
	var n int
	_ = k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM update_requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d requests", n)
	}
}

// Pause all holds the clock too.
func TestPauseAllHoldsTheClock(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	if err := pgx.BeginFunc(context.Background(), k.d.Server, func(tx pgx.Tx) error {
		_, err := hold.Pause(context.Background(), tx, "pause", "operator")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	k.now = k.now.Add(48 * time.Hour)
	k.pass()
	if k.running() != "0.1.0" {
		t.Fatal("installed while Crew was paused")
	}
}

// A folder found before its gate passed gets its notice when the gate is there; one without a gate
// gets none.
func TestTheNoticeWaitsForTheGate(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, false)
	k.pass()
	if r := k.get("0.2.0"); r.State != "kept" || r.InstallAfter != nil {
		t.Fatalf("before the gate: %+v", r)
	}
	k.gate("0.2.0")
	k.pass()
	if r := k.get("0.2.0"); r.State != "waiting" || r.InstallAfter == nil {
		t.Fatalf("after the gate: %+v", r)
	}
	// A folder that is gone is removed.
	if err := os.RemoveAll(filepath.Join(k.rel, "0.2.0")); err != nil {
		t.Fatal(err)
	}
	k.pass()
	if k.get("0.2.0").State != "removed" {
		t.Fatal("a removed folder is still listed")
	}
}

// A release that needs a newer machine setup waits, as code does, with the kind setup.
func TestAReleaseThatNeedsANewerSetupWaits(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	body, _ := os.ReadFile(filepath.Join(k.rel, "0.2.0", "release.json"))
	_ = os.WriteFile(filepath.Join(k.rel, "0.2.0", "release.json"), []byte(strings.Replace(string(body), `"machineSetup": 2`, `"machineSetup": 3`, 1)), 0o644)
	k.gate("0.2.0")
	k.pass()
	if r := k.get("0.2.0"); r.Kind != release.KindSetup || r.InstallAfter != nil {
		t.Fatalf("%+v", r)
	}
}

// The server installs the updater of its own version once it runs on it: the updater never itself
// (16.8). Once.
func TestTheServerInstallsTheNewUpdater(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"internal/board/board.go": "new"}, true)
	k.pass()
	if _, err := k.s.Install(context.Background(), "0.2.0", ""); err != nil {
		t.Fatal(err)
	}
	k.pass()
	if len(k.r.to) != 0 {
		t.Fatal("the old server replaced the updater")
	}
	// The new server, on 0.2.0, with the updater of 0.1.0.
	k.s.Running = "0.2.0"
	k.pass()
	k.pass()
	if len(k.r.to) != 1 || k.r.to[0] != "0.2.0" {
		t.Fatalf("replaced %v", k.r.to)
	}
	var tries int
	_ = k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM changes WHERE tbl = 'updater'`).Scan(&tries)
	if tries != 1 {
		t.Fatalf("%d", tries)
	}
	_ = db.RoleServer
}

// The Versions screen: the running version, the next release and whether it waits, the releases
// newest first with Roll back, and the updater's requests with their steps.
func TestTheVersionsScreen(t *testing.T) {
	k := newKit(t, "0.2.0")
	k.release("0.1.0", nil, true)
	_ = os.WriteFile(filepath.Join(k.rel, "0.2.0", "notes.md"), []byte("# Crew 0.2.0\n\nThe commits:\n\n- Builders run the checks first (abc)\n"), 0o644)
	k.release("0.3.0", map[string]string{"internal/board/board.go": "new"}, true)
	k.pass()
	sc, err := k.s.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sc.Running == nil || sc.Running.Version != "0.2.0" || sc.Running.Title != "Builders run the checks first (abc)" || sc.Running.Images["crew-server"] != "crew-server:0.2.0" ||
		sc.Running.InstalledAt == nil {
		t.Fatalf("running %+v", sc.Running)
	}
	if sc.Next == nil || sc.Next.Version != "0.3.0" || sc.Next.Kind != release.KindCode || sc.Next.InstallAfter != nil {
		t.Fatalf("next %+v", sc.Next)
	}
	var order []string
	for _, r := range sc.Releases {
		order = append(order, r.Version)
		if r.Version == "0.1.0" && !r.RollBack {
			t.Fatal("0.1.0 cannot be rolled back to")
		}
		if r.Version != "0.1.0" && r.RollBack {
			t.Fatalf("%s offers a roll back", r.Version)
		}
	}
	if strings.Join(order, " ") != "0.3.0 0.2.0 0.1.0" || !sc.Updater.Answers || sc.Updater.Running != "0.2.0" {
		t.Fatalf("%v %+v", order, sc.Updater)
	}
	// A roll back to 0.1.0, and the screen has its request with every step.
	if _, err := k.s.Install(context.Background(), "0.1.0", ""); err != nil {
		t.Fatal(err)
	}
	k.pass()
	sc, _ = k.s.View(context.Background())
	if len(sc.Requests) != 1 || sc.Requests[0].Version != "0.1.0" || sc.Requests[0].State != "done" || len(sc.Requests[0].Steps) != 7 {
		t.Fatalf("requests %+v", sc.Requests)
	}
}

// The Versions screen says whether a release that passed its gate is copied to GitHub (16.9, since
// 0.53): only with updates.publish github and a GitHub and a registry to copy to; with publish off
// nothing is copied, and the screen must not say a release is being copied.
func TestTheVersionsScreenSaysWhetherReleasesAreCopied(t *testing.T) {
	k := newKit(t, "0.2.0")
	ctx := context.Background()
	publish := "github"
	effective := k.s.Settings
	k.s.Settings = func(ctx context.Context) (settings.Doc, error) {
		d, err := effective(ctx)
		if err == nil {
			d["updates"].(settings.Doc)["publish"] = publish
		}
		return d, err
	}
	copies := func(want bool) {
		t.Helper()
		sc, err := k.s.View(ctx)
		if err != nil || sc.Publish != want {
			t.Fatalf("publish %v, not %v: %v", sc.Publish, want, err)
		}
	}
	// No GitHub or registry to copy to: nothing is copied, whatever the setting says.
	copies(false)
	k.s.GitHub, k.s.Registry = &fakeGitHub{tags: map[string]string{}, pages: map[string]string{}}, &fakeRegistry{}
	copies(true)
	publish = "off"
	copies(false)
}

// The Versions screen names its candidate, whose gate it shows (16.2 step 6, since 0.52): a gate whose
// live part runs, whatever its version; else the newest, by when it started, of the builds and the
// gates whose version is newer than the running release's and was never installed. A failed build
// stays until a newer one starts; a gate crew-ctl gate made, which has no build, counts; one
// installed once, or rolled back from, does not.
func TestTheVersionsScreenNamesItsCandidate(t *testing.T) {
	k := newKit(t, "0.2.0")
	ctx := context.Background()
	candidate := func(want string) {
		t.Helper()
		sc, err := k.s.View(ctx)
		if err != nil || sc.Candidate != want {
			t.Fatalf("the candidate %q, not %q: %v", sc.Candidate, want, err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := k.d.Server.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	build := func(version, state string, ago time.Duration) {
		t.Helper()
		exec(`INSERT INTO builds (version, commit, asked_by, state, started_at) VALUES ($1, $2, 'operator', $3, now() - $4::interval)`,
			version, strings.Repeat("c", 40), state, fmt.Sprintf("%d seconds", int(ago.Seconds())))
	}
	candidate("")
	build("0.1.9", "done", 5*time.Hour)
	build("0.2.0", "done", 4*time.Hour)
	candidate("")
	build("0.3.0", "failed", 3*time.Hour)
	candidate("0.3.0")
	exec(`INSERT INTO gates (version, state, started_at) VALUES ('0.3.1', 'waiting', now() - interval '1 hour')`)
	candidate("0.3.1")
	// 0.3.2 was installed and rolled back from: it is no candidate.
	build("0.3.2", "done", 30*time.Minute)
	exec(`INSERT INTO releases (version, commit, manifest_sha256, manifest, built_at, kind, state, installed_at)
		VALUES ('0.3.2', 'c', 'h', '{"schema": 51}', now(), 'code', 'kept', now() - interval '10 minutes')`)
	candidate("0.3.1")
	build("0.3.3", "running", 10*time.Minute)
	candidate("0.3.3")
	// By when it started, not by its number.
	build("0.2.5", "done", time.Minute)
	candidate("0.2.5")
	// A gate whose live part runs holds the agent CLI's login: it stays the candidate while it runs,
	// though newer builds started since, and the newest such gate first (security review of M16 P5).
	exec(`INSERT INTO gates (version, state, started_at) VALUES ('0.2.4', 'live', now() - interval '6 hours')`)
	candidate("0.2.4")
	exec(`INSERT INTO gates (version, state, started_at) VALUES ('0.2.3', 'live', now() - interval '7 hours')`)
	candidate("0.2.4")
	exec(`UPDATE gates SET state = 'done' WHERE version = '0.2.4'`)
	candidate("0.2.3")
	exec(`UPDATE gates SET state = 'done' WHERE version = '0.2.3'`)
	candidate("0.2.5")
	// A live gate the operator pressed is not decided again, so it can run on while a newer release is
	// installed: it holds the login all the same, and stays on screen (security review of M16 P5, round 2).
	exec(`INSERT INTO gates (version, state, started_at) VALUES ('0.1.8', 'live', now() - interval '8 hours')`)
	candidate("0.1.8")
}

// The inbox says what a release waits for, and what a release that was not installed did (16.3).
func TestTheInboxSaysWhatAReleaseWaitsFor(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	items := k.inbox()
	if len(items) != 1 || items[0].Kind != inbox.KindRelease || !strings.Contains(items[0].Body, "installs by itself at 2026-10-19 12:00 UTC") ||
		labels(items[0]) != "See the release|Hold|Install now" {
		t.Fatalf("%+v", items)
	}
	k.release("0.3.0", map[string]string{"internal/board/board.go": "new"}, true)
	k.pass()
	items = k.inbox()
	if len(items) != 1 || items[0].ID != "release/0.3.0" || !strings.Contains(items[0].Body, "waits until you press Install") || labels(items[0]) != "See the release|Install" {
		t.Fatalf("%+v", items)
	}
	k.m.fail["Load"] = true
	if _, err := k.s.Install(context.Background(), "0.3.0", ""); err != nil {
		t.Fatal(err)
	}
	k.pass()
	items = k.inbox()
	if len(items) != 1 || items[0].Kind != inbox.KindReleaseFailed || !strings.Contains(items[0].Body, "Nothing was replaced") ||
		!strings.Contains(items[0].Body, "Load failed") {
		t.Fatalf("%+v", items)
	}
}

func (k *kit) inbox() []inbox.Item {
	k.t.Helper()
	all, err := inbox.List(context.Background(), k.d.Server, settings.Doc{})
	if err != nil {
		k.t.Fatal(err)
	}
	var out []inbox.Item
	for _, it := range all {
		if strings.HasPrefix(it.Kind, "release") {
			out = append(out, it)
		}
	}
	return out
}

func labels(it inbox.Item) string {
	var l []string
	for _, a := range it.Alternatives {
		l = append(l, a.Label)
	}
	return strings.Join(l, "|")
}

// No agent can shorten the wait, lift a hold, or install (16.3): the Crew MCP has no tool that
// reaches a release or the updater.
func TestNoToolOfTheCrewMCPReachesAReleaseOrTheUpdater(t *testing.T) {
	tools, err := baseline.Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 40 {
		t.Fatalf("%d tools read", len(tools))
	}
	for name := range tools {
		for _, word := range []string{"release", "version", "install", "updater", "hold", "deploy", "rollback"} {
			if strings.Contains(strings.ToLower(name), word) {
				t.Errorf("the tool %s reaches releases", name)
			}
		}
	}
}

type fakeGitHub struct {
	fail  error
	tags  map[string]string
	pages map[string]string
	body  string
}

func (g *fakeGitHub) Tag(_ context.Context, repo, tag, sha string) error {
	if g.fail != nil {
		return g.fail
	}
	g.tags[repo+" "+tag] = sha
	return nil
}
func (g *fakeGitHub) Release(_ context.Context, repo, tag, name, body string) (string, error) {
	g.pages[tag], g.body = "https://github.com/"+repo+"/releases/tag/"+tag, body
	return g.pages[tag], nil
}
func (g *fakeGitHub) RegistryLogin(context.Context) (string, string, error) {
	return "op", "gho_made_up", nil
}

type fakeRegistry struct{ pushed []string }

func (r *fakeRegistry) Push(_ context.Context, local, remote, user, token string) error {
	r.pushed = append(r.pushed, local+" -> "+remote+" as "+user)
	return nil
}

// A release that passed the gate is copied to GitHub: its tag at its commit, a page with its notes
// and the gate, and its images in the registry (16.9). One that cannot be copied is tried again
// later, and installs all the same.
func TestAReleaseThatPassedTheGateIsCopiedToGitHub(t *testing.T) {
	k := newKit(t, "0.1.0")
	gh := &fakeGitHub{tags: map[string]string{}, pages: map[string]string{}, fail: errors.New("GitHub cannot be reached")}
	reg := &fakeRegistry{}
	k.s.GitHub, k.s.Registry = gh, reg
	effective := k.s.Settings
	k.s.Settings = func(ctx context.Context) (settings.Doc, error) {
		d, err := effective(ctx)
		if err == nil {
			d["updates"].(settings.Doc)["source_repo"] = "Op/crew"
		}
		return d, err
	}
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	_ = os.WriteFile(filepath.Join(k.rel, "0.2.0", "notes.md"), []byte("# Crew 0.2.0\n\n- A prompt\n- See [this](https://evil.example)\n"), 0o644)
	k.pass()
	var errText *string
	var next *time.Time
	_ = k.d.Server.QueryRow(context.Background(), `SELECT publish_error, publish_next FROM releases WHERE version = '0.2.0'`).Scan(&errText, &next)
	if errText == nil || !strings.Contains(*errText, "the tag: GitHub cannot be reached") || next == nil || !next.After(k.now) {
		t.Fatalf("%v %v", errText, next)
	}
	// Not before its next try; then all of it.
	gh.fail = nil
	k.pass()
	if len(gh.tags) != 0 {
		t.Fatal("tried again before its time")
	}
	k.now = k.now.Add(time.Hour)
	k.pass()
	if gh.tags["Op/crew v0.2.0"] != "c0.2.0" || gh.pages["v0.2.0"] == "" || !strings.Contains(gh.body, "- A prompt") || !strings.Contains(gh.body, "- See \\[this\\]\\(https\\://evil\\.example\\)") || !strings.Contains(gh.body, "install test on a fresh virtual machine: skipped") {
		t.Fatalf("%+v %q", gh, gh.body)
	}
	// The running release passed its gate too, and is copied with it.
	if len(reg.pushed) != 8 || reg.pushed[4] != "crew-server:0.2.0 -> ghcr.io/op/crew-server:0.2.0 as op" || gh.tags["Op/crew v0.1.0"] != "c0.1.0" {
		t.Fatalf("%v", reg.pushed)
	}
	var done *string
	_ = k.d.Server.QueryRow(context.Background(), `SELECT published->>'done' FROM releases WHERE version = '0.2.0'`).Scan(&done)
	if done == nil {
		t.Fatal("not recorded as copied")
	}
	k.now = k.now.Add(24 * time.Hour)
	k.pass()
	if len(reg.pushed) != 8 {
		t.Fatal("copied twice")
	}
	// With updates.publish off, nothing is copied.
	k.release("0.3.0", map[string]string{"baseline/stages/build.md": "newer"}, true)
	on := k.s.Settings
	k.s.Settings = func(ctx context.Context) (settings.Doc, error) {
		d, err := on(ctx)
		if err == nil {
			d["updates"].(settings.Doc)["publish"] = "off"
		}
		return d, err
	}
	k.pass()
	if len(reg.pushed) != 8 || gh.tags["Op/crew v0.3.0"] != "" {
		t.Fatal("copied with publish off")
	}
}

// The news of an install is the new server's: a done request of another version than the server's is
// history, which a restore of an older snapshot made the table forget, and makes no news again. A
// failure at the switch, which the old server reads when it is up again, is recorded and held for
// the clock; a failure the table forgot does not touch a release that runs since.
func TestWhatFollowActsOn(t *testing.T) {
	k := newKit(t, "0.1.0")
	k.release("0.2.0", map[string]string{"baseline/stages/build.md": "new"}, true)
	k.pass()
	k.m.fail["Migrate"] = true
	k.now = k.now.Add(25 * time.Hour)
	k.pass()
	k.pass()
	if r := k.get("0.2.0"); r.State != "failed" || !r.Held || r.Failure == nil || !strings.Contains(*r.Failure, "Switch") {
		t.Fatalf("a failure at the switch: %+v", r)
	}
	// The table forgets the requests; the release is installed since, and runs.
	delete(k.m.fail, "Migrate")
	if _, err := k.s.Install(context.Background(), "0.2.0", ""); err != nil {
		t.Fatal(err)
	}
	k.u.Wait()
	k.s.Running = "0.2.0"
	k.pass()
	news := func() int {
		var n int
		_ = k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM changes WHERE tbl = 'releases' AND after ? 'news'`).Scan(&n)
		return n
	}
	if news() != 1 {
		t.Fatalf("%d news of the install", news())
	}
	if _, err := k.d.Server.Exec(context.Background(), `DELETE FROM update_requests`); err != nil {
		t.Fatal(err)
	}
	k.pass()
	if r := k.get("0.2.0"); r.State != "running" {
		t.Fatalf("a forgotten failure changed the running release: %+v", r)
	}
	if news() != 1 {
		t.Fatalf("%d news of the install after the table forgot, want one", news())
	}
}
func (m *machine) LoadImage(_ context.Context, _, tag string) (string, error) {
	return "sha256:" + strings.Repeat("e", 64), m.err("LoadImage " + tag)
}
func (m *machine) Untag(context.Context, string) error { return nil }
