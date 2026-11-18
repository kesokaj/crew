package sandboxes

import (
	"context"
	"crypto/subtle"
	"slices"
	"time"
)

// Authorize returns the sandbox a broker request comes from: local is the server's address it
// arrived at, remote the address it came from, token what it carried. Anything that does not match
// one live sandbox is ErrRefused.
func (m *Manager) Authorize(ctx context.Context, local, remote, token string) (Row, error) {
	if token == "" {
		return Row{}, ErrRefused
	}
	// A request that did not arrive at a sandbox's door is refused before any work in the database:
	// the broker's port is open on every network of the server. A door not known yet, of a sandbox
	// another process started, is learned first, at most once a second.
	if !m.Runtime.IsSandboxDoor(local) {
		m.adoptLive(ctx)
		if !m.Runtime.IsSandboxDoor(local) {
			return Row{}, ErrRefused
		}
	}
	row, err := m.authorize(ctx, local, remote, token)
	if err == nil {
		return row, nil
	}
	// A sandbox started by another process, or before a restart, is learned once; so is one on a
	// subnet Docker gave again after the sandbox that had it was removed elsewhere.
	m.adoptLive(ctx)
	return m.authorize(ctx, local, remote, token)
}

func (m *Manager) authorize(ctx context.Context, local, remote, token string) (Row, error) {
	id, ok := m.Runtime.SandboxAt(local, remote)
	if !ok {
		return Row{}, ErrRefused
	}
	var stored string
	err := m.Pool.QueryRow(ctx, `SELECT t.sha256 FROM sandbox_tokens t JOIN sandboxes s ON s.id = t.sandbox
		WHERE t.sandbox = $1 AND t.sha256 = $2 AND t.ends_at > now() AND s.removed_at IS NULL`, id, Hash(token)).Scan(&stored)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(Hash(token))) != 1 {
		return Row{}, ErrRefused
	}
	row, err := m.Get(ctx, id)
	if err != nil {
		return Row{}, ErrRefused
	}
	return row, nil
}

// adoptLive gives every live sandbox its door again. It runs at most once every five seconds, so a
// flood of strangers cannot turn into a flood of Docker calls, unless a sandbox started since it
// last ran: that one must be learned before its first request is answered.
func (m *Manager) adoptLive(ctx context.Context) {
	m.adoptMu.Lock()
	defer m.adoptMu.Unlock()
	// A floor that holds whoever asks: an unauthenticated caller makes at most one query a second.
	// A sandbox this server started since lifts it, so its first request is answered.
	if time.Since(m.adoptedAt) < time.Second && !m.startedAt.After(m.adoptedAt) {
		return
	}
	live, err := m.Live(ctx)
	if err != nil {
		return
	}
	newer := false
	for _, s := range live {
		newer = newer || s.Started.After(m.adoptedAt)
	}
	if !newer && time.Since(m.adoptedAt) < 5*time.Second {
		m.startedAt = time.Time{} // the start that lifted the floor is adopted already
		return
	}
	m.adoptedAt = time.Now()
	for _, s := range live {
		if err := m.Runtime.AdoptSandbox(ctx, s.ID); err != nil {
			m.Log.Debug("adopting a sandbox", "sandbox", s.ID, "error", err)
		}
	}
}

// Short-lived kinds are removed when the server starts: their work cannot go on without it. A check
// that ran is an error after a restart and runs again in a new sandbox; a build that ran is failed.
var shortLived = []string{KindSetup, KindLoginCheck, KindJudge, KindCheck, KindBuild, KindHostBuild, KindProbe}

// Reconcile is run when the server starts. A short-lived sandbox is removed. Any other live
// sandbox gets its door back, and a row whose container is gone is closed.
func (m *Manager) Reconcile(ctx context.Context) {
	live, err := m.Live(ctx)
	if err != nil {
		m.Log.Warn("reading the sandboxes", "error", err)
		return
	}
	have := map[string]bool{}
	list, err := m.Runtime.Sandboxes(ctx)
	if err != nil {
		// What Docker holds is not known, but what was short-lived is over either way.
		m.Log.Warn("listing the sandboxes in Docker", "error", err)
		for _, s := range live {
			if slices.Contains(shortLived, s.Kind) {
				if err := m.Remove(ctx, s.ID); err != nil {
					m.Log.Warn("removing a sandbox left from before", "sandbox", s.ID, "error", err)
				}
			}
		}
		return
	}
	known := map[string]bool{}
	for _, s := range live {
		known[s.ID] = true
	}
	for _, s := range list {
		have[s.ID] = true
		if !known[s.ID] {
			// Docker has a sandbox the database does not: nothing can use it. Asked again first,
			// because the server answers while this runs and may have just started it.
			if _, err := m.Get(ctx, s.ID); err == nil {
				continue
			}
			if err := m.Runtime.RemoveSandbox(ctx, s.ID); err != nil {
				m.Log.Warn("removing a sandbox with no row", "sandbox", s.ID, "error", err)
			}
		}
	}
	for _, s := range live {
		if slices.Contains(shortLived, s.Kind) || !have[s.ID] {
			if err := m.Remove(ctx, s.ID); err != nil {
				m.Log.Warn("removing a sandbox left from before", "sandbox", s.ID, "error", err)
			}
			continue
		}
		if err := m.Runtime.AdoptSandbox(ctx, s.ID); err != nil {
			m.Log.Warn("adopting a sandbox", "sandbox", s.ID, "error", err)
		}
	}
}
