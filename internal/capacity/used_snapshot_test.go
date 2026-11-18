package capacity_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/capacity"
)

type snapshotHookPool struct {
	pool         *pgxpool.Pool
	beforeChecks func()
}

func (p *snapshotHookPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM check_runs") && p.beforeChecks != nil {
		p.beforeChecks()
	}
	return p.pool.Query(ctx, sql, args...)
}

func (p *snapshotHookPool) BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.pool.BeginTx(ctx, txOptions)
	if err != nil {
		return nil, err
	}
	return &snapshotHookTx{Tx: tx, beforeChecks: p.beforeChecks}, nil
}

type snapshotHookTx struct {
	pgx.Tx
	beforeChecks func()
}

func (tx *snapshotHookTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM check_runs") && tx.beforeChecks != nil {
		tx.beforeChecks()
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func commitRunToCheckTransition(t *testing.T, r *room) {
	t.Helper()
	tx, err := r.db.Server.Begin(r.ctx)
	if err != nil {
		t.Errorf("begin transition tx: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(r.ctx) }()
	if _, err := tx.Exec(r.ctx, `UPDATE runs SET state = 'idle'`); err != nil {
		t.Errorf("idle run: %v", err)
		return
	}
	if _, err := tx.Exec(r.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state)
		SELECT $1, id, 'base', repeat('a', 40), 'running' FROM tickets WHERE project = $1`, fixture); err != nil {
		t.Errorf("insert check_run: %v", err)
		return
	}
	if err := tx.Commit(r.ctx); err != nil {
		t.Errorf("commit transition tx: %v", err)
	}
}

// Used reads project_sandboxes, runs, and check_runs/sandboxes from one RepeatableRead snapshot when
// q supports BeginTx, so a run transitioning working -> idle while a check run starts cannot be
// double-counted across statements (BUG-038).
func TestUsedReadsOneSnapshot(t *testing.T) {
	r := newRoom(t)
	r.running(capacity.Small)
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'One', 'fix', 'review', 'plan', 'lead', 1)`, fixture); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	hooked := &snapshotHookPool{
		pool:         r.db.Server,
		beforeChecks: func() { once.Do(func() { commitRunToCheckTransition(t, r) }) },
	}
	doc, _ := r.gate.Settings(r.ctx)
	sizes := capacity.Sizes(doc)
	u, err := capacity.Used(r.ctx, hooked, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if u.CPUs != 2 || u.MemMB != 2048 || u.Runs != 1 || u.Checks != 0 {
		t.Fatalf("read-skew across Used queries: got %+v, want pre-commit snapshot {CPUs:2 MemMB:2048 Runs:1 Checks:0}", u)
	}
	// A non-BeginTx querier (such as an existing pgx.Tx) reads directly without opening a nested transaction.
	tx, err := r.db.Server.Begin(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(r.ctx) }()
	after, err := capacity.Used(r.ctx, tx, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if after.CPUs != 4 || after.MemMB != 6144 || after.Runs != 0 || after.Checks != 1 {
		t.Fatalf("post-commit Used on pgx.Tx: got %+v, want {CPUs:4 MemMB:6144 Runs:0 Checks:1}", after)
	}
}

// Running judge and stage_judge rows in check_runs record the criteria of a Small judge agent run
// (already counted in runs) and start no Large check sandbox, so Used must not count them a second
// time as Large checks (BUG-038).
func TestJudgeCheckRunsAreNotDoubleCounted(t *testing.T) {
	r := newRoom(t)
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'One', 'fix', 'review', 'plan', 'lead', 1)`, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `WITH s AS (INSERT INTO subtasks (scope, project, number, kind, role)
			VALUES ('project', $1, 1, 'judge', 'judge') RETURNING id)
		INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, size, started_at)
		SELECT gen_random_uuid(), id, 1, 'working', 'agy', 'v', 'img', 'small', now() FROM s`, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Server.Exec(r.ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, stage, criteria)
		SELECT $1, id, 'judge', repeat('a', 40), 'running', NULL, ARRAY['clean'] FROM tickets WHERE project = $1
		UNION ALL
		SELECT $1, id, 'stage_judge', repeat('a', 40), 'running', 'qa', ARRAY['tested'] FROM tickets WHERE project = $1`, fixture); err != nil {
		t.Fatal(err)
	}
	doc, _ := r.gate.Settings(r.ctx)
	u, err := capacity.Used(r.ctx, r.db.Server, capacity.Sizes(doc))
	if err != nil {
		t.Fatal(err)
	}
	if u.CPUs != 2 || u.MemMB != 2048 || u.Runs != 1 || u.Checks != 0 || u.Small != 1 || u.Large != 0 {
		t.Fatalf("judge check_runs double-counted: got %+v, want {CPUs:2 MemMB:2048 Runs:1 Small:1 Checks:0 Large:0}", u)
	}
}
