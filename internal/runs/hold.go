package runs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/hold"
)

// ErrHeld is returned for a run or a turn that would start while Crew is held: Pause all, Stop all,
// two microVMs in a row that did not boot, the disk guard, or a restart for security updates (7.9, 16.10,
// 21). The caller waits.
var ErrHeld = errors.New("nothing starts now")

// holdKind is pause or stop while the operator holds Crew, else "". A restart for security updates
// that waited 7 days is a pause too (16.10): it lets the running runs finish, so their own turns go
// on; the ten minutes after a yes, when the machine goes down, are not.
func (d *Driver) holdKind(ctx context.Context) string {
	s, err := hold.Read(ctx, d.Pool)
	if err != nil {
		return ""
	}
	if s.Why == "" && s.Held() == hold.RestartWaitWhy {
		return "pause"
	}
	return s.Why
}

// PauseAll holds Crew: no new run or turn starts, and running runs finish (7.9).
func (d *Driver) PauseAll(ctx context.Context, by string) (hold.State, error) {
	var s hold.State
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		var err error
		s, err = hold.Pause(ctx, tx, "pause", by)
		return err
	})
	return s, err
}

// StopAll holds Crew and ends every run now; each keeps its conversation, and its subtask waits for
// Resume (7.9).
func (d *Driver) StopAll(ctx context.Context, by string) (hold.State, int, error) {
	var s hold.State
	if err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		var err error
		s, err = hold.Pause(ctx, tx, "stop", by)
		return err
	}); err != nil {
		return s, 0, err
	}
	// A run that registers after this read ends itself: it reads the state written above (start).
	d.mu.Lock()
	var live []*liveRun
	for _, lr := range d.live {
		live = append(live, lr)
	}
	d.mu.Unlock()
	n := 0
	for _, lr := range live {
		lr.mu.Lock()
		lr.stopAll = true
		lr.mu.Unlock()
		if err := d.Stop(ctx, lr.id); err == nil {
			n++
		} else if !errors.Is(err, ErrEnded) {
			d.Log.Warn("Stop all could not stop a run", "run", lr.id, "error", err)
		}
	}
	return s, n, nil
}

// ResumeAll lets Crew start again: the subtasks Stop all ended continue at the monitor's next round,
// unless their project is paused or their ticket on hold (15.2), and the clocks of notices move on by the time Crew was paused (7.9).
func (d *Driver) ResumeAll(ctx context.Context, by string) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		_, err := hold.Resume(ctx, tx, by)
		return err
	})
}

// resumeHeld continues the subtasks that Stop all ended, without counting them as Crew's own continues.
func (d *Driver) resumeHeld(ctx context.Context) {
	rows, err := d.Pool.Query(ctx, `SELECT s.id FROM subtasks s WHERE s.state = 'waiting' AND s.wait_for = 'held' AND `+startsSomething)
	if err != nil {
		d.Log.Warn("the monitor cannot read what Stop all ended", "error", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return
	}
	for _, id := range ids {
		run, err := d.Continue(ctx, id, ContinueRequest{Auto: true, Text: "Crew was stopped and resumed. Go on with your work where you left it."})
		if err != nil {
			if !errors.Is(err, ErrBusy) {
				d.Log.Warn("a subtask Stop all ended did not continue", "subtask", id, "error", err)
			}
			continue
		}
		d.Log.Info("a subtask Stop all ended continues", "subtask", id, "run", run)
	}
}
