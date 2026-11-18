package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// runServer is the API with a run driver on a FakeHarness that holds each turn until it is stopped.
func runServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := dbtest.New(t, false)
	ctx := context.Background()
	d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`)
	f := &harness.Fake{
		Next: func(harness.RunSpec, int) harness.Script {
			return harness.Script{Recording: "../../testdata/agy/normal-run-flash-low.jsonl", Hold: true}
		},
		MakeSandbox: func(id string) error {
			_, err := d.Server.Exec(ctx, `INSERT INTO sandboxes (id, kind, container, image) VALUES ($1, 'run', $1, 'x')`, id)
			return err
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &httpapi.Server{Machine: settings.Machine{Version: "1.0.0", AdminEmail: admin}, Hostname: host, AdminEmail: admin,
		IngressToken: []byte(token), Log: log}
	s.Pool.Store(d.Server)
	s.Runs.Store(&runs.Driver{Pool: d.Server, Harness: f, Version: "1.0.0", Image: "x", Log: log})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

var change = with(map[string]string{"Origin": "https://" + host})

func TestTheRunRoutes(t *testing.T) {
	t.Parallel()
	srv := runServer(t)
	code, body := do(t, srv, http.MethodPost, "/api/runs", change, `{"cli":"agy","model":"gemini-3.8-flash-low","prompt":"Make hello.txt."}`)
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	var started struct{ ID string }
	json.Unmarshal([]byte(body), &started)

	// The run shows as running, with its events, live.
	var det runs.Detail
	deadline := time.Now().Add(10 * time.Second)
	for det.Events < 3 && time.Now().Before(deadline) {
		_, body = do(t, srv, http.MethodGet, "/api/runs/"+started.ID, signedIn, "")
		json.Unmarshal([]byte(body), &det)
		time.Sleep(20 * time.Millisecond)
	}
	if det.State != "working" || det.Prompt != "Make hello.txt." || det.Model != "gemini-3.8-flash-low" {
		t.Fatalf("%+v", det.RunView)
	}
	code, body = do(t, srv, http.MethodGet, "/api/runs/"+started.ID+"/events?after=1&raw=true", signedIn, "")
	if code != 200 || !strings.Contains(body, `"seq":2`) || strings.Contains(body, `"seq":1,`) || !strings.Contains(body, `"raw":"{`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body = do(t, srv, http.MethodGet, "/api/runs", signedIn, ""); code != 200 || !strings.Contains(body, started.ID) {
		t.Fatalf("%d %s", code, body)
	}

	// Stop, then Continue in a new microVM.
	if code, body = do(t, srv, http.MethodPost, "/api/runs/"+started.ID+"/stop", change, ""); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	for det.State != "ended" && time.Now().Before(deadline) {
		_, body = do(t, srv, http.MethodGet, "/api/runs/"+started.ID, signedIn, "")
		json.Unmarshal([]byte(body), &det)
		time.Sleep(20 * time.Millisecond)
	}
	if det.StopReason != "operator" {
		t.Fatalf("%+v", det.RunView)
	}
	if code, body = do(t, srv, http.MethodPost, "/api/runs/"+started.ID+"/stop", change, ""); code != http.StatusConflict {
		t.Fatalf("a second stop: %d %s", code, body)
	}
	code, body = do(t, srv, http.MethodPost, "/api/subtasks/"+itoa(det.Subtask)+"/continue", change, `{"way":"files"}`)
	if code != 200 || !strings.Contains(body, `"run":`) {
		t.Fatalf("%d %s", code, body)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestTheRunRoutesRefuse(t *testing.T) {
	t.Parallel()
	srv := runServer(t)
	for _, c := range []struct {
		method, path string
		hdr          map[string]string
		body         string
		code         int
	}{
		{http.MethodPost, "/api/runs", signedIn, `{"cli":"agy","prompt":"x"}`, http.StatusForbidden}, // not from Crew's own page
		{http.MethodGet, "/api/runs", nil, "", http.StatusUnauthorized},
		{http.MethodPost, "/api/runs", change, `{"cli":"codex","prompt":"x"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/runs", change, `{"cli":"agy","prompt":""}`, http.StatusBadRequest},
		{http.MethodPost, "/api/runs", change, `{"cli":`, http.StatusBadRequest},
		{http.MethodGet, "/api/runs/not-a-uuid", signedIn, "", http.StatusNotFound},
		{http.MethodGet, "/api/runs/f1c50000-0000-4000-8000-000000000009", signedIn, "", http.StatusNotFound},
		{http.MethodPost, "/api/runs/not-a-uuid/stop", change, "", http.StatusNotFound},
		{http.MethodPost, "/api/subtasks/x/continue", change, "", http.StatusNotFound},
		{http.MethodPost, "/api/subtasks/999/continue", change, "", http.StatusNotFound},
		{http.MethodPost, "/api/subtasks/1/continue", change, `{"way":"sideways"}`, http.StatusBadRequest},
	} {
		if code, body := do(t, srv, c.method, c.path, c.hdr, c.body); code != c.code {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, code, body, c.code)
		}
	}
}
