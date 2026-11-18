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

// fakeReleases is GitHub's API for tags and release pages, in memory.
type fakeReleases struct {
	mu    sync.Mutex
	tags  map[string]string
	pages map[string]string
	auth  []string
}

func (f *fakeReleases) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/op/crew/git/refs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		var b struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		tag := strings.TrimPrefix(b.Ref, "refs/tags/")
		if _, ok := f.tags[tag]; ok {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
			return
		}
		f.tags[tag] = b.SHA
		w.WriteHeader(201)
	})
	mux.HandleFunc("GET /repos/op/crew/git/ref/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": f.tags[r.PathValue("tag")], "type": "commit"}})
	})
	mux.HandleFunc("GET /repos/op/crew/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if u, ok := f.pages[r.PathValue("tag")]; ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"html_url": u})
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("POST /repos/op/crew/releases", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		tag := b["tag_name"].(string)
		f.pages[tag] = "https://github.com/op/crew/releases/tag/" + tag
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"html_url": f.pages[tag]})
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":"Op"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A release's tag is made at its commit once, and never moved; its page is made once (16.9).
func TestATagAndAReleasePage(t *testing.T) {
	f := &fakeReleases{tags: map[string]string{}, pages: map[string]string{}}
	srv := f.server(t)
	c := &Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: srv.URL}
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	if err := c.Tag(ctx, "op/crew", "v0.1.0", sha); err != nil {
		t.Fatal(err)
	}
	if err := c.Tag(ctx, "op/crew", "v0.1.0", sha); err != nil {
		t.Fatalf("the same tag again: %v", err)
	}
	if err := c.Tag(ctx, "op/crew", "v0.1.0", strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "never moved") {
		t.Fatalf("a tag at another commit: %v", err)
	}
	for _, bad := range [][2]string{{"latest", sha}, {"v0.1.0", "main"}} {
		if err := c.Tag(ctx, "op/crew", bad[0], bad[1]); err == nil {
			t.Fatalf("%v was taken", bad)
		}
	}
	u, err := c.Release(ctx, "op/crew", "v0.1.0", "Crew 0.1.0", "notes")
	if err != nil || u != "https://github.com/op/crew/releases/tag/v0.1.0" {
		t.Fatalf("%q %v", u, err)
	}
	if u2, err := c.Release(ctx, "op/crew", "v0.1.0", "Crew 0.1.0", "notes"); err != nil || u2 != u || len(f.pages) != 1 {
		t.Fatalf("the page again: %q %v", u2, err)
	}
	user, tok, err := c.RegistryLogin(ctx)
	if err != nil || user != "op" || tok == "" {
		t.Fatalf("%q %v", user, err)
	}
	if !strings.HasPrefix(f.auth[0], "Bearer ") {
		t.Fatal("no token was sent")
	}
}
