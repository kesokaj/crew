package board

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/settings"
)

// move moves a ticket to stage to, with its row in stage_moves, the results of the checks that let it
// pass, and its row in changes, in the transaction given. A move to the left is refused whoever asks
// (R179). A ticket that enters In progress gets its branch first (7.12).
func (b *Board) move(ctx context.Context, tx pgx.Tx, p *Project, pipe Pipeline, t *Ticket, to, who, reason string, results []Result) error {
	return b.moveWith(ctx, tx, p, pipe, t, to, who, reason, results, false)
}

// moveWith is move; mergedOutside moves a ticket to Done whose pull request someone merged on GitHub
// (21): Crew makes no merge of its own, because the merge is on the remote's main.
func (b *Board) moveWith(ctx context.Context, tx pgx.Tx, p *Project, pipe Pipeline, t *Ticket, to, who, reason string, results []Result, mergedOutside bool) error {
	if !pipe.Right(t.Stage, to) {
		return errf(Refused, "%s cannot move from %s to %s: a ticket only moves to the right", t.Key, t.Stage, to)
	}
	branch := t.Branch
	if to == settings.StageInProgress && branch == nil {
		name := BranchName(t)
		if err := b.Git.CreateBranch(ctx, p.Name, name); err != nil {
			return fmt.Errorf("the branch of %s: %w", t.Key, err)
		}
		branch = &name
	}
	// A closed ticket says why it was closed, in the same row (closed_reason is set with the stage).
	var closed *string
	if to == settings.StageClosed {
		if t.ClosedReason == nil {
			return fmt.Errorf("board: %s is closed without a reason", t.Key)
		}
		closed = t.ClosedReason
	}
	// A ticket enters Done when it is merged: main moves to the merge its checks passed on (7.10).
	var merged map[string]any
	if _, stoodIn := b.Checks["merges_and_passes_on_main"]; to == settings.StageDone && t.Branch != nil && !stoodIn && !mergedOutside {
		var err error
		if merged, err = b.merge(ctx, tx, p, t); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET stage = $2, stage_at = $3, waiting = NULL, stuck = NULL, branch = $4, closed_reason = $5, updated_at = now() WHERE id = $1`,
		t.ID, to, b.now(), branch, closed); err != nil {
		return err
	}
	// The bug a ticket fixes follows it (9.4).
	if err := bugFollows(ctx, tx, p, t, to, reason, who); err != nil {
		return err
	}
	var moveID int64
	if err := tx.QueryRow(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, reason, at) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		t.ID, t.Stage, to, who, nullIf(reason), b.now()).Scan(&moveID); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := tx.Exec(ctx, `INSERT INTO check_results (ticket, move, stage, name, passed, detail, at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			t.ID, moveID, t.Stage, r.Name, r.Passed, r.Detail, b.now()); err != nil {
			return err
		}
	}
	after := map[string]any{"stage": to}
	if reason != "" {
		after["reason"] = reason
	}
	if len(results) > 0 {
		after["checks"] = "passed"
		after["results"] = results
	}
	if branch != t.Branch {
		after["branch"] = *branch
	}
	for k, v := range merged {
		after[k] = v
	}
	if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, map[string]any{"stage": t.Stage}, after); err != nil {
		return err
	}
	// Its pull request (7.12): opened as it leaves In progress, told of its merge, closed with it.
	switch {
	case t.Stage == settings.StageInProgress && to != settings.StageClosed && to != settings.StageDone:
		if err := b.openPull(ctx, tx, p, &Ticket{ID: t.ID, Key: t.Key, Title: t.Title, Why: t.Why, What: t.What, Goal: t.Goal, Branch: branch}); err != nil {
			return err
		}
	case to == settings.StageDone && merged != nil:
		if err := pullPost(ctx, tx, p.ID, t.ID, "comment", map[string]any{"body": fmt.Sprintf("Crew merged %s on its machine as %v, after its checks passed on top of main. "+
			"The push of main marks this pull request merged.", t.Key, merged["main"])}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'merged', updated_at = now() WHERE ticket = $1 AND state IN ('opening', 'open')`, t.ID); err != nil {
			return err
		}
	case to == settings.StageClosed:
		if err := pullPost(ctx, tx, p.ID, t.ID, "close", map[string]any{}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'closed', updated_at = now() WHERE ticket = $1 AND state IN ('opening', 'open')`, t.ID); err != nil {
			return err
		}
	}
	t.Stage, t.Branch = to, branch
	return nil
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// BranchName is a ticket's branch: task/<ticket>-<slug> (6.1, 7.12).
func BranchName(t *Ticket) string {
	slug := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(t.Title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return "task/" + t.Key
	}
	return "task/" + t.Key + "-" + slug
}

