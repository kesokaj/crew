package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

// A prompt's records keep the newest review and test entry however many notes came after them, so a
// run cannot write a verdict out of the next prompt (security review of M6).
func TestAPromptKeepsTheVerdictsPastManyNotes(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	p, err := b.CreateProject(ctx, board.NewProject{Name: "shop", Key: "SF", Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := b.AddTicket(ctx, p.Name, board.NewTicket{Title: "One", Why: "w", What: "w", Goal: "g",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
	if err != nil {
		t.Fatal(err)
	}
	add := func(n board.NewRecord) {
		if _, err := b.AddRecord(ctx, p, tk.Key, n); err != nil {
			t.Fatal(err)
		}
	}
	add(board.NewRecord{Kind: board.RecordReview, Author: "reviewer", Verdict: board.VerdictChanges, Body: "Fix the unlock."})
	add(board.NewRecord{Kind: board.RecordTest, Author: "tester", Result: "fail", Body: "It fails."})
	for i := 0; i < 15; i++ {
		add(board.NewRecord{Kind: board.RecordNote, Author: "builder", Body: fmt.Sprintf("note %d", i)})
	}
	pl := &Pipeline{Pool: d.Server, Board: b}
	recs, err := pl.records(ctx, tk)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, r := range recs {
		kinds[r.Kind]++
	}
	if len(recs) != 10 || kinds["review"] != 1 || kinds["test"] != 1 || recs[len(recs)-1].Body != "note 14" {
		t.Fatalf("%d records: %v", len(recs), kinds)
	}
}
