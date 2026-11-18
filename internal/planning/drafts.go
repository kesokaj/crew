// Package planning is the way from a plan to the project OK (design 7.2): plan drafts, which the
// operator develops with the planning agent before any project exists; the Crew MCP's tools of the
// planning agent and of the lead's breakdown (11.5); and the orchestrator's rows that start them
// (15.2): a draft with a message from the operator, a project with no breakdown, and a breakdown
// sent back with a comment.
package planning

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/db"
)

// Refusal is a request the operator made that is refused, with the reason for them.
type Refusal struct {
	NotFound bool
	Msg      string
}

func (e *Refusal) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Refusal{Msg: fmt.Sprintf(format, a...)} }

// Limits of what a draft takes.
const (
	maxMessage = 100_000
	maxTitle   = 200
	MaxPlan    = 1 << 20
	// MaxVersions is how many versions of its plan a draft keeps: rows the server may only add.
	MaxVersions = 100
)

// GitHubRepo is a repository on GitHub, owner/name, as a draft names one for the planning agent.
var GitHubRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// Drafts keeps the plan drafts.
type Drafts struct {
	Pool *pgxpool.Pool
	// Kick tells the orchestrator that something waits for it.
	Kick func()
}

// NewDraft is a draft as the operator starts it: its first message, and optionally a title and the
// GitHub repository the planning agent should read.
type NewDraft struct {
	Title   string `json:"title"`
	Repo    string `json:"repo"`
	Message string `json:"message"`
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func checkMessage(text string) error {
	if strings.TrimSpace(text) == "" {
		return refuse("message: say what you want built")
	}
	if len(text) > maxMessage {
		return refuse("message: at most %d characters", maxMessage)
	}
	return nil
}

// Create starts a draft with the operator's first message. The planning agent is queued for it.
func (d *Drafts) Create(ctx context.Context, n NewDraft) (string, error) {
	n.Title, n.Repo = strings.TrimSpace(n.Title), strings.TrimSpace(n.Repo)
	if err := checkMessage(n.Message); err != nil {
		return "", err
	}
	if len(n.Title) > maxTitle || strings.ContainsAny(n.Title, "\r\n") {
		return "", refuse("title: one line of at most %d characters", maxTitle)
	}
	if n.Repo != "" && !GitHubRepo.MatchString(n.Repo) {
		return "", refuse("repo: a repository on GitHub as owner/name")
	}
	id := newUUID()
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO plan_drafts (id, title, repo) VALUES ($1, $2, nullif($3, ''))`, id, n.Title, n.Repo); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO draft_messages (draft, text) VALUES ($1, $2)`, id, n.Message); err != nil {
			return err
		}
		return db.RecordChange(ctx, tx, "operator", "plan_drafts", id, nil, map[string]any{"title": n.Title, "repo": n.Repo})
	})
	if err != nil {
		return "", err
	}
	d.kick()
	return id, nil
}

func (d *Drafts) kick() {
	if d.Kick != nil {
		d.Kick()
	}
}

// Message adds the operator's next message to an open draft. It reaches the planning agent when its
// turn that runs has ended, or at once.
func (d *Drafts) Message(ctx context.Context, id, text string) error {
	return d.MessageAbout(ctx, id, Said{Text: text})
}

// Said is a message of the operator: its text, and for a request about a part of the plan, the part
// quoted and what is asked of it (7.2: Improve text and Review text on the plan of a draft).
type Said struct {
	Text  string `json:"text"`
	Quote string `json:"quote"`
	Ask   string `json:"ask"`
	// Rewrite is the prompt writer's rewrite the text was chosen from, if it was.
	Rewrite int64 `json:"-"`
}

// Asks of a part of the plan.
const (
	AskImprove = "improve"
	AskReview  = "review"
)

// MessageAbout adds a message, which may quote a part of the plan, to an open draft.
func (d *Drafts) MessageAbout(ctx context.Context, id string, m Said) error {
	if err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error { return d.AddMessage(ctx, tx, id, m) }); err != nil {
		return err
	}
	d.kick()
	return nil
}