// Reconcile is the board's part of the reconcile loop (15.2) for one project: the clocks of new
// features, skipped tickets whose condition is met, and every move whose checks pass and whose stage
// has room. It reads what is there first, so it can run any number of times without harm. A project
// that is not running moves nothing; a Done one only has its clocks read, since a skipped ticket's
// condition is often met just when the rest is done (7.6).
//
// A ticket whose step fails does not stop the others: its waiting says the board could not try it, the
// error is logged, the pass goes on, and the errors are returned together at the end.
func (b *Board) Reconcile(ctx context.Context, ref string) error {
	p, err := b.Project(ctx, ref)
	if err != nil {
		return err
	}
	if p.State != "running" && p.State != "done" {
		return nil
	}
	all, err := b.Tickets(ctx, p)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range all {
		if t.Stage != settings.StageAnalysis {
			continue
		}
		if err := b.clocks(ctx, p.ID, t.ID); err != nil {
			errs = append(errs, b.failed(ctx, t, err))
		}
	}
	if p, err = b.Project(ctx, p.ID); err != nil || p.State != "running" {
		return errors.Join(append(errs, err)...)
	}
	// Every stage from the right, so a ticket that leaves a full stage makes room for the one behind
	// it in the same pass; passes repeat while a ticket moved, so a ticket whose checks pass goes as
	// far as they let it.
	for pass := 0; pass < 50; pass++ {
		if all, err = b.Tickets(ctx, p); err != nil {
			return err
		}
		order, err := b.pullOrder(ctx, p, all)
		if err != nil {
			return err
		}
		moved := false
		for _, id := range order {
			m, err := b.advance(ctx, p.ID, id)
			if err != nil {
				t := &Ticket{ID: id}
				for _, x := range all {
					if x.ID == id {
						t = x
					}
				}
				errs = append(errs, b.failed(ctx, t, err))
				continue
			}
			moved = moved || m
		}
		if !moved {
			break
		}
	}
	if err := b.settle(ctx, p.ID); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// CouldNotTry is a ticket's waiting reason when a step of the board failed on it. It holds no error's
// text: an agent reads the reason through get_ticket, and an error can name the server's paths; the
// error goes to the server's log (13.8, the security review of 0.3.7, finding 2).
const CouldNotTry = "the board could not try it; the error is in Crew's server log"

// failed writes into a ticket's waiting that the board could not try it, logs the error, and returns it.
func (b *Board) failed(ctx context.Context, t *Ticket, err error) error {
	b.log().Error("the board could not try a ticket", "ticket", t.Key, "id", t.ID, "error", err)
	if _, werr := b.Pool.Exec(ctx, `UPDATE tickets SET waiting = $2 WHERE id = $1`, t.ID, CouldNotTry); werr != nil {
		b.log().Error("writing why a ticket waits", "ticket", t.Key, "error", werr)
	}
	return fmt.Errorf("%s: %w", t.Key, err)
}

// pullOrder is the order in which tickets are tried: the later a stage, the sooner; in Backlog a
// ticket that an approved ticket waits for first, then by priority and the order of the backlog (7.9);
// elsewhere by priority, then by how long it has been in its stage.
func (b *Board) pullOrder(ctx context.Context, p *Project, all []*Ticket) ([]int64, error) {
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return nil, err
	}
	pipe := PipelineOf(doc)
	needed := map[int64]bool{}
	rows, err := b.Pool.Query(ctx, `SELECT l.to_ticket FROM ticket_links l JOIN tickets f ON f.id = l.from_ticket
		WHERE f.project = $1 AND l.kind = 'depends_on' AND f.analysis->>'status' = 'ready' AND f.stage <> 'closed'`, p.ID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		needed[id] = true
	}
	var list []*Ticket
	for _, t := range all {
		if t.Stage != settings.StagePlanning && t.Stage != settings.StageDone && t.Stage != settings.StageClosed {
			list = append(list, t)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, c := list[i], list[j]
		if ia, ic := pipe.index(a.Stage), pipe.index(c.Stage); ia != ic {
			return ia > ic
		}
		if a.Stage == settings.StageBacklog && needed[a.ID] != needed[c.ID] {
			return needed[a.ID]
		}
		if a.Priority != c.Priority {
			return a.Priority < c.Priority
		}
		if a.Stage == settings.StageBacklog {
			return a.Position < c.Position
		}
		return a.StageAt.Before(c.StageAt)
	})
	out := make([]int64, len(list))
	for i, t := range list {
		out[i] = t.ID
	}
	return out, nil
}

