package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
)

// Every conversations.summary_every_turns turns of a conversation (20 by default), Crew has its older
// turns summarised, keeping the newest keepTurns out of it: a conversation rebuilt from its turns
// (12.10, the third way) begins with the summary, then the turns after it in full. A run's
// conversation, the operator's about a project, and a ticket's chat have one; a conversation about no
// project has none, and is rebuilt from its last turns (plan M20 D21).
const keepTurns = 6

const kindConversation = "conversation_summary"

// conversations writes the summaries of a project's conversations that grew, one at a time, with a
// judge's call, and stores the answer of one that ended.
func (p *Pipeline) conversations(ctx context.Context, j *job) error {
	var sub int64
	var run, conv, state string
	var through int
	err := p.Pool.QueryRow(ctx, `SELECT s.id, coalesce(r.id::text, ''), coalesce(s.summarises::text, ''), coalesce(s.through, 0), coalesce(r.state, '')
		FROM subtasks s LEFT JOIN LATERAL (SELECT id, state FROM runs WHERE subtask = s.id ORDER BY number DESC LIMIT 1) r ON true
		WHERE s.project = $1 AND s.kind = $2 AND s.state IN ('running', 'idle', 'waiting') ORDER BY s.id LIMIT 1`,
		j.proj.ID, kindConversation).Scan(&sub, &run, &conv, &through, &state)
	switch {
	case err == nil && state == "idle":
		var answer string
		if err := p.Pool.QueryRow(ctx, `SELECT text FROM turns WHERE run = $1 AND speaker = 'agent' ORDER BY number DESC LIMIT 1`, run).Scan(&answer); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if a := strings.TrimSpace(answer); a != "" && len(a) <= 8000 {
			if _, err := p.Pool.Exec(ctx, `UPDATE conversations SET summary = $2, summary_through = $3 WHERE id = $1 AND summary_through < $3`,
				conv, a, through); err != nil {
				return err
			}
		}
		return p.Runs.Finish(ctx, run)
	case err == nil:
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	var last int
	var summary string
	// A conversation whose summary failed maxSummaryTries times since its last one is left: the
	// third way then gives its last turns, as it did before summaries.
	err = p.Pool.QueryRow(ctx, `SELECT c.id, c.summary_through, coalesce(c.summary, ''), max(t.number) FROM conversations c
		JOIN turns t ON t.conversation = c.id WHERE c.project = $1 AND c.purpose IN ('run', 'operator', 'chat')
			AND (SELECT count(*) FROM subtasks s WHERE s.summarises = c.id AND s.through > c.summary_through) < $3
		GROUP BY c.id HAVING max(t.number) - c.summary_through >= $2 ORDER BY max(t.at) DESC LIMIT 1`,
		j.proj.ID, int(num(j.machine, "conversations.summary_every_turns", 20))+keepTurns, maxSummaryTries).Scan(&conv, &through, &summary, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	upTo := last - keepTurns
	rows, err := p.Pool.Query(ctx, `SELECT number, speaker, text FROM turns WHERE conversation = $1 AND number > $2 AND number <= $3 ORDER BY number`,
		conv, through, upTo)
	if err != nil {
		return err
	}
	var b strings.Builder
	if summary != "" {
		fmt.Fprintf(&b, "Crew's summary of turns 1 to %d:\n%s\n\n", through, summary)
	}
	for rows.Next() {
		var n int
		var speaker, text string
		if err := rows.Scan(&n, &speaker, &text); err != nil {
			rows.Close()
			return err
		}
		if len(text) > 4000 {
			text = text[:4000] + "…"
		}
		fmt.Fprintf(&b, "Turn %d, %s:\n%s\n\n", n, speaker, strings.TrimSpace(text))
	}
	rows.Close()
	f := newFence(b.String())
	parts := []runs.PromptPart{p.stagePart(ctx, j.proj.ID, "conversation"),
		{Part: "turns", Source: "conversation " + conv, Text: f.data("The conversation", "the agents of the conversation, Crew, and the operator", b.String())}}
	c, err := models.Choose(ctx, p.Pool, models.Ask{Role: "summary", Machine: j.machine, Project: j.doc})
	if err != nil {
		return nil // no model of the summary writer can run now: the next pass tries again
	}
	defer planning.LockSlots(p.Pool)()
	key := "conv-summary:" + conv
	title := fmt.Sprintf("Summarise a conversation through turn %d", upTo)
	ok, err := planning.TakeSlot(ctx, p.Pool, j.machine, planning.LeadAsk{Key: key, Owner: "pipeline", Class: planning.ClassHousekeeping,
		Priority: planning.NormalPriority, Own: planning.NormalPriority, Project: j.proj.ID, CLI: c.CLI, What: title})
	if err != nil || !ok {
		return err
	}
	_, _, err = p.Runs.StartRole(ctx, runs.RoleRun{Role: "summary", Kind: kindConversation, Title: title,
		Project: j.proj.ID, Judge: true, CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: 10 * time.Minute,
		Record: func(ctx context.Context, tx pgx.Tx, run string) error {
			_, err := tx.Exec(ctx, `UPDATE subtasks SET summarises = $2, through = $3 WHERE id = (SELECT subtask FROM runs WHERE id = $1)`, run, conv, upTo)
			return err
		}})
	if tErr := planning.Taken(ctx, p.Pool, key); tErr != nil && err == nil {
		err = tErr
	}
	if errors.Is(err, runs.ErrNotSignedIn) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
		return nil
	}
	return err
}
