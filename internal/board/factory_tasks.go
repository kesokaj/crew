package board

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
)

// FactoryTaskKinds are the off-board work unit kinds tracked through Tasks API outside Backlog (R217).
var FactoryTaskKinds = []string{"spec", "research", "dream", "coordinator", "chat_action"}

// FactoryTaskStates are the allowed lifecycle states for off-board factory tasks.
var FactoryTaskStates = []string{"queued", "running", "done", "failed", "cancelled"}

// FactoryTask represents one tracked non-code unit of work in Tasks API.
type FactoryTask struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"projectId"`
	Project      string    `json:"project,omitempty"`
	Number       int       `json:"number"`
	Key          string    `json:"key"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Role         string    `json:"role"`
	State        string    `json:"state"`
	TicketID     *int64    `json:"ticketId,omitempty"`
	TicketKey    string    `json:"ticketKey,omitempty"`
	SpecRef      string    `json:"specRef,omitempty"`
	OriginRoom   string    `json:"originRoom,omitempty"`
	YAMLContract string    `json:"yamlContract"`
	SessionID    *string   `json:"sessionId,omitempty"`
	SubtaskID    *int64    `json:"subtaskId,omitempty"`
	SandboxID    string    `json:"sandboxId,omitempty"`
	TriesUsed    int       `json:"triesUsed"`
	MaxTries     int       `json:"maxTries"`
	BudgetTokens int64     `json:"budgetTokens"`
	TokensIn     int64     `json:"tokensIn"`
	TokensOut    int64     `json:"tokensOut"`
	ArtifactPath string    `json:"artifactPath,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// NewFactoryTask describes a new off-board factory task created via Tasks API.
type NewFactoryTask struct {
	Kind         string
	Title        string
	Role         string
	State        string
	TicketRef    string
	TicketID     *int64
	SpecRef      string
	OriginRoom   string
	MaxTries     int
	BudgetTokens int64
	ArtifactPath string
	CreatedBy    string
}

// LedgerTicket summarizes an executable Kanban ticket inside the unified Tasks API ledger.
type LedgerTicket struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Stage         string `json:"stage"`
	SpecRef       string `json:"specRef,omitempty"`
	AssignedAgent string `json:"assignedAgent,omitempty"`
	TriesUsed     int    `json:"triesUsed"`
	MaxTries      int    `json:"maxTries"`
	BudgetTokens  int64  `json:"budgetTokens"`
	TokensIn      int64  `json:"tokensIn"`
	TokensOut     int64  `json:"tokensOut"`
	Hold          bool   `json:"hold"`
}

// TasksLedgerView unifies executable Kanban tickets (Backlog->Done) and off-board factory tasks.
type TasksLedgerView struct {
	Project       string         `json:"project"`
	Key           string         `json:"key"`
	KanbanTickets []LedgerTicket `json:"kanbanTickets"`
	FactoryTasks  []FactoryTask  `json:"factoryTasks"`
}

type taskContractYAML struct {
	ID           string `yaml:"id"`
	Key          string `yaml:"key"`
	Kind         string `yaml:"kind"`
	Title        string `yaml:"title"`
	Role         string `yaml:"role"`
	State        string `yaml:"state"`
	Ticket       string `yaml:"ticket,omitempty"`
	Spec         string `yaml:"spec,omitempty"`
	OriginRoom   string `yaml:"origin_room,omitempty"`
	MaxTries     int    `yaml:"max_tries"`
	BudgetTokens int64  `yaml:"budget_tokens,omitempty"`
	ArtifactPath string `yaml:"artifact_path,omitempty"`
}

// ValidateFactoryTaskKind ensures non-code Tasks API items never mix with executable Kanban kinds.
func ValidateFactoryTaskKind(raw string) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(raw))
	if slices.Contains(mirrorKinds, kind) {
		return "", errf(Invalid, "executable code work (%s) must enter Kanban Backlog as a ticket, not an off-board factory task", kind)
	}
	if !slices.Contains(FactoryTaskKinds, kind) {
		return "", errf(Invalid, "unsupported factory task kind %q (want spec, research, dream, coordinator, or chat_action)", raw)
	}
	return kind, nil
}

func defaultRoleForTaskKind(kind, role string) string {
	r := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(role), "@"))
	if r != "" {
		return "@" + r
	}
	switch kind {
	case "research":
		return "@researcher"
	case "dream":
		return "@dreamer"
	case "coordinator":
		return "@coordinator"
	case "spec":
		return "@planner"
	default:
		return "@assistant"
	}
}

func renderTaskYAMLContract(ft FactoryTask) (string, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(taskContractYAML{
		ID: ft.ID, Key: ft.Key, Kind: ft.Kind, Title: ft.Title, Role: ft.Role, State: ft.State,
		Ticket: ft.TicketKey, Spec: ft.SpecRef, OriginRoom: ft.OriginRoom,
		MaxTries: ft.MaxTries, BudgetTokens: ft.BudgetTokens, ArtifactPath: ft.ArtifactPath,
	})
	if err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	fmt.Fprintf(&buf, "---\n\n# %s: %s\n", ft.Key, oneLine(ft.Title))
	return buf.String(), nil
}

const factoryTaskCols = `ft.id::text, ft.project_id::text, pr.name, ft.number, ft.key, ft.kind, ft.title, ft.role,
	ft.state, ft.ticket_id, coalesce(pr.key || '-' || tk.number, ''), ft.spec_ref, ft.origin_room,
	ft.yaml_contract, ft.session_id::text, ft.subtask_id, ft.sandbox_id, ft.tries_used, ft.max_tries,
	ft.budget_tokens, ft.tokens_in, ft.tokens_out, ft.artifact_path, ft.summary, ft.created_by, ft.created_at, ft.updated_at`

const factoryTaskFrom = ` FROM factory_tasks ft JOIN projects pr ON pr.id = ft.project_id LEFT JOIN tickets tk ON tk.id = ft.ticket_id`

func scanFactoryTask(row pgx.Row) (FactoryTask, error) {
	var t FactoryTask
	err := row.Scan(&t.ID, &t.ProjectID, &t.Project, &t.Number, &t.Key, &t.Kind, &t.Title, &t.Role,
		&t.State, &t.TicketID, &t.TicketKey, &t.SpecRef, &t.OriginRoom,
		&t.YAMLContract, &t.SessionID, &t.SubtaskID, &t.SandboxID, &t.TriesUsed, &t.MaxTries,
		&t.BudgetTokens, &t.TokensIn, &t.TokensOut, &t.ArtifactPath, &t.Summary, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreateFactoryTask mints one sequential off-board task (<PROJ>-T<N>) with a YAML contract.
func (b *Board) CreateFactoryTask(ctx context.Context, projRef string, in NewFactoryTask) (*FactoryTask, error) {
	kind, err := ValidateFactoryTaskKind(in.Kind)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > maxTitle || strings.ContainsAny(title, "\n\r") {
		return nil, errf(Invalid, "title: one line of at most %d characters", maxTitle)
	}
	state := strings.TrimSpace(in.State)
	if state == "" {
		state = "queued"
	}
	if !slices.Contains(FactoryTaskStates, state) {
		return nil, errf(Invalid, "invalid factory task state %q", state)
	}
	who := strings.TrimSpace(in.CreatedBy)
	if who == "" {
		who = Backend
	}
	var out *FactoryTask
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, projRef, true)
		if err != nil {
			return err
		}
		var ticketID *int64
		var ticketKey string
		if in.TicketID != nil && *in.TicketID > 0 {
			tk, terr := ticketByID(ctx, tx, *in.TicketID, false)
			if terr != nil {
				return terr
			}
			if tk.Project != p.ID {
				return errf(Invalid, "ticket %s belongs to another project", tk.Key)
			}
			ticketID = &tk.ID
			ticketKey = tk.Key
		} else if strings.TrimSpace(in.TicketRef) != "" {
			num, perr := ParseNumber(p, in.TicketRef)
			if perr != nil {
				return perr
			}
			tk, terr := ticketByNumber(ctx, tx, p.ID, num, false)
			if terr != nil {
				return terr
			}
			ticketID = &tk.ID
			ticketKey = tk.Key
		}
		var nextNum int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM factory_tasks WHERE project_id = $1::uuid`, p.ID).Scan(&nextNum); err != nil {
			return err
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		maxTries := in.MaxTries
		if maxTries < 1 || maxTries > 20 {
			maxTries = 3
		}
		ft := FactoryTask{
			ID: id, ProjectID: p.ID, Project: p.Name, Number: nextNum,
			Key: fmt.Sprintf("%s-T%d", p.Key, nextNum), Kind: kind, Title: title,
			Role: defaultRoleForTaskKind(kind, in.Role), State: state,
			TicketID: ticketID, TicketKey: ticketKey, SpecRef: strings.TrimSpace(in.SpecRef),
			OriginRoom: strings.TrimSpace(in.OriginRoom), MaxTries: maxTries,
			BudgetTokens: max(in.BudgetTokens, int64(0)), ArtifactPath: strings.TrimSpace(in.ArtifactPath),
			CreatedBy: who,
		}
		ft.YAMLContract, err = renderTaskYAMLContract(ft)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO factory_tasks (
				id, project_id, number, key, kind, title, role, state, ticket_id, spec_ref, origin_room,
				yaml_contract, max_tries, budget_tokens, artifact_path, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			ft.ID, ft.ProjectID, ft.Number, ft.Key, ft.Kind, ft.Title, ft.Role, ft.State, ft.TicketID,
			ft.SpecRef, ft.OriginRoom, ft.YAMLContract, ft.MaxTries, ft.BudgetTokens, ft.ArtifactPath, ft.CreatedBy); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "factory_tasks", ft.Key, nil, ft); err != nil {
			return err
		}
		got, err := scanFactoryTask(tx.QueryRow(ctx, `SELECT `+factoryTaskCols+factoryTaskFrom+` WHERE ft.id = $1::uuid`, ft.ID))
		if err != nil {
			return err
		}
		out = &got
		return nil
	})
	return out, err
}

