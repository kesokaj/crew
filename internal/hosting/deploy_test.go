package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

const head1 = "1111111111111111111111111111111111111111"

// hosted is a machine with one project to host, shop, and fakes for the rest.
type hosted struct {
	t     *testing.T
	ctx   context.Context
	db    *dbtest.DB
	s     *Service
	rt    *fakeRuntime
	build *harness.Fake
	git   *fakeGit
	h     *fakeHealth
	id    string
}

func newHosted(t *testing.T, own map[string]any) *hosted {
	t.Helper()
	free := harness.FreeSpace
	harness.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(func() { harness.FreeSpace = free })
	for _, v := range []*time.Duration{&CheckTimeout, &CheckEvery, &RetireAfter} {
		old := *v
		t.Cleanup(func() { *v = old })
	}
	CheckTimeout, CheckEvery, RetireAfter = 300*time.Millisecond, 10*time.Millisecond, 0
	ctx := context.Background()
	d := dbtest.New(t, false)
	vault, err := credstore.NewVault(d.Server, bytes.Repeat([]byte{5}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	h := &hosted{t: t, ctx: ctx, db: d, rt: newFakeRuntime(), git: &fakeGit{head: head1, files: map[string][]byte{"Dockerfile": []byte("FROM scratch\n")}}}
	tar, err := os.ReadFile("testdata/save-plain.tar")
	if err != nil {
		t.Fatal(err)
	}
	h.build = &harness.Fake{ImageTar: tar}
	h.h = &fakeHealth{rt: h.rt}
	if err := d.Server.QueryRow(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'shop', 'SH', 'running', 'local') RETURNING id`).Scan(&h.id); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"version": 1, "project": "shop", "key": "SH"}
	if own != nil {
		doc["hosting"] = own
	}
	raw, _ := json.Marshal(doc)
	if _, err := d.Server.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, $2::jsonb, 'test')`, h.id, raw); err != nil {
		t.Fatal(err)
	}
	h.s = &Service{Pool: d.Server, Vault: vault, AppsDomain: "apps.test", CrewHost: "crew.example.com", Log: slog.New(slog.DiscardHandler),
		Runtime: h.rt, Builder: h.build, Git: h.git, Dir: t.TempDir(), Builds: t.TempDir(), HealthClient: &http.Client{Transport: h.h}}
	return h
}

// deploy asks a deploy and waits until it ended.
func (h *hosted) deploy(a Ask) Deploy {
	h.t.Helper()
	d, err := h.s.Deploy(h.ctx, h.id, a)
	if err != nil {
		h.t.Fatal(err)
	}
	h.s.Wait()
	got, err := h.s.deployByNumber(h.ctx, h.id, d.Number)
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

var asked = Ask{Why: "asked", By: "operator"}

func (h *hosted) row() (state string, live *int64, sandbox, addr *string, retiring *string) {
	h.t.Helper()
	if err := h.db.Server.QueryRow(h.ctx, `SELECT state, live_deploy, sandbox, host(address), retiring FROM hosting WHERE project = $1`, h.id).
		Scan(&state, &live, &sandbox, &addr, &retiring); err != nil {
		h.t.Fatal(err)
	}
	return
}

// serving says which deploy number serves, with its sandbox, or 0.
func (h *hosted) serving() (int, string) {
	h.t.Helper()
	var n int
	var sb string
	err := h.db.Server.QueryRow(h.ctx, `SELECT d.number, h.sandbox FROM hosting h JOIN deploys d ON d.id = h.live_deploy WHERE h.project = $1 AND h.state = 'live'`, h.id).Scan(&n, &sb)
	if err != nil {
		return 0, ""
	}
	return n, sb
}

func (h *hosted) data(name string) string {
	b, err := os.ReadFile(filepath.Join(h.s.dataDir(h.id), name))
	if err != nil {
		return ""
	}
	return string(b)
}

func (h *hosted) write(name, body string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.s.dataDir(h.id), name), []byte(body), 0o640); err != nil {
		h.t.Fatal(err)
	}
}

