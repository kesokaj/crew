package board

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/version"
)

// The purposes of a check run (13.9).
const (
	PurposeBase  = "base"  // the ticket's command checks on main, before it is built (7.6)
	PurposeExit  = "exit"  // the ticket's command checks on its branch's head, for a move
	PurposeTest  = "test"  // the ticket's command checks and the project's test command, after the tester
	PurposeMerge = "merge" // the same on the merge candidate (7.10)
	PurposePart  = "part"  // a parallel subtask's own check on its branch's head (7.11)
	PurposeJoin  = "join"  // the same on its join into the ticket's branch (7.11)
	// PurposeJudge is a judge's answer to the ticket's judge: checks on the branch's head (7.1, from
	// M10): one result each, met or not, with the judge's reason. No check sandbox runs it.
	PurposeJudge = "judge"
	// PurposeE2E is Crew's own run of the end-to-end tests, the E2E stage's command, after its E2E
	// tester finished (7.1).
	PurposeE2E = "e2e"
	// PurposeStage is Crew's run of a project's own stage's commands, its exit's "cmd: …" checks, and
	// PurposeStageJudge a judge's answer to its "judge: …" checks (7.4, from M10).
	PurposeStage      = "stage"
	PurposeStageJudge = "stage_judge"
	// PurposeOutside is the ticket's command checks and the project's tests on main, after its pull
	// request was merged on GitHub by someone else (21, since 0.47).
	PurposeOutside = "outside"
)

// The states of a check run. An error is Crew's own trouble, never a failure of the ticket (13.9).
const (
	CheckRunning = "running"
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckError   = "error"
)

