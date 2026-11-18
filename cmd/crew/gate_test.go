package main

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/versions"
)

// The server gives its builder the gate's parts: the updater's gate and the live steps, and a gate's
// make check that a stop cut off runs again; once the board exists, the live steps get it, so a live
// gate's step goes past "no board" (16.2 step 5).
func TestTheServerWiresTheGate(t *testing.T) {
	d := useDB(t, false)
	ctx := context.Background()
	if _, err := d.Server.Exec(ctx, `INSERT INTO gates (version, state) VALUES ('0.3.0', 'checking')`); err != nil {
		t.Fatal(err)
	}
	b := &versions.Builder{Pool: d.Server}
	gates := updater.NewClient("/nonexistent.sock")
	if err := gateParts(ctx, b, gates, d.Server); err != nil {
		t.Fatal(err)
	}
	if b.Gates == nil || b.Live == nil {
		t.Fatalf("the builder's gate: %+v", b)
	}
	var n int
	d.Server.QueryRow(ctx, `SELECT count(*) FROM gates`).Scan(&n)
	if n != 0 {
		t.Fatal("a cut-off make check is left")
	}
	l := versions.LiveStart{Version: "0.3.0", Image: "sha256:" + strings.Repeat("c", 64)}
	if _, err := b.Live.Step(ctx, l); err == nil || !strings.Contains(err.Error(), "no board") {
		t.Fatalf("before the board: %v", err)
	}
	d.Server.Exec(ctx, `INSERT INTO gates (version, state) VALUES ('0.3.0', 'live')`)
	if !wireLive(&versions.Service{Builder: b}, versions.LiveDeps{Board: &board.Board{Pool: d.Server}}) {
		t.Fatal("no live steps were wired")
	}
	if _, err := b.Live.Step(ctx, l); err == nil || strings.Contains(err.Error(), "no board") || !strings.Contains(err.Error(), "crew-gate is not made") {
		t.Fatalf("after the board: %v", err)
	}
	if wireLive(nil, versions.LiveDeps{}) || wireLive(&versions.Service{}, versions.LiveDeps{}) {
		t.Fatal("a server with no builder wired live steps")
	}
}
