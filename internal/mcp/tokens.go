package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tokens are the operator's personal tokens for their own tools (11.7, R156, R168): made in Crew while
// signed in and shown once, kept only as a hash, naming the projects they may use and whether they may
// answer. A token runs out unless it is renewed, and can be revoked at any time.
type Tokens struct {
	Pool *pgxpool.Pool
	// Days is how long a token lasts, and PerMinute how many calls it makes in a minute: the machine's
	// mcp_tokens settings.
	Days      func(ctx context.Context) int
	PerMinute func(ctx context.Context) int
	Now       func() time.Time

	// seen are the requests of each token in the minute that runs: every request a token makes counts,
	// under the lock, so calls at once cannot pass the limit together.
	mu   sync.Mutex
	seen map[int64][]time.Time
}

// The rules of 11.7 that are not settings.
const (
	tokenPrefix = "crew_pat_"
	// FailuresToBlock wrong tokens from one address in BlockFor block that address for BlockFor.
	FailuresToBlock = 5
	BlockFor        = time.Hour
)

// ErrTooMany is the answer to an address that sent too many wrong tokens, or a token past its calls.
var ErrTooMany = errors.New("mcp: too many requests")

// PersonalToken is a token as the operator's list shows it; never the token itself.
type PersonalToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Projects   []string   `json:"projects"` // the projects' names
	MayAnswer  bool       `json:"mayAnswer"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	LastAddr   *string    `json:"lastAddr,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Calls      int        `json:"calls"`
}

func (t *Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tokens) days(ctx context.Context) int {
	if t.Days != nil {
		if d := t.Days(ctx); d > 0 {
			return d
		}
	}
	return 90
}

func hashOf(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Create makes a token for the projects named, and returns it once.
func (t *Tokens) Create(ctx context.Context, name string, projects []string, mayAnswer bool) (string, PersonalToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsAny(name, "\r\n") {
		return "", PersonalToken{}, Errf("name: one line of at most 60 characters")
	}
	if len(projects) == 0 || len(projects) > 100 {
		return "", PersonalToken{}, Errf("projects: name the projects the token may use")
	}
	var ids []string
	for _, p := range projects {
		var id string
		// A hidden project, the gate's (16.2), is named by no token.
		err := t.Pool.QueryRow(ctx, `SELECT id::text FROM projects WHERE name = $1 AND state <> 'archived' AND NOT hidden`, p).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", PersonalToken{}, Errf("projects: there is no project %q", p)
		}
		if err != nil {
			return "", PersonalToken{}, err
		}
		ids = append(ids, id)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", PersonalToken{}, err
	}
	plain := tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	// The token and its row in the history are made together: no live token that nobody holds.
	var pt PersonalToken
	err := pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO personal_tokens (name, hash, projects, may_answer, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, name, hashOf(plain), ids, mayAnswer, t.now(),
			t.now().Add(time.Duration(t.days(ctx))*24*time.Hour)).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "personal_tokens_live_name") {
				return Errf("name: a token of that name is in use")
			}
			return err
		}
		if pt, err = t.get(ctx, tx, id); err != nil {
			return err
		}
		return t.record(ctx, tx, pt, nil, map[string]any{"made": true, "projects": pt.Projects, "mayAnswer": pt.MayAnswer, "expiresAt": pt.ExpiresAt})
	})
	if err != nil {
		return "", PersonalToken{}, err
	}
	return plain, pt, nil
}

// querier is a pool or a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// record writes a token's change into the history (6.4): what was made, switched, renewed, or revoked,
// and when, never the token itself.
func (t *Tokens) record(ctx context.Context, q querier, pt PersonalToken, before, after map[string]any) error {
	var b any
	if before != nil {
		b = before
	}
	_, err := q.Exec(ctx, `INSERT INTO changes (who, tbl, row_key, before, after) VALUES ('operator', 'personal_tokens', $1, $2, $3)`,
		pt.Name, b, after)
	return err
}

const tokenCols = `t.id, t.name, coalesce(ARRAY(SELECT p.name FROM projects p WHERE p.id = ANY(t.projects) ORDER BY p.name), '{}'),
	t.may_answer, t.created_at, t.expires_at, t.last_used_at, t.last_addr, t.revoked_at,
	(SELECT count(*) FROM mcp_calls c WHERE c.token = t.id)`

func scanToken(row pgx.Row) (PersonalToken, error) {
	var pt PersonalToken
	err := row.Scan(&pt.ID, &pt.Name, &pt.Projects, &pt.MayAnswer, &pt.CreatedAt, &pt.ExpiresAt, &pt.LastUsedAt, &pt.LastAddr, &pt.RevokedAt, &pt.Calls)
	return pt, err
}

func (t *Tokens) get(ctx context.Context, q querier, id int64) (PersonalToken, error) {
	pt, err := scanToken(q.QueryRow(ctx, `SELECT `+tokenCols+` FROM personal_tokens t WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return pt, Errf("there is no token %d", id)
	}
	return pt, err
}

// List gives every token, the newest first.
func (t *Tokens) List(ctx context.Context) ([]PersonalToken, error) {
	rows, err := t.Pool.Query(ctx, `SELECT `+tokenCols+` FROM personal_tokens t ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PersonalToken, error) { return scanToken(r) })
}

