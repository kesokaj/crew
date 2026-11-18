package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/version"
)

// The routes of the app's frame and the inbox (design 7.8, 20).
func (s *Server) frameRoutes(api *routes) {
	api.HandleFunc("GET /api/frame", s.frame)
	api.HandleFunc("GET /api/inbox", s.inbox)
	api.HandleFunc("GET /api/overview", s.overview)
}

// LeadSlots is one CLI's lead slots: how many there are, and how many lead runs hold one now (7.9).
type LeadSlots struct {
	CLI   string `json:"cli"`
	Used  int    `json:"used"`
	Slots int    `json:"slots"`
}

// FrameProject is a project as the menu lists it: what needs the operator, and how many agents work.
type FrameProject struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	State    string `json:"state"`
	NeedsYou int    `json:"needsYou"`
	Working  int    `json:"working"`
}

// Frame is what the top bar and the menu show on every page (20): Crew's version, the lead slots of
// each CLI, the running sandboxes, the inbox, the open plan drafts, and the projects.
type Frame struct {
	Email         string                        `json:"email"`
	Version       string                        `json:"version"`
	LeadSlots     []LeadSlots                   `json:"leadSlots"`
	ActiveTickets pipeline.ActiveTicketsSummary `json:"activeTickets"`
	Sandboxes     int                           `json:"sandboxes"`
	Inbox         int                           `json:"inbox"`
	Drafts        int                           `json:"drafts"`
	Projects      []FrameProject                `json:"projects"`
	// Held says why nothing new starts, while Crew is paused, stopped, held because microVMs do not
	// boot, or its disk is full (7.9, 21).
	Held string `json:"held,omitempty"`
	// NextVersion is the release that waits to be installed, for the menu (16.3).
	NextVersion string `json:"nextVersion,omitempty"`
}

