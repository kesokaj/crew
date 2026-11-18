package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/runs"
)

// kindReport is the lead's run that writes a Done project's final report (7.2, 9.8).
const kindReport = "report"

// ReportSections are the sections of a final report, in their order.
var ReportSections = []string{"built", "skipped", "cancelled", "missing", "learned"}

// finalReport gives a Done project its final report: one is due when the project's newest Done is
// newer than its newest report, so a project that runs again and is Done again gets the next one, and
// a restart in between loses none. The lead writes the sections in a run of its own, in the lead
// queue's class final_report; Crew counts the facts. A run that stops twice leaves the facts, with the
// sections marked not written and why (21).
func (p *Pipeline) finalReport(ctx context.Context, j *job) error {
	// A hidden project, crew-gate, gets none: its Done is a gate's real ticket (16.2).
	if j.proj.Hidden || j.proj.State != "done" {
		return nil
	}
	// A project Done with no such change, as one imported in that state, counts from its last change.
	var doneAt time.Time
	var reportAt *time.Time
	err := p.Pool.QueryRow(ctx, `SELECT
			coalesce((SELECT max(at) FROM changes WHERE tbl = 'projects' AND row_key = $1 AND after->>'state' = 'done'),
				(SELECT updated_at FROM projects WHERE id = $1::uuid)),
			(SELECT max(at) FROM reports WHERE project = $1::uuid)`, j.proj.ID).Scan(&doneAt, &reportAt)
	if err != nil || (reportAt != nil && reportAt.After(doneAt)) {
		return err
	}
	var sub int64
	var state, runID, runState, detail string
	var rep []byte
	var reminded bool
	err = p.Pool.QueryRow(ctx, `SELECT s.id, s.state, s.reported, s.reminded, coalesce(r.id::text, ''), coalesce(r.state, ''), coalesce(r.stop_detail, '')
		FROM subtasks s LEFT JOIN LATERAL (SELECT * FROM runs WHERE subtask = s.id ORDER BY number DESC LIMIT 1) r ON true
		WHERE s.project = $1 AND s.ticket IS NULL AND s.kind = $2 AND s.created_at > $3 ORDER BY s.id DESC LIMIT 1`,
		j.proj.ID, kindReport, doneAt).Scan(&sub, &state, &rep, &reminded, &runID, &runState, &detail)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if j.held != "" {
			return nil
		}
		// The retrospective of this Done comes first: its lessons are what the report learned (9.8).
		if pending, err := p.retroPending(ctx, j); err != nil || pending {
			return err
		}
		return p.startReport(ctx, j)
	case err != nil:
		return err
	case state == "stopped":
		return p.writeReportRow(ctx, j, nil, "", "the lead's run stopped twice: "+short(detail, 300))
	case runState != "idle":
		return nil
	}
	var r report
	if len(rep) > 0 && json.Unmarshal(rep, &r) == nil && r.Tool == "write_report" {
		if err := p.writeReportRow(ctx, j, r.Sections, runID, ""); err != nil {
			return err
		}
		return p.Runs.Finish(ctx, runID)
	}
	// A turn that ended without the report: one reminder, then the run has failed and is retried once.
	if !reminded {
		var cliName string
		if err := p.Pool.QueryRow(ctx, `SELECT cli FROM runs WHERE id = $1`, runID).Scan(&cliName); err != nil {
			return err
		}
		defer planning.LockSlots(p.Pool)()
		key := fmt.Sprintf("turn:%d", sub)
		ok, err := planning.TakeSlot(ctx, p.Pool, j.machine, planning.LeadAsk{Key: key, Owner: "pipeline", Class: planning.ClassFinalReport,
			Continues: true, Priority: planning.NormalPriority, Own: planning.NormalPriority, Project: j.proj.ID, CLI: cliName, What: "The final report of " + j.proj.Name})
		if err != nil || !ok {
			return err
		}
		_, err = p.Runs.Continue(ctx, sub, runs.ContinueRequest{Auto: true, Finishing: true,
			Text: "You ended your turn without calling write_report. Write the five sections, call write_report, and end your turn.",
			Record: func(ctx context.Context, tx pgx.Tx, _ string) error {
				_, err := tx.Exec(ctx, `UPDATE subtasks SET reminded = true WHERE id = $1`, sub)
				return err
			}})
		tErr := planning.Taken(ctx, p.Pool, key)
		if errors.Is(err, runs.ErrBusy) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
			return nil
		}
		if err == nil {
			err = tErr
		}
		return err
	}
	return p.Runs.End(ctx, runID, runs.ReasonRounds, "the final report was not written")
}

