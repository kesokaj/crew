package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Bugs are a project's observed defects (9.4). A report saves the bug and makes its fix ticket in the
// Backlog, which another agent analyses; the bug follows its fix ticket: in work while it is built,
// fixed when it is Done, closed when Analysis discards it.

// Severities of a bug.
var Severities = []string{"low", "medium", "high"}

// CloseReasons are why the operator closes a bug; discarded is Analysis's, through the fix ticket.
var CloseReasons = []string{"duplicate", "not_reproducible", "wont_fix"}

// Bug is one bug.
type Bug struct {
	ID       int64   `json:"-"`
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	Severity string  `json:"severity"`
	Status   string  `json:"status"`
	FoundBy  string  `json:"foundBy"`
	FoundIn  *string `json:"foundIn"`
	FoundRun *string `json:"foundRun"`
	// InTicket is the Done ticket whose change the bug is in, when it is known (12.3), and InConfirmed
	// whether that is more than its reporter's word: the operator's, or what the analysis of its fix
	// ticket found (since 0.54). Only a confirmed one is an escaped defect.
	InTicket        *string   `json:"inTicket"`
	InConfirmed     bool      `json:"inConfirmed"`
	FixTicket       *string   `json:"fixTicket"`
	FixTicketID     *int64    `json:"-"`
	RegressionCheck *string   `json:"regressionCheck"`
	ClosedReason    *string   `json:"closedReason"`
	ClosedNote      *string   `json:"closedNote"`
	FlakyCmd        *string   `json:"flakyCmd"`
	Repeats         int       `json:"repeats"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NewBug is a report: what happens, the steps to see it, and what was expected.
type NewBug struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Happens  string `json:"happens"`
	Steps    string `json:"steps"`
	Expected string `json:"expected"`
	// InTicket is the Done ticket whose change the defect is in, such as SF-12, when it is known.
	InTicket string `json:"inTicket"`
}

// BugFound is where a bug was found: the role or operator or system that found it, its ticket, and its run.
type BugFound struct {
	By     string
	Ticket int64
	Run    string
	// FlakyCmd is the end-to-end command whose result keeps changing (21).
	FlakyCmd string
}

const bugCols = `g.id, g.number, g.title, g.body, g.severity, g.status, g.found_by,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = g.found_ticket), g.found_run::text,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = g.fix_ticket), g.fix_ticket,
	g.regression_check, g.closed_reason, g.closed_note, g.flaky_cmd, g.repeats, g.created_at, g.updated_at,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = g.in_ticket), g.in_confirmed`

func scanBug(r pgx.Row) (*Bug, error) {
	var g Bug
	err := r.Scan(&g.ID, &g.Number, &g.Title, &g.Body, &g.Severity, &g.Status, &g.FoundBy, &g.FoundIn, &g.FoundRun,
		&g.FixTicket, &g.FixTicketID, &g.RegressionCheck, &g.ClosedReason, &g.ClosedNote, &g.FlakyCmd, &g.Repeats, &g.CreatedAt, &g.UpdatedAt, &g.InTicket, &g.InConfirmed)
	return &g, err
}

func (n *NewBug) check() error {
	n.Title, n.Happens, n.Steps, n.Expected = strings.TrimSpace(n.Title), strings.TrimSpace(n.Happens), strings.TrimSpace(n.Steps), strings.TrimSpace(n.Expected)
	if n.Title == "" || len(n.Title) > maxTitle || strings.ContainsAny(n.Title, "\r\n") {
		return errf(Invalid, "title: one line of at most %d characters", maxTitle)
	}
	if !slices.Contains(Severities, n.Severity) {
		return errf(Invalid, "severity: low, medium, or high")
	}
	if n.Happens == "" {
		return errf(Invalid, "happens: what happens")
	}
	if len(n.Happens)+len(n.Steps)+len(n.Expected) > maxText {
		return errf(Invalid, "happens, steps, and expected: at most %d characters together", maxText)
	}
	return nil
}

func (n *NewBug) body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## What happens\n\n%s\n", n.Happens)
	if n.Steps != "" {
		fmt.Fprintf(&b, "\n## Steps to reproduce\n\n%s\n", n.Steps)
	}
	if n.Expected != "" {
		fmt.Fprintf(&b, "\n## Expected\n\n%s\n", n.Expected)
	}
	return b.String()
}

// ReportBug saves a bug and makes its fix ticket in the Backlog, linked to the ticket it was found on,
// in one transaction (9.4). origin and createdBy are the fix ticket's, as for any ticket.
func (b *Board) ReportBug(ctx context.Context, p *Project, n NewBug, found BugFound, origin, createdBy string) (*Bug, *Ticket, error) {
	if err := n.check(); err != nil {
		return nil, nil, err
	}
	var bug *Bug
	var fix *Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		bug, fix, err = b.reportBug(ctx, tx, p, n, found, origin, createdBy)
		return err
	})
	return bug, fix, err
}

func (b *Board) reportBug(ctx context.Context, tx pgx.Tx, p *Project, n NewBug, found BugFound, origin, createdBy string) (*Bug, *Ticket, error) {
	p, err := projectByRef(ctx, tx, p.ID, true)
	if err != nil {
		return nil, nil, err
	}
	if p.State != "running" && p.State != "done" {
		return nil, nil, errf(Refused, "%s is %s: bugs are reported once it runs", p.Name, p.State)
	}
	doc, err := b.Settings(ctx, tx, p)
	if err != nil {
		return nil, nil, err
	}
	var in int64
	if n.InTicket != "" {
		num, err := ParseNumber(p, n.InTicket)
		if err != nil {
			return nil, nil, err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, num, false)
		if err != nil {
			return nil, nil, err
		}
		if t.Stage != settings.StageDone {
			return nil, nil, errf(Invalid, "inTicket: %s is not Done; a bug is in a change that was merged", t.Key)
		}
		in = t.ID
	}
	var num int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM bugs WHERE project = $1`, p.ID).Scan(&num); err != nil {
		return nil, nil, err
	}
	body := n.body()
	seen := fmt.Sprintf("Bug %04d, reported by %s:\n\n%s", num, found.By, body)
	reason := "a fix of a bug changes the pages that describe what it fixes, if any"
	t, err := b.addTicket(ctx, tx, p, doc, NewTicket{
		Title: cut(n.Title, maxTitle),
		Kind:  "fix",
		Why:   fmt.Sprintf("Bug %04d (%s): %s", num, n.Severity, cut(n.Happens, 1000)),
		What:  "Find the cause of the bug and fix it. Add a check that fails before the fix and passes after it.",
		Goal:  cut(firstNonEmpty(n.Expected, "The bug no longer happens."), 1000),
		Seen:  &seen, DocsReason: &reason,
	}, origin, createdBy, nil)
	if err != nil {
		return nil, nil, err
	}
	if found.Ticket != 0 && found.Ticket != t.ID {
		on, err := ticketByID(ctx, tx, found.Ticket, false)
		if err != nil {
			return nil, nil, err
		}
		if err := b.link(ctx, tx, p, t, on.Key, "created_from"); err != nil {
			return nil, nil, err
		}
	}
	// The ticket an agent names is its word until the analysis of the fix ticket, another agent's,
	// confirms it (since 0.54); the operator's is confirmed as it is given.
	confirmed := in != 0 && found.By == Operator
	var id int64
	// Its time is the board's, as a ticket's stage time is: the ticket its defect is in was Done
	// before it (confirmBugIn).
	if err := tx.QueryRow(ctx, `INSERT INTO bugs (project, number, title, body, severity, found_by, found_ticket, found_run, fix_ticket, flaky_cmd, in_ticket, in_confirmed,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13) RETURNING id`,
		p.ID, num, n.Title, body, n.Severity, found.By, nullID(found.Ticket), nullIf(found.Run), t.ID, nullIf(found.FlakyCmd), nullID(in), confirmed, b.now()).Scan(&id); err != nil {
		return nil, nil, err
	}
	bug, err := scanBug(tx.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.id = $1`, id))
	if err != nil {
		return nil, nil, err
	}
	if err := db.RecordProjectChange(ctx, tx, p.ID, createdBy, "bugs", fmt.Sprintf("%04d", num), nil,
		map[string]any{"title": n.Title, "severity": n.Severity, "fixTicket": t.Key}); err != nil {
		return nil, nil, err
	}
	if confirmed {
		if err := escapedDefect(ctx, tx, p, bug, in, t.Key); err != nil {
			return nil, nil, err
		}
	}
	return bug, t, nil
}

// escapedDefect is the signal of a bug in a change that was merged, one the review and the test both
// missed (10.2): in the Done ticket in, once that is confirmed (since 0.54).
func escapedDefect(ctx context.Context, tx pgx.Tx, p *Project, g *Bug, in int64, fixKey string) error {
	return RecordSignal(ctx, tx, Signal{Project: p.ID, Ticket: in, Kind: "escaped_defect",
		Detail: map[string]any{"bug": g.Number, "severity": g.Severity, "foundBy": g.FoundBy, "fixTicket": fixKey, "text": g.Title}})
}

// confirmBugIn is what the analysis of a fix ticket says of the ticket its bug is in (9.4, since 0.54):
// bugIn, a Done ticket of the project or none. It is asked of a lead's approval of a ticket that fixes
// a bug whose ticket is not settled, and refused anywhere else; nil when nothing is to be written. The
// analysis is never the reporter's own run: a run does not analyse a ticket it created. Only a lead
// places a bug: when the lead created the fix ticket a reviewer analyses it, an agent that checks,
// whose model the escapes choose (12.3), so its approval confirms nothing. A settled ticket, the
// operator's word or what an analysis confirmed, stays: the analysis of a replacement fix ticket is
// not asked again (reviews of a9532aa).
func confirmBugIn(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, choice string, bugIn *string, by string) (*bugInAnswer, error) {
	var g *Bug
	got, err := scanBug(tx.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.fix_ticket = $1 AND g.status <> 'closed' FOR UPDATE`, t.ID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		g = got
	}
	approves := choice == "approve" || choice == "notice"
	role, _, _ := strings.Cut(by, " · ")
	switch {
	case g == nil && bugIn != nil:
		return nil, errf(Invalid, "bugIn: %s fixes no bug", t.Key)
	case g == nil:
		return nil, nil
	case g.InConfirmed:
		if bugIn != nil && strings.TrimSpace(*bugIn) != firstNonEmpty(deref(g.InTicket), "none") {
			whose := "an analysis confirmed"
			if g.FoundBy == Operator {
				whose = "the operator reported"
			}
			return nil, errf(Invalid, "bugIn: %s bug %04d in %s, and that stays", whose, g.Number, firstNonEmpty(deref(g.InTicket), "none"))
		}
		return nil, nil
	case role != "curator" && role != "lead":
		if bugIn != nil {
			return nil, errf(Invalid, "bugIn: only the curator's analysis says which ticket a bug is in; an agent that checks is chosen by the defects that escape it")
		}
		return nil, nil
	case bugIn != nil && !approves:
		return nil, errf(Invalid, "bugIn: only an approval says which ticket bug %04d is in", g.Number)
	case bugIn == nil && approves:
		return nil, errf(Invalid, "bugIn: %s fixes bug %04d: say which Done ticket's change the defect is in, such as %s-12, found in main's history, or none when it cannot be told", t.Key, g.Number, p.Key)
	case bugIn == nil:
		return nil, nil
	}
	ans := &bugInAnswer{bug: g}
	if v := strings.TrimSpace(*bugIn); v != "none" {
		num, err := ParseNumber(p, v)
		if err != nil {
			return nil, errf(Invalid, "bugIn: %q is no ticket of %s, nor none", v, p.Key)
		}
		in, err := ticketByNumber(ctx, tx, p.ID, num, false)
		if err != nil {
			return nil, errf(Invalid, "bugIn: %s has no ticket %s", p.Key, v)
		}
		if in.Stage != settings.StageDone || in.ID == t.ID {
			return nil, errf(Invalid, "bugIn: %s is not Done; a bug is in a change that was merged", in.Key)
		}
		// The defect was there when the bug was reported: a ticket Done after that cannot hold it.
		if !in.StageAt.Before(g.CreatedAt) {
			return nil, errf(Invalid, "bugIn: %s was Done after bug %04d was reported, so its change cannot hold the defect", in.Key, g.Number)
		}
		ans.in, ans.key = in.ID, &in.Key
	}
	return ans, nil
}

