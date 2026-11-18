package capacity_test

import (
	"context"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

const fixture = "f1c50000-0000-4000-8000-000000000001"

// The sizes and the room of dave as 13.10 counts them: a small sandbox is 2 virtual CPUs and 2 GB, a
// large one 4 and 6 GB; dave's 16 threads and 62 GiB, less the reserve of 2 and 8 GB (4 GB before
// 1.08), give out 28 virtual CPUs at the overcommit of 2, and about 54 GB.
func TestTheSizesAndTheRoomOfTheDesign(t *testing.T) {
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	sizes := capacity.Sizes(doc)
	if s := sizes[capacity.Small]; s.CPUs != 2 || s.MemMB != 2048 || s.Docker {
		t.Fatalf("small: %+v", s)
	}
	if s := sizes[capacity.Large]; s.CPUs != 4 || s.MemMB != 6144 || !s.Docker {
		t.Fatalf("large: %+v", s)
	}
	// What the container is given is what the size adds to Kata's base.
	if z := sizes[capacity.Large].Sizing(); z.NanoCPUs != 3e9 || z.MemBytes != 4<<30 {
		t.Fatalf("large's sizing: %+v", z)
	}
	if z := sizes[capacity.Small].Sizing(); z.NanoCPUs != 1e9 || z.MemBytes != 0 {
		t.Fatalf("small's sizing: %+v", z)
	}
	// The build of a release and the gate's make check: a large sandbox's CPUs with 6 GB over the
	// base, which the export of the runtime image needs (L5, design 0.60).
	if s := sizes[capacity.Build]; s.CPUs != 4 || s.MemMB != 8192 || !s.Docker {
		t.Fatalf("build: %+v", s)
	}
	if z := sizes[capacity.Build].Sizing(); z.NanoCPUs != 3e9 || z.MemBytes != 6<<30 {
		t.Fatalf("build's sizing: %+v", z)
	}
	r := capacity.RoomOf(orchestrator.Host{CPUs: 16, MemMiB: 62 * 1024}, doc)
	if r.CPUs != 28 || r.MemMB != 54*1024 {
		t.Fatalf("dave's room: %+v", r)
	}
	for role, want := range map[string]string{"builder": capacity.Large, "tester": capacity.Large, "e2e": capacity.Large,
		"lead": capacity.Small, "reviewer": capacity.Small, "verifier": capacity.Small, "judge": capacity.Small, "summary": capacity.Small,
		// A conversation's and a ticket's chat (11.8, 11.9, crew.yaml's sandbox_sizes; plan M20 D16).
		"assistant": capacity.Large} {
		if got := capacity.SizeOf(role); got != want {
			t.Errorf("%s is %s, want %s", role, got, want)
		}
	}
}

type room struct {
	t    *testing.T
	ctx  context.Context
	db   *dbtest.DB
	gate *capacity.Gate
}

// newRoom is a machine of 6 cores and 20 GB: with the default reserve and overcommit, 8 virtual CPUs
// and 12 GB are given out, so one large and one small sandbox fit, or four small.
func newRoom(t *testing.T) *room {
	d := dbtest.New(t, false)
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	g := &capacity.Gate{Pool: d.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: 6, MemMiB: 20 * 1024}, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return doc, nil }}
	return &room{t: t, ctx: context.Background(), db: d, gate: g}
}

