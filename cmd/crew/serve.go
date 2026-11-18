package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/assistant"
	"github.com/kesokaj/crew/internal/blobstore"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/sqlite"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/improve"
	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/notify"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/pins"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/rewrite"
	"github.com/kesokaj/crew/internal/rooms"
	"github.com/kesokaj/crew/internal/sessions"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/version"
	"github.com/kesokaj/crew/internal/versions"
)

// serve runs the server until ctx ends. When the database is down it keeps answering: the health
// check says so, and it connects as soon as the database answers (15.3). In check mode (16.4, step
// 3) it answers and reads, on the scratch database, and starts nothing: no orchestrator, notifier, or
// timer, and it takes over no sandbox.
func serve(ctx context.Context, e env, log *slog.Logger) error {
	if e.Hostname == "" || e.AdminEmail == "" {
		return errors.New("CREW_HOSTNAME and CREW_ADMIN_EMAIL must be set")
	}
	token, err := os.ReadFile(e.IngressTokenFile)
	if err != nil {
		return fmt.Errorf("the secret shared with the ingress: %w", err)
	}
	var orch atomic.Pointer[orchestrator.Orchestrator]
	s, err := openIngressDoor(ctx, e, token, &orch, log)
	if err != nil {
		return err
	}
	s.Backups = func(ctx context.Context) (string, []string, error) {
		pool := s.Pool.Load()
		if pool == nil {
			return "", nil, errors.New("the database does not answer yet")
		}
		return listBackups(ctx, pool, e)
	}
	srv := &http.Server{
		Addr:              e.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("crew is serving", "version", version.Version, "listen", e.Listen, "check", e.Check)

	pool := waitForDatabase(ctx, log)
	if pool != nil {
		defer pool.Close()
		s.Pool.Store(pool)
		// Install fills each agent's entries from the baseline; a release adds its new ones (12.3).
		if !e.Check {
			if changed, err := models.FillRoles(ctx, pool, e.machine(), "crew "+version.Version); err != nil {
				log.Error("the agents' entries from the baseline", "error", err)
			} else if changed {
				log.Info("the agents' entries were filled from the baseline", "version", version.Version)
			}
			go daily(ctx, pool, e, log)
		}
		m, err := openMachine(ctx, e, pool, log)
		if err != nil {
			log.Error("the credentials or the sandboxes are not available", "error", err)
		}
		// The mail (18): its routes read in check mode too; it sends only from the loop below.
		notifier := &notify.Notifier{Pool: pool, Vault: m.vault, Machine: e.machine(), Hostname: e.Hostname,
			Loc: e.location(), Send: notify.Production, Log: log,
			// The daily mail holds Activity's news lines (D27).
			News: func(ctx context.Context, after time.Time) ([]notify.NewsLine, error) {
				lines, err := httpapi.ReadNews(ctx, pool, after)
				out := make([]notify.NewsLine, 0, len(lines))
				for _, l := range lines {
					out = append(out, notify.NewsLine{At: l.At, Project: l.Project, Text: l.Text})
				}
				return out, err
			}}
		s.Notify.Store(notifier)
		// Hosting (19): a project's values are sealed, so it waits for the key file.
		if m.vault != nil {
			hs := &hosting.Service{Pool: pool, Vault: m.vault, AppsDomain: e.AppsDomain, CrewHost: e.Hostname, Log: log, Base: ctx,
				Dir: filepath.Join(e.DataDir, "hosted"), Builds: filepath.Join(e.DataDir, "builds"), Room: m.room,
				Git: &gitstore.Store{Dir: filepath.Join(e.DataDir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(e.DataDir, "repos"), Log: log}}}
			if m.orch != nil && m.sandboxes != nil {
				hs.Runtime = m.orch
				hs.Builder = &harness.Kata{Sandboxes: m.sandboxes, Runtime: m.orch, Log: log}
				// The ingress joined again to the hosted bridges, an install having made it anew (19.4), and
				// the health of what serves asked (19.3); what a stop cut off is taken up after the
				// sandboxes are reconciled (m.start).
				if !e.Check {
					go hs.RunRejoin(ctx)
					go hs.RunHealth(ctx)
					m.hosting = hs
				}
			}
			s.Hosting.Store(hs)
		}
		if !e.Check {
			// The database written every 30 seconds, so the inbox says when it could be written again
			// (21, D22); its file is in the backups folder, the server's own mount (D31).
			go writeProbe(pool, e, log).Run(ctx, 30*time.Second)
			// Releases (16.3): the folders on the machine, the notice and its clock, the updater,
			// and the copy on GitHub with gh's login.
			vs, err := openVersions(e, pool, m.vault, log)
			if err != nil {
				log.Error("releases", "error", err)
			} else {
				vs.Builder = openBuilder(ctx, e, pool, m, vs, log)
				s.Versions.Store(vs)
				go vs.Run(ctx, 30*time.Second)
			}
		}
		// The board needs only the database and the repositories (6.1).
		var app interface {
			TokenFor(ctx context.Context, holder string, repos []string) (string, error)
			Revoke(ctx context.Context, holder string)
		}
		if m.github != nil {
			app = m.github
		}
		b, x := openBoard(e, pool, m.vault, app, log)
		s.Board.Store(b)
		baseRooms := &rooms.Service{Pool: pool, Board: b, Log: log}
		baseRooms.AttachBoard(b)
		s.Rooms.Store(baseRooms)
		if e.DataDir != "" {
			s.BlobDir = filepath.Join(e.DataDir, "blobs")
			if fs, ferr := blobstore.NewLocalFS(s.BlobDir); ferr == nil {
				var bst blobstore.Store = fs
				s.Blobs.Store(&bst)
			} else {
				log.Warn("initializing local BlobStore", "error", ferr)
			}
			if wal, werr := sqlite.Open(filepath.Join(e.DataDir, "statedb", "state.db")); werr == nil {
				s.SQLite.Store(wal)
			} else {
				log.Warn("opening embedded SQLite WAL StateDB", "error", werr)
			}
		}
		// Hosting makes the ticket that makes a project hostable on the board, as the operator's (19.2),
		// and follows main and copies the logs outside check mode (19.5, plan M19 D16, D17).
		if hs := s.Hosting.Load(); hs != nil {
			hs.Tickets = func(ctx context.Context, projectID string, n board.NewTicket) (hosting.TicketMade, error) {
				t, err := b.AddTicket(ctx, projectID, n, board.OriginOperator, board.Operator)
				if err != nil {
					return hosting.TicketMade{}, err
				}
				b.Kick()
				return hosting.TicketMade{ID: t.ID, Key: t.Key}, nil
			}
			if m.hosting != nil {
				go hs.RunFollow(ctx)
				go hs.RunLogs(ctx)
			}
		}
		s.Export.Store(x)
		drafts := &planning.Drafts{Pool: pool}
		s.Drafts.Store(drafts)
		if e.Check {
			// What the screens read, with none of its loops started: they only read, and the server
			// refuses every request that would change something.
			s.Assistant.Store(openAssistant(e, pool, nil, b, drafts, log))
			if m.runs != nil {
				s.Setup.Store(m.setup)
				s.Logins.Store(m.logins)
				s.Runs.Store(m.runs)
				s.Room.Store(m.room)
			}
			s.Tokens.Store(&mcp.Tokens{Pool: pool})
			// Releases as the copy holds them; this server reaches no updater (its socket is not here).
			if vs, err := openVersions(e, pool, nil, log); err == nil {
				s.Versions.Store(vs)
			}
			log.Info("check mode: the server answers and reads, and starts nothing")
			return waitServing(ctx, srv, errc)
		}
		if err := ensureFixtureCanonical(ctx, b); err != nil {
			log.Error("the fixture's repositories", "error", err)
		}
		// The repositories of a deleted project that could not be removed then go now (7.13).
		if err := b.SweepDeleted(ctx); err != nil {
			log.Error("removing the repositories of deleted projects", "error", err)
		}
		go b.Run(ctx, 30*time.Second)
		// The mail (18): never in a check server, which returned above (16.4).
		go notifier.Run(ctx, 30*time.Second)
		// The daily look (12.8, since 0.56): the publishers against Crew's project's versions.lock, at
		// the hour of the backup and on Look now. Never in a check server, which returned above.
		look := &pins.Look{Pool: pool, Board: b, Sources: pins.Publishers(cli.AgyManifest, cli.AgyTarball), Log: log}
		s.Pins.Store(look)
		go look.Run(ctx, func(now time.Time) time.Time { return nextDaily(now, e.location()) })
		if m.orch != nil {
			// The Crew MCP with the tools of the planning agent and the lead, and the orchestrator's
			// rows that start them (11.5, 15.2).
			planner, pipe, err := openPlanning(e, pool, m, b, log)
			if err != nil {
				return fmt.Errorf("the Crew MCP: %w", err)
			}
			drafts.Kick = planner.Kick
			s.Planner.Store(planner)
			s.Pipeline.Store(pipe)
			if pipe.Rooms != nil {
				s.Rooms.Store(pipe.Rooms)
			}
			// The gate's live steps (16.2 step 5): the real ticket moves on the board and the pipeline.
			wireLive(s.Versions.Load(), versions.LiveDeps{Board: b, Kick: func() { b.Kick(); pipe.Kick() }, SignedIn: m.logins.SignedIn})
			var outside http.Handler = m.outside
			s.OutsideMCP.Store(&outside)
			s.Tokens.Store(m.tokens)
			// The project terminals (13.11): their requests at the git endpoint, and what follows
			// a push from one in edit mode.
			terms := openTerminals(pool, m, b, log)
			s.Terminals.Store(terms)
			go terms.Run(ctx)
			// The prompt writer (7.2): a judge's call for a rewrite of what the operator wrote.
			rw := &rewrite.Service{
				Pool: pool, Runs: m.runs, Drafts: drafts, Version: version.Version, Log: log,
				Machine: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) },
				Project: func(ctx context.Context, id string) (settings.Doc, error) {
					p, err := b.Project(ctx, id)
					if err != nil {
						return nil, err
					}
					return b.Settings(ctx, pool, p)
				},
			}
			s.Rewrites.Store(rw)
			// The operator's conversations and the tickets' chats (11.8, 11.9): their turns start once
			// the runs of the server before are taken over, as the planner's do.
			talk := openAssistant(e, pool, m, b, drafts, log)
			s.Assistant.Store(talk)
			// A change of the prompt writer's rules shows three examples, old rules and new (7.2).
			pipe.Example = rw.Example
			// The doors are known before m.start adopts the sandboxes, which joins the server to their
			// bridges or, when its container started again, finds it still there: from then on a
			// request at a sandbox's door or from its bridge is refused (20.3). Before, the ingress's
			// door answers only the ingress's network and the loopback, as it always does (13.6).
			orch.Store(m.orch)
			m.start(ctx, log)
			s.Setup.Store(m.setup)
			s.Logins.Store(m.logins)
			s.Runs.Store(m.runs)
			s.Room.Store(m.room)
			closeDoor, err := m.openDoor(e, errc)
			if err != nil {
				return err
			}
			defer closeDoor()
			log.Info("the broker, the git endpoint, and the Crew MCP are serving the sandboxes", "listen", e.BrokerListen)
			// The planner starts runs only once the door answers and the runs of the server before
			// are taken over (planner.Ready), so no run it starts is taken for one that did not
			// survive (21).
			go planner.Run(ctx, 5*time.Second)
			go pipe.Run(ctx, 3*time.Second)
			go talk.Run(ctx, 5*time.Second)
		} else {
			// A server without Docker reads the conversations and keeps the operator's messages, as it
			// reads the plan drafts; it starts no turn.
			s.Assistant.Store(openAssistant(e, pool, nil, b, drafts, log))
		}
	}

	return waitServing(ctx, srv, errc)
}

