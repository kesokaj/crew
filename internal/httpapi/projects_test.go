package httpapi_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/export"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/settings"
)

type boardServer struct {
	srv *httptest.Server
	b   *board.Board
	git *gitstore.Store
	s   *httpapi.Server
}

// boardServer is the server with a board on a test database and temporary repositories, and the
// board's reconcile loop running, as the server runs it.
func newBoardServer(t *testing.T) *boardServer {
	t.Helper()
	d := dbtest.New(t, false)
	dir := t.TempDir()
	g := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}}
	b := &board.Board{Pool: d.Server, Git: g,
		Identity: func(context.Context) (gitstore.Identity, error) {
			return gitstore.Identity{Name: "Op", Email: "op@example.com"}, nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := &httpapi.Server{
		Machine:      settings.Machine{Version: "1.0.0", AdminEmail: admin},
		Hostname:     host,
		AdminEmail:   admin,
		IngressToken: []byte(token),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.Pool.Store(d.Server)
	s.Board.Store(b)
	s.Export.Store(&export.Exporter{Pool: d.Server, Board: b, Git: g, Version: "1.0.0"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &boardServer{srv: srv, b: b, git: g, s: s}
}

// call makes a request as the operator and decodes the answer into out, failing on another code.
func (bs *boardServer) call(t *testing.T, method, path, body string, code int, out any) {
	t.Helper()
	h := signedIn
	if method != "GET" {
		h = change
	}
	got, resp := do(t, bs.srv, method, path, h, body)
	if got != code {
		t.Fatalf("%s %s: %d %s", method, path, got, resp)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(resp), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, resp)
		}
	}
}

type ticketView struct {
	Ticket struct {
		Key     string  `json:"key"`
		Stage   string  `json:"stage"`
		Branch  *string `json:"branch"`
		Waiting *string `json:"waiting"`
	} `json:"ticket"`
	History []struct {
		Who   string          `json:"Who"`
		After json.RawMessage `json:"After"`
	} `json:"history"`
}

func (bs *boardServer) waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}
		bs.b.Kick()
		time.Sleep(50 * time.Millisecond)
	}
}

func (bs *boardServer) stage(t *testing.T, key string) ticketView {
	t.Helper()
	var v ticketView
	bs.call(t, "GET", "/api/projects/shop/tasks/"+key, "", 200, &v)
	return v
}

