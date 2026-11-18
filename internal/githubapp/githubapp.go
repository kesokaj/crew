// Package githubapp makes each sandbox's temporary GitHub token (design 12.6, R189): an
// installation token of the operator's private GitHub App, which names only the repositories the
// sandbox may read, can only read contents, and stops working after one hour. Crew revokes it when
// the sandbox ends, and makes a new one for a sandbox that runs longer than 50 minutes.
//
// The App's JWT is signed with github.com/golang-jwt/jwt/v5, never by hand (AGENTS.md).
package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kesokaj/crew/internal/credstore"
)

// Credential is the App as Setup keeps it, under credstore.GitHubApp.
type Credential struct {
	AppID string `json:"appId"`
	Key   string `json:"key"` // the private key GitHub gave, PEM
}

// Vault is the part of the credential store the App uses.
type Vault interface {
	Get(ctx context.Context, id string) (credstore.Credential, error)
	Put(ctx context.Context, c credstore.Credential, who string) error
	Delete(ctx context.Context, id, who string) error
}

// App makes and revokes the temporary tokens.
type App struct {
	Vault Vault
	HTTP  *http.Client
	API   string // https://api.github.com, or a test server
	Log   *slog.Logger
	Now   func() time.Time

	mu    sync.Mutex
	cache map[string]token     // by sandbox
	gone  map[string]time.Time // sandboxes whose token was revoked: no new one is made for them
}

type token struct {
	value   string
	expires time.Time
}

// Renew is how long before its end a token is replaced: a sandbox still running after 50 minutes
// gets a new one (12.6).
const Renew = 10 * time.Minute

// ErrNotSetUp is returned when Setup has no App yet.
var ErrNotSetUp = errors.New("the GitHub App is not set up")

// ErrNotInstalled is returned when the App is not installed on the account that owns the repositories.
var ErrNotInstalled = errors.New("the GitHub App is not installed")

var repoRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) api() string {
	if a.API != "" {
		return strings.TrimRight(a.API, "/")
	}
	return "https://api.github.com"
}

func (a *App) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// vaultID is where a sandbox's token is kept, sealed, so it can be revoked after a restart too.
func vaultID(sandbox string) string { return "github-token/" + sandbox }

// TokenFor returns the sandbox's token for repos, making one when it has none or when its is near
// its end. With no repository there is no token: GitHub would read an empty list as all of them.
func (a *App) TokenFor(ctx context.Context, sandbox string, repos []string) (string, error) {
	if len(repos) == 0 {
		return "", nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.gone[sandbox]; ok {
		return "", errors.New("the sandbox is removed")
	}
	if t, ok := a.cache[sandbox]; ok && a.now().Before(t.expires.Add(-Renew)) {
		return t.value, nil
	}
	if old, ok := a.cache[sandbox]; ok {
		a.revoke(ctx, old.value)
	} else if c, err := a.Vault.Get(ctx, vaultID(sandbox)); err == nil && len(c.Data) > 0 {
		// A token made before a restart: Crew no longer knows when it ends, so it is revoked, not
		// left to run its hour unknown.
		a.revoke(ctx, string(c.Data))
	}
	t, err := a.make(ctx, repos)
	if err != nil {
		return "", err
	}
	if a.cache == nil {
		a.cache = map[string]token{}
	}
	a.cache[sandbox] = t
	if err := a.Vault.Put(ctx, credstore.Credential{ID: vaultID(sandbox), Kind: "temporary", Data: []byte(t.value)}, "crew"); err != nil {
		a.Log.Warn("keeping a sandbox's GitHub token to revoke it later", "sandbox", sandbox, "error", err)
	}
	return t.value, nil
}

// Revoke ends the sandbox's token, at GitHub and in Crew. It is called when the sandbox is removed.
func (a *App) Revoke(ctx context.Context, sandbox string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A request that passed the broker before the sandbox was refused gets no new token after this.
	if a.gone == nil {
		a.gone = map[string]time.Time{}
	}
	for id, at := range a.gone {
		if a.now().Sub(at) > 2*time.Hour {
			delete(a.gone, id)
		}
	}
	a.gone[sandbox] = a.now()
	value := ""
	if t, ok := a.cache[sandbox]; ok {
		value = t.value
		delete(a.cache, sandbox)
	} else if c, err := a.Vault.Get(ctx, vaultID(sandbox)); err == nil {
		value = string(c.Data)
	} else if errors.Is(err, credstore.ErrUnreadable) {
		// Sealed with a key file that is lost (21): it cannot be revoked, and ends within the hour.
		if err := a.Vault.Delete(ctx, vaultID(sandbox), "crew"); err != nil {
			a.Log.Warn("forgetting a GitHub token that cannot be read", "sandbox", sandbox, "error", err)
		}
		return
	}
	if value == "" {
		return
	}
	a.revoke(ctx, value)
	if err := a.Vault.Delete(ctx, vaultID(sandbox), "crew"); err != nil {
		a.Log.Warn("forgetting a revoked GitHub token", "sandbox", sandbox, "error", err)
	}
}

func (a *App) revoke(ctx context.Context, value string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, a.api()+"/installation/token", nil)
	req.Header.Set("Authorization", "token "+value)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.client().Do(req)
	if err != nil {
		a.Log.Warn("revoking a GitHub token", "error", err)
		return
	}
	resp.Body.Close()
	// 204 is revoked, 401 is already gone: both are what Crew wants.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusUnauthorized {
		a.Log.Warn("revoking a GitHub token", "status", resp.Status)
	}
}