// waitServing serves until ctx ends or a listener fails.
func waitServing(ctx context.Context, srv *http.Server, errc <-chan error) error {
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// health asks the server in this container for its health check. Docker's health check runs it, so
// the image needs no other HTTP client.
func health(ctx context.Context, e env, stdout io.Writer) error {
	port := e.Listen
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Fprint(stdout, string(body))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the health check says %s", resp.Status)
	}
	return nil
}

func waitForDatabase(ctx context.Context, log *slog.Logger) *pgxpool.Pool {
	for wait := time.Second; ; wait = min(2*wait, 30*time.Second) {
		pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
		if err == nil {
			log.Info("the database answers")
			return pool
		}
		log.Warn("the database does not answer yet", "error", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// daily runs the server's own scheduled jobs (13.1): the month partitions of events, made ahead, the
// operator's own calls of the Crew MCP past their 180 days (6.5), the devices past their 31 days (20.3),
// the ingress's certificate (16.10, also once soon after the start), and the day's backup with the month
// files (16.9), at night in the machine's time zone. There is no
// timer on the machine for either.
func daily(ctx context.Context, pool *pgxpool.Pool, e env, log *slog.Logger) {
	ensureMonths(ctx, pool, log)
	// The ingress starts after the server; a server that restarts often still looks once.
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Minute):
			certificateCheck(ctx, pool, e, log)
		}
	}()
	loc := e.location()
	for {
		next := nextDaily(time.Now(), loc)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		ensureMonths(ctx, pool, log)
		if n, err := mcp.PruneOwnCalls(ctx, pool); err != nil {
			log.Error("removing the operator's calls of the Crew MCP older than 180 days", "error", err)
		} else if n > 0 {
			log.Info("removed the operator's calls of the Crew MCP older than 180 days", "calls", n)
		}
		if n, err := sessions.Prune(ctx, pool); err != nil {
			log.Error("removing the devices signed in more than 31 days ago", "error", err)
		} else if n > 0 {
			log.Info("removed the devices signed in more than 31 days ago", "devices", n)
		}
		certificateCheck(ctx, pool, e, log)
		dir, keep, err := backupPlace(ctx, pool, e)
		if err != nil {
			log.Error("the daily backup: its settings", "error", err)
			continue
		}
		file, err := dailyBackup(ctx, pool, e, dir, keep)
		if err != nil {
			log.Error("the daily backup failed", "error", err)
			continue
		}
		log.Info("the daily backup is made", "file", file)
	}
}

