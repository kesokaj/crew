// Package sandboxes starts and removes Crew's sandboxes (design 13.3) and knows, for the broker,
// which sandbox a request comes from and what that sandbox may have (12.6).
//
// A sandbox gets a token when it starts, written into its control folder (12.4). The database keeps
// only its hash. The broker answers a request only when it arrives at that sandbox's own door, from
// that sandbox's address, with that sandbox's token: the network alone is not enough.
package sandboxes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/githubapp"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// The kinds of sandbox (13.3). A kind fixes what the broker gives it (Grant).
const (
	KindRun        = "run"
	KindTerminal   = "terminal"
	KindImprover   = "improver"
	KindJudge      = "judge"
	KindLoginCheck = "login-check"
	KindChat       = "chat"
	// KindConversation is the operator's conversation about one project or none (11.8): what a run of
	// its project holds. The chat of a ticket is KindChat, which holds less (11.9, 12.6).
	KindConversation = "conversation"
	KindCheck        = "check"
	KindSetup        = "setup"
	KindHosted       = "hosted"
	// KindBuild builds a release of Crew from an exported commit (16.2 step 4, since 0.51): a check
	// sandbox with no agent, in Sources, which holds nothing of the operator's.
	KindBuild = "build"
	// KindHostBuild builds a hosted project's image from main (19.2 step 3, since 0.72): no agent and no
	// grant, its own Docker daemon, in the mode of its project's checks.
	KindHostBuild = "host-build"
	// KindProbe is a microVM that runs nothing and is removed as soon as it started (21, D11): while
	// two in a row did not boot, its boot is what lifts the hold. It is given nothing.
	KindProbe = "probe"
)

// CLIs are the agent CLIs Crew runs (12.2): agy alone since 0.42. Whether it is signed in is the
// login check's.
var CLIs = cli.Names()

// Grant is what the broker gives a sandbox (the table in 12.6).
type Grant struct {
	CLIs        []string // the CLIs whose login it gets
	GitIdentity bool
	GitHubToken bool // a temporary token that can only read
	Gcloud      bool
}

// GrantFor returns what a sandbox of kind gets. cli is the one CLI it runs, signedIn every CLI that
// is signed in (for the project terminal), and gcloud whether a terminal's row asks for gcloud: no run
// and no conversation gets it, whatever its row says (AGENTS.md safety rule 8, design 0.79). The SSH
// key and the gh login are in no grant: they stay with the server.
func GrantFor(kind, cli string, signedIn []string, gcloud bool) Grant {
	one := func() []string {
		if slices.Contains(CLIs, cli) {
			return []string{cli}
		}
		return nil
	}
	switch kind {
	case KindRun, KindConversation:
		return Grant{CLIs: one(), GitIdentity: true, GitHubToken: true}
	case KindTerminal:
		var all []string
		for _, c := range CLIs {
			if slices.Contains(signedIn, c) {
				all = append(all, c)
			}
		}
		return Grant{CLIs: all, GitIdentity: true, GitHubToken: true, Gcloud: gcloud}
	case KindImprover, KindJudge, KindLoginCheck:
		return Grant{CLIs: one()}
	case KindChat:
		return Grant{CLIs: one(), GitHubToken: true}
	case KindCheck:
		return Grant{GitHubToken: true}
	}
	// Setup makes the credentials and gets none; a hosted project gets its own secrets (19.3).
	return Grant{}
}

// Spec is a sandbox to start.
type Spec struct {
	Kind    string
	CLI     string   // the one CLI it runs, when it runs one
	Project string   // the project's id, when it has one
	Repos   []string // what its temporary GitHub token may read, owner/name
	Gcloud  bool     // whether a terminal asks for gcloud; a run or a conversation gets none (GrantFor)
	Minimal bool     // no Docker inside
	// Sizing is what its microVM gets beyond Kata's base (13.10); zero is the base.
	Sizing orchestrator.Sizing
	// Mode is the network mode its project or ticket asks for (13.6), for the kinds that have their
	// project's mode; empty is the internet. ModeFor decides the others by their kind.
	Mode string
	Env  []string
	// Image is the runtime image a run's sandbox starts from, by its id: the candidate's, for an agent
	// run of a release's eval suite (16.2 step 5, since 0.52). Empty is the running release's.
	Image string
	// RunID is the optional run identifier when preparing an isolated session inside a shared project microVM.
	RunID string
	Life  time.Duration // how long its token lasts; 0 is two hours
	// Prepare runs after the sandbox's home, token, and row exist and before its microVM starts,
	// with its id, its home, and the hash of its token: a run writes its control folder here (12.4).
	// An error removes the sandbox again.
	Prepare func(id, home, tokenHash string) error
}

