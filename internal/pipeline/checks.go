package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/settings"
)

// projectTests names the project's test command among the results of a check run.
const projectTests = "the project's tests: "

// checkCmds are the commands of a check run: the ticket's command checks, by their spec, and the
// project's test command when test is not "".
func checkCmds(cmds []board.CheckSpec, test string) []harness.CheckCmd {
	var out []harness.CheckCmd
	for _, c := range cmds {
		out = append(out, harness.CheckCmd{Name: c.Spec, Run: c.Spec})
	}
	if test != "" {
		out = append(out, harness.CheckCmd{Name: projectTests + test, Run: test})
	}
	return out
}

// runChecks records a check run and runs it in a check sandbox in the background (13.9). Crew's own
// trouble, such as a sandbox that did not start or a command that ran past its time, ends it as an
// error, which is tried again and is never a failure of the ticket. A merge candidate that fails is run
// once more: a pass the second time is a pass, recorded as flaky (7.10, R178).
func (p *Pipeline) runChecks(ctx context.Context, j *job, t *board.Ticket, purpose, commit, main, head string, cmds []harness.CheckCmd) (int64, error) {
	return p.runChecksFor(ctx, j, t, purpose, "", commit, main, head, cmds)
}

// runChecksFor is runChecks for a project's own stage, stage; "" for none.
func (p *Pipeline) runChecksFor(ctx context.Context, j *job, t *board.Ticket, purpose, stage, commit, main, head string, cmds []harness.CheckCmd) (int64, error) {
	// A check sandbox is large, and waits for room as a run does (7.9): the check run is recorded
	// before the room is let go, so the next ask counts it. Waiting for room starts no check run: the
	// caller finds none, and asks again on its next pass.
	// Checks need the project's setup: while the ticket that fixes it is open, they wait for it; that
	// ticket's own checks on main run without it, which fails there (21, 13.5).
	setup := str(j.doc, "sandbox.setup")
	ownFix := false
	if why, err := p.waitsForSetup(ctx, j, t, "check"); err != nil || why != "" {
		if err != nil || t == nil {
			return 0, err
		}
		return 0, p.Board.Wait(ctx, t, "Crew's checks: "+why)
	}
	if fix, err := p.setupTicket(ctx, j.proj); err != nil {
		return 0, err
	} else if fix != nil && t != nil && t.ID == fix.ID {
		ownFix = true
		if purpose == board.PurposeBase {
			setup = ""
		}
	}
	var sizing orchestrator.Sizing
	if p.Room != nil {
		key := fmt.Sprintf("check:%s:%s:%s:%s", j.proj.ID, purpose, stage, head)
		ask := capacity.Ask{Key: key, Owner: "pipeline", Size: capacity.Large, Project: j.proj.ID, What: "Crew's checks",
			Priority: planning.NormalPriority, Own: planning.NormalPriority}
		if t != nil {
			across, own := queuePriorities(t)
			ask.Rank, ask.Ticket, ask.Priority, ask.Own = capacity.StageRank(t.Stage), t.Key, across, own
		}
		p.Room.Mu.Lock()
		ok, err := p.Room.Take(ctx, ask)
		if err != nil || !ok {
			p.Room.Mu.Unlock()
			if err != nil || t == nil {
				return 0, err
			}
			return 0, p.Board.Wait(ctx, t, "Crew's checks: "+capacity.Waits(capacity.Large))
		}
		defer p.Room.Mu.Unlock()
		defer func() {
			if err := p.Room.Taken(context.WithoutCancel(ctx), key); err != nil {
				p.log().Error("taking a started check out of the queue for room", "error", err)
			}
		}()
		if s, err := p.Room.SizeFor(ctx, capacity.Large); err == nil {
			sizing = s.Sizing()
		}
	}
	id, err := p.Board.StartCheckRunFor(ctx, j.proj, t, purpose, commit, main, head, stage, nil)
	if err != nil {
		return 0, err
	}
	timeout := time.Duration(num(j.doc, "limits.check_timeout_minutes", 30)) * time.Minute
	name, project := j.proj.Name, j.proj.ID
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.Kick()
		bg := context.WithoutCancel(ctx)
		var tree bytes.Buffer
		if err := p.Board.Git.Archive(bg, name, commit, &tree); err != nil {
			p.finishChecks(bg, id, board.CheckError, nil, "the tree to check: "+err.Error(), false, false)
			return
		}
		var ticket int64
		if t != nil {
			ticket = t.ID
		}
		mode, err := settings.CheckNetworkMode(bg, p.Pool, project, ticket)
		if err != nil {
			p.finishChecks(bg, id, board.CheckError, nil, "the check sandbox's network mode: "+err.Error(), false, false)
			return
		}
		spec := harness.CheckSpec{Project: project, Mode: mode, Tree: tree.Bytes(), Setup: setup, Cmds: cmds, Timeout: timeout, Sizing: sizing}
		state, results, detail, setupFailed := p.execChecks(bg, spec)
		// The setup's own fix ticket fails its check where the setup still fails: its work is not done.
		// The mark says so, never the detail, which begins with a command an agent named (13.9).
		if ownFix && state == board.CheckError && setupFailed {
			state = board.CheckFailed
		}
		// A merge candidate, and the end-to-end tests (21), that fail are run once more in a new
		// sandbox before a fix is asked for: a pass the second time is a pass, recorded as flaky.
		flaky := false
		var failed []string
		if (purpose == board.PurposeMerge || purpose == board.PurposeE2E) && state == board.CheckFailed {
			// The commands that failed, as they are run: a bug names the command (21).
			for _, r := range results {
				for _, c := range cmds {
					if !r.Passed && c.Name == r.Name {
						failed = append(failed, c.Run)
					}
				}
			}
			// A run that passed or failed had no setup that failed: what is kept of it keeps its mark,
			// also where the setup's own fix ticket's first run failed at its setup.
			if again, results2, detail2, setup2 := p.execChecks(bg, spec); again == board.CheckPassed {
				state, results, detail, setupFailed, flaky = again, results2, "it failed once, and passed when it was run again", setup2, true
			} else if again == board.CheckFailed {
				results, detail, setupFailed = results2, detail2, setup2
			}
		}
		p.finishChecks(bg, id, state, results, detail, flaky, setupFailed)
		if flaky {
			p.signal(bg, j, t, "", "flaky_check", map[string]any{"checkRun": id, "purpose": purpose, "cmds": failed})
			if purpose == board.PurposeE2E {
				p.flakyBugs(bg, j, t, failed)
			}
		}
	}()
	return id, nil
}

