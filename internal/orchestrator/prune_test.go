package orchestrator

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/kesokaj/crew/internal/config"
)

// The disk guard's first mark removes dangling images and the build cache, each older than a day,
// and nothing else (16.10).
func TestPruneTakesOnlyDanglingImagesAndOldBuildCache(t *testing.T) {
	fd := newFakeDocker()
	o := &Orchestrator{docker: fd, cfg: &config.Config{}, logger: slog.New(slog.DiscardHandler)}
	freed, err := o.Prune(context.Background())
	if err != nil || freed != 3<<20 {
		t.Fatalf("%d %v", freed, err)
	}
	fd.mu.Lock()
	calls := append([]string(nil), fd.calls...)
	fd.mu.Unlock()
	if !slices.Equal(calls, []string{"ImagesPrune(24h)", "BuildCachePrune(24h)"}) {
		t.Fatalf("calls %v", calls)
	}
}
