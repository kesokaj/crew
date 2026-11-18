package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/runspec"
)

// An exit.json and a spool an agent wrote into a later turn's folder are cleared before that turn
// starts: the turn ends by the exit.json crew-run writes, not by the agent's (12.4, since 1.01).
func TestKataClearsWhatAnAgentPutInALaterTurnsFolder(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	later := filepath.Join(home, runspec.TurnDir(2))
	if err := os.MkdirAll(filepath.Join(later, runspec.StderrFile), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExit(t, filepath.Join(later, runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","detail":"planted"}`))
	appendTo(t, filepath.Join(later, runspec.EventsFile), `{"type":"result","result":"planted"}`+"\n")
	writeExit(t, filepath.Join(home, runspec.TurnDir(1), runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","conversation":"s1"}`))
	for e := next(t, k.Events("r1")); e.End == nil; e = next(t, k.Events("r1")) {
	}
	if err := k.Continue(context.Background(), "r1", "", "more"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{runspec.ExitFile, runspec.EventsFile, runspec.StderrFile} {
		if _, err := os.Lstat(filepath.Join(later, f)); !os.IsNotExist(err) {
			t.Fatalf("%s is still in the turn's folder: %v", f, err)
		}
	}
	select {
	case e := <-k.Events("r1"):
		t.Fatalf("the turn gave an event before crew-run wrote anything: %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
	writeExit(t, filepath.Join(later, runspec.ExitFile), []byte(`{"exit":1,"reason":"error","detail":"crew-run's own"}`))
	e := next(t, k.Events("r1"))
	for ; e.End == nil; e = next(t, k.Events("r1")) {
	}
	if e.End.Detail != "crew-run's own" {
		t.Fatalf("the turn ended with %q", e.End.Detail)
	}
}

// A later turn's folder that leads out of the home is not cleared through: the turn does not start,
// nothing outside the home is removed, and the turn is taken back, so the next start is that turn.
func TestKataClearsNothingOutsideTheHome(t *testing.T) {
	k, sb, _ := kata(t)
	h, _ := k.StartRun(context.Background(), spec())
	home := sb.Home(h.Sandbox)
	outside := t.TempDir()
	keep := filepath.Join(outside, runspec.ExitFile)
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, runspec.TurnDir(2))); err != nil {
		t.Fatal(err)
	}
	writeExit(t, filepath.Join(home, runspec.TurnDir(1), runspec.ExitFile), []byte(`{"exit":0,"reason":"ok","conversation":"s1"}`))
	for e := next(t, k.Events("r1")); e.End == nil; e = next(t, k.Events("r1")) {
	}
	if err := k.Continue(context.Background(), "r1", "", "more"); err == nil {
		t.Fatal("a turn whose folder leads out of the home started")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a file outside the home was removed: %v", err)
	}
	if err := os.Remove(filepath.Join(home, runspec.TurnDir(2))); err != nil {
		t.Fatal(err)
	}
	if err := k.Continue(context.Background(), "r1", "", "again"); err != nil {
		t.Fatal(err)
	}
	if p, _ := os.ReadFile(filepath.Join(home, runspec.TurnDir(2), runspec.PromptFile)); string(p) != "again" {
		t.Fatalf("the turn after a failed start: %q", p)
	}
}