// largeSizing is what a check sandbox of Crew's own on main gets: a large sandbox's (13.10). Such a
// check is counted as one, and not held back: it is short, and work waits for its answer.
func (p *Pipeline) largeSizing(ctx context.Context) orchestrator.Sizing {
	if p.Room == nil {
		return orchestrator.Sizing{}
	}
	s, err := p.Room.SizeFor(ctx, capacity.Large)
	if err != nil {
		p.log().Warn("the size of a check sandbox cannot be read; it gets Kata's base", "error", err)
		return orchestrator.Sizing{}
	}
	return s.Sizing()
}

// execChecks runs a check sandbox once, and says how it ended, and whether the project's setup failed
// before the commands: the harness's own error says so, never a command's name, which an agent wrote.
func (p *Pipeline) execChecks(ctx context.Context, spec harness.CheckSpec) (string, []board.CmdResult, string, bool) {
	res, err := p.Checks.ExecFresh(ctx, spec)
	if err != nil {
		return board.CheckError, nil, err.Error(), false
	}
	state := board.CheckPassed
	var out []board.CmdResult
	for _, r := range res {
		out = append(out, board.CmdResult{Name: r.Name, Code: r.Code, Passed: r.Passed, Error: r.Error, Output: r.Output})
		switch {
		case r.Error != "":
			state = board.CheckError
		case !r.Passed && state != board.CheckError:
			state = board.CheckFailed
		}
	}
	detail, setup := "", false
	if state == board.CheckError {
		for _, r := range out {
			if r.Error != "" {
				detail = r.Name + ": " + r.Error
				// The project's setup failed before any command: it says so first (13.5).
				if strings.HasPrefix(r.Error, harness.SetupError) {
					detail, setup = r.Error, true
				}
				break
			}
		}
	}
	return state, out, detail, setup
}

func (p *Pipeline) finishChecks(ctx context.Context, id int64, state string, results []board.CmdResult, detail string, flaky, setup bool) {
	if err := p.Board.FinishCheckRun(ctx, id, state, results, detail, flaky, setup); err != nil {
		p.log().Error("recording the end of a check run", "checkRun", id, "error", err)
	}
}

// checkRun reads one check run.
func (p *Pipeline) checkRun(ctx context.Context, id int64) (*board.CheckRun, error) {
	var c board.CheckRun
	err := p.Pool.QueryRow(ctx, `SELECT id, purpose, commit, main, head, state, results, detail, flaky, started_at, ended_at
		FROM check_runs WHERE id = $1`, id).Scan(&c.ID, &c.Purpose, &c.Commit, &c.Main, &c.Head, &c.State, &c.Results, &c.Detail,
		&c.Flaky, &c.StartedAt, &c.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("no such check run")
	}
	return &c, err
}

// failuresText is what failed in a check run, as the commands printed it: data for the agent that
// fixes it.
func failuresText(run *board.CheckRun) string {
	var b bytes.Buffer
	for _, r := range run.Results {
		if r.Passed {
			continue
		}
		b.WriteString("$ " + r.Name + "\n")
		// A setup the runtime could not run, counted as a failure, is Crew's own error (13.9).
		if e := board.PublicError(r.Error); e != "" {
			b.WriteString("(" + e + ")\n")
		}
		b.WriteString(r.Output)
		if r.Output != "" && r.Output[len(r.Output)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("exit code " + strconv.Itoa(r.Code) + "\n\n")
	}
	return b.String()
}

// flakyRepeat is how often in flakyWindow an end-to-end command fails and then passes before it is a
// bug (21, since 0.46).
const (
	flakyRepeat = 2
	flakyWindow = 30 * 24 * time.Hour
)

// flakyBugs makes each end-to-end command whose result keeps changing a bug, or one more repeat of its
// open bug (21).
func (p *Pipeline) flakyBugs(ctx context.Context, j *job, t *board.Ticket, cmds []string) {
	for _, cmd := range cmds {
		var times int
		if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE project = $1 AND kind = 'flaky_check'
			AND detail->>'purpose' = 'e2e' AND detail->'cmds' ? $2 AND at > $3`, j.proj.ID, cmd, time.Now().Add(-flakyWindow)).Scan(&times); err != nil {
			p.log().Error("counting the flaky runs", "project", j.proj.Name, "error", err)
			continue
		}
		if times < flakyRepeat {
			continue
		}
		bug, opened, err := p.Board.FlakyBug(ctx, j.proj, cmd, t, times)
		if err != nil {
			p.log().Error("the bug of a flaky end-to-end run", "project", j.proj.Name, "error", err)
			continue
		}
		if opened {
			p.log().Info("end-to-end tests that pass or fail at random are a bug", "project", j.proj.Name, "bug", bug.Number, "fixTicket", derefS(bug.FixTicket))
		}
	}
}
