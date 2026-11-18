package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/settings"
)

var allowedPeerRoles = []string{
	"planner",
	"coordinator",
	"builder",
	"reviewer",
	"security",
	"researcher",
	"tester",
	"e2e",
	"verifier",
	"curator",
	"dreamer",
}

// ErrUntrackedWork is returned when an ACP peer session attempts to start without a bound
// ticket or Tasks API off-board task record (R217).
var ErrUntrackedWork = errors.New("untracked agent work is refused (R217): bind a ticket, taskId, or taskKind")

var validPeerTaskKinds = []string{"spec", "research", "dream", "coordinator", "chat_action"}

// PeerSession represents one peer agent ACP session exposed by Crew's Agent API (design 11.10, M21).
type PeerSession struct {
	ID             string     `json:"id"`
	SubtaskID      int64      `json:"subtaskId"`
	ConversationID string     `json:"conversationId"`
	CLISessionID   string     `json:"cliSessionId,omitempty"`
	Project        string     `json:"project,omitempty"`
	Ticket         string     `json:"ticket,omitempty"`
	TaskID         string     `json:"taskId,omitempty"`
	TaskKey        string     `json:"taskKey,omitempty"`
	Role           string     `json:"role"`
	CLI            string     `json:"cli"`
	Model          string     `json:"model,omitempty"`
	Effort         string     `json:"effort,omitempty"`
	ModelFrom      string     `json:"modelFrom,omitempty"`
	State          string     `json:"state"`
	SandboxID      string     `json:"sandboxId,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	Turn           int        `json:"turn"`
	Events         int64      `json:"events"`
	TokensIn       int64      `json:"tokensIn"`
	TokensOut      int64      `json:"tokensOut"`
	ReadOnly       bool       `json:"readOnly"`
	StartedAt      time.Time  `json:"startedAt"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
	StopReason     string     `json:"stopReason,omitempty"`
}

// SpawnPeerRequest configures a new peer agent ACP session via POST /api/agents/sessions.
type SpawnPeerRequest struct {
	ProjectID   string
	TicketID    int64
	TaskID      string
	TaskKind    string
	Untracked   bool
	Role        string
	Title       string
	Prompt      string
	Branch      string
	Base        string
	ReadOnly    *bool
	ModelPin    string
	EffortPin   string
	AvoidFamily string
}

// NormalizePeerRole strips any leading '@' handle prefix and validates a built-in peer agent role.
func NormalizePeerRole(raw string) (string, error) {
	role := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if !slices.Contains(allowedPeerRoles, role) {
		return "", fmt.Errorf("unsupported peer agent role %q", raw)
	}
	return role, nil
}

// NormalizeProjectPeerRole validates a built-in role or a custom WORKFLOW.md specialist slug.
func NormalizeProjectPeerRole(raw string, custom []string) (string, error) {
	if role, err := NormalizePeerRole(raw); err == nil {
		return role, nil
	}
	role := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if role != "" && slices.Contains(custom, role) {
		return role, nil
	}
	return "", fmt.Errorf("unsupported peer agent role %q", raw)
}

func (d *Driver) resolveProjectPeerRole(ctx context.Context, projectID, raw string) (string, *board.WorkflowAgent, error) {
	if role, err := NormalizePeerRole(raw); err == nil {
		return role, nil, nil
	}
	role := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	var rawMD string
	var fmBytes []byte
	err := d.Pool.QueryRow(ctx, `SELECT raw_markdown, frontmatter FROM workflow_defs WHERE project_id = $1::uuid`, projectID).Scan(&rawMD, &fmBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, fmt.Errorf("unsupported peer agent role %q", raw)
		}
		return "", nil, err
	}
	var fm map[string]any
	if len(fmBytes) > 0 {
		_ = json.Unmarshal(fmBytes, &fm)
	}
	if symErr, ok := fm["symlink_error"].(string); ok && symErr != "" {
		return "", nil, fmt.Errorf("WORKFLOW.md symlink refused: %s", symErr)
	}
	def, perr := board.ParseWorkflow(rawMD)
	if perr != nil {
		return "", nil, fmt.Errorf("unsupported peer agent role %q: %w", raw, perr)
	}
	for i := range def.Agents {
		if def.Agents[i].Slug == role {
			ag := def.Agents[i]
			return ag.Slug, &ag, nil
		}
	}
	return "", nil, fmt.Errorf("unsupported peer agent role %q", raw)
}

