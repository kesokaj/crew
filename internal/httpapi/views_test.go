package httpapi_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// theScreensShowTheTicket reads, through the routes of the web UI (M7), the ticket that
// TestATicketGoesFromBacklogToDoneWithNoOperator took from Backlog to Done, merged as main.
func theScreensShowTheTicket(t *testing.T, k *ticketKit, main string) {
	t.Helper()
	// The activity feed: one line per move, per run that started and ended, per check run, per
	// decision; newest first; the same lines for the project and across all projects.
	var feed struct {
		Lines []struct {
			Kind, Project, Text string
			Ticket              *string
		}
	}
	k.call("GET", "/api/projects/shop/activity", "", 200, &feed)
	kinds := map[string]int{}
	var texts []string
	for _, l := range feed.Lines {
		kinds[l.Kind]++
		texts = append(texts, l.Text)
		if l.Project != "shop" {
			t.Errorf("a line of another project: %+v", l)
		}
	}
	for _, want := range []string{
		"SF-1 moved from Test to Done",
		"SF-1 moved from Backlog to Analysis",
		"Builder started SF-1.2",
		"Builder finished SF-1.2",
		"The checks on top of main of SF-1 passed",
		"The base check of SF-1 failed",
		"The curator decided: Count failures per account or per address? → account",
	} {
		if !slices.ContainsFunc(texts, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("no line %q in the feed:\n%s", want, strings.Join(texts, "\n"))
		}
	}
	if kinds["move"] != 6 || kinds["check"] < 5 || kinds["decision"] != 1 {
		t.Errorf("the feed's kinds: %v", kinds)
	}
	if !strings.HasPrefix(feed.Lines[len(feed.Lines)-1].Text, "You created SF-1 Lock account") {
		t.Errorf("the oldest line is %q", feed.Lines[len(feed.Lines)-1].Text)
	}
	var all struct{ Lines []struct{ Text string } }
	k.call("GET", "/api/activity", "", 200, &all)
	if len(all.Lines) != len(feed.Lines) {
		t.Errorf("across all projects %d lines, in the project %d", len(all.Lines), len(feed.Lines))
	}

	// The frame and the front page: the project, its done ticket, and no agent at work. agy says no
	// cost in dollars, so the project's spending is not checked.
	var f struct {
		Version   string
		LeadSlots []struct {
			CLI         string
			Used, Slots int
		}
		Inbox    int
		Projects []struct {
			Name              string
			NeedsYou, Working int
		}
	}
	k.call("GET", "/api/frame", "", 200, &f)
	if f.Inbox != 0 || len(f.LeadSlots) != 1 || f.LeadSlots[0].CLI != "agy" || f.LeadSlots[0].Slots != 8 || f.LeadSlots[0].Used != 0 {
		t.Errorf("the frame: %+v", f)
	}
	if !slices.ContainsFunc(f.Projects, func(p struct {
		Name              string
		NeedsYou, Working int
	}) bool {
		return p.Name == "shop" && p.Working == 0
	}) {
		t.Errorf("the frame's projects: %+v", f.Projects)
	}
	var o struct {
		Projects []struct {
			Name    string
			ByStage map[string]int
		}
	}
	k.call("GET", "/api/overview", "", 200, &o)
	for _, p := range o.Projects {
		if p.Name == "shop" && p.ByStage["done"] != 1 {
			t.Errorf("the overview of shop: %+v", p)
		}
	}

	// The board: the done ticket in Done, the stages with the ones that are off, and a card's agents,
	// steps, and stuck, read while a run of it works.
	ctx := context.Background()
	if _, err := k.d.Server.Exec(ctx, `UPDATE runs SET state = 'working' WHERE id = (SELECT r.id FROM runs r
		JOIN subtasks s ON s.id = r.subtask WHERE s.kind = 'build' ORDER BY r.number DESC LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.d.Server.Exec(ctx, `UPDATE tickets SET stuck = 'merge errors three times' WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	type card struct {
		Key, Stuck string
		Agents     []struct {
			Role, CLI, State string
			StepsDone, Steps int
		}
	}
	var bv struct {
		Columns []struct {
			ID      string
			Tickets []card
		}
		Stages []struct {
			ID           string
			Optional, On bool
		}
	}
	k.call("GET", "/api/projects/shop/board", "", 200, &bv)
	var done []card
	for _, c := range bv.Columns {
		if c.ID == "done" {
			done = c.Tickets
		}
	}
	if len(done) != 1 || done[0].Key != "SF-1" || done[0].Stuck != "merge errors three times" || len(done[0].Agents) != 1 {
		t.Fatalf("the Done column: %+v", done)
	}
	if a := done[0].Agents[0]; a.Role != "builder" || a.State != "working" || a.Steps != 3 || a.StepsDone != 1 {
		t.Errorf("the card's agent: %+v", a)
	}
	var off []string
	for _, st := range bv.Stages {
		if !st.On {
			off = append(off, st.ID)
		}
	}
	if !slices.Equal(off, []string{"e2e", "verification"}) || len(bv.Stages) != 9 {
		t.Errorf("the stages: %+v", bv.Stages)
	}
	k.d.Server.Exec(ctx, `UPDATE runs SET state = 'ended' WHERE state = 'working'`)
	k.d.Server.Exec(ctx, `UPDATE tickets SET stuck = NULL`)

	// The ticket's git: its branch, the merge, its commits by the subtask that pushed them, and what it
	// changed on main, file by file.
	var g struct {
		Branch   string
		Pushed   bool
		MergedAs string
		Groups   []struct {
			Subtask, Role, From, To string
			Commits                 []struct{ ID, Subject string }
		}
		Other []struct{ ID string }
		Files []struct {
			Path           string
			Added, Removed int
			Patch          string
		}
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &g)
	if g.Branch != "task/SF-1-lock-account" || !g.Pushed || g.MergedAs != main || len(g.Other) != 0 {
		t.Errorf("the ticket's git: %+v", g)
	}
	var groups []string
	for _, x := range g.Groups {
		var subjects []string
		for _, c := range x.Commits {
			subjects = append(subjects, c.Subject)
		}
		groups = append(groups, x.Subtask+" "+x.Role+": "+strings.Join(subjects, ", "))
	}
	if want := []string{"SF-1.2 builder: Add lock_test.go, Add lock.go", "SF-1.4 builder: Add NOTES.md"}; !slices.Equal(groups, want) {
		t.Errorf("the commits by subtask: %q", groups)
	}
	var files []string
	for _, f := range g.Files {
		files = append(files, f.Path)
		// The changelog's entry is Crew's own, written into the merge commit (9.9).
		if (f.Added != 1 && f.Path != "CHANGELOG.md") || !strings.Contains(f.Patch, "+++ b/"+f.Path) {
			t.Errorf("the diff of %s: %+v", f.Path, f)
		}
	}
	if !slices.Equal(files, []string{"CHANGELOG.md", "NOTES.md", "lock.go", "lock_test.go"}) {
		t.Errorf("the files: %v", files)
	}
	// The diff of only one subtask's run is a comparison of its first and its last push.
	var only struct{ Files []struct{ Path string } }
	k.call("GET", "/api/projects/shop/git/compare?from="+g.Groups[1].From+"&to="+g.Groups[1].To, "", 200, &only)
	if len(only.Files) != 1 || only.Files[0].Path != "NOTES.md" {
		t.Errorf("the fix round's own diff: %+v", only)
	}

	// The prompt of a run in its parts, each with its source and its text, which add up to the prompt.
	var subs struct {
		Subtasks []struct {
			ID   string
			Runs []struct{ ID string }
		}
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/subtasks", "", 200, &subs)
	var run struct {
		Prompt string
		Parts  []struct{ Part, Source, Text string }
	}
	k.call("GET", "/api/runs/"+subs.Subtasks[1].Runs[0].ID, "", 200, &run)
	var joined []string
	var names []string
	for _, p := range run.Parts {
		joined = append(joined, p.Text)
		names = append(names, p.Part)
	}
	if len(run.Parts) < 3 || strings.Join(joined, "\n\n") != run.Prompt || !slices.Contains(names, "ticket") {
		t.Errorf("the builder's prompt in parts: %q", names)
	}

	// The last lines of a run's log: the newest events, oldest first.
	var all2, tail struct{ Events []struct{ Seq int64 } }
	id := subs.Subtasks[1].Runs[0].ID
	k.call("GET", "/api/runs/"+id+"/events?limit=1000", "", 200, &all2)
	k.call("GET", "/api/runs/"+id+"/events?tail=2", "", 200, &tail)
	if n := len(all2.Events); n < 3 || len(tail.Events) != 2 || tail.Events[1].Seq != all2.Events[n-1].Seq || tail.Events[0].Seq != all2.Events[n-2].Seq {
		t.Errorf("the tail %+v of %d events", tail, len(all2.Events))
	}
	// The ticket's links back: none link to SF-1.
	var tv struct{ LinkedFrom []any }
	k.call("GET", "/api/projects/shop/tasks/SF-1", "", 200, &tv)
	if tv.LinkedFrom == nil || len(tv.LinkedFrom) != 0 {
		t.Errorf("links back: %+v", tv.LinkedFrom)
	}

	// A subtask's page: where its run belongs, and what only that run pushed.
	var fix struct {
		Role, Kind, Project, Ticket string
		SubtaskNumber               int
		Push                        bool
	}
	fixRun := subs.Subtasks[3].Runs[0].ID
	k.call("GET", "/api/runs/"+fixRun, "", 200, &fix)
	if fix.Role != "builder" || fix.Kind != "fix" || fix.Project != "shop" || fix.Ticket != "SF-1" || fix.SubtaskNumber != 4 || !fix.Push {
		t.Errorf("the fix round's run: %+v", fix)
	}
	var rg struct {
		Commits []struct{ Subject string }
		Files   []struct{ Path string }
	}
	k.call("GET", "/api/runs/"+fixRun+"/git", "", 200, &rg)
	if len(rg.Commits) != 1 || rg.Commits[0].Subject != "Add NOTES.md" || len(rg.Files) != 1 || rg.Files[0].Path != "NOTES.md" {
		t.Errorf("what the fix round pushed: %+v", rg)
	}
	k.call("GET", "/api/runs/"+subs.Subtasks[2].Runs[0].ID+"/git", "", 200, &rg)
	if len(rg.Commits) != 0 || len(rg.Files) != 0 {
		t.Errorf("a reviewer pushed: %+v", rg)
	}

	// The Trajectories screen: every run of the project, newest first, by role, and searched.
	var tr struct {
		Runs []struct {
			Subtask, Role, State string
			Events               int64
		}
		Events int64
	}
	k.call("GET", "/api/projects/shop/runs", "", 200, &tr)
	// Seven runs of the ticket, and the curator's retrospective and final report of the project (7.2, 9.8).
	reports := 0
	for _, r := range tr.Runs {
		if r.Role == "curator" && !strings.HasPrefix(r.Subtask, "SF-") {
			reports++
		}
	}
	if len(tr.Runs) != 9 || reports != 2 || tr.Runs[len(tr.Runs)-1].Subtask != "SF-1.1" || tr.Runs[len(tr.Runs)-1].Role != "curator" || tr.Events == 0 {
		t.Errorf("the runs: %+v", tr)
	}
	k.call("GET", "/api/projects/shop/runs?role=reviewer", "", 200, &tr)
	if len(tr.Runs) != 2 || tr.Runs[0].Subtask != "SF-1.5" || tr.Runs[1].Subtask != "SF-1.3" {
		t.Errorf("the reviewer's runs: %+v", tr.Runs)
	}
	k.call("GET", "/api/projects/shop/runs?q=summary", "", 200, &tr)
	if len(tr.Runs) != 1 || tr.Runs[0].Role != "summary" {
		t.Errorf("a search: %+v", tr.Runs)
	}

	// The Database screen: the version, the groups with their rows, the last schema step, the changes.
	var dbv struct {
		Version string
		Groups  []struct {
			Name   string
			Tables map[string]int64
		}
		LastStep int
		Backups  []string
		Changes  []struct{ Table string }
	}
	k.call("GET", "/api/database", "", 200, &dbv)
	byGroup := map[string]map[string]int64{}
	for _, g := range dbv.Groups {
		byGroup[g.Name] = g.Tables
	}
	var lastStep int
	k.d.Server.QueryRow(ctx, `SELECT max(number) FROM schema_steps`).Scan(&lastStep)
	if dbv.Version == "" || lastStep < 12 || dbv.LastStep != lastStep || len(dbv.Changes) == 0 || dbv.Backups == nil {
		t.Errorf("the database: %+v", dbv)
	}
	if _, ok := byGroup["The board"]["tickets"]; !ok || byGroup["Runs"]["ref_pushes"] < 0 {
		t.Errorf("the groups: %+v", byGroup)
	}
	if _, ok := byGroup["Conversations and trajectories"]["events"]; !ok {
		t.Errorf("the events are not one table: %+v", byGroup)
	}
	if _, ok := byGroup["Other"]; ok {
		t.Errorf("a table in no group of 15.3: %+v", byGroup["Other"])
	}
	for name, ts := range byGroup {
		for tb := range ts {
			if strings.HasPrefix(tb, "events_") {
				t.Errorf("a month of events listed as a table in %s", name)
			}
		}
	}

	// The Git screen: main first, the ticket's branch with its ticket, and every branch's history.
	var gb struct {
		Branches []struct {
			Name   string
			Ticket *string
			Ahead  int
		}
	}
	k.call("GET", "/api/projects/shop/git/branches", "", 200, &gb)
	if len(gb.Branches) != 2 || gb.Branches[0].Name != "main" || gb.Branches[1].Ticket == nil || *gb.Branches[1].Ticket != "SF-1" || gb.Branches[1].Ahead != 0 {
		t.Errorf("the branches: %+v", gb)
	}
	var gl struct {
		Commits []struct{ ID, Subject, Ref string }
	}
	k.call("GET", "/api/projects/shop/git/log?ref=main&limit=1", "", 200, &gl)
	if len(gl.Commits) != 1 || gl.Commits[0].ID != main {
		t.Errorf("main's history: %+v", gl)
	}
	k.call("GET", "/api/projects/shop/git/log?ref=all", "", 200, &gl)
	if len(gl.Commits) < 4 {
		t.Errorf("every branch's history: %+v", gl)
	}
	k.call("GET", "/api/projects/shop/git/log?ref=--all", "", 404, nil)
	var gc struct {
		Commit struct{ ID string }
		Files  []struct{ Path string }
	}
	k.call("GET", "/api/projects/shop/git/commits/"+main, "", 200, &gc)
	if gc.Commit.ID != main || len(gc.Files) != 4 {
		t.Errorf("the merge commit: %+v", gc)
	}

	// Decisions: the operator changes one, and a ticket carries the change out, created from the
	// ticket it was made on. A decision is changed once, to something else, by one line.
	var ds struct {
		Decisions []struct {
			ID                      int64
			Choice                  string
			ChangedTo, ChangeTicket *string
		}
	}
	k.call("GET", "/api/projects/shop/decisions", "", 200, &ds)
	path := fmt.Sprintf("/api/projects/shop/decisions/%d/change", ds.Decisions[0].ID)
	k.call("POST", path, `{"choice": "account"}`, 400, nil)
	k.call("POST", path, `{"choice": ""}`, 400, nil)
	k.call("POST", path, `{"choice": "two\nlines"}`, 400, nil)
	k.call("POST", "/api/projects/shop/decisions/999999/change", `{"choice": "address"}`, 404, nil)
	var ct struct {
		Key, Stage, Origin, Title, Why, What, Goal string
		Links                                      []struct{ Kind, To string }
	}
	k.call("POST", path, `{"choice": "address"}`, 201, &ct)
	if ct.Key != "SF-2" || ct.Stage != "backlog" || ct.Origin != "operator" || !strings.Contains(ct.What, `Use "address" in place of the curator's choice`) ||
		len(ct.Links) != 1 || ct.Links[0].Kind != "created_from" || ct.Links[0].To != "SF-1" {
		t.Errorf("the ticket that carries the change out: %+v", ct)
	}
	// The operator's ticket holds none of what the agent wrote; that is a note of the curator's on it,
	// which the next agents read as the curator's (security review of M7).
	for _, field := range []string{ct.Title, ct.Why, ct.What, ct.Goal} {
		if strings.Contains(field, "Count failures") || strings.Contains(field, "account") {
			t.Errorf("the agent's words in the operator's ticket: %q", field)
		}
	}
	var sf2 struct {
		Records []struct{ Kind, Author, Body string }
	}
	k.call("GET", "/api/projects/shop/tasks/SF-2", "", 200, &sf2)
	if len(sf2.Records) != 1 || sf2.Records[0].Kind != "note" || sf2.Records[0].Author != "crew" ||
		!strings.Contains(sf2.Records[0].Body, "Count failures per account or per address?") || !strings.Contains(sf2.Records[0].Body, "The curator chose: account") {
		t.Errorf("the decision's note: %+v", sf2.Records)
	}
	// A builder's decision gives its ticket no note of the builder's, so the ticket's note_exists
	// passes only on what its own builder writes (round two of the review).
	var bd int64
	if err := k.d.Server.QueryRow(ctx, `INSERT INTO decisions (project, ticket, role, question, alternatives, choice, reason)
		SELECT project, id, 'builder', 'Tabs or spaces?', '["tabs", "spaces"]', 'tabs', 'gofmt' FROM tickets WHERE number = 1 RETURNING id`).Scan(&bd); err != nil {
		t.Fatal(err)
	}
	var sf3 struct{ Key string }
	k.call("POST", fmt.Sprintf("/api/projects/shop/decisions/%d/change", bd), `{"choice": "spaces"}`, 201, &sf3)
	var builderNotes int
	k.d.Server.QueryRow(ctx, `SELECT count(*) FROM records r JOIN tickets t ON t.id = r.ticket WHERE t.number = 3 AND r.kind = 'note' AND r.author = 'builder'`).Scan(&builderNotes)
	if sf3.Key != "SF-3" || builderNotes != 0 {
		t.Errorf("a builder's decision gave %s %d notes of the builder's", sf3.Key, builderNotes)
	}
	lead := ds.Decisions[0].ID
	k.call("GET", "/api/projects/shop/decisions", "", 200, &ds)
	var d struct {
		ID                      int64
		Choice                  string
		ChangedTo, ChangeTicket *string
	}
	for _, x := range ds.Decisions {
		if x.ID == lead {
			d = x
		}
	}
	if d.ChangedTo == nil || *d.ChangedTo != "address" || d.ChangeTicket == nil || *d.ChangeTicket != "SF-2" || d.Choice != "account" {
		t.Errorf("the changed decision: %+v", d)
	}
	k.call("POST", path, `{"choice": "both"}`, 400, nil)

	// A branch with more pushes than a view reads shows the newest: the oldest are what is cut.
	if _, err := k.d.Server.Exec(ctx, `INSERT INTO ref_pushes (repo, run, who, ref, old, new)
		SELECT repo, run, who, ref, old, new FROM (SELECT * FROM ref_pushes WHERE ref = 'refs/heads/task/SF-1-lock-account' ORDER BY id LIMIT 1) p,
			generate_series(1, 500)`); err != nil {
		t.Fatal(err)
	}
	var newest struct {
		Groups []struct{ Subtask string }
	}
	k.call("GET", "/api/projects/shop/tasks/SF-1/git", "", 200, &newest)
	for _, g := range newest.Groups {
		if g.Subtask == "SF-1.4" {
			t.Errorf("the oldest pushes were read, not the newest: %+v", newest.Groups)
		}
	}
}