// GetFactoryTask returns one off-board task by UUID or <PROJ>-T<N> key within projRef.
func (b *Board) GetFactoryTask(ctx context.Context, projRef, idOrKey string) (*FactoryTask, error) {
	p, err := b.Project(ctx, projRef)
	if err != nil {
		return nil, err
	}
	ft, err := scanFactoryTask(b.Pool.QueryRow(ctx, `SELECT `+factoryTaskCols+factoryTaskFrom+`
		WHERE ft.project_id = $1::uuid AND (ft.id::text = $2 OR upper(ft.key) = upper($2))`, p.ID, strings.TrimSpace(idOrKey)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no such factory task %q", idOrKey)
	}
	return &ft, err
}

// ListFactoryTasks lists off-board factory tasks for projRef, optionally filtered by kind or state.
func (b *Board) ListFactoryTasks(ctx context.Context, projRef, kindFilter, stateFilter string) ([]FactoryTask, error) {
	p, err := b.Project(ctx, projRef)
	if err != nil {
		return nil, err
	}
	rows, err := b.Pool.Query(ctx, `SELECT `+factoryTaskCols+factoryTaskFrom+`
		WHERE ft.project_id = $1::uuid
		  AND ($2 = '' OR ft.kind = $2)
		  AND ($3 = '' OR ft.state = $3)
		ORDER BY ft.number DESC`, p.ID, strings.TrimSpace(kindFilter), strings.TrimSpace(stateFilter))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FactoryTask, error) { return scanFactoryTask(r) })
}

