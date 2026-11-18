package inbox_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/settings"
)

func machine(t *testing.T, own settings.Doc) settings.Doc {
	t.Helper()
	def, err := settings.Defaults(settings.Machine{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return settings.Merge(def, own)
}

// The inbox holds what needs the operator and nothing else (7.8): a breakdown that waits for its OK,
// a project Crew paused at a limit, and a CLI that is signed out. A project whose breakdown was sent
// back, or whose lead is still writing it, a project the operator paused, and a CLI that is signed
// in need nobody.
func TestTheInboxHoldsWhatNeedsTheOperator(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server) // every agent has a model, so none is in the inbox for that
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	project := func(id, name, key, state string) {
		exec(`INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, $2, $3, $4, 'local')`, id, name, key, state)
	}
	ticket := func(project string, n int, stage string) {
		exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
			VALUES ($1, $2, 't', 'feature', $3, 'plan', 'lead', $4)`, project, n, stage, int64(n))
	}
	plan := func(project, kind string, n int) {
		exec(`INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, $2, $3, 'x', 'lead')`, project, kind, n)
	}
	const (
		waits    = "00000000-0000-4000-8000-000000000001"
		sentBack = "00000000-0000-4000-8000-000000000002"
		writing  = "00000000-0000-4000-8000-000000000003"
		atLimit  = "00000000-0000-4000-8000-000000000004"
		byYou    = "00000000-0000-4000-8000-000000000005"
	)
	project(waits, "waits", "WA", "planning")
	ticket(waits, 1, "planning")
	ticket(waits, 2, "planning")
	plan(waits, "original", 1)
	plan(waits, "breakdown", 1)

	project(sentBack, "sentback", "SB", "planning")
	ticket(sentBack, 1, "planning")
	plan(sentBack, "breakdown", 1)
	plan(sentBack, "sent_back", 1)

	project(writing, "writing", "WR", "planning")
	ticket(writing, 1, "planning")
	plan(writing, "breakdown", 1)
	plan(writing, "sent_back", 1)
	plan(writing, "breakdown", 2)
	exec(`INSERT INTO subtasks (scope, project, number, kind, role) VALUES ('project', $1, 1, 'breakdown', 'lead')`, writing)
	exec(`INSERT INTO runs (id, subtask, number, state, cli, crew_version, image)
		SELECT '00000000-0000-4000-8000-0000000000aa', id, 1, 'working', 'agy', 'v', 'img' FROM subtasks WHERE project = $1`, writing)

	project(atLimit, "atlimit", "AL", "paused")
	exec(`UPDATE projects SET pause_reason = 'the budget of $10 is spent' WHERE id = $1`, atLimit)
	exec(`INSERT INTO changes (project, who, tbl, row_key, after) VALUES ($1::uuid, 'crew', 'projects', $1::text, '{"state": "paused"}')`, atLimit)

	project(byYou, "byyou", "BY", "paused")
	exec(`INSERT INTO changes (project, who, tbl, row_key, after) VALUES ($1::uuid, 'crew', 'projects', $1::text, '{"state": "paused"}')`, byYou)
	exec(`INSERT INTO changes (project, who, tbl, row_key, after) VALUES ($1::uuid, 'operator', 'projects', $1::text, '{"state": "paused"}')`, byYou)

	exec(`INSERT INTO cli_logins (cli, state, result) VALUES ('agy', 'signed_out', 'the token was refused')
		ON CONFLICT (cli) DO UPDATE SET state = excluded.state, result = excluded.result`)

	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	if want := []string{"ok/waits", "paused/atlimit", "signin/agy"}; !slices.Equal(ids, want) {
		t.Fatalf("items %v, want %v", ids, want)
	}
	for _, it := range items {
		switch it.ID {
		case "ok/waits":
			if it.Body != "2 tickets wait in Planning. Nothing is built until you say OK." || it.Project != "waits" {
				t.Errorf("the OK item: %+v", it)
			}
			if a := it.Alternatives[len(it.Alternatives)-1]; a.Action != "/api/projects/waits/ok" || !a.Main {
				t.Errorf("the OK item's main alternative: %+v", a)
			}
		case "paused/atlimit":
			if it.Body != "the budget of $10 is spent. It starts nothing new; what runs finishes. Raise the limit, and it resumes by itself." {
				t.Errorf("the paused item: %q", it.Body)
			}
			// Raising the limit resumes it; a Resume while the limit is passed would pause it again (7.7).
			if a := it.Alternatives[len(it.Alternatives)-1]; a.Label != "Raise the limit" || a.Link != "/p/atlimit/settings" || !a.Main {
				t.Errorf("the paused item's main alternative: %+v", a)
			}
		case "signin/agy":
			if it.Project != "" || it.Alternatives[1].Link != "/setup" {
				t.Errorf("the sign-in item: %+v", it)
			}
		}
	}
	// Once the CLI is signed in again, it needs nobody.
	exec(`UPDATE cli_logins SET state = 'signed_in', result = 'ok' WHERE cli = 'agy'`)
	items, err = inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	ids = ids[:0]
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	if want := []string{"ok/waits", "paused/atlimit"}; !slices.Equal(ids, want) {
		t.Fatalf("signed in: items %v, want %v", ids, want)
	}
}

// An agent none of whose entries a CLI in use offers is what Crew could not do by itself: its runs
// wait, and the inbox says so until it has a model again (12.3, 7.8).
func TestAnAgentWithNoModelIsInTheInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	// With the machine's defaults every agent has an entry that is offered.
	if items, err := inbox.List(ctx, d.Server, machine(t, nil)); err != nil || slices.ContainsFunc(items, func(it inbox.Item) bool { return strings.HasPrefix(it.ID, "nomodel/") }) {
		t.Fatalf("%+v %v", items, err)
	}
	// A builder whose one entry is gemini-3.1-pro, which agy no longer lists, has none.
	only := machine(t, settings.Doc{"agents": settings.Doc{"roles": settings.Doc{"builder": settings.Doc{"model": []any{"agy/gemini-3.1-pro"}}}}})
	items, err := inbox.List(ctx, d.Server, only)
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(items, func(it inbox.Item) bool { return it.ID == "nomodel/builder" })
	if idx < 0 || items[idx].Kind != inbox.KindCrewCannot || items[idx].Alternatives[0].Link != "/models" {
		t.Fatalf("%+v", items)
	}
}

// A main the project terminal pushed waits for the operator, with its diff and the two answers, and
// goes when it was decided (13.11).
func TestAMainFromTheTerminalIsInTheInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	const shop = "00000000-0000-4000-8000-000000000001"
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `INSERT INTO terminal_mains (project, repo, old, new, files, pushed_by) VALUES ($1, 'shop', $2, $3, '{README.md}', 'terminal x')`,
		shop, strings.Repeat("a", 40), strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatalf("%+v %v", items, err)
	}
	it := itemOf(t, items, inbox.KindTerminalMain)
	if it == nil || it.Project != "shop" || !strings.Contains(it.Body, "1 file (README.md)") ||
		it.Alternatives[0].Link != "/p/shop/git?from="+strings.Repeat("a", 40)+"&to="+strings.Repeat("b", 40) ||
		!strings.HasSuffix(it.Alternatives[2].Action, "/take") || !it.Alternatives[2].Main {
		t.Fatalf("%+v", it)
	}
	if strings.Contains(it.Body, "code") {
		t.Fatalf("a main with no code files says it has some: %s", it.Body)
	}
	// A held main that changes files that are code names them first (13.8).
	d.Server.Exec(ctx, `UPDATE terminal_mains SET files = '{README.md,.github/workflows/ci.yml}', code = '{.github/workflows/ci.yml}'`)
	items, _ = inbox.List(ctx, d.Server, machine(t, nil))
	if it = itemOf(t, items, inbox.KindTerminalMain); it == nil ||
		!strings.HasPrefix(it.Body, "It changes files that are code for the agent CLI, or GitHub workflows: .github/workflows/ci.yml.") {
		t.Fatalf("a main with code files: %+v", items)
	}
	d.Server.Exec(ctx, `UPDATE terminal_mains SET state = 'refused'`)
	items, _ = inbox.List(ctx, d.Server, machine(t, nil))
	if it = itemOf(t, items, inbox.KindTerminalMain); it != nil {
		t.Fatalf("a decided main is still in the inbox: %+v", items)
	}
}

// The gate's crew-gate is Crew's own and hidden (16.2): what waits there is the gate's to report, so
// it is in no item of the inbox, also when Crew paused it at its budget or an agent created a ticket
// there that waits for a decision (M16's fourth phase).
func TestTheInboxLeavesOutTheGatesProject(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const gate = "00000000-0000-4000-8000-0000000000c1"
	exec(`INSERT INTO projects (id, name, key, state, repo_source, hidden, pause_reason)
		VALUES ($1, 'crew-gate', 'CGATE', 'paused', 'local', true, 'the budget of $2 is spent')`, gate)
	exec(`INSERT INTO changes (project, who, tbl, row_key, after) VALUES ($1::uuid, 'crew', 'projects', $1::text, '{"state": "paused"}')`, gate)
	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Project == "crew-gate" {
			t.Errorf("the gate's project is in the inbox: %+v", it)
		}
	}
	// The same project shown is in the inbox: the test reaches the item it leaves out.
	exec(`UPDATE projects SET hidden = false WHERE id = $1`, gate)
	if items, _ = inbox.List(ctx, d.Server, machine(t, nil)); !slices.ContainsFunc(items, func(it inbox.Item) bool { return it.Project == "crew-gate" }) {
		t.Fatalf("the project shown is not in the inbox either: %+v", items)
	}
}

// A ticket whose stuck line holds the guard's text for a check waiting on a model of a family Crew
// does not know is one crew_cannot item in the inbox, naming the model; with its stuck line cleared,
// or in a hidden project, there is none (12.3, 7.8, register row 17).
func TestACheckWaitingOnAFamilyCrewDoesNotKnowIsInTheInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const (
		shop  = "00000000-0000-4000-8000-000000000001"
		gate  = "00000000-0000-4000-8000-000000000002"
		stuck = "Review: the reviewer waits: nova-1 is of a family Crew does not know: a pin of the reviewer on a model of a known family (Gemini, Claude, GPT) lets it run: a check does not run on the builder's family"
	)
	exec(`INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, shop)
	exec(`INSERT INTO projects (id, name, key, state, repo_source, hidden) VALUES ($1, 'crew-gate', 'CGATE', 'running', 'local', true)`, gate)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, stuck)
		VALUES ($1, 7, 'Check order total', 'feature', 'review', 'plan', 'lead', 1, $2)`, shop, stuck)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, stuck)
		VALUES ($1, 1, 'Gate check', 'feature', 'review', 'plan', 'lead', 1, $2)`, gate, stuck)

	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(items, func(it inbox.Item) bool { return it.ID == "family/shop/SF-7" })
	if idx < 0 {
		t.Fatalf("items %+v, want family/shop/SF-7", items)
	}
	it := items[idx]
	if it.Kind != inbox.KindCrewCannot || it.Project != "shop" ||
		!strings.Contains(it.Title, "SF-7") || it.Body != stuck || !strings.Contains(it.Body, "nova-1") ||
		len(it.Alternatives) != 1 || it.Alternatives[0].Link != "/p/shop/tickets/SF-7" || !it.Alternatives[0].Main {
		t.Fatalf("unexpected item: %+v", it)
	}

	// With the stuck line cleared, there is no item.
	exec(`UPDATE tickets SET stuck = NULL WHERE project = $1 AND number = 7`, shop)
	if items, err = inbox.List(ctx, d.Server, machine(t, nil)); err != nil || slices.ContainsFunc(items, func(it inbox.Item) bool { return strings.HasPrefix(it.ID, "family/") }) {
		t.Fatalf("after clearing stuck: %+v %v", items, err)
	}
}

