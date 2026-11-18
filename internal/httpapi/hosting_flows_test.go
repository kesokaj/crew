package httpapi_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/hosting"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// hostRuntime is the orchestrator as hosting asks it, in a test of the routes: it keeps every image
// and starts nothing, so a deploy beyond its record ends failed at once.
type hostRuntime struct{}

var errNoMicroVM = errors.New("no microVM in this test")

func (hostRuntime) JoinIngress(context.Context, string) error { return nil }
func (hostRuntime) CreateHostedNet(context.Context, string, bool) (orchestrator.HostedNet, error) {
	return orchestrator.HostedNet{}, errNoMicroVM
}
func (hostRuntime) ProbeHosted(context.Context, string, orchestrator.HostedNet) error {
	return errNoMicroVM
}
func (hostRuntime) StartHosted(context.Context, orchestrator.HostedSpec) (orchestrator.Sandbox, error) {
	return orchestrator.Sandbox{}, errNoMicroVM
}
func (hostRuntime) RemoveSandbox(context.Context, string) error          { return nil }
func (hostRuntime) LoadImage(context.Context, io.Reader) (string, error) { return "", errNoMicroVM }
func (hostRuntime) TagImage(context.Context, string, string) error       { return nil }
func (hostRuntime) RemoveImage(context.Context, string) error            { return nil }
func (hostRuntime) HasImage(context.Context, string) (bool, error)       { return true, nil }
func (hostRuntime) ImageConfig(context.Context, string) ([]string, []string, []string, error) {
	return nil, nil, nil, nil
}
func (hostRuntime) Pause(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}
func (hostRuntime) Restart(context.Context, string) error               { return nil }
func (hostRuntime) StopHosted(context.Context, string) error            { return nil }
func (hostRuntime) StartAgain(context.Context, string) error            { return nil }
func (hostRuntime) HostedRunning(context.Context, string) (bool, error) { return true, nil }
func (hostRuntime) HostedLogs(context.Context, string, time.Time) ([]byte, error) {
	return nil, nil
}

// noDockerfile is main with a commit and no Dockerfile.
type noDockerfile struct{}

func (noDockerfile) Main(context.Context, string) string                            { return strings.Repeat("a", 40) }
func (noDockerfile) FileAt(context.Context, string, string, string) ([]byte, error) { return nil, nil }
func (noDockerfile) Archive(context.Context, string, string, io.Writer) error       { return nil }

// hostingFlows is the board's server with a hosting that has a runtime, a repository and the board.
func hostingFlows(t *testing.T) (*boardServer, *hosting.Service) {
	t.Helper()
	bs, _, _ := newHostingServer(t)
	h := bs.s.Hosting.Load()
	h.Runtime, h.Git, h.Dir, h.Builds = hostRuntime{}, noDockerfile{}, t.TempDir(), t.TempDir()
	h.Tickets = func(ctx context.Context, projectID string, n board.NewTicket) (hosting.TicketMade, error) {
		tk, err := bs.b.AddTicket(ctx, projectID, n, board.OriginOperator, board.Operator)
		if err != nil {
			return hosting.TicketMade{}, err
		}
		return hosting.TicketMade{ID: tk.ID, Key: tk.Key}, nil
	}
	return bs, h
}

// Host locally on a project whose main has no Dockerfile (19.2 step 2, plan M19 D14): 202 with the
// ticket that makes it hostable, made once; a second press makes none.
func TestHostLocallyWithNoDockerfileMakesOneTicket(t *testing.T) {
	t.Parallel()
	bs, _ := hostingFlows(t)
	var out struct {
		State, Ticket string
	}
	bs.call(t, "POST", "/api/projects/shop/hosting/deploys", "", 202, &out)
	if out.State != "waiting_dockerfile" || out.Ticket == "" {
		t.Fatalf("%+v", out)
	}
	var again struct{ Ticket string }
	bs.call(t, "POST", "/api/projects/shop/hosting/deploys", "", 202, &again)
	if again.Ticket != out.Ticket {
		t.Fatalf("a second ticket: %s", again.Ticket)
	}
	var n int
	var checks string
	if err := bs.b.Pool.QueryRow(context.Background(), `SELECT count(*), max(title) FROM tickets WHERE title LIKE 'Make shop hostable%'`).Scan(&n, &checks); err != nil || n != 1 {
		t.Fatalf("%d tickets %v", n, err)
	}
	var view struct {
		State  string
		Ticket struct{ Key string }
	}
	bs.call(t, "GET", "/api/projects/shop/hosting", "", 200, &view)
	if view.State != "waiting_dockerfile" || view.Ticket.Key != out.Ticket {
		t.Fatalf("the view: %+v", view)
	}
}

