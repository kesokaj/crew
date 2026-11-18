package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// After Done (7.13): a revert, an amendment to the plan, and deleting a project.

// MergeOf is the merge commit a Done ticket entered main with: its newest merge candidate whose
// checks passed. "" for a ticket that merged nothing.
func MergeOf(ctx context.Context, q db.Querier, t *Ticket) (string, error) {
	var c string
	err := q.QueryRow(ctx, `SELECT commit FROM check_runs WHERE ticket = $1 AND purpose = 'merge' AND state = 'passed'
		ORDER BY id DESC LIMIT 1`, t.ID).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return c, err
}

// Revert makes the ticket that reverts a Done one (7.13): a new ticket of the kind revert, linked to it
// with reverts, which names the merge to undo. The backend writes its analysis, so no lead run is
// spent: its checks are the Done ticket's own command checks turned round, which pass once its work is
// gone, and those that keep passing. In In progress the backend commits the revert on its branch, and
// the ticket then passes the project's stages like any other. The Done ticket stays Done.
func (b *Board) Revert(ctx context.Context, p *Project, ref, who string) (*Ticket, error) {
	var out *Ticket
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		t, err := b.lockTicket(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		locked, err := projectByRef(ctx, tx, p.ID, false)
		if err != nil {
			return err
		}
		if locked.State != "running" && locked.State != "done" {
			return errf(Refused, "%s is %s: a revert is made in a project that runs or is Done", p.Name, locked.State)
		}
		if t.Stage != settings.StageDone {
			return errf(Refused, "%s is not Done: only a merged ticket is reverted", t.Key)
		}
		merge, err := MergeOf(ctx, tx, t)
		if err != nil {
			return err
		}
		if merge == "" {
			return errf(Refused, "%s merged nothing: there is nothing to revert", t.Key)
		}
		// One revert of a ticket: one under way, or Done already, is it; a revert that was closed is not.
		var other string
		err = tx.QueryRow(ctx, `SELECT p.key || '-' || r.number FROM tickets r JOIN ticket_links l ON l.from_ticket = r.id
			JOIN projects p ON p.id = r.project WHERE l.to_ticket = $1 AND l.kind = 'reverts' AND r.stage <> 'closed' LIMIT 1`, t.ID).Scan(&other)
		if err == nil {
			return errf(Refused, "%s reverts %s already", other, t.Key)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		doc, err := b.Settings(ctx, tx, locked)
		if err != nil {
			return err
		}
		var checks []CheckSpec
		for _, c := range t.Checks {
			switch {
			case c.Kind == "cmd" && c.Keeps:
				checks = append(checks, c)
			case c.Kind == "cmd":
				// The check turned round, as one quoted word for a shell of its own: a check an agent
				// wrote cannot close the parentheses and pass always (security review of M11).
				checks = append(checks, CheckSpec{Kind: "cmd", Spec: "! sh -c " + shellQuote(c.Spec)})
			}
		}
		if len(checks) == 0 {
			test := settingString(doc, "sandbox.test")
			if test == "" {
				test = "true"
			}
			checks = append(checks, CheckSpec{Kind: "cmd", Spec: test})
		}
		title := "Revert " + t.Key + ": " + t.Title
		if len(title) > maxTitle {
			title = title[:maxTitle]
		}
		reason := "No page: a revert puts back what was there."
		n := NewTicket{Title: title, Kind: "revert", Checks: checks, DocsReason: &reason,
			Why:  "You revert " + t.Key + ", which merged as " + merge[:12] + ".",
			What: "Undo the change " + t.Key + " merged, with git revert -m 1 " + merge[:12] + ", and nothing else.",
			Goal: "main no longer has " + t.Key + "'s change, and the project's checks pass."}
		r, err := b.addTicket(ctx, tx, locked, doc, n, OriginOperator, who, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, 'reverts')`, r.ID, t.ID); err != nil {
			return err
		}
		// The operator's revert is the operator stepping in; Crew's own, of an improvement measured worse,
		// is not (10.2, 10.5).
		if who != "crew" {
			if err := intervention(ctx, tx, locked.ID, t.ID, MergeReverted, "Reverted the merge of "+t.Key, map[string]any{"revert": r.Key}); err != nil {
				return err
			}
		}
		a := Analysis{Status: StatusReady, DecidedBy: Backend, Reason: "a revert: Crew commits git revert -m 1 of " + merge}
		approach := "Crew commits the revert of " + merge + " on the branch itself; a builder is needed only when it does not apply."
		if _, err := tx.Exec(ctx, `UPDATE tickets SET revert_of = $2, analysis = $3, approach = $4 WHERE id = $1`, r.ID, merge, a, approach); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", r.Key, nil,
			map[string]any{"reverts": t.Key, "merge": merge, "analysis": a}); err != nil {
			return err
		}
		out, err = ticketByID(ctx, tx, r.ID, false)
		return err
	})
	return out, err
}

// shellQuote is s as one word for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Amendment is an amendment to the plan whose tickets have no OK yet (7.13).
type Amendment struct {
	ID     int64     `json:"id"`
	Number int       `json:"number"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// openAmendment is the SQL of a project's open amendment: the newest one with no OK, for the project p.
const openAmendment = `SELECT am.id, am.number, am.text, am.created_at FROM plans am WHERE am.project = %s AND am.kind = 'amendment'
	AND NOT EXISTS (SELECT 1 FROM plans ok WHERE ok.project = am.project AND ok.kind = 'amendment_ok' AND ok.number = am.number)
	ORDER BY am.id DESC LIMIT 1`

// OpenAmendmentSQL is that SQL for a project named by expr, such as p.id, for a LATERAL join.
func OpenAmendmentSQL(expr string) string { return fmt.Sprintf(openAmendment, expr) }

// Amending is the project's open amendment, or nil.
func Amending(ctx context.Context, q db.Querier, project string) (*Amendment, error) {
	var a Amendment
	err := q.QueryRow(ctx, OpenAmendmentSQL("$1"), project).Scan(&a.ID, &a.Number, &a.Text, &a.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// AddAmendment adds an amendment to the plan of a project that runs or is Done (7.13): the lead breaks
// it down like the plan, its tickets wait in Planning for the OK, and the project runs on meanwhile.
// One amendment is open at a time.
func (b *Board) AddAmendment(ctx context.Context, ref, text, who string) (*Amendment, error) {
	p, err := b.Project(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.State != "running" && p.State != "done" {
		return nil, errf(Refused, "%s is %s: an amendment is made to a plan that has its OK; before it, change the plan", p.Name, p.State)
	}
	if open, err := Amending(ctx, b.Pool, p.ID); err != nil || open != nil {
		if err == nil {
			err = errf(Refused, "amendment %d of %s waits for its OK", open.Number, p.Name)
		}
		return nil, err
	}
	if err := b.addPlanRow(ctx, p, "amendment", text, who); err != nil {
		return nil, err
	}
	return Amending(ctx, b.Pool, p.ID)
}

// okAmendment is the OK of an open amendment, in tx: its tickets in Planning move to Backlog, and the
// OK is a row of the plan's own; a project that was Done runs again.
func (b *Board) okAmendment(ctx context.Context, tx pgx.Tx, p *Project, am *Amendment, who string) error {
	doc, err := b.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	// Only the amendment's own tickets: what its breakdown proposed (security review of M11).
	all, err := tickets(ctx, tx, p.ID, "planning")
	if err != nil {
		return err
	}
	var planned []*Ticket
	for _, t := range all {
		if t.Amendment != nil && *t.Amendment == am.ID {
			planned = append(planned, t)
		}
	}
	if len(planned) == 0 {
		return errf(Refused, "amendment %d of %s has no ticket in Planning to approve", am.Number, p.Name)
	}
	keys := []string{}
	for _, t := range planned {
		if err := b.move(ctx, tx, p, PipelineOf(doc), t, settings.StageBacklog, who, fmt.Sprintf("the OK of amendment %d", am.Number), nil); err != nil {
			return err
		}
		keys = append(keys, t.Key)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'amendment_ok', $2, $3, $4)`,
		p.ID, am.Number, strings.Join(keys, " "), who); err != nil {
		return err
	}
	if p.State == "done" {
		if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'running', updated_at = now() WHERE id = $1`, p.ID); err != nil {
			return err
		}
	}
	return db.RecordProjectChange(ctx, tx, p.ID, who, "plans", fmt.Sprintf("%s/amendment/%d", p.Key, am.Number), nil,
		map[string]any{"ok": keys, "state": "running"})
}

// Delete removes a project and every row of it (7.13), when name is its name, typed: nothing else.
// The caller archives it first, so nothing new starts, and waits for its runs to end; the rows go in
// one transaction through the database's own function, which leaves a row in deleted_projects. The
// repositories are the caller's to remove after it.
func (b *Board) Delete(ctx context.Context, ref, name, who string) (*Project, error) {
	p, err := b.Project(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := b.MayDelete(ctx, p, name); err != nil {
		return nil, err
	}
	if p.State != "archived" {
		return nil, errf(Refused, "%s is %s: it is archived first, so nothing starts in it", p.Name, p.State)
	}
	var live int
	if err := b.Pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE (s.project = $1 OR s.ticket IN (SELECT id FROM tickets WHERE project = $1)) AND r.state <> 'ended'`, p.ID).Scan(&live); err != nil {
		return nil, err
	}
	if live > 0 {
		return nil, errf(Refused, "%d runs of %s still end: try again in a minute", live, p.Name)
	}
	var boxes int
	if err := b.Pool.QueryRow(ctx, `SELECT count(*) FROM sandboxes WHERE project = $1 AND removed_at IS NULL`, p.ID).Scan(&boxes); err != nil {
		return nil, err
	}
	if boxes > 0 {
		return nil, errf(Refused, "%d sandboxes of %s still run, such as its terminal: close them, and try again", boxes, p.Name)
	}
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT crew_delete_project($1, $2)`, p.ID, who)
		return err
	})
	return p, err
}

// MayDelete says whether the project may be deleted with the name typed, before anything is done to
// it: the name must be its own, the fixture and Crew's project are Crew's own, a protected project is
// not deleted while Protected is on, and a terminal of the project must be closed first, as its sandbox
// is the operator's.
func (b *Board) MayDelete(ctx context.Context, p *Project, name string) error {
	if name != p.Name {
		return errf(Invalid, "name: type the project's name, %s, to delete it", p.Name)
	}
	if p.ID == fixtureProject {
		return errf(Refused, "the fixture project is Crew's own, and is not deleted")
	}
	var crew string
	var terms int
	if err := b.Pool.QueryRow(ctx, `SELECT coalesce((SELECT doc->'improvements'->>'crew_project' FROM settings WHERE project IS NULL), ''),
		(SELECT count(*) FROM sandboxes WHERE project = $1 AND kind = 'terminal' AND removed_at IS NULL)`, p.ID).Scan(&crew, &terms); err != nil {
		return err
	}
	if crew != "" && crew == p.Name {
		return errf(Refused, "%s is Crew's project, and is not deleted", p.Name)
	}
	if p.Protected {
		return errf(Refused, "%s is protected: switch Protected off in its Settings first", p.Name)
	}
	if terms > 0 {
		return errf(Refused, "%s has its terminal open: close it, and try again", p.Name)
	}
	return nil
}

// SetProtected switches a project's protection on or off (7.13). A protected project is neither
// archived nor deleted until Protected is switched off. Crew's own project (16.2) is always protected,
// and cannot be switched off.
func (b *Board) SetProtected(ctx context.Context, ref string, protected bool, who string) (*Project, error) {
	var out *Project
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if p.ID == fixtureProject {
			return errf(Refused, "the fixture project is Crew's own")
		}
		lockErr := settings.LockMachine(ctx, tx)
		crew, crewErr := CrewProject(ctx, tx)
		if crew != "" && crew == p.Name && !protected {
			return errf(Refused, "%s is Crew's project: it is always protected", p.Name)
		}
		if p.Protected == protected {
			out = p
			return errors.Join(lockErr, crewErr)
		}
		_, updErr := tx.Exec(ctx, `UPDATE projects SET protected = $2, updated_at = now() WHERE id = $1`, p.ID, protected)
		recErr := db.RecordProjectChange(ctx, tx, p.ID, who, "projects", p.ID,
			map[string]any{"protected": p.Protected}, map[string]any{"protected": protected})
		var readErr error
		out, readErr = projectByRef(ctx, tx, p.ID, false)
		return errors.Join(lockErr, crewErr, updErr, recErr, readErr)
	})
	return out, err
}

// SweepDeleted removes the repositories of deleted projects that are still on the machine, as a
// removal that failed when the project was deleted leaves them (7.13), when no project has the name
// again. It runs when the server starts.
func (b *Board) SweepDeleted(ctx context.Context) error {
	rows, err := b.Pool.Query(ctx, `SELECT DISTINCT d.name FROM deleted_projects d WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.name = d.name)`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		if b.Git != nil && b.Git.Exists(n) {
			errs = append(errs, b.Git.Remove(n))
		}
	}
	return errors.Join(errs...)
}

// fixtureProject is the project every installation has since M4 (runs.FixtureProject).
const fixtureProject = "f1c50000-0000-4000-8000-000000000001"