// running records a run that goes, at a size.
func (r *room) running(size string) {
	r.t.Helper()
	if _, err := r.db.Server.Exec(r.ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			VALUES ('project', $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE project = $1 AND ticket IS NULL), 'run', 'builder') RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, size, started_at)
		SELECT gen_random_uuid(), id, 1, 'working', 'agy', 'v', 'img', $2, now() FROM s`, fixture, size); err != nil {
		r.t.Fatal(err)
	}
}

func (r *room) take(ask capacity.Ask) bool {
	r.t.Helper()
	ok, err := r.gate.Take(r.ctx, ask)
	if err != nil {
		r.t.Fatal(err)
	}
	return ok
}

// Freed room goes to the run in the stage furthest right: a small sandbox that would fit waits when a
// large one further right comes first, and takes the room once that one started.
func TestFreedRoomGoesToTheStageFurthestRight(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Large) // 4 of 8 virtual CPUs and 6 of 12 GB are left
	analysis := capacity.Ask{Key: "a", Size: capacity.Small, Rank: capacity.StageRank("analysis"), What: "Analyse"}
	test := capacity.Ask{Key: "t", Size: capacity.Large, Rank: capacity.StageRank("test"), What: "Test"}
	if !r.take(analysis) {
		t.Fatal("alone, the small sandbox fits")
	}
	if !r.take(test) {
		t.Fatal("the large sandbox in Test is first, and fits")
	}
	if r.take(analysis) {
		t.Fatal("the small sandbox took the room of the large one before it")
	}
	// The large one starts, and its run is counted: the small one still does not fit.
	r.running(capacity.Large)
	if err := r.gate.Taken(r.ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if r.take(analysis) {
		t.Fatal("a small sandbox started on a full machine")
	}
	q, err := capacity.Queue(r.ctx, r.db.Server)
	if err != nil || len(q) != 1 || q[0].Key != "a" {
		t.Fatalf("what waits: %+v %v", q, err)
	}
}

// A large sandbox that does not fit keeps its room: a small one after it does not take what is free,
// so small ones cannot starve it (7.9).
func TestALargeSandboxThatWaitsKeepsItsRoom(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Large)
	r.running(capacity.Small) // 2 virtual CPUs and 4 GB are left: room for one more small
	review := capacity.Ask{Key: "r", Size: capacity.Small, Rank: capacity.StageRank("review"), What: "Review"}
	if !r.take(review) {
		t.Fatal("the small one fits while nothing is before it")
	}
	verify := capacity.Ask{Key: "v", Size: capacity.Large, Rank: capacity.StageRank("verification"), What: "Verify"}
	if r.take(verify) {
		t.Fatal("a large sandbox started where only a small one fits")
	}
	if r.take(review) {
		t.Fatal("the small one took the room the large one waits for")
	}
}

// In the same stage, the project served least recently goes first; then the priority, then the age.
func TestInOneStageTheProjectServedLeastRecentlyGoesFirst(t *testing.T) {
	r := newRoom(t)
	var other string
	if err := r.db.Server.QueryRow(r.ctx, `INSERT INTO projects (id, name, key, goal, state, repo_source) VALUES (gen_random_uuid(), 'other', 'OT', 'g', 'running', 'local')
		RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	// The fixture project was served a moment ago; the other never.
	r.running(capacity.Large)
	r.running(capacity.Small) // room for one small
	served := capacity.Ask{Key: "served", Size: capacity.Small, Rank: 4, Project: fixture, Priority: 2, What: "x"}
	never := capacity.Ask{Key: "never", Size: capacity.Small, Rank: 4, Project: other, Priority: 2, What: "y"}
	r.take(served)
	if !r.take(never) || r.take(served) {
		t.Fatal("the project never served did not go first")
	}
	// Your priority comes before the age, within the project's turn.
	urgent := capacity.Ask{Key: "urgent", Size: capacity.Small, Rank: 4, Project: other, Priority: 0, What: "z"}
	if !r.take(urgent) || r.take(never) {
		t.Fatal("an urgent ticket did not go before a normal one of the same project")
	}
	// Then the age: of two asks alike but for when they began to wait, the older goes first, though
	// its key sorts after the other's.
	if err := r.gate.Taken(r.ctx, "urgent"); err != nil {
		t.Fatal(err)
	}
	r.gate.Taken(r.ctx, "never")
	r.gate.Taken(r.ctx, "served")
	older := capacity.Ask{Key: "z-older", Size: capacity.Small, Rank: 4, Project: other, Priority: 2, What: "o"}
	r.take(older)
	time.Sleep(10 * time.Millisecond)
	newer := capacity.Ask{Key: "a-newer", Size: capacity.Small, Rank: 4, Project: other, Priority: 2, What: "n"}
	if r.take(newer) || !r.take(older) {
		t.Fatal("the ask that waited longer did not go first")
	}
}