// The done-when of M4, through the API: a project with its rows and its repository; tickets with a
// number, a why, a what, a goal, and checks; the project OK; analysis decisions; moves through the
// fixed and the optional stages on checks, with the stages' limits; one row of history per change;
// and the export. The checks of M6 are scripted; Analysis is the lead's call, which the Crew MCP
// makes from M6.
func TestATicketGoesFromPlanningToDoneThroughTheAPI(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	m := map[string]board.Check{}
	for _, n := range []string{"note_exists", "task_cmd_checks_pass", "review_verdict_approve", "test_result_pass",
		"task_checks_pass", "e2e_result_pass", "verify_pass", "merges_and_passes_on_main"} {
		m[n] = func(context.Context, *board.CheckContext) (board.Result, error) {
			return board.Result{Passed: true, Detail: "scripted"}, nil
		}
	}
	bs.b.Checks = m
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bs.b.Run(ctx, 100*time.Millisecond)

	var p board.Project
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "goal": "Sell things.", "plan": "## Goal\n\nSell.\n",
		"stages": ["review", "test", "e2e", "verification"], "repository": {"kind": "local"}}`, 201, &p)
	if p.State != "planning" {
		t.Fatalf("project = %+v", p)
	}
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "XX", "plan": "x", "repository": {"kind": "local"}}`, 409, nil)
	for _, title := range []string{"Lock account", "Reset password"} {
		bs.call(t, "POST", "/api/projects/shop/tasks", `{"title": "`+title+`", "why": "The plan says so.", "what": "Lock after five.",
			"goal": "Five failures lock the account.", "checks": [{"kind": "cmd", "spec": "go test ./auth"}], "docsReason": "none"}`, 201, nil)
	}
	var brd struct {
		Columns []struct {
			ID      string `json:"id"`
			Tickets []struct {
				Key string `json:"key"`
			} `json:"tickets"`
		} `json:"columns"`
	}
	bs.call(t, "GET", "/api/projects/shop/board", "", 200, &brd)
	if brd.Columns[0].ID != "planning" || len(brd.Columns[0].Tickets) != 2 || len(brd.Columns) != 9 {
		t.Fatalf("board = %+v", brd)
	}
	bs.call(t, "POST", "/api/projects/shop/ok", "", 200, &p)
	if p.State != "running" {
		t.Fatalf("after the OK: %s", p.State)
	}
	bs.waitFor(t, "both tickets in Analysis", func() bool {
		return bs.stage(t, "SF-1").Ticket.Stage == "analysis" && bs.stage(t, "SF-2").Ticket.Stage == "analysis"
	})

	pr, _ := bs.b.Project(ctx, "shop")
	if _, err := bs.b.Decide(ctx, pr, "SF-1", board.Decision{Choice: "approve"}, "lead · r_1"); err != nil {
		t.Fatal(err)
	}
	var v ticketView
	// The move clears why a ticket waits; the board's next pass says it again.
	bs.waitFor(t, "SF-1 in In progress", func() bool {
		v = bs.stage(t, "SF-1")
		return v.Ticket.Stage == "in_progress" && v.Ticket.Waiting != nil
	})
	if *v.Ticket.Branch != "task/SF-1-lock-account" || *v.Ticket.Waiting != "its branch is not pushed" {
		t.Fatalf("SF-1 = %+v", v.Ticket)
	}
	pushWork(t, bs.git, "shop", *v.Ticket.Branch)
	bs.waitFor(t, "SF-1 Done", func() bool { v = bs.stage(t, "SF-1"); return v.Ticket.Stage == "done" })
	var moves []string
	for _, h := range v.History {
		var a struct {
			Stage string `json:"stage"`
		}
		json.Unmarshal(h.After, &a)
		if a.Stage != "" {
			moves = append(moves, a.Stage)
		}
	}
	if got := strings.Join(moves, " "); got != "planning backlog analysis in_progress review test e2e verification done" {
		t.Fatalf("the history's moves: %s", got)
	}

	// A new feature an agent found: its notice, and Approve now through the API.
	if _, err := bs.b.AddTicket(ctx, "shop", board.NewTicket{Title: "Remember me", Why: "Users asked.", What: "A box.", Goal: "It stays.",
		Checks: []board.CheckSpec{{Kind: "cmd", Spec: "go test ./session"}}}, board.OriginAgent, "builder · r_9"); err != nil {
		t.Fatal(err)
	}
	bs.waitFor(t, "SF-3 in Analysis", func() bool { return bs.stage(t, "SF-3").Ticket.Stage == "analysis" })
	if _, err := bs.b.Decide(ctx, pr, "SF-3", board.Decision{Choice: "notice", Reason: "Not in the plan."}, "lead · r_2"); err != nil {
		t.Fatal(err)
	}
	var dec struct {
		Decisions []struct {
			Key      string         `json:"key"`
			Analysis board.Analysis `json:"analysis"`
		} `json:"decisions"`
	}
	bs.call(t, "GET", "/api/projects/shop/analysis", "", 200, &dec)
	if len(dec.Decisions) != 2 || dec.Decisions[1].Analysis.Status != board.StatusNotice {
		t.Fatalf("decisions = %+v", dec)
	}
	bs.call(t, "POST", "/api/projects/shop/tasks/SF-3/decision", `{"choice": "approve_now"}`, 200, nil)
	bs.call(t, "POST", "/api/projects/shop/tasks/SF-3/decision", `{"choice": "approve_now"}`, 400, nil)
	bs.call(t, "POST", "/api/projects/shop/tasks/SF-2/discard", `{}`, 400, nil)
	bs.call(t, "POST", "/api/projects/shop/tasks/SF-2/discard", `{"reason": "Not now."}`, 200, nil)
	bs.call(t, "PATCH", "/api/projects/shop/tasks/SF-3", `{"why": "other"}`, 400, nil)
	bs.call(t, "PATCH", "/api/projects/shop/tasks/SF-3", `{"priority": 0, "hold": true}`, 200, nil)
	bs.call(t, "PATCH", "/api/projects/shop/tasks/SF-1", `{"priority": 0}`, 400, nil)

	// Stages and settings.
	var st struct {
		Stages []settings.Stage `json:"stages"`
	}
	bs.call(t, "PUT", "/api/projects/shop/stages", `{"verification": false}`, 200, &st)
	if st.Stages[7].ID != "verification" || st.Stages[7].On {
		t.Fatalf("stages = %+v", st.Stages)
	}
	bs.call(t, "PUT", "/api/projects/shop/stages", `{"analysis": false}`, 400, nil)
	var doc settings.Doc
	bs.call(t, "GET", "/api/projects/shop/settings", "", 200, &doc)
	doc["limits"].(map[string]any)["max_fix_rounds"] = 0
	body, _ := json.Marshal(doc)
	bs.call(t, "PUT", "/api/projects/shop/settings", string(body), 400, nil)

	// Pause, and the export.
	bs.call(t, "POST", "/api/projects/shop/pause", `{}`, 200, &p)
	if p.State != "paused" {
		t.Fatalf("paused: %s", p.State)
	}
	bs.call(t, "POST", "/api/projects/shop/resume", `{}`, 200, nil)
	code, tarBody := do(t, bs.srv, "POST", "/api/projects/shop/export", change, "")
	if code != 200 {
		t.Fatalf("export: %d %s", code, tarBody)
	}
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader([]byte(tarBody)))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = true
	}
	for _, want := range []string{"board.yaml", "project.md", "plan/original.md", "tasks/0001-lock-account/task.md", "repo.bundle"} {
		if !names[want] {
			t.Errorf("the export has no %s", want)
		}
	}
	var overview struct {
		Tickets int            `json:"tickets"`
		ByStage map[string]int `json:"byStage"`
	}
	bs.call(t, "GET", "/api/projects/shop/overview", "", 200, &overview)
	if overview.Tickets != 3 || overview.ByStage["done"] != 1 || overview.ByStage["closed"] != 1 {
		t.Fatalf("overview = %+v", overview)
	}
}

