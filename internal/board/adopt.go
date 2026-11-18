package board

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// ChatAuthor is the author of what the operator sends on from an assistant's message with Tell the
// agent or Remember this (11.9): the assistant wrote it after reading trajectories and pages, so it
// reaches an agent as data with that source, never as the operator's instruction (plan M20 D11).
const ChatAuthor = "assistant (from the chat)"

// QuoteParts are the parts of the task view a marked text may come from (11.9; plan M20 D14).
var QuoteParts = []string{"why", "what", "goal", "approach", "checks", "log", "trajectory", "prompt", "diff", "record", "summary", "subtask", "message", "other"}

// The most a marked text holds, and its label.
const (
	MaxQuote      = 20000
	MaxQuoteLabel = 80
)

// QuoteSource is where a marked text came from in the task view: its part from a fixed set, and a label
// of one short line, which a prompt names beside the fence (11.9; plan M20 D14).
type QuoteSource struct {
	Part    string `json:"part"`
	Label   string `json:"label"`
	Run     string `json:"run,omitempty"`
	Subtask int    `json:"subtask,omitempty"`
}

// CheckQuoteSource refuses a source whose part is not one of QuoteParts, whose label is not one short
// line, or whose run is not a run's id. A nil source is none.
func CheckQuoteSource(src *QuoteSource) error {
	if src == nil {
		return nil
	}
	ok := false
	for _, p := range QuoteParts {
		ok = ok || p == src.Part
	}
	if !ok {
		return errf(Invalid, "source.part: one of %s", strings.Join(QuoteParts, ", "))
	}
	if strings.ContainsAny(src.Label, "\r\n") || utf8.RuneCountInString(src.Label) > MaxQuoteLabel {
		return errf(Invalid, "source.label: at most %d characters on one line", MaxQuoteLabel)
	}
	if src.Run != "" && !isUUID(src.Run) {
		return errf(Invalid, "source.run: a run's id")
	}
	if src.Subtask < 0 {
		return errf(Invalid, "source.subtask: a number")
	}
	return nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !strings.ContainsRune("0123456789abcdefABCDEF", r):
			return false
		}
	}
	return true
}

// Adopt makes a ticket the assistant of conversation proposed the operator's (11.8; plan M20 D19).
// While no Analysis run took it up (status proposed) the form's title, kind, why, what, and goal
// replace its own and it is approved by adding it, so Analysis only makes it ready. Later, only its
// adoption is recorded: its text, status, and stage stay, since a ticket only moves to the right and
// an agent may be reading it (rule 3). One that ended, or was adopted, is refused: the form then
// adds a new ticket. Its origin stays conversation, so its Review cannot be skipped (7.4).
func (b *Board) Adopt(ctx context.Context, p *Project, ref, conversation string, form NewTicket, who string) (*Ticket, error) {
	if err := form.check(); err != nil {
		return nil, err
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
		n, err := ParseNumber(p, ref)
		if err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		switch {
		case t.Origin != OriginConversation || t.Conversation == nil || *t.Conversation != conversation:
			return errf(Refused, "%s was not proposed in this conversation", t.Key)
		case t.AdoptedAt != nil:
			return errf(Refused, "%s is yours already", t.Key)
		case t.Stage == settings.StageDone || t.Stage == settings.StageClosed:
			return errf(Refused, "%s is %s: make a new ticket of this instead", t.Key, t.Stage)
		}
		before := map[string]any{"adoptedAt": nil}
		after := map[string]any{"adopted": true}
		if t.Analysis.Status == StatusProposed {
			a := t.Analysis
			a.Approved = true
			cols := map[string]any{"title": form.Title, "kind": form.Kind, "why": form.Why, "what": form.What, "goal": form.Goal, "analysis": a}
			for k, v := range map[string]any{"title": t.Title, "kind": t.Kind, "why": t.Why, "what": t.What, "goal": t.Goal, "analysis": t.Analysis} {
				before[k] = v
			}
			for k, v := range cols {
				after[k] = v
			}
			if err := updateTicket(ctx, tx, t.ID, cols); err != nil {
				return err
			}
		} else {
			after["kept"] = "its analysis is " + t.Analysis.Status + ": its text and its stage stay as they are"
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET adopted_at = now(), updated_at = now() WHERE id = $1`, t.ID); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, before, after); err != nil {
			return err
		}
		out, err = ticketByID(ctx, tx, t.ID, false)
		return err
	})
	if err == nil {
		b.Kick()
	}
	return out, err
}
