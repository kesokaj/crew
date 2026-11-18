package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/credbroker"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/diskguard"
	"github.com/kesokaj/crew/internal/egress"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/githubapp"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/logins"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/projterm"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/setup"
	"github.com/kesokaj/crew/internal/version"
)

// machine is what the server works with beside the database: the credentials and the sandboxes.
type machine struct {
	vault     *credstore.Vault
	orch      *orchestrator.Orchestrator
	sandboxes *sandboxes.Manager
	github    *githubapp.App
	setup     *setup.Service
	logins    *logins.Service
	brokerSvc *credbroker.Broker
	git       *gitendpoint.Endpoint
	runs      *runs.Driver
	// outside is the Crew MCP for the operator's own tools, and tokens their personal tokens (11.7).
	outside *mcp.Server
	tokens  *mcp.Tokens
	// mcp is the Crew MCP server, on every sandbox's door (11.5); set by serve with its tools.
	mcp http.Handler
	// adopted is closed once start has taken over the runs of the server before (21).
	adopted chan struct{}
	// proxy is the way out of a Model only or Sources sandbox (13.6), at the same door.
	proxy *egress.Proxy
	// guard looks after the data folder's disk (16.10).
	guard *diskguard.Guard
	// room is the capacity check (7.9, 13.10), with the machine's size from Docker.
	room *capacity.Gate
	// hosting is set by serve outside check mode: its deploys a stop cut off are taken up at start.
	hosting *hosting.Service
}

// ipForwardProc is the sysctl openMachine reads the server's forwarding from; a test names a file.
var ipForwardProc = config.IPForwardProc

// openMachine opens the vault with the key file and the orchestrator with Docker. Either can be
// missing: the server then serves without it, and says why. It starts nothing: the server does, with
// start, and crew admin uses the parts.
func openMachine(ctx context.Context, e env, pool *pgxpool.Pool, log *slog.Logger) (*machine, error) {
	m := &machine{adopted: make(chan struct{})}
	// In check mode the server gets no key file (16.8): a key of its own, which opens no credential
	// of the copy it runs on.
	key, err := checkKey(e)
	if err != nil {
		return m, err
	}
	if m.vault, err = credstore.NewVault(pool, key); err != nil {
		return m, err
	}
	m.github = &githubapp.App{Vault: m.vault, Log: log}
	if e.RuntimeImage == "" || e.SelfContainer == "" {
		return m, errors.New("CREW_RUNTIME_IMAGE and CREW_SELF_CONTAINER are not set, so no sandbox can start")
	}
	if err := config.CheckRuntime(e.Runtime); err != nil {
		return m, err
	}
	// The server joins every sandbox's bridge, so its network routes nothing between them (13.6).
	if err := config.CheckIPForward(ipForwardProc, os.Getenv(config.EnvAllowIPForward) == "1"); err != nil {
		return m, err
	}
	cfg := orchConfig(e)
	if m.orch, err = orchestrator.New(cfg, log); err != nil {
		return m, err
	}
	m.sandboxes = &sandboxes.Manager{
		Runtime: m.orch,
		Pool:    pool,
		Dir:     filepath.Join(e.DataDir, "sandboxes"),
		Image:   e.RuntimeImage,
		Log:     log,
		// The temporary GitHub token ends with its sandbox (12.6).
		OnRemove: m.github.Revoke,
	}
	m.proxy = &egress.Proxy{
		Hosts:     func(ctx context.Context) ([]string, error) { return settings.ModelOnlyHosts(ctx, pool) },
		Sources:   func(ctx context.Context) ([]string, error) { return settings.SourcesHosts(ctx, pool) },
		IsSources: func(sandbox string) bool { return m.orch.SandboxMode(sandbox) == config.ModeSources },
		SandboxAt: m.orch.SandboxAt,
		Log:       log,
	}
	m.logins = &logins.Service{Pool: pool, Vault: m.vault, Sandboxes: m.sandboxes, Exec: m.orch, Log: log}
	m.brokerSvc = m.newBroker(log)
	m.git = &gitendpoint.Endpoint{Dir: filepath.Join(e.DataDir, "repos"), Log: log}
	// The hook tests a push's paths with this program (13.8): without it, a push with rules is refused.
	if self, err := os.Executable(); err == nil {
		m.git.Self = self
	} else {
		log.Error("the git endpoint cannot test what a push changes", "error", err)
	}
	m.room = &capacity.Gate{Pool: pool, Host: m.orch.Host,
		Settings: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) }}
	m.runs = &runs.Driver{
		Room:    m.room,
		Pool:    pool,
		Harness: &harness.Kata{Sandboxes: m.sandboxes, Runtime: m.orch, Log: log},
		Secrets: m.secrets,
		// A conversation's work folder is sealed with the key file (D41).
		Seal:   m.vault,
		Logins: m.logins,
		Auth:   m.sandboxes,
		// A run sandbox that no live run follows is removed when the server starts (12.4).
		Sandboxes: m.sandboxes,
		Version:   version.Version,
		Image:     e.RuntimeImage,
		Log:       log,
	}
	// The disk of the data folder, measured at a folder the server mounts from it: on dave it is also
	// Docker's (16.10).
	m.guard = &diskguard.Guard{Pool: pool, Dir: filepath.Join(e.DataDir, "repos"), Stat: diskguard.Statfs, Prune: m.orch.Prune, Log: log,
		Settings: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) }}
	m.git.Auth = m.runs.GitAccess
	m.git.OnPush = m.runs.RecordPush
	m.setup = &setup.Service{
		Vault:     m.vault,
		Sandboxes: m.sandboxes,
		Runtime:   m.orch,
		GitHub:    m.github,
		Machine:   m.orch.CheckMachine,
		Who:       e.AdminEmail,
		Log:       log,
		// A login that was just collected is checked at once, with its real request (12.6).
		Collected: func(id string) {
			if _, ok := cli.Get(id); ok {
				go m.logins.Check(context.WithoutCancel(ctx), id, true)
			}
		},
	}
	return m, nil
}

