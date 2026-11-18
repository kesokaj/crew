package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
)

// analysis starts the analysis of a ticket that waits for it (7.6, 15.2): for a ready plan or
// operator ticket whose command checks fail on main, Crew verifies the base checks directly without
// an LLM run; otherwise the curator analyses it, or a reviewer for a ticket the curator created,
// never the run that created it. An analysis that failed twice sends the ticket to the curator, who
// looks at it again (7.7).
func (p *Pipeline) analysis(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	st := t.Analysis.Status
	var failed int
	for _, s := range subs {
		if s.kind != kindAnalysis {
			continue
		}
		if s.active() {
			return nil
		}
		if s.created.After(t.StageAt) && (s.state == "stopped" || s.state == "failed") {
			failed++
		}
	}
	if st == board.StatusAnalysing {
		// Its analysis ended without a decision: it waits for analysis again.
		return p.Board.SetAnalysis(ctx, j.proj, t, board.StatusWaiting, "its analysis ended without a decision")
	}
	// A ticket on hold starts nothing (15.2); its analysis waits for it to be let go.
	if err := p.releaseIfFailureLimitRaised(ctx, j, t); err != nil {
		return err
	}
	if t.Hold || (st != board.StatusWaiting && st != board.StatusProposed) {
		return nil
	}
	if deps, err := p.openDependsOn(ctx, t); err != nil || len(deps) > 0 {
		if err != nil {
			return err
		}
		why := "its analysis waits for " + strings.Join(deps, ", ") + " to finish on main"
		if t.Waiting == nil || *t.Waiting != why {
			if err := p.Board.Wait(ctx, t, why); err != nil {
				return err
			}
			p.Board.Kick()
		}
		return nil
	}
	if failed >= 2 {
		return p.lookAgain(ctx, j, t, subs, "its analysis failed twice")
	}
	unresolved, err := p.Board.Unresolved(ctx, t)
	if err != nil {
		return err
	}
	if (t.Origin == board.OriginPlan || t.Origin == board.OriginOperator) && t.AdoptedAt == nil &&
		len(unresolved) == 0 && failed == 0 &&
		strings.TrimSpace(t.Why) != "" && strings.TrimSpace(t.What) != "" && strings.TrimSpace(t.Goal) != "" {
		if cmds := cmdOnly(t.Checks); len(cmds) > 0 {
			bug, err := p.Board.BugOf(ctx, t.ID)
			if err != nil {
				return err
			}
			if bug == nil || bug.InConfirmed {
				if done, err := p.fastAnalysis(ctx, j, t, cmds); err != nil || done {
					return err
				}
			}
		}
	}
	role := "curator"
	if t.Origin == board.OriginAgent && (strings.HasPrefix(t.CreatedBy, "curator") || strings.HasPrefix(t.CreatedBy, "lead")) {
		role = "reviewer"
	}
	plan, err := p.Board.Plan(ctx, j.proj)
	if err != nil {
		return err
	}
	original := plan.Original
	parts := p.ticketPrompt(ctx, promptInput{stage: "analysis", role: role, project: j.proj, ticket: t, plan: planPart(original, t),
		comments: unresolved, context: "Your workspace is the project's main, which you read and do not push."})
	run, err := p.start(ctx, j, t, startSpec{kind: kindAnalysis, role: role, title: "Analyse the ticket", parts: parts,
		readOnly: true, comments: unresolved})
	if err != nil || run == "" {
		return err
	}
	return p.Board.SetAnalysis(ctx, j.proj, t, board.StatusAnalysing, "analysed in run "+run)
}

