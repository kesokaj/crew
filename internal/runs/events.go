package runs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/models"
)

func gzipText(s string) ([]byte, string) {
	return gzipBytes([]byte(s))
}

func gzipBytes(b []byte) ([]byte, string) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	sum := sha256.Sum256(b)
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 512<<20))
}

// follow writes every event of the run to the database, in order, and acts on it. An event that
// cannot be stored is tried again: the spool keeps it meanwhile (12.4). When events cannot be stored
// for StoreGrace, the run is stopped, because no run goes on without its trajectory (8.3); the
// events it printed are still stored when the database answers again.
func (d *Driver) follow(ctx context.Context, lr *liveRun) {
	ch := d.Harness.Events(lr.id)
	if ch == nil {
		return
	}
	for {
		var ev harness.RunEvent
		select {
		case <-lr.quit:
			return
		case ev = <-ch:
		}
		var failingSince time.Time
		for wait := 100 * time.Millisecond; ; wait = min(2*wait, 10*time.Second) {
			var err error
			ended := false
			if ev.End != nil {
				ended, err = d.finishTurn(ctx, lr, ev)
			} else {
				err = d.store(ctx, lr, ev)
			}
			if err == nil {
				if ev.End == nil {
					d.react(ctx, lr, ev)
				}
				if ended {
					return
				}
				break
			}
			if failingSince.IsZero() {
				failingSince = d.now()
			}
			d.Log.Error("an event of the run is not stored", "run", lr.id, "error", err)
			if d.now().Sub(failingSince) > StoreGrace {
				d.askStop(ctx, lr, cli.ReasonError, "the run's events could not be stored")
			}
			select {
			case <-lr.quit:
				return
			case <-time.After(wait):
			}
		}
	}
}

func eventPayload(lr *liveRun, ev harness.RunEvent) ([]byte, []byte) {
	payload := map[string]any{}
	if ev.Payload != nil {
		payload = lr.redact.Value(ev.Payload).(map[string]any)
	}
	if cli.SafeID(ev.ConversationID) {
		payload["conversationId"] = ev.ConversationID
	}
	p, _ := json.Marshal(payload)
	var raw []byte
	if ev.Raw != nil {
		raw = lr.redact.Line(ev.Raw)
	}
	return p, raw
}

