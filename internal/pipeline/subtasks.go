package pipeline

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runs"
)

// sub is a subtask of a ticket with its newest run.
type sub struct {
	id                  int64
	kind, role, state   string
	reported            *report
	goalRounds          int
	reminded            bool
	commit              string
	created             time.Time
	run, runState       string
	cli, model          string
	stopReason, waitFor string
	stopDetail          string
	// What a subtask an agent created has (7.5, 7.11): its number under the ticket, its branch, the
	// subtasks it comes after, the files it touches, its check, when it was joined, who created it,
	// and what it was asked for.
	number              int
	branch              string
	after               []int64
	touches             []string
	check               string
	joined              *time.Time
	createdBy           string
	title, what, doneAs string
}

// active says the subtask is still being worked: a run goes, waits for its next turn, or waits to
// continue (12.4).
// A helper is a record of its parent's run, never work of its own.
func (s *sub) active() bool {
	return s.kind != "helper" && (s.state == "running" || s.state == "idle" || s.state == "waiting")
}

// lastNonHelper returns the newest subtask of subs that is not a helper record, or nil.
func lastNonHelper(subs []*sub) *sub {
	for i := len(subs) - 1; i >= 0; i-- {
		if subs[i].kind != "helper" {
			return subs[i]
		}
	}
	return nil
}

// waitedOut says a builder's turn asked to move, and its run then ended while it waited for Crew to act
// on the turn: the monitor ends a run past its idle time, while its checks or its judge run, and while
// its project is paused (since 0.55, from L4), or reportedDone rescued a turn that pushed and reported
// right at its step, time, or quota limit.
func (s *sub) waitedOut() bool {
	return s.kind == kindBuild && s.state == "done" && s.reported != nil && s.reported.Tool == toolOf[kindBuild] &&
		s.runState == "ended" && s.stopReason == cli.ReasonOK && s.stopDetail == runs.IdleEnded
}

// finish ends a run whose turn's work is done. One that ended while it waited has nothing to end: it
// is marked taken up, so a ticket that then waits for room or for its merge is not taken up again on
// every pass.
func (p *Pipeline) finish(ctx context.Context, s *sub) error {
	if s.runState == "ended" {
		_, err := p.Pool.Exec(ctx, `UPDATE runs SET stop_detail = $2 WHERE id = $1 AND state = 'ended' AND stop_detail = $3`, s.run, runs.TakenUp, runs.IdleEnded)
		return err
	}
	return p.Runs.Finish(ctx, s.run)
}

// report is what a run reported with the call that ends its kind of run (11.3), as the tool wrote it.
type report struct {
	Tool     string          `json:"tool"`
	Decision *board.Decision `json:"decision,omitempty"`
	// The lead's call when it looks again at a ticket (7.7): its reason, and the new approach or the
	// tickets that replace it.
	Reason       string            `json:"reason,omitempty"`
	Approach     string            `json:"approach,omitempty"`
	Replacements []board.NewTicket `json:"replacements,omitempty"`
	// Sections are the lead's final report (7.2).
	Sections map[string]string `json:"sections,omitempty"`
	// BaseRun is the check run of the decision's command checks on main (7.6).
	BaseRun int64 `json:"baseRun,omitempty"`
}

