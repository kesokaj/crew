package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// View is a conversation as the screens show it (conv.png, chat.png; plan M20 D8): the operator's
// messages and the agent's turns, in order, with what each turn did and how it was continued.
type View struct {
	ID      string `json:"id,omitempty"`
	Purpose string `json:"purpose"`
	Project string `json:"project,omitempty"`
	// ProjectName is the project's name, which the screens show and link by; Project is its key.
	ProjectName string     `json:"projectName,omitempty"`
	Ticket      string     `json:"ticket,omitempty"`
	Title       string     `json:"title"`
	SavedAt     *time.Time `json:"savedAt,omitempty"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	Network     string     `json:"network,omitempty"`
	Agent       Agent      `json:"agent"`
	CLIID       string     `json:"cliId,omitempty"`
	Turns       int        `json:"turns"`
	Tokens      int64      `json:"tokens"`
	Sandbox     Sandbox    `json:"sandbox"`
	// Waiting says why the operator's last message waits, from the last pass; "" when none waits or
	// a turn works.
	Waiting    string      `json:"waiting,omitempty"`
	Entries    []Entry     `json:"entries"`
	CameOf     CameOf      `json:"cameOf"`
	WorkFolder *WorkFolder `json:"workFolder,omitempty"`
}

// Agent is who answers: the CLI, the model and the effort of the last turn, or what the operator
// picked for the next.
type Agent struct {
	CLI         string `json:"cli,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	PickedByYou bool   `json:"pickedByYou"`
}

// Sandbox is the conversation's sandbox: running while a turn works, idle while it waits for the next
// message, saved when it is gone and the conversation is kept, closed when the operator closed it.
type Sandbox struct {
	State   string `json:"state"`
	ID      string `json:"id,omitempty"`
	Size    string `json:"size,omitempty"`
	Network string `json:"network,omitempty"`
}

// Entry is one message of the operator's or one turn of the agent's.
type Entry struct {
	Kind   string   `json:"kind"` // you, agent
	ID     int64    `json:"id,omitempty"`
	Text   string   `json:"text"`
	Quote  string   `json:"quote,omitempty"`
	Source *Source  `json:"source,omitempty"`
	Run    string   `json:"run,omitempty"`
	Turn   int      `json:"turn,omitempty"`
	Tools  []string `json:"tools,omitempty"`
	Way    string   `json:"way,omitempty"`
	// CLI, Model and Effort are what the turn's run ran on.
	CLI    string    `json:"cli,omitempty"`
	Model  string    `json:"model,omitempty"`
	Effort string    `json:"effort,omitempty"`
	Ended  *Ended    `json:"ended,omitempty"`
	At     time.Time `json:"at"`
	// Tickets are the tickets the turn's run created, by key.
	Tickets []string `json:"tickets,omitempty"`
}

// Ended is how a turn's run ended when it did not end well.
type Ended struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// CameOf is what the conversation led to: the tickets made from it, adopted or not, and its draft.
type CameOf struct {
	Tickets []TicketOf `json:"tickets"`
	Draft   string     `json:"draft,omitempty"`
}

