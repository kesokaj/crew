package httpapi

import (
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/diskguard"
	"github.com/kesokaj/crew/internal/settings"
)

// The Database screen (design 15.3, 20.1, db.png): the container and its version, the size on disk,
// every group of tables with its rows and what is kept for how long, the last backup, the last schema
// change, and the latest changes. It reads, and changes nothing.
func (s *Server) databaseRoutes(api *routes) {
	api.HandleFunc("GET /api/database", s.database)
}

// tableGroup is a group of tables of 15.3, with how long its rows are kept.
type tableGroup struct {
	Name   string
	Tables []string
	Kept   string
}

// The groups of 15.3, with the tables each has by now. A table no group names is listed under Other,
// so a new one is never left out.
var tableGroups = []tableGroup{
	{"Projects and plans", []string{"projects", "workflow_defs", "plan_drafts", "plan_draft_versions", "draft_messages", "prompt_rewrites", "plans", "settings", "deleted_projects"}, "for good; a deleted project leaves one row"},
	{"The board", []string{"tickets", "ticket_links", "specs", "factory_tasks", "subtasks", "subtask_steps", "stage_moves", "checks", "check_results", "check_runs"}, "for good"},
	{"Records", []string{"records", "record_files", "research_briefs", "spec_questions", "spec_decisions", "summaries", "decisions", "reports"}, "for good"},
	{"Knowledge", []string{"memory", "dreamstore_entries", "dream_proposals", "bugs", "feedback", "skill_requests", "doc_pages", "doc_page_tickets", "eval_cases", "eval_suites", "eval_runs", "overrides", "retrospectives"}, "for good; the index of the pages is made again from main"},
	{"Runs", []string{"runs", "run_prompts", "run_tokens", "run_spools", "sandbox_tokens", "sandboxes", "project_sandboxes", "project_snapshots", "cli_logins", "jobs", "lead_waits", "room_waits", "quota", "models", "baseline_entries", "model_choices", "comparisons", "pushes", "ref_pushes", "git_repos", "pull_requests", "github_posts", "remote_mains", "setup_checks", "update_requests"}, "runs and prompts for good"},
	{"Conversations and trajectories", []string{"conversations", "conversation_messages", "chat_rooms", "chat_threads", "chat_messages", "chat_message_addressees", "turns", "events", "trajectories", "cli_files", "work_folders"}, "turns and events for good, the operator's conversations until deleted; a run's CLI files keep_cli_files_days after its last turn, 7 unless set, a conversation's conversations.keep_cli_files_days, 30 unless set"},
	{"The Crew level", []string{"signals", "releases", "builds", "gates", "improvements", "improvement_evals", "metrics", "pin_tickets", "pin_looks", "machine_news", "crew_harness_files", "skills_registry", "skill_installs", "knowledge_entries"}, "for good; the analytics are computed again after five minutes; the daily look keeps its newest 30 looks, and its keys go with a deleted project; the news of looking after the machine, a few lines a year, for good"},
	{"Hosting", []string{"hosting", "deploys"}, "for good"},
	{"The operator", []string{"sessions", "session_ids", "session_handovers", "operator_prefs", "mcp_calls", "personal_tokens", "mcp_token_failures", "notifications", "inbox_decisions", "notify_state", "project_terminals", "terminal_mains", "terminal_log"}, "terminal logs, and the calls of your own tools, 180 days; a device 31 days after its newest sign-in, one signed out for good; the mail sent and the inbox's presses for good"},
	{"Secrets", []string{"credentials"}, "until replaced; encrypted"},
	{"History", []string{"changes"}, "for good; rows are only added, and a deleted project's go with it"},
	{"Crew itself", []string{"schema_steps", "crew_state", "disk_samples", "storage_settings", "blob_objects"}, "for good; disk readings a year"},
}

// DBGroup is a group of tables with the rows of each.
type DBGroup struct {
	Name   string           `json:"name"`
	Kept   string           `json:"kept"`
	Tables map[string]int64 `json:"tables"`
	Rows   int64            `json:"rows"`
}

// DBChange is a row of the history.
type DBChange struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Project *string   `json:"project"`
	Who     string    `json:"who"`
	Table   string    `json:"table"`
	Row     string    `json:"row"`
	After   any       `json:"after"`
}

