package board_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

func TestM23FoundationGateAutonomousSpecDecisionsAndEscalation(t *testing.T) {
	e := newEnv(t)
	p := e.project("studio-m23", "MS", "review", "test")

	// 1. Project Creation seeds .crew/specs/000-foundation.md (SPEC-000) in waiting_project_ok.
	specs, err := e.b.ListSpecs(e.ctx, p)
	if err != nil {
		t.Fatalf("ListSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0].SpecKey != "SPEC-000" || specs[0].Tier != board.TierFoundation || specs[0].State != "waiting_project_ok" {
		t.Fatalf("expected seeded SPEC-000 waiting_project_ok, got %+v", specs)
	}
	blob, err := e.git.ReadBlob(e.ctx, p.Name, "main", ".crew/specs/000-foundation.md")
	if err != nil || !strings.Contains(blob.Content, "state: waiting_project_ok") {
		t.Fatalf("expected 000-foundation.md on main waiting Project OK, got blob=%+v err=%v", blob, err)
	}

	// Autonomous/agent attempts to modify or decide 000-foundation.md must be refused (Rule 12).
	if _, err := e.b.WriteFeatureSpec(e.ctx, p, board.EnsureSpecInput{Ref: "SPEC-000", Title: "Hijack foundation", BodyMarkdown: "nope"}); err == nil {
		t.Fatal("expected WriteFeatureSpec on SPEC-000 to be refused")
	}
	if _, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef: "SPEC-000", Choice: "Self-approve", Rationale: "bypass",
	}); err == nil {
		t.Fatal("expected DecideSpec on SPEC-000 to be refused")
	}

	// Give Project OK (add one Planning ticket first as required by OK), verify SPEC-000 becomes approved.
	if _, err := e.b.AddTicket(e.ctx, p.ID, board.NewTicket{
		Title: "Bootstrap service skeleton",
		Kind:  "chore",
		Why:   "Initial foundation slice",
		What:  "Seed main module",
		Goal:  "Builds cleanly",
		Checks: []board.CheckSpec{
			{Kind: "cmd", Spec: "test -f Makefile"},
		},
	}, board.OriginPlan, "planner"); err != nil {
		t.Fatalf("AddTicket planning: %v", err)
	}
	if _, err := e.b.OK(e.ctx, p.ID, board.Operator); err != nil {
		t.Fatalf("OK: %v", err)
	}
	foundAfterOK, err := e.b.GetSpec(e.ctx, p, "SPEC-000")
	if err != nil {
		t.Fatalf("GetSpec SPEC-000 after OK: %v", err)
	}
	if foundAfterOK.State != "approved" || foundAfterOK.DecidedBy != board.Operator || foundAfterOK.ApprovedAt == nil {
		t.Fatalf("expected SPEC-000 approved by operator, got %+v", foundAfterOK.Spec)
	}
	blobAfterOK, err := e.git.ReadBlob(e.ctx, p.Name, "main", ".crew/specs/000-foundation.md")
	if err != nil || !strings.Contains(blobAfterOK.Content, "state: approved") {
		t.Fatalf("expected 000-foundation.md on main updated to approved, got %+v err=%v", blobAfterOK, err)
	}

	// 2. Peer -> @planner Feature Spec outside Backlog (R209, R217) + autonomous DEC-001 emission.
	sp1, q1, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{
		Title:        "Structured error envelope",
		AskingRole:   "builder",
		Question:     "Should API errors use AIP-193 shape or flat errors?",
		ContextNote:  "Observed inconsistent JSON errors in handlers.",
		TouchedPaths: []string{"internal/httpapi/errors.go"},
	})
	if err != nil {
		t.Fatalf("AskPlanner: %v", err)
	}
	if sp1.SpecKey != "SPEC-001" || sp1.State != "deliberating" || sp1.FactoryTaskID == nil || q1.HopCount != 1 {
		t.Fatalf("unexpected AskPlanner result: spec=%+v q=%+v", sp1, q1)
	}
	// Verify tracked spec factory_task exists and Kanban Backlog still has only MS-1 (no spec in Backlog).
	tasks, err := e.b.ListFactoryTasks(e.ctx, p.ID, "spec", "")
	if err != nil || len(tasks) != 1 || tasks[0].ID != *sp1.FactoryTaskID {
		t.Fatalf("expected 1 tracked spec factory_task, got %+v err=%v", tasks, err)
	}
	ticketsBefore, _ := e.b.Tickets(e.ctx, p)
	if len(ticketsBefore) != 1 {
		t.Fatalf("expected only MS-1 in Backlog before DecideSpec, got %d", len(ticketsBefore))
	}

	// Enforce max_agent_hops (default 3) on the feature thread.
	if _, q2, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{SpecRef: sp1.SpecKey, AskingRole: "reviewer", Question: "Follow-up hop 2"}); err != nil || q2.HopCount != 2 {
		t.Fatalf("AskPlanner hop 2: q=%+v err=%v", q2, err)
	}
	if _, q3, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{SpecRef: sp1.SpecKey, AskingRole: "builder", Question: "Follow-up hop 3"}); err != nil || q3.HopCount != 3 {
		t.Fatalf("AskPlanner hop 3: q=%+v err=%v", q3, err)
	}
	if _, _, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{SpecRef: sp1.SpecKey, AskingRole: "builder", Question: "Hop 4 must refuse"}); kindOf(err) != board.Refused {
		t.Fatalf("expected max_agent_hops refusal on hop 4, got %v", err)
	}

	// Autonomous DecideSpec within 000-foundation.md and 13.8 guardrails emits Backlog YAML tickets + DEC-001.
	res1, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef:       sp1.SpecKey,
		DecidedByRole: "planner",
		BodyMarkdown:  "# SPEC-001 — Structured error envelope\n\nUse standard AIP-193 JSON errors.",
		Choice:        "Use AIP-193 error envelope",
		Alternatives:  []string{"Flat {error: string}", "RFC 7807"},
		Rationale:     "Aligns with json-api-style and existing server responses within 000-foundation.md.",
		TouchedPaths:  []string{"internal/httpapi/errors.go"},
		Tickets: []board.NewTicket{
			{
				Title:  "Implement AIP-193 error helper",
				Kind:   "feature",
				Why:    "Per SPEC-001",
				What:   "Add writeAPIError helper in internal/httpapi/errors.go",
				Goal:   "Uniform error payloads",
				Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -f internal/httpapi/errors.go"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("DecideSpec autonomous: %v", err)
	}
	if res1.Escalated || res1.Spec.State != "approved" || res1.Spec.ApprovalMode != "autonomous" ||
		res1.Decision.Key != "DEC-001" || res1.Decision.State != "approved" || len(res1.EmittedTickets) != 1 {
		t.Fatalf("unexpected autonomous DecideSpec result: %+v", res1)
	}
	emitted := res1.EmittedTickets[0]
	if emitted.Stage != settings.StageBacklog || emitted.SpecRef != sp1.Path {
		t.Fatalf("expected emitted ticket in Backlog linked to %s, got stage=%s specRef=%s", sp1.Path, emitted.Stage, emitted.SpecRef)
	}
	for _, mainPath := range []string{sp1.Path, ".crew/decisions/DEC-001.md", ".crew/tickets/" + emitted.Key + ".md"} {
		if _, err := e.git.ReadBlob(e.ctx, p.Name, "main", mainPath); err != nil {
			t.Fatalf("expected %s mirrored onto main: %v", mainPath, err)
		}
	}
	doneSpecTask, err := e.b.GetFactoryTask(e.ctx, p.ID, tasks[0].Key)
	if err != nil || doneSpecTask.State != "done" {
		t.Fatalf("expected spec factory_task done, got %+v err=%v", doneSpecTask, err)
	}

	// 3. Guardrail escalation triggers: (a) 13.8 protected files, (b) altersFoundation, (c) manual mode.
	sp138, _, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{
		Title:      "Update workflow and skills",
		AskingRole: "coordinator",
		Question:   "Add custom stage to WORKFLOW.md?",
	})
	if err != nil {
		t.Fatalf("AskPlanner sp138: %v", err)
	}
	res138, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef:      sp138.SpecKey,
		Choice:       "Add verify stage to WORKFLOW.md",
		Rationale:    "Needs workflow orchestration tweak",
		TouchedPaths: []string{"WORKFLOW.md", ".agents/skills/ops/SKILL.md"},
		Tickets: []board.NewTicket{
			{
				Title:  "Edit WORKFLOW.md stage",
				Kind:   "chore",
				Why:    "Enable ops stage",
				What:   "Update WORKFLOW.md",
				Goal:   "WORKFLOW.md updated",
				Checks: []board.CheckSpec{{Kind: "cmd", Spec: "grep -q ops WORKFLOW.md"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("DecideSpec 13.8 escalation: %v", err)
	}
	if !res138.Escalated || res138.Spec.State != "escalated" || res138.Decision.State != "pending_operator" ||
		len(res138.EmittedTickets) != 0 || !strings.Contains(res138.Spec.EscalationReason, "13.8") {
		t.Fatalf("expected 13.8 escalation with 0 emitted tickets, got %+v", res138)
	}

	// (b) altersFoundation: true -> escalates.
	spFound, _, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{
		Title:      "Expand scope to mobile app",
		AskingRole: "builder",
		Question:   "Should we also ship an iOS client?",
	})
	if err != nil {
		t.Fatalf("AskPlanner spFound: %v", err)
	}
	resFound, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef:          spFound.SpecKey,
		Choice:           "Add iOS client",
		Rationale:        "Expands non-goals in 000-foundation.md",
		AltersFoundation: true,
		Tickets: []board.NewTicket{
			{
				Title:  "Init iOS client",
				Kind:   "feature",
				Why:    "Scope change",
				What:   "Add ios/",
				Goal:   "iOS scaffold",
				Checks: []board.CheckSpec{{Kind: "cmd", Spec: "test -d ios"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("DecideSpec altersFoundation: %v", err)
	}
	if !resFound.Escalated || !strings.Contains(resFound.Spec.EscalationReason, "000-foundation.md") || len(resFound.EmittedTickets) != 0 {
		t.Fatalf("expected altersFoundation escalation, got %+v", resFound)
	}

	// Operator approves sp138 -> ticket finally enters Backlog.
	opRes, err := e.b.OperatorDecideSpec(e.ctx, p, sp138.SpecKey, "approve", "Approved by operator", board.Operator)
	if err != nil {
		t.Fatalf("OperatorDecideSpec approve: %v", err)
	}
	if opRes.Spec.State != "approved" || opRes.Spec.DecidedBy != board.Operator || len(opRes.EmittedTickets) != 1 {
		t.Fatalf("expected operator approval to emit 1 ticket, got %+v", opRes)
	}

	// (c) approvals.feature_specs: manual -> escalates even clean changes.
	doc, err := e.b.Settings(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	doc["approvals"].(settings.Doc)["feature_specs"] = "manual"
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatalf("SaveSettings manual: %v", err)
	}
	spMan, _, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{
		Title:      "Add CSV export helper",
		AskingRole: "builder",
		Question:   "Include UTF-8 BOM in CSV exports?",
	})
	if err != nil {
		t.Fatalf("AskPlanner manual: %v", err)
	}
	resMan, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef:      spMan.SpecKey,
		Choice:       "Omit UTF-8 BOM",
		Rationale:    "Standard RFC 4180 CSV",
		TouchedPaths: []string{"internal/export/csv.go"},
	})
	if err != nil {
		t.Fatalf("DecideSpec manual: %v", err)
	}
	if !resMan.Escalated || !strings.Contains(resMan.Spec.EscalationReason, "manual") {
		t.Fatalf("expected manual mode escalation, got %+v", resMan)
	}

	// (d) Path traversal (../) in TouchedPaths escalates under 13.8 even when autonomous, and
	// autonomously emitted spec tickets keep Review mandatory and require MayChange agreement.
	spTrap, _, err := e.b.AskPlanner(e.ctx, p, board.AskPlannerInput{
		Title: "Traversal guard check", AskingRole: "builder", Question: "Touch ../outside?",
	})
	if err != nil {
		t.Fatalf("AskPlanner spTrap: %v", err)
	}
	// Restore autonomous mode to verify traversal guardrail specifically.
	doc["approvals"].(settings.Doc)["feature_specs"] = "autonomous"
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatalf("SaveSettings autonomous: %v", err)
	}
	resTrap, err := e.b.DecideSpec(e.ctx, p, board.DecideSpecInput{
		SpecRef: spTrap.SpecKey, Choice: "No", Rationale: "Test traversal", TouchedPaths: []string{"../AGENTS.md"},
	})
	if err != nil || !resTrap.Escalated || !strings.Contains(resTrap.Spec.EscalationReason, "13.8") {
		t.Fatalf("expected ../ traversal 13.8 escalation, res=%+v err=%v", resTrap, err)
	}
	if !board.NeedsAgreement(&board.Ticket{Origin: board.OriginPlan, CreatedBy: "planner", SpecRef: sp1.Path, Analysis: board.Analysis{MayChange: []string{"AGENTS.md"}}}) {
		t.Fatal("expected NeedsAgreement=true for autonomously emitted spec ticket with MayChange")
	}
	if board.NeedsAgreement(&board.Ticket{Origin: board.OriginPlan, CreatedBy: board.Operator, SpecRef: sp1.Path, Analysis: board.Analysis{MayChange: []string{"AGENTS.md"}}}) {
		t.Fatal("expected NeedsAgreement=false for operator-approved spec ticket")
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'analysis' WHERE id = $1`, emitted.ID); err != nil {
		t.Fatal(err)
	}
	skipsReason := "skip review attempt"
	if _, err := e.b.Decide(e.ctx, p, emitted.Key, board.Decision{
		Choice: "approve", Skips: []string{"review"}, SkipsReason: &skipsReason,
	}, "curator"); err == nil {
		t.Fatal("expected autonomously emitted spec ticket to forbid skipping Review")
	}

	// Clean up gitstore usage check so compiler uses gitstore import if needed; verify ReadBlob worked above.
	_ = gitstore.ErrNoRev
}
