package runs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// ErrNoRoom is returned for a run whose sandbox does not fit on the machine now (7.9, R71): the
// caller waits, and asks again on its next pass.
var ErrNoRoom = errors.New("no room on the machine")

// normalPriority is a ticket's priority when it has none, as the lead queue counts it (7.9).
const normalPriority = 2

// keyboardRank puts the runs the operator waits for at the keyboard before every stage: the planning
// agent of a draft.
const keyboardRank = 99

// takeRoom asks the gate for room for a run's sandbox. On yes the gate stays locked until the returned
// function is called, with whether the run was recorded, so the next ask counts it. With no gate,
// every run fits.
func (d *Driver) takeRoom(ctx context.Context, ask capacity.Ask) (func(recorded bool), error) {
	if d.Room == nil {
		if err := d.checkProjectSandboxBusy(ctx, ask); err != nil {
			return nil, err
		}
		return func(bool) {}, nil
	}
	d.Room.Mu.Lock()
	if err := d.checkProjectSandboxBusy(ctx, ask); err != nil {
		d.Room.Mu.Unlock()
		return nil, err
	}
	ok, err := d.Room.Take(ctx, ask)
	if err != nil || !ok {
		d.Room.Mu.Unlock()
		if err != nil {
			// The room cannot be read: nothing starts, as with a hold that cannot be read, and the
			// log says why (review of M13).
			d.Log.Warn("the room on the machine cannot be read: nothing starts", "error", err)
			return nil, fmt.Errorf("%w: the room on the machine cannot be read: %v", ErrNoRoom, err)
		}
		return nil, fmt.Errorf("%w: it %s", ErrNoRoom, capacity.Waits(ask.Size))
	}
	var once sync.Once
	return func(recorded bool) {
		once.Do(func() {
			if recorded {
				if err := d.Room.Taken(context.WithoutCancel(ctx), ask.Key); err != nil {
					d.Log.Error("taking a started run out of the queue for room", "error", err)
				}
			}
			d.Room.Mu.Unlock()
		})
	}, nil
}

// checkProjectSandboxBusy returns ErrNoRoom when a project's warm microVM (crew-proj-<project>) has
// an active session or in-flight run whose network mode differs from ask's, so the caller waits
// before recording a subtask or run instead of failing in EnsureProjectSandbox.
func (d *Driver) checkProjectSandboxBusy(ctx context.Context, ask capacity.Ask) error {
	if !ask.ProjectVM || ask.Project == "" || d.Pool == nil {
		return nil
	}
	var active int
	var curMode string
	if err := d.Pool.QueryRow(ctx, `SELECT ps.active_sessions, coalesce(s.mode, '')
		FROM project_sandboxes ps
		LEFT JOIN sandboxes s ON s.id = ps.sandbox_id AND s.removed_at IS NULL
		WHERE ps.project_id = $1::uuid AND ps.state IN ('running', 'paused') AND ps.sandbox_id <> ''`,
		ask.Project).Scan(&active, &curMode); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: the project's microVM state cannot be read: %v", ErrNoRoom, err)
	}

	var liveSubtask int64
	if err := d.Pool.QueryRow(ctx, `SELECT s.id
		FROM runs r
		JOIN subtasks s ON s.id = r.subtask
		WHERE s.project = $1::uuid
		  AND r.state IN ('starting', 'working')
		  AND (r.tools OR r.branch IS NOT NULL)
		  AND s.scope <> 'improver'
		  AND s.role NOT IN ('judge', 'summary', 'prompt', 'improver')
		ORDER BY r.started_at DESC LIMIT 1`, ask.Project).Scan(&liveSubtask); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: the project's active runs cannot be read: %v", ErrNoRoom, err)
	}

	if active <= 0 && liveSubtask == 0 {
		return nil
	}
	wantMode, err := d.askMode(ctx, ask)
	if err != nil {
		return fmt.Errorf("%w: the run's network mode cannot be read: %v", ErrNoRoom, err)
	}
	if active > 0 && curMode != "" && curMode != wantMode {
		return fmt.Errorf("%w: the project's microVM has %d active session(s) in %s mode", ErrNoRoom, active, curMode)
	}
	if liveSubtask != 0 {
		liveSc, err := d.scopeOf(ctx, d.Pool, liveSubtask)
		if err != nil {
			return fmt.Errorf("%w: the active run's scope cannot be read: %v", ErrNoRoom, err)
		}
		liveMode := sandboxes.ModeFor(sandboxes.KindRun, liveSc.mode)
		if liveMode != "" && liveMode != wantMode {
			return fmt.Errorf("%w: the project's microVM has an active run in %s mode", ErrNoRoom, liveMode)
		}
	}
	return nil
}

