package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// RunView is a run as the monitor and the Run screen show it (8.1, 20.1).
type RunView struct {
	ID              string          `json:"id"`
	Subtask         int64           `json:"subtask"`
	Number          int             `json:"number"`
	Title           string          `json:"title"`
	SubtaskState    string          `json:"subtaskState"`
	WaitFor         string          `json:"waitFor,omitempty"`
	State           string          `json:"state"`
	Stopping        string          `json:"stopping,omitempty"`
	StopReason      string          `json:"stopReason,omitempty"`
	StopDetail      string          `json:"stopDetail,omitempty"`
	CLI             string          `json:"cli"`
	CLIVersion      string          `json:"cliVersion,omitempty"`
	Model           string          `json:"model,omitempty"`
	Effort          string          `json:"effort,omitempty"`
	CrewVersion     string          `json:"crewVersion"`
	Image           string          `json:"image"`
	Sandbox         string          `json:"sandbox,omitempty"`
	Conversation    string          `json:"conversation"`
	CLIConversation string          `json:"cliConversation,omitempty"`
	Way             string          `json:"way"`
	Continues       string          `json:"continues,omitempty"`
	Branch          string          `json:"branch"`
	Base            string          `json:"base,omitempty"`
	WIP             string          `json:"wip,omitempty"`
	Turn            int             `json:"turn"`
	Events          int64           `json:"events"`
	ToolSteps       int             `json:"toolSteps"`
	MaxTurns        int             `json:"maxTurns"`
	Tools           bool            `json:"tools"`
	TimeoutSeconds  int             `json:"timeoutSeconds"`
	TokensIn        int64           `json:"tokensIn"`
	TokensOut       int64           `json:"tokensOut"`
	TokensCached    int64           `json:"tokensCached"`
	LastAction      string          `json:"lastAction,omitempty"`
	Warnings        json.RawMessage `json:"warnings"`
	StartedAt       time.Time       `json:"startedAt"`
	LastEventAt     *time.Time      `json:"lastEventAt,omitempty"`
	EndedAt         *time.Time      `json:"endedAt,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	PromptSHA256    string          `json:"promptSha256,omitempty"`
	// Where the run belongs (20.1, Subtask): its role and kind, its project, and its ticket with the
	// subtask's number under it; empty for a run of no project.
	Role          string `json:"role"`
	Kind          string `json:"kind"`
	Project       string `json:"project,omitempty"`
	Ticket        string `json:"ticket,omitempty"`
	SubtaskNumber int    `json:"subtaskNumber"`
	// Push says the run may push its branch; a run that judges or analyses reads only (6.1).
	Push      bool   `json:"push"`
	ModelFrom string `json:"modelFrom,omitempty"`
	// ModelName is what the CLI was given, ModelResolved the full name it said it ran, and EffortFrom
	// where the effort came from (12.3).
	ModelName     string `json:"modelName,omitempty"`
	ModelResolved string `json:"modelResolved,omitempty"`
	EffortFrom    string `json:"effortFrom,omitempty"`
	// Mode is the network mode of its sandbox (13.6), as the sandbox recorded it when it started.
	Mode string `json:"mode,omitempty"`
	// Size is the size of its sandbox (13.10), small or large; empty for a run from before 0.47.
	Size string `json:"size,omitempty"`
}

const viewColumns = `r.id, r.subtask, r.number, s.title, s.state, coalesce(s.wait_for, ''), r.state, coalesce(r.stopping, ''),
	coalesce(r.stop_reason, ''), coalesce(r.stop_detail, ''), r.cli, coalesce(r.cli_version, ''), coalesce(r.model, ''),
	coalesce(r.effort, ''), r.crew_version, r.image, coalesce(r.sandbox, ''), r.conversation,
	coalesce((SELECT e.payload->>'conversationId' FROM events e WHERE e.run = r.id AND e.payload ? 'conversationId' ORDER BY e.seq LIMIT 1),
		c.cli_id, ''), r.way,
	coalesce(r.continues::text, ''), coalesce(r.branch, ''), coalesce(r.base_ref, ''), coalesce(r.wip_ref, ''), r.turn, r.events,
	r.tool_steps, r.max_turns, r.timeout_s, r.tokens_in, r.tokens_out, r.tokens_cached, coalesce(r.last_action, ''), r.warnings,
	r.started_at, r.last_event_at, r.ended_at, r.exit_code, coalesce(r.prompt_sha256, ''), r.tools,
	s.role, s.kind, coalesce(p.name, ''), coalesce(p.key || '-' || t.number, ''), coalesce(s.number, 0), r.push, coalesce(r.model_from, ''),
	coalesce(r.model_name, r.model, ''), coalesce(r.model_resolved, ''), coalesce(r.effort_from, ''),
	coalesce((SELECT sb.mode FROM sandboxes sb WHERE sb.id = r.sandbox), ''), coalesce(r.size, '')`

const viewFrom = ` FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN conversations c ON c.id = r.conversation
	LEFT JOIN projects p ON p.id = s.project LEFT JOIN tickets t ON t.id = s.ticket`

func scanView(row pgx.Row) (RunView, error) {
	var v RunView
	err := row.Scan(&v.ID, &v.Subtask, &v.Number, &v.Title, &v.SubtaskState, &v.WaitFor, &v.State, &v.Stopping, &v.StopReason,
		&v.StopDetail, &v.CLI, &v.CLIVersion, &v.Model, &v.Effort, &v.CrewVersion, &v.Image, &v.Sandbox, &v.Conversation,
		&v.CLIConversation, &v.Way, &v.Continues, &v.Branch, &v.Base, &v.WIP, &v.Turn, &v.Events, &v.ToolSteps, &v.MaxTurns,
		&v.TimeoutSeconds, &v.TokensIn, &v.TokensOut, &v.TokensCached, &v.LastAction, &v.Warnings, &v.StartedAt, &v.LastEventAt,
		&v.EndedAt, &v.ExitCode, &v.PromptSHA256, &v.Tools, &v.Role, &v.Kind, &v.Project, &v.Ticket, &v.SubtaskNumber, &v.Push, &v.ModelFrom,
		&v.ModelName, &v.ModelResolved, &v.EffortFrom, &v.Mode, &v.Size)
	return v, err
}

// List is every run that is not ended, the subtasks that wait, and the last runs that ended.
type List struct {
	Running  []RunView `json:"running"`
	Waiting  []RunView `json:"waiting"`
	Finished []RunView `json:"finished"`
}

// List reads what the Runs screen shows.
func (d *Driver) List(ctx context.Context) (List, error) {
	l := List{Running: []RunView{}, Waiting: []RunView{}, Finished: []RunView{}}
	collect := func(q string, args ...any) ([]RunView, error) {
		rows, err := d.Pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []RunView{}
		for rows.Next() {
			v, err := scanView(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	var err error
	if l.Running, err = collect(`SELECT ` + viewColumns + viewFrom + ` WHERE r.state <> 'ended' ORDER BY r.started_at DESC`); err != nil {
		return l, err
	}
	// A waiting subtask is shown by its last run.
	if l.Waiting, err = collect(`SELECT ` + viewColumns + viewFrom + ` WHERE s.state IN ('waiting', 'stopped') AND r.state = 'ended'
		AND r.number = (SELECT max(number) FROM runs WHERE subtask = s.id) ORDER BY s.updated_at DESC`); err != nil {
		return l, err
	}
	l.Finished, err = collect(`SELECT ` + viewColumns + viewFrom + ` WHERE r.state = 'ended' ORDER BY r.ended_at DESC LIMIT 50`)
	return l, err
}

// Turn is one turn of a conversation (12.10).
type Turn struct {
	Number    int       `json:"number"`
	Run       string    `json:"run,omitempty"`
	Speaker   string    `json:"speaker"`
	Text      string    `json:"text"`
	ToolCalls []string  `json:"toolCalls"`
	Tokens    int64     `json:"tokens"`
	Rebuilt   bool      `json:"rebuilt"`
	At        time.Time `json:"at"`
}

// Detail is one run with what the Run screen shows of it (20.1, run.png): the prompt, the turns of its
// conversation, the other runs of its subtask, and the CLI's files kept for it.
type Detail struct {
	RunView
	Prompt      string `json:"prompt"`
	PromptParts any    `json:"promptParts"`
	// Parts is the prompt cut into its parts, each with its source and its text, as the task view
	// shows it (8.6, 20.2); empty when the saved parts do not add up to the prompt.
	Parts []PartText `json:"parts"`
	// Command is how the CLI was started, as its adapter starts it, with the prompt on standard input.
	Command  []string  `json:"command"`
	Turns    []Turn    `json:"turns"`
	Runs     []RunView `json:"runs"`
	CLIFiles []CLIFile `json:"cliFiles"`
	// Sizing is what a microVM of the run's size has, by the machine's settings now (13.10, 14.1): its
	// virtual CPUs and memory with the 1 and 2 GB every microVM has, and whether it has a Docker daemon
	// of its own. Absent when the size or the settings cannot be read.
	Sizing *SizeView `json:"sizing,omitempty"`
}

// SizeView is one sandbox size as the run's page shows it.
type SizeView struct {
	CPUs   float64 `json:"cpus"`
	MemMB  int64   `json:"memMB"`
	Docker bool    `json:"docker"`
}

// PartText is one part of a saved prompt with its text.
type PartText struct {
	Part   string `json:"part"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

// PromptOf is a run's prompt as it was saved, cut into its parts; the parts are empty when they do
// not add up to the prompt, and both when the run saved none (the chat's get_subtask, 11.9).
func PromptOf(ctx context.Context, pool *pgxpool.Pool, run string) (string, []PartText, error) {
	prompt, parts, err := savedPrompt(ctx, pool, run)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", []PartText{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	return prompt, cutParts(prompt, parts), nil
}

func savedPrompt(ctx context.Context, pool *pgxpool.Pool, run string) (string, []byte, error) {
	var gz, parts []byte
	if err := pool.QueryRow(ctx, `SELECT gzip, parts FROM run_prompts WHERE run = $1::uuid`, run).Scan(&gz, &parts); err != nil {
		return "", nil, err
	}
	b, err := gunzip(gz)
	if err != nil {
		return "", parts, nil
	}
	return string(b), parts, nil
}

// cutParts cuts a prompt into the parts it was joined from (Join): each part's bytes, then a blank
// line. A prompt its parts do not add up to exactly is not cut.
func cutParts(prompt string, saved []byte) []PartText {
	var ps []struct {
		Part   string `json:"part"`
		Source string `json:"source"`
		Bytes  int    `json:"bytes"`
	}
	out := []PartText{}
	if json.Unmarshal(saved, &ps) != nil || len(ps) == 0 {
		return out
	}
	at := 0
	for i, p := range ps {
		if i > 0 {
			if !strings.HasPrefix(prompt[at:], "\n\n") {
				return []PartText{}
			}
			at += 2
		}
		if p.Bytes < 0 || at+p.Bytes > len(prompt) {
			return []PartText{}
		}
		out = append(out, PartText{Part: p.Part, Source: p.Source, Text: prompt[at : at+p.Bytes]})
		at += p.Bytes
	}
	if at != len(prompt) {
		return []PartText{}
	}
	return out
}

// CLIFile is one of the CLI's own files of the conversation, as kept (12.10).
type CLIFile struct {
	Path      string    `json:"path"`
	Bytes     int       `json:"bytes"`
	SavedAt   time.Time `json:"savedAt"`
	KeepUntil time.Time `json:"keepUntil"`
}

// Get reads one run.
func (d *Driver) Get(ctx context.Context, id string) (Detail, error) {
	v, err := scanView(d.Pool.QueryRow(ctx, `SELECT `+viewColumns+viewFrom+` WHERE r.id = $1`, id))
	if err != nil {
		return Detail{}, err
	}
	det := Detail{RunView: v, Turns: []Turn{}, Runs: []RunView{}, CLIFiles: []CLIFile{}, Command: []string{}, Parts: []PartText{}}
	if d.Room != nil && v.Size != "" {
		if z, err := d.Room.SizeFor(ctx, v.Size); err == nil && z.MemMB > 0 {
			det.Sizing = &SizeView{CPUs: z.CPUs, MemMB: z.MemMB, Docker: z.Docker}
		}
	}
	if a, ok := cli.Get(v.CLI); ok {
		t := cli.Turn{Model: v.ModelName, Effort: v.Effort, Workspace: runspec.Workspace, Tools: v.Tools,
			Timeout: time.Duration(v.TimeoutSeconds) * time.Second, MaxTurns: v.MaxTurns}
		if v.Way == WayFiles || v.Turn > 1 {
			t.Continue = v.CLIConversation
		}
		det.Command = a.TurnCommand(t).Args
	}
	if prompt, parts, err := savedPrompt(ctx, d.Pool, id); err == nil {
		det.Prompt = prompt
		json.Unmarshal(parts, &det.PromptParts)
		det.Parts = cutParts(det.Prompt, parts)
	}
	rows, err := d.Pool.Query(ctx, `SELECT number, coalesce(run::text, ''), speaker, text, tool_calls, tokens, rebuilt, at
		FROM turns WHERE conversation = $1 ORDER BY number`, v.Conversation)
	if err != nil {
		return det, err
	}
	for rows.Next() {
		var t Turn
		var calls []byte
		if rows.Scan(&t.Number, &t.Run, &t.Speaker, &t.Text, &calls, &t.Tokens, &t.Rebuilt, &t.At) == nil {
			json.Unmarshal(calls, &t.ToolCalls)
			if t.ToolCalls == nil {
				t.ToolCalls = []string{}
			}
			det.Turns = append(det.Turns, t)
		}
	}
	rows.Close()
	rows, err = d.Pool.Query(ctx, `SELECT `+viewColumns+viewFrom+` WHERE r.subtask = $1 ORDER BY r.number`, v.Subtask)
	if err != nil {
		return det, err
	}
	for rows.Next() {
		if rv, err := scanView(rows); err == nil {
			det.Runs = append(det.Runs, rv)
		}
	}
	rows.Close()
	rows, err = d.Pool.Query(ctx, `SELECT path, length(gzip), saved_at, keep_until FROM cli_files WHERE conversation = $1 ORDER BY path`,
		v.Conversation)
	if err != nil {
		return det, err
	}
	for rows.Next() {
		var f CLIFile
		if rows.Scan(&f.Path, &f.Bytes, &f.SavedAt, &f.KeepUntil) == nil {
			det.CLIFiles = append(det.CLIFiles, f)
		}
	}
	rows.Close()
	return det, nil
}

// Event is one stored event of a run's trajectory (8.3).
type Event struct {
	Seq     int64           `json:"seq"`
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Raw     string          `json:"raw,omitempty"`
}

// Events reads a run's events after seq, at most limit, in order: the live trajectory polls with the
// last seq it has.
func (d *Driver) Events(ctx context.Context, run string, after int64, limit int, raw bool) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := d.Pool.Query(ctx, `SELECT seq, at, type, payload, CASE WHEN $4 THEN coalesce(raw, '') ELSE '' END
		FROM events WHERE run = $1 AND seq > $2 ORDER BY seq LIMIT $3`, run, after, limit, raw)
	return collectEvents(rows, err)
}

