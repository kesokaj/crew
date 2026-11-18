package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// Monitor runs the watchdog's clock and the waiting subtasks until ctx ends (8.2, 12.4): it stops a
// silent run and one past its time, ends a run that waited too long for its next turn, and continues
// a subtask when what it waits for is there.
func (d *Driver) Monitor(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 10 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick is one round of the monitor.
func (d *Driver) Tick(ctx context.Context) {
	now := d.now()
	// The time limit and the silence are a turn's: the clock starts again with every turn (8.2).
	rows, err := d.Pool.Query(ctx, `SELECT id, state, turn_started_at, greatest(coalesce(last_event_at, turn_started_at), turn_started_at),
		timeout_s FROM runs WHERE state <> 'ended'`)
	if err != nil {
		d.Log.Warn("the monitor cannot read the runs", "error", err)
		return
	}
	type row struct {
		id, state     string
		started, last time.Time
		timeout       int
	}
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.state, &r.started, &r.last, &r.timeout) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	for _, r := range list {
		lr, ok := d.getLive(r.id)
		if !ok {
			continue
		}
		lr.mu.Lock()
		stopping, stopAt, pending := lr.stopping, lr.stopAt, lr.pendingEnd
		lr.pendingEnd = nil
		lr.mu.Unlock()
		if pending != nil {
			d.endRun(ctx, lr, pending.reason, pending.detail, pending.exitCode, pending.removeVM)
			continue
		}
		silent := now.Sub(r.last)
		limit := time.Duration(r.timeout) * time.Second
		lr.mu.Lock()
		silentWarn, silentStop := lr.limits.SilentWarn, lr.limits.SilentStop
		lr.mu.Unlock()
		switch {
		case stopping != "" && now.Sub(stopAt) > StopWait:
			// The turn did not end after it was asked to: the microVM goes, and the run with it.
			d.endRun(ctx, lr, stopping, "the turn did not end when it was asked to stop", nil, true)
		case r.state == "idle" && silent > d.idleFor(lr):
			d.endRun(ctx, lr, cli.ReasonOK, IdleEnded, nil, true)
		case r.state == "idle":
		case silent > silentStop:
			d.askStop(ctx, lr, ReasonSilent, "no event for "+silent.Round(time.Second).String())
		case now.Sub(r.started) > limit+2*time.Minute:
			d.askStop(ctx, lr, cli.ReasonTimeout, "past the turn's time limit")
		case now.Sub(r.started)*5 > limit*4 && d.warnOnce(lr, "time"):
			d.warn(ctx, r.id, fmt.Sprintf("%s of the turn's %s", now.Sub(r.started).Round(time.Second), limit))
		case silent > silentWarn:
			lr.mu.Lock()
			first := !lr.warned["silent"]
			lr.warned["silent"] = true
			lr.mu.Unlock()
			if first {
				d.warn(ctx, r.id, "no event for "+silent.Round(time.Second).String())
			}
		}
	}
	d.wakeWaiting(ctx)
	d.probeWhileHeld(ctx, now)
	d.reapProjectSandboxes(ctx)
}

// ProbeEvery is how often a probe starts while microVMs do not boot (21, D6).
const ProbeEvery = 5 * time.Minute

// probeWhileHeld starts a probe when two microVMs in a row did not boot and the last probe started
// ProbeEvery ago or more, unless the operator paused or stopped Crew or the disk is full.
func (d *Driver) probeWhileHeld(ctx context.Context, now time.Time) {
	if d.Probe == nil {
		return
	}
	st, err := hold.Read(ctx, d.Pool)
	// Stop all, Pause all, and a full disk start nothing, the probe neither; Run the check still does
	// (security review of 5d1b414).
	if err != nil || st.BootFailed == "" || st.PausedAt != nil || st.DiskFull != "" {
		return
	}
	d.mu.Lock()
	due := now.Sub(d.probedAt) >= ProbeEvery
	d.mu.Unlock()
	if due {
		d.ProbeNow(ctx)
	}
}

