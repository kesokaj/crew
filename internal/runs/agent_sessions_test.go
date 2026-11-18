package runs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/models/modelstest"
)

func TestConcurrentPeerSessionsSteerTurnStreamAndAbort(t *testing.T) {
	k := newKit(t)
	defer k.d.Close()
	k.f.AllowSteer = true
	modelstest.Recorded(t, k.db.Server)

	ctx := context.Background()
	projID := newUUID()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'factory', 'FC', 'local', 'running')`, projID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	k.play(
		harness.Script{Recording: agyRun, Delay: 300 * time.Millisecond},
		harness.Script{Recording: agyRun, Delay: 300 * time.Millisecond},
		harness.Script{Recording: agyRun},
		harness.Script{Recording: agySonnetRun},
	)

	builderSess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TaskKind:  "chat_action",
		Role:      "@builder",
		Title:     "Peer builder",
		Prompt:    "Implement module A",
	})
	if err != nil {
		t.Fatalf("SpawnPeerSession(@builder): %v", err)
	}
	researchSess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TaskKind:  "research",
		Role:      "@researcher",
		Title:     "Peer researcher",
		Prompt:    "Analyze module A",
	})
	if err != nil {
		t.Fatalf("SpawnPeerSession(@researcher): %v", err)
	}

	// Both peer sessions run concurrently on Gemini.
	k.wait(builderSess.ID, "working")
	k.wait(researchSess.ID, "working")
	if got := cli.Family(builderSess.Model); got != "gemini" {
		t.Fatalf("@builder resolved family %q (model %s), want gemini", got, builderSess.Model)
	}
	if got := cli.Family(researchSess.Model); got != "gemini" {
		t.Fatalf("@researcher resolved family %q (model %s), want gemini", got, researchSess.Model)
	}

	// Untracked peer compute without ticket, taskId, or taskKind is refused (R217).
	if _, uerr := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		Role:      "@researcher",
		Prompt:    "Should refuse untracked work",
	}); !errors.Is(uerr, ErrUntrackedWork) {
		t.Fatalf("expected ErrUntrackedWork without ticket/task, got %v", uerr)
	}

	// Attempting to spawn @reviewer without a ticket/eval is refused under checkguard (Rule 15).
	if _, rerr := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TaskKind:  "research",
		Role:      "@reviewer",
		Prompt:    "Should refuse without ticket",
	}); !errors.Is(rerr, ErrCheckFamily) {
		t.Fatalf("expected ErrCheckFamily without ticket, got %v", rerr)
	}

	active, err := k.d.ListPeerSessions(ctx, projID, true)
	if err != nil || len(active) != 2 {
		t.Fatalf("ListPeerSessions active: len=%d err=%v", len(active), err)
	}

	// Mid-turn live steer against the working @builder session succeeds.
	if err := k.d.SteerPeerSession(ctx, builderSess.ID, "Add edge-case test"); err != nil {
		t.Fatalf("SteerPeerSession: %v", err)
	}

	k.wait(builderSess.ID, "idle")
	k.wait(researchSess.ID, "idle")

	evs, err := k.d.StreamPeerSession(ctx, builderSess.ID, 0, 50)
	if err != nil || len(evs) == 0 {
		t.Fatalf("StreamPeerSession: len=%d err=%v", len(evs), err)
	}

	// Follow-up turn continues on @builder.
	turned, err := k.d.TurnPeerSession(ctx, builderSess.ID, "Summarize changes")
	if err != nil {
		t.Fatalf("TurnPeerSession: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, gerr := k.d.GetPeerSession(ctx, turned.ID)
		if gerr == nil && cur.Turn >= 2 && cur.State == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected turn 2 idle on @builder, got %+v err=%v", cur, gerr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Spawning @reviewer on a ticket selects Anthropic Claude under Rule 15.
	var ticketID int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, why, what, goal, stage, origin, created_by, position)
		VALUES ($1, 1, 'T1', 'feature', 'w', 'w', 'g', 'backlog', 'operator', 'operator', 1000) RETURNING id`, projID).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	reviewerSess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TicketID:  ticketID,
		Role:      "@reviewer",
		Title:     "Peer reviewer",
		Prompt:    "Review FC-1",
	})
	if err != nil {
		t.Fatalf("SpawnPeerSession(@reviewer with ticket): %v", err)
	}
	if got := cli.Family(reviewerSess.Model); got != "claude" {
		t.Fatalf("@reviewer resolved family %q (model %s), want claude", got, reviewerSess.Model)
	}
	k.wait(reviewerSess.ID, "idle")

	// Abort all peer sessions cleanly.
	for _, id := range []string{builderSess.ID, researchSess.ID, reviewerSess.ID} {
		if err := k.d.AbortPeerSession(ctx, id); err != nil {
			t.Fatalf("AbortPeerSession(%s): %v", id, err)
		}
		k.wait(id, "ended")
	}
}