// What is counted: runs that go, at their size, check runs that go as large, and the sandboxes of
// other kinds; a run that ended, a run idle between its turns, and a sandbox removed are not.
func TestWhatIsCounted(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Large)
	r.running(capacity.Small)
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE runs SET state = 'ended' WHERE size = 'small'`); err != nil {
		t.Fatal(err)
	}
	// A run idle between its turns waits, such as for its own checks: it is not counted, so those
	// checks do not wait for its room.
	r.running(capacity.Large)
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE runs SET state = 'idle' WHERE id = (SELECT id FROM runs ORDER BY started_at DESC LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image) VALUES
		('crew-t1', 'terminal', $1, 'c1', 'img'), ('crew-l1', 'login-check', NULL, 'c2', 'img'), ('crew-r1', 'run', $1, 'c3', 'img')`, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = 'crew-l1'`); err != nil {
		t.Fatal(err)
	}
	// A check run, a check of the remote's main, and a check of the setup on main, each as it runs,
	// and one of each that ended.
	for _, q := range []string{
		`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position) VALUES ($1, 1, 'One', 'fix', 'analysis', 'plan', 'lead', 1)`,
		`INSERT INTO check_runs (project, ticket, purpose, commit, state) SELECT $1, id, 'base', repeat('a', 40), 'running' FROM tickets WHERE project = $1
			UNION ALL SELECT $1, id, 'base', repeat('b', 40), 'passed' FROM tickets WHERE project = $1`,
		`INSERT INTO remote_mains (project, remote, base, kind, commit, state) VALUES ($1, repeat('c', 40), repeat('d', 40), 'ahead', repeat('c', 40), 'checking')`,
		`INSERT INTO setup_checks (project, commit, through, state) VALUES ($1, repeat('e', 40), now(), 'checking'), ($1, repeat('f', 40), now(), 'failed')`,
	} {
		if _, err := r.db.Server.Exec(r.ctx, q, fixture); err != nil {
			t.Fatal(err)
		}
	}
	doc, _ := r.gate.Settings(r.ctx)
	u, err := capacity.Used(r.ctx, r.db.Server, capacity.Sizes(doc))
	if err != nil {
		t.Fatal(err)
	}
	// The large run, the terminal, and the three checks; the run's own sandbox row is its run's,
	// counted once.
	if u.Large != 5 || u.Small != 0 || u.Runs != 1 || u.Checks != 3 || u.Terminals != 1 || u.CPUs != 20 || u.MemMB != 30*1024 {
		t.Fatalf("used: %+v", u)
	}
	rep, err := r.gate.Report(r.ctx)
	if err != nil || rep.Room.CPUs != 8 || rep.Cores != 6 || rep.Waiting == nil {
		t.Fatalf("report: %+v %v", rep, err)
	}
}

// A conversation's run idle between the operator's messages keeps its large microVM up to
// conversations.idle_minutes, so it is counted, unlike a run idle for its checks; a chat's sandbox
// row is its run's, counted once (plan M20 D32).
func TestAnIdleConversationIsCounted(t *testing.T) {
	r := newRoom(t)
	if _, err := r.db.Server.Exec(r.ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			VALUES ('conversation', $1, 1, 'talk', 'assistant') RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, size, sandbox)
		SELECT gen_random_uuid(), id, 1, 'idle', 'agy', 'v', 'img', 'large', NULL FROM s`, fixture); err != nil {
		t.Fatal(err)
	}
	// A run idle for its checks, which is not counted.
	r.running(capacity.Large)
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE runs SET state = 'idle' WHERE subtask IN (SELECT id FROM subtasks WHERE scope = 'project')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image) VALUES
		('crew-chat1', 'chat', $1, 'c1', 'img'), ('crew-conv1', 'conversation', $1, 'c2', 'img')`, fixture); err != nil {
		t.Fatal(err)
	}
	doc, _ := r.gate.Settings(r.ctx)
	u, err := capacity.Used(r.ctx, r.db.Server, capacity.Sizes(doc))
	if err != nil {
		t.Fatal(err)
	}
	if u.Large != 1 || u.Small != 0 || u.Runs != 1 || u.CPUs != 4 || u.MemMB != 6*1024 {
		t.Fatalf("used: %+v", u)
	}
}

// A build's sandbox holds the room of its own size while it runs, and is counted with the checks and
// among the sandboxes with Docker inside (review of M16 P3: a build was in no count; since 0.60 it has
// a size of its own).
func TestABuildIsCountedAtItsSizeAsACheck(t *testing.T) {
	r := newRoom(t)
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image, mode) VALUES
		('build-1', 'build', NULL, 'c1', 'img', 'sources'), ('build-2', 'build', NULL, 'c2', 'img', 'sources')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = 'build-2'`); err != nil {
		t.Fatal(err)
	}
	doc, _ := r.gate.Settings(r.ctx)
	sizes := capacity.Sizes(doc)
	u, err := capacity.Used(r.ctx, r.db.Server, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if u.Large != 1 || u.Checks != 1 || u.MemMB != sizes[capacity.Build].MemMB || u.CPUs != sizes[capacity.Build].CPUs {
		t.Fatalf("%+v", u)
	}
}

