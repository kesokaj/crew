package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
)

// kindJudge is a judge's run that answers a ticket's judge: checks about its branch's head (7.1).
const kindJudge = "judge"

// maxJudgeErrors is how often a head is given to a judge whose answer was not in its form, or whose
// run failed, before the ticket waits: an error is Crew's own trouble, never a fix round (13.9).
const maxJudgeErrors = 3

// maxJudgeDiff bounds the diff a judge reads; the rest is named as left out.
const maxJudgeDiff = 120_000

// judge has a ticket's judge: checks answered on its head, after its command checks passed (7.1):
// one judge run, with no tools, on another family than the builder, is given the ticket's record and
// the diff from main, and answers every criterion in a fixed form. It says whether the stage may go
// on to its exit checks: true when the ticket has no judge check or the judge answered; false while a
// judge runs, or when one could not answer three times and the ticket waits.
func (p *Pipeline) judge(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head string) (bool, error) {
	return p.judgeOn(ctx, j, t, subs, head, board.JudgeChecks(t), board.PurposeJudge, "")
}

// judgeOn has criteria answered on head by one judge, recorded as a check run of purpose: a ticket's
// judge: checks, or a project's own stage's "judge: …" exit checks (7.4).
func (p *Pipeline) judgeOn(ctx context.Context, j *job, t *board.Ticket, subs []*sub, head string, judges []board.CheckSpec, purpose, stage string) (bool, error) {
	if len(judges) == 0 {
		return true, nil
	}
	var run *board.CheckRun
	var err error
	if stage != "" {
		run, err = board.LatestStageRun(ctx, p.Pool, t.ID, head, stage, purpose)
	} else {
		run, err = board.LatestCheckRun(ctx, p.Pool, t.ID, head, purpose)
	}
	if err != nil {
		return false, err
	}
	switch {
	case run == nil:
		// A judge that runs already has its check run recorded as it starts.
		for _, s := range on(subs, kindJudge, head) {
			if s.active() {
				return false, nil
			}
		}
	case run.State == board.CheckRunning:
		for _, s := range on(subs, kindJudge, head) {
			if s.active() {
				return false, nil
			}
		}
		// Its judge's run ended without an answer that was read, or never started: Crew's own trouble.
		return false, p.Board.FinishCheckRun(ctx, run.ID, board.CheckError, nil, "the judge's run ended without an answer", false, false)
	case run.State == board.CheckError:
		n, err := p.judgeErrorsOn(ctx, t, head, purpose, stage)
		if err != nil {
			return false, err
		}
		if n >= maxJudgeErrors {
			return false, p.Board.Wait(ctx, t, fmt.Sprintf("Crew could not have %s judged %d times: %s", head[:12], maxJudgeErrors, board.PublicDetail(run)))
		}
	default:
		return true, nil
	}
	parts, err := p.judgePrompt(ctx, j, t, judges, head)
	if err != nil {
		return false, err
	}
	started, err := p.start(ctx, j, t, startSpec{kind: kindJudge, role: "judge", title: "Judge the criteria", parts: parts, judge: true,
		commit: head})
	if err != nil || started == "" {
		// No run yet: it waits for a model, a slot, or its CLI's login, as start says, and no try is
		// counted (review of M10). The check run is recorded once a judge runs.
		return false, err
	}
	// The criteria are kept with the run, so the answer is read against what the judge was asked.
	criteria := make([]string, len(judges))
	for i, c := range judges {
		criteria[i] = c.Spec
	}
	_, err = p.Board.StartCheckRunFor(ctx, j.proj, t, purpose, head, "", "", stage, criteria)
	return false, err
}

