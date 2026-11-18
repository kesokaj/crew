// Package board is the board core (design 6.3, 6.4, 7.1, 7.3, 7.4, 7.6, 7.7, 7.12): projects and
// their plans, tickets with their why, what, goal, and checks, analysis decisions, and the moves of
// tickets through the fixed and the optional stages. A ticket moves only to the right, and only when
// the checks of its stage pass, as the backend evaluates them; nothing an agent says moves it.
//
// Every change is one transaction with its rows in changes (6.4).
package board

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// Board works on every project's board.
type Board struct {
	Pool *pgxpool.Pool
	Git  *gitstore.Store
	// Identity is the operator's git identity from Setup, for the first commit of a new repository.
	Identity func(ctx context.Context) (gitstore.Identity, error)
	// ReadHeader gives the header that lets the server read an existing repository at url, such as a
	// token of the GitHub App that can only read, and a function that ends it. "" reads without one.
	ReadHeader func(ctx context.Context, url string) (header string, done func(), err error)
	// GitHub creates a project's repository on GitHub and reaches it over SSH (6.6); nil without it.
	GitHub GitHub
	// Pulls is GitHub's side of a ticket's pull request (7.12); nil opens none.
	Pulls Pulls
	// PushTimeout bounds one push to a remote; two minutes when 0.
	PushTimeout time.Duration
	// Checks replaces the evaluation of the checks it names. Tests of the board's moves use it for
	// the checks that need Crew's records and its check sandbox; with the merge check stood in for, a
	// move to Done moves no main.
	Checks map[string]Check
	Now    func() time.Time
	Log    *slog.Logger
	// OnRoomEvent optionally mirrors board/spec/research milestones into #<project> / feature threads.
	OnRoomEvent func(ctx context.Context, p *Project, threadKey, specRef, ticketKey, senderRole, runID, body string)

	kick     chan struct{}
	kickInit sync.Once
}

func (b *Board) emitRoomEvent(ctx context.Context, p *Project, threadKey, specRef, ticketKey, senderRole, runID, body string) {
	if b != nil && b.OnRoomEvent != nil && p != nil {
		b.OnRoomEvent(ctx, p, threadKey, specRef, ticketKey, senderRole, runID, body)
	}
}

func (b *Board) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Board) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// Kind says what kind of refusal an Error is.
type Kind int

const (
	NotFound Kind = iota + 1
	Invalid
	Conflict
	// Refused is a request that is well formed but not allowed now, such as a move to the left.
	Refused
)

// Error is a refusal, with a message for the operator.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(k Kind, format string, a ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

// Who changed something, as the history names it: the operator, the backend, or an agent's role.
const (
	Operator = "operator"
	Backend  = "backend"
)

// Project is a project's row.
type Project struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Key         string  `json:"key"`
	Goal        string  `json:"goal"`
	State       string  `json:"state"`
	PauseReason *string `json:"pauseReason"`
	RepoSource  string  `json:"repoSource"`
	// Remote is where git pushes main over SSH, for a project with a repository on GitHub (6.6).
	Remote *string `json:"remote"`
	// Draft is the plan draft the project was made from.
	Draft     *string    `json:"draft"`
	OKAt      *time.Time `json:"okAt"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	// Hidden is a project of Crew's own, the gate's crew-gate (16.2): it runs as any project but for
	// the lead's retrospective and final report, which it never gets (7.2, 9.8), and no list of
	// projects shows it.
	Hidden bool `json:"hidden,omitempty"`
	// Protected says the project is neither archived nor deleted while it is on (7.13). Crew's own
	// project (16.2) is always protected, and cannot be switched off.
	Protected bool `json:"protected"`
}

const projectCols = `id::text, name, key, goal, state, pause_reason, repo_source, remote, draft::text, ok_at, created_at, updated_at, hidden, protected`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Key, &p.Goal, &p.State, &p.PauseReason, &p.RepoSource, &p.Remote, &p.Draft, &p.OKAt, &p.CreatedAt, &p.UpdatedAt, &p.Hidden, &p.Protected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no such project")
	}
	return &p, err
}

// Project returns a project by its name or its id.
func (b *Board) Project(ctx context.Context, ref string) (*Project, error) {
	return projectByRef(ctx, b.Pool, ref, false)
}

func projectByRef(ctx context.Context, q db.Querier, ref string, lock bool) (*Project, error) {
	sql := `SELECT ` + projectCols + ` FROM projects WHERE name = $1 OR id::text = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanProject(q.QueryRow(ctx, sql, ref))
}

// ProjectIn reads a project by its name or its id with q, such as a transaction of one snapshot.
func ProjectIn(ctx context.Context, q db.Querier, ref string) (*Project, error) {
	return projectByRef(ctx, q, ref, false)
}

