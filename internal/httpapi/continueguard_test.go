package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/models/modelstest"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/settings"
)

// The operator's own continue of a ticket's reviewer onto the builder's family is refused as a
// precondition, with the guard's reason: the rule for checks moves the operator's choice as it moves a
// pin (12.3; the security review of 1.15's change, finding 3).
func TestTheOperatorsContinueOfACheckKeepsOffTheBuildersFamily(t *testing.T) {
	t.Parallel()
	d := dbtest.New(t, false)
	ctx := context.Background()
	d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, checked_at) VALUES ('agy', 'signed_in', now())`)
	modelstest.Recorded(t, d.Server)
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

	code, body := do(t, srv, http.MethodPost, "/api/runs", change, `{"cli":"agy","model":"claude-opus-5-5","prompt":"Review it."}`)
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	var started struct{ ID string }
	json.Unmarshal([]byte(body), &started)
	if code, body = do(t, srv, http.MethodPost, "/api/runs/"+started.ID+"/stop", change, ""); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	var det runs.Detail
	for deadline := time.Now().Add(10 * time.Second); det.State != "ended" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, body = do(t, srv, http.MethodGet, "/api/runs/"+started.ID, signedIn, "")
		json.Unmarshal([]byte(body), &det)
	}
	// The run becomes a ticket's reviewer, whose builder ran on Gemini.
	const shop = "5a000000-0000-4000-8000-000000000001"
	var ticket int64
	if _, err := d.Server.Exec(ctx, `INSERT INTO projects (id, name, key, repo_source, state) VALUES ($1, 'shop', 'SF', 'local', 'running')`, shop); err != nil {
		t.Fatal(err)
	}
	if err := d.Server.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position)
		VALUES ($1, 1, 'Lock', 'feature', 'review', 'plan', 'lead', 1) RETURNING id`, shop).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	var build int64
	if err := d.Server.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state)
		VALUES ('ticket', $1, $2, 1, 'build', 'builder', 'done') RETURNING id`, shop, ticket).Scan(&build); err != nil {
		t.Fatal(err)
	}
	const conv = "5a000000-0000-4000-8000-0000000000b1"
	d.Server.Exec(ctx, `INSERT INTO conversations (id, project, ticket, purpose, cli) VALUES ($1, $2, $3, 'run', 'agy')`, conv, shop, ticket)
	if _, err := d.Server.Exec(ctx, `INSERT INTO runs (id, subtask, number, state, cli, model, model_name, crew_version, image, conversation)
		VALUES ($1, $2, 1, 'ended', 'agy', 'gemini-3.8-flash', 'gemini-3.8-flash-high', 'test', 'test', $1::uuid)`, conv, build); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE subtasks SET scope = 'ticket', project = $2, ticket = $3, number = 2, kind = 'review', role = 'reviewer'
		WHERE id = $1`, det.Subtask, shop, ticket); err != nil {
		t.Fatal(err)
	}
	code, body = do(t, srv, http.MethodPost, "/api/subtasks/"+itoa(det.Subtask)+"/continue", change, `{"model":"gemini-4-argon"}`)
	if code != http.StatusConflict || !strings.Contains(body, "gemini-4-argon is of the builder's family, gemini") {
		t.Fatalf("%d %s", code, body)
	}
}