// startReport starts the lead's run for the report, with a lead slot of its CLI in the class
// final_report (7.9).
func (p *Pipeline) startReport(ctx context.Context, j *job) error {
	facts, err := p.reportFacts(ctx, j)
	if err != nil {
		return err
	}
	c, err := models.Choose(ctx, p.Pool, models.Ask{Role: "curator", Machine: j.machine, Project: j.doc})
	if err != nil {
		return nil // no model of the curator can run now: the next pass tries again
	}
	defer planning.LockSlots(p.Pool)()
	key := "report:" + j.proj.ID
	ok, err := planning.TakeSlot(ctx, p.Pool, j.machine, planning.LeadAsk{Key: key, Owner: "pipeline", Class: planning.ClassFinalReport,
		Priority: planning.NormalPriority, Own: planning.NormalPriority, Project: j.proj.ID, CLI: c.CLI, What: "The final report of " + j.proj.Name})
	if err != nil || !ok {
		return err
	}
	b, _ := json.MarshalIndent(facts, "", "  ")
	f := newFence(string(b))
	parts := []runs.PromptPart{p.stagePart(ctx, j.proj.ID, "report"), p.agentPart(ctx, j.proj.ID, "curator"),
		{Part: "facts", Source: "Crew, project " + j.proj.Name, Text: f.data("What Crew counted", "Crew, with the titles and reasons agents wrote", string(b))}}
	_, _, err = p.Runs.StartRole(ctx, runs.RoleRun{Role: "curator", Kind: kindReport, Title: "Write the final report", Project: j.proj.ID, ReadOnly: true,
		CLI: c.CLI, Model: c.Model, Name: c.Name, Effort: c.Effort, From: c.ModelFrom, EffortFrom: c.EffortFrom, Parts: parts, Timeout: 30 * time.Minute})
	tErr := planning.Taken(ctx, p.Pool, key)
	if errors.Is(err, runs.ErrNotSignedIn) || errors.Is(err, runs.ErrHeld) || errors.Is(err, runs.ErrNoRoom) {
		return nil
	}
	if err != nil {
		return err
	}
	return tErr
}

// ReportFacts are what Crew counts itself for a final report.
type ReportFacts struct {
	Done      []string       `json:"done"`
	Cancelled []ReportTicket `json:"cancelled"`
	Skipped   []ReportTicket `json:"skipped"`
	Discarded []ReportTicket `json:"discarded"`
	Missing   []string       `json:"missing"`
	Tokens    int64          `json:"tokens"`
	Runs      int            `json:"runs"`
	FixRounds int            `json:"fixRounds"`
	ByEnd     map[string]int `json:"byEnd"`
	// Lessons are what the project's retrospectives wrote to its memory (9.8).
	Lessons []string `json:"lessons"`
}

// ReportTicket is a ticket that did not end Done, with why and what replaced it.
type ReportTicket struct {
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	Reason     string   `json:"reason"`
	ReplacedBy []string `json:"replacedBy,omitempty"`
}