// insertEvent writes one event and moves the run's counters, in tx.
func (d *Driver) insertEvent(ctx context.Context, tx pgx.Tx, lr *liveRun, ev harness.RunEvent) error {
	payload, raw := eventPayload(lr, ev)
	toolStep := 0
	if ev.Type == cli.EventToolCall {
		toolStep = 1
	}
	var seq int64
	var conv string
	if err := tx.QueryRow(ctx, `UPDATE runs SET events = events + 1, last_event_at = $2,
			tool_steps = tool_steps + $3, last_action = coalesce(nullif($4, ''), last_action),
			state = CASE WHEN state = 'starting' THEN 'working' ELSE state END
		WHERE id = $1 RETURNING events, conversation`, lr.id, ev.At, toolStep, lastAction(lr, ev)).Scan(&seq, &conv); err != nil {
		return err
	}
	var rawText *string
	if raw != nil {
		s := strings.ToValidUTF8(string(raw), "�")
		rawText = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (run, conversation, seq, at, type, payload, raw) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		lr.id, conv, seq, ev.At, ev.Type, payload, rawText); err != nil {
		return err
	}
	if ev.Offset > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO run_spools (run, turn, "offset", lines) VALUES ($1, $2, $3, 1)
			ON CONFLICT (run, turn) DO UPDATE SET "offset" = $3, lines = run_spools.lines + 1`, lr.id, ev.Turn, ev.Offset); err != nil {
			return err
		}
	}
	if ev.ConversationID != "" && cli.SafeID(ev.ConversationID) {
		if _, err := tx.Exec(ctx, `UPDATE conversations SET cli_id = $2 WHERE id = $1 AND cli_id IS NULL`, conv, ev.ConversationID); err != nil {
			return err
		}
	}
	// The full name the CLI says it runs, from its first event that names one: an alias such as opus
	// moves when the vendor has a newer model, and results are kept under the full name (12.3).
	if m, _ := ev.Payload["model"].(string); m != "" && ev.Type == cli.EventStatus {
		if _, err := tx.Exec(ctx, `UPDATE runs SET model_resolved = $2 WHERE id = $1 AND model_resolved IS NULL`, lr.id, m); err != nil {
			return err
		}
	}
	if ev.Type == cli.EventToolCall {
		if cmd := installed(lr.redact.Text(ev.ToolInput)); cmd != "" {
			if err := recordInstalled(ctx, tx, lr.id, cmd); err != nil {
				return err
			}
		}
	}
	// The first event of a run says how long its sandbox took to start, clone, and set up (10.2).
	if seq == 1 {
		if _, err := tx.Exec(ctx, `INSERT INTO signals (project, ticket, run, kind, role, cli, model, effort, crew_version, detail)
			SELECT s.project, s.ticket, r.id, 'sandbox_startup', s.role, r.cli, r.model, r.effort, r.crew_version,
				jsonb_build_object('seconds', round(extract(epoch FROM ($2::timestamptz - r.started_at))::numeric, 1))
			FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1`, lr.id, ev.At); err != nil {
			return err
		}
	}
	return nil
}

// signal records a signal about a run (10.2), with what it ran on; a failure is logged and stops
// nothing.
func (d *Driver) signal(ctx context.Context, run, kind string, detail map[string]any) {
	b, _ := json.Marshal(detail)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO signals (project, ticket, run, kind, role, cli, model, effort, crew_version, detail)
		SELECT s.project, s.ticket, r.id, $2, s.role, r.cli, r.model, r.effort, r.crew_version, $3
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1`, run, kind, b); err != nil {
		d.Log.Error("recording a signal", "run", run, "kind", kind, "error", err)
	}
}

// store writes one event, with the place of its line in the spool, in one transaction: so every
// line becomes one row exactly once, also across a restart (12.4).
func (d *Driver) store(ctx context.Context, lr *liveRun, ev harness.RunEvent) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error { return d.insertEvent(ctx, tx, lr, ev) })
}

// lastAction is what the monitor shows as the run's last step (8.1).
func lastAction(lr *liveRun, ev harness.RunEvent) string {
	switch ev.Type {
	case cli.EventToolCall:
		return trim(lr.redact.Text(ev.Tool+" "+ev.ToolInput), 160)
	case cli.EventMessage:
		// agy sends its text in pieces, the last often a line break alone: a piece with no words
		// leaves the last action as it was.
		if strings.TrimSpace(ev.Speaks) != "" {
			return trim("said: "+lr.redact.Text(ev.Speaks), 160)
		}
	}
	return ""
}