// AddMessage adds a message in the transaction given; the caller kicks the planner after it commits.
func (d *Drafts) AddMessage(ctx context.Context, tx pgx.Tx, id string, m Said) error {
	if m.Quote == "" && m.Ask == "" {
		if err := checkMessage(m.Text); err != nil {
			return err
		}
	} else {
		if m.Ask != AskImprove && m.Ask != AskReview {
			return refuse("ask: improve or review")
		}
		if strings.TrimSpace(m.Quote) == "" || len(m.Quote) > maxMessage || len(m.Text) > maxMessage {
			return refuse("quote: the part of the plan, at most %d characters", maxMessage)
		}
	}
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM plan_drafts WHERE id::text = $1 FOR UPDATE`, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Refusal{NotFound: true, Msg: "no such draft"}
	}
	if err != nil {
		return err
	}
	if state != "open" {
		return refuse("the draft is %s: it takes no more messages", state)
	}
	var rewrite any
	if m.Rewrite != 0 {
		rewrite = m.Rewrite
	}
	_, err = tx.Exec(ctx, `INSERT INTO draft_messages (draft, text, quote, ask, rewrite) VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5)`,
		id, strings.TrimSpace(m.Text), m.Quote, m.Ask, rewrite)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE plan_drafts SET updated_at = now() WHERE id = $1`, id)
	}
	return err
}

// Kicked tells the planner that a message waits, after a transaction of the caller's added it.
func (d *Drafts) Kicked() { d.kick() }

// DropEmpty removes a draft that CreateEmpty just made and nothing used: no message, no run, and no
// rewrite. Its row in the history stays.
func (d *Drafts) DropEmpty(ctx context.Context, id string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM plan_drafts d WHERE d.id::text = $1 AND d.state = 'open'
		AND NOT EXISTS (SELECT 1 FROM draft_messages WHERE draft = d.id) AND NOT EXISTS (SELECT 1 FROM subtasks WHERE draft = d.id)
		AND NOT EXISTS (SELECT 1 FROM prompt_rewrites WHERE draft = d.id) AND NOT EXISTS (SELECT 1 FROM plan_draft_versions WHERE draft = d.id)`, id)
	return err
}

// CreateEmpty starts a draft with no message yet: the first message waits for the prompt writer's
// proposal and the operator's choice (7.2). The planning agent starts with that message.
func (d *Drafts) CreateEmpty(ctx context.Context, title, repo string) (string, error) {
	title, repo = strings.TrimSpace(title), strings.TrimSpace(repo)
	if len(title) > maxTitle || strings.ContainsAny(title, "\r\n") {
		return "", refuse("title: one line of at most %d characters", maxTitle)
	}
	if repo != "" && !GitHubRepo.MatchString(repo) {
		return "", refuse("repo: a repository on GitHub as owner/name")
	}
	id := newUUID()
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO plan_drafts (id, title, repo) VALUES ($1, $2, nullif($3, ''))`, id, title, repo); err != nil {
			return err
		}
		return db.RecordChange(ctx, tx, "operator", "plan_drafts", id, nil, map[string]any{"title": title, "repo": repo})
	})
	return id, err
}

// Draft is a draft as the operator reads it (the Plans screen, 20.1): its plan, its versions, the
// conversation with the planning agent with the model of each turn, and what waits.
type Draft struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Repo      *string   `json:"repo"`
	Project   *string   `json:"project"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Version is the newest version of the plan, and Plan its text; 0 and "" before the first.
	Version  int       `json:"version"`
	Plan     string    `json:"plan"`
	Versions []Version `json:"versions"`
	// Waiting are the operator's messages no run took yet, and Messages every one.
	Waiting  []Message `json:"waiting"`
	Messages []Message `json:"messages"`
	// FirstRewrite is the prompt writer's newest proposal for the first message, while the draft has
	// no message and the operator has not chosen (7.2).
	FirstRewrite *int64 `json:"firstRewrite"`
	Turns        []Turn `json:"turns"`
	// Agent is the planning agent's newest run, or nil before the first.
	Agent *Agent `json:"agent"`
}

// Version is one version of a draft's plan.
type Version struct {
	Number    int       `json:"number"`
	Bytes     int       `json:"bytes"`
	Run       *string   `json:"run"`
	CreatedAt time.Time `json:"createdAt"`
}

// Message is one message of the operator in a draft: what it says, the part of the plan it asks
// about, the rewrite it was chosen from, and whether a run took it.
type Message struct {
	ID      int64     `json:"id"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Quote   *string   `json:"quote,omitempty"`
	Ask     *string   `json:"ask,omitempty"`
	Rewrite *int64    `json:"rewrite,omitempty"`
	Taken   bool      `json:"taken"`
}

// Turn is one turn of the draft's conversation with the planning agent, with what ran it. The prompt
// writer's calls for the draft are runs of the draft too, and are not of this conversation (7.2).
type Turn struct {
	Number  int       `json:"number"`
	Speaker string    `json:"speaker"`
	Text    string    `json:"text"`
	Run     *string   `json:"run"`
	CLI     *string   `json:"cli"`
	Model   *string   `json:"model"`
	Effort  *string   `json:"effort"`
	At      time.Time `json:"at"`
	Rebuilt bool      `json:"rebuilt"`
}

