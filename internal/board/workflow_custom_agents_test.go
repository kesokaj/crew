package board_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

func TestParseWorkflowCustomAgentsAndBounds(t *testing.T) {
	def, err := board.ParseWorkflow(`---
version: 1
harness: agy
max_tries: 3
max_agent_hops: 3
approvals:
  feature_specs: autonomous
agents:
  - slug: perf-auditor
    promptFile: .crew/agents/perf-auditor.md
    modelBracket: flash
    docker: true
    skills: [sql-tuning, pprof]
  - slug: doc-lint
    prompt_file: .agents/agents/doc-lint.md
    read_only: false
---
# Custom workflow
`)
	if err != nil {
		t.Fatalf("ParseWorkflow valid custom agents: %v", err)
	}
	if len(def.Agents) != 2 {
		t.Fatalf("expected 2 custom agents, got %+v", def.Agents)
	}
	if def.Agents[0].Slug != "perf-auditor" || def.Agents[0].ModelBracket != "flash" || !def.Agents[0].ReadOnly || !def.Agents[0].Docker || len(def.Agents[0].Skills) != 2 {
		t.Fatalf("unexpected first custom agent defaults/fields: %+v", def.Agents[0])
	}
	if def.Agents[1].Slug != "doc-lint" || def.Agents[1].ModelBracket != "pro" || def.Agents[1].ReadOnly {
		t.Fatalf("unexpected second custom agent fields: %+v", def.Agents[1])
	}

	// Reserved built-in slugs must be rejected.
	for _, reserved := range []string{"builder", "reviewer", "security", "planner", "coordinator"} {
		if _, rerr := board.ParseWorkflow("---\nagents:\n  - slug: " + reserved + "\n---\n"); rerr == nil {
			t.Fatalf("expected reserved slug %q to be rejected", reserved)
		}
	}

	// Traversal or outside .crew/agents / .agents/agents paths must be rejected.
	if _, perr := board.ParseWorkflow("---\nagents:\n  - slug: bad-path\n    promptFile: ../secret.md\n---\n"); perr == nil {
		t.Fatal("expected path traversal promptFile to be rejected")
	}
	if _, perr := board.ParseWorkflow("---\nagents:\n  - slug: bad-root\n    promptFile: README.md\n---\n"); perr == nil {
		t.Fatal("expected non-agent promptFile to be rejected")
	}

	// More than 16 custom agents must be rejected.
	var sb strings.Builder
	sb.WriteString("---\nagents:\n")
	for i := range 17 {
		sb.WriteString("  - slug: custom-" + string(rune('a'+i)) + "\n")
	}
	sb.WriteString("---\n")
	if _, cerr := board.ParseWorkflow(sb.String()); cerr == nil {
		t.Fatal("expected > 16 custom agents to fail ParseWorkflow")
	}
}

func TestMainWorkflowAndCustomAgentPromptRefuseSymlinks(t *testing.T) {
	e := newEnv(t)
	p := e.project("customsym", "CS")

	// Commit valid custom specialist + WORKFLOW.md onto canonical main.
	me := gitstore.Identity{Name: "Op", Email: "op@example.com"}
	if _, err := e.git.CommitOverlay(e.ctx, p.Name, gitstore.Overlay{
		".crew/agents/perf-auditor.md": func([]byte) []byte {
			return []byte("# Perf Auditor\nFocus on latency regressions and SQL indexes.\n")
		},
		"WORKFLOW.md": func([]byte) []byte {
			return []byte("---\nversion: 1\nharness: agy\nmax_tries: 3\nmax_agent_hops: 3\napprovals:\n  feature_specs: autonomous\nagents:\n  - slug: perf-auditor\n    promptFile: .crew/agents/perf-auditor.md\n    modelBracket: flash\n    readOnly: true\n    skills: [cache-hints]\n---\n# Factory\n")
		},
	}, "Add custom perf-auditor on main", me); err != nil {
		t.Fatalf("CommitOverlay valid: %v", err)
	}

	wf, err := e.b.GetWorkflow(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if len(wf.Agents) != 1 || wf.Agents[0].Slug != "perf-auditor" {
		t.Fatalf("expected perf-auditor in synced WorkflowView, got %+v", wf.Agents)
	}

	ag, body, err := e.b.LoadCustomWorkflowAgent(e.ctx, p, "@perf-auditor")
	if err != nil {
		t.Fatalf("LoadCustomWorkflowAgent: %v", err)
	}
	if ag.Slug != "perf-auditor" || !strings.Contains(body, "Focus on latency regressions") {
		t.Fatalf("unexpected loaded custom agent: %+v body=%q", ag, body)
	}

	// Push a git symlink (mode 120000) for .crew/agents/perf-auditor.md onto canonical main and confirm fail-closed refusal.
	cloneWork := filepath.Join(t.TempDir(), "work")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(gitstore.CleanEnv(),
			"GIT_AUTHOR_NAME=Op", "GIT_AUTHOR_EMAIL=op@example.com",
			"GIT_COMMITTER_NAME=Op", "GIT_COMMITTER_EMAIL=op@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(filepath.Dir(cloneWork), "clone", e.git.Canonical(p.Name), cloneWork)
	symPath := filepath.Join(cloneWork, ".crew", "agents", "perf-auditor.md")
	if err := os.Remove(symPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", symPath); err != nil {
		t.Fatal(err)
	}
	runGit(cloneWork, "add", ".crew/agents/perf-auditor.md")
	runGit(cloneWork, "commit", "-m", "Replace promptFile with symlink")
	runGit(cloneWork, "push", "origin", "main")

	if _, _, lerr := e.b.LoadCustomWorkflowAgent(e.ctx, p, "perf-auditor"); !errors.Is(lerr, gitstore.ErrSymlinkRefused) {
		t.Fatalf("expected ErrSymlinkRefused on symlinked promptFile, got %v", lerr)
	}

	// Replace WORKFLOW.md itself with a symlink on main and verify SyncMainWorkflow refuses it fail-closed.
	wfSym := filepath.Join(cloneWork, "WORKFLOW.md")
	if err := os.Remove(wfSym); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", wfSym); err != nil {
		t.Fatal(err)
	}
	runGit(cloneWork, "add", "WORKFLOW.md")
	runGit(cloneWork, "commit", "-m", "Replace WORKFLOW.md with symlink")
	runGit(cloneWork, "push", "origin", "main")

	if _, serr := e.b.SyncMainWorkflow(e.ctx, e.db.Server, p); !errors.Is(serr, gitstore.ErrSymlinkRefused) {
		t.Fatalf("expected ErrSymlinkRefused on symlinked WORKFLOW.md, got %v", serr)
	}
}
