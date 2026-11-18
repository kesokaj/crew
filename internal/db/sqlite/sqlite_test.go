package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/sqlite"
)

func TestWALStoreCrashRecoveryAndQueries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if st.JournalMode() != "WAL" {
		t.Fatalf("expected WAL journal mode, got %q", st.JournalMode())
	}

	if err := st.SavePrefs(sqlite.OperatorPrefs{
		Email: "op@example.com", Theme: "linear", Dark: true, Direction: "A",
	}); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	if err := st.UpsertTrajectory(sqlite.TrajectoryRecord{
		RunID:           "r-1",
		ProjectID:       "p-1",
		TicketKey:       "SF-12",
		Role:            "builder",
		ToolVerbs:       []string{"read_chat", "edit_file"},
		TouchedPaths:    []string{"internal/cache/evict.go"},
		EventsCount:     14,
		TurnsCount:      2,
		TokensTotal:     4200,
		DurationSeconds: 18,
		Outcome:         "pass",
		SearchText:      "SF-12 builder evict.go read_chat",
		StartedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertTrajectory: %v", err)
	}

	// Simulate an un-checkpointed crash by appending a truncated torn WAL line and reopening.
	wal, err := os.OpenFile(dbPath+"-wal", os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = wal.WriteString("deadbeef {\"op\":\"broken")
	_ = wal.Close()

	reopened, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after torn WAL tail: %v", err)
	}
	pref, ok := reopened.GetPrefs("op@example.com")
	if !ok || pref.Theme != "linear" || !pref.Dark {
		t.Fatalf("expected recovered linear dark pref, got %+v ok=%v", pref, ok)
	}
	hits := reopened.SearchTrajectories(sqlite.TrajectoryQuery{
		ProjectID: "p-1", Query: "evict", Tool: "read_chat", Path: "internal/cache", Outcome: "pass",
	})
	if len(hits) != 1 || hits[0].RunID != "r-1" {
		t.Fatalf("expected 1 recovered trajectory hit, got %+v", hits)
	}

	if err := reopened.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	stats := reopened.Stats()
	if !stats.Healthy || stats.Checkpoints != 1 || stats.WALFrames != 0 || stats.TrajectoriesCount != 1 {
		t.Fatalf("unexpected Stats after checkpoint: %+v", stats)
	}
}
