package planning

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/settings"
)

// The classes of the unified run queue, in the order a free slot is given (7.9, R53).
const (
	ClassOperatorWaiting = "operator_waiting"
	ClassTicket          = "ticket"
	ClassAnalysis        = "analysis"
	ClassFinalReport     = "final_report"
	ClassHousekeeping    = "housekeeping"
	ClassRetrospective   = "retrospective"
	ClassEvals           = "evals"
	ClassImprovement     = "improvement"
)

// NormalPriority is a ticket's priority when it has none of its own, and the priority of work with
// no ticket (7.9): 0 is urgent, 1 high, 2 normal, 3 low.
const NormalPriority = 2

// LeadAsk is an agent run that waits for a concurrent run slot of its CLI (7.9).
type LeadAsk struct {
	// Key names what the run is for, the same on every pass while it waits.
	Key string
	// Owner is the loop that asks, which takes away its own asks that no longer wait.
	Owner string
	Class string
	// Keyboard says the operator waits at the keyboard: it goes first in its class, and bypasses
	// background ticket contention up to conversations.max_sandboxes.
	Keyboard bool
	// Continues says this ask resumes an in-flight subtask (e.g. after a check sandbox completed).
	Continues bool
	// StageRank is how far right the ticket's stage is (capacity.StageRank).
	StageRank int
	// Priority orders the ask across projects: the priority the operator set on its ticket, else
	// normal. Own is the ticket's priority as it is, an agent's too, which orders the asks of its own
	// project only (7.9, from 0.42): a curator cannot put its project before another by marking its own
	// tickets urgent.
	Priority, Own int
	// Project is the project's id, or "" for none; Ticket the ticket's key, for the front page.
	Project, Ticket string
	CLI, What       string
}

// TakeSlot writes the ask into the queue, or marks it seen again, and says whether it may start now:
// its CLI has a free slot left after every ask before it in the queue, or the ask is an interactive
// keyboard turn (`ask.Keyboard`) within the reserved interactive capacity (`conversations.max_sandboxes`).
// The caller holds SlotMu, starts the run when this says yes, and then calls Taken.
func TakeSlot(ctx context.Context, pool *pgxpool.Pool, doc settings.Doc, ask LeadAsk) (bool, error) {
	if ask.Class == "" {
		ask.Class = ClassAnalysis
	}
	if ask.Class == ClassAnalysis && ask.StageRank == 0 {
		ask.StageRank = 3
	}
	var project *string
	if ask.Project != "" {
		project = &ask.Project
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lead_waits (key, owner, class, keyboard, continues, stage_rank, priority, own_priority, project, ticket, cli, what)
		VALUES ($1, $2, $3, $4, $11, $12, $5, $10, $6, nullif($7, ''), $8, $9)
		ON CONFLICT (key) DO UPDATE SET owner = $2, class = $3, keyboard = $4, continues = $11, stage_rank = $12, priority = $5, own_priority = $10, project = $6,
			ticket = nullif($7, ''), cli = $8, what = $9, seen_at = now()`,
		ask.Key, ask.Owner, ask.Class, ask.Keyboard, ask.Priority, project, ask.Ticket, ask.CLI, ask.What, ask.Own, ask.Continues, ask.StageRank); err != nil {
		return false, err
	}
	free, err := FreeSlots(ctx, pool, doc)
	if err != nil {
		return false, err
	}
	waits, err := Queue(ctx, pool, doc)
	if err != nil {
		return false, err
	}
	before := 0
	keyboardBefore := 0
	for _, w := range waits {
		if w.Key == ask.Key {
			break
		}
		if w.CLI == ask.CLI {
			before++
			if w.Keyboard {
				keyboardBefore++
			}
		}
	}
	if before < free[ask.CLI] {
		return true, nil
	}
	if ask.Keyboard {
		slots, err := Slots(ctx, pool, doc)
		if err != nil {
			return false, err
		}
		maxSandboxes := 3
		switch v := dig(doc, "conversations", "max_sandboxes").(type) {
		case int:
			if v > 0 {
				maxSandboxes = v
			}
		case float64:
			if int(v) > 0 {
				maxSandboxes = int(v)
			}
		}
		maxInteractive := min(slots[ask.CLI], maxSandboxes)
		var nonTicketWorking int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN subtasks s ON s.id = r.subtask
			WHERE r.cli = $1 AND r.state IN ('starting', 'working')
			  AND (s.kind <> 'helper' OR s.parent IS NULL)
			  AND s.scope <> 'ticket'`, ask.CLI).Scan(&nonTicketWorking); err != nil {
			return false, err
		}
		if nonTicketWorking+keyboardBefore < maxInteractive {
			return true, nil
		}
	}
	return false, nil
}

