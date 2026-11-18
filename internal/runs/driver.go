// Package runs is the run driver's part in the backend (design 11.2, 12.4, 12.10): it starts a run
// with its subtask, conversation, and saved prompt, writes every event of its trajectory, the turns
// of its conversation, and its CLI's own files to the database, watches it (8.2), stops it with its
// work kept, and continues it in each of the three ways of 12.10. The harness (12.5) does the work in
// the microVM; nothing here names a CLI: what differs is asked of the CLI's adapter.
package runs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/redact"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
)

// The fixture project: the project of the runs the operator starts without a ticket. It is a row of
// projects since M4 (schema step 5), and its repositories have this name (6.1).
const (
	FixtureProject = "f1c50000-0000-4000-8000-000000000001"
	FixtureRepo    = "fixture"
)

// The watchdog's numbers (8.2).
const (
	SilentWarn   = 5 * time.Minute
	SilentStop   = 15 * time.Minute
	FailWarn     = 3 // the same action failing in a row
	FailStop     = 6
	StopWait     = 90 * time.Second
	DefaultIdle  = 10 * time.Minute
	rebuildTurns = 6 // the last turns a rebuilt conversation gets in full, until summaries exist (M6)
)

// StoreGrace is how long events may fail to be stored before the run is stopped (8.3).
var StoreGrace = 2 * time.Minute

// Stop reasons the backend sets itself (12.4).
const (
	ReasonOperator = "operator"
	ReasonRestart  = "restart"
	ReasonSilent   = "silent"
	ReasonTurns    = "turns"
	ReasonSandbox  = "sandbox"
	// ReasonRounds ends a run whose goal loop reached its limit, or whose call that reports was still
	// missing after a reminder (11.3): it counts as a stop, and is retried once.
	ReasonRounds = "rounds"
)

// counting are the reasons that count as a stop: retried once in a new sandbox (12.4).
var counting = map[string]bool{
	cli.ReasonTimeout: true, ReasonSilent: true, ReasonTurns: true, ReasonRounds: true, ReasonBudget: true,
	cli.ReasonError: true, cli.ReasonRefused: true, ReasonSandbox: true,
}

// Logins is what the driver asks of the login check (12.6).
type Logins interface {
	CheckNow(ctx context.Context, name string, real bool) bool
}

// Sealer seals data for a purpose with the key file, and opens it again: credstore.Vault.
type Sealer interface {
	Seal(purpose string, data []byte) ([]byte, error)
	Open(purpose string, blob []byte) ([]byte, error)
}

// Authorizer knows a sandbox by its door, its address, and its token: sandboxes.Manager.
type Authorizer interface {
	Authorize(ctx context.Context, local, remote, token string) (sandboxes.Row, error)
}

// Driver drives runs.
type Driver struct {
	Pool    *pgxpool.Pool
	Harness harness.Harness
	// Secrets are the values handed to a sandbox, which are removed from what is stored of it (8.3).
	Secrets func(ctx context.Context, sandbox string) []string
	Logins  Logins
	Auth    Authorizer
	// Seal seals a conversation's work folder and every run's CLI files with the key file before they
	// are stored, and opens them to put them back (11.8, 12.10, D41): the vault. With none, neither is
	// stored.
	Seal Sealer
	// Sandboxes lists and removes the sandboxes, so a run sandbox no live run follows is removed
	// when the server starts (12.4).
	Sandboxes SandboxLister
	Version   string // Crew's version, on every run (11.2)
	Image     string // the runtime image
	Log       *slog.Logger
	// IdleFor is how long a run whose turn ended well keeps its microVM for another turn in it.
	IdleFor time.Duration
	// MCP is the path of Crew's MCP server on a sandbox's door, given to the runs of the roles that
	// have its tools; "" gives it to none (11.5).
	MCP string
	Now func() time.Time
	// LimitsFor gives the watchdog's numbers of a subtask's run, from its project's settings; nil keeps
	// the design's numbers.
	LimitsFor func(ctx context.Context, subtask int64) (Limits, error)
	// Room is the capacity check (7.9, R71): a run starts only when its sandbox fits on the machine,
	// and its sandbox gets the allowance of its role's size. nil checks nothing.
	Room *capacity.Gate
	// Probe starts a microVM that runs nothing and removes it, so its boot is counted (21, D11): the
	// monitor starts one every ProbeEvery while microVMs do not boot. nil starts none.
	Probe func(ctx context.Context) error

	mu         sync.Mutex
	live       map[string]*liveRun
	nextChoice NextChoice
	busy       map[int64]bool // subtasks being started or continued
	probing    bool           // a probe runs
	probedAt   time.Time      // when the last probe started
	// folderMu holds one conversation's work folder in memory at a time while it is scrubbed (D17).
	folderMu sync.Mutex
}