// ProbeNow starts a probe in the background unless one runs, and says whether it started one: the
// monitor's, and Run the check in the inbox.
func (d *Driver) ProbeNow(ctx context.Context) bool {
	if d.Probe == nil {
		return false
	}
	d.mu.Lock()
	if d.probing {
		d.mu.Unlock()
		return false
	}
	d.probing, d.probedAt = true, d.now()
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			d.probing = false
			d.mu.Unlock()
		}()
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		if err := d.Probe(pctx); err != nil {
			d.Log.Warn("the probe microVM did not start", "error", err)
			return
		}
		d.Log.Info("the probe microVM booted")
	}()
	return true
}

// warnOnce says whether the run has not had this warning yet, and marks it given.
func (d *Driver) warnOnce(lr *liveRun, kind string) bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.warned[kind] {
		return false
	}
	lr.warned[kind] = true
	return true
}

// IdleEnded is the detail of a run the monitor ended past its idle time, its turn ended well: its
// subtask is done, though a builder's turn may still wait for Crew to act on it (since 0.55).
const IdleEnded = "no next turn came"

// TakenUp is the detail of such a run once Crew acted on its turn after it ended, so it is taken up
// once (since 0.55), or once its ticket moved on from In progress before Crew came back to it (since
// 1.16, BUG-028).
const TakenUp = "no next turn came; Crew took up its turn after"

// startsSomething holds for a subtask (as s) that may continue by itself: its project is not paused
// and its ticket is not on hold, for which nothing starts (15.2, 7.9). It waits as it is, and goes on
// after Resume, or once the hold is let go.
const startsSomething = `NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project AND p.state = 'paused')
	AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.id = s.ticket AND t.hold)`

// MaxAutoContinues is how often Crew continues a subtask by itself; then it waits for the operator, so
// nothing a sandbox writes can start runs without end.
const MaxAutoContinues = 8

// wakeWaiting continues the subtasks whose wait is over: a retry or a restart at once, a sign-in when
// the CLI's login works again after the subtask stopped, and a limit after its reset or when Crew's
// own login check reads its group free after the wait began; a run's report never ends a wait early
// (12.4, 12.7). Before that, a subtask that waits for a sign-in, a limit, or a refused model continues
// on its next choice when it has one that can run (12.7). A subtask of a paused project or a ticket on
// hold waits on.
func (d *Driver) wakeWaiting(ctx context.Context) {
	// While Crew is held nothing continues by itself; after Resume, what Stop all ended continues, and
	// that is no continue of Crew's own (7.9), unless its project is paused or its ticket on hold.
	if hold.Held(ctx, d.Pool) != "" {
		return
	}
	d.resumeHeld(ctx)
	d.moveWaiting(ctx)
	rows, err := d.Pool.Query(ctx, `SELECT s.id, s.wait_for FROM subtasks s
		WHERE s.state = 'waiting' AND (
			s.wait_for IN ('retry', 'restart')
			OR (s.wait_for LIKE 'login:%' AND EXISTS (SELECT 1 FROM cli_logins l
				WHERE l.cli = substr(s.wait_for, 7) AND l.state = 'signed_in' AND l.checked_at > s.updated_at))
			OR (s.wait_for LIKE 'quota:%' AND s.wait_until IS NOT NULL AND s.wait_until < now()))
		AND `+startsSomething)
	if err != nil {
		d.Log.Warn("the monitor cannot read the waiting subtasks", "error", err)
		return
	}
	type wait struct {
		id  int64
		why string
	}
	var list []wait
	for rows.Next() {
		var w wait
		if rows.Scan(&w.id, &w.why) == nil {
			list = append(list, w)
		}
	}
	rows.Close()
	if guard, err := models.LoadGuard(ctx, d.Pool, 0); err != nil {
		d.Log.Warn("the monitor cannot read the quota guard", "error", err)
	} else if qrows, err := d.Pool.Query(ctx, `SELECT s.id, s.wait_for, s.updated_at FROM subtasks s
		WHERE s.state = 'waiting' AND s.wait_for LIKE 'quota:%' AND s.wait_until >= now()
		AND `+startsSomething); err != nil {
		d.Log.Warn("the monitor cannot read the subtasks that wait for a limit", "error", err)
	} else {
		type quotaWait struct {
			id      int64
			why     string
			updated time.Time
		}
		var qlist []quotaWait
		for qrows.Next() {
			var q quotaWait
			if qrows.Scan(&q.id, &q.why, &q.updated) == nil {
				qlist = append(qlist, q)
			}
		}
		qrows.Close()
		for _, q := range qlist {
			cliName := strings.TrimPrefix(q.why, "quota:")
			a, ok := cli.Get(cliName)
			if !ok {
				continue
			}
			var model string
			if err := d.Pool.QueryRow(ctx, `SELECT coalesce(model_resolved, model, '') FROM runs
				WHERE subtask = $1 ORDER BY number DESC LIMIT 1`, q.id).Scan(&model); err != nil {
				continue
			}
			if _, limited := guard.Limited(a, model); limited {
				continue
			}
			var free bool
			if err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM quota
				WHERE cli = $1 AND grp = $2 AND source = 'login check' AND read_at > $3)`,
				cliName, a.LimitGroup(model), q.updated).Scan(&free); err != nil || !free {
				continue
			}
			list = append(list, wait{id: q.id, why: q.why})
		}
	}
	for _, w := range list {
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT auto_continues FROM subtasks WHERE id = $1`, w.id).Scan(&n); err != nil {
			continue
		}
		if n >= MaxAutoContinues {
			d.Pool.Exec(ctx, `UPDATE subtasks SET state = 'stopped', wait_for = 'operator', updated_at = now() WHERE id = $1`, w.id)
			d.Log.Warn("a subtask continued by itself too often; it waits for the operator", "subtask", w.id, "times", n)
			continue
		}
		run, err := d.Continue(ctx, w.id, ContinueRequest{Auto: true})
		if err != nil {
			if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrNoRoom) && !errors.Is(err, ErrCheckFamily) {
				d.Log.Warn("a waiting subtask did not continue", "subtask", w.id, "waited for", w.why, "error", err)
			}
			continue
		}
		// Counted only when a run started, so a continue that could not happen spends none.
		d.Pool.Exec(ctx, `UPDATE subtasks SET auto_continues = auto_continues + 1 WHERE id = $1`, w.id)
		d.Log.Info("a waiting subtask continues", "subtask", w.id, "waited for", w.why, "run", run)
	}
}

