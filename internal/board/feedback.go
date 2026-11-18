package board

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// Feedback is a judgment of someone's work that should change how that work is done from now on
// (9.5). It comes from the operator, from agents on each other's work, and from the backend (system),
// which captures what the operator and Analysis sent back. The lead handles it in housekeeping.

// FeedbackRoles are the roles feedback can be about.
var FeedbackRoles = []string{"curator", "lead", "planner", "builder", "reviewer", "tester", "e2e", "verifier"}

// System is who the backend's captured feedback and bugs come from (9.4, 9.5).
const System = "system"

// Feedback is one piece of feedback.
type Feedback struct {
	ID         int64      `json:"-"`
	Number     int        `json:"number"`
	From       string     `json:"from"`
	FromRun    *string    `json:"fromRun"`
	About      string     `json:"about"`
	Kind       string     `json:"kind"`
	Ticket     *string    `json:"ticket"`
	Record     *string    `json:"record"`
	Body       string     `json:"body"`
	Status     string     `json:"status"`
	Outcome    *string    `json:"outcome"`
	ResolvedBy *string    `json:"resolvedBy"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// NewFeedback is what give_feedback and the operator give.
type NewFeedback struct {
	About  string `json:"about"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
	Ticket string `json:"ticket"`
	Record string `json:"record"`
}

// FeedbackFrom is who gives it: operator, system, or an agent's role with its run.
type FeedbackFrom struct {
	Role string
	Run  string
	// MaxPerRun is how many pieces one run may give (knowledge.feedback.max_per_run); 0 reads the setting.
	MaxPerRun int
}

const feedbackCols = `f.id, f.number, f.from_role, f.from_run::text, f.about, f.kind,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = f.ticket),
	f.record, f.body, f.status, f.outcome, f.resolved_by, f.resolved_at, f.created_at`

func scanFeedback(r pgx.Row) (*Feedback, error) {
	var f Feedback
	err := r.Scan(&f.ID, &f.Number, &f.From, &f.FromRun, &f.About, &f.Kind, &f.Ticket, &f.Record, &f.Body, &f.Status,
		&f.Outcome, &f.ResolvedBy, &f.ResolvedAt, &f.CreatedAt)
	return &f, err
}

// GiveFeedback saves a piece of feedback (9.5). An agent cannot give it on its own role, a run gives at
// most knowledge.feedback.max_per_run pieces, and only the operator makes a rule, which becomes a
// memory entry at once.
func (b *Board) GiveFeedback(ctx context.Context, p *Project, n NewFeedback, from FeedbackFrom) (*Feedback, error) {
	var out *Feedback
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = b.giveFeedback(ctx, tx, p, n, from)
		return err
	})
	return out, err
}

func (b *Board) giveFeedback(ctx context.Context, tx pgx.Tx, p *Project, n NewFeedback, from FeedbackFrom) (*Feedback, error) {
	n.Body, n.Ticket, n.Record = strings.TrimSpace(n.Body), strings.TrimSpace(n.Ticket), strings.TrimSpace(n.Record)
	if n.Kind == "" {
		n.Kind = "correction"
	}
	switch {
	case !slices.Contains(FeedbackRoles, n.About):
		return nil, errf(Invalid, "about: one of %s", strings.Join(FeedbackRoles, ", "))
	case !slices.Contains([]string{"praise", "correction", "rule"}, n.Kind):
		return nil, errf(Invalid, "kind: praise, correction, or rule")
	case n.Kind == "rule" && from.Role != Operator:
		return nil, errf(Refused, "kind: only the operator makes a rule")
	case from.Role == n.About:
		return nil, errf(Refused, "feedback is on another role's work, not on your own role's")
	case n.Body == "" || len(n.Body) > maxNote:
		return nil, errf(Invalid, "body: what should change, in at most %d characters", maxNote)
	case len(n.Record) > maxTitle || strings.ContainsAny(n.Record, "\r\n"):
		return nil, errf(Invalid, "record: one line")
	}
	p, err := projectByRef(ctx, tx, p.ID, true)
	if err != nil {
		return nil, err
	}
	var ticket int64
	if n.Ticket != "" {
		num, err := ParseNumber(p, n.Ticket)
		if err != nil {
			return nil, err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, num, false)
		if err != nil {
			return nil, err
		}
		ticket = t.ID
	}
	if from.Run != "" {
		max := from.MaxPerRun
		if max == 0 {
			doc, err := b.Settings(ctx, tx, p)
			if err != nil {
				return nil, err
			}
			v, _ := settingNumber(doc, "knowledge.feedback.max_per_run")
			max = int(v)
		}
		var given int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM feedback WHERE from_run = $1`, from.Run).Scan(&given); err != nil {
			return nil, err
		}
		if given >= max {
			return nil, errf(Refused, "this run gave %d pieces of feedback, the most one run gives", given)
		}
	}
	var num int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM feedback WHERE project = $1`, p.ID).Scan(&num); err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO feedback (project, number, from_role, from_run, about, kind, ticket, record, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		p.ID, num, from.Role, nullIf(from.Run), n.About, n.Kind, nullID(ticket), nullIf(n.Record), n.Body).Scan(&id); err != nil {
		return nil, err
	}
	if err := db.RecordProjectChange(ctx, tx, p.ID, from.Role, "feedback", fmt.Sprintf("%04d", num), nil,
		map[string]any{"about": n.About, "kind": n.Kind}); err != nil {
		return nil, err
	}
	// A rule from the operator is a memory entry at once, for the role it is about (9.5).
	if n.Kind == "rule" {
		title, body, _ := strings.Cut(n.Body, "\n")
		e, err := b.remember(ctx, tx, p, NewEntry{Kind: "convention", Title: cut(strings.TrimSpace(title), 200), Body: strings.TrimSpace(body),
			AppliesTo: []string{n.About}}, MemorySource{Ticket: ticket, Feedback: id}, Operator)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE feedback SET status = 'applied', outcome = $2, resolved_by = $3, resolved_at = now() WHERE id = $1`,
			id, fmt.Sprintf("memory %04d", e.Number), Operator); err != nil {
			return nil, err
		}
	}
	return scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM feedback f WHERE f.id = $1`, id))
}