// The routes of Interfaces: a deploy where hosting is refused is 409; a roll back asks a fresh
// sign-in, and one of a deploy that is not is 409; Stop of a project not hosted is 409; the logs are
// bounded; a personal token without the operator's sign-in reaches none.
func TestTheHostingRoutes(t *testing.T) {
	t.Parallel()
	bs, h := hostingFlows(t)
	if code, b := do(t, bs.srv, "POST", "/api/projects/shop/hosting/deploys/1/rollback", signedInAt(stale, ""), `{}`); code != 401 {
		t.Fatalf("a stale roll back: %d %s", code, b)
	}
	if code, b := do(t, bs.srv, "POST", "/api/projects/shop/hosting/deploys/1/rollback", signedInAt(time.Minute, ""), `{"restoreData": true}`); code != 409 {
		t.Fatalf("a roll back to no deploy: %d %s", code, b)
	}
	if code, b := do(t, bs.srv, "DELETE", "/api/projects/shop/hosting", signedInAt(stale, ""), ``); code != 409 {
		t.Fatalf("stop, not hosted: %d %s", code, b)
	}
	for q, want := range map[string]int{"": 200, "?lines=5": 200, "?lines=0": 400, "?lines=2001": 400, "?lines=x": 400} {
		if code, b := do(t, bs.srv, "GET", "/api/projects/shop/hosting/logs"+q, with(nil), ``); code != want {
			t.Fatalf("logs%s: %d %s", q, code, b)
		}
	}
	for _, r := range [][2]string{{"POST", "/api/projects/shop/hosting/deploys"}, {"DELETE", "/api/projects/shop/hosting"},
		{"POST", "/api/projects/shop/hosting/deploys/1/rollback"}} {
		if code, _ := do(t, bs.srv, r[0], r[1], map[string]string{"Authorization": "Bearer crew_pat_x"}, `{}`); code < 400 {
			t.Fatalf("a personal token reached %s %s: %d", r[0], r[1], code)
		}
	}
	h.AppsDomain = ""
	if code, b := do(t, bs.srv, "POST", "/api/projects/shop/hosting/deploys", signedInAt(stale, ""), ``); code != 409 || !strings.Contains(b, "CREW_APPS_DOMAIN") {
		t.Fatalf("a deploy where hosting is refused: %d %s", code, b)
	}
}

