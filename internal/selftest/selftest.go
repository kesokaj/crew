// Package selftest is `crew admin selftest` (design 16.11): one ticket on a project of its own, from
// its OK to Done, with the fake harness in place of agy. The board, the pipeline, the run driver, the
// Crew MCP, and the checks of the backend are the release's own; only the agents and the sandboxes
// are played. It needs no login, no microVM, and no network, so the gate and the install test run it
// on any machine. The project it makes is deleted again.
package selftest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/mcp"
	crewmodels "github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// The made-up operator the selftest's own API calls come from.
const (
	host  = "selftest.invalid"
	admin = "selftest@selftest.invalid"
	token = "selftest-ingress"
)

// Result is what a selftest says.
type Result struct {
	Project  string        `json:"project"`
	Ticket   string        `json:"ticket"`
	Done     bool          `json:"done"`
	Stage    string        `json:"stage"`
	Took     time.Duration `json:"took"`
	Turns    []string      `json:"turns"`
	Problems []string      `json:"problems,omitempty"`
}

type kit struct {
	pool  *pgxpool.Pool
	git   *gitstore.Store
	api   http.Handler
	mcp   http.Handler
	dir   string
	name  string
	mu    sync.Mutex
	turns []string
	probs []string
}

// Run makes a project, gives it one ticket and the OK, and waits up to wait for the ticket to be Done.
// version is the release's, recorded on its runs.
func Run(ctx context.Context, pool *pgxpool.Pool, version string, wait time.Duration) (res Result, err error) {
	start := time.Now()
	dir, err := os.MkdirTemp("", "crew-selftest-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := &kit{pool: pool, dir: dir, name: fmt.Sprintf("selftest-%d", start.Unix())}
	// Deferred first, so it runs last: the result says what every cleanup below could not do.
	defer func() {
		k.mu.Lock()
		res.Problems = append([]string(nil), k.probs...)
		k.mu.Unlock()
	}()
	k.git = &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}}
	identity := func(context.Context) (gitstore.Identity, error) {
		return gitstore.Identity{Name: "Crew selftest", Email: admin}, nil
	}
	b := &board.Board{Pool: pool, Git: k.git, Log: log, Identity: identity}
	fake := &harness.Fake{Next: k.agent, Check: check}
	fake.MakeSandbox = func(id string) error {
		_, err := pool.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ($1, 'run', $1, 'selftest')`, id)
		return err
	}
	// The fake harness needs no login, and the pipeline asks for one: each CLI is signed in for the
	// selftest, and its row is put back as it was after it, or taken out when it had none. The app
	// must not show a login that does not exist. (It is a tool of the install test: on a running Crew
	// a real run started meanwhile would meet the login check of its own sandbox.)
	for _, c := range cli.Names() {
		var had bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cli_logins WHERE cli = $1)`, c).Scan(&had); err != nil {
			return Result{}, err
		}
		if !had {
			if _, err := pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, version) VALUES ($1, 'signed_in', now(), 'selftest')`, c); err != nil {
				return Result{}, err
			}
			defer k.cleanup(ctx, "taking out the login row of "+c, `DELETE FROM cli_logins WHERE cli = $1 AND version = 'selftest'`, c)
			continue
		}
		var before []byte
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(l) FROM cli_logins l WHERE cli = $1`, c).Scan(&before); err != nil {
			return Result{}, err
		}
		if _, err := pool.Exec(ctx, `UPDATE cli_logins SET state = 'signed_in' WHERE cli = $1`, c); err != nil {
			return Result{}, err
		}
		defer k.cleanup(ctx, "putting back the login row of "+c, `UPDATE cli_logins l SET (state, failures, checked_at, result, version, updated_at) =
			(SELECT b.state, b.failures, b.checked_at, b.result, b.version, b.updated_at FROM jsonb_populate_record(l, $2::jsonb) b)
			WHERE l.cli = $1`, c, before)
	}
	// A machine whose agy has never listed its models, such as a fresh one, gets a list of two families
	// for the selftest, taken out again after it.
	listed, err := ensureModels(ctx, pool)
	if err != nil {
		return Result{}, err
	}
	if listed {
		defer k.cleanup(ctx, "taking out the models it lent", `DELETE FROM models WHERE cli = 'agy'`)
	}
	driver := &runs.Driver{Pool: pool, Harness: fake, Auth: auth{}, MCP: "/mcp", Version: version, Image: "selftest", Log: log}
	defer driver.Close()
	machine := func(ctx context.Context) (settings.Doc, error) {
		return settings.Effective(ctx, pool, settings.Machine{Version: version, AdminEmail: admin})
	}
	pipe := &pipeline.Pipeline{Pool: pool, Board: b, Runs: driver, Checks: fake, Settings: machine, Identity: identity, Version: version, Log: log}
	m, err := mcp.New(pool, driver.MCPCaller, log)
	if err != nil {
		return Result{}, err
	}
	tools := &pipeline.Tools{Pool: pool, Board: b, Kick: pipe.Kick}
	tools.Home = func(ctx context.Context, run string) (string, error) {
		var sandbox string
		if err := pool.QueryRow(ctx, `SELECT sandbox FROM runs WHERE id = $1`, run).Scan(&sandbox); err != nil {
			return "", err
		}
		return filepath.Join(dir, "homes", sandbox), nil
	}
	if err := tools.Register(m); err != nil {
		return Result{}, err
	}
	k.mcp = m
	s := &httpapi.Server{Machine: settings.Machine{Version: version, AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: log}
	s.Pool.Store(pool)
	s.Board.Store(b)
	s.Runs.Store(driver)
	k.api = s.Handler()

	res = Result{Project: k.name, Ticket: "ST-1"}
	if err := k.project(ctx); err != nil {
		return res, err
	}
	loops, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); b.Run(loops, 200*time.Millisecond) }()
	go func() { defer wg.Done(); pipe.Run(loops, 200*time.Millisecond) }()
	go func() { defer wg.Done(); driver.Monitor(loops, time.Second) }()
	// The project goes while the loops still run, so the runs that end after Done, its summary's,
	// can end; then the loops stop.
	defer func() { cancel(); wg.Wait(); pipe.Wait() }()
	defer k.remove(context.WithoutCancel(ctx), &res)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `SELECT t.stage FROM tickets t JOIN projects p ON p.id = t.project WHERE p.name = $1 AND t.number = 1`,
			k.name).Scan(&res.Stage); err != nil {
			return res, err
		}
		if res.Stage == "done" {
			res.Done = true
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !res.Done {
		// What the ticket says it waits for is what the selftest tells.
		var v struct {
			Ticket struct {
				Waiting, Stuck *string
			} `json:"ticket"`
		}
		if err := k.call("GET", "/api/projects/"+k.name+"/tasks/ST-1", "", 200, &v); err == nil {
			for _, s := range []*string{v.Ticket.Waiting, v.Ticket.Stuck} {
				if s != nil && *s != "" {
					k.problem("ST-1 in " + res.Stage + ": " + *s)
				}
			}
		} else {
			k.problem(err.Error())
		}
	}
	res.Took = time.Since(start).Round(time.Millisecond)
	k.mu.Lock()
	res.Turns, res.Problems = append([]string(nil), k.turns...), append([]string(nil), k.probs...)
	k.mu.Unlock()
	return res, nil
}

