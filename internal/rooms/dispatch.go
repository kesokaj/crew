package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/runs"
)

var (
	validMessageKinds   = []string{"text", "event", "choice_prompt", "choice_response"}
	validMessageIntents = []string{"inform", "request", "question"}
)

// PostInput creates one message inside #<project> or a feat/<NNN>-<slug> thread.
type PostInput struct {
	ThreadKey      string
	SpecRef        string
	TicketID       int64
	TicketKey      string
	SenderRole     string // "user", "system", or peer role ("builder", "planner", "security", or custom WORKFLOW.md slug)
	RunID          string
	Kind           string // text | event | choice_prompt | choice_response
	Intent         string // inform | request | question
	EventType      string
	ReplyToID      int64
	To             []string // explicit @recipient handles
	Interrupt      bool     // honored only for @user; routes to live acp-steer
	Metadata       map[string]any
	ChoicePrompt   *ChoicePromptSpec
	ChoiceResponse *ChoiceResponseSpec
	Body           string
	Secrets        []string
	Actionable     *bool
	SkipDispatch   bool
	FromBoardHook  bool
}

// PostResult describes the persisted message, per-addressee delivery status, optional linked thread,
// and any tracked task/session minted or woken.
type PostResult struct {
	Message          Message            `json:"message"`
	Thread           *Thread            `json:"thread,omitempty"`
	Addressees       []Addressee        `json:"addressees,omitempty"`
	DeliveryOutcome  string             `json:"deliveryOutcome"`
	SpawnedTask      *board.FactoryTask `json:"spawnedTask,omitempty"`
	SpawnedSessionID string             `json:"spawnedSessionId,omitempty"`
	HopLimitReached  bool               `json:"hopLimitReached,omitempty"`
}

// AttachBoard connects Board lifecycle events (AskPlanner, DecideSpec, SaveResearchBrief)
// into #<project> and feature threads without re-triggering peer dispatch loops.
func (s *Service) AttachBoard(b *board.Board) {
	if s == nil || b == nil {
		return
	}
	b.OnRoomEvent = func(ctx context.Context, p *board.Project, threadKey, specRef, ticketKey, senderRole, runID, body string) {
		no := false
		_, err := s.PostMessage(ctx, p, PostInput{
			ThreadKey:     threadKey,
			SpecRef:       specRef,
			TicketKey:     ticketKey,
			SenderRole:    nonEmpty(senderRole, "system"),
			RunID:         runID,
			Kind:          "event",
			Intent:        "inform",
			EventType:     "board.event",
			Body:          body,
			Actionable:    &no,
			SkipDispatch:  true,
			FromBoardHook: true,
		})
		if err != nil && !errors.Is(err, ErrMaxAgentHops) {
			s.log().Warn("mirroring board event to project room", "project", p.Name, "error", err)
		}
	}
}

func (s *Service) customAgentSlugs(ctx context.Context, p *board.Project) []string {
	if s.Board == nil || p == nil {
		return nil
	}
	wf, err := s.Board.GetWorkflow(ctx, s.Pool, p)
	if err != nil || wf == nil {
		return nil
	}
	out := make([]string, 0, len(wf.Agents))
	for _, a := range wf.Agents {
		out = append(out, a.Slug)
	}
	return out
}

func (s *Service) normalizeProjectSender(ctx context.Context, p *board.Project, raw string) (role, handle string, err error) {
	r := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	switch r {
	case "", "user", "operator":
		return "user", "@user", nil
	case "system", "crew":
		return "system", "@crew", nil
	}
	norm, nerr := runs.NormalizeProjectPeerRole(r, s.customAgentSlugs(ctx, p))
	if nerr != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalid, nerr)
	}
	return norm, "@" + norm, nil
}

type resolvedAddressee struct {
	role   string
	handle string
	via    string
	isUser bool
}

