package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Decision is what Analysis decides about a ticket (7.6), with what it wrote on the ticket. The lead
// makes it through the Crew MCP (M6); the board checks it and records it.
type Decision struct {
	Choice    string     `json:"choice"` // approve | skip | discard | notice
	Reason    string     `json:"reason"`
	LookAgain *LookAgain `json:"lookAgain"`
	// What Analysis writes or confirms (7.6, step 5). A nil field is left as it is.
	Why         *string     `json:"why"`
	What        *string     `json:"what"`
	Goal        *string     `json:"goal"`
	Checks      []CheckSpec `json:"checks"`
	Approach    *string     `json:"approach"`
	Docs        []string    `json:"docs"`
	DocsReason  *string     `json:"docsReason"`
	Skips       []string    `json:"skips"`
	SkipsReason *string     `json:"skipsReason"`
	MayChange   []string    `json:"mayChange"`
	DependsOn   []string    `json:"dependsOn"`
	Priority    *int        `json:"priority"`
	// Network lets Analysis make a ticket's sandboxes Model only (13.6). It can only make the network
	// smaller: the internet and the LAN are the operator's to set.
	Network *string `json:"network"`
	// BugIn is, for a ticket that fixes a bug, the Done ticket whose change the defect is in, such as
	// SF-12, or none: what the analysis found, which the bug's reporter's word is not (9.4, since 0.54).
	BugIn *string `json:"bugIn"`
}

// Origins whose ticket cannot skip Review: what an agent created, a personal token sent, or an
// assistant drafted (7.4, 11.7, 13.8).
var mustReview = []string{OriginAgent, OriginToken, "conversation", "chat"}

// MustReview says a ticket of origin cannot skip Review.
func MustReview(origin string) bool { return slices.Contains(mustReview, origin) }

// Decide records an analysis decision on a ticket in Analysis, as one transaction with its row in
// changes. by names who analysed it, the role and its run. A ticket from the plan or from the
// operator has nothing to decide (R31): it is approved, or skipped when its work is done already.
func (b *Board) Decide(ctx context.Context, p *Project, ref string, d Decision, by string) (*Ticket, error) {
	return b.decide(ctx, p, ref, d, by, false)
}

// errTried rolls back a decision that was only tried.
var errTried = errors.New("board: tried, not written")

// TryDecide says whether a decision would be written, and writes nothing: what the Crew MCP answers
// when an agent decides, before Crew applies the decision after its turn (11.3).
func (b *Board) TryDecide(ctx context.Context, p *Project, ref string, d Decision, by string) error {
	_, err := b.decide(ctx, p, ref, d, by, true)
	if errors.Is(err, errTried) {
		return nil
	}
	return err
}