// Row is a sandbox as the database has it.
type Row struct {
	ID      string
	Kind    string
	CLI     string
	Project string
	Repos   []string
	Gcloud  bool
	Mode    string
	Started time.Time
}

// ModeFor is the network mode a sandbox of kind starts in (13.6). The build of a release is Sources.
// A judge, a summary writer, a
// prompt writer, a login check, and the improver need only their model and Crew, so they are Model
// only whatever is asked. Setup has the internet: it signs the CLIs and gh in. A run, a check, a
// conversation, a chat, and a terminal have the mode of their project or ticket; a conversation about
// no project, the machine's.
func ModeFor(kind, asked string) string {
	switch kind {
	case KindBuild:
		return config.ModeSources
	case KindJudge, KindLoginCheck, KindImprover, KindProbe:
		return config.ModeModelOnly
	case KindSetup:
		return config.ModeInternet
	}
	if asked == "" {
		return config.ModeInternet
	}
	return asked
}

// Runtime is the part of the orchestrator a Manager uses.
type Runtime interface {
	StartSandbox(ctx context.Context, spec orchestrator.SandboxSpec) (orchestrator.Sandbox, error)
	CutSandbox(ctx context.Context, id string) error
	RemoveSandbox(ctx context.Context, id string) error
	AdoptSandbox(ctx context.Context, id string) error
	SandboxAt(local, remote string) (string, bool)
	IsSandboxDoor(local string) bool
	Sandboxes(ctx context.Context) ([]orchestrator.SandboxInfo, error)
}

// Manager starts and removes sandboxes, and keeps their rows.
type Manager struct {
	Runtime Runtime
	Pool    *pgxpool.Pool
	Dir     string // the sandboxes' homes on the machine: <data>/sandboxes
	Image   string
	Log     *slog.Logger
	// OnRemove runs before a sandbox's row is closed, for what must end with it, such as its
	// temporary GitHub token.
	OnRemove func(ctx context.Context, id string)

	adoptMu   sync.Mutex
	adoptedAt time.Time
	startedAt time.Time // the last Start of this server, under adoptMu
	// noToken holds the sandboxes whose missing GitHub token is recorded: a sandbox may ask the
	// broker as often as it likes, and gets one signal.
	noToken   sync.Map
	projHomes sync.Map
}

// removeTimeout bounds one removal, which no caller can cut short.
const removeTimeout = 2 * time.Minute

// ErrRefused is the broker's one answer to a request it does not trust. It says no more, so a
// caller learns nothing from it.
var ErrRefused = errors.New("refused")

var idRand = regexp.MustCompile(`[^a-z0-9-]`)

// imageID is a runtime image's id, the only way a run names the image it starts from.
var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func newID(kind string) (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return idRand.ReplaceAllString(kind, "") + "-" + hex.EncodeToString(b), nil
}

func newToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	return token, Hash(token), nil
}

// Hash is how a token is stored.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Home is the directory on the machine that is the sandbox's home.
func (m *Manager) Home(id string) string {
	if h, ok := m.projHomes.Load(id); ok {
		if s, ok := h.(string); ok && s != "" {
			return s
		}
	}
	if strings.HasPrefix(id, "proj-") && m.Pool != nil {
		var hp string
		if err := m.Pool.QueryRow(context.Background(), `SELECT home_path FROM project_sandboxes WHERE sandbox_id = $1`, id).Scan(&hp); err == nil && hp != "" {
			m.projHomes.Store(id, hp)
			return hp
		}
	}
	return filepath.Join(m.Dir, id, "home")
}