// call is one request to the API as the operator, through the ingress.
func (k *kit) call(method, path, body string, want int, out any) error {
	r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
	r.Header.Set(httpapi.HeaderIngress, token)
	r.Header.Set(httpapi.HeaderEmail, admin)
	r.Header.Set("Origin", "https://"+host)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	k.api.ServeHTTP(w, r)
	if w.Code != want {
		return fmt.Errorf("%s %s: %d %s", method, path, w.Code, strings.TrimSpace(w.Body.String()))
	}
	if out != nil {
		return json.Unmarshal(w.Body.Bytes(), out)
	}
	return nil
}

// project makes the project with one ticket that checks for its file, and gives the OK.
func (k *kit) project(ctx context.Context) error {
	if err := k.call("POST", "/api/projects", fmt.Sprintf(`{"name": %q, "key": "ST", "plan": "## Goal\n\nShow that a ticket reaches Done.\n",
		"stages": [], "repository": {"kind": "local"}}`, k.name), 201, nil); err != nil {
		return err
	}
	var doc map[string]any
	if err := k.call("GET", "/api/projects/"+k.name+"/settings", "", 200, &doc); err != nil {
		return err
	}
	if sb, ok := doc["sandbox"].(map[string]any); ok {
		sb["setup"] = ""
	}
	body, _ := json.Marshal(doc)
	if err := k.call("PUT", "/api/projects/"+k.name+"/settings", string(body), 200, nil); err != nil {
		return err
	}
	if err := k.call("POST", "/api/projects/"+k.name+"/tasks", `{"title": "Selftest", "why": "", "what": "add selftest.txt",
		"goal": "selftest.txt is on main", "checks": [{"kind": "cmd", "spec": "test -f selftest.txt"}], "docsReason": "none"}`, 201, nil); err != nil {
		return err
	}
	return k.call("POST", "/api/projects/"+k.name+"/ok", "", 200, nil)
}

