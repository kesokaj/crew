package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
)

func TestM22TasksGateCoordinatorAndResearchCircuitBreaker(t *testing.T) {
	k := newTicketKit(t)
	ctx := context.Background()

	p, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "factory",
		Key:    "FC",
		Plan:   "## Goal\n\nVerify M22 Factory Execution\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := k.b.Remember(ctx, p, board.NewEntry{
		Kind:  "convention",
		Title: "Auth sentinel requirement",
		Body:  "Auth checks require auth_ready.txt before leaving In progress.",
	}, board.MemorySource{}, board.Operator); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// 1. Zero untracked agent compute through Tasks API (R217).
	var untrackedErr struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","role":"@researcher","prompt":"Should refuse bare untracked"}`,
		http.StatusConflict, &untrackedErr)
	if untrackedErr.Error.Status != "UNTRACKED_WORK" {
		t.Fatalf("expected UNTRACKED_WORK status, got %+v", untrackedErr)
	}
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"research","untracked":true,"role":"@researcher","prompt":"Should refuse untracked=true"}`,
		http.StatusConflict, nil)
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"feature","role":"@builder","prompt":"Code belongs on Kanban"}`,
		http.StatusConflict, nil)

	k.custom = func(_ string, spec harness.RunSpec, _ string) (harness.Script, bool) {
		if strings.HasPrefix(spec.Prompt, "Peer:") {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}, true
		}
		return harness.Script{}, false
	}
	// Valid off-board taskKind mints a tracked factory_task and links the session without touching Backlog.
	var trackedSess runs.PeerSession
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"research","role":"@researcher","title":"Initial spec probe","prompt":"Peer: probe spec"}`,
		http.StatusCreated, &trackedSess)
	if trackedSess.TaskID == "" || trackedSess.TaskKey != "FC-T1" {
		t.Fatalf("expected bound task FC-T1 on peer session, got %+v", trackedSess)
	}
	if curTickets, err := k.b.Tickets(ctx, p); err != nil || len(curTickets) != 0 {
		t.Fatalf("expected 0 Kanban tickets after off-board research session, got %d err=%v", len(curTickets), err)
	}

	waitM22 := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	waitM22("tracked peer session idle", func() bool {
		var cur runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+trackedSess.ID, "", http.StatusOK, &cur)
		return cur.State == "idle"
	})
	_ = k.driver.AbortPeerSession(ctx, trackedSess.ID)
	waitM22("tracked peer session ended", func() bool {
		var cur runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+trackedSess.ID, "", http.StatusOK, &cur)
		return cur.State == "ended"
	})

	// 2. @coordinator dispatch & Global Queue integration (R211).
	addT := func(title, spec string, maxTries int, assigned, checkFile string) *board.Ticket {
		t.Helper()
		tk, err := k.b.AddTicket(ctx, p.Name, board.NewTicket{
			Title:         title,
			Why:           "Needed for auth factory test",
			What:          "Write " + checkFile,
			Goal:          checkFile + " exists",
			Kind:          "feature",
			SpecRef:       spec,
			MaxTries:      maxTries,
			AssignedAgent: assigned,
			BudgetTokens:  60000,
			Checks:        []board.CheckSpec{{Kind: "cmd", Spec: "test -f " + checkFile}},
		}, board.OriginOperator, board.Operator)
		if err != nil {
			t.Fatalf("AddTicket(%s): %v", title, err)
		}
		return tk
	}
	t1 := addT("Implement auth middleware", ".crew/specs/001-auth.md", 2, "@builder", "auth_ready.txt")
	t2 := addT("Document auth endpoints", ".crew/specs/001-auth.md", 3, "@builder", "auth_docs.txt")
	t3 := addT("Seed core error codes", ".crew/specs/002-errors.md", 3, "@builder", "errors.txt")

	if _, err := k.b.OK(ctx, p.Name, board.Operator); err != nil {
		t.Fatalf("OK: %v", err)
	}
	// Make FC-1 and FC-2 depend on FC-3 so @coordinator elevates FC-3 ahead of FC-1/FC-2.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $3, 'depends_on'), ($2, $3, 'depends_on')`,
		t1.ID, t2.ID, t3.ID); err != nil {
		t.Fatalf("insert depends_on: %v", err)
	}

	var dispatchResp struct {
		Rank    board.CoordinatorRankResult `json:"rank"`
		Session *runs.PeerSession           `json:"session"`
	}
	k.call("POST", "/api/projects/factory/coordinator/dispatch", `{}`, http.StatusOK, &dispatchResp)
	if len(dispatchResp.Rank.Ready) != 1 || dispatchResp.Rank.Ready[0] != "FC-3" || len(dispatchResp.Rank.Blocked) != 2 {
		t.Fatalf("unexpected @coordinator rank: %+v", dispatchResp.Rank)
	}

	var gq planning.GlobalQueueSnapshot
	k.call("GET", "/api/queue/global", "", http.StatusOK, &gq)
	if gq.MaxConcurrentRuns <= 0 || len(gq.ProjectHeads) == 0 || gq.ProjectHeads[0].NextTicket != "FC-3" {
		t.Fatalf("unexpected GlobalQueueSnapshot: %+v", gq)
	}

	// 3. max_tries circuit breaker -> clean-context @researcher (R213).
	// Unblock FC-1 and hold FC-2/FC-3 so only FC-1 drives through In progress -> max_tries=2 -> @researcher.
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET hold = true WHERE id IN ($1, $2)`, t2.ID, t3.ID); err != nil {
		t.Fatalf("hold t2/t3: %v", err)
	}
	if _, err := k.d.Server.Exec(ctx, `DELETE FROM ticket_links WHERE from_ticket = $1`, t1.ID); err != nil {
		t.Fatalf("unlink FC-1: %v", err)
	}
	if err := k.b.Reconcile(ctx, p.Name); err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}
	docsReason := "none"
	if _, err := k.b.Decide(ctx, p, t1.Key, board.Decision{Choice: "approve", DocsReason: &docsReason}, "curator · r_test"); err != nil {
		t.Fatalf("Decide FC-1: %v", err)
	}
	if err := k.b.Reconcile(ctx, p.Name); err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}

	const transcriptNoise = "FAILED_BUILDER_TRANSCRIPT_NOISE_DO_NOT_LEAK_99182"
	var mu sync.Mutex
	var builderAttempt int
	var builderRunID, builderConvID, researchRunID string
	k.custom = func(kind string, spec harness.RunSpec, prompt string) (harness.Script, bool) {
		switch {
		case (kind == "build" || kind == "build-again") && strings.Contains(spec.Branch, "FC-1"):
			var conv string
			_ = k.d.Server.QueryRow(ctx, `SELECT conversation::text FROM runs WHERE id = $1::uuid`, spec.Run).Scan(&conv)
			mu.Lock()
			builderAttempt++
			attempt := builderAttempt
			builderRunID = spec.Run
			builderConvID = conv
			mu.Unlock()
			commitBranch := spec.Branch
			if commitBranch == "" {
				commitBranch = "task/" + t1.Key
			}
			pushM22Branch(t, k, p.Name, spec.Run, commitBranch, map[string]string{
				"partial.txt": fmt.Sprintf("incomplete attempt %d\n", attempt),
			})
			wipRef := "refs/crew/wip/" + spec.Run
			_, _ = k.d.Server.Exec(ctx, `UPDATE runs SET wip_ref = $2 WHERE id = $1::uuid`, spec.Run, wipRef)
			k.tool(spec.Run, "add_note", map[string]any{"text": "Attempt failed without auth_ready.txt: " + transcriptNoise})
			k.tool(spec.Run, "request_move", map[string]any{})
			return harness.Script{Lines: answerLines(spec, "Builder transcript: "+transcriptNoise)}, true

		case strings.Contains(prompt, "# Research: diagnose a ticket that hit max_tries") && strings.Contains(prompt, t1.Key):
			mu.Lock()
			researchRunID = spec.Run
			mu.Unlock()
			k.tool(spec.Run, "write_research_brief", map[string]any{
				"diagnosis":         "FC-1 command check failed because auth_ready.txt was not committed on the branch.",
				"recommendedAction": "retry_builder",
			})
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}, true
		}
		return harness.Script{}, false
	}

	defer k.run()()
	waitDeadline := time.Now().Add(15 * time.Second)
	for {
		if k.count(`SELECT count(*) FROM research_briefs WHERE ticket_id = $1`, t1.ID) > 0 &&
			k.count(`SELECT count(*) FROM factory_tasks WHERE ticket_id = $1 AND kind = 'research' AND state = 'done'`, t1.ID) > 0 {
			break
		}
		if time.Now().After(waitDeadline) {
			mu.Lock()
			att := builderAttempt
			mu.Unlock()
			var triesUsed int
			var stage string
			var stuck *string
			_ = k.d.Server.QueryRow(ctx, `SELECT stage, tries_used, stuck FROM tickets WHERE id = $1`, t1.ID).Scan(&stage, &triesUsed, &stuck)
			t.Fatalf("timeout waiting for @researcher brief: stage=%s triesUsed=%d stuck=%v briefs=%d tasks_done=%d subtasks=%d runs=%d attempts=%d",
				stage, triesUsed, stuck,
				k.count(`SELECT count(*) FROM research_briefs WHERE ticket_id = $1`, t1.ID),
				k.count(`SELECT count(*) FROM factory_tasks WHERE ticket_id = $1 AND kind = 'research' AND state = 'done'`, t1.ID),
				k.count(`SELECT count(*) FROM subtasks WHERE ticket = $1`, t1.ID),
				k.count(`SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.ticket = $1`, t1.ID),
				att)
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	bAttempts, bRunID, bConvID, rRunID := builderAttempt, builderRunID, builderConvID, researchRunID
	mu.Unlock()

	if bAttempts != 2 {
		t.Fatalf("expected exactly 2 builder attempts before @researcher circuit breaker, got %d", bAttempts)
	}

	// Verify tries_used hit max_tries (2), builder ended preserving WIP, and @researcher used a fresh Gemini conversation.
	tkAfter, err := k.b.Ticket(ctx, p, t1.Key)
	if err != nil {
		t.Fatalf("reload FC-1: %v", err)
	}
	if tkAfter.TriesUsed != 2 {
		t.Fatalf("expected FC-1 triesUsed == 2, got %d", tkAfter.TriesUsed)
	}
	if bRunID == "" || rRunID == "" {
		t.Fatalf("expected both builder (%q) and research (%q) runs", bRunID, rRunID)
	}
	var bState, bWIP, rModel, rConv string
	if err := k.d.Server.QueryRow(ctx, `SELECT state, coalesce(wip_ref, '') FROM runs WHERE id = $1::uuid`, bRunID).Scan(&bState, &bWIP); err != nil {
		t.Fatalf("query builder run: %v", err)
	}
	if bState != "ended" || bWIP == "" {
		t.Fatalf("expected builder ended with wip_ref preserved, got state=%q wip=%q", bState, bWIP)
	}
	if err := k.d.Server.QueryRow(ctx, `SELECT coalesce(model, ''), conversation::text FROM runs WHERE id = $1::uuid`, rRunID).Scan(&rModel, &rConv); err != nil {
		t.Fatalf("query research run: %v", err)
	}
	if cli.Family(rModel) != "gemini" {
		t.Fatalf("@researcher resolved to %q (%s), want gemini (Rule 15)", rModel, cli.Family(rModel))
	}
	if rConv == "" || rConv == bConvID {
		t.Fatalf("@researcher conversation %q must differ from failed builder conversation %q", rConv, bConvID)
	}
	if k.count(`SELECT count(*) FROM turns WHERE conversation = $1::uuid AND text LIKE '%' || $2 || '%'`, bConvID, transcriptNoise) == 0 {
		t.Fatal("expected failed builder conversation turns to record transcriptNoise")
	}

	// Verify clean-context prompt isolation: includes ticket YAML, failing check, branch diff, memory, and zero noise.
	fullResearchPrompt, _, err := runs.PromptOf(ctx, k.d.Server, rRunID)
	if err != nil {
		t.Fatalf("PromptOf(@researcher): %v", err)
	}
	if strings.Contains(fullResearchPrompt, transcriptNoise) {
		t.Fatalf("clean-context violation: builder transcript noise leaked into @researcher prompt:\n%s", fullResearchPrompt)
	}
	for _, mustContain := range []string{
		".crew/specs/001-auth.md",
		"auth_ready.txt is missing",
		"partial.txt",
		"Auth sentinel requirement",
	} {
		if !strings.Contains(fullResearchPrompt, mustContain) {
			t.Fatalf("@researcher prompt missing %q:\n%s", mustContain, fullResearchPrompt)
		}
	}

	// Verify HTTP research endpoint returns the brief and .crew/tickets/FC-1-research.md was mirrored on main.
	var briefsResp struct {
		Briefs []board.ResearchBrief `json:"briefs"`
	}
	k.call("GET", "/api/projects/factory/tasks/FC-1/research", "", http.StatusOK, &briefsResp)
	if len(briefsResp.Briefs) != 1 || briefsResp.Briefs[0].WIPRef != bWIP || briefsResp.Briefs[0].RecommendedAction != "retry_builder" {
		t.Fatalf("unexpected research briefs response: %+v", briefsResp.Briefs)
	}
	if _, err := k.git.ReadBlob(ctx, p.Name, "main", ".crew/tickets/FC-1-research.md"); err != nil {
		t.Fatalf("expected .crew/tickets/FC-1-research.md committed on main: %v", err)
	}

	// Verify Tasks Ledger separates Kanban tickets from off-board factory tasks cleanly.
	var ledger board.TasksLedgerView
	k.call("GET", "/api/projects/factory/tasks-ledger", "", http.StatusOK, &ledger)
	if len(ledger.KanbanTickets) != 3 || len(ledger.FactoryTasks) < 2 {
		t.Fatalf("unexpected tasks ledger: kanban=%d factory=%d", len(ledger.KanbanTickets), len(ledger.FactoryTasks))
	}
}

func pushM22Branch(t *testing.T, k *ticketKit, repoName, runID, branch string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(gitstore.CleanEnv(),
			"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@example.com",
			"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("clone", "-q", k.git.Quarantine.Path(repoName), ".")
	old := runGit("rev-parse", "HEAD")
	if runGit("ls-remote", "origin", "refs/heads/"+branch) != "" {
		runGit("fetch", "-q", "origin", branch)
		runGit("checkout", "-q", "-B", branch, "FETCH_HEAD")
		old = runGit("rev-parse", "HEAD")
	} else {
		runGit("checkout", "-q", "-b", branch)
	}
	for f, body := range files {
		dst := filepath.Join(work, f)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.WriteFile(dst, []byte(body), 0o644)
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "WIP branch update")
	runGit("push", "-q", "origin", branch)
	head := runGit("rev-parse", "HEAD")
	k.driver.RecordPush(gitendpoint.Access{Repo: repoName, Run: runID, Who: "run " + runID}, "refs/heads/"+branch, old, head)
}
