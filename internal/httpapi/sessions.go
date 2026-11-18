package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/sessions"
	"github.com/kesokaj/crew/internal/settings"
)

// The headers the ingress passes on Crew's route from the token of the sign-in, after it removed any
// copy the client sent (ingress/generator.go): the sign-in's time, the token's iat in decimal
// seconds, and the device's id, its jti (design 20.3).
const (
	HeaderAuthTime = "X-Crew-Auth-Time"
	HeaderSession  = "X-Crew-Session"
)

// The reasons of a 401 that asks for a sign-in, in the error's details: the page offers the sign-in
// each names.
const (
	ReasonFresh      = "FRESH_SIGN_IN"
	ReasonOver       = "SESSION_OVER"
	ReasonSignedOut  = "SIGNED_OUT"
	maxSessionID     = 200
	authTimeFromSkew = time.Minute
)

// freshRoutes are the routes that ask for a fresh sign-in, at most sessions.fresh_minutes old (20.3):
// what could hand over the machine or the credentials. plainRoutes are every other route. Each route
// is in one of them, which TestEveryRouteIsFreshOrPlain checks, so a new route cannot be added
// without the choice; a route in neither is held as fresh.
var freshRoutes = []string{
	// Opening a terminal and its edit mode: the project's, a run's, and Setup's, which holds the logins.
	"POST /api/projects/{p}/terminal",
	"PUT /api/projects/{p}/terminal/mode",
	"GET /api/projects/{p}/terminal/ws",
	"GET /api/setup/terminal",
	// A secret: the GitHub App's key.
	"POST /api/setup/github-app",
	// Creating or changing a personal token.
	"POST /api/crew/mcp/tokens",
	"PATCH /api/crew/mcp/tokens/{id}",
	"POST /api/crew/mcp/tokens/{id}/renew",
	// Install, Install now and a roll back are one request; letting a held release install is a
	// change of how releases install.
	"POST /api/crew/versions/{v}/install",
	"POST /api/crew/versions/{v}/gate/install",
	"POST /api/crew/versions/{v}/deploy",
	"DELETE /api/crew/versions/{v}/hold",
	// Lifting a hold: Crew's pause or stop, and the boot hold.
	"POST /api/crew/resume",
	"POST /api/machine/boot/resume",
	// Signing a device out.
	"POST /api/sessions/{id}/signout",
	// Taking a held main, whose change of code, hooks or workflows waited for the operator (rule 13),
	// and a project's files written out whole (security review of M17's Close).
	"POST /api/projects/{p}/remote/mains/{id}/take",
	"POST /api/projects/{p}/terminal/mains/{id}/take",
	"POST /api/projects/{p}/export",
	// The mail account's app password, a secret (18.4).
	"PUT /api/setup/mail",
	// A hosted project's settings and secrets (19.3, plan M19 D12).
	"PUT /api/projects/{p}/hosting/settings",
	// A roll back can put back older data, and the version that had it (plan M19 D12, D15).
	"POST /api/projects/{p}/hosting/deploys/{d}/rollback",
	// Installing a skill into .agents/skills/ or VENDORED.md modifies main (Rule 13, M26).
	"POST /api/projects/{p}/skills-registry/install",
	// Building and switching a project's custom sandbox image runs an image build (M25).
	"POST /api/projects/{p}/sandbox/build-image",
}

