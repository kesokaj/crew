// Package capacity is the room on the machine (7.9, R71; 13.10). Every sandbox Crew starts is counted
// at its size, small or large, or build for the build of a release (since 0.60); a run that does not
// fit waits for room, in the order freed room is given: the stage furthest right, then the project
// served least recently, then the priority, then the age. A large sandbox first in that order keeps
// its room, so small ones cannot starve it.
//
// Memory is counted strictly. CPU allowances are ceilings, so they may add up to cpu_overcommit times
// the cores. The machine's reserve is taken off both.
package capacity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/settings"
)

// The sizes of a sandbox (13.10, 14.1): small and large for runs, build for the build of a release and
// the gate's make check, whose export of the runtime image needs more memory than a large one has
// (since 0.60).
const (
	Small = "small"
	Large = "large"
	Build = "build"
)

// Kata's base: every microVM has 1 virtual CPU and 2 GB, and a size adds to it (14.1). Kata 3.31 gave
// it as its base VM; Kata 4 gives it as the overhead Crew's drop-in sets (deploy/lib/kata-config.sh).
const (
	baseCPUs  = 1
	baseMemMB = 2048
)

// stale is how long a wait stays in the queue when nobody asks for it again.
const stale = time.Minute

// SizeOf is the size of a run's sandbox by its role (7.9, since 0.47): large for the roles that build
// or run the project's code, and for the assistant of a conversation or a ticket's chat, which builds
// and runs it to find out (11.8, 11.9); small for the roles that only read.
func SizeOf(role string) string {
	switch role {
	case "builder", "tester", "e2e", "assistant":
		return Large
	}
	return Small
}

// Size is what a sandbox of one size is given, Kata's base included.
type Size struct {
	CPUs   float64
	MemMB  int64
	Docker bool
}

// Sizing is what the container is created with: what the size adds to Kata's base, which Kata adds
// to it (14.1). Memory 0 is no limit, which leaves the base.
func (s Size) Sizing() orchestrator.Sizing {
	return orchestrator.Sizing{NanoCPUs: int64((s.CPUs - baseCPUs) * 1e9), MemBytes: (s.MemMB - baseMemMB) << 20}
}

// Sizes reads the sizes from the machine's settings.
func Sizes(doc settings.Doc) map[string]Size {
	out := map[string]Size{}
	for _, name := range []string{Small, Large, Build} {
		m, _ := dig(doc, "sandbox_sizes", name).(settings.Doc)
		docker, _ := m["docker"].(bool)
		out[name] = Size{CPUs: baseCPUs + number(m["cpus"]), MemMB: baseMemMB + int64(number(m["memory_gb"])*1024), Docker: docker}
	}
	return out
}

// Room is what the machine gives out: its cores times the overcommit and its memory, the reserve taken
// off both.
type Room struct {
	CPUs  float64 `json:"cpus"`
	MemMB int64   `json:"memMB"`
}

// RoomOf is the room a machine of this size gives out under the settings.
func RoomOf(h orchestrator.Host, doc settings.Doc) Room {
	over := number(dig(doc, "cpu_overcommit"))
	if over <= 0 {
		over = 1
	}
	cpus := (float64(h.CPUs) - number(dig(doc, "machine_reserve", "cpus"))) * over
	mem := int64(h.MemMiB) - int64(number(dig(doc, "machine_reserve", "memory_gb"))*1024)
	return Room{CPUs: max(cpus, 0), MemMB: max(mem, 0)}
}

// Gate decides whether a sandbox may start now. The caller holds Mu from Take until what it started
// is recorded, so the next Take counts it.
type Gate struct {
	Pool *pgxpool.Pool
	// Host is the machine's size, read from Docker (13.10).
	Host func(ctx context.Context) (orchestrator.Host, error)
	// Settings are the machine's settings.
	Settings func(ctx context.Context) (settings.Doc, error)
	Mu       sync.Mutex
}

