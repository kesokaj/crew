package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/settings"
)

// setupRoles are the roles whose sandbox runs the project's setup before its CLI (11.2, since 0.47),
// as the run driver gives it to them.
var setupRoles = map[string]bool{"builder": true, "tester": true, "e2e": true}

// notSetup leaves out of a count of check runs that ended as errors the ones whose setup failed, while
// Crew still looks after the setup on main: those wait for its check there and are tried again (13.5).
// Once Crew has stopped trying, nothing would end them, so they count as Crew's errors and the ticket
// waits after three (since 1.12). It reads the run's mark, never its detail, which begins with a
// command an agent named (13.9).
var notSetup = `(NOT setup_failed OR ` + stoppedTrying("check_runs.project") + `)`

// maxSetupErrors is how often Crew tries to run the setup on main when its check sandbox could not
// tell, before it stops trying and says so.
const maxSetupErrors = 3

// stoppedTrying is SQL that is true where Crew has stopped trying to run the setup on main for the
// project in column: maxSetupErrors checks there could not tell after the last that passed or failed.
// watchSetup and notSetup read the same.
func stoppedTrying(column string) string {
	return fmt.Sprintf(`((SELECT count(*) FROM setup_checks e WHERE e.project = %[1]s AND e.state = 'error'
		AND e.through > (SELECT coalesce(max(d.through), 'epoch') FROM setup_checks d WHERE d.project = %[1]s AND d.state IN ('passed', 'failed'))) >= %[2]d)`,
		column, maxSetupErrors)
}

// setupTicket is the project's open ticket that fixes its setup, or nil (13.5): while it is open, the
// runs that need the setup wait for it.
func (p *Pipeline) setupTicket(ctx context.Context, proj *board.Project) (*board.Ticket, error) {
	var number int
	err := p.Pool.QueryRow(ctx, `SELECT t.number FROM setup_checks c JOIN tickets t ON t.id = c.ticket
		WHERE c.project = $1 AND t.stage NOT IN ('done', 'closed') ORDER BY c.id DESC LIMIT 1`, proj.ID).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p.Board.Ticket(ctx, proj, fmt.Sprintf("%s-%d", proj.Key, number))
}