// DatabaseView is what the screen shows.
type DatabaseView struct {
	Version    string     `json:"version"`
	SizeBytes  int64      `json:"sizeBytes"`
	Groups     []DBGroup  `json:"groups"`
	LastStep   *int       `json:"lastStep"`
	LastStepAt *time.Time `json:"lastStepAt"`
	StepName   *string    `json:"stepName"`
	Backups    []string   `json:"backups"`
	BackupDir  string     `json:"backupDir"`
	BackupErr  string     `json:"backupError,omitempty"`
	Changes    []DBChange `json:"changes"`
	// Disk is the data folder's disk, from the disk guard's readings (16.10); nil before the first.
	Disk *diskguard.Room `json:"disk,omitempty"`
}

func (s *Server) database(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	v := DatabaseView{Groups: []DBGroup{}, Backups: []string{}, Changes: []DBChange{}}
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version'), pg_database_size(current_database())`).Scan(&v.Version, &v.SizeBytes); err != nil {
		s.boardError(w, "reading the database", err)
		return
	}
	// The rows of each table, as PostgreSQL counts them: close, and read without a scan. The months
	// of events are one table.
	rows, err := pool.Query(ctx, `SELECT c.relname, greatest(c.reltuples, s.n_live_tup)::bigint FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition`)
	if err != nil {
		s.boardError(w, "reading the tables", err)
		return
	}
	counts := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			s.boardError(w, "reading the tables", err)
			return
		}
		counts[name] = max(n, 0)
	}
	rows.Close()
	var events int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(greatest(c.reltuples, s.n_live_tup)), 0)::bigint FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE i.inhparent = 'events'::regclass`).Scan(&events); err == nil {
		counts["events"] = events
	}
	named := map[string]bool{}
	for _, g := range tableGroups {
		dg := DBGroup{Name: g.Name, Kept: g.Kept, Tables: map[string]int64{}}
		for _, t := range g.Tables {
			named[t] = true
			if n, ok := counts[t]; ok {
				dg.Tables[t] = n
				dg.Rows += n
			}
		}
		if len(dg.Tables) > 0 {
			v.Groups = append(v.Groups, dg)
		}
	}
	other := DBGroup{Name: "Other", Kept: "", Tables: map[string]int64{}}
	for t, n := range counts {
		if !named[t] {
			other.Tables[t] = n
			other.Rows += n
		}
	}
	if len(other.Tables) > 0 {
		v.Groups = append(v.Groups, other)
	}
	var step int
	var at time.Time
	var name string
	if err := pool.QueryRow(ctx, `SELECT number, applied_at, name FROM schema_steps ORDER BY number DESC LIMIT 1`).Scan(&step, &at, &name); err == nil {
		v.LastStep, v.LastStepAt, v.StepName = &step, &at, &name
	}
	if s.Backups != nil {
		dir, files, err := s.Backups(ctx)
		v.BackupDir = dir
		if err != nil {
			v.BackupErr = err.Error()
		} else {
			slices.Sort(files)
			slices.Reverse(files)
			v.Backups = append(v.Backups, files...)
		}
	}
	rows, err = pool.Query(ctx, `SELECT c.seq, c.at, p.name, c.who, c.tbl, c.row_key, c.after FROM changes c
		LEFT JOIN projects p ON p.id = c.project ORDER BY c.seq DESC LIMIT 50`)
	if err != nil {
		s.boardError(w, "reading the changes", err)
		return
	}
	v.Changes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DBChange, error) {
		var c DBChange
		return c, r.Scan(&c.Seq, &c.At, &c.Project, &c.Who, &c.Table, &c.Row, &c.After)
	})
	if err != nil {
		s.boardError(w, "reading the changes", err)
		return
	}
	if v.Changes == nil {
		v.Changes = []DBChange{}
	}
	stopAt := 95.0
	if doc, err := settings.Effective(ctx, pool, s.Machine); err == nil {
		if disk, ok := doc["disk"].(settings.Doc); ok {
			switch n := disk["stop_at"].(type) {
			case int:
				stopAt = float64(n)
			case float64:
				stopAt = n
			}
		}
	}
	if v.Disk, err = diskguard.ReadRoom(ctx, pool, stopAt); err != nil {
		s.boardError(w, "reading the disk", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