// Ask is a sandbox that waits for room.
type Ask struct {
	// Key names what the sandbox is for, the same on every pass while it waits.
	Key string
	// Owner is the loop that asks, which takes away its own asks that no longer wait; "" is none,
	// and the ask goes when nobody asked for it in a minute.
	Owner string
	Size  string
	// Rank is how far right its ticket's stage is (StageRank); work with no stage is 0.
	Rank int
	// Priority is the priority the operator set across projects, else normal; Own the ticket's own.
	Priority, Own int
	// Project is the project's id, or "" for none; Ticket the ticket's key; What says what it is.
	Project, Ticket, What string
	// ProjectVM is true when this ask runs as an ACP session inside the project's warm microVM (crew-proj-<id>).
	ProjectVM bool
	// Continues is true when this ask resumes an in-flight subtask (e.g. after a check sandbox completed).
	Continues bool
}

// stages are the fixed stages in their order (7.4); a project's own stage stands after Review.
var stages = []string{settings.StagePlanning, settings.StageBacklog, settings.StageAnalysis, settings.StageInProgress,
	"review", "test", "e2e", "verification", settings.StageDone}

// StageRank is how far right a stage is.
func StageRank(stage string) int {
	for i, s := range stages {
		if s == stage {
			return i + 1
		}
	}
	return 5 + 1 // a project's own stage
}

// Use is what is given out now, and what of it.
type Use struct {
	CPUs  float64 `json:"cpus"`
	MemMB int64   `json:"memMB"`
	// Runs are the agent runs among them.
	Runs       int `json:"runs"`
	ProjectVMs int `json:"projectVMs"`
	PausedVMs  int `json:"pausedVMs"`
	Large      int `json:"large"`
	Small      int `json:"small"`
	Checks     int `json:"checks"`
	Terminals  int `json:"terminals"`
	Hosted     int `json:"hosted"`
}

func (u *Use) add(s Size, n int) {
	u.CPUs += s.CPUs * float64(n)
	u.MemMB += s.MemMB * int64(n)
}

// sandboxKinds is the size of each kind of sandbox that is not a run's or a check's: those are counted
// from their runs and check runs, which exist before their microVM does. A conversation's and a chat's
// sandbox is a run's (plan M20 D32).
var sandboxKinds = map[string]string{
	"terminal": Large, "hosted": Large, "improver": Small, "setup": Small, "login-check": Small,
	// The build of a release and the gate's make check are check sandboxes with no check run: they are
	// counted from their sandbox, at a size of their own (since 0.60), and so is a project's build of
	// its image to host (since 0.72). A hosted project is counted at its row's size, its hosting.size,
	// large when the row has none.
	"build": Build, "host-build": Build,
}

// HostedRank is the rank of a hosted project's start in the queue for room: before every stage's, so
// hosted projects get their room before agent sandboxes do (19.3).
const HostedRank = 100

// projectVM is a project's warm microVM row in project_sandboxes (13.3, 13.10).
type projectVM struct {
	sandboxID string
	state     string
	cpus      float64
	memMB     int64
}