func peerSessionFromView(v RunView) PeerSession {
	return PeerSession{
		ID:             v.ID,
		SubtaskID:      v.Subtask,
		ConversationID: v.Conversation,
		CLISessionID:   v.CLIConversation,
		Project:        v.Project,
		Ticket:         v.Ticket,
		Role:           "@" + v.Role,
		CLI:            v.CLI,
		Model:          v.Model,
		Effort:         v.Effort,
		ModelFrom:      v.ModelFrom,
		State:          v.State,
		SandboxID:      v.Sandbox,
		Branch:         v.Branch,
		Turn:           v.Turn,
		Events:         v.Events,
		TokensIn:       v.TokensIn,
		TokensOut:      v.TokensOut,
		ReadOnly:       !v.Push,
		StartedAt:      v.StartedAt,
		EndedAt:        v.EndedAt,
		StopReason:     v.StopReason,
	}
}

// SpawnPeerSession starts an isolated peer ACP session for one role on a project (1 session = 1 peer agent).
func (d *Driver) SpawnPeerSession(ctx context.Context, req SpawnPeerRequest) (*PeerSession, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, errors.New("project id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	role, customAgent, err := d.resolveProjectPeerRole(ctx, req.ProjectID, req.Role)
	if err != nil {
		return nil, err
	}
	var projName, projKey string
	if err := d.Pool.QueryRow(ctx, `SELECT name, key FROM projects WHERE id = $1::uuid`, req.ProjectID).Scan(&projName, &projKey); err != nil {
		return nil, fmt.Errorf("project %s: %w", req.ProjectID, err)
	}
	machineDoc, err := settings.Effective(ctx, d.Pool, settings.Machine{})
	if err != nil {
		return nil, err
	}
	projDoc, err := settings.EffectiveProject(ctx, d.Pool, req.ProjectID, projName, projKey)
	if err != nil {
		return nil, err
	}
	var pin settings.Doc
	if req.ModelPin != "" || req.EffortPin != "" {
		pin = settings.Doc{}
		if req.ModelPin != "" {
			pin["model"] = req.ModelPin
		}
		if req.EffortPin != "" {
			pin["effort"] = req.EffortPin
		}
	}
	if req.AvoidFamily == "" && req.TicketID != 0 && slices.Contains(models.CheckRoles, role) {
		if bm, found, berr := BuilderModel(ctx, d.Pool, req.TicketID); berr == nil && bm != "" && found {
			req.AvoidFamily = cli.Family(bm)
		}
	}
	askBracket := ""
	if customAgent != nil {
		askBracket = customAgent.ModelBracket
	}
	ch, err := models.Choose(ctx, d.Pool, models.Ask{
		Role:    role,
		Machine: machineDoc,
		Project: projDoc,
		Pin:     pin,
		Avoid:   req.AvoidFamily,
		Bracket: askBracket,
	})
	if err != nil {
		return nil, err
	}
	readOnly := role != "builder"
	allowMCP := false
	forceDocker := false
	if customAgent != nil {
		readOnly = customAgent.ReadOnly
		allowMCP = true
		forceDocker = customAgent.Docker
	}
	if req.ReadOnly != nil {
		readOnly = *req.ReadOnly
	}
	branch := strings.TrimSpace(req.Branch)
	if !readOnly && branch == "" {
		branch = fmt.Sprintf("subtask/peer-%s-%s", role, newUUID()[:8])
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = fmt.Sprintf("@%s session", role)
	}
	taskID, _, preMinted, err := d.preparePeerTask(ctx, req, projKey, role, title)
	if err != nil {
		return nil, err
	}
	kind := "peer:" + role + ":" + newUUID()[:8]
	runID, _, err := d.StartRole(ctx, RoleRun{
		Role:        role,
		Kind:        kind,
		Title:       title,
		Project:     req.ProjectID,
		Ticket:      req.TicketID,
		Branch:      branch,
		Base:        req.Base,
		ReadOnly:    readOnly,
		AllowMCP:    allowMCP,
		ForceDocker: forceDocker,
		CLI:         ch.CLI,
		Model:       ch.Model,
		Name:        ch.Name,
		Effort:      ch.Effort,
		From:        ch.ModelFrom,
		EffortFrom:  ch.EffortFrom,
		Parts:       []PromptPart{{Part: "prompt", Source: "operator", Text: req.Prompt}},
		Record: func(ctx context.Context, tx pgx.Tx, runID string) error {
			return bindPeerTaskTx(ctx, tx, taskID, runID)
		},
	})
	if err != nil {
		if preMinted && taskID != "" {
			_, _ = d.Pool.Exec(ctx, `UPDATE factory_tasks SET state = 'failed', result_summary = $2, updated_at = now() WHERE id = $1::uuid`, taskID, err.Error())
		}
		return nil, err
	}
	return d.GetPeerSession(ctx, runID)
}

func (d *Driver) preparePeerTask(ctx context.Context, req SpawnPeerRequest, projKey, role, title string) (taskID, taskKind string, preMinted bool, err error) {
	if req.Untracked {
		return "", "", false, ErrUntrackedWork
	}
	taskID = strings.TrimSpace(req.TaskID)
	taskKind = strings.ToLower(strings.TrimSpace(req.TaskKind))
	if req.TicketID == 0 && taskID == "" && taskKind == "" {
		return "", "", false, ErrUntrackedWork
	}
	if taskID != "" {
		var id, key, state string
		qerr := d.Pool.QueryRow(ctx, `SELECT id::text, key, state FROM factory_tasks
			WHERE project_id = $1::uuid AND (id::text = $2 OR upper(key) = upper($2))`, req.ProjectID, taskID).
			Scan(&id, &key, &state)
		if qerr != nil {
			return "", "", false, fmt.Errorf("factory task %q: %w", taskID, ErrUntrackedWork)
		}
		if state == "done" || state == "failed" || state == "cancelled" {
			return "", "", false, fmt.Errorf("factory task %s already %s: %w", key, state, ErrUntrackedWork)
		}
		return id, taskKind, false, nil
	}
	if taskKind == "" {
		return "", "", false, nil
	}
	if !slices.Contains(validPeerTaskKinds, taskKind) {
		return "", "", false, fmt.Errorf("taskKind %q must be an off-board kind (executable code belongs in Backlog): %w", taskKind, ErrUntrackedWork)
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return "", "", false, err
	}
	defer tx.Rollback(ctx)
	var num int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM factory_tasks WHERE project_id = $1::uuid`, req.ProjectID).Scan(&num); err != nil {
		return "", "", false, err
	}
	taskID = newUUID()
	key := fmt.Sprintf("%s-T%d", projKey, num)
	contract := fmt.Sprintf("---\nid: %s\nkey: %s\nkind: %s\ntitle: %q\nrole: \"@%s\"\nstate: queued\n---\n\n# %s: %s\n",
		taskID, key, taskKind, title, role, key, title)
	var ticketPtr *int64
	if req.TicketID != 0 {
		ticketPtr = &req.TicketID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO factory_tasks (
			id, project_id, number, key, kind, title, role, state, ticket_id, yaml_contract, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, 'queued', $8, $9, $10)`,
		taskID, req.ProjectID, num, key, taskKind, title, "@"+role, ticketPtr, contract, "@"+role); err != nil {
		return "", "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", false, err
	}
	return taskID, taskKind, true, nil
}