func TestNewFactoryPeerRolesResolveToGemini(t *testing.T) {
	k := newKit(t)
	defer k.d.Close()
	modelstest.Recorded(t, k.db.Server)

	ctx := context.Background()
	projID := newUUID()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'rolesproj', 'RP', 'local', 'running')`, projID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	for _, role := range []string{"@coordinator", "@researcher", "@dreamer"} {
		taskKind := strings.TrimPrefix(role, "@")
		if taskKind == "coordinator" || taskKind == "researcher" || taskKind == "dream" {
			// direct mapping for factory task kinds below
		}
		switch role {
		case "@researcher":
			taskKind = "research"
		case "@dreamer":
			taskKind = "dream"
		default:
			taskKind = "coordinator"
		}
		sess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
			ProjectID: projID,
			TaskKind:  taskKind,
			Role:      role,
			Prompt:    "Check Rule 15 model resolution for " + role,
		})
		if err != nil {
			t.Fatalf("SpawnPeerSession(%s): %v", role, err)
		}
		if got := cli.Family(sess.Model); got != "gemini" || !strings.HasPrefix(sess.Role, "@") {
			t.Fatalf("role %s resolved %+v (family %q), want gemini", role, sess, got)
		}
		k.wait(sess.ID, "idle", "ended")
		_ = k.d.AbortPeerSession(ctx, sess.ID)
		k.wait(sess.ID, "ended")
	}
}

func TestSecurityAndCustomWorkflowPeerRolesAndPreComputeTask(t *testing.T) {
	k := newKit(t)
	defer k.d.Close()
	modelstest.Recorded(t, k.db.Server)

	ctx := context.Background()
	projID := newUUID()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'secproj', 'SP', 'local', 'running')`, projID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	rawWF := "---\nversion: 1\nharness: agy\nmax_tries: 3\nmax_agent_hops: 3\nagents:\n  - slug: perf-auditor\n    promptFile: .crew/agents/perf-auditor.md\n    modelBracket: flash\n    readOnly: true\n    skills: [cache-hints]\n---\n# Factory workflow\n"
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO workflow_defs (project_id, main_sha, raw_markdown, frontmatter, harness, max_tries, max_agent_hops, feature_specs)
		VALUES ($1, 'deadbeef', $2, '{}'::jsonb, 'agy', 3, 3, 'autonomous')`, projID, rawWF); err != nil {
		t.Fatalf("insert workflow_defs: %v", err)
	}

	k.play(
		harness.Script{Recording: agyRun, Delay: 200 * time.Millisecond},
		harness.Script{Recording: agyRun},
	)

	// 1. Built-in @security spawns on Gemini as read-only and mints its FactoryTask before compute.
	secSess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TaskKind:  "chat_action",
		Role:      "@security",
		Title:     "Security audit peer",
		Prompt:    "Audit auth boundaries",
	})
	if err != nil {
		t.Fatalf("SpawnPeerSession(@security): %v", err)
	}
	if !secSess.ReadOnly || secSess.Role != "@security" || secSess.TaskID == "" || cli.Family(secSess.Model) != "gemini" {
		t.Fatalf("unexpected @security session: %+v (family=%s)", secSess, cli.Family(secSess.Model))
	}

	// FindActivePeerSession locates the working @security session while it runs.
	k.wait(secSess.ID, "working")
	foundActive, err := k.d.FindActivePeerSession(ctx, projID, "security", 0)
	if err != nil || foundActive == nil || foundActive.ID != secSess.ID {
		t.Fatalf("FindActivePeerSession(@security) = %+v, err=%v", foundActive, err)
	}

	// Verify factory_tasks row was bound to the spawned session.
	var boundSess string
	if err := k.db.Server.QueryRow(ctx, `SELECT session_id::text FROM factory_tasks WHERE id = $1::uuid`, secSess.TaskID).Scan(&boundSess); err != nil {
		t.Fatalf("query factory_tasks binding: %v", err)
	}
	if boundSess != secSess.ID {
		t.Fatalf("expected factory_tasks.session_id=%s, got %s", secSess.ID, boundSess)
	}

	// 2. Custom WORKFLOW.md specialist @perf-auditor spawns on Gemini Flash under Rule 15.
	customSess, err := k.d.SpawnPeerSession(ctx, SpawnPeerRequest{
		ProjectID: projID,
		TaskKind:  "coordinator",
		Role:      "@perf-auditor",
		Title:     "Performance auditor",
		Prompt:    "Check query latencies",
	})
	if err != nil {
		t.Fatalf("SpawnPeerSession(@perf-auditor): %v", err)
	}
	if !customSess.ReadOnly || customSess.Role != "@perf-auditor" || customSess.TaskID == "" || cli.Family(customSess.Model) != "gemini" || !strings.Contains(customSess.Model, "flash") {
		t.Fatalf("expected read-only Gemini flash @perf-auditor session, got %+v", customSess)
	}

	for _, id := range []string{secSess.ID, customSess.ID} {
		k.wait(id, "idle", "ended")
		_ = k.d.AbortPeerSession(ctx, id)
		k.wait(id, "ended")
	}
}