func resolveAddressees(explicitTo, bodyMentions, customSlugs []string, senderRole string) (all []resolvedAddressee, wakePeers []resolvedAddressee, err error) {
	seen := map[string]bool{}
	addOne := func(raw, via string) {
		norm := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
		if norm == "" || seen[norm] {
			return
		}
		if norm == "user" || norm == "operator" {
			seen["user"] = true
			if senderRole != "user" {
				all = append(all, resolvedAddressee{role: "user", handle: "@user", via: via, isUser: true})
			}
			return
		}
		if norm == "system" || norm == "crew" {
			return
		}
		if role, rerr := runs.NormalizeProjectPeerRole(norm, customSlugs); rerr == nil {
			seen[role] = true
			ra := resolvedAddressee{role: role, handle: "@" + role, via: via}
			all = append(all, ra)
			if role != senderRole {
				wakePeers = append(wakePeers, ra)
			}
		}
	}
	for _, h := range explicitTo {
		addOne(h, "explicit")
	}
	for _, h := range bodyMentions {
		addOne(h, "body-mention")
	}
	if len(all) > MaxMentionRecipients {
		return nil, nil, fmt.Errorf("%w: got %d", ErrTooManyMentions, len(all))
	}
	return all, wakePeers, nil
}

func normalizeKindAndIntent(in PostInput, hasPeerTargets bool) (kind, intent string, err error) {
	kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = "text"
	}
	if !slices.Contains(validMessageKinds, kind) {
		return "", "", fmt.Errorf("%w: unsupported message kind %q", ErrInvalid, in.Kind)
	}
	if kind == "choice_prompt" {
		if err := ValidateChoicePrompt(in.ChoicePrompt); err != nil {
			return "", "", err
		}
	}
	intent = strings.ToLower(strings.TrimSpace(in.Intent))
	if intent == "" {
		if in.Actionable != nil && !*in.Actionable {
			intent = "inform"
		} else if kind == "choice_prompt" {
			intent = "question"
		} else if kind == "event" || kind == "choice_response" {
			intent = "inform"
		} else if hasPeerTargets {
			intent = "request"
		} else {
			intent = "inform"
		}
	}
	if !slices.Contains(validMessageIntents, intent) {
		return "", "", fmt.Errorf("%w: unsupported message intent %q", ErrInvalid, in.Intent)
	}
	return kind, intent, nil
}