// remove deletes the selftest's project with its rows, once its last runs have ended, and adds what
// went wrong to the result.
func (k *kit) remove(ctx context.Context, res *Result) {
	if err := k.call("POST", "/api/projects/"+k.name+"/archive", "", 200, nil); err != nil {
		k.problem("archiving the project: " + err.Error())
	}
	var err error
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		if err = k.call("DELETE", "/api/projects/"+k.name, fmt.Sprintf(`{"name": %q}`, k.name), 200, nil); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err != nil {
		k.problem("deleting the project: " + err.Error())
	}
	k.mu.Lock()
	res.Problems = append([]string(nil), k.probs...)
	k.mu.Unlock()
}

// cleanup runs one statement that puts back what the selftest changed, and tells when it could not.
func (k *kit) cleanup(ctx context.Context, what, sql string, args ...any) {
	if _, err := k.pool.Exec(context.WithoutCancel(ctx), sql, args...); err != nil {
		k.problem(what + ": " + err.Error())
	}
}

func (k *kit) problem(p string) {
	k.mu.Lock()
	k.probs = append(k.probs, p)
	k.mu.Unlock()
}

// The kinds of turn, by the words of their prompts.
var kinds = []struct{ marker, kind string }{
	{"# Analysis: make one ticket ready", "analysis"},
	{"# In progress: build one ticket", "build"},
	{"Write the summary of the ticket", "summary"},
}

var ticketKey = regexp.MustCompile(`this run works on its ticket (ST-\d+)`)

// agent plays the agent of each turn: the lead approves, the builder pushes the file and asks for
// the move, and every other turn answers and ends.
func (k *kit) agent(spec harness.RunSpec, _ int) harness.Script {
	kind := "other"
	for _, m := range kinds {
		if strings.Contains(spec.Prompt, m.marker) {
			kind = m.kind
		}
	}
	k.mu.Lock()
	k.turns = append(k.turns, kind)
	k.mu.Unlock()
	switch kind {
	case "analysis":
		k.tool(spec.Run, "analyse_ticket", map[string]any{"choice": "approve", "why": "the selftest", "docsReason": "none"})
	case "build":
		if m := ticketKey.FindStringSubmatch(spec.Prompt); m != nil {
			if err := k.push(m[1]); err != nil {
				k.problem("the builder's push: " + err.Error())
			}
		}
		k.tool(spec.Run, "add_note", map[string]any{"text": "Added selftest.txt"})
		k.tool(spec.Run, "request_move", map[string]any{})
	}
	return harness.Script{Lines: answer("Done.")}
}

// tool calls a tool of the Crew MCP as the run.
func (k *kit) tool(run, name string, args any) {
	var sandbox string
	if err := k.pool.QueryRow(context.Background(), `SELECT sandbox FROM runs WHERE id = $1`, run).Scan(&sandbox); err != nil {
		k.problem("the run's sandbox: " + err.Error())
		return
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+sandbox)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	k.mcp.ServeHTTP(w, r)
	var out struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Result.IsError {
		text := ""
		if len(out.Result.Content) > 0 {
			text = out.Result.Content[0].Text
		}
		k.problem(fmt.Sprintf("%s refused: %s %v", name, text, err))
	}
}

