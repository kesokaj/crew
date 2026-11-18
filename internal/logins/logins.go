package logins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// When the logins are checked (12.6). How often each CLI's check makes its real request, and how
// long a login lasts, are its adapter's.
const (
	Every = 30 * time.Minute // the scheduled check of each CLI
	// RefusedInARow is how many refused checks in a row mark a CLI as signed out.
	RefusedInARow = 2
)

// The states of a CLI's login, as cli_logins keeps them.
const (
	StateUnknown   = "unknown"
	StateSignedIn  = "signed_in"
	StateSignedOut = "signed_out"
)

// Execer runs a command in a sandbox: the orchestrator.
type Execer interface {
	Exec(ctx context.Context, containerID string, spec orchestrator.ExecSpec) (int, string, error)
}

// Vault is the part of the credential store the check uses.
type Vault interface {
	Get(ctx context.Context, id string) (credstore.Credential, error)
	Put(ctx context.Context, c credstore.Credential, who string) error
}

// Service checks the logins and keeps their state.
type Service struct {
	Pool      *pgxpool.Pool
	Vault     Vault
	Sandboxes *sandboxes.Manager
	Exec      Execer
	Log       *slog.Logger
	Now       func() time.Time

	locks sync.Map // CLI → *sync.Mutex: one check of a CLI at a time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// State is what the Agent CLIs screen shows of one CLI.
type State struct {
	CLI       string     `json:"cli"`
	State     string     `json:"state"`
	Failures  int        `json:"failures"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	Result    string     `json:"result"`
	RealAt    *time.Time `json:"realAt,omitempty"`
	Version   string     `json:"version,omitempty"`
	Checking  bool       `json:"checking"`
	// SetUp says whether Setup holds a login of this CLI.
	SetUp    bool       `json:"setUp"`
	Account  string     `json:"account,omitempty"`
	SignedAt *time.Time `json:"signedAt,omitempty"`
}

// States reads the state of every CLI.
func (s *Service) States(ctx context.Context) ([]State, error) {
	var out []State
	for _, name := range cli.Names() {
		st := State{CLI: name, State: StateUnknown}
		var checked, real *time.Time
		var version *string
		err := s.Pool.QueryRow(ctx, `SELECT state, failures, checked_at, result, real_at, version FROM cli_logins WHERE cli = $1`, name).
			Scan(&st.State, &st.Failures, &checked, &st.Result, &real, &version)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		st.CheckedAt, st.RealAt = checked, real
		if version != nil {
			st.Version = *version
		}
		if m, ok := s.locks.Load(name); ok && !m.(*sync.Mutex).TryLock() {
			st.Checking = true
		} else if ok {
			m.(*sync.Mutex).Unlock()
		}
		c, err := s.Vault.Get(ctx, name)
		switch {
		case err == nil:
			st.SetUp, st.Account = true, c.Account
			signed := c.UpdatedAt
			st.SignedAt = &signed
		case !errors.Is(err, credstore.ErrNotFound):
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// SignedIn lists the CLIs whose login works: for the project terminal (12.6) and, from M3, for
// which CLI may run.
func (s *Service) SignedIn(ctx context.Context) []string {
	states, err := s.States(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, st := range states {
		if st.SetUp && st.State == StateSignedIn {
			out = append(out, st.CLI)
		}
	}
	return out
}

func (s *Service) lock(name string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Check checks one CLI now and records what it found. A refused check is tried once more at once,
// so two refused checks in a row mark the CLI as signed out (12.6). real makes the real request.
func (s *Service) Check(ctx context.Context, name string, real bool) (cli.LoginResult, error) {
	a, ok := cli.Get(name)
	if !ok {
		return cli.LoginResult{}, fmt.Errorf("no CLI %q", name)
	}
	m := s.lock(name)
	m.Lock()
	defer m.Unlock()
	return s.checkLocked(ctx, a, real)
}

// CheckNow starts a check of one CLI in the background, unless one of that CLI runs already: a
// click on "Check now" never queues another real request on the subscription. It reports whether
// it started one.
func (s *Service) CheckNow(ctx context.Context, name string, real bool) bool {
	a, ok := cli.Get(name)
	if !ok {
		return false
	}
	m := s.lock(name)
	if !m.TryLock() {
		return false
	}
	go func() {
		defer m.Unlock()
		if _, err := s.checkLocked(context.WithoutCancel(ctx), a, real); err != nil {
			s.Log.Error("check now", "cli", name, "error", err)
		}
	}()
	return true
}

// checkLocked is Check, with the CLI's lock held.
func (s *Service) checkLocked(ctx context.Context, a cli.Adapter, real bool) (cli.LoginResult, error) {
	name := a.Name()
	if _, err := s.Vault.Get(ctx, name); errors.Is(err, credstore.ErrNotFound) {
		r := cli.LoginResult{CLI: name, State: cli.LoginRefused, Detail: "not signed in in Setup"}
		// Nothing to try again: a CLI with no login is signed out at once.
		return r, s.record(ctx, r, "", RefusedInARow)
	}
	var r cli.LoginResult
	for try := 1; try <= RefusedInARow; try++ {
		var version string
		var err error
		r, version, err = s.checkOnce(ctx, a, real)
		if err != nil {
			r = cli.LoginResult{CLI: name, State: cli.LoginError, Detail: err.Error()}
		}
		if err := s.record(ctx, r, version, 1); err != nil {
			return r, err
		}
		if r.State != cli.LoginRefused {
			break
		}
	}
	return r, nil
}

// checkOnce runs the check in a login-check sandbox of its own, which gets that CLI's login and
// nothing else, and removes it afterwards.
func (s *Service) checkOnce(ctx context.Context, a cli.Adapter, real bool) (cli.LoginResult, string, error) {
	row, sb, err := s.Sandboxes.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindLoginCheck, CLI: a.Name(), Minimal: true, Life: 15 * time.Minute})
	if err != nil {
		return cli.LoginResult{}, "", fmt.Errorf("the login-check sandbox did not start: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Sandboxes.Remove(rctx, row.ID); err != nil {
			s.Log.Warn("removing a login-check sandbox", "sandbox", row.ID, "error", err)
		}
	}()
	args := []string{"crew-run", "login-check", a.Name()}
	if real {
		args = append(args, "--real")
	}
	ectx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	code, out, err := s.Exec.Exec(ectx, sb.Container, orchestrator.ExecSpec{
		Cmd: args, User: orchestrator.GuestUser, Env: []string{"HOME=/home/" + orchestrator.GuestUser},
	})
	if err != nil {
		return cli.LoginResult{}, "", fmt.Errorf("the login check did not run: %w", err)
	}
	r, ok := lastResult(out)
	if !ok {
		return cli.LoginResult{}, "", fmt.Errorf("the login check ended with %d and no result: %s", code, tail(out))
	}
	version := s.version(ctx, sb.Container, a.Name())
	if r.State == cli.LoginOK && a.LoginFile() != "" {
		s.keepRenewed(ctx, a, s.Sandboxes.Home(row.ID))
	}
	return r, version, nil
}

// keepRenewed stores the login the CLI renewed during the check, when it is the same account as
// the one signed in in Setup. The file comes from a sandbox, so it is read by its fixed name, only
// as a regular file (12.6).
func (s *Service) keepRenewed(ctx context.Context, a cli.Adapter, home string) {
	b, err := ReadLoginFile(home, a.LoginFile())
	if err != nil {
		s.Log.Warn("reading the renewed login", "cli", a.Name(), "error", err)
		return
	}
	if kept, why := s.Offer(ctx, a, b); !kept && why != "" {
		s.Log.Warn("the renewed login is not kept", "cli", a.Name(), "why", why)
	}
}

// Offer stores login as the CLI's login when it differs from the stored one, has the CLI's shape,
// and the vendor says it is the account of Setup. Its own claims are not believed (spike item 56).
// Another account is refused; the inbox item for that comes with the inbox (M7). why says what
// was decided, and is "" when the login is the one already stored.
func (s *Service) Offer(ctx context.Context, a cli.Adapter, login []byte) (kept bool, why string) {
	stored, err := s.Vault.Get(ctx, a.Name())
	if err != nil {
		return false, "no login of Setup to compare with"
	}
	if bytes.Equal(login, stored.Data) {
		return false, ""
	}
	if err := a.CheckLogin(login); err != nil {
		return false, err.Error()
	}
	// Only what a renewal changes is taken, into the stored login; then that is what the vendor is
	// asked about and what is stored.
	login, err = a.Renewed(stored.Data, login)
	if err != nil {
		return false, err.Error()
	}
	if bytes.Equal(login, stored.Data) {
		return false, ""
	}
	account, err := a.Account(ctx, login)
	if err != nil {
		return false, "the vendor does not know it: " + err.Error()
	}
	if stored.Account == "" || !strings.EqualFold(account, stored.Account) {
		s.Log.Error("a login for another account is refused", "cli", a.Name(), "account", account, "setup", stored.Account)
		return false, fmt.Sprintf("refused: it is the login of %s, and Setup signed in %s", account, stored.Account)
	}
	if err := s.Vault.Put(ctx, credstore.Credential{ID: a.Name(), Kind: stored.Kind, Account: stored.Account, Data: login}, "login-check"); err != nil {
		return false, "storing it: " + err.Error()
	}
	s.Log.Info("the renewed login is kept", "cli", a.Name(), "account", account)
	return true, "kept: the same account, " + account
}

// ErrNotKept is returned for a login file that is not read: a link, not a regular file, or too large.
var ErrNotKept = errors.New("the login file is not kept")

// ReadLoginFile reads a CLI's login file from a sandbox's home: only a regular file, never through a
// link in any part of its path, under 64 KB. The sandbox may still be running.
func ReadLoginFile(home, rel string) ([]byte, error) {
	b, err := homepath.ReadFile(home, rel, 64<<10)
	if errors.Is(err, homepath.ErrNotRegular) || errors.Is(err, homepath.ErrTooLarge) {
		return nil, ErrNotKept
	}
	return b, err
}

// version reads the CLI's version from the image's record of what it holds.
func (s *Service) version(ctx context.Context, container, name string) string {
	_, out, err := s.Exec.Exec(ctx, container, orchestrator.ExecSpec{Cmd: []string{"cat", "/crew/versions.json"}})
	if err != nil {
		return ""
	}
	var v map[string]string
	if json.Unmarshal([]byte(out), &v) != nil {
		return ""
	}
	return strings.TrimSpace(v[name])
}

// lastResult finds the adapter's result: the last line that is one.
func lastResult(out string) (cli.LoginResult, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r cli.LoginResult
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &r) == nil && r.CLI != "" && r.State != "" {
			return r, true
		}
	}
	return cli.LoginResult{}, false
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = "…" + s[len(s)-300:]
	}
	return s
}

// record writes what a check found. refusals is how many refused checks the result counts for: a
// refusal adds to the count, and RefusedInARow of them in a row mark the CLI signed out. A check
// that could not tell changes no state.
func (s *Service) record(ctx context.Context, r cli.LoginResult, version string, refusals int) error {
	detail := r.Detail
	if r.Credits != "" {
		detail += "; paid credits left: " + r.Credits
	}
	var err error
	switch r.State {
	case cli.LoginOK:
		_, err = s.Pool.Exec(ctx, `INSERT INTO cli_logins AS c (cli, state, failures, checked_at, result, real_at, version)
			VALUES ($1, 'signed_in', 0, now(), $2, CASE WHEN $3::boolean THEN now() END, nullif($4::text, ''))
			ON CONFLICT (cli) DO UPDATE SET state = 'signed_in', failures = 0, checked_at = now(), result = $2,
				real_at = CASE WHEN $3::boolean THEN now() ELSE c.real_at END,
				version = coalesce(nullif($4::text, ''), c.version), updated_at = now()`,
			r.CLI, detail, r.Real, version)
	case cli.LoginRefused:
		_, err = s.Pool.Exec(ctx, `INSERT INTO cli_logins AS c (cli, state, failures, checked_at, result, version)
			VALUES ($1, CASE WHEN $4::int >= $5::int THEN 'signed_out' ELSE 'unknown' END, $4::int, now(), $2, nullif($3::text, ''))
			ON CONFLICT (cli) DO UPDATE SET failures = c.failures + $4::int, checked_at = now(), result = $2,
				state = CASE WHEN c.failures + $4::int >= $5::int THEN 'signed_out' ELSE c.state END,
				version = coalesce(nullif($3::text, ''), c.version), updated_at = now()`,
			r.CLI, detail, version, refusals, RefusedInARow)
	default:
		_, err = s.Pool.Exec(ctx, `INSERT INTO cli_logins AS c (cli, checked_at, result) VALUES ($1, now(), $2)
			ON CONFLICT (cli) DO UPDATE SET checked_at = now(), result = $2, updated_at = now()`, r.CLI, detail)
	}
	if err != nil {
		return fmt.Errorf("recording the login check: %w", err)
	}
	if r.State == cli.LoginOK {
		// The list and the limits are kept apart from the login's state: neither changes it (12.3, 12.7).
		if err := models.Record(ctx, s.Pool, r.CLI, r.Models, r.ModelsError); err != nil {
			s.Log.Warn("recording the models", "cli", r.CLI, "error", err)
		}
		if err := models.RecordLimits(ctx, s.Pool, r.CLI, r.Limits, "login check"); err != nil {
			s.Log.Warn("recording the limits", "cli", r.CLI, "error", err)
		}
	}
	s.Log.Info("login check", "cli", r.CLI, "state", r.State, "real", r.Real, "detail", detail)
	return nil
}

// Schedule checks every CLI every 30 minutes, with the real request as often as its adapter says
// (12.6). It runs until ctx ends.
func (s *Service) Schedule(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		for _, name := range cli.Names() {
			due, real := s.due(ctx, name)
			if due {
				if _, err := s.Check(ctx, name, real); err != nil {
					s.Log.Error("the scheduled login check", "cli", name, "error", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// due says whether a CLI's scheduled check is due, and whether it makes the real request.
func (s *Service) due(ctx context.Context, name string) (bool, bool) {
	var checked, real *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT checked_at, real_at FROM cli_logins WHERE cli = $1`, name).Scan(&checked, &real)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, false
	}
	now := s.now()
	isDue := checked == nil || now.Sub(*checked) >= Every
	a, _ := cli.Get(name)
	wantReal := real == nil || now.Sub(*real) >= a.RealEvery()
	return isDue, wantReal
}
