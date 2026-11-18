package projterm_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/projterm"
	"github.com/kesokaj/crew/internal/sandboxes"
)

const (
	shopID  = "00000000-0000-4000-8000-0000000000a1"
	otherID = "00000000-0000-4000-8000-0000000000a2"
	runID   = "00000000-0000-4000-8000-0000000000b1"
	ended   = "00000000-0000-4000-8000-0000000000b2"
)

// fakes plays the sandboxes, the microVMs, and the repositories.
type fakes struct {
	mu      sync.Mutex
	d       *dbtest.DB
	started []sandboxes.Spec
	removed []string
	execs   [][]string
	execIn  []string
	streams []*stream
	adopted []string
	held    []string
	dropped []string
	// dropDelay holds a decision between its git work and its record, for the test of two at once.
	dropDelay time.Duration
	comments  []string
	tokens    map[string]sandboxes.Row
	failNext  bool
	// clone, when set, is asked at the clone what the endpoint lets the new sandbox read, as the
	// clone in its microVM asks it.
	clone func(sandbox string) error
	// What a push to main changed, whether adopting it fails, and how often main was given back.
	changed    []string
	changedErr error
	fileErr    error
	adoptErr   error
	holdErr    error
	giveErr    error
	givenBack  int
	// files are main's files, such as an index of agy that names a folder of plugins.
	files map[string]string
	// ways are how an index file of main, or a path a link of main stands for, is reached through links.
	ways map[string]cli.Way
	// links are main's links, followed by ways; linkErr is a listing of them that fails.
	links   []string
	linkErr error
}

func (f *fakes) Start(ctx context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "terminal-" + string(rune('a'+len(f.started)))
	f.started = append(f.started, spec)
	if _, err := f.d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, project, container, image) VALUES ($1, 'terminal', $2, $1, 'img')`, id, spec.Project); err != nil {
		return sandboxes.Row{}, orchestrator.Sandbox{}, err
	}
	return sandboxes.Row{ID: id, Kind: spec.Kind, Project: spec.Project}, orchestrator.Sandbox{}, nil
}

func (f *fakes) Remove(ctx context.Context, id string) error {
	f.mu.Lock()
	f.removed = append(f.removed, id)
	f.mu.Unlock()
	_, err := f.d.Server.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1`, id)
	return err
}

func (f *fakes) Authorize(ctx context.Context, local, remote, token string) (sandboxes.Row, error) {
	if r, ok := f.tokens[token]; ok {
		return r, nil
	}
	return sandboxes.Row{}, sandboxes.ErrRefused
}

func (f *fakes) Exec(ctx context.Context, container string, spec orchestrator.ExecSpec) (int, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, spec.Cmd)
	f.execIn = append(f.execIn, container)
	if f.failNext {
		f.failNext = false
		return 128, "fatal: could not read from remote", nil
	}
	if slices.Contains(spec.Cmd, "--show-current") {
		return 0, "main\n", nil
	}
	if f.clone != nil && strings.Contains(strings.Join(spec.Cmd, " "), "git fetch -q origin\n") {
		f.mu.Unlock()
		err := f.clone(strings.TrimPrefix(container, "crew-"))
		f.mu.Lock()
		if err != nil {
			return 128, "fatal: could not read Username: " + err.Error(), nil
		}
	}
	return 0, "", nil
}

// stream echoes what is typed, as a shell with echo on.
type stream struct {
	container string
	cmd       []string
	in        chan []byte
	closed    chan struct{}
	once      sync.Once
}