func trim(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// react is the watchdog's part that follows every event (8.2): the turn limit, the same action
// failing in a row, and a CLI that lost Crew's MCP (12.2).
func (d *Driver) react(ctx context.Context, lr *liveRun, ev harness.RunEvent) {
	lr.mu.Lock()
	var warn, stop, why string
	switch ev.Type {
	case cli.EventMessage:
		if ev.Speaks != "" {
			lr.speaks.WriteString(ev.Speaks)
		}
	case cli.EventToolCall:
		lr.steps++
		lr.calls = append(lr.calls, ev.Tool)
		if ev.ToolID != "" {
			lr.toolByID[ev.ToolID] = ev.Tool + " " + ev.ToolInput
		}
		switch {
		case lr.maxTurns > 0 && lr.steps >= lr.maxTurns:
			stop, why = ReasonTurns, fmt.Sprintf("the run took %d tool steps, its limit", lr.steps)
		case lr.maxTurns > 0 && lr.steps*5 >= lr.maxTurns*4 && !lr.warned["turns"]:
			lr.warned["turns"] = true
			warn = fmt.Sprintf("%d of %d tool steps", lr.steps, lr.maxTurns)
		}
	case cli.EventToolResult:
		key := lr.toolByID[ev.ToolID]
		if key == "" {
			key = ev.Tool + " " + ev.ToolInput
		}
		if ev.ToolFailed {
			if key == lr.lastFailed {
				lr.failStreak++
			} else {
				lr.lastFailed, lr.failStreak = key, 1
			}
		} else {
			lr.lastFailed, lr.failStreak = "", 0
		}
		switch {
		case lr.failStreak >= lr.limits.FailStop:
			stop, why = cli.ReasonError, fmt.Sprintf("the same action failed %d times in a row", lr.failStreak)
		case lr.failStreak == lr.limits.FailWarn:
			warn = fmt.Sprintf("the same action failed %d times in a row: %s", lr.failStreak, trim(lr.redact.Text(key), 120))
		}
	}
	lr.mu.Unlock()
	if warn != "" {
		d.warn(ctx, lr.id, warn)
	}
	if stop != "" {
		d.askStop(ctx, lr, stop, why)
	}
}

// Warn adds a warning to a run from outside the driver, such as the pipeline's for a run's third
// round of checks (8.2).
func (d *Driver) Warn(ctx context.Context, run, text string) { d.warn(ctx, run, text) }

// warn adds a warning to the run, which the monitor shows (8.2).
func (d *Driver) warn(ctx context.Context, run, text string) {
	w, _ := json.Marshal([]map[string]any{{"at": d.now().UTC(), "text": text}})
	if _, err := d.Pool.Exec(ctx, `UPDATE runs SET warnings = warnings || $2::jsonb WHERE id = $1`, run, w); err != nil {
		d.Log.Warn("recording a warning", "run", run, "error", err)
	}
	d.Log.Warn("run warning", "run", run, "warning", text)
	d.signal(ctx, run, "watchdog_warning", map[string]any{"warning": text})
}

// askStop asks the running turn to stop for reason, once. The reason is Crew's own, and wins over
// what the turn says about itself.
func (d *Driver) askStop(ctx context.Context, lr *liveRun, reason, detail string) {
	lr.mu.Lock()
	if lr.stopping != "" {
		lr.mu.Unlock()
		return
	}
	lr.stopping, lr.stopDetail, lr.stopAt = reason, detail, d.now()
	lr.mu.Unlock()
	if _, err := d.Pool.Exec(ctx, `UPDATE runs SET stopping = $2 WHERE id = $1`, lr.id, reason); err != nil {
		d.Log.Warn("recording a stop", "run", lr.id, "error", err)
	}
	d.Log.Info("stopping a run", "run", lr.id, "reason", reason, "detail", detail)
	// A stop of Crew's own is the watchdog's; the operator's is no signal of how the agents did.
	if reason != ReasonOperator {
		d.signal(ctx, lr.id, "watchdog_stop", map[string]any{"reason": reason, "detail": trim(detail, 300)})
	}
	if err := d.Harness.Interrupt(ctx, lr.id, reason); err != nil && !errors.Is(err, harness.ErrNoRun) {
		d.Log.Warn("asking the run to stop", "run", lr.id, "error", err)
	}
}

// endRetry is how long endRun waits before each try of its transaction again.
var endRetry = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// pendingEnd is the end of a run that could not be written yet.
type pendingEnd struct {
	reason, detail string
	exitCode       *int
	removeVM       bool
}

// errEnded says another caller ended the run first.
var errEnded = errors.New("the run has ended already")

// endRun ends a run: its microVM goes, its token ends, and its subtask waits, is retried, or is
// done, by the reason (12.4).
// Only one caller ends a run: the run, its stop, and its subtask are written by the one whose update
// found the run not ended yet, so a stop is never counted twice. The transaction is tried again for a
// while, because a run that is not ended keeps its subtask from going on.
func (d *Driver) endRun(ctx context.Context, lr *liveRun, reason, detail string, exitCode *int, removeVM bool) {
	detail = trim(lr.redact.Text(detail), 2000)
	var cliName string
	tx := func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE runs SET state = 'ended', stop_reason = $2, stop_detail = $3, ended_at = now(),
				stopping = NULL, exit_code = coalesce($4, exit_code)
			WHERE id = $1 AND state <> 'ended' RETURNING cli`, lr.id, reason, detail, exitCode).Scan(&cliName)
		if errors.Is(err, pgx.ErrNoRows) {
			return errEnded
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_tokens WHERE run = $1`, lr.id); err != nil {
			return err
		}
		return d.endSubtask(ctx, tx, lr.id, lr.subtask, cliName, reason, detail, lr.stopAll)
	}
	var err error
	for i := 0; ; i++ {
		if err = pgx.BeginFunc(ctx, d.Pool, tx); err == nil || errors.Is(err, errEnded) || i >= len(endRetry) || ctx.Err() != nil {
			break
		}
		d.Log.Warn("recording the end of a run; trying again", "run", lr.id, "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(endRetry[i]):
		}
	}
	if err != nil && !errors.Is(err, errEnded) {
		// The run stays followed, with its microVM, and the monitor ends it again: a run whose row is
		// not ended keeps its subtask from going on, so it is never let go of half ended.
		d.Log.Error("recording the end of a run; the monitor tries again", "run", lr.id, "error", err)
		lr.mu.Lock()
		lr.pendingEnd = &pendingEnd{reason: reason, detail: detail, exitCode: exitCode, removeVM: removeVM}
		lr.mu.Unlock()
		return
	}
	// The microVM goes with the caller that ended the run.
	if removeVM && !errors.Is(err, errEnded) {
		if serr := d.Harness.Stop(ctx, lr.id); serr != nil && !errors.Is(serr, harness.ErrNoRun) {
			d.Log.Warn("removing the run's microVM", "run", lr.id, "error", serr)
		}
	}
	d.mu.Lock()
	if cur, ok := d.live[lr.id]; ok && cur == lr {
		delete(d.live, lr.id)
		close(lr.quit)
	}
	d.mu.Unlock()
	if err != nil {
		return
	}
	d.Log.Info("run ended", "run", lr.id, "reason", reason, "detail", trim(detail, 300))
	// A run that stopped for its login has the login checked now; the subtask continues when it
	// works again (12.6).
	if reason == cli.ReasonLogin && d.Logins != nil {
		if a, ok := cli.Get(cliName); ok {
			d.Logins.CheckNow(ctx, a.Name(), a.RealEvery() == 0)
		}
	}
	// A run that ended at a limit has the limits read again where they can be read with no model
	// request, for the reset time (12.7).
	// What the run said is only its own account (12.4): Crew's own check, with its one request,
	// reads what the CLI says now and holds the group to its reset.
	if reason == cli.ReasonQuota && d.Logins != nil {
		d.Logins.CheckNow(ctx, cliName, true)
	}
}