// CmdResult is what one command of a check run did.
type CmdResult struct {
	Name   string `json:"name"`
	Code   int    `json:"code"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// CheckRun is one run of a check sandbox for a ticket.
type CheckRun struct {
	ID        int64       `json:"id"`
	Purpose   string      `json:"purpose"`
	Commit    string      `json:"commit"`
	Main      *string     `json:"main"`
	Head      *string     `json:"head"`
	State     string      `json:"state"`
	Results   []CmdResult `json:"results"`
	Detail    *string     `json:"detail"`
	Flaky     bool        `json:"flaky"`
	StartedAt time.Time   `json:"startedAt"`
	EndedAt   *time.Time  `json:"endedAt"`
	// Stage is the project's own stage a run of its commands or its judge is for; Criteria are what a
	// judge was asked, in their order, which its answer is read against.
	Stage    *string  `json:"stage,omitempty"`
	Criteria []string `json:"criteria,omitempty"`
}

const checkRunCols = `id, purpose, commit, main, head, state, results, detail, flaky, started_at, ended_at, stage, criteria`

func scanCheckRun(row pgx.Row) (CheckRun, error) {
	var c CheckRun
	err := row.Scan(&c.ID, &c.Purpose, &c.Commit, &c.Main, &c.Head, &c.State, &c.Results, &c.Detail, &c.Flaky, &c.StartedAt, &c.EndedAt, &c.Stage, &c.Criteria)
	if c.Results == nil {
		c.Results = []CmdResult{}
	}
	return c, err
}

// CheckRuns returns a ticket's check runs, newest first.
func (b *Board) CheckRuns(ctx context.Context, t *Ticket) ([]CheckRun, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+checkRunCols+` FROM check_runs WHERE ticket = $1 ORDER BY id DESC`, t.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CheckRun, error) { return scanCheckRun(r) })
}

// LatestStageRun is a ticket's newest check run of purpose for the project's own stage on commit, or nil.
func LatestStageRun(ctx context.Context, q db.Querier, ticket int64, commit, stage, purpose string) (*CheckRun, error) {
	c, err := scanCheckRun(q.QueryRow(ctx, `SELECT `+checkRunCols+` FROM check_runs WHERE ticket = $1 AND commit = $2 AND purpose = $3 AND stage = $4
		ORDER BY id DESC LIMIT 1`, ticket, commit, purpose, stage))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// LatestCheckRun is a ticket's newest check run of one of purposes on commit, or nil.
func LatestCheckRun(ctx context.Context, q db.Querier, ticket int64, commit string, purposes ...string) (*CheckRun, error) {
	c, err := scanCheckRun(q.QueryRow(ctx, `SELECT `+checkRunCols+` FROM check_runs WHERE ticket = $1 AND commit = $2 AND purpose = ANY($3)
		ORDER BY id DESC LIMIT 1`, ticket, commit, purposes))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// OwnStageCmds and OwnStageJudges are the commands and the criteria a project's own stage names in
// its exit list, as checks.
func OwnStageCmds(exit []string) []CheckSpec {
	var out []CheckSpec
	for _, e := range exit {
		if c, ok := strings.CutPrefix(e, "cmd: "); ok {
			out = append(out, CheckSpec{Kind: "cmd", Spec: c})
		}
	}
	return out
}

func OwnStageJudges(exit []string) []CheckSpec {
	var out []CheckSpec
	for _, e := range exit {
		if c, ok := strings.CutPrefix(e, "judge: "); ok {
			out = append(out, CheckSpec{Kind: "judge", Spec: c})
		}
	}
	return out
}

// ownStageCheck answers the checks of a project's own stage (7.4): its agent's verdict, a command
// Crew ran on the head, and a criterion a judge answered there.
func ownStageCheck(name string) Check {
	resultOf := func(purpose, result, what string) Check {
		return func(ctx context.Context, c *CheckContext) (Result, error) {
			h, err := head(ctx, c)
			if err != nil || h == "" {
				return Result{Detail: "its branch is not pushed"}, err
			}
			run, err := LatestStageRun(ctx, c.Tx, c.Ticket.ID, h, c.Ticket.Stage, purpose)
			switch {
			case err != nil:
				return Result{}, err
			case run == nil || run.State == CheckRunning:
				return Result{Detail: "Crew has not " + what + " on " + h[:12] + " yet"}, nil
			case run.State == CheckError:
				return Result{Detail: "Crew could not have it " + what + " on " + h[:12] + ": " + PublicDetail(run)}, nil
			}
			for _, r := range run.Results {
				if r.Name == result {
					if r.Passed {
						return Result{Passed: true}, nil
					}
					return Result{Detail: "not passed on " + h[:12] + ": " + r.Output}, nil
				}
			}
			return Result{Detail: "Crew's run on " + h[:12] + " did not answer it"}, nil
		}
	}
	switch {
	case name == "stage_verdict_pass":
		return stageVerdict
	case strings.HasPrefix(name, "cmd: "):
		return resultOf(PurposeStage, strings.TrimPrefix(name, "cmd: "), "run it")
	case strings.HasPrefix(name, "judge: "):
		return resultOf(PurposeStageJudge, strings.TrimPrefix(name, "judge: "), "judged it")
	}
	return nil
}

// stageVerdict is the check that the stage's agent's newest verdict in the ticket's stage is about
// its head, and passes.
func stageVerdict(ctx context.Context, c *CheckContext) (Result, error) {
	h, err := head(ctx, c)
	if err != nil {
		return Result{}, err
	}
	var commit, result *string
	err = c.Tx.QueryRow(ctx, `SELECT commit, result FROM records WHERE ticket = $1 AND kind = 'stage' AND stage = $2 ORDER BY id DESC LIMIT 1`,
		c.Ticket.ID, c.Ticket.Stage).Scan(&commit, &result)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Result{Detail: "its stage's agent has not decided yet"}, nil
	case err != nil:
		return Result{}, err
	case h == "" || commit == nil || *commit != h:
		return Result{Detail: "its stage's agent decided about an earlier commit than its head"}, nil
	case result == nil || *result != "pass":
		return Result{Detail: "its stage's agent found it does not pass"}, nil
	}
	return Result{Passed: true}, nil
}

// MaxJudgeChecks is how many judge: checks a ticket may have: one judge answers them together.
const MaxJudgeChecks = 10

// JudgeChecks are a ticket's judge: checks, in their order.
func JudgeChecks(t *Ticket) []CheckSpec {
	var out []CheckSpec
	for _, c := range t.Checks {
		if c.Kind == "judge" {
			out = append(out, c)
		}
	}
	return out
}

// judgeChecksOn is the check that a judge answered the ticket's judge: checks on its head, and that
// each is met (7.1).
func judgeChecksOn(ctx context.Context, c *CheckContext) (Result, error) {
	judges := JudgeChecks(c.Ticket)
	if len(judges) == 0 {
		return Result{Passed: true, Detail: "it has no judge check"}, nil
	}
	h, err := head(ctx, c)
	if err != nil || h == "" {
		return Result{Detail: "its branch is not pushed"}, err
	}
	run, err := LatestCheckRun(ctx, c.Tx, c.Ticket.ID, h, PurposeJudge)
	if err != nil {
		return Result{}, err
	}
	switch {
	case run == nil:
		return Result{Detail: "no judge has read " + h[:12] + " yet"}, nil
	case run.State == CheckRunning:
		return Result{Detail: "a judge reads " + h[:12]}, nil
	case run.State == CheckError:
		return Result{Detail: "Crew could not have " + h[:12] + " judged: " + PublicDetail(run)}, nil
	}
	met := map[string]bool{}
	var not []string
	for _, r := range run.Results {
		met[r.Name] = r.Passed
		if !r.Passed {
			not = append(not, r.Name+" (the judge: "+r.Output+")")
		}
	}
	for _, x := range judges {
		if _, ok := met[x.Spec]; !ok {
			not = append(not, x.Spec+" (not answered)")
		}
	}
	if len(not) > 0 {
		return Result{Detail: "not met on " + h[:12] + ": " + strings.Join(not, "; ")}, nil
	}
	return Result{Passed: true, Detail: fmt.Sprintf("a judge found %d criteria met on %s", len(judges), h[:12])}, nil
}

// CmdChecks are a ticket's command checks.
func CmdChecks(t *Ticket) []CheckSpec {
	var out []CheckSpec
	for _, c := range t.Checks {
		if c.Kind == "cmd" {
			out = append(out, c)
		}
	}
	return out
}

// head is the commit of the ticket's branch in the quarantine repository, or "".
func head(ctx context.Context, c *CheckContext) (string, error) {
	if c.Ticket.Branch == nil {
		return "", nil
	}
	return c.Git.Head(ctx, c.Project.Name, *c.Ticket.Branch)
}

// failures says which commands of a run failed, in a line.
func failures(run *CheckRun) string {
	var bad []string
	for _, r := range run.Results {
		if !r.Passed {
			bad = append(bad, r.Name)
		}
	}
	return strings.Join(bad, ", ")
}

// cmdChecksOn is the check that the ticket's command checks passed on its head, in a run of one of
// purposes that ran every one of them.
func cmdChecksOn(ctx context.Context, c *CheckContext, purposes ...string) (Result, error) {
	cmds := CmdChecks(c.Ticket)
	if len(cmds) == 0 {
		return Result{Passed: true, Detail: "it has no command check"}, nil
	}
	h, err := head(ctx, c)
	if err != nil || h == "" {
		return Result{Detail: "its branch is not pushed"}, err
	}
	run, err := LatestCheckRun(ctx, c.Tx, c.Ticket.ID, h, purposes...)
	if err != nil {
		return Result{}, err
	}
	switch {
	case run == nil:
		return Result{Detail: "Crew has not run its checks on " + h[:12] + " yet"}, nil
	case run.State == CheckRunning:
		return Result{Detail: "Crew runs its checks on " + h[:12]}, nil
	case run.State == CheckError:
		return Result{Detail: "Crew could not run its checks on " + h[:12] + ": " + PublicDetail(run)}, nil
	case run.State == CheckFailed:
		return Result{Detail: "its checks failed on " + h[:12] + ": " + failures(run)}, nil
	}
	ran := map[string]bool{}
	for _, r := range run.Results {
		ran[r.Name] = r.Passed
	}
	for _, x := range cmds {
		if !ran[x.Spec] {
			return Result{Detail: "Crew's run of the checks on " + h[:12] + " did not pass " + x.Spec}, nil
		}
	}
	return Result{Passed: true, Detail: fmt.Sprintf("%d command checks passed on %s in a check sandbox", len(cmds), h[:12])}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// newestRecord is a ticket's newest entry of kind, or nil.
func newestRecord(ctx context.Context, q db.Querier, ticket int64, kind string) (*Record, error) {
	r, err := scanRecord(q.QueryRow(ctx, `SELECT `+recordCols+` FROM records r JOIN projects p ON p.id = r.project
		LEFT JOIN tickets t ON t.id = r.ticket WHERE r.ticket = $1 AND r.kind = $2 ORDER BY r.id DESC LIMIT 1`, ticket, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// m6Checks are the checks that read the records agents write and Crew's runs of the check sandbox.
var m6Checks = map[string]Check{
	// The builder wrote what it changed and which commands it ran (7.4).
	"note_exists": func(ctx context.Context, c *CheckContext) (Result, error) {
		var ok bool
		// A revert's note is Crew's own, which made its commit (7.13).
		err := c.Tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE ticket = $1 AND kind = 'note'
			AND (author = 'builder' OR (author = 'crew' AND $2 = 'revert')))`, c.Ticket.ID, c.Ticket.Kind).Scan(&ok)
		if err != nil || !ok {
			return Result{Detail: "the builder wrote no note"}, err
		}
		return Result{Passed: true}, nil
	},
	"task_cmd_checks_pass": func(ctx context.Context, c *CheckContext) (Result, error) {
		return cmdChecksOn(ctx, c, PurposeExit, PurposeTest)
	},
	"task_checks_pass": func(ctx context.Context, c *CheckContext) (Result, error) {
		return cmdChecksOn(ctx, c, PurposeExit, PurposeTest)
	},
	"task_judge_checks_pass": judgeChecksOn,
	// The newest review is about the branch as it is, and approves it (7.1).
	"review_verdict_approve": func(ctx context.Context, c *CheckContext) (Result, error) {
		h, err := head(ctx, c)
		if err != nil {
			return Result{}, err
		}
		r, err := newestRecord(ctx, c.Tx, c.Ticket.ID, RecordReview)
		switch {
		case err != nil:
			return Result{}, err
		case r == nil:
			return Result{Detail: "it has no review yet"}, nil
		case h == "" || r.Commit == nil || *r.Commit != h:
			return Result{Detail: "its newest review is about an earlier commit than its head"}, nil
		case *r.Verdict != VerdictApprove:
			return Result{Detail: "its newest review asks for changes"}, nil
		}
		return Result{Passed: true, Detail: "approved by the " + r.Author}, nil
	},
	// A review owed since Review (a change after its approval, or a package file) read the head (7.4).
	"review_covers_head": reviewCoversHead,
	// The tester finished on the branch as it is, and Crew's own run of the ticket's checks and the
	// project's tests passed there; the tester's entry is a record and decides nothing (7.1, 13.9).
	"test_result_pass": func(ctx context.Context, c *CheckContext) (Result, error) {
		h, err := head(ctx, c)
		if err != nil || h == "" {
			return Result{Detail: "its branch is not pushed"}, err
		}
		r, err := newestRecord(ctx, c.Tx, c.Ticket.ID, RecordTest)
		if err != nil {
			return Result{}, err
		}
		if r == nil || r.Commit == nil || *r.Commit != h {
			return Result{Detail: "the tester has not finished on " + h[:12]}, nil
		}
		run, err := LatestCheckRun(ctx, c.Tx, c.Ticket.ID, h, PurposeTest)
		switch {
		case err != nil:
			return Result{}, err
		case run == nil || run.State == CheckRunning:
			return Result{Detail: "Crew runs the checks and the tests on " + h[:12]}, nil
		case run.State == CheckError:
			return Result{Detail: "Crew could not run the tests on " + h[:12] + ": " + PublicDetail(run)}, nil
		case run.State == CheckFailed:
			return Result{Detail: "Crew's run of the checks and the tests failed on " + h[:12] + ": " + failures(run)}, nil
		}
		return Result{Passed: true, Detail: "Crew's run of the checks and the tests passed in a check sandbox"}, nil
	},
	// The E2E tester finished on the branch as it is, and Crew's own run of the stage's end-to-end
	// command passed there, in a check sandbox; the tester's entry, with its logs and screenshots, is a
	// record and decides nothing (7.1, 13.9).
	"e2e_result_pass": func(ctx context.Context, c *CheckContext) (Result, error) {
		h, err := head(ctx, c)
		if err != nil || h == "" {
			return Result{Detail: "its branch is not pushed"}, err
		}
		r, err := newestRecord(ctx, c.Tx, c.Ticket.ID, RecordE2E)
		if err != nil {
			return Result{}, err
		}
		if r == nil || r.Commit == nil || *r.Commit != h {
			return Result{Detail: "the E2E tester has not finished on " + h[:12]}, nil
		}
		run, err := LatestCheckRun(ctx, c.Tx, c.Ticket.ID, h, PurposeE2E)
		switch {
		case err != nil:
			return Result{}, err
		case run == nil || run.State == CheckRunning:
			return Result{Detail: "Crew runs the end-to-end tests on " + h[:12]}, nil
		case run.State == CheckError:
			return Result{Detail: "Crew could not run the end-to-end tests on " + h[:12] + ": " + PublicDetail(run)}, nil
		case run.State == CheckFailed:
			return Result{Detail: "Crew's run of the end-to-end tests failed on " + h[:12] + ": " + failures(run)}, nil
		}
		return Result{Passed: true, Detail: "Crew's run of the end-to-end tests passed in a check sandbox"}, nil
	},
	// The newest verification is about the branch as it is, and says it does what was asked (7.1).
	"verify_pass": func(ctx context.Context, c *CheckContext) (Result, error) {
		h, err := head(ctx, c)
		if err != nil {
			return Result{}, err
		}
		r, err := newestRecord(ctx, c.Tx, c.Ticket.ID, RecordVerify)
		switch {
		case err != nil:
			return Result{}, err
		case r == nil:
			return Result{Detail: "it has no verification yet"}, nil
		case h == "" || r.Commit == nil || *r.Commit != h:
			return Result{Detail: "its newest verification is about an earlier commit than its head"}, nil
		case r.Result == nil || *r.Result != "pass":
			return Result{Detail: "its newest verification says it does not do what was asked"}, nil
		}
		return Result{Passed: true, Detail: "verified by the " + r.Author}, nil
	},
	// The ticket's branch merges with main as it is now, and the checks passed on the result (7.10).
	// The move to Done then moves main to that merge.
	"merges_and_passes_on_main": func(ctx context.Context, c *CheckContext) (Result, error) {
		if r, err := remoteHoldResult(ctx, c); err != nil || r != nil {
			if r == nil {
				return Result{}, err
			}
			return *r, nil
		}
		code, err := codeChanges(ctx, c.Git, c.Project, c.Ticket)
		if err != nil {
			return Result{}, err
		}
		if len(code) > 0 {
			h, err := head(ctx, c)
			if err != nil {
				return Result{}, err
			}
			if !CodeAllowed(c.Ticket, h) {
				return Result{Detail: CodeWaitText(code)}, nil
			}
		}
		run, err := mergeCandidate(ctx, c)
		if err != nil || run == nil {
			return Result{Detail: "it waits for its turn to merge into main"}, err
		}
		return Result{Passed: true, Detail: "the merge " + run.Commit[:12] + " passed its checks in a check sandbox"}, nil
	},
}

