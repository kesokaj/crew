package credbroker_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credbroker"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/sandboxes"
)

type vault map[string]credstore.Credential

func (v vault) Get(_ context.Context, id string) (credstore.Credential, error) {
	c, ok := v[id]
	if !ok {
		return credstore.Credential{}, credstore.ErrNotFound
	}
	return c, nil
}

// Every credential of Setup is there, so what a sandbox does not get is left out by the broker.
func full(t *testing.T) vault {
	id, _ := json.Marshal(credstore.Identity{Name: "Ada", Email: "ada@example.com"})
	gc, err := credstore.MakeBundle([]credstore.BundleFile{
		{Path: ".config/gcloud/application_default_credentials.json", Mode: "0600", Data: []byte("adc")},
		{Path: ".config/gcloud/credentials.db", Mode: "0600", Data: []byte("db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return vault{
		credstore.GitIdentity: {ID: credstore.GitIdentity, Data: id},
		credstore.SSHKey:      {ID: credstore.SSHKey, Data: []byte("PRIVATE-SSH-KEY")},
		credstore.GH:          {ID: credstore.GH, Data: []byte("GH-LOGIN")},
		credstore.GitHubApp:   {ID: credstore.GitHubApp, Data: []byte("APP-KEY")},
		credstore.Gcloud:      {ID: credstore.Gcloud, Data: gc},
		credstore.Agy:         {ID: credstore.Agy, Data: []byte(`{"token":"agy"}`)},
		// The mail account's app password stays with the server (18.4, rules 6, 8).
		credstore.Mail: {ID: credstore.Mail, Data: []byte(`{"account":"crew@example.com","password":"MAIL-APP-PASSWORD"}`)},
		// A claude login left from before 0.42, when Crew ran claude too: it reaches no sandbox.
		"claude":            {ID: "claude", Data: []byte("claude-token\n")},
		"ANTHROPIC_API_KEY": {ID: "ANTHROPIC_API_KEY", Data: []byte("must-never-leave")},
	}
}

type github struct{ asked [][]string }

func (g *github) TokenFor(_ context.Context, _ string, repos []string) (string, error) {
	g.asked = append(g.asked, repos)
	return "ghs_temporary", nil
}

func names(c credbroker.Creds) []string {
	var out []string
	for k := range c.Env {
		out = append(out, "env:"+k)
	}
	for _, f := range c.Files {
		out = append(out, "file:"+f.Path)
	}
	sort.Strings(out)
	return out
}

func TestEachKindGetsWhatTheTableGivesIt(t *testing.T) {
	gh := &github{}
	b := &credbroker.Broker{Vault: full(t), GitHub: gh, Log: slog.New(slog.DiscardHandler),
		SignedIn: func(context.Context) []string { return []string{"agy", "claude"} }}
	agy := "file:" + cli.Agy{}.LoginFile()
	identity := []string{"env:CREW_GIT_EMAIL", "env:CREW_GIT_NAME"}
	gcloud := []string{"file:.config/gcloud/application_default_credentials.json", "file:.config/gcloud/credentials.db"}
	cases := []struct {
		row  sandboxes.Row
		want []string
	}{
		// A run's row that says gcloud gets none (AGENTS.md safety rule 8, 0.79); a terminal's does.
		{sandboxes.Row{Kind: sandboxes.KindRun, CLI: "agy", Gcloud: true}, append([]string{agy, "env:GH_TOKEN"}, identity...)},
		{sandboxes.Row{Kind: sandboxes.KindRun, CLI: "agy", Gcloud: false}, append([]string{agy, "env:GH_TOKEN"}, identity...)},
		{sandboxes.Row{Kind: sandboxes.KindRun, CLI: "claude", Gcloud: false}, append([]string{"env:GH_TOKEN"}, identity...)},
		{sandboxes.Row{Kind: sandboxes.KindTerminal, Gcloud: true}, append(append([]string{agy, "env:GH_TOKEN"}, identity...), gcloud...)},
		{sandboxes.Row{Kind: sandboxes.KindImprover, CLI: "agy", Gcloud: true}, []string{agy}},
		{sandboxes.Row{Kind: sandboxes.KindJudge, CLI: "agy", Gcloud: true}, []string{agy}},
		{sandboxes.Row{Kind: sandboxes.KindLoginCheck, CLI: "agy"}, []string{agy}},
		{sandboxes.Row{Kind: sandboxes.KindChat, CLI: "agy"}, []string{agy, "env:GH_TOKEN"}},
		// A conversation holds what a run of its project holds (13.3), and no gcloud.
		{sandboxes.Row{Kind: sandboxes.KindConversation, CLI: "agy"}, append([]string{agy, "env:GH_TOKEN"}, identity...)},
		{sandboxes.Row{Kind: sandboxes.KindConversation, CLI: "agy", Gcloud: true}, append([]string{agy, "env:GH_TOKEN"}, identity...)},
		{sandboxes.Row{Kind: sandboxes.KindCheck}, []string{"env:GH_TOKEN"}},
		{sandboxes.Row{Kind: sandboxes.KindSetup}, nil},
		{sandboxes.Row{Kind: sandboxes.KindHosted}, nil},
		{sandboxes.Row{Kind: sandboxes.KindBuild}, nil},
		{sandboxes.Row{Kind: sandboxes.KindProbe}, nil},
	}
	for _, c := range cases {
		got, err := b.Collect(context.Background(), c.row)
		if err != nil {
			t.Fatal(err)
		}
		want := slices.Clone(c.want)
		sort.Strings(want)
		if !slices.Equal(names(got), want) {
			t.Errorf("%s/%s: %v, want %v", c.row.Kind, c.row.CLI, names(got), want)
		}
		raw, _ := json.Marshal(got)
		for _, never := range []string{"PRIVATE-SSH-KEY", "GH-LOGIN", "APP-KEY", "must-never-leave", "ANTHROPIC_API_KEY", "claude-token", "MAIL-APP-PASSWORD"} {
			if strings.Contains(string(raw), never) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(never))) {
				t.Errorf("%s got %s", c.row.Kind, never)
			}
		}
	}
}

func TestARunGetsTheTokenForItsRepositoriesOnly(t *testing.T) {
	gh := &github{}
	b := &credbroker.Broker{Vault: full(t), GitHub: gh, Log: slog.New(slog.DiscardHandler)}
	if _, err := b.Collect(context.Background(), sandboxes.Row{ID: "run-1", Kind: sandboxes.KindRun, CLI: "agy", Repos: []string{"kesokaj/fixture"}}); err != nil {
		t.Fatal(err)
	}
	if len(gh.asked) != 1 || !slices.Equal(gh.asked[0], []string{"kesokaj/fixture"}) {
		t.Fatalf("asked GitHub for %v", gh.asked)
	}
}

// A conversation about no project names no repository, so the broker asks GitHub for none, and the
// App makes no token for none (githubapp.TokenFor, TestNoTokenWhenItCannotBeRight): it gets no
// GitHub token (11.8, plan M20 D4).
func TestAConversationAboutNoProjectAsksForNoRepository(t *testing.T) {
	gh := &github{}
	b := &credbroker.Broker{Vault: full(t), GitHub: gh, Log: slog.New(slog.DiscardHandler)}
	if _, err := b.Collect(context.Background(), sandboxes.Row{ID: "conversation-1", Kind: sandboxes.KindConversation, CLI: "agy"}); err != nil {
		t.Fatal(err)
	}
	if len(gh.asked) != 1 || len(gh.asked[0]) != 0 {
		t.Fatalf("asked GitHub for %v", gh.asked)
	}
}

type noGitHub struct{}

func (noGitHub) TokenFor(context.Context, string, []string) (string, error) {
	return "", errors.New("github: 503 Service Unavailable")
}

// lockedLog is a log the broker's handler writes while the test reads it.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// GitHub gives no token for a sandbox (21): the run starts without one, with the rest of what it
// gets, and the sandbox's own request records a signal; collecting its credentials to redact them
// records none. The error is in the server's log with the sandbox, since the signal keeps only a
// fixed sentence of it (13.9).
func TestARunStartsWithoutAGitHubTokenAndItIsSignalled(t *testing.T) {
	var told []string
	var log lockedLog
	row := sandboxes.Row{ID: "run-1", Kind: sandboxes.KindRun, CLI: "agy", Repos: []string{"kesokaj/fixture"}}
	b := &credbroker.Broker{Vault: full(t), GitHub: noGitHub{}, Auth: auth{row}, Log: slog.New(slog.NewTextHandler(&log, nil)),
		NoToken: func(_ context.Context, row sandboxes.Row, err error) { told = append(told, row.ID+": "+err.Error()) }}
	if _, err := b.Collect(context.Background(), row); err != nil || len(told) != 0 {
		t.Fatalf("collecting to redact: %v, signalled %v", err, told)
	}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/creds", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var c credbroker.Creds
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the run cannot start: %d %v", resp.StatusCode, err)
	}
	if _, ok := c.Env["GH_TOKEN"]; ok {
		t.Fatal("a GH_TOKEN with no token")
	}
	if !slices.Contains(names(c), "file:"+(cli.Agy{}).LoginFile()) || c.Env["CREW_GIT_NAME"] != "Ada" {
		t.Fatalf("the rest is missing: %v", names(c))
	}
	if !slices.Equal(told, []string{"run-1: github: 503 Service Unavailable"}) {
		t.Fatalf("signalled %v", told)
	}
	if !strings.Contains(log.String(), `level=WARN msg="broker: no GitHub token for the sandbox" sandbox=run-1 error="github: 503 Service Unavailable"`) {
		t.Fatalf("the error is not in the log:\n%s", log.String())
	}
}

