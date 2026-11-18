package board_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/settings"
)

// A project has its rows in the database and its code in a repository (6.6, R39): the project, the
// original plan, the settings with the optional stages the plan asked for, and a first commit on
// main in the canonical and the quarantine repository.
func TestAProjectHasItsRowsAndItsRepository(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", "review", "e2e")
	if p.State != "planning" || p.Key != "SF" {
		t.Fatalf("project = %+v", p)
	}
	pl, err := e.b.Plan(e.ctx, p)
	if err != nil || pl.Original != plan {
		t.Fatalf("plan = %+v (%v)", pl, err)
	}
	doc, err := e.b.Settings(e.ctx, e.db.Server, p)
	if err != nil {
		t.Fatal(err)
	}
	var on []string
	for _, s := range board.PipelineOf(doc).Columns() {
		on = append(on, s.ID)
	}
	if got := strings.Join(on, " "); got != "planning backlog analysis in_progress review e2e done" {
		t.Fatalf("the board's columns: %s", got)
	}
	main := e.git.Main(e.ctx, "shop")
	if main == "" || e.git.Quarantine.Ref(e.ctx, "shop", "refs/heads/main") != main {
		t.Fatal("main is not in both repositories")
	}
	if got := git(t, e.git.Canonical("shop"), "show", "main:README.md"); !strings.HasPrefix(got, "# shop") {
		t.Fatalf("README.md = %q", got)
	}
	if e.changes(p) < 2 {
		t.Fatal("the project and its settings have no history")
	}

	for name, n := range map[string]board.NewProject{
		"the name taken":   {Name: "shop", Key: "XX", Plan: plan, Repo: board.Repo{Kind: "local"}},
		"the key taken":    {Name: "other", Key: "SF", Plan: plan, Repo: board.Repo{Kind: "local"}},
		"a bad name":       {Name: "Shop!", Key: "XX", Plan: plan, Repo: board.Repo{Kind: "local"}},
		"a bad key":        {Name: "other", Key: "sf", Plan: plan, Repo: board.Repo{Kind: "local"}},
		"no plan":          {Name: "other", Key: "XX", Repo: board.Repo{Kind: "local"}},
		"no repository":    {Name: "other", Key: "XX", Plan: plan},
		"github, for now":  {Name: "other", Key: "XX", Plan: plan, Repo: board.Repo{Kind: "github"}},
		"a URL with a key": {Name: "other", Key: "XX", Plan: plan, Repo: board.Repo{Kind: "existing", URL: "https://u:p@example.com/r.git"}},
		"a stage unknown":  {Name: "other", Key: "XX", Plan: plan, Repo: board.Repo{Kind: "local"}, Stages: []string{"security"}},
	} {
		if _, err := e.b.CreateProject(e.ctx, n, board.Operator); err == nil {
			t.Errorf("%s: created", name)
		}
	}
	if e.git.Exists("other") {
		t.Fatal("a refused project left a repository")
	}
}

// When the rows cannot be written after the repositories were made, the repositories go again: no
// repository without its project.
func TestAProjectWhoseRowsFailLeavesNoRepository(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO git_repos (name) VALUES ('orphan')`); err != nil {
		t.Fatal(err)
	}
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "orphan", Key: "OR", Plan: plan, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err == nil {
		t.Fatal("created")
	}
	if e.git.Exists("orphan") {
		t.Fatal("the repositories were left")
	}
	var n int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM projects WHERE name = 'orphan'`).Scan(&n)
	if n != 0 {
		t.Fatal("the project's row was left")
	}
}

