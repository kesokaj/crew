package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/credstore"
)

// pullAPI plays GitHub's API for pull requests: what it was asked, by method and path, with the body.
type pullAPI struct {
	mu    sync.Mutex
	calls []string
	open  bool // a pull request is open for the branch already: POST /pulls answers 422
}

func (a *pullAPI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_made_up_0123" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		a.mu.Lock()
		a.calls = append(a.calls, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		a.mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/op/shop/pulls" && a.open:
			w.WriteHeader(422)
			w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for op:task/SF-1-x."}]}`))
		case r.Method == "POST" && r.URL.Path == "/repos/op/shop/pulls":
			w.WriteHeader(201)
			w.Write([]byte(`{"number":12,"html_url":"https://github.com/op/shop/pull/12","state":"open"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/op/shop/pulls":
			w.Write([]byte(`[{"number":11,"html_url":"https://github.com/op/shop/pull/11","state":"open"}]`))
		case r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/comments") || strings.Contains(r.URL.Path, "/statuses/")):
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		case r.Method == "PATCH":
			w.Write([]byte(`{"number":12,"state":"closed"}`))
		default:
			http.Error(w, `{"message":"Not Found"}`, 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAPullRequestIsOpenedPostedToAndClosed(t *testing.T) {
	a := &pullAPI{}
	c := &Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: a.server(t).URL}
	ctx := context.Background()
	p, err := c.OpenPull(ctx, "op/shop", "task/SF-1-x", "main", "SF-1: x", "Why.")
	if err != nil || p.Number != 12 || p.URL != "https://github.com/op/shop/pull/12" {
		t.Fatalf("%+v %v", p, err)
	}
	// One open already, from a try whose answer was lost: it is found, not opened twice.
	a.open = true
	if p, err := c.OpenPull(ctx, "op/shop", "task/SF-1-x", "main", "SF-1: x", "Why."); err != nil || p.Number != 11 {
		t.Fatalf("%+v %v", p, err)
	}
	sha := strings.Repeat("ab", 20)
	for _, err := range []error{
		c.Comment(ctx, "op/shop", 12, "**Review** by the reviewer"),
		c.Status(ctx, "op/shop", sha, "success", "crew/exit", "the ticket's checks: passed"),
		c.ClosePull(ctx, "op/shop", 12),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	a.mu.Lock()
	got := strings.Join(a.calls, "\n")
	a.mu.Unlock()
	for _, want := range []string{
		`POST /repos/op/shop/pulls {"base":"main","body":"Why.","head":"task/SF-1-x","maintainer_can_modify":false,"title":"SF-1: x"}`,
		`GET /repos/op/shop/pulls?state=open&head=op%3Atask%2FSF-1-x null`,
		`POST /repos/op/shop/issues/12/comments {"body":"**Review** by the reviewer"}`,
		`POST /repos/op/shop/statuses/` + sha + ` {"context":"crew/exit","description":"the ticket's checks: passed","state":"success"}`,
		`PATCH /repos/op/shop/pulls/12 {"state":"closed"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no call %s in:\n%s", want, got)
		}
	}
	// Crew never asks GitHub to merge.
	if strings.Contains(got, "/merge") {
		t.Fatalf("a merge was asked of GitHub:\n%s", got)
	}
}

func TestWhatAPullRequestCallRefuses(t *testing.T) {
	a := &pullAPI{}
	c := &Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: a.server(t).URL}
	ctx := context.Background()
	sha := strings.Repeat("ab", 20)
	for name, err := range map[string]error{
		"a repository with a path":   c.Comment(ctx, "op/../x", 1, "x"),
		"a status of another's":      c.Status(ctx, "op/shop", sha, "success", "ci/build", "x"),
		"a state GitHub has not":     c.Status(ctx, "op/shop", sha, "fine", "crew/exit", "x"),
		"a status of no commit":      c.Status(ctx, "op/shop", "main", "success", "crew/exit", "x"),
		"a pull request of no owner": func() error { _, err := c.OpenPull(ctx, "shop", "b", "main", "t", "b"); return err }(),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(a.calls) != 0 {
		t.Fatalf("a refused call reached GitHub: %v", a.calls)
	}
	noGH := &Client{Vault: vault{}, API: a.server(t).URL}
	if noGH.SignedIn(ctx) || !c.SignedIn(ctx) {
		t.Fatal("SignedIn")
	}
	if err := noGH.Comment(ctx, "op/shop", 1, "x"); err != ErrNoGH {
		t.Fatalf("without gh: %v", err)
	}
	if RepoOf("git@github.com:op/shop.git") != "op/shop" || RepoOf("https://github.com/op/shop") != "" {
		t.Fatal("RepoOf")
	}
}
