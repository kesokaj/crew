package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/version"
	"github.com/kesokaj/crew/internal/versions"
)

// The server builds releases only with its sandboxes and the updater's socket (16.2 step 4); when it
// starts, a build a stop cut off is failed.
func TestTheServerBuildsWithItsSandboxesAndTheUpdater(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{DataDir: t.TempDir()}
	vs := &versions.Service{Pool: d.Server, Releases: filepath.Join(e.DataDir, "releases"), Updater: updater.NewClient("/nonexistent.sock")}
	if b := openBuilder(ctx, e, d.Server, nil, vs, log); b != nil {
		t.Fatal("a builder with no machine")
	}
	if b := openBuilder(ctx, e, d.Server, &machine{}, vs, log); b != nil {
		t.Fatal("a builder with no orchestrator")
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO builds (version, commit, asked_by) VALUES ('9.9.9', $1, 'operator')`, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	m := &machine{orch: &orchestrator.Orchestrator{}, sandboxes: &sandboxes.Manager{}}
	b := openBuilder(ctx, e, d.Server, m, vs, log)
	if b == nil || b.Builds != filepath.Join(e.DataDir, "builds") || b.Releases != vs.Releases || b.Running != version.Version || b.Loader == nil || b.Sandbox == nil {
		t.Fatalf("%+v", b)
	}
	bd, err := b.Get(ctx, "9.9.9")
	if err != nil || bd.State != versions.BuildFailed {
		t.Fatalf("the cut-off build: %+v %v", bd, err)
	}
}
