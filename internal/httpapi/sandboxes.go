package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The Agents screen of a project (20.1, agents.png) and the top-level Sandboxes screen (/sandboxes):
// every sandbox that works in it with its agent, model, ticket, and step; what waits for an
// active-ticket slot, for a lead slot, for room, for a limit, or for the setup's fix; and the runs
// that ended last.
func (s *Server) sandboxRoutes(api *routes) {
	api.HandleFunc("GET /api/sandboxes", s.allSandboxes)
	api.HandleFunc("GET /api/projects/{p}/sandboxes", s.projectSandboxes)
	api.HandleFunc("GET /api/projects/{p}/sandbox", s.getProjectSandboxStatus)
	api.HandleFunc("POST /api/projects/{p}/sandbox/start", s.startProjectSandbox)
	api.HandleFunc("POST /api/projects/{p}/sandbox/suspend", s.suspendProjectSandbox)
	api.HandleFunc("POST /api/projects/{p}/sandbox/stop", s.stopProjectSandbox)
	api.HandleFunc("POST /api/projects/{p}/sandbox/snapshot", s.snapshotProjectSandbox)
	api.HandleFunc("POST /api/projects/{p}/sandbox/snapshots/{id}/restore", s.restoreProjectSandboxSnapshot)
	api.HandleFunc("POST /api/projects/{p}/sandbox/build-image", s.buildProjectSandboxImage)
}

// AgentRun is a run on the Agents screen.
type AgentRun struct {
	Run     string     `json:"run"`
	Role    string     `json:"role"`
	CLI     string     `json:"cli"`
	Model   *string    `json:"model"`
	Effort  *string    `json:"effort"`
	Subtask string     `json:"subtask"`
	Ticket  *string    `json:"ticket"`
	Title   string     `json:"title"`
	Sandbox *string    `json:"sandbox"`
	Size    *string    `json:"size"`
	State   string     `json:"state"`
	Started *time.Time `json:"startedAt"`
	Ended   *time.Time `json:"endedAt,omitempty"`
	Tokens  int64      `json:"tokens"`
	// Steps are the run's own checklist (set_steps): how many, how many are done, and the next.
	Steps     int    `json:"steps"`
	StepsDone int    `json:"stepsDone"`
	Step      string `json:"step,omitempty"`
	// Result says how an ended run ended, in words.
	Result string `json:"result,omitempty"`
}

// AgentWait is what waits in the project, and for what.
type AgentWait struct {
	What  string    `json:"what"`
	For   string    `json:"for"`
	Since time.Time `json:"since"`
}