// Host locally (19.2): main's commit is built in a sandbox with no agent in the mode of the project's
// checks, its tar cleaned and loaded by its id, tagged by the deploy's number; the bridge is proved
// by the probe before the app starts as a counted sandbox of the project's size, with the values; its
// health address answers, and it is what serves, at its address.
func TestHostLocallyBuildsChecksAndSwitches(t *testing.T) {
	h := newHosted(t, nil)
	if err := h.s.SetValues(h.ctx, h.id, map[string]string{"DB_PASSWORD": "x", "A": "1"}, "test"); err != nil {
		t.Fatal(err)
	}
	d := h.deploy(asked)
	if d.State != "live" || d.Number != 1 || d.Commit != head1 || d.Image != plainID || d.LiveAt == nil {
		t.Fatalf("the deploy: %+v", d)
	}
	state, _, sandbox, addr, _ := h.row()
	id := SandboxID(h.id, 1)
	if state != "live" || sandbox == nil || *sandbox != id || addr == nil || *addr != h.rt.app(id) {
		t.Fatalf("hosting: %s %v %v", state, sandbox, addr)
	}
	b := h.build.Images[0]
	if b.Project != h.id || b.Mode != "internet" || b.Dockerfile != "Dockerfile" || string(b.Tree) != "the tree of "+head1 {
		t.Fatalf("the build: %+v", b)
	}
	if h.rt.images[ImageRef(h.id, 1)] != plainID || h.rt.loads != 1 {
		t.Fatalf("the image: %v", h.rt.images)
	}
	if i, j := slices.Index(h.rt.calls, "probe "+id), slices.Index(h.rt.calls, "start "+id); i < 0 || j < i {
		t.Fatalf("the probe did not come before the start: %v", h.rt.calls)
	}
	spec := h.rt.started[0]
	if spec.Port != 8080 || spec.DataMount != "/data" || spec.DataDir != h.s.dataDir(h.id) || !slices.Equal(spec.Env, []string{"A=1", "DB_PASSWORD=x"}) || spec.Image != plainID {
		t.Fatalf("the start: %+v", spec)
	}
	var kind, size, mode string
	if err := h.db.Server.QueryRow(h.ctx, `SELECT kind, size, mode FROM sandboxes WHERE id = $1 AND removed_at IS NULL`, id).Scan(&kind, &size, &mode); err != nil ||
		kind != "hosted" || size != "small" || mode != "hosted_internet" {
		t.Fatalf("the sandbox's row: %s %s %s %v", kind, size, mode, err)
	}
	if entries, _ := os.ReadDir(h.s.Builds); len(entries) != 0 {
		t.Fatalf("the build folder was left: %v", entries)
	}
	routes, err := h.s.Routes(h.ctx)
	if err != nil || len(routes) != 1 || routes[0].Upstream != h.rt.app(id)+":8080" {
		t.Fatalf("routes: %+v %v", routes, err)
	}
}

// Deploy again starts beside what serves, on the same data folder, after a copy of it made with the
// one that serves paused; then the new one serves and the old one is removed.
func TestDeployAgainCopiesTheDataAndRetiresTheOldOne(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("shop.db", "before")
	old := SandboxID(h.id, 1)
	d := h.deploy(asked)
	if d.State != "live" || d.Number != 2 || d.DataCopy != "2" {
		t.Fatalf("the deploy: %+v", d)
	}
	if b, _ := os.ReadFile(filepath.Join(h.s.copyDir(h.id, "2"), "shop.db")); string(b) != "before" {
		t.Fatalf("the copy: %q", b)
	}
	if i, j := slices.Index(h.rt.calls, "pause "+old), slices.Index(h.rt.calls, "unpause "+old); i < 0 || j < i || slices.Index(h.rt.calls, "start "+SandboxID(h.id, 2)) < j {
		t.Fatalf("the copy was not made with the old one paused, before the start: %v", h.rt.calls)
	}
	if n, sb := h.serving(); n != 2 || sb != SandboxID(h.id, 2) {
		t.Fatalf("serving %d %s", n, sb)
	}
	if !slices.Contains(h.rt.removed, old) {
		t.Fatalf("the old sandbox was kept: %v", h.rt.removed)
	}
	if _, _, _, _, retiring := h.row(); retiring != nil {
		t.Fatalf("still retiring %s", *retiring)
	}
	var state string
	h.db.Server.QueryRow(h.ctx, `SELECT state FROM deploys WHERE project = $1 AND number = 1`, h.id).Scan(&state)
	if state != "replaced" {
		t.Fatalf("deploy 1 is %s", state)
	}
}

