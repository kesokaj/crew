// Package sessions keeps the devices signed in to Crew (design 20.3), from what the ingress passes on
// Crew's route: the sign-in's time and the device's id, which are the token's iat and jti. The jti is
// the portal's session id, which lasts while the browser runs: a fresh sign-in in the same browser
// keeps it, and one after a restart of the browser gets a new one, which a handover moves onto the
// device (plan M17 D13). A device signed out refuses every id it has, for good.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
)

// DB is what the store needs of the database: a pool, or a transaction.
type DB interface {
	db.Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

const (
	// Kept is how long a device is kept after its newest sign-in: the ingress's 30 days and one,
	// after which no token of it is taken any more. A device signed out is kept for good.
	Kept = 31 * 24 * time.Hour
	// HandoverFor is how long a handover's nonce can be used, once (D13).
	HandoverFor = 10 * time.Minute
	// LastEvery is how often a device's last time is written (D21).
	LastEvery = time.Minute
	// maxAgent is how much of the browser's User-Agent is kept, to name the device in the list.
	maxAgent = 400
)

// ErrNoDevice is a device that is not signed in: none of that id, or signed out already.
var ErrNoDevice = errors.New("no such device is signed in")

// Device is a row of the list on Machine settings.
type Device struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	FirstAt   time.Time `json:"firstAt"`
	AuthAt    time.Time `json:"authAt"`
	LastAt    time.Time `json:"lastAt"`
	UserAgent string    `json:"userAgent"`
	This      bool      `json:"this"`
}

// Store keeps when it last wrote each device's last time, so a device is written at most once a
// minute. In check mode (ReadOnly) it writes nothing at all.
type Store struct {
	ReadOnly bool
	Now      func() time.Time

	mu      sync.Mutex
	written map[int64]time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// due says whether device id's last time is to be written now, and notes that it is.
func (s *Store) due(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if t, ok := s.written[id]; ok && now.Sub(t) < LastEvery {
		return false
	}
	if s.written == nil {
		s.written = map[int64]time.Time{}
	}
	s.written[id] = now
	return true
}

// Seen is a request of the device with id jti, signed in at authAt: its row, made at its first
// request. It says whether the device was signed out. In check mode a device Crew has not seen has
// no row, and id is 0.
func (s *Store) Seen(ctx context.Context, q DB, jti, email string, authAt time.Time, agent string) (id int64, signedOut bool, err error) {
	var out *time.Time
	var stored time.Time
	err = q.QueryRow(ctx, `SELECT s.id, s.signed_out_at, s.auth_at FROM session_ids i JOIN sessions s ON s.id = i.session
		WHERE i.jti = $1`, jti).Scan(&id, &out, &stored)
	switch {
	case err == nil:
		if out != nil {
			return id, true, nil
		}
		// A sign-in newer than the row's is written at once, so the list says when the device last
		// signed in; the last time only once a minute.
		if s.ReadOnly || (!authAt.After(stored) && !s.due(id)) {
			return id, false, nil
		}
		_, err = q.Exec(ctx, `UPDATE sessions SET last_at = now(), auth_at = greatest(auth_at, $2), user_agent = $3 WHERE id = $1`,
			id, authAt, cut(agent))
		return id, false, err
	case errors.Is(err, pgx.ErrNoRows):
		if s.ReadOnly {
			return 0, false, nil
		}
		return s.add(ctx, q, jti, email, authAt, agent)
	default:
		return 0, false, err
	}
}

// SignedOut says whether the device with id jti was signed out.
func SignedOut(ctx context.Context, q db.Querier, jti string) (bool, error) {
	var out bool
	err := q.QueryRow(ctx, `SELECT s.signed_out_at IS NOT NULL FROM session_ids i JOIN sessions s ON s.id = i.session WHERE i.jti = $1`, jti).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return out, err
}

// add makes the row of a device Crew has not seen, after Prune.
func (s *Store) add(ctx context.Context, q DB, jti, email string, authAt time.Time, agent string) (int64, bool, error) {
	tx, err := q.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := Prune(ctx, tx); err != nil {
		return 0, false, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO sessions (email, auth_at, user_agent) VALUES ($1, $2, $3) RETURNING id`,
		email, authAt, cut(agent)).Scan(&id); err != nil {
		return 0, false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO session_ids (jti, session) VALUES ($1, $2) ON CONFLICT (jti) DO NOTHING`, jti, id)
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() == 0 {
		// Another request of the same device made its row first: that one is the device.
		_ = tx.Rollback(ctx)
		var out *time.Time
		if err := q.QueryRow(ctx, `SELECT s.id, s.signed_out_at FROM session_ids i JOIN sessions s ON s.id = i.session
			WHERE i.jti = $1`, jti).Scan(&id, &out); err != nil {
			return 0, false, err
		}
		return id, out != nil, nil
	}
	s.due(id)
	return id, false, tx.Commit(ctx)
}

// Prune takes out the devices whose newest sign-in is older than Kept, with their ids and handovers,
// and the handovers that ran out. It runs with each new device and once a day. A device signed out is
// kept for good: a browser that still holds its session id would sign in with it again (review of
// M17 P4, finding 2).
func Prune(ctx context.Context, q db.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE signed_out_at IS NULL AND auth_at < now() - make_interval(secs => $1)`, Kept.Seconds())
	if err != nil {
		return 0, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM session_handovers WHERE until < now()`); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// List is the devices signed in, newest first: not signed out, and signed in within days. this is
// the device of the request, marked.
func List(ctx context.Context, q db.Querier, days int, this int64) ([]Device, error) {
	rows, err := q.Query(ctx, `SELECT id, email, first_at, auth_at, last_at, user_agent FROM sessions
		WHERE signed_out_at IS NULL AND auth_at > now() - make_interval(days => $1)
		ORDER BY last_at DESC, id DESC LIMIT 100`, days)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Device, error) {
		var d Device
		err := r.Scan(&d.ID, &d.Email, &d.FirstAt, &d.AuthAt, &d.LastAt, &d.UserAgent)
		d.This = d.ID == this
		return d, err
	})
	if out == nil {
		out = []Device{}
	}
	return out, err
}