// Tail is the last n events of a run, oldest first: the last lines of its log (20.2).
func (d *Driver) Tail(ctx context.Context, run string, n int) ([]Event, error) {
	if n <= 0 || n > 100 {
		n = 10
	}
	rows, err := d.Pool.Query(ctx, `SELECT seq, at, type, payload, '' FROM (SELECT * FROM events WHERE run = $1 ORDER BY seq DESC LIMIT $2) e
		ORDER BY seq`, run, n)
	return collectEvents(rows, err)
}

func collectEvents(rows pgx.Rows, err error) ([]Event, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.At, &e.Type, &e.Payload, &e.Raw); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// StoreProof stores the event of install's proof, a line a microVM printed, as the event of a
// conversation of its own, and reads it back (16.11).
func StoreProof(ctx context.Context, pool *pgxpool.Pool, line []byte) (time.Time, error) {
	if !json.Valid(line) {
		return time.Time{}, errors.New("the proof's event is not JSON")
	}
	conv := newUUID()
	at := time.Now().UTC()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO conversations (id, purpose) VALUES ($1, 'proof')`, conv); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO events (conversation, seq, at, type, payload, raw) VALUES ($1, 1, $2, 'status', $3, $4)`,
			conv, at, line, string(line))
		return err
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("storing the proof's event: %w", err)
	}
	var raw string
	var stored time.Time
	if err := pool.QueryRow(ctx, `SELECT raw, at FROM events WHERE conversation = $1 AND seq = 1`, conv).Scan(&raw, &stored); err != nil {
		return time.Time{}, fmt.Errorf("reading the proof's event back: %w", err)
	}
	if raw != string(line) {
		return time.Time{}, fmt.Errorf("stored %q and read back %q", line, raw)
	}
	return stored, nil
}
