package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
)

// The Crew MCP screen (7.5, 11.5, 11.7, 20.1, mcp.png): the server, what agents created through it,
// the operator's own tools, every call as it happens, and the tools with who may call each.

// mcpScreenCall is one call to the Crew MCP, newest first: which run or token called what.
type mcpScreenCall struct {
	At      time.Time `json:"at"`
	Project *string   `json:"project"`
	Run     *string   `json:"run"`
	Subtask *string   `json:"subtask"` // the run's subtask, such as SF-9.2
	Role    string    `json:"role"`
	Token   *string   `json:"token"` // the personal token's name
	Tool    string    `json:"tool"`
	Args    any       `json:"args"`
	OK      bool      `json:"ok"`
	Result  string    `json:"result"`
	MS      int       `json:"ms"`
}

// mcpCreated is a ticket or a subtask an agent created, newest first, with what became of it.
type mcpCreated struct {
	Key       string    `json:"key"`
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	What      string    `json:"what"` // ticket or subtask
	By        string    `json:"by"`   // who created it: the role, or the token
	In        *string   `json:"in"`   // the ticket or subtask its run worked on
	At        time.Time `json:"at"`
	State     string    `json:"state"`
	Parallel  bool      `json:"parallel,omitempty"`
	CreatedOn string    `json:"on,omitempty"` // a subtask's ticket and its title
}

type mcpScreenTool struct {
	Name string `json:"name"`
	baseline.Tool
	Calls int `json:"calls"`
}

func (s *Server) mcpScreen(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	words, err := baseline.Tools()
	if err != nil {
		s.boardError(w, "reading the baseline's tools", err)
		return
	}
	counts := map[string]int{}
	rows, err := pool.Query(ctx, `SELECT tool, count(*) FROM mcp_calls GROUP BY tool`)
	if err != nil {
		s.boardError(w, "counting the calls", err)
		return
	}
	for rows.Next() {
		var n string
		var c int
		if rows.Scan(&n, &c) == nil {
			counts[n] = c
		}
	}
	rows.Close()
	tools := []mcpScreenTool{}
	for name, t := range words {
		tools = append(tools, mcpScreenTool{Name: name, Tool: t, Calls: counts[name]})
	}
	order := map[string]int{"Ask": 0, "Tickets": 1, "Subtasks": 2, "Docs": 3, "Skills": 4, "Record": 5, "Move": 6, "Decide": 7, "Plan": 8, "Analysis": 9, "Curator": 10, "Lead": 10}
	sort.Slice(tools, func(i, j int) bool {
		if order[tools[i].Group] != order[tools[j].Group] {
			return order[tools[i].Group] < order[tools[j].Group]
		}
		return tools[i].Name < tools[j].Name
	})
	rows, err = pool.Query(ctx, `SELECT c.at, p.name, c.run::text,
			CASE WHEN st.ticket IS NULL THEN NULL ELSE tp.key || '-' || t.number || '.' || st.number END,
			c.role, pt.name, c.tool, c.args, c.ok, c.result, c.ms
		FROM mcp_calls c LEFT JOIN projects p ON p.id = c.project LEFT JOIN runs r ON r.id = c.run
		LEFT JOIN subtasks st ON st.id = r.subtask LEFT JOIN tickets t ON t.id = st.ticket LEFT JOIN projects tp ON tp.id = t.project
		LEFT JOIN personal_tokens pt ON pt.id = c.token
		ORDER BY c.id DESC LIMIT 100`)
	if err != nil {
		s.boardError(w, "reading the calls", err)
		return
	}
	calls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mcpScreenCall, error) {
		var c mcpScreenCall
		return c, row.Scan(&c.At, &c.Project, &c.Run, &c.Subtask, &c.Role, &c.Token, &c.Tool, &c.Args, &c.OK, &c.Result, &c.MS)
	})
	if err != nil {
		s.boardError(w, "reading the calls", err)
		return
	}
	rows, err = pool.Query(ctx, `(SELECT p.key || '-' || t.number, p.name, t.title, 'ticket', t.created_by,
			(SELECT p.key || '-' || f.number FROM ticket_links l JOIN tickets f ON f.id = l.to_ticket WHERE l.from_ticket = t.id AND l.kind = 'created_from' LIMIT 1),
			t.created_at, CASE WHEN t.stage = 'analysis' THEN coalesce(t.analysis->>'status', 'analysis') ELSE t.stage END, false, ''
		FROM tickets t JOIN projects p ON p.id = t.project WHERE t.origin IN ('agent', 'token'))
		UNION ALL
		(SELECT p.key || '-' || t.number || '.' || s.number, p.name, s.title, 'subtask', s.created_by,
			(SELECT p.key || '-' || t.number || '.' || c.number FROM runs r JOIN subtasks c ON c.id = r.subtask WHERE r.id = s.created_run),
			s.created_at, CASE WHEN s.joined_at IS NOT NULL THEN 'joined' ELSE s.state END, true, t.title
		FROM subtasks s JOIN tickets t ON t.id = s.ticket JOIN projects p ON p.id = t.project WHERE s.kind = 'part')
		ORDER BY 7 DESC LIMIT 50`)
	if err != nil {
		s.boardError(w, "reading what agents created", err)
		return
	}
	created, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mcpCreated, error) {
		var c mcpCreated
		return c, row.Scan(&c.Key, &c.Project, &c.Title, &c.What, &c.By, &c.In, &c.At, &c.State, &c.Parallel, &c.CreatedOn)
	})
	if err != nil {
		s.boardError(w, "reading what agents created", err)
		return
	}
	var stats struct {
		Tickets   int `json:"tickets"`
		Waiting   int `json:"waitingForAnalysis"`
		Notice    int `json:"newFeatures"`
		Approved  int `json:"approved"`
		Skipped   int `json:"skipped"`
		Discarded int `json:"discarded"`
		Subtasks  int `json:"subtasks"`
		Queued    int `json:"queued"`
		Running   int `json:"running"`
		Done      int `json:"done"`
		Decisions int `json:"decisions"`
		Calls     int `json:"calls"`
	}
	err = pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token')),
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token') AND stage IN ('backlog', 'analysis') AND analysis->>'status' IN ('waiting', 'proposed', 'analysing')),
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token') AND analysis->>'status' = 'new_feature_notice'),
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token') AND analysis->>'status' = 'ready'),
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token') AND analysis->>'status' = 'skipped'),
			(SELECT count(*) FROM tickets WHERE origin IN ('agent', 'token') AND stage = 'closed' AND closed_reason = 'discarded'),
			(SELECT count(*) FROM subtasks WHERE kind = 'part'),
			(SELECT count(*) FROM subtasks WHERE kind = 'part' AND state = 'queued'),
			(SELECT count(*) FROM subtasks WHERE kind = 'part' AND state IN ('running', 'idle', 'waiting')),
			(SELECT count(*) FROM subtasks WHERE kind = 'part' AND state = 'done'),
			(SELECT count(*) FROM decisions),
			(SELECT count(*) FROM mcp_calls)`).Scan(&stats.Tickets, &stats.Waiting, &stats.Notice, &stats.Approved, &stats.Skipped, &stats.Discarded,
		&stats.Subtasks, &stats.Queued, &stats.Running, &stats.Done, &stats.Decisions, &stats.Calls)
	if err != nil {
		s.boardError(w, "counting what agents created", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools, "calls": calls, "created": created, "stats": stats})
}
