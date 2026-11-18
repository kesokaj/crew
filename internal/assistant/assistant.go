// Package assistant is Crew's side of a conversation with the operator and of a ticket's chat (design
// 11.8, 11.9, 12.10): it keeps the operator's messages, starts the assistant's turns in the lead queue
// when a slot, the room and Crew allow, makes room by removing the conversation that has been idle
// longest, and shows a conversation with what each turn did. A turn is a run of the role assistant,
// of the conversation's one subtask (internal/runs); nothing here names a CLI.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// Runs is the part of the run driver the service uses.
type Runs interface {
	StartRole(ctx context.Context, rr runs.RoleRun) (string, int64, error)
	Continue(ctx context.Context, subtask int64, req runs.ContinueRequest) (string, error)
	Stop(ctx context.Context, run string) error
	End(ctx context.Context, run, reason, detail string) error
	EndNow(ctx context.Context, run, reason, detail string) error
}

// Service keeps the conversations of the operator and the chats of the tickets.
type Service struct {
	Pool     *pgxpool.Pool
	Board    *board.Board
	Runs     Runs
	Drafts   *planning.Drafts
	Settings func(ctx context.Context) (settings.Doc, error)
	Version  string
	Log      *slog.Logger
	// Ready, when set, is closed once the server took over the runs of the server before it.
	Ready <-chan struct{}

	mu      sync.Mutex
	waiting map[string]string // why a conversation's message waits, by its id, from the last pass
	kick    chan struct{}
}

// The limits of what the operator writes.
const MaxText = 20000

// Parts a marked text may come from in the task view (11.9, D14).
var Parts = board.QuoteParts

// Errors the routes answer with.
var (
	ErrNotFound = errors.New("no such conversation")
	ErrInvalid  = errors.New("invalid")
	ErrRefused  = errors.New("refused")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}
func refused(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, a...)...)
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Source is where a marked text came from in the task view (11.9, D14): the board's, which a comment
// of the operator's with a quote keeps too.
type Source = board.QuoteSource

func checkSource(src *Source) error {
	if err := board.CheckQuoteSource(src); err != nil {
		return invalid("%s", err.Error())
	}
	return nil
}

// Message is what the operator sends.
type Message struct {
	Text   string  `json:"text"`
	Quote  string  `json:"quote,omitempty"`
	Source *Source `json:"source,omitempty"`
}

func (m *Message) check() error {
	m.Text = strings.TrimSpace(m.Text)
	if utf8.RuneCountInString(m.Text) > MaxText || utf8.RuneCountInString(m.Quote) > MaxText {
		return invalid("text and quote: at most %d characters each", MaxText)
	}
	if m.Text == "" && strings.TrimSpace(m.Quote) == "" {
		return invalid("text: a message")
	}
	if m.Quote == "" && m.Source != nil {
		return invalid("source: only with a quote")
	}
	return checkSource(m.Source)
}

// Start is a new conversation of the operator's.
type Start struct {
	// Project is the project's id or key, "" for none (a normal conversation outside a project).
	Project string `json:"project"`
	Text    string `json:"text"`
	// Model is the operator's pick as cli/model, and Effort its effort; "" is the assistant's list.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Network is the network mode of a normal conversation outside a project: "internet" or
	// "model_only"; "" uses the machine's network mode (11.8, 13.6).
	Network string `json:"network,omitempty"`
}

