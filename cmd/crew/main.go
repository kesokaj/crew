// Command crew is Crew's server. `crew admin` is the same binary, for what crew-ctl passes on: it is
// run through Docker on the machine, never over a network (design 16.11, 20.3).
//
//	crew serve [--check]             the server (the default); --check beside a running one (16.4)
//	crew admin migrate               apply the schema steps that are not applied, as crew_migrate
//	crew admin backup                a dump now, into the backup folder
//	crew admin restore FILE          read a dump back, as crew_migrate
//	crew admin settings export       the machine's settings as crew.yaml, on standard output
//	crew admin settings import FILE  read crew.yaml back; a file that does not validate changes nothing
//	crew admin proof                 write a row and read it back: install's proof (16.11)
//	crew admin proof event           a microVM prints an event, which is stored (in the server's container)
//	crew admin proof store           the event a microVM printed, on stdin, is stored: the check server's
//	                                 smoke run, whose microVM the updater starts (16.4, since 0.52)
//	crew admin selftest              one ticket from its OK to Done with the fake harness (16.11)
//	crew admin restart-check --reboot-required=yes|no
//	                                 whether the machine may restart for security updates now: exit 0
//	                                 is the one yes, after its news is written (16.10; crew-ctl's timer)
//	crew admin sandbox ...           start, list, and remove sandboxes (in the server's container)
//	crew admin run ...               start, follow, stop, and continue runs (in the server's container)
//	crew admin repo seed NAME URL    bring a repository into the git endpoint (in the server's container)
//	crew admin project ...           list, export, and import projects (in the server's container)
//	crew admin api METHOD PATH [BODY] call one route of the API as the operator (in the server's container)
//	crew admin login ...             check a CLI's login, store one, or offer a renewed one
//	crew health                      ask this container's server for its health check, for Docker
//	crew release write ...           write release.json for crew-ctl build (16.11)
//	crew version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/backup"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/selftest"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/version"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, log); err != nil {
		var u usageError
		if errors.As(err, &u) {
			fmt.Fprintln(os.Stderr, "crew:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "crew:", err)
		os.Exit(1)
	}
}

type usageError string

func (u usageError) Error() string { return string(u) }

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, log *slog.Logger) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return nil
	case "serve":
		e := loadEnv()
		if len(args) == 2 && args[1] == "--check" {
			e.Check = true
		} else if len(args) > 1 {
			return usageError("serve [--check]")
		}
		return serve(ctx, e, log)
	case "health":
		return health(ctx, loadEnv(), stdout)
	case "release":
		return releaseCmd(args[1:], stdin, stdout)
	case "admin":
		return admin(ctx, loadEnv(), args[1:], stdin, stdout)
	case gitendpoint.HookCommand:
		// The git endpoint's hook asks the server's own program which files a push may change (13.8).
		if code := gitendpoint.PreReceive(stdin, os.Stderr); code != 0 {
			os.Exit(code)
		}
		return nil
	}
	return usageError(fmt.Sprintf("unknown command %q: serve, admin, health, or version", args[0]))
}

// env is what the server takes from its environment: the values of the install file that it needs,
// and where its files are. Secrets are files, never values in the environment.
type env struct {
	Hostname         string
	AppsDomain       string // CREW_APPS_DOMAIN: hosted projects' addresses are under it (19.4)
	AdminEmail       string
	SourceRepo       string
	Timezone         string
	Listen           string
	DataDir          string
	WebDir           string
	IngressTokenFile string
	// The sandboxes (13.3): the runtime image, the Docker runtime that makes it a microVM, the
	// server's own container, which joins each sandbox's bridge, and the broker's listener there.
	RuntimeImage  string
	Runtime       string
	SelfContainer string
	// IngressContainer is the ingress's container, which the server joins to each hosted bridge (19.4).
	IngressContainer string
	BrokerListen     string
	SecretKeyFile    string
	// Check is check mode (16.4): the server of a release that is tried beside the running one.
	Check bool
	// TLS is the install file's CREW_TLS, and IngressAddr where the ingress answers on the Compose
	// network: the daily look at its certificate (16.10).
	TLS         string
	IngressAddr string
}

func loadEnv() env {
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	return env{
		Hostname:         os.Getenv("CREW_HOSTNAME"),
		AppsDomain:       os.Getenv("CREW_APPS_DOMAIN"),
		AdminEmail:       os.Getenv("CREW_ADMIN_EMAIL"),
		SourceRepo:       os.Getenv("CREW_SOURCE_REPO"),
		Timezone:         os.Getenv("CREW_TIMEZONE"),
		Listen:           get("CREW_LISTEN", ":8080"),
		DataDir:          get("CREW_DATA_DIR", "/var/lib/crew"),
		WebDir:           get("CREW_WEB_DIR", "/usr/share/crew/web"),
		IngressTokenFile: get("CREW_INGRESS_TOKEN_FILE", "/run/secrets/ingress_token"),
		RuntimeImage:     os.Getenv("CREW_RUNTIME_IMAGE"),
		Runtime:          get("CREW_RUNTIME", "mvm"),
		SelfContainer:    os.Getenv("CREW_SELF_CONTAINER"),
		IngressContainer: os.Getenv("CREW_INGRESS_CONTAINER"),
		BrokerListen:     get("CREW_BROKER_LISTEN", ":8091"),
		SecretKeyFile:    get("CREW_SECRET_KEY_FILE", "/run/secrets/secret_key"),
		Check:            os.Getenv("CREW_CHECK") == "1",
		TLS:              get("CREW_TLS", "acme"),
		IngressAddr:      get("CREW_INGRESS_ADDR", "crew-caddy:443"),
	}
}

