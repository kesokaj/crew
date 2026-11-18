// Package httpapi is the server's door for the ingress: the health check, the API, and the UI's
// files (design 15.4, 20.3). The door for the sandboxes is the broker's (internal/credbroker).
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/assistant"
	"github.com/kesokaj/crew/internal/blobstore"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/db/sqlite"
	"github.com/kesokaj/crew/internal/export"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/logins"
	"github.com/kesokaj/crew/internal/mcp"
	"github.com/kesokaj/crew/internal/notify"
	"github.com/kesokaj/crew/internal/pins"
	"github.com/kesokaj/crew/internal/pipeline"
	"github.com/kesokaj/crew/internal/planning"
	"github.com/kesokaj/crew/internal/projterm"
	"github.com/kesokaj/crew/internal/rewrite"
	"github.com/kesokaj/crew/internal/rooms"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/sessions"
	"github.com/kesokaj/crew/internal/settings"
	"github.com/kesokaj/crew/internal/setup"
	"github.com/kesokaj/crew/internal/version"
	"github.com/kesokaj/crew/internal/versions"
)

// The headers the ingress sets on every request it lets through, after it removed any copy the
// client sent (ingress/generator.go).
const (
	HeaderEmail   = "X-Caddy-Email"
	HeaderIngress = "X-Crew-Ingress"
)

// Server serves the door for the ingress.
type Server struct {
	// Pool is the database, set once it answers: the server serves before that, and says so (15.3).
	Pool         atomic.Pointer[pgxpool.Pool]
	Machine      settings.Machine
	Hostname     string // CREW_HOSTNAME: requests that change something come from https://<it>
	AppsDomain   string // CREW_APPS_DOMAIN: hosted projects' addresses are under it (19.4)
	AdminEmail   string
	IngressToken []byte // the secret the ingress and the server share
	WebDir       string // the built UI; empty serves no UI
	Log          *slog.Logger
	// CrewSource is where Crew's project is read from for updates.source_repo (16.2); nil is GitHub.
	CrewSource func(repo string) board.CrewSource
	// FromIngress reports whether an address a request came from is on the ingress's own network, or
	// is the server's loopback, where Docker's health check and crew admin ask. A request from
	// anywhere else is not for this door, whether or not the sandboxes are known yet (13.6, 20.3).
	FromIngress func(remote string) bool
	// SandboxDoor reports whether an address of the server is on a sandbox's bridge. A request that
	// arrives there is not for this door, whatever it carries (20.3, "Two doors").
	SandboxDoor func(local string) bool
	// SandboxGuest reports whether an address a request came from is on a sandbox's or a hosted
	// project's bridge. Such a request is not for this door either, at whichever address of the server
	// it arrived (13.6, 20.3).
	SandboxGuest func(remote string) bool
	// Setup is set once the database, the key file, and Docker are there.
	Setup atomic.Pointer[setup.Service]
	// Logins is the login check, set at the same time.
	Logins atomic.Pointer[logins.Service]
	// Runs is the run driver, set once the database and Docker are there (12.4).
	Runs atomic.Pointer[runs.Driver]
	// Room is the capacity check (7.9), for the front page.
	Room atomic.Pointer[capacity.Gate]
	// Board is the board core, and Export writes a project out; both are set once the database
	// answers.
	Board  atomic.Pointer[board.Board]
	Export atomic.Pointer[export.Exporter]
	// Drafts are the plan drafts, and Planner the orchestrator that starts the planning agent and the
	// lead (7.2, 15.2); the planner is set once Docker is there.
	Drafts   atomic.Pointer[planning.Drafts]
	Planner  atomic.Pointer[planning.Planner]
	Pipeline atomic.Pointer[pipeline.Pipeline]
	// Rooms manages unified #<project> channels and per-feature threads (M24, R214).
	Rooms atomic.Pointer[rooms.Service]
	// BlobDir, Blobs, and SQLite hold split storage drivers (M25, R220).
	BlobDir string
	Blobs   atomic.Pointer[blobstore.Store]
	SQLite  atomic.Pointer[sqlite.WALStore]
	// Assistant keeps the operator's conversations and the tickets' chats (11.8, 11.9), set once the
	// runs can start.
	Assistant atomic.Pointer[assistant.Service]
	// Terminals are the project terminals (13.11), set once the database and Docker are there.
	Terminals atomic.Pointer[projterm.Service]
	// Rewrites is the prompt writer (7.2), set once the runs can start.
	Rewrites atomic.Pointer[rewrite.Service]
	// Versions is the server's side of releases (16.3), set once the database answers.
	Versions atomic.Pointer[versions.Service]
	// Pins is the daily look (12.8, since 0.56), set in the running server only: a check server has none.
	Pins atomic.Pointer[pins.Look]
	// Backups lists where the daily backups go and the files there, for the Database screen.
	Backups func(ctx context.Context) (dir string, files []string, err error)
	// ProjectHomeDir optionally overrides the per-project warm-VM home path in tests (M26).
	ProjectHomeDir func(projectID string) string
	// OutsideMCP is the Crew MCP for the operator's own tools, at /mcp of this door, which the
	// ingress lets through without the sign-in: a personal token is its only key (11.7). Tokens are
	// those tokens, made and revoked in the app.
	OutsideMCP atomic.Pointer[http.Handler]
	Tokens     atomic.Pointer[mcp.Tokens]
	// Hosting hosts projects on the machine (19), once the database and the key file are there.
	Hosting atomic.Pointer[hosting.Service]
	// Notify sends the mail (18), once the database answers.
	Notify atomic.Pointer[notify.Notifier]
	// ReadOnly is check mode (16.4): the release that is checked answers and reads, and refuses
	// every request that would change something. It writes no device either (D21).
	ReadOnly bool

	sessOnce sync.Once
	sess     *sessions.Store
	// api is the API's routes, which Confirm of the inbox serves an action through (D11).
	api http.Handler
}