// Start makes a conversation about one project or none with its first message; the next pass starts
// its first turn (11.8).
func (s *Service) Start(ctx context.Context, st Start) (View, error) {
	m := Message{Text: st.Text}
	if err := m.check(); err != nil {
		return View{}, err
	}
	st.Network = strings.TrimSpace(st.Network)
	if st.Network != "" && st.Network != "internet" && st.Network != "model_only" {
		return View{}, invalid("network: internet or model_only")
	}
	var project string
	if st.Project != "" {
		if st.Network != "" {
			return View{}, invalid("network: only for a normal conversation outside a project")
		}
		p, err := s.Board.Project(ctx, st.Project)
		if err != nil {
			return View{}, err
		}
		if p.State == "archived" {
			return View{}, refused("%s is archived", p.Name)
		}
		project = p.ID
	}
	id := newUUID()
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO conversations (id, project, purpose, title, network_mode) VALUES ($1, nullif($2, '')::uuid, 'operator', $3, nullif($4, ''))`,
			id, project, title(m.Text), st.Network); err != nil {
			return err
		}
		if err := s.pick(ctx, tx, id, st.Model, st.Effort); err != nil {
			return err
		}
		return addMessage(ctx, tx, id, m)
	})
	if err != nil {
		return View{}, err
	}
	s.Kick()
	return s.Get(ctx, id)
}

// title is a conversation's title from its first message: its first line, shortened.
func title(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	if utf8.RuneCountInString(t) > 80 {
		t = string([]rune(t)[:79]) + "…"
	}
	return t
}

func addMessage(ctx context.Context, tx pgx.Tx, conv string, m Message) error {
	var quote *string
	if m.Quote != "" {
		quote = &m.Quote
	}
	_, err := tx.Exec(ctx, `INSERT INTO conversation_messages (conversation, text, quote, source) VALUES ($1, $2, $3, $4)`,
		conv, m.Text, quote, m.Source)
	return err
}

// Send keeps the operator's next message; the next pass gives it to the agent. A closed conversation
// is opened again by it (D25); one whose project is archived takes none.
func (s *Service) Send(ctx context.Context, id string, m Message) (int64, error) {
	if !isUUID(id) {
		return 0, ErrNotFound
	}
	if err := m.check(); err != nil {
		return 0, err
	}
	var msg int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var state, project, key, stage string
		var ticket *int64
		err := tx.QueryRow(ctx, `SELECT coalesce(p.state, ''), coalesce(p.id::text, ''), c.ticket, coalesce(p.key || '-' || t.number, ''), coalesce(t.stage, '')
			FROM conversations c LEFT JOIN projects p ON p.id = c.project LEFT JOIN tickets t ON t.id = c.ticket
			WHERE c.id = $1 AND c.purpose IN ('operator', 'chat') FOR UPDATE OF c`, id).Scan(&state, &project, &ticket, &key, &stage)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == "archived" {
			return refused("its project is archived")
		}
		if _, err := tx.Exec(ctx, `UPDATE conversations SET closed_at = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		if err := addMessage(ctx, tx, id, m); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT max(id) FROM conversation_messages WHERE conversation = $1`, id).Scan(&msg); err != nil {
			return err
		}
		if ticket == nil {
			return nil
		}
		// A question in a ticket's chat is a signal and a line of the ticket's history; what was asked
		// is in neither (11.9, D28).
		text := "You asked about " + key + " in its chat"
		if m.Quote != "" {
			text = "You commented on marked text of " + key + " in its chat"
		}
		if err := board.RecordSignal(ctx, tx, board.Signal{Project: project, Ticket: *ticket, Kind: "asked_about",
			Detail: map[string]any{"ticket": key, "stage": stage, "marked": m.Quote != "", "text": text}}); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, project, board.Operator, "tickets", key, nil, map[string]any{"chat": "asked", "message": msg, "marked": m.Quote != ""})
	})
	if err != nil {
		return 0, err
	}
	s.Kick()
	return msg, nil
}

// Ask is a question in a ticket's chat, which its first question makes (D30): a question that would
// be refused makes no chat.
func (s *Service) Ask(ctx context.Context, p *board.Project, ref string, m Message) (string, int64, error) {
	if err := m.check(); err != nil {
		return "", 0, err
	}
	if p.State == "archived" {
		return "", 0, refused("its project is archived")
	}
	v, err := s.ChatOf(ctx, p, ref, true)
	if err != nil {
		return "", 0, err
	}
	id, err := s.Send(ctx, v.ID, m)
	return v.ID, id, err
}

// ChatOf is a ticket's chat (11.9). make makes it when it has none, for its first message (D30);
// otherwise a ticket with no chat yet answers an empty View.
func (s *Service) ChatOf(ctx context.Context, p *board.Project, ref string, make bool) (View, error) {
	t, err := s.Board.Ticket(ctx, p, ref)
	if err != nil {
		return View{}, err
	}
	var id string
	err = s.Pool.QueryRow(ctx, `SELECT id FROM conversations WHERE ticket = $1 AND purpose = 'chat'`, t.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if !make {
			return View{Purpose: "chat", Project: p.ID, Ticket: t.Key, Entries: []Entry{}}, nil
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO conversations (id, project, ticket, purpose, title) VALUES ($1, $2, $3, 'chat', $4)
			ON CONFLICT (ticket) WHERE purpose = 'chat' DO NOTHING`, newUUID(), p.ID, t.ID, "The chat of "+t.Key); err != nil {
			return View{}, err
		}
		err = s.Pool.QueryRow(ctx, `SELECT id FROM conversations WHERE ticket = $1 AND purpose = 'chat'`, t.ID).Scan(&id)
	}
	if err != nil {
		return View{}, err
	}
	return s.Get(ctx, id)
}