// codeChanges are the files the ticket's branch changes that are code for a CLI, or GitHub workflows
// (13.8): a ticket that changes one never merges by itself.
func codeChanges(ctx context.Context, g *gitstore.Store, p *Project, t *Ticket) ([]string, error) {
	if t.Branch == nil {
		return nil, nil
	}
	br, err := g.ReadBranch(ctx, p.Name, *t.Branch)
	if err != nil {
		return nil, err
	}
	// What main's index files name as code is code too, wherever it is (13.8). An index file that
	// cannot be read, past the bound of a read or by any failure of git, is an error, not one that
	// names nothing (since 1.04).
	var ferr error
	named, _, nerr := cli.Indexed(func(f string) ([]byte, cli.Way) {
		b, way, err := g.IndexFile(ctx, p.Name, f)
		ferr = cmp.Or(ferr, err)
		return b, way
	})
	if ferr = cmp.Or(ferr, nerr); ferr != nil {
		return nil, ferr
	}
	code := append(cli.CodeFiles(), named...)
	// What a link of main that stands for code leads to is code too (since 1.06).
	linked, err := g.Linked(ctx, p.Name, code)
	if err != nil {
		return nil, err
	}
	code = append(code, linked...)
	match := cli.NewMatcher(code)
	var out []string
	for _, f := range br.Changed {
		if match.Matches(f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// CodeChanges are the files the ticket's branch changes that are code (13.8).
func (b *Board) CodeChanges(ctx context.Context, p *Project, t *Ticket) ([]string, error) {
	return codeChanges(ctx, b.Git, p, t)
}

// CodeWait is a head of a ticket's branch whose files that are code wait for the operator (13.8).
type CodeWait struct {
	Head  string   `json:"head"`
	Main  string   `json:"main"`
	Files []string `json:"files"`
}

// CodeSay is the operator's answer about one head: allow merges it, refuse sends it back to the
// builder in place.
type CodeSay struct {
	Head   string    `json:"head"`
	Choice string    `json:"choice"`
	At     time.Time `json:"at"`
}

// CodeAllowed says the operator allowed the files that are code of this head of the ticket's branch.
func CodeAllowed(t *Ticket, head string) bool {
	return t.CodeSay != nil && t.CodeSay.Choice == "allow" && t.CodeSay.Head == head && head != ""
}

// SetCodeWait records that the files that are code of a head wait for the operator, once per head.
func (b *Board) SetCodeWait(ctx context.Context, t *Ticket, w CodeWait) error {
	_, err := b.Pool.Exec(ctx, `UPDATE tickets SET code_wait = $2 WHERE id = $1 AND code_wait IS DISTINCT FROM $2::jsonb`, t.ID, w)
	return err
}

// CodeAnswer is the operator's answer to a ticket whose files that are code wait (13.8), about the
// head they saw: a newer head asks again.
func (b *Board) CodeAnswer(ctx context.Context, p *Project, ref, choice, head, who string) (*Ticket, error) {
	n, err := ParseNumber(p, ref)
	if err != nil {
		return nil, err
	}
	if choice != "allow" && choice != "refuse" {
		return nil, errf(Invalid, "choice: allow or refuse")
	}
	var out *Ticket
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		t, err := ticketByNumber(ctx, tx, p.ID, n, true)
		if err != nil {
			return err
		}
		if t.CodeWait == nil || t.CodeWait.Head != head || t.Stage == settings.StageDone || t.Stage == settings.StageClosed {
			return errf(Refused, "%s has no files that are code waiting for you at %s", t.Key, head)
		}
		say := CodeSay{Head: head, Choice: choice, At: b.now()}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET code_say = $2, code_wait = NULL, updated_at = now() WHERE id = $1`, t.ID, say); err != nil {
			return err
		}
		if err := db.RecordProjectChange(ctx, tx, p.ID, who, "tickets", t.Key, map[string]any{"codeWait": t.CodeWait},
			map[string]any{"codeSay": say}); err != nil {
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

// CodeWaitText is why a ticket that changes files that are code waits: it never merges by itself
// (13.8), and the operator's say on it is in the inbox.
func CodeWaitText(files []string) string {
	if len(files) > 5 {
		files = append(files[:5:5], "…")
	}
	return "it changes files that are code (" + strings.Join(files, ", ") + "): it never merges by itself, and waits for your answer in the inbox (13.8)"
}

// mergeCandidate is the ticket's merge check run that passed on a merge of its head with main as it
// is now, or with main that is that merge already, as after a move that was tried again.
func mergeCandidate(ctx context.Context, c *CheckContext) (*CheckRun, error) {
	h, err := head(ctx, c)
	if err != nil || h == "" {
		return nil, err
	}
	main := c.Git.Main(ctx, c.Project.Name)
	run, err := scanCheckRun(c.Tx.QueryRow(ctx, `SELECT `+checkRunCols+` FROM check_runs WHERE ticket = $1 AND purpose = 'merge'
		AND state = 'passed' AND head = $2 AND (main = $3 OR commit = $3) ORDER BY id DESC LIMIT 1`, c.Ticket.ID, h, main))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &run, err
}

// merge moves main to the ticket's merge candidate, which its check passed on (7.10, step 3), and
// queues the push of main to the remote. It runs in the move's transaction, under the project's lock,
// so no other merge of the project moves main meanwhile; main moves before the transaction commits,
// and a move that is tried again finds main there already.
func (b *Board) merge(ctx context.Context, tx pgx.Tx, p *Project, t *Ticket) (map[string]any, error) {
	run, err := mergeCandidate(ctx, &CheckContext{Tx: tx, Project: p, Ticket: t, Git: b.Git})
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errf(Refused, "%s has no merge that passed its checks on main as it is", t.Key)
	}
	if err := b.Git.AdvanceMain(ctx, p.Name, deref(run.Main), run.Commit); err != nil {
		return nil, fmt.Errorf("moving main to the merge of %s: %w", t.Key, err)
	}
	if p.Remote != nil {
		// A pull request's branch is on the remote at the head that merged before main is: the push of
		// main then marks the pull request merged at that head (7.12).
		if run.Head != nil {
			if err := queueBranchPush(ctx, tx, p, t, *run.Head); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			p.ID, *p.Remote, Backend); err != nil {
			return nil, err
		}
	}
	return map[string]any{"main": run.Commit, "merged": deref(run.Head)}, nil
}

// MergeMessage is the message of a ticket's merge commit, with its trailers (6.3).
func MergeMessage(t *Ticket) string {
	return fmt.Sprintf("%s: %s\n\nMerge %s into main.\n\nTicket: %s\n", t.Key, t.Title, BranchOf(t), t.Key)
}

// BranchOf is the ticket's branch, or "".
func BranchOf(t *Ticket) string {
	if t.Branch == nil {
		return ""
	}
	return *t.Branch
}

// Head returns the commit of a ticket's branch as the runs pushed it, or "".
func (b *Board) Head(ctx context.Context, p *Project, t *Ticket) (string, error) {
	if t.Branch == nil {
		return "", nil
	}
	return b.Git.Head(ctx, p.Name, *t.Branch)
}

// ExitResults evaluates the checks the ticket must pass to leave its stage, and to enter the next
// one, without moving it: what the goal loop gives back to an agent whose move failed (11.3).
func (b *Board) ExitResults(ctx context.Context, p *Project, t *Ticket) ([]Result, error) {
	var out []Result
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		pipe := PipelineOf(doc)
		cur, ok := pipe.Stage(t.Stage)
		if !ok {
			return errf(Refused, "%s is in %s, which is not on the board", t.Key, t.Stage)
		}
		next, ok := pipe.Next(t.Stage, t.Skips)
		names := exitNames(pipe, cur, t)
		if ok {
			names = append(names, next.Enter...)
		}
		c := &CheckContext{Tx: tx, Project: p, Ticket: t, Pipeline: pipe, Git: b.Git, Now: b.now()}
		for _, name := range names {
			r, err := b.evaluate(ctx, c, name)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

// FixRound counts one more fix round on a ticket that failed a stage, with its row in changes, and
// says whether the ticket has reached its limit (7.4, 7.7).
func (b *Board) FixRound(ctx context.Context, p *Project, t *Ticket, why string) (int, error) {
	var n int
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE tickets SET fix_rounds = fix_rounds + 1, updated_at = now() WHERE id = $1 RETURNING fix_rounds`,
			t.ID).Scan(&n); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, Backend, "tickets", t.Key, map[string]any{"fixRounds": n - 1},
			map[string]any{"fixRounds": n, "why": why})
	})
	return n, err
}