// change changes a live token, and records what changed.
func (t *Tokens) change(ctx context.Context, id int64, what string, sql string, args ...any) (PersonalToken, error) {
	// The change and its row in the history are made together, so a change is never made unrecorded.
	var pt PersonalToken
	err := pgx.BeginFunc(ctx, t.Pool, func(tx pgx.Tx) error {
		before, err := t.get(ctx, tx, id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE personal_tokens SET `+sql+` WHERE id = $1 AND revoked_at IS NULL`, append([]any{id}, args...)...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return Errf("there is no live token %d", id)
		}
		if pt, err = t.get(ctx, tx, id); err != nil {
			return err
		}
		return t.record(ctx, tx, pt, map[string]any{"mayAnswer": before.MayAnswer, "expiresAt": before.ExpiresAt},
			map[string]any{what: true, "mayAnswer": pt.MayAnswer, "expiresAt": pt.ExpiresAt, "revokedAt": pt.RevokedAt})
	})
	if err != nil {
		return PersonalToken{}, err
	}
	return pt, nil
}

// Revoke ends a token at once.
func (t *Tokens) Revoke(ctx context.Context, id int64) (PersonalToken, error) {
	return t.change(ctx, id, "revoked", `revoked_at = $2`, t.now())
}

// Renew gives a token its days again, from now.
func (t *Tokens) Renew(ctx context.Context, id int64) (PersonalToken, error) {
	return t.change(ctx, id, "renewed", `expires_at = $2`, t.now().Add(time.Duration(t.days(ctx))*24*time.Hour))
}

// SetAnswer switches "may answer" (11.7): with it, the token may approve or discard a new feature and
// change a recorded decision. It is off for a new token unless asked.
func (t *Tokens) SetAnswer(ctx context.Context, id int64, on bool) (PersonalToken, error) {
	return t.change(ctx, id, "mayAnswerSwitched", `may_answer = $2`, on)
}

// clientAddr is the address a request came from: the one the ingress gives, else the connection's.
// Only the ingress reaches the operator's door (deploy/compose.yaml: the server's door is on the
// ingress's network alone, and a sandbox's request there is refused), and the ingress sets
// X-Forwarded-For itself, so the address is the client's.
func clientAddr(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		first, _, _ := strings.Cut(f, ",")
		if a := strings.TrimSpace(first); a != "" {
			return a
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Resolve says who a request with a personal token is (11.7): a live token that has not run out, from
// an address that is not blocked, within its calls of a minute. A wrong token counts against its
// address. A run's token is never a personal token: it is not in this table.
func (t *Tokens) Resolve(r *http.Request) (Caller, error) {
	ctx := r.Context()
	addr := clientAddr(r)
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	var c Caller
	err := t.Pool.QueryRow(ctx, `SELECT id, name, coalesce(ARRAY(SELECT x::text FROM unnest(projects) x), '{}'), may_answer FROM personal_tokens
		WHERE hash = $1 AND revoked_at IS NULL AND expires_at > $2`, hashOf(token), t.now()).Scan(&c.Token, &c.TokenName, &c.Projects, &c.MayAnswer)
	if !ok || !strings.HasPrefix(token, tokenPrefix) || err != nil {
		// The block stops guessing: five wrong tokens from an address in an hour, and that address's
		// wrong tokens are refused without a row more. A token that is right is not guessed, so it
		// works from a blocked address too (security review of M9).
		var failures int
		if err := t.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_token_failures WHERE addr = $1 AND at > $2`, addr, t.now().Add(-BlockFor)).Scan(&failures); err != nil {
			return Caller{}, err
		}
		if failures >= FailuresToBlock {
			return Caller{}, ErrTooMany
		}
		bg := context.WithoutCancel(ctx)
		if _, ferr := t.Pool.Exec(bg, `INSERT INTO mcp_token_failures (addr, at) VALUES ($1, $2)`, addr, t.now()); ferr != nil {
			return Caller{}, ferr
		}
		if _, ferr := t.Pool.Exec(bg, `DELETE FROM mcp_token_failures WHERE at < $1`, t.now().Add(-BlockFor)); ferr != nil {
			return Caller{}, ferr
		}
		return Caller{}, ErrNoToken
	}
	per := 60
	if t.PerMinute != nil {
		if n := t.PerMinute(ctx); n > 0 {
			per = n
		}
	}
	if !t.take(c.Token, per) {
		return Caller{}, ErrTooMany
	}
	if _, err := t.Pool.Exec(ctx, `UPDATE personal_tokens SET last_used_at = $2, last_addr = $3 WHERE id = $1`, c.Token, t.now(), addr); err != nil {
		return Caller{}, err
	}
	c.Role = "token"
	return c, nil
}

// take counts one request of a token, and says whether it is within the token's requests of a minute.
func (t *Tokens) take(id int64, per int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[int64][]time.Time{}
	}
	now := t.now()
	kept := t.seen[id][:0]
	for _, at := range t.seen[id] {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	if len(kept) >= per {
		t.seen[id] = kept
		return false
	}
	t.seen[id] = append(kept, now)
	return true
}
