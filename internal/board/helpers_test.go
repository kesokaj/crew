package board_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

// clock is a fake clock a test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	t     *testing.T
	ctx   context.Context
	db    *dbtest.DB
	git   *gitstore.Store
	b     *board.Board
	clock *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	g := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}, Protocols: "file"}
	c := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	b := &board.Board{
		Pool: d.Server,
		Git:  g,
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op Erator", Email: "op@example.com"}, nil
		},
		Now: c.Now,
	}
	return &env{t: t, ctx: context.Background(), db: d, git: g, b: b, clock: c}
}

const plan = "## Goal\n\nCustomers can sign in.\n\n## Parts\n\n### Sign-in\n"

// project makes a local project in planning with the optional stages given.
func (e *env) project(name, key string, stages ...string) *board.Project {
	e.t.Helper()
	n := board.NewProject{Name: name, Key: key, Plan: plan, Repo: board.Repo{Kind: "local"}}
	if stages != nil {
		n.Stages = stages
	}
	p, err := e.b.CreateProject(e.ctx, n, board.Operator)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// ready is a ticket as Analysis leaves it: with a why, a what, a goal, and a check.
func ready(title string) board.NewTicket {
	return board.NewTicket{Title: title, Why: "The plan says so.", What: "Change " + title + ".", Goal: title + " works.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "make test"}}}
}

func (e *env) add(p *board.Project, n board.NewTicket, origin string) *board.Ticket {
	e.t.Helper()
	t, err := e.b.AddTicket(e.ctx, p.Name, n, origin, "lead")
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *env) ok(p *board.Project) {
	e.t.Helper()
	if _, err := e.b.OK(e.ctx, p.Name, board.Operator); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) reconcile(p *board.Project) {
	e.t.Helper()
	if err := e.b.Reconcile(e.ctx, p.Name); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ticket(p *board.Project, key string) *board.Ticket {
	e.t.Helper()
	t, err := e.b.Ticket(e.ctx, p, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *env) stage(p *board.Project, key string) string { return e.ticket(p, key).Stage }

func (e *env) approve(p *board.Project, key string, d board.Decision) *board.Ticket {
	e.t.Helper()
	if d.Choice == "" {
		d.Choice = "approve"
	}
	t, err := e.b.Decide(e.ctx, p, key, d, "lead · r_test")
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

// changes counts the rows of history of the project.
func (e *env) changes(p *board.Project) int {
	e.t.Helper()
	var n int
	if err := e.db.Server.QueryRow(e.ctx, `SELECT count(*) FROM changes WHERE project = $1`, p.ID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// moves returns a ticket's moves, as "from>to" each.
func (e *env) moves(t *board.Ticket) []string {
	e.t.Helper()
	rows, err := e.db.Server.Query(e.ctx, `SELECT m.from_stage || '>' || m.to_stage FROM stage_moves m
		JOIN tickets t ON t.id = m.ticket JOIN projects p ON p.id = t.project
		WHERE p.key || '-' || t.number = $1 ORDER BY m.id`, t.Key)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func kindOf(err error) board.Kind {
	var be *board.Error
	if errors.As(err, &be) {
		return be.Kind
	}
	return 0
}

func git(t *testing.T, dir string, args ...string) string {
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

// push commits files on a ticket's branch and pushes it to the quarantine repository, as the builder's
// run does through the git endpoint.
func (e *env) push(p *board.Project, branch string, files map[string]string) {
	e.t.Helper()
	work := e.t.TempDir()
	git(e.t, work, "clone", "-q", e.git.Quarantine.Path(p.Name), ".")
	if git(e.t, work, "ls-remote", "origin", "refs/heads/"+branch) != "" {
		git(e.t, work, "fetch", "-q", "origin", branch)
		git(e.t, work, "checkout", "-q", "-B", branch, "FETCH_HEAD")
	} else {
		git(e.t, work, "checkout", "-q", "-B", branch)
	}
	for f, body := range files {
		os.MkdirAll(filepath.Join(work, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	git(e.t, work, "add", "-A")
	git(e.t, work, "commit", "-q", "-m", "work")
	git(e.t, work, "push", "-q", "origin", branch)
}

// passing is a check that passes, standing in for what Crew's records and its check sandbox give
// from M6.
func passing(_ context.Context, _ *board.CheckContext) (board.Result, error) {
	return board.Result{Passed: true, Detail: "scripted"}, nil
}

// scripted makes every check that this version cannot evaluate pass, as the tests' stand-in.
func scripted() map[string]board.Check {
	m := map[string]board.Check{}
	for _, n := range []string{"note_exists", "task_cmd_checks_pass", "review_verdict_approve", "test_result_pass",
		"task_checks_pass", "e2e_result_pass", "verify_pass", "merges_and_passes_on_main"} {
		m[n] = passing
	}
	return m
}