// SignOut signs a device out, for good: every id it has is refused from then on. The change is
// recorded as the operator's.
func SignOut(ctx context.Context, tx pgx.Tx, id int64, who string) error {
	tag, err := tx.Exec(ctx, `UPDATE sessions SET signed_out_at = now() WHERE id = $1 AND signed_out_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoDevice
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_handovers WHERE session = $1`, id); err != nil {
		return err
	}
	return db.RecordChange(ctx, tx, who, "sessions", strconv.FormatInt(id, 10), map[string]any{"signedOut": false}, map[string]any{"signedOut": true})
}

// Handover is a nonce for device id, good once for HandoverFor: the page puts it in the address the
// fresh sign-in comes back to, and hands it in with the new sign-in (TakeOver). Only its hash is kept.
func Handover(ctx context.Context, q db.Querier, id int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(b)
	if _, err := q.Exec(ctx, `INSERT INTO session_handovers (nonce_sha256, session, until) VALUES ($1, $2, now() + make_interval(secs => $3))`,
		hash(nonce), id, HandoverFor.Seconds()); err != nil {
		return "", err
	}
	return nonce, nil
}

// TakeOver hands a fresh sign-in over to the device that asked for it: the request's id jti becomes
// one of that device's, and the device's sign-in time the new one. A row the new id made in the
// meantime, at the page's first requests, goes. It says whether the handover took: a nonce used or
// run out, another email, or a device signed out is refused, and the new id stays a device of its
// own (D13). The nonce is used up either way.
func TakeOver(ctx context.Context, q DB, nonce, jti, email string, authAt time.Time) (bool, error) {
	tx, err := q.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	var good bool
	err = tx.QueryRow(ctx, `DELETE FROM session_handovers WHERE nonce_sha256 = $1 RETURNING session, until > now()`, hash(nonce)).Scan(&id, &good)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var owner string
	var out *time.Time
	if err := tx.QueryRow(ctx, `SELECT email, signed_out_at FROM sessions WHERE id = $1 FOR UPDATE`, id).Scan(&owner, &out); err != nil {
		return false, err
	}
	if !good || out != nil || !strings.EqualFold(owner, email) {
		return false, tx.Commit(ctx)
	}
	var other int64
	var otherOut *time.Time
	err = tx.QueryRow(ctx, `SELECT s.id, s.signed_out_at FROM session_ids i JOIN sessions s ON s.id = i.session WHERE i.jti = $1 FOR UPDATE OF i`,
		jti).Scan(&other, &otherOut)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `INSERT INTO session_ids (jti, session) VALUES ($1, $2)`, jti, id); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	case otherOut != nil:
		// The new id belongs to a device signed out: it stays refused.
		return false, tx.Commit(ctx)
	case other != id:
		if _, err := tx.Exec(ctx, `UPDATE session_ids SET session = $2 WHERE jti = $1`, jti, id); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM session_ids WHERE session = $1)`, other); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET auth_at = greatest(auth_at, $2), last_at = now() WHERE id = $1`, id, authAt); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func hash(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

// cut keeps at most maxAgent bytes of a User-Agent, on a rune's edge.
func cut(s string) string {
	if len(s) <= maxAgent {
		return s
	}
	s = s[:maxAgent]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