// liveRun is what the driver keeps in memory of a run it follows.
type liveRun struct {
	id           string
	subtask      int64
	conversation string
	cli          string
	maxTurns     int
	redact       *redact.Redactor
	quit         chan struct{}

	mu         sync.Mutex
	stopping   string
	stopDetail string
	stopAt     time.Time
	speaks     strings.Builder
	calls      []string
	steps      int
	toolByID   map[string]string
	lastFailed string
	failStreak int
	warned     map[string]bool
	pendingEnd *pendingEnd
	// stopAll says Stop all ended the run: its subtask waits for Resume (7.9).
	stopAll bool
	// idle is how long the run waits for its next turn, read when it started: a conversation's
	// conversations.idle_minutes (11.8); 0 is the driver's IdleFor.
	idle time.Duration
	// folder is the work folder of turn folderTurn as it was read, with its note, so an end that is
	// written again does not read its removed file (D17).
	folder     *workFolder
	folderNote string
	folderTurn int
	// setup says the run's sandbox runs the project's setup before its CLI: only then is the
	// reason setup taken from its home (review of M13).
	setup bool
	// What the watchdog holds the run to (8.2).
	limits Limits
}

func (d *Driver) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Driver) idleFor(lr *liveRun) time.Duration {
	if lr != nil && lr.idle > 0 {
		return lr.idle
	}
	if d.IdleFor > 0 {
		return d.IdleFor
	}
	return DefaultIdle
}

// conversationIdle is how long a conversation's sandbox waits for the next message: the machine's
// conversations.idle_minutes, or the listing's 30 when it cannot be read (11.8, 14.1).
func (d *Driver) conversationIdle(ctx context.Context) time.Duration {
	v, err := settings.MachineValue(ctx, d.Pool, "conversations.idle_minutes")
	if n, ok := v.(int); err == nil && ok && n >= 5 && n <= 240 {
		return time.Duration(n) * time.Minute
	}
	d.Log.Warn("reading conversations.idle_minutes: a conversation waits 30 minutes", "value", v, "error", err)
	return 30 * time.Minute
}

// Close stops following the runs, as a server that stops: they go on in their microVMs, and a server
// that starts again follows them on with Reconcile.
func (d *Driver) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, lr := range d.live {
		close(lr.quit)
		delete(d.live, id)
	}
}

func (d *Driver) getLive(id string) (*liveRun, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.live[id]
	return r, ok
}

func (d *Driver) claim(subtask int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy == nil {
		d.busy = map[int64]bool{}
	}
	if d.busy[subtask] {
		return false
	}
	d.busy[subtask] = true
	return true
}

func (d *Driver) release(subtask int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.busy, subtask)
}

// newUUID is a random UUID of version 4.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// StartRequest is a run the operator starts on the fixture project.
type StartRequest struct {
	CLI, Model, Effort string
	Title              string
	Prompt             string
	NoTools            bool
	Timeout            time.Duration
	MaxTurns           int
	// WithoutLogin gives the first run no login of its CLI, so it stops for a sign-in and continues
	// after it (12.4); the run that continues gets the login.
	WithoutLogin bool
	// Minimal starts its microVM without Docker inside.
	Minimal bool
}

// ErrNotSignedIn is returned for a run on a CLI whose login does not work (12.6).
var ErrNotSignedIn = errors.New("the CLI is not signed in")