// Every failure from the build to the check leaves what served serving (19.2 "If it fails"), with
// the deploy failed in Crew's words, and the new sandbox and the build's folder removed.
func TestAFailedDeployLeavesTheLiveOneServing(t *testing.T) {
	volume, err := os.ReadFile("testdata/save-volume.tar")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		set  func(h *hosted)
		want string
	}{
		"the build":  {func(h *hosted) { h.build.ImageErr = errors.New("exit 1") }, "the build failed: exit 1"},
		"a VOLUME":   {func(h *hosted) { h.build.ImageTar = volume }, "the image was refused: the image declares the VOLUME /cache"},
		"a tar":      {func(h *hosted) { h.build.ImageTar = []byte("not a tar") }, "the image was refused"},
		"another id": {func(h *hosted) { h.rt.loadID = "sha256:" + strings.Repeat("c", 64) }, "Docker loaded sha256:ccc"},
		"the probe": {func(h *hosted) {
			h.rt.probeErr = errors.New("the firewall of the hosted bridge is not in place: 172.20.2.254:8080 answered")
		}, "the firewall of the hosted bridge is not in place"},
		"the start": {func(h *hosted) { h.rt.startErr = errors.New("kata: no room") }, "the sandbox did not start: kata: no room"},
		"no answer": {func(h *hosted) {
			h.h.answer = func(sb string) int { return map[bool]int{true: 503, false: 200}[strings.HasSuffix(sb, "-2")] }
		}, "/healthz did not answer in 300ms\nlistening on 8080"},
		"a redirect": {func(h *hosted) {
			h.h.answer = func(sb string) int { return map[bool]int{true: 302, false: 200}[strings.HasSuffix(sb, "-2")] }
		}, "did not answer"},
		"no Dockerfile": {func(h *hosted) { h.git.files = map[string][]byte{} }, ""},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHosted(t, nil)
			h.deploy(asked)
			tc.set(h)
			d, err := h.s.Deploy(h.ctx, h.id, asked)
			h.s.Wait()
			if tc.want == "" {
				var nd *NoDockerfileError
				if !errors.As(err, &nd) {
					t.Fatalf("no Dockerfile: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got, _ := h.s.deployByNumber(h.ctx, h.id, d.Number)
				if got.State != "failed" || !strings.Contains(got.Detail, tc.want) {
					t.Fatalf("the deploy: %s %q", got.State, got.Detail)
				}
			}
			if n, sb := h.serving(); n != 1 || sb != SandboxID(h.id, 1) || !h.rt.running[sb] || h.rt.paused[sb] {
				t.Fatalf("serving %d %s", n, sb)
			}
			if h.rt.running[SandboxID(h.id, 2)] {
				t.Fatal("the failed deploy's sandbox was left")
			}
			if entries, _ := os.ReadDir(h.s.Builds); len(entries) != 0 {
				t.Fatalf("the build folder was left: %v", entries)
			}
			if _, ok := h.rt.images[ImageRef(h.id, 2)]; ok {
				t.Fatal("the failed deploy's image was kept")
			}
		})
	}
}

// A first deploy that fails leaves the project stopped, not deploying.
func TestAFailedFirstDeployStopsTheProject(t *testing.T) {
	h := newHosted(t, nil)
	h.build.ImageErr = errors.New("exit 2")
	if d := h.deploy(asked); d.State != "failed" {
		t.Fatalf("%+v", d)
	}
	if state, live, _, _, _ := h.row(); state != "stopped" || live != nil {
		t.Fatalf("hosting: %s %v", state, live)
	}
}