var plainRoutes = []string{
	"/api/",
	"DELETE /api/crew/comparisons/{id}",
	"DELETE /api/projects/{p}",
	"DELETE /api/projects/{p}/stages/{id}",
	"DELETE /api/projects/{p}/terminal",
	"GET /api/activity",
	"GET /api/clis",
	"GET /api/crew/analytics",
	"GET /api/crew/baseline",
	"GET /api/crew/baseline/file",
	"PUT /api/crew/baseline/file",
	"DELETE /api/crew/baseline/file",
	"GET /api/crew/comparisons",
	"GET /api/crew/hold",
	"GET /api/crew/improvements",
	"GET /api/crew/improvements/{n}",
	"GET /api/crew/mcp",
	"GET /api/crew/mcp/tokens",
	"GET /api/crew/mcp/tools",
	"GET /api/crew/models",
	"GET /api/crew/models/{cli}/{model}",
	"GET /api/crew/pins",
	"GET /api/crew/project",
	"GET /api/crew/quota",
	"GET /api/crew/signals",
	"GET /api/crew/updater",
	"GET /api/crew/versions",
	"GET /api/crew/versions/{v}",
	"GET /api/database",
	"GET /api/conversations",
	"POST /api/conversations",
	"GET /api/conversations/{id}",
	"POST /api/conversations/{id}/messages",
	"POST /api/conversations/{id}/pick",
	"POST /api/conversations/{id}/save",
	"POST /api/conversations/{id}/close",
	"DELETE /api/conversations/{id}",
	"POST /api/conversations/{id}/ticket",
	"POST /api/conversations/{id}/tell",
	"POST /api/conversations/{id}/remember",
	"POST /api/conversations/{id}/plan",
	"GET /api/conversations/{id}/files",
	"GET /api/conversations/{id}/preview/{path...}",
	"POST /api/conversations/{id}/branch",
	"GET /api/projects/{p}/tasks/{t}/chat",
	"POST /api/projects/{p}/tasks/{t}/chat",
	"GET /api/drafts",
	"GET /api/drafts/{d}",
	"GET /api/frame",
	"GET /api/inbox",
	"GET /api/inbox/item",
	"GET /api/notifications",
	"GET /api/overview",
	"GET /api/projects",
	"GET /api/projects/{p}/activity",
	"GET /api/projects/{p}/agents",
	"GET /api/projects/{p}/analysis",
	"GET /api/projects/{p}/analytics",
	"GET /api/projects/{p}/board",
	"GET /api/projects/{p}/bugs",
	"GET /api/projects/{p}/decisions",
	"GET /api/projects/{p}/docs",
	"GET /api/projects/{p}/docs/page",
	"GET /api/projects/{p}/docs/search",
	"GET /api/projects/{p}/evals",
	"GET /api/projects/{p}/evals/runs",
	"GET /api/projects/{p}/feedback",
	"GET /api/projects/{p}/git",
	"GET /api/projects/{p}/git/branches",
	"GET /api/projects/{p}/git/commits/{c}",
	"GET /api/projects/{p}/git/compare",
	"GET /api/projects/{p}/git/log",
	"GET /api/projects/{p}/git/tree",
	"GET /api/projects/{p}/git/blob",
	"GET /api/projects/{p}/hosting",
	"GET /api/projects/{p}/hosting/logs",
	"POST /api/projects/{p}/hosting/deploys",
	"DELETE /api/projects/{p}/hosting",
	"GET /api/projects/{p}/mcp/calls",
	"GET /api/projects/{p}/memory",
	"GET /api/projects/{p}/network",
	"GET /api/projects/{p}/overrides",
	"GET /api/projects/{p}/overview",
	"GET /api/projects/{p}/plan",
	"GET /api/projects/{p}/records/{id}/files/{name}",
	"GET /api/projects/{p}/report",
	"GET /api/projects/{p}/runs",
	"GET /api/projects/{p}/sandbox",
	"POST /api/projects/{p}/sandbox/start",
	"POST /api/projects/{p}/sandbox/suspend",
	"POST /api/projects/{p}/sandbox/stop",
	"POST /api/projects/{p}/sandbox/snapshot",
	"POST /api/projects/{p}/sandbox/snapshots/{id}/restore",
	"GET /api/projects/{p}/sandboxes",
	"GET /api/projects/{p}/settings",
	"GET /api/projects/{p}/settings/history",
	"GET /api/projects/{p}/skills",
	"GET /api/projects/{p}/skills/file",
	"PUT /api/projects/{p}/skills/file",
	"DELETE /api/projects/{p}/skills/file",
	"POST /api/projects/{p}/skills/request",
	"GET /api/projects/{p}/stages",
	"GET /api/projects/{p}/summaries",
	"GET /api/projects/{p}/tasks/{t}",
	"GET /api/projects/{p}/tasks/{t}/agents",
	"GET /api/projects/{p}/tasks/{t}/git",
	"GET /api/projects/{p}/tasks/{t}/subtasks",
	"GET /api/projects/{p}/tasks/{t}/summary",
	"GET /api/projects/{p}/terminal",
	"GET /api/projects/{p}/terminal/mains",
	"GET /api/projects/{p}/tickets",
	"GET /api/rewrites/{id}",
	"GET /api/agents/runtime",
	"GET /api/agents/sessions",
	"POST /api/agents/sessions",
	"GET /api/agents/sessions/{id}",
	"POST /api/agents/sessions/{id}/turn",
	"POST /api/agents/sessions/{id}/steer",
	"POST /api/agents/sessions/{id}/abort",
	"GET /api/agents/sessions/{id}/stream",
	"GET /api/projects/{p}/workflow",
	"POST /api/projects/{p}/workflow/sync",
	"GET /api/projects/{p}/tasks/{t}/yaml",
	"POST /api/projects/{p}/tickets/sync",
	"GET /api/projects/{p}/factory-tasks",
	"POST /api/projects/{p}/factory-tasks",
	"GET /api/projects/{p}/factory-tasks/{id}",
	"POST /api/projects/{p}/factory-tasks/{id}/complete",
	"GET /api/projects/{p}/tasks-ledger",
	"POST /api/projects/{p}/coordinator/rank",
	"POST /api/projects/{p}/coordinator/dispatch",
	"GET /api/queue/global",
	"GET /api/projects/{p}/tasks/{t}/research",
	"POST /api/projects/{p}/tasks/{t}/research",
	"GET /api/projects/{p}/specs",
	"POST /api/projects/{p}/specs",
	"GET /api/projects/{p}/specs/{spec}",
	"POST /api/projects/{p}/specs/ask",
	"POST /api/projects/{p}/specs/{spec}/decide",
	"POST /api/projects/{p}/specs/{spec}/decision",
	"GET /api/projects/{p}/spec-decisions",
	"GET /api/rooms",
	"GET /api/projects/{p}/room",
	"GET /api/projects/{p}/room/threads",
	"POST /api/projects/{p}/room/threads",
	"GET /api/projects/{p}/room/threads/{thread...}",
	"GET /api/projects/{p}/room/messages",
	"POST /api/projects/{p}/room/messages",
	"POST /api/projects/{p}/room/choices/{key}/respond",
	"GET /api/prefs",
	"PUT /api/prefs",
	"GET /api/storage/status",
	"PUT /api/storage/config",
	"GET /api/projects/{p}/factory-line",
	"GET /api/projects/{p}/skills-registry",
	"GET /api/projects/{p}/trajectories",
	"POST /api/projects/{p}/trajectories/{run}/offload",
	"GET /api/projects/{p}/trajectories/{run}/blob",
	"GET /api/projects/{p}/dreamstore",
	"POST /api/projects/{p}/dreamstore",
	"POST /api/projects/{p}/dreamstore/{n}/outcome",
	"POST /api/projects/{p}/dreamstore/drift",
	"GET /api/projects/{p}/dream/proposals",
	"POST /api/projects/{p}/dream/run",
	"POST /api/projects/{p}/dream/proposals/{key}/decision",
	"POST /api/projects/{p}/skills-registry/vet",
	"GET /api/knowledge",
	"POST /api/knowledge/promote",
	"POST /api/projects/{p}/knowledge/{key}/act",
	"GET /api/runs",
	"GET /api/runs/options",
	"GET /api/runs/{id}",
	"GET /api/runs/{id}/events",
	"GET /api/runs/{id}/git",
	"GET /api/sandboxes",
	"GET /api/sessions",
	"GET /api/sessions/this",
	"GET /api/settings/machine",
	"GET /api/settings/machine/history",
	"GET /api/settings/schema",
	"GET /api/setup",
	"GET /api/setup/mail",
	"GET /api/whoami",
	"PATCH /api/projects/{p}/memory/{n}",
	"PATCH /api/projects/{p}/tasks/{t}",
	"POST /api/clis/{cli}/check",
	"POST /api/crew/comparisons",
	"POST /api/crew/improvements/{n}",
	"POST /api/crew/improver/run",
	"POST /api/crew/mcp/tokens/{id}/revoke",
	"POST /api/crew/model-choices/{id}/dismiss",
	"POST /api/crew/model-choices/{id}/undo",
	"POST /api/crew/model-choices/{id}/use",
	"POST /api/crew/models",
	"POST /api/crew/models/refresh",
	"POST /api/crew/pause",
	"POST /api/crew/pins/look",
	"POST /api/crew/project",
	"POST /api/crew/stop",
	"POST /api/crew/updater/stop",
	"POST /api/crew/versions/build",
	"POST /api/crew/versions/{v}/gate",
	"POST /api/crew/versions/{v}/hold",
	"POST /api/drafts",
	"POST /api/drafts/{d}/messages",
	"POST /api/machine/boot/probe",
	"POST /api/machine/db-writable/read",
	"POST /api/notifications/test",
	"POST /api/projects",
	"POST /api/projects/{p}/amendments",
	"POST /api/projects/{p}/archive",
	"POST /api/projects/{p}/bugs",
	"POST /api/projects/{p}/bugs/{n}/close",
	"POST /api/projects/{p}/comments",
	"POST /api/projects/{p}/decisions/{id}/change",
	"POST /api/projects/{p}/evals/cases",
	"POST /api/projects/{p}/evals/cases/{n}/accept",
	"POST /api/projects/{p}/evals/cases/{n}/retire",
	"POST /api/projects/{p}/evals/run",
	"POST /api/projects/{p}/feedback",
	"POST /api/projects/{p}/feedback/{n}/resolve",
	"POST /api/projects/{p}/memory",
	"POST /api/projects/{p}/ok",
	"POST /api/projects/{p}/overrides",
	"POST /api/projects/{p}/overrides/{n}",
	"POST /api/projects/{p}/pause",
	"POST /api/projects/{p}/remote/mains/{id}/leave",
	"POST /api/projects/{p}/resume",
	"POST /api/projects/{p}/settings/putback",
	"POST /api/projects/{p}/tasks",
	"POST /api/projects/{p}/tasks/{t}/code",
	"POST /api/projects/{p}/tasks/{t}/decision",
	"POST /api/projects/{p}/tasks/{t}/discard",
	"POST /api/projects/{p}/tasks/{t}/may-change",
	"POST /api/projects/{p}/tasks/{t}/pull/keep",
	"POST /api/projects/{p}/tasks/{t}/revert",
	"POST /api/projects/{p}/terminal/mains/{id}/refuse",
	"POST /api/rewrites",
	"POST /api/rewrites/{id}/choice",
	"POST /api/runs",
	"POST /api/runs/{id}/finish",
	"POST /api/runs/{id}/stop",
	"POST /api/handovers",
	"POST /api/handovers/{nonce}",
	"POST /api/inbox/decision",
	"POST /api/settings/machine/putback",
	"POST /api/setup/close",
	"POST /api/subtasks/{id}/continue",
	"PUT /api/capacity/active-tickets",
	"PUT /api/capacity/lead-slots",
	"PUT /api/crew/agents/{role}",
	"PUT /api/crew/agents/{role}/choice",
	"PUT /api/projects/{p}/docs/page",
	"PUT /api/projects/{p}/evals/cases/{n}",
	"PUT /api/projects/{p}/network",
	"PUT /api/projects/{p}/protected",
	"PUT /api/projects/{p}/settings",
	"PUT /api/projects/{p}/stages",
	"PUT /api/projects/{p}/stages/{id}",
	"PUT /api/settings/machine",
}