func (s *stream) Read(p []byte) (int, error) {
	select {
	case b := <-s.in:
		return copy(p, b), nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *stream) Write(p []byte) (int, error) {
	b := append([]byte("echo:"), p...)
	select {
	case s.in <- b:
	case <-s.closed:
		return 0, io.EOF
	}
	return len(p), nil
}
func (s *stream) Resize(context.Context, uint, uint) error { return nil }
func (s *stream) Close()                                   { s.once.Do(func() { close(s.closed) }) }

func (f *fakes) Interactive(ctx context.Context, container string, spec orchestrator.ExecSpec, cols, rows uint) (projterm.Stream, error) {
	st := &stream{container: container, cmd: spec.Cmd, in: make(chan []byte, 16), closed: make(chan struct{})}
	f.mu.Lock()
	f.streams = append(f.streams, st)
	f.mu.Unlock()
	return st, nil
}

func (f *fakes) HoldMain(ctx context.Context, name, to, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holdErr != nil {
		return f.holdErr
	}
	f.held = append(f.held, name+" "+to+" "+key)
	f.givenBack++
	return nil
}

func (f *fakes) DropHeld(ctx context.Context, name, key string) error {
	time.Sleep(f.dropDelay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, key)
	return nil
}

func (f *fakes) AdvanceMain(ctx context.Context, name, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adoptErr != nil {
		return f.adoptErr
	}
	f.adopted = append(f.adopted, name+" "+from+" "+to)
	return nil
}

func (f *fakes) GiveBackMain(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.givenBack++
	return f.giveErr
}

func (f *fakes) ChangedFiles(ctx context.Context, name, from, to string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed, f.changedErr
}

func (f *fakes) IndexFile(ctx context.Context, name, path string) ([]byte, cli.Way, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.files[path]; ok {
		return []byte(b), f.ways[path], nil
	}
	return nil, f.ways[path], f.fileErr
}

// seal is the fake's key: a prefix that says the bytes went through Seal.
func seal(b []byte) ([]byte, error) { return append([]byte("sealed:"), b...), nil }

func setup(t *testing.T) (*projterm.Service, *fakes) {
	t.Helper()
	d := dbtest.New(t, false)
	ctx := context.Background()
	f := &fakes{d: d, tokens: map[string]sandboxes.Row{}}
	for _, q := range []string{
		`INSERT INTO projects (id, name, key, state, repo_source, remote) VALUES ('` + shopID + `', 'shop', 'SF', 'running', 'github', 'git@github.com:op/shop.git'),
			('` + otherID + `', 'other', 'OT', 'running', 'local', NULL)`,
		`INSERT INTO git_repos (name, project) VALUES ('shop', '` + shopID + `'), ('other', '` + otherID + `')`,
		`INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, branch) VALUES
			('` + shopID + `', 1, 'free', 'feature', 'in_progress', 'plan', 'lead', 1, 'task/SF-1-free'),
			('` + shopID + `', 2, 'busy', 'feature', 'review', 'plan', 'lead', 2, 'task/SF-2-busy'),
			('` + shopID + `', 3, 'done', 'feature', 'done', 'plan', 'lead', 3, 'task/SF-3-done'),
			('` + shopID + `', 4, 'new', 'feature', 'backlog', 'plan', 'lead', 4, NULL)`,
		`INSERT INTO sandboxes (id, kind, project, container, image) VALUES ('run-x', 'run', '` + shopID + `', 'run-x', 'img'), ('run-y', 'run', '` + shopID + `', 'run-y', 'img')`,
		`INSERT INTO subtasks (scope, project, ticket, number, kind, role) SELECT 'ticket', project, id, 1, 'review', 'reviewer' FROM tickets WHERE number = 2`,
		`INSERT INTO subtasks (scope, project, ticket, number, kind, role) SELECT 'ticket', project, id, 1, 'build', 'builder' FROM tickets WHERE number = 1`,
		`INSERT INTO conversations (id, purpose) VALUES ('` + runID + `', 'run'), ('` + ended + `', 'run')`,
		`INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, sandbox, conversation)
			SELECT '` + runID + `', s.id, 1, 'working', 'claude', 'v', 'img', 'run-x', '` + runID + `' FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 2`,
		`INSERT INTO runs (id, subtask, number, state, cli, crew_version, image, sandbox, conversation)
			SELECT '` + ended + `', s.id, 1, 'ended', 'claude', 'v', 'img', 'run-y', '` + ended + `' FROM subtasks s JOIN tickets t ON t.id = s.ticket WHERE t.number = 1`,
		`INSERT INTO cli_logins (cli, state) VALUES ('agy', 'signed_out'), ('claude', 'signed_in')`,
	} {
		if _, err := d.Server.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s := &projterm.Service{Pool: d.Server, Sandboxes: f, Runtime: f, Git: f, Seal: seal, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Comment: func(ctx context.Context, project, ticket, text string) error {
			f.mu.Lock()
			f.comments = append(f.comments, ticket+": "+text)
			f.mu.Unlock()
			return nil
		}}
	return s, f
}

func history(t *testing.T, f *fakes) []string {
	t.Helper()
	rows, err := f.d.Server.Query(context.Background(), `SELECT who || ' ' || tbl || ' ' || row_key || ' ' || coalesce(after::text, '') FROM changes ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
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

// The project terminal (13.11): one per project, a microVM of the terminal kind with the project's
// repository cloned with every branch and main checked out, opened in diagnostics mode, its opening,
// its switches, and its closing in the project's history.
func TestTheProjectTerminalOpensOncePerProject(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	if _, err := s.Get(ctx, "shop"); !errors.Is(err, projterm.ErrNotOpen) {
		t.Fatalf("a terminal before it was opened: %v", err)
	}
	// The clone reads the repository through the git endpoint with the sandbox's token: the
	// endpoint must know the terminal by then.
	f.clone = func(sandbox string) error {
		f.tokens["clone"] = sandboxes.Row{ID: sandbox, Kind: sandboxes.KindTerminal, Project: shopID}
		r := httptest.NewRequest("GET", "/git/shop.git/info/refs", nil)
		r.Header.Set("Authorization", "Bearer clone")
		_, err := s.GitAccess(r, "shop")
		return err
	}
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	f.clone = nil
	if term.Mode != projterm.ModeDiagnostics || term.Branch != "main" || !slices.Equal(term.Logins, []string{"claude"}) || term.Project != "shop" {
		t.Errorf("the terminal: %+v", term)
	}
	if len(f.started) != 1 || f.started[0].Kind != sandboxes.KindTerminal || f.started[0].Project != shopID || !slices.Equal(f.started[0].Repos, []string{"op/shop"}) {
		t.Fatalf("its microVM: %+v", f.started)
	}
	clone := strings.Join(f.execs[0], " ")
	if !strings.Contains(clone, "crew-run creds --quiet") || !strings.Contains(clone, "/git/shop.git") || !strings.Contains(clone, "checkout -q -B main origin/main") ||
		!strings.Contains(clone, "+refs/heads/*:refs/remotes/origin/*") || f.execIn[0] != "crew-terminal-a" {
		t.Errorf("the clone: %s in %s", clone, f.execIn[0])
	}
	again, err := s.Open(ctx, "shop", "", "operator")
	if err != nil || again.Sandbox != term.Sandbox || len(f.started) != 1 {
		t.Fatalf("a second open started another: %+v %v", again, err)
	}
	// A ticket's branch, while no agent runs on it; not while one does, and not before it has one.
	if _, err := s.Open(ctx, "shop", "SF-1", "operator"); err != nil {
		t.Fatal(err)
	}
	if last := strings.Join(f.execs[len(f.execs)-1], " "); !strings.Contains(last, "checkout -q -B \"$2\" \"origin/$2\"") || !strings.HasSuffix(last, "task/SF-1-free") {
		t.Errorf("the switch to the ticket's branch: %s", last)
	}
	for ticket, want := range map[string]string{"SF-2": "an agent runs on SF-2", "SF-4": "SF-4 has no branch yet", "SF-9": "has no ticket SF-9"} {
		var e *projterm.Error
		if _, err := s.Open(ctx, "shop", ticket, "operator"); !errors.As(err, &e) || !strings.Contains(e.Msg, want) {
			t.Errorf("%s: %v", ticket, err)
		}
	}
	if _, err := s.SetMode(ctx, "shop", "root", "operator"); err == nil {
		t.Error("a mode that is not one")
	}
	if term, err = s.SetMode(ctx, "shop", projterm.ModeEdit, "operator"); err != nil || term.Mode != projterm.ModeEdit {
		t.Fatalf("edit mode: %+v %v", term, err)
	}
	if err := s.Close(ctx, "shop", "operator", "closed by you"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "shop"); !errors.Is(err, projterm.ErrNotOpen) || !slices.Equal(f.removed, []string{"terminal-a"}) {
		t.Fatalf("after Close: %v, removed %v", err, f.removed)
	}
	h := history(t, f)
	if len(h) != 3 || !strings.Contains(h[0], `"opened": "terminal-a"`) || !strings.Contains(h[1], `{"mode": "edit"}`) || !strings.Contains(h[2], `"closed": "closed by you"`) {
		t.Errorf("the history: %q", h)
	}
	// A clone that fails removes the microVM again, and nothing is open.
	f.failNext = true
	if _, err := s.Open(ctx, "other", "", "operator"); err == nil || !strings.Contains(err.Error(), "could not be cloned") {
		t.Fatalf("a failed clone: %v", err)
	}
	if _, err := s.Get(ctx, "other"); !errors.Is(err, projterm.ErrNotOpen) || f.removed[len(f.removed)-1] != "terminal-b" {
		t.Errorf("after a failed clone: %v %v", err, f.removed)
	}
}

// What a terminal may do at the git endpoint: read every branch and tag of its own project; in edit
// mode push main, its own branches, and the branch of a ticket no agent runs on, that is not done.
func TestATerminalPushesOnlyInEditMode(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	f.tokens["tok"] = sandboxes.Row{ID: term.Sandbox, Kind: sandboxes.KindTerminal, Project: shopID}
	f.tokens["run"] = sandboxes.Row{ID: "run-x", Kind: sandboxes.KindRun, Project: shopID}
	ask := func(token, repo string) (gitendpoint.Access, error) {
		r := httptest.NewRequest("GET", "/git/"+repo+".git/info/refs", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return s.GitAccess(r, repo)
	}
	acc, err := ask("tok", "shop")
	if err != nil || len(acc.Push) != 0 || !slices.Equal(acc.Read, []string{"refs/heads/", "refs/tags/"}) || acc.Who != "terminal "+term.Sandbox {
		t.Fatalf("diagnostics: %+v %v", acc, err)
	}
	for _, c := range [][2]string{{"tok", "other"}, {"run", "shop"}, {"nothing", "shop"}, {"", "shop"}} {
		if _, err := ask(c[0], c[1]); !errors.Is(err, gitendpoint.ErrRefused) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, err := s.SetMode(ctx, "shop", projterm.ModeEdit, "operator"); err != nil {
		t.Fatal(err)
	}
	acc, err = ask("tok", "shop")
	if err != nil || !slices.Equal(acc.Push, []string{"refs/heads/main", projterm.OwnBranches, "refs/heads/task/SF-1-free"}) {
		t.Fatalf("edit mode: %+v %v", acc, err)
	}
	// A terminal that was closed and replaced by another is refused, even in edit mode.
	f.tokens["old"] = sandboxes.Row{ID: "terminal-old", Kind: sandboxes.KindTerminal, Project: shopID}
	if _, err := ask("old", "shop"); !errors.Is(err, gitendpoint.ErrRefused) {
		t.Errorf("an old terminal: %v", err)
	}
}

// After a push from the terminal: main is held for the operator's confirmation, and once taken it
// becomes the new base and goes on to the remote; a ticket's branch tells the next agent; the
// operator's own branch and a run's push do neither.
func TestWhatFollowsATerminalsPush(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	kicked := 0
	s.Kick = func() { kicked++ }
	acc := gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}
	s.Pushed(acc, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	s.Pushed(acc, "refs/heads/task/SF-1-free", "ccccccc", "ddddddd")
	s.Pushed(acc, "refs/heads/you/try", "", "eeeeeee")
	s.Pushed(gitendpoint.Access{Repo: "shop", Who: "run x", Run: runID}, "refs/heads/main", "1", "2")
	s.Pushed(gitendpoint.Access{Repo: "shop", Who: "terminal gone"}, "refs/heads/main", "3", "4")
	// Main waits: nothing became the base, nothing went to the remote, and the endpoint serves
	// Crew's main again.
	held, err := projterm.Held(ctx, f.d.Server, "")
	if err != nil || len(held) != 1 || held[0].New != "bbbbbbb" || held[0].Project != "shop" || len(f.held) != 1 {
		t.Fatalf("held %+v %v %v", held, f.held, err)
	}
	if len(f.adopted) != 0 || kicked != 0 || f.givenBack != 1 {
		t.Errorf("before the confirmation: adopted %v, kicked %d, given back %d", f.adopted, kicked, f.givenBack)
	}
	if len(f.comments) != 1 || !strings.HasPrefix(f.comments[0], "SF-1: I pushed to this ticket's branch myself") || !strings.Contains(f.comments[0], "ddddddd") {
		t.Errorf("the comments: %q", f.comments)
	}
	if h := history(t, f); !strings.Contains(h[len(h)-1], `"held": "bbbbbbb"`) {
		t.Errorf("the history: %q", h)
	}
	// Taken, it becomes main and goes on to the remote; it cannot be decided twice.
	if err := s.DecideMain(ctx, "shop", held[0].ID, true, "op@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideMain(ctx, "shop", held[0].ID, true, "op@example.com"); !errors.Is(err, projterm.ErrNotHeld) {
		t.Fatalf("decided twice: %v", err)
	}
	if !slices.Equal(f.adopted, []string{"shop aaaaaaa bbbbbbb"}) || kicked != 1 || len(f.dropped) != 1 {
		t.Errorf("taken: adopted %v, kicked %d, dropped %v", f.adopted, kicked, f.dropped)
	}
	var pushes int
	f.d.Server.QueryRow(ctx, `SELECT count(*) FROM pushes WHERE project = $1 AND remote = 'git@github.com:op/shop.git'`, shopID).Scan(&pushes)
	if pushes != 1 {
		t.Errorf("%d pushes to the remote", pushes)
	}
	if h := history(t, f); !strings.Contains(h[len(h)-1], `"main": "bbbbbbb"`) || !strings.Contains(h[len(h)-1], "confirmed") {
		t.Errorf("the history: %q", h)
	}
	// Refused, it is forgotten; a main that cannot become main any more, as one main moved under, is
	// recorded as failed.
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ccccccc")
	held, _ = projterm.Held(ctx, f.d.Server, shopID)
	if err := s.DecideMain(ctx, "shop", held[0].ID, false, "op@example.com"); err != nil {
		t.Fatal(err)
	}
	if h := history(t, f); len(f.adopted) != 1 || !strings.Contains(h[len(h)-1], `"decided": "refused"`) {
		t.Errorf("refused: adopted %v, %q", f.adopted, h[len(h)-1])
	}
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ddddddd")
	held, _ = projterm.Held(ctx, f.d.Server, shopID)
	f.adoptErr = errors.New("main moved meanwhile")
	if err := s.DecideMain(ctx, "shop", held[0].ID, true, "op@example.com"); err == nil || !strings.Contains(err.Error(), "main moved meanwhile") {
		t.Fatalf("a main that moved: %v", err)
	}
	if h := history(t, f); !strings.Contains(h[len(h)-1], `"decided": "failed"`) || kicked != 1 {
		t.Errorf("the history of a main that could not be taken: %q", h[len(h)-1])
	}
	f.adoptErr = nil

	// A main that changes a file that is code for the agent CLI, or a workflow, is held like any other,
	// with those files named, so the operator decides with them in view (13.8, 13.11).
	codeOf := func(n int) []string {
		var code []string
		if err := f.d.Server.QueryRow(ctx, `SELECT code FROM terminal_mains ORDER BY id DESC LIMIT 1 OFFSET $1`, n).Scan(&code); err != nil {
			t.Fatal(err)
		}
		return code
	}
	f.changed = []string{"README.md", ".github/workflows/ci.yml"}
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ccccccc")
	if h := history(t, f); len(f.held) != 4 || !strings.Contains(h[len(h)-1], `"held": "ccccccc"`) || !strings.Contains(h[len(h)-1], ".github/workflows/ci.yml") {
		t.Errorf("a main with code files: held %v, %q", f.held, h[len(h)-1])
	}
	if code := codeOf(0); len(code) != 1 || code[0] != ".github/workflows/ci.yml" {
		t.Errorf("the held main's code files: %v", code)
	}
	// A folder that main's index of plugins names is code too.
	f.files = map[string]string{".agents/plugins.json": `{"entries": [{"path": "tools/plug"}]}`}
	f.changed = []string{"tools/plug/hook.sh"}
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "eeeeeee")
	if code := codeOf(0); len(f.held) != 5 || len(code) != 1 || code[0] != "tools/plug/hook.sh" {
		t.Errorf("a main that changes a folder an index names: held %v, code %v", f.held, code)
	}
	// When what a push changed cannot be read, it is not taken (the guard fails closed). When Crew's
	// main cannot be given back, the history says the endpoint still serves the other.
	f.files, f.changed, f.changedErr, f.giveErr = nil, []string{"README.md"}, errors.New("no such commit"), errors.New("the disk is full")
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "fffffff")
	if h := history(t, f); len(f.held) != 5 || !strings.Contains(h[len(h)-1], "what it changed could not be read") ||
		!strings.Contains(h[len(h)-1], "still serves that main") || !strings.Contains(h[len(h)-1], "the disk is full") {
		t.Errorf("a push whose changes cannot be read: held %v, %q", f.held, h[len(h)-1])
	}
	f.changedErr, f.giveErr = nil, nil
	// A main Crew could not hold is recorded, not only logged.
	f.changed, f.holdErr = []string{"README.md"}, errors.New("the disk is full")
	s.Pushed(acc, "refs/heads/main", "bbbbbbb", "ddddddd")
	if h := history(t, f); !strings.Contains(h[len(h)-1], `"notTaken": "ddddddd"`) || !strings.Contains(h[len(h)-1], "the disk is full") {
		t.Errorf("the history of a main that could not be held: %q", h[len(h)-1])
	}
}

// A terminal with no input for the idle time is closed, and the log keeps 180 days.
func TestAnIdleTerminalIsClosed(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	if _, err := s.Open(ctx, "shop", "", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "other", "", "operator"); err != nil {
		t.Fatal(err)
	}
	f.d.Server.Exec(ctx, `UPDATE project_terminals SET input_at = now() - interval '31 minutes' WHERE project = $1`, shopID)
	f.d.Server.Exec(ctx, `INSERT INTO terminal_log (project, sandbox, dir, data, at) VALUES ($1, 'x', 'out', 'old', now() - interval '181 days'),
		($1, 'x', 'out', 'new', now() - interval '179 days')`, shopID)
	s.Reap(ctx)
	if _, err := s.Get(ctx, "shop"); !errors.Is(err, projterm.ErrNotOpen) {
		t.Errorf("the idle terminal is open: %v", err)
	}
	if _, err := s.Get(ctx, "other"); err != nil {
		t.Errorf("the busy terminal was closed: %v", err)
	}
	var kept []string
	rows, _ := f.d.Server.Query(ctx, `SELECT convert_from(data, 'UTF8') FROM terminal_log ORDER BY id`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		kept = append(kept, s)
	}
	rows.Close()
	if !slices.Equal(kept, []string{"new"}) {
		t.Errorf("the log kept %v", kept)
	}
	if h := history(t, f); !strings.Contains(h[len(h)-1], "no input for 30m0s") {
		t.Errorf("the history: %q", h)
	}
}

// Attaching: from a page of Crew's own address only; the project terminal's tmux session, or with a
// run that has not ended, that run's microVM; what is typed and printed goes both ways and is kept.
func TestAttachingToATerminal(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	if _, err := s.Open(ctx, "shop", "", "operator"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Attach(w, r, "crew.example", "shop", r.URL.Query().Get("run"), "operator")
	}))
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	dial := func(q, origin string) (*websocket.Conn, *http.Response, error) {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return websocket.Dial(dctx, ws+q, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
	}
	if _, resp, err := dial("", "https://elsewhere.example"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a page of another address: %v", err)
	}
	for q, want := range map[string]int{"?run=" + ended: http.StatusConflict, "?run=" + otherID: http.StatusConflict} {
		if _, resp, err := dial(q, "https://crew.example"); err == nil || resp == nil || resp.StatusCode != want {
			t.Errorf("%s: %v", q, err)
		}
	}
	// Edit mode lasts while a page is on the terminal.
	if _, err := s.SetMode(ctx, "shop", projterm.ModeEdit, "operator"); err != nil {
		t.Fatal(err)
	}
	c, _, err := dial("", "https://crew.example")
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Write(rctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.Read(rctx); err != nil || string(got) != "echo:ls\r" {
		t.Fatalf("the terminal printed %q %v", got, err)
	}
	c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "diagnostics after the page went", func() bool {
		term, err := s.Get(ctx, "shop")
		return err == nil && term.Mode == projterm.ModeDiagnostics
	})
	// Into the running run's microVM: its own tmux session there, and the jump is in its ticket's
	// history.
	j, _, err := dial("?run="+runID, "https://crew.example")
	if err != nil {
		t.Fatal(err)
	}
	j.Write(rctx, websocket.MessageBinary, []byte("ps\r"))
	j.Read(rctx)
	j.Close(websocket.StatusNormalClosure, "")
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	streams := f.streams
	f.mu.Unlock()
	if len(streams) != 2 || streams[0].container != "crew-terminal-a" || !slices.Contains(streams[0].cmd, "crew-terminal") ||
		streams[1].container != "crew-run-x" || !slices.Contains(streams[1].cmd, "crew-jump") {
		t.Fatalf("the streams: %+v %+v", streams[0], streams[len(streams)-1])
	}
	var logs []string
	rows, _ := f.d.Server.Query(ctx, `SELECT dir || ' ' || sandbox || ' ' || coalesce(run::text, '-') || ' ' || sealed || ' ' || convert_from(data, 'UTF8') FROM terminal_log ORDER BY id`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		logs = append(logs, s)
	}
	rows.Close()
	want := []string{"in terminal-a - true sealed:ls\r", "out terminal-a - true sealed:echo:ls\r", "in run-x " + runID + " true sealed:ps\r",
		"out run-x " + runID + " true sealed:echo:ps\r"}
	if !slices.Equal(logs, want) {
		t.Errorf("the log %q", logs)
	}
	if h := history(t, f); !strings.Contains(h[len(h)-1], "tickets SF-2") || !strings.Contains(h[len(h)-1], "opened inside run "+runID) {
		t.Errorf("the ticket's history: %q", h)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(end) {
			t.Fatalf("waited for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// One attachment's log is kept up to its limit; past it, one row says the rest was not kept.
func TestATerminalsLogHasALimit(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	s.MaxLog = 64
	if _, err := s.Open(ctx, "shop", "", "operator"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Attach(w, r, "crew.example", "shop", "", "operator")
	}))
	defer srv.Close()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://crew.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		c.Write(dctx, websocket.MessageBinary, []byte(strings.Repeat("x", 30)))
		c.Read(dctx)
	}
	c.Close(websocket.StatusNormalClosure, "")
	var kept int
	var last string
	waitFor(t, "the log's last row", func() bool {
		f.d.Server.QueryRow(ctx, `SELECT coalesce(sum(length(data) - 7), 0), coalesce((SELECT convert_from(data, 'UTF8') FROM terminal_log ORDER BY id DESC LIMIT 1), '')
			FROM terminal_log`).Scan(&kept, &last)
		return strings.Contains(last, "was not kept")
	})
	if kept > 64+200 {
		t.Errorf("kept %d bytes past a limit of 64", kept)
	}
}

// Two decisions on one held main at once: one decides, the other finds it decided, and main moves
// and goes to the remote once (reviews of M8).
func TestAHeldMainIsDecidedOnce(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	term, err := s.Open(ctx, "shop", "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	s.Pushed(gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	held, _ := projterm.Held(ctx, f.d.Server, shopID)
	f.dropDelay = 100 * time.Millisecond
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.DecideMain(ctx, "shop", held[0].ID, i%2 == 0, "op@example.com")
		}()
	}
	wg.Wait()
	decided := 0
	for _, err := range errs {
		switch {
		case err == nil:
			decided++
		case !errors.Is(err, projterm.ErrNotHeld):
			t.Fatal(err)
		}
	}
	var pushes, histories int
	f.d.Server.QueryRow(ctx, `SELECT count(*) FROM pushes WHERE project = $1`, shopID).Scan(&pushes)
	f.d.Server.QueryRow(ctx, `SELECT count(*) FROM changes WHERE tbl = 'terminals' AND after ? 'decided' OR after->>'from' LIKE '%confirmed'`).Scan(&histories)
	if decided != 1 || len(f.adopted) > 1 || pushes > 1 || histories != 1 {
		t.Fatalf("decided %d, adopted %v, pushes %d, histories %d", decided, f.adopted, pushes, histories)
	}
}

// A decision that began and never ended, as when the server stopped in the middle of it, leaves the
// main to be decided again after a while (round two of the review of M8).
func TestAHeldMainWhoseDecisionNeverEndedWaitsAgain(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	term, _ := s.Open(ctx, "shop", "", "operator")
	s.Pushed(gitendpoint.Access{Repo: "shop", Who: "terminal " + term.Sandbox}, "refs/heads/main", "aaaaaaa", "bbbbbbb")
	f.d.Server.Exec(ctx, `UPDATE terminal_mains SET state = 'deciding', decided_at = now()`)
	if held, _ := projterm.Held(ctx, f.d.Server, shopID); len(held) != 0 {
		t.Fatal("a main being decided is offered to decide")
	}
	f.d.Server.Exec(ctx, `UPDATE terminal_mains SET decided_at = now() - interval '11 minutes'`)
	held, _ := projterm.Held(ctx, f.d.Server, shopID)
	if len(held) != 1 {
		t.Fatal("a decision that never ended left the main undecidable")
	}
	if err := s.DecideMain(ctx, "shop", held[0].ID, false, "op"); err != nil {
		t.Fatal(err)
	}
}

// The gate's project crew-gate is Crew's own and hidden, and its sandboxes may start from a
// candidate's runtime image while a gate's live steps run (16.2 step 5): it has no terminal, which
// would get the agent CLI's login in that image.
func TestAHiddenProjectHasNoTerminal(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE projects SET hidden = true WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "shop", "", "operator"); err == nil || !strings.Contains(err.Error(), "no such project") {
		t.Fatalf("a hidden project's terminal: %v", err)
	}
	if len(f.started) != 0 {
		t.Fatalf("a microVM started: %+v", f.started)
	}
}
