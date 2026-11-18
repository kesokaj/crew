package export_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/export"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

type env struct {
	ctx context.Context
	db  *dbtest.DB
	git *gitstore.Store
	b   *board.Board
	x   *export.Exporter
}

func newEnv(t *testing.T) *env {
	d := dbtest.New(t, false)
	dir := t.TempDir()
	g := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}}
	now := func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	b := &board.Board{Pool: d.Server, Git: g, Now: now,
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		}}
	return &env{ctx: context.Background(), db: d, git: g, b: b, x: &export.Exporter{Pool: d.Server, Board: b, Git: g, Version: "test", Now: now}}
}

func ready(title string) board.NewTicket {
	return board.NewTicket{Title: title, Why: "Because.", What: "This.", Goal: "Done.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test"}, {Kind: "judge", Spec: "It reads well."}}}
}

// shop is a project with something of everything M4 keeps: a plan sent back and broken down, tickets
// in several stages, a skip, a discard, links, a setting of its own, and work on a branch.
func shop(t *testing.T, e *env) *board.Project {
	t.Helper()
	p, err := e.b.CreateProject(e.ctx, board.NewProject{Name: "shop", Key: "SF", Goal: "Sell things.", Plan: "## Goal\n\nSell.\n",
		Stages: []string{"review", "verification"}, Repo: board.Repo{Kind: "local"}}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.b.SendBack(e.ctx, "shop", "Smaller tickets, please.", board.Operator))
	must(e.b.SetBreakdown(e.ctx, p, "1. Sign-in\n2. Cart\n", "lead"))
	for _, title := range []string{"Sign-in", "Cart", "Checkout", "Old idea"} {
		n := ready(title)
		n.Part = ptr("Accounts")
		n.Source = &board.Source{File: "plan/original.md", Heading: "Goal", Quote: "Sell."}
		if _, err := e.b.AddTicket(e.ctx, "shop", n, board.OriginPlan, "lead"); err != nil {
			t.Fatal(err)
		}
	}
	must(e.b.Link(e.ctx, p, "SF-3", "SF-2", "depends_on", "lead"))
	if _, err := e.b.OK(e.ctx, "shop", board.Operator); err != nil {
		t.Fatal(err)
	}
	must(e.b.Reconcile(e.ctx, "shop"))
	if _, err := e.b.Decide(e.ctx, p, "SF-1", board.Decision{Choice: "approve", Docs: []string{"docs/auth.md"},
		Skips: []string{"verification"}, SkipsReason: ptr("small"), MayChange: []string{"src/auth/**"}}, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Decide(e.ctx, p, "SF-2", board.Decision{Choice: "skip", Reason: "Later.",
		LookAgain: &board.LookAgain{Kind: "ticket_done", Value: "SF-1"}}, "lead · r_2"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Discard(e.ctx, p, "SF-4", "Not wanted.", board.Operator); err != nil {
		t.Fatal(err)
	}
	must(e.b.Reconcile(e.ctx, "shop"))
	tk, _ := e.b.Ticket(e.ctx, p, "SF-1")
	if tk.Stage != "in_progress" {
		t.Fatalf("SF-1 is in %s", tk.Stage)
	}
	work := t.TempDir()
	for _, args := range [][]string{{"clone", "-q", e.git.Quarantine.Path("shop"), "."}, {"checkout", "-q", "-b", *tk.Branch}} {
		run(t, work, args...)
	}
	os.WriteFile(filepath.Join(work, "auth.go"), []byte("package auth\n"), 0o644)
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "-m", "SF-1: sign-in")
	run(t, work, "push", "-q", "origin", *tk.Branch)
	high := 1
	if _, err := e.b.EditTicket(e.ctx, p, "SF-3", board.Edit{Priority: &high}, board.Operator); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitstore.CleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ptr[T any](v T) *T { return &v }

// entries reads a tar stream into its files.
func entries(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = body
	}
}

// An export read back gives the same project: written out again under its new name and key, every
// file but the name and the key is the same, and its repositories hold the same refs (22, Database).
func TestAnExportReadsBackAsTheSameProject(t *testing.T) {
	e := newEnv(t)
	shop(t, e)
	var first bytes.Buffer
	if err := e.x.Write(e.ctx, "shop", &first); err != nil {
		t.Fatal(err)
	}
	files := entries(t, first.Bytes())
	for _, want := range []string{"board.yaml", "project.md", "plan/original.md", "plan/breakdown/01.md", "plan/sent-back/01.md",
		"tasks/0001-sign-in/task.md", "tasks/0004-old-idea/task.md", "repo.bundle"} {
		if files[want] == nil {
			t.Errorf("the export has no %s", want)
		}
	}
	p2, err := e.x.Read(e.ctx, bytes.NewReader(first.Bytes()), export.As{Name: "shop-copy", Key: "SC"}, "crew-ctl")
	if err != nil {
		t.Fatal(err)
	}
	// What ran on the machine it came from runs here only when the operator resumes it.
	if p2.Name != "shop-copy" || p2.Key != "SC" || p2.State != "paused" || p2.PauseReason == nil ||
		*p2.PauseReason != "imported: resume it to let it run here" {
		t.Fatalf("imported %+v", p2)
	}
	var second bytes.Buffer
	if err := e.x.Write(e.ctx, "shop-copy", &second); err != nil {
		t.Fatal(err)
	}
	again := entries(t, second.Bytes())
	if len(again) != len(files) {
		t.Fatalf("%d files, then %d", len(files), len(again))
	}
	for name, body := range files {
		if name == "repo.bundle" {
			continue
		}
		want := string(body)
		// A branch keeps the name it has in the repository.
		want = strings.NewReplacer("task/SF-", "task/SF-", "SF-", "SC-", "key: SF", "key: SC", "project: shop", "project: shop-copy",
			"name: shop", "name: shop-copy", "# shop", "# shop-copy").Replace(want)
		got := string(again[name])
		if name == "project.md" {
			// Where the copy's repository came from is the import, and it waits, paused.
			want = strings.Replace(want, "repo_source: local", "repo_source: import", 1)
			want = strings.Replace(want, "state: running\npause_reason: null", "state: paused\npause_reason: 'imported: resume it to let it run here'", 1)
		}
		if strings.HasPrefix(name, "tasks/") {
			// Every move that came with the import says so.
			want = movedAt.ReplaceAllString(want, "$1\n    imported: true")
		}
		if got != want {
			t.Errorf("%s differs after a round trip: %s", name, firstDiff(want, got))
		}
	}
	// main as the canonical repository has it, and the ticket's branch as its run pushed it.
	for _, ref := range []struct{ repo, ref string }{{e.git.Canonical("shop"), "refs/heads/main"},
		{e.git.Quarantine.Path("shop"), "refs/heads/task/SF-1-sign-in"}} {
		for _, copy := range []string{e.git.Canonical("shop-copy"), e.git.Quarantine.Path("shop-copy")} {
			if a, b := run(t, ref.repo, "rev-parse", ref.ref), run(t, copy, "rev-parse", ref.ref); a != b {
				t.Errorf("%s in %s: %s, the copy %s", ref.ref, copy, a, b)
			}
		}
	}
	var imported int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM changes WHERE project = $1 AND after ? 'imported'`, p2.ID).Scan(&imported)
	if imported != 1 {
		t.Fatalf("the import has %d rows that say so", imported)
	}
	// Its moves say they came with the import; this board's own do not.
	var marked, own int
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FILTER (WHERE m.imported), count(*) FILTER (WHERE NOT m.imported)
		FROM stage_moves m JOIN tickets t ON t.id = m.ticket WHERE t.project = $1`, p2.ID).Scan(&marked, &own)
	if marked == 0 || own != 0 {
		t.Fatalf("moves marked imported: %d, not marked: %d", marked, own)
	}
	// Resumed, the copy runs as a board of its own, and a new ticket gets the next number.
	if _, err := e.b.SetState(e.ctx, "shop-copy", "resume", "", board.Operator); err != nil {
		t.Fatal(err)
	}
	if err := e.b.Reconcile(e.ctx, "shop-copy"); err != nil {
		t.Fatal(err)
	}
	if tk, err := e.b.AddTicket(e.ctx, "shop-copy", ready("Next"), board.OriginOperator, board.Operator); err != nil || tk.Key != "SC-5" {
		t.Fatalf("a new ticket on the copy: %v, %v", tk, err)
	}
	if _, err := e.x.Read(e.ctx, bytes.NewReader(first.Bytes()), export.As{}, "crew-ctl"); err == nil {
		t.Fatal("an export was read over the project it came from")
	}
}

