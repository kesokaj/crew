package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// ChangeDecision is the operator's change of a decision an agent recorded with decide (7.5): Crew
// creates a ticket that carries the change out, linked to the ticket the decision was made on, and
// the decision says what it was changed to and by which ticket. A decision is changed once; another
// change is a decision of its own, made on the ticket that carries this one out.
func (b *Board) ChangeDecision(ctx context.Context, p *Project, id int64, choice, who string) (*Ticket, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" || utf8.RuneCountInString(choice) > 500 || strings.ContainsAny(choice, "\n\r") {
		return nil, errf(Invalid, "choice: one line of at most 500 characters")
	}
	var out *Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if p.State == "archived" {
			return errf(Refused, "%s is archived", p.Name)
		}
		var question, was, role, reason string
		var on *int64
		var run *string
		var changed *string
		err = tx.QueryRow(ctx, `SELECT question, choice, role, reason, ticket, run::text, changed_to FROM decisions WHERE id = $1 AND project = $2 FOR UPDATE`,
			id, p.ID).Scan(&question, &was, &role, &reason, &on, &run, &changed)
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no such decision")
		}
		if err != nil {
			return err
		}
		if changed != nil {
			return errf(Refused, "this decision was changed already, to %q", *changed)
		}
		if choice == was {
			return errf(Invalid, "choice: that is what the %s chose", role)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		var from *Ticket
		if on != nil {
			if from, err = ticketByID(ctx, tx, *on, false); err != nil {
				return err
			}
		}
		where := ""
		if from != nil {
			where = " on " + from.Key
		}
		// The ticket is the operator's: it holds only the operator's words and choice. What the agent
		// wrote, the question, its choice, and its reason, is a note on it, which reaches the next
		// agents fenced as data (13.8, AGENTS.md rules 2 and 14).
		docs := "a change of a decision, which Analysis looks at"
		n := NewTicket{
			Title:      fmt.Sprintf("Change decision d_%04d%s", id, where),
			Kind:       "chore",
			Why:        fmt.Sprintf("You changed decision d_%04d, which the %s recorded%s. The decision is a note on this ticket.", id, role, where),
			What:       fmt.Sprintf("Use %q in place of the %s's choice, wherever that choice was carried out.", choice, role),
			Goal:       fmt.Sprintf("The work follows %q.", choice),
			DocsReason: &docs,
		}
		if err := n.check(); err != nil {
			return err
		}
		out, err = b.addTicket(ctx, tx, p, doc, n, OriginOperator, who, nil)
		if err != nil {
			return err
		}
		if from != nil {
			if err := b.link(ctx, tx, p, out, from.Key, "created_from"); err != nil {
				return err
			}
			if out, err = ticketByID(ctx, tx, out.ID, false); err != nil {
				return err
			}
		}
		// The note is Crew's, which no check reads as an agent's work (note_exists reads the
		// builder's); the role that decided is named in it, and its words are quoted as its.
		note := fmt.Sprintf("The decision d_%04d that the operator changed, as the %s recorded it. The question: %s\nThe %s chose: %s\nIts reason: %s",
			id, role, question, role, was, reason)
		if _, err := tx.Exec(ctx, `INSERT INTO records (project, ticket, kind, author, run, body) VALUES ($1, $2, 'note', 'crew', $3::uuid, $4)`,
			p.ID, out.ID, run, note); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE decisions SET changed_to = $2, change_ticket = $3, changed_at = now() WHERE id = $1`,
			id, choice, out.ID); err != nil {
			return err
		}
		var on0 int64
		if on != nil {
			on0 = *on
		}
		if err := intervention(ctx, tx, p.ID, on0, DecisionChanged, "Changed a decision an agent made",
			map[string]any{"decision": id, "role": role, "changeTicket": out.Key}); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "decisions", fmt.Sprint(id), map[string]any{"choice": was},
			map[string]any{"changedTo": choice, "ticket": out.Key})
	})
	return out, err
}