// An existing repository is cloned with its history (6.6).
func TestAProjectFromAnExistingRepository(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	git(t, src, "init", "-q", "--initial-branch=main")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "two")
	var asked string
	e.b.ReadHeader = func(_ context.Context, url string) (string, func(), error) {
		asked = url
		return "", func() {}, nil
	}
	// The address is checked as https first, so the test's file address goes to the store directly.
	if err := e.git.CreateFrom(e.ctx, "old", "file://"+src, ""); err != nil {
		t.Fatal(err)
	}
	if got := git(t, e.git.Canonical("old"), "rev-list", "--count", "main"); got != "2" {
		t.Fatalf("main has %s commits", got)
	}
	_, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "remote", Key: "RM", Plan: plan,
		Repo: board.Repo{Kind: "existing", URL: "https://example.invalid/owner/repo.git"}}, board.Operator)
	if err == nil || asked != "https://example.invalid/owner/repo.git" {
		t.Fatalf("a repository that cannot be reached: %v, header asked for %q", err, asked)
	}
	if e.git.Exists("remote") {
		t.Fatal("a failed clone left a repository")
	}
}

// Tickets get the project's key and a counter (7.12). Before the OK they wait in Planning; the OK
// moves every one to Backlog and the project runs (7.2, step 5).
func TestTheProjectOK(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	if _, err := e.b.OK(e.ctx, p.Name, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("an OK with no ticket: %v", err)
	}
	for _, title := range []string{"Sign-in form", "Lockout", "Reset"} {
		e.add(p, ready(title), board.OriginPlan)
	}
	if got := e.ticket(p, "SF-2"); got.Stage != "planning" || got.Key != "SF-2" || got.Analysis.Status != board.StatusWaiting {
		t.Fatalf("SF-2 = %s %s %s", got.Key, got.Stage, got.Analysis.Status)
	}
	if err := e.b.SendBack(e.ctx, p.Name, "Split the sign-in form.", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-1") != "planning" {
		t.Fatal("a ticket moved before the OK")
	}
	e.ok(p)
	for _, k := range []string{"SF-1", "SF-2", "SF-3"} {
		if mv := e.moves(e.ticket(p, k)); !slices.Equal(mv, []string{"planning>backlog"}) {
			t.Fatalf("%s moved %v", k, mv)
		}
	}
	p, _ = e.b.Project(e.ctx, p.Name)
	if p.State != "running" || p.OKAt == nil {
		t.Fatalf("after the OK: %+v", p)
	}
	if _, err := e.b.OK(e.ctx, p.Name, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a second OK: %v", err)
	}
	if err := e.b.SendBack(e.ctx, p.Name, "late", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("sent back after the OK: %v", err)
	}
	pl, _ := e.b.Plan(e.ctx, p)
	if len(pl.SentBack) != 1 {
		t.Fatalf("sent back = %v", pl.SentBack)
	}
	// After the OK the operator's own ticket goes to the top of the backlog (7.9).
	mine := e.add(p, ready("Mine"), board.OriginOperator)
	all, _ := e.b.Tickets(e.ctx, p)
	if all[0].Key != mine.Key || mine.Stage != "backlog" {
		t.Fatalf("the operator's ticket is %s in %s, first is %s", mine.Key, mine.Stage, all[0].Key)
	}
}

