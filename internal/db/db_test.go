package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

func TestStepsAreNumberedWithoutGaps(t *testing.T) {
	steps, err := db.Steps()
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 || steps[0].Name != "0001_first.sql" {
		t.Fatalf("steps = %v", steps)
	}
}

func TestMigrateAppliesEachStepOnce(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, true)
	conn := d.ConnectAs(t, db.RoleMigrate)
	first, err := db.Migrate(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := db.Steps()
	if len(first) != len(steps) {
		t.Fatalf("applied %v, want every step", first)
	}
	again, err := db.Migrate(ctx, conn)
	if err != nil || len(again) != 0 {
		t.Fatalf("a second run applied %v (%v)", again, err)
	}
	n, err := db.Applied(ctx, d.Server)
	if err != nil || n != len(steps) {
		t.Fatalf("Applied = %d, %v", n, err)
	}
}

// PostgreSQL applies a step as one transaction: a step that fails leaves nothing of itself, and is
// not marked as applied (15.3).
func TestAFailedStepChangesNothing(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	bad := db.Step{Number: len(steps) + 1, Name: "9999_bad.sql",
		SQL: "CREATE TABLE half_done (x int); SELECT no_such_function();"}
	if _, err := db.MigrateSteps(ctx, conn, append(steps, bad)); err == nil {
		t.Fatal("a step that fails was reported as applied")
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('half_done') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("the failed step left its table (%v)", err)
	}
	if n, _ := db.Applied(ctx, conn); n != len(steps) {
		t.Fatalf("Applied = %d after a failed step, want %d", n, len(steps))
	}
}

func TestTheServerRoleCannotChangeTheSchema(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	for _, q := range []string{
		"CREATE TABLE intruder (x int)",
		"DROP TABLE runs",
		"ALTER TABLE settings ADD COLUMN x int",
	} {
		if _, err := d.Server.Exec(ctx, q); err == nil {
			t.Errorf("the role crew could run %q", q)
		}
	}
}

func TestTheReadRoleCanOnlyRead(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	read := d.ConnectAs(t, db.RoleRead)
	var n int
	if err := read.QueryRow(ctx, "SELECT count(*) FROM settings").Scan(&n); err != nil {
		t.Fatalf("crew_read cannot read: %v", err)
	}
	if _, err := read.Exec(ctx, "INSERT INTO jobs (kind, key) VALUES ('x', 'x')"); err == nil {
		t.Error("crew_read could write a row")
	}
}

// Every change has its row in changes, and the history is only added to (6.4).
func TestHistoryIsOnlyAddedTo(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	tx, err := d.Server.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChange(ctx, tx, "test", "settings", "machine", nil, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := db.History(ctx, d.Server, "settings", "machine")
	if err != nil || len(h) != 1 {
		t.Fatalf("History = %v, %v", h, err)
	}
	var after map[string]int
	if err := json.Unmarshal(h[0].After, &after); err != nil || after["a"] != 1 || h[0].Before != nil {
		t.Fatalf("the row holds %s / %s", h[0].Before, h[0].After)
	}
	for _, q := range []string{"UPDATE changes SET who = 'x'", "DELETE FROM changes", "TRUNCATE changes"} {
		if _, err := d.Server.Exec(ctx, q); err == nil {
			t.Errorf("the role crew could run %q", q)
		}
	}
	// The versions of a plan draft and the calls to the Crew MCP are history too (M5).
	for _, tbl := range []string{"plans", "stage_moves", "check_results", "plan_draft_versions", "mcp_calls"} {
		for _, q := range []string{"UPDATE " + tbl + " SET created_by = 'x'", "DELETE FROM " + tbl, "TRUNCATE " + tbl} {
			if tbl != "plans" && q[0] == 'U' {
				q = "UPDATE " + tbl + " SET at = now()"
				if tbl == "plan_draft_versions" {
					q = "UPDATE " + tbl + " SET text = 'x'"
				}
			}
			_, err := d.Server.Exec(ctx, q)
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("the role crew ran %q: %v", q, err)
			}
		}
	}
}