func TestATerminalGetsOnlyTheCLIsThatAreSignedIn(t *testing.T) {
	b := &credbroker.Broker{Vault: full(t), Log: slog.New(slog.DiscardHandler),
		SignedIn: func(context.Context) []string { return []string{"agy"} }}
	got, _ := b.Collect(context.Background(), sandboxes.Row{Kind: sandboxes.KindTerminal})
	if len(got.Files) != 1 || got.Files[0].Path != (cli.Agy{}).LoginFile() {
		t.Fatalf("terminal got %v", names(got))
	}
}

func TestWhatIsNotSetUpIsLeftOut(t *testing.T) {
	b := &credbroker.Broker{Vault: vault{}, Log: slog.New(slog.DiscardHandler)}
	got, err := b.Collect(context.Background(), sandboxes.Row{Kind: sandboxes.KindTerminal, Gcloud: true})
	if err != nil || len(names(got)) != 0 {
		t.Fatalf("%v, %v", names(got), err)
	}
}

type auth struct{ row sandboxes.Row }

func (a auth) Authorize(_ context.Context, _, _, token string) (sandboxes.Row, error) {
	if token != "good" {
		return sandboxes.Row{}, sandboxes.ErrRefused
	}
	return a.row, nil
}

func TestTheHandlerAnswersOnlyAnAuthorizedSandbox(t *testing.T) {
	b := &credbroker.Broker{Vault: full(t), GitHub: &github{}, Log: slog.New(slog.DiscardHandler),
		Auth: auth{sandboxes.Row{ID: "run-1", Kind: sandboxes.KindRun, CLI: "agy"}}}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	get := func(path, token string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp, sb.String()
	}
	for _, tok := range []string{"", "bad"} {
		resp, body := get("/v1/creds", tok)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "agy") {
			t.Fatalf("token %q: %d %s", tok, resp.StatusCode, body)
		}
	}
	resp, body := get("/v1/creds", "good")
	var c credbroker.Creds
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &c) != nil || len(c.Files) == 0 || c.Files[0].Path != (cli.Agy{}).LoginFile() {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the answer may be cached")
	}
	resp, body = get("/v1/git?host=github.com", "good")
	if resp.StatusCode != 200 || !strings.Contains(body, "ghs_temporary") {
		t.Fatalf("git: %d %s", resp.StatusCode, body)
	}
	if resp, _ := get("/v1/git?host=evil.example.com", "good"); resp.StatusCode != 404 {
		t.Fatalf("git for another host: %d", resp.StatusCode)
	}
	if resp, _ := get("/v1/git?host=github.com", "bad"); resp.StatusCode != 403 {
		t.Fatalf("git without the token: %d", resp.StatusCode)
	}
}

func TestBundlesStayInTheHome(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "../x", "a/../../x", "~/x", "", "a//b", "./a"} {
		if _, err := credstore.MakeBundle([]credstore.BundleFile{{Path: p, Mode: "0600", Data: []byte("x")}}); err == nil {
			t.Errorf("MakeBundle took %q", p)
		}
		raw, _ := json.Marshal([]credstore.BundleFile{{Path: p, Mode: "0600", Data: []byte("x")}})
		if _, err := credstore.ParseBundle(raw); err == nil {
			t.Errorf("ParseBundle took %q", p)
		}
	}
	if _, err := credstore.ParseBundle([]byte(`[{"path":"a","mode":"rwx","data":""}]`)); err == nil {
		t.Error("a bad mode was taken")
	}
}