// A change of the internet on a live project starts a settings deploy of the live image at once (plan
// M19 D21); a change of another key starts none.
func TestTurningTheInternetOffRedeploysTheLiveImage(t *testing.T) {
	t.Parallel()
	bs, h := hostingFlows(t)
	ctx := context.Background()
	var id string
	bs.b.Pool.QueryRow(ctx, `SELECT id FROM projects WHERE name = 'shop'`).Scan(&id)
	img := "sha256:" + strings.Repeat("b", 64)
	var d int64
	if err := bs.b.Pool.QueryRow(ctx, `INSERT INTO deploys (project, number, commit, image, why, asked_by, state, live_at)
		VALUES ($1, 1, $2, $3, 'asked', 'operator', 'live', now()) RETURNING id`, id, strings.Repeat("a", 40), img).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.b.Pool.Exec(ctx, `INSERT INTO hosting (project, state, live_deploy, sandbox, address) VALUES ($1, 'live', $2, 'host-x-1', '172.20.1.252')`, id, d); err != nil {
		t.Fatal(err)
	}
	put := func(doc string) {
		t.Helper()
		if code, b := do(t, bs.srv, "PUT", "/api/projects/shop/settings", signedInAt(time.Minute, ""), doc); code != 200 {
			t.Fatalf("%d %s", code, b)
		}
		h.Wait()
	}
	put(`{"version": 1, "project": "shop", "key": "SF", "hosting": {"log_days": 7}}`)
	var n int
	bs.b.Pool.QueryRow(ctx, `SELECT count(*) FROM deploys WHERE project = $1`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("log_days started a deploy: %d", n)
	}
	put(`{"version": 1, "project": "shop", "key": "SF", "hosting": {"log_days": 7, "internet": false}}`)
	var why, image, state string
	if err := bs.b.Pool.QueryRow(ctx, `SELECT why, image, state FROM deploys WHERE project = $1 AND number = 2`, id).Scan(&why, &image, &state); err != nil ||
		why != "settings" || image != img {
		t.Fatalf("no settings deploy of the live image: %s %s %s %v", why, image, state, err)
	}
}

// seedLive makes shop hosted and live, with its data folder.
func seedLive(t *testing.T, bs *boardServer, h *hosting.Service) string {
	t.Helper()
	ctx := context.Background()
	var id string
	bs.b.Pool.QueryRow(ctx, `SELECT id FROM projects WHERE name = 'shop'`).Scan(&id)
	var d int64
	if err := bs.b.Pool.QueryRow(ctx, `INSERT INTO deploys (project, number, commit, image, why, asked_by, state, live_at)
		VALUES ($1, 1, $2, $3, 'asked', 'operator', 'live', now()) RETURNING id`, id, strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.b.Pool.Exec(ctx, `INSERT INTO hosting (project, state, live_deploy, sandbox, address) VALUES ($1, 'live', $2, 'host-x-1', '172.20.1.252')`, id, d); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.Dir, id, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	return id
}

func hostingState(t *testing.T, bs *boardServer, id string) string {
	var s string
	bs.b.Pool.QueryRow(context.Background(), `SELECT state FROM hosting WHERE project = $1`, id).Scan(&s)
	return s
}

// Pause does not touch hosting; Archive stops it and keeps the data (plan M19, Stop).
func TestArchiveStopsHostingAndPauseDoesNot(t *testing.T) {
	t.Parallel()
	bs, h := hostingFlows(t)
	id := seedLive(t, bs, h)
	if _, err := bs.b.Pool.Exec(context.Background(), `UPDATE projects SET state = 'running' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	bs.call(t, "POST", "/api/projects/shop/pause", `{}`, 200, nil)
	if s := hostingState(t, bs, id); s != "live" {
		t.Fatalf("pause: %s", s)
	}
	bs.call(t, "POST", "/api/projects/shop/archive", `{}`, 200, nil)
	if s := hostingState(t, bs, id); s != "stopped" {
		t.Fatalf("archive: %s", s)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, id, "data")); err != nil {
		t.Fatal("archive removed the data")
	}
}

// Delete removes the hosting's folder and its values.
func TestDeleteForgetsTheHosting(t *testing.T) {
	t.Parallel()
	bs, h := hostingFlows(t)
	id := seedLive(t, bs, h)
	if err := h.SetValues(context.Background(), id, map[string]string{"A": "1"}, "test"); err != nil {
		t.Fatal(err)
	}
	bs.call(t, "DELETE", "/api/projects/shop", `{"name": "shop"}`, 200, nil)
	if _, err := os.Stat(filepath.Join(h.Dir, id)); !os.IsNotExist(err) {
		t.Fatal("the hosted folder was kept")
	}
	var n int
	bs.b.Pool.QueryRow(context.Background(), `SELECT count(*) FROM credentials WHERE id = $1`, "host/"+id).Scan(&n)
	if n != 0 {
		t.Fatal("the values were kept")
	}
}