// What an import reads is data from a file. A path outside the layout, a link, a stage the board does
// not have, a circle of dependencies, a ticket whose number and folder disagree, and another format
// are refused, and nothing is left of a refused import.
func TestAnImportChecksWhatItReads(t *testing.T) {
	e := newEnv(t)
	shop(t, e)
	var buf bytes.Buffer
	if err := e.x.Write(e.ctx, "shop", &buf); err != nil {
		t.Fatal(err)
	}
	files := entries(t, buf.Bytes())
	build := func(change func(map[string][]byte), extra ...*tar.Header) []byte {
		f := map[string][]byte{}
		for k, v := range files {
			f[k] = v
		}
		change(f)
		var out bytes.Buffer
		tw := tar.NewWriter(&out)
		for name, body := range f {
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write(body)
		}
		for _, h := range extra {
			tw.WriteHeader(h)
		}
		tw.Close()
		return out.Bytes()
	}
	edit := func(name, old, new string) func(map[string][]byte) {
		return func(f map[string][]byte) {
			if !bytes.Contains(f[name], []byte(old)) {
				t.Fatalf("%s holds no %q", name, old)
			}
			f[name] = bytes.Replace(f[name], []byte(old), []byte(new), 1)
		}
	}
	for name, stream := range map[string][]byte{
		"a link":                   build(func(map[string][]byte) {}, &tar.Header{Name: "tasks/0009-x/task.md", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}),
		"a stage not on the board": build(edit("tasks/0002-cart/task.md", "stage: analysis", "stage: security")),
		"a circle":                 build(edit("tasks/0002-cart/task.md", "links: []", "links:\n  - kind: depends_on\n    to: SF-3")),
		"a number and a folder":    build(edit("tasks/0002-cart/task.md", "id: \"0002\"", "id: \"0003\"")),
		"another format":           build(edit("project.md", "format: 1", "format: 2")),
		"a field not known":        build(edit("tasks/0002-cart/task.md", "kind: feature", "kind: feature\nrun_this: rm -rf /")),
		"a branch of its own":      build(edit("tasks/0001-sign-in/task.md", "branch: task/SF-1-sign-in", "branch: ../../etc")),
		"settings that fail":       build(edit("board.yaml", "max_fix_rounds: 3", "max_fix_rounds: 0")),
		"no bundle":                build(func(f map[string][]byte) { delete(f, "repo.bundle") }),
		"a bundle that is not":     build(func(f map[string][]byte) { f["repo.bundle"] = []byte("x") }),
		"a link out of it":         build(edit("tasks/0003-checkout/task.md", "to: SF-2", "to: SF-99")),
		// The board's own rules: what the API could never have made.
		"a stage its moves do not reach": build(edit("tasks/0002-cart/task.md", "stage: analysis", "stage: review")),
		"moves that skip a stage":        build(edit("tasks/0002-cart/task.md", "from: backlog\n    to: analysis", "from: backlog\n    to: done")),
		"In progress without approval":   build(edit("tasks/0001-sign-in/task.md", "status: ready", "status: waiting")),
		"an agent's ticket skips Review": build(both(edit("tasks/0001-sign-in/task.md", "origin: plan", "origin: agent"),
			edit("tasks/0001-sign-in/task.md", "skips:\n  - verification", "skips:\n  - review"))),
		"a skip of a fixed stage":       build(edit("tasks/0001-sign-in/task.md", "skips:\n  - verification", "skips:\n  - analysis")),
		"a pin that is not one":         build(edit("tasks/0001-sign-in/task.md", "agents: {}", "agents:\n  builder:\n    model: sonnet")),
		"a ticket in Done with no move": build(edit("tasks/0004-old-idea/task.md", "stage: closed", "stage: done")),
	} {
		if _, err := e.x.Read(e.ctx, bytes.NewReader(stream), export.As{Name: "copy", Key: "CP"}, "crew-ctl"); err == nil {
			t.Errorf("%s: imported", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
		if e.git.Exists("copy") {
			t.Fatalf("%s: the repositories of a refused import were left", name)
		}
		var n int
		e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM projects WHERE name = 'copy'`).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: the project of a refused import was left", name)
		}
	}
	// A second bundle, a real one, is refused for being a second; so is a name shaped as an id.
	second := build(func(map[string][]byte) {})
	var twice bytes.Buffer
	tw := tar.NewWriter(&twice)
	tr := tar.NewReader(bytes.NewReader(second))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(tr)
		tw.WriteHeader(h)
		tw.Write(body)
	}
	tw.WriteHeader(&tar.Header{Name: "./repo.bundle", Mode: 0o644, Size: int64(len(files["repo.bundle"])), Typeflag: tar.TypeReg})
	tw.Write(files["repo.bundle"])
	tw.Close()
	if _, err := e.x.Read(e.ctx, bytes.NewReader(twice.Bytes()), export.As{Name: "copy", Key: "CP"}, "crew-ctl"); err == nil || !strings.Contains(err.Error(), "repo.bundle is there twice") {
		t.Errorf("a second bundle: %v", err)
	}
	if _, err := e.x.Read(e.ctx, bytes.NewReader(second), export.As{Name: "f1c50000-0000-4000-8000-000000000001", Key: "CP"}, "crew-ctl"); err == nil || !strings.Contains(err.Error(), "not one Crew takes") {
		t.Errorf("a name shaped as an id: %v", err)
	}
	// A path outside the layout is not read at all: the import works without it.
	stream := build(func(f map[string][]byte) { f["../outside.md"] = []byte("x"); f["notes/1.md"] = []byte("x") })
	if _, err := e.x.Read(e.ctx, bytes.NewReader(stream), export.As{Name: "copy", Key: "CP"}, "crew-ctl"); err != nil {
		t.Fatalf("an export with files outside the layout: %v", err)
	}
	// An export made before 0.53 names budgets in board.yaml and in a ticket's file: it imports, and
	// they are left out (7.7).
	old := build(both(edit("board.yaml", "max_fix_rounds: 3", "max_fix_rounds: 3\n  run_budget_usd: 5\n  default_task_budget_usd: 3"),
		edit("tasks/0001-sign-in/task.md", "fix_rounds:", "budget_usd: 3\nover_budget: 2026-10-01T00:00:00Z\nfix_rounds:")))
	p, err := e.x.Read(e.ctx, bytes.NewReader(old), export.As{Name: "old", Key: "OLD"}, "crew-ctl")
	if err != nil {
		t.Fatalf("an export from before 0.53: %v", err)
	}
	var own string
	var budgets int
	e.db.Server.QueryRow(e.ctx, `SELECT coalesce((SELECT doc::text FROM settings WHERE project = $1), '')`, p.ID).Scan(&own)
	e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE project = $1 AND (budget_usd IS NOT NULL OR over_budget IS NOT NULL)`, p.ID).Scan(&budgets)
	if strings.Contains(own, "budget") || budgets != 0 {
		t.Fatalf("the budgets were imported: %s, %d tickets", own, budgets)
	}
}

var movedAt = regexp.MustCompile(`(\n    at: [^\n]*)`)

// both applies two changes to the files of an export.
func both(a, b func(map[string][]byte)) func(map[string][]byte) {
	return func(f map[string][]byte) { a(f); b(f) }
}

// firstDiff names the first line where two texts differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d: %q, then %q", i+1, x, y)
		}
	}
	return "no line"
}