func (s *Server) projectSandboxes(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	runs := func(where, order string) ([]AgentRun, error) {
		rows, err := b.Pool.Query(ctx, `SELECT r.id::text, s.role, r.cli, coalesce(r.model_name, r.model), r.effort,
				CASE WHEN t.id IS NULL THEN s.kind ELSE $2 || '-' || t.number || '.' || s.number END,
				CASE WHEN t.id IS NULL THEN NULL ELSE $2 || '-' || t.number END, coalesce(s.title, s.kind), r.sandbox, r.size, r.state,
				r.started_at, r.ended_at, r.tokens_in + r.tokens_out,
				(SELECT count(*) FROM subtask_steps WHERE subtask = s.id), (SELECT count(*) FROM subtask_steps WHERE subtask = s.id AND done_at IS NOT NULL),
				coalesce((SELECT text FROM subtask_steps WHERE subtask = s.id AND done_at IS NULL ORDER BY position LIMIT 1), ''),
				coalesce(r.stop_reason, ''), coalesce(s.reported->>'tool', ''), coalesce(s.reported->'decision'->>'choice', ''),
				coalesce((SELECT coalesce(verdict, result, '') FROM records WHERE run = r.id AND kind IN ('review', 'test', 'e2e', 'verify', 'stage') LIMIT 1), '')
			FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN tickets t ON t.id = s.ticket
			WHERE s.project = $1 AND `+where+` ORDER BY `+order+` LIMIT 20`, p.ID, p.Key)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AgentRun, error) {
			var a AgentRun
			var reason, tool, choice, verdict string
			err := row.Scan(&a.Run, &a.Role, &a.CLI, &a.Model, &a.Effort, &a.Subtask, &a.Ticket, &a.Title, &a.Sandbox, &a.Size, &a.State,
				&a.Started, &a.Ended, &a.Tokens, &a.Steps, &a.StepsDone, &a.Step, &reason, &tool, &choice, &verdict)
			if a.State == "ended" {
				a.Result = resultWords(reason, tool, choice, verdict)
			}
			return a, err
		})
	}
	running, err := runs(`r.state IN ('starting', 'working', 'idle')`, `r.started_at`)
	if err != nil {
		s.boardError(w, "reading the runs", err)
		return
	}
	finished, err := runs(`r.state = 'ended'`, `r.ended_at DESC NULLS LAST`)
	if err != nil {
		s.boardError(w, "reading the runs", err)
		return
	}
	waits, err := projectWaits(ctx, b.Pool, p.ID, p.Key)
	if err != nil {
		s.boardError(w, "reading what waits", err)
		return
	}
	if running == nil {
		running = []AgentRun{}
	}
	if finished == nil {
		finished = []AgentRun{}
	}
	mDoc, _ := s.machineDoc(ctx, b.Pool)
	pDoc, _ := b.Settings(ctx, b.Pool, p)
	projActive, projMax, _ := pipeline.ProjectActiveTickets(ctx, b.Pool, p.ID, pDoc)
	machActive, _ := pipeline.ActiveTicketsOf(ctx, b.Pool, mDoc)
	writeJSON(w, http.StatusOK, map[string]any{
		"running":  running,
		"waiting":  waits,
		"finished": finished,
		"activeTickets": map[string]any{
			"active":        machActive.Active,
			"max":           machActive.Max,
			"set":           machActive.Set,
			"projectActive": projActive,
			"projectMax":    projMax,
		},
	})
}

// projectWaits are the project's lead runs that wait for a slot, its sandboxes that wait for room,
// its subtasks that wait for a limit, a sign-in, the setup's fix, or Resume, and its tickets that wait
// for their turn on a group of models close to its limit or for an active-ticket slot.
func projectWaits(ctx context.Context, q *pgxpool.Pool, project, key string) ([]AgentWait, error) {
	rows, err := q.Query(ctx, `
		SELECT coalesce(ticket || ' ', '') || what, CASE WHEN class = 'ticket' THEN 'a run slot of ' || cli ELSE 'a lead slot of ' || cli END, since FROM lead_waits WHERE project = $1
		UNION ALL
		SELECT coalesce(ticket || ' ', '') || what, 'room on the machine for a ' || size || ' sandbox', since FROM room_waits
			WHERE project = $1 AND seen_at > now() - interval '1 minute'
		UNION ALL
		SELECT CASE WHEN t.id IS NULL THEN s.title ELSE $2 || '-' || t.number || '.' || s.number || ' ' || coalesce(s.title, s.kind) END,
			CASE WHEN s.wait_for LIKE 'quota:%' THEN 'the limit of its group of models' || coalesce(' until ' || to_char(s.wait_until, 'HH24:MI'), '')
				WHEN s.wait_for LIKE 'login:%' THEN 'a sign-in of ' || substr(s.wait_for, 7)
				WHEN s.wait_for = 'setup' THEN 'the project''s setup, which fails'
				ELSE 'Resume' END, s.updated_at
		FROM subtasks s LEFT JOIN tickets t ON t.id = s.ticket
		WHERE s.project = $1 AND s.state = 'waiting' AND (s.wait_for LIKE 'quota:%' OR s.wait_for LIKE 'login:%' OR s.wait_for IN ('setup', 'held'))
		UNION ALL
		SELECT $2 || '-' || number || ' ' || title, stuck, updated_at FROM tickets
			WHERE project = $1 AND stage NOT IN ('done', 'closed') AND (stuck LIKE '%close to its limit%' OR stuck LIKE '%active-ticket slot%')
		ORDER BY 3`, project, key)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AgentWait, error) {
		var x AgentWait
		return x, r.Scan(&x.What, &x.For, &x.Since)
	})
	if list == nil {
		list = []AgentWait{}
	}
	return list, err
}