// Wait writes why the work around a ticket starts nothing more for it now, beside the board's own
// waiting; Unstuck clears it once a run starts for it again.
func (b *Board) Wait(ctx context.Context, t *Ticket, why string) error {
	_, err := b.Pool.Exec(ctx, `UPDATE tickets SET stuck = $2 WHERE id = $1 AND stuck IS DISTINCT FROM $2`, t.ID, why)
	return err
}

// Unstuck clears why the work around a ticket had stopped.
func Unstuck(ctx context.Context, tx pgx.Tx, ticket int64) error {
	_, err := tx.Exec(ctx, `UPDATE tickets SET stuck = NULL WHERE id = $1 AND stuck IS NOT NULL`, ticket)
	return err
}

// Signal is a fact about how the agents and the pipeline did (10.2). Only the backend records one.
type Signal struct {
	Project, Kind, Run string
	Ticket             int64
	Detail             map[string]any
}

// RecordSignal writes a signal with what the run it names ran on.
func RecordSignal(ctx context.Context, q db.Querier, s Signal) error {
	detail, err := json.Marshal(s.Detail)
	if err != nil || s.Detail == nil {
		detail = []byte("{}")
	}
	_, err = q.Exec(ctx, `INSERT INTO signals (project, ticket, run, kind, role, cli, model, effort, crew_version, detail)
		SELECT nullif($1, '')::uuid, nullif($2, 0), r.id, $4, s.role, r.cli, r.model, r.effort, coalesce(r.crew_version, $6), $5
		FROM (SELECT 1) one LEFT JOIN runs r ON r.id = nullif($3, '')::uuid LEFT JOIN subtasks s ON s.id = r.subtask`,
		s.Project, s.Ticket, s.Run, s.Kind, detail, version.Version)
	return err
}