// freshKeys are the machine's settings a change of which asks for a fresh sign-in (20.3, plan M17
// D15): how releases install, the mail address, LAN mode and what a sandbox reaches, the sessions
// themselves, edit mode, and hosting's access; and where the mail account's password, the chat's
// notices, and a copy of every backup go, whether new repositories are public, and whether an
// improvement waits for you (security review of M17 P4). A PUT and a Put back that change one of them are
// refused without it; the other keys of the same request are not stored either.
var freshKeys = []string{
	"updates.auto_install", "updates.install_after_hours", "updates.install_test", "updates.source_repo", "updates.publish",
	"notifications.mail_to",
	"network.mode", "network.model_only_hosts", "sandbox.sources",
	"sessions.days", "sessions.fresh_minutes",
	"terminal.edit_mode",
	"hosting.default_access",
	"notifications.mail_smtp", "notifications.chat_webhook", "updates.backup_copy_to", "github.visibility", "improvements.approval",
	// Rule 15's check on another family (security review of M17's Close).
	"agents.checks_use_another_family",
	// What mail is sent: either turns it off, and what needs you meanwhile is not mailed later
	// (security review of M18, S2).
	"notifications.needs_you", "notifications.news",
}

// routes registers the API's routes beside the mux and records each pattern, so a test can hold
// every pattern against freshRoutes and plainRoutes. A pattern not in plainRoutes asks for a fresh
// sign-in.
type routes struct {
	s        *Server
	mux      *http.ServeMux
	patterns []string
}

