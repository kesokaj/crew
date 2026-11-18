package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/rooms"
)

func TestM24UnifiedProjectRoomFeatureThreadsAndPeerChatter(t *testing.T) {
	k := newTicketKit(t)
	ctx := context.Background()

	p, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "shop",
		Key:    "SF",
		Goal:   "Ship an isolated store with shared peer chat.",
		Plan:   "## Goal\n\nVerify M24 room chat and hops.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	var roomsResp struct {
		Rooms []rooms.Room `json:"rooms"`
	}
	k.call("GET", "/api/rooms", "", http.StatusOK, &roomsResp)
	foundShop := false
	for _, rm := range roomsResp.Rooms {
		if rm.Channel == "#shop" {
			foundShop = true
			break
		}
	}
	if !foundShop {
		t.Fatalf("expected canonical #shop room, got %+v", roomsResp.Rooms)
	}

	var roomView rooms.RoomView
	k.call("GET", "/api/projects/shop/room", "", http.StatusOK, &roomView)
	if roomView.Channel != "#shop" || roomView.MaxAgentHops != 3 {
		t.Fatalf("unexpected roomView: %+v", roomView)
	}

	// Create a feat/<NNN>-<slug> thread rooted in #shop.
	var createdThread struct {
		Room   rooms.Room   `json:"room"`
		Thread rooms.Thread `json:"thread"`
	}
	k.call("POST", "/api/projects/shop/room/threads",
		`{"threadKey":"feat/001-cache","title":"Cache invalidation"}`,
		http.StatusCreated, &createdThread)
	if createdThread.Thread.ThreadKey != "feat/001-cache" {
		t.Fatalf("unexpected created thread: %+v", createdThread.Thread)
	}

	// FakeHarness handler for @researcher verifying fenced prompt, read_chat fencing, ReadOnly session, and post_chat reply.
	var mu sync.Mutex
	var peerPrompt string
	var peerBranch string
	var peerReadText string
	var peerReplied bool

	k.custom = func(kind string, spec harness.RunSpec, prompt string) (harness.Script, bool) {
		if strings.Contains(prompt, "# Project Chat Turn:") {
			mu.Lock()
			peerPrompt = prompt
			peerBranch = spec.Branch
			mu.Unlock()
			txt, ok := k.tool(spec.Run, "read_chat", map[string]any{"threadKey": "feat/001-cache"})
			if !ok {
				t.Errorf("read_chat via MCP failed: %s", txt)
			}
			mu.Lock()
			peerReadText = txt
			mu.Unlock()
			postTxt, postOK := k.tool(spec.Run, "post_chat", map[string]any{
				"threadKey":  "feat/001-cache",
				"body":       "Verified cache_test.go; LRU eviction cleanly fits internal/cache.",
				"actionable": false,
			})
			if !postOK {
				t.Errorf("post_chat reply via MCP failed: %s", postTxt)
			}
			mu.Lock()
			peerReplied = postOK
			mu.Unlock()
			return harness.Script{Lines: answerLines(spec, "Posted grounded findings to feat/001-cache.")}, true
		}
		return harness.Script{}, false
	}

	defer k.run()()

	// Actionable @researcher mention mints a tracked chat_action Tasks API item + read-only ACP session (R217).
	var postRes rooms.PostResult
	k.call("POST", "/api/projects/shop/room/messages", `{
		"threadKey": "feat/001-cache",
		"senderRole": "builder",
		"body": "@researcher Please check cache_test.go eviction boundaries."
	}`, http.StatusCreated, &postRes)
	if postRes.Message.HopCount != 1 || postRes.SpawnedTask == nil || postRes.SpawnedTask.Kind != "chat_action" || postRes.SpawnedSessionID == "" {
		t.Fatalf("expected tracked chat_action task and ACP session, got %+v", postRes)
	}

	waitM24 := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s", what)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}

	var threadView rooms.ThreadView
	waitM24("@researcher post_chat reply in feat/001-cache", func() bool {
		mu.Lock()
		replied := peerReplied
		mu.Unlock()
		if !replied {
			return false
		}
		k.call("GET", "/api/projects/shop/room/threads/feat/001-cache", "", http.StatusOK, &threadView)
		return len(threadView.Messages) >= 2 && threadView.Messages[len(threadView.Messages)-1].SenderHandle == "@researcher"
	})

	mu.Lock()
	gotPrompt, gotBranch, gotRead := peerPrompt, peerBranch, peerReadText
	mu.Unlock()
	if gotBranch != "main" && gotBranch != "" {
		t.Fatalf("expected read-only chat_action peer session on main or empty branch, got branch %q", gotBranch)
	}
	if !strings.Contains(gotPrompt, "<crew_data_fence") {
		t.Fatalf("expected peer spawn prompt to wrap chatter in <crew_data_fence>, got %q", gotPrompt)
	}
	if !strings.Contains(gotRead, "crew_data_fence") || !strings.Contains(gotRead, "BEGIN Peer chat message") {
		t.Fatalf("expected read_chat MCP output to carry <crew_data_fence>, got %s", gotRead)
	}

	// Verify GET /api/agent-sessions shows ReadOnly == true and R217 task linkage.
	var sessResp struct {
		Sessions []struct {
			ID       string `json:"id"`
			Role     string `json:"role"`
			ReadOnly bool   `json:"readOnly"`
			TaskID   string `json:"taskId"`
		} `json:"sessions"`
	}
	k.call("GET", "/api/agents/sessions?project=shop", "", http.StatusOK, &sessResp)
	foundSess := false
	for _, s := range sessResp.Sessions {
		if s.ID == postRes.SpawnedSessionID {
			foundSess = true
			if !s.ReadOnly || s.Role != "@researcher" || s.TaskID != postRes.SpawnedTask.ID {
				t.Fatalf("expected readOnly @researcher session tied to %s, got %+v", postRes.SpawnedTask.ID, s)
			}
		}
	}
	if !foundSess {
		t.Fatalf("spawned session %s not found in /agent-sessions", postRes.SpawnedSessionID)
	}

	// Confirm the thread lists 2 messages (hop 1 from @builder, hop 2 from @researcher) and hop 3 reaches max_agent_hops=3.
	if threadView.CurrentHops != 2 || len(threadView.Messages) != 2 {
		t.Fatalf("expected 2 messages and CurrentHops=2, got %+v", threadView)
	}
	k.call("POST", "/api/projects/shop/room/messages", `{
		"threadKey": "feat/001-cache",
		"senderRole": "tester",
		"body": "Hop 3 confirmation in thread.",
		"actionable": false
	}`, http.StatusCreated, nil)

	// 4th consecutive agent message must return 409 MAX_AGENT_HOPS until @user resets.
	var hopErr struct {
		Error struct {
			Code   int    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	k.call("POST", "/api/projects/shop/room/messages", `{
		"threadKey": "feat/001-cache",
		"senderRole": "builder",
		"body": "Hop 4 must stop.",
		"actionable": false
	}`, http.StatusConflict, &hopErr)
	if hopErr.Error.Status != "MAX_AGENT_HOPS" {
		t.Fatalf("expected MAX_AGENT_HOPS conflict, got %+v", hopErr)
	}

	// Reading via /room/messages?forAgent=true wraps agent posts in <crew_data_fence>.
	var agentRead struct {
		Messages []rooms.Message `json:"messages"`
	}
	k.call("GET", "/api/projects/shop/room/messages?threadKey=feat/001-cache&forAgent=true", "", http.StatusOK, &agentRead)
	if len(agentRead.Messages) != 3 || !strings.Contains(agentRead.Messages[0].Content, "<crew_data_fence") {
		t.Fatalf("expected 3 fenced messages from forAgent=true read, got %+v", agentRead.Messages)
	}

	// @user resets hop counter so agent replies resume at hop 1; top-level #shop channel also accepts posts.
	var userMsg, resumedMsg, rootMsg rooms.PostResult
	k.call("POST", "/api/projects/shop/room/messages", `{
		"threadKey": "feat/001-cache",
		"senderRole": "user",
		"body": "Continue with tests.",
		"actionable": false
	}`, http.StatusCreated, &userMsg)
	if userMsg.Message.HopCount != 0 {
		t.Fatalf("expected user hop 0, got %+v", userMsg.Message)
	}
	k.call("POST", "/api/projects/shop/room/messages", `{
		"threadKey": "feat/001-cache",
		"senderRole": "builder",
		"body": "Resumed after @user reset.",
		"actionable": false
	}`, http.StatusCreated, &resumedMsg)
	if resumedMsg.Message.HopCount != 1 {
		t.Fatalf("expected resumed hop 1, got %+v", resumedMsg.Message)
	}
	k.call("POST", "/api/projects/shop/room/messages", `{
		"senderRole": "user",
		"body": "Welcome to #shop"
	}`, http.StatusCreated, &rootMsg)
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
	if rootMsg.Message.ThreadID != nil || rootMsg.Message.HopCount != 0 {
		t.Fatalf("expected top-level #shop message without threadId, got %+v", rootMsg.Message)
	}
}

func TestM24ChoicePromptsMessageByIDAndCrossProjectBoundary(t *testing.T) {
	k := newTicketKit(t)
	ctx := context.Background()

	shop, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "shop",
		Key:    "SF",
		Goal:   "Test choice prompt HTTP routes and cross-project boundary.",
		Plan:   "## Goal\n\nVerify M24 HTTP hardening.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject shop: %v", err)
	}
	if _, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "other",
		Key:    "OT",
		Goal:   "Sibling factory.",
		Plan:   "## Goal\n\nSibling isolation.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator); err != nil {
		t.Fatalf("CreateProject other: %v", err)
	}
	if _, err := k.b.WriteFeatureSpec(ctx, shop, board.EnsureSpecInput{
		Title:        "Storage Strategy",
		BodyMarkdown: "## Goal\n\nPick storage tier.\n",
		CreatedBy:    "planner",
	}); err != nil {
		t.Fatalf("WriteFeatureSpec: %v", err)
	}

	// Post keyed choice_prompt card.
	var cpRes rooms.PostResult
	k.call("POST", "/api/projects/shop/room/messages", `{
		"senderRole": "planner",
		"kind": "choice_prompt",
		"intent": "question",
		"body": "Which storage driver should we use?",
		"choicePrompt": {
			"id": "storage-driver",
			"question": "Pick storage driver",
			"default": "localfs",
			"specRef": "SPEC-001",
			"choices": [
				{"id": "localfs", "label": "Local FS"},
				{"id": "gcs", "label": "Google Cloud Storage"}
			]
		},
		"actionable": false
	}`, http.StatusCreated, &cpRes)
	if cpRes.Message.ChoicePrompt == nil || cpRes.Message.ChoicePrompt.Version != 1 {
		t.Fatalf("expected v1 choice_prompt, got %+v", cpRes.Message)
	}

	// Answer directly via HTTP POST /room/choices/{key}/respond.
	var respOut struct {
		Decision *board.SpecDecision `json:"decision"`
		Post     *rooms.PostResult   `json:"post"`
	}
	k.call("POST", "/api/projects/shop/room/choices/storage-driver/respond", `{
		"promptVersion": 1,
		"choice": "gcs",
		"senderRole": "user"
	}`, http.StatusCreated, &respOut)
	if respOut.Decision == nil || respOut.Decision.Choice != "gcs" || respOut.Decision.Provenance != "direct" {
		t.Fatalf("unexpected direct decision response: %+v", respOut)
	}

	// Fetch single message by ?messageId=<id>&forAgent=true and confirm full body + fenced content.
	var single struct {
		Message *rooms.Message `json:"message"`
	}
	k.call("GET", "/api/projects/shop/room/messages?messageId="+strconvFormat(cpRes.Message.ID)+"&forAgent=true", "", http.StatusOK, &single)
	if single.Message == nil || single.Message.ID != cpRes.Message.ID || !strings.Contains(single.Message.Content, "<crew_data_fence") {
		t.Fatalf("unexpected ?messageId response: %+v", single.Message)
	}

	// Cross-project ?messageId lookup returns 403 PERMISSION_DENIED.
	var errResp struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	k.call("GET", "/api/projects/other/room/messages?messageId="+strconvFormat(cpRes.Message.ID)+"&forAgent=true", "", http.StatusForbidden, &errResp)
	if errResp.Error.Status != "PERMISSION_DENIED" {
		t.Fatalf("expected PERMISSION_DENIED on cross-project messageId, got %+v", errResp)
	}
}

func strconvFormat(n int64) string {
	return strconv.FormatInt(n, 10)
}