func (p *Pipeline) subtasks(ctx context.Context, t *board.Ticket) ([]*sub, error) {
	rows, err := p.Pool.Query(ctx, `SELECT s.id, s.kind, s.role, s.state, s.reported, s.goal_rounds, s.reminded, coalesce(s.commit, ''),
			s.created_at, coalesce(r.id::text, ''), coalesce(r.state, ''), coalesce(r.cli, ''), coalesce(r.model, ''),
			coalesce(r.stop_reason, ''), coalesce(s.wait_for, ''), s.number, coalesce(s.branch, ''), s.after, s.touches,
			coalesce(s.check_cmd, ''), s.joined_at, s.created_by, coalesce(s.title, ''), coalesce(s.what, ''), coalesce(s.done_when, ''),
			coalesce(r.stop_detail, '')
		FROM subtasks s LEFT JOIN LATERAL (SELECT * FROM runs WHERE subtask = s.id ORDER BY number DESC LIMIT 1) r ON true
		WHERE s.ticket = $1 ORDER BY s.id`, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sub
	for rows.Next() {
		var s sub
		var rep []byte
		if err := rows.Scan(&s.id, &s.kind, &s.role, &s.state, &rep, &s.goalRounds, &s.reminded, &s.commit, &s.created,
			&s.run, &s.runState, &s.cli, &s.model, &s.stopReason, &s.waitFor, &s.number, &s.branch, &s.after, &s.touches,
			&s.check, &s.joined, &s.createdBy, &s.title, &s.what, &s.doneAs, &s.stopDetail); err != nil {
			return nil, err
		}
		if len(rep) > 0 && string(rep) != "null" {
			var r report
			if json.Unmarshal(rep, &r) == nil && r.Tool != "" {
				s.reported = &r
			}
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// judgingKinds are the subtasks whose work is their one entry: once it is written, nothing is left.
var judgingKinds = map[string]bool{kindReview: true, kindTest: true, kindE2E: true, kindVerify: true, kindStage: true}

func rescuableBuildLimit(reason string) bool {
	return reason == runs.ReasonTurns || reason == cli.ReasonQuota || reason == cli.ReasonTimeout
}

// reportedDone ends a judging subtask that waits for a retry although its entry is written (found live
// in M10, WC-14's verifier), and rescues a build or fix subtask whose run already pushed its commit and
// called request_move or fix_done before hitting its step, time, or quota limit (7.11, 12.4).
func (p *Pipeline) reportedDone(ctx context.Context, j *job, t *board.Ticket, subs []*sub) (bool, error) {
	ended := false
	headLoaded := false
	var head string
	for _, s := range subs {
		if s.reported == nil || s.runState != "ended" {
			continue
		}
		if s.state == "waiting" && judgingKinds[s.kind] {
			if _, err := p.Pool.Exec(ctx, `UPDATE subtasks SET state = 'done', wait_for = NULL, ended_at = coalesce(ended_at, now())
				WHERE id = $1 AND state = 'waiting'`, s.id); err != nil {
				return false, err
			}
			p.log().Info("a judging subtask that wrote its entry and then met a limit is done", "subtask", s.id, "kind", s.kind)
			ended = true
			continue
		}
		if (s.kind == kindBuild || s.kind == kindFix) && (s.state == "waiting" || s.state == "stopped") &&
			s.reported.Tool == toolOf[s.kind] && rescuableBuildLimit(s.stopReason) {
			if !headLoaded {
				var err error
				head, err = p.Board.Head(ctx, j.proj, t)
				if err != nil {
					return false, err
				}
				headLoaded = true
			}
			if head == "" || (s.kind == kindFix && head == s.commit) {
				continue
			}
			if err := pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
				if s.kind == kindBuild {
					if _, err := tx.Exec(ctx, `UPDATE runs SET stop_reason = $2, stop_detail = $3 WHERE id = $1 AND state = 'ended'`,
						s.run, cli.ReasonOK, runs.IdleEnded); err != nil {
						return err
					}
				}
				_, err := tx.Exec(ctx, `UPDATE subtasks SET state = 'done', wait_for = NULL, wait_until = NULL,
					stops = greatest(stops - 1, 0), ended_at = coalesce(ended_at, now())
					WHERE id = $1 AND state IN ('waiting', 'stopped')`, s.id)
				return err
			}); err != nil {
				return false, err
			}
			p.log().Info("a build or fix subtask that pushed and reported before meeting a limit is done", "ticket", t.Key, "subtask", s.id, "kind", s.kind)
			ended = true
		}
	}
	if ended {
		p.Kick()
	}
	return ended, nil
}

// endHelpers ends a ticket's helpers whose parent's run no longer works (7.11): a helper ends with its
// parent's turn, whatever ended it, also a run that failed.
func (p *Pipeline) endHelpers(ctx context.Context, ticket int64) error {
	_, err := p.Pool.Exec(ctx, `UPDATE subtasks h SET state = 'done', ended_at = now() WHERE h.ticket = $1 AND h.kind = 'helper'
		AND h.state = 'running' AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.subtask = h.parent AND r.state IN ('starting', 'working'))`, ticket)
	return err
}

// The kinds of a ticket's subtasks (7.11).
const (
	kindAnalysis = "analysis"
	kindBuild    = "build"
	kindFix      = "fix"
	kindReview   = "review"
	kindTest     = "test"
	kindMerge    = "merge"
	kindSummary  = "summary"
	// kindPart is a subtask an agent created on its ticket (7.5), and kindJoin the builder's subtask
	// that joins parallel work that did not join by itself (7.11).
	kindPart = "part"
	kindJoin = "join"
	// kindE2E is the E2E tester's run and kindVerify the verifier's (7.4, from M10), and kindStage
	// the run of the agent of a project's own stage.
	kindE2E      = "e2e"
	kindVerify   = "verify"
	kindStage    = "stage"
	kindResearch = "research"
)

// toolOf is the call that ends each kind of run (11.3).
var toolOf = map[string]string{
	kindAnalysis: "analyse_ticket", kindBuild: "request_move", kindFix: "fix_done",
	kindReview: "write_review", kindTest: "add_test_result", kindPart: "subtask_done", kindJoin: "subtask_done",
	kindE2E: "add_e2e_result", kindVerify: "add_verification", kindStage: "add_stage_verdict",
	kindResearch: "write_research_brief", kindLookAgain: "new_approach or cancel_and_replace",
}

// setReported writes a subtask's report, or clears it with nil.
func (p *Pipeline) setReported(ctx context.Context, s *sub, r *report) error {
	var b []byte
	if r != nil {
		b, _ = json.Marshal(r)
	}
	_, err := p.Pool.Exec(ctx, `UPDATE subtasks SET reported = $2 WHERE id = $1`, s.id, b)
	return err
}

// lastBuild is the newest run of the ticket's builder: the conversation its fix rounds continue.
func lastBuild(subs []*sub) string {
	for i := len(subs) - 1; i >= 0; i-- {
		if (subs[i].kind == kindBuild || subs[i].kind == kindFix) && subs[i].run != "" {
			return subs[i].run
		}
	}
	return ""
}
