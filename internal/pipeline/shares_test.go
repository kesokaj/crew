package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/models"
)

// Projects share a group of models close to its limit (7.9, R164; 12.7): one new run on it starts in a
// pass, and among the projects that wait for it, it goes to the one served least recently on that
// group, whatever they ran on other groups; the next pass gives the next project its turn. A group
// that is free starts every run.
func TestProjectsTakeTurnsOnAGroupCloseToItsLimit(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	b := &board.Board{Pool: d.Server, Git: &gitstore.Store{Dir: filepath.Join(dir, "c"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "q")}},
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@x"}, nil
		}}
	ids := map[string]string{}
	for i, n := range []string{"first", "second", "third"} {
		p, err := b.CreateProject(ctx, board.NewProject{Name: n, Key: "P" + string(rune('A'+i)), Plan: "## Goal\n\nx\n", Repo: board.Repo{Kind: "local"}}, board.Operator)
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = p.ID
	}
	run := func(project string, n int, model string, ago time.Duration) {
		t.Helper()
		var sub int64
		d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, $2, 'run', 'builder') RETURNING id`, ids[project], n).Scan(&sub)
		id := "00000000-0000-4000-8000-00000000000" + string(rune('0'+n))
		d.Server.Exec(ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'run', 'agy')`, id, ids[project])
		if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image, conversation, started_at)
			VALUES ($1, $2, 1, 'ended', 'agy', $3, 't', 't', $1::uuid, now() - $4::interval)`, id, sub, model, ago.String()); err != nil {
			t.Fatal(err)
		}
	}
	// first ran on Gemini a minute ago; second on Gemini an hour ago, and on Claude just now; third
	// never ran on Gemini.
	run("first", 1, "gemini-3.8-flash", time.Minute)
	run("second", 2, "gemini-3.8-flash", time.Hour)
	run("second", 3, "claude-sonnet-5-5", time.Second)
	run("third", 4, "claude-sonnet-5-5", 2*time.Hour)
	if err := models.RecordLimits(ctx, d.Server, "agy", []cli.Limit{{Group: "gemini", Window: "5h", Used: 0.9}}, "login check"); err != nil {
		t.Fatal(err)
	}
	pl := &Pipeline{Pool: d.Server, Board: b}
	pass := func() {
		g, err := models.LoadGuard(ctx, d.Server, 0.8)
		if err != nil {
			t.Fatal(err)
		}
		pl.shares.beginPass(&g)
	}
	gemini := models.Choice{CLI: "agy", Model: "gemini-3.8-flash"}
	turn := func(project string) (bool, string) {
		t.Helper()
		ok, _, first, done, err := pl.takeTurn(ctx, ids[project], gemini)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			done(true)
		}
		return ok, first
	}
	// A pass in which a run started on Gemini already: each project that waits asks, and is told to wait.
	pass()
	pl.shares.started["agy/gemini"] = true
	for _, n := range []string{"first", "second", "third"} {
		if ok, _ := turn(n); ok {
			t.Fatalf("%s started a second run on Gemini in one pass", n)
		}
	}
	// The next pass visits first, then second, then third: third, never served on Gemini, has the turn.
	pass()
	if ok, who := turn("first"); ok || who != ids["third"] {
		t.Fatalf("first: %v, the turn is %s's", ok, who)
	}
	if ok, who := turn("second"); ok || who != ids["third"] {
		t.Fatalf("second went before third: %v %s", ok, who)
	}
	if ok, _ := turn("third"); !ok {
		t.Fatal("third, never served on Gemini, did not get its turn")
	}
	// One run a pass: nobody else starts on Gemini in this pass.
	if ok, _ := turn("second"); ok {
		t.Fatal("a second run started on Gemini in one pass")
	}
	// A group that is not close starts every run.
	if ok, _, _, _, err := pl.takeTurn(ctx, ids["first"], models.Choice{CLI: "agy", Model: "claude-sonnet-5-5"}); err != nil || !ok {
		t.Fatalf("a run on the free group waited: %v %v", ok, err)
	}
	// The next pass: second, served on Gemini an hour ago, before first, a minute ago; its run on
	// Claude just now does not count.
	run("third", 5, "gemini-3.8-flash", 0)
	pass()
	if ok, _ := turn("first"); ok {
		t.Fatal("first went before second")
	}
	if ok, _ := turn("second"); !ok {
		t.Fatal("second did not get its turn")
	}
}