// endSubtask records on a run's subtask what the run's end means for it, in the transaction that ended
// the run: done for a turn that ended well, else what it waits for, and a stop that counts.
func (d *Driver) endSubtask(ctx context.Context, tx pgx.Tx, run string, subtask int64, cliName, reason, detail string, stopAll bool) error {
	state, waitFor, add := "waiting", "", 0
	var until *time.Time
	switch {
	case reason == cli.ReasonOK:
		state = "done"
	case reason == cli.ReasonLogin:
		waitFor = "login:" + cliName
	case reason == cli.ReasonQuota:
		// The run's group, or its model, is at its limit until the reset the CLI said, else for a
		// while; the subtask continues on its next choice, or there after the reset (12.7).
		waitFor = "quota:" + cliName
		var model string
		if err := tx.QueryRow(ctx, `SELECT coalesce(model_resolved, model, '') FROM runs WHERE id = $1`, run).Scan(&model); err != nil {
			return err
		}
		a, _ := cli.Get(cliName)
		t, err := models.Reached(ctx, tx, cliName, a.Reached(model, detail), "", time.Time{})
		if err != nil {
			return err
		}
		until = &t
	case reason == cli.ReasonModel:
		// The CLI refused the name: it is passed over for a while, and the subtask continues on its
		// next choice (12.3, 21).
		waitFor = "model:" + cliName
		var name string
		if err := tx.QueryRow(ctx, `SELECT coalesce(model_name, model, '') FROM runs WHERE id = $1`, run).Scan(&name); err != nil {
			return err
		}
		if err := models.Refused(ctx, tx, cliName, name); err != nil {
			return err
		}
	case reason == ReasonRestart:
		waitFor = "restart"
	case reason == ReasonOperator && stopAll:
		waitFor = "held"
	case reason == cli.ReasonSetup:
		// The project's setup failed before the CLI started (12.4): retried once in a new sandbox,
		// counted as no stop; a second failure waits for the pipeline, which checks the setup on
		// main and makes the fix ticket, or counts the failures as stops of the ticket (13.5).
		var fails int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE subtask = $1 AND stop_reason = 'setup'`, subtask).Scan(&fails); err != nil {
			return err
		}
		waitFor = "retry"
		if fails >= 2 {
			waitFor = "setup"
		}
	case counting[reason]:
		add = 1
		waitFor = "retry"
	default:
		waitFor = "operator"
	}
	// A conversation's subtask goes on only with the operator's next message (D23): done after a turn
	// that ended well, an idle end, Close or an eviction, else stopped, waiting for nothing that Crew
	// continues by itself. What a limit or a refused model records above holds for every run.
	var conv bool
	if err := tx.QueryRow(ctx, `SELECT scope = 'conversation' FROM subtasks WHERE id = $1`, subtask).Scan(&conv); err != nil {
		return err
	}
	if conv {
		if state != "done" {
			state = "stopped"
		}
		_, err := tx.Exec(ctx, `UPDATE subtasks SET state = $2, wait_for = NULL, wait_until = NULL, updated_at = now() WHERE id = $1`, subtask, state)
		return err
	}
	var stops int
	if err := tx.QueryRow(ctx, `UPDATE subtasks SET stops = stops + $2, updated_at = now() WHERE id = $1 RETURNING stops`,
		subtask, add).Scan(&stops); err != nil {
		return err
	}
	// A subtask stopped twice for a reason that counts goes to the lead (7.7); until the lead
	// exists, to the operator.
	if waitFor == "retry" && stops >= 2 {
		state, waitFor = "stopped", "operator"
	}
	_, err := tx.Exec(ctx, `UPDATE subtasks SET state = $2, wait_for = nullif($3, ''), wait_until = $4 WHERE id = $1`,
		subtask, state, waitFor, until)
	return err
}

// EndWaited ends for a reason of Crew's own a run that the monitor ended well while it waited for Crew
// to act on its turn (IdleEnded), as End ends one that still waits (since 0.55): the run records the
// reason, and its subtask, done, goes on as End's would, to its one retry or to the lead.
func (d *Driver) EndWaited(ctx context.Context, runID, reason, detail string) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		var subtask int64
		var cliName string
		err := tx.QueryRow(ctx, `UPDATE runs r SET stop_reason = $2, stop_detail = $3 FROM subtasks s
			WHERE r.id = $1 AND s.id = r.subtask AND r.state = 'ended' AND r.stop_reason = $4 AND r.stop_detail = $5 AND s.state = 'done'
			RETURNING r.subtask, r.cli`, runID, reason, trim(detail, 2000), cli.ReasonOK, IdleEnded).Scan(&subtask, &cliName)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("only a run that ended while it waited for Crew can be ended so; %s is not one", runID)
		}
		if err != nil {
			return err
		}
		return d.endSubtask(ctx, tx, runID, subtask, cliName, reason, detail, false)
	})
}