// The server makes the month partitions of events ahead, with the one function it may call for that.
func TestEventsGoIntoMonthPartitions(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	future := time.Now().AddDate(0, 5, 0)
	insert := func() error {
		_, err := d.Server.Exec(ctx, `INSERT INTO events (conversation, seq, at, type)
			VALUES (gen_random_uuid(), 1, $1, 'status')`, future)
		return err
	}
	if err := insert(); err == nil {
		t.Fatal("an event five months ahead was stored before its partition existed")
	}
	var name string
	if err := d.Server.QueryRow(ctx, "SELECT crew_ensure_events_month($1)", future).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "events_"+future.Format("2006_01")) {
		t.Fatalf("partition %q", name)
	}
	if err := insert(); err != nil {
		t.Fatalf("after its partition was made: %v", err)
	}
	// Twice is harmless.
	if err := d.Server.QueryRow(ctx, "SELECT crew_ensure_events_month($1)", future).Scan(&name); err != nil {
		t.Fatal(err)
	}
	// The current month and the next exist from the first step. The next is counted from the first of
	// this month: AddDate(0, 1, 0) on the 31st of October is the 1st of December (BUG-029).
	now := time.Now().UTC()
	for _, m := range []time.Time{now, time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)} {
		var ok bool
		if err := d.Server.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "events_"+m.Format("2006_01")).Scan(&ok); err != nil || !ok {
			t.Errorf("no partition for %s", m.Format("2006-01"))
		}
	}
}

// At the switch of an update every new step applies in one transaction (16.4): a step that fails
// after one that worked leaves neither, and the schema is where it was.
func TestTheStepsOfASwitchApplyAllOrNone(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	conn := d.ConnectAs(t, db.RoleMigrate)
	steps, _ := db.Steps()
	good := db.Step{Number: len(steps) + 1, Name: "9998_good.sql", SQL: "CREATE TABLE switched (x int);", OlderReads: true}
	bad := db.Step{Number: len(steps) + 2, Name: "9999_bad.sql", SQL: "SELECT no_such_function();"}
	applied, err := db.MigrateStepsAtOnce(ctx, conn, append(steps, good, bad))
	if err == nil || len(applied) != 0 || !strings.Contains(err.Error(), "9999_bad.sql") || !strings.Contains(err.Error(), "no step was applied") {
		t.Fatalf("applied %v, %v", applied, err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('switched') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("the step before the failed one stayed (%v)", err)
	}
	if n, _ := db.Applied(ctx, conn); n != len(steps) {
		t.Fatalf("Applied = %d, want %d", n, len(steps))
	}
	applied, err = db.MigrateStepsAtOnce(ctx, conn, append(steps, good))
	if err != nil || len(applied) != 1 || applied[0] != "9998_good.sql" {
		t.Fatalf("applied %v, %v", applied, err)
	}
}

// Each step from FirstStepThatSays on says whether the version before it reads the database after it
// (16.6), and ReadBy is the last one that does not.
func TestEachNewStepSaysWhetherOlderVersionsReadIt(t *testing.T) {
	steps, err := db.Steps()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Number >= db.FirstStepThatSays && !strings.Contains(s.SQL, "-- older versions read: ") {
			t.Fatalf("%s does not say whether older versions read it", s.Name)
		}
	}
	got := db.ReadBy([]db.Step{{Number: 1, OlderReads: true}, {Number: 2, OlderReads: false}, {Number: 3, OlderReads: true}})
	if got != 2 {
		t.Fatalf("ReadBy = %d, want 2", got)
	}
	if db.ReadBy([]db.Step{{Number: 1, OlderReads: true}}) != 0 {
		t.Fatal("a schema every version reads has ReadBy 0")
	}
}

// No non-test Go file under internal/ or cmd/ drops the error returned by .Scan( (register row 52,
// CREW-39).
func TestNoProductionGoFileDropsAScanError(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	isScanCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Scan"
	}
	var dropped []string
	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				switch s := n.(type) {
				case *ast.ExprStmt:
					if isScanCall(s.X) {
						pos := fset.Position(s.Pos())
						dropped = append(dropped, fmt.Sprintf("%s:%d", rel, pos.Line))
					}
				case *ast.AssignStmt:
					if len(s.Lhs) == 1 && len(s.Rhs) == 1 && isScanCall(s.Rhs[0]) {
						if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
							pos := fset.Position(s.Pos())
							dropped = append(dropped, fmt.Sprintf("%s:%d", rel, pos.Line))
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(dropped) != 0 {
		t.Fatalf("non-test Go files dropping .Scan( errors: %v", dropped)
	}
}
