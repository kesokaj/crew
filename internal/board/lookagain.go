package board

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// A ticket that cannot get through (7.7): its run stopped twice, or it needed its fix rounds. The
// lead looks again, and either gives it one new approach or cancels it and creates the tickets that
// replace it. Both are recorded with the lead's reason, in changes and as news.

// IsReplacement says the ticket replaces a cancelled one: it is not replaced a second time.
func (t *Ticket) IsReplacement() bool {
	for _, l := range t.Links {
		if l.Kind == "replaces" {
			return true
		}
	}
	return false
}

// TryNewApproach says whether the ticket may have a new approach now, with the reason it may not.
func TryNewApproach(t *Ticket) error {
	switch {
	case t.Approaches > 0:
		return errf(Refused, "%s had its new approach already: cancel it and replace it with cancel_and_replace", t.Key)
	case t.Stage == settings.StageAnalysis || t.Stage == settings.StageBacklog || t.Stage == settings.StagePlanning:
		return errf(Refused, "%s is in %s and has no work to take another way: cancel it and replace it with cancel_and_replace", t.Key, stageName(t.Stage))
	case t.Stage == settings.StageDone || t.Stage == settings.StageClosed:
		return errf(Refused, "%s is %s", t.Key, stageName(t.Stage))
	}
	return nil
}

// NewApproach records the lead's new approach (7.7): the ticket stays where it is with two more fix
// rounds, and the parts and subtasks that stopped or wait for the old approach are superseded.
// The caller starts the builder that carries it out.
func (b *Board) NewApproach(ctx context.Context, p *Project, ref, reason, who string) (*Ticket, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, errf(Invalid, "reason: say why the ticket keeps failing")
	}
	var out *Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		t, err := b.lockTicket(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		if err := TryNewApproach(t); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET approaches = approaches + 1, approach_at = now(), stuck = NULL, updated_at = now()
			WHERE id = $1`, t.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE subtasks SET state = 'superseded', wait_for = NULL, updated_at = now()
			WHERE ticket = $1 AND (state = 'stopped' OR (kind IN ('part', 'join') AND state IN ('queued', 'waiting', 'failed')))`, t.ID); err != nil {
			return err
		}
		text := fmt.Sprintf("The lead gave %s a new approach: %s", t.Key, reason)
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, map[string]any{"approaches": t.Approaches},
			map[string]any{"approaches": t.Approaches + 1, "why": reason}); err != nil {
			return err
		}
		if err := RecordSignal(ctx, tx, Signal{Project: p.ID, Ticket: t.ID, Kind: "new_approach",
			Detail: map[string]any{"reason": reason, "text": text}}); err != nil {
			return err
		}
		// The lead had to look at the ticket again: feedback on the builder's work (9.5).
		if err := b.systemFeedback(ctx, tx, p, "builder", fmt.Sprintf("The lead had to look at %s again and gave it a new approach: %s", t.Key, reason), "ticket", t); err != nil {
			return err
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	return out, err
}

// TryCancelAndReplace says whether the ticket may be cancelled with these replacements: a
// replacement is not replaced again (7.7), and every other ticket is replaced by at least one.
func TryCancelAndReplace(t *Ticket, replacements []NewTicket) error {
	if t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
		return errf(Refused, "%s is %s", t.Key, stageName(t.Stage))
	}
	if t.IsReplacement() && len(replacements) > 0 {
		return errf(Refused, "%s replaces a cancelled ticket and is not replaced again: cancel it with no tickets, and say in reason what is missing", t.Key)
	}
	if !t.IsReplacement() && len(replacements) == 0 {
		return errf(Invalid, "tickets: name the tickets that replace %s, smaller or cut differently", t.Key)
	}
	for i := range replacements {
		n := &replacements[i] // check fills what a ticket takes by default, for the caller too
		if err := n.check(); err != nil {
			return errf(Invalid, "tickets[%d]: %s", i, err.Error())
		}
		own := 0
		for _, c := range n.Checks {
			if c.Kind == "cmd" && !c.Keeps {
				own++
			}
		}
		if own == 0 {
			return errf(Invalid, "tickets[%d]: give at least one command check that fails until its goal is met", i)
		}
	}
	return nil
}