// Start makes the sandbox's home with its control folder and token, records it, and starts it.
// A sandbox that does not start is removed again.
func (m *Manager) Start(ctx context.Context, spec Spec) (Row, orchestrator.Sandbox, error) {
	// Only a run's sandbox starts from an image it names, by its id: a check decides with the running
	// release's image, and so does a judge (16.2 step 5, since 0.52).
	if spec.Image != "" && (spec.Kind != KindRun || !imageID.MatchString(spec.Image)) {
		return Row{}, orchestrator.Sandbox{}, fmt.Errorf("a %s sandbox cannot start from the image %q: only a run's may name one, by its id", spec.Kind, spec.Image)
	}
	// Only a terminal may hold gcloud (AGENTS.md safety rule 8, 0.79): a row of another kind never says
	// it does, so an older server after a roll back gives none either (security review of 8543b5a).
	if spec.Gcloud && spec.Kind != KindTerminal {
		return Row{}, orchestrator.Sandbox{}, fmt.Errorf("a %s sandbox cannot hold gcloud: only a terminal may", spec.Kind)
	}
	id, err := newID(spec.Kind)
	if err != nil {
		return Row{}, orchestrator.Sandbox{}, err
	}
	token, hash, err := newToken()
	if err != nil {
		return Row{}, orchestrator.Sandbox{}, err
	}
	home := m.Home(id)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Row{}, orchestrator.Sandbox{}, fmt.Errorf("the sandbox's home: %w", err)
	}
	if err := homepath.WriteFile(home, ".crew/token", []byte(token+"\n"), 0o600); err != nil {
		os.RemoveAll(filepath.Join(m.Dir, id))
		return Row{}, orchestrator.Sandbox{}, err
	}
	life := spec.Life
	if life == 0 {
		life = 2 * time.Hour
	}
	row := Row{ID: id, Kind: spec.Kind, CLI: spec.CLI, Project: spec.Project, Repos: spec.Repos, Gcloud: spec.Gcloud,
		Mode: ModeFor(spec.Kind, spec.Mode)}
	// Sources is the build's and a check's alone (since 0.53), and a project's build's, which has its
	// checks' mode (since 0.72): a project or a ticket cannot give it to a sandbox that runs an agent.
	// The hosted modes are no sandbox's this starts: a hosted project's microVM is hosting's.
	if !slices.Contains(config.Modes, row.Mode) && (row.Mode != config.ModeSources || (spec.Kind != KindBuild && spec.Kind != KindCheck && spec.Kind != KindHostBuild)) {
		return Row{}, orchestrator.Sandbox{}, fmt.Errorf("no network mode %q", row.Mode)
	}
	if row.Repos == nil {
		row.Repos = []string{}
	}
	image := spec.Image
	err = pgx.BeginFunc(ctx, m.Pool, func(tx pgx.Tx) error {
		// A sandbox of a project gets a GitHub token only for the repository of its own project
		// (safety rule 7, 12.6, 13.11): a run gets none, and the project terminal gets its project's
		// remote, so any other repository is refused before the sandbox is recorded.
		if spec.Project != "" && len(spec.Repos) > 0 {
			var remote string
			if err := tx.QueryRow(ctx, `SELECT coalesce(remote, '') FROM projects WHERE id = $1`, spec.Project).Scan(&remote); err != nil {
				return err
			}
			repo := githubRepo(remote)
			for _, r := range spec.Repos {
				if repo == "" || r != repo {
					return fmt.Errorf("the repository %q is not this project's remote", r)
				}
			}
		}
		// The gate's project, hidden, may name the runtime image its runs start from: the candidate's,
		// while a gate's live steps run (16.2 step 5). Its checks, which decide whether the real
		// ticket passed, run in the running release's image: the candidate grades nothing of its own
		// (security review of M16 P4c).
		if spec.Project != "" && spec.Kind == KindRun {
			var named *string
			if err := tx.QueryRow(ctx, `SELECT runtime_image FROM projects WHERE id = $1 AND hidden`, spec.Project).Scan(&named); err != nil &&
				!errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if named != nil {
				// An eval never runs on a hidden project: a run that names an image there is refused,
				// rather than one of the two chosen.
				if image != "" {
					return fmt.Errorf("the run names the image %s and its project names %s", image, *named)
				}
				image = *named
			}
		}
		// An image a run names is the run's only while a release suite of its project from that image
		// is open and its gate runs its live part: a subtask keeps its image after its suite ended, and
		// a run that continues it must not start from the candidate then (security review of M16 P4d).
		if spec.Image != "" {
			var open bool
			// The gate runs the suite as the pipeline's rule says: live, its suites step not ended, and its
			// list naming the suite's project with this suite or none yet (security review of P4d, round 2).
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM eval_suites s JOIN gates g ON g.version = s.gate,
					jsonb_array_elements(CASE WHEN jsonb_typeof(g.suites) = 'array' THEN g.suites ELSE '[]' END) e
				WHERE s.project = $1::uuid AND s.purpose = 'release' AND s.image = $2 AND s.state IN ('queued', 'running')
				AND g.state = 'live' AND g.suites_step IS NULL
				AND e->>'project' = s.project::text AND (NOT e ? 'suite' OR (e->>'suite')::bigint = s.id))`,
				spec.Project, spec.Image).Scan(&open); err != nil {
				return err
			}
			if !open {
				return fmt.Errorf("the image %s is no open release suite's of this project whose gate runs", spec.Image)
			}
		}
		recorded := m.Image
		if image != "" {
			recorded = image
		}
		if err := tx.QueryRow(ctx, `INSERT INTO sandboxes (id, kind, project, container, network, image, cli, repos, gcloud, mode)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $4, $5, nullif($6, ''), $7, $8, $9) RETURNING started_at`,
			id, spec.Kind, spec.Project, orchestrator.SandboxName(id), recorded, spec.CLI, row.Repos, spec.Gcloud, row.Mode).Scan(&row.Started); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO sandbox_tokens (sha256, sandbox, ends_at) VALUES ($1, $2, now() + $3::interval)`,
			hash, id, fmt.Sprintf("%d seconds", int(life.Seconds())))
		return err
	})
	if err != nil {
		os.RemoveAll(filepath.Join(m.Dir, id))
		return Row{}, orchestrator.Sandbox{}, fmt.Errorf("recording the sandbox: %w", err)
	}
	if spec.Prepare != nil {
		if err := spec.Prepare(id, home, hash); err != nil {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if rerr := m.Remove(rctx, id); rerr != nil {
				m.Log.Warn("removing a sandbox that was not prepared", "sandbox", id, "error", rerr)
			}
			return Row{}, orchestrator.Sandbox{}, fmt.Errorf("preparing the sandbox: %w", err)
		}
	}
	sb, err := m.Runtime.StartSandbox(ctx, orchestrator.SandboxSpec{
		ID: id, Kind: spec.Kind, Home: home, Env: spec.Env, Minimal: spec.Minimal, Mode: row.Mode, Sizing: spec.Sizing, Image: image,
	})
	if err != nil {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if rerr := m.Remove(rctx, id); rerr != nil {
			m.Log.Warn("removing a sandbox that did not start", "sandbox", id, "error", rerr)
		}
		return Row{}, orchestrator.Sandbox{}, err
	}
	m.Log.Info("sandbox started", "sandbox", id, "kind", spec.Kind, "cli", spec.CLI, "mode", row.Mode)
	m.adoptMu.Lock()
	m.startedAt = time.Now()
	m.adoptMu.Unlock()
	return row, sb, nil
}