// TicketsIn reads every ticket of a project with q.
func TicketsIn(ctx context.Context, q db.Querier, project string) ([]*Ticket, error) {
	return tickets(ctx, q, project, "")
}

// Projects returns every project, the archived ones last.
func (b *Board) Projects(ctx context.Context) ([]*Project, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+projectCols+` FROM projects ORDER BY state = 'archived', name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Settings returns a project's settings as they apply.
func (b *Board) Settings(ctx context.Context, q db.Querier, p *Project) (settings.Doc, error) {
	return settings.EffectiveProject(ctx, q, p.ID, p.Name, p.Key)
}

// Ticket is a ticket's row, with its checks and links (6.3).
type Ticket struct {
	ID           int64    `json:"-"`
	Project      string   `json:"project"`
	Key          string   `json:"key"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Kind         string   `json:"kind"`
	Part         *string  `json:"part"`
	Source       *Source  `json:"source"`
	Why          string   `json:"why"`
	What         string   `json:"what"`
	Goal         string   `json:"goal"`
	Approach     *string  `json:"approach"`
	Docs         []string `json:"docs"`
	DocsReason   *string  `json:"docsReason"`
	Stage        string   `json:"stage"`
	ClosedReason *string  `json:"closedReason"`
	Skips        []string `json:"skips"`
	SkipsReason  *string  `json:"skipsReason"`
	// Network is the ticket's own network mode, or "inherit" for its project's (13.6).
	Network   string   `json:"network"`
	Origin    string   `json:"origin"`
	CreatedBy string   `json:"createdBy"`
	Seen      *string  `json:"seen"`
	Analysis  Analysis `json:"analysis"`
	Priority  int      `json:"priority"`
	// OperatorPriority is the priority the operator set, which alone orders the lead queue across
	// projects (7.9, from 0.42); nil when the priority is an agent's or the default.
	OperatorPriority *int `json:"operatorPriority"`
	// CodeWait is the head whose files that are code wait for the operator, and CodeSay their answer
	// about a head (13.8).
	CodeWait    *CodeWait  `json:"codeWait,omitempty"`
	CodeSay     *CodeSay   `json:"codeSay,omitempty"`
	Position    int64      `json:"-"`
	Hold        bool       `json:"hold"`
	NoticeUntil *time.Time `json:"noticeUntil"`
	Branch      *string    `json:"branch"`
	FixRounds   int        `json:"fixRounds"`
	// Approaches is 1 once the lead gave the ticket a new approach (7.7), at ApproachAt: it has two
	// more fix rounds, and what failed before it no longer counts.
	Approaches int        `json:"approaches"`
	ApproachAt *time.Time `json:"approachAt"`
	// RevertOf is the merge a ticket of the kind revert undoes (7.13), and Amendment the amendment to
	// the plan a ticket came from.
	RevertOf  *string `json:"revertOf,omitempty"`
	Amendment *int64  `json:"amendment,omitempty"`
	Waiting   *string `json:"waiting"`
	// Stuck says why the work around the ticket starts nothing more for it, such as a run that
	// stopped twice; nil while it goes on.
	Stuck *string `json:"stuck"`
	// Agents are the operator's pins on this ticket: for a role, a model with its CLI, an effort, or
	// both (12.3). They apply to the next subtask of the role that starts.
	Agents    map[string]any `json:"agents"`
	StageAt   time.Time      `json:"stageAt"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Checks    []CheckSpec    `json:"checks"`
	Links     []Link         `json:"links"`
	// Skill marks a ticket that finds or writes a skill an agent asked for (12.9).
	Skill bool `json:"skill"`
	// Conversation is the conversation or the chat a ticket of origin conversation or chat came from,
	// ConversationRun the assistant's run that proposed it, and AdoptedAt when the operator made a
	// proposed one theirs with Make a ticket of this (11.8; plan M20 D19).
	Conversation    *string    `json:"conversation,omitempty"`
	ConversationRun *string    `json:"conversationRun,omitempty"`
	AdoptedAt       *time.Time `json:"adoptedAt,omitempty"`
	SpecRef         string     `json:"specRef,omitempty"`
	MaxTries        int        `json:"maxTries"`
	TriesUsed       int        `json:"triesUsed"`
	AssignedAgent   string     `json:"assignedAgent,omitempty"`
	BudgetTokens    int64      `json:"budgetTokens,omitempty"`
}

// Source is where in the plan a ticket comes from.
type Source struct {
	File    string `json:"file" yaml:"file"`
	Heading string `json:"heading,omitempty" yaml:"heading,omitempty"`
	Quote   string `json:"quote,omitempty" yaml:"quote,omitempty"`
}

// Analysis is what Analysis decided about a ticket (7.6).
type Analysis struct {
	// waiting | proposed | analysing | ready | skipped | new_feature_notice | waits_for_operator
	Status string `json:"status" yaml:"status"`
	// Proposal is what the analysing agent decided about a ticket the operator decides, with
	// approvals.created_task set to operator (7.3, since 0.47): its choice, reason, and condition.
	Proposal  *Proposal  `json:"proposal,omitempty" yaml:"proposal,omitempty"`
	DecidedBy string     `json:"decidedBy,omitempty" yaml:"decided_by,omitempty"`
	Reason    string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	LookAgain *LookAgain `json:"lookAgain,omitempty" yaml:"look_again,omitempty"`
	MayChange []string   `json:"mayChange,omitempty" yaml:"may_change,omitempty"`
	// MayChangeAgreed says the operator agreed that a ticket an agent created may change the files
	// that instruct agents its MayChange names (13.8). A new list needs a new yes.
	MayChangeAgreed bool `json:"mayChangeAgreed,omitempty" yaml:"may_change_agreed,omitempty"`
	// Approved marks a ticket approved by being added that is not the plan's or the operator's: one
	// a personal token with "may answer" created (11.7).
	Approved bool `json:"approvedByAdding,omitempty" yaml:"approved_by_adding,omitempty"`
	// What was left of a new feature's notice when it was held: Hold stops its clock (7.3).
	// NoticePaused says a hold stopped a clock, which letting the ticket go starts again; a notice
	// that waits for the operator has no clock to start.
	NoticeLeftSeconds int64 `json:"noticeLeftSeconds,omitempty" yaml:"notice_left_seconds,omitempty"`
	NoticePaused      bool  `json:"noticePaused,omitempty" yaml:"notice_paused,omitempty"`
}

// The analysis statuses (6.3).
const (
	StatusWaiting   = "waiting"
	StatusProposed  = "proposed"
	StatusAnalysing = "analysing"
	StatusReady     = "ready"
	StatusSkipped   = "skipped"
	StatusNotice    = "new_feature_notice"
	// StatusOperator is a ticket the operator did not make, analysed, whose decision waits for the
	// operator (approvals.created_task: operator; 7.3, since 0.47).
	StatusOperator = "waits_for_operator"
)

// Proposal is the analysing agent's decision on a ticket that waits for the operator.
type Proposal struct {
	Choice    string     `json:"choice" yaml:"choice"` // approve | skip | discard
	Reason    string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	By        string     `json:"by" yaml:"by"`
	LookAgain *LookAgain `json:"lookAgain,omitempty" yaml:"look_again,omitempty"`
}

// LookAgain is the condition on which a skipped ticket is analysed again: one of four kinds, so that
// Crew can test it (7.6).
type LookAgain struct {
	Kind  string `json:"kind" yaml:"kind"` // part_done | ticket_done | date | all_done
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
}

// CheckSpec is one of a ticket's own checks (7.1).
type CheckSpec struct {
	Kind string `json:"kind" yaml:"kind"` // cmd | judge
	Spec string `json:"spec" yaml:"spec"`
	// Keeps marks a command check that must keep passing, which passes before the work too: the
	// base check refuses every other command check that passes on the ticket's base (7.6).
	Keeps bool `json:"keeps,omitempty" yaml:"keeps,omitempty"`
}

// Link is a link from one ticket to another (7.12).
type Link struct {
	Kind string `json:"kind" yaml:"kind"`
	To   string `json:"to" yaml:"to"` // the other ticket's key
}

const ticketCols = `t.id, t.project::text, p.key, t.number, t.title, t.kind, t.part, t.source, t.why, t.what, t.goal,
	t.approach, t.docs, t.docs_reason, t.stage, t.closed_reason, t.skips, t.skips_reason, t.origin, t.created_by,
	t.seen, t.analysis, t.priority, t.position, t.hold, t.notice_until, t.branch,
	t.fix_rounds, t.waiting, t.stuck, t.stage_at, t.created_at, t.updated_at, t.agents, t.operator_priority, t.code_wait, t.code_say, t.network,
	t.approaches, t.approach_at, t.revert_of, t.amendment, t.skill, t.conversation::text, t.conversation_run::text, t.adopted_at,
	t.spec_ref, t.max_tries, t.tries_used, t.assigned_agent, t.budget_tokens`

func scanTicket(row pgx.Row) (*Ticket, error) {
	var t Ticket
	var key string
	err := row.Scan(&t.ID, &t.Project, &key, &t.Number, &t.Title, &t.Kind, &t.Part, &t.Source, &t.Why, &t.What, &t.Goal,
		&t.Approach, &t.Docs, &t.DocsReason, &t.Stage, &t.ClosedReason, &t.Skips, &t.SkipsReason, &t.Origin, &t.CreatedBy,
		&t.Seen, &t.Analysis, &t.Priority, &t.Position, &t.Hold, &t.NoticeUntil, &t.Branch,
		&t.FixRounds, &t.Waiting, &t.Stuck, &t.StageAt, &t.CreatedAt, &t.UpdatedAt, &t.Agents, &t.OperatorPriority, &t.CodeWait, &t.CodeSay, &t.Network,
		&t.Approaches, &t.ApproachAt, &t.RevertOf, &t.Amendment, &t.Skill, &t.Conversation, &t.ConversationRun, &t.AdoptedAt,
		&t.SpecRef, &t.MaxTries, &t.TriesUsed, &t.AssignedAgent, &t.BudgetTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no such ticket")
	}
	if err != nil {
		return nil, err
	}
	t.Key = key + "-" + strconv.Itoa(t.Number)
	return &t, nil
}

// ParseNumber reads a ticket's number as a request names it: 42, or the key with it, SF-42.
func ParseNumber(p *Project, ref string) (int, error) {
	s := ref
	if k, n, ok := strings.Cut(ref, "-"); ok {
		if k != p.Key {
			return 0, errf(NotFound, "%s is not a ticket of %s", ref, p.Name)
		}
		s = n
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errf(NotFound, "%q is not a ticket's number", ref)
	}
	return n, nil
}

func ticketByNumber(ctx context.Context, q db.Querier, project string, number int, lock bool) (*Ticket, error) {
	sql := `SELECT ` + ticketCols + ` FROM tickets t JOIN projects p ON p.id = t.project WHERE t.project = $1 AND t.number = $2`
	if lock {
		sql += ` FOR UPDATE OF t`
	}
	t, err := scanTicket(q.QueryRow(ctx, sql, project, number))
	if err != nil {
		return nil, err
	}
	return t, fillTicket(ctx, q, t)
}

func ticketByID(ctx context.Context, q db.Querier, id int64, lock bool) (*Ticket, error) {
	sql := `SELECT ` + ticketCols + ` FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = $1`
	if lock {
		sql += ` FOR UPDATE OF t`
	}
	t, err := scanTicket(q.QueryRow(ctx, sql, id))
	if err != nil {
		return nil, err
	}
	return t, fillTicket(ctx, q, t)
}

// fillTicket reads a ticket's checks and its links.
func fillTicket(ctx context.Context, q db.Querier, t *Ticket) error {
	rows, err := q.Query(ctx, `SELECT kind, spec, keeps FROM checks WHERE ticket = $1 ORDER BY position`, t.ID)
	if err != nil {
		return err
	}
	t.Checks, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (CheckSpec, error) {
		var c CheckSpec
		return c, r.Scan(&c.Kind, &c.Spec, &c.Keeps)
	})
	if err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT l.kind, p.key || '-' || o.number FROM ticket_links l
		JOIN tickets o ON o.id = l.to_ticket JOIN projects p ON p.id = o.project
		WHERE l.from_ticket = $1 ORDER BY l.kind, o.number`, t.ID)
	if err != nil {
		return err
	}
	t.Links, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Link, error) {
		var l Link
		return l, r.Scan(&l.Kind, &l.To)
	})
	if t.Checks == nil {
		t.Checks = []CheckSpec{}
	}
	if t.Links == nil {
		t.Links = []Link{}
	}
	if t.Docs == nil {
		t.Docs = []string{}
	}
	if t.Skips == nil {
		t.Skips = []string{}
	}
	return err
}