// Reconcile follows again the runs that were live when the server stopped (21, "Backend restart"): a
// run whose microVM still runs is read on from the last stored line of its spool; one whose microVM
// is gone ends with the reason restart, and its subtask continues in a new microVM.
func (d *Driver) Reconcile(ctx context.Context) {
	// A sandbox made after this moment belongs to a run that starts meanwhile: it is not reaped.
	since := time.Now()
	rows, err := d.Pool.Query(ctx, `SELECT r.id, r.subtask, r.cli, r.state, r.turn, r.conversation, coalesce(r.sandbox, ''),
			coalesce(c.cli_id, ''), r.max_turns, coalesce(r.stopping, ''),
			coalesce(sp."offset", 0), coalesce(sp.done, false), s.removed_at IS NULL AND r.sandbox IS NOT NULL,
			r.tool_steps, coalesce(r.model_name, r.model, ''), coalesce(r.effort, ''), r.tools, r.timeout_s, coalesce(r.branch, ''),
			coalesce(r.base_ref, ''), coalesce(sub.project::text, ''), r.push, coalesce(s.kind, '') = 'judge', coalesce(r.git_commit, '')
		FROM runs r JOIN conversations c ON c.id = r.conversation
		LEFT JOIN run_spools sp ON sp.run = r.id AND sp.turn = r.turn
		LEFT JOIN sandboxes s ON s.id = r.sandbox
		JOIN subtasks sub ON sub.id = r.subtask
		WHERE r.state <> 'ended'`)
	if err != nil {
		d.Log.Error("reading the runs to follow again", "error", err)
		return
	}
	type row struct {
		id, cli, state, conv, sandbox, cliID, stopping string
		model, effort, branch, base, project, commit   string
		subtask                                        int64
		turn, maxTurns, steps, timeout                 int
		offset                                         int64
		done, alive, tools, push, judge                bool
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.subtask, &r.cli, &r.state, &r.turn, &r.conv, &r.sandbox, &r.cliID, &r.maxTurns, &r.stopping,
			&r.offset, &r.done, &r.alive, &r.steps, &r.model, &r.effort, &r.tools, &r.timeout, &r.branch, &r.base, &r.project, &r.push, &r.judge,
			&r.commit); err == nil {
			list = append(list, r)
		} else {
			d.Log.Error("reading a run to follow again", "error", err)
		}
	}
	rows.Close()
	// The silence clock starts again with the server: the events a run printed meanwhile are in its
	// spool and are read now (8.2).
	d.Pool.Exec(ctx, `UPDATE runs SET last_event_at = now() WHERE state <> 'ended'`)
	keep := map[string]bool{}
	for _, r := range list {
		lr := &liveRun{id: r.id, subtask: r.subtask, conversation: r.conv, cli: r.cli, maxTurns: r.maxTurns, steps: r.steps,
			quit: make(chan struct{}), toolByID: map[string]string{}, warned: map[string]bool{}, redact: redact.New(), limits: DefaultLimits()}
		d.arm(ctx, lr)
		if r.stopping != "" {
			lr.stopping, lr.stopAt = r.stopping, d.now()
		}
		d.mu.Lock()
		if d.live == nil {
			d.live = map[string]*liveRun{}
		}
		d.live[r.id] = lr
		d.mu.Unlock()
		if !r.alive {
			d.endRun(ctx, lr, ReasonRestart, "its microVM did not survive the restart", nil, false)
			continue
		}
		if d.Secrets != nil {
			lr.redact = redact.New(d.Secrets(ctx, r.sandbox)...)
		}
		keep[r.sandbox] = true
		sc, err := d.scopeOf(ctx, d.Pool, r.subtask)
		if err != nil {
			d.Log.Error("reading a run's subtask after the restart", "run", r.id, "error", err)
			d.endRun(ctx, lr, ReasonRestart, NoScopeAfterRestart, nil, true)
			continue
		}
		hs := d.specFor(sc, harness.RunSpec{Judge: r.judge}, r.branch, r.base)
		hs.Commit = r.commit
		if sc.conversation {
			lr.idle = d.conversationIdle(ctx)
		}
		spec := runspec.Run{Run: r.id, Subtask: r.subtask, Project: r.project, CLI: r.cli, Model: r.model, Effort: r.effort,
			Conversation: r.cliID, Tools: r.tools, Limits: runspec.Limits{TimeoutSeconds: r.timeout, MaxTurns: r.maxTurns},
			Git: harness.RunGit(r.id, hs.Repo, hs.Branch, hs.Base), MCP: hs.MCP, NoRepoConfig: hs.NoRepoConfig,
			MainInstructions: hs.MainInstructions}
		harness.OwnBranch(&spec.Git, hs)
		// A run that pushes nothing has no work in progress to keep (6.1).
		if !r.push {
			spec.Git.WIP = ""
		}
		err = d.Harness.Resume(ctx, harness.Resumed{Run: r.id, Sandbox: r.sandbox, CLI: r.cli, Turn: r.turn, Offset: r.offset, KeepFolder: hs.KeepFolder,
			Conversation: r.cliID, TurnDone: r.done, Spec: spec})
		if err == nil && !d.Harness.Alive(ctx, r.id) {
			err = errors.New("its microVM does not run")
		}
		if err != nil {
			d.Harness.Stop(ctx, r.id)
			d.Log.Error("following a run again after the restart", "run", r.id, "error", err)
			d.endRun(ctx, lr, ReasonRestart, NotFollowedAfterRestart, nil, true)
			continue
		}
		d.Log.Info("following a run again after the restart", "run", r.id, "turn", r.turn, "offset", r.offset)
		go d.follow(context.WithoutCancel(ctx), lr)
	}
	d.reap(ctx, keep, since)
}