func (e env) machine() settings.Machine {
	return settings.Machine{Version: version.Version, AdminEmail: e.AdminEmail, SourceRepo: e.SourceRepo}
}

func (e env) location() *time.Location {
	if e.Timezone == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

func admin(ctx context.Context, e env, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError("admin: migrate, backup, restore, settings, proof, selftest, pause, resume, restart-check, sandbox, login, run, repo, project, or api")
	}
	switch args[0] {
	case "migrate":
		// --at-once is the switch of an update (16.4): every new step in one transaction.
		atOnce := len(args) == 2 && args[1] == "--at-once"
		if len(args) > 1 && !atOnce {
			return usageError("admin migrate [--at-once]")
		}
		conn, err := connect(ctx, db.RoleMigrate)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		migrate := db.Migrate
		if atOnce {
			migrate = db.MigrateAtOnce
		}
		applied, err := migrate(ctx, conn)
		for _, name := range applied {
			fmt.Fprintln(stdout, "applied", name)
		}
		if err != nil {
			return err
		}
		n, err := db.Applied(ctx, conn)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "the schema is at step %d\n", n)
		return nil

	case "backup":
		// --snapshot FILE is the updater's (16.4): the database alone, beside the month files.
		snapshot := ""
		switch {
		case len(args) == 3 && args[1] == "--snapshot":
			snapshot = args[2]
		case len(args) != 1:
			return usageError("admin backup [--snapshot FILE]")
		}
		pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
		if err != nil {
			return err
		}
		defer pool.Close()
		dir, keep, err := backupPlace(ctx, pool, e)
		if err != nil {
			return err
		}
		if snapshot != "" {
			if err := backup.Snapshot(ctx, db.ConnFromEnv(db.RoleRead), dir, snapshot); err != nil {
				return err
			}
			fmt.Fprintln(stdout, snapshot)
			return nil
		}
		file, err := dailyBackup(ctx, pool, e, dir, keep)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, file)
		return nil

	case "restore":
		// --no-months leaves the ended months out: the scratch copy of an update (16.4).
		opt := backup.Options{Canonical: filepath.Join(e.DataDir, "canonical"), MonthsRoot: filepath.Join(e.DataDir, "backups"),
			Hosted: filepath.Join(e.DataDir, "hosted")}
		switch {
		case len(args) == 3 && args[2] == "--no-months":
			opt.NoMonths = true
		case len(args) != 2:
			return usageError("admin restore FILE [--no-months]")
		}
		if err := backup.RestoreWith(ctx, db.ConnFromEnv(db.RoleMigrate), args[1], opt); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "restored", args[1])
		return nil

	case "settings":
		return adminSettings(ctx, e, args[1:], stdin, stdout)

	case "pause", "resume":
		// Pause all, and Resume, from the machine when the app cannot be opened (7.9): the row every
		// start asks, written here; the server acts on it at its next start of anything.
		pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
		if err != nil {
			return err
		}
		defer pool.Close()
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if args[0] == "resume" {
				paused, err := hold.Resume(ctx, tx, board.Operator)
				if err == nil {
					fmt.Fprintf(stdout, "Crew is resumed after %s\n", paused.Round(time.Second))
				}
				return err
			}
			if _, err := hold.Pause(ctx, tx, "pause", board.Operator); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "Crew is paused: no new run starts, and running runs finish")
			return nil
		})

	case "selftest":
		// One ticket on a project of its own, from the OK to Done, with the fake harness (16.11).
		pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
		if err != nil {
			return err
		}
		defer pool.Close()
		res, err := selftest.Run(ctx, pool, version.Version, 3*time.Minute)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "selftest: %s of %s is %s after %s; turns: %s\n", res.Ticket, res.Project, res.Stage, res.Took, strings.Join(res.Turns, " "))
		for _, p := range res.Problems {
			fmt.Fprintln(stdout, "  "+p)
		}
		if !res.Done {
			return errors.New("the selftest's ticket did not reach Done")
		}
		return nil

	case "proof":
		if len(args) > 1 && args[1] == "event" {
			return proofEvent(ctx, e, stdout)
		}
		if len(args) > 1 && args[1] == "store" {
			return proofStore(ctx, stdin, stdout)
		}
		return proof(ctx, stdout)

	case "run":
		return adminRun(ctx, e, args[1:], stdout)

	case "repo":
		return adminRepo(ctx, e, args[1:], stdout)

	case "project":
		return adminProject(ctx, e, args[1:], stdin, stdout)

	case "api":
		return adminAPI(ctx, e, args[1:], stdin, stdout)

	case "sandbox":
		return adminSandbox(ctx, e, args[1:], stdout)

	case "login":
		return adminLogin(ctx, e, args[1:], stdout)

	case "probe":
		return adminProbe(ctx, e, args[1:], stdout)

	case "restart-check":
		return restartCheck(ctx, e, args[1:], stdout)
	}
	return usageError(fmt.Sprintf("admin: unknown command %q", args[0]))
}