// fastAnalysis runs the base command checks on main directly for a ready plan or operator ticket
// (7.6, R31) and approves it when every non-keeps command check fails on main. When a non-keeps
// command check passes on main, it returns false so the curator's analysis run rewrites the check.
func (p *Pipeline) fastAnalysis(ctx context.Context, j *job, t *board.Ticket, cmds []board.CheckSpec) (bool, error) {
	startBase := func() (bool, error) {
		main := p.Board.Git.Main(ctx, j.proj.Name)
		if main == "" {
			return true, errors.New("the project has no main")
		}
		_, err := p.runChecks(ctx, j, t, board.PurposeBase, main, "", "", checkCmds(cmds, ""))
		return true, err
	}
	var checkID int64
	err := p.Pool.QueryRow(ctx, `SELECT id FROM check_runs WHERE ticket = $1 AND purpose = 'base' AND `+notSetup+`
		AND started_at >= $2 ORDER BY id DESC LIMIT 1`, t.ID, t.StageAt).Scan(&checkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return startBase()
	}
	if err != nil {
		return true, err
	}
	run, err := p.checkRun(ctx, checkID)
	if err != nil || run.State == board.CheckRunning {
		return true, err
	}
	if run.State == board.CheckError {
		var tries int
		if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND purpose = 'base' AND state = 'error' AND `+notSetup+`
			AND started_at >= $2`, t.ID, t.StageAt).Scan(&tries); err != nil {
			return true, err
		}
		if tries < 3 {
			return startBase()
		}
		p.signal(ctx, j, t, "", "base_check_not_run", map[string]any{"detail": board.PublicDetail(run)})
		return true, p.Board.Wait(ctx, t, "Crew could not run its checks on main three times: "+board.PublicDetail(run))
	}
	keeps := map[string]bool{}
	for _, c := range cmds {
		keeps[c.Spec] = c.Keeps
	}
	for _, res := range run.Results {
		if res.Passed && !keeps[res.Name] {
			return false, nil
		}
	}
	for _, res := range run.Results {
		if !res.Passed && !keeps[res.Name] {
			if err := p.Board.SetRegressionCheck(ctx, t.ID, res.Name); err != nil {
				return true, err
			}
			break
		}
	}
	d := board.Decision{Choice: "approve"}
	if len(t.Docs) == 0 && (t.DocsReason == nil || strings.TrimSpace(*t.DocsReason) == "") {
		none := "none"
		d.DocsReason = &none
	}
	if _, err := p.Board.Decide(ctx, j.proj, t.Key, d, board.Backend); err != nil {
		var be *board.Error
		if errors.As(err, &be) {
			return false, nil
		}
		return true, err
	}
	p.signal(ctx, j, t, "", "analysis_decision", map[string]any{"choice": d.Choice, "origin": t.Origin, "fast": true})
	p.Board.Kick()
	return true, nil
}

// afterAnalysis acts on the analysis run's turn: with its decision, the decision's command checks run
// on main first, and a check that passes there before any work is refused (7.6, M6); then the
// decision is applied, and the run ends. A decision the board refuses goes back to the run, as a
// round of the goal loop.
func (p *Pipeline) afterAnalysis(ctx context.Context, j *job, t *board.Ticket, s *sub) error {
	r := s.reported
	if r == nil || r.Decision == nil {
		return p.missing(ctx, j, s)
	}
	d := *r.Decision
	if d.Choice == "approve" || d.Choice == "notice" {
		checks := d.Checks
		if checks == nil {
			checks = t.Checks
		}
		cmds := cmdOnly(checks)
		if len(cmds) > 0 {
			done, refusal, err := p.baseCheck(ctx, j, t, s, r, cmds)
			if err != nil || !done {
				return err
			}
			if refusal != "" {
				return p.round(ctx, j, t, s, refusal)
			}
		}
	}
	by := fmt.Sprintf("%s · %s", s.role, s.run)
	if _, err := p.Board.Decide(ctx, j.proj, t.Key, d, by); err != nil {
		var be *board.Error
		if errors.As(err, &be) {
			return p.round(ctx, j, t, s, "Crew could not apply your decision: "+be.Msg+". Decide again with analyse_ticket.")
		}
		return err
	}
	p.signal(ctx, j, t, s.run, "analysis_decision", map[string]any{"choice": d.Choice, "origin": t.Origin})
	if err := p.Runs.Finish(ctx, s.run); err != nil {
		return err
	}
	p.Board.Kick()
	return nil
}

// openDependsOn returns the keys of tickets t depends on that have not yet reached Done, been
// discarded, or been skipped, so t is analysed against main after its dependencies merge.
func (p *Pipeline) openDependsOn(ctx context.Context, t *board.Ticket) ([]string, error) {
	rows, err := p.Pool.Query(ctx, `SELECT p.key || '-' || o.number
		FROM ticket_links l
		JOIN tickets o ON o.id = l.to_ticket
		JOIN projects p ON p.id = o.project
		WHERE l.from_ticket = $1 AND l.kind = 'depends_on'
		  AND o.stage <> 'done'
		  AND coalesce(o.closed_reason, '') NOT IN ('discarded', 'cancelled')
		  AND coalesce(o.analysis->>'status', '') <> 'skipped'
		ORDER BY o.number`, t.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// cmdOnly are the non-keeps command checks of a list, which must fail on main before the ticket is built.
func cmdOnly(cs []board.CheckSpec) []board.CheckSpec {
	var out []board.CheckSpec
	for _, c := range cs {
		if c.Kind == "cmd" && !c.Keeps {
			out = append(out, c)
		}
	}
	return out
}

// baseCheck runs a decision's command checks on main as it is, once per decision, and says whether it
// has a result, and the refusal when a check that is not marked keeps passed there. A check run that
// could not tell is tried again twice; after that the decision goes ahead, and a signal says the
// base was not checked.
func (p *Pipeline) baseCheck(ctx context.Context, j *job, t *board.Ticket, s *sub, r *report, cmds []board.CheckSpec) (bool, string, error) {
	if r.BaseRun == 0 {
		main := p.Board.Git.Main(ctx, j.proj.Name)
		if main == "" {
			return false, "", errors.New("the project has no main")
		}
		id, err := p.runChecks(ctx, j, t, board.PurposeBase, main, "", "", checkCmds(cmds, ""))
		if err != nil {
			return false, "", err
		}
		r.BaseRun = id
		return false, "", p.setReported(ctx, s, r)
	}
	run, err := p.checkRun(ctx, r.BaseRun)
	if err != nil || run.State == board.CheckRunning {
		return false, "", err
	}
	if run.State == board.CheckError {
		// A count that cannot be read neither asks again nor ends the run: the next pass reads it again.
		var tries int
		if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND purpose = 'base' AND state = 'error' AND `+notSetup+`
			AND started_at >= $2`, t.ID, s.created).Scan(&tries); err != nil {
			return false, "", err
		}
		if tries < 3 {
			r.BaseRun = 0
			return false, "", p.setReported(ctx, s, r)
		}
		// A decision whose checks could not be run on main is not applied (7.6): the run ends as a
		// stop of Crew's own sandbox, is retried once, and then the ticket waits.
		p.signal(ctx, j, t, s.run, "base_check_not_run", map[string]any{"detail": board.PublicDetail(run)})
		if err := p.Board.Wait(ctx, t, "Crew could not run its checks on main three times: "+board.PublicDetail(run)); err != nil {
			return false, "", err
		}
		return false, "", p.Runs.End(ctx, s.run, runs.ReasonSandbox, "the check sandbox could not run the checks on main: "+board.PublicDetail(run))
	}
	keeps := map[string]bool{}
	for _, c := range cmds {
		keeps[c.Spec] = c.Keeps
	}
	var passed []string
	for _, res := range run.Results {
		if res.Passed && !keeps[res.Name] {
			passed = append(passed, res.Name)
		}
	}
	if len(passed) == 0 {
		// A bug's fix ticket keeps the first of its checks that failed before the fix, which proves the
		// bug stays fixed (9.4); for any other ticket this changes nothing.
		for _, res := range run.Results {
			if !res.Passed && !keeps[res.Name] {
				if err := p.Board.SetRegressionCheck(ctx, t.ID, res.Name); err != nil {
					return false, "", err
				}
				break
			}
		}
		return true, "", nil
	}
	return true, fmt.Sprintf("Crew ran your command checks on main (%s), before any work, in a check sandbox. These passed there, so they "+
		"cannot show that the goal is met: %s. Write checks that fail until the work is done, such as a test this ticket adds; mark a "+
		"check keeps only when it must keep passing, and keep at least one check that is not. Then call analyse_ticket again.",
		run.Commit[:12], strings.Join(passed, "; ")), nil
}

