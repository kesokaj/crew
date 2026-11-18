package pipeline

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

func gitIn(t *testing.T, repoDir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Env = gitstore.CleanEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitSingleFile(t *testing.T, repoDir, parent, file, body string) string {
	t.Helper()
	blob := gitIn(t, repoDir, body, "hash-object", "-w", "--stdin")
	var entries strings.Builder
	for _, ln := range strings.Split(gitIn(t, repoDir, "", "ls-tree", parent), "\n") {
		if ln != "" && !strings.HasSuffix(ln, "\t"+file) {
			entries.WriteString(ln + "\n")
		}
	}
	entries.WriteString("100644 blob " + blob + "\t" + file + "\n")
	tree := gitIn(t, repoDir, entries.String(), "mktree")
	return gitIn(t, repoDir, "", "commit-tree", tree, "-p", parent, "-m", file)
}

func prepareVerifiedTicket(t *testing.T, ctx context.Context, d *dbtest.DB, b *board.Board, proj *board.Project, title, branch, head string) *board.Ticket {
	t.Helper()
	tk, err := b.AddTicket(ctx, proj.Name, board.NewTicket{Title: title, Why: "w", What: "w", Goal: "g"}, board.OriginPlan, "lead")
	if err != nil {
		t.Fatal(err)
	}
	main := b.Git.Main(ctx, proj.Name)
	if err := b.Git.AdvanceBranch(ctx, proj.Name, branch, main); err != nil {
		t.Fatal(err)
	}
	gitIn(t, b.Git.Quarantine.Path(proj.Name), "", "update-ref", "refs/heads/"+branch, head)
	tk.Branch = &branch
	tk.Stage = settings.StageVerification
	if _, err := d.Server.Exec(ctx, `UPDATE tickets SET branch = $2, stage = 'verification' WHERE id = $1`, tk.ID, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO records (project, ticket, kind, author, commit, result, body)
		VALUES ($1, $2, 'verify', 'verifier', $3, 'pass', 'ok')`, proj.ID, tk.ID, head); err != nil {
		t.Fatal(err)
	}
	return tk
}

// Fast-forward merges reusing a passed Test check and merge-conflict records start no check sandbox
// and take no room, so neither may insert a transient state='running' check_runs row that capacity.Used
// would count as a 4-CPU / 6-GB check sandbox alongside active work (BUG-038).
func TestNonSandboxMergeCheckRunsNeverInsertRunningState(t *testing.T) {
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "ambient-git-dir"))
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{
		Pool: d.Server,
		Git:  &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		},
	}
	proj, err := b.CreateProject(ctx, board.NewProject{Name: "shop", Key: "SF", Stages: []string{}, Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	baseMain := b.Git.Main(ctx, proj.Name)
	qRepo := b.Git.Quarantine.Path(proj.Name)

	ffHead := commitSingleFile(t, qRepo, baseMain, "a.txt", "branch-v1\n")
	tk1 := prepareVerifiedTicket(t, ctx, d, b, proj, "One", "task/SF-1-one", ffHead)
	if _, err := d.Server.Exec(ctx, `INSERT INTO check_runs (project, ticket, purpose, commit, state, ended_at)
		VALUES ($1, $2, 'test', $3, 'passed', now())`, proj.ID, tk1.ID, ffHead); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ConnectAs(t, db.RoleMigrate).Exec(ctx, `ALTER TABLE check_runs ADD CONSTRAINT no_running_merge CHECK (purpose <> 'merge' OR state <> 'running')`); err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Pool: d.Server, Board: b, Identity: b.Identity, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	j := &job{doc: settings.Doc{}, proj: proj}
	if took, err := p.mergeOne(ctx, j, tk1); err != nil || !took {
		t.Fatalf("fast-forward mergeOne: took=%v err=%v", took, err)
	}

	cRepo := b.Git.Canonical(proj.Name)
	mainNext := commitSingleFile(t, cRepo, baseMain, "a.txt", "main-v2\n")
	gitIn(t, cRepo, "", "update-ref", "refs/heads/main", mainNext)
	gitIn(t, qRepo, "", "fetch", cRepo, "+refs/heads/main:refs/heads/main")
	conflictHead := commitSingleFile(t, qRepo, baseMain, "a.txt", "branch-conflict\n")
	tk2 := prepareVerifiedTicket(t, ctx, d, b, proj, "Two", "task/SF-2-two", conflictHead)
	if took, err := p.mergeOne(ctx, j, tk2); err != nil || !took {
		t.Fatalf("conflict mergeOne: took=%v err=%v", took, err)
	}
	assertRecordedMergeOutcome(t, ctx, b, tk1, board.CheckPassed)
	assertRecordedMergeOutcome(t, ctx, b, tk2, board.CheckFailed)
}

func assertRecordedMergeOutcome(t *testing.T, ctx context.Context, b *board.Board, tk *board.Ticket, wantState string) {
	t.Helper()
	runs, err := b.CheckRuns(ctx, tk)
	if err != nil || len(runs) == 0 {
		t.Fatalf("CheckRuns(%s): %+v %v", tk.Key, runs, err)
	}
	if runs[0].Purpose != board.PurposeMerge || runs[0].State != wantState || runs[0].EndedAt == nil {
		t.Fatalf("CheckRuns(%s)[0] = %+v, want merge/%s with EndedAt", tk.Key, runs[0], wantState)
	}
}