func (p *Pipeline) reportFacts(ctx context.Context, j *job) (*ReportFacts, error) {
	f := &ReportFacts{Done: []string{}, Cancelled: []ReportTicket{}, Skipped: []ReportTicket{}, Discarded: []ReportTicket{}, Missing: []string{},
		ByEnd: map[string]int{}}
	rows, err := p.Pool.Query(ctx, `SELECT p.key || '-' || t.number, t.title, t.stage, coalesce(t.closed_reason, ''),
			coalesce(t.analysis->>'status', ''), coalesce(t.analysis->>'reason', ''), t.fix_rounds,
			coalesce((SELECT array_agg(p.key || '-' || r.number ORDER BY r.number) FROM ticket_links l JOIN tickets r ON r.id = l.from_ticket
				WHERE l.to_ticket = t.id AND l.kind = 'replaces'), '{}')
		FROM tickets t JOIN projects p ON p.id = t.project WHERE t.project = $1 ORDER BY t.number`, j.proj.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, title, stage, closed, status, reason string
		var rounds int
		var by []string
		if err := rows.Scan(&key, &title, &stage, &closed, &status, &reason, &rounds, &by); err != nil {
			rows.Close()
			return nil, err
		}
		f.FixRounds += rounds
		rt := ReportTicket{Key: key, Title: title, Reason: reason}
		switch {
		case stage == "done":
			f.Done = append(f.Done, key+" "+title)
			f.ByEnd["done"]++
		case closed == "cancelled":
			rt.ReplacedBy = by
			f.Cancelled = append(f.Cancelled, rt)
			f.ByEnd["cancelled"]++
		case closed == "discarded":
			f.Discarded = append(f.Discarded, rt)
			f.ByEnd["discarded"]++
		case status == "skipped":
			f.Skipped = append(f.Skipped, rt)
			f.ByEnd["skipped"]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	err = p.Pool.QueryRow(ctx, `SELECT coalesce(sum(r.tokens_in + r.tokens_out), 0)::bigint, count(*)
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.project = $1`, j.proj.ID).Scan(&f.Tokens, &f.Runs)
	if err != nil {
		return nil, err
	}
	rows, err = p.Pool.Query(ctx, `SELECT detail->>'text' FROM signals WHERE project = $1 AND kind = 'cancel_and_replace'
		AND (detail->>'missing')::bool ORDER BY id`, j.proj.ID)
	if err != nil {
		return nil, err
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	f.Missing = append(f.Missing, missing...)
	// What the project's retrospectives learned: the lessons their runs wrote (9.8).
	rows, err = p.Pool.Query(ctx, `SELECT m.title FROM memory m JOIN retrospectives r ON r.run = m.source_run
		WHERE m.project = $1 AND m.kind = 'lesson' AND m.status = 'active' ORDER BY m.number`, j.proj.ID)
	if err != nil {
		return nil, err
	}
	if f.Lessons, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	if f.Lessons == nil {
		f.Lessons = []string{}
	}
	return f, nil
}

// writeReportRow stores a report with the next number, the facts Crew counts now, and the lead's
// sections, or the reason they are missing.
func (p *Pipeline) writeReportRow(ctx context.Context, j *job, sections map[string]string, run, missing string) error {
	facts, err := p.reportFacts(ctx, j)
	if err != nil {
		return err
	}
	if sections == nil {
		sections = map[string]string{}
	}
	var runArg any
	if run != "" {
		runArg = run
	}
	_, err = p.Pool.Exec(ctx, `INSERT INTO reports (project, number, sections, facts, missing, run)
		VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM reports WHERE project = $1), $2, $3, nullif($4, ''), $5)`,
		j.proj.ID, sections, facts, missing, runArg)
	if err != nil {
		return err
	}
	p.log().Info("a final report is written", "project", j.proj.Name, "missing", missing)
	return nil
}

func (t *Tools) writeReport(ctx context.Context, c mcp.Caller, args json.RawMessage) (any, error) {
	var a map[string]string
	if err := mcp.Decode(args, &a); err != nil {
		return nil, err
	}
	k, _, err := t.subtaskOf(ctx, c)
	if err != nil {
		return nil, err
	}
	if k != kindReport || (c.Role != "curator" && c.Role != "lead") {
		return nil, mcp.Errf("write_report is for the curator's run that writes a Done project's final report; this run is a %s's %s", c.Role, k)
	}
	sections := map[string]string{}
	for _, s := range ReportSections {
		text := strings.TrimSpace(a[s])
		if len(text) > 20000 {
			return nil, mcp.Errf("%s: at most 20000 characters", s)
		}
		sections[s] = text
	}
	if err := t.report(ctx, c, report{Tool: "write_report", Sections: sections}); err != nil {
		return nil, err
	}
	return map[string]any{"recorded": "write_report", "next": "End your turn now."}, nil
}