// A ticket goes from Planning to Done through every stage that is on, each move only when the checks
// of its stage pass (7.1, 7.4). The checks of M6 are scripted here; the rest are the board's own.
func TestATicketMovesThroughEveryStage(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", "review", "test", "e2e", "verification")
	n := ready("Lock account")
	n.Checks = nil
	e.add(p, n, board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	if tk.Stage != "analysis" || tk.Waiting == nil || *tk.Waiting != "waiting for analysis" {
		t.Fatalf("SF-1 is in %s, waiting %v", tk.Stage, tk.Waiting)
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "approve"}, "lead"); kindOf(err) != board.Refused {
		t.Fatalf("approved without a check: %v", err)
	}
	e.approve(p, "SF-1", board.Decision{Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./auth"}},
		Docs: []string{"docs/auth.md"}, Approach: ptr("A counter on the account.")})

	// Without the records and the check sandbox of M6 it goes as far as its branch, and says why.
	e.reconcile(p)
	tk = e.ticket(p, "SF-1")
	if tk.Stage != "in_progress" || tk.Branch == nil || *tk.Branch != "task/SF-1-lock-account" {
		t.Fatalf("SF-1 is in %s on %v", tk.Stage, tk.Branch)
	}
	if git(t, e.git.Canonical("shop"), "rev-parse", "refs/heads/"+*tk.Branch) != e.git.Main(e.ctx, "shop") {
		t.Fatal("the branch was not cut from main")
	}
	if *tk.Waiting != "its branch is not pushed" {
		t.Fatalf("waiting: %s", *tk.Waiting)
	}
	e.push(p, *tk.Branch, map[string]string{"auth.go": "package auth\n"})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "the builder wrote no note" {
		t.Fatalf("waiting: %s", w)
	}

	e.b.Checks = scripted()
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; w != "not changed on its branch: docs/auth.md" {
		t.Fatalf("waiting: %s", w)
	}
	e.push(p, *tk.Branch, map[string]string{"docs/auth.md": "# Auth\n"})
	e.reconcile(p)
	tk = e.ticket(p, "SF-1")
	want := []string{"planning>backlog", "backlog>analysis", "analysis>in_progress", "in_progress>review",
		"review>test", "test>e2e", "e2e>verification", "verification>done"}
	if got := e.moves(tk); !slices.Equal(got, want) {
		t.Fatalf("moves = %v", got)
	}
	// Each move is saved with the checks that let it pass (6.4).
	var results []string
	rows, _ := e.db.Server.Query(e.ctx, `SELECT r.stage || ':' || r.name FROM check_results r JOIN stage_moves m ON m.id = r.move
		WHERE r.ticket = (SELECT ticket FROM stage_moves WHERE id = r.move) AND r.passed ORDER BY r.id`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		results = append(results, s)
	}
	rows.Close()
	if !slices.Contains(results, "in_progress:docs_done") || !slices.Contains(results, "analysis:analysis_approved") ||
		!slices.Contains(results, "verification:merges_and_passes_on_main") {
		t.Fatalf("check results = %v", results)
	}
	p, _ = e.b.Project(e.ctx, p.Name)
	if p.State != "done" {
		t.Fatalf("the project is %s with its only ticket done", p.State)
	}
	// Its Done is a change of its row, which the mail's news reads (18.1, M18 D15).
	var done int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM changes WHERE tbl = 'projects' AND project = $1 AND after->>'state' = 'done'`, p.ID).Scan(&done)
	if done != 1 {
		t.Fatalf("%d changes say the project is Done", done)
	}
	// A ticket added to a Done project makes it run again (7.2).
	e.add(p, ready("More"), board.OriginOperator)
	if p, _ = e.b.Project(e.ctx, p.Name); p.State != "running" {
		t.Fatalf("after a new ticket the project is %s", p.State)
	}
	// Done does not leave (7.4).
	if _, err := e.b.Discard(e.ctx, p, "SF-1", "no", board.Operator); kindOf(err) != board.Refused || err.Error() != "SF-1 is done: a change is a new ticket" {
		t.Fatalf("a done ticket was discarded: %v", err)
	}
}

// The stages of a board, every pair: a ticket only moves to the right, or to closed, and never out of
// Done or Closed (R179).
func TestAMoveOnlyGoesToTheRight(t *testing.T) {
	d, _ := settings.ProjectDefaults("shop", "SF")
	pipe := board.PipelineOf(d)
	ids := []string{"planning", "backlog", "analysis", "in_progress", "review", "test", "e2e", "verification", "done", "closed"}
	for i, from := range ids {
		for j, to := range ids {
			want := from != "done" && from != "closed" && (to == "closed" || j > i)
			if got := pipe.Right(from, to); got != want {
				t.Errorf("Right(%s, %s) = %v", from, to, got)
			}
		}
	}
	// Next skips the stages that are off and those the ticket skips.
	for _, c := range []struct {
		from  string
		skips []string
		want  string
	}{
		{"in_progress", nil, "review"},
		{"in_progress", []string{"review"}, "test"},
		{"review", []string{"test"}, "done"},
		{"test", nil, "done"},
		{"analysis", nil, "in_progress"},
	} {
		if got, _ := pipe.Next(c.from, c.skips); got.ID != c.want {
			t.Errorf("Next(%s, %v) = %s, want %s", c.from, c.skips, got.ID, c.want)
		}
	}
	if _, ok := pipe.Next("done", nil); ok {
		t.Error("Done has a next stage")
	}
}

// The WIP limit of a stage holds: the ticket behind waits in its stage and says why (7.4). Waiting
// tickets do not fill Analysis: a skipped one, a new feature in its notice, and a ready one that
// waits only for a dependency.
func TestStageLimits(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	stages := doc["stages"].([]any)
	if wip, _ := stages[2].(settings.Doc)["wip"].(int); wip != 4 {
		t.Fatalf("default Analysis wip = %d, want 4", wip)
	}
	stages[2].(settings.Doc)["wip"] = 2
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		e.add(p, ready(title), board.OriginPlan)
	}
	e.ok(p)
	e.reconcile(p)
	got := []string{e.stage(p, "SF-1"), e.stage(p, "SF-2"), e.stage(p, "SF-3")}
	if !slices.Equal(got, []string{"analysis", "analysis", "backlog"}) {
		t.Fatalf("stages = %v", got)
	}
	if w := *e.ticket(p, "SF-3").Waiting; w != "Analysis is full (2 of 2)" {
		t.Fatalf("SF-3 waits: %s", w)
	}
	// A skipped ticket stays in Analysis and leaves its place.
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "skip", Reason: "Not needed before Reset.",
		LookAgain: &board.LookAgain{Kind: "date", Value: "2026-11-01"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-3") != "analysis" || e.stage(p, "SF-4") != "backlog" {
		t.Fatalf("after the skip: SF-3 %s, SF-4 %s", e.stage(p, "SF-3"), e.stage(p, "SF-4"))
	}
	// A ready ticket that waits only for a dependency leaves its place too.
	if err := e.b.Link(e.ctx, p, "SF-2", "SF-3", "depends_on", "lead"); err != nil {
		t.Fatal(err)
	}
	e.approve(p, "SF-2", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if e.stage(p, "SF-2") != "analysis" || e.stage(p, "SF-4") != "analysis" {
		t.Fatalf("SF-2 %s, SF-4 %s", e.stage(p, "SF-2"), e.stage(p, "SF-4"))
	}
	if w := *e.ticket(p, "SF-2").Waiting; w != "waits for SF-3" {
		t.Fatalf("SF-2 waits: %s", w)
	}
	// In progress holds six by default; with wip=1 the second ready ticket waits in Analysis.
	doc, _ = e.b.Settings(e.ctx, e.db.Server, p)
	doc, _ = settings.SetStagesOn(doc, map[string]bool{})
	stages = doc["stages"].([]any)
	stages[3].(settings.Doc)["wip"] = 1
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	e.approve(p, "SF-3", board.Decision{DocsReason: ptr("no page")})
	e.approve(p, "SF-4", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if e.stage(p, "SF-3") != "in_progress" || e.stage(p, "SF-4") != "analysis" {
		t.Fatalf("SF-3 %s, SF-4 %s", e.stage(p, "SF-3"), e.stage(p, "SF-4"))
	}
	if w := *e.ticket(p, "SF-4").Waiting; w != "In progress is full (1 of 1)" {
		t.Fatalf("SF-4 waits: %s", w)
	}
}

// The order the backlog is pulled in: a ticket an approved ticket waits for goes first, then by
// priority, then the order of the backlog (7.9).
func TestTheBacklogIsPulledInOrder(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["stages"].([]any)[2].(settings.Doc)["wip"] = 2
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	low := 3
	e.add(p, ready("A"), board.OriginPlan)
	n := ready("B")
	n.Priority = &low
	e.add(p, n, board.OriginPlan)
	e.add(p, ready("C"), board.OriginPlan)
	urgent := 0
	n = ready("D")
	n.Priority = &urgent
	e.add(p, n, board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	if e.stage(p, "SF-4") != "analysis" || e.stage(p, "SF-1") != "analysis" || e.stage(p, "SF-3") != "backlog" {
		t.Fatalf("SF-4 %s, SF-1 %s, SF-3 %s", e.stage(p, "SF-4"), e.stage(p, "SF-1"), e.stage(p, "SF-3"))
	}
	// SF-1, ready, waits for the low SF-2: SF-2 goes before SF-3 when a place frees.
	if err := e.b.Link(e.ctx, p, "SF-1", "SF-2", "depends_on", "lead"); err != nil {
		t.Fatal(err)
	}
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if e.stage(p, "SF-2") != "analysis" || e.stage(p, "SF-3") != "backlog" {
		t.Fatalf("SF-2 %s, SF-3 %s", e.stage(p, "SF-2"), e.stage(p, "SF-3"))
	}
}

// A circle of depends_on is refused (7.9).
func TestADependencyCircleIsRefused(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	for _, title := range []string{"A", "B", "C"} {
		e.add(p, ready(title), board.OriginPlan)
	}
	if err := e.b.Link(e.ctx, p, "SF-1", "SF-2", "depends_on", "lead"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.Link(e.ctx, p, "SF-2", "SF-3", "depends_on", "lead"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"SF-3", "SF-1"}, {"SF-1", "SF-1"}} {
		if err := e.b.Link(e.ctx, p, c[0], c[1], "depends_on", "lead"); err == nil {
			t.Errorf("%s depends on %s: accepted", c[0], c[1])
		}
	}
	n := ready("D")
	n.DependsOn = []string{"SF-9"}
	if _, err := e.b.AddTicket(e.ctx, p.Name, n, board.OriginPlan, "lead"); err == nil {
		t.Error("a dependency on a ticket that does not exist was accepted")
	}
}

// Dependencies cannot lock the board: a discarded one counts as met; a cancelled one passes its
// place to its replacements; a skipped one sends the waiting ticket back to analysis (7.9).
func TestDependencies(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Base"), board.OriginPlan)
	n := ready("Top")
	n.DependsOn = []string{"SF-1"}
	e.add(p, n, board.OriginPlan)
	e.add(p, ready("Other"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-2", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if *e.ticket(p, "SF-2").Waiting != "waits for SF-1" {
		t.Fatalf("SF-2 waits: %s", *e.ticket(p, "SF-2").Waiting)
	}
	// SF-1 skipped: SF-2's plan no longer holds, so it is analysed again.
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "skip", Reason: "Done already.",
		LookAgain: &board.LookAgain{Kind: "all_done"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if st := e.ticket(p, "SF-2").Analysis.Status; st != board.StatusWaiting {
		t.Fatalf("SF-2 after its dependency was skipped: %s", st)
	}
	// Discarded by the operator: met.
	if _, err := e.b.Discard(e.ctx, p, "SF-1", "Not wanted.", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.approve(p, "SF-2", board.Decision{DocsReason: ptr("no page")})
	e.reconcile(p)
	if e.stage(p, "SF-2") != "in_progress" {
		t.Fatalf("SF-2 with a discarded dependency: %s, %v", e.stage(p, "SF-2"), e.ticket(p, "SF-2").Waiting)
	}
}

// Analysis decisions (7.6): skip with a condition that Crew tests, discard with a reason, and a new
// feature's notice with its 24 hours, Approve now, Discard, and Hold, which stops its clock (7.3).
func TestAnalysisDecisions(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Planned"), board.OriginPlan)
	e.ok(p)
	for _, title := range []string{"Seen bug", "New feature", "Another feature"} {
		n := ready(title)
		n.Seen = ptr("go test ./... fails in auth_test.go:42")
		e.add(p, n, board.OriginAgent)
	}
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["stages"].([]any)[2].(settings.Doc)["wip"] = 4
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	for _, k := range []string{"SF-1", "SF-2", "SF-3", "SF-4"} {
		if e.stage(p, k) != "analysis" {
			t.Fatalf("%s is in %s", k, e.stage(p, k))
		}
	}
	if st := e.ticket(p, "SF-2").Analysis.Status; st != board.StatusProposed {
		t.Fatalf("an agent's ticket is %s", st)
	}

	// A plan ticket has nothing to decide (R31).
	for _, c := range []string{"discard", "notice"} {
		if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: c, Reason: "x"}, "lead"); kindOf(err) != board.Refused {
			t.Errorf("%s of a plan ticket: %v", c, err)
		}
	}
	// A skip needs a reason and one of four conditions.
	for _, d := range []board.Decision{
		{Choice: "skip", LookAgain: &board.LookAgain{Kind: "all_done"}},
		{Choice: "skip", Reason: "later"},
		{Choice: "skip", Reason: "later", LookAgain: &board.LookAgain{Kind: "when-ready"}},
		{Choice: "skip", Reason: "later", LookAgain: &board.LookAgain{Kind: "date", Value: "soon"}},
		{Choice: "skip", Reason: "later", LookAgain: &board.LookAgain{Kind: "ticket_done", Value: "SF-99"}},
	} {
		if _, err := e.b.Decide(e.ctx, p, "SF-1", d, "lead"); err == nil {
			t.Errorf("skip %+v: accepted", d.LookAgain)
		}
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "skip", Reason: "After the launch.",
		LookAgain: &board.LookAgain{Kind: "date", Value: "2026-10-20"}}, "lead"); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if tk := e.ticket(p, "SF-1"); tk.Analysis.Status != board.StatusSkipped || *tk.Waiting != "skipped: After the launch." {
		t.Fatalf("SF-1 = %s, %v", tk.Analysis.Status, tk.Waiting)
	}
	e.clock.Add(5 * 24 * time.Hour)
	e.reconcile(p)
	if st := e.ticket(p, "SF-1").Analysis.Status; st != board.StatusWaiting {
		t.Fatalf("SF-1 after its date: %s", st)
	}

	// Discard closes it with the reason.
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "discard"}, "lead"); kindOf(err) != board.Invalid {
		t.Fatalf("a discard with no reason: %v", err)
	}
	tk, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "discard", Reason: "A duplicate of SF-1."}, "lead")
	if err != nil || tk.Stage != "closed" || *tk.ClosedReason != "discarded" || tk.Analysis.Reason != "A duplicate of SF-1." {
		t.Fatalf("discarded: %+v, %v", tk, err)
	}

	// A new feature: notice, 24 hours, then ready.
	tk, err = e.b.Decide(e.ctx, p, "SF-3", board.Decision{Choice: "notice", Reason: "Not in the plan."}, "lead")
	if err != nil || tk.Analysis.Status != board.StatusNotice || !tk.NoticeUntil.Equal(e.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("notice: %+v, %v", tk, err)
	}
	e.clock.Add(time.Hour)
	if _, err := e.b.NoticeDecision(e.ctx, p, "SF-3", "hold", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.clock.Add(48 * time.Hour)
	e.reconcile(p)
	if tk = e.ticket(p, "SF-3"); tk.Analysis.Status != board.StatusNotice || tk.NoticeUntil != nil {
		t.Fatalf("a held notice went ahead: %s, %v", tk.Analysis.Status, tk.NoticeUntil)
	}
	off := false
	if tk, err = e.b.EditTicket(e.ctx, p, "SF-3", board.Edit{Hold: &off}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if !tk.NoticeUntil.Equal(e.clock.Now().Add(23 * time.Hour)) {
		t.Fatalf("the clock after the hold: %v", tk.NoticeUntil)
	}
	e.clock.Add(23 * time.Hour)
	e.reconcile(p)
	if st := e.ticket(p, "SF-3").Analysis.Status; st != board.StatusReady {
		t.Fatalf("after its notice SF-3 is %s", st)
	}
	// Approve now, at once.
	if _, err := e.b.Decide(e.ctx, p, "SF-4", board.Decision{Choice: "notice", Reason: "New."}, "lead"); err != nil {
		t.Fatal(err)
	}
	if tk, err = e.b.NoticeDecision(e.ctx, p, "SF-4", "approve_now", "", board.Operator); err != nil || tk.Analysis.Status != board.StatusReady {
		t.Fatalf("approve now: %v, %v", tk.Analysis.Status, err)
	}
	if _, err := e.b.NoticeDecision(e.ctx, p, "SF-4", "approve_now", "", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("approve now twice: %v", err)
	}
}

// With approvals.new_feature set to operator a notice has no clock; with agent it goes ahead at once.
func TestNewFeatureApprovalSettings(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("Planned"), board.OriginPlan)
	e.ok(p)
	e.add(p, ready("New"), board.OriginAgent)
	e.reconcile(p)
	doc, _ := e.b.Settings(e.ctx, e.db.Server, p)
	doc["approvals"].(settings.Doc)["new_feature"] = "operator"
	if err := e.b.SaveSettings(e.ctx, p, doc, board.Operator, nil); err != nil {
		t.Fatal(err)
	}
	tk, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "notice", Reason: "New."}, "lead")
	if err != nil || tk.Analysis.Status != board.StatusNotice || tk.NoticeUntil != nil {
		t.Fatalf("with operator: %+v %v", tk, err)
	}
	e.clock.Add(1000 * time.Hour)
	e.reconcile(p)
	if e.ticket(p, "SF-2").Analysis.Status != board.StatusNotice {
		t.Fatal("a notice without a clock went ahead")
	}
}

// An agent's ticket cannot skip Review (7.4); a skipped stage is passed by (R98).
func TestSkippedStages(t *testing.T) {
	e := newEnv(t)
	e.b.Checks = scripted()
	p := e.project("shop", "SF", "review", "test")
	e.add(p, ready("Docs only"), board.OriginPlan)
	e.ok(p)
	e.add(p, ready("From an agent"), board.OriginAgent)
	e.reconcile(p)
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "approve", Skips: []string{"review"}, SkipsReason: ptr("small")}, "lead"); kindOf(err) != board.Refused {
		t.Fatalf("an agent's ticket skipped Review: %v", err)
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "approve", Skips: []string{"test"}}, "lead"); kindOf(err) != board.Invalid {
		t.Fatalf("a skip with no reason: %v", err)
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "approve", Skips: []string{"analysis"}, SkipsReason: ptr("x")}, "lead"); kindOf(err) != board.Invalid {
		t.Fatalf("a fixed stage skipped: %v", err)
	}
	tk := e.approve(p, "SF-1", board.Decision{Skips: []string{"test"}, SkipsReason: ptr("Only documentation."), DocsReason: ptr("none")})
	e.reconcile(p)
	tk = e.ticket(p, "SF-1")
	e.push(p, *tk.Branch, map[string]string{"README.md": "better\n"})
	e.reconcile(p)
	if got := e.moves(tk); !slices.Equal(got[2:], []string{"analysis>in_progress", "in_progress>review", "review>done"}) {
		t.Fatalf("moves = %v", got)
	}
}

// A paused project and a held ticket move nothing (15.2); resuming goes on where it stopped (7.7).
func TestPauseAndHold(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("A"), board.OriginPlan)
	e.add(p, ready("B"), board.OriginPlan)
	e.ok(p)
	if _, err := e.b.SetState(e.ctx, p.Name, "pause", "budget", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-1") != "backlog" {
		t.Fatal("a paused project moved a ticket")
	}
	if _, err := e.b.SetState(e.ctx, p.Name, "pause", "", board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("paused twice: %v", err)
	}
	on := true
	if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Hold: &on}, board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.SetState(e.ctx, p.Name, "resume", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	e.reconcile(p)
	if e.stage(p, "SF-1") != "backlog" || *e.ticket(p, "SF-1").Waiting != "on hold" || e.stage(p, "SF-2") != "analysis" {
		t.Fatalf("SF-1 %s, SF-2 %s", e.stage(p, "SF-1"), e.stage(p, "SF-2"))
	}
	if _, err := e.b.SetState(e.ctx, p.Name, "archive", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.AddTicket(e.ctx, p.Name, ready("C"), board.OriginOperator, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a ticket on an archived project: %v", err)
	}
}

// The operator edits a ticket's texts only before Analysis; its priority, hold, and budget at any time
// before it is done (15.4). Each edit is one row of history.
func TestEditingATicket(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("A"), board.OriginPlan)
	before := e.changes(p)
	tk, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Title: ptr("  A, better  "), Goal: ptr("It works well.")}, board.Operator)
	if err != nil || tk.Title != "A, better" || tk.Goal != "It works well." {
		t.Fatalf("edit: %+v %v", tk, err)
	}
	if e.changes(p) != before+1 {
		t.Fatal("an edit is not one row of history")
	}
	e.ok(p)
	e.reconcile(p)
	if _, err := e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Why: ptr("other")}, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("the why changed in Analysis: %v", err)
	}
	urgent := 0
	if tk, err = e.b.EditTicket(e.ctx, p, "SF-1", board.Edit{Priority: &urgent}, board.Operator); err != nil || tk.Priority != 0 {
		t.Fatalf("priority: %v %v", tk.Priority, err)
	}
	for name, ed := range map[string]board.Edit{
		"a priority of 9":  {Priority: ptr(9)},
		"a title of lines": {Title: ptr("a\nb")},
	} {
		if _, err := e.b.EditTicket(e.ctx, p, "SF-1", ed, board.Operator); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	h, err := e.b.History(e.ctx, e.ticket(p, "SF-1"))
	if err != nil || len(h) < 4 {
		t.Fatalf("history has %d rows (%v)", len(h), err)
	}
}

// One logical change is one transaction (6.4): a move whose git step fails leaves the ticket where it
// was, with no move and no history row; a refused decision changes nothing.
func TestAChangeIsOneTransaction(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	e.add(p, ready("A"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	before := e.changes(p)
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "skip", Reason: "x"}, "lead"); err == nil {
		t.Fatal("a skip with no condition was accepted")
	}
	if e.changes(p) != before || e.ticket(p, "SF-1").Analysis.Status != board.StatusReady {
		t.Fatal("a refused decision changed something")
	}
	// The canonical repository is gone, so the branch cannot be cut.
	if err := os.Rename(e.git.Canonical("shop"), e.git.Canonical("shop")+".away"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.Reconcile(e.ctx, p.Name); err == nil {
		t.Fatal("a move whose branch could not be made succeeded")
	}
	tk := e.ticket(p, "SF-1")
	if tk.Stage != "analysis" || tk.Branch != nil || e.changes(p) != before || len(e.moves(tk)) != 2 {
		t.Fatalf("after the failed move: %s %v, %d changes, moves %v", tk.Stage, tk.Branch, e.changes(p)-before, e.moves(tk))
	}
	os.Rename(e.git.Canonical("shop")+".away", e.git.Canonical("shop"))
	e.reconcile(p)
	if e.stage(p, "SF-1") != "in_progress" {
		t.Fatal("the move did not finish once the repository was back")
	}
}

// The history of a project is only added to: the server's role cannot change or remove a row of
// changes, a move, a check result, or a plan.
func TestHistoryIsOnlyAddedTo(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	// A column each table has, so an UPDATE fails for the right reason: the privilege.
	var sqls []string
	for table, col := range map[string]string{"changes": "who", "plans": "text", "stage_moves": "actor", "check_results": "detail"} {
		sqls = append(sqls, `UPDATE `+table+` SET `+col+` = `+col, `DELETE FROM `+table, `TRUNCATE `+table)
	}
	for _, sql := range sqls {
		if _, err := e.db.Server.Exec(e.ctx, sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	_ = p
}

func ptr[T any](v T) *T { return &v }

// The kanban rule has one door (R179): no code but move writes a ticket's stage, so every move of
// every path goes through the check that it is to the right. Tickets are made in their first stage.
func TestOnlyMoveWritesATicketsStage(t *testing.T) {
	writes := regexp.MustCompile(`(?is)UPDATE\s+tickets\s+(?:\w+\s+)?SET[^;` + "`" + `]*?\bstage\s*=`)
	var found []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for range writes.FindAllIndex(b, -1) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found, []string{"../board/reconcile.go"}) {
		t.Fatalf("a ticket's stage is written in %v, want only by move in board/reconcile.go", found)
	}
}
