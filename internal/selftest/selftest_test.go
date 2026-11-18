package selftest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/selftest"
)

// The selftest takes one ticket from the OK to Done with the release's own board, pipeline, and
// checks, and leaves no project behind (16.11).
func TestTheSelftestReachesDoneAndLeavesNothing(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	res, err := selftest.Run(ctx, d.Server, "0.1.0", 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || len(res.Problems) != 0 {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.Turns, " ")
	if !strings.Contains(joined, "analysis") || !strings.Contains(joined, "build") {
		t.Fatalf("turns %v", res.Turns)
	}
	var n int
	if err := d.Server.QueryRow(ctx, `SELECT count(*) FROM projects WHERE name LIKE 'selftest-%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d selftest projects left (%v)", n, err)
	}
	// Nor the login rows or the models it lent.
	if err := d.Server.QueryRow(ctx, `SELECT (SELECT count(*) FROM cli_logins) + (SELECT count(*) FROM models)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d login and model rows left (%v)", n, err)
	}
}

// A CLI the login check found signed out stays so after the selftest, which signs it in for its own
// runs only: the install test's fresh machine has such a row.
func TestTheSelftestPutsALoginBackAsItWas(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	if _, err := d.Server.Exec(ctx, `INSERT INTO cli_logins (cli, state, failures, checked_at, result, version)
		VALUES ('agy', 'signed_out', 2, '2026-10-01T00:00:00Z', 'no login', '1.3.4')`); err != nil {
		t.Fatal(err)
	}
	res, err := selftest.Run(ctx, d.Server, "0.1.0", 60*time.Second)
	if err != nil || !res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	var state, result, version string
	var failures int
	if err := d.Server.QueryRow(ctx, `SELECT state, failures, result, version FROM cli_logins WHERE cli = 'agy'`).Scan(&state, &failures, &result, &version); err != nil {
		t.Fatal(err)
	}
	if state != "signed_out" || failures != 2 || result != "no login" || version != "1.3.4" {
		t.Fatalf("%s %d %q %q", state, failures, result, version)
	}
}