// CompleteFactoryTask updates a factory task's terminal/progress state, artifact path, and summary.
func (b *Board) CompleteFactoryTask(ctx context.Context, projRef, idOrKey, state, summary, artifactPath, who string) (*FactoryTask, error) {
	st := strings.TrimSpace(state)
	if st == "" {
		st = "done"
	}
	if !slices.Contains([]string{"done", "failed", "cancelled"}, st) {
		return nil, errf(Invalid, "completion state must be done, failed, or cancelled")
	}
	if strings.TrimSpace(who) == "" {
		who = Backend
	}
	var out *FactoryTask
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, projRef, false)
		if err != nil {
			return err
		}
		cur, err := scanFactoryTask(tx.QueryRow(ctx, `SELECT `+factoryTaskCols+factoryTaskFrom+`
			WHERE ft.project_id = $1::uuid AND (ft.id::text = $2 OR upper(ft.key) = upper($2)) FOR UPDATE OF ft`, p.ID, strings.TrimSpace(idOrKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no such factory task %q", idOrKey)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE factory_tasks
			SET state = $2, summary = coalesce(nullif($3, ''), summary),
			    artifact_path = coalesce(nullif($4, ''), artifact_path), updated_at = now()
			WHERE id = $1::uuid`, cur.ID, st, strings.TrimSpace(summary), strings.TrimSpace(artifactPath)); err != nil {
			return err
		}
		updated, err := scanFactoryTask(tx.QueryRow(ctx, `SELECT `+factoryTaskCols+factoryTaskFrom+` WHERE ft.id = $1::uuid`, cur.ID))
		if err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "factory_tasks", updated.Key, cur, updated); err != nil {
			return err
		}
		out = &updated
		return nil
	})
	return out, err
}

// TasksLedger returns both executable Kanban tickets and off-board factory tasks with token totals.
func (b *Board) TasksLedger(ctx context.Context, projRef string) (*TasksLedgerView, error) {
	p, err := b.Project(ctx, projRef)
	if err != nil {
		return nil, err
	}
	ts, err := tickets(ctx, b.Pool, p.ID, "")
	if err != nil {
		return nil, err
	}
	tokenRows, err := b.Pool.Query(ctx, `SELECT s.ticket, COALESCE(SUM(r.tokens_in), 0), COALESCE(SUM(r.tokens_out), 0)
		FROM subtasks s JOIN runs r ON r.subtask = s.id
		WHERE s.ticket IS NOT NULL GROUP BY s.ticket`)
	if err != nil {
		return nil, err
	}
	defer tokenRows.Close()
	tokMap := map[int64][2]int64{}
	for tokenRows.Next() {
		var tid, tin, tout int64
		if err := tokenRows.Scan(&tid, &tin, &tout); err != nil {
			return nil, err
		}
		tokMap[tid] = [2]int64{tin, tout}
	}
	if err := tokenRows.Err(); err != nil {
		return nil, err
	}
	kt := make([]LedgerTicket, 0, len(ts))
	for _, t := range ts {
		toks := tokMap[t.ID]
		kt = append(kt, LedgerTicket{
			Key: t.Key, Title: t.Title, Kind: t.Kind, Stage: t.Stage,
			SpecRef: t.SpecRef, AssignedAgent: t.AssignedAgent,
			TriesUsed: t.TriesUsed, MaxTries: t.MaxTries, BudgetTokens: t.BudgetTokens,
			TokensIn: toks[0], TokensOut: toks[1], Hold: t.Hold,
		})
	}
	ft, err := b.ListFactoryTasks(ctx, p.ID, "", "")
	if err != nil {
		return nil, err
	}
	return &TasksLedgerView{Project: p.Name, Key: p.Key, KanbanTickets: kt, FactoryTasks: ft}, nil
}
