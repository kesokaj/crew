package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/mcp"
)

// get_summary and search_summaries read a project's own summaries: a written one with its facts, a
// search by its words, and nothing of another project.
func TestTheSummaryToolsReadTheProjectsSummaries(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	var ps []*board.Project
	for _, n := range []string{"shop", "other"} {
		p, err := b.CreateProject(ctx, board.NewProject{Name: n, Key: map[string]string{"shop": "SF", "other": "OT"}[n], Plan: "## Goal\n\nx\n",
			Repo: board.Repo{Kind: "local"}}, board.Operator)
		if err != nil {
			t.Fatal(err)
		}
		tk, err := b.AddTicket(ctx, p.Name, board.NewTicket{Title: "Lockout in " + n, Why: "w", What: "w", Goal: "g",
			Checks: []board.CheckSpec{{Kind: "cmd", Spec: "true"}}}, board.OriginPlan, "lead")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Server.Exec(ctx, `INSERT INTO summaries (ticket, project, outcome, facts, done, how, next, missing)
			SELECT id, project, 'done', '{"fix_rounds": 1}', 'Added the lockout of '||$2, 'One round.', 'Test early.', false
			FROM tickets WHERE id = (SELECT id FROM tickets WHERE project = $1)`, p.ID, n); err != nil {
			t.Fatal(err)
		}
		_ = tk
		ps = append(ps, p)
	}
	tools := &Tools{Pool: d.Server, Board: b}
	c := mcp.Caller{Project: ps[0].ID, Role: "builder"}
	got, err := tools.getSummary(ctx, c, json.RawMessage(`{"key": "SF-1"}`))
	if l, ok := got.(summaryLine); err != nil || !ok || l.Done != "Added the lockout of shop" || l.Facts["fix_rounds"] != float64(1) {
		t.Fatalf("%+v %v", got, err)
	}
	var re *mcp.Error
	if _, err := tools.getSummary(ctx, c, json.RawMessage(`{"key": "SF-9"}`)); !errors.As(err, &re) {
		t.Fatalf("a summary that is not there: %v", err)
	}
	res, err := tools.searchSummaries(ctx, c, json.RawMessage(`{"query": "lockout"}`))
	if err != nil {
		t.Fatal(err)
	}
	found := res.(map[string]any)["summaries"].([]summaryLine)
	if len(found) != 1 || found[0].Ticket != "SF-1" {
		t.Fatalf("the search reached another project, or found nothing: %+v", found)
	}
}

// The agents are told who says which ticket a bug is in, and when (design 0.54): analyse_ticket's
// bugIn is the lead's, not when the bug's ticket is settled; report_bug's inTicket is checked by another
// agent; and the analysis stage's prompt and the tools' texts say the same (verifier of 621d116..4faf130).
func TestTheAgentsAreToldWhoPlacesABug(t *testing.T) {
	d := dbtest.New(t, false)
	s, err := mcp.New(d.Server, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Tools{Pool: d.Server}).Register(s); err != nil {
		t.Fatal(err)
	}
	describe := func(tool, field string) string {
		t.Helper()
		tl := s.Tool(tool)
		if tl == nil {
			t.Fatalf("no tool %s", tool)
		}
		props, _ := tl.Input["properties"].(map[string]any)
		f, _ := props[field].(map[string]any)
		d, _ := f["description"].(string)
		return d
	}
	if d := describe("analyse_ticket", "bugIn"); !strings.HasPrefix(d, "The curator,") || !strings.Contains(d, "settled") || !strings.Contains(d, "or none") {
		t.Fatalf("analyse_ticket's bugIn: %q", d)
	}
	if d := describe("report_bug", "inTicket"); !strings.Contains(d, "Another agent checks it") {
		t.Fatalf("report_bug's inTicket: %q", d)
	}
	for path, words := range map[string][]string{
		"../../baseline/stages/analysis.md": {"`bugIn`", "settled already", "none as a reviewer"},
		"../../baseline/tools.yaml":         {"the curator says in bugIn", "never a reviewer", "finds which Done"},
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(body)), " ")
		for _, w := range words {
			if !strings.Contains(text, w) {
				t.Errorf("%s does not say %q", path, w)
			}
		}
	}
}
