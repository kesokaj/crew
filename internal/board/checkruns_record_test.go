package board_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
)

// RecordCheckRun writes a finished non-sandbox check_runs row in one statement (never entering
// state='running'), normalizes nil results, and queues a GitHub pull status when a PR is open (BUG-038).
func TestRecordCheckRun(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	tk := e.add(p, ready("A"), board.OriginPlan)
	mig := e.db.ConnectAs(t, db.RoleMigrate)
	if _, err := mig.Exec(e.ctx, `ALTER TABLE check_runs ADD CONSTRAINT no_running_record CHECK (state <> 'running')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO pull_requests (ticket, project, repo, branch) VALUES ($1, $2, 'op/shop', 'task/SF-1-a')`, tk.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.b.RecordCheckRun(e.ctx, p, tk, board.PurposeMerge, "not-a-sha", "", "", board.CheckPassed, nil, ""); err == nil {
		t.Fatal("invalid commit accepted")
	}
	mainSHA := strings.Repeat("a", 40)
	for _, badState := range []string{board.CheckRunning, board.CheckError, ""} {
		if err := e.b.RecordCheckRun(e.ctx, p, tk, board.PurposeMerge, mainSHA, "", "", badState, nil, ""); err == nil {
			t.Fatalf("state %q accepted by RecordCheckRun", badState)
		}
	}
	headSHA := strings.Repeat("b", 40)
	candSHA := strings.Repeat("c", 40)
	if err := e.b.RecordCheckRun(e.ctx, p, tk, board.PurposeMerge, candSHA, mainSHA, headSHA, board.CheckPassed, nil, "the Test check passed on this commit"); err != nil {
		t.Fatalf("RecordCheckRun passed: %v", err)
	}
	if err := e.b.RecordCheckRun(e.ctx, p, tk, board.PurposeMerge, mainSHA, mainSHA, headSHA, board.CheckFailed, nil, "could not merge cleanly with main: README.md"); err != nil {
		t.Fatalf("RecordCheckRun failed: %v", err)
	}
	var rows, posts int
	if err := e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM check_runs WHERE ticket = $1 AND ended_at IS NOT NULL AND results = '[]'::jsonb`, tk.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("check_runs rows = %d, err = %v; want 2", rows, err)
	}
	if err := e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM github_posts WHERE ticket = $1 AND kind = 'status' AND body->>'sha' = $2 AND body->>'context' = 'crew/merge'`, tk.ID, headSHA).Scan(&posts); err != nil || posts != 2 {
		t.Fatalf("github_posts status rows = %d, err = %v; want 2", posts, err)
	}
}
