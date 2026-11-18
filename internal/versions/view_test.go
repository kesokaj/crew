package versions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

func TestViewExcludesHostedFromRuntimeImageCount(t *testing.T) {
	k := newKit(t, "0.2.0")
	ctx := context.Background()
	k.pass()

	for _, s := range []struct {
		id, kind, image string
	}{
		{"run-cur", "run", "crew-runtime:0.2.0"},
		{"run-old", "run", "crew-runtime:0.1.0"},
		{"host-1", "hosted", "sha256:63a3b669c6187da75743ce32eb69eddad7565ea2325725934eb6850253f18fcc"},
	} {
		if _, err := k.d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ($1, $2, $1, $3)`, s.id, s.kind, s.image); err != nil {
			t.Fatal(err)
		}
	}

	v, err := k.s.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.OnImage != 1 || v.OnOldImage != 1 {
		t.Fatalf("OnImage = %d, OnOldImage = %d, want 1 and 1 (hosted container excluded)", v.OnImage, v.OnOldImage)
	}
}

func TestScanSyncsByHandMarkedAfterBuild(t *testing.T) {
	k := newKit(t, "0.1.0")
	ctx := context.Background()

	// 1. crew-ctl build creates 0.2.0 without gate.json and with byHand: false.
	k.release("0.2.0", map[string]string{"cmd/crew/main.go": "new"}, false)
	k.pass()

	var byHand bool
	if err := k.d.Server.QueryRow(ctx, `SELECT by_hand FROM releases WHERE version = '0.2.0'`).Scan(&byHand); err != nil {
		t.Fatal(err)
	}
	if byHand {
		t.Fatal("expected by_hand = false before install --from")
	}

	// 2. crew-ctl install --from marks release.json with byHand: true.
	manifestPath := filepath.Join(k.rel, "0.2.0", release.ManifestFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m release.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.ByHand = true
	updated, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(updated, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Subsequent Scan updates releases.by_hand = true, and when 0.3.0 runs, 0.2.0 is rollback-eligible.
	k.release("0.3.0", map[string]string{"cmd/crew/main.go": "newer"}, true)
	k.s.Running = "0.3.0"
	k.pass()

	v, err := k.s.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range v.Releases {
		if r.Version == "0.2.0" {
			found = true
			if !r.ByHand || !r.RollBack {
				t.Fatalf("release 0.2.0: ByHand = %v, RollBack = %v, want true and true", r.ByHand, r.RollBack)
			}
		}
	}
	if !found {
		t.Fatal("release 0.2.0 not found in View")
	}
}