// waitsForSetup says why a run of role on t waits for the project's setup to be fixed, or "".
func (p *Pipeline) waitsForSetup(ctx context.Context, j *job, t *board.Ticket, role string) (string, error) {
	if !setupRoles[role] && role != "check" {
		return "", nil
	}
	fix, err := p.setupTicket(ctx, j.proj)
	if err != nil {
		return "", err
	}
	if fix == nil {
		// While Crew runs the setup on main, what needs it waits for the answer.
		var checking bool
		if err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM setup_checks WHERE project = $1 AND state = 'checking')`,
			j.proj.ID).Scan(&checking); err != nil || !checking {
			return "", err
		}
		return "waits while Crew runs the project's setup on main: it failed twice in a row", nil
	}
	if t != nil && t.ID == fix.ID {
		return "", nil
	}
	return "waits for " + fix.Key + ", which fixes the project's setup: it fails on main", nil
}

// watchSetup looks after the project's setup once a pass (21, 12.4, 13.5). Two runs in a row that
// ended at their setup, of the project, of one subtask or of one ticket's checks, have the setup run on
// main in a check sandbox. Where it fails there too, one
// fix ticket is made, with news, and the runs that need the setup wait for it until it is Done or
// closed. Where it passes there, the tickets' own branches broke it: each failure counts as a stop of
// its subtask, which a second stop sends to the lead (7.7).
func (p *Pipeline) watchSetup(ctx context.Context, j *job) error {
	setup := strings.TrimSpace(str(j.doc, "sandbox.setup"))
	if fix, err := p.setupTicket(ctx, j.proj); err != nil || fix != nil {
		return err
	}
	var id int64
	var state string
	var acted *time.Time
	var ticket *int64
	err := p.Pool.QueryRow(ctx, `SELECT id, state, acted_at, ticket FROM setup_checks WHERE project = $1 ORDER BY id DESC LIMIT 1`, j.proj.ID).
		Scan(&id, &state, &acted, &ticket)
	switch {
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return err
	case err == nil && state == "checking":
		return nil
	case err == nil && acted == nil:
		return p.actOnSetup(ctx, j, id, state)
	case err == nil && state == "failed" && ticket != nil:
		// Its fix ticket is Done or closed: the runs that waited for it go on, and a setup that still
		// fails is found again.
		if err := p.releaseSetup(ctx, j); err != nil {
			return err
		}
	}
	if setup == "" || j.held != "" {
		return nil
	}
	// Two runs in a row, after the last decided check, that ended at their setup.
	var since time.Time
	if err := p.Pool.QueryRow(ctx, `SELECT coalesce(max(through), 'epoch') FROM setup_checks
		WHERE project = $1 AND state IN ('passed', 'failed')`, j.proj.ID).Scan(&since); err != nil {
		return err
	}
	var stopped bool
	if err := p.Pool.QueryRow(ctx, `SELECT `+stoppedTrying("p.id")+` FROM projects p WHERE p.id = $1`, j.proj.ID).Scan(&stopped); err != nil {
		return err
	}
	if stopped {
		// Crew could not tell, and stops trying: nothing new waits for it, as waitsForSetup holds runs
		// only while a check on main runs or a fix ticket is open, and a check that ends at the setup
		// counts in its three tries (notSetup); a subtask that already waits for the setup waits on
		// (13.5). The operator hears it once (review of M13).
		var told int
		if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE project = $1 AND kind = 'setup_failed' AND detail->>'since' = $2`,
			j.proj.ID, since.UTC().Format(time.RFC3339Nano)).Scan(&told); err != nil {
			return err
		}
		if told == 0 {
			p.log().Warn("the project's setup could not be run on main three times", "project", j.proj.Name)
			p.signal(ctx, j, nil, "", "setup_failed", map[string]any{"since": since.UTC().Format(time.RFC3339Nano), "text": fmt.Sprintf(
				"Crew could not run the setup of %s on main %d times and stops trying: new runs start without waiting for it, a check that ends at the setup counts as Crew's error, and a run that already waits for the setup waits on",
				j.proj.Name, maxSetupErrors)})
		}
		return nil
	}
	// Two runs of the project in a row, or two of one subtask, after the last decided check.
	var reasons []string
	var latest time.Time
	// A check sandbox runs the setup too; the fix ticket's own branch checks are left out, while its
	// check on main after a merge on GitHub (purpose 'outside') counts as a check of main.
	rows, err := p.Pool.Query(ctx, `SELECT reason, at FROM (
			SELECT coalesce(r.stop_reason, '') AS reason, r.started_at AS at FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE s.project = $1 AND s.role IN ('builder', 'tester', 'e2e') AND r.started_at > $2
			UNION ALL
			SELECT CASE WHEN c.state = 'error' AND c.setup_failed THEN 'setup' ELSE c.state END, c.started_at FROM check_runs c
			WHERE c.project = $1 AND c.started_at > $2 AND c.state <> 'running'
			AND (c.ticket IS NULL OR c.purpose = 'outside' OR c.ticket NOT IN (SELECT ticket FROM setup_checks WHERE project = $1 AND ticket IS NOT NULL))
		) x ORDER BY at DESC LIMIT 2`, j.proj.ID, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var reason string
		var at time.Time
		if err := rows.Scan(&reason, &at); err != nil {
			rows.Close()
			return err
		}
		reasons = append(reasons, reason)
		if at.After(latest) {
			latest = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	inARow := len(reasons) == 2 && reasons[0] == "setup" && reasons[1] == "setup"
	var twice *time.Time
	if err := p.Pool.QueryRow(ctx, `SELECT max(r.started_at) FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1 AND s.state = 'waiting' AND s.wait_for = 'setup' AND r.stop_reason = 'setup' AND r.started_at > $2`,
		j.proj.ID, since).Scan(&twice); err != nil {
		return err
	}
	// Two check runs in a row of one open ticket that ended at their setup, or two 'outside' check runs
	// of a ticket merged on GitHub, which the runs of other tickets between them do not hide (the setup's
	// open fix ticket returned above, and a closed one is not open): else a branch that breaks the setup
	// in its own check sandbox would be tried again without end in a busy project (the security review of
	// 1.12's change, finding 1, and register row 12). It is asked only when nothing above starts the
	// check, so its error holds back no other.
	if !inARow && twice == nil {
		var ticketTwice *time.Time
		if err := p.Pool.QueryRow(ctx, `SELECT max(at) FROM (SELECT max(at) AS at FROM (
				SELECT c.ticket, c.started_at AS at, c.state = 'error' AND c.setup_failed AS marked,
					row_number() OVER (PARTITION BY c.ticket ORDER BY c.started_at DESC, c.id DESC) AS n
				FROM check_runs c JOIN tickets t ON t.id = c.ticket
				WHERE c.project = $1 AND c.started_at > $2 AND c.state <> 'running'
				AND ((t.stage NOT IN ('done', 'closed') AND (c.purpose = 'outside' OR c.ticket NOT IN (SELECT ticket FROM setup_checks WHERE project = $1 AND ticket IS NOT NULL)))
				  OR (t.stage = 'done' AND c.purpose = 'outside'))
				AND c.purpose NOT IN ('judge', 'stage_judge')
			) x WHERE n <= 2 GROUP BY ticket HAVING count(*) = 2 AND bool_and(marked)) y`, j.proj.ID, since).Scan(&ticketTwice); err != nil {
			return err
		}
		if ticketTwice == nil {
			return nil
		}
		if ticketTwice.After(latest) {
			latest = *ticketTwice
		}
	}
	// A ticket's two are among the project's runs that latest already covers.
	if twice != nil && twice.After(latest) {
		latest = *twice
	}
	return p.checkSetup(ctx, j, setup, latest)
}

// checkSetup runs the project's setup on main in a check sandbox, in the background, and records how
// it ended; the next pass acts on it.
func (p *Pipeline) checkSetup(ctx context.Context, j *job, setup string, through time.Time) error {
	main := p.Board.Git.Main(ctx, j.proj.Name)
	if main == "" {
		return errors.New("the project has no main")
	}
	var id int64
	if err := p.Pool.QueryRow(ctx, `INSERT INTO setup_checks (project, commit, through) VALUES ($1, $2, $3) RETURNING id`,
		j.proj.ID, main, through).Scan(&id); err != nil {
		return err
	}
	p.log().Info("two runs in a row failed at the project's setup: it is run on main", "project", j.proj.Name, "main", main[:12])
	proj := j.proj
	timeout := time.Duration(num(j.doc, "limits.check_timeout_minutes", 30)) * time.Minute
	sizing := p.largeSizing(ctx)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.Kick()
		bg := context.WithoutCancel(ctx)
		end := func(state, detail string) {
			if _, err := p.Pool.Exec(bg, `UPDATE setup_checks SET state = $2, detail = nullif($3, ''), decided_at = now() WHERE id = $1`,
				id, state, detail); err != nil {
				p.log().Error("recording the check of the project's setup", "error", err)
			}
		}
		var tree bytes.Buffer
		if err := p.Board.Git.Archive(bg, proj.Name, main, &tree); err != nil {
			end("error", "the tree to check: "+err.Error())
			return
		}
		mode, err := settings.CheckNetworkMode(bg, p.Pool, proj.ID, 0)
		if err != nil {
			end("error", err.Error())
			return
		}
		// The setup runs before the commands; one command that does nothing says how it ended.
		res, err := p.Checks.ExecFresh(bg, harness.CheckSpec{Project: proj.ID, Mode: mode, Tree: tree.Bytes(), Setup: setup, Timeout: timeout, Sizing: sizing,
			Cmds: []harness.CheckCmd{{Name: "the project's setup on main", Run: "true"}}})
		switch {
		case err != nil:
			end("error", err.Error())
		case len(res) == 1 && strings.HasPrefix(res[0].Error, harness.SetupError):
			// The fix ticket shows a setup the runtime could not run as CouldNotCheck: the error is here.
			if board.PublicError(res[0].Error) != res[0].Error {
				p.log().Error("the project's setup on main ended as Crew's own error", "project", proj.Name, "setupCheck", id, "error", res[0].Error)
			}
			end("failed", res[0].Error+"\n"+res[0].Output)
		case len(res) == 1 && res[0].Error == "" && res[0].Passed:
			end("passed", "")
		default:
			detail := "the check sandbox could not tell"
			if len(res) == 1 {
				detail = res[0].Error
			}
			end("error", detail)
		}
	}()
	return nil
}

