package rooms_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/rooms"
	"github.com/kesokaj/crew/internal/runs"
)

type recordingDispatcher struct {
	mu       sync.Mutex
	requests []runs.SpawnPeerRequest
}

func (d *recordingDispatcher) SpawnPeerSession(_ context.Context, req runs.SpawnPeerRequest) (*runs.PeerSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	return &runs.PeerSession{
		ID:       "11111111-1111-4111-8111-111111111111",
		Role:     req.Role,
		ReadOnly: true,
		TaskID:   req.TaskID,
	}, nil
}

func TestM24ProjectChannelFeatureThreadsHopsFenceAndRedaction(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	git := &gitstore.Store{
		Dir:        filepath.Join(dir, "canonical"),
		Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")},
	}
	b := &board.Board{
		Pool: d.Server,
		Git:  git,
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		},
	}
	disp := &recordingDispatcher{}
	svc := &rooms.Service{
		Pool:       d.Server,
		Board:      b,
		Dispatcher: disp,
	}
	svc.AttachBoard(b)

	p, err := b.CreateProject(ctx, board.NewProject{
		Name:   "factory",
		Key:    "FC",
		Goal:   "Verify unified room and feature threads.",
		Plan:   "## Goal\n\nVerify M24 room service.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	roomList, err := svc.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	foundFactory := false
	for _, r := range roomList {
		if r.Channel == "#factory" {
			foundFactory = true
			break
		}
	}
	if !foundFactory {
		t.Fatalf("expected #factory room, got %+v", roomList)
	}

	// AskPlanner mirrors the question into a feat/001-* thread and opens it in #factory.
	_, _, err = b.AskPlanner(ctx, p, board.AskPlannerInput{
		Title:      "Cache invalidation",
		AskingRole: "builder",
		Question:   "Should cache entries invalidate by version or TTL?",
	})
	if err != nil {
		t.Fatalf("AskPlanner: %v", err)
	}

	roomView, err := svc.GetRoomView(ctx, p, 50)
	if err != nil {
		t.Fatalf("GetRoomView: %v", err)
	}
	if len(roomView.Threads) != 1 || !strings.HasPrefix(roomView.Threads[0].ThreadKey, "feat/001-") {
		t.Fatalf("expected auto-created feature thread, got %+v", roomView.Threads)
	}
	threadKey := roomView.Threads[0].ThreadKey

	// AskPlanner already mirrored Hop 1; res1 (@builder -> @researcher) is Hop 2.
	fakeSecret := "gh" + "s_1234567890abcdefghijklmnopqrstuvwxyz1234"
	longPrefix := strings.Repeat("a", 138) + "—"
	res1, err := svc.PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:  threadKey,
		SenderRole: "builder",
		Body:       longPrefix + " @researcher inspect token " + fakeSecret + " please",
		Secrets:    []string{fakeSecret},
	})
	if err != nil {
		t.Fatalf("PostMessage hop 2: %v", err)
	}
	if res1.Message.HopCount != 2 || !res1.Message.Fenced {
		t.Fatalf("expected hop 2 fenced message, got %+v", res1.Message)
	}
	if strings.Contains(res1.Message.Body, fakeSecret) || strings.Contains(res1.Message.FencedBody, fakeSecret) {
		t.Fatalf("expected secret scrubbed from body and fenced body")
	}
	if res1.SpawnedTask == nil || res1.SpawnedTask.Kind != "chat_action" || res1.SpawnedTask.Role != "@researcher" {
		t.Fatalf("expected chat_action task for @researcher, got %+v", res1.SpawnedTask)
	}

	// ReadMessages differentiates operator vs agent views with <crew_data_fence> (Rule 14).
	opMsgs, err := svc.ReadMessages(ctx, p, threadKey, 0, 50, false)
	if err != nil {
		t.Fatalf("ReadMessages operator: %v", err)
	}
	agentMsgs, err := svc.ReadMessages(ctx, p, threadKey, 0, 50, true)
	if err != nil {
		t.Fatalf("ReadMessages agent: %v", err)
	}
	lastOp := opMsgs[len(opMsgs)-1]
	lastAgent := agentMsgs[len(agentMsgs)-1]
	if strings.Contains(lastOp.Content, "<crew_data_fence") {
		t.Fatalf("operator view should not fence Content, got %q", lastOp.Content)
	}
	if !strings.Contains(lastAgent.Content, `<crew_data_fence sender="@builder"`) || !strings.Contains(lastAgent.Content, "BEGIN Peer chat message") {
		t.Fatalf("agent view must wrap peer message in <crew_data_fence> and fence.New markers, got %q", lastAgent.Content)
	}

	disp.mu.Lock()
	if len(disp.requests) != 1 || disp.requests[0].ReadOnly == nil || !*disp.requests[0].ReadOnly {
		t.Fatalf("expected read-only SpawnPeerSession for @researcher, got %+v", disp.requests)
	}
	if !strings.Contains(disp.requests[0].Prompt, "<crew_data_fence") {
		t.Fatalf("expected dispatched peer prompt to fence chatter, got %q", disp.requests[0].Prompt)
	}
	disp.mu.Unlock()

	// Hop 3 reaches max_agent_hops=3 and stops re-dispatching peers; Hop 4 refuses + persists signal.
	res2, err := svc.PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:  threadKey,
		SenderRole: "researcher",
		Body:       "Hop 3 findings reviewed; @tester please check edge cases.",
	})
	if err != nil {
		t.Fatalf("PostMessage hop 3: %v", err)
	}
	if res2.Message.HopCount != 3 || !res2.HopLimitReached || res2.SpawnedTask != nil {
		t.Fatalf("expected hop 3 to set HopLimitReached without spawning task, got %+v", res2)
	}

	_, err = svc.PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:  threadKey,
		SenderRole: "builder",
		Body:       "Hop 4 should be rejected.",
	})
	if !errors.Is(err, rooms.ErrMaxAgentHops) {
		t.Fatalf("expected ErrMaxAgentHops on hop 4, got %v", err)
	}

	var sigCount int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM signals WHERE project = $1 AND kind = 'chat_hop_limit'`, p.ID).Scan(&sigCount); err != nil {
		t.Fatalf("query chat_hop_limit signals: %v", err)
	}
	if sigCount < 1 {
		t.Fatalf("expected committed chat_hop_limit signal row, got %d", sigCount)
	}

	// @user resets hop counter back to 0 so agents may resume bounded replies.
	no := false
	userRes, err := svc.PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:  threadKey,
		SenderRole: "user",
		Body:       "@builder Proceed with the cache fix.",
		Actionable: &no,
	})
	if err != nil {
		t.Fatalf("PostMessage user reset: %v", err)
	}
	if userRes.Message.HopCount != 0 || userRes.Message.Fenced {
		t.Fatalf("expected @user post hop 0 unfenced, got %+v", userRes.Message)
	}

	afterReset, err := svc.PostMessage(ctx, p, rooms.PostInput{
		ThreadKey:  threadKey,
		SenderRole: "builder",
		Body:       "Acknowledged @user, restarting at hop 1.",
		Actionable: &no,
	})
	if err != nil {
		t.Fatalf("PostMessage hop 1 after reset: %v", err)
	}
	if afterReset.Message.HopCount != 1 {
		t.Fatalf("expected hop_count=1 after user reset, got %+v", afterReset.Message)
	}
}

type mockActiveRouter struct {
	mu      sync.Mutex
	active  map[string]*runs.PeerSession
	turns   []string
	steers  []string
	turnErr error
	spawns  []runs.SpawnPeerRequest
}

func (m *mockActiveRouter) SpawnPeerSession(_ context.Context, req runs.SpawnPeerRequest) (*runs.PeerSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawns = append(m.spawns, req)
	return &runs.PeerSession{
		ID:       "22222222-2222-4222-8222-222222222222",
		Role:     req.Role,
		ReadOnly: true,
		TaskID:   req.TaskID,
	}, nil
}

func (m *mockActiveRouter) FindActivePeerSession(_ context.Context, _ string, role string, _ int64) (*runs.PeerSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[role], nil
}

func (m *mockActiveRouter) TurnPeerSession(_ context.Context, sessionID, text string) (*runs.PeerSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns = append(m.turns, sessionID+":"+text)
	if m.turnErr != nil {
		return nil, m.turnErr
	}
	return &runs.PeerSession{ID: sessionID}, nil
}

func (m *mockActiveRouter) SteerPeerSession(_ context.Context, sessionID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steers = append(m.steers, sessionID+":"+text)
	return nil
}

func TestExtractProseMentionsAndMetadataOffload(t *testing.T) {
	md := strings.Join([]string{
		"Please route this to @builder and @security.",
		"Inline `@reviewer` must be ignored.",
		"> @researcher inside blockquote is ignored.",
		"```go",
		"// @planner inside backtick fence is ignored",
		"```",
		"~~~txt",
		"@coordinator inside tilde fence is ignored",
		"~~~",
	}, "\n")
	mentions, err := rooms.ExtractProseMentions(md)
	if err != nil {
		t.Fatalf("ExtractProseMentions: %v", err)
	}
	if len(mentions) != 2 || mentions[0] != "builder" || mentions[1] != "security" {
		t.Fatalf("unexpected mentions: %v", mentions)
	}

	if _, err := rooms.ExtractProseMentions("@a1 @a2 @a3 @a4 @a5 @a6 @a7 @a8 @a9 @a10 @a11"); !errors.Is(err, rooms.ErrTooManyMentions) {
		t.Fatalf("expected ErrTooManyMentions, got %v", err)
	}

	stripped := rooms.StripReservedMetadata(map[string]any{
		"fenced":      false,
		"verified":    true,
		"provenance":  "fake",
		"crew_secret": "nope",
		"_crew_tag":   "nope",
		"topic":       "caching",
	})
	if len(stripped) != 1 || stripped["topic"] != "caching" {
		t.Fatalf("StripReservedMetadata leaked reserved keys: %+v", stripped)
	}

	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{
		Pool: d.Server,
		Git:  &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		},
	}
	svc := &rooms.Service{Pool: d.Server, Board: b}
	svc.AttachBoard(b)
	p, err := b.CreateProject(ctx, board.NewProject{
		Name:   "offloadproj",
		Key:    "OP",
		Goal:   "Test large-body offload.",
		Plan:   "## Goal\n\nVerify message offload.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	longBody := "line-one-prefix\n" + strings.Repeat("x", 4500) + "\nend-marker"
	no := false
	posted, err := svc.PostMessage(ctx, p, rooms.PostInput{
		SenderRole: "builder",
		Intent:     "inform",
		Body:       longBody,
		Metadata:   map[string]any{"verified": true, "crew_spoof": "bad", "trace": "ok"},
		Actionable: &no,
	})
	if err != nil {
		t.Fatalf("PostMessage long body: %v", err)
	}
	if posted.Message.BodyRunes <= 4000 || posted.Message.BodySHA256 == "" {
		t.Fatalf("expected BodyRunes>4000 and BodySHA256, got %+v", posted.Message)
	}
	if _, has := posted.Message.Metadata["verified"]; has || posted.Message.Metadata["trace"] != "ok" {
		t.Fatalf("expected stripped metadata on posted message, got %+v", posted.Message.Metadata)
	}

	uiMsgs, err := svc.ReadMessages(ctx, p, "", 0, 20, false)
	if err != nil || len(uiMsgs) != 1 || uiMsgs[0].Offloaded || uiMsgs[0].Content != longBody {
		t.Fatalf("expected full UI body when forAgent=false, got len=%d err=%v", len(uiMsgs), err)
	}

	agentMsgs, err := svc.ReadMessages(ctx, p, "", 0, 20, true)
	if err != nil || len(agentMsgs) != 1 {
		t.Fatalf("ReadMessages(forAgent=true): %v", err)
	}
	if !agentMsgs[0].Offloaded || !strings.Contains(agentMsgs[0].Content, "read_chat(messageId=") || !strings.Contains(agentMsgs[0].Content, posted.Message.BodySHA256) {
		t.Fatalf("expected offloaded preview + sha256 + read_chat hint, got offloaded=%v content=%s", agentMsgs[0].Offloaded, agentMsgs[0].Content)
	}
	if len([]rune(agentMsgs[0].Content)) >= 4000 {
		t.Fatalf("expected compact offloaded agent content, got %d runes", len([]rune(agentMsgs[0].Content)))
	}

	fullMsg, err := svc.ReadMessageByID(ctx, p, posted.Message.ID, true)
	if err != nil {
		t.Fatalf("ReadMessageByID: %v", err)
	}
	if fullMsg.Offloaded || !strings.Contains(fullMsg.Content, "end-marker") || fullMsg.Body != longBody {
		t.Fatalf("expected full un-offloaded message from ReadMessageByID, got offloaded=%v", fullMsg.Offloaded)
	}
}

func TestOrthogonalEnvelopeDeliveryActiveRouterAndChoicePrompts(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{
		Pool: d.Server,
		Git:  &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		},
	}
	router := &mockActiveRouter{
		active: map[string]*runs.PeerSession{
			"builder": {ID: "33333333-3333-4333-8333-333333333333", Role: "@builder", State: "working"},
		},
		turnErr: runs.ErrBusy,
	}
	svc := &rooms.Service{Pool: d.Server, Board: b, Dispatcher: router}
	svc.AttachBoard(b)

	p1, err := b.CreateProject(ctx, board.NewProject{
		Name:   "alpha",
		Key:    "AL",
		Goal:   "Verify chat envelope, router, and choices.",
		Plan:   "## Goal\n\nChat test.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject p1: %v", err)
	}
	p2, err := b.CreateProject(ctx, board.NewProject{
		Name:   "beta",
		Key:    "BT",
		Goal:   "Sibling project boundary.",
		Plan:   "## Goal\n\nBoundary test.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject p2: %v", err)
	}

	// Ensure a baseline spec exists so SpecDecision writes can attach.
	sp, spErr := b.WriteFeatureSpec(ctx, p1, board.EnsureSpecInput{
		Title:        "Cache Policy",
		BodyMarkdown: "## Goal\n\nDefine cache eviction.\n",
		CreatedBy:    "planner",
	})
	if spErr != nil {
		t.Fatalf("WriteFeatureSpec: %v", spErr)
	}

	// 1. 0-addressee inform posts cleanly without waking anyone or spawning tasks.
	noWake, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		SenderRole: "user",
		Intent:     "inform",
		Body:       "Status update only; nobody woken.",
	})
	if err != nil {
		t.Fatalf("PostMessage inform: %v", err)
	}
	if noWake.DeliveryOutcome != "posted_nobody_woken" || noWake.SpawnedTask != nil || len(router.spawns) != 0 {
		t.Fatalf("expected posted_nobody_woken with zero tasks, got %+v spawns=%d", noWake, len(router.spawns))
	}

	// Even with @builder mentioned, intent=inform suppresses waking.
	suppressedWake, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		SenderRole: "user",
		Intent:     "inform",
		To:         []string{"@builder"},
		Body:       "FYI @builder this does not wake you.",
	})
	if err != nil {
		t.Fatalf("PostMessage inform with mention: %v", err)
	}
	if suppressedWake.DeliveryOutcome != "posted_nobody_woken" || len(suppressedWake.Addressees) != 1 || suppressedWake.Addressees[0].DeliveryState != "suppressed" {
		t.Fatalf("expected suppressed addressee on intent=inform, got %+v", suppressedWake)
	}

	// 2. ReplyToID enforces same-thread and cross-project fail-closed boundary.
	_, th, err := svc.EnsureThread(ctx, p1, "feat/001-cache-policy", "Cache Policy", sp.SpecKey)
	if err != nil {
		t.Fatalf("EnsureThread: %v", err)
	}
	if _, rerr := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		ReplyToID:  noWake.Message.ID,
		SenderRole: "user",
		Body:       "Cross-thread reply should fail.",
	}); !errors.Is(rerr, rooms.ErrInvalid) {
		t.Fatalf("expected ErrInvalid on cross-thread replyToId, got %v", rerr)
	}
	if _, xerr := svc.PostMessage(ctx, p2, rooms.PostInput{
		ReplyToID:  noWake.Message.ID,
		SenderRole: "user",
		Body:       "Cross-project reply must fail closed.",
	}); !errors.Is(xerr, rooms.ErrCrossProjectBoundary) {
		t.Fatalf("expected ErrCrossProjectBoundary on cross-project replyToId, got %v", xerr)
	}
	if _, xerr := svc.ReadMessageByID(ctx, p2, noWake.Message.ID, true); !errors.Is(xerr, rooms.ErrCrossProjectBoundary) {
		t.Fatalf("expected ErrCrossProjectBoundary on ReadMessageByID, got %v", xerr)
	}

	// 3. Routine peer ping routes via queued TurnPeerSession (ErrBusy -> queued), and agent Interrupt=true is ignored!
	routineRes, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		SenderRole: "security",
		To:         []string{"@builder", "@user"},
		Interrupt:  true, // ignored because senderRole != user
		Intent:     "request",
		Body:       "Check input sanitization with @user in mind.",
	})
	if err != nil {
		t.Fatalf("PostMessage peer routine: %v", err)
	}
	if routineRes.Message.Interrupt {
		t.Fatalf("expected Interrupt stripped for non-user sender")
	}
	if !strings.Contains(routineRes.Message.FencedBody, `to="@builder,@user"`) {
		t.Fatalf("expected co-addressees in <crew_data_fence>, got %s", routineRes.Message.FencedBody)
	}
	if len(routineRes.Addressees) != 2 || routineRes.Addressees[0].DeliveryMode != "turn" || routineRes.Addressees[0].DeliveryState != "queued" {
		t.Fatalf("expected turn/queued for busy active builder, got %+v", routineRes.Addressees)
	}

	// Explicit @user Interrupt=true invokes SteerPeerSession (steer/delivered).
	steerRes, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		SenderRole: "user",
		To:         []string{"@builder"},
		Interrupt:  true,
		Intent:     "request",
		Body:       "Stop current test run and switch to TTL.",
	})
	if err != nil {
		t.Fatalf("PostMessage user steer: %v", err)
	}
	if !steerRes.Message.Interrupt || len(steerRes.Addressees) != 1 || steerRes.Addressees[0].DeliveryMode != "steer" || steerRes.Addressees[0].DeliveryState != "delivered" {
		t.Fatalf("expected steer/delivered for @user interrupt, got %+v", steerRes)
	}
	if len(router.steers) != 1 || len(router.turns) != 1 {
		t.Fatalf("expected 1 turn and 1 steer invocation, got turns=%d steers=%d", len(router.turns), len(router.steers))
	}

	// 4. Keyed Choice Prompts: v1 -> v2 atomic update, agent_interpreted with sourceMessageId, direct @user override, stale v1 rejection.
	cpPost1, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		SpecRef:    sp.SpecKey,
		SenderRole: "planner",
		Kind:       "choice_prompt",
		Body:       "Choose eviction policy v1",
		ChoicePrompt: &rooms.ChoicePromptSpec{
			ID:       "eviction-policy",
			Question: "Select cache eviction policy",
			Default:  "lru",
			SpecRef:  sp.SpecKey,
			Choices: []rooms.ChoiceOption{
				{ID: "lru", Label: "LRU"},
				{ID: "fifo", Label: "FIFO"},
			},
		},
	})
	if err != nil || cpPost1.Message.ChoicePrompt == nil || cpPost1.Message.ChoicePrompt.Version != 1 {
		t.Fatalf("choice_prompt v1 failed: res=%+v err=%v", cpPost1, err)
	}

	cpPost2, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		SpecRef:    sp.SpecKey,
		SenderRole: "planner",
		Kind:       "choice_prompt",
		Body:       "Choose eviction policy v2",
		ChoicePrompt: &rooms.ChoicePromptSpec{
			ID:       "eviction-policy",
			Update:   true,
			Question: "Select cache eviction policy (revised)",
			Default:  "lru",
			SpecRef:  sp.SpecKey,
			Choices: []rooms.ChoiceOption{
				{ID: "lru", Label: "LRU"},
				{ID: "ttl", Label: "Fixed TTL"},
			},
		},
	})
	if err != nil || cpPost2.Message.ChoicePrompt.Version != 2 {
		t.Fatalf("choice_prompt v2 failed: res=%+v err=%v", cpPost2, err)
	}

	// Confirm v1 is marked Replaced: true and responding to v1 fails with ErrStaleChoiceVersion.
	if _, _, serr := svc.RespondChoicePrompt(ctx, p1, rooms.RespondChoiceInput{
		PromptKey:     "eviction-policy",
		PromptVersion: 1,
		Choice:        "lru",
		SenderRole:    "user",
	}); !errors.Is(serr, rooms.ErrStaleChoiceVersion) {
		t.Fatalf("expected ErrStaleChoiceVersion on superseded v1, got %v", serr)
	}

	// Agent-interpreted requires sourceMessageId from a @user message in the same thread.
	if _, _, aerr := svc.RespondChoicePrompt(ctx, p1, rooms.RespondChoiceInput{
		PromptKey:     "eviction-policy",
		PromptVersion: 2,
		Choice:        "ttl",
		SenderRole:    "planner",
	}); !errors.Is(aerr, rooms.ErrInvalid) {
		t.Fatalf("expected ErrInvalid when agent_interpreted omits sourceMessageId, got %v", aerr)
	}

	userProse, err := svc.PostMessage(ctx, p1, rooms.PostInput{
		ThreadKey:  th.ThreadKey,
		SenderRole: "user",
		Intent:     "inform",
		Body:       "Let's use fixed TTL for predictable invalidation.",
	})
	if err != nil {
		t.Fatalf("PostMessage user prose: %v", err)
	}
	_, decInterp, err := svc.RespondChoicePrompt(ctx, p1, rooms.RespondChoiceInput{
		PromptKey:       "eviction-policy",
		PromptVersion:   2,
		Choice:          "ttl",
		SenderRole:      "planner",
		SourceMessageID: userProse.Message.ID,
	})
	if err != nil {
		t.Fatalf("RespondChoicePrompt agent_interpreted: %v", err)
	}
	if decInterp.Provenance != "agent_interpreted" || decInterp.PromptVersion != 2 || decInterp.SourceMessageID == nil || *decInterp.SourceMessageID != userProse.Message.ID {
		t.Fatalf("unexpected interpreted decision: %+v", decInterp)
	}

	// @user directly overrides agent_interpreted decision.
	_, decDirect, err := svc.RespondChoicePrompt(ctx, p1, rooms.RespondChoiceInput{
		PromptKey:     "eviction-policy",
		PromptVersion: 2,
		Choice:        "lru",
		SenderRole:    "user",
	})
	if err != nil {
		t.Fatalf("RespondChoicePrompt direct override: %v", err)
	}
	if decDirect.Provenance != "direct" || decDirect.Choice != "lru" {
		t.Fatalf("unexpected direct decision: %+v", decDirect)
	}

	allDecs, err := b.ListSpecDecisions(ctx, p1)
	if err != nil || len(allDecs) < 2 {
		t.Fatalf("ListSpecDecisions: len=%d err=%v", len(allDecs), err)
	}
	var foundOverridden bool
	for _, dec := range allDecs {
		if dec.Number == decInterp.Number && dec.State == "overridden" && dec.SupersededBy != nil && *dec.SupersededBy == decDirect.Number {
			foundOverridden = true
		}
	}
	if !foundOverridden {
		t.Fatalf("expected DEC-%03d marked overridden with SupersededBy=%d, got %+v", decInterp.Number, decDirect.Number, allDecs)
	}

	// Agent cannot overwrite a direct @user decision on the same version.
	if _, _, oerr := svc.RespondChoicePrompt(ctx, p1, rooms.RespondChoiceInput{
		PromptKey:       "eviction-policy",
		PromptVersion:   2,
		Choice:          "ttl",
		SenderRole:      "planner",
		SourceMessageID: userProse.Message.ID,
	}); !errors.Is(oerr, rooms.ErrInvalid) {
		t.Fatalf("expected refusal overwriting direct @user decision, got %v", oerr)
	}
}