// One deploy of a project at a time: a second while one runs is refused, by the database's index too.
func TestTwoDeploysAtOnceAreRefused(t *testing.T) {
	h := newHosted(t, nil)
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO hosting (project, state) VALUES ($1, 'deploying')`, h.id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO deploys (project, number, commit, why, asked_by, state) VALUES ($1, 7, $2, 'asked', 'operator', 'building')`, h.id, head1); err != nil {
		t.Fatal(err)
	}
	_, err := h.s.Deploy(h.ctx, h.id, asked)
	var pe *PreconditionError
	if !errors.As(err, &pe) || pe.Msg != "deploy 7 is running" {
		t.Fatalf("a second deploy: %v", err)
	}
	if len(h.build.Images) != 0 {
		t.Fatal("it was built")
	}
}

// A roll back is not built: it starts the kept deploy's image, checked, and switches; the data stays
// as it is unless asked.
func TestARollBackStartsTheKeptImage(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.deploy(asked)
	h.write("new.txt", "made by 2")
	d := h.deploy(Ask{Why: "rollback", By: "operator", From: 1})
	if d.State != "live" || d.Image != plainID || len(h.build.Images) != 2 {
		t.Fatalf("the roll back: %+v, %d builds", d, len(h.build.Images))
	}
	if n, _ := h.serving(); n != 3 || h.data("new.txt") != "made by 2" {
		t.Fatalf("serving %d, data %q", n, h.data("new.txt"))
	}
	if spec := h.rt.started[len(h.rt.started)-1]; spec.Image != plainID {
		t.Fatalf("started %s", spec.Image)
	}
}

// The capacity check's lock is let go once the sandbox's row is written, before the bridge and the
// probe, which can take long: another start does not wait for them (review of M19, finding 4).
func TestTheRoomIsLetGoBeforeTheProbe(t *testing.T) {
	h := newHosted(t, nil)
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	h.s.Room = &capacity.Gate{Pool: h.db.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: 32, MemMiB: 128 * 1024}, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return doc, nil }}
	var free []bool
	h.rt.onProbe = func() {
		ok := h.s.Room.Mu.TryLock()
		if ok {
			h.s.Room.Mu.Unlock()
		}
		free = append(free, ok)
	}
	if d := h.deploy(asked); d.State != "live" {
		t.Fatalf("the deploy: %s %q", d.State, d.Detail)
	}
	if len(free) != 1 || !free[0] {
		t.Fatalf("the room's lock was free at the probe: %v", free)
	}
}

// A roll back of a kept image that has Crew's own labels is refused: a kept image is checked again
// (security review of M19, round 2).
func TestARollBackChecksTheKeptImagesLabels(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.deploy(asked)
	h.rt.labels = []string{"crew.sandbox", "org.example"}
	d := h.deploy(Ask{Why: "rollback", By: "operator", From: 1})
	if d.State != "failed" || !strings.Contains(d.Detail, "Crew's own labels, crew.sandbox") {
		t.Fatalf("the roll back: %s %q", d.State, d.Detail)
	}
}

// A roll back with the data puts back the copy made before the deploy that replaced the one rolled
// back to (the data as that one left it), even with one copy kept; the data of now is copied first.
func TestARollBackWithTheDataPutsBackTheCopyOfBefore(t *testing.T) {
	h := newHosted(t, map[string]any{"data_copies": 1})
	h.deploy(asked)
	h.write("old.txt", "from 1")
	h.deploy(asked) // copy 2: old.txt
	h.write("new.txt", "from 2")
	os.Remove(filepath.Join(h.s.dataDir(h.id), "old.txt"))
	os.Symlink("/etc/shadow", filepath.Join(h.s.dataDir(h.id), "link"))
	d := h.deploy(Ask{Why: "rollback", By: "operator", From: 1, RestoreData: true})
	if d.State != "live" {
		t.Fatalf("the roll back: %+v", d)
	}
	if h.data("old.txt") != "from 1" || h.data("new.txt") != "" {
		t.Fatalf("the data: old %q new %q", h.data("old.txt"), h.data("new.txt"))
	}
	if _, err := os.Lstat(filepath.Join(h.s.dataDir(h.id), "link")); err == nil {
		t.Fatal("the link of now was kept")
	}
	if b, _ := os.ReadFile(filepath.Join(h.s.copyDir(h.id, "3"), "new.txt")); string(b) != "from 2" {
		t.Fatal("the data of now was not copied first")
	}
	var restored string
	h.db.Server.QueryRow(h.ctx, `SELECT restored FROM deploys WHERE id = $1`, d.ID).Scan(&restored)
	if restored != "2" || !slices.Contains(h.rt.calls, "stop "+SandboxID(h.id, 2)) {
		t.Fatalf("restored %q, calls %v", restored, h.rt.calls)
	}
}

