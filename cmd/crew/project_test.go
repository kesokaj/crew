package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/httpapi"
)

// crew admin api calls one route as the ingress would, prints the answer, and fails on a refusal. It
// takes only the API's own paths and the methods of its routes.
func TestAdminAPI(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, body = r, string(b)
		if r.URL.Path == "/api/projects/nope/board" {
			w.WriteHeader(http.StatusNotFound)
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("the-secret\n"), 0o600)
	e := env{IngressTokenFile: tokenFile, Listen: strings.TrimPrefix(srv.URL, "http://"), AdminEmail: "op@example.com", Hostname: "crew.example.com"}
	ctx := context.Background()

	var out bytes.Buffer
	if err := adminAPI(ctx, e, []string{"POST", "/api/projects", "-"}, strings.NewReader(`{"name":"shop"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "HTTP 200\n{\"ok\":true}" || body != `{"name":"shop"}` {
		t.Fatalf("out %q, body %q", out.String(), body)
	}
	if got.Header.Get(httpapi.HeaderIngress) != "the-secret" || got.Header.Get(httpapi.HeaderEmail) != "op@example.com" ||
		got.Header.Get("Origin") != "https://crew.example.com" || !freshNow(got.Header.Get(httpapi.HeaderAuthTime)) || got.Header.Get(httpapi.HeaderSession) != "" {
		t.Fatalf("%v", got.Header)
	}
	if err := adminAPI(ctx, e, []string{"GET", "/api/projects/nope/board"}, nil, io.Discard); err == nil {
		t.Fatal("a 404 was not an error")
	}
	for _, args := range [][]string{{"DELETE", "/api/projects/x"}, {"GET", "/healthz"}, {"GET", "http://elsewhere/api/x"}, {"GET"}} {
		if err := adminAPI(ctx, e, args, nil, io.Discard); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

type fakeApp struct{ minted, revoked []string }

func (f *fakeApp) TokenFor(_ context.Context, holder string, repos []string) (string, error) {
	f.minted = append(f.minted, holder+" "+strings.Join(repos, ","))
	return "tok", nil
}
func (f *fakeApp) Revoke(_ context.Context, holder string) { f.revoked = append(f.revoked, holder) }

// Each clone of an existing repository on GitHub gets a token of its own, for that repository only,
// revoked when the clone is done; two clones of one repository never share a holder.
func TestACloneGetsATokenOfItsOwn(t *testing.T) {
	app := &fakeApp{}
	b, _ := openBoard(env{DataDir: t.TempDir()}, nil, nil, app, nil)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		h, done, err := b.ReadHeader(ctx, "https://github.com/owner/repo.git")
		if err != nil || !strings.HasPrefix(h, "Authorization: Basic ") {
			t.Fatalf("header %q, %v", h, err)
		}
		done()
	}
	if len(app.minted) != 2 || app.minted[0] == app.minted[1] || !strings.HasSuffix(app.minted[0], " owner/repo") {
		t.Fatalf("minted %v", app.minted)
	}
	if len(app.revoked) != 2 || !strings.HasPrefix(app.minted[0], app.revoked[0]+" ") {
		t.Fatalf("revoked %v", app.revoked)
	}
	if h, _, err := b.ReadHeader(ctx, "https://example.com/owner/repo.git"); h != "" || err != nil {
		t.Fatalf("another host: %q %v", h, err)
	}
}