// Kick asks for a pass soon.
func (s *Service) Kick() {
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	k := s.kick
	s.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// Run makes a pass every every and when kicked, until ctx ends: after Ready, once the server took
// over the runs of the server before it, as the planner does (21).
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if s.Ready != nil {
		select {
		case <-ctx.Done():
			return
		case <-s.Ready:
		}
	}
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	k := s.kick
	s.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k:
		}
	}
}

func (s *Service) setWaiting(id, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiting == nil {
		s.waiting = map[string]string{}
	}
	if why == "" {
		delete(s.waiting, id)
	} else {
		s.waiting[id] = why
	}
}

func (s *Service) waitingFor(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting[id]
}

// Reconcile starts the turns the operator's messages wait for: each conversation that is not closed
// with messages no run took, whose turn is not running, in the order of its oldest message. While
// Crew is held nothing starts (D29). A turn waits for a lead slot of its CLI, first in its class (7.9), and for
// room among conversations.max_sandboxes, made by removing the one idle longest (D12). The messages
// are marked taken in the transaction that records the run or the turn, so each is given once.
func (s *Service) Reconcile(ctx context.Context) {
	if s.Runs == nil {
		return
	}
	list, err := s.waitingConversations(ctx)
	if err != nil {
		s.log().Error("the assistant cannot read the conversations", "error", err)
		return
	}
	if len(list) == 0 {
		return
	}
	if why := hold.Held(ctx, s.Pool); why != "" {
		for _, c := range list {
			s.setWaiting(c.id, "Crew is held: "+why)
		}
		return
	}
	doc, err := s.Settings(ctx)
	if err != nil {
		s.log().Error("the assistant cannot read the settings", "error", err)
		return
	}
	defer planning.LockSlots(s.Pool)()
	pass := time.Now()
	defer func() {
		if err := planning.Sweep(context.WithoutCancel(ctx), s.Pool, "assistant", pass); err != nil {
			s.log().Error("the assistant cannot clear the lead queue", "error", err)
		}
	}()
	for _, c := range list {
		why, err := s.startTurn(ctx, doc, c)
		if err != nil {
			s.log().Warn("a conversation's turn did not start; it is tried again", "conversation", c.id, "error", err)
			why = "the turn did not start: " + err.Error()
		}
		s.setWaiting(c.id, why)
	}
}

type waitingConv struct {
	id, purpose, project, kind string
	subtask                    int64
	ticket                     int64
	pickModel, pickEffort      string
}