// judgeErrorsOn counts a head's judge runs that ended as errors, or returns the error that kept it
// from counting: no judge starts on a count that was not read.
func (p *Pipeline) judgeErrorsOn(ctx context.Context, t *board.Ticket, commit, purpose, stage string) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND commit = $2 AND purpose = $3 AND coalesce(stage, '') = $4 AND state = 'error' AND `+notSetup+``,
		t.ID, commit, purpose, stage).Scan(&n)
	return n, err
}

// judgePrompt is what a judge reads: its role, the form of its answer, the ticket, the builder's
// notes, the criteria, and the diff. All but Crew's own text is data (13.8).
func (p *Pipeline) judgePrompt(ctx context.Context, j *job, t *board.Ticket, judges []board.CheckSpec, head string) ([]runs.PromptPart, error) {
	files, err := p.Board.Git.Compare(ctx, j.proj.Name, "main", head)
	if err != nil {
		return nil, fmt.Errorf("the diff a judge reads: %w", err)
	}
	var diff strings.Builder
	left := 0
	for _, f := range files {
		switch {
		case f.Omitted > 0:
			left += f.Omitted
		case diff.Len()+len(f.Patch) > maxJudgeDiff:
			left++
		case f.Binary:
			fmt.Fprintf(&diff, "Binary file %s changed.\n", f.Path)
		default:
			diff.WriteString(f.Patch)
			if f.Cut {
				fmt.Fprintf(&diff, "\n[%s: the rest of its diff is left out]\n", f.Path)
			}
		}
	}
	if left > 0 {
		fmt.Fprintf(&diff, "\n[%d more files changed; their diffs are left out]\n", left)
	}
	recs, err := p.records(ctx, t)
	if err != nil {
		return nil, err
	}
	var notes strings.Builder
	for _, r := range recs {
		if r.Kind == board.RecordNote {
			fmt.Fprintf(&notes, "From the %s:\n%s\n\n", r.Author, r.Body)
		}
	}
	var crit strings.Builder
	for i, c := range judges {
		fmt.Fprintf(&crit, "%d. %s\n", i+1, c.Spec)
	}
	ticket := ticketText(t)
	f := newFence(ticket, notes.String(), crit.String(), diff.String())
	parts := []runs.PromptPart{
		p.agentPart(ctx, t.Project, "judge"),
		p.stagePart(ctx, t.Project, "judge"),
		{Part: "ticket", Source: "ticket " + t.Key, Text: f.data("The ticket", "the agents that wrote and analysed it", ticket)},
		{Part: "criteria", Source: "ticket " + t.Key, Text: f.data("The criteria, numbered", "the agent that analysed the ticket", crit.String()) +
			fmt.Sprintf("\n\nAnswer each of the %d criteria, by its number, in the form you were given.", len(judges))},
	}
	if notes.Len() > 0 {
		parts = append(parts, runs.PromptPart{Part: "notes", Source: "ticket " + t.Key, Text: f.data("The builder's notes", "the builder", notes.String())})
	}
	parts = append(parts, runs.PromptPart{Part: "diff", Source: "branch " + derefS(t.Branch) + " at " + head[:12],
		Text: f.data("The diff from main to "+head[:12], "the builder", diff.String())})
	return parts, nil
}

// afterJudge reads a judge's answer and ends its check run: passed when every criterion is met,
// failed when one is not, and an error when the answer is not in its form, which is tried again.
func (p *Pipeline) afterJudge(ctx context.Context, j *job, t *board.Ticket, s *sub) error {
	// The judge answers the one check run that waits on its head: the ticket's judge: checks, or a
	// project's own stage's.
	run, err := board.LatestCheckRun(ctx, p.Pool, t.ID, s.commit, board.PurposeJudge, board.PurposeStageJudge)
	if err != nil {
		return err
	}
	var judges []board.CheckSpec
	if run != nil {
		for _, c := range run.Criteria {
			judges = append(judges, board.CheckSpec{Kind: "judge", Spec: c})
		}
		// A run from before the criteria were kept with it reads the ticket's.
		if len(judges) == 0 && run.Purpose == board.PurposeJudge {
			judges = board.JudgeChecks(t)
		}
	}
	if run != nil && run.State == board.CheckRunning {
		var answer string
		err := p.Pool.QueryRow(ctx, `SELECT text FROM turns WHERE run = $1 AND speaker = 'agent' ORDER BY number DESC LIMIT 1`, s.run).Scan(&answer)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		results, ferr := Verdicts(answer, judges)
		state, detail := board.CheckPassed, ""
		switch {
		case ferr != nil:
			state, detail, results = board.CheckError, "the judge's answer is not in its form: "+ferr.Error(), nil
		default:
			for _, r := range results {
				if !r.Passed {
					state = board.CheckFailed
				}
			}
		}
		if err := p.Board.FinishCheckRun(ctx, run.ID, state, results, detail, false, false); err != nil {
			return err
		}
	}
	if err := p.Runs.Finish(ctx, s.run); err != nil {
		return err
	}
	p.Board.Kick()
	return nil
}

// verdictLine is one line of a judge's answer: "2. NOT MET: the page has no example."
var verdictLine = regexp.MustCompile(`^(\d+)\.\s+(MET|NOT MET):\s*(\S.*)$`)

// Verdicts reads a judge's answer in its fixed form (13.9): one line for each criterion, by its
// number, MET or NOT MET and the reason, and nothing else. A fence around it is allowed. Anything
// else is an error, never a verdict.
func Verdicts(answer string, judges []board.CheckSpec) ([]board.CmdResult, error) {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(answer), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "```") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) != len(judges) {
		return nil, fmt.Errorf("%d lines for %d criteria", len(lines), len(judges))
	}
	out := make([]board.CmdResult, len(judges))
	seen := map[int]bool{}
	for _, l := range lines {
		m := verdictLine.FindStringSubmatch(l)
		if m == nil {
			return nil, fmt.Errorf("a line is not \"N. MET: reason\" or \"N. NOT MET: reason\"")
		}
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > len(judges) || seen[n] {
			return nil, fmt.Errorf("criterion %s is not one of 1 to %d once", m[1], len(judges))
		}
		seen[n] = true
		reason := m[3]
		if len(reason) > 1000 {
			reason = reason[:1000] + "…"
		}
		out[n-1] = board.CmdResult{Name: judges[n-1].Spec, Passed: m[2] == "MET", Output: reason}
	}
	return out, nil
}

// judgeText is what a judge found not met, each criterion with its reason: data for the builder.
func judgeText(run *board.CheckRun) string {
	var b strings.Builder
	for _, r := range run.Results {
		if !r.Passed {
			fmt.Fprintf(&b, "- %s\n  The judge: %s\n", r.Name, r.Output)
		}
	}
	return b.String()
}
