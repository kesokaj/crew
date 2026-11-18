package board

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Withdrawal is a ticket of the breakdown the lead takes back when it revises, with the reason.
type Withdrawal struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Revision is a ticket of the breakdown the lead changes in place when it revises: its number, and
// the ticket as it should be. Its number and the tickets that depend on it stay.
type Revision struct {
	Key string `json:"key"`
	NewTicket
}

// The most tickets one call of the breakdown proposes, and the most a project's breakdown holds in
// all, withdrawn ones counted.
const (
	maxProposed  = 60
	maxBreakdown = 300
)

// ProposeTickets adds the tickets of the breakdown in Planning, and takes back the ones withdrawn,
// all in one transaction (7.2, step 4). Only before the OK. A withdrawn ticket must be one the lead
// proposed and still in Planning; it is closed as discarded with the reason, and its history stays.
func (b *Board) ProposeTickets(ctx context.Context, ref string, add []NewTicket, withdraw []Withdrawal, revise []Revision, who string) ([]*Ticket, error) {
	if len(add) == 0 && len(withdraw) == 0 && len(revise) == 0 {
		return nil, errf(Invalid, "tickets: propose at least one ticket, or withdraw or revise one")
	}
	if len(add) > maxProposed || len(revise) > maxProposed {
		return nil, errf(Invalid, "tickets: at most %d proposed and %d revised in one call", maxProposed, maxProposed)
	}
	for i := range add {
		if err := checkBreakdownTicket(&add[i]); err != nil {
			return nil, errf(Invalid, "ticket %d (%s): %v", i+1, add[i].Title, err)
		}
	}
	// A ticket withdrawn in the call is closed before the revisions, which then refuse it.
	for i := range revise {
		if err := checkBreakdownTicket(&revise[i].NewTicket); err != nil {
			return nil, errf(Invalid, "revise %s: %v", revise[i].Key, err)
		}
	}
	var out []*Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		am, err := Amending(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if p.State != "planning" && am == nil {
			return errf(Refused, "%s is %s: the breakdown is proposed before the OK, or of an amendment before its OK", p.Name, p.State)
		}
		var proposed int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND origin = 'plan' AND (created_by LIKE 'planner%' OR created_by LIKE 'lead%')`, p.ID).Scan(&proposed); err != nil {
			return err
		}
		if proposed+len(add) > maxBreakdown {
			return errf(Refused, "%s's breakdown has %d tickets; it holds at most %d", p.Name, proposed, maxBreakdown)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		pipe := PipelineOf(doc)
		for _, w := range withdraw {
			n, err := ParseNumber(p, w.Key)
			if err != nil {
				return errf(Invalid, "withdraw: %v", err)
			}
			if strings.TrimSpace(w.Reason) == "" || len(w.Reason) > maxText {
				return errf(Invalid, "withdraw %s: give the reason", w.Key)
			}
			t, err := ticketByNumber(ctx, tx, p.ID, n, true)
			if err != nil {
				return errf(Invalid, "withdraw: %s has no ticket %s", p.Name, w.Key)
			}
			if t.Stage != settings.StagePlanning || t.Origin != OriginPlan || !isBreakdownAuthor(t.CreatedBy) {
				return errf(Refused, "withdraw %s: only a ticket of the breakdown that is still in Planning", t.Key)
			}
			if err := b.close(ctx, tx, p, pipe, t, "discarded", "withdrawn by the planner: "+strings.TrimSpace(w.Reason), who); err != nil {
				return err
			}
		}
		keys := make([]string, 0, len(add))
		for _, n := range add {
			t, err := b.addTicket(ctx, tx, p, doc, n, OriginPlan, who, keys)
			if err != nil {
				return errf(kindOf(err), "ticket %d (%s): %v", len(keys)+1, n.Title, err)
			}
			keys = append(keys, t.Key)
			out = append(out, t)
		}
		// Revisions come last, so their dependencies may name the tickets of this call as @n.
		for _, r := range revise {
			t, err := b.revise(ctx, tx, p, r, who, keys)
			if err != nil {
				return errf(kindOf(err), "revise %s: %v", r.Key, err)
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

// checkBreakdownTicket checks a ticket of the breakdown: what every ticket needs, and a why, a what,
// a goal, and at least one check.
func checkBreakdownTicket(n *NewTicket) error {
	if err := n.check(); err != nil {
		return err
	}
	if strings.TrimSpace(n.Why) == "" || strings.TrimSpace(n.What) == "" || strings.TrimSpace(n.Goal) == "" {
		return errf(Invalid, "a ticket of the breakdown has a why, a what, and a goal")
	}
	if len(n.Checks) == 0 {
		return errf(Invalid, "a ticket of the breakdown has at least one check")
	}
	return nil
}

// revise changes one of the lead's tickets still in Planning in place: every field the breakdown
// gives, its checks, and what it depends on. Its number, its history, and the tickets that depend on
// it stay; the change is a row of history with the ticket before and after.
func (b *Board) revise(ctx context.Context, tx pgx.Tx, p *Project, r Revision, who string, earlier []string) (*Ticket, error) {
	num, err := ParseNumber(p, r.Key)
	if err != nil {
		return nil, errf(Invalid, "%v", err)
	}
	t, err := ticketByNumber(ctx, tx, p.ID, num, true)
	if err != nil {
		return nil, errf(Invalid, "%s has no ticket %s", p.Name, r.Key)
	}
	if t.Stage != settings.StagePlanning || t.Origin != OriginPlan || !isBreakdownAuthor(t.CreatedBy) {
		return nil, errf(Refused, "only a ticket of the breakdown that is still in Planning")
	}
	// The operator's own change of a ticket wins over the planner's revision of it (security review of
	// the follow-ups of M5): the last row of its history must be the planner's.
	var last string
	if err := tx.QueryRow(ctx, `SELECT who FROM changes WHERE project = $1 AND tbl = 'tickets' AND row_key = $2 ORDER BY seq DESC LIMIT 1`,
		p.ID, t.Key).Scan(&last); err != nil {
		return nil, err
	}
	if !isBreakdownAuthor(last) {
		return nil, errf(Refused, "the operator changed %s since you wrote it: leave it as it is, and say in the breakdown what should change", t.Key)
	}
	n := r.NewTicket
	// A priority the revision leaves out stays as it is.
	priority := t.Priority
	if n.Priority != nil {
		priority = *n.Priority
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET title = $2, kind = $3, part = $4, source = $5, why = $6, what = $7, goal = $8,
			docs = $9, docs_reason = $10, priority = $11, updated_at = now() WHERE id = $1`,
		t.ID, n.Title, n.Kind, n.Part, n.Source, n.Why, n.What, n.Goal, nonNil(n.Docs), n.DocsReason, priority); err != nil {
		return nil, err
	}
	if err := writeChecks(ctx, tx, t.ID, n.Checks); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ticket_links WHERE from_ticket = $1 AND kind = 'depends_on'`, t.ID); err != nil {
		return nil, err
	}
	for _, dep := range n.DependsOn {
		if rest, ok := strings.CutPrefix(dep, "@"); ok {
			i, err := strconv.Atoi(rest)
			if err != nil || i < 1 || i > len(earlier) {
				return nil, errf(Invalid, "dependsOn: %s names no ticket proposed in this call", dep)
			}
			dep = earlier[i-1]
		}
		if err := b.link(ctx, tx, p, t, dep, "depends_on"); err != nil {
			return nil, err
		}
	}
	after, err := ticketByID(ctx, tx, t.ID, false)
	if err != nil {
		return nil, err
	}
	// The row of history holds what changed, before and after, not the whole ticket twice.
	was, is, err := changed(t, after)
	if err != nil {
		return nil, err
	}
	return after, db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, was, is)
}

// changed returns the fields of a ticket that differ between before and after, as JSON names them.
func changed(before, after *Ticket) (map[string]any, map[string]any, error) {
	var a, b map[string]any
	for _, x := range []struct {
		t *Ticket
		m *map[string]any
	}{{before, &a}, {after, &b}} {
		raw, err := json.Marshal(x.t)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(raw, x.m); err != nil {
			return nil, nil, err
		}
	}
	was, is := map[string]any{}, map[string]any{}
	for k, v := range b {
		if k == "updatedAt" {
			continue
		}
		if !reflect.DeepEqual(a[k], v) {
			was[k], is[k] = a[k], v
		}
	}
	return was, is, nil
}

func isBreakdownAuthor(who string) bool {
	return strings.HasPrefix(who, "planner") || strings.HasPrefix(who, "lead")
}

// kindOf is the kind of a board error, or Invalid.
func kindOf(err error) Kind {
	if e, ok := err.(*Error); ok {
		return e.Kind
	}
	return Invalid
}
