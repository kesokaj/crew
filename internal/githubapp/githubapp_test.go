package githubapp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/githubapp"
)

type vault struct {
	mu sync.Mutex
	m  map[string]credstore.Credential
	// lost names what was sealed with a key file that is lost (21).
	lost map[string]bool
}

func (v *vault) Get(_ context.Context, id string) (credstore.Credential, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.m[id]; ok && v.lost[id] {
		return credstore.Credential{}, credstore.ErrUnreadable
	}
	c, ok := v.m[id]
	if !ok {
		return c, credstore.ErrNotFound
	}
	return c, nil
}
func (v *vault) Put(_ context.Context, c credstore.Credential, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[c.ID] = c
	return nil
}
func (v *vault) Delete(_ context.Context, id, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, id)
	return nil
}

// fakeGitHub checks the App's JWT against the App's public key, as GitHub does.
type fakeGitHub struct {
	t          *testing.T
	pub        *rsa.PublicKey
	mu         sync.Mutex
	n          int
	asked      []map[string]any
	pages      map[string][]map[string]any
	pagesAsked []string
	revoked    []string
	expires    time.Time
	now        *time.Time // the test's clock: the JWT is checked against it, never against the real one
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
		g.revoked = append(g.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "token "))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, err := jwt.ParseWithClaims(raw, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) { return g.pub, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(func() time.Time { return *g.now }))
	if err != nil || tok.Claims.(*jwt.RegisteredClaims).Issuer != "12345" {
		http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/app":
		fmt.Fprint(w, `{"slug":"crew-dave","name":"Crew dave","owner":{"login":"kesokaj"},"permissions":{"contents":"read","metadata":"read"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
		g.pagesAsked = append(g.pagesAsked, r.URL.RawQuery)
		if g.pages != nil {
			json.NewEncoder(w).Encode(g.pages[r.URL.Query().Get("page")])
			return
		}
		fmt.Fprint(w, `[{"id":7,"account":{"login":"other"}},{"id":42,"account":{"login":"kesokaj"}}]`)
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		g.asked = append(g.asked, body)
		g.n++
		json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("ghs_%d", g.n), "expires_at": g.expires})
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}
}

func setup(t *testing.T) (*githubapp.App, *fakeGitHub, *vault, *time.Time) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cred, _ := json.Marshal(githubapp.Credential{AppID: "12345", Key: string(pemKey)})
	v := &vault{m: map[string]credstore.Credential{credstore.GitHubApp: {ID: credstore.GitHubApp, Data: cred}}}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	g := &fakeGitHub{t: t, pub: &key.PublicKey, expires: now.Add(time.Hour), now: &now}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	app := &githubapp.App{Vault: v, API: srv.URL, Log: slog.New(slog.DiscardHandler)}
	app.Now = func() time.Time { return now }
	return app, g, v, &now
}

func TestATokenReadsContentsOfTheNamedRepositoriesOnly(t *testing.T) {
	app, g, v, _ := setup(t)
	ctx := context.Background()
	tok, err := app.TokenFor(ctx, "run-1", []string{"kesokaj/fixture", "kesokaj/lib"})
	if err != nil || tok != "ghs_1" {
		t.Fatalf("TokenFor = %q, %v", tok, err)
	}
	body := g.asked[0]
	if fmt.Sprint(body["permissions"]) != "map[contents:read]" {
		t.Fatalf("permissions %v: only reading contents (R189)", body["permissions"])
	}
	var repos []string
	for _, r := range body["repositories"].([]any) {
		repos = append(repos, r.(string))
	}
	if !slices.Equal(repos, []string{"fixture", "lib"}) {
		t.Fatalf("repositories %v", repos)
	}
	// The same sandbox gets the same token while it is fresh.
	if again, _ := app.TokenFor(ctx, "run-1", []string{"kesokaj/fixture", "kesokaj/lib"}); again != "ghs_1" || len(g.asked) != 1 {
		t.Fatalf("a second ask made a new token: %q", again)
	}
	if _, ok := v.m["github-token/run-1"]; !ok {
		t.Fatal("the token is not kept to revoke after a restart")
	}
}

func TestATokenIsRenewedAfterFiftyMinutesAndRevokedAtTheEnd(t *testing.T) {
	app, g, v, now := setup(t)
	ctx := context.Background()
	app.TokenFor(ctx, "term-1", []string{"kesokaj/fixture"})
	*now = now.Add(49 * time.Minute)
	if tok, _ := app.TokenFor(ctx, "term-1", []string{"kesokaj/fixture"}); tok != "ghs_1" {
		t.Fatalf("renewed too early: %s", tok)
	}
	*now = now.Add(2 * time.Minute) // 51 minutes in
	tok, err := app.TokenFor(ctx, "term-1", []string{"kesokaj/fixture"})
	if err != nil || tok != "ghs_2" {
		t.Fatalf("after 51 minutes: %q, %v", tok, err)
	}
	if !slices.Equal(g.revoked, []string{"ghs_1"}) {
		t.Fatalf("the old token was not revoked: %v", g.revoked)
	}
	app.Revoke(ctx, "term-1")
	if !slices.Equal(g.revoked, []string{"ghs_1", "ghs_2"}) {
		t.Fatalf("revoked %v", g.revoked)
	}
	if _, ok := v.m["github-token/term-1"]; ok {
		t.Fatal("a revoked token is still kept")
	}
	app.Revoke(ctx, "term-1") // twice is harmless
	if len(g.revoked) != 2 {
		t.Fatal("revoked twice")
	}
}

// After a restart the sandbox asks again: the token made before is revoked, not overwritten and left
// to run its hour unknown to Crew.
func TestAfterARestartTheEarlierTokenIsRevokedBeforeANewOne(t *testing.T) {
	app, g, v, _ := setup(t)
	ctx := context.Background()
	app.TokenFor(ctx, "run-9", []string{"kesokaj/fixture"})
	fresh := &githubapp.App{Vault: v, API: app.API, Now: app.Now, Log: slog.New(slog.DiscardHandler)}
	tok, err := fresh.TokenFor(ctx, "run-9", []string{"kesokaj/fixture"})
	if err != nil || tok != "ghs_2" {
		t.Fatalf("TokenFor after a restart = %q, %v", tok, err)
	}
	if !slices.Equal(g.revoked, []string{"ghs_1"}) {
		t.Fatalf("revoked %v: the token from before the restart is still valid", g.revoked)
	}
	fresh.Revoke(ctx, "run-9")
	if _, err := fresh.TokenFor(ctx, "run-9", []string{"kesokaj/fixture"}); err == nil {
		t.Fatal("a removed sandbox got a new token")
	}
	if !slices.Equal(g.revoked, []string{"ghs_1", "ghs_2"}) {
		t.Fatalf("revoked %v", g.revoked)
	}
}

func TestARestartedServerStillRevokes(t *testing.T) {
	app, g, v, _ := setup(t)
	ctx := context.Background()
	app.TokenFor(ctx, "run-9", []string{"kesokaj/fixture"})
	fresh := &githubapp.App{Vault: v, API: app.API, Log: slog.New(slog.DiscardHandler)}
	fresh.Revoke(ctx, "run-9")
	if !slices.Equal(g.revoked, []string{"ghs_1"}) {
		t.Fatalf("revoked %v", g.revoked)
	}
}

// A sandbox's token that was sealed with a key file now lost cannot be revoked, as it cannot be read;
// its row goes all the same, and the token ends within the hour (21).
func TestATokenThatCannotBeReadIsForgotten(t *testing.T) {
	app, g, v, _ := setup(t)
	ctx := context.Background()
	app.TokenFor(ctx, "run-9", []string{"kesokaj/fixture"})
	v.mu.Lock()
	v.lost = map[string]bool{"github-token/run-9": true}
	v.mu.Unlock()
	fresh := &githubapp.App{Vault: v, API: app.API, Log: slog.New(slog.DiscardHandler)}
	fresh.Revoke(ctx, "run-9")
	v.mu.Lock()
	_, kept := v.m["github-token/run-9"]
	v.mu.Unlock()
	if kept || len(g.revoked) != 0 {
		t.Fatalf("kept %v, revoked %v", kept, g.revoked)
	}
}

func TestNoTokenWhenItCannotBeRight(t *testing.T) {
	app, g, v, _ := setup(t)
	ctx := context.Background()
	if tok, err := app.TokenFor(ctx, "chat-1", nil); tok != "" || err != nil {
		t.Fatalf("no repositories: %q %v: GitHub reads an empty list as all", tok, err)
	}
	// The signal of a sandbox with no token keeps the reason's fixed sentence (13.9).
	for _, tc := range []struct {
		repos []string
		why   string
	}{
		{[]string{"kesokaj/a", "other/b"}, githubapp.NoTokenOther},
		{[]string{"nobody/x"}, githubapp.NoTokenNotInstalled},
		{[]string{"../etc"}, githubapp.NoTokenOther},
	} {
		_, err := app.TokenFor(ctx, "run-2", tc.repos)
		if err == nil {
			t.Errorf("%v: a token was made", tc.repos)
		} else if why := githubapp.NoTokenReason(err); why != tc.why {
			t.Errorf("%v: %v reads as %q, not %q", tc.repos, err, why, tc.why)
		}
	}
	if len(g.asked) != 0 {
		t.Fatalf("asked GitHub %d times", len(g.asked))
	}
	v.lost = map[string]bool{credstore.GitHubApp: true}
	if _, err := app.TokenFor(ctx, "run-3", []string{"kesokaj/fixture"}); githubapp.NoTokenReason(err) != githubapp.NoTokenNotSetUp {
		t.Fatalf("a credential that does not open: %v", err)
	}
	delete(v.m, credstore.GitHubApp)
	if _, err := app.TokenFor(ctx, "run-3", []string{"kesokaj/fixture"}); err == nil {
		t.Fatal("a token without the App")
	} else if why := githubapp.NoTokenReason(err); why != githubapp.NoTokenNotSetUp {
		t.Fatalf("no App reads as %q", why)
	}
}

func TestCheckReadsTheApp(t *testing.T) {
	app, _, _, _ := setup(t)
	info, err := app.Check(context.Background())
	if err != nil || info.Slug != "crew-dave" || !slices.Equal(info.Installations, []string{"other", "kesokaj"}) || info.Permissions["contents"] != "read" {
		t.Fatalf("Check = %+v, %v", info, err)
	}
}

func TestInstallationsFollowsPagesUntilNoneIsLeft(t *testing.T) {
	app, g, _, _ := setup(t)
	first := make([]map[string]any, 100)
	for i := range first {
		first[i] = map[string]any{"id": i + 100, "account": map[string]any{"login": fmt.Sprintf("acct-%d", i)}}
	}
	g.pages = map[string][]map[string]any{
		"1": first,
		"2": {{"id": 42, "account": map[string]any{"login": "kesokaj"}}},
	}
	tok, err := app.TokenFor(context.Background(), "run-p", []string{"kesokaj/fixture"})
	if err != nil || tok != "ghs_1" {
		t.Fatalf("TokenFor = %q, %v", tok, err)
	}
	if !slices.Equal(g.pagesAsked, []string{"per_page=100&page=1", "per_page=100&page=2"}) {
		t.Fatalf("pages asked = %v", g.pagesAsked)
	}
}

func TestParseCredential(t *testing.T) {
	if _, err := githubapp.ParseCredential([]byte(`{"appId":"12x","key":"x"}`)); err == nil {
		t.Fatal("a bad id")
	}
	if _, err := githubapp.ParseCredential([]byte(`{"appId":"12","key":"not a key"}`)); err == nil {
		t.Fatal("a bad key")
	}
}