// TicketOf is a ticket made from a conversation.
type TicketOf struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Project     string `json:"project,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Adopted     bool   `json:"adopted"`
}

// WorkFolder is what of the work folder is saved (D17).
type WorkFolder struct {
	Bytes   int64     `json:"bytes"`
	Files   int       `json:"files"`
	SavedAt time.Time `json:"savedAt"`
	Note    string    `json:"note,omitempty"`
}

// Get is one conversation of the operator's, or a ticket's chat.
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	if !isUUID(id) {
		return View{}, ErrNotFound
	}
	// One snapshot: a turn's answer and its run's idle state are written in one transaction, and a
	// view read across it must show both or neither (BUG-007).
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return View{}, err
	}
	defer tx.Rollback(ctx)
	v := View{Entries: []Entry{}, CameOf: CameOf{Tickets: []TicketOf{}}}
	var subtask *int64
	var pickModel, pickEffort, cliID *string
	err = tx.QueryRow(ctx, `SELECT c.id, c.purpose, coalesce(p.key, ''), coalesce(p.name, ''), coalesce(p.key || '-' || t.number, ''), coalesce(c.title, ''),
			c.saved_at, c.closed_at, coalesce(c.network_mode, ''), c.subtask, c.pick_model, c.pick_effort, c.cli_id
		FROM conversations c LEFT JOIN projects p ON p.id = c.project LEFT JOIN tickets t ON t.id = c.ticket
		WHERE c.id = $1 AND c.purpose IN ('operator', 'chat')`, id).Scan(&v.ID, &v.Purpose, &v.Project, &v.ProjectName, &v.Ticket, &v.Title,
		&v.SavedAt, &v.ClosedAt, &v.Network, &subtask, &pickModel, &pickEffort, &cliID)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, err
	}
	if cliID != nil {
		v.CLIID = *cliID
	}
	if pickModel != nil {
		cli, model, _ := strings.Cut(*pickModel, "/")
		v.Agent = Agent{CLI: cli, Model: model, PickedByYou: true}
	}
	if pickEffort != nil {
		v.Agent.Effort, v.Agent.PickedByYou = *pickEffort, true
	}
	if err := s.entries(ctx, tx, &v); err != nil {
		return View{}, err
	}
	if err := s.sandbox(ctx, tx, &v, subtask); err != nil {
		return View{}, err
	}
	if err := s.cameOf(ctx, tx, &v); err != nil {
		return View{}, err
	}
	var wf WorkFolder
	var note *string
	err = tx.QueryRow(ctx, `SELECT bytes, files, saved_at, note FROM work_folders WHERE conversation = $1`, id).Scan(&wf.Bytes, &wf.Files, &wf.SavedAt, &note)
	if err == nil {
		if note != nil {
			wf.Note = *note
		}
		v.WorkFolder = &wf
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return View{}, err
	}
	if v.Sandbox.State != "running" {
		var waits int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM conversation_messages WHERE conversation = $1 AND run IS NULL`, id).Scan(&waits); err != nil {
			return View{}, err
		}
		if waits > 0 {
			v.Waiting = s.waitingFor(id)
			if v.Waiting == "" {
				v.Waiting = "it starts at the next pass"
			}
		}
	}
	return v, nil
}