func bindPeerTaskTx(ctx context.Context, tx pgx.Tx, taskID, runID string) error {
	if taskID == "" {
		return nil
	}
	var subtaskID int64
	if err := tx.QueryRow(ctx, `SELECT subtask FROM runs WHERE id = $1::uuid`, runID).Scan(&subtaskID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE subtasks SET factory_task_id = $1::uuid WHERE id = $2`, taskID, subtaskID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE factory_tasks
		SET state = 'running', session_id = $2::uuid, subtask_id = $3, updated_at = now()
		WHERE id = $1::uuid`, taskID, runID, subtaskID)
	return err
}

func (d *Driver) enrichPeerSession(ctx context.Context, ps *PeerSession) {
	if ps == nil {
		return
	}
	var id, key string
	err := d.Pool.QueryRow(ctx, `SELECT ft.id::text, ft.key FROM subtasks s
		JOIN factory_tasks ft ON ft.id = s.factory_task_id
		WHERE s.id = $1`, ps.SubtaskID).Scan(&id, &key)
	if err == nil {
		ps.TaskID, ps.TaskKey = id, key
		_, _ = d.Pool.Exec(ctx, `UPDATE factory_tasks
			SET tokens_in = $2, tokens_out = $3, sandbox_id = $4,
			    state = CASE WHEN $5 = 'ended' AND state = 'running' THEN 'done' ELSE state END,
			    updated_at = now()
			WHERE id = $1::uuid`, id, ps.TokensIn, ps.TokensOut, ps.SandboxID, ps.State)
	}
}