func connect(ctx context.Context, role string) (*pgx.Conn, error) {
	dsn, err := db.ConnFromEnv(role).URL()
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database as %s: %w", role, err)
	}
	return conn, nil
}

// backupPlace reads where backups go and how many are kept from the machine's settings (14.1).
func backupPlace(ctx context.Context, q db.Querier, e env) (string, int, error) {
	s, err := settings.Effective(ctx, q, e.machine())
	if err != nil {
		return "", 0, err
	}
	u, _ := s["updates"].(settings.Doc)
	dir, _ := u["backup_path"].(string)
	keep, _ := u["backup_keep"].(int)
	if dir == "" || keep < 1 {
		return "", 0, errors.New("updates.backup_path and updates.backup_keep must be set")
	}
	// Backups go only into the backups folder of the data folder: it is the one folder of it that
	// the server's container mounts, and the one that restore reads from.
	root := filepath.Join(e.DataDir, "backups")
	if clean := filepath.Clean(dir); clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", 0, fmt.Errorf("updates.backup_path %q is not under %s", dir, root)
	}
	return dir, keep, nil
}

// dailyBackup makes a day's backup with the repositories of the projects that have no remote (16.9).
func dailyBackup(ctx context.Context, q db.Querier, e env, dir string, keep int) (string, error) {
	rows, err := q.Query(ctx, `SELECT g.name FROM git_repos g JOIN projects p ON p.id = g.project WHERE p.remote IS NULL ORDER BY g.name`)
	if err != nil {
		return "", err
	}
	repos := map[string]string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		path := filepath.Join(e.DataDir, "canonical", name+".git")
		if _, err := os.Stat(path); err == nil {
			repos[name] = path
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	// Each hosted project's data, which its app may write while it is read (plan M19 D18).
	return backup.Daily(ctx, backup.Plan{Conn: db.ConnFromEnv(db.RoleRead), Dir: dir, Keep: keep, Now: time.Now(), Repos: repos,
		Hosted: filepath.Join(e.DataDir, "hosted"),
		Note:   func(line string) { slog.Warn("the backup " + line) }})
}

// listBackups is the folder backups go to and the names of the files there for the Database screen
// (15.3): the days' files, newest last, then the month files (16.9).
func listBackups(ctx context.Context, q db.Querier, e env) (string, []string, error) {
	dir, _, err := backupPlace(ctx, q, e)
	if err != nil {
		return "", nil, err
	}
	files, err := backup.List(dir)
	if err != nil {
		return dir, nil, err
	}
	for i := range files {
		files[i] = filepath.Base(files[i])
	}
	months, err := backup.ListMonths(dir)
	for _, m := range months {
		files = append(files, backup.MonthName(m))
	}
	return dir, files, err
}

func adminSettings(ctx context.Context, e env, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		return usageError("admin settings export | admin settings import FILE")
	}
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	if args[0] == "export" {
		d, err := settings.Effective(ctx, pool, e.machine())
		if err != nil {
			return err
		}
		out, err := settings.Format(d)
		if err != nil {
			return err
		}
		_, err = stdout.Write(out)
		return err
	}
	if len(args) != 2 {
		return usageError("admin settings import FILE (- for standard input)")
	}
	var body []byte
	if args[1] == "-" {
		body, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
	} else {
		body, err = os.ReadFile(args[1])
	}
	if err != nil {
		return err
	}
	d, err := settings.Parse(body)
	if err != nil {
		return err
	}
	if err := settings.Save(ctx, pool, e.machine(), d, "crew-ctl"); err != nil {
		return fmt.Errorf("the settings were not changed:\n%w", err)
	}
	fmt.Fprintln(stdout, "the settings are imported")
	return nil
}

// proof writes a row and reads it back, as the server's role: install's proof that the database
// works (16.11). The row is one line of history, so it is kept.
func proof(ctx context.Context, stdout io.Writer) error {
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := db.RecordChange(ctx, tx, "crew-ctl", "install", "proof", nil, map[string]string{"at": at, "version": version.Version}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	var got string
	err = pool.QueryRow(ctx, `SELECT after->>'at' FROM changes WHERE tbl = 'install' AND row_key = 'proof'
		ORDER BY seq DESC LIMIT 1`).Scan(&got)
	if err != nil {
		return err
	}
	if got != at {
		return fmt.Errorf("wrote %s and read back %s", at, got)
	}
	fmt.Fprintln(stdout, "the database answers: a row was written and read back")
	return nil
}