// resultWords says how a run ended.
func resultWords(reason, tool, choice, verdict string) string {
	if reason != "" && reason != "ok" {
		return "Stopped: " + strings.ReplaceAll(reason, "_", " ")
	}
	switch verdict {
	case "approve":
		return "Approved"
	case "changes_requested":
		return "Asked for changes"
	case "pass":
		return "Passed"
	case "fail":
		return "Failed"
	}
	switch tool {
	case "analyse_ticket":
		return map[string]string{"approve": "Approved, ready to build", "skip": "Skipped", "discard": "Discarded", "notice": "A new feature"}[choice]
	case "request_move":
		return "Checks passed"
	case "fix_done":
		return "Fixed"
	case "subtask_done":
		return "Its part is done"
	case "write_report":
		return "Report written"
	}
	return "Ended"
}

// MachineSandboxSummary is the machine capacity and concurrency header of GET /api/sandboxes (15.4, 20.1).
type MachineSandboxSummary struct {
	Cores                int     `json:"cores"`
	TotalMemMB           int64   `json:"totalMemMB"`
	RoomCPUs             float64 `json:"roomCPUs"`
	RoomMemMB            int64   `json:"roomMemMB"`
	UsedCPUs             float64 `json:"usedCPUs"`
	UsedMemMB            int64   `json:"usedMemMB"`
	ActiveTickets        int     `json:"activeTickets"`
	MaxActiveTickets     int     `json:"maxActiveTickets"`
	SetMaxActiveTickets  int     `json:"setMaxActiveTickets"`
	RunningRuns          int     `json:"runningRuns"`
	MaxConcurrentRuns    int     `json:"maxConcurrentRuns"`
	SetMaxConcurrentRuns int     `json:"setMaxConcurrentRuns"`
}

// ProjectSandboxOverview is one project's warm microVM (crew-proj-<id>) on /sandboxes (13.3, 15.4, 20.1).
type ProjectSandboxOverview struct {
	Project             string     `json:"project"`
	ProjectID           string     `json:"projectId"`
	ProjectKey          string     `json:"projectKey"`
	ProjectState        string     `json:"projectState"`
	SandboxID           string     `json:"sandboxId"`
	Container           string     `json:"container"`
	State               string     `json:"state"`
	ActiveSessions      int        `json:"activeSessions"`
	ActiveTickets       int        `json:"activeTickets"`
	MaxActiveTickets    int        `json:"maxActiveTickets"`
	CPUs                int        `json:"cpus"`
	UsedCPUs            float64    `json:"usedCPUs"`
	CPUPercent          float64    `json:"cpuPercent"`
	MemoryGB            int        `json:"memoryGB"`
	MemUsedMB           int64      `json:"memUsedMB"`
	MemLimitMB          int64      `json:"memLimitMB"`
	DiskGB              int        `json:"diskGB"`
	DiskUsedMB          int64      `json:"diskUsedMB"`
	SnapshotsCount      int        `json:"snapshotsCount"`
	SnapshotsBytes      int64      `json:"snapshotsBytes"`
	MaxSnapshots        int        `json:"maxSnapshots"`
	SuspendAfterMinutes int        `json:"suspendAfterMinutes"`
	StopAfterMinutes    int        `json:"stopAfterMinutes"`
	ImageTag            string     `json:"imageTag"`
	LastActiveAt        *time.Time `json:"lastActiveAt,omitempty"`
	Runs                []AgentRun `json:"runs"`
}

