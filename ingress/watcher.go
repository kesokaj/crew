package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// reloadRetryDelay paces retries after a failed config load. The outage
	// that motivated it (2026-09-01): at host boot caddy started before the
	// network was up, caddy-security could not fetch Google's OIDC metadata,
	// the initial /load 400ed — and with no retry the ingress sat on the
	// minimal admin-only config, site down, until the next container event
	// (which a quiet host never sends).
	reloadRetryDelay = PollInterval
	// adminDownLimit is how many CONSECUTIVE transport-level failures reaching
	// the admin API the watcher tolerates before declaring the process
	// unrecoverable. The admin listener lives in this same process: if it stops
	// answering, no future reload can ever land, so the only self-heal is a
	// process exit and a restart by Docker (fresh minimal admin + initial scan).
	adminDownLimit = 4
)

// errAdminUnreachable marks a reload failure at the transport layer — the
// admin API did not answer at all (vs. answering with a rejection).
var errAdminUnreachable = errors.New("caddy admin API unreachable")

// Routes returns the sites the ingress serves. In Crew they come from the
// ingress's own environment (ServerRoutes), never from Docker: the ingress
// holds no Docker socket (design 19.4).
type Routes func(context.Context) ([]App, error)

// Watcher generates the Caddyfile from its routes and loads it into Caddy,
// retrying a load that fails until it lands.
type Watcher struct {
	routes    Routes
	cfg       GeneratorConfig
	log       *slog.Logger
	adminAddr string // Caddy admin API address (e.g., "localhost:2019")

	// scanMu serializes scanAndReload: the initial scan and the failed-reload
	// retry timer call it from their own goroutines.
	// adminDown is guarded by it.
	scanMu    sync.Mutex
	adminDown int

	caddyfileMu   sync.Mutex // guards lastCaddyfile
	lastCaddyfile string

	retryMu    sync.Mutex // guards retryTimer
	retryTimer *time.Timer
	retryDelay time.Duration // reloadRetryDelay unless a test shortens it
	// tick is how often the routes are asked again: the server's hosted routes change (19.4).
	tick time.Duration

	fatal chan error // buffered(1); see Fatal
}

// NewWatcher creates a watcher that serves the given routes.
func NewWatcher(routes Routes, cfg GeneratorConfig, logger *slog.Logger, adminAddr string) *Watcher {
	return &Watcher{
		routes:     routes,
		cfg:        cfg,
		log:        logger,
		adminAddr:  adminAddr,
		retryDelay: reloadRetryDelay,
		tick:       PollInterval,
		fatal:      make(chan error, 1),
	}
}

// Fatal reports a condition the watcher cannot recover from in-process (the
// admin API gone). The caller should exit nonzero and let the container
// restart policy recover.
func (w *Watcher) Fatal() <-chan error { return w.fatal }

// Start performs the initial scan and load.
func (w *Watcher) Start(ctx context.Context) error {
	w.log.Info("performing initial scan of the routes")
	if err := w.scanAndReload(ctx); err != nil {
		// Not fatal: a failed reload has already scheduled its own retry
		// (scanAndReload), which is exactly how the boot race resolves —
		// the network comes up, a retry lands, the site serves.
		w.log.Error("initial scan failed (retry scheduled)", "error", err)
	}
	if w.tick > 0 {
		go w.poll(ctx)
	}
	return nil
}

// poll asks the routes again every tick; a Caddyfile that did not change is not loaded (19.4).
func (w *Watcher) poll(ctx context.Context) {
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.scanAndReload(ctx); err != nil && ctx.Err() == nil {
				w.log.Error("a scan of the routes failed", "error", err)
			}
		}
	}
}

// scanAndReload reads the routes, generates a Caddyfile,
// and pushes it to Caddy if it has changed.
func (w *Watcher) scanAndReload(ctx context.Context) error {
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	apps, err := w.discoverApps(ctx)
	if err != nil {
		// Retry here too: a transient failure to read the routes inside a retry
		// tick must continue the chain, not terminate it — a dead chain is the
		// wedged-at-boot outage all over again (review finding).
		w.scheduleRetry(ctx)
		return fmt.Errorf("discovering apps: %w", err)
	}

	caddyfile := GenerateCaddyfile(apps, w.cfg)

	// Only reload if config changed
	w.caddyfileMu.Lock()
	changed := caddyfile != w.lastCaddyfile
	w.caddyfileMu.Unlock()

	if !changed {
		return nil
	}

	// Every ten seconds the routes are asked: what they are is logged only when they change.
	for _, app := range apps {
		w.log.Info("route", "hostname", app.Hostname, "ip", app.ContainerIP, "port", app.Port, "mode", app.Mode, "hosted", app.Hosted)
	}
	w.log.Info("caddyfile changed, reloading caddy")
	if err := w.reloadCaddy(ctx, caddyfile); err != nil {
		// Do NOT record this as the last-applied config: a failed reload must be
		// retried on the next scan, not masked as "unchanged" — otherwise one
		// bad reload wedges the ingress on the stale live config forever (M23).
		w.noteReloadFailure(ctx, err)
		return err
	}
	w.adminDown = 0
	w.caddyfileMu.Lock()
	w.lastCaddyfile = caddyfile
	w.caddyfileMu.Unlock()
	return nil
}