func (d *Driver) askMode(ctx context.Context, ask capacity.Ask) (string, error) {
	switch {
	case strings.HasPrefix(ask.Key, "continue:"):
		subID, err := strconv.ParseInt(strings.TrimPrefix(ask.Key, "continue:"), 10, 64)
		if err != nil {
			return "", err
		}
		sc, err := d.scopeOf(ctx, d.Pool, subID)
		if err != nil {
			return "", err
		}
		return sandboxes.ModeFor(sandboxes.KindRun, sc.mode), nil
	case strings.HasPrefix(ask.Key, "conversation:"):
		convID := strings.TrimPrefix(ask.Key, "conversation:")
		var chatTicket int64
		var convMode string
		if err := d.Pool.QueryRow(ctx, `SELECT coalesce(ticket, 0), coalesce(network_mode, '') FROM conversations WHERE id = $1`,
			convID).Scan(&chatTicket, &convMode); err != nil {
			return "", err
		}
		mode, err := settings.NetworkMode(ctx, d.Pool, ask.Project, chatTicket)
		if err != nil {
			return "", err
		}
		if chatTicket == 0 && convMode == string(config.ModeModelOnly) {
			mode = config.ModeModelOnly
		}
		return sandboxes.ModeFor(sandboxes.KindRun, mode), nil
	default:
		var ticket int64
		if strings.HasPrefix(ask.Key, "run:") {
			if parts := strings.Split(ask.Key, ":"); len(parts) >= 3 {
				ticket, _ = strconv.ParseInt(parts[2], 10, 64)
			}
		}
		mode, err := settings.NetworkMode(ctx, d.Pool, ask.Project, ticket)
		if err != nil {
			return "", err
		}
		return sandboxes.ModeFor(sandboxes.KindRun, mode), nil
	}
}

// roomAsk is the ask of a run of role on a project's ticket, a project, or a plan draft: its place in
// the queue comes from its ticket's stage and priorities.
func (d *Driver) roomAsk(ctx context.Context, key, owner, role, project, draft string, ticket int64, what string) capacity.Ask {
	ask := capacity.Ask{
		Key:     key,
		Owner:   owner,
		Size:    capacity.SizeOf(role),
		Project: project,
		ProjectVM: project != "" && draft == "" &&
			role != "judge" && role != "summary" && role != "prompt" && role != "improver" &&
			!strings.Contains(key, ":eval_"),
		Continues: strings.HasPrefix(key, "continue:") || strings.HasPrefix(key, "cont:"),
		What:      what,
		Priority:  normalPriority,
		Own:       normalPriority,
	}
	if draft != "" || role == "planner" {
		ask.Rank = keyboardRank
	}
	if ticket != 0 {
		var stage string
		var across *int
		if err := d.Pool.QueryRow(ctx, `SELECT t.stage, p.key || '-' || t.number, t.operator_priority, t.priority
			FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = $1`, ticket).Scan(&stage, &ask.Ticket, &across, &ask.Own); err == nil {
			ask.Rank = capacity.StageRank(stage)
			if across != nil {
				ask.Priority = *across
			}
		}
	}
	return ask
}

// sized gives a run's sandbox the allowance of its role's size (13.10).
func (d *Driver) sized(ctx context.Context, role string, spec *harness.RunSpec) {
	if d.Room == nil {
		return
	}
	s, err := d.Room.SizeFor(ctx, capacity.SizeOf(role))
	if err != nil {
		d.Log.Warn("the size of a run's sandbox cannot be read; it gets Kata's base", "run", spec.Run, "error", err)
		return
	}
	spec.Sizing = s.Sizing()
}