func (s *Service) waitingConversations(ctx context.Context) ([]waitingConv, error) {
	rows, err := s.Pool.Query(ctx, `SELECT c.id, c.purpose, coalesce(c.project::text, ''), coalesce(c.subtask, 0), coalesce(c.ticket, 0),
			coalesce(c.pick_model, ''), coalesce(c.pick_effort, '')
		FROM conversations c JOIN conversation_messages m ON m.conversation = c.id
		WHERE c.purpose IN ('operator', 'chat') AND m.run IS NULL AND c.closed_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.subtask = c.subtask AND r.state IN ('starting', 'working'))
		GROUP BY c.id ORDER BY min(m.id)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (waitingConv, error) {
		var c waitingConv
		err := r.Scan(&c.id, &c.purpose, &c.project, &c.subtask, &c.ticket, &c.pickModel, &c.pickEffort)
		c.kind = "talk"
		if c.purpose == "chat" {
			c.kind = "chat"
		}
		return c, err
	})
}

// startTurn starts one conversation's turn, or says why it waits.
func (s *Service) startTurn(ctx context.Context, doc settings.Doc, c waitingConv) (string, error) {
	var project settings.Doc
	if c.project != "" {
		var name, key string
		if err := s.Pool.QueryRow(ctx, `SELECT name, key FROM projects WHERE id = $1`, c.project).Scan(&name, &key); err != nil {
			return "", err
		}
		var err error
		if project, err = settings.EffectiveProject(ctx, s.Pool, c.project, name, key); err != nil {
			return "", err
		}
	}
	var pin settings.Doc
	if c.pickModel != "" || c.pickEffort != "" {
		pin = settings.Doc{"model": c.pickModel, "effort": c.pickEffort}
	}
	ch, err := models.Choose(ctx, s.Pool, models.Ask{Role: "assistant", Machine: doc, Project: project, Pin: pin})
	if err != nil {
		return "none of the assistant's models can run now: " + err.Error(), nil
	}
	if why, err := s.makeRoom(ctx, doc, c); why != "" || err != nil {
		return why, err
	}
	ok, err := planning.TakeSlot(ctx, s.Pool, doc, planning.LeadAsk{Key: "conv:" + c.id, Owner: "assistant", Class: planning.ClassOperatorWaiting,
		Keyboard: true, Priority: planning.NormalPriority, Own: planning.NormalPriority, Project: c.project, CLI: ch.CLI, What: "a turn of a conversation"})
	if err != nil {
		return "", err
	}
	if !ok {
		return "it waits for a lead slot of " + ch.CLI, nil
	}
	ids, text, err := s.waitingText(ctx, c.id)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	take := func(ctx context.Context, tx pgx.Tx, run string) error {
		tag, err := tx.Exec(ctx, `UPDATE conversation_messages SET run = $2 WHERE id = ANY($1) AND run IS NULL`, ids, run)
		if err == nil && tag.RowsAffected() != int64(len(ids)) {
			err = runs.ErrBusy // another pass took them meanwhile
		}
		return err
	}
	untake := func(ctx context.Context, tx pgx.Tx, run string) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_messages SET run = NULL WHERE id = ANY($1) AND run = $2`, ids, run)
		return err
	}
	var run string
	if c.subtask == 0 {
		parts, err := s.firstPrompt(ctx, c, text)
		if err != nil {
			return "", err
		}
		run, _, err = s.Runs.StartRole(ctx, runs.RoleRun{Role: "assistant", Kind: c.kind, Title: "A turn of a conversation", Conversation: c.id,
			ReadOnly: true, CLI: ch.CLI, Model: ch.Model, Name: ch.Name, Effort: ch.Effort, From: ch.ModelFrom, EffortFrom: ch.EffortFrom,
			Parts: parts, Record: take, Owner: "assistant"})
	} else {
		run, err = s.Runs.Continue(ctx, c.subtask, runs.ContinueRequest{Text: text, CLI: ch.CLI, Model: ch.Model, Name: ch.Name, Effort: ch.Effort,
			ModelFrom: ch.ModelFrom, EffortFrom: ch.EffortFrom, Record: take, Unrecord: untake})
	}
	if err := planning.Taken(ctx, s.Pool, "conv:"+c.id); err != nil {
		s.log().Error("the assistant cannot take a started turn out of the lead queue", "error", err)
	}
	switch {
	case errors.Is(err, runs.ErrNotSignedIn):
		return ch.CLI + " is not signed in: the message goes once it is signed in again", nil
	case errors.Is(err, runs.ErrNoRoom):
		return "it waits for room on the machine", nil
	case errors.Is(err, runs.ErrHeld):
		return "Crew is held", nil
	case errors.Is(err, runs.ErrBusy):
		return "", nil
	case err != nil:
		return "", err
	}
	s.log().Info("a conversation's turn started", "conversation", c.id, "run", run, "cli", ch.CLI, "model", ch.Model)
	return "", nil
}