// SandboxLister lists and removes the sandboxes: sandboxes.Manager.
type SandboxLister interface {
	Live(ctx context.Context) ([]sandboxes.Row, error)
	Remove(ctx context.Context, id string) error
}

// ProjectSandboxReaper transitions idle project sandboxes (`running` -> `paused` -> `stopped`).
type ProjectSandboxReaper interface {
	ReapIdleProjectSandboxes(ctx context.Context, suspendAfter, stopAfter time.Duration) (paused, stopped int, err error)
}

func (d *Driver) reapProjectSandboxes(ctx context.Context) {
	reaper, ok := d.Sandboxes.(ProjectSandboxReaper)
	if !ok || d.Pool == nil {
		return
	}
	suspendAfter := 5 * time.Minute
	stopAfter := 30 * time.Minute
	if doc, err := settings.Effective(ctx, d.Pool, settings.Machine{}); err == nil {
		if sb, ok := doc["sandbox"].(settings.Doc); ok {
			if m := settingsInt(sb["suspend_after_minutes"], 5); m >= 0 {
				suspendAfter = time.Duration(m) * time.Minute
			}
			if m := settingsInt(sb["stop_after_minutes"], 30); m >= 0 {
				stopAfter = time.Duration(m) * time.Minute
			}
		}
	}
	if _, _, err := reaper.ReapIdleProjectSandboxes(ctx, suspendAfter, stopAfter); err != nil && d.Log != nil {
		d.Log.Warn("reaping idle project sandboxes", "error", err)
	}
}