// Probe starts a probe, which holds nothing and runs nothing, Model only at Kata's base size, and
// removes it at once (21, D11). Its boot is counted at the orchestrator's StartSandbox, as every
// microVM's is; Start asks no hold, so it starts while new runs are held.
func (m *Manager) Probe(ctx context.Context) error {
	row, _, err := m.Start(ctx, Spec{Kind: KindProbe, Minimal: true, Life: 10 * time.Minute})
	if err != nil {
		return err
	}
	return m.Remove(ctx, row.ID)
}

// Cut disconnects the sandbox from its bridge and drops its door, so it has no route out and the
// broker and the proxy refuse it (9.6, 13.6).
func (m *Manager) Cut(ctx context.Context, id string) error {
	return m.Runtime.CutSandbox(ctx, id)
}

// Remove removes the sandbox, ends its token, closes its row, and deletes its home. The home is a
// copy of what the sandbox held, credentials included, so it goes with it (12.6, R169).
func (m *Manager) Remove(ctx context.Context, id string) error {
	// A removal is finished even when the request that asked for it goes away: a page closed right
	// after "Stop" cancelled Docker's delete on dave and left the microVM running.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	// The token goes first, so the broker refuses the sandbox from here on and nothing new is
	// made for it; then the container; then what must end with it, such as its GitHub token, which
	// is revoked even when the container could not be removed yet.
	if _, err := m.Pool.Exec(ctx, `DELETE FROM sandbox_tokens WHERE sandbox = $1`, id); err != nil {
		return err
	}
	rerr := m.Runtime.RemoveSandbox(ctx, id)
	if m.OnRemove != nil {
		m.OnRemove(ctx, id)
	}
	m.noToken.Delete(id)
	if rerr != nil {
		return rerr
	}
	if _, err := m.Pool.Exec(ctx, `UPDATE sandboxes SET removed_at = now() WHERE id = $1 AND removed_at IS NULL`, id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(m.Dir, id)); err != nil {
		return fmt.Errorf("the sandbox's home: %w", err)
	}
	m.Log.Info("sandbox removed", "sandbox", id)
	return nil
}