// makeRoom keeps the open sandboxes of conversations and chats within conversations.max_sandboxes:
// one more ends the run that has been idle longest; when every one works, the turn waits (D12).
func (s *Service) makeRoom(ctx context.Context, doc settings.Doc, c waitingConv) (string, error) {
	max := 3
	if n, ok := dig(doc, "conversations", "max_sandboxes").(int); ok && n >= 1 {
		max = n
	}
	var own bool
	var live int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(r.subtask = $1), false) FROM runs r JOIN subtasks st ON st.id = r.subtask
		WHERE st.scope = 'conversation' AND r.state <> 'ended'`, c.subtask).Scan(&live, &own); err != nil {
		return "", err
	}
	// A turn in its own idle sandbox takes no more room.
	if own || live < max {
		return "", nil
	}
	var idle string
	err := s.Pool.QueryRow(ctx, `SELECT r.id FROM runs r JOIN subtasks st ON st.id = r.subtask
		WHERE st.scope = 'conversation' AND r.state = 'idle' ORDER BY greatest(r.last_event_at, r.turn_started_at) NULLS FIRST LIMIT 1`).Scan(&idle)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Sprintf("all %d sandboxes of conversations work: it waits for one to be free", max), nil
	}
	if err != nil {
		return "", err
	}
	if err := s.Runs.End(ctx, idle, "ok", "removed to make room for another conversation"); err != nil {
		return "", err
	}
	return "", nil
}

// waitingText is the operator's messages no run took yet, as one text for the agent: each message's
// own words are the operator's; a marked text is data in a fence with where it came from (11.9, 13.8,
// D14).
func (s *Service) waitingText(ctx context.Context, conv string) ([]int64, string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, text, coalesce(quote, ''), source FROM conversation_messages
		WHERE conversation = $1 AND run IS NULL ORDER BY id`, conv)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []int64
	var texts []string
	for rows.Next() {
		var id int64
		var text, quote string
		var src *Source
		if err := rows.Scan(&id, &text, &quote, &src); err != nil {
			return nil, "", err
		}
		ids, texts = append(ids, id), append(texts, Said(text, quote, src))
	}
	return ids, strings.Join(texts, "\n\n"), rows.Err()
}

// Said is one message as the agent reads it.
func Said(text, quote string, src *Source) string {
	if quote == "" {
		return "The operator says: " + strings.TrimSpace(text)
	}
	name := "Text the operator marked in the task view"
	if src != nil {
		name += ", under " + src.Part
		if src.Label != "" {
			name += ", " + src.Label
		}
	}
	f := fence.New(text, quote, name)
	out := f.Data(name, "an agent, a tool, or a page a run read, as the task view shows it", quote)
	if strings.TrimSpace(text) != "" {
		out += "\n\nThe operator asks: " + strings.TrimSpace(text)
	}
	return out
}

// firstPrompt is the first turn's prompt: the assistant's instructions, the stage of a conversation or
// a chat, and the messages.
func (s *Service) firstPrompt(ctx context.Context, c waitingConv, text string) ([]runs.PromptPart, error) {
	// A project's own overrides of the assistant's files are read as every agent's are (10.3).
	part := func(name, path string, body bool) runs.PromptPart {
		text, source := baseline.MustText(path), "baseline/"+path+"@"+s.Version
		if c.project != "" {
			var err error
			if text, source, err = board.BaselineText(ctx, s.Pool, c.project, path, s.Version); err != nil {
				s.log().Error("reading a project's override", "project", c.project, "path", path, "error", err)
				text, source = baseline.MustText(path), "baseline/"+path+"@"+s.Version+" (its override could not be read)"
			}
		}
		if body {
			text = baseline.Body(text)
		}
		return runs.PromptPart{Part: name, Source: source, Text: text}
	}
	about := "This is a normal conversation with the operator, outside any single project: you have the Crew MCP to read the live Crew map, every project, its tickets, runs, checks, and diffs, and you can clone any non-archived project's repository read-only into your work folder."
	if c.purpose == "chat" {
		var key string
		if err := s.Pool.QueryRow(ctx, `SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = $1`,
			c.ticket).Scan(&key); err != nil {
			return nil, err
		}
		about = "This is the chat of the ticket " + key + "."
	} else if c.project != "" {
		var name, key string
		if err := s.Pool.QueryRow(ctx, `SELECT name, key FROM projects WHERE id = $1`, c.project).Scan(&name, &key); err != nil {
			return nil, err
		}
		about = fmt.Sprintf("This conversation is about the project %s (%s).", name, key)
	}
	return []runs.PromptPart{
		part("agent", "agents/assistant.md", true),
		part("stage", "stages/"+c.kind+".md", false),
		{Part: "context", Source: "crew", Text: about},
		{Part: "operator", Source: "the operator's messages", Text: text},
	}, nil
}

func dig(d settings.Doc, path ...string) any {
	var cur any = d
	for _, k := range path {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}
