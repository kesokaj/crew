package board_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// Which Done ticket a bug is in counts once another agent than its reporter confirmed it (9.4, 12.3;
// design 0.54, the operator's choice of report 9, option B): the analysis of the bug's fix ticket names
// the Done ticket the defect is in, or none, and only then is the bug an escaped defect of that ticket,
// with its signal. The operator's own word needs no one to confirm it.
func TestAnalysisConfirmsTheTicketABugIsIn(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.toDone(p, "SF-1")
	e.add(p, ready("Second"), board.OriginOperator)
	e.toDone(p, "SF-2")
	e.setWIP(p, "analysis", 10)
	e.clock.Add(time.Minute)
	decide := func(key string, bugIn *string) error {
		t.Helper()
		_, err := e.b.Decide(e.ctx, p, key, board.Decision{Choice: "approve", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./receipts"}},
			DocsReason: ptr("No page covers it."), BugIn: bugIn}, "lead · r_2")
		return err
	}
	bugOf := func(key string) *board.Bug {
		t.Helper()
		g, err := e.b.BugOf(e.ctx, e.ticket(p, key).ID)
		if err != nil || g == nil {
			t.Fatalf("the bug of %s: %+v %v", key, g, err)
		}
		return g
	}

	// A tester names SF-1: that is its word, which counts for nothing yet.
	bug, fix, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Receipts lose the cents", Severity: "high", Happens: "1.5 shows as 1",
		InTicket: "SF-1"}, board.BugFound{By: "tester"}, board.OriginAgent, "tester · r_1")
	if err != nil {
		t.Fatal(err)
	}
	if bug.InConfirmed || deref(bug.InTicket) != "SF-1" || len(e.signalsOf(p, "escaped_defect")) != 0 {
		t.Fatalf("a report alone: %+v, signals %v", bug, e.signalsOf(p, "escaped_defect"))
	}
	e.reconcile(p)
	// Analysis approves only once it says which ticket the defect is in: a Done ticket, never the fix
	// itself, or none.
	if err := decide(fix.Key, nil); kindOf(err) != board.Invalid || !strings.Contains(err.Error(), "bugIn") {
		t.Fatalf("an approval with no bugIn: %v", err)
	}
	pending := e.add(p, ready("Not done yet"), board.OriginOperator)
	for _, bad := range []string{fix.Key, pending.Key, "SF-99", "nothing"} {
		if err := decide(fix.Key, ptr(bad)); err == nil {
			t.Fatalf("bugIn %s was taken", bad)
		}
	}
	if g := bugOf(fix.Key); g.InConfirmed || deref(g.InTicket) != "SF-1" {
		t.Fatalf("a refused decision changed the bug: %+v", g)
	}
	// It finds the defect came with SF-2: the bug is in SF-2, confirmed, and SF-2 has the signal.
	if err := decide(fix.Key, ptr("SF-2")); err != nil {
		t.Fatal(err)
	}
	if g := bugOf(fix.Key); !g.InConfirmed || deref(g.InTicket) != "SF-2" {
		t.Fatalf("after the analysis: %+v", g)
	}
	esc := e.signalsOf(p, "escaped_defect")
	var on int64
	e.db.Server.QueryRow(e.ctx, `SELECT ticket FROM signals WHERE kind = 'escaped_defect'`).Scan(&on)
	if len(esc) != 1 || on != e.ticket(p, "SF-2").ID || esc[0]["foundBy"] != "tester" {
		t.Fatalf("escaped defects %v on %d", esc, on)
	}

	// Another one the analysis cannot place: confirmed as in none, which is no escaped defect.
	_, fix2, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "The total rounds down", Severity: "low", Happens: "x", InTicket: "SF-1"},
		board.BugFound{By: "reviewer"}, board.OriginAgent, "reviewer · r_3")
	if err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if err := decide(fix2.Key, ptr("none")); err != nil {
		t.Fatal(err)
	}
	if g := bugOf(fix2.Key); !g.InConfirmed || g.InTicket != nil || len(e.signalsOf(p, "escaped_defect")) != 1 {
		t.Fatalf("a bug in none: %+v", g)
	}

	// The operator's word is confirmed as it is given, with its signal; Analysis does not change it.
	_, fix3, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "The receipt has no date", Severity: "low", Happens: "x", InTicket: "SF-1"},
		board.BugFound{By: board.Operator}, board.OriginOperator, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if g := bugOf(fix3.Key); !g.InConfirmed || deref(g.InTicket) != "SF-1" || len(e.signalsOf(p, "escaped_defect")) != 2 {
		t.Fatalf("the operator's bug: %+v", g)
	}
	e.reconcile(p)
	if err := decide(fix3.Key, ptr("SF-2")); kindOf(err) != board.Invalid {
		t.Fatalf("Analysis changed the operator's word: %v", err)
	}
	if err := decide(fix3.Key, nil); err != nil {
		t.Fatal(err)
	}
	if g := bugOf(fix3.Key); !g.InConfirmed || deref(g.InTicket) != "SF-1" {
		t.Fatalf("after its analysis: %+v", g)
	}

	// A bug's ticket, once confirmed, is settled: when its fix ticket is cancelled and replaced, the
	// replacement's analysis is not asked again, may say the same, may not say another, and records no
	// second signal (review of a9532aa).
	again := e.add(p, ready("Replaces the fix"), board.OriginOperator)
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE bugs SET fix_ticket = $1 WHERE fix_ticket = $2`, again.ID, e.ticket(p, fix.Key).ID); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if err := decide(again.Key, ptr("SF-1")); kindOf(err) != board.Invalid {
		t.Fatalf("a settled ticket changed: %v", err)
	}
	if err := decide(again.Key, ptr("SF-2")); err != nil {
		t.Fatalf("the same answer again: %v", err)
	}
	if g := bugOf(again.Key); !g.InConfirmed || deref(g.InTicket) != "SF-2" || len(e.signalsOf(p, "escaped_defect")) != 2 {
		t.Fatalf("after its replacement's analysis: %+v, signals %v", g, e.signalsOf(p, "escaped_defect"))
	}

	// A ticket that fixes no bug has no bug to place.
	other := e.add(p, ready("Third"), board.OriginOperator)
	e.reconcile(p)
	if err := decide(other.Key, ptr("SF-1")); kindOf(err) != board.Invalid {
		t.Fatalf("bugIn on a ticket that fixes no bug: %v", err)
	}
}

// An agent's decision that is only a proposal (approvals.created_task: operator) confirms nothing: the
// operator may discard it, and a bug its decision placed would keep a signal and count (review of
// a9532aa). The bug's ticket stays its reporter's word.
func TestAProposalConfirmsNoBugsTicket(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.toDone(p, "SF-1")
	e.clock.Add(time.Minute)
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["approvals"].(settings.Doc)["created_task"] = "operator"
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	_, fix, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Receipts lose the cents", Severity: "high", Happens: "x", InTicket: "SF-1"},
		board.BugFound{By: "tester"}, board.OriginAgent, "tester · r_1")
	if err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	tk, err := e.b.Decide(e.ctx, p, fix.Key, board.Decision{Choice: "approve", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./receipts"}},
		DocsReason: ptr("No page covers it."), BugIn: ptr("SF-1")}, "lead · r_2")
	if err != nil || tk.Analysis.Status != board.StatusOperator {
		t.Fatalf("the proposal: %+v %v", tk, err)
	}
	g, _ := e.b.BugOf(e.ctx, tk.ID)
	if g.InConfirmed || len(e.signalsOf(p, "escaped_defect")) != 0 {
		t.Fatalf("a proposal confirmed the bug: %+v", g)
	}
}

// Only a lead's analysis places a bug (security review of a9532aa): when the lead created the fix
// ticket, a reviewer analyses it, and the reviewer is an agent that checks, whose model the escapes
// choose. Its approval needs no bugIn, may give none, and confirms nothing. And the ticket a bug is in
// was Done before the bug was reported: one finished after cannot hold its defect.
func TestOnlyALeadPlacesABugAndOnlyInATicketDoneBefore(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.toDone(p, "SF-1")
	e.setWIP(p, "analysis", 10)
	e.clock.Add(time.Minute)
	_, fix, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Receipts lose the cents", Severity: "high", Happens: "x", InTicket: "SF-1"},
		board.BugFound{By: "lead"}, board.OriginAgent, "lead · r_1")
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Add(time.Minute)
	e.add(p, ready("Finished after the report"), board.OriginOperator)
	e.toDone(p, "SF-3")
	e.reconcile(p)
	decide := func(by string, bugIn *string) error {
		_, err := e.b.Decide(e.ctx, p, fix.Key, board.Decision{Choice: "approve", Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./receipts"}},
			DocsReason: ptr("No page covers it."), BugIn: bugIn}, by)
		return err
	}
	if err := decide("reviewer · r_2", ptr("SF-1")); kindOf(err) != board.Invalid {
		t.Fatalf("a reviewer placed the bug: %v", err)
	}
	if err := decide("lead · r_3", ptr("SF-3")); kindOf(err) != board.Invalid || !strings.Contains(err.Error(), "after") {
		t.Fatalf("a ticket Done after the report: %v", err)
	}
	if err := decide("reviewer · r_2", nil); err != nil {
		t.Fatalf("a reviewer's approval with no bugIn: %v", err)
	}
	g, _ := e.b.BugOf(e.ctx, e.ticket(p, fix.Key).ID)
	if g.InConfirmed || deref(g.InTicket) != "SF-1" || len(e.signalsOf(p, "escaped_defect")) != 0 {
		t.Fatalf("after a reviewer's approval: %+v", g)
	}
}

// A notice of a new feature waits for its clock or for the operator, who may discard it: like a
// proposal it confirms nothing (security review round 2 of a9532aa).
func TestANoticeConfirmsNoBugsTicket(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.toDone(p, "SF-1")
	e.clock.Add(time.Minute)
	_, fix, err := e.b.ReportBug(e.ctx, p, board.NewBug{Title: "Receipts lose the cents", Severity: "high", Happens: "x", InTicket: "SF-1"},
		board.BugFound{By: "tester"}, board.OriginAgent, "tester · r_1")
	if err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	tk, err := e.b.Decide(e.ctx, p, fix.Key, board.Decision{Choice: "notice", Reason: "It adds what the plan has not.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./receipts"}}, DocsReason: ptr("No page covers it."), BugIn: ptr("SF-1")}, "lead · r_2")
	if err != nil || tk.Analysis.Status == board.StatusReady {
		t.Fatalf("the notice: %+v %v", tk, err)
	}
	g, _ := e.b.BugOf(e.ctx, tk.ID)
	if g.InConfirmed || len(e.signalsOf(p, "escaped_defect")) != 0 {
		t.Fatalf("a notice confirmed the bug: %+v", g)
	}
}
