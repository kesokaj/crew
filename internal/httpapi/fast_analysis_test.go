package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/harness"
)

// Ready plan and operator tickets whose command checks fail on main skip an LLM analysis run and
// advance directly to In progress after Crew's base check; tickets from an agent, a personal token,
// a conversation, or a chat, and tickets missing a field or check, still run an LLM analysis, with
// a reviewer analysing tickets created by the curator (7.6, R31, Safety Rules 2, 14, 17).
func TestReadyPlanAndOperatorTicketsFastPathAnalysisWhileCreatedOnesDoNot(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	ctx := context.Background()
	k.builder(func(kind, key string, spec harness.RunSpec, p string) bool {
		if kind == "analysis" {
			k.tool(spec.Run, "analyse_ticket", map[string]any{
				"choice":     "approve",
				"why":        "w",
				"what":       "w",
				"goal":       "g",
				"docsReason": "none",
				"checks":     []map[string]any{{"kind": "cmd", "spec": "test -f " + key + ".txt"}},
			})
			return true
		}
		if kind == "build" {
			k.push(spec.Run, spec.Branch, map[string]string{key + ".txt": key})
			k.tool(spec.Run, "add_note", map[string]any{"text": "Built " + key})
			k.tool(spec.Run, "request_move", map[string]any{})
			return true
		}
		return false
	})

	k.call("POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "## Goal\n\nSell.\n", "stages": ["review"],
		"repository": {"kind": "local"}}`, 201, nil)
	var doc map[string]any
	k.call("GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["sandbox"].(map[string]any)["setup"] = ""
	b, _ := json.Marshal(doc)
	k.call("PUT", "/api/projects/shop/settings", string(b), 200, nil)

	// 1. Ready plan ticket (added in Planning before OK): fast-paths Analysis with 0 LLM analysis subtasks.
	if _, err := k.b.AddTicket(ctx, "shop", board.NewTicket{
		Title:  "Ready plan ticket",
		Why:    "w",
		What:   "w",
		Goal:   "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f SF-1.txt"}},
	}, board.OriginPlan, "planner · 00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	k.call("POST", "/api/projects/shop/ok", "", 200, nil)

	// 2. Ready operator ticket (all fields + failing cmd check): fast-paths Analysis with 0 LLM analysis subtasks.
	if _, err := k.b.AddTicket(ctx, "shop", board.NewTicket{
		Title:  "Ready operator ticket",
		Why:    "w",
		What:   "w",
		Goal:   "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f SF-2.txt"}},
	}, board.OriginOperator, board.Operator); err != nil {
		t.Fatal(err)
	}
	// 3. Token ticket (even with Approved=true and all fields): MUST run an LLM analysis subtask.
	if _, err := k.b.AddTicket(ctx, "shop", board.NewTicket{
		Title:    "Token ticket",
		Why:      "w",
		What:     "w",
		Goal:     "g",
		Checks:   []board.CheckSpec{{Kind: "cmd", Spec: "test -f SF-3.txt"}},
		Approved: true,
	}, board.OriginToken, "token:laptop"); err != nil {
		t.Fatal(err)
	}
	// 4. Agent ticket created by curator: MUST run an LLM analysis subtask, routed to reviewer.
	if _, err := k.b.AddTicket(ctx, "shop", board.NewTicket{
		Title:  "Curator agent ticket",
		Why:    "w",
		What:   "w",
		Goal:   "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f SF-4.txt"}},
	}, board.OriginAgent, "curator · 00000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatal(err)
	}

	stop := k.run()
	defer stop()

	k.until("all 4 tickets pass Analysis", func() bool {
		return k.count(`SELECT count(*) FROM tickets WHERE analysis->>'status' = 'ready'`) == 4
	})

	// SF-1 and SF-2 were approved directly by Crew with 0 analysis subtasks.
	if n := k.count(`SELECT count(*) FROM tickets WHERE number IN (1, 2) AND analysis->>'decidedBy' = 'backend'`); n != 2 {
		t.Fatalf("expected SF-1 and SF-2 decidedBy backend, got %d", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number IN (1, 2) AND s.kind = 'analysis'`); n != 0 {
		t.Fatalf("expected 0 analysis subtasks for fast-pathed SF-1 and SF-2, got %d", n)
	}
	// SF-3 (token) was analysed by curator; SF-4 (created by curator) was analysed by reviewer.
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 3 AND s.kind = 'analysis' AND s.role = 'curator'`); n != 1 {
		t.Fatalf("expected SF-3 (token) to have 1 curator analysis subtask, got %d", n)
	}
	if n := k.count(`SELECT count(*) FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 4 AND s.kind = 'analysis' AND s.role = 'reviewer'`); n != 1 {
		t.Fatalf("expected SF-4 (created by curator) to have 1 reviewer analysis subtask, got %d", n)
	}
}
