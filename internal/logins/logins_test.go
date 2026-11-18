package logins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type fakeRuntime struct {
	mu      sync.Mutex
	started []orchestrator.SandboxSpec
	removed []string
}

func (f *fakeRuntime) StartSandbox(_ context.Context, s orchestrator.SandboxSpec) (orchestrator.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, s)
	return orchestrator.Sandbox{ID: s.ID, Container: orchestrator.SandboxName(s.ID)}, nil
}
func (f *fakeRuntime) RemoveSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeRuntime) CutSandbox(context.Context, string) error                      { return nil }
func (f *fakeRuntime) AdoptSandbox(context.Context, string) error                    { return nil }
func (f *fakeRuntime) SandboxAt(string, string) (string, bool)                       { return "", false }
func (f *fakeRuntime) IsSandboxDoor(string) bool                                     { return false }
func (f *fakeRuntime) Sandboxes(context.Context) ([]orchestrator.SandboxInfo, error) { return nil, nil }

// fakeExec answers crew-run login-check with the next scripted result, and may leave a renewed
// login in the sandbox's home, as agy does.
type fakeExec struct {
	m       *sandboxes.Manager
	results []cli.LoginResult
	renewed []byte
	asked   []string
}

func (f *fakeExec) Exec(_ context.Context, container string, spec orchestrator.ExecSpec) (int, string, error) {
	f.asked = append(f.asked, strings.Join(spec.Cmd, " "))
	if spec.Cmd[0] == "cat" {
		return 0, `{"agy":"1.3.4"}`, nil
	}
	if f.renewed != nil {
		id := strings.TrimPrefix(container, "crew-")
		p := filepath.Join(f.m.Home(id), cli.Agy{}.LoginFile())
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, f.renewed, 0o600)
	}
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	b, _ := json.Marshal(r)
	return 0, "some noise on stderr\n" + string(b) + "\n", nil
}

