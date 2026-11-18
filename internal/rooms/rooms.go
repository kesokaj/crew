package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/fence"
	"github.com/kesokaj/crew/internal/runs"
)

var (
	// ErrInvalid indicates malformed room, thread, or message arguments.
	ErrInvalid = errors.New("invalid chat room input")
	// ErrMaxAgentHops indicates consecutive agent-to-agent messages reached max_agent_hops.
	ErrMaxAgentHops = errors.New("max_agent_hops reached in this channel or feature thread; waiting for @user")
	// ErrCrossProjectBoundary refuses cross-factory room, thread, or message references fail-closed.
	ErrCrossProjectBoundary = errors.New("cross-project room, thread, or message reference refused")
)

// PeerDispatcher spawns a tracked ACP peer session when an actionable @peer mention is posted.
type PeerDispatcher interface {
	SpawnPeerSession(ctx context.Context, req runs.SpawnPeerRequest) (*runs.PeerSession, error)
}

// ActivePeerRouter optionally routes messages to an already-active peer ACP session via queued turn
// by default, or live mid-turn steer only when explicitly interrupted by @user.
type ActivePeerRouter interface {
	FindActivePeerSession(ctx context.Context, projectID, role string, ticketID int64) (*runs.PeerSession, error)
	TurnPeerSession(ctx context.Context, sessionID, text string) (*runs.PeerSession, error)
	SteerPeerSession(ctx context.Context, sessionID, text string) error
}

// Service manages each project's single canonical #<project> channel and per-feature threads (R214).
type Service struct {
	Pool         *pgxpool.Pool
	Board        *board.Board
	Dispatcher   PeerDispatcher
	KickPlanner  func()
	KickPipeline func()
	Log          *slog.Logger
}