// A roll back with the data that fails after the stop puts back the data of now and starts again
// what served, and says both.
func TestAFailedRestorePutsBackTheDataAndTheVersion(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("old.txt", "from 1")
	h.deploy(asked)
	h.write("new.txt", "from 2")
	h.h.answer = func(sb string) int { return map[bool]int{true: 500, false: 200}[strings.HasSuffix(sb, "-3")] }
	// Until the data of before is back and what served runs again, the roll back still names its copy,
	// so a restart in between finishes the put back (recoverRollBacks); cleared before, a restart would
	// leave the roll back's data (review of the release of M20, F1).
	var whileBack []string
	h.rt.onAgain = func(string) {
		var restored *string
		h.db.Server.QueryRow(h.ctx, `SELECT restored FROM deploys WHERE project = $1 AND number = 3`, h.id).Scan(&restored)
		whileBack = append(whileBack, fmt.Sprintf("%t %s", restored != nil, h.data("new.txt")))
	}
	d := h.deploy(Ask{Why: "rollback", By: "operator", From: 1, RestoreData: true})
	if !slices.Equal(whileBack, []string{"true from 2"}) {
		t.Fatalf("named while what served starts again, with the data put back: %v", whileBack)
	}
	if d.State != "failed" || !strings.Contains(d.Detail, "did not answer") || !strings.Contains(d.Detail, "the data was put back") ||
		!strings.Contains(d.Detail, "deploy 2 serves again") {
		t.Fatalf("the roll back: %s %q", d.State, d.Detail)
	}
	if h.data("new.txt") != "from 2" || h.data("old.txt") != "from 1" {
		t.Fatalf("the data: %q %q", h.data("new.txt"), h.data("old.txt"))
	}
	if n, sb := h.serving(); n != 2 || !h.rt.running[sb] || !slices.Contains(h.rt.again, sb) {
		t.Fatalf("serving %d %s", n, sb)
	}
	// It put nothing back that stays, so a later restart takes it for no roll back with data (round 2).
	var restored *string
	h.db.Server.QueryRow(h.ctx, `SELECT restored FROM deploys WHERE id = $1`, d.ID).Scan(&restored)
	if restored != nil {
		t.Fatalf("restored %q", *restored)
	}
}

// A roll back to a deploy whose image is gone, or with the data when its copy is gone, is refused.
func TestARollBackNeedsItsImageAndItsCopy(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.deploy(asked)
	os.RemoveAll(h.s.copyDir(h.id, "2"))
	var pe *PreconditionError
	if _, err := h.s.Deploy(h.ctx, h.id, Ask{Why: "rollback", By: "operator", From: 1, RestoreData: true}); !errors.As(err, &pe) || !strings.Contains(pe.Msg, "copy") {
		t.Fatalf("no copy: %v", err)
	}
	delete(h.rt.images, ImageRef(h.id, 1))
	if _, err := h.s.Deploy(h.ctx, h.id, Ask{Why: "rollback", By: "operator", From: 1}); !errors.As(err, &pe) || !strings.Contains(pe.Msg, "image") {
		t.Fatalf("no image: %v", err)
	}
}

