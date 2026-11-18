package board_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

func TestTicketYAMLContractSyncAndForwardOnlyStage(t *testing.T) {
	e := newEnv(t)
	p := e.project("factory-yaml", "FY", "review", "test")

	t1, err := e.b.AddTicket(e.ctx, p.Name, board.NewTicket{
		Title:         "Bootstrap API",
		Why:           "Needed for M21",
		What:          "Add handlers",
		Goal:          "Routes respond 200",
		Kind:          "feature",
		SpecRef:       ".crew/specs/001-api.md",
		MaxTries:      4,
		AssignedAgent: "@builder",
		BudgetTokens:  120000,
		Checks:        []board.CheckSpec{{Kind: "cmd", Spec: "go test ./..."}},
	}, board.OriginOperator, board.Operator)
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}

	rawYAML, err := e.b.FormatTicketYAML(e.ctx, p, t1.Key)
	if err != nil {
		t.Fatalf("FormatTicketYAML: %v", err)
	}
	s := string(rawYAML)
	for _, want := range []string{
		"key: FY-1",
		"spec: .crew/specs/001-api.md",
		"max_tries: 4",
		"assigned_agent: '@builder'",
		"budget_tokens: 120000",
		"## Why",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("FormatTicketYAML missing %q in:\n%s", want, s)
		}
	}

	// Move FY-1 forward past backlog (e.g., to in_progress) via Move/Reconcile, then commit a YAML file on main
	// claiming stage: backlog, plus a brand-new FY-2 depending on FY-1.
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "FY-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	if got := e.stage(p, "FY-1"); got != settings.StageInProgress {
		t.Fatalf("expected FY-1 in in_progress before sync, got %s", got)
	}

	me := gitstore.Identity{Name: "Op", Email: "op@example.com"}
	overlay := gitstore.Overlay{
		".crew/tickets/FY-1.md": func([]byte) []byte {
			return []byte("---\nid: \"0001\"\nkey: FY-1\ntitle: Bootstrap API\nkind: feature\nstage: backlog\norigin: operator\ncreated_by: operator\nspec: .crew/specs/001-api-v2.md\nmax_tries: 5\nassigned_agent: '@reviewer'\nbudget_tokens: 240000\nmay_change:\n  - internal/httpapi/**\n---\n\n# FY-1: Bootstrap API\n")
		},
		".crew/tickets/FY-2.md": func([]byte) []byte {
			return []byte("---\nid: \"0002\"\nkey: FY-2\ntitle: Add YAML UI\nkind: docs\nstage: backlog\nspec: .crew/specs/001-api-v2.md\ndepends_on:\n  - FY-1\nmax_tries: 3\nassigned_agent: '@builder'\nbudget_tokens: 60000\nchecks:\n  - kind: cmd\n    spec: go test ./...\n---\n\n## Why\n\nExpose docs\n\n## What\n\nWrite docs\n\n## Goal\n\nDocs updated\n")
		},
	}
	if _, err := e.git.CommitOverlay(e.ctx, p.Name, overlay, "Sync .crew/tickets from main", me); err != nil {
		t.Fatalf("CommitOverlay: %v", err)
	}

	added, updated, err := e.b.SyncTicketsFromMain(e.ctx, p, board.Operator)
	if err != nil {
		t.Fatalf("SyncTicketsFromMain: %v", err)
	}
	if added != 1 || updated != 1 {
		t.Fatalf("SyncTicketsFromMain added=%d updated=%d, want 1, 1", added, updated)
	}

	after1 := e.ticket(p, "FY-1")
	if after1.Stage != settings.StageInProgress {
		t.Fatalf("Rule 3 violated: FY-1 stage moved backwards to %s", after1.Stage)
	}
	if after1.SpecRef != ".crew/specs/001-api-v2.md" || after1.MaxTries != 5 || after1.AssignedAgent != "@reviewer" || after1.BudgetTokens != 240000 {
		t.Fatalf("FY-1 metadata not synced: %+v", after1)
	}

	after2 := e.ticket(p, "FY-2")
	if after2.SpecRef != ".crew/specs/001-api-v2.md" || after2.Why != "Expose docs" || len(after2.Links) != 1 || after2.Links[0].To != "FY-1" {
		t.Fatalf("FY-2 synced incorrectly: %+v", after2)
	}

	// Non-executable kind (e.g. "spec") must be refused before entering Kanban.
	badOverlay := gitstore.Overlay{
		".crew/tickets/FY-3.md": func([]byte) []byte {
			return []byte("---\nid: \"0003\"\nkey: FY-3\ntitle: Pure Spec\nkind: spec\nstage: backlog\n---\n")
		},
	}
	if _, err := e.git.CommitOverlay(e.ctx, p.Name, badOverlay, "Add bad ticket kind", me); err != nil {
		t.Fatalf("CommitOverlay bad kind: %v", err)
	}
	if _, _, err := e.b.SyncTicketsFromMain(e.ctx, p, board.Operator); err == nil {
		t.Fatal("expected SyncTicketsFromMain to refuse non-executable ticket kind 'spec'")
	}
}