// clocks applies what time and other tickets change in Analysis: a new feature whose notice has
// passed and that is not held is ready (7.3); a skipped ticket whose condition is met, and a ready
// ticket that depends on a skipped one, are analysed again (7.6, 7.9).
func (b *Board) clocks(ctx context.Context, project string, id int64) error {
	return b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, project, true)
		if err != nil || (p.State != "running" && p.State != "done") {
			return err
		}
		t, err := ticketByID(ctx, tx, id, true)
		if err != nil || t.Stage != settings.StageAnalysis {
			return err
		}
		switch t.Analysis.Status {
		case StatusNotice:
			// While Crew is paused no new feature goes ahead: its clock waits, and Resume moves it on
			// by the pause (7.9).
			if st, err := hold.Read(ctx, tx); err != nil || st.PausedAt != nil {
				return err
			}
			if !t.Hold && t.NoticeUntil != nil && !b.now().Before(*t.NoticeUntil) {
				return b.markReady(ctx, tx, p, t, Backend, "its notice passed")
			}
		case StatusSkipped:
			met, why, err := b.lookAgainMet(ctx, tx, p, t)
			if err != nil || !met {
				return err
			}
			return b.analyseAgain(ctx, tx, p, t, why)
		case StatusReady:
			d, err := dependencies(ctx, tx, t)
			if err != nil || len(d.skipped) == 0 {
				return err
			}
			return b.analyseAgain(ctx, tx, p, t, "it depends on "+strings.Join(d.skipped, ", "))
		}
		return nil
	})
}

func (b *Board) analyseAgain(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket, why string) error {
	a := t.Analysis
	a.Status, a.LookAgain = StatusWaiting, nil
	if err := updateTicket(ctx, tx, t.ID, map[string]any{"analysis": a}); err != nil {
		return err
	}
	if err := db.RecordProjectChange(ctx, tx, p.ID, Backend, "tickets", t.Key,
		map[string]any{"analysis": t.Analysis}, map[string]any{"analysis": a, "why": "analysed again: " + why}); err != nil {
		return err
	}
	// A ticket to analyse again makes a Done project run again, as a new ticket does (7.2).
	if p.State == "done" {
		if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'running', updated_at = now() WHERE id = $1`, p.ID); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, Backend, "projects", p.ID,
			map[string]any{"state": "done"}, map[string]any{"state": "running", "because": t.Key})
	}
	return nil
}

// lookAgainMet tests the condition of a skipped ticket (7.6).
func (b *Board) lookAgainMet(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket) (bool, string, error) {
	l := t.Analysis.LookAgain
	if l == nil {
		return false, "", nil
	}
	settled := `(stage IN ('done', 'closed') OR (stage = 'analysis' AND analysis->>'status' = 'skipped'))`
	var open int
	var err error
	switch l.Kind {
	case "date":
		at, perr := time.Parse(time.RFC3339, l.Value)
		if perr != nil {
			at, perr = time.ParseInLocation(time.DateOnly, l.Value, time.UTC)
		}
		return perr == nil && !b.now().Before(at), "the date " + l.Value + " has come", nil
	case "ticket_done":
		n, perr := ParseNumber(p, l.Value)
		if perr != nil {
			return false, "", nil
		}
		err = tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND number = $2 AND stage NOT IN ('done', 'closed')`, p.ID, n).Scan(&open)
		return err == nil && open == 0, l.Value + " is done", err
	case "part_done":
		var total int
		err = tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE stage NOT IN ('done', 'closed'))
			FROM tickets WHERE project = $1 AND part = $2 AND id <> $3`, p.ID, l.Value, t.ID).Scan(&total, &open)
		return err == nil && total > 0 && open == 0, "part " + l.Value + " is done", err
	case "all_done":
		err = tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND id <> $2 AND NOT `+settled, p.ID, t.ID).Scan(&open)
		return err == nil && open == 0, "everything else is done", err
	}
	return false, "", nil
}