// Taken takes away the ask of a run that started.
func Taken(ctx context.Context, pool *pgxpool.Pool, key string) error {
	_, err := pool.Exec(ctx, `DELETE FROM lead_waits WHERE key = $1`, key)
	return err
}

// Sweep takes away the asks of one loop that it did not write again since a time: they no longer wait.
func Sweep(ctx context.Context, pool *pgxpool.Pool, owner string, since time.Time) error {
	_, err := pool.Exec(ctx, `DELETE FROM lead_waits WHERE owner = $1 AND seen_at < $2`, owner, since)
	return err
}

// LeadWait is one run in the unified scheduler queue, for the front page.
type LeadWait struct {
	Key       string    `json:"key"`
	Class     string    `json:"class"`
	Keyboard  bool      `json:"keyboard"`
	Continues bool      `json:"continues"`
	StageRank int       `json:"stageRank,omitempty"`
	Priority  int       `json:"priority"`
	Own       int       `json:"ownPriority"`
	Project   string    `json:"project,omitempty"`
	Ticket    string    `json:"ticket,omitempty"`
	CLI       string    `json:"cli"`
	What      string    `json:"what"`
	Since     time.Time `json:"since"`
}

// Queue lists the agent runs that wait, in the order a free slot goes to them (7.9):
//  1. operator_waiting (keyboard first, then breakdown),
//  2. ticket work (resuming continues runs first, then stage furthest right, then operator priority across projects,
//     then the project served least recently, then the ticket's own priority within its project, then longest waiting),
//  3. low-priority maintenance (final_report, housekeeping, retrospective, evals, improvement).
func Queue(ctx context.Context, pool *pgxpool.Pool, _ settings.Doc) ([]LeadWait, error) {
	rows, err := pool.Query(ctx, `SELECT w.key, w.class, w.keyboard, w.continues, w.stage_rank, w.priority, w.own_priority, coalesce(p.name, ''), coalesce(w.ticket, ''), w.cli, w.what, w.since
		FROM lead_waits w LEFT JOIN projects p ON p.id = w.project
		LEFT JOIN LATERAL (SELECT max(r.started_at) AS last FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE s.project = w.project) served ON true
		ORDER BY
			CASE
				WHEN w.class = 'operator_waiting' THEN 1
				WHEN w.class IN ('ticket', 'analysis') THEN 2
				WHEN w.class = 'final_report' THEN 3
				WHEN w.class = 'housekeeping' THEN 4
				WHEN w.class = 'retrospective' THEN 5
				WHEN w.class = 'evals' THEN 6
				WHEN w.class = 'improvement' THEN 7
				ELSE 99
			END,
			w.keyboard DESC,
			w.continues DESC,
			CASE WHEN w.stage_rank > 0 THEN w.stage_rank WHEN w.class = 'analysis' THEN 3 ELSE 0 END DESC,
			w.priority,
			served.last NULLS FIRST,
			w.project,
			w.own_priority,
			w.since,
			w.key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LeadWait, error) {
		var w LeadWait
		err := r.Scan(&w.Key, &w.Class, &w.Keyboard, &w.Continues, &w.StageRank, &w.Priority, &w.Own, &w.Project, &w.Ticket, &w.CLI, &w.What, &w.Since)
		return w, err
	})
}