// dailyHour is when the daily jobs run, in the machine's time zone: at night (16.9).
const dailyHour = 3

// nextDaily is the next dailyHour after now, in loc. Across a change of summer time a day has 23 or
// 25 hours, and the job still runs once that day, at that hour.
func nextDaily(now time.Time, loc *time.Location) time.Time {
	now = now.In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), dailyHour, 0, 0, 0, loc)
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, dailyHour, 0, 0, 0, loc)
	}
	return next
}

// ensureMonths makes the partitions of events for this month and the next two.
func ensureMonths(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		month := time.Date(now.Year(), now.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, `SELECT crew_ensure_events_month($1)`, month); err != nil {
			log.Error("making a month partition of events", "month", month.Format("2006-01"), "error", err)
		}
	}
}

// openAssistant makes the service of the operator's conversations and the tickets' chats, which
// starts its turns after the takeover of runs (m.adopted); with no machine, one that starts none.
func openAssistant(e env, pool *pgxpool.Pool, m *machine, b *board.Board, drafts *planning.Drafts, log *slog.Logger) *assistant.Service {
	a := &assistant.Service{Pool: pool, Board: b, Drafts: drafts, Version: version.Version, Log: log,
		Settings: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) }}
	// Without a machine the service has no runs: a nil driver in the interface would not be nil.
	if m != nil {
		a.Runs, a.Ready = m.runs, m.adopted
	}
	return a
}

