package board_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

func TestWorkflowSeededAndReadFromMainOnly(t *testing.T) {
	e := newEnv(t)
	p := e.project("workflow-proj", "WF")

	wf, err := e.b.GetWorkflow(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if wf.Harness != "agy" || wf.MaxTries != 3 || wf.MaxAgentHops != 3 || wf.FeatureSpecs != "autonomous" {
		t.Fatalf("unexpected defaults from baseline WORKFLOW.md: %+v", wf.WorkflowDef)
	}
	if !strings.Contains(wf.RawMarkdown, `project: "workflow-proj"`) || !strings.Contains(wf.RawMarkdown, `key: "WF"`) {
		t.Fatalf("baseline WORKFLOW.md missing project identity: %s", wf.RawMarkdown)
	}
	if len(wf.HeldBranches) != 0 {
		t.Fatalf("expected no held branches initially, got %+v", wf.HeldBranches)
	}

	// Open a ticket with a branch and push an unapproved WORKFLOW.md change onto that ticket branch.
	tk := e.add(p, ready("Edit workflow on branch"), board.OriginOperator)
	branch := "task/WF-1-workflow"
	if err := e.git.CreateBranch(e.ctx, p.Name, branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'in_progress', branch = $2 WHERE id = $1`, tk.ID, branch); err != nil {
		t.Fatalf("update ticket branch: %v", err)
	}
	pushBranchCommit(t, e.git, p.Name, branch, map[string]string{
		"WORKFLOW.md": "---\nharness: agy\nmax_tries: 19\nmax_agent_hops: 1\napprovals:\n  feature_specs: manual\n---\n# Hijacked branch workflow\n",
	})

	reloaded := e.ticket(p, tk.Key)
	codeFiles, err := e.b.CodeChanges(e.ctx, p, reloaded)
	if err != nil {
		t.Fatalf("CodeChanges: %v", err)
	}
	if len(codeFiles) != 1 || codeFiles[0] != "WORKFLOW.md" {
		t.Fatalf("expected WORKFLOW.md flagged under 13.8 CodeChanges, got %v", codeFiles)
	}

	afterBranchPush, err := e.b.GetWorkflow(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatalf("GetWorkflow after branch push: %v", err)
	}
	if afterBranchPush.MaxTries != 3 || afterBranchPush.FeatureSpecs != "autonomous" {
		t.Fatalf("branch WORKFLOW.md leaked into active workflow! got %+v", afterBranchPush.WorkflowDef)
	}
	if len(afterBranchPush.HeldBranches) != 1 || afterBranchPush.HeldBranches[0].TicketKey != tk.Key || afterBranchPush.HeldBranches[0].Approved {
		t.Fatalf("expected held unapproved branch entry for %s, got %+v", tk.Key, afterBranchPush.HeldBranches)
	}

	// Updating main directly advances workflow_defs on GetWorkflow/SyncMainWorkflow.
	mainHead := e.git.Main(e.ctx, p.Name)
	nextSHA, err := e.git.CommitPage(e.ctx, p.Name, mainHead, "WORKFLOW.md",
		[]byte("---\nversion: 1\nharness: agy\nmax_tries: 5\nmax_agent_hops: 4\napprovals:\n  feature_specs: manual\n---\n# Updated main\n"),
		"Update WORKFLOW.md on main", gitstore.Identity{Name: "Test", Email: "test@example.com"})
	if err != nil {
		t.Fatalf("CommitPage: %v", err)
	}
	updated, err := e.b.GetWorkflow(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatalf("GetWorkflow after main update: %v", err)
	}
	if updated.MainSHA != nextSHA || updated.MaxTries != 5 || updated.MaxAgentHops != 4 || updated.FeatureSpecs != "manual" {
		t.Fatalf("expected workflow_defs updated from main, got %+v", updated.WorkflowDef)
	}
}

func pushBranchCommit(t *testing.T, gs *gitstore.Store, projName, branch string, files map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	qRepo := gs.Quarantine.Path(projName)
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Agent", "GIT_AUTHOR_EMAIL=agent@example.com",
			"GIT_COMMITTER_NAME=Agent", "GIT_COMMITTER_EMAIL=agent@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(tmp, "clone", qRepo, work)
	runGit(work, "checkout", "-b", branch)
	for rel, body := range files {
		dst := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(work, "add", "-A")
	runGit(work, "commit", "-m", "branch edit")
	runGit(work, "push", "origin", branch)
}