func (s *Server) frame(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	f := Frame{Email: r.Header.Get(HeaderEmail), Version: version.Version, LeadSlots: []LeadSlots{}, Projects: []FrameProject{},
		Held: hold.Held(ctx, pool)}
	doc, err := settings.Effective(ctx, pool, s.Machine)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	free, err := planning.FreeSlots(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "counting the lead slots", err)
		return
	}
	// The slots as they are now: the guard lowers a CLI's near a limit (12.7).
	slots, err := planning.Slots(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "counting the lead slots", err)
		return
	}
	for cli, n := range slots {
		f.LeadSlots = append(f.LeadSlots, LeadSlots{CLI: cli, Slots: n, Used: n - free[cli]})
	}
	slices.SortFunc(f.LeadSlots, func(a, b LeadSlots) int { return strings.Compare(a.CLI, b.CLI) })
	if at, err := pipeline.ActiveTicketsOf(ctx, pool, doc); err == nil {
		f.ActiveTickets = at
	}
	items, err := inbox.List(ctx, pool, doc)
	if err != nil {
		s.boardError(w, "reading the inbox", err)
		return
	}
	f.Inbox = len(items)
	for _, it := range items {
		if it.Kind == inbox.KindRelease {
			f.NextVersion = strings.TrimPrefix(it.ID, "release/")
		}
	}
	needs := map[string]int{}
	for _, it := range items {
		if it.Project != "" {
			needs[it.Project]++
		}
	}
	if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM sandboxes WHERE removed_at IS NULL),
			(SELECT count(*) FROM plan_drafts WHERE state = 'open')`).Scan(&f.Sandboxes, &f.Drafts); err != nil {
		s.boardError(w, "counting the sandboxes", err)
		return
	}
	rows, err := pool.Query(ctx, `SELECT p.name, p.key, p.state,
			(SELECT count(*) FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE st.project = p.id AND r.state <> 'ended')
		FROM projects p WHERE p.state <> 'archived' AND NOT p.hidden ORDER BY p.created_at`)
	if err != nil {
		s.boardError(w, "reading the projects", err)
		return
	}
	f.Projects, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (FrameProject, error) {
		var p FrameProject
		err := r.Scan(&p.Name, &p.Key, &p.State, &p.Working)
		p.NeedsYou = needs[p.Name]
		return p, err
	})
	if err != nil {
		s.boardError(w, "reading the projects", err)
		return
	}
	if f.Projects == nil {
		f.Projects = []FrameProject{}
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	items, ok := s.listInbox(w, r)
	if !ok {
		return
	}
	out, ok := s.withNotified(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// OverviewProject is a project on the front page (20.1, Projects): its goal and phase, its tickets
// by stage, the agents working on it, and its runs' tokens in and out (since 0.53 no cost).
type OverviewProject struct {
	Name    string         `json:"name"`
	Key     string         `json:"key"`
	Goal    string         `json:"goal"`
	State   string         `json:"state"`
	Pause   *string        `json:"pauseReason"`
	ByStage map[string]int `json:"byStage"`
	Working int            `json:"working"`
	Tokens  int64          `json:"tokens"`
	// Models are the models, each with its effort, the project's runs have run on.
	Models []string `json:"models"`
	// GitHub says the project has a repository on GitHub, and Network is its sandboxes' mode (13.6).
	GitHub    bool   `json:"github"`
	Network   string `json:"network"`
	Protected bool   `json:"protected"`
	Locked    bool   `json:"locked"`
	id        string
}

// overview is the front page: every project that is not archived.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	rows, err := pool.Query(r.Context(), `SELECT p.id::text, p.name, p.key, p.goal, p.state, p.pause_reason, p.remote IS NOT NULL, p.protected,
			coalesce((SELECT jsonb_object_agg(stage, n) FROM (SELECT stage, count(*) AS n FROM tickets WHERE project = p.id GROUP BY stage) x), '{}'),
			(SELECT count(*) FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE st.project = p.id AND r.state <> 'ended'),
			(SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE st.project = p.id),
			coalesce((SELECT array_agg(m ORDER BY m) FROM (SELECT DISTINCT coalesce(r.model_name, r.model) || CASE WHEN r.model_name = r.model AND r.effort IS NOT NULL
				THEN ' · ' || r.effort ELSE '' END AS m FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE st.project = p.id AND r.model IS NOT NULL) x), '{}')
		FROM projects p WHERE p.state <> 'archived' AND NOT p.hidden ORDER BY p.created_at`)
	if err != nil {
		s.boardError(w, "reading the projects", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OverviewProject, error) {
		var p OverviewProject
		return p, r.Scan(&p.id, &p.Name, &p.Key, &p.Goal, &p.State, &p.Pause, &p.GitHub, &p.Protected, &p.ByStage, &p.Working, &p.Tokens, &p.Models)
	})
	if err != nil {
		s.boardError(w, "reading the projects", err)
		return
	}
	if list == nil {
		list = []OverviewProject{}
	}
	doc, err := s.machineDoc(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return
	}
	var crewProject string
	if imp, ok := doc["improvements"].(settings.Doc); ok {
		crewProject, _ = imp["crew_project"].(string)
	}
	for i := range list {
		p := &list[i]
		p.Locked = crewProject != "" && p.Name == crewProject
		p.Protected = p.Protected || p.Locked
		var err error
		if p.Network, err = settings.NetworkMode(r.Context(), pool, p.id, 0); err != nil {
			s.boardError(w, "reading a project's network mode", err)
			return
		}
	}
	lead, err := s.leadPanel(r.Context(), pool, doc)
	if err != nil {
		s.boardError(w, "reading the lead slots", err)
		return
	}
	// The room on the machine (7.9; 21, "The machine is full"): what is given out, and what waits for
	// room. Without Docker the machine's size is not known, and the panel is left out.
	var room *capacity.Report
	if g := s.Room.Load(); g != nil {
		if rep, err := g.Report(r.Context()); err == nil {
			room = &rep
		} else {
			s.Log.Warn("the room on the machine cannot be read", "error", err)
		}
	}
	type OverviewSandbox struct {
		ID        string    `json:"id"`
		Kind      string    `json:"kind"`
		Size      string    `json:"size"`
		State     string    `json:"state"`
		Project   string    `json:"project,omitempty"`
		Ticket    string    `json:"ticket,omitempty"`
		Run       string    `json:"run,omitempty"`
		Role      string    `json:"role,omitempty"`
		Title     string    `json:"title,omitempty"`
		StartedAt time.Time `json:"startedAt"`
	}
	sbRows, err := pool.Query(r.Context(), `SELECT sb.id, sb.kind,
			coalesce(nullif(sb.size, ''), r.size, CASE WHEN sb.kind IN ('check', 'terminal', 'hosted', 'build', 'host-build') THEN 'large' ELSE 'small' END),
			coalesce(r.state, 'running'), coalesce(p.name, rp.name, ''),
			coalesce(CASE WHEN t.id IS NOT NULL THEN coalesce(p.key, rp.key) || '-' || t.number END, ''),
			coalesce(r.id::text, ''), coalesce(s.role, ''), coalesce(s.title, sb.kind), sb.started_at
		FROM sandboxes sb
		LEFT JOIN runs r ON r.sandbox = sb.id AND r.state <> 'ended'
		LEFT JOIN subtasks s ON s.id = r.subtask
		LEFT JOIN projects p ON p.id = sb.project
		LEFT JOIN projects rp ON rp.id = s.project
		LEFT JOIN tickets t ON t.id = s.ticket
		WHERE sb.removed_at IS NULL
		ORDER BY sb.started_at`)
	if err != nil {
		s.boardError(w, "reading the sandboxes", err)
		return
	}
	sandboxes, err := pgx.CollectRows(sbRows, func(row pgx.CollectableRow) (OverviewSandbox, error) {
		var sb OverviewSandbox
		return sb, row.Scan(&sb.ID, &sb.Kind, &sb.Size, &sb.State, &sb.Project, &sb.Ticket, &sb.Run, &sb.Role, &sb.Title, &sb.StartedAt)
	})
	if err != nil {
		s.boardError(w, "reading the sandboxes", err)
		return
	}
	if sandboxes == nil {
		sandboxes = []OverviewSandbox{}
	}
	activeTickets, _ := pipeline.ActiveTicketsOf(r.Context(), pool, doc)
	writeJSON(w, http.StatusOK, map[string]any{
		"projects":      list,
		"lead":          lead,
		"activeTickets": activeTickets,
		"room":          room,
		"sandboxes":     sandboxes,
	})
}