// bugInAnswer is a confirmed answer of confirmBugIn: the bug, and the ticket it is in, 0 and nil for
// none.
type bugInAnswer struct {
	bug *Bug
	in  int64
	key *string
}

// write records it: the bug is in that ticket, or none, confirmed; a ticket gets its signal.
func (a *bugInAnswer) write(ctx context.Context, tx pgx.Tx, p *Project, fixKey, by string) error {
	if _, err := tx.Exec(ctx, `UPDATE bugs SET in_ticket = $2, in_confirmed = true, updated_at = now() WHERE id = $1`, a.bug.ID, nullID(a.in)); err != nil {
		return err
	}
	if err := db.RecordProjectChange(ctx, tx, p.ID, by, "bugs", fmt.Sprintf("%04d", a.bug.Number),
		map[string]any{"inTicket": a.bug.InTicket, "inConfirmed": a.bug.InConfirmed}, map[string]any{"inTicket": a.key, "inConfirmed": true}); err != nil {
		return err
	}
	if a.in == 0 {
		return nil
	}
	return escapedDefect(ctx, tx, p, a.bug, a.in, fixKey)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Bugs lists a project's bugs, newest first.
func (b *Board) Bugs(ctx context.Context, p *Project) ([]*Bug, error) {
	return bugsWhere(ctx, b.Pool, `g.project = $1 ORDER BY g.number DESC`, p.ID)
}

func bugsWhere(ctx context.Context, q db.Querier, where string, args ...any) ([]*Bug, error) {
	rows, err := q.Query(ctx, `SELECT `+bugCols+` FROM bugs g WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bug
	for rows.Next() {
		g, err := scanBug(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// BugOf is the bug a ticket fixes, or nil.
func (b *Board) BugOf(ctx context.Context, ticket int64) (*Bug, error) {
	g, err := scanBug(b.Pool.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.fix_ticket = $1`, ticket))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

// CloseBug is the operator's close of a bug, with a reason (9.4). Its fix ticket stays as it is; the
// operator discards it on its own.
func (b *Board) CloseBug(ctx context.Context, p *Project, number int, reason, note, who string) (*Bug, error) {
	if !slices.Contains(CloseReasons, reason) {
		return nil, errf(Invalid, "reason: duplicate, not_reproducible, or wont_fix")
	}
	var out *Bug
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		g, err := scanBug(tx.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.project = $1 AND g.number = $2 FOR UPDATE OF g`, p.ID, number))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no bug %04d", number)
		}
		if err != nil {
			return err
		}
		if g.Status == "closed" || g.Status == "fixed" {
			return errf(Refused, "bug %04d is %s", number, g.Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE bugs SET status = 'closed', closed_reason = $2, closed_note = $3, updated_at = now() WHERE id = $1`,
			g.ID, reason, nullIf(strings.TrimSpace(note))); err != nil {
			return err
		}
		if out, err = scanBug(tx.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.id = $1`, g.ID)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "bugs", fmt.Sprintf("%04d", number), map[string]any{"status": g.Status},
			map[string]any{"status": "closed", "reason": reason})
	})
	return out, err
}

// SetRegressionCheck records the check that proves a bug stays fixed: the first of its fix ticket's
// command checks that failed before the fix (9.4).
func (b *Board) SetRegressionCheck(ctx context.Context, ticket int64, cmd string) error {
	_, err := b.Pool.Exec(ctx, `UPDATE bugs SET regression_check = $2, updated_at = now() WHERE fix_ticket = $1`, ticket, cmd)
	return err
}

// bugFollows moves the bug a ticket fixes with the ticket (9.4): in work once the ticket is past
// Analysis, fixed when it is Done, closed when Analysis discards it, and open again when the ticket is
// cancelled. A bug the operator closed stays closed.
func bugFollows(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, to, reason, who string) error {
	var id int64
	var number int
	var status string
	err := tx.QueryRow(ctx, `SELECT id, number, status FROM bugs WHERE fix_ticket = $1 AND status <> 'closed' FOR UPDATE`, t.ID).Scan(&id, &number, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next, closed, note := status, "", ""
	switch {
	case to == settings.StageDone:
		next = "fixed"
	case to == settings.StageClosed && t.ClosedReason != nil && *t.ClosedReason == "discarded":
		next, closed, note = "closed", "discarded", reason
	case to == settings.StageClosed:
		next = "open"
	case to != settings.StageBacklog && to != settings.StageAnalysis && to != settings.StagePlanning:
		next = "in_work"
	}
	if next == status {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE bugs SET status = $2, closed_reason = $3, closed_note = $4, updated_at = now() WHERE id = $1`,
		id, next, nullIf(closed), nullIf(note)); err != nil {
		return err
	}
	return db.RecordProjectChange(ctx, tx, p.ID, who, "bugs", fmt.Sprintf("%04d", number), map[string]any{"status": status},
		map[string]any{"status": next, "ticket": t.Key})
}

// FlakyBug is the bug for an end-to-end command whose result keeps changing (21): the open one for the
// command gets one more repeat; with none, a bug found by system opens, with its fix ticket.
func (b *Board) FlakyBug(ctx context.Context, p *Project, cmd string, on *Ticket, times int) (*Bug, bool, error) {
	var out *Bug
	opened := false
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM bugs WHERE project = $1 AND flaky_cmd = $2 AND status IN ('open', 'in_work')
			ORDER BY id DESC LIMIT 1`, p.ID, cmd).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.Exec(ctx, `UPDATE bugs SET repeats = repeats + 1, updated_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
		case errors.Is(err, pgx.ErrNoRows):
			where := ""
			found := BugFound{By: System, FlakyCmd: cmd}
			if on != nil {
				where, found.Ticket = ", the last time on "+on.Key, on.ID
			}
			bug, _, err := b.reportBug(ctx, tx, p, NewBug{
				Title:    cut("End-to-end tests pass or fail at random: "+oneLine(cmd), maxTitle),
				Severity: "medium",
				Happens: fmt.Sprintf("Crew's run of the end-to-end tests failed, and passed when Crew ran it again in a new sandbox, %d times in 30 days%s. "+
					"A result that keeps changing hides real failures and costs fix rounds.", times, where),
				Steps:    "Run, more than once, in a fresh sandbox:\n\n    " + cmd,
				Expected: "The end-to-end tests give the same result every time on the same code.",
			}, found, OriginAgent, Backend+" · end-to-end tests that pass or fail at random")
			if err != nil {
				return err
			}
			id, opened = bug.ID, true
		default:
			return err
		}
		var err2 error
		out, err2 = scanBug(tx.QueryRow(ctx, `SELECT `+bugCols+` FROM bugs g WHERE g.id = $1`, id))
		return err2
	})
	return out, opened, err
}
