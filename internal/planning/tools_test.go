package planning

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/mcp"
)

func toolKit(t *testing.T) (*Tools, *board.Board, *board.Project, *dbtest.DB) {
	t.Helper()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		}}
	p, err := b.CreateProject(context.Background(), board.NewProject{Name: "shop", Key: "SF", Plan: "## Goal\n\nA shop.\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	return &Tools{Pool: d.Server, Board: b}, b, p, d
}

func args(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestTheLeadFindsATicketBeforeItProposesAnother(t *testing.T) {
	tl, b, p, _ := toolKit(t)
	ctx := context.Background()
	lead := mcp.Caller{Run: "r1", Role: "lead", Project: p.ID}
	n := board.NewTicket{Title: "Password sign-in", Why: "Customers sign in.", What: "A form.", Goal: "Sign-in works.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./..."}}}
	if _, err := b.ProposeTickets(ctx, "shop", []board.NewTicket{n}, nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	got, err := tl.searchTickets(ctx, lead, args(map[string]string{"query": "SIGN form"}))
	if err != nil || len(got.([]ticketLine)) != 1 || got.([]ticketLine)[0].Key != "SF-1" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := tl.searchTickets(ctx, lead, args(map[string]string{"query": "sign payment"})); len(got.([]ticketLine)) != 0 {
		t.Fatalf("every word must match: %v", got)
	}
	if _, err := tl.searchTickets(ctx, lead, args(map[string]string{"query": " "})); err == nil {
		t.Fatal("an empty search")
	}
	x, err := tl.getTicket(ctx, lead, args(map[string]string{"key": "SF-1"}))
	if err != nil || x.(*board.Ticket).Title != "Password sign-in" {
		t.Fatalf("%v %v", x, err)
	}
	var me *mcp.Error
	if _, err := tl.getTicket(ctx, lead, args(map[string]string{"key": "SF-9"})); err == nil || !asMCP(err, &me) {
		t.Fatalf("a ticket that is not there: %v", err)
	}
	if l, _ := tl.listTickets(ctx, lead, args(map[string]string{"stage": "backlog"})); len(l.([]ticketLine)) != 0 {
		t.Fatalf("a filter by stage: %v", l)
	}
}

func asMCP(err error, target **mcp.Error) bool {
	e, ok := err.(*mcp.Error)
	if ok {
		*target = e
	}
	return ok
}

func TestTheBreakdownIsWrittenOnlyBeforeTheOK(t *testing.T) {
	tl, b, p, _ := toolKit(t)
	ctx := context.Background()
	lead := mcp.Caller{Run: "r1", Role: "lead", Project: p.ID}
	if _, err := tl.writeBreakdown(ctx, lead, args(map[string]string{"text": "## Parts"})); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.writeBreakdown(ctx, lead, args(map[string]string{"text": " "})); err == nil {
		t.Fatal("an empty breakdown")
	}
	if _, err := b.AddTicket(ctx, "shop", board.NewTicket{Title: "Mine", Why: "w", What: "w", Goal: "g"}, board.OriginOperator, board.Operator); err != nil {
		t.Fatal(err)
	}
	// A ticket has no budget (since 0.53): one the lead still gives a budget is refused, and its words say
	// which field (11.5).
	withBudget := map[string]any{"title": "Lockout", "kind": "feature", "why": "w", "what": "w", "goal": "g", "budgetUsd": 3,
		"checks": []any{map[string]any{"kind": "cmd", "spec": "go test ./..."}}}
	if _, err := tl.proposeTickets(ctx, lead, args(map[string]any{"tickets": []any{withBudget}})); err == nil || !strings.Contains(err.Error(), `unknown field "budgetUsd"`) {
		t.Fatalf("a ticket with a budget: %v", err)
	}
	if _, err := b.OK(ctx, "shop", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.writeBreakdown(ctx, lead, args(map[string]string{"text": "late"})); err == nil || !strings.Contains(err.Error(), "before the OK") {
		t.Fatalf("a breakdown after the OK: %v", err)
	}
	if _, err := tl.proposeTickets(ctx, lead, args(map[string]any{"tickets": []any{map[string]any{"title": "x"}}})); err == nil {
		t.Fatal("a ticket after the OK")
	}
}

func TestThePlanOfADraftThatBecameAProjectNoLongerChanges(t *testing.T) {
	tl, _, _, d := toolKit(t)
	ctx := context.Background()
	id := draftWith(t, d, "first")
	pa := mcp.Caller{Run: "", Role: "planner", Draft: id}
	if _, err := tl.writePlanDraft(ctx, pa, args(map[string]string{"plan": "## Goal\n\nOne."})); err != nil {
		t.Fatal(err)
	}
	if out, err := tl.getPlanDraft(ctx, pa, args(map[string]any{})); err != nil || out.(map[string]any)["version"] != 1 {
		t.Fatalf("%v %v", out, err)
	}
	for _, a := range []map[string]string{{"plan": ""}, {"plan": "x", "title": "a\nb"}, {"plan": strings.Repeat("x", MaxPlan+1)}} {
		if _, err := tl.writePlanDraft(ctx, pa, args(a)); err == nil {
			t.Errorf("%.40v was taken", a)
		}
	}
	d.Server.Exec(ctx, `UPDATE plan_drafts SET state = 'closed' WHERE id = $1`, id)
	if _, err := tl.writePlanDraft(ctx, pa, args(map[string]string{"plan": "Two."})); err == nil {
		t.Fatal("a closed draft's plan changed")
	}
}

func TestADraftKeepsAtMostItsVersions(t *testing.T) {
	tl, _, _, d := toolKit(t)
	ctx := context.Background()
	id := draftWith(t, d, "first")
	if _, err := d.Server.Exec(ctx, `INSERT INTO plan_draft_versions (draft, number, text) SELECT $1, n, 'v' FROM generate_series(1, $2) n`,
		id, MaxVersions); err != nil {
		t.Fatal(err)
	}
	_, err := tl.writePlanDraft(ctx, mcp.Caller{Role: "planner", Draft: id}, args(map[string]string{"plan": "one more"}))
	if err == nil || !strings.Contains(err.Error(), "as many as a draft keeps") {
		t.Fatalf("%v", err)
	}
}

// After the OK, only the run that breaks down the open amendment writes a breakdown and proposes
// tickets (7.13): not a lead that analyses a ticket, looks again, or writes the report (security
// review of M11).
func TestOnlyTheAmendmentsBreakdownRunProposes(t *testing.T) {
	tl, b, p, d := toolKit(t)
	ctx := context.Background()
	n := board.NewTicket{Title: "Sign-in", Why: "w", What: "w", Goal: "g", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./..."}}}
	if _, err := b.ProposeTickets(ctx, "shop", []board.NewTicket{n}, nil, nil, "lead · r1"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBreakdown(ctx, p, "## Sign-in", "lead · r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.OK(ctx, "shop", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddAmendment(ctx, "shop", "Add sign-out.", board.Operator); err != nil {
		t.Fatal(err)
	}
	subtask := func(kind string) int64 {
		var id int64
		if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state)
			VALUES ('project', $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE project = $1), $2, 'lead', 'running') RETURNING id`,
			p.ID, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	propose := args(map[string]any{"tickets": []any{map[string]any{"title": "Sign-out", "why": "w", "what": "w", "goal": "g",
		"checks": []any{map[string]any{"kind": "cmd", "spec": "test -f out"}}}}})
	for _, kind := range []string{"report", "look_again", "analysis"} {
		c := mcp.Caller{Run: "r2", Role: "lead", Project: p.ID, Subtask: subtask(kind)}
		if _, err := tl.proposeTickets(ctx, c, propose); err == nil || !strings.Contains(err.Error(), "only the run that breaks down amendment 1") {
			t.Fatalf("a %s run proposed: %v", kind, err)
		}
		if _, err := tl.writeBreakdown(ctx, c, args(map[string]string{"text": "x"})); err == nil {
			t.Fatalf("a %s run wrote the breakdown", kind)
		}
	}
	c := mcp.Caller{Run: "r3", Role: "lead", Project: p.ID, Subtask: subtask("breakdown")}
	if _, err := tl.proposeTickets(ctx, c, propose); err != nil {
		t.Fatalf("the amendment's breakdown run: %v", err)
	}
	if _, err := b.AddAmendment(ctx, "shop", "And more.", board.Operator); err == nil {
		t.Fatal("a second amendment while one is open")
	}
}