// A ticket's chat and the operator's conversations are not in a project's export, as the
// trajectories are not (6.2, 11.9): neither a question, an answer, nor a marked text reaches it.
func TestAnExportHoldsNoConversation(t *testing.T) {
	e := newEnv(t)
	p := shop(t, e)
	tk, err := e.b.Ticket(e.ctx, p, "SF-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ('00000000-0000-4000-8000-0000000020c9', $1, $2, 'chat', 'agy'),
			('00000000-0000-4000-8000-0000000020ca', $1, NULL, 'operator', 'agy')`, []any{p.ID, tk.ID}},
		{`INSERT INTO conversation_messages (conversation, text, quote) VALUES ('00000000-0000-4000-8000-0000000020c9', 'QUESTION-OF-THE-CHAT', 'MARKED-TEXT'),
			('00000000-0000-4000-8000-0000000020ca', 'QUESTION-OF-A-TALK', NULL)`, nil},
		{`INSERT INTO turns (conversation, number, speaker, text) VALUES ('00000000-0000-4000-8000-0000000020c9', 1, 'agent', 'ANSWER-OF-THE-CHAT')`, nil},
	} {
		if _, err := e.db.Server.Exec(e.ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := e.x.Write(e.ctx, "shop", &out); err != nil {
		t.Fatal(err)
	}
	for name, body := range entries(t, out.Bytes()) {
		for _, w := range []string{"QUESTION-OF-THE-CHAT", "MARKED-TEXT", "QUESTION-OF-A-TALK", "ANSWER-OF-THE-CHAT", "00000000-0000-4000-8000-0000000020c9"} {
			if bytes.Contains(body, []byte(w)) {
				t.Errorf("%s holds %s", name, w)
			}
		}
	}
}
