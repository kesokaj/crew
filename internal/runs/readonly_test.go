package runs

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The database cannot be written (21): no new run starts and no turn either, and no microVM is made
// for one; when it can be written again, runs start. The disk guard and a run whose events cannot be
// stored have tests of their own.
func TestNoRunStartsWhileTheDatabaseCannotBeWritten(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	run := k.start(StartRequest{})
	v := k.wait(run, "idle")
	cfg := k.db.Server.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec(ctx, `INSERT INTO crew_state (id) VALUES (2)`); !readOnly(err) {
		t.Fatalf("the database can still be written: %v", err)
	}
	k.d.Pool = ro
	started, continued := len(k.f.Started), len(k.f.Continued)
	if _, err := k.d.Start(ctx, StartRequest{CLI: "agy", Prompt: "While it cannot be written."}); !readOnly(err) {
		t.Fatalf("a run started: %v", err)
	}
	if _, err := k.d.Continue(ctx, v.Subtask, ContinueRequest{Text: "From the operator."}); !readOnly(err) {
		t.Fatalf("a turn started: %v", err)
	}
	if len(k.f.Started) != started || len(k.f.Continued) != continued {
		t.Fatalf("the harness was asked: %d starts, %d continues", len(k.f.Started)-started, len(k.f.Continued)-continued)
	}
	k.d.Pool = k.db.Server
	k.wait(k.start(StartRequest{Prompt: "Written again."}), "idle")
}

func readOnly(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "25006"
}