func (b *Board) decide(ctx context.Context, p *Project, ref string, d Decision, by string, try bool) (*Ticket, error) {
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	var out *Ticket
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		if t.Stage != settings.StageAnalysis {
			return errf(Refused, "%s is in %s: a decision is made in Analysis", t.Key, t.Stage)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		pipe := PipelineOf(doc)
		decided := t.Origin == OriginPlan || t.Origin == OriginOperator || t.Analysis.Approved
		if decided && (d.Choice == "discard" || d.Choice == "notice") {
			return errf(Refused, "%s comes from the %s: it is approved by being added, and is only made ready or skipped", t.Key, t.Origin)
		}
		bugIn, err := confirmBugIn(ctx, tx, p, t, d.Choice, d.BugIn, by)
		if err != nil {
			return err
		}
		before, after, err := b.writeAnalysis(ctx, tx, p, pipe, t, d)
		if err != nil {
			return err
		}
		a := t.Analysis
		a.DecidedBy, a.Reason, a.LookAgain = by, strings.TrimSpace(d.Reason), nil
		if d.MayChange != nil {
			if !slices.Equal(a.MayChange, d.MayChange) {
				a.MayChangeAgreed = false
			}
			a.MayChange = d.MayChange
		}
		cols := after
		switch d.Choice {
		case "approve":
			if err := approvable(t, cols, checksAfter(t, d)); err != nil {
				return err
			}
			a.Status = StatusReady
		case "skip":
			if a.Reason == "" {
				return errf(Invalid, "reason: say why the ticket is not needed now")
			}
			if err := checkLookAgain(ctx, tx, p, d.LookAgain); err != nil {
				return err
			}
			a.Status, a.LookAgain = StatusSkipped, d.LookAgain
		case "notice":
			if err := approvable(t, cols, checksAfter(t, d)); err != nil {
				return err
			}
			switch settingString(doc, "approvals.new_feature") {
			case "agent":
				a.Status = StatusReady
			case "operator":
				// No clock: it waits for Approve now or Discard.
				a.Status = StatusNotice
			default:
				a.Status = StatusNotice
				hours, _ := settingNumber(doc, "approvals.new_feature_wait_hours")
				until := b.now().Add(time.Duration(hours * float64(time.Hour)))
				before["notice_until"], cols["notice_until"] = t.NoticeUntil, until
			}
		case "discard":
			if a.Reason == "" {
				return errf(Invalid, "reason: say why the ticket is discarded")
			}
		default:
			return errf(Invalid, "choice: approve, skip, discard, or notice")
		}
		// With approvals.created_task set to operator, the agent's decision on a ticket the operator
		// did not make is a proposal: the ticket waits in Analysis for the operator's Approve or Discard
		// (7.3, since 0.47). Calling it a new feature is a proposal too, with no clock: the agent does
		// not decide whether the operator decides (security review of M13).
		proposed := !decided && settingString(doc, "approvals.created_task") == "operator"
		if proposed {
			a.Proposal = &Proposal{Choice: d.Choice, Reason: a.Reason, By: by, LookAgain: a.LookAgain}
			a.Status, a.LookAgain = StatusOperator, nil
			delete(cols, "notice_until")
			delete(before, "notice_until")
		}
		before["analysis"], cols["analysis"] = t.Analysis, a
		if err := updateTicket(ctx, tx, t.ID, cols); err != nil {
			return err
		}
		if d.Checks != nil {
			if err := writeChecks(ctx, tx, t.ID, d.Checks); err != nil {
				return err
			}
			before["checks"], cols["checks"] = t.Checks, d.Checks
		}
		for _, dep := range d.DependsOn {
			if err := b.link(ctx, tx, p, t, dep, "depends_on"); err != nil {
				return err
			}
		}
		if len(d.DependsOn) > 0 {
			before["dependsOn"], cols["dependsOn"] = t.Links, d.DependsOn
		}
		cols["decision"] = d.Choice
		if err := db.RecordProjectChange(ctx, tx, p.ID, by, "tickets", t.Key, before, cols); err != nil {
			return err
		}
		if d.Choice == "discard" && !proposed {
			t, err = ticketByID(ctx, tx, t.ID, true)
			if err != nil {
				return err
			}
			if err := b.close(ctx, tx, p, pipe, t, "discarded", a.Reason, by); err != nil {
				return err
			}
			// A ticket an agent created that Analysis discards is feedback on the creating role (9.5, 21).
			if t.Origin == OriginAgent {
				if err := b.systemFeedback(ctx, tx, p, roleOf(t.CreatedBy), fmt.Sprintf("Analysis discarded %s, which the %s created: %s",
					t.Key, roleOf(t.CreatedBy), a.Reason), "ticket", t); err != nil {
					return err
				}
			}
		}
		if try {
			return errTried
		}
		// Only a decision that makes the ticket ready confirms: a proposal, or a notice that waits for
		// its clock or the operator, may still be discarded (reviews of a9532aa). Such a bug stays
		// unconfirmed, which counts no escape.
		if bugIn != nil && a.Status == StatusReady {
			if err := bugIn.write(ctx, tx, p, t.Key, by); err != nil {
				return err
			}
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	return out, err
}

// SetAnalysis marks a ticket in Analysis as being analysed, or as waiting for analysis again after an
// analysis that failed, with its row in changes. A ticket that was decided meanwhile is left as it is.
func (b *Board) SetAnalysis(ctx context.Context, p *Project, t *Ticket, status, why string) error {
	if status != StatusAnalysing && status != StatusWaiting {
		return fmt.Errorf("board: %q is not a status of an analysis that runs", status)
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		cur, err := ticketByID(ctx, tx, t.ID, true)
		if err != nil {
			return err
		}
		from := cur.Analysis.Status
		ok := map[string][]string{StatusAnalysing: {StatusWaiting, StatusProposed}, StatusWaiting: {StatusAnalysing}}[status]
		if cur.Stage != settings.StageAnalysis || !slices.Contains(ok, from) {
			return nil
		}
		a := cur.Analysis
		a.Status = status
		if err := updateTicket(ctx, tx, t.ID, map[string]any{"analysis": a}); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, Backend, "tickets", cur.Key, map[string]any{"analysis": cur.Analysis},
			map[string]any{"analysis": a, "why": why})
	})
}