// Ticket returns one ticket of a project, by its number or its key.
func (b *Board) Ticket(ctx context.Context, p *Project, ref string) (*Ticket, error) {
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	return ticketByNumber(ctx, b.Pool, p.ID, n, false)
}

// Tickets returns every ticket of a project, in their order on the board.
func (b *Board) Tickets(ctx context.Context, p *Project) ([]*Ticket, error) {
	return tickets(ctx, b.Pool, p.ID, "")
}

func tickets(ctx context.Context, q db.Querier, project, stage string) ([]*Ticket, error) {
	sql := `SELECT ` + ticketCols + ` FROM tickets t JOIN projects p ON p.id = t.project WHERE t.project = $1`
	args := []any{project}
	if stage != "" {
		sql += ` AND t.stage = $2`
		args = append(args, stage)
	}
	rows, err := q.Query(ctx, sql+` ORDER BY t.priority, t.position, t.number`, args...)
	if err != nil {
		return nil, err
	}
	var out []*Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range out {
		if err := fillTicket(ctx, q, t); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// History returns the changes of a ticket, oldest first: what the app shows as its history (6.4).
func (b *Board) History(ctx context.Context, t *Ticket) ([]db.Change, error) {
	return db.History(ctx, b.Pool, "tickets", t.Key)
}

// inTx runs f in one transaction, which it commits when f returns nil.
func (b *Board) inTx(ctx context.Context, f func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, b.Pool, f)
}