// Info is what Setup shows about the App.
type Info struct {
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	Owner         string            `json:"owner"`
	Installations []string          `json:"installations"` // the accounts it is installed on
	Permissions   map[string]string `json:"permissions"`
}

// Check reads the App from GitHub with its key, for Setup: it proves the id and the key belong
// together, and shows where it is installed and what it may do.
func (a *App) Check(ctx context.Context) (Info, error) {
	cred, err := a.credential(ctx)
	if err != nil {
		return Info{}, err
	}
	jwtTok, err := appJWT(cred, a.now())
	if err != nil {
		return Info{}, err
	}
	var app struct {
		Slug        string                 `json:"slug"`
		Name        string                 `json:"name"`
		Owner       struct{ Login string } `json:"owner"`
		Permissions map[string]string      `json:"permissions"`
	}
	if err := a.call(ctx, http.MethodGet, "/app", "Bearer "+jwtTok, nil, &app); err != nil {
		return Info{}, err
	}
	insts, err := a.installations(ctx, jwtTok)
	if err != nil {
		return Info{}, err
	}
	info := Info{Slug: app.Slug, Name: app.Name, Owner: app.Owner.Login, Permissions: app.Permissions, Installations: []string{}}
	for _, i := range insts {
		info.Installations = append(info.Installations, i.Account.Login)
	}
	return info, nil
}

// ParseCredential checks what Setup was given: an App id and a private key that parses.
func ParseCredential(data []byte) (Credential, error) {
	var c Credential
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("the GitHub App: %w", err)
	}
	if _, err := strconv.ParseUint(c.AppID, 10, 64); err != nil {
		return c, fmt.Errorf("the App id %q is not a number", c.AppID)
	}
	if _, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(c.Key)); err != nil {
		return c, fmt.Errorf("the App's private key: %w", err)
	}
	return c, nil
}

func (a *App) credential(ctx context.Context) (Credential, error) {
	c, err := a.Vault.Get(ctx, credstore.GitHubApp)
	if errors.Is(err, credstore.ErrNotFound) {
		return Credential{}, ErrNotSetUp
	}
	if err != nil {
		return Credential{}, err
	}
	return ParseCredential(c.Data)
}

// appJWT is the App's own token, which lasts ten minutes and only asks for installation tokens.
func appJWT(c Credential, now time.Time) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(c.Key))
	if err != nil {
		return "", err
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    c.AppID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)), // GitHub allows for a clock that is behind
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	}).SignedString(key)
}

type installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

func (a *App) installations(ctx context.Context, jwtTok string) ([]installation, error) {
	var all []installation
	for page := 1; ; page++ {
		var insts []installation
		if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), "Bearer "+jwtTok, nil, &insts); err != nil {
			return nil, err
		}
		all = append(all, insts...)
		if len(insts) < 100 {
			return all, nil
		}
	}
}

// make asks GitHub for a token that may read the contents of repos and nothing else. All of repos
// must belong to one account, the one the App is installed on.
func (a *App) make(ctx context.Context, repos []string) (token, error) {
	owner := ""
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		if !repoRe.MatchString(r) {
			return token{}, fmt.Errorf("repository %q is not owner/name", r)
		}
		o, n, _ := strings.Cut(r, "/")
		if owner == "" {
			owner = o
		} else if !strings.EqualFold(owner, o) {
			return token{}, fmt.Errorf("the repositories belong to %s and %s: one token reads one account's", owner, o)
		}
		names = append(names, n)
	}
	cred, err := a.credential(ctx)
	if err != nil {
		return token{}, err
	}
	jwtTok, err := appJWT(cred, a.now())
	if err != nil {
		return token{}, err
	}
	insts, err := a.installations(ctx, jwtTok)
	if err != nil {
		return token{}, err
	}
	var inst int64
	for _, i := range insts {
		if strings.EqualFold(i.Account.Login, owner) {
			inst = i.ID
		}
	}
	if inst == 0 {
		return token{}, fmt.Errorf("%w on %s", ErrNotInstalled, owner)
	}
	body, _ := json.Marshal(map[string]any{
		"repositories": names,
		"permissions":  map[string]string{"contents": "read"},
	})
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst), "Bearer "+jwtTok, body, &out); err != nil {
		return token{}, err
	}
	if out.Token == "" {
		return token{}, errors.New("GitHub gave no token")
	}
	return token{value: out.Token, expires: out.ExpiresAt}, nil
}

func (a *App) call(ctx context.Context, method, path, auth string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.api()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Message string }
		json.Unmarshal(data, &e)
		return fmt.Errorf("GitHub %s %s: %s %s", method, strings.SplitN(path, "?", 2)[0], resp.Status, e.Message)
	}
	return json.Unmarshal(data, out)
}