// Start starts a run on the fixture project, with a subtask that has no ticket (11.2).
func (d *Driver) Start(ctx context.Context, req StartRequest) (string, error) {
	a, ok := cli.Get(req.CLI)
	if !ok {
		return "", fmt.Errorf("no CLI %q", req.CLI)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("a run needs a prompt")
	}
	if !req.WithoutLogin && !d.signedIn(ctx, a.Name()) {
		return "", ErrNotSignedIn
	}
	if why := hold.Held(ctx, d.Pool); why != "" {
		return "", fmt.Errorf("%w: %s", ErrHeld, why)
	}
	if req.Timeout <= 0 {
		req.Timeout = time.Hour
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = 200
	}
	runID, convID := newUUID(), newUUID()
	room, err := d.takeRoom(ctx, d.roomAsk(ctx, "operator-run", "", "builder", FixtureProject, "", 0, "the operator's run"))
	if err != nil {
		return "", err
	}
	defer room(false)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = firstLine(req.Prompt, 80)
	}
	var subtask int64
	err = pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO subtasks (scope, project, number, kind, role, state, title)
			VALUES ('project', $1, (SELECT coalesce(max(number), 0) + 1 FROM subtasks WHERE project = $1), 'run', 'builder', 'running', $2)
			RETURNING id`, FixtureProject, title).Scan(&subtask); err != nil {
			return err
		}
		branch := fmt.Sprintf("subtask/%d", subtask)
		if _, err := tx.Exec(ctx, `UPDATE subtasks SET branch = $2 WHERE id = $1`, subtask, branch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'run', $3)`,
			convID, FixtureProject, a.Name()); err != nil {
			return err
		}
		return d.insertRun(ctx, tx, newRun{
			id: runID, subtask: subtask, number: 1, cli: a.Name(), model: req.Model, effort: req.Effort,
			conversation: convID, way: "new", branch: branch, base: "refs/heads/main", timeout: req.Timeout,
			maxTurns: req.MaxTurns, tools: !req.NoTools, prompt: req.Prompt, speaker: "operator", size: capacity.SizeOf("builder"),
		})
	})
	if err != nil {
		return "", fmt.Errorf("recording the run: %w", err)
	}
	room(true)
	// The fixture project's network mode, as for every run of a project (13.6).
	mode, err := settings.NetworkMode(ctx, d.Pool, FixtureProject, 0)
	if err != nil {
		return "", err
	}
	spec := harness.RunSpec{
		Run: runID, Subtask: subtask, Project: FixtureProject, Repo: FixtureRepo, Branch: fmt.Sprintf("subtask/%d", subtask),
		Base: "refs/heads/main", CLI: a.Name(), Model: req.Model, Effort: req.Effort, Prompt: req.Prompt, Tools: !req.NoTools,
		Limits:       harness.Limits{Timeout: req.Timeout, MaxTurns: req.MaxTurns},
		WithoutLogin: req.WithoutLogin, Minimal: req.Minimal, Mode: mode,
	}
	d.sized(ctx, "builder", &spec)
	d.start(ctx, runID, spec)
	return runID, nil
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// signedIn says the CLI's login worked at its last check (12.6).
func (d *Driver) signedIn(ctx context.Context, name string) bool {
	var state string
	err := d.Pool.QueryRow(ctx, `SELECT state FROM cli_logins WHERE cli = $1`, name).Scan(&state)
	return err == nil && state == "signed_in"
}

