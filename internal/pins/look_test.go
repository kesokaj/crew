package pins_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/pins"
)

// lockText is versions.lock on main of Crew's project, as lock() gives it.
func lockText() string {
	var b strings.Builder
	b.WriteString("# versions.lock\n")
	l := lock()
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + l[k] + "\n")
	}
	return b.String()
}

type lookKit struct {
	t    *testing.T
	d    *dbtest.DB
	b    *board.Board
	git  *gitstore.Store
	p    *publishers
	look *pins.Look
}

// newLookKit is the fixture project as Crew's project, with versions.lock on main, and the publishers
// of the recorded day with every image as pinned.
func newLookKit(t *testing.T) *lookKit {
	t.Helper()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	git := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}, Protocols: "file"}
	k := &lookKit{t: t, d: d, git: git, b: &board.Board{Pool: d.Server, Git: git}, p: recorded(t)}
	k.repo("fixture")
	k.crewProject("fixture")
	k.p.asPinned()
	k.look = k.newLook()
	return k
}

func (k *lookKit) newLook() *pins.Look {
	return &pins.Look{Pool: k.d.Server, Board: k.b, Sources: k.p.serve(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func (k *lookKit) repo(name string) {
	k.t.Helper()
	if err := k.git.CreateLocal(context.Background(), name, map[string]string{"versions.lock": lockText()}, "Crew", gitstore.Identity{Name: "a", Email: "a@x"}); err != nil {
		k.t.Fatal(err)
	}
}

func (k *lookKit) crewProject(name string) {
	k.t.Helper()
	if _, err := k.d.Server.Exec(context.Background(), `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, $1::jsonb, 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = excluded.doc`, `{"version": 1, "improvements": {"crew_project": "`+name+`"}}`); err != nil {
		k.t.Fatal(err)
	}
}

func (k *lookKit) once() *pins.Result {
	k.t.Helper()
	r, err := k.look.Once(context.Background())
	if err != nil {
		k.t.Fatal(err)
	}
	return r
}

// tickets are the titles of the look's tickets on a project, with their stage, origin, and status.
func (k *lookKit) tickets(project string) []string {
	k.t.Helper()
	rows, err := k.d.Server.Query(context.Background(), `SELECT t.title || ' · ' || t.stage || ' · ' || t.origin || ' · ' || (t.analysis->>'status')
		FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = $1 AND t.created_by = $2 ORDER BY t.number`, project, pins.Author)
	if err != nil {
		k.t.Fatal(err)
	}
	out, err := pgxStrings(rows)
	if err != nil {
		k.t.Fatal(err)
	}
	return out
}

func pgxStrings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// A new version makes one ticket, an agent's for Analysis, with Crew's text and a check per line; a
// look again makes none, with the ticket open, closed, or discarded; a newer version makes one more.
func TestANewVersionMakesOneTicketEver(t *testing.T) {
	k := newLookKit(t)
	ctx := context.Background()
	r := k.once()
	if !slices.Equal(r.Made, []string{"chrome-devtools-mcp@1.11.0"}) {
		t.Fatalf("made %v\n%v", r.Made, r.Lines)
	}
	if got := k.tickets("fixture"); !slices.Equal(got, []string{"Update chrome-devtools-mcp to 1.11.0, from npm · backlog · agent · proposed"}) {
		t.Fatalf("tickets %v", got)
	}
	var what, why string
	if err := k.d.Server.QueryRow(ctx, `SELECT what, why FROM tickets WHERE created_by = $1`, pins.Author).Scan(&what, &why); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(what, "- CHROME_DEVTOOLS_MCP_VERSION=1.11.0\n- CHROME_DEVTOOLS_MCP_INTEGRITY=sha512-") || !strings.Contains(what, "image/Dockerfile") ||
		!strings.Contains(why, "versions.lock on main pins 1.10.1") {
		t.Fatalf("what %q why %q", what, why)
	}
	checks, err := pgxStrings(must(k.d.Server.Query(ctx, `SELECT c.spec || ' · ' || c.keeps FROM checks c JOIN tickets t ON t.id = c.ticket
		WHERE t.created_by = $1 ORDER BY c.position`, pins.Author)))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 3 || checks[0] != "grep -qxF 'CHROME_DEVTOOLS_MCP_VERSION=1.11.0' versions.lock · false" ||
		!strings.HasPrefix(checks[1], "grep -qxF 'CHROME_DEVTOOLS_MCP_INTEGRITY=sha512-") || checks[2] != "python3 tools/check-versions.py · true" {
		t.Fatalf("checks %v", checks)
	}
	// Again: with the ticket open, discarded in Analysis, cancelled, and done.
	for _, set := range []string{"", "stage = 'closed', closed_reason = 'discarded'", "closed_reason = 'cancelled'", "stage = 'done', closed_reason = NULL"} {
		if set != "" {
			if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET `+set+` WHERE created_by = $1`, pins.Author); err != nil {
				t.Fatal(err)
			}
		}
		if r := k.once(); len(r.Made) != 0 {
			t.Fatalf("with the ticket %q: made %v", set, r.Made)
		}
	}
	k.p.answers["/chrome-devtools-mcp/latest"] = strings.ReplaceAll(k.p.answers["/chrome-devtools-mcp/latest"], "1.11.0", "1.12.0")
	if r := k.once(); !slices.Equal(r.Made, []string{"chrome-devtools-mcp@1.12.0"}) {
		t.Fatalf("a newer version: %v", r.Made)
	}
	// Each look is kept, and Versions reads the newest.
	last, err := pins.Last(ctx, k.d.Server)
	if err != nil || last == nil || !slices.Equal(last.Made, []string{"chrome-devtools-mcp@1.12.0"}) || len(last.Lines) < 5 {
		t.Fatalf("last %+v %v", last, err)
	}
	var n int
	if err := k.d.Server.QueryRow(ctx, `SELECT count(*) FROM pin_looks`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("looks %d %v", n, err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Two looks at once, of two servers, make one ticket.
func TestTwoLooksAtOnceMakeOne(t *testing.T) {
	k := newLookKit(t)
	a, b := k.newLook(), k.newLook()
	var wg sync.WaitGroup
	var made [2][]string
	for i, l := range []*pins.Look{a, b} {
		wg.Go(func() {
			r, err := l.Once(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			made[i] = r.Made
		})
	}
	wg.Wait()
	if len(made[0])+len(made[1]) != 1 || len(k.tickets("fixture")) != 1 {
		t.Fatalf("made %v, tickets %v", made, k.tickets("fixture"))
	}
}

// A Go release and the old tag's moved digest in one look make Go's ticket, which moves the image; a
// digest ticket open holds back the next of the same image until it closes.
func TestGoKeepsItsImageAndAnOpenDigestTicketHoldsTheNext(t *testing.T) {
	k := newLookKit(t)
	ctx := context.Background()
	k.p.answers["/dl/"] = strings.ReplaceAll(k.p.answers["/dl/"], "1.27.3", "1.27.4")
	k.p.digests["/v2/library/golang/manifests/1.27.4-trixie"] = digest('9')
	k.p.digests["/v2/library/golang/manifests/1.27.3-trixie"] = digest('8')
	k.p.digests["/v2/library/debian/manifests/trixie-slim"] = digest('a')
	r := k.once()
	if !slices.Equal(r.Made, []string{"go@1.27.4", "chrome-devtools-mcp@1.11.0", "image:SERVER_BASE_IMAGE@" + digest('a')}) {
		t.Fatalf("made %v\n%v", r.Made, r.Lines)
	}
	var images string
	if err := k.d.Server.QueryRow(ctx, `SELECT what FROM tickets WHERE title = 'Move the pinned image SERVER_BASE_IMAGE to its tag''s new digest'`).Scan(&images); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(images, "GO_IMAGE") {
		t.Fatalf("the images' ticket moves Go's image: %q", images)
	}
	// The next day the slim image moved again: its ticket is open, so the next waits.
	k.p.digests["/v2/library/debian/manifests/trixie-slim"] = digest('b')
	r = k.once()
	if len(r.Made) != 0 || !slices.ContainsFunc(r.Lines, func(l pins.Line) bool {
		return strings.Contains(l.Text, "waits: an open ticket of the look changes SERVER_BASE_IMAGE")
	}) {
		t.Fatalf("made %v\n%v", r.Made, r.Lines)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET stage = 'done' WHERE title LIKE 'Move the pinned image%'`); err != nil {
		t.Fatal(err)
	}
	if r = k.once(); !slices.Equal(r.Made, []string{"image:SERVER_BASE_IMAGE@" + digest('b')}) {
		t.Fatalf("after it closed: %v", r.Made)
	}
	// A day partly keyed: the slim image's digest has its ticket, done; mcr's moved since. The ticket
	// names the new one alone.
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET stage = 'done' WHERE title LIKE 'Move the pinned image%'`); err != nil {
		t.Fatal(err)
	}
	k.p.digests["/v2/playwright/manifests/v1.64.0-noble"] = digest('c')
	if r = k.once(); !slices.Equal(r.Made, []string{"image:PLAYWRIGHT_IMAGE@" + digest('c')}) {
		t.Fatalf("partly keyed: %v\n%v", r.Made, r.Lines)
	}
	var what string
	if err := k.d.Server.QueryRow(ctx, `SELECT what FROM tickets WHERE title = 'Move the pinned image PLAYWRIGHT_IMAGE to its tag''s new digest'`).Scan(&what); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(what, "SERVER_BASE_IMAGE") {
		t.Fatalf("the ticket names a line that has its ticket: %q", what)
	}
}

// The newest 30 looks are kept.
func TestTheNewestLooksAreKept(t *testing.T) {
	k := newLookKit(t)
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `DELETE FROM settings WHERE project IS NULL`); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	for i := range 31 {
		r := k.once()
		if i == 0 {
			first = r.At
		}
	}
	var n int
	var oldest time.Time
	if err := k.d.Server.QueryRow(ctx, `SELECT count(*), min(at) FROM pin_looks`).Scan(&n, &oldest); err != nil || n != 30 || !oldest.After(first) {
		t.Fatalf("%d looks, the oldest %v, the first %v (%v)", n, oldest, first, err)
	}
}

// The loop makes no look when it starts, and one at the time it is given.
func TestTheLoopLooksAtItsTimeOnly(t *testing.T) {
	k := newLookKit(t)
	count := func() int {
		var n int
		if err := k.d.Server.QueryRow(context.Background(), `SELECT count(*) FROM pin_looks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	run := func(next func(time.Time) time.Time, d time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		k.look.Run(ctx, next)
	}
	run(func(now time.Time) time.Time { return now.Add(time.Hour) }, 300*time.Millisecond)
	if n := count(); n != 0 {
		t.Fatalf("%d looks at start", n)
	}
	calls := 0
	run(func(now time.Time) time.Time {
		calls++
		if calls == 1 {
			return now.Add(50 * time.Millisecond)
		}
		return now.Add(time.Hour)
	}, 2*time.Second)
	if n := count(); n != 1 {
		t.Fatalf("%d looks at its time", n)
	}
}

// A paused Crew's project gets its ticket in Backlog, where it waits; a Crew's project made again has
// no keys of the one before; with none, the look makes nothing and says why.
func TestPausedRemadeAndNoCrewProject(t *testing.T) {
	k := newLookKit(t)
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET state = 'paused' WHERE name = 'fixture'`); err != nil {
		t.Fatal(err)
	}
	if r := k.once(); len(r.Made) != 1 || !strings.HasSuffix(k.tickets("fixture")[0], "· backlog · agent · proposed") {
		t.Fatalf("paused: %v %v", r.Made, k.tickets("fixture"))
	}
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES (gen_random_uuid(), 'crew', 'CREW', 'running', 'local')`); err != nil {
		t.Fatal(err)
	}
	k.repo("crew")
	k.crewProject("crew")
	if r := k.once(); !slices.Equal(r.Made, []string{"chrome-devtools-mcp@1.11.0"}) || len(k.tickets("crew")) != 1 {
		t.Fatalf("made again: %v %v", r.Made, k.tickets("crew"))
	}
	if _, err := k.d.Server.Exec(ctx, `DELETE FROM settings WHERE project IS NULL`); err != nil {
		t.Fatal(err)
	}
	r := k.once()
	if len(r.Made) != 0 || len(r.Lines) != 1 || r.Lines[0].OK || !strings.Contains(r.Lines[0].Text, "there is none") {
		t.Fatalf("no Crew project: %+v", r)
	}
	k.p.mu.Lock()
	k.p.hits = nil
	k.p.mu.Unlock()
	k.crewProject("nothere")
	// A project with no repository has no versions.lock git can read (since 1.04, a failure of git is
	// no main with none).
	if r := k.once(); len(r.Made) != 0 || !strings.Contains(r.Lines[0].Text, "versions.lock could not be read") || len(k.p.asked()) != 0 {
		t.Fatalf("no versions.lock: %+v, %v", r, k.p.asked())
	}
}

// A ticket the board refuses costs its line, not the look: the next finding's ticket is made, and the
// look is kept with both (review of c5eff65..5a72946). The images that moved are named in the title.
func TestATicketThatCannotBeMadeCostsItsLine(t *testing.T) {
	k := newLookKit(t)
	ctx := context.Background()
	owner := k.d.ConnectAs(t, db.RoleMigrate)
	if _, err := owner.Exec(ctx, `CREATE FUNCTION refuse_npm() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.key LIKE 'chrome-devtools-mcp@%' THEN RAISE EXCEPTION 'refused for the test'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER refuse_npm BEFORE INSERT ON pin_tickets FOR EACH ROW EXECUTE FUNCTION refuse_npm()`); err != nil {
		t.Fatal(err)
	}
	k.p.digests["/v2/library/debian/manifests/trixie-slim"] = digest('a')
	k.p.digests["/v2/playwright/manifests/v1.64.0-noble"] = digest('b')
	r, err := k.look.Once(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Made, []string{"image:PLAYWRIGHT_IMAGE@" + digest('b'), "image:SERVER_BASE_IMAGE@" + digest('a')}) {
		t.Fatalf("made %v\n%v", r.Made, r.Lines)
	}
	if !slices.ContainsFunc(r.Lines, func(l pins.Line) bool {
		return !l.OK && l.Text == "Update chrome-devtools-mcp to 1.11.0, from npm: not made: the board did not take it"
	}) {
		t.Fatalf("lines %v", r.Lines)
	}
	if got := k.tickets("fixture"); !slices.Equal(got, []string{"Move the pinned images PLAYWRIGHT_IMAGE, SERVER_BASE_IMAGE to their tags' new digests · backlog · agent · proposed"}) {
		t.Fatalf("tickets %v", got)
	}
	if last, err := pins.Last(ctx, k.d.Server); err != nil || last == nil || len(last.Made) != 2 {
		t.Fatalf("the look was not kept: %+v %v", last, err)
	}
}

// A look is bounded as a whole: publishers that do not answer make it end, with its lines kept.
func TestALookIsBounded(t *testing.T) {
	k := newLookKit(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	k.look.Sources = &pins.Sources{GoDL: slow.URL, Node: slow.URL, NPM: slow.URL, DockerAuth: slow.URL, DockerHub: slow.URL, MCR: slow.URL,
		GH: slow.URL, Agy: slow.URL + "/m", AgyTarball: cli.AgyTarball}
	k.look.For = 300 * time.Millisecond
	start := time.Now()
	r, err := k.look.Once(context.Background())
	if took := time.Since(start); err != nil || took > 5*time.Second {
		t.Fatalf("a look of %v (%v)", took, err)
	}
	if len(r.Made) != 0 || !slices.ContainsFunc(r.Lines, func(l pins.Line) bool { return l.Source == "go.dev" && l.Text == "not read: it did not answer" }) {
		t.Fatalf("lines %v", r.Lines)
	}
	// It is kept, with what it read, outside the bound.
	if last, err := pins.Last(context.Background(), k.d.Server); err != nil || last == nil || len(last.Lines) != len(r.Lines) {
		t.Fatalf("the look was not kept: %+v %v", last, err)
	}
}

// remoteOf makes a bare remote of the fixture's canonical repository, with commit made on top of main
// when change is not nil: versions.lock as change has it. diverged makes it a history of its own.
func (k *lookKit) remoteOf(change map[string]string, diverged bool) string {
	k.t.Helper()
	dir := k.t.TempDir()
	remote, work := filepath.Join(dir, "remote.git"), filepath.Join(dir, "work")
	git := func(in string, args ...string) {
		k.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = in
		cmd.Env = gitstore.CleanEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			k.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(dir, "clone", "-q", "--bare", k.git.Canonical("fixture"), remote)
	git(dir, "clone", "-q", remote, work)
	if diverged {
		git(work, "checkout", "-q", "--orphan", "other")
	}
	l := lock()
	for key, v := range change {
		l[key] = v
	}
	var b strings.Builder
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range l {
			if !yield(key) {
				return
			}
		}
	}) {
		b.WriteString(key + "=" + l[key] + "\n")
	}
	os.WriteFile(filepath.Join(work, "versions.lock"), []byte(b.String()), 0o644)
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "a pin moved on the remote")
	git(work, "push", "-q", "-f", "origin", "HEAD:main")
	return remote
}

// The look holds back a finding the remote's main pins already, while that follows main: Crew's project
// is paused, so the pipeline fetches nothing, and main on the machine lags what the operator pushed (on
// dave, 85fb1c9 behind the agy pin; design 0.58). Every value a ticket names still comes from main and
// the publishers: a remote that names another image makes no ticket of it (security review of
// 002e3a3..ef7a82a). A remote that does not answer, has no main or no versions.lock, or does not follow
// main changes nothing, and the look says so, naming no address; a running project's remote is read as
// the pipeline fetched it.
func TestTheLookHoldsBackWhatTheRemotePinsAlready(t *testing.T) {
	ctx := context.Background()
	npm := map[string]string{"CHROME_DEVTOOLS_MCP_VERSION": "1.11.0",
		"CHROME_DEVTOOLS_MCP_INTEGRITY": "sha512-CytJ/5tnzrS+J/74be2P5aCOsS/mmGRHys/Si+IYC4ucrbnWAmES/dYdxGJjeUCECRcoFUilZAysdjlwqUjXIA=="}
	evil := map[string]string{"SERVER_BASE_IMAGE": "evil/debian:trixie-slim@" + digest('e')}
	bare := func(k *lookKit) string {
		dir := filepath.Join(k.t.TempDir(), "empty.git")
		if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
			k.t.Fatalf("%v %s", err, out)
		}
		return dir
	}
	noLock := func(k *lookKit) string {
		r := k.remoteOf(npm, false)
		work := filepath.Join(k.t.TempDir(), "w")
		for _, args := range [][]string{{"clone", "-q", r, work}, {"-C", work, "rm", "-q", "versions.lock"},
			{"-C", work, "-c", "user.name=a", "-c", "user.email=a@x", "commit", "-q", "-m", "no lock"}, {"-C", work, "push", "-q", "origin", "HEAD:main"}} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				k.t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		return r
	}
	for name, c := range map[string]struct {
		remote  func(k *lookKit) string
		state   string
		fetched bool
		made    []string
		read    string
		readOK  bool
		bound   int64
	}{
		"ahead of main":         {func(k *lookKit) string { return k.remoteOf(npm, false) }, "paused", false, []string{}, "and the remote's main, ", true, 0},
		"naming another image":  {func(k *lookKit) string { return k.remoteOf(evil, false) }, "paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "and the remote's main, ", true, 0},
		"not following main":    {func(k *lookKit) string { return k.remoteOf(npm, true) }, "paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "does not follow main", false, 0},
		"not answering":         {func(k *lookKit) string { return filepath.Join(k.t.TempDir(), "none.git") }, "paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "could not be fetched", false, 0},
		"with no main":          {bare, "paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "the remote has no main", false, 0},
		"with no versions.lock": {noLock, "paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "has no versions.lock", false, 0},
		// Past the bound of a read it is no lock that pins nothing, since 1.04.
		"with a versions.lock past the bound": {func(k *lookKit) string {
			return k.remoteOf(map[string]string{"PAD": strings.Repeat("p", 64<<10)}, false)
		},
			"paused", false, []string{"chrome-devtools-mcp@1.11.0"}, "the remote's main's versions.lock could not be read", false, 32 << 10},
		"running, not fetched":             {func(k *lookKit) string { return k.remoteOf(npm, false) }, "running", false, []string{"chrome-devtools-mcp@1.11.0"}, "the remote has no main", false, 0},
		"running, fetched by the pipeline": {func(k *lookKit) string { return k.remoteOf(npm, false) }, "running", true, []string{}, "and the remote's main, ", true, 0},
	} {
		t.Run(name, func(t *testing.T) {
			k := newLookKit(t)
			main := k.git.Main(ctx, "fixture")
			remote := c.remote(k)
			if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET remote = $1, state = $2 WHERE name = 'fixture'`, remote, c.state); err != nil {
				t.Fatal(err)
			}
			if c.fetched {
				p, _ := k.b.Project(ctx, "fixture")
				if _, err := k.b.FetchMain(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			k.git.MaxOutput = c.bound
			r := k.once()
			if !slices.Equal(r.Made, c.made) {
				t.Fatalf("made %v, not %v", r.Made, c.made)
			}
			var read *pins.Line
			for i := range r.Lines {
				if r.Lines[i].Source == "Crew's project" {
					read = &r.Lines[i]
				}
				if strings.Contains(r.Lines[i].Text, filepath.Dir(remote)) || strings.Contains(r.Lines[i].Text, "evil") {
					t.Fatalf("a line names the remote or its values: %+v", r.Lines[i])
				}
			}
			if read == nil || !strings.Contains(read.Text, c.read) || read.OK != c.readOK {
				t.Fatalf("what the look read: %+v", r.Lines)
			}
			if k.git.Main(ctx, "fixture") != main {
				t.Fatal("main moved: the look merged the remote")
			}
			for _, title := range k.tickets("fixture") {
				if strings.Contains(title, "evil") {
					t.Fatalf("a ticket names the remote's image: %s", title)
				}
			}
		})
	}
}

// A remote main the operator left, refused for good (6.6), holds back nothing: its pins never reach
// main, so the look makes the ticket main's versions.lock asks for (round 2 of the security review of
// M16's close).
func TestARefusedRemoteMainHoldsBackNothing(t *testing.T) {
	ctx := context.Background()
	npm := map[string]string{"CHROME_DEVTOOLS_MCP_VERSION": "1.11.0",
		"CHROME_DEVTOOLS_MCP_INTEGRITY": "sha512-CytJ/5tnzrS+J/74be2P5aCOsS/mmGRHys/Si+IYC4ucrbnWAmES/dYdxGJjeUCECRcoFUilZAysdjlwqUjXIA=="}
	for _, state := range []string{"refused", "held"} {
		t.Run(state, func(t *testing.T) {
			k := newLookKit(t)
			main := k.git.Main(ctx, "fixture")
			remote := k.remoteOf(npm, false)
			out, err := exec.Command("git", "ls-remote", remote, "refs/heads/main").Output()
			if err != nil || len(out) < 40 {
				t.Fatalf("%v %s", err, out)
			}
			commit := string(out[:40])
			if _, err := k.d.Server.Exec(ctx, `UPDATE projects SET remote = $1, state = 'paused' WHERE name = 'fixture'`, remote); err != nil {
				t.Fatal(err)
			}
			if _, err := k.d.Server.Exec(ctx, `INSERT INTO remote_mains (project, remote, base, kind, commit, state, files)
				SELECT id, $1, $2, 'ahead', $1, $3, '{.github/workflows/x.yml}' FROM projects WHERE name = 'fixture'`, commit, main, state); err != nil {
				t.Fatal(err)
			}
			r := k.once()
			want := []string{}
			if state == "refused" {
				want = []string{"chrome-devtools-mcp@1.11.0"}
			}
			if !slices.Equal(r.Made, want) {
				t.Fatalf("made %v, not %v; %+v", r.Made, want, r.Lines)
			}
			if state == "refused" && !slices.ContainsFunc(r.Lines, func(l pins.Line) bool {
				return l.Source == "Crew's project" && strings.Contains(l.Text, "the operator refused the remote's main") && !l.OK
			}) {
				t.Fatalf("the lines: %+v", r.Lines)
			}
		})
	}
}