// PostMessage posts a message to #<project> or a feat/<NNN>-<slug> thread, applying secret scrubbing,
// CommonMark prose mention parsing, fail-closed boundary/reply checks, provenance fencing (Rule 14),
// max_agent_hops bounds, Tasks API minting (R217), and queue-by-default vs @user steer delivery.
func (s *Service) PostMessage(ctx context.Context, p *board.Project, in PostInput) (*PostResult, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	senderRole, senderHandle, err := s.normalizeProjectSender(ctx, p, in.SenderRole)
	if err != nil {
		return nil, err
	}
	cleanBody := strings.TrimSpace(redact.New(in.Secrets...).Text(in.Body))
	if cleanBody == "" && strings.EqualFold(strings.TrimSpace(in.Kind), "choice_prompt") && in.ChoicePrompt != nil {
		cleanBody = strings.TrimSpace(redact.New(in.Secrets...).Text(in.ChoicePrompt.Question))
	}
	if cleanBody == "" {
		return nil, fmt.Errorf("%w: message body cannot be empty", ErrInvalid)
	}

	threadKey := strings.TrimSpace(in.ThreadKey)
	if strings.HasPrefix(threadKey, "#") {
		chanProj := strings.TrimPrefix(strings.SplitN(threadKey, "/", 2)[0], "#")
		if chanProj != "" && !strings.EqualFold(chanProj, p.Name) {
			return nil, ErrCrossProjectBoundary
		}
		if !strings.Contains(threadKey, "/") {
			threadKey = ""
		}
	}
	specRef := strings.TrimSpace(in.SpecRef)

	bodyMentions, mErr := ExtractProseMentions(cleanBody)
	if mErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, mErr)
	}
	customSlugs := s.customAgentSlugs(ctx, p)
	allAddressees, peerTargets, rErr := resolveAddressees(in.To, bodyMentions, customSlugs, senderRole)
	if rErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, rErr)
	}

	kind, intent, kErr := normalizeKindAndIntent(in, len(peerTargets) > 0)
	if kErr != nil {
		return nil, kErr
	}

	maxHops := s.maxHops(ctx, p)
	var room *Room
	var thread *Thread
	if threadKey != "" || specRef != "" {
		room, thread, err = s.EnsureThread(ctx, p, threadKey, "", specRef)
		if err != nil {
			return nil, err
		}
	} else {
		room, err = s.EnsureRoom(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	scopeName := room.Channel
	if thread != nil {
		scopeName = room.Channel + " " + thread.ThreadKey
	}

	var curThreadID *int64
	if thread != nil {
		v := thread.ID
		curThreadID = &v
	}
	if in.ReplyToID > 0 {
		var repProj string
		var repRoom int64
		var repThread *int64
		qerr := s.Pool.QueryRow(ctx, `SELECT project_id::text, room_id, thread_id FROM chat_messages WHERE id = $1`, in.ReplyToID).
			Scan(&repProj, &repRoom, &repThread)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: replyToId %d not found", ErrInvalid, in.ReplyToID)
		}
		if qerr != nil {
			return nil, qerr
		}
		if repProj != p.ID {
			return nil, ErrCrossProjectBoundary
		}
		if repRoom != room.ID || !ptrInt64Eq(repThread, curThreadID) {
			return nil, fmt.Errorf("%w: replyToId %d must belong to the same room or feature thread", ErrInvalid, in.ReplyToID)
		}
	}

	var hopCount int
	var hopBlocked bool
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if thread != nil {
			var lockedID int64
			if err := tx.QueryRow(ctx, `SELECT id FROM chat_threads WHERE id = $1 FOR UPDATE`, thread.ID).Scan(&lockedID); err != nil {
				return err
			}
		} else {
			var lockedID int64
			if err := tx.QueryRow(ctx, `SELECT id FROM chat_rooms WHERE id = $1 FOR UPDATE`, room.ID).Scan(&lockedID); err != nil {
				return err
			}
		}
		if kind == "choice_prompt" {
			if cerr := prepareChoicePromptTx(ctx, tx, p.ID, in.ChoicePrompt); cerr != nil {
				return cerr
			}
		}
		var lastRole string
		var lastHop int
		var scanErr error
		if thread != nil {
			scanErr = tx.QueryRow(ctx, `SELECT sender_role, hop_count FROM chat_messages
				WHERE thread_id = $1 AND sender_role <> 'system' ORDER BY id DESC LIMIT 1`, thread.ID).Scan(&lastRole, &lastHop)
		} else {
			scanErr = tx.QueryRow(ctx, `SELECT sender_role, hop_count FROM chat_messages
				WHERE room_id = $1 AND thread_id IS NULL AND sender_role <> 'system' ORDER BY id DESC LIMIT 1`, room.ID).Scan(&lastRole, &lastHop)
		}
		if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
			return scanErr
		}
		switch senderRole {
		case "user":
			hopCount = 0
		case "system":
			hopCount = lastHop
		default:
			if errors.Is(scanErr, pgx.ErrNoRows) || lastRole == "user" {
				hopCount = 1
			} else {
				hopCount = lastHop + 1
			}
			if hopCount > maxHops {
				if err := recordHopLimitSignalTx(ctx, tx, p.ID, senderRole, scopeName, hopCount, maxHops); err != nil {
					return err
				}
				hopBlocked = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hopBlocked {
		return nil, ErrMaxAgentHops
	}

	wantAction := len(peerTargets) > 0 && senderRole != "system" && kind != "event" && kind != "choice_response" && intent != "inform"
	if in.Actionable != nil {
		wantAction = *in.Actionable && len(peerTargets) > 0
	}

	var ticketID *int64
	ticketKey := strings.TrimSpace(in.TicketKey)
	if in.TicketID > 0 {
		id := in.TicketID
		ticketID = &id
	} else if ticketKey != "" && s.Board != nil {
		if tk, tkErr := s.Board.Ticket(ctx, p, ticketKey); tkErr == nil {
			ticketID = &tk.ID
			ticketKey = tk.Key
		}
	}

	res := &PostResult{Thread: thread, DeliveryOutcome: "posted_nobody_woken"}
	var taskID *string
	var targetRole string
	if wantAction && hopCount >= maxHops {
		res.HopLimitReached = true
		_ = s.recordHopLimitSignal(ctx, p.ID, senderRole, scopeName, hopCount+1, maxHops)
	} else if wantAction && !in.FromBoardHook && s.Board != nil {
		targetRole = peerTargets[0].role
		if targetRole == "planner" {
			ref := specRef
			if thread != nil && thread.SpecKey != "" {
				ref = thread.SpecKey
			}
			thTitle := ""
			if thread != nil {
				thTitle = thread.Title
			}
			sp, _, askErr := s.Board.AskPlanner(ctx, p, board.AskPlannerInput{
				SpecRef:        ref,
				Title:          thTitle,
				TicketID:       dbOrZero(ticketID),
				TicketKey:      ticketKey,
				AskingRole:     senderRole,
				AskingRun:      in.RunID,
				Question:       cleanBody,
				SkipRoomMirror: true,
			})
			if askErr != nil {
				return nil, askErr
			}
			if sp.FactoryTaskID != nil {
				tid := *sp.FactoryTaskID
				taskID = &tid
				if ft, ferr := s.Board.GetFactoryTask(ctx, p.ID, tid); ferr == nil {
					res.SpawnedTask = ft
				}
			}
			if thread == nil && sp.ThreadRef != "" {
				if r2, t2, e2 := s.EnsureThread(ctx, p, sp.ThreadRef, sp.Title, sp.SpecKey); e2 == nil {
					room, thread = r2, t2
					res.Thread = thread
					v := thread.ID
					curThreadID = &v
				}
			}
			if s.KickPlanner != nil {
				s.KickPlanner()
			}
		} else {
			specPath := specRef
			if thread != nil && thread.SpecKey != "" {
				specPath = ".crew/specs/" + strings.TrimPrefix(thread.ThreadKey, "feat/") + ".md"
			}
			ft, ferr := s.Board.CreateFactoryTask(ctx, p.ID, board.NewFactoryTask{
				Kind:       "chat_action",
				Title:      truncateText(fmt.Sprintf("Chat @%s in %s: %s", targetRole, scopeName, cleanBody), 140),
				Role:       "@" + targetRole,
				TicketRef:  ticketKey,
				SpecRef:    specPath,
				OriginRoom: scopeName,
				CreatedBy:  senderHandle,
			})
			if ferr != nil {
				return nil, ferr
			}
			tid := ft.ID
			taskID = &tid
			res.SpawnedTask = ft
		}
	}

	toHandles := make([]string, 0, len(allAddressees))
	for _, a := range allAddressees {
		toHandles = append(toHandles, a.handle)
	}
	fenced := senderRole != "user"
	fencedBody := cleanBody
	if fenced {
		fencedBody = FencePeerMessageWithEnvelope(senderHandle, in.RunID, hopCount, toHandles, cleanBody)
	}
	safeMeta := StripReservedMetadata(in.Metadata)
	metaJSON, _ := json.Marshal(safeMeta)
	var cpJSON, crJSON []byte
	if in.ChoicePrompt != nil {
		cpJSON, _ = json.Marshal(in.ChoicePrompt)
	}
	if in.ChoiceResponse != nil {
		crJSON, _ = json.Marshal(in.ChoiceResponse)
	}
	bodySHA, bodyRunes := ComputeBodyDigest(cleanBody)
	var replyPtr *int64
	if in.ReplyToID > 0 {
		v := in.ReplyToID
		replyPtr = &v
	}
	mentions := make([]string, 0, len(allAddressees))
	for _, a := range allAddressees {
		mentions = append(mentions, a.role)
	}
	interruptAllowed := senderRole == "user" && in.Interrupt

	var msg Message
	var rawMeta, rawCP, rawCR []byte
	err = s.Pool.QueryRow(ctx, `INSERT INTO chat_messages (
			room_id, thread_id, project_id, task_id, ticket_id, run_id,
			sender_role, sender_handle, kind, intent, event_type, reply_to_id,
			interrupt, metadata, choice_prompt, choice_response, body_sha256, body_runes,
			mentions, hop_count, fenced, body, fenced_body, actionable)
		VALUES ($1, $2, $3, $4::uuid, $5, nullif($6, '')::uuid,
			$7, $8, $9, $10, $11, $12,
			$13, $14::jsonb, $15::jsonb, $16::jsonb, $17, $18,
			$19, $20, $21, $22, $23, $24)
		RETURNING id, room_id, thread_id, $25::text, project_id::text, task_id::text, $26::text, ticket_id, $27::text,
			run_id::text, sender_role, sender_handle, kind, intent, event_type, reply_to_id,
			interrupt, metadata, choice_prompt, choice_response, body_sha256, body_runes,
			mentions, hop_count, fenced, body, fenced_body, actionable,
			spawned_session::text, created_at`,
		room.ID, curThreadID, p.ID, taskID, ticketID, strings.TrimSpace(in.RunID),
		senderRole, senderHandle, kind, intent, strings.TrimSpace(in.EventType), replyPtr,
		interruptAllowed, metaJSON, cpJSON, crJSON, bodySHA, bodyRunes,
		mentions, hopCount, fenced, cleanBody, fencedBody, wantAction,
		nonEmptyKey(thread), taskKeyOf(res.SpawnedTask), ticketKey).Scan(
		&msg.ID, &msg.RoomID, &msg.ThreadID, &msg.ThreadKey, &msg.ProjectID, &msg.TaskID, &msg.TaskKey,
		&msg.TicketID, &msg.TicketKey, &msg.RunID, &msg.SenderRole, &msg.SenderHandle,
		&msg.Kind, &msg.Intent, &msg.EventType, &msg.ReplyToID, &msg.Interrupt,
		&rawMeta, &rawCP, &rawCR, &msg.BodySHA256, &msg.BodyRunes,
		&msg.Mentions, &msg.HopCount, &msg.Fenced, &msg.Body, &msg.FencedBody, &msg.Actionable, &msg.SpawnedSession, &msg.CreatedAt)
	if err != nil {
		return nil, err
	}
	decodeMessageExtras(&msg, rawMeta, rawCP, rawCR)
	msg.Content = msg.Body

	if thread != nil {
		if _, uerr := s.Pool.Exec(ctx, `UPDATE chat_threads SET updated_at = now() WHERE id = $1`, thread.ID); uerr != nil {
			s.log().Warn("updating thread timestamp", "thread", thread.ThreadKey, "error", uerr)
		}
	}

	addressees := make([]Addressee, 0, len(allAddressees))
	for _, ra := range allAddressees {
		mode, state := "none", "suppressed"
		var aTaskID *string
		if !ra.isUser && wantAction && res.SpawnedTask != nil && ra.role == targetRole {
			state = "pending"
			aTaskID = taskID
			if targetRole == "planner" {
				mode, state = "turn", "queued"
			}
		} else if ra.isUser {
			mode, state = "none", "delivered"
		}
		var a Addressee
		if ierr := s.Pool.QueryRow(ctx, `INSERT INTO chat_message_addressees (
				message_id, project_id, recipient_role, recipient_handle, via, delivery_mode, delivery_state, task_id)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8::uuid)
			ON CONFLICT (message_id, recipient_role) DO UPDATE SET updated_at = now()
			RETURNING id, message_id, project_id::text, recipient_role, recipient_handle, via,
				delivery_mode, delivery_state, task_id::text, session_id::text, failure_reason, created_at, updated_at`,
			msg.ID, p.ID, ra.role, ra.handle, ra.via, mode, state, aTaskID).Scan(
			&a.ID, &a.MessageID, &a.ProjectID, &a.RecipientRole, &a.RecipientHandle, &a.Via,
			&a.DeliveryMode, &a.DeliveryState, &a.TaskID, &a.SessionID, &a.FailureReason, &a.CreatedAt, &a.UpdatedAt); ierr != nil {
			return nil, ierr
		}
		addressees = append(addressees, a)
	}

	if res.SpawnedTask != nil && targetRole != "" {
		res.DeliveryOutcome = "woken"
	}

	if res.SpawnedTask != nil && targetRole != "" && targetRole != "planner" && s.Dispatcher != nil && !in.SkipDispatch {
		prompt := s.buildChatActionPrompt(ctx, p, room, thread, msg, maxHops, toHandles)
		customSpec := ""
		readOnly := targetRole != "builder"
		if s.Board != nil {
			if _, runErr := runs.NormalizePeerRole(targetRole); runErr != nil {
				if ag, body, lerr := s.Board.LoadCustomWorkflowAgent(ctx, p, targetRole); lerr == nil && ag != nil {
					readOnly = ag.ReadOnly
					if body != "" {
						customSpec = body + "\n\n"
					}
				} else if lerr != nil {
					updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "none", "failed", "", lerr.Error())
					return nil, lerr
				}
			}
		}
		prompt = customSpec + prompt

		var actSess *runs.PeerSession
		if router, ok := s.Dispatcher.(ActivePeerRouter); ok {
			actSess, _ = router.FindActivePeerSession(ctx, p.ID, targetRole, dbOrZero(ticketID))
			if actSess != nil {
				if interruptAllowed {
					if serr := router.SteerPeerSession(ctx, actSess.ID, prompt); serr != nil {
						updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "steer", "failed", actSess.ID, serr.Error())
						s.log().Warn("steering active peer session", "project", p.Name, "role", targetRole, "error", serr)
					} else {
						res.SpawnedSessionID = actSess.ID
						msg.SpawnedSession = &actSess.ID
						updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "steer", "delivered", actSess.ID, "")
					}
				} else {
					nextSess, terr := router.TurnPeerSession(ctx, actSess.ID, prompt)
					switch {
					case terr == nil && nextSess != nil:
						res.SpawnedSessionID = nextSess.ID
						msg.SpawnedSession = &nextSess.ID
						updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "turn", "delivered", nextSess.ID, "")
					case errors.Is(terr, runs.ErrBusy):
						res.SpawnedSessionID = actSess.ID
						msg.SpawnedSession = &actSess.ID
						updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "turn", "queued", actSess.ID, "")
					default:
						updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "turn", "failed", actSess.ID, terr.Error())
						s.log().Warn("queuing turn on active peer session", "project", p.Name, "role", targetRole, "error", terr)
					}
				}
			}
		}

		if actSess == nil {
			sess, derr := s.Dispatcher.SpawnPeerSession(ctx, runs.SpawnPeerRequest{
				ProjectID: p.ID,
				TicketID:  dbOrZero(ticketID),
				TaskID:    res.SpawnedTask.ID,
				TaskKind:  "chat_action",
				Role:      "@" + targetRole,
				Title:     res.SpawnedTask.Title,
				Prompt:    prompt,
				ReadOnly:  &readOnly,
			})
			if derr != nil {
				updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "spawn", "failed", "", derr.Error())
				s.log().Warn("dispatching chat_action peer session", "project", p.Name, "role", targetRole, "error", derr)
			} else if sess != nil {
				res.SpawnedSessionID = sess.ID
				msg.SpawnedSession = &sess.ID
				updateAddresseeState(ctx, s.Pool, msg.ID, targetRole, "spawn", "delivered", sess.ID, "")
				if _, uerr := s.Pool.Exec(ctx, `UPDATE chat_messages SET spawned_session = $2::uuid WHERE id = $1`, msg.ID, sess.ID); uerr != nil {
					s.log().Warn("recording spawned session on chat message", "message", msg.ID, "error", uerr)
				}
			}
		}
	}

	if refreshed, rerr := loadAddresseesByMessageIDs(ctx, s.Pool, []int64{msg.ID}); rerr == nil {
		if list := refreshed[msg.ID]; len(list) > 0 {
			addressees = list
		}
	}
	msg.Addressees = addressees
	res.Message = msg
	res.Addressees = addressees
	if s.KickPipeline != nil && wantAction {
		s.KickPipeline()
	}
	return res, nil
}