// start starts a recorded run in the harness and follows it. A microVM that does not start ends the
// run with the reason sandbox.
func (d *Driver) start(ctx context.Context, runID string, spec harness.RunSpec) {
	a, _ := cli.Get(spec.CLI)
	lr := &liveRun{id: runID, subtask: spec.Subtask, cli: a.Name(), maxTurns: spec.Limits.MaxTurns,
		quit: make(chan struct{}), toolByID: map[string]string{}, warned: map[string]bool{}, limits: DefaultLimits(),
		setup: spec.Setup != ""}
	lr.redact = redact.New()
	if err := d.Pool.QueryRow(ctx, `SELECT conversation FROM runs WHERE id = $1`, runID).Scan(&lr.conversation); err != nil {
		d.Log.Warn("reading the run's conversation", "run", runID, "error", err)
	}
	if spec.Conversation || spec.Chat {
		lr.idle = d.conversationIdle(ctx)
	}
	d.arm(ctx, lr)
	spec.OnSandbox = func(sandbox, hash string) error {
		var secrets []string
		if d.Secrets != nil {
			secrets = d.Secrets(ctx, sandbox)
		}
		lr.redact = redact.New(secrets...)
		return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE runs SET sandbox = $2 WHERE id = $1`, runID, sandbox); err != nil {
				return err
			}
			// The token's role and its one project or plan draft are the subtask's: the Crew MCP and
			// the git endpoint decide by them (11.5).
			_, err := tx.Exec(ctx, `INSERT INTO run_tokens (sha256, run, role, project, draft, ends_at)
				SELECT $1, r.id, s.role, CASE WHEN s.draft IS NULL THEN s.project END, s.draft, now() + interval '24 hours'
				FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $2
				ON CONFLICT DO NOTHING`, hash, runID)
			return err
		})
	}
	d.mu.Lock()
	if d.live == nil {
		d.live = map[string]*liveRun{}
	}
	d.live[runID] = lr
	d.mu.Unlock()
	// Stop all writes its state before it reads the runs that are going, so a run it did not see is
	// registered after that and reads the state here: it ends as Stop all would have ended it. A
	// state that cannot be read ends it too, and the monitor continues it once Crew may start again.
	if st, err := hold.Read(ctx, d.Pool); err != nil || st.Why == "stop" {
		lr.mu.Lock()
		lr.stopAll = true
		lr.mu.Unlock()
		d.endRun(context.WithoutCancel(ctx), lr, ReasonOperator, "Stop all came as the run started", nil, false)
		return
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO run_spools (run, turn) VALUES ($1, 1) ON CONFLICT DO NOTHING`, runID); err != nil {
		d.Log.Error("recording the run's spool", "run", runID, "error", err)
	}
	if !spec.Judge {
		if customFiles, err := board.CustomHarnessSkeleton(ctx, d.Pool); err == nil && len(customFiles) > 0 {
			if spec.Files == nil {
				spec.Files = map[string][]byte{}
			}
			for k, v := range customFiles {
				spec.Files[k] = v
			}
		}
	}
	if _, err := d.Harness.StartRun(ctx, spec); err != nil {
		d.Log.Error("the run did not start", "run", runID, "error", err)
		d.endRun(context.WithoutCancel(ctx), lr, ReasonSandbox, NoStart, nil, false)
		return
	}
	d.Log.Info("run started", "run", runID, "subtask", spec.Subtask, "cli", spec.CLI, "model", spec.Model)
	go d.follow(context.WithoutCancel(ctx), lr)
}

// newRun is a run to record.
type newRun struct {
	id, cli, model, effort, conversation, way, branch, base, continues string
	subtask                                                            int64
	number                                                             int
	timeout                                                            time.Duration
	maxTurns                                                           int
	tools                                                              bool
	prompt, speaker                                                    string
	rebuilt                                                            bool
	// parts are the prompt's parts with their sources (8.6); nil is the one part of its speaker.
	parts []PromptPart
	// name is what the CLI is given as its model, as it lists it; "" is model (12.3).
	name string
	// modelFrom and effortFrom say where the model and the effort came from (12.3); "" is the
	// operator.
	modelFrom, effortFrom string
	// readOnly says the run pushes nothing (6.1).
	readOnly bool
	// size is the size of its sandbox, small or large (13.10).
	size string
	// judge says the run starts in a judge sandbox, which never takes its project's runtime image.
	judge bool
	// commit is the commit a read-only run reads in place of its branch's head: a chat's of a Done
	// ticket (11.9).
	commit string
}

// PromptPart is one part of a saved prompt (8.6): what it is, where it came from with its version,
// and its text.
type PromptPart struct {
	Part   string
	Source string
	Text   string
}

// Join puts the parts of a prompt together, each after the one before with a blank line.
func Join(parts []PromptPart) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(p.Text))
	}
	return b.String()
}