// openPlanning makes the Crew MCP server with its tools on the sandboxes' door, the planner that
// starts the planning agent and the lead on a plan, and the pipeline that works the tickets (15.2).
func openPlanning(e env, pool *pgxpool.Pool, m *machine, b *board.Board, log *slog.Logger) (*planning.Planner, *pipeline.Pipeline, error) {
	machineSettings := func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) }
	planner := &planning.Planner{
		Pool: pool, Runs: m.runs, Version: version.Version, Log: log, Ready: m.adopted, Settings: machineSettings,
	}
	pipe := &pipeline.Pipeline{
		Pool: pool, Board: b, Runs: m.runs, Checks: m.runs.Harness, Settings: machineSettings,
		Version: version.Version, Log: log, Ready: m.adopted, Room: m.room,
	}
	if b != nil {
		pipe.Identity = b.Identity
	}
	pipe.Chooser = &models.Chooser{Pool: pool, Machine: e.machine()}
	// The Crew level's improvements (10.5): a worse one is taken out by going back to the release before,
	// through the updater, by Crew itself.
	if b != nil {
		sock := os.Getenv("CREW_UPDATER_SOCKET")
		if sock == "" {
			sock = "/run/crew/updater.sock"
		}
		up := updaterClient(sock, pool)
		pipe.Improve = &improve.Service{Pool: pool, Board: b, Settings: machineSettings, Running: version.Version,
			Rollback: func(ctx context.Context, v string) error {
				_, err := up.Install(ctx, updater.Ask{Version: v, AskedBy: "crew"})
				return err
			}}
	}
	// A subtask that waits for a sign-in, a limit, or a refused model moves to its next choice (12.7).
	m.runs.SetNext(pipe.NextChoice)
	// A run is watched with its project's numbers (8.2); no limit is in dollars (7.7, since 0.53).
	m.runs.LimitsFor = pipe.RunLimits
	srv, err := mcp.New(pool, m.runs.MCPCaller, log)
	if err != nil {
		return nil, nil, err
	}
	tools := &planning.Tools{Pool: pool, Board: b, Kick: planner.Kick}
	if err := tools.Register(srv); err != nil {
		return nil, nil, err
	}
	kickAll := func() {
		if b != nil {
			b.Kick()
		}
		pipe.Kick()
	}
	roomsSvc := &rooms.Service{
		Pool:         pool,
		Board:        b,
		KickPlanner:  planner.Kick,
		KickPipeline: kickAll,
		Log:          log,
	}
	if m != nil && m.runs != nil {
		roomsSvc.Dispatcher = m.runs
	}
	roomsSvc.AttachBoard(b)
	pipe.Rooms = roomsSvc
	ticketTools := &pipeline.Tools{Pool: pool, Board: b, Improve: pipe.Improve, Version: version.Version, Kick: kickAll, KickPlanner: planner.Kick, Rooms: roomsSvc}
	// What a run's entry says never keeps the values its sandbox was handed (8.3).
	ticketTools.Secrets = m.runSecrets(pool)
	// The E2E tester's screenshots are read from its run's home (7.4).
	ticketTools.Home = func(ctx context.Context, run string) (string, error) {
		var sandbox *string
		if err := pool.QueryRow(ctx, `SELECT sandbox FROM runs WHERE id = $1`, run).Scan(&sandbox); err != nil {
			return "", err
		}
		if sandbox == nil || m.sandboxes == nil {
			return "", errors.New("the run has no sandbox")
		}
		return m.sandboxes.Home(*sandbox), nil
	}
	items := func(ctx context.Context) ([]inbox.Item, error) {
		doc, err := machineSettings(ctx)
		if err != nil {
			return nil, err
		}
		return inbox.List(ctx, pool, doc)
	}
	ticketTools.Inbox = items
	if err := ticketTools.Register(srv); err != nil {
		return nil, nil, err
	}
	// The tools of later milestones have their words in the baseline already; they are not offered.
	log.Info("the Crew MCP serves", "tools", srv.Names(), "not yet", srv.Missing())
	m.mcp = srv
	m.runs.MCP = "/mcp"
	// The Crew MCP for the operator's own tools (11.7): a server of its own, which only a personal
	// token reaches, at /mcp of the operator's door.
	number := func(ctx context.Context, key string) int {
		doc, err := machineSettings(ctx)
		if err != nil {
			return 0
		}
		own, _ := doc["mcp_tokens"].(settings.Doc)
		switch v := own[key].(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
		return 0
	}
	m.tokens = &mcp.Tokens{Pool: pool, Days: func(ctx context.Context) int { return number(ctx, "days") },
		PerMinute: func(ctx context.Context) int { return number(ctx, "calls_per_minute") }}
	outside, err := mcp.New(pool, m.tokens.Resolve, log)
	if err != nil {
		return nil, nil, err
	}
	if err := ticketTools.RegisterOutside(outside, srv, items); err != nil {
		return nil, nil, err
	}
	m.outside = outside
	return planner, pipe, nil
}