// advance tries one move of one ticket, in its own transaction under the project's lock, so the
// counts its stage limits read cannot change under it. It returns whether the ticket moved. When it
// does not, the ticket's waiting says why.
func (b *Board) advance(ctx context.Context, project string, id int64) (bool, error) {
	moved := false
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, project, true)
		if err != nil || p.State != "running" {
			return err
		}
		t, err := ticketByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if t.Stage == settings.StagePlanning || t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return nil
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		pipe := PipelineOf(doc)
		wait := func(why string) error {
			_, err := tx.Exec(ctx, `UPDATE tickets SET waiting = $2 WHERE id = $1 AND waiting IS DISTINCT FROM $2`, t.ID, why)
			return err
		}
		if t.Hold {
			return wait("on hold")
		}
		cur, ok := pipe.Stage(t.Stage)
		if !ok {
			return wait("its stage " + t.Stage + " is no longer on the board")
		}
		next, ok := pipe.Next(t.Stage, t.Skips)
		if !ok {
			return nil
		}
		if t.Stage == settings.StageAnalysis && t.Analysis.Status != StatusReady {
			return wait(statusText(t))
		}
		if t.Stage == settings.StageAnalysis && NeedsAgreement(t) {
			return wait(AgreementWait(t))
		}
		// A ticket stays in In progress until every subtask agents created on it is done and joined into
		// its branch, and no join of the builder's runs (7.5, 7.11); superseded and cancelled ones wait
		// for nothing (7.7).
		if t.Stage == settings.StageInProgress {
			var open int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM subtasks WHERE ticket = $1 AND kind IN ('part', 'join') AND role <> 'crew'
				AND (state NOT IN ('done', 'cancelled', 'superseded') OR (kind = 'part' AND state = 'done' AND joined_at IS NULL))`, t.ID).Scan(&open); err != nil {
				return err
			}
			if open > 0 {
				return wait(fmt.Sprintf("%d of its subtasks are not done and joined yet", open))
			}
		}
		names := append(exitNames(pipe, cur, t), next.Enter...)
		c := &CheckContext{Tx: tx, Project: p, Ticket: t, Pipeline: pipe, Git: b.Git, Now: b.now()}
		results := make([]Result, 0, len(names))
		for _, name := range names {
			r, err := b.evaluate(ctx, c, name)
			if err != nil {
				return fmt.Errorf("%s of %s: %w", name, t.Key, err)
			}
			if !r.Passed {
				return wait(r.Detail)
			}
			results = append(results, r)
		}
		// The ticket that fixes the project's setup enters a stage that is full: the tickets that fill
		// it wait for it (13.5, since 0.47).
		var fixesSetup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM setup_checks WHERE ticket = $1)`, t.ID).Scan(&fixesSetup); err != nil {
			return err
		}
		if next.WIP > 0 && !fixesSetup {
			n, err := b.occupied(ctx, tx, p, next.ID)
			if err != nil {
				return err
			}
			if n >= next.WIP {
				return wait(fmt.Sprintf("%s is full (%d of %d)", stageName(next.ID), n, next.WIP))
			}
		}
		if err := b.move(ctx, tx, p, pipe, t, next.ID, Backend, "", results); err != nil {
			return err
		}
		moved = true
		return nil
	})
	return moved, err
}