// Room represents the canonical #<project> channel for a factory project.
type Room struct {
	ID        int64     `json:"id"`
	ProjectID string    `json:"projectId"`
	Project   string    `json:"project"`
	Channel   string    `json:"channel"`
	Topic     string    `json:"topic,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Thread represents a per-feature thread (feat/<NNN>-<slug>) rooted in #<project>.
type Thread struct {
	ID            int64     `json:"id"`
	RoomID        int64     `json:"roomId"`
	ProjectID     string    `json:"projectId"`
	ThreadKey     string    `json:"threadKey"`
	Title         string    `json:"title"`
	SpecID        *int64    `json:"specId,omitempty"`
	SpecKey       string    `json:"specKey,omitempty"`
	RootMessageID *int64    `json:"rootMessageId,omitempty"`
	TicketKeys    []string  `json:"ticketKeys"`
	State         string    `json:"state"`
	MessageCount  int       `json:"messageCount"`
	LastHopCount  int       `json:"lastHopCount"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Addressee records per-recipient routing and delivery state for one chat message.
type Addressee struct {
	ID              int64     `json:"id"`
	MessageID       int64     `json:"messageId"`
	ProjectID       string    `json:"projectId"`
	RecipientRole   string    `json:"recipientRole"`
	RecipientHandle string    `json:"recipientHandle"`
	Via             string    `json:"via"`
	DeliveryMode    string    `json:"deliveryMode"`
	DeliveryState   string    `json:"deliveryState"`
	TaskID          *string   `json:"taskId,omitempty"`
	SessionID       *string   `json:"sessionId,omitempty"`
	FailureReason   string    `json:"failureReason,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Message is one post in #<project> or inside a feat/<NNN>-<slug> thread.
type Message struct {
	ID             int64               `json:"id"`
	RoomID         int64               `json:"roomId"`
	ThreadID       *int64              `json:"threadId,omitempty"`
	ThreadKey      string              `json:"threadKey,omitempty"`
	ProjectID      string              `json:"projectId"`
	TaskID         *string             `json:"taskId,omitempty"`
	TaskKey        string              `json:"taskKey,omitempty"`
	TicketID       *int64              `json:"ticketId,omitempty"`
	TicketKey      string              `json:"ticketKey,omitempty"`
	RunID          *string             `json:"runId,omitempty"`
	SenderRole     string              `json:"senderRole"`
	SenderHandle   string              `json:"senderHandle"`
	Kind           string              `json:"kind"`
	Intent         string              `json:"intent"`
	EventType      string              `json:"eventType,omitempty"`
	ReplyToID      *int64              `json:"replyToId,omitempty"`
	Interrupt      bool                `json:"interrupt,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	ChoicePrompt   *ChoicePromptSpec   `json:"choicePrompt,omitempty"`
	ChoiceResponse *ChoiceResponseSpec `json:"choiceResponse,omitempty"`
	BodySHA256     string              `json:"bodySha256,omitempty"`
	BodyRunes      int                 `json:"bodyRunes,omitempty"`
	Offloaded      bool                `json:"offloaded,omitempty"`
	Mentions       []string            `json:"mentions"`
	Addressees     []Addressee         `json:"addressees,omitempty"`
	HopCount       int                 `json:"hopCount"`
	Fenced         bool                `json:"fenced"`
	Body           string              `json:"body"`
	FencedBody     string              `json:"fencedBody,omitempty"`
	Content        string              `json:"content"`
	Actionable     bool                `json:"actionable"`
	SpawnedSession *string             `json:"spawnedSession,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
}

// RoomView returns the canonical #<project> channel, its feature threads, and top-level messages.
type RoomView struct {
	Room
	MaxAgentHops int       `json:"maxAgentHops"`
	Threads      []Thread  `json:"threads"`
	Messages     []Message `json:"messages"`
}

// ThreadView returns one feat/<NNN>-<slug> thread and its messages.
type ThreadView struct {
	Room         Room      `json:"room"`
	Thread       Thread    `json:"thread"`
	MaxAgentHops int       `json:"maxAgentHops"`
	CurrentHops  int       `json:"currentHops"`
	Messages     []Message `json:"messages"`
}

// FencePeerMessage wraps peer or system text with an explicit <crew_data_fence> header and
// cryptographic fence.New mark so reading agents treat peer chatter strictly as data (Rule 14).
func FencePeerMessage(senderHandle, runID string, hop int, cleanBody string) string {
	return FencePeerMessageWithEnvelope(senderHandle, runID, hop, nil, cleanBody)
}

// FencePeerMessageWithEnvelope wraps peer or system text with <crew_data_fence> and co-addressee
// metadata (to="...") so receiving agents know which co-recipients already received the message.
func FencePeerMessageWithEnvelope(senderHandle, runID string, hop int, toHandles []string, cleanBody string) string {
	handle := strings.TrimSpace(senderHandle)
	if handle == "" {
		handle = "@agent"
	}
	f := fence.New(cleanBody)
	inner := f.Data("Peer chat message", handle, cleanBody)
	if len(toHandles) > 0 {
		return fmt.Sprintf("<crew_data_fence sender=%q run=%q hop=\"%d\" to=%q>\n%s\n</crew_data_fence>",
			handle, strings.TrimSpace(runID), hop, strings.Join(toHandles, ","), inner)
	}
	return fmt.Sprintf("<crew_data_fence sender=%q run=%q hop=\"%d\">\n%s\n</crew_data_fence>",
		handle, strings.TrimSpace(runID), hop, inner)
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) maxHops(ctx context.Context, p *board.Project) int {
	if s.Board != nil {
		if wf, err := s.Board.GetWorkflow(ctx, s.Pool, p); err == nil && wf != nil && wf.MaxAgentHops > 0 {
			return wf.MaxAgentHops
		}
	}
	var hops int
	if err := s.Pool.QueryRow(ctx, `SELECT max_agent_hops FROM workflow_defs WHERE project_id = $1`, p.ID).Scan(&hops); err == nil && hops > 0 {
		return hops
	}
	return 3
}

// EnsureRoom guarantees the single canonical #<project> room exists and syncs feature threads from specs.
func (s *Service) EnsureRoom(ctx context.Context, p *board.Project) (*Room, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	var room Room
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		room, err = ensureRoomTx(ctx, tx, p)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, thread_ref, title, spec_key, emitted_keys
			FROM specs WHERE project_id = $1 AND tier = 'feature' AND thread_ref <> ''
			ORDER BY number ASC`, p.ID)
		if err != nil {
			return err
		}
		type specRow struct {
			id      int64
			thread  string
			title   string
			specKey string
			emitted []string
		}
		var list []specRow
		for rows.Next() {
			var r specRow
			if err := rows.Scan(&r.id, &r.thread, &r.title, &r.specKey, &r.emitted); err != nil {
				rows.Close()
				return err
			}
			list = append(list, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range list {
			sid := r.id
			if _, err := ensureThreadTx(ctx, tx, p, &room, r.thread, r.title, &sid, r.specKey, r.emitted); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// EnsureThread guarantees a feat/<NNN>-<slug> thread rooted in #<project>.
func (s *Service) EnsureThread(ctx context.Context, p *board.Project, threadKey, title, specRef string) (*Room, *Thread, error) {
	if p == nil {
		return nil, nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	key := strings.TrimSpace(threadKey)
	var specID *int64
	var specKey string
	var emitted []string
	if strings.TrimSpace(specRef) != "" && s.Board != nil {
		if sp, err := s.Board.GetSpec(ctx, p, specRef); err == nil {
			id := sp.ID
			specID = &id
			specKey = sp.SpecKey
			emitted = sp.EmittedKeys
			if key == "" {
				key = sp.ThreadRef
			}
			if strings.TrimSpace(title) == "" {
				title = sp.Title
			}
		}
	}
	if key == "" {
		return nil, nil, fmt.Errorf("%w: threadKey is required", ErrInvalid)
	}
	if strings.TrimSpace(title) == "" {
		title = key
	}
	var room Room
	var th Thread
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		room, err = ensureRoomTx(ctx, tx, p)
		if err != nil {
			return err
		}
		th, err = ensureThreadTx(ctx, tx, p, &room, key, title, specID, specKey, emitted)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return &room, &th, nil
}

func ensureRoomTx(ctx context.Context, tx pgx.Tx, p *board.Project) (Room, error) {
	var r Room
	channel := "#" + p.Name
	err := tx.QueryRow(ctx, `INSERT INTO chat_rooms (project_id, channel, topic)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE SET
			channel = EXCLUDED.channel,
			topic = CASE WHEN chat_rooms.topic = '' THEN EXCLUDED.topic ELSE chat_rooms.topic END
		RETURNING id, project_id::text, $4::text, channel, topic, created_at, updated_at`,
		p.ID, channel, p.Goal, p.Name).Scan(
		&r.ID, &r.ProjectID, &r.Project, &r.Channel, &r.Topic, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func ensureThreadTx(ctx context.Context, tx pgx.Tx, p *board.Project, room *Room, threadKey, title string, specID *int64, specKey string, ticketKeys []string) (Thread, error) {
	var t Thread
	if ticketKeys == nil {
		ticketKeys = []string{}
	}
	err := tx.QueryRow(ctx, `INSERT INTO chat_threads (room_id, project_id, thread_key, title, spec_id, ticket_keys)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project_id, thread_key) DO UPDATE SET
			title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE chat_threads.title END,
			spec_id = coalesce(chat_threads.spec_id, EXCLUDED.spec_id),
			ticket_keys = CASE WHEN cardinality(EXCLUDED.ticket_keys) > 0 THEN EXCLUDED.ticket_keys ELSE chat_threads.ticket_keys END,
			updated_at = now()
		RETURNING id, room_id, project_id::text, thread_key, title, spec_id, root_message_id, ticket_keys, state, created_at, updated_at`,
		room.ID, p.ID, threadKey, title, specID, ticketKeys).Scan(
		&t.ID, &t.RoomID, &t.ProjectID, &t.ThreadKey, &t.Title, &t.SpecID, &t.RootMessageID, &t.TicketKeys, &t.State, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Thread{}, err
	}
	if t.TicketKeys == nil {
		t.TicketKeys = []string{}
	}
	t.SpecKey = specKey
	if t.RootMessageID == nil {
		rootText := fmt.Sprintf("Opened feature thread `%s` — %s", t.ThreadKey, t.Title)
		if specKey != "" {
			rootText = fmt.Sprintf("Opened feature thread `%s` for %s — %s", t.ThreadKey, specKey, t.Title)
		}
		fenced := FencePeerMessage("@crew", "", 0, rootText)
		var rootID int64
		if err := tx.QueryRow(ctx, `INSERT INTO chat_messages (
				room_id, thread_id, project_id, sender_role, sender_handle, mentions, hop_count, fenced, body, fenced_body, actionable)
			VALUES ($1, NULL, $2, 'system', '@crew', '{}', 0, true, $3, $4, false)
			RETURNING id`,
			room.ID, p.ID, rootText, fenced).Scan(&rootID); err != nil {
			return Thread{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE chat_threads SET root_message_id = $2 WHERE id = $1`, t.ID, rootID); err != nil {
			return Thread{}, err
		}
		t.RootMessageID = &rootID
	}
	return t, nil
}

// ListRooms lists canonical #<project> channels for all non-archived, non-hidden projects.
func (s *Service) ListRooms(ctx context.Context) ([]Room, error) {
	if _, err := s.Pool.Exec(ctx, `INSERT INTO chat_rooms (project_id, channel, topic)
		SELECT id, '#' || name, goal FROM projects WHERE state <> 'archived' AND NOT hidden
		ON CONFLICT (project_id) DO NOTHING`); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.id, r.project_id::text, p.name, r.channel, r.topic, r.created_at, r.updated_at
		FROM chat_rooms r JOIN projects p ON p.id = r.project_id
		WHERE p.state <> 'archived' AND NOT p.hidden
		ORDER BY p.name ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Room, error) {
		var r Room
		return r, row.Scan(&r.ID, &r.ProjectID, &r.Project, &r.Channel, &r.Topic, &r.CreatedAt, &r.UpdatedAt)
	})
}

// ListThreads lists feature threads for one project.
func (s *Service) ListThreads(ctx context.Context, p *board.Project) ([]Thread, error) {
	if _, err := s.EnsureRoom(ctx, p); err != nil {
		return nil, err
	}
	return s.listThreadsRaw(ctx, p.ID)
}

func (s *Service) listThreadsRaw(ctx context.Context, projectID string) ([]Thread, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.id, t.room_id, t.project_id::text, t.thread_key, t.title, t.spec_id,
			coalesce(sp.spec_key, ''), t.root_message_id, t.ticket_keys, t.state,
			(SELECT count(*) FROM chat_messages m WHERE m.thread_id = t.id),
			coalesce((SELECT m.hop_count FROM chat_messages m WHERE m.thread_id = t.id AND m.sender_role <> 'system' ORDER BY m.id DESC LIMIT 1), 0),
			t.created_at, t.updated_at
		FROM chat_threads t
		LEFT JOIN specs sp ON sp.id = t.spec_id
		WHERE t.project_id = $1
		ORDER BY t.updated_at DESC, t.id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Thread, error) {
		var th Thread
		err := row.Scan(&th.ID, &th.RoomID, &th.ProjectID, &th.ThreadKey, &th.Title, &th.SpecID,
			&th.SpecKey, &th.RootMessageID, &th.TicketKeys, &th.State, &th.MessageCount, &th.LastHopCount,
			&th.CreatedAt, &th.UpdatedAt)
		if th.TicketKeys == nil {
			th.TicketKeys = []string{}
		}
		return th, err
	})
	return list, err
}

// GetRoomView loads #<project> channel metadata, all feature threads, and root channel messages.
func (s *Service) GetRoomView(ctx context.Context, p *board.Project, limit int) (*RoomView, error) {
	room, err := s.EnsureRoom(ctx, p)
	if err != nil {
		return nil, err
	}
	threads, err := s.listThreadsRaw(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.ReadMessages(ctx, p, "", 0, limit, false)
	if err != nil {
		return nil, err
	}
	return &RoomView{
		Room:         *room,
		MaxAgentHops: s.maxHops(ctx, p),
		Threads:      threads,
		Messages:     msgs,
	}, nil
}

// GetThreadView loads a single feat/<NNN>-<slug> thread and its messages.
func (s *Service) GetThreadView(ctx context.Context, p *board.Project, threadKey string, limit int) (*ThreadView, error) {
	room, err := s.EnsureRoom(ctx, p)
	if err != nil {
		return nil, err
	}
	var th Thread
	err = s.Pool.QueryRow(ctx, `SELECT t.id, t.room_id, t.project_id::text, t.thread_key, t.title, t.spec_id,
			coalesce(sp.spec_key, ''), t.root_message_id, t.ticket_keys, t.state,
			(SELECT count(*) FROM chat_messages m WHERE m.thread_id = t.id),
			coalesce((SELECT m.hop_count FROM chat_messages m WHERE m.thread_id = t.id AND m.sender_role <> 'system' ORDER BY m.id DESC LIMIT 1), 0),
			t.created_at, t.updated_at
		FROM chat_threads t LEFT JOIN specs sp ON sp.id = t.spec_id
		WHERE t.project_id = $1 AND t.thread_key = $2`,
		p.ID, strings.TrimSpace(threadKey)).Scan(
		&th.ID, &th.RoomID, &th.ProjectID, &th.ThreadKey, &th.Title, &th.SpecID,
		&th.SpecKey, &th.RootMessageID, &th.TicketKeys, &th.State, &th.MessageCount, &th.LastHopCount,
		&th.CreatedAt, &th.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("thread %q not found", threadKey)
	}
	if err != nil {
		return nil, err
	}
	if th.TicketKeys == nil {
		th.TicketKeys = []string{}
	}
	msgs, err := s.ReadMessages(ctx, p, th.ThreadKey, 0, limit, false)
	if err != nil {
		return nil, err
	}
	return &ThreadView{
		Room:         *room,
		Thread:       th,
		MaxAgentHops: s.maxHops(ctx, p),
		CurrentHops:  th.LastHopCount,
		Messages:     msgs,
	}, nil
}

const chatSelectCols = `SELECT m.id, m.room_id, m.thread_id, coalesce(th.thread_key, ''), m.project_id::text,
		m.task_id::text, coalesce(ft.key, ''), m.ticket_id, coalesce(pr.key || '-' || tk.number, ''),
		m.run_id::text, m.sender_role, m.sender_handle,
		m.kind, m.intent, m.event_type, m.reply_to_id, m.interrupt, m.metadata, m.choice_prompt, m.choice_response,
		m.body_sha256, m.body_runes,
		m.mentions, m.hop_count, m.fenced, m.body, m.fenced_body, m.actionable, m.spawned_session::text, m.created_at
	FROM chat_messages m
	LEFT JOIN chat_threads th ON th.id = m.thread_id
	LEFT JOIN factory_tasks ft ON ft.id = m.task_id
	LEFT JOIN tickets tk ON tk.id = m.ticket_id
	LEFT JOIN projects pr ON pr.id = tk.project`

// ReadMessages returns chronological messages in #<project> (threadKey=="") or a feature thread.
// When forAgent is true, every non-user message Content holds FencedBody (<crew_data_fence>, Rule 14)
// and bodies > 4000 runes are offloaded to a <=768-byte preview with SHA-256 + read_chat(messageId=...) hint.
func (s *Service) ReadMessages(ctx context.Context, p *board.Project, threadKey string, afterID int64, limit int, forAgent bool) ([]Message, error) {
	room, err := s.EnsureRoom(ctx, p)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	key := strings.TrimSpace(threadKey)
	var rows pgx.Rows
	if key == "" {
		rows, err = s.Pool.Query(ctx, chatSelectCols+`
			WHERE m.room_id = $1 AND m.thread_id IS NULL AND m.id > $2
			ORDER BY m.id ASC LIMIT $3`, room.ID, afterID, limit)
	} else {
		rows, err = s.Pool.Query(ctx, chatSelectCols+`
			WHERE m.room_id = $1 AND th.thread_key = $2 AND m.id > $3
			ORDER BY m.id ASC LIMIT $4`, room.ID, key, afterID, limit)
	}
	if err != nil {
		return nil, err
	}
	list, err := collectMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]int64, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	byMsg, err := loadAddresseesByMessageIDs(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	for i := range list {
		applyLoadedAddressees(&list[i], byMsg[list[i].ID], forAgent, true)
	}
	return list, nil
}

// ReadMessageByID loads one chat message by id for this project with full un-offloaded body text.
// Cross-project message ids are rejected fail-closed with ErrCrossProjectBoundary.
func (s *Service) ReadMessageByID(ctx context.Context, p *board.Project, messageID int64, forAgent bool) (*Message, error) {
	if p == nil || messageID <= 0 {
		return nil, fmt.Errorf("%w: project and positive messageId are required", ErrInvalid)
	}
	rows, err := s.Pool.Query(ctx, chatSelectCols+` WHERE m.id = $1`, messageID)
	if err != nil {
		return nil, err
	}
	list, err := collectMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: message %d not found", ErrInvalid, messageID)
	}
	msg := list[0]
	if msg.ProjectID != p.ID {
		return nil, ErrCrossProjectBoundary
	}
	byMsg, err := loadAddresseesByMessageIDs(ctx, s.Pool, []int64{msg.ID})
	if err != nil {
		return nil, err
	}
	applyLoadedAddressees(&msg, byMsg[msg.ID], forAgent, false)
	return &msg, nil
}

func collectMessages(rows pgx.Rows) ([]Message, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		var rawMeta, rawCP, rawCR []byte
		err := row.Scan(&m.ID, &m.RoomID, &m.ThreadID, &m.ThreadKey, &m.ProjectID,
			&m.TaskID, &m.TaskKey, &m.TicketID, &m.TicketKey,
			&m.RunID, &m.SenderRole, &m.SenderHandle,
			&m.Kind, &m.Intent, &m.EventType, &m.ReplyToID, &m.Interrupt, &rawMeta, &rawCP, &rawCR,
			&m.BodySHA256, &m.BodyRunes,
			&m.Mentions, &m.HopCount, &m.Fenced, &m.Body, &m.FencedBody, &m.Actionable, &m.SpawnedSession, &m.CreatedAt)
		if err != nil {
			return m, err
		}
		if m.Mentions == nil {
			m.Mentions = []string{}
		}
		decodeMessageExtras(&m, rawMeta, rawCP, rawCR)
		m.Content = m.Body
		return m, nil
	})
}

func applyLoadedAddressees(m *Message, addrs []Addressee, forAgent, allowOffload bool) {
	if len(addrs) > 0 {
		m.Addressees = addrs
	}
	if !forAgent {
		m.Content = m.Body
		m.Offloaded = false
		return
	}
	preview := m.Body
	offloaded := false
	if allowOffload {
		preview, offloaded = FormatAgentDeliveryBody(m.ID, m.Body, m.BodySHA256)
	}
	m.Offloaded = offloaded
	toHandles := make([]string, 0, len(m.Addressees))
	for _, a := range m.Addressees {
		toHandles = append(toHandles, a.RecipientHandle)
	}
	if m.Fenced {
		if offloaded || m.FencedBody == "" {
			fenced := FencePeerMessageWithEnvelope(m.SenderHandle, derefStr(m.RunID), m.HopCount, toHandles, preview)
			m.Content = fenced
			if offloaded {
				m.FencedBody = fenced
			}
		} else {
			m.Content = m.FencedBody
		}
		return
	}
	m.Content = preview
}

func loadAddresseesByMessageIDs(ctx context.Context, pool *pgxpool.Pool, ids []int64) (map[int64][]Addressee, error) {
	out := make(map[int64][]Addressee, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT id, message_id, project_id::text, recipient_role, recipient_handle, via,
			delivery_mode, delivery_state, task_id::text, session_id::text, failure_reason, created_at, updated_at
		FROM chat_message_addressees
		WHERE message_id = ANY($1)
		ORDER BY message_id ASC, id ASC`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Addressee, error) {
		var a Addressee
		err := row.Scan(&a.ID, &a.MessageID, &a.ProjectID, &a.RecipientRole, &a.RecipientHandle, &a.Via,
			&a.DeliveryMode, &a.DeliveryState, &a.TaskID, &a.SessionID, &a.FailureReason, &a.CreatedAt, &a.UpdatedAt)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, nil
}

func decodeMessageExtras(m *Message, rawMeta, rawCP, rawCR []byte) {
	if len(rawMeta) > 0 && string(rawMeta) != "{}" && string(rawMeta) != "null" {
		_ = json.Unmarshal(rawMeta, &m.Metadata)
	}
	if len(rawCP) > 0 && string(rawCP) != "null" {
		var cp ChoicePromptSpec
		if err := json.Unmarshal(rawCP, &cp); err == nil {
			m.ChoicePrompt = &cp
		}
	}
	if len(rawCR) > 0 && string(rawCR) != "null" {
		var cr ChoiceResponseSpec
		if err := json.Unmarshal(rawCR, &cr); err == nil {
			m.ChoiceResponse = &cr
		}
	}
}

func ptrInt64Eq(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dbOrZero(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}
