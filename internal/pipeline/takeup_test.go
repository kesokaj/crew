package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// finishedRuns records the runs the pipeline finishes.
type finishedRuns struct {
	fakeRuns
	finished []string
}

func (f *finishedRuns) Finish(_ context.Context, run string) error {
	f.finished = append(f.finished, run)
	return nil
}

// A builder whose run ended while it waited is marked taken up once its ticket moved on from In
// progress, to Done or to a later stage; in In progress the stage takes it up first, and a closed
// ticket did not move on by its checks. A later fix is the builder's last work, which leaves the
// build unmarked; a fix whose run still works is not finished either (BUG-028).
func TestABuilderWhoseTicketMovedOnIsMarkedTakenUp(t *testing.T) {
	for _, c := range []struct {
		stage    string
		fix      string // the state of a fix's run after the build, "" for none
		upMarked bool
	}{
		{settings.StageInProgress, "", false},
		{settings.StageClosed, "", false},
		{settings.StageReview, "", true},
		{settings.StageDone, "", true},
		{settings.StageReview, "ended", false},
		{settings.StageReview, "running", false},
	} {
		k := newSetupMarkKit(t, nil)
		rr := &finishedRuns{}
		k.p.Runs = rr
		build := k.ranAs(kindBuild, toolOf[kindBuild], "ended", runs.IdleEnded)
		switch c.fix {
		case "ended":
			k.ranAs(kindFix, toolOf[kindFix], "ended", "finished")
		case "running":
			k.ranAs(kindFix, "", "running", "")
		}
		subs, err := k.p.subtasks(k.ctx, k.one)
		if err != nil {
			t.Fatal(err)
		}
		k.one.Stage = c.stage
		if err := k.p.markMovedOn(k.ctx, k.one, subs); err != nil {
			t.Fatal(err)
		}
		want := runs.IdleEnded
		if c.upMarked {
			want = runs.TakenUp
		}
		if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND stop_detail = $2`, build, want); n != 1 || len(rr.finished) != 0 {
			t.Fatalf("in %s, with a fix after it %q: the builder's run is not %q, or a run was finished: %v", c.stage, c.fix, want, rr.finished)
		}
	}
}

// ranAs records a subtask of SF-1 of kind that reported with the call tool, with one run in state,
// ended with detail, its subtask done when the run ended and in the run's state otherwise, and gives
// the run's id.
func (k *setupMarkKit) ranAs(kind, tool, state, detail string) string {
	k.t.Helper()
	var sub int64
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, reported)
		VALUES ('ticket', $1, $2, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE ticket = $2), $3, 'builder',
			CASE WHEN $5::text = 'ended' THEN 'done' ELSE $5::text END, jsonb_build_object('tool', $4::text)) RETURNING id`,
		k.j.proj.ID, k.one.ID, kind, tool, state).Scan(&sub); err != nil {
		k.t.Fatal(err)
	}
	var run string
	if err := k.p.Pool.QueryRow(k.ctx, `INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, stop_reason, stop_detail, started_at)
		VALUES (gen_random_uuid(), $1, 1, $2::text, 'agy', 'test', 'img', CASE WHEN $2::text = 'ended' THEN 'ok' END, nullif($3, ''), now()) RETURNING id::text`,
		sub, state, detail).Scan(&run); err != nil {
		k.t.Fatal(err)
	}
	return run
}

// A mark of a builder whose ticket moved on that fails is the pass's error for its ticket, which goes no
// further, and the run keeps its detail, so the next pass marks it.
func TestAFailedMarkOfAMovedOnBuilderIsSaid(t *testing.T) {
	k := newSetupMarkKit(t, nil)
	k.p.Runs = &finishedRuns{}
	build := k.ranAs(kindBuild, toolOf[kindBuild], "ended", runs.IdleEnded)
	k.p.Pool = failingPool(t, k.p.Pool, "UPDATE runs SET stop_detail = $2 WHERE id = $1")
	k.one.Stage = settings.StageReview
	err := k.p.ticket(k.ctx, k.j, k.one)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("the failed mark: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE id = $1 AND stop_detail = $2`, build, runs.IdleEnded); n != 1 {
		t.Fatal("the builder's run lost its detail")
	}
}