// openBuilder is the build of a release in a check sandbox (16.2 step 4): Crew's project's canonical
// repository, read only, the sandboxes and the capacity check of every sandbox, and the updater's
// load. Without the orchestrator there is no build.
func openBuilder(ctx context.Context, e env, pool *pgxpool.Pool, m *machine, vs *versions.Service, log *slog.Logger) *versions.Builder {
	loader, ok := vs.Updater.(versions.Loader)
	gates, gok := vs.Updater.(versions.GateUpdater)
	if m == nil || m.orch == nil || m.sandboxes == nil || !ok || !gok {
		return nil
	}
	b := &versions.Builder{
		Pool:     pool,
		Git:      &gitstore.Store{Dir: filepath.Join(e.DataDir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(e.DataDir, "repos"), Log: log}},
		Sandbox:  &harness.Kata{Sandboxes: m.sandboxes, Runtime: m.orch, Log: log},
		Loader:   loader,
		Releases: vs.Releases,
		Builds:   filepath.Join(e.DataDir, "builds"),
		Running:  version.Version,
		Room:     m.room,
		Log:      log,
	}
	// A build that ran when the server stopped lost its sandbox with it.
	if err := b.Recover(ctx); err != nil {
		log.Error("ending the builds a stop cut off", "error", err)
	}
	if err := gateParts(ctx, b, gates, pool); err != nil {
		log.Error("taking up the gates a stop cut off", "error", err)
	}
	return b
}