// entries are the operator's messages and the agent's turns, in the order they were written. The
// first turn a run writes, its prompt, and the operator's turns the runs write are the messages'
// copies and not shown (D8).
func (s *Service) entries(ctx context.Context, q db.Querier, v *View) error {
	rows, err := q.Query(ctx, `SELECT 'you', m.id, m.text, coalesce(m.quote, ''), m.source, '', 0, '[]'::jsonb, '', '', '', m.created_at, 0, '', '', ''
			FROM conversation_messages m WHERE m.conversation = $1
		UNION ALL
		SELECT 'agent', 0, t.text, '', NULL, r.id::text, t.number, t.tool_calls, coalesce(r.way, ''),
			CASE WHEN r.state = 'ended' AND r.stop_reason <> 'ok' AND t.number = (SELECT max(t2.number) FROM turns t2 WHERE t2.run = r.id AND t2.speaker = 'agent')
				THEN r.stop_reason ELSE '' END,
			coalesce(r.stop_detail, ''), t.at, t.tokens, r.cli, coalesce(r.model, ''), coalesce(r.effort, '')
			FROM turns t JOIN runs r ON r.id = t.run WHERE t.conversation = $1 AND t.speaker = 'agent'
		ORDER BY 12, 1 DESC`, v.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		var reason, detail string
		var tokens int64
		if err := rows.Scan(&e.Kind, &e.ID, &e.Text, &e.Quote, &e.Source, &e.Run, &e.Turn, &e.Tools, &e.Way, &reason, &detail, &e.At, &tokens,
			&e.CLI, &e.Model, &e.Effort); err != nil {
			return err
		}
		if reason != "" {
			e.Ended = &Ended{Reason: reason, Detail: detail}
		}
		if e.Kind == "agent" {
			v.Turns++
			v.Tokens += tokens
		}
		v.Entries = append(v.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// A run that ended before it said anything shows its end under the message it took.
	ended, err := q.Query(ctx, `SELECT m.id, r.id::text, r.stop_reason, coalesce(r.stop_detail, '') FROM conversation_messages m JOIN runs r ON r.id = m.run
		WHERE m.conversation = $1 AND r.state = 'ended' AND r.stop_reason <> 'ok'
			AND NOT EXISTS (SELECT 1 FROM turns t WHERE t.run = r.id AND t.speaker = 'agent')`, v.ID)
	if err != nil {
		return err
	}
	defer ended.Close()
	for ended.Next() {
		var msg int64
		var run, reason, detail string
		if err := ended.Scan(&msg, &run, &reason, &detail); err != nil {
			return err
		}
		for i := range v.Entries {
			if v.Entries[i].Kind == "you" && v.Entries[i].ID == msg {
				v.Entries[i].Run, v.Entries[i].Ended = run, &Ended{Reason: reason, Detail: detail}
			}
		}
	}
	if err := ended.Err(); err != nil {
		return err
	}
	// The tickets each turn's run created.
	made, err := q.Query(ctx, `SELECT t.conversation_run::text, p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project
		WHERE t.conversation = $1 AND t.conversation_run IS NOT NULL ORDER BY t.number`, v.ID)
	if err != nil {
		return err
	}
	defer made.Close()
	for made.Next() {
		var run, key string
		if err := made.Scan(&run, &key); err != nil {
			return err
		}
		// The tickets go under the run's last turn.
		for i := len(v.Entries) - 1; i >= 0; i-- {
			if v.Entries[i].Kind == "agent" && v.Entries[i].Run == run {
				v.Entries[i].Tickets = append(v.Entries[i].Tickets, key)
				break
			}
		}
	}
	return made.Err()
}

// sandbox reads the state of the conversation's newest run and its sandbox, and the agent of its last
// turn when the operator picked none.
func (s *Service) sandbox(ctx context.Context, q db.Querier, v *View, subtask *int64) error {
	v.Sandbox.State = "saved"
	if v.ClosedAt != nil {
		v.Sandbox.State = "closed"
	}
	if subtask == nil {
		if v.Turns == 0 && v.ClosedAt == nil {
			v.Sandbox.State = "none"
		}
		return nil
	}
	var state, cli, model, effort, sandbox, size, network string
	err := q.QueryRow(ctx, `SELECT r.state, r.cli, coalesce(r.model, ''), coalesce(r.effort, ''), coalesce(r.sandbox, ''), coalesce(r.size, ''),
			coalesce(b.network, '')
		FROM runs r LEFT JOIN sandboxes b ON b.id = r.sandbox WHERE r.subtask = $1 ORDER BY r.number DESC LIMIT 1`, *subtask).Scan(
		&state, &cli, &model, &effort, &sandbox, &size, &network)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !v.Agent.PickedByYou {
		v.Agent = Agent{CLI: cli, Model: model, Effort: effort}
	}
	switch state {
	case "starting", "working":
		v.Sandbox = Sandbox{State: "running", ID: sandbox, Size: size, Network: network}
	case "idle":
		v.Sandbox = Sandbox{State: "idle", ID: sandbox, Size: size, Network: network}
	}
	return nil
}

// cameOf reads the tickets made from the conversation and the draft started from it.
func (s *Service) cameOf(ctx context.Context, q db.Querier, v *View) error {
	rows, err := q.Query(ctx, `SELECT p.key || '-' || t.number, t.title, p.key, p.name, t.adopted_at IS NOT NULL OR t.conversation_run IS NULL
		FROM tickets t JOIN projects p ON p.id = t.project WHERE t.conversation = $1 ORDER BY t.number`, v.ID)
	if err != nil {
		return err
	}
	tickets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TicketOf, error) {
		var t TicketOf
		err := r.Scan(&t.Key, &t.Title, &t.Project, &t.ProjectName, &t.Adopted)
		return t, err
	})
	if err != nil {
		return err
	}
	v.CameOf.Tickets = append(v.CameOf.Tickets, tickets...)
	err = q.QueryRow(ctx, `SELECT id::text FROM plan_drafts WHERE source_conversation = $1 ORDER BY created_at DESC LIMIT 1`, v.ID).Scan(&v.CameOf.Draft)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Summary is a conversation in the list (convs.png).
type Summary struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Last    string     `json:"last"`              // the start of the agent's last answer
	About   string     `json:"about,omitempty"`   // the project's key
	Project string     `json:"project,omitempty"` // its name
	CLI     string     `json:"cli,omitempty"`
	Model   string     `json:"model,omitempty"`
	Turns   int        `json:"turns"`
	LastAt  time.Time  `json:"lastAt"`
	Sandbox string     `json:"sandbox"`
	SavedAt *time.Time `json:"savedAt,omitempty"`
	Network string     `json:"network,omitempty"`
	Tickets int        `json:"tickets,omitempty"`
	Draft   string     `json:"draft,omitempty"`
}

