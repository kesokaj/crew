package board_test

import (
	"fmt"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

// Only a priority the operator set is theirs: it alone orders the lead queue across projects (7.9,
// from 0.42). A priority an agent gives, in the breakdown or in Analysis, is the ticket's own.
func TestOnlyTheOperatorsPriorityIsMarkedAsTheirs(t *testing.T) {
	e := newEnv(t)
	p := e.project("prio", "PR")
	e.add(p, ready("plan"), board.OriginPlan)
	e.ok(p)
	urgent, high := 0, 1
	n := ready("agent")
	n.Priority = &urgent
	agent := e.add(p, n, board.OriginAgent)
	if agent.Priority != 0 || agent.OperatorPriority != nil {
		t.Fatalf("an agent's priority: %d, operator's %v", agent.Priority, agent.OperatorPriority)
	}
	n = ready("operator")
	n.Priority = &high
	op := e.add(p, n, board.OriginOperator)
	if op.OperatorPriority == nil || *op.OperatorPriority != 1 {
		t.Fatalf("the operator's own ticket: %v", op.OperatorPriority)
	}
	// An analysis that sets a priority leaves it an agent's.
	e.reconcile(p)
	e.approve(p, agent.Key, board.Decision{Priority: &high})
	if got := e.ticket(p, agent.Key); got.Priority != 1 || got.OperatorPriority != nil {
		t.Fatalf("after the analysis: %d, operator's %v", got.Priority, got.OperatorPriority)
	}
	// The operator's edit makes it theirs.
	edited, err := e.b.EditTicket(e.ctx, p, agent.Key, board.Edit{Priority: &urgent}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Priority != 0 || edited.OperatorPriority == nil || *edited.OperatorPriority != 0 {
		t.Fatalf("after the operator's edit: %d, operator's %v", edited.Priority, edited.OperatorPriority)
	}
}

// A ticket made through the Crew MCP is analysed like an agent's (11.7, 13.8): it cannot skip Review,
// and its may_change waits for the operator's yes. One a token with may answer made is approved by
// being added, so it is not discarded or given notice of, and still waits for the yes.
func TestATokensTicketIsAnalysedLikeAnAgents(t *testing.T) {
	e := newEnv(t)
	p := e.project("tok", "TK", "review", "test")
	e.add(p, ready("plan"), board.OriginPlan)
	e.ok(p)
	plain, err := e.b.AddTicket(e.ctx, p.Name, ready("plain"), board.OriginToken, "token laptop")
	if err != nil {
		t.Fatal(err)
	}
	n := ready("answered")
	n.Approved = true
	answered, err := e.b.AddTicket(e.ctx, p.Name, n, board.OriginToken, "token laptop")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Analysis.Status != board.StatusProposed || answered.Analysis.Status != board.StatusWaiting || !answered.Analysis.Approved {
		t.Fatalf("the statuses: %+v %+v", plain.Analysis, answered.Analysis)
	}
	for e.stage(p, answered.Key) != "analysis" || e.stage(p, plain.Key) != "analysis" {
		e.reconcile(p)
		e.b.Decide(e.ctx, p, "TK-1", board.Decision{Choice: "approve"}, "lead · r_1")
	}
	reason := "not needed"
	if _, err := e.b.Decide(e.ctx, p, plain.Key, board.Decision{Choice: "approve", Skips: []string{"review"}, SkipsReason: &reason}, "lead · r_1"); err == nil {
		t.Fatal("a token's ticket skipped Review")
	}
	if _, err := e.b.Decide(e.ctx, p, answered.Key, board.Decision{Choice: "discard", Reason: "no"}, "lead · r_1"); err == nil {
		t.Fatal("a ticket approved by being added was discarded")
	}
	got, err := e.b.Decide(e.ctx, p, answered.Key, board.Decision{Choice: "approve", MayChange: []string{"AGENTS.md"}}, "lead · r_1")
	if err != nil || !board.NeedsAgreement(got) {
		t.Fatalf("a token's ticket with may_change does not wait for the operator: %v", err)
	}
}

// A comment a personal token sent waits for the next run on its ticket, and is answered like the
// operator's: Waiting, Unresolved, and Reply take it (11.6, 11.7). Another author's text is not one.
func TestATokensCommentReachesTheRunAndIsAnswered(t *testing.T) {
	e := newEnv(t)
	p := e.project("com", "CM")
	tk := e.add(p, ready("one"), board.OriginPlan)
	e.ok(p)
	c, err := e.b.CommentBy(e.ctx, p, tk.Key, "Use the short name.", board.TokenAuthor("laptop"), "token laptop")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != "token laptop" {
		t.Fatalf("the comment's author: %q", c.Author)
	}
	waiting, err := e.b.Waiting(e.ctx, p.ID, tk.ID)
	if err != nil || len(waiting) != 1 || waiting[0].ID != c.ID {
		t.Fatalf("waiting: %+v %v", waiting, err)
	}
	open, err := e.b.Unresolved(e.ctx, e.ticket(p, tk.Key))
	if err != nil || len(open) != 1 || open[0].Author != "token laptop" {
		t.Fatalf("unresolved: %+v %v", open, err)
	}
	// A run given it answers it.
	var sub int64
	if err := e.db.Server.QueryRow(e.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role) VALUES ('ticket', $1, $2, 1, 'build', 'builder')
		RETURNING id`, p.ID, tk.ID).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	run := "00000000-0000-4000-8000-0000000000e9"
	e.db.Server.Exec(e.ctx, `INSERT INTO conversations (id, purpose, cli) VALUES ($1, 'run', 'agy')`, run)
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, conversation)
		VALUES ($1, $2, 1, 'working', 'agy', 't', 't', $1)`, run, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE records SET delivered_run = $2 WHERE id = $1`, c.ID, run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Reply(e.ctx, p, tk.ID, c.ID, "Done.", "builder", run, sub); err != nil {
		t.Fatalf("the answer: %v", err)
	}
	if open, _ = e.b.Unresolved(e.ctx, e.ticket(p, tk.Key)); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	// What an agent wrote is no comment that waits for a run.
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO records (project, ticket, kind, author, body) VALUES ($1, $2, 'comment', 'builder', 'x')`, p.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	if waiting, _ = e.b.Waiting(e.ctx, p.ID, tk.ID); len(waiting) != 0 {
		t.Fatalf("an agent's text waits as a comment: %+v", waiting)
	}
}

// A token has at most MaxTokenUnresolved comments that wait for an answer on a ticket; the operator's
// own are not counted with them, and an answered one makes room (security review of M9).
func TestATokensCommentsThatWaitAreCapped(t *testing.T) {
	e := newEnv(t)
	p := e.project("cap", "CP")
	tk := e.add(p, ready("one"), board.OriginPlan)
	e.ok(p)
	var first board.Record
	for i := 0; i < board.MaxTokenUnresolved; i++ {
		c, err := e.b.CommentBy(e.ctx, p, tk.Key, fmt.Sprintf("Note %d.", i), board.TokenAuthor("laptop"), "token laptop")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = c
		}
	}
	if _, err := e.b.CommentBy(e.ctx, p, tk.Key, "One more.", board.TokenAuthor("laptop"), "token laptop"); kindOf(err) != board.Refused {
		t.Fatalf("the token's comment over the cap: %v", err)
	}
	if _, err := e.b.CommentBy(e.ctx, p, tk.Key, "Mine.", board.Operator, board.Operator); err != nil {
		t.Fatalf("the operator's comment: %v", err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE records SET resolved = true WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.CommentBy(e.ctx, p, tk.Key, "Now there is room.", board.TokenAuthor("laptop"), "token laptop"); err != nil {
		t.Fatalf("after an answer: %v", err)
	}
}