// FeedbackList lists a project's feedback, newest first; open only gives the new pieces.
func (b *Board) FeedbackList(ctx context.Context, p *Project, open bool) ([]*Feedback, error) {
	where := `f.project = $1`
	if open {
		where += ` AND f.status = 'new'`
	}
	rows, err := b.Pool.Query(ctx, `SELECT `+feedbackCols+` FROM feedback f WHERE `+where+` ORDER BY f.number DESC`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

var outcomeShape = regexp.MustCompile(`^(memory|ticket) (\S+)$`)

// ResolveFeedback records what was done about a piece of feedback (9.5): applied, as a memory entry or
// a ticket it names, or dismissed with a reason. The lead cannot dismiss feedback about the lead.
func (b *Board) ResolveFeedback(ctx context.Context, p *Project, number int, outcome, reason, who string) (*Feedback, error) {
	outcome, reason = strings.TrimSpace(outcome), strings.TrimSpace(reason)
	var out *Feedback
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		f, err := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM feedback f WHERE f.project = $1 AND f.number = $2 FOR UPDATE OF f`, p.ID, number))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no feedback %04d", number)
		}
		if err != nil {
			return err
		}
		if f.Status != "new" {
			return errf(Refused, "feedback %04d is %s already", number, f.Status)
		}
		status, text := "applied", outcome
		if outcome == "none" {
			if reason == "" {
				return errf(Invalid, "reason: why nothing is done about it")
			}
			if (f.About == "curator" || f.About == "lead") && who != Operator {
				return errf(Refused, "feedback %04d is about the %s: the %s does not dismiss it", number, f.About, f.About)
			}
			status, text = "dismissed", "none: "+reason
		} else {
			m := outcomeShape.FindStringSubmatch(outcome)
			if m == nil {
				return errf(Invalid, "outcome: memory NNNN, ticket KEY-N, or none with a reason")
			}
			if m[1] == "memory" {
				n, err := strconv.Atoi(m[2])
				var ok bool
				if err == nil {
					err = tx.QueryRow(ctx, `SELECT true FROM memory WHERE project = $1 AND number = $2`, p.ID, n).Scan(&ok)
				}
				if err != nil {
					return errf(Invalid, "outcome: no memory entry %s", m[2])
				}
				text = fmt.Sprintf("memory %04d", n)
			} else {
				n, err := ParseNumber(p, m[2])
				if err != nil {
					return err
				}
				if _, err := ticketByNumber(ctx, tx, p.ID, n, false); err != nil {
					return err
				}
			}
			if reason != "" {
				text += ": " + reason
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE feedback SET status = $2, outcome = $3, resolved_by = $4, resolved_at = now() WHERE id = $1`,
			f.ID, status, cut(text, maxNote), who); err != nil {
			return err
		}
		if out, err = scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM feedback f WHERE f.id = $1`, f.ID)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "feedback", fmt.Sprintf("%04d", number), map[string]any{"status": f.Status},
			map[string]any{"status": status, "outcome": text})
	})
	return out, err
}

// roleOf is the role in a ticket's created_by, "builder · <run>", or "" for one the backend made.
func roleOf(createdBy string) string {
	role, _, _ := strings.Cut(createdBy, " · ")
	if slices.Contains(FeedbackRoles, role) {
		return role
	}
	return ""
}

// systemFeedback is feedback the backend captured (9.5): a breakdown sent back, a ticket discarded in
// Analysis, a ticket the lead had to look at again. It has no run, so no cap.
func (b *Board) systemFeedback(ctx context.Context, tx pgx.Tx, p *Project, about, body, record string, ticket *Ticket) error {
	if about == "" {
		return nil
	}
	n := NewFeedback{About: about, Kind: "correction", Body: cut(body, maxNote), Record: record}
	if ticket != nil {
		n.Ticket = ticket.Key
	}
	_, err := b.giveFeedback(ctx, tx, p, n, FeedbackFrom{Role: System})
	return err
}