// List is the operator's conversations, the newest first; q, when given, matches the title and the
// text of the messages and turns.
func (s *Service) List(ctx context.Context, q string) ([]Summary, error) {
	q = strings.TrimSpace(q)
	if len(q) > 200 {
		return nil, invalid("q: at most 200 characters")
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.Pool.Query(ctx, `SELECT c.id, coalesce(c.title, ''), coalesce(p.key, ''), coalesce(p.name, ''),
			coalesce((SELECT left(t.text, 160) FROM turns t WHERE t.conversation = c.id AND t.speaker = 'agent' ORDER BY t.number DESC LIMIT 1), ''),
			(SELECT count(*) FROM turns t WHERE t.conversation = c.id AND t.speaker = 'agent'),
			greatest(c.created_at, coalesce(c.last_turn_at, c.created_at), coalesce((SELECT max(m.created_at) FROM conversation_messages m WHERE m.conversation = c.id), c.created_at)),
			coalesce(lr.cli, ''), coalesce(lr.model, ''), coalesce(lr.state, ''), c.closed_at IS NOT NULL,
			c.saved_at, coalesce(c.network_mode, ''),
			(SELECT count(*) FROM tickets tk WHERE tk.conversation = c.id),
			coalesce((SELECT pd.id::text FROM plan_drafts pd WHERE pd.source_conversation = c.id ORDER BY pd.created_at DESC LIMIT 1), '')
		FROM conversations c LEFT JOIN projects p ON p.id = c.project
		LEFT JOIN LATERAL (SELECT r.cli, r.model, r.state FROM runs r WHERE r.subtask = c.subtask ORDER BY r.number DESC LIMIT 1) lr ON true
		WHERE c.purpose = 'operator' AND ($1 = '' OR c.title ILIKE $2
			OR EXISTS (SELECT 1 FROM conversation_messages m WHERE m.conversation = c.id AND m.text ILIKE $2)
			OR EXISTS (SELECT 1 FROM turns t WHERE t.conversation = c.id AND t.speaker = 'agent' AND t.text ILIKE $2))
		ORDER BY 7 DESC LIMIT 500`, q, like)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Summary, error) {
		var x Summary
		var state string
		var closed bool
		err := r.Scan(&x.ID, &x.Title, &x.About, &x.Project, &x.Last, &x.Turns, &x.LastAt, &x.CLI, &x.Model, &state, &closed,
			&x.SavedAt, &x.Network, &x.Tickets, &x.Draft)
		switch {
		case state == "starting" || state == "working":
			x.Sandbox = "running"
		case state == "idle":
			x.Sandbox = "idle"
		case closed:
			x.Sandbox = "closed"
		default:
			x.Sandbox = "saved"
		}
		return x, err
	})
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
