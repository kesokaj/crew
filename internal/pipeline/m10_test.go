package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
)

// A judge's answer is read only in its fixed form: one line for each criterion, by its number, and
// nothing else (13.9). Anything else is an error, never a verdict.
func TestVerdicts(t *testing.T) {
	judges := []board.CheckSpec{{Kind: "judge", Spec: "The page has an example."}, {Kind: "judge", Spec: "The page names the flag."}}
	r, err := Verdicts("2. NOT MET: no flag is named.\n1. MET: README.md shows a table.\n", judges)
	if err != nil {
		t.Fatal(err)
	}
	if !r[0].Passed || r[0].Name != "The page has an example." || r[0].Output != "README.md shows a table." || r[1].Passed {
		t.Fatalf("%+v", r)
	}
	if _, err := Verdicts("```\n1. MET: yes.\n2. MET: yes.\n```", judges); err != nil {
		t.Fatalf("a fenced answer: %v", err)
	}
	for name, answer := range map[string]string{
		"one line short":        "1. MET: yes.",
		"a line too many":       "1. MET: yes.\n2. MET: yes.\n3. MET: yes.",
		"a criterion twice":     "1. MET: yes.\n1. NOT MET: no.",
		"a number past the end": "1. MET: yes.\n3. MET: yes.",
		"no reason":             "1. MET:\n2. MET: yes.",
		"another word":          "1. PASS: yes.\n2. MET: yes.",
		"an introduction":       "Here is my answer.\n1. MET: yes.\n2. MET: yes.",
		"lower case":            "1. met: yes.\n2. MET: yes.",
		"nothing":               "",
	} {
		if _, err := Verdicts(answer, judges); err == nil {
			t.Errorf("%s: read as a verdict", name)
		}
	}
	long, _ := Verdicts("1. MET: "+strings.Repeat("x", 3000)+"\n2. MET: y", judges)
	if len(long[0].Output) > 1004 {
		t.Fatalf("a reason of %d bytes was kept whole", len(long[0].Output))
	}
}

// joined takes the project's and the ticket's locks before a subtask's row, in the board's order
// (7.11; the deadlock of M9): while another writer holds the ticket, it waits without holding the
// subtask's row, so the two can never wait on each other. Test debt named in M9, owed in M10 since
// parts.go changed.
func TestJoinedLocksTheTicketBeforeTheSubtask(t *testing.T) {
	tools, proj, c := toolsKit(t)
	ctx := context.Background()
	pool := tools.Pool
	var subID int64
	if err := pool.QueryRow(ctx, `INSERT INTO subtasks (scope, project, ticket, number, kind, role, state, title, created_by)
		VALUES ('ticket', $1, $2, 2, 'part', 'builder', 'done', 'A part', 'lead') RETURNING id`, proj.ID, c.Ticket).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	tk, err := tools.Board.Ticket(ctx, proj, "SF-1")
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Pool: pool, Board: tools.Board}
	j := &job{proj: proj}

	// Another writer of the board holds the ticket, as create_subtask does.
	hold, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(ctx)
	if err := board.LockForSubtasks(ctx, hold, c.Ticket); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.joined(ctx, j, tk, &sub{id: subID, number: 2}, "merged as abc") }()
	select {
	case err := <-done:
		t.Fatalf("joined went ahead while another writer held the ticket: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	// Meanwhile joined holds no lock of the subtask's row.
	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, perr := probe.Exec(ctx, `SELECT 1 FROM subtasks WHERE id = $1 FOR UPDATE NOWAIT`, subID)
	probe.Rollback(ctx)
	if perr != nil {
		hold.Rollback(ctx)
		<-done
		t.Fatalf("joined locked the subtask's row before the ticket: %v", perr)
	}
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var joined bool
	pool.QueryRow(ctx, `SELECT joined_at IS NOT NULL FROM subtasks WHERE id = $1`, subID).Scan(&joined)
	if !joined {
		t.Fatal("the part was not marked joined")
	}
}
