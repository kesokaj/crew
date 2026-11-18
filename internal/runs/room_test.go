package runs

import (
	"context"
	"errors"
	"testing"

	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/settings"
)

// A run starts only when its sandbox fits on the machine (7.9, R71), and its sandbox gets its role's
// size (13.10): on a machine with room for one large sandbox the second builder waits, with no
// microVM and no run recorded, until the first one ended.
func TestARunWaitsForRoomAndGetsItsSize(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	// 4 cores and 16 GB, less the reserve: 4 virtual CPUs and 8 GB, one large sandbox.
	k.d.Room = &capacity.Gate{Pool: k.db.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: 4, MemMiB: 16 * 1024}, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return doc, nil }}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	first := k.start(StartRequest{})
	k.wait(first, "working")
	if _, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "Another."}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("a second large sandbox on a full machine: %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs`); n != 1 || len(started(k)) != 1 {
		t.Fatalf("%d runs recorded and %d microVMs started, want the first only", n, len(started(k)))
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE size = 'large'`); n != 1 {
		t.Fatal("the run's size is not recorded")
	}
	if z := started(k)[0].Sizing; z.NanoCPUs != 3e9 || z.MemBytes != 4<<30 {
		t.Fatalf("the builder's microVM is sized %+v, want a large sandbox's", z)
	}
	// The run's page says its size: a large sandbox has 4 virtual CPUs and 6 GB in all, and a Docker
	// daemon of its own (13.10, 14.1).
	if det, err := k.d.Get(ctx, first); err != nil || det.Size != "large" || det.Sizing == nil ||
		*det.Sizing != (SizeView{CPUs: 4, MemMB: 6144, Docker: true}) {
		t.Fatalf("the run's page says size %q, %+v (%v)", det.Size, det.Sizing, err)
	}
	if err := k.d.Stop(ctx, first); err != nil {
		t.Fatal(err)
	}
	k.wait(first, "ended")
	k.start(StartRequest{Prompt: "Now there is room."})
}

// A continue in a new microVM that finds no room leaves the idle run it would replace as it was
// (review of M13): the room is asked for before that run ends.
func TestAContinueWithNoRoomLeavesTheIdleRun(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	doc, err := settings.Defaults(settings.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	k.d.Room = &capacity.Gate{Pool: k.db.Server,
		Host: func(context.Context) (orchestrator.Host, error) {
			return orchestrator.Host{CPUs: 4, MemMiB: 16 * 1024}, nil
		},
		Settings: func(context.Context) (settings.Doc, error) { return doc, nil }}
	first := k.start(StartRequest{})
	v := k.wait(first, "idle")
	k.play(harness.Script{Recording: agyRun, Hold: true})
	second := k.start(StartRequest{Prompt: "Another."})
	k.wait(second, "working")
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "Go on.", Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-low"}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("a continue in a new microVM on a full machine: %v", err)
	}
	if got := k.wait(first, "idle"); got.State != "idle" {
		t.Fatalf("the idle run is %s", got.State)
	}
	var state string
	k.db.Server.QueryRow(ctx, `SELECT state FROM subtasks WHERE id = $1`, v.Subtask).Scan(&state)
	if state == "done" {
		t.Fatal("the subtask is done though nothing continued it")
	}
}

// The reason setup is a sandbox's own word: it counts only from a run whose sandbox ran the project's
// setup (review of M13). The operator's fixture run has none, so a turn that says setup ended in error.
func TestASetupThatARunWithNoSetupClaimsIsAnError(t *testing.T) {
	k := newKit(t)
	k.play(harness.Script{Exit: runspec.Exit{Reason: cli.ReasonSetup, Exit: 3, Detail: "the project's setup failed"}})
	run := k.start(StartRequest{})
	if v := k.wait(run, "ended"); v.StopReason != cli.ReasonError {
		t.Fatalf("stop reason %s", v.StopReason)
	}
}

// When a project's warm microVM (crew-proj-<project>) has an active ACP session in one network mode
// (e.g. internet during analysis), starting a run on a ticket narrowed to model_only waits with
// ErrNoRoom before recording a subtask or run, while a run in the same mode or a standalone judge
// starts immediately; once active_sessions drains to 0, the model_only run starts.
func TestProjectSandboxModeMismatchWaitsForActiveSession(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, project, container, network, image, cli, repos, gcloud, mode)
		VALUES ('proj-fixture-1', 'run', $1::uuid, 'crew-proj-fixture-1', 'crew-proj-fixture-1', 'crew-runtime:test', 'agy', '{}', false, 'internet')`,
		FixtureProject); err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, image_tag, active_sessions, last_active_at, updated_at)
		VALUES ($1::uuid, 'proj-fixture-1', 'running', '/tmp/proj-fixture-home', 'crew-runtime:test', 1, now(), now())`,
		FixtureProject); err != nil {
		t.Fatal(err)
	}
	var modelOnlyTicket, internetTicket int64
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, priority, network, origin, created_by, position)
		VALUES ($1::uuid, 1, 'Model only ticket', 'feature', 'in_progress', 2, 'model_only', 'plan', 'operator', 1) RETURNING id`,
		FixtureProject).Scan(&modelOnlyTicket); err != nil {
		t.Fatal(err)
	}
	if err := k.db.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, priority, network, origin, created_by, position)
		VALUES ($1::uuid, 2, 'Internet ticket', 'feature', 'in_progress', 2, 'internet', 'plan', 'operator', 2) RETURNING id`,
		FixtureProject).Scan(&internetTicket); err != nil {
		t.Fatal(err)
	}

	// 1. A model_only build run while active_sessions=1 in internet mode returns ErrNoRoom without recording a run.
	_, _, err := k.d.StartRole(ctx, RoleRun{
		Role: "builder", Kind: "build", Title: "Build", Project: FixtureProject, Ticket: modelOnlyTicket,
		Branch: "task/FIX-1", CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high", Effort: "high",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Build FIX-1."}},
	})
	if !errors.Is(err, ErrNoRoom) {
		t.Fatalf("expected ErrNoRoom when project microVM has active session in internet mode, got %v", err)
	}
	if n := k.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("expected 0 runs recorded when blocked on project microVM mode mismatch, got %d", n)
	}

	// 2. A standalone judge run on the same model_only ticket does not use the project microVM and starts immediately.
	jrun, _, err := k.d.StartRole(ctx, RoleRun{
		Role: "summary", Kind: "summary", Title: "Summarise", Project: FixtureProject, Ticket: modelOnlyTicket,
		Judge: true, CLI: "agy", Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-low", Effort: "low",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Summarise FIX-1."}},
	})
	if err != nil {
		t.Fatalf("expected standalone judge run to start while project microVM is active in internet mode, got %v", err)
	}
	k.wait(jrun, "idle")
	if err := k.d.Finish(ctx, jrun); err != nil {
		t.Fatal(err)
	}

	// 3. Another run with the same mode (internet) starts in parallel on the active project microVM.
	irun, _, err := k.d.StartRole(ctx, RoleRun{
		Role: "builder", Kind: "build", Title: "Build", Project: FixtureProject, Ticket: internetTicket,
		Branch: "task/FIX-2", CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high", Effort: "high",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Build FIX-2."}},
	})
	if err != nil {
		t.Fatalf("expected internet run to start on active internet project microVM, got %v", err)
	}
	k.wait(irun, "idle")
	if err := k.d.Finish(ctx, irun); err != nil {
		t.Fatal(err)
	}

	// 4. Once active_sessions drains to 0, an in-flight working run in internet mode still blocks a model_only run,
	// and once that working run ends, the model_only ticket run starts cleanly.
	if _, err := k.db.Server.Exec(ctx, `UPDATE project_sandboxes SET active_sessions = 0 WHERE project_id = $1::uuid`, FixtureProject); err != nil {
		t.Fatal(err)
	}
	k.play(harness.Script{Recording: agyRun, Hold: true})
	holdRun, _, err := k.d.StartRole(ctx, RoleRun{
		Role: "builder", Kind: "build", Title: "Build", Project: FixtureProject, Ticket: internetTicket,
		Branch: "task/FIX-2", CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high", Effort: "high",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Hold internet run."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k.wait(holdRun, "working")
	if _, _, err := k.d.StartRole(ctx, RoleRun{
		Role: "builder", Kind: "build", Title: "Build", Project: FixtureProject, Ticket: modelOnlyTicket,
		Branch: "task/FIX-1", CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high", Effort: "high",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Build FIX-1."}},
	}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("expected ErrNoRoom while an in-flight internet run is working, got %v", err)
	}
	if err := k.d.Stop(ctx, holdRun); err != nil {
		t.Fatal(err)
	}
	k.wait(holdRun, "ended")

	mrun, msub, err := k.d.StartRole(ctx, RoleRun{
		Role: "builder", Kind: "build", Title: "Build", Project: FixtureProject, Ticket: modelOnlyTicket,
		Branch: "task/FIX-1", CLI: "agy", Model: "gemini-4-argon", Name: "gemini-4-argon-high", Effort: "high",
		Parts: []PromptPart{{Part: "task", Source: "test", Text: "Build FIX-1."}},
	})
	if err != nil {
		t.Fatalf("expected model_only run to start once active_sessions=0, got %v", err)
	}
	k.wait(mrun, "idle")

	// 5. Continuing that model_only subtask in a new microVM while active_sessions=1 in internet mode also waits with ErrNoRoom and leaves the idle run intact.
	if _, err := k.db.Server.Exec(ctx, `UPDATE project_sandboxes SET active_sessions = 1 WHERE project_id = $1::uuid`, FixtureProject); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Continue(ctx, msub, ContinueRequest{Text: "Go on.", Model: "gemini-3.8-flash", Name: "gemini-3.8-flash-low"}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("expected ErrNoRoom on continueNew when project microVM has active session in internet mode, got %v", err)
	}
	if got := k.wait(mrun, "idle"); got.State != "idle" {
		t.Fatalf("expected idle run to remain idle after ErrNoRoom, got %s", got.State)
	}
}