// insertRun records a run, its saved prompt (8.6), and the turn that starts it (12.10).
func (d *Driver) insertRun(ctx context.Context, tx pgx.Tx, r newRun) error {
	// A run of the gate's project, and an eval's run of a release's suite, records the candidate's
	// runtime image it runs in (16.2 step 5). A judge's sandbox takes no project's image, as the
	// sandboxes decide: the gate's judges run in the running release's (13.4; GATE-7 and GATE-8 on dave
	// recorded the candidate's).
	image := d.Image
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT s.image FROM subtasks s WHERE s.id = $1),
		(SELECT p.runtime_image FROM subtasks s JOIN projects p ON p.id = s.project WHERE s.id = $1 AND p.hidden AND NOT $3), $2)`,
		r.subtask, d.Image, r.judge).Scan(&image); err != nil {
		return err
	}
	cliVersion := cliVersionIn(ctx, tx, r.cli, image, d.Image)
	gz, sum := gzipText(r.prompt)
	modelFrom, effortFrom := r.modelFrom, r.effortFrom
	if modelFrom == "" {
		modelFrom = "operator"
	}
	if effortFrom == "" {
		effortFrom = modelFrom
	}
	name := r.name
	if name == "" {
		name = r.model
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, effort, model_name, model_from, effort_from,
			cli_version, crew_version, image, conversation, way, continues, branch, base_ref, timeout_s, max_turns, tools, turn, prompt_sha256, push, size,
			git_commit)
		VALUES ($1, $2, $3, 'starting', $4, nullif($5, ''), nullif($6, ''), nullif($21, ''), $19, $22,
			$7, $8, $9, $10, $11, nullif($12, '')::uuid, nullif($13, ''), nullif($14, ''), $15, $16, $17, 1, $18, $20, nullif($23, ''), nullif($24, ''))`,
		r.id, r.subtask, r.number, r.cli, r.model, r.effort, cliVersion, d.Version, image, r.conversation, r.way,
		r.continues, r.branch, r.base, int(r.timeout.Seconds()), r.maxTurns, r.tools, sum, modelFrom, !r.readOnly, name, effortFrom, r.size,
		r.commit); err != nil {
		return err
	}
	parts := []map[string]any{{"part": r.speaker, "bytes": len(r.prompt), "sha256": sum}}
	if len(r.parts) > 0 {
		parts = parts[:0]
		for _, p := range r.parts {
			_, psum := gzipText(strings.TrimSpace(p.Text))
			parts = append(parts, map[string]any{"part": p.Part, "source": p.Source, "bytes": len(strings.TrimSpace(p.Text)), "sha256": psum})
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_prompts (run, gzip, parts) VALUES ($1, $2, $3)`, r.id, gz, parts); err != nil {
		return err
	}
	return d.insertTurn(ctx, tx, r.conversation, r.id, r.speaker, r.prompt, nil, 0, r.rebuilt)
}

// cliVersionIn is the version of the CLI a run in image runs: in the running release's image, what its
// login check read; in a candidate's, what the candidate's build read of the image, as its release.json
// pins it, so a gate's or a release suite's run names the CLI it ran (found at the gate of 0.2.25).
// A candidate no release names, or whose build read no version, records none rather than another's.
func cliVersionIn(ctx context.Context, tx pgx.Tx, name, image, running string) *string {
	var v *string
	if image == running {
		if err := tx.QueryRow(ctx, `SELECT version FROM cli_logins WHERE cli = $1`, name).Scan(&v); err != nil {
			return nil
		}
		return v
	}
	if err := tx.QueryRow(ctx, `SELECT manifest->'pins'->>$2::text FROM releases WHERE manifest->'images'->'crew-runtime'->>'id' = $1
		ORDER BY built_at DESC LIMIT 1`, image, name).Scan(&v); err != nil {
		return nil
	}
	if v == nil || !pinnedVersion.MatchString(*v) {
		return nil
	}
	return v
}

// pinnedVersion is a version a release's build read of a CLI; "unknown", or what failed, is none.
var pinnedVersion = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,9}){1,3}$`)

func (d *Driver) insertTurn(ctx context.Context, tx pgx.Tx, conv, run, speaker, text string, calls []string, tokens int64, rebuilt bool) error {
	if calls == nil {
		calls = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO turns (conversation, number, run, speaker, text, tool_calls, tokens, rebuilt)
		VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM turns WHERE conversation = $1), $2, $3, $4, $5, $6, $7)`,
		conv, run, speaker, text, calls, tokens, rebuilt)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE conversations SET last_turn_at = now() WHERE id = $1`, conv)
	}
	return err
}

// NextChoice is the next choice of a subtask that waits for a sign-in, a limit, or a model its CLI
// refused: the CLI, the model, and the effort it continues on in a new sandbox, or nil when it has
// none that can run now and waits (12.7).
type NextChoice func(ctx context.Context, subtask int64) (*ContinueRequest, error)

// SetNext gives the driver the pipeline's next choice, which the monitor asks: it may be set while
// the monitor runs.
func (d *Driver) SetNext(f NextChoice) {
	d.mu.Lock()
	d.nextChoice = f
	d.mu.Unlock()
}
