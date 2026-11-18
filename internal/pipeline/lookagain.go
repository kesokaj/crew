package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/settings"
)

// kindLookAgain is the lead's run on a ticket that cannot get through (7.7).
const kindLookAgain = "look_again"

// maxReplacements is how many tickets one cancelled ticket may become.
const maxReplacements = 6

// maxLookAgainErrors is how many look-again subtasks ended by crew-run's own step Crew tries on a
// ticket before the ticket waits for the operator (7.7, 21).
const maxLookAgainErrors = 3

// lookAgain sends a ticket that cannot get through to the lead (7.7): its run stopped twice, it used
// its fix rounds, or its work did not join. One look again runs at a time; a look again whose stop
// counted at crew-run's own step (end_step set) is Crew's error and is tried up to three look agains
// before the ticket waits for the operator; one that stopped twice for the lead's own reasons cancels
// the ticket with that reason (21). The ticket waits meanwhile, and says why.
func (p *Pipeline) lookAgain(ctx context.Context, j *job, t *board.Ticket, subs []*sub, why string) error {
	var la *sub
	for _, s := range subs {
		if s.kind == kindLookAgain {
			la = s
		}
	}
	if la != nil && la.active() {
		return nil
	}
	if held, err := p.holdIfFailureLimitReached(ctx, j, t, why); err != nil || held {
		return err
	}
	if la != nil && why == fmt.Sprintf("its subtask %s.%d %s stopped twice", t.Key, la.number, la.title) {
		why = strings.TrimPrefix(la.title, "Look again: ")
		for _, s := range subs {
			if s.kind != kindLookAgain && s.state == "stopped" {
				why = fmt.Sprintf("its subtask %s.%d %s stopped twice", t.Key, s.number, s.title)
			}
		}
	}
	if la != nil && la.state == "stopped" {
		step, tries, err := p.lookAgainErrorStep(ctx, t.ID, la.id)
		if err != nil {
			return err
		}
		if step == "" {
			reason := "the lead's look again stopped twice, so nobody decided how it goes on: " + why
			_, err := p.Board.Cancel(ctx, j.proj, t.Key, reason, board.Backend)
			return err
		}
		if tries >= maxLookAgainErrors {
			return p.Board.Wait(ctx, t, fmt.Sprintf("Crew could not run the lead's look again three times (step %s): the operator decides how it goes on", step))
		}
	}
	if err := p.Board.Wait(ctx, t, "the lead looks at it again: "+why); err != nil {
		return err
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return err
	}
	stopped, err := p.stoppedRuns(ctx, t)
	if err != nil {
		return err
	}
	ctxText := fmt.Sprintf("The ticket is in %s, with %d fix rounds", t.Stage, t.FixRounds)
	if t.Approaches > 0 {
		ctxText += ", and it had its new approach already: now cancel it and replace it"
	}
	if t.IsReplacement() {
		ctxText += ". It replaces a cancelled ticket, so it is not replaced again: cancel it with no tickets, and say what is missing"
	}
	branch := ""
	if t.Branch != nil {
		branch = *t.Branch
		head, err := p.Board.Head(ctx, j.proj, t)
		if err != nil {
			return err
		}
		if head == "" {
			ctxText += ". Its branch " + branch + " was never pushed: your workspace is the project's main, which you only read"
		} else {
			ctxText += ". Your workspace is a clone of its branch " + branch + ", which you only read"
		}
	}
	parts := p.ticketPrompt(ctx, promptInput{stage: "look_again", role: "curator", project: j.proj, ticket: t, records: recs,
		extra: []extra{{name: "Why Crew sent it to you", who: "Crew, naming subtasks agents titled", text: why},
			{name: "What the stopped runs did", who: "Crew, quoting what the runs recorded", text: stopped}},
		context: ctxText + "."})
	run, err := p.start(ctx, j, t, startSpec{kind: kindLookAgain, role: "curator", title: "Look again: " + short(why, 80),
		parts: parts, branch: branch, readOnly: true})
	if err != nil || run == "" || la == nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE subtasks SET state = 'superseded', wait_for = NULL, updated_at = now() WHERE id = $1 AND state = 'stopped'`, la.id)
	return err
}

// lookAgainErrorStep returns the step of crew-run's own (runspec.StepDetail) at which a counted
// stop of subID ended, or "" when subID stopped for the lead's own reasons, and how many look-again
// subtasks of the ticket had a counted stop at a step of crew-run's own.
func (p *Pipeline) lookAgainErrorStep(ctx context.Context, ticket, subID int64) (string, int, error) {
	rows, err := p.Pool.Query(ctx, `SELECT s.id, coalesce(r.end_step, '')
		FROM subtasks s JOIN runs r ON r.subtask = s.id
		WHERE s.ticket = $1 AND s.kind = 'look_again' AND s.state IN ('stopped', 'superseded')
			AND r.state = 'ended'
			AND coalesce(r.stop_reason, 'ok') NOT IN ('ok', 'operator', 'restart', 'login', 'quota', 'model', 'setup')
		ORDER BY s.number, r.number`, ticket)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	bySub := map[int64]string{}
	for rows.Next() {
		var id int64
		var step string
		if err := rows.Scan(&id, &step); err != nil {
			return "", 0, err
		}
		if _, ok := runspec.StepDetail(step); ok {
			bySub[id] = step
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return bySub[subID], len(bySub), nil
}

// stoppedRuns is Crew's summary of what the ticket's stopped runs did, not their transcripts (8.2).
func (p *Pipeline) stoppedRuns(ctx context.Context, t *board.Ticket) (string, error) {
	rows, err := p.Pool.Query(ctx, `SELECT s.number, s.role, coalesce(s.title, ''), r.number, coalesce(r.model, ''), coalesce(r.stop_reason, ''),
			coalesce(r.stop_detail, ''), coalesce(r.last_action, ''), r.turn, r.tool_steps
		FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.ticket = $1 AND r.state = 'ended' AND coalesce(r.stop_reason, 'ok') NOT IN ('ok', 'operator', 'restart', 'login', 'quota', 'model')
		ORDER BY r.started_at DESC LIMIT 10`, t.ID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var sn, rn, turns, steps int
		var role, title, model, reason, detail, last string
		if err := rows.Scan(&sn, &role, &title, &rn, &model, &reason, &detail, &last, &turns, &steps); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- %s.%d %s (%s), run %d on %s: stopped (%s) after %d turns and %d tool steps. %s",
			t.Key, sn, title, role, rn, model, reason, turns, steps, short(detail, 300))
		if last != "" {
			fmt.Fprintf(&b, " Its last action: %s.", short(last, 200))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "No run of the ticket stopped: it used its rounds.", rows.Err()
	}
	return b.String(), rows.Err()
}

// afterLookAgain applies the lead's call (7.7). A new approach is carried out by a builder of its
// own in the ticket's stage; cancel and replace closes the ticket with its replacements. Each step
// looks first, so a pass that is tried again does it once. A call the board refuses goes back to
// the run as a round of its goal loop.
func (p *Pipeline) afterLookAgain(ctx context.Context, j *job, t *board.Ticket, subs []*sub, s *sub) error {
	if t.Stage == "closed" || t.Stage == "done" {
		return p.Runs.Finish(ctx, s.run)
	}
	r := s.reported
	if r == nil {
		return p.missing(ctx, j, s)
	}
	switch r.Tool {
	case "new_approach":
		if t.ApproachAt == nil || t.ApproachAt.Before(s.created) {
			now, err := p.Board.NewApproach(ctx, j.proj, t.Key, r.Reason, fmt.Sprintf("%s · %s", s.role, s.run))
			if err != nil {
				return p.refusedCall(ctx, j, t, s, err)
			}
			t = now
			p.signal(ctx, j, t, s.run, "look_again", map[string]any{"choice": "new_approach"})
		}
		for _, x := range subs {
			if (x.kind == kindBuild || x.kind == kindFix) && x.created.After(*t.ApproachAt) {
				return p.Runs.Finish(ctx, s.run)
			}
		}
		run, err := p.approach(ctx, j, t, subs, r)
		if err != nil || run == "" {
			return err
		}
		return p.Runs.Finish(ctx, s.run)
	case "cancel_and_replace":
		if _, err := p.Board.CancelAndReplace(ctx, j.proj, t.Key, r.Reason, r.Replacements, fmt.Sprintf("%s · %s", s.role, s.run)); err != nil {
			return p.refusedCall(ctx, j, t, s, err)
		}
		p.signal(ctx, j, t, s.run, "look_again", map[string]any{"choice": "cancel_and_replace", "replacements": len(r.Replacements)})
		p.Board.Kick()
		return p.Runs.Finish(ctx, s.run)
	}
	return p.missing(ctx, j, s)
}

// refusedCall gives a call the board refused back to the run, as a round of its goal loop.
func (p *Pipeline) refusedCall(ctx context.Context, j *job, t *board.Ticket, s *sub, err error) error {
	var be *board.Error
	if errors.As(err, &be) {
		if err := p.setReported(ctx, s, nil); err != nil {
			return err
		}
		return p.round(ctx, j, t, s, "Crew could not apply your call: "+be.Msg+". Call new_approach or cancel_and_replace again.")
	}
	return err
}

// approach starts the builder that carries out the lead's new approach, in the ticket's stage, on
// its branch, in a conversation of its own: it is not a fix round (7.7). In In progress it runs as
// a build, so it hands over with request_move and goes through In progress's exit checks and judge;
// in a later stage it runs in place with fix_done.
func (p *Pipeline) approach(ctx context.Context, j *job, t *board.Ticket, subs []*sub, r *report) (string, error) {
	if t.Branch == nil {
		return "", nil
	}
	head, err := p.Board.Head(ctx, j.proj, t)
	if err != nil {
		return "", err
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return "", err
	}
	stage, kind, plan := "fix", kindFix, ""
	if t.Stage == settings.StageInProgress {
		stage, kind = "in_progress", kindBuild
		if plan, err = p.planOf(ctx, j, t); err != nil {
			return "", err
		}
	}
	parts := p.ticketPrompt(ctx, promptInput{stage: stage, role: "builder", project: j.proj, ticket: t, plan: plan, records: recs,
		extra: []extra{{name: "The lead's new approach", who: "the lead, which looked at the ticket again",
			text: "Why it kept failing: " + r.Reason + "\n\nThe approach to take now:\n" + r.Approach}},
		context: fmt.Sprintf("The ticket is in %s. The lead gave it a new approach: carry it out. Your workspace is a clone of its branch %s, which you push.",
			t.Stage, *t.Branch)})
	return p.start(ctx, j, t, startSpec{kind: kind, role: "builder", title: "New approach: " + short(r.Reason, 80), parts: parts,
		branch: *t.Branch, commit: head})
}

// The tools of the lead's look again (7.7). Each checks what the board would refuse now; Crew
// applies the call after the turn.

func (t *Tools) lookAgainCall(ctx context.Context, c mcp.Caller, tool string) (*board.Project, *board.Ticket, error) {
	p, tk, err := t.own(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	k, _, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	if k != kindLookAgain || (c.Role != "curator" && c.Role != "lead") {
		return nil, nil, mcp.Errf("%s is for the curator's run that looks again at a ticket that cannot get through; this run is a %s's %s", tool, c.Role, k)
	}
	return p, tk, nil
}

func (t *Tools) newApproach(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct{ Reason, Approach string }
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	_, tk, err := t.lookAgainCall(ctx, c, "new_approach")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Reason) == "" || strings.TrimSpace(a.Approach) == "" {
		return nil, mcp.Errf("say why the ticket keeps failing, and the approach a builder takes now")
	}
	if len(a.Reason) > 4000 || len(a.Approach) > 20000 {
		return nil, mcp.Errf("the reason has at most 4000 characters, and the approach at most 20000")
	}
	if err := board.TryNewApproach(tk); err != nil {
		return nil, boardError(err)
	}
	if err := t.report(ctx, c, report{Tool: "new_approach", Reason: a.Reason, Approach: a.Approach}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": "new_approach", "next": "End your turn now. Crew records your reason, and a builder carries out the approach."}, nil
}

func (t *Tools) cancelAndReplace(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a struct {
		Reason  string
		Tickets []board.NewTicket
	}
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	_, tk, err := t.lookAgainCall(ctx, c, "cancel_and_replace")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 4000 {
		return nil, mcp.Errf("reason: say why the ticket cannot get through, in at most 4000 characters")
	}
	if len(a.Tickets) > maxReplacements {
		return nil, mcp.Errf("tickets: at most %d", maxReplacements)
	}
	if err := board.TryCancelAndReplace(tk, a.Tickets); err != nil {
		return nil, boardError(err)
	}
	if err := t.report(ctx, c, report{Tool: "cancel_and_replace", Reason: a.Reason, Replacements: a.Tickets}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": "cancel_and_replace", "next": "End your turn now. Crew cancels the ticket and creates the tickets that replace it."}, nil
}

// short is s on one line, cut to at most n bytes at a rune.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