func updateAddresseeState(ctx context.Context, pool *pgxpool.Pool, msgID int64, role, mode, state, sessID, reason string) {
	if pool == nil || msgID <= 0 || strings.TrimSpace(role) == "" {
		return
	}
	_, _ = pool.Exec(ctx, `UPDATE chat_message_addressees
		SET delivery_mode = $3,
			delivery_state = $4,
			session_id = coalesce(nullif($5, '')::uuid, session_id),
			failure_reason = $6,
			updated_at = now()
		WHERE message_id = $1 AND recipient_role = $2`,
		msgID, role, mode, state, strings.TrimSpace(sessID), strings.TrimSpace(reason))
}

func (s *Service) buildChatActionPrompt(ctx context.Context, p *board.Project, room *Room, thread *Thread, trigger Message, maxHops int, toHandles []string) string {
	var b strings.Builder
	b.WriteString(baseline.MustText("stages/chat_action.md"))
	b.WriteString("\n\n")
	if thread != nil {
		fmt.Fprintf(&b, "Channel: %s\nThread: %s (%s)\n", room.Channel, thread.ThreadKey, thread.Title)
	} else {
		fmt.Fprintf(&b, "Channel: %s\n", room.Channel)
	}
	if len(toHandles) > 0 {
		fmt.Fprintf(&b, "Addressees: %s\n", strings.Join(toHandles, ", "))
	}
	fmt.Fprintf(&b, "Current hop: %d/%d\n\nRecent messages (all peer messages fenced as data):\n", trigger.HopCount, maxHops)
	thKey := ""
	if thread != nil {
		thKey = thread.ThreadKey
	}
	msgs, err := s.ReadMessages(ctx, p, thKey, 0, 12, true)
	if err == nil && len(msgs) > 0 {
		for _, m := range msgs {
			fmt.Fprintf(&b, "\n[#%d %s hop=%d kind=%s intent=%s]\n%s\n", m.ID, m.SenderHandle, m.HopCount, m.Kind, m.Intent, m.Content)
		}
	} else {
		preview, _ := FormatAgentDeliveryBody(trigger.ID, trigger.Body, trigger.BodySHA256)
		content := preview
		if trigger.Fenced || trigger.SenderRole != "user" {
			content = FencePeerMessageWithEnvelope(trigger.SenderHandle, derefStr(trigger.RunID), trigger.HopCount, toHandles, preview)
		}
		fmt.Fprintf(&b, "\n[#%d %s hop=%d kind=%s intent=%s]\n%s\n", trigger.ID, trigger.SenderHandle, trigger.HopCount, trigger.Kind, trigger.Intent, content)
	}
	return b.String()
}

func (s *Service) recordHopLimitSignal(ctx context.Context, projectID, senderRole, scope string, hop, maxHops int) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return recordHopLimitSignalTx(ctx, tx, projectID, senderRole, scope, hop, maxHops)
	})
}

func recordHopLimitSignalTx(ctx context.Context, tx pgx.Tx, projectID, senderRole, scope string, hop, maxHops int) error {
	detail, _ := json.Marshal(map[string]any{
		"role":    senderRole,
		"scope":   scope,
		"hop":     hop,
		"maxHops": maxHops,
		"text":    fmt.Sprintf("Stopped @%s reply in %s at hop %d/%d", senderRole, scope, hop, maxHops),
	})
	_, err := tx.Exec(ctx, `INSERT INTO signals (project, kind, detail) VALUES ($1, 'chat_hop_limit', $2::jsonb)`,
		projectID, detail)
	return err
}

func nonEmptyKey(t *Thread) string {
	if t == nil {
		return ""
	}
	return t.ThreadKey
}

func taskKeyOf(ft *board.FactoryTask) string {
	if ft == nil {
		return ""
	}
	return ft.Key
}

func truncateText(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}
