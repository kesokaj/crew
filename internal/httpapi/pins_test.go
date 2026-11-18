package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/pins"
	"github.com/kesokaj/crew/internal/settings"
)

// The daily look on Versions (12.8, since 0.56): the newest look, and Look now, from a page of Crew's
// own address only; a server without the look, as a check server is, says so and looks at nothing.
func TestTheDailyLooksRoutes(t *testing.T) {
	t.Parallel()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "_shell.html"), []byte(shellHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := 0
	publishers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(publishers.Close)
	d := dbtest.New(t, false)
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), WebDir: web, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Pool.Store(d.Server)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	code, body := do(t, srv, "GET", "/api/crew/pins", signedIn, "")
	if code != 200 || body != `{"look":null,"looks":false}`+"\n" {
		t.Fatalf("no look: %d %s", code, body)
	}
	page := with(map[string]string{"Origin": "https://" + host})
	if code, body = do(t, srv, "POST", "/api/crew/pins/look", page, ""); code != 503 || !strings.Contains(body, "does not run on this server") {
		t.Fatalf("a server without the look: %d %s", code, body)
	}

	src := &pins.Sources{GoDL: publishers.URL, Node: publishers.URL, NPM: publishers.URL, DockerAuth: publishers.URL, DockerHub: publishers.URL,
		MCR: publishers.URL, GH: publishers.URL, Agy: publishers.URL + "/m", AgyTarball: cli.AgyTarball}
	s.Pins.Store(&pins.Look{Pool: d.Server, Board: &board.Board{Pool: d.Server}, Sources: src, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if code, body = do(t, srv, "POST", "/api/crew/pins/look", with(map[string]string{"Origin": "https://elsewhere.example.com"}), ""); code != 403 {
		t.Fatalf("from another page: %d %s", code, body)
	}
	if code, body = do(t, srv, "POST", "/api/crew/pins/look", page, ""); code != 200 || !strings.Contains(body, `"source":"Crew's project","text":"there is none`) {
		t.Fatalf("look now: %d %s", code, body)
	}
	if asked != 0 {
		t.Fatalf("with no Crew's project the publishers were asked %d times", asked)
	}
	if code, body = do(t, srv, "GET", "/api/crew/pins", signedIn, ""); code != 200 || !strings.Contains(body, `"looks":true`) || !strings.Contains(body, "there is none") {
		t.Fatalf("the newest look: %d %s", code, body)
	}
}