// checkRoundsWarn is the round of checks in one run that is warned of (8.2).
const checkRoundsWarn = 3

// round gives the run the reason its report was not taken, as one more round of its goal loop (11.3);
// after limits.max_goal_rounds the run ends as a stop, and its subtask is retried once.
func (p *Pipeline) round(ctx context.Context, j *job, t *board.Ticket, s *sub, text string) error {
	max := int(num(j.doc, "limits.max_goal_rounds", 5))
	if s.goalRounds+1 >= max {
		p.signal(ctx, j, t, s.run, "check_round", map[string]any{"kind": s.kind, "round": s.goalRounds + 1, "last": true})
		why := fmt.Sprintf("%d rounds of checks failed", s.goalRounds+1)
		if s.waitedOut() {
			return p.Runs.EndWaited(ctx, s.run, runs.ReasonRounds, why)
		}
		return p.Runs.End(ctx, s.run, runs.ReasonRounds, why)
	}
	// The third round of checks in one run is a warning on its card (8.2).
	if s.goalRounds+1 == checkRoundsWarn {
		p.Runs.Warn(ctx, s.run, fmt.Sprintf("%d rounds of checks in this run", checkRoundsWarn))
	}
	// The report is cleared, and the round counted and recorded as a signal, with the turn that is
	// given: a turn that cannot start now keeps it, and adds nothing (10.2).
	if s.kind == kindBuild || s.kind == kindFix {
		t.TriesUsed++
	}
	return p.continueRun(ctx, j, s, text, func(ctx context.Context, tx pgx.Tx, _ string) error {
		if _, err := tx.Exec(ctx, `UPDATE subtasks SET goal_rounds = goal_rounds + 1, reported = NULL WHERE id = $1`, s.id); err != nil {
			return err
		}
		if s.kind == kindBuild || s.kind == kindFix {
			if _, err := tx.Exec(ctx, `UPDATE tickets SET tries_used = tries_used + 1 WHERE id = $1`, t.ID); err != nil {
				return err
			}
		}
		return board.RecordSignal(ctx, tx, board.Signal{Project: j.proj.ID, Ticket: t.ID, Run: s.run, Kind: "check_round",
			Detail: map[string]any{"kind": s.kind, "round": s.goalRounds + 1}})
	})
}

func derefS(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