// push commits selftest.txt on the ticket's branch of the quarantine repository, as a run's git
// does through the endpoint.
func (k *kit) push(key string) error {
	var branch string
	if err := k.pool.QueryRow(context.Background(), `SELECT t.branch FROM tickets t JOIN projects p ON p.id = t.project
		WHERE p.name = $1 AND p.key || '-' || t.number = $2`, k.name, key).Scan(&branch); err != nil {
		return err
	}
	if branch == "" {
		return errors.New("the ticket has no branch")
	}
	work, err := os.MkdirTemp(k.dir, "work-")
	if err != nil {
		return err
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=agent",
			"GIT_AUTHOR_EMAIL=agent@selftest.invalid", "GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@selftest.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	for _, args := range [][]string{
		{"clone", "-q", k.git.Quarantine.Path(k.name), "."},
		{"checkout", "-q", "-B", branch, "origin/main"},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(work, "selftest.txt"), []byte("Crew's selftest\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "selftest.txt"}, {"commit", "-q", "-m", "Add selftest.txt"}, {"push", "-q", "origin", branch}} {
		if err := git(args...); err != nil {
			return err
		}
	}
	return nil
}

// check plays a check sandbox: `test -f F` passes when F is in the tree, and every other passes.
func check(spec harness.CheckSpec, c harness.CheckCmd) harness.CheckResult {
	if f, ok := strings.CutPrefix(c.Run, "test -f "); ok {
		if treeHas(spec.Tree, f) {
			return harness.CheckResult{Passed: true}
		}
		return harness.CheckResult{Code: 1, Output: f + " is missing"}
	}
	return harness.CheckResult{Passed: true, Output: "ok"}
}

// treeHas reports whether the tar of a check sandbox's tree holds the file name.
func treeHas(tree []byte, name string) bool {
	r := tar.NewReader(bytes.NewReader(tree))
	for {
		h, err := r.Next()
		if err != nil {
			return false
		}
		if h.Name == name {
			return true
		}
	}
}

// answer is a call of agy with no tools that answers with text.
func answer(text string) [][]byte {
	res, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": "5e1f7e57-0000-4000-8000-000000000001",
		"status": "SUCCESS", "response": text, "duration_seconds": 1.0, "num_turns": 1, "usage": map[string]any{"input_tokens": 100, "output_tokens": 10}}})
	return [][]byte{[]byte(`{"event":"init","conversation_id":"5e1f7e57-0000-4000-8000-000000000001","init":{"model":"selftest"}}`), res}
}

// models is the list agy printed on the operator's account (testdata/agy/models-gemini-4.txt): the
// baseline's entries name models of it, of both families, so a check runs on another family than the
// build (12.3).
const models = `gemini-4-argon-high	Gemini 4 Argon (High)
gemini-4-argon-medium	Gemini 4 Argon (Medium)
gemini-4-argon-low	Gemini 4 Argon (Low)
gemini-3.8-flash-high	Gemini 3.8 Flash (High)
gemini-3.8-flash-medium	Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low	Gemini 3.8 Flash (Low)
claude-opus-5-5-low	Claude Opus 5.5 (Low)
claude-opus-5-5-medium	Claude Opus 5.5 (Medium)
claude-opus-5-5-high	Claude Opus 5.5 (High)
claude-sonnet-5-5-low	Claude Sonnet 5.5 (Low)
claude-sonnet-5-5-medium	Claude Sonnet 5.5 (Medium)
claude-sonnet-5-5-high	Claude Sonnet 5.5 (High)
gpt-oss-120b-medium	GPT-OSS 120B (Medium)
`

// ensureModels records the list when agy has none, and says whether it did.
func ensureModels(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM models WHERE cli = 'agy'`).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	list, err := cli.Agy{}.ReadModels(ctx, func(context.Context, string, ...string) ([]byte, []byte, int, error) {
		return []byte(models), nil, 0, nil
	}, nil)
	if err != nil {
		return false, err
	}
	return true, modelsRecord(ctx, pool, list)
}

// auth lets every run of the fake harness in, as the broker lets in a sandbox it knows.
type auth struct{}

func (auth) Authorize(_ context.Context, _, _, token string) (sandboxes.Row, error) {
	if !strings.HasPrefix(token, "run-fake") {
		return sandboxes.Row{}, sandboxes.ErrRefused
	}
	return sandboxes.Row{ID: token}, nil
}

func modelsRecord(ctx context.Context, pool *pgxpool.Pool, list []cli.Model) error {
	return crewmodels.Record(ctx, pool, cli.AgyName, list, "")
}