// A build that waits for room is held at its own size in the queue, never as a small one: on a machine
// giving out 12 GB, with a large sandbox's 6 GB taken, the build's 8 GB does not fit, and a small one
// asked after it, with no stage either, does not take the room it waits for.
func TestABuildWaitsForRoomAtItsSize(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Large)
	build := capacity.Ask{Key: "gate:0.2.28", Size: capacity.Build, What: "the gate's make check of Crew 0.2.28"}
	if r.take(build) {
		t.Fatal("a build started where only 6 GB are left")
	}
	if r.take(capacity.Ask{Key: "s", Size: capacity.Small, What: "a setup check"}) {
		t.Fatal("the small one took the room the build waits for")
	}
	waits, err := capacity.Queue(r.ctx, r.db.Server)
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0].Key != "gate:0.2.28" || waits[0].Size != capacity.Build {
		t.Fatalf("%+v", waits)
	}
}

// A hosted project is counted at its row's size, its hosting.size, large when the row has none, and a
// project's build of its image at the build's size, with the checks (19.3, plan M19 Capacity).
func TestAHostedProjectIsCountedAtItsRowsSize(t *testing.T) {
	r := newRoom(t)
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image, size) VALUES
		('host-a-1', 'hosted', $1, 'c1', 'img', 'small'), ('host-a-2', 'hosted', $1, 'c2', 'img', 'large'),
		('host-a-3', 'hosted', $1, 'c3', 'img', NULL), ('host-build-1', 'host-build', $1, 'c4', 'img', NULL)`, fixture); err != nil {
		t.Fatal(err)
	}
	doc, _ := r.gate.Settings(r.ctx)
	sizes := capacity.Sizes(doc)
	u, err := capacity.Used(r.ctx, r.db.Server, sizes)
	if err != nil {
		t.Fatal(err)
	}
	want := sizes[capacity.Small].MemMB + 2*sizes[capacity.Large].MemMB + sizes[capacity.Build].MemMB
	if u.Hosted != 3 || u.Small != 1 || u.Large != 3 || u.Checks != 1 || u.MemMB != want {
		t.Fatalf("used: %+v, want %d MB", u, want)
	}
}

// A hosted start takes room before every run that waits, whatever its stage (19.3: hosted projects
// get their room before agent sandboxes do).
func TestAHostedStartTakesRoomBeforeAWaitingRun(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Large) // room for one more large
	verify := capacity.Ask{Key: "v", Size: capacity.Large, Rank: capacity.StageRank("verification"), What: "Verify"}
	if !r.take(verify) {
		t.Fatal("alone, the run fits")
	}
	hosted := capacity.Ask{Key: "host:shop:2", Size: capacity.Large, Rank: capacity.HostedRank, What: "hosting shop"}
	if !r.take(hosted) {
		t.Fatal("the hosted start did not come first")
	}
	if r.take(verify) {
		t.Fatal("the run took the room the hosted start was given")
	}
}

// A project's warm microVM (crew-proj-<id>) is counted once from project_sandboxes at its configured
// sandbox.{cpus, memory_gb} when running, at 0 CPUs and its memory when paused, and at 0 when stopped;
// concurrent ACP sessions inside it do not add CPU or RAM a second time, and Gate.Take lets additional
// sessions in a running project microVM start immediately (13.3, 13.10, R71).
func TestProjectSandboxesAreCountedOnceAndPausedOrStoppedUseLessRoom(t *testing.T) {
	r := newRoom(t)
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO settings (project, doc, updated_by)
		VALUES ($1::uuid, '{"sandbox":{"cpus":6,"memory_gb":8,"disk_gb":30}}'::jsonb, 'op@example.com')`, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image) VALUES
		('proj-shop-1', 'run', $1, 'crew-proj-shop-1', 'img'),
		('judge-sb-1', 'run', $1, 'crew-judge-sb-1', 'img')`, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, active_sessions)
		VALUES ($1::uuid, 'proj-shop-1', 'running', '/tmp/home', 2)`, fixture); err != nil {
		t.Fatal(err)
	}
	// Two runs inside crew-proj-shop-1 (one large builder, one small reviewer) plus one throwaway judge run.
	for i, tc := range []struct {
		role, size, sb string
	}{
		{"builder", capacity.Large, "proj-shop-1"},
		{"reviewer", capacity.Small, ""},
		{"judge", capacity.Small, "judge-sb-1"},
	} {
		if _, err := r.db.Server.Exec(r.ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
				VALUES ('project', $1, $2, $3, $3) RETURNING id)
			INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, size, sandbox)
			SELECT gen_random_uuid(), id, 1, 'working', 'agy', 'v', 'img', $4, nullif($5, '') FROM s`,
			fixture, i+1, tc.role, tc.size, tc.sb); err != nil {
			t.Fatal(err)
		}
	}

	doc, _ := r.gate.Settings(r.ctx)
	sizes := capacity.Sizes(doc)
	u, err := capacity.Used(r.ctx, r.db.Server, sizes)
	if err != nil {
		t.Fatal(err)
	}
	// 6 CPUs + 8192 MB from the running project microVM, plus 2 CPUs + 2048 MB from the throwaway judge VM.
	if u.ProjectVMs != 1 || u.PausedVMs != 0 || u.Runs != 3 || u.CPUs != 8 || u.MemMB != 10*1024 {
		t.Fatalf("running project VM + judge used: %+v", u)
	}

	// While crew-proj-shop-1 is running (using all 8 CPUs with the judge on an 8-CPU room), another
	// ACP session in the same project needs 0 extra host CPU/RAM and starts immediately.
	if !r.take(capacity.Ask{Key: "run:extra", Size: capacity.Large, Project: fixture, ProjectVM: true, What: "extra session"}) {
		t.Fatal("expected additional ACP session in already-running project VM to fit with 0 extra room")
	}
	// A throwaway judge microVM (ProjectVM: false) in the same project still requires sizes[Small] (2 CPUs)
	// and must wait when 0 CPUs are free.
	if r.take(capacity.Ask{Key: "run:judge-2", Size: capacity.Small, Project: fixture, ProjectVM: false, What: "judge check"}) {
		t.Fatal("expected throwaway judge microVM (ProjectVM: false) to require sizes[Small] and wait when 0 CPUs are free")
	}
	_ = r.gate.Taken(r.ctx, "run:judge-2")

	// Transition crew-proj-shop-1 to paused (and end the runs): 0 CPUs from the paused VM, 8192 MB RAM retained.
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE runs SET state = 'ended'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE project_sandboxes SET state = 'paused', active_sessions = 0 WHERE project_id = $1::uuid`, fixture); err != nil {
		t.Fatal(err)
	}
	u, err = capacity.Used(r.ctx, r.db.Server, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if u.ProjectVMs != 0 || u.PausedVMs != 1 || u.CPUs != 0 || u.MemMB != 8*1024 {
		t.Fatalf("paused project VM used: %+v", u)
	}
	// Waking the paused VM (8 GB already allocated out of 12 GB room; only 4 GB free, but waking needs 0 extra RAM and 6 CPUs out of 8 free CPUs) succeeds!
	if !r.take(capacity.Ask{Key: "run:wake", Size: capacity.Large, Project: fixture, ProjectVM: true, What: "wake paused VM"}) {
		t.Fatal("expected waking a paused project VM to require 0 additional RAM and fit in 6/8 free CPUs")
	}
	_ = r.gate.Taken(r.ctx, "run:wake")

	// Transition crew-proj-shop-1 to stopped: 0 CPUs and 0 MB RAM.
	if _, err := r.db.Server.Exec(r.ctx, `UPDATE project_sandboxes SET state = 'stopped', sandbox_id = '' WHERE project_id = $1::uuid`, fixture); err != nil {
		t.Fatal(err)
	}
	u, err = capacity.Used(r.ctx, r.db.Server, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if u.ProjectVMs != 0 || u.PausedVMs != 0 || u.CPUs != 0 || u.MemMB != 0 {
		t.Fatalf("stopped project VM used: %+v", u)
	}
	// With 8 GB taken by a build sandbox (leaving 4 GB free out of 12 GB), waking the 8 GB stopped project VM
	// must wait, whereas a throwaway 2 GB small judge VM (ProjectVM: false) in the same project fits.
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO sandboxes (id, kind, project, container, image, mode) VALUES
		('build-hold', 'build', NULL, 'c-b', 'img', 'sources')`); err != nil {
		t.Fatal(err)
	}
	if !r.take(capacity.Ask{Key: "run:judge-3", Size: capacity.Small, Project: fixture, ProjectVM: false, What: "judge check"}) {
		t.Fatal("expected 2 GB throwaway judge microVM (ProjectVM: false) to fit in 4 GB free room when project VM is stopped")
	}
	_ = r.gate.Taken(r.ctx, "run:judge-3")
	if r.take(capacity.Ask{Key: "run:boot-vm", Size: capacity.Small, Project: fixture, ProjectVM: true, What: "lead session"}) {
		t.Fatal("expected 8 GB stopped project VM (ProjectVM: true) to wait when only 4 GB are free")
	}
}