// A settings deploy starts the live image again with the settings of now, nothing rebuilt (D21): the
// internet off gives a bridge without it.
func TestASettingsDeployStartsTheLiveImageAgain(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	if _, err := h.db.Server.Exec(h.ctx, `UPDATE settings SET doc = jsonb_set(doc, '{hosting}', '{"internet": false, "size": "large"}') WHERE project = $1`, h.id); err != nil {
		t.Fatal(err)
	}
	d := h.deploy(Ask{Why: "settings", By: "settings", From: 1})
	if d.State != "live" || len(h.build.Images) != 1 || !slices.Contains(h.rt.calls, "net "+SandboxID(h.id, 2)+" false") {
		t.Fatalf("the settings deploy: %+v %v", d, h.rt.calls)
	}
	var size, mode string
	h.db.Server.QueryRow(h.ctx, `SELECT size, mode FROM sandboxes WHERE id = $1`, SandboxID(h.id, 2)).Scan(&size, &mode)
	if size != "large" || mode != "hosted_offline" {
		t.Fatalf("%s %s", size, mode)
	}
}

// Stop: the sandbox goes and the route with it; the data, its copies and the images are kept.
func TestStopKeepsTheDataAndTheImages(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("shop.db", "kept")
	if err := h.s.Stop(h.ctx, h.id); err != nil {
		t.Fatal(err)
	}
	if state, live, sb, addr, _ := h.row(); state != "stopped" || live != nil || sb != nil || addr != nil {
		t.Fatalf("hosting: %s", state)
	}
	if routes, _ := h.s.Routes(h.ctx); len(routes) != 0 {
		t.Fatalf("routes: %+v", routes)
	}
	if h.data("shop.db") != "kept" || h.rt.images[ImageRef(h.id, 1)] == "" || h.rt.running[SandboxID(h.id, 1)] {
		t.Fatal("stop removed what it keeps, or kept the sandbox")
	}
	var state string
	h.db.Server.QueryRow(h.ctx, `SELECT state FROM deploys WHERE project = $1 AND number = 1`, h.id).Scan(&state)
	if state != "stopped" {
		t.Fatalf("deploy 1 is %s", state)
	}
}

// The images of the newest hosting.keep_images deploys that served are kept, and never the live one's.
func TestOnlyTheNewestImagesAreKept(t *testing.T) {
	h := newHosted(t, nil)
	for range 5 {
		h.deploy(asked)
	}
	var kept []string
	for ref := range h.rt.images {
		kept = append(kept, ref)
	}
	slices.Sort(kept)
	want := []string{ImageRef(h.id, 3), ImageRef(h.id, 4), ImageRef(h.id, 5)}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v", kept)
	}
}

// Values sealed with a key file that is gone fail the deploy with the words of 21 (D32).
func TestLostValuesFailADeploy(t *testing.T) {
	h := newHosted(t, nil)
	if err := h.s.SetValues(h.ctx, h.id, map[string]string{"A": "1"}, "test"); err != nil {
		t.Fatal(err)
	}
	other, _ := credstore.NewVault(h.db.Server, bytes.Repeat([]byte{6}, credstore.KeySize))
	h.s.Vault = other
	if d := h.deploy(asked); d.State != "failed" || !strings.Contains(d.Detail, "the values must be entered again") {
		t.Fatalf("%s %q", d.State, d.Detail)
	}
}

// logs is everything in the project's log files.
func (h *hosted) logs() string {
	var b strings.Builder
	entries, _ := os.ReadDir(filepath.Join(h.s.projectDir(h.id), "logs"))
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(h.s.projectDir(h.id), "logs", e.Name()))
		b.Write(raw)
	}
	return b.String()
}