func service(t *testing.T) (*Service, *fakeExec, *credstore.Vault, *fakeRuntime) {
	d := dbtest.New(t, false)
	v, err := credstore.NewVault(d.Server, make([]byte, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{}
	m := &sandboxes.Manager{Runtime: rt, Pool: d.Server, Dir: t.TempDir(), Image: "crew-runtime:1", Log: slog.New(slog.DiscardHandler)}
	ex := &fakeExec{m: m}
	return &Service{Pool: d.Server, Vault: v, Sandboxes: m, Exec: ex, Log: slog.New(slog.DiscardHandler)}, ex, v, rt
}

func state(t *testing.T, s *Service, name string) State {
	states, err := s.States(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.CLI == name {
			return st
		}
	}
	t.Fatalf("no state of %s", name)
	return State{}
}

func TestTwoRefusedChecksInARowSignACLIOut(t *testing.T) {
	ctx := context.Background()
	s, ex, v, rt := service(t)
	v.Put(ctx, credstore.Credential{ID: "agy", Kind: "file", Account: "agy-only@example.com", Data: agyFile("tok")}, "test")

	ex.results = []cli.LoginResult{{CLI: "agy", State: cli.LoginOK, Real: true, Detail: "agy models listed the models"}}
	if r, err := s.Check(ctx, "agy", true); err != nil || r.State != cli.LoginOK {
		t.Fatalf("%+v %v", r, err)
	}
	st := state(t, s, "agy")
	if st.State != StateSignedIn || st.Version != "1.3.4" || st.RealAt == nil || !st.SetUp {
		t.Fatalf("after an ok check: %+v", st)
	}
	// The check ran in a login-check sandbox of its own, which got only that CLI, and was removed.
	if len(rt.started) != 1 || rt.started[0].Kind != sandboxes.KindLoginCheck || len(rt.removed) != 1 {
		t.Fatalf("sandboxes: started %v, removed %v", rt.started, rt.removed)
	}
	if ex.asked[0] != "crew-run login-check agy --real" {
		t.Fatalf("asked %v", ex.asked)
	}

	// A refused check is tried again at once; the second refusal signs the CLI out.
	ex.results = []cli.LoginResult{{CLI: "agy", State: cli.LoginRefused, Detail: "agy says: authentication required"}}
	s.Check(ctx, "agy", true)
	st = state(t, s, "agy")
	if st.State != StateSignedOut || st.Failures != 2 || len(rt.started) != 3 {
		t.Fatalf("after two refusals: %+v, %d sandboxes", st, len(rt.started))
	}
	// One refusal, then an answer: still signed in, and the count starts again.
	ex.results = []cli.LoginResult{{CLI: "agy", State: cli.LoginRefused}, {CLI: "agy", State: cli.LoginOK}}
	s.Check(ctx, "agy", true)
	if st := state(t, s, "agy"); st.State != StateSignedIn || st.Failures != 0 {
		t.Fatalf("after a refusal and an answer: %+v", st)
	}
	// A check that could not tell changes nothing.
	ex.results = []cli.LoginResult{{CLI: "agy", State: cli.LoginError, Detail: "the sandbox did not start"}}
	s.Check(ctx, "agy", true)
	if st := state(t, s, "agy"); st.State != StateSignedIn || st.Result != "the sandbox did not start" {
		t.Fatalf("after an error: %+v", st)
	}
	if got := s.SignedIn(ctx); len(got) != 1 || got[0] != "agy" {
		t.Fatalf("SignedIn = %v", got)
	}
}

func TestACLIWithNoLoginIsSignedOutWithoutASandbox(t *testing.T) {
	s, _, _, rt := service(t)
	r, err := s.Check(context.Background(), "agy", false)
	if err != nil || r.State != cli.LoginRefused || len(rt.started) != 0 {
		t.Fatalf("%+v %v, %d sandboxes", r, err, len(rt.started))
	}
	if st := state(t, s, "agy"); st.State != StateSignedOut || st.SetUp {
		t.Fatalf("%+v", st)
	}
}

func agyFile(access string) []byte {
	return []byte(fmt.Sprintf(`{"auth_method":"consumer","token":{"access_token":%q,"refresh_token":"r","expiry":"2026-10-14T13:00:00Z"}}`, access))
}

// access is the access token of an agy login file.
func access(t *testing.T, login []byte) string {
	var f struct {
		Token struct {
			AccessToken string `json:"access_token"`
		} `json:"token"`
	}
	if err := json.Unmarshal(login, &f); err != nil {
		t.Fatal(err)
	}
	return f.Token.AccessToken
}

func google(t *testing.T, known map[string]string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		for tok, email := range known {
			if strings.Contains(string(b), "access_token="+tok) {
				fmt.Fprintf(w, `{"email":%q,"email_verified":"true"}`, email)
				return
			}
		}
		http.Error(w, "invalid", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	old := cli.TokenInfoURL
	cli.TokenInfoURL = srv.URL
	t.Cleanup(func() { cli.TokenInfoURL = old })
}

func TestARenewedAgyLoginIsKeptOnlyForTheSameAccount(t *testing.T) {
	ctx := context.Background()
	google(t, map[string]string{"old": "agy-only@example.com", "new": "agy-only@example.com", "other": "someone@example.com"})
	s, ex, v, _ := service(t)
	v.Put(ctx, credstore.Credential{ID: "agy", Kind: "file", Account: "agy-only@example.com", Data: agyFile("old")}, "setup")
	ok := []cli.LoginResult{{CLI: "agy", State: cli.LoginOK, Models: []cli.Model{{Name: "gemini-3.8-flash-low", Model: "gemini-3.8-flash", Efforts: []string{"low"}, InName: true}}}}

	ex.results, ex.renewed = ok, agyFile("new")
	s.Check(ctx, "agy", false)
	kept, _ := v.Get(ctx, "agy")
	if access(t, kept.Data) != "new" || kept.Account != "agy-only@example.com" {
		t.Fatalf("the renewed login was not kept: %s", kept.Data)
	}

	// A file for another account is refused (12.6, the done-when of M2).
	ex.results, ex.renewed = ok, agyFile("other")
	s.Check(ctx, "agy", false)
	if c, _ := v.Get(ctx, "agy"); string(c.Data) != string(kept.Data) {
		t.Fatalf("a login for another account was stored: %s", c.Data)
	}
	// So is one Google does not know.
	ex.results, ex.renewed = ok, agyFile("made-up")
	s.Check(ctx, "agy", false)
	if c, _ := v.Get(ctx, "agy"); string(c.Data) != string(kept.Data) {
		t.Fatal("a login Google does not know was stored")
	}
	// The models agy listed are kept.
	var n int
	s.Pool.QueryRow(ctx, `SELECT count(*) FROM models WHERE cli = 'agy' AND offered`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d models", n)
	}
}

// A sandbox may write anything in its home: a file with the operator's live access token beside
// another account's refresh token passes Google's check of the access token, so only the access
// token and its expiry are taken, into the stored login (security review of M2).
func TestARenewedAgyLoginBringsInOnlyTheAccessToken(t *testing.T) {
	ctx := context.Background()
	google(t, map[string]string{"old": "agy-only@example.com", "new": "agy-only@example.com"})
	s, ex, v, _ := service(t)
	v.Put(ctx, credstore.Credential{ID: "agy", Kind: "file", Account: "agy-only@example.com", Data: agyFile("old")}, "setup")
	ok := []cli.LoginResult{{CLI: "agy", State: cli.LoginOK, Models: []cli.Model{{Name: "gemini-3.8-flash-low", Model: "gemini-3.8-flash", Efforts: []string{"low"}, InName: true}}}}
	planted := []byte(`{"auth_method":"consumer","token":{"access_token":"new","refresh_token":"o","id_token":"x","expiry":"2026-10-14T14:00:00Z"}}`)
	ex.results, ex.renewed = ok, planted
	s.Check(ctx, "agy", false)
	c, _ := v.Get(ctx, "agy")
	var got struct {
		Token map[string]string `json:"token"`
	}
	if err := json.Unmarshal(c.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Token["access_token"] != "new" || got.Token["expiry"] != "2026-10-14T14:00:00Z" {
		t.Fatalf("the renewed access token was not taken: %s", c.Data)
	}
	if got.Token["refresh_token"] != "r" || got.Token["id_token"] != "" {
		t.Fatalf("the sandbox's refresh token or id token was stored: %s", c.Data)
	}
}

func TestALinkedLoginFileIsNotRead(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(outside, agyFile("x"), 0o600)
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	os.Symlink(filepath.Dir(outside), filepath.Join(home, ".gemini", "antigravity-cli"))
	if _, err := ReadLoginFile(home, ".gemini/antigravity-cli/elsewhere"); err == nil {
		t.Fatal("a file through a linked folder was read")
	}
	os.Remove(filepath.Join(home, ".gemini", "antigravity-cli"))
	os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0o700)
	os.Symlink(outside, filepath.Join(home, cli.Agy{}.LoginFile()))
	if _, err := ReadLoginFile(home, cli.Agy{}.LoginFile()); err == nil {
		t.Fatal("a linked file was read")
	}
}

func TestWhenAChecksIsDue(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := service(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if due, real := s.due(ctx, "agy"); !due || !real {
		t.Fatal("a CLI never checked is due, with the real request")
	}
	s.Pool.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at, real_at) VALUES ('agy', 'signed_in', $1, $1)`, now.Add(-10*time.Minute))
	if due, _ := s.due(ctx, "agy"); due {
		t.Fatal("checked 10 minutes ago")
	}
	now = now.Add(25 * time.Minute)
	if due, real := s.due(ctx, "agy"); !due || real {
		t.Fatalf("agy after 35 minutes: due %v real %v", due, real)
	}
	// agy's real request is made once a day.
	now = now.Add(24 * time.Hour)
	if due, real := s.due(ctx, "agy"); !due || !real {
		t.Fatalf("agy after a day: due %v real %v", due, real)
	}
}

// A click on Check now while a check of that CLI runs starts no second one: each is a real request
// on the subscription (review of M2).
func TestCheckNowDoesNotQueueASecondCheck(t *testing.T) {
	s, _, _, _ := service(t)
	m := s.lock("agy")
	m.Lock()
	if s.CheckNow(context.Background(), "agy", true) {
		t.Fatal("a second check was started while one runs")
	}
	m.Unlock()
}