// Agent is the planning agent's newest run.
type Agent struct {
	Run    string  `json:"run"`
	State  string  `json:"state"`
	CLI    string  `json:"cli"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	Reason *string `json:"stopReason"`
}

// Get reads one draft.
func (d *Drafts) Get(ctx context.Context, id string) (*Draft, error) {
	var x Draft
	err := d.Pool.QueryRow(ctx, `SELECT id::text, title, state, repo, project::text, created_at, updated_at
		FROM plan_drafts WHERE id::text = $1`, id).Scan(&x.ID, &x.Title, &x.State, &x.Repo, &x.Project, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &Refusal{NotFound: true, Msg: "no such draft"}
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT number, length(text), run::text, created_at, text FROM plan_draft_versions
		WHERE draft = $1 ORDER BY number`, x.ID)
	if err != nil {
		return nil, err
	}
	x.Versions = []Version{}
	for rows.Next() {
		var v Version
		var text string
		if err := rows.Scan(&v.Number, &v.Bytes, &v.Run, &v.CreatedAt, &text); err != nil {
			rows.Close()
			return nil, err
		}
		x.Versions = append(x.Versions, v)
		x.Version, x.Plan = v.Number, text
	}
	rows.Close()
	rows, err = d.Pool.Query(ctx, `SELECT id, text, at, quote, ask, rewrite, run IS NOT NULL FROM draft_messages WHERE draft = $1 ORDER BY id`, x.ID)
	if err != nil {
		return nil, err
	}
	if x.Messages, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		return m, r.Scan(&m.ID, &m.Text, &m.At, &m.Quote, &m.Ask, &m.Rewrite, &m.Taken)
	}); err != nil {
		return nil, err
	}
	x.Waiting = []Message{}
	for _, m := range x.Messages {
		if !m.Taken {
			x.Waiting = append(x.Waiting, m)
		}
	}
	if x.Messages == nil {
		x.Messages = []Message{}
	}
	if len(x.Messages) == 0 {
		var id int64
		if err := d.Pool.QueryRow(ctx, `SELECT id FROM prompt_rewrites WHERE draft = $1 AND kind = 'first' AND choice IS NULL
			ORDER BY id DESC LIMIT 1`, x.ID).Scan(&id); err == nil {
			x.FirstRewrite = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	rows, err = d.Pool.Query(ctx, `SELECT t.number, t.speaker, t.text, t.run::text, r.cli, r.model, r.effort, t.at, t.rebuilt
		FROM turns t JOIN conversations c ON c.id = t.conversation LEFT JOIN runs r ON r.id = t.run
		WHERE c.draft = $1 AND c.id IN (SELECT r.conversation FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE s.draft = $1 AND s.role = 'planner')
		ORDER BY c.created_at, t.number`, x.ID)
	if err != nil {
		return nil, err
	}
	if x.Turns, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Turn, error) {
		var t Turn
		return t, r.Scan(&t.Number, &t.Speaker, &t.Text, &t.Run, &t.CLI, &t.Model, &t.Effort, &t.At, &t.Rebuilt)
	}); err != nil {
		return nil, err
	}
	var a Agent
	err = d.Pool.QueryRow(ctx, `SELECT r.id::text, r.state, r.cli, r.model, r.effort, r.stop_reason FROM runs r
		JOIN subtasks s ON s.id = r.subtask WHERE s.draft = $1 AND s.role = 'planner' ORDER BY r.started_at DESC LIMIT 1`, x.ID).
		Scan(&a.Run, &a.State, &a.CLI, &a.Model, &a.Effort, &a.Reason)
	if err == nil {
		x.Agent = &a
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return &x, nil
}

// Summary is a draft in the list.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Project   *string   `json:"project"`
	Version   int       `json:"version"`
	Waiting   int       `json:"waiting"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// List lists the drafts, open ones first, newest first.
func (d *Drafts) List(ctx context.Context) ([]Summary, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id::text, title, state, project::text,
			coalesce((SELECT max(number) FROM plan_draft_versions v WHERE v.draft = d.id), 0),
			(SELECT count(*) FROM draft_messages m WHERE m.draft = d.id AND m.run IS NULL), updated_at
		FROM plan_drafts d ORDER BY state <> 'open', updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Summary, error) {
		var s Summary
		return s, r.Scan(&s.ID, &s.Title, &s.State, &s.Project, &s.Version, &s.Waiting, &s.UpdatedAt)
	})
}
