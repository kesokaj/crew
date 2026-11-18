package board_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

func TestGitMirrorSyncAndImportRoundTrip(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	tk1, err := e.b.AddTicket(e.ctx, p.ID, board.NewTicket{
		Title: "Core cart domain",
		Kind:  "feature",
		Why:   "Need cart types.",
		What:  "Define Cart and Item.",
		Goal:  "go test ./internal/cart passes.",
		Checks: []board.CheckSpec{
			{Kind: "cmd", Spec: "go test ./internal/cart"},
		},
	}, board.OriginOperator, "op")
	if err != nil {
		t.Fatal(err)
	}
	tk2, err := e.b.AddTicket(e.ctx, p.ID, board.NewTicket{
		Title:     "Cart HTTP API",
		Kind:      "feature",
		Why:       "Expose cart over HTTP.",
		What:      "Add /api/cart handler.",
		Goal:      "go test ./internal/httpapi passes.",
		DependsOn: []string{tk1.Key},
	}, board.OriginOperator, "op")
	if err != nil {
		t.Fatal(err)
	}
	_ = tk2

	commit, err := e.b.SyncMirror(e.ctx, p, "SF: sync .crew mirror")
	if err != nil || commit == "" {
		t.Fatalf("SyncMirror: commit=%q err=%v", commit, err)
	}
	// A second SyncMirror with no changes is a no-op on main.
	commit2, err := e.b.SyncMirror(e.ctx, p, "SF: sync .crew mirror again")
	if err != nil || commit2 != commit {
		t.Fatalf("second SyncMirror moved main: %q -> %q (%v)", commit, commit2, err)
	}

	blob, err := e.git.ReadBlob(e.ctx, p.Name, "main", ".crew/tickets/"+tk2.Key+".md")
	if err != nil || !strings.Contains(blob.Content, "Cart HTTP API") || !strings.Contains(blob.Content, "depends_on") {
		t.Fatalf("ReadBlob(.crew/tickets/%s.md): %+v %v", tk2.Key, blob, err)
	}

	// Simulate restoring into a fresh project that has the same canonical repository's .crew/ files.
	p2, err := e.b.CreateProject(e.ctx, board.NewProject{
		Name: "shop-restored",
		Key:  "SR",
		Goal: "Restored shop",
		Plan: "# Placeholder\n",
		Repo: board.Repo{Kind: "local"},
	}, "op")
	if err != nil {
		t.Fatal(err)
	}
	o, err := e.b.MirrorOverlay(e.ctx, e.db.Server, p, tk1)
	if err != nil {
		t.Fatal(err)
	}
	// Overlay the full mirror of shop (with SF-1 marked Done) onto shop-restored's main.
	full, err := e.b.MirrorOverlay(e.ctx, e.db.Server, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range o {
		full[k] = v
	}
	if _, err := e.git.CommitOverlay(e.ctx, p2.Name, full, "seed .crew", me); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE projects SET state = 'planning' WHERE id = $1`, p2.ID); err != nil {
		t.Fatal(err)
	}
	doc, err := settings.ProjectDefaults(p2.Name, p2.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(e.ctx, e.db.Server, func(tx pgx.Tx) error {
		imported, err := e.b.ImportMirror(e.ctx, tx, p2, doc, "op")
		if err != nil {
			return err
		}
		if !imported {
			t.Fatal("expected ImportMirror to return true")
		}
		return settings.SaveProject(e.ctx, tx, p2.ID, p2.Name, p2.Key, doc, "op")
	}); err != nil {
		t.Fatal(err)
	}

	p2, err = e.b.Project(e.ctx, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p2.State != "paused" {
		t.Fatalf("expected restored project state 'paused', got %q", p2.State)
	}
	tks, err := e.b.Tickets(e.ctx, p2)
	if err != nil || len(tks) != 3 {
		t.Fatalf("restored tickets: %d (%v)", len(tks), err)
	}
	if tks[1].Key != "SR-2" || tks[1].Stage != "done" || len(tks[1].Checks) != 1 {
		t.Fatalf("restored SR-2: %+v", tks[1])
	}
	if tks[2].Key != "SR-3" || len(tks[2].Links) != 1 || tks[2].Links[0].To != "SR-2" {
		t.Fatalf("restored SR-3: %+v", tks[2])
	}
}