func (rt *routes) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	rt.patterns = append(rt.patterns, pattern)
	if slices.Contains(plainRoutes, pattern) {
		rt.mux.HandleFunc(pattern, h)
		return
	}
	rt.mux.Handle(pattern, rt.s.fresh(http.HandlerFunc(h)))
}

// session is what the server knows of a request's sign-in.
type session struct {
	id           int64     // the device's row; 0 when the request names no device, as crew admin's
	authAt       time.Time // when it signed in; zero when the ingress passed no time
	fresh        bool
	freshMinutes int
}

type sessionKey struct{}

func sessionOf(r *http.Request) session {
	se, _ := r.Context().Value(sessionKey{}).(session)
	return se
}

// checkSession is the session of every request of the operator (20.3): a sign-in older than
// sessions.days is over, and a device signed out is refused, on every request. A request without
// the time of its sign-in passes, not fresh: the ingress passes the time from every token the portal
// writes, so only an ingress without it, during an update, or `crew admin` sends none, and refusing
// them would lock the operator out where nothing is gained. Nothing is checked before the database
// answers, and nothing is fresh then.
func (s *Server) checkSession(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	var se session
	if raw := r.Header.Get(HeaderAuthTime); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			s.signIn(w, ReasonOver, "the time of this sign-in cannot be read: sign in again", nil)
			return nil, false
		}
		se.authAt = time.Unix(n, 0)
	}
	jti := r.Header.Get(HeaderSession)
	if len(jti) > maxSessionID {
		s.signIn(w, ReasonOver, "this sign-in names no device Crew can keep: sign in again", nil)
		return nil, false
	}
	pool := s.Pool.Load()
	if pool == nil {
		return r.WithContext(context.WithValue(r.Context(), sessionKey{}, se)), true
	}
	ctx := r.Context()
	days, freshMinutes, err := settings.Sessions(ctx, pool)
	if err != nil {
		s.Log.Error("reading the sessions' settings", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sign-in cannot be checked: the settings cannot be read")
		return nil, false
	}
	se.freshMinutes = freshMinutes
	if !se.authAt.IsZero() {
		age := time.Since(se.authAt)
		if age > time.Duration(days)*24*time.Hour {
			s.signIn(w, ReasonOver, "this sign-in is older than "+strconv.Itoa(days)+" days: sign in again", nil)
			return nil, false
		}
		se.fresh = age <= time.Duration(freshMinutes)*time.Minute && age >= -authTimeFromSkew
		if jti != "" {
			id, out, err := s.sessionStore().Seen(ctx, pool, jti, r.Header.Get(HeaderEmail), se.authAt, r.UserAgent())
			if err != nil {
				s.Log.Error("reading the device of a request", "error", err)
				writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sign-in cannot be checked: the database does not answer")
				return nil, false
			}
			if out {
				s.signIn(w, ReasonSignedOut, "This device was signed out", nil)
				return nil, false
			}
			se.id = id
		}
	} else if jti != "" {
		// A device id without a time names no device to keep, but one signed out is still refused
		// (security review of M17 P4).
		out, err := sessions.SignedOut(ctx, pool, jti)
		if err != nil {
			s.Log.Error("reading the device of a request", "error", err)
			writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the sign-in cannot be checked: the database does not answer")
			return nil, false
		}
		if out {
			s.signIn(w, ReasonSignedOut, "This device was signed out", nil)
			return nil, false
		}
	}
	return r.WithContext(context.WithValue(ctx, sessionKey{}, se)), true
}