// writeAnalysis checks what a decision writes on a ticket, and returns the columns before and after.
func (b *Board) writeAnalysis(ctx context.Context, tx pgx.Tx, p *Project, pipe Pipeline, t *Ticket, d Decision) (map[string]any, map[string]any, error) {
	before, after := map[string]any{}, map[string]any{}
	why, what, goal := t.Why, t.What, t.Goal
	for _, f := range []struct {
		col string
		v   *string
		dst *string
	}{{"why", d.Why, &why}, {"what", d.What, &what}, {"goal", d.Goal, &goal}} {
		if f.v != nil {
			before[f.col], after[f.col] = *f.dst, *f.v
			*f.dst = *f.v
		}
	}
	if err := checkTexts(t.Title, why, what, goal); err != nil {
		return nil, nil, err
	}
	if d.Checks != nil {
		if err := checkChecks(d.Checks); err != nil {
			return nil, nil, err
		}
	}
	if d.Approach != nil {
		if len(*d.Approach) > maxText {
			return nil, nil, errf(Invalid, "approach: at most %d characters", maxText)
		}
		before["approach"], after["approach"] = t.Approach, *d.Approach
	}
	if d.Docs != nil {
		if err := checkDocs(d.Docs); err != nil {
			return nil, nil, err
		}
		before["docs"], after["docs"] = t.Docs, d.Docs
	}
	if d.DocsReason != nil {
		before["docs_reason"], after["docs_reason"] = t.DocsReason, *d.DocsReason
	}
	if d.Skips != nil {
		for _, s := range d.Skips {
			if !slices.Contains(pipe.Optional(), s) {
				return nil, nil, errf(Invalid, "skips: %q is not an optional stage of %s", s, p.Name)
			}
			if s == settings.StageReview {
				if slices.Contains(mustReview, t.Origin) {
					return nil, nil, errf(Refused, "skips: %s was created by an %s, so it cannot skip Review", t.Key, t.Origin)
				}
				if t.SpecRef != "" && t.CreatedBy != Operator {
					return nil, nil, errf(Refused, "skips: %s was created from spec %s by %s, so it cannot skip Review", t.Key, t.SpecRef, t.CreatedBy)
				}
			}
		}
		if len(d.Skips) > 0 && (d.SkipsReason == nil || strings.TrimSpace(*d.SkipsReason) == "") {
			return nil, nil, errf(Invalid, "skipsReason: say why the ticket does not need those stages")
		}
		before["skips"], after["skips"] = t.Skips, d.Skips
	}
	if d.SkipsReason != nil {
		before["skips_reason"], after["skips_reason"] = t.SkipsReason, *d.SkipsReason
	}
	if d.MayChange != nil {
		for _, m := range d.MayChange {
			if strings.TrimSpace(m) == "" || len(m) > 200 {
				return nil, nil, errf(Invalid, "mayChange: paths or patterns of at most 200 characters")
			}
		}
	}
	if d.Priority != nil {
		if *d.Priority < 0 || *d.Priority > 3 {
			return nil, nil, errf(Invalid, "priority: 0 urgent, 1 high, 2 normal, or 3 low")
		}
		before["priority"], after["priority"] = t.Priority, *d.Priority
	}
	if d.Network != nil {
		if *d.Network != config.ModeModelOnly {
			return nil, nil, errf(Refused, "network: Analysis can only make a ticket model_only; the internet and the LAN are the operator's to set")
		}
		before["network"], after["network"] = t.Network, *d.Network
	}
	return before, after, nil
}