// start is the server's part: it removes what was left from before, follows the runs that live on,
// and runs the login check, the run monitor, and the reaper of Setup until ctx ends.
func (m *machine) start(ctx context.Context, log *slog.Logger) {
	if err := ensureFixture(ctx, m.git); err != nil {
		log.Error("the fixture's repository", "error", err)
	}
	// Reconcile may take minutes; the server answers meanwhile. The runs are followed again only
	// after the sandboxes are known (21, "Backend restart").
	go m.guard.Run(ctx, 10*time.Minute)
	m.watchBoots(log)
	go func() {
		takeOver(ctx, m.adopted, m.sandboxes.Reconcile, m.recoverHosting, m.runs.Reconcile)
		go m.runs.Monitor(ctx, 10*time.Second)
		m.logins.Schedule(ctx)
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				m.setup.Reap(ctx)
			}
		}
	}()
}

// orchConfig is the orchestrator's config from the server's environment: the runtime image and its
// runtime, the server's own container and listeners, and the ingress's container (19.4).
func orchConfig(e env) *config.Config {
	return &config.Config{
		Image:         e.RuntimeImage,
		Runtime:       e.Runtime,
		SelfContainer: e.SelfContainer,
		// The ingress, which a hosted project's bridge has too (19.4), and the server's own listener,
		// which the probe of a hosted bridge must find silent (plan M19 D22).
		IngressContainer: e.IngressContainer,
		Addr:             e.Listen,
		BrokerAddr:       e.BrokerListen,
		UID:              os.Getuid(),
		GID:              os.Getgid(),
	}
}

// recoverHosting takes up the hosted projects after the sandboxes are known (plan M19 D29).
func (m *machine) recoverHosting(ctx context.Context) {
	if m.hosting == nil {
		return
	}
	if err := m.hosting.Recover(ctx); err != nil {
		m.hosting.Log.Error("taking up the hosted projects a stop cut off", "error", err)
	}
}

// watchBoots counts the boots of every microVM, and lets the monitor start a probe while two in a row
// did not boot (21, D10, D11).
func (m *machine) watchBoots(log *slog.Logger) {
	m.orch.OnBoot = countBoot(m.runs.Pool, m.orch.CheckMachine, log)
	m.runs.Probe = m.sandboxes.Probe
}