// EphemeralSandboxOverview is an ephemeral check, build, terminal, hosted, judge, or conversation sandbox on /sandboxes.
type EphemeralSandboxOverview struct {
	ID         string    `json:"id"`
	Container  string    `json:"container"`
	Kind       string    `json:"kind"`
	Project    string    `json:"project,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CPUs       float64   `json:"cpus"`
	CPUPercent float64   `json:"cpuPercent"`
	MemUsedMB  int64     `json:"memUsedMB"`
	MemLimitMB int64     `json:"memLimitMB"`
	StartedAt  time.Time `json:"startedAt"`
}

// AllSandboxesView is the payload of GET /api/sandboxes (15.4, 20.1).
type AllSandboxesView struct {
	Machine            MachineSandboxSummary      `json:"machine"`
	ProjectSandboxes   []ProjectSandboxOverview   `json:"projectSandboxes"`
	EphemeralSandboxes []EphemeralSandboxOverview `json:"ephemeralSandboxes"`
	Waiting            []capacity.Wait            `json:"waiting"`
}

func (s *Server) readContainerStats(ctx context.Context, idOrContainer string) (orchestrator.ContainerStats, bool) {
	if idOrContainer == "" {
		return orchestrator.ContainerStats{}, false
	}
	d := s.Runs.Load()
	if d == nil || d.Sandboxes == nil {
		return orchestrator.ContainerStats{}, false
	}
	sr, ok := d.Sandboxes.(sandboxes.StatsRuntime)
	if !ok {
		return orchestrator.ContainerStats{}, false
	}
	st, err := sr.SandboxStats(ctx, idOrContainer)
	if err != nil {
		return orchestrator.ContainerStats{}, false
	}
	return st, true
}

func dirDiskUsedMB(dir string) int64 {
	if dir == "" {
		return 0
	}
	var totalBytes int64
	entries := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > 50000 {
			return fs.SkipAll
		}
		typ := d.Type()
		if typ&os.ModeSymlink != 0 || !typ.IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > 0 {
			totalBytes += info.Size()
		}
		return nil
	})
	if totalBytes <= 0 {
		return 0
	}
	mb := totalBytes / (1024 * 1024)
	if mb == 0 {
		return 1
	}
	return mb
}

func docIntVal(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

func (s *Server) allSandboxes(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	mDoc, err := s.machineDoc(ctx, pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	machTickets, err := pipeline.ActiveTicketsOf(ctx, pool, mDoc)
	if err != nil {
		s.boardError(w, "counting active tickets", err)
		return
	}
	sizes := capacity.Sizes(mDoc)
	used, err := capacity.Used(ctx, pool, sizes)
	if err != nil {
		s.boardError(w, "reading used capacity", err)
		return
	}
	waits, _ := capacity.Queue(ctx, pool)
	if waits == nil {
		waits = []capacity.Wait{}
	}
	nowSlots, _ := planning.Slots(ctx, pool, mDoc)
	setSlots := planning.ConfiguredSlots(mDoc)
	var runningRuns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.state IN ('starting', 'working') AND (s.kind <> 'helper' OR s.parent IS NULL)`).Scan(&runningRuns); err != nil {
		s.boardError(w, "counting running runs", err)
		return
	}
	out := AllSandboxesView{
		Machine: MachineSandboxSummary{
			UsedCPUs:             used.CPUs,
			UsedMemMB:            used.MemMB,
			ActiveTickets:        machTickets.Active,
			MaxActiveTickets:     machTickets.Max,
			SetMaxActiveTickets:  machTickets.Set,
			RunningRuns:          runningRuns,
			MaxConcurrentRuns:    nowSlots[cli.AgyName],
			SetMaxConcurrentRuns: setSlots,
		},
		ProjectSandboxes:   []ProjectSandboxOverview{},
		EphemeralSandboxes: []EphemeralSandboxOverview{},
		Waiting:            waits,
	}
	if g := s.Room.Load(); g != nil {
		if rep, err := g.Report(ctx); err == nil {
			out.Machine.Cores = rep.Cores
			out.Machine.TotalMemMB = rep.MemMB
			out.Machine.RoomCPUs = rep.Room.CPUs
			out.Machine.RoomMemMB = rep.Room.MemMB
			out.Machine.UsedCPUs = rep.Used.CPUs
			out.Machine.UsedMemMB = rep.Used.MemMB
			out.Waiting = rep.Waiting
		}
	}

	b := s.Board.Load()
	ctrl, _ := s.projectSandboxCtrl()
	projSandboxIDs := map[string]bool{}
	if b != nil {
		projects, err := b.Projects(ctx)
		if err != nil {
			s.boardError(w, "reading the projects", err)
			return
		}
		for _, p := range projects {
			if p.State == "archived" || p.Hidden {
				continue
			}
			psView, err := s.projectSandboxOverview(ctx, pool, b, ctrl, p)
			if err != nil {
				s.boardError(w, "reading project settings", err)
				return
			}
			if psView.SandboxID != "" {
				projSandboxIDs[psView.SandboxID] = true
			}
			out.ProjectSandboxes = append(out.ProjectSandboxes, psView)
		}
	}
	out.EphemeralSandboxes = s.ephemeralSandboxesOverview(ctx, pool, sizes, projSandboxIDs)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectSandboxOverview(ctx context.Context, pool *pgxpool.Pool, b *board.Board, ctrl ProjectSandboxController, p *board.Project) (ProjectSandboxOverview, error) {
	pDoc, err := b.Settings(ctx, pool, p)
	if err != nil {
		return ProjectSandboxOverview{}, err
	}
	pActive, pMax, _ := pipeline.ProjectActiveTickets(ctx, pool, p.ID, pDoc)
	cfg := sandboxes.ProjectConfig{
		CPUs: 6, MemoryGB: 8, DiskGB: 20, SuspendAfterMinutes: 5, StopAfterMinutes: 30, MaxSnapshots: sandboxes.DefaultMaxSnapshots,
	}
	if ctrl != nil {
		cfg = ctrl.ProjectConfigFor(ctx, p.ID)
	} else if sbDoc, ok := pDoc["sandbox"].(map[string]any); ok {
		if v, ok := docIntVal(sbDoc["cpus"]); ok && v >= 2 {
			cfg.CPUs = v
		}
		if v, ok := docIntVal(sbDoc["memory_gb"]); ok && v >= 2 {
			cfg.MemoryGB = v
		}
		if v, ok := docIntVal(sbDoc["disk_gb"]); ok && v >= 5 {
			cfg.DiskGB = v
		}
	}
	ps := sandboxes.ProjectSandbox{ProjectID: p.ID, State: sandboxes.StateStopped}
	if ctrl != nil {
		if got, err := ctrl.GetProjectSandbox(ctx, p.ID); err == nil {
			ps = got
		}
	} else {
		err := pool.QueryRow(ctx, `SELECT project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id,
			active_sessions, last_active_at, updated_at
			FROM project_sandboxes WHERE project_id = $1::uuid`, p.ID).Scan(
			&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
			&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ProjectSandboxOverview{}, err
		}
	}
	containerName := "crew-proj-" + strings.ToLower(p.Key)
	if ps.SandboxID != "" {
		containerName = orchestrator.SandboxName(ps.SandboxID)
	}
	memLimitMB := int64(cfg.MemoryGB) * 1024
	var usedCPUs, cpuPct float64
	var memUsedMB int64
	switch ps.State {
	case sandboxes.StateRunning:
		usedCPUs, memUsedMB = float64(cfg.CPUs), memLimitMB
		if st, ok := s.readContainerStats(ctx, ps.SandboxID); ok {
			cpuPct = st.CPUPercent
			if st.MemUsedMB > 0 {
				memUsedMB = st.MemUsedMB
			}
			if st.MemLimitMB > 0 {
				memLimitMB = st.MemLimitMB
			}
		}
	case sandboxes.StatePaused:
		usedCPUs, memUsedMB = 0, memLimitMB
		if st, ok := s.readContainerStats(ctx, ps.SandboxID); ok && st.MemUsedMB > 0 {
			memUsedMB = st.MemUsedMB
		}
	}
	homePath := ps.HomePath
	if homePath == "" && ctrl != nil {
		homePath = ctrl.ProjectHome(p.ID)
	}
	var snapsCount int
	var snapsBytes int64
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0)::bigint FROM project_snapshots WHERE project_id = $1::uuid`, p.ID).Scan(&snapsCount, &snapsBytes); err != nil {
		return ProjectSandboxOverview{}, err
	}

	runRows, err := pool.Query(ctx, `SELECT r.id::text, s.role, r.cli, coalesce(r.model_name, r.model), r.effort,
		CASE WHEN t.id IS NULL THEN s.kind ELSE $2 || '-' || t.number || '.' || s.number END,
		CASE WHEN t.id IS NULL THEN NULL ELSE $2 || '-' || t.number END, coalesce(s.title, s.kind), r.sandbox, r.size, r.state,
		r.started_at, r.ended_at, r.tokens_in + r.tokens_out
		FROM runs r JOIN subtasks s ON s.id = r.subtask LEFT JOIN tickets t ON t.id = s.ticket
		WHERE s.project = $1 AND r.state IN ('starting', 'working', 'idle')
		ORDER BY r.started_at`, p.ID, p.Key)
	var activeRuns []AgentRun
	if err == nil {
		activeRuns, _ = pgx.CollectRows(runRows, func(row pgx.CollectableRow) (AgentRun, error) {
			var a AgentRun
			err := row.Scan(&a.Run, &a.Role, &a.CLI, &a.Model, &a.Effort, &a.Subtask, &a.Ticket, &a.Title, &a.Sandbox, &a.Size, &a.State,
				&a.Started, &a.Ended, &a.Tokens)
			return a, err
		})
	}
	if activeRuns == nil {
		activeRuns = []AgentRun{}
	}
	var lastActive *time.Time
	if !ps.LastActiveAt.IsZero() {
		t := ps.LastActiveAt
		lastActive = &t
	}
	return ProjectSandboxOverview{
		Project: p.Name, ProjectID: p.ID, ProjectKey: p.Key, ProjectState: p.State,
		SandboxID: ps.SandboxID, Container: containerName, State: ps.State,
		ActiveSessions: ps.ActiveSessions, ActiveTickets: pActive, MaxActiveTickets: pMax,
		CPUs: cfg.CPUs, UsedCPUs: usedCPUs, CPUPercent: cpuPct,
		MemoryGB: cfg.MemoryGB, MemUsedMB: memUsedMB, MemLimitMB: memLimitMB,
		DiskGB: cfg.DiskGB, DiskUsedMB: dirDiskUsedMB(homePath),
		SnapshotsCount: snapsCount, SnapshotsBytes: snapsBytes, MaxSnapshots: cfg.MaxSnapshots,
		SuspendAfterMinutes: cfg.SuspendAfterMinutes, StopAfterMinutes: cfg.StopAfterMinutes,
		ImageTag: ps.ImageTag, LastActiveAt: lastActive, Runs: activeRuns,
	}, nil
}

func (s *Server) ephemeralSandboxesOverview(ctx context.Context, pool *pgxpool.Pool, sizes map[string]capacity.Size, projSandboxIDs map[string]bool) []EphemeralSandboxOverview {
	out := []EphemeralSandboxOverview{}
	sbRows, err := pool.Query(ctx, `SELECT sb.id, sb.container, sb.kind,
		coalesce(nullif(sb.size, ''), r.size, CASE WHEN sb.kind IN ('check', 'terminal', 'hosted', 'build', 'host-build') THEN 'large' ELSE 'small' END),
		coalesce(p.name, rp.name, ''),
		coalesce(CASE WHEN t.id IS NOT NULL THEN coalesce(p.key, rp.key) || '-' || t.number || ' ' || coalesce(s.title, s.kind) ELSE s.title END, sb.kind),
		sb.started_at
		FROM sandboxes sb
		LEFT JOIN runs r ON r.sandbox = sb.id AND r.state <> 'ended'
		LEFT JOIN subtasks s ON s.id = r.subtask
		LEFT JOIN projects p ON p.id = sb.project
		LEFT JOIN projects rp ON rp.id = s.project
		LEFT JOIN tickets t ON t.id = s.ticket
		WHERE sb.removed_at IS NULL
		ORDER BY sb.started_at`)
	if err != nil {
		return out
	}
	defer sbRows.Close()
	for sbRows.Next() {
		var id, container, kind, szName, projName, detail string
		var startedAt time.Time
		if sbRows.Scan(&id, &container, &kind, &szName, &projName, &detail, &startedAt) != nil || projSandboxIDs[id] {
			continue
		}
		sz := sizes[szName]
		if sz.CPUs == 0 {
			sz = sizes[capacity.Small]
		}
		ep := EphemeralSandboxOverview{
			ID: id, Container: container, Kind: kind, Project: projName, Detail: detail,
			CPUs: sz.CPUs, MemUsedMB: sz.MemMB, MemLimitMB: sz.MemMB, StartedAt: startedAt,
		}
		if st, ok := s.readContainerStats(ctx, id); ok {
			ep.CPUPercent = st.CPUPercent
			if st.MemUsedMB > 0 {
				ep.MemUsedMB = st.MemUsedMB
			}
			if st.MemLimitMB > 0 {
				ep.MemLimitMB = st.MemLimitMB
			}
		}
		out = append(out, ep)
	}
	return out
}