// Handler routes the requests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	// The ingress asks for the hosted routes on a door of its own, outside the API (19.4).
	mux.HandleFunc("GET /ingress/routes", s.ingressRoutes)
	api, _ := s.apiRoutes()
	// Confirm of the inbox serves an alternative's action through these same routes (D11).
	s.api = api
	// The API checks the session of each request (20.3); the UI's files are served to a device signed
	// out too, so its page can say so and offer the sign-in.
	mux.Handle("/api/", s.operator(s.sessionChecked(api)))
	// The operator's own tools reach the Crew MCP with a personal token, which the server checks itself
	// (11.7): this one path is not behind the sign-in, and nothing else is reached through it.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		h := s.OutsideMCP.Load()
		if h == nil {
			writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the Crew MCP is not ready")
			return
		}
		(*h).ServeHTTP(w, r)
	})
	mux.Handle("/", s.operator(http.HandlerFunc(s.ui)))
	return s.notFromSandboxes(s.readOnly(mux))
}

// apiRoutes are the API's routes, with the patterns they were registered by: each route asks for a
// fresh sign-in unless plainRoutes names it (20.3).
func (s *Server) apiRoutes() (*http.ServeMux, []string) {
	api := &routes{s: s, mux: http.NewServeMux()}
	api.HandleFunc("GET /api/whoami", s.whoami)
	api.HandleFunc("GET /api/settings/machine", s.getSettings)
	api.HandleFunc("PUT /api/settings/machine", s.putSettings)
	api.HandleFunc("GET /api/settings/machine/history", s.machineHistory)
	api.HandleFunc("POST /api/settings/machine/putback", s.machinePutBack)
	api.HandleFunc("GET /api/settings/schema", s.settingsSchema)
	s.setupRoutes(api)
	s.notificationRoutes(api)
	s.hostingRoutes(api)
	s.inboxDecisionRoutes(api)
	s.cliRoutes(api)
	s.modelRoutes(api)
	s.holdRoutes(api)
	s.reportRoutes(api)
	s.afterDoneRoutes(api)
	s.ticketListRoutes(api)
	s.runRoutes(api)
	s.agentAPIRoutes(api)
	s.workflowRoutes(api)
	s.m22Routes(api)
	s.m23Routes(api)
	s.roomRoutes(api)
	s.m25Routes(api)
	s.m26Routes(api)
	s.projectRoutes(api)
	s.ticketRoutes(api)
	s.knowledgeRoutes(api)
	s.draftRoutes(api)
	s.conversationRoutes(api)
	s.frameRoutes(api)
	s.sandboxRoutes(api)
	s.activityRoutes(api)
	s.gitRoutes(api)
	s.trajectoryRoutes(api)
	s.databaseRoutes(api)
	s.versionRoutes(api)
	s.analyticsRoutes(api)
	s.overrideRoutes(api)
	s.evalRoutes(api)
	s.improvementRoutes(api)
	s.crewProjectRoutes(api)
	s.pinRoutes(api)
	s.terminalRoutes(api)
	s.rewriteRoutes(api)
	s.tokenRoutes(api)
	s.sessionRoutes(api)
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint")
	})
	return api.mux, api.patterns
}

// readOnly refuses, in check mode, every request but one that reads.
func (s *Server) readOnly(next http.Handler) http.Handler {
	if !s.ReadOnly {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "this is a release in check mode: it only reads")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) notFromSandboxes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.FromIngress != nil && !s.FromIngress(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		if s.SandboxDoor != nil {
			if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); !ok || s.SandboxDoor(a.String()) {
				http.NotFound(w, r)
				return
			}
		}
		if s.SandboxGuest != nil && s.SandboxGuest(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// health answers without the sign-in: the ingress serves this path open, with every identity header
// removed (ingress/route.go). It says only whether the server and its database answer.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	body := map[string]string{"status": "ok", "version": version.Version, "database": "ok"}
	code := http.StatusOK
	// Healthy is that the database can be read and written (16.8): an update that changes no row is
	// still refused by a database that does not take writes.
	if pool := s.Pool.Load(); pool == nil || pool.Ping(ctx) != nil || execErr(pool.Exec(ctx, `UPDATE crew_state SET id = id WHERE false`)) != nil {
		body["status"], body["database"] = "degraded", "down"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, body)
}