// ListPeerSessions returns recent or active sessions filtered optionally by project id/name.
func (d *Driver) ListPeerSessions(ctx context.Context, projectRef string, activeOnly bool) ([]PeerSession, error) {
	q := `SELECT ` + viewColumns + viewFrom + `
		WHERE ($1 = '' OR p.id::text = $1 OR p.name = $1)
		  AND (NOT $2 OR r.state <> 'ended')
		ORDER BY r.started_at DESC LIMIT 100`
	rows, err := d.Pool.Query(ctx, q, strings.TrimSpace(projectRef), activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerSession{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		ps := peerSessionFromView(v)
		d.enrichPeerSession(ctx, &ps)
		out = append(out, ps)
	}
	return out, rows.Err()
}

// FindActivePeerSession returns the newest active ('starting' or 'working') peer session matching
// project, role, and optional ticketID (when > 0), or nil if none is currently running.
func (d *Driver) FindActivePeerSession(ctx context.Context, projectID, role string, ticketID int64) (*PeerSession, error) {
	norm := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(role), "@"))
	var runID string
	err := d.Pool.QueryRow(ctx, `SELECT r.id::text FROM runs r
		JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1::uuid AND s.role = $2
		  AND ($3 = 0 OR coalesce(s.ticket, 0) = $3)
		  AND r.state IN ('starting', 'working')
		ORDER BY r.started_at DESC LIMIT 1`, projectID, norm, ticketID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d.GetPeerSession(ctx, runID)
}

// GetPeerSession returns one peer session by run ID, reflecting the latest continued run if the subtask moved.
func (d *Driver) GetPeerSession(ctx context.Context, sessionID string) (*PeerSession, error) {
	det, err := d.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	v := det.RunView
	if len(det.Runs) > 0 {
		latest := det.Runs[len(det.Runs)-1]
		if latest.Number >= v.Number {
			v = latest
		}
	}
	ps := peerSessionFromView(v)
	d.enrichPeerSession(ctx, &ps)
	return &ps, nil
}

// TurnPeerSession sends a follow-up prompt turn to an existing peer session's subtask.
func (d *Driver) TurnPeerSession(ctx context.Context, sessionID, text string) (*PeerSession, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("turn text is required")
	}
	var subtask int64
	if err := d.Pool.QueryRow(ctx, `SELECT subtask FROM runs WHERE id = $1::uuid`, sessionID).Scan(&subtask); err != nil {
		return nil, err
	}
	nextID, err := d.Continue(ctx, subtask, ContinueRequest{Text: text})
	if err != nil {
		return nil, err
	}
	return d.GetPeerSession(ctx, nextID)
}

// SteerPeerSession injects a live mid-turn steering prompt into a running ACP session.
func (d *Driver) SteerPeerSession(ctx context.Context, sessionID, text string) error {
	return d.Steer(ctx, sessionID, text, nil)
}

// AbortPeerSession stops a running or idle peer session cleanly.
func (d *Driver) AbortPeerSession(ctx context.Context, sessionID string) error {
	err := d.Stop(ctx, sessionID)
	_, _ = d.Pool.Exec(ctx, `UPDATE factory_tasks SET state = CASE WHEN state IN ('queued','running') THEN 'cancelled' ELSE state END, updated_at = now()
		WHERE id = (SELECT s.factory_task_id FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1::uuid)`, sessionID)
	if err == nil || !errors.Is(err, ErrEnded) {
		return err
	}
	var state string
	if qerr := d.Pool.QueryRow(ctx, `SELECT state FROM runs WHERE id = $1::uuid`, sessionID).Scan(&state); qerr != nil {
		return qerr
	}
	if state == "ended" {
		return nil
	}
	return d.EndNow(ctx, sessionID, ReasonOperator, "aborted via agent api")
}

// StreamPeerSession reads stored ACP/trajectory events after the given sequence cursor.
func (d *Driver) StreamPeerSession(ctx context.Context, sessionID string, after int64, limit int) ([]Event, error) {
	return d.Events(ctx, sessionID, after, limit, false)
}