func (s *Server) sessionChecked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r, ok := s.checkSession(w, r); ok {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) sessionStore() *sessions.Store {
	s.sessOnce.Do(func() { s.sess = &sessions.Store{ReadOnly: s.ReadOnly} })
	return s.sess
}

// fresh lets a request through only with a fresh sign-in.
func (s *Server) fresh(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sessionOf(r).fresh {
			s.signIn(w, ReasonFresh, "this needs a fresh sign-in", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// signIn answers 401 with the sign-in that would let the request through: the ingress's Google
// sign-in, asking which account (D12), or for a device signed out its logout first, which makes a
// new device. The page adds where to come back to (redirect_url).
func (s *Server) signIn(w http.ResponseWriter, reason, message string, keys []string) {
	detail := map[string]any{"reason": reason, "signIn": s.signInLink(reason)}
	if len(keys) > 0 {
		detail["keys"] = keys
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{
		"code": http.StatusUnauthorized, "status": "UNAUTHENTICATED", "message": message, "details": []any{detail}}})
}

func (s *Server) signInLink(reason string) string {
	auth := "https://auth." + s.Hostname
	if reason == ReasonSignedOut {
		return auth + "/oauth2/google/logout?redirect_uri=" + url.QueryEscape("https://"+s.Hostname+"/")
	}
	return auth + "/oauth2/google/?prompt=select_account"
}

// staleError is a change of a fresh key without a fresh sign-in.
type staleError struct{ keys []string }

func (e *staleError) Error() string {
	return "a change of " + strings.Join(e.keys, ", ") + " needs a fresh sign-in"
}

// writeStale answers a staleError, and says whether err was one.
func (s *Server) writeStale(w http.ResponseWriter, err error) bool {
	var st *staleError
	if !errors.As(err, &st) {
		return false
	}
	s.signIn(w, ReasonFresh, st.Error(), st.keys)
	return true
}

// machineGuard refuses, without a fresh sign-in, a change of the machine's settings at a fresh key
// (D15). Crew's own changes of a setting pass no guard.
func machineGuard(r *http.Request) settings.Guard {
	if sessionOf(r).fresh {
		return nil
	}
	return func(before, after settings.Doc) error {
		var changed []string
		for _, k := range freshKeys {
			b, _ := valueAt(before, k)
			a, _ := valueAt(after, k)
			if !reflect.DeepEqual(b, a) {
				changed = append(changed, k)
			}
		}
		if len(changed) > 0 {
			return &staleError{keys: changed}
		}
		return nil
	}
}

// projectGuard refuses, without a fresh sign-in, a change of a project's settings by which its runs'
// or its checks' network mode becomes or leaves LAN, as it applies with the machine's mode, or by
// which more people may reach its hosting: listed addresses, any Google account, or everyone (D15);
// and one by which its checks may run on the builder's family (rule 15; security review of M17's
// Close).
func (s *Server) projectGuard(ctx context.Context, r *http.Request, q settings.RowQuerier) (settings.Guard, error) {
	if sessionOf(r).fresh {
		return nil, nil
	}
	v, err := settings.MachineValue(ctx, q, "network.mode")
	if err != nil {
		return nil, err
	}
	machine, _ := v.(string)
	return func(before, after settings.Doc) error {
		var changed []string
		for _, k := range []string{"sandbox.network", "sandbox.check_network"} {
			if lan(before, k, machine) != lan(after, k, machine) {
				changed = append(changed, k)
			}
		}
		b, _ := valueAt(before, "hosting.access")
		a, _ := valueAt(after, "hosting.access")
		if reach(a) > reach(b) {
			changed = append(changed, "hosting.access")
		}
		// Listed, an address added is one more who may reach it (security review of M17 P4, round 2).
		if a == "listed" {
			had, _ := valueAt(before, "hosting.allowed")
			has, _ := valueAt(after, "hosting.allowed")
			if added(had, has) {
				changed = append(changed, "hosting.allowed")
			}
		}
		// The internet from inside a hosted project, turned on, widens what it reaches (plan M19 D12).
		off, _ := valueAt(before, "hosting.internet")
		on, _ := valueAt(after, "hosting.internet")
		if on == true && off == false {
			changed = append(changed, "hosting.internet")
		}
		was, _ := valueAt(before, "agents.checks_use_another_family")
		now, _ := valueAt(after, "agents.checks_use_another_family")
		if now == false && was != false {
			changed = append(changed, "agents.checks_use_another_family")
		}
		if len(changed) > 0 {
			return &staleError{keys: changed}
		}
		return nil
	}, nil
}

// ticketLAN says whether setting a ticket's own network mode to mode makes its runs' mode, as it
// applies, become or leave LAN, which asks for a fresh sign-in as the project's does (D15).
func (s *Server) ticketLAN(r *http.Request, b *board.Board, p *board.Project, mode string) (bool, error) {
	t, err := b.Ticket(r.Context(), p, r.PathValue("t"))
	if err != nil {
		return false, err
	}
	before, err := settings.NetworkMode(r.Context(), b.Pool, p.ID, t.ID)
	if err != nil {
		return false, err
	}
	after := mode
	if mode == "inherit" {
		if after, err = settings.NetworkMode(r.Context(), b.Pool, p.ID, 0); err != nil {
			return false, err
		}
	}
	return (before == "lan") != (after == "lan"), nil
}

// reach orders hosting's access by who may reach a hosted project: everyone is public, and any Google
// account nearly so (review of M17 P4, finding 3).
func reach(v any) int {
	s, _ := v.(string)
	return map[string]int{"private": 0, "listed": 1, "google": 2, "public": 3}[s]
}

// added says whether list has an entry was has not.
func added(was, list any) bool {
	old, _ := was.([]any)
	now, _ := list.([]any)
	for _, v := range now {
		if !slices.Contains(old, v) {
			return true
		}
	}
	return false
}

// lan says whether the mode at key applies as LAN: inherit is the project's own mode for its checks,
// and the machine's for its runs.
func lan(d settings.Doc, key, machine string) bool {
	mode := func(k string) string {
		v, _ := valueAt(d, k)
		m, _ := v.(string)
		return m
	}
	runs := mode("sandbox.network")
	if runs == "" || runs == "inherit" {
		runs = machine
	}
	if key == "sandbox.network" {
		return runs == "lan"
	}
	check := mode("sandbox.check_network")
	if check == "" || check == "inherit" {
		check = runs
	}
	return check == "lan"
}

// valueAt is the value of d at a dotted path.
func valueAt(d settings.Doc, path string) (any, bool) {
	cur := any(d)
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(settings.Doc)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// The devices on Machine settings (20.3), and the handover of a fresh sign-in (D13).
func (s *Server) sessionRoutes(api *routes) {
	api.HandleFunc("GET /api/sessions", s.listSessions)
	api.HandleFunc("GET /api/sessions/this", s.thisSession)
	api.HandleFunc("POST /api/sessions/{id}/signout", s.signOut)
	api.HandleFunc("POST /api/handovers", s.handover)
	api.HandleFunc("POST /api/handovers/{nonce}", s.takeOver)
}

// sessionView is what the page knows of its own sign-in: whether it is fresh and until when, so a
// screen offers a fresh sign-in before a change that asks for one.
type sessionView struct {
	ID           int64      `json:"id,omitempty"`
	AuthAt       *time.Time `json:"authAt,omitempty"`
	Fresh        bool       `json:"fresh"`
	FreshUntil   *time.Time `json:"freshUntil,omitempty"`
	FreshMinutes int        `json:"freshMinutes"`
	// SignIn is where a fresh sign-in starts, for a page that offers one before it is refused, such
	// as before a terminal's connection.
	SignIn string `json:"signIn"`
}

func (s *Server) viewOf(se session) sessionView {
	v := sessionView{ID: se.id, Fresh: se.fresh, FreshMinutes: se.freshMinutes, SignIn: s.signInLink(ReasonFresh)}
	if !se.authAt.IsZero() {
		at := se.authAt
		v.AuthAt = &at
		if se.fresh {
			until := at.Add(time.Duration(se.freshMinutes) * time.Minute)
			v.FreshUntil = &until
		}
	}
	return v
}

func (s *Server) thisSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.viewOf(sessionOf(r)))
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	days, _, err := settings.Sessions(r.Context(), pool)
	if err != nil {
		s.boardError(w, "reading the sessions' settings", err)
		return
	}
	se := sessionOf(r)
	list, err := sessions.List(r.Context(), pool, days, se.id)
	if err != nil {
		s.boardError(w, "reading the devices", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list, "this": s.viewOf(se)})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", sessions.ErrNoDevice.Error())
		return
	}
	err = pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
		return sessions.SignOut(r.Context(), tx, id, r.Header.Get(HeaderEmail))
	})
	if errors.Is(err, sessions.ErrNoDevice) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if err != nil {
		s.boardError(w, "signing the device out", err)
		return
	}
	s.Log.Info("the operator signed a device out", "device", id)
	s.listSessions(w, r)
}

// handover is a nonce the page puts in the address the fresh sign-in comes back to.
func (s *Server) handover(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	se := sessionOf(r)
	if se.id == 0 {
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", "this request names no device to hand over")
		return
	}
	nonce, err := sessions.Handover(r.Context(), pool, se.id)
	if err != nil {
		s.boardError(w, "making the handover", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"nonce": nonce})
}

// takeOver hands the request's sign-in over to the device that made the nonce. A handover that does
// not check refuses nothing: the sign-in stays a device of its own (D13).
func (s *Server) takeOver(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	se := sessionOf(r)
	jti := r.Header.Get(HeaderSession)
	moved := false
	if jti != "" && !se.authAt.IsZero() {
		var err error
		moved, err = sessions.TakeOver(r.Context(), pool, r.PathValue("nonce"), jti, r.Header.Get(HeaderEmail), se.authAt)
		if err != nil {
			s.boardError(w, "handing the sign-in over", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"moved": moved})
}
