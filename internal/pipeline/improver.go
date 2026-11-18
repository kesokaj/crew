package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crew "github.com/kesokaj/crew"
	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/measure"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// kindImprove is the improver's run (10.4).
const kindImprove = "improve"

// improver moves the improvements on and runs the improver (10.4, 10.5): every improvements.run_every_days,
// sooner after improvements.run_after_tasks finished tickets across all projects since its last run, or
// when the operator asks; one at a time, in the last class of the lead queue.
func (p *Pipeline) improver(ctx context.Context, machine settings.Doc) error {
	if p.Improve == nil {
		return nil
	}
	if err := p.Improve.Step(ctx); err != nil {
		return err
	}
	if err := p.examples(ctx); err != nil {
		return err
	}
	if err := p.Improve.Measure(ctx, int(num(machine, "improvements.measure_after_tasks", 20))); err != nil {
		return err
	}
	var run, state string
	var started time.Time
	err := p.Pool.QueryRow(ctx, `SELECT r.id::text, r.state, r.started_at FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE s.scope = 'improver' AND s.kind = $1 ORDER BY r.started_at DESC LIMIT 1`, kindImprove).Scan(&run, &state, &started)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	case state == "idle":
		return p.Runs.Finish(ctx, run)
	case state != "ended":
		return nil // going, or waiting to start
	}
	due, why, err := p.improverDue(ctx, machine, started)
	if err != nil || !due {
		return err
	}
	// Its schedule bounds it (10.4): since 0.53 it has no weekly budget.
	return p.startImprover(ctx, machine, why)
}

// improverDue says whether the improver should run now, and why.
func (p *Pipeline) improverDue(ctx context.Context, machine settings.Doc, last time.Time) (bool, string, error) {
	var asked *time.Time
	if err := p.Pool.QueryRow(ctx, `SELECT improver_asked FROM crew_state WHERE id = 1`).Scan(&asked); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, "", err
	}
	if asked != nil && asked.After(last) {
		return true, "the operator asked for it", nil
	}
	// With no run yet, it counts from the first ticket that finished.
	if last.IsZero() {
		var first *time.Time
		if err := p.Pool.QueryRow(ctx, `SELECT min(at) FROM stage_moves WHERE to_stage = 'done'`).Scan(&first); err != nil || first == nil {
			return false, "", err
		}
		last = *first
	}
	var done int
	if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM stage_moves m JOIN tickets t ON t.id = m.ticket
		WHERE m.to_stage = 'done' AND m.at > $1 AND t.project <> 'f1c50000-0000-4000-8000-000000000001'`, last).Scan(&done); err != nil {
		return false, "", err
	}
	if after := int(num(machine, "improvements.run_after_tasks", 25)); done >= after {
		return true, fmt.Sprintf("%d tickets finished since its last run", done), nil
	}
	if every := num(machine, "improvements.run_every_days", 7); done > 0 && time.Since(last) >= time.Duration(every*24)*time.Hour {
		return true, fmt.Sprintf("%.0f days since its last run", every), nil
	}
	return false, "", nil
}

// startImprover starts the improver with a lead slot in the class improvement, which comes last in the
// lead queue (7.9).
func (p *Pipeline) startImprover(ctx context.Context, machine settings.Doc, why string) error {
	c, err := models.Choose(ctx, p.Pool, models.Ask{Role: "improver", Machine: machine})
	if err != nil {
		return nil // no model of the improver can run now: the next pass tries again
	}
	defer planning.LockSlots(p.Pool)()
	key := "improver"
	ok, err := planning.TakeSlot(ctx, p.Pool, machine, planning.LeadAsk{Key: key, Owner: "pipeline", Class: planning.ClassImprovement,
		Priority: planning.NormalPriority, Own: planning.NormalPriority, CLI: c.CLI, What: "The improver"})
	if err != nil || !ok {
		return err
	}
	var metrics []string
	for _, m := range measure.Metrics {
		metrics = append(metrics, m.Name+": "+m.Label)
	}
	say := fmt.Sprintf("Crew: the running Crew version is %s. You run now because %s. The metrics an improvement can name are:\n- %s",
		p.Version, why, strings.Join(metrics, "\n- "))
	parts := []runs.PromptPart{
		{Part: "agent", Source: "improver.md@" + p.Version, Text: baseline.Body(crew.Improver)},
		{Part: "context", Source: "crew", Text: say},
	}
	_, _, err = p.Runs.StartRole(ctx, runs.RoleRun{Role: "improver", Kind: kindImprove, Title: "The improver", Improver: true,
		CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: time.Hour, Owner: "pipeline"})
	tErr := planning.Taken(ctx, p.Pool, key)
	if errors.Is(err, runs.ErrNotSignedIn) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
		return nil
	}
	if err != nil {
		return err
	}
	return tErr
}

// example is one earlier first message of the operator's, rewritten by the prompt writer's rules as they
// are and as a change would make them (7.2).
type example struct {
	Rewrite  int64  `json:"rewrite"`
	Original string `json:"original"`
	Old      string `json:"old"`
	New      string `json:"new"`
}

// examples makes the three examples of a change of the prompt writer's rules that waits for the
// operator: six calls of the prompt writer, one after another, in the background.
func (p *Pipeline) examples(ctx context.Context) error {
	if p.Example == nil {
		return nil
	}
	p.mu.Lock()
	busy := p.exampling
	p.mu.Unlock()
	if busy {
		return nil
	}
	var id int64
	var raw []byte
	err := p.Pool.QueryRow(ctx, `SELECT id, patch FROM improvements WHERE status = 'proposed' AND waits_for = 'operator' AND examples = '[]'::jsonb
		ORDER BY id LIMIT 1`).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var patch map[string]string
	if err := json.Unmarshal(raw, &patch); err != nil {
		return err
	}
	rules := map[string]string{}
	for path, text := range patch {
		if rest, ok := strings.CutPrefix(path, "baseline/"); ok && baseline.PromptWriter(rest) {
			rules[rest] = text
		}
	}
	rows, err := p.Pool.Query(ctx, `SELECT id, original FROM prompt_rewrites WHERE kind = 'first' AND choice IS NOT NULL ORDER BY id DESC LIMIT 3`)
	if err != nil {
		return err
	}
	var list []example
	for rows.Next() {
		var e example
		if err := rows.Scan(&e.Rewrite, &e.Original); err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	rows.Close()
	p.mu.Lock()
	p.exampling = true
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			p.mu.Lock()
			p.exampling = false
			p.mu.Unlock()
		}()
		bg := context.WithoutCancel(ctx)
		for i := range list {
			for _, side := range []struct {
				rules map[string]string
				out   *string
			}{{nil, &list[i].Old}, {rules, &list[i].New}} {
				text, err := p.Example(bg, list[i].Rewrite, side.rules)
				if err != nil {
					text = "(no rewrite: " + err.Error() + ")"
				}
				*side.out = text
			}
		}
		// None is an answer too: there were no earlier prompts to show.
		if list == nil {
			list = []example{{Original: "(no earlier prompt)"}}
		}
		b, _ := json.Marshal(list)
		if _, err := p.Pool.Exec(bg, `UPDATE improvements SET examples = $2 WHERE id = $1`, id, b); err != nil {
			p.log().Error("the examples of a change of the prompt writer's rules", "error", err)
		}
	}()
	return nil
}