// gateParts gives a builder the gate's parts (16.2 step 5): the updater's gate, and the live steps,
// which get their board once it exists (wireLive). A gate's make check that a stop cut off runs again.
func gateParts(ctx context.Context, b *versions.Builder, gates versions.GateUpdater, pool *pgxpool.Pool) error {
	b.Gates, b.Live = gates, &versions.LiveRunner{Pool: pool}
	return b.RecoverGates(ctx)
}

// wireLive gives the live steps of the server's builder the board, which moves the real ticket, the
// kick of the board and the pipeline, and who is signed in. It reports whether there were live steps.
func wireLive(vs *versions.Service, d versions.LiveDeps) bool {
	if vs == nil || vs.Builder == nil {
		return false
	}
	live, ok := vs.Builder.Live.(*versions.LiveRunner)
	if ok {
		live.Use(d)
	}
	return ok
}

// openVersions is the server's side of releases: the release folders, read only, and the updater's
// socket (16.4).
func openVersions(e env, pool *pgxpool.Pool, vault *credstore.Vault, log *slog.Logger) (*versions.Service, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	sock := os.Getenv("CREW_UPDATER_SOCKET")
	if sock == "" {
		sock = "/run/crew/updater.sock"
	}
	vs := &versions.Service{
		Pool:     pool,
		Releases: filepath.Join(e.DataDir, "releases"),
		Running:  version.Version,
		Updater:  updaterClient(sock, pool),
		Replacer: versions.DockerReplacer{Docker: cli},
		Settings: func(ctx context.Context) (settings.Doc, error) { return settings.Effective(ctx, pool, e.machine()) },
		// The hold of a release that holds an improvement taken out reads the history, also where the
		// server has no builder (16.3, since 0.55).
		Git: &gitstore.Store{Dir: filepath.Join(e.DataDir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(e.DataDir, "repos"), Log: log}},
		Log: log,
	}
	if vault != nil {
		vs.GitHub = &github.Client{Vault: vault}
		vs.Registry = versions.DockerRegistry{Docker: cli}
	}
	return vs, nil
}

// writeProbe is the server's write probe, started, with its file in the backups folder (21, D31).
func writeProbe(pool *pgxpool.Pool, e env, log *slog.Logger) *hold.WriteProbe {
	p := &hold.WriteProbe{Pool: pool, File: filepath.Join(e.DataDir, "backups", ".db-unwritable"), Log: log}
	p.Start()
	return p
}

// updaterClient reaches the updater's socket; every install ask names updates.keep_versions (D30).
func updaterClient(sock string, pool *pgxpool.Pool) *updater.Client {
	c := updater.NewClient(sock)
	c.Keep = func(ctx context.Context) (int, error) {
		v, err := settings.MachineValue(ctx, pool, "updates.keep_versions")
		n, _ := v.(int)
		return n, err
	}
	return c
}