// NoGitHubToken records that GitHub gave a sandbox no token (21), once for each sandbox. The sandbox
// starts without one; the signal names it, with its project and its run when it has them, and the
// cause's fixed sentence (13.9): the improver reads it, and the broker logs the error itself.
func (m *Manager) NoGitHubToken(ctx context.Context, row Row, cause error) {
	if _, seen := m.noToken.LoadOrStore(row.ID, true); seen {
		return
	}
	why := githubapp.NoTokenReason(cause)
	if _, err := m.Pool.Exec(ctx, `INSERT INTO signals (project, run, kind, detail)
		VALUES (nullif($1, '')::uuid, (SELECT id FROM runs WHERE sandbox = $2 ORDER BY started_at DESC LIMIT 1), 'no_github_token',
			jsonb_build_object('sandbox', $2::text, 'kind', $3::text, 'error', $4::text))`,
		row.Project, row.ID, row.Kind, why); err != nil {
		m.noToken.Delete(row.ID) // the next request records it
		m.Log.Error("recording that a sandbox has no GitHub token", "sandbox", row.ID, "error", err)
	}
}

// Get returns a sandbox that has not been removed.
func (m *Manager) Get(ctx context.Context, id string) (Row, error) {
	var r Row
	err := m.Pool.QueryRow(ctx, `SELECT id, kind, coalesce(cli, ''), coalesce(project::text, ''), repos, gcloud, mode, started_at
		FROM sandboxes WHERE id = $1 AND removed_at IS NULL`, id).Scan(&r.ID, &r.Kind, &r.CLI, &r.Project, &r.Repos, &r.Gcloud, &r.Mode, &r.Started)
	return r, err
}

// Live lists the sandboxes that have not been removed, oldest first.
func (m *Manager) Live(ctx context.Context) ([]Row, error) {
	rows, err := m.Pool.Query(ctx, `SELECT id, kind, coalesce(cli, ''), coalesce(project::text, ''), repos, gcloud, mode, started_at
		FROM sandboxes WHERE removed_at IS NULL ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var s Row
		err := r.Scan(&s.ID, &s.Kind, &s.CLI, &s.Project, &s.Repos, &s.Gcloud, &s.Mode, &s.Started)
		return s, err
	})
}

func githubRepo(remote string) string {
	if !strings.HasPrefix(remote, "git@github.com:") {
		return ""
	}
	r := strings.TrimSuffix(strings.TrimPrefix(remote, "git@github.com:"), ".git")
	if strings.Count(r, "/") != 1 {
		return ""
	}
	return r
}