// CancelAndReplace cancels a ticket that cannot get through and creates the tickets that replace it
// (7.7), in one transaction: each starts in Backlog with the link replaces, at the cancelled ticket's
// priority, and is analysed by another agent; a ticket that depended on the cancelled one depends on
// its replacements; the cancelled ticket's branch is kept.
// Replacements are not work agents added. A replacement that cannot get through is cancelled with
// none, and its reason is what is missing.
func (b *Board) CancelAndReplace(ctx context.Context, p *Project, ref, reason string, replacements []NewTicket, who string) ([]*Ticket, error) {
	return b.cancel(ctx, p, ref, reason, replacements, who, true)
}

// Cancel cancels a ticket with no replacement, whatever it is: the backend's, when the lead's own look
// again stopped twice (21). The reason is what the final report lists.
func (b *Board) Cancel(ctx context.Context, p *Project, ref, reason, who string) ([]*Ticket, error) {
	return b.cancel(ctx, p, ref, reason, nil, who, false)
}

func (b *Board) cancel(ctx context.Context, p *Project, ref, reason string, replacements []NewTicket, who string, lead bool) ([]*Ticket, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, errf(Invalid, "reason: say why the ticket cannot get through")
	}
	var out []*Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		t, err := b.lockTicket(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		if lead {
			if err := TryCancelAndReplace(t, replacements); err != nil {
				return err
			}
		} else if t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return errf(Refused, "%s is %s", t.Key, stageName(t.Stage))
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		keys := []string{}
		for _, n := range replacements {
			n.Priority = &t.Priority
			n.DependsOn = nil
			r, err := b.addTicket(ctx, tx, p, doc, n, OriginAgent, who, nil)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, 'replaces')`, r.ID, t.ID); err != nil {
				return err
			}
			// What the cancelled ticket waited for, its replacements wait for too.
			if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind)
				SELECT $1, to_ticket, 'depends_on' FROM ticket_links WHERE from_ticket = $2 AND kind = 'depends_on'
				ON CONFLICT DO NOTHING`, r.ID, t.ID); err != nil {
				return err
			}
			keys = append(keys, r.Key)
			out = append(out, r)
		}
		// What depended on the cancelled ticket now depends on what replaces it (7.9).
		if len(out) > 0 {
			for _, r := range out {
				if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind)
					SELECT from_ticket, $2, 'depends_on' FROM ticket_links WHERE to_ticket = $1 AND kind = 'depends_on'
					ON CONFLICT DO NOTHING`, t.ID, r.ID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ticket_links WHERE to_ticket = $1 AND kind = 'depends_on'`, t.ID); err != nil {
				return err
			}
		}
		text := fmt.Sprintf("The lead cancelled %s and replaced it with %s: %s", t.Key, strings.Join(keys, ", "), reason)
		switch {
		case !lead:
			text = fmt.Sprintf("Crew cancelled %s: %s", t.Key, reason)
		case len(out) == 0:
			text = fmt.Sprintf("The lead cancelled %s, which replaced a cancelled ticket and is not replaced again. What is missing: %s", t.Key, reason)
		}
		if err := b.close(ctx, tx, p, PipelineOf(doc), t, "cancelled", reason, who); err != nil {
			return err
		}
		// The bug a cancelled ticket fixed is fixed by what replaces it (9.4).
		if len(out) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE bugs SET fix_ticket = $2, updated_at = now() WHERE fix_ticket = $1`, t.ID, out[0].ID); err != nil {
				return err
			}
		}
		// The lead had to look at the ticket again: feedback on the builder's work (9.5).
		if lead {
			if err := b.systemFeedback(ctx, tx, p, "builder", fmt.Sprintf("The lead had to look at %s again and cancelled it: %s", t.Key, reason), "ticket", t); err != nil {
				return err
			}
		}
		if err := RecordSignal(ctx, tx, Signal{Project: p.ID, Ticket: t.ID, Kind: "cancel_and_replace",
			Detail: map[string]any{"reason": reason, "replacements": keys, "missing": len(out) == 0, "text": text}}); err != nil {
			return err
		}
		for i, r := range out {
			if out[i], err = ticketByID(ctx, tx, r.ID, false); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// lockTicket reads a ticket of the project by its key, with the project and the ticket locked.
func (b *Board) lockTicket(ctx context.Context, tx pgx.Tx, p *Project, ref string) (*Ticket, error) {
	if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
		return nil, err
	}
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	return ticketByNumber(ctx, tx, p.ID, n, true)
}