// The server restarted during a deploy (E2E 8, plan M19 D29): the deploy is failed "interrupted", its
// sandbox and its build's folder go, what served serves on, and the next deploy starts.
func TestARestartDuringADeployFailsItAndTheNextOneRuns(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	half := SandboxID(h.id, 2)
	for _, q := range []string{
		`INSERT INTO deploys (project, number, commit, why, asked_by, state, image) VALUES ($1, 2, '` + head1 + `', 'asked', 'operator', 'checking', '` + plainID + `')`,
		`INSERT INTO sandboxes (id, kind, project, container, network, image, size, mode) VALUES ('` + half + `', 'hosted', $1, 'c', 'c', 'i', 'small', 'hosted_internet')`,
	} {
		if _, err := h.db.Server.Exec(h.ctx, q, h.id); err != nil {
			t.Fatal(err)
		}
	}
	h.rt.running[half] = true
	os.MkdirAll(h.s.buildDir(h.id, 2), 0o750)
	if err := h.s.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	d, _ := h.s.deployByNumber(h.ctx, h.id, 2)
	if d.State != "failed" || d.Detail != "interrupted by a restart of Crew" {
		t.Fatalf("deploy 2: %s %q", d.State, d.Detail)
	}
	if h.rt.running[half] || !slices.Contains(h.rt.removed, half) {
		t.Fatal("the half started sandbox was kept")
	}
	if _, err := os.Stat(h.s.buildDir(h.id, 2)); err == nil {
		t.Fatal("the build's folder was kept")
	}
	if n, sb := h.serving(); n != 1 || slices.Contains(h.rt.removed, sb) {
		t.Fatalf("serving %d %s", n, sb)
	}
	if d := h.deploy(asked); d.State != "live" || d.Number != 3 {
		t.Fatalf("the next deploy: %+v", d)
	}
}