// operator lets a request through only when the ingress vouches for it, with the shared secret, and
// for the admin email. A request that changes something must also come from a page of Crew's own
// address (20.3).
func (s *Server) operator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(HeaderIngress))
		if len(s.IngressToken) == 0 || subtle.ConstantTimeCompare(got, s.IngressToken) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "this door is for the ingress")
			return
		}
		email := strings.TrimSpace(r.Header.Get(HeaderEmail))
		if email == "" || !strings.EqualFold(email, s.AdminEmail) {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "only the admin email may use Crew")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "a change must come from a page of Crew's own address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	return strings.EqualFold(origin, "https://"+s.Hostname)
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"email": r.Header.Get(HeaderEmail), "version": version.Version})
}

// pool returns the database, or answers 503 and nil while it does not answer yet.
func (s *Server) pool(w http.ResponseWriter) *pgxpool.Pool {
	p := s.Pool.Load()
	if p == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the database does not answer yet")
	}
	return p
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	d, err := settings.Effective(r.Context(), pool, s.Machine)
	if err != nil {
		s.Log.Error("reading the settings", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the settings could not be read")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// The largest settings document the API takes.
const maxSettingsBody = 1 << 20

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "the settings are too large")
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	d, err := settings.ParseJSON(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err := settings.SaveGuarded(r.Context(), pool, s.Machine, d, r.Header.Get(HeaderEmail), machineGuard(r)); err != nil {
		if s.writeStale(w, err) {
			return
		}
		var inv *settings.InvalidError
		if errors.As(err, &inv) {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", inv.Error())
			return
		}
		s.Log.Error("saving the settings", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the settings could not be saved")
		return
	}
	s.getSettings(w, r)
}

// machineHistory is a key's history in the machine's settings (14.2): ?key=, a dotted path.
func (s *Server) machineHistory(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	h, err := settings.MachineHistory(r.Context(), pool, s.Machine, r.URL.Query().Get("key"))
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// putBack is what a Put back names: a change of the set, and the key of it to put back.
type putBack struct {
	Seq int64  `json:"seq"`
	Key string `json:"key"`
}

// machinePutBack puts a key of the machine's settings back to its value before a change, as a change
// of its own, and answers the settings after it.
func (s *Server) machinePutBack(w http.ResponseWriter, r *http.Request) {
	var in putBack
	if !decode(w, r, &in) {
		return
	}
	pool := s.pool(w)
	if pool == nil {
		return
	}
	if err := settings.PutBack(r.Context(), pool, s.Machine, in.Seq, in.Key, r.Header.Get(HeaderEmail), machineGuard(r)); err != nil {
		s.settingsError(w, err)
		return
	}
	s.getSettings(w, r)
}

// settingsSchema is what the screens need of the two schemas beyond the values.
func (s *Server) settingsSchema(w http.ResponseWriter, _ *http.Request) {
	v, err := settings.Schemas(s.Machine)
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// The page the UI's build writes, which every route of the UI loads (TanStack Start in SPA mode).
const shell = "_shell.html"

// ui serves the built UI. A path that is not a file is a route of the UI, so it gets the shell.
func (s *Server) ui(w http.ResponseWriter, r *http.Request) {
	if s.WebDir == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no UI in this build")
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	path := filepath.Join(s.WebDir, clean)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() && filepath.Base(path) != shell {
		w.Header().Set("Content-Security-Policy", basePolicy+"; script-src 'self'")
		http.ServeFile(w, r, path)
		return
	}
	s.serveShell(w)
}

// The policy of everything the UI serves: nothing but Crew's own address.
const basePolicy = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// serveShell sends the shell with a fresh nonce on each of its script elements, and a policy that
// runs only scripts with that nonce and those they load ('strict-dynamic'). The app starts with an
// inline script and creates another while it starts, so a policy of 'self' or of hashes left the
// page blank, and 'unsafe-inline' would let injected markup run.
func (s *Server) serveShell(w http.ResponseWriter) {
	body, err := os.ReadFile(filepath.Join(s.WebDir, shell))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no UI in this build")
		return
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "no randomness")
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(b)
	body = scriptTag.ReplaceAll(body, []byte(`<script nonce="`+nonce+`"$1`))
	// The same nonce lets the page's own code add style elements: the terminal (xterm.js) makes two
	// as it starts, and the page puts the nonce on them (web/src/lib/stylenonce.ts). Styles from
	// elsewhere stay refused, and 'unsafe-inline' stays out.
	w.Header().Set("Content-Security-Policy", basePolicy+"; script-src 'nonce-"+nonce+"' 'strict-dynamic'; style-src 'self' 'nonce-"+nonce+"'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// scriptTag matches the start of a script element, with what follows the name.
var scriptTag = regexp.MustCompile(`<script(\s|>)`)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers in the shape {"error": {"code", "status", "message"}}.
func writeError(w http.ResponseWriter, code int, status, message string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "status": status, "message": message}})
}

func execErr(_ pgconn.CommandTag, err error) error { return err }