// occupied counts the tickets that take a place in a stage's limit. In Analysis a skipped ticket, a
// new feature during its notice, and a ready ticket that waits only for a dependency do not, so
// waiting tickets cannot fill it (7.4).
func (b *Board) occupied(ctx context.Context, tx pgx.Tx, p *Project, stage string) (int, error) {
	list, err := tickets(ctx, tx, p.ID, stage)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range list {
		if stage == settings.StageAnalysis {
			if t.Analysis.Status == StatusSkipped || t.Analysis.Status == StatusNotice || t.Analysis.Status == StatusOperator {
				continue
			}
			if t.Analysis.Status == StatusReady || (t.Waiting != nil && strings.HasPrefix(*t.Waiting, "its analysis waits for ")) {
				d, err := dependencies(ctx, tx, t)
				if err != nil {
					return 0, err
				}
				if !d.met() {
					continue
				}
			}
		}
		n++
	}
	return n, nil
}

// StageName is a stage's name in the app.
func StageName(id string) string { return stageName(id) }

func stageName(id string) string {
	if n, ok := map[string]string{
		settings.StageBacklog: "Backlog", settings.StageAnalysis: "Analysis", settings.StageInProgress: "In progress",
		settings.StageReview: "Review", settings.StageTest: "Test", settings.StageE2E: "E2E",
		settings.StageVerification: "Verification", settings.StageDone: "Done",
	}[id]; ok {
		return n
	}
	return id
}

// settle marks a running project Done when every ticket is Done, closed, or skipped, and none waits
// for analysis (7.2). A ticket added later makes it run again (AddTicket). Crew's project is never Done
// (16.2, since 0.55): it has no plan, and its board takes tickets for as long as Crew runs.
func (b *Board) settle(ctx context.Context, project string) error {
	return b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, project, true)
		if err != nil || p.State != "running" {
			return err
		}
		crew, err := CrewProject(ctx, tx)
		if err != nil || crew == p.Name {
			return err
		}
		var total, open int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE NOT (stage IN ('done', 'closed')
				OR (stage = 'analysis' AND analysis->>'status' = 'skipped'))) FROM tickets WHERE project = $1`, p.ID).Scan(&total, &open); err != nil {
			return err
		}
		if total == 0 || open > 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'done', updated_at = now() WHERE id = $1`, p.ID); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, Backend, "projects", p.ID, map[string]any{"state": "running"}, map[string]any{"state": "done"})
	})
}

// ReconcileAll reconciles every project that is running or Done: a Done one for the clocks of its
// skipped tickets (Reconcile).
func (b *Board) ReconcileAll(ctx context.Context) error {
	ps, err := b.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		// The pages of main are indexed in every project that is not archived (9.9).
		if p.State != "archived" {
			if err := b.IndexDocs(ctx, p); err != nil {
				b.log().Error("indexing the documentation", "project", p.Name, "error", err)
			}
		}
		if p.State != "running" && p.State != "done" {
			continue
		}
		if err := b.Reconcile(ctx, p.ID); err != nil {
			b.log().Error("reconciling the board", "project", p.Name, "error", err)
		}
		if err := b.SyncPulls(ctx, p.ID); err != nil {
			b.log().Error("keeping the pull requests' branches pushed", "project", p.Name, "error", err)
		}
	}
	return nil
}

// Run reconciles every project on a timer and whenever Kick is called, until ctx ends. The pushes to
// the remotes go on beside it, so a remote that is slow holds up no board.
func (b *Board) Run(ctx context.Context, every time.Duration) {
	b.kickOnce()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			b.PushPending(ctx, "")
			b.PullsPending(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_ = b.ReconcileAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-b.kick:
		}
	}
}

// Kick asks for a reconcile soon: the loop runs on every board change (15.2).
func (b *Board) Kick() {
	b.kickOnce()
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

func (b *Board) kickOnce() {
	b.kickInit.Do(func() { b.kick = make(chan struct{}, 1) })
}