// Each model a CLI in use offers that no bracket of agents.brackets names is one crew_cannot item in
// the inbox, id nobracket/<cli>/<model>, until it is placed in a bracket or stops being offered (12.3,
// 7.8, register row 85).
func TestAModelInNoBracketIsInTheInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)

	// Standard models (including gpt-oss-120b in oss) are auto-bracketed and produce no inbox item.
	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil || len(items) != 0 {
		t.Fatalf("items %+v %v, want 0", items, err)
	}

	// An unclassifiable model (nova-1) emits nobracket/agy/nova-1 with Since = min(first_seen).
	firstSeen := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	laterRead := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	rows, err := models.Rows(ctx, d.Server)
	if err != nil {
		t.Fatal(err)
	}
	var list []cli.Model
	for _, r := range rows {
		list = append(list, cli.Model{Name: r.Name, Model: r.Model, Efforts: r.Efforts, InName: r.InName})
	}
	list = append(list, cli.Model{Name: "nova-1-medium", Model: "nova-1", Efforts: []string{"medium"}, InName: true})
	if err := models.Record(ctx, d.Server, "agy", list, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE models SET first_seen = $1, read_at = $2 WHERE model = 'nova-1'`, firstSeen, laterRead); err != nil {
		t.Fatal(err)
	}
	eff, err := settings.Effective(ctx, d.Server, settings.Machine{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	items, err = inbox.List(ctx, d.Server, eff)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %+v %v, want 1", items, err)
	}
	it := items[0]
	if it.ID != "nobracket/agy/nova-1" || it.Kind != inbox.KindCrewCannot || !it.Since.Equal(firstSeen) ||
		!strings.Contains(it.Title, "agy/nova-1") || !strings.Contains(it.Body, "nova-1") ||
		len(it.Alternatives) != 1 || it.Alternatives[0].Link != "/settings" || !it.Alternatives[0].Main {
		t.Fatalf("unexpected item: %+v (want Since %v)", it, firstSeen)
	}

	// Once placed in a bracket, the item goes away.
	placed := settings.Merge(eff, settings.Doc{"agents": settings.Doc{"brackets": settings.Doc{
		"custom": []any{"agy/nova-1"},
	}}})
	if items, err = inbox.List(ctx, d.Server, placed); err != nil || len(items) != 0 {
		t.Fatalf("after placing in bracket: %+v %v", items, err)
	}

	// If it stops being offered, the item also goes away.
	if _, err := d.Server.Exec(ctx, `UPDATE models SET offered = false WHERE model = 'nova-1'`); err != nil {
		t.Fatal(err)
	}
	if items, err = inbox.List(ctx, d.Server, eff); err != nil || len(items) != 0 {
		t.Fatalf("after unoffering: %+v %v", items, err)
	}
}

// A ticket of a non-hidden project whose stuck line holds the wait reason for a pinned/operator model
// waiting while a model of its bracket has room (models.WaitBracketHasRoom) is one crew_cannot item in
// the inbox; and when a check ran on the builder's family (check_rule_not_met), an open ticket gets a
// notice in the inbox too (12.3, 12.7, 7.8, register row 85).
func TestARunThatWaitsWhileItsBracketHasRoomIsInTheInbox(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	modelstest.Recorded(t, d.Server)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Server.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const (
		shop  = "00000000-0000-4000-8000-000000000001"
		gate  = "00000000-0000-4000-8000-000000000002"
		stuck = "In progress: the builder waits: agy's Claude and GPT models are at their limit; " + models.WaitBracketHasRoom + "agy/gemini-4-argon: pin agy/gemini-4-argon to run now"
	)
	exec(`INSERT INTO projects (id, name, key, state, repo_source) VALUES ($1, 'shop', 'SF', 'running', 'local')`, shop)
	exec(`INSERT INTO projects (id, name, key, state, repo_source, hidden) VALUES ($1, 'crew-gate', 'CGATE', 'running', 'local', true)`, gate)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, stuck)
		VALUES ($1, 3, 'Pin wait', 'feature', 'in_progress', 'plan', 'lead', 1, $2)`, shop, stuck)
	exec(`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, stuck)
		VALUES ($1, 1, 'Gate wait', 'feature', 'in_progress', 'plan', 'lead', 1, $2)`, gate, stuck)

	items, err := inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(items, func(it inbox.Item) bool { return it.ID == "bracket/shop/SF-3" })
	if idx < 0 {
		t.Fatalf("items %+v, want bracket/shop/SF-3", items)
	}
	it := items[idx]
	if it.Kind != inbox.KindCrewCannot || it.Project != "shop" ||
		!strings.Contains(it.Title, "SF-3") || it.Body != stuck || !strings.Contains(it.Body, "agy/gemini-4-argon") ||
		len(it.Alternatives) != 1 || it.Alternatives[0].Link != "/p/shop/tickets/SF-3" || !it.Alternatives[0].Main {
		t.Fatalf("unexpected item: %+v", it)
	}
	if slices.ContainsFunc(items, func(it inbox.Item) bool { return it.Project == "crew-gate" }) {
		t.Fatalf("hidden project in inbox: %+v", items)
	}

	// Clearing the stuck line removes the item.
	exec(`UPDATE tickets SET stuck = NULL WHERE project = $1 AND number = 3`, shop)
	if items, err = inbox.List(ctx, d.Server, machine(t, nil)); err != nil || slices.ContainsFunc(items, func(it inbox.Item) bool { return strings.HasPrefix(it.ID, "bracket/") }) {
		t.Fatalf("after clearing stuck: %+v %v", items, err)
	}

	// When a check ran on the builder's family (check_rule_not_met), the inbox shows a clear notice on the open ticket.
	var tid, sub int64
	d.Server.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND number = 3`, shop).Scan(&tid)
	d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state) VALUES ('ticket', $1, $2, 1, 'review', 'reviewer', 'done') RETURNING id`, shop, tid).Scan(&sub)
	const runID = "00000000-0000-4000-8000-0000000000ab"
	exec(`INSERT INTO runs (id, subtask, number, state, cli, model, crew_version, image) VALUES ($1, $2, 1, 'ended', 'agy', 'gemini-4-argon', 'v', 'img')`, runID, sub)
	exec(`INSERT INTO signals (project, ticket, run, kind, detail) VALUES ($1, $2, $3, 'check_rule_not_met', '{"role": "reviewer", "family": "gemini"}')`, shop, tid, runID)

	items, err = inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	idx = slices.IndexFunc(items, func(it inbox.Item) bool { return it.ID == "checkfamily/shop/SF-3/reviewer" })
	if idx < 0 || !strings.Contains(items[idx].Body, "gemini-4-argon (gemini family), the same family as the builder") {
		t.Fatalf("expected checkfamily notice in inbox, got %+v", items)
	}

	// A diverged pull request is in the inbox while the ticket is open, and omitted once the ticket is done or closed.
	exec(`INSERT INTO pull_requests (ticket, project, repo, branch, state, diverged)
		VALUES ($1, $2, 'o/r', 'crew/SF-3', 'open', '222222222222')`, tid, shop)
	items, err = inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil || !slices.ContainsFunc(items, func(it inbox.Item) bool { return it.ID == "diverged/shop/SF-3" }) {
		t.Fatalf("expected diverged/shop/SF-3 on open ticket, got %+v %v", items, err)
	}
	exec(`UPDATE tickets SET stage = 'done' WHERE id = $1`, tid)
	items, err = inbox.List(ctx, d.Server, machine(t, nil))
	if err != nil || slices.ContainsFunc(items, func(it inbox.Item) bool { return it.ID == "diverged/shop/SF-3" }) {
		t.Fatalf("diverged item remained on done ticket: %+v %v", items, err)
	}
}
