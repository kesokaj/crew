package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

func TestM21AgentAPIWorkflowAndYAMLContracts(t *testing.T) {
	k := newTicketKit(t)
	k.f.AllowSteer = true
	ctx := context.Background()

	// 1. Harness Runtime endpoint reports agy ACP per-session mode with internal subagents disabled.
	var rtResp struct {
		Harness cli.RuntimeDescriptor `json:"harness"`
		Driver  string                `json:"computeDriver"`
		K8sSeam bool                  `json:"kubernetesSeam"`
	}
	k.call("GET", "/api/agents/runtime", "", http.StatusOK, &rtResp)
	if rtResp.Harness.ActiveCLI != "agy" || !rtResp.Harness.SubagentsDisabled || rtResp.Driver != "docker-kata" || !rtResp.K8sSeam {
		t.Fatalf("unexpected /api/agents/runtime response: %+v", rtResp)
	}
	if strings.Join(rtResp.Harness.Command, " ") != "agy acp --stdio --process-per-session" {
		t.Fatalf("unexpected ACP command: %v", rtResp.Harness.Command)
	}

	// Create a local project: verify baseline WORKFLOW.md is seeded and synced strictly from main.
	p, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "factory",
		Key:    "FC",
		Plan:   "## Goal\n\nShip Software Factory\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	var wfResp board.WorkflowView
	k.call("GET", "/api/projects/factory/workflow", "", http.StatusOK, &wfResp)
	if wfResp.Harness != "agy" || wfResp.MaxTries != 3 || wfResp.MaxAgentHops != 3 || wfResp.FeatureSpecs != "autonomous" {
		t.Fatalf("unexpected initial workflow: %+v", wfResp.WorkflowDef)
	}
	if !cli.NewMatcher(cli.CodeFiles()).Matches("WORKFLOW.md") {
		t.Fatal("WORKFLOW.md must match cli.CodeFiles() under 13.8")
	}

	// 2. Concurrent peer sessions on one project with live steer, stream (JSON + SSE), turn, and abort.
	waitM21 := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	k.custom = func(kind string, spec harness.RunSpec, _ string) (harness.Script, bool) {
		if strings.HasPrefix(spec.Prompt, "Peer ") {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl", Delay: 300 * time.Millisecond}, true
		}
		if strings.HasPrefix(spec.Prompt, "Turn2:") {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl"}, true
		}
		return harness.Script{}, false
	}

	var builderSess, coordSess runs.PeerSession
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"chat_action","role":"@builder","title":"Builder peer","prompt":"Peer builder task"}`,
		http.StatusCreated, &builderSess)
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"coordinator","role":"@coordinator","title":"Coordinator peer","prompt":"Peer coordinator task"}`,
		http.StatusCreated, &coordSess)

	waitM21("both peer sessions working", func() bool {
		var s1, s2 runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+builderSess.ID, "", http.StatusOK, &s1)
		k.call("GET", "/api/agents/sessions/"+coordSess.ID, "", http.StatusOK, &s2)
		return s1.State == "working" && s2.State == "working"
	})

	// Attempting @reviewer without a ticket is rejected with 409 Conflict under checkguard.
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","taskKind":"research","role":"@reviewer","title":"Reviewer peer","prompt":"Turn2: missing ticket"}`,
		http.StatusConflict, nil)

	var activeList struct {
		Sessions []runs.PeerSession `json:"sessions"`
	}
	k.call("GET", "/api/agents/sessions?project=factory&active=1", "", http.StatusOK, &activeList)
	if len(activeList.Sessions) < 2 {
		t.Fatalf("expected >= 2 active peer sessions, got %d", len(activeList.Sessions))
	}

	var steerResp map[string]bool
	k.call("POST", "/api/agents/sessions/"+builderSess.ID+"/steer", `{"text":"Include edge cases"}`, http.StatusOK, &steerResp)
	if !steerResp["steered"] {
		t.Fatalf("expected steered=true, got %+v", steerResp)
	}

	waitM21("both peer sessions idle", func() bool {
		var s1, s2 runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+builderSess.ID, "", http.StatusOK, &s1)
		k.call("GET", "/api/agents/sessions/"+coordSess.ID, "", http.StatusOK, &s2)
		return s1.State == "idle" && s2.State == "idle"
	})

	var streamJSON struct {
		Events []runs.Event `json:"events"`
	}
	k.call("GET", "/api/agents/sessions/"+builderSess.ID+"/stream", "", http.StatusOK, &streamJSON)
	if len(streamJSON.Events) == 0 {
		t.Fatal("expected stored trajectory events from /stream JSON endpoint")
	}

	sseCode, sseBody := do(t, k.srv, "GET", "/api/agents/sessions/"+builderSess.ID+"/stream?sse=1&once=1",
		with(map[string]string{"Accept": "text/event-stream"}), "")
	if sseCode != http.StatusOK || !strings.Contains(sseBody, "data: ") {
		t.Fatalf("unexpected SSE stream response (%d): %s", sseCode, sseBody)
	}

	var turned runs.PeerSession
	k.call("POST", "/api/agents/sessions/"+builderSess.ID+"/turn", `{"text":"Turn2: finalize summary"}`, http.StatusOK, &turned)
	waitM21("builder turn 2 idle", func() bool {
		var cur runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+turned.ID, "", http.StatusOK, &cur)
		return cur.Turn >= 2 && cur.State == "idle"
	})

	var aborted map[string]any
	k.call("POST", "/api/agents/sessions/"+builderSess.ID+"/abort", "", http.StatusOK, &aborted)
	k.call("POST", "/api/agents/sessions/"+coordSess.ID+"/abort", "", http.StatusOK, &aborted)
	waitM21("both peer sessions ended", func() bool {
		var s1, s2 runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+builderSess.ID, "", http.StatusOK, &s1)
		k.call("GET", "/api/agents/sessions/"+coordSess.ID, "", http.StatusOK, &s2)
		return s1.State == "ended" && s2.State == "ended"
	})

	// 3. Branch WORKFLOW.md edit is held under 13.8 and never changes active main workflow.
	t1, err := k.b.AddTicket(ctx, p.Name, board.NewTicket{
		Title:         "Bootstrap API",
		Why:           "Needed for factory",
		What:          "Implement endpoint",
		Goal:          "Endpoint works",
		Kind:          "feature",
		SpecRef:       ".crew/specs/001-api.md",
		MaxTries:      4,
		AssignedAgent: "@builder",
		BudgetTokens:  90000,
		Checks:        []board.CheckSpec{{Kind: "cmd", Spec: "go test ./..."}},
	}, board.OriginOperator, board.Operator)
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	var reviewerSess runs.PeerSession
	k.call("POST", "/api/agents/sessions",
		`{"project":"factory","ticket":"FC-1","role":"@reviewer","title":"Reviewer peer","prompt":"Turn2: review FC-1"}`,
		http.StatusCreated, &reviewerSess)
	if got := cli.Family(reviewerSess.Model); got != "claude" {
		t.Fatalf("@reviewer on ticket resolved family %q, want claude", got)
	}
	waitM21("reviewer idle", func() bool {
		var cur runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+reviewerSess.ID, "", http.StatusOK, &cur)
		return cur.State == "idle"
	})
	k.call("POST", "/api/agents/sessions/"+reviewerSess.ID+"/abort", "", http.StatusOK, &aborted)
	waitM21("reviewer ended", func() bool {
		var cur runs.PeerSession
		k.call("GET", "/api/agents/sessions/"+reviewerSess.ID, "", http.StatusOK, &cur)
		return cur.State == "ended"
	})
	if _, err := k.b.OK(ctx, p.Name, board.Operator); err != nil {
		t.Fatalf("OK: %v", err)
	}
	if err := k.b.Reconcile(ctx, p.Name); err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}
	docsReason := "none"
	if _, err := k.b.Decide(ctx, p, t1.Key, board.Decision{Choice: "approve", DocsReason: &docsReason}, "curator · r_test"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if err := k.b.Reconcile(ctx, p.Name); err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}

	branch := "task/FC-1-workflow"
	if err := k.git.CreateBranch(ctx, p.Name, branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET branch = $2 WHERE id = $1`, t1.ID, branch); err != nil {
		t.Fatalf("set branch: %v", err)
	}
	pushM21Branch(t, k.git.Quarantine.Path(p.Name), branch, map[string]string{
		"WORKFLOW.md": "---\nharness: agy\nmax_tries: 17\nfeature_specs: manual\n---\n# Unapproved\n",
	})

	var wfAfterBranch board.WorkflowView
	k.call("GET", "/api/projects/factory/workflow", "", http.StatusOK, &wfAfterBranch)
	if wfAfterBranch.MaxTries != 3 || wfAfterBranch.FeatureSpecs != "autonomous" {
		t.Fatalf("branch WORKFLOW.md altered active workflow: %+v", wfAfterBranch.WorkflowDef)
	}
	if len(wfAfterBranch.HeldBranches) != 1 || wfAfterBranch.HeldBranches[0].TicketKey != t1.Key {
		t.Fatalf("expected %s in heldBranches, got %+v", t1.Key, wfAfterBranch.HeldBranches)
	}

	// 4. .crew/tickets/<KEY>.md YAML inspection and forward-only sync from main.
	var yamlView struct {
		Key  string `json:"key"`
		Path string `json:"path"`
		YAML string `json:"yaml"`
	}
	k.call("GET", "/api/projects/factory/tasks/FC-1/yaml", "", http.StatusOK, &yamlView)
	if yamlView.Path != ".crew/tickets/FC-1.md" || !strings.Contains(yamlView.YAML, "spec: .crew/specs/001-api.md") {
		t.Fatalf("unexpected YAML view: %+v", yamlView)
	}

	me := gitstore.Identity{Name: "Op", Email: "op@example.com"}
	overlay := gitstore.Overlay{
		".crew/tickets/FC-1.md": func([]byte) []byte {
			return []byte("---\nid: \"0001\"\nkey: FC-1\ntitle: Bootstrap API\nkind: feature\nstage: backlog\norigin: operator\ncreated_by: operator\nspec: .crew/specs/001-api-rev2.md\nmax_tries: 6\nassigned_agent: '@reviewer'\nbudget_tokens: 150000\n---\n")
		},
		".crew/tickets/FC-2.md": func([]byte) []byte {
			return []byte("---\nid: \"0002\"\nkey: FC-2\ntitle: Document Factory\nkind: docs\nstage: backlog\nspec: .crew/specs/001-api-rev2.md\ndepends_on:\n  - FC-1\nchecks:\n  - kind: cmd\n    spec: go test ./...\n---\n")
		},
	}
	if _, err := k.git.CommitOverlay(ctx, p.Name, overlay, "Sync tickets on main", me); err != nil {
		t.Fatalf("CommitOverlay: %v", err)
	}

	var syncResp struct {
		Added   int `json:"added"`
		Updated int `json:"updated"`
	}
	k.call("POST", "/api/projects/factory/tickets/sync", "", http.StatusOK, &syncResp)
	if syncResp.Added != 1 || syncResp.Updated != 1 {
		t.Fatalf("unexpected /tickets/sync counts: %+v", syncResp)
	}
	reloaded, err := k.b.Ticket(ctx, p, "FC-1")
	if err != nil {
		t.Fatalf("reload FC-1: %v", err)
	}
	if reloaded.Stage != settings.StageInProgress || reloaded.SpecRef != ".crew/specs/001-api-rev2.md" || reloaded.MaxTries != 6 {
		t.Fatalf("FC-1 sync violated forward-only stage or metadata: %+v", reloaded)
	}
}

func TestM21UntrackedZeroTicketRefusedAndCustomWorkflowAgentSpawn(t *testing.T) {
	k := newTicketKit(t)
	ctx := context.Background()

	p, err := k.b.CreateProject(ctx, board.NewProject{
		Name:   "secwf",
		Key:    "SW",
		Plan:   "## Goal\n\nVerify R217 closure, @security, and custom WORKFLOW.md specialists.\n",
		Repo:   board.Repo{Kind: "local"},
		Stages: []string{"review", "test"},
	}, board.Operator)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 1. Spawning without ticket AND without taskId/taskKind returns 409 UNTRACKED_WORK (closes TicketID==0 gap).
	var untrackedErr struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	k.call("POST", "/api/agents/sessions",
		`{"project":"secwf","role":"@builder","prompt":"Attempt untracked compute"}`,
		http.StatusConflict, &untrackedErr)
	if untrackedErr.Error.Status != "UNTRACKED_WORK" {
		t.Fatalf("expected UNTRACKED_WORK conflict, got %+v", untrackedErr)
	}

	// 2. Built-in @security peer session mints a FactoryTask before compute and runs on Gemini.
	var secSess runs.PeerSession
	k.call("POST", "/api/agents/sessions",
		`{"project":"secwf","taskKind":"chat_action","role":"@security","title":"Security review","prompt":"Check sandbox rules"}`,
		http.StatusCreated, &secSess)
	if !secSess.ReadOnly || secSess.Role != "@security" || secSess.TaskID == "" || cli.Family(secSess.Model) != "gemini" {
		t.Fatalf("unexpected @security session: %+v", secSess)
	}

	// 3. Custom specialist agent committed on canonical main WORKFLOW.md + .crew/agents/perf-auditor.md spawns cleanly on Gemini.
	me := gitstore.Identity{Name: "Op", Email: "op@example.com"}
	if _, err := k.git.CommitOverlay(ctx, p.Name, gitstore.Overlay{
		".crew/agents/perf-auditor.md": func([]byte) []byte {
			return []byte("# Custom Perf Auditor\nAudit SQL indexes and latency regressions.\n")
		},
		"WORKFLOW.md": func([]byte) []byte {
			return []byte("---\nversion: 1\nharness: agy\nmax_tries: 3\nmax_agent_hops: 3\napprovals:\n  feature_specs: autonomous\nagents:\n  - slug: perf-auditor\n    promptFile: .crew/agents/perf-auditor.md\n    modelBracket: flash\n    readOnly: true\n    skills: [cache-hints]\n---\n# Factory workflow\n")
		},
	}, "Add custom specialist agent to main WORKFLOW.md", me); err != nil {
		t.Fatalf("CommitOverlay: %v", err)
	}

	var customSess runs.PeerSession
	k.call("POST", "/api/agents/sessions",
		`{"project":"secwf","taskKind":"coordinator","role":"@perf-auditor","title":"Perf audit","prompt":"Inspect DB queries"}`,
		http.StatusCreated, &customSess)
	if !customSess.ReadOnly || customSess.Role != "@perf-auditor" || customSess.TaskID == "" || cli.Family(customSess.Model) != "gemini" || !strings.Contains(customSess.Model, "flash") {
		t.Fatalf("expected Gemini flash read-only custom @perf-auditor session, got %+v", customSess)
	}
}

func pushM21Branch(t *testing.T, repoDir, branch string, files map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(gitstore.CleanEnv(),
			"GIT_AUTHOR_NAME=Agent", "GIT_AUTHOR_EMAIL=agent@example.com",
			"GIT_COMMITTER_NAME=Agent", "GIT_COMMITTER_EMAIL=agent@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(tmp, "clone", repoDir, work)
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