// The routes refuse what they do not take: a change from another page, a field they do not know, a
// project or a ticket that does not exist.
func TestTheProjectRoutesRefuse(t *testing.T) {
	t.Parallel()
	bs := newBoardServer(t)
	create := `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}}`
	if code, _ := do(t, bs.srv, "POST", "/api/projects", signedIn, create); code != 403 {
		t.Fatalf("a change without the page's origin: %d", code)
	}
	if code, _ := do(t, bs.srv, "POST", "/api/projects", map[string]string{"Origin": "https://" + host}, create); code != 401 {
		t.Fatalf("a change without the ingress: %d", code)
	}
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "local"}, "admin": true}`, 400, nil)
	bs.call(t, "POST", "/api/projects", `{"name": "shop", "key": "SF", "plan": "x", "repository": {"kind": "github"}}`, 400, nil)
	bs.call(t, "POST", "/api/projects", create, 201, nil)
	bs.call(t, "GET", "/api/projects/nope/board", "", 404, nil)
	bs.call(t, "GET", "/api/projects/shop/tasks/SF-9", "", 404, nil)
	bs.call(t, "GET", "/api/projects/shop/tasks/XX-1", "", 404, nil)
	bs.call(t, "POST", "/api/projects/shop/ok", "", 400, nil) // no ticket to approve
	bs.call(t, "POST", "/api/projects/shop/ok", `{"sendBack": "Smaller, please."}`, 200, nil)
	bs.call(t, "POST", "/api/projects/shop/tasks", `{"title": ""}`, 400, nil)
	var list struct {
		Projects []board.Project `json:"projects"`
	}
	bs.call(t, "GET", "/api/projects", "", 200, &list)
	if len(list.Projects) != 2 || list.Projects[0].Name != "fixture" {
		t.Fatalf("projects = %+v", list.Projects)
	}
}

// pushWork commits on branch and pushes it to the quarantine repository, as a builder's run does.
func pushWork(t *testing.T, g *gitstore.Store, name, branch string) {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("clone", "-q", g.Quarantine.Path(name), ".")
	run("checkout", "-q", "-b", branch)
	os.WriteFile(filepath.Join(work, "auth.go"), []byte("package auth\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "work")
	run("push", "-q", "origin", branch)
}
