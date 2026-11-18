package memory_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/memory"
)

func TestDreamstoreDriftDecayPassBoostAndQuarantine(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()

	const projID = "99000000-0000-4000-8000-000000000001"
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, repo_source, state)
		VALUES ($1::uuid, 'dreamproj', 'DR', 'local', 'running')`, projID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	if _, err := memory.Add(ctx, d.Server, memory.AddInput{
		ProjectID: projID, Kind: "semantic", Title: "Bad entry", Body: "token sk-ant-secret1234567890",
	}); err == nil {
		t.Fatal("expected secret-shaped dreamstore entry to be rejected")
	}

	sem, err := memory.Add(ctx, d.Server, memory.AddInput{
		ProjectID: projID,
		Kind:      "semantic",
		Title:     "Store bounds contract",
		Body:      "internal/cache/store.go enforces 1024-entry LRU eviction.",
		Paths:     []string{"internal/cache/store.go"},
		Author:    "researcher",
	})
	if err != nil || sem.ValidityScore != 1.0 || sem.Status != "active" {
		t.Fatalf("unexpected semantic entry: %+v err=%v", sem, err)
	}

	if _, err := memory.Add(ctx, d.Server, memory.AddInput{
		ProjectID: projID,
		Kind:      "semantic",
		Title:     "Store bounds contract",
		Body:      "internal/cache/store.go enforces 1024-entry LRU eviction.",
	}); err == nil {
		t.Fatal("expected Jaccard near-duplicate dreamstore entry to be rejected")
	}

	proc, err := memory.Add(ctx, d.Server, memory.AddInput{
		ProjectID: projID,
		Kind:      "procedural",
		Title:     "Verify cache package tests before push",
		Body:      "Run go test ./internal/cache/... after modifying store.go.",
		AppliesTo: []string{"builder"},
		Author:    "builder",
	})
	if err != nil {
		t.Fatalf("add procedural: %v", err)
	}

	// Merge 1 touching internal/cache/store.go -> 1.0 - 0.35 = 0.65 (drift).
	decayed, quar, err := memory.ApplyCommitDrift(ctx, d.Server, projID, "commit-1", []string{"internal/cache/store.go"})
	if err != nil || decayed != 1 || quar != 0 {
		t.Fatalf("expected 1 drift decay 0 quarantine, got decayed=%d quar=%d err=%v", decayed, quar, err)
	}

	// Merge 2 touching internal/cache/store.go -> 0.65 - 0.35 = 0.30 (< 0.40 -> quarantined).
	decayed, quar, err = memory.ApplyCommitDrift(ctx, d.Server, projID, "commit-2", []string{"internal/cache/store.go"})
	if err != nil || decayed != 1 || quar != 1 {
		t.Fatalf("expected quarantine after second drift, got decayed=%d quar=%d err=%v", decayed, quar, err)
	}

	// Agent search/list must exclude quarantined semantic entry (< 0.40).
	forAgents, err := memory.Search(ctx, d.Server, projID, "cache", 10, true)
	if err != nil {
		t.Fatalf("Search forAgent: %v", err)
	}
	if len(forAgents) != 1 || forAgents[0].Number != proc.Number {
		t.Fatalf("expected only active procedural entry for agents, got %+v", forAgents)
	}

	// Check-pass boost restores quarantined entry when verified, or contradiction drops below 0.40.
	updated, err := memory.RecordOutcome(ctx, d.Server, projID, sem.Number, "run-1", "commit-3", true, false)
	if err != nil || updated.HitPassCount != 1 || updated.ValidityScore != 0.45 || updated.Status != "active" {
		t.Fatalf("unexpected boosted entry: %+v err=%v", updated, err)
	}
	contra, err := memory.RecordOutcome(ctx, d.Server, projID, proc.Number, "run-2", "", false, true)
	if err != nil || contra.ContradictionCount != 1 {
		t.Fatalf("unexpected contra entry: %+v err=%v", contra, err)
	}
}
