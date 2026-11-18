package board_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

func TestM22FactoryTasksLedgerAndCoordinatorRankBacklog(t *testing.T) {
	e := newEnv(t)
	p := e.project("factory-m22", "FM", "review", "test")

	// 1. All 5 off-board Tasks API kinds get sequential keys and YAML contracts, never enter Backlog.
	var createdKeys []string
	for _, tc := range []struct {
		kind  string
		title string
		role  string
	}{
		{"spec", "Draft 001 auth spec", "@planner"},
		{"research", "Investigate build failure", "@researcher"},
		{"dream", "Synthesize retry skill", "@dreamer"},
		{"coordinator", "Sequence Backlog DAG", "@coordinator"},
		{"chat_action", "Check dependency compatibility", "@researcher"},
	} {
		ft, err := e.b.CreateFactoryTask(e.ctx, p.ID, board.NewFactoryTask{
			Kind:  tc.kind,
			Title: tc.title,
			Role:  tc.role,
		})
		if err != nil {
			t.Fatalf("CreateFactoryTask(%s): %v", tc.kind, err)
		}
		if !strings.HasPrefix(ft.Key, "FM-T") || !strings.Contains(ft.YAMLContract, "kind: "+tc.kind) {
			t.Fatalf("unexpected factory task: %+v", ft)
		}
		createdKeys = append(createdKeys, ft.Key)
	}
	if len(createdKeys) != 5 || createdKeys[4] != "FM-T5" {
		t.Fatalf("expected FM-T1..FM-T5, got %v", createdKeys)
	}

	// Completing an off-board task records state, summary, and artifact path.
	doneTask, err := e.b.CompleteFactoryTask(e.ctx, p.ID, "FM-T2", "done", "Root cause found", ".crew/tickets/FM-1-research.md", board.Operator)
	if err != nil {
		t.Fatalf("CompleteFactoryTask: %v", err)
	}
	if doneTask.State != "done" || doneTask.ArtifactPath != ".crew/tickets/FM-1-research.md" {
		t.Fatalf("unexpected completed task: %+v", doneTask)
	}

	// Code work kinds must never enter off-board factory_tasks.
	for _, codeKind := range []string{"feature", "fix", "chore", "docs", "revert"} {
		if _, err := e.b.CreateFactoryTask(e.ctx, p.ID, board.NewFactoryTask{
			Kind:  codeKind,
			Title: "Code task should be rejected",
		}); err == nil {
			t.Fatalf("expected CreateFactoryTask(%s) to be rejected", codeKind)
		}
	}

	// Confirm off-board tasks created zero Kanban tickets in Backlog.
	if boardTickets, err := e.b.Tickets(e.ctx, p); err != nil || len(boardTickets) != 0 {
		t.Fatalf("off-board tasks leaked into Kanban tickets: len=%d err=%v", len(boardTickets), err)
	}

	// 2. @coordinator RankBacklog deterministically orders Backlog by DAG readiness, spec order,
	// transitive unblocks, and operator priority without mutating stages.
	addBacklogTicket := func(title, kind, spec string, deps []string, prio *int) *board.Ticket {
		t.Helper()
		tk, err := e.b.AddTicket(e.ctx, p.Name, board.NewTicket{
			Title:     title,
			Kind:      kind,
			Why:       "Required",
			What:      "Build",
			Goal:      "Pass",
			SpecRef:   spec,
			DependsOn: deps,
			Priority:  prio,
			Checks:    []board.CheckSpec{{Kind: "cmd", Spec: "go test ./..."}},
		}, board.OriginOperator, board.Operator)
		if err != nil {
			t.Fatalf("AddTicket(%s): %v", title, err)
		}
		return tk
	}
	urgent := 0
	t3 := addBacklogTicket("Root foundational change", "feature", ".crew/specs/002-storage.md", nil, nil)
	t1 := addBacklogTicket("Child 1 blocked by root", "feature", ".crew/specs/001-core.md", []string{t3.Key}, nil)
	t2 := addBacklogTicket("Child 2 blocked by root", "feature", ".crew/specs/001-core.md", []string{t3.Key}, nil)
	t4 := addBacklogTicket("Spec-001 ready helper", "chore", ".crew/specs/001-core.md", nil, nil)
	t5 := addBacklogTicket("Operator urgent ready fix", "fix", ".crew/specs/003-ui.md", nil, &urgent)

	// Move project from Planning -> Backlog cleanly so all 5 tickets enter Backlog.
	e.ok(p)

	rank, err := e.b.RankBacklog(e.ctx, p.ID)
	if err != nil {
		t.Fatalf("RankBacklog: %v", err)
	}
	if len(rank.Ready) != 3 || len(rank.Blocked) != 2 {
		t.Fatalf("unexpected ready/blocked split: ready=%v blocked=%v", rank.Ready, rank.Blocked)
	}
	// Expected order:
	// 1. FM-5 (operator urgent priority = 0, ready)
	// 2. FM-4 (ready, spec 001-core.md < spec 002-storage.md)
	// 3. FM-3 (ready, unblocks 2 downstream tickets)
	// 4/5. FM-1, FM-2 (blocked on FM-3)
	wantOrder := []string{t5.Key, t4.Key, t3.Key, t1.Key, t2.Key}
	if len(rank.Ordered) != len(wantOrder) {
		t.Fatalf("ordered len=%d, want %d", len(rank.Ordered), len(wantOrder))
	}
	for i, want := range wantOrder {
		if rank.Ordered[i].Key != want {
			t.Fatalf("rank[%d] = %s, want %s (full: %+v)", i, rank.Ordered[i].Key, want, rank.Ordered)
		}
	}
	for _, k := range wantOrder {
		if got := e.stage(p, k); got != settings.StageBacklog {
			t.Fatalf("RankBacklog mutated stage of %s to %s", k, got)
		}
	}

	// Unified ledger keeps both Kanban tickets and off-board factory tasks apart and accountable.
	ledger, err := e.b.TasksLedger(e.ctx, p.ID)
	if err != nil {
		t.Fatalf("TasksLedger: %v", err)
	}
	if len(ledger.KanbanTickets) != 5 || len(ledger.FactoryTasks) != 5 {
		t.Fatalf("unexpected ledger counts: kanban=%d factory=%d", len(ledger.KanbanTickets), len(ledger.FactoryTasks))
	}
}