// actOnSetup acts once on a decided check of the setup on main.
func (p *Pipeline) actOnSetup(ctx context.Context, j *job, id int64, state string) error {
	var through time.Time
	var detail *string
	if err := p.Pool.QueryRow(ctx, `SELECT through, detail FROM setup_checks WHERE id = $1`, id).Scan(&through, &detail); err != nil {
		return err
	}
	switch state {
	case "failed":
		setup := str(j.doc, "sandbox.setup")
		testCmd := str(j.doc, "sandbox.test")
		// The setup's own end and its output, which the fix needs; an error of Crew's that the
		// runtime gave is CouldNotCheck, since agents read the ticket (13.9).
		errLine, output, found := strings.Cut(derefS(detail), "\n")
		seen := board.PublicError(errLine)
		if found {
			seen += "\n" + output
		}
		// A fix ticket made on a pass whose record of it failed is found again, not made twice.
		// A lookup that fails makes no ticket, which would be a second one (BUG-012's rule).
		var have int64
		switch err := p.Pool.QueryRow(ctx, `SELECT t.id FROM tickets t WHERE t.project = $1 AND t.created_by = $2 AND t.stage NOT IN ('done', 'closed')
			ORDER BY t.id LIMIT 1`, j.proj.ID, board.Backend+" · the project's setup").Scan(&have); {
		case err == nil:
			_, err := p.Pool.Exec(ctx, `UPDATE setup_checks SET ticket = $2, acted_at = now() WHERE id = $1`, id, have)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		what := fmt.Sprintf("Make the setup command `%s` succeed in a fresh sandbox, on main as it is, without leaving out what it sets up.", setup)
		goal := fmt.Sprintf("`%s` succeeds in a fresh sandbox.", setup)
		if testCmd != "" && testCmd != setup {
			what = fmt.Sprintf("Make the setup command `%s` and the test command `%s` succeed in a fresh sandbox, on main as it is, without leaving out what they set up or test.", setup, testCmd)
			goal = fmt.Sprintf("Both `%s` and `%s` succeed in a fresh sandbox.", setup, testCmd)
		}
		t, err := p.Board.AddTicket(ctx, j.proj.ID, board.NewTicket{Title: "Fix the project's setup", Kind: "fix",
			Why: "Two runs in a row failed before their agent started, because the project's setup failed in their new sandbox. " +
				"Crew ran it on main in a check sandbox, and it fails there too: every run that builds or runs the code waits for this ticket.",
			What: what,
			Goal: goal, Seen: &seen},
			board.OriginAgent, board.Backend+" · the project's setup")
		if err != nil {
			return err
		}
		if _, err := p.Pool.Exec(ctx, `UPDATE setup_checks SET ticket = $2, acted_at = now() WHERE id = $1`, id, t.ID); err != nil {
			return err
		}
		p.signal(ctx, j, t, "", "setup_failed", map[string]any{"text": fmt.Sprintf(
			"The setup of %s fails, also on main: %s fixes it, and the runs that need it wait for it", j.proj.Name, t.Key)})
		p.log().Info("the project's setup fails on main: a fix ticket", "project", j.proj.Name, "ticket", t.Key)
	case "passed":
		// The branches broke it: each run that failed at its setup is a stop of its subtask.
		if err := pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE subtasks s SET stops = s.stops + f.n, updated_at = now(),
					state = CASE WHEN s.stops + f.n >= 2 THEN 'stopped' ELSE s.state END,
					wait_for = CASE WHEN s.stops + f.n >= 2 THEN 'operator' ELSE 'retry' END
				FROM (SELECT r.subtask, count(*) AS n FROM runs r JOIN subtasks x ON x.id = r.subtask
					WHERE x.project = $1 AND r.stop_reason = 'setup' AND r.started_at <= $2
					AND NOT EXISTS (SELECT 1 FROM setup_checks c WHERE c.project = $1 AND c.id < $3 AND c.state IN ('passed', 'failed') AND r.started_at <= c.through)
					GROUP BY r.subtask) f
				WHERE s.id = f.subtask AND s.state = 'waiting' AND s.wait_for IN ('setup', 'retry')`, j.proj.ID, through, id); err != nil {
				return err
			}
			// A check sandbox whose setup failed on a branch failed its check: the branch's builder
			// fixes it, in a fix round.
			if _, err := tx.Exec(ctx, `UPDATE check_runs SET state = 'failed',
					detail = detail || '; it passes on main, so this branch breaks it'
				WHERE project = $1 AND state = 'error' AND setup_failed AND started_at <= $2`, j.proj.ID, through); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE setup_checks SET acted_at = now() WHERE id = $1`, id)
			return err
		}); err != nil {
			return err
		}
		p.log().Info("the project's setup passes on main: the failures are the tickets' own", "project", j.proj.Name)
	default:
		// The check could not tell: it is tried again on a later pass, a few times.
		_, err := p.Pool.Exec(ctx, `UPDATE setup_checks SET acted_at = now() WHERE id = $1`, id)
		return err
	}
	return nil
}

