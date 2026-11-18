package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// maxSummaryTries is how often a summary is tried before its written sections stay missing (21).
const maxSummaryTries = 3

// summary has a finished ticket's summary written (8.4): its facts are computed from the records at
// once; a run of the judge kind, with the ticket's records and no tools, writes its three sections.
func (p *Pipeline) summary(ctx context.Context, j *job, t *board.Ticket, subs []*sub) error {
	// The board's own pass may have moved it to Done before the merge queue saw it pass.
	if t.Stage == settings.StageDone && t.Branch != nil {
		p.recordMerge(ctx, j, t)
	}
	for _, s := range subs {
		if s.kind == kindSummary && s.active() {
			return nil
		}
	}
	var missing bool
	var tries int
	err := p.Pool.QueryRow(ctx, `SELECT missing, tries FROM summaries WHERE ticket = $1`, t.ID).Scan(&missing, &tries)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		facts, err := p.facts(ctx, j, t)
		if err != nil {
			return err
		}
		outcome := "done"
		if t.ClosedReason != nil {
			outcome = *t.ClosedReason
		}
		// A ticket withdrawn in Planning never ran: its facts say so, and no summary writer is
		// started for it, as nothing happened to write about (8.4, decided after M6).
		var withdrawn bool
		if err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stage_moves WHERE ticket = $1 AND from_stage = 'planning' AND to_stage = 'closed')`,
			t.ID).Scan(&withdrawn); err != nil {
			return err
		}
		if withdrawn {
			facts["withdrawn_in_planning"] = true
		}
		if _, err := p.Pool.Exec(ctx, `INSERT INTO summaries (ticket, project, outcome, facts, missing) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			t.ID, j.proj.ID, outcome, facts, !withdrawn); err != nil {
			return err
		}
		if withdrawn {
			return nil
		}
		// The kind keeps its stored name; since 0.53 it holds tokens and minutes, and no cost (10.2).
		p.signal(ctx, j, t, "", "cost_and_time", map[string]any{"tokens": facts["tokens"], "minutes": facts["minutes"], "outcome": outcome})
		missing = true
	case err != nil:
		return err
	}
	if !missing || tries >= maxSummaryTries {
		return nil
	}
	rec, err := p.summaryRecord(ctx, t, subs)
	if err != nil {
		return err
	}
	f := newFence(rec)
	parts := []runs.PromptPart{
		p.agentPart(ctx, j.proj.ID, "summary"),
		p.stagePart(ctx, j.proj.ID, "summary"),
		{Part: "record", Source: "ticket " + t.Key, Text: f.data("The ticket's record", "Crew, from the ticket's rows and the agents' entries", rec)},
	}
	run, err := p.start(ctx, j, t, startSpec{kind: kindSummary, role: "summary", title: "Write the summary", parts: parts, judge: true})
	if err != nil || run == "" {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE summaries SET tries = tries + 1, subtask = (SELECT subtask FROM runs WHERE id = $2) WHERE ticket = $1`, t.ID, run)
	return err
}

// afterSummary reads the summary writer's answer, which must be in its fixed form; anything else
// leaves the sections missing, to be tried again (21).
func (p *Pipeline) afterSummary(ctx context.Context, j *job, t *board.Ticket, s *sub) error {
	var answer string
	err := p.Pool.QueryRow(ctx, `SELECT text FROM turns WHERE run = $1 AND speaker = 'agent' ORDER BY number DESC LIMIT 1`, s.run).Scan(&answer)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if done, how, next, ok := sections(answer); ok {
		if _, err := p.Pool.Exec(ctx, `UPDATE summaries SET done = $2, how = $3, next = $4, missing = false, written_at = now() WHERE ticket = $1`,
			t.ID, done, how, next); err != nil {
			return err
		}
	} else {
		p.log().Warn("a summary's answer is not in its form; it is tried again", "ticket", t.Key, "run", s.run)
	}
	return p.Runs.Finish(ctx, s.run)
}

// summaryHeadings are the three sections a summary writer answers with (6.3).
var summaryHeadings = []string{"## What was done", "## How it went", "## What to know next time"}

// sections reads the three sections of an answer in the fixed form, each at most 2000 characters.
func sections(answer string) (done, how, next string, ok bool) {
	rest := strings.TrimSpace(answer)
	var out []string
	for i, h := range summaryHeadings {
		if !strings.HasPrefix(rest, h) {
			return "", "", "", false
		}
		rest = strings.TrimSpace(strings.TrimPrefix(rest, h))
		end := len(rest)
		if i+1 < len(summaryHeadings) {
			end = strings.Index(rest, summaryHeadings[i+1])
			if end < 0 {
				return "", "", "", false
			}
		}
		sec := strings.TrimSpace(rest[:end])
		if sec == "" || len(sec) > 2000 {
			return "", "", "", false
		}
		out = append(out, sec)
		rest = strings.TrimSpace(rest[end:])
	}
	return out[0], out[1], out[2], true
}

// facts are the frontmatter of a summary (6.3), computed from the records, never from a model.
func (p *Pipeline) facts(ctx context.Context, j *job, t *board.Ticket) (map[string]any, error) {
	f := map[string]any{"task": t.Key, "fix_rounds": t.FixRounds}
	var started *time.Time
	moves := map[string]float64{}
	rows, err := p.Pool.Query(ctx, `SELECT from_stage, to_stage, at FROM stage_moves WHERE ticket = $1 ORDER BY id`, t.ID)
	if err != nil {
		return nil, err
	}
	var prev *time.Time
	for rows.Next() {
		var from, to string
		var at time.Time
		if err := rows.Scan(&from, &to, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if from == settings.StageBacklog && started == nil {
			a := at
			started = &a
		}
		if prev != nil && from != settings.StagePlanning && from != settings.StageBacklog {
			moves[from] += float64(int(at.Sub(*prev).Minutes()))
		}
		a := at
		prev = &a
	}
	rows.Close()
	if started != nil {
		f["started"] = started.UTC().Format(time.RFC3339)
		f["minutes"] = int(t.StageAt.Sub(*started).Minutes())
	}
	f["ended"] = t.StageAt.UTC().Format(time.RFC3339)
	f["minutes_in_stage"] = moves
	type subLine struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Role    string `json:"role"`
		Run     string `json:"run,omitempty"`
		CLI     string `json:"cli,omitempty"`
		Model   string `json:"model,omitempty"`
		Minutes int    `json:"minutes"`
		Tokens  int64  `json:"tokens"`
	}
	var subs []subLine
	var tokens int64
	rows, err = p.Pool.Query(ctx, `SELECT s.number, s.kind, s.role, coalesce(r.id::text, ''), coalesce(r.cli, ''), coalesce(r.model, ''),
			coalesce(extract(epoch FROM (coalesce(r.ended_at, now()) - r.started_at)) / 60, 0)::int,
			coalesce(r.tokens_in + r.tokens_out, 0)
		FROM subtasks s LEFT JOIN runs r ON r.subtask = s.id WHERE s.ticket = $1 ORDER BY s.number, r.number`, t.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l subLine
		var n int
		if err := rows.Scan(&n, &l.Kind, &l.Role, &l.Run, &l.CLI, &l.Model, &l.Minutes, &l.Tokens); err != nil {
			rows.Close()
			return nil, err
		}
		l.ID = fmt.Sprintf("%s.%d", t.Key, n)
		tokens += l.Tokens
		subs = append(subs, l)
	}
	rows.Close()
	f["subtasks"], f["tokens"] = subs, tokens
	var checks []map[string]any
	cr, err := p.Board.CheckRuns(ctx, t)
	if err != nil {
		return nil, err
	}
	for i := len(cr) - 1; i >= 0; i-- {
		c := cr[i]
		if c.State == board.CheckRunning {
			continue
		}
		checks = append(checks, map[string]any{"name": c.Purpose + " on " + c.Commit[:12], "passed": c.State == board.CheckPassed, "flaky": c.Flaky})
	}
	f["checks"] = checks
	for _, c := range cr {
		if c.Purpose == board.PurposeMerge && c.State == board.CheckPassed && c.Main != nil && c.Head != nil && t.Stage == settings.StageDone {
			commits, files, err := p.Board.Git.Changes(ctx, j.proj.Name, *c.Main, *c.Head)
			if err == nil {
				var docs []string
				for _, d := range t.Docs {
					for _, x := range files {
						if x == d {
							docs = append(docs, d)
						}
					}
				}
				f["commits"], f["files_changed"], f["docs_changed"], f["merged_as"] = commits, len(files), docs, c.Commit
			}
			break
		}
	}
	return f, nil
}

// summaryRecord is what the summary writer reads: the ticket, its entries, and its subtasks with how
// each ended.
func (p *Pipeline) summaryRecord(ctx context.Context, t *board.Ticket, subs []*sub) (string, error) {
	var b strings.Builder
	b.WriteString(ticketText(t))
	if t.ClosedReason != nil {
		fmt.Fprintf(&b, "Outcome: %s, %s\n", *t.ClosedReason, t.Analysis.Reason)
	} else {
		fmt.Fprintf(&b, "Outcome: %s\n", t.Stage)
	}
	fmt.Fprintf(&b, "Fix rounds: %d\n\nSubtasks:\n", t.FixRounds)
	for _, s := range subs {
		if s.kind == kindSummary {
			continue
		}
		fmt.Fprintf(&b, "- %s by the %s: %s\n", s.kind, s.role, s.state)
	}
	recs, err := p.Board.Records(ctx, t, false)
	if err != nil {
		return "", err
	}
	b.WriteString("\nEntries, oldest first:\n")
	for _, r := range recs {
		body := r.Body
		if len(body) > 1500 {
			body = body[:1500] + "…"
		}
		fmt.Fprintf(&b, "\n%s by the %s:\n%s\n", r.Kind, r.Author, body)
	}
	cr, err := p.Board.CheckRuns(ctx, t)
	if err != nil {
		return "", err
	}
	b.WriteString("\nCrew's check runs, oldest first:\n")
	for i := len(cr) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "- %s on %s: %s\n", cr[i].Purpose, cr[i].Commit[:12], cr[i].State)
	}
	return b.String(), nil
}
