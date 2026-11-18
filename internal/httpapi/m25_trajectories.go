package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/sqlite"
)

func (s *Server) m25TrajectoryRoutes(api *routes) {
	api.HandleFunc("GET /api/projects/{p}/trajectories", s.searchProjectTrajectories)
	api.HandleFunc("POST /api/projects/{p}/trajectories/{run}/offload", s.offloadTrajectoryBlob)
	api.HandleFunc("GET /api/projects/{p}/trajectories/{run}/blob", s.readTrajectoryBlob)
}

type indexedTrajectory struct {
	RunID           string     `json:"runId"`
	ProjectID       string     `json:"projectId"`
	Ticket          *string    `json:"ticket,omitempty"`
	TaskKey         *string    `json:"taskKey,omitempty"`
	Title           string     `json:"title"`
	Role            string     `json:"role"`
	CLI             string     `json:"cli"`
	Model           string     `json:"model"`
	Effort          string     `json:"effort"`
	ToolVerbs       []string   `json:"toolVerbs"`
	TouchedPaths    []string   `json:"touchedPaths"`
	Events          int64      `json:"events"`
	Turns           int        `json:"turns"`
	Tokens          int64      `json:"tokens"`
	DurationSeconds int        `json:"durationSeconds"`
	Outcome         string     `json:"outcome"`
	BlobKey         string     `json:"blobKey,omitempty"`
	StartedAt       time.Time  `json:"startedAt"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
}

func classifyOutcome(state string, stopReason, subKind, reportedTool string) string {
	if state != "ended" {
		return "live"
	}
	if strings.Contains(subKind, "research") || strings.Contains(reportedTool, "research") {
		return "research"
	}
	if strings.Contains(stopReason, "max_tries") {
		return "max_tries"
	}
	if stopReason == "ok" {
		return "pass"
	}
	return "error"
}

func (s *Server) syncProjectTrajectories(ctx context.Context, pool *pgxpool.Pool, wal *sqlite.WALStore, p *board.Project) error {
	rows, err := pool.Query(ctx, `SELECT r.id::text, r.subtask, st.ticket,
			CASE WHEN t.id IS NULL THEN '' ELSE $2 || '-' || t.number END,
			st.factory_task_id::text, coalesce(ft.key, ''),
			st.role, r.cli, coalesce(r.model, ''), coalesce(r.effort, ''),
			r.events, r.turn, (r.tokens_in + r.tokens_out)::bigint,
			r.state, coalesce(r.stop_reason, ''), st.kind, st.title,
			coalesce(st.reported->>'tool', ''), r.started_at, r.ended_at,
			coalesce((SELECT blob_key FROM trajectories tx WHERE tx.run_id = r.id), '')
		FROM runs r
		JOIN subtasks st ON st.id = r.subtask
		LEFT JOIN tickets t ON t.id = st.ticket
		LEFT JOIN factory_tasks ft ON ft.id = st.factory_task_id
		WHERE st.project = $1
		ORDER BY r.started_at DESC
		LIMIT 200`, p.ID, p.Key)
	if err != nil {
		return err
	}
	type runRow struct {
		runID        string
		subtaskID    int64
		ticketID     *int64
		ticketKey    string
		taskID       *string
		taskKey      string
		role         string
		cli          string
		model        string
		effort       string
		events       int64
		turns        int
		tokens       int64
		state        string
		stopReason   string
		subKind      string
		subTitle     string
		reportedTool string
		startedAt    time.Time
		endedAt      *time.Time
		blobKey      string
	}
	var list []runRow
	for rows.Next() {
		var x runRow
		if ser := rows.Scan(&x.runID, &x.subtaskID, &x.ticketID, &x.ticketKey, &x.taskID, &x.taskKey,
			&x.role, &x.cli, &x.model, &x.effort, &x.events, &x.turns, &x.tokens,
			&x.state, &x.stopReason, &x.subKind, &x.subTitle, &x.reportedTool, &x.startedAt, &x.endedAt, &x.blobKey); ser != nil {
			rows.Close()
			return ser
		}
		list = append(list, x)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return rerr
	}

	for _, x := range list {
		tools := []string{}
		paths := []string{}
		evRows, evErr := pool.Query(ctx, `SELECT coalesce(payload->>'tool', ''), coalesce(payload->>'path', '')
			FROM events WHERE run = $1::uuid AND type = 'tool_call' ORDER BY seq ASC LIMIT 120`, x.runID)
		if evErr == nil {
			for evRows.Next() {
				var tl, pth string
				if ser := evRows.Scan(&tl, &pth); ser != nil {
					evRows.Close()
					return ser
				}
				if tl != "" && !slices.Contains(tools, tl) {
					tools = append(tools, tl)
				}
				if pth != "" && !slices.Contains(paths, pth) {
					paths = append(paths, pth)
				}
			}
			evRows.Close()
			if rerr := evRows.Err(); rerr != nil {
				return rerr
			}
		}
		dur := int(time.Since(x.startedAt).Seconds())
		if x.endedAt != nil {
			dur = max(0, int(x.endedAt.Sub(x.startedAt).Seconds()))
		}
		outcome := classifyOutcome(x.state, x.stopReason, x.subKind, x.reportedTool)
		searchText := strings.Join([]string{
			x.runID, x.ticketKey, x.taskKey, x.role, x.model, x.subTitle, outcome,
			strings.Join(tools, " "), strings.Join(paths, " "),
		}, " ")
		if _, uerr := pool.Exec(ctx, `INSERT INTO trajectories (
				run_id, project_id, ticket_id, task_id, subtask_id, role, cli, model, effort,
				tool_verbs, touched_paths, events_count, turns_count, tokens_total,
				duration_seconds, outcome, blob_key, search_text, started_at, ended_at)
			VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
			ON CONFLICT (run_id) DO UPDATE SET
				role = EXCLUDED.role,
				model = EXCLUDED.model,
				effort = EXCLUDED.effort,
				tool_verbs = EXCLUDED.tool_verbs,
				touched_paths = EXCLUDED.touched_paths,
				events_count = EXCLUDED.events_count,
				turns_count = EXCLUDED.turns_count,
				tokens_total = EXCLUDED.tokens_total,
				duration_seconds = EXCLUDED.duration_seconds,
				outcome = EXCLUDED.outcome,
				search_text = EXCLUDED.search_text,
				ended_at = EXCLUDED.ended_at`,
			x.runID, p.ID, x.ticketID, x.taskID, x.subtaskID, x.role, x.cli, x.model, x.effort,
			tools, paths, x.events, x.turns, x.tokens, dur, outcome, x.blobKey, searchText, x.startedAt, x.endedAt); uerr != nil {
			return uerr
		}
		if wal != nil {
			_ = wal.UpsertTrajectory(sqlite.TrajectoryRecord{
				RunID: x.runID, ProjectID: p.ID, TicketKey: x.ticketKey, Role: x.role, Model: x.model,
				ToolVerbs: tools, TouchedPaths: paths, EventsCount: x.events, TurnsCount: x.turns,
				TokensTotal: x.tokens, DurationSeconds: dur, Outcome: outcome, BlobKey: x.blobKey,
				SearchText: searchText, StartedAt: x.startedAt,
			})
		}
	}
	return nil
}

func (s *Server) searchProjectTrajectories(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	if err := s.syncProjectTrajectories(ctx, b.Pool, s.ensureWAL(), p); err != nil {
		s.boardError(w, "indexing project trajectories", err)
		return
	}
	q := r.URL.Query()
	queryText := strings.TrimSpace(q.Get("q"))
	role := strings.TrimSpace(q.Get("role"))
	tool := strings.TrimSpace(q.Get("tool"))
	pathSub := strings.TrimSpace(q.Get("path"))
	outcome := strings.TrimSpace(q.Get("outcome"))
	tokensBucket := strings.TrimSpace(q.Get("tokensBucket"))
	durBucket := strings.TrimSpace(q.Get("durationBucket"))

	rows, err := b.Pool.Query(ctx, `SELECT tr.run_id::text, tr.project_id::text,
			CASE WHEN tk.id IS NULL THEN NULL ELSE $2 || '-' || tk.number END,
			ft.key, coalesce(st.title, ''), tr.role, tr.cli, tr.model, tr.effort,
			tr.tool_verbs, tr.touched_paths, tr.events_count, tr.turns_count, tr.tokens_total,
			tr.duration_seconds, tr.outcome, tr.blob_key, tr.started_at, tr.ended_at
		FROM trajectories tr
		LEFT JOIN tickets tk ON tk.id = tr.ticket_id
		LEFT JOIN factory_tasks ft ON ft.id = tr.task_id
		LEFT JOIN subtasks st ON st.id = tr.subtask_id
		WHERE tr.project_id = $1
		  AND ($3 = '' OR tr.role = $3)
		  AND ($4 = '' OR tr.outcome = $4)
		  AND ($5 = '' OR $5 = ANY(tr.tool_verbs))
		  AND ($6 = '' OR exists (SELECT 1 FROM unnest(tr.touched_paths) AS tp WHERE tp ILIKE '%' || $6 || '%'))
		  AND ($7 = '' OR tr.search_tsv @@ websearch_to_tsquery('english', $7) OR tr.search_text ILIKE '%' || $7 || '%')
		  AND (
			$8 = '' OR
			($8 = '<10k' AND tr.tokens_total < 10000) OR
			($8 = '10k-100k' AND tr.tokens_total >= 10000 AND tr.tokens_total <= 100000) OR
			($8 = '>100k' AND tr.tokens_total > 100000)
		  )
		  AND (
			$9 = '' OR
			($9 = '<60s' AND tr.duration_seconds < 60) OR
			($9 = '1m-10m' AND tr.duration_seconds >= 60 AND tr.duration_seconds <= 600) OR
			($9 = '>10m' AND tr.duration_seconds > 600)
		  )
		ORDER BY tr.started_at DESC
		LIMIT 200`, p.ID, p.Key, role, outcome, tool, pathSub, queryText, tokensBucket, durBucket)
	if err != nil {
		s.boardError(w, "querying trajectory store", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (indexedTrajectory, error) {
		var x indexedTrajectory
		err := row.Scan(&x.RunID, &x.ProjectID, &x.Ticket, &x.TaskKey, &x.Title, &x.Role, &x.CLI,
			&x.Model, &x.Effort, &x.ToolVerbs, &x.TouchedPaths, &x.Events, &x.Turns, &x.Tokens,
			&x.DurationSeconds, &x.Outcome, &x.BlobKey, &x.StartedAt, &x.EndedAt)
		if x.ToolVerbs == nil {
			x.ToolVerbs = []string{}
		}
		if x.TouchedPaths == nil {
			x.TouchedPaths = []string{}
		}
		return x, err
	})
	if err != nil {
		s.boardError(w, "reading trajectory store", err)
		return
	}
	if list == nil {
		list = []indexedTrajectory{}
	}
	var eventsSum, tokensSum int64
	var offloadedCount int
	roleSet, toolSet, outcomeSet := []string{}, []string{}, []string{}
	for _, x := range list {
		eventsSum += x.Events
		tokensSum += x.Tokens
		if x.BlobKey != "" {
			offloadedCount++
		}
		if x.Role != "" && !slices.Contains(roleSet, x.Role) {
			roleSet = append(roleSet, x.Role)
		}
		if x.Outcome != "" && !slices.Contains(outcomeSet, x.Outcome) {
			outcomeSet = append(outcomeSet, x.Outcome)
		}
		for _, tl := range x.ToolVerbs {
			if tl != "" && !slices.Contains(toolSet, tl) {
				toolSet = append(toolSet, tl)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"trajectories": list,
		"events":       eventsSum,
		"tokens":       tokensSum,
		"offloaded":    offloadedCount,
		"facets": map[string]any{
			"roles":    roleSet,
			"tools":    toolSet,
			"outcomes": outcomeSet,
		},
	})
}