// approvable says a ticket can leave Analysis: it has a why, a what, a goal, and a check (7.1), with
// what the decision writes laid on it.
func approvable(t *Ticket, cols map[string]any, checks []CheckSpec) error {
	field := func(col, cur string) string {
		if v, ok := cols[col].(string); ok {
			return v
		}
		return cur
	}
	for _, f := range [][2]string{{"why", t.Why}, {"what", t.What}, {"goal", t.Goal}} {
		if strings.TrimSpace(field(f[0], f[1])) == "" {
			return errf(Refused, "%s has no %s: a ticket leaves Analysis with a why, a what, a goal, and a check", t.Key, f[0])
		}
	}
	if len(checks) == 0 {
		return errf(Refused, "%s has no check: a ticket leaves Analysis with a why, a what, a goal, and a check", t.Key)
	}
	return nil
}

func checksAfter(t *Ticket, d Decision) []CheckSpec {
	if d.Checks != nil {
		return d.Checks
	}
	return t.Checks
}

// checkLookAgain checks the condition of a skip: one of four kinds Crew can test (7.6).
func checkLookAgain(ctx context.Context, tx pgx.Tx, p *Project, l *LookAgain) error {
	if l == nil {
		return errf(Invalid, "lookAgain: a skip says when to look again: part_done, ticket_done, date, or all_done")
	}
	switch l.Kind {
	case "all_done":
		if l.Value != "" {
			return errf(Invalid, "lookAgain: all_done takes no value")
		}
	case "date":
		if _, err := time.Parse(time.RFC3339, l.Value); err != nil {
			if _, err := time.Parse(time.DateOnly, l.Value); err != nil {
				return errf(Invalid, "lookAgain: a date such as 2026-11-01")
			}
		}
	case "ticket_done":
		n, err := ParseNumber(p, l.Value)
		if err != nil {
			return errf(Invalid, "lookAgain: %v", err)
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE project = $1 AND number = $2)`, p.ID, n).Scan(&ok); err != nil || !ok {
			return errf(Invalid, "lookAgain: %s has no ticket %s", p.Name, l.Value)
		}
	case "part_done":
		if strings.TrimSpace(l.Value) == "" || len(l.Value) > 200 {
			return errf(Invalid, "lookAgain: part_done names a part of the plan")
		}
	default:
		return errf(Invalid, "lookAgain: part_done, ticket_done, date, or all_done")
	}
	return nil
}

// Analyses returns the analysis decisions of a project (15.4): every ticket that was approved,
// skipped, discarded, or given notice as a new feature, with its reasoning.
func (b *Board) Analyses(ctx context.Context, p *Project) ([]*Ticket, error) {
	all, err := b.Tickets(ctx, p)
	if err != nil {
		return nil, err
	}
	var out []*Ticket
	for _, t := range all {
		if t.Analysis.DecidedBy != "" || t.ClosedReason != nil {
			out = append(out, t)
		}
	}
	return out, nil
}