// releaseSetup lets the runs that waited for the setup's fix go on. A ticket's branch, cut from main
// before the fix, is brought up to date with main first, as a join of main into it, so its new sandbox
// runs the fixed setup; a branch that conflicts with main stays as it is, and a setup that fails there
// is its own (13.5, since 0.47).
func (p *Pipeline) releaseSetup(ctx context.Context, j *job) error {
	rows, err := p.Pool.Query(ctx, `SELECT DISTINCT t.branch FROM subtasks s JOIN tickets t ON t.id = s.ticket
		WHERE s.project = $1 AND s.state = 'waiting' AND s.wait_for = 'setup' AND t.branch IS NOT NULL`, j.proj.ID)
	if err != nil {
		return err
	}
	branches, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(branches) > 0 {
		id, err := p.Identity(ctx)
		if err != nil {
			return err
		}
		for _, b := range branches {
			jn, err := p.Board.Git.JoinMain(ctx, j.proj.Name, b, "Merge main, with the fixed setup, into "+b, id)
			switch {
			case errors.Is(err, gitstore.ErrNoHead):
				continue
			case err != nil:
				return err
			case jn.Commit != "":
				if err := p.Board.Git.AdvanceBranch(ctx, j.proj.Name, b, jn.Commit); err != nil {
					p.log().Warn("a branch was not brought up to date with the fixed setup", "branch", b, "error", err)
				}
			}
		}
	}
	_, err = p.Pool.Exec(ctx, `UPDATE subtasks SET wait_for = 'retry', updated_at = now()
		WHERE project = $1 AND state = 'waiting' AND wait_for = 'setup'`, j.proj.ID)
	return err
}