func loadProjectVMs(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) (byProject map[string]projectVM, activeSandboxIDs map[string]bool, err error) {
	byProject = map[string]projectVM{}
	activeSandboxIDs = map[string]bool{}
	rows, err := q.Query(ctx, `SELECT ps.project_id::text, ps.sandbox_id, ps.state,
		coalesce((st.doc->'sandbox'->>'cpus')::int, 6),
		coalesce((st.doc->'sandbox'->>'memory_gb')::int, 8)
		FROM project_sandboxes ps
		JOIN projects p ON p.id = ps.project_id
		LEFT JOIN settings st ON st.project = ps.project_id
		WHERE p.state <> 'archived'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, sid, state string
		var cpus, memGB int
		if err := rows.Scan(&pid, &sid, &state, &cpus, &memGB); err != nil {
			return nil, nil, err
		}
		if cpus < 2 {
			cpus = 6
		}
		if memGB < 2 {
			memGB = 8
		}
		byProject[pid] = projectVM{
			sandboxID: sid,
			state:     state,
			cpus:      float64(cpus),
			memMB:     int64(memGB) * 1024,
		}
		if (state == "running" || state == "paused") && sid != "" {
			activeSandboxIDs[sid] = true
		}
	}
	return byProject, activeSandboxIDs, rows.Err()
}

// Wait is a sandbox in the queue for room, for the Projects screen.
type Wait struct {
	Key       string    `json:"key"`
	Size      string    `json:"size"`
	Rank      int       `json:"-"`
	Priority  int       `json:"priority"`
	ProjectID string    `json:"-"`
	Project   string    `json:"project,omitempty"`
	Ticket    string    `json:"ticket,omitempty"`
	What      string    `json:"what"`
	Since     time.Time `json:"since"`
}

// Queue lists the sandboxes that wait for room, in the order freed room goes to them (7.9).
func Queue(ctx context.Context, pool *pgxpool.Pool) ([]Wait, error) {
	rows, err := pool.Query(ctx, `SELECT w.key, w.size, w.rank, w.priority, coalesce(w.project::text, ''), coalesce(p.name, ''), coalesce(w.ticket, ''), w.what, w.since
		FROM room_waits w LEFT JOIN projects p ON p.id = w.project
		LEFT JOIN LATERAL (SELECT max(r.started_at) AS last FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.project = w.project) served ON true
		WHERE w.seen_at >= now() - $1::interval
		ORDER BY (w.rank >= 99) DESC, w.continues DESC, w.rank DESC, served.last NULLS FIRST, w.project, w.priority, w.own_priority, w.since, w.key`,
		fmt.Sprintf("%d seconds", int(stale.Seconds())))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Wait, error) {
		var w Wait
		err := r.Scan(&w.Key, &w.Size, &w.Rank, &w.Priority, &w.ProjectID, &w.Project, &w.Ticket, &w.What, &w.Since)
		return w, err
	})
}

// Take writes the ask into the queue, or marks it seen again, and says whether its sandbox may start
// now: it fits in what is free after every ask before it in the queue. An ask before it that does not
// fit keeps its room too, so a large sandbox is not starved by small ones. The caller holds Mu, starts
// the sandbox when this says yes, records it, and then calls Taken.
func (g *Gate) Take(ctx context.Context, ask Ask) (bool, error) {
	if ask.Size != Large && ask.Size != Build {
		ask.Size = Small
	}
	byProject, _, err := loadProjectVMs(ctx, g.Pool)
	if err != nil {
		return false, err
	}
	// An ACP session in an already-running project microVM needs 0 additional host CPUs or RAM.
	if ask.ProjectVM && ask.Project != "" {
		if vm, ok := byProject[ask.Project]; ok && vm.state == "running" {
			_, _ = g.Pool.Exec(ctx, `DELETE FROM room_waits WHERE key = $1`, ask.Key)
			return true, nil
		}
	}
	var project *string
	if ask.Project != "" {
		project = &ask.Project
	}
	if _, err := g.Pool.Exec(ctx, `INSERT INTO room_waits (key, owner, size, rank, continues, priority, own_priority, project, ticket, what)
		VALUES ($1, $2, $3, $4, $10, $5, $6, $7, nullif($8, ''), $9)
		ON CONFLICT (key) DO UPDATE SET owner = $2, size = $3, rank = $4, continues = $10, priority = $5, own_priority = $6, project = $7,
			ticket = nullif($8, ''), what = $9, seen_at = now()`,
		ask.Key, ask.Owner, ask.Size, ask.Rank, ask.Priority, ask.Own, project, ask.Ticket, ask.What, ask.Continues); err != nil {
		return false, err
	}
	free, sizes, err := g.free(ctx)
	if err != nil {
		return false, err
	}
	waits, err := Queue(ctx, g.Pool)
	if err != nil {
		return false, err
	}
	wokenProjects := map[string]bool{}
	for _, w := range waits {
		s := sizes[w.Size]
		var isProjVM bool
		if w.Key == ask.Key {
			isProjVM = ask.ProjectVM
		} else {
			isProjVM = (strings.HasPrefix(w.Key, "run:") || strings.HasPrefix(w.Key, "cont:")) &&
				!strings.HasPrefix(w.What, "judge ") && !strings.HasPrefix(w.What, "improver ")
		}
		if isProjVM && w.ProjectID != "" {
			if vm, ok := byProject[w.ProjectID]; ok {
				switch {
				case vm.state == "running" || wokenProjects[w.ProjectID]:
					s = Size{}
				case vm.state == "paused":
					s = Size{CPUs: vm.cpus}
					wokenProjects[w.ProjectID] = true
				default:
					s = Size{CPUs: vm.cpus, MemMB: vm.memMB}
					wokenProjects[w.ProjectID] = true
				}
			}
		}
		if w.Key == ask.Key {
			return s.CPUs <= free.CPUs && s.MemMB <= free.MemMB, nil
		}
		// What is before it takes its room, whether it fits now or keeps it until it does.
		free.CPUs -= s.CPUs
		free.MemMB -= s.MemMB
	}
	return false, nil
}

// free is the room not given out now.
func (g *Gate) free(ctx context.Context) (Room, map[string]Size, error) {
	doc, err := g.Settings(ctx)
	if err != nil {
		return Room{}, nil, err
	}
	h, err := g.Host(ctx)
	if err != nil {
		return Room{}, nil, err
	}
	sizes := Sizes(doc)
	u, err := Used(ctx, g.Pool, sizes)
	if err != nil {
		return Room{}, nil, err
	}
	r := RoomOf(h, doc)
	return Room{CPUs: r.CPUs - u.CPUs, MemMB: r.MemMB - u.MemMB}, sizes, nil
}

// Taken takes away the ask of a sandbox that started.
func (g *Gate) Taken(ctx context.Context, key string) error {
	_, err := g.Pool.Exec(ctx, `DELETE FROM room_waits WHERE key = $1`, key)
	return err
}

// Sweep takes away the asks of one loop that it did not write again since a time, and every ask
// nobody wrote again in a minute: they no longer wait.
func (g *Gate) Sweep(ctx context.Context, owner string, since time.Time) error {
	_, err := g.Pool.Exec(ctx, `DELETE FROM room_waits WHERE (owner = $1 AND seen_at < $2) OR seen_at < now() - $3::interval`,
		owner, since, fmt.Sprintf("%d seconds", int(stale.Seconds())))
	return err
}

// SizeFor is the size a role's sandbox is given under the machine's settings.
func (g *Gate) SizeFor(ctx context.Context, size string) (Size, error) {
	doc, err := g.Settings(ctx)
	if err != nil {
		return Size{}, err
	}
	return Sizes(doc)[size], nil
}

// Report is the room on the machine for the Projects screen (7.9, 21: "The machine is full").
type Report struct {
	// Cores and MemMB are the machine's, as Docker reads them.
	Cores   int    `json:"cores"`
	MemMB   int64  `json:"memMB"`
	Room    Room   `json:"room"`
	Used    Use    `json:"used"`
	Waiting []Wait `json:"waiting"`
}

// Report reads the room on the machine and what waits for it.
func (g *Gate) Report(ctx context.Context) (Report, error) {
	var rep Report
	doc, err := g.Settings(ctx)
	if err != nil {
		return rep, err
	}
	h, err := g.Host(ctx)
	if err != nil {
		return rep, err
	}
	rep.Cores, rep.MemMB, rep.Room = h.CPUs, int64(h.MemMiB), RoomOf(h, doc)
	if rep.Used, err = Used(ctx, g.Pool, Sizes(doc)); err != nil {
		return rep, err
	}
	if rep.Waiting, err = Queue(ctx, g.Pool); err != nil {
		return rep, err
	}
	if rep.Waiting == nil {
		rep.Waiting = []Wait{}
	}
	return rep, nil
}

// Waits says a wait for room in words, for a card.
func Waits(size string) string {
	return "waits for room on the machine for a " + strings.ToLower(size) + " sandbox"
}

func dig(d settings.Doc, path ...string) any {
	var cur any = d
	for _, k := range path {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func number(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