// countBoot counts every start of a microVM that reached Docker's (21, D10) in the row every start
// asks: two in a row that did not boot hold new runs, with the error and the machine check's line,
// and one that boots lifts the hold. Only the server outside check mode counts (D21).
func countBoot(pool *pgxpool.Pool, check func(context.Context) (orchestrator.MachineCheck, error), log *slog.Logger) func(ok bool, why string) {
	return func(ok bool, why string) {
		// The check and the count have a time each, so a Docker that hangs does not lose the count.
		if !ok {
			// Docker's error is cut first, so the check's line is kept whole within hold.Boot's bound.
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			why = hold.Cut(why, 400) + "; " + machineCheckLine(cctx, check)
			cancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		held, err := hold.Boot(ctx, pool, ok, why)
		switch {
		case err != nil:
			log.Error("counting a boot of a microVM", "ok", ok, "error", err)
		case held:
			log.Warn("two microVMs in a row did not boot: new runs are held", "why", why)
		case !ok:
			log.Warn("a microVM did not boot", "why", why)
		}
	}
}

// machineCheckLine is the machine check in one line, for the inbox's item of a boot hold (21).
func machineCheckLine(ctx context.Context, check func(context.Context) (orchestrator.MachineCheck, error)) string {
	c, err := check(ctx)
	switch {
	case err != nil:
		return "the machine check could not ask Docker: " + err.Error()
	case !c.RuntimeOK:
		return fmt.Sprintf("the machine check: Docker %s has no runtime %s, so Kata is not set up", c.Docker, c.Runtime)
	}
	return fmt.Sprintf("the machine check: Docker %s has the runtime %s; whether /dev/kvm works shows only a microVM that boots", c.Docker, c.Runtime)
}

// openDoor serves the sandboxes' door on e.BrokerListen, sending its end to errc, until the function
// it returns closes it. The door, its handler and its server are wired here alone, in a file 16.8
// protects (since 0.62).
func (m *machine) openDoor(e env, errc chan<- error) (func() error, error) {
	broker := &http.Server{
		Handler:           m.sandboxDoor(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	door, err := net.Listen("tcp", e.BrokerListen)
	if err != nil {
		return nil, fmt.Errorf("the sandboxes' door: %w", err)
	}
	go func() { errc <- broker.Serve(door) }()
	return broker.Close, nil
}

// runSecrets are the secrets of a run's sandbox, which a run's entry never keeps (8.3); none when the
// run or its sandbox cannot be read.
func (m *machine) runSecrets(pool *pgxpool.Pool) func(ctx context.Context, run string) []string {
	return func(ctx context.Context, run string) []string {
		var sandbox *string
		if err := pool.QueryRow(ctx, `SELECT sandbox FROM runs WHERE id = $1`, run).Scan(&sandbox); err != nil || sandbox == nil || m.sandboxes == nil {
			return nil
		}
		return m.secrets(ctx, *sandbox)
	}
}

// sandboxDoor is what the server answers on the sandboxes' own networks (20.3): the broker, which
// hands them their credentials, the git endpoint for runs (6.1), the Crew MCP (11.5), the proxy
// of Model only and Sources (13.6), and Sources' module proxy.
func (m *machine) sandboxDoor() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", m.brokerSvc.Handler())
	mux.Handle("/git/", m.git)
	if m.mcp != nil {
		mux.Handle("/mcp", m.mcp)
	}
	// Go's modules for a Sources sandbox, which the server fetches itself (13.6, since 0.61).
	mux.HandleFunc(egress.ModulesPath, func(w http.ResponseWriter, r *http.Request) {
		if m.proxy == nil {
			http.Error(w, "no proxy", http.StatusServiceUnavailable)
			return
		}
		m.proxy.ServeModules(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The proxy's CONNECT names a host, not a path of this server.
		if r.Method == http.MethodConnect {
			if m.proxy == nil {
				http.Error(w, "no proxy", http.StatusServiceUnavailable)
				return
			}
			m.proxy.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (m *machine) newBroker(log *slog.Logger) *credbroker.Broker {
	return &credbroker.Broker{Vault: m.vault, Auth: m.sandboxes, GitHub: m.github, SignedIn: m.logins.SignedIn,
		NoToken: m.sandboxes.NoGitHubToken, Log: log}
}

// secrets are the values the broker hands a sandbox, and its token: what is removed from everything
// Crew stores of it (8.3). A login file is taken apart into its values, so a part of it that a CLI
// prints is found too.
func (m *machine) secrets(ctx context.Context, sandbox string) []string {
	var out []string
	if tok, err := homepath.ReadFile(m.sandboxes.Home(sandbox), runspec.TokenFile, 4096); err == nil {
		out = append(out, strings.TrimSpace(string(tok)))
	}
	row, err := m.sandboxes.Get(ctx, sandbox)
	if err != nil {
		return out
	}
	c, err := m.brokerSvc.Collect(ctx, row)
	if err != nil {
		return out
	}
	for _, v := range c.Env {
		out = append(out, v)
	}
	for _, f := range c.Files {
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			continue
		}
		out = append(out, string(data))
		out = append(out, jsonStrings(data, 16)...)
	}
	return out
}

// jsonStrings are the strings of a JSON document at least min long, at any depth.
func jsonStrings(data []byte, min int) []string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if len(t) >= min {
				out = append(out, t)
			}
		case map[string]any:
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}

// The fixture project's first content, for an installation with no other source: the git endpoint's
// fixture repository gets it when it has no main. crew admin repo seed brings in another.
var fixtureFiles = map[string]string{
	"README.md": "# Fixture\n\nThe fixture project of Crew's own runs and tests. Agents may change anything here.\n",
	"hello.sh":  "#!/bin/sh\n# Prints a greeting.\necho \"hello from the fixture\"\n",
}

func ensureFixture(ctx context.Context, git *gitendpoint.Endpoint) error {
	if err := git.Ensure(ctx, runs.FixtureRepo); err != nil {
		return err
	}
	return git.Init(ctx, runs.FixtureRepo, fixtureFiles, "The fixture project")
}

// takeOver takes over what the server before left, step by step, and only then closes adopted, which
// the planner waits on before it starts a run (21, "Backend restart").
func takeOver(ctx context.Context, adopted chan struct{}, steps ...func(context.Context)) {
	for _, step := range steps {
		step(ctx)
	}
	close(adopted)
}

// openTerminals makes the project terminals (13.11) and gives them their part of the git endpoint: a
// request that is not a run's may be a terminal's, and a push from a terminal in edit mode is acted
// on after it is recorded.
func openTerminals(pool *pgxpool.Pool, m *machine, b *board.Board, log *slog.Logger) *projterm.Service {
	terms := &projterm.Service{
		Pool: pool, Sandboxes: m.sandboxes, Runtime: orchRuntime{m.orch}, Git: b.Git, Kick: b.Kick, Log: log,
		Seal: func(data []byte) ([]byte, error) { return m.vault.Seal(projterm.LogPurpose, data) },
		Comment: func(ctx context.Context, project, ticket, text string) error {
			p, err := b.Project(ctx, project)
			if err != nil {
				return err
			}
			_, err = b.Comment(ctx, p, ticket, text, board.Operator)
			return err
		},
	}
	runAccess := m.runs.GitAccess
	m.git.Auth = func(r *http.Request, repo string) (gitendpoint.Access, error) {
		if acc, err := runAccess(r, repo); err == nil {
			return acc, nil
		}
		return terms.GitAccess(r, repo)
	}
	m.git.OnPush = func(acc gitendpoint.Access, ref, old, new string) {
		m.runs.RecordPush(acc, ref, old, new)
		terms.Pushed(acc, ref, old, new)
	}
	return terms
}

// orchRuntime is the orchestrator as the terminals use it.
type orchRuntime struct{ o *orchestrator.Orchestrator }

func (r orchRuntime) Exec(ctx context.Context, container string, spec orchestrator.ExecSpec) (int, string, error) {
	return r.o.Exec(ctx, container, spec)
}

func (r orchRuntime) Interactive(ctx context.Context, container string, spec orchestrator.ExecSpec, cols, rows uint) (projterm.Stream, error) {
	return r.o.StartInteractive(ctx, container, spec, cols, rows)
}

// checkKey is the key file's key, or in check mode a new random one.
func checkKey(e env) ([]byte, error) {
	if !e.Check {
		return credstore.ReadKey(e.SecretKeyFile)
	}
	key := make([]byte, credstore.KeySize)
	_, err := rand.Read(key)
	return key, err
}