func settingsInt(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

// reap removes every run sandbox that no live run follows: a microVM never outlives its run, also
// when its run ended while the server was away, or could not be followed again (12.4).
func (d *Driver) reap(ctx context.Context, keep map[string]bool, since time.Time) {
	if d.Sandboxes == nil {
		return
	}
	live, err := d.Sandboxes.Live(ctx)
	if err != nil {
		d.Log.Warn("listing the sandboxes to remove what no run follows", "error", err)
		return
	}
	for _, s := range live {
		if strings.HasPrefix(s.ID, "proj-") {
			continue
		}
		if (s.Kind != sandboxes.KindRun && s.Kind != sandboxes.KindConversation && s.Kind != sandboxes.KindChat) || keep[s.ID] || !s.Started.Before(since) {
			continue
		}
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE sandbox = $1 AND state <> 'ended'`, s.ID).Scan(&n); err != nil {
			d.Log.Warn("counting the runs of a sandbox", "sandbox", s.ID, "error", err)
			continue
		}
		if n > 0 {
			continue
		}
		if err := d.Sandboxes.Remove(ctx, s.ID); err != nil {
			d.Log.Warn("removing a run sandbox that no run follows", "sandbox", s.ID, "error", err)
			continue
		}
		d.Log.Info("removed a run sandbox that no run follows", "sandbox", s.ID)
	}
}

// moveWaiting continues each subtask that waits for its CLI's sign-in, its group's limit, or a model
// its CLI refused, on the next choice its agent has that can run, in a new sandbox (12.7). It keeps
// that choice until it ends.
func (d *Driver) moveWaiting(ctx context.Context) {
	d.mu.Lock()
	next := d.nextChoice
	d.mu.Unlock()
	if next == nil {
		return
	}
	rows, err := d.Pool.Query(ctx, `SELECT s.id FROM subtasks s WHERE s.state = 'waiting'
		AND s.auto_continues < $1 AND (s.wait_for LIKE 'login:%' OR s.wait_for LIKE 'quota:%' OR s.wait_for LIKE 'model:%')
		AND `+startsSomething, MaxAutoContinues)
	if err != nil {
		d.Log.Warn("the monitor cannot read the subtasks that wait for a model", "error", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		req, err := next(ctx, id)
		if err != nil {
			d.Log.Warn("the next choice of a waiting subtask", "subtask", id, "error", err)
			continue
		}
		if req == nil {
			continue
		}
		req.Auto = true
		run, err := d.Continue(ctx, id, *req)
		if err != nil {
			if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrNoRoom) && !errors.Is(err, ErrCheckFamily) {
				d.Log.Warn("a waiting subtask did not move to its next choice", "subtask", id, "error", err)
			}
			continue
		}
		d.Pool.Exec(ctx, `UPDATE subtasks SET auto_continues = auto_continues + 1 WHERE id = $1`, id)
		d.Log.Info("a waiting subtask moved to its next choice", "subtask", id, "run", run, "cli", req.CLI, "model", req.Name)
	}
}