// noteReloadFailure decides how a failed reload heals. A REJECTED load (the
// admin answered, e.g. 400 because an upstream fetch failed at boot) retries
// forever — caddy 2.11 keeps both the admin endpoint and the running config
// intact across a rejected load, verified live, so retrying is always safe. A
// TRANSPORT failure means the admin listener in this same process is gone;
// after adminDownLimit consecutive ones nothing in-process can ever fix it,
// and the watcher reports fatal instead. Caller holds scanMu.
func (w *Watcher) noteReloadFailure(ctx context.Context, err error) {
	if errors.Is(err, errAdminUnreachable) {
		w.adminDown++
		if w.adminDown >= adminDownLimit {
			select {
			case w.fatal <- fmt.Errorf("admin API unreachable %d consecutive times, last: %w", w.adminDown, err):
			default:
			}
			return
		}
	} else {
		w.adminDown = 0
	}
	w.scheduleRetry(ctx)
}

// scheduleRetry arms (or re-arms) a one-shot retry of the full scan+reload.
// Each failed attempt schedules the next, so a persistent failure becomes a
// steady retryDelay-paced loop rather than a wedge or a hot spin.
func (w *Watcher) scheduleRetry(ctx context.Context) {
	w.retryMu.Lock()
	defer w.retryMu.Unlock()
	if w.retryTimer != nil {
		w.retryTimer.Stop()
	}
	w.retryTimer = time.AfterFunc(w.retryDelay, func() {
		if ctx.Err() != nil {
			return
		}
		w.log.Info("retrying failed caddy reload")
		if err := w.scanAndReload(ctx); err != nil {
			w.log.Error("reload retry failed", "error", err)
		}
	})
}

// discoverApps returns the routes to serve.
func (w *Watcher) discoverApps(ctx context.Context) ([]App, error) {
	return w.routes(ctx)
}

// reloadCaddy pushes a new Caddyfile to Caddy's admin API.
func (w *Watcher) reloadCaddy(ctx context.Context, caddyfile string) error {
	url := fmt.Sprintf("http://%s/load", w.adminAddr)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(caddyfile))
	if err != nil {
		return fmt.Errorf("creating reload request: %w", err)
	}
	req.Header.Set("Content-Type", "text/caddyfile")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Transport-level: the admin listener did not answer at all. Marked so
		// noteReloadFailure can tell "rejected" from "gone" (only the latter
		// is beyond in-process recovery).
		return fmt.Errorf("%w: %v", errAdminUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("caddy reload failed with status %d", resp.StatusCode)
	}

	// A 200 is not proof of a load. When the
	// Caddyfile adapts with WARNINGS, caddy writes them to the response
	// before it loads anything (caddyconfig/load.go, handleLoad) — that sends
	// the 200 — and a load that then fails can only say so in the body, as
	// its {"error": …} object. At boot, with no network for the OIDC
	// metadata fetch, exactly that happened: this function logged success,
	// the Caddyfile was latched as applied, every later scan said
	// "unchanged", and caddy sat on its admin-only config for six hours.
	// Returned as a plain error, not errAdminUnreachable: the admin answered,
	// and the in-process retry (noteReloadFailure) is what heals it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("caddy reload: reading the admin API's response: %v", err)
	}
	if msg := loadError(body); msg != "" {
		return fmt.Errorf("caddy reported a failed load behind status 200: %s", msg)
	}

	w.log.Info("caddy reloaded successfully")
	return nil
}

// loadError returns caddy's own error message when the body of a /load
// response carries one, "" otherwise. The body is a sequence of JSON values:
// the adapter's warnings array when there were any, then — only on failure —
// caddy's APIError object, {"error": "…"}. Only that object means failure:
// an empty body, warnings alone, any other value and an undecodable tail are
// all a load caddy did not report as failed (treating them as failures would
// re-load caddy on every retry tick forever).
func loadError(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			return ""
		}
		if obj, ok := v.(map[string]any); ok {
			if msg, ok := obj["error"].(string); ok && msg != "" {
				return msg
			}
		}
	}
}