// A live sandbox whose microVM went while the server was down is started again from its own image.
func TestALiveSandboxThatWentIsStartedAgain(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	if _, err := h.db.Server.Exec(h.ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1`, SandboxID(h.id, 1)); err != nil {
		t.Fatal(err)
	}
	delete(h.rt.running, SandboxID(h.id, 1))
	h.rt.pauseErr = errors.New("Error response from daemon: container crew-x is not running")
	if err := h.s.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	d, _ := h.s.deployByNumber(h.ctx, h.id, 2)
	if d.State != "live" || d.Why != "settings" || len(h.build.Images) != 1 {
		t.Fatalf("deploy 2: %+v", d)
	}
}

// On Docker's containerd image store a load gives the manifest's digest (plan M19 D42): that is the
// image the cleaner read too, and it is hosted.
func TestAnImageLoadedByItsManifestIsHosted(t *testing.T) {
	h := newHosted(t, nil)
	h.rt.loadID = plainManifest
	if d := h.deploy(asked); d.State != "live" || d.Image != plainManifest {
		t.Fatalf("%s %q %s", d.State, d.Detail, d.Image)
	}
}

// A deploy that takes another's image, a settings deploy or a roll back, can itself be rolled back to
// once it served: it tags the image with its own number, as a build does (found on the Hosting tier).
func TestADeployOfAKeptImageCanBeRolledBackTo(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	if d := h.deploy(Ask{Why: "settings", By: "settings", From: 1}); d.State != "live" {
		t.Fatalf("the settings deploy: %+v", d)
	}
	if h.rt.images[ImageRef(h.id, 2)] != plainID {
		t.Fatalf("the settings deploy's image is not tagged with its number: %v", h.rt.images)
	}
	h.deploy(asked)
	if d := h.deploy(Ask{Why: "rollback", By: "operator", From: 2}); d.State != "live" {
		t.Fatalf("the roll back to the settings deploy: %+v", d)
	}
}

// The route and health keep the port and health address what serves was checked on: a change of
// hosting.port holds with its own deploy, and a failed one leaves the route as it was (review of M19,
// finding 3).
func TestTheRouteKeepsThePortOfWhatServes(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	if _, err := h.db.Server.Exec(h.ctx, `UPDATE settings SET doc = jsonb_set(doc, '{hosting}', '{"port": 9090, "health": "/ready"}') WHERE project = $1`, h.id); err != nil {
		t.Fatal(err)
	}
	routes, _ := h.s.Routes(h.ctx)
	if len(routes) != 1 || !strings.HasSuffix(routes[0].Upstream, ":8080") {
		t.Fatalf("the route moved before a deploy: %+v", routes)
	}
	h.rt.probeErr = errors.New("the firewall of the hosted bridge is not in place")
	if d := h.deploy(Ask{Why: "settings", By: "settings", From: 1}); d.State != "failed" {
		t.Fatalf("%+v", d)
	}
	if routes, _ := h.s.Routes(h.ctx); !strings.HasSuffix(routes[0].Upstream, ":8080") {
		t.Fatalf("a failed deploy moved the route: %+v", routes)
	}
	h.rt.probeErr = nil
	h.deploy(Ask{Why: "settings", By: "settings", From: 1})
	if routes, _ := h.s.Routes(h.ctx); !strings.HasSuffix(routes[0].Upstream, ":9090") {
		t.Fatalf("the deploy that serves on 9090: %+v", routes)
	}
}

// A restart of Crew after a roll back with data stopped what served: at the start the data of before is
// put back and what served starts again (review of M19, finding 5).
func TestARestartDuringARollBackPutsTheDataBack(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("old.txt", "from 1")
	h.deploy(asked)
	h.write("now.txt", "of now")
	live := SandboxID(h.id, 2)
	// Played: deploy 3, a roll back to 1 with its data, copied the data of now as copy 3, named copy 1
	// as the one it puts back, stopped what serves, had half replaced the data and started its own
	// sandbox, which still runs, when Crew stopped.
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO deploys (project, number, commit, image, why, asked_by, from_deploy, state, data_copy, restored)
		SELECT $1, 3, $2, image, 'rollback', 'operator', id, 'checking', '3', '1' FROM deploys WHERE project = $1 AND number = 1`, h.id, head1); err != nil {
		t.Fatal(err)
	}
	cut := SandboxID(h.id, 3)
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO sandboxes (id, kind, project, container, network, image, size, mode)
		VALUES ($2, 'hosted', $1, 'c', 'c', 'i', 'small', 'hosted_internet')`, h.id, cut); err != nil {
		t.Fatal(err)
	}
	h.rt.running[cut] = true
	var notes []string
	if err := copyTree(h.s.dataDir(h.id), h.s.copyDir(h.id, "3"), time.Now().Add(time.Minute), &notes); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(h.s.dataDir(h.id), "now.txt"))
	h.rt.running[live] = false
	if err := h.s.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	d, _ := h.s.deployByNumber(h.ctx, h.id, 3)
	if d.State != "failed" || !strings.Contains(d.Detail, "the data was put back") || !strings.Contains(d.Detail, "deploy 2 serves again") {
		t.Fatalf("deploy 3: %s %q", d.State, d.Detail)
	}
	if h.data("now.txt") != "of now" || !h.rt.running[live] {
		t.Fatalf("the data %q, running %v", h.data("now.txt"), h.rt.running[live])
	}
	// The cut-off deploy's app went before the data was put back under it (review of M19, round 2).
	removed, again := slices.Index(h.rt.calls, "remove "+cut), slices.Index(h.rt.calls, "start again "+live)
	if removed < 0 || again < 0 || removed > again {
		t.Fatalf("calls %v", h.rt.calls)
	}
}

// A roll back without data that a restart cut off leaves the data as it is: it changed none (review
// of M19, round 2).
func TestARestartDuringARollBackWithoutDataLeavesTheData(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.deploy(asked)
	live := SandboxID(h.id, 2)
	// Played: deploy 3, a roll back to 1 without data, copied the data of now as copy 3 and was checked
	// when Crew stopped, with the machine's microVM of deploy 2 not yet running again.
	if _, err := h.db.Server.Exec(h.ctx, `INSERT INTO deploys (project, number, commit, image, why, asked_by, from_deploy, state, data_copy)
		SELECT $1, 3, $2, image, 'rollback', 'operator', id, 'checking', '3' FROM deploys WHERE project = $1 AND number = 1`, h.id, head1); err != nil {
		t.Fatal(err)
	}
	var notes []string
	if err := copyTree(h.s.dataDir(h.id), h.s.copyDir(h.id, "3"), time.Now().Add(time.Minute), &notes); err != nil {
		t.Fatal(err)
	}
	h.write("later.txt", "written after the copy")
	h.rt.running[live] = false
	if err := h.s.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	d, _ := h.s.deployByNumber(h.ctx, h.id, 3)
	if d.State != "failed" || strings.Contains(d.Detail, "put back") || h.data("later.txt") != "written after the copy" {
		t.Fatalf("deploy 3: %s %q, the data %q", d.State, d.Detail, h.data("later.txt"))
	}
}
