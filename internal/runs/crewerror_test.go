package runs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
)

// lockedLog keeps every line a driver logs, from its goroutines too.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func logTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// endedByCrew asserts a run Crew's own error ended: its end detail is the fixed sentence, which the
// lead reads when it looks again, and the error is only in the server's log, with the run (13.9).
func endedByCrew(t *testing.T, k *kit, log *lockedLog, run, reason, detail, crewErr string) {
	t.Helper()
	v := k.wait(run, "ended")
	if v.StopReason != reason || v.StopDetail != detail {
		t.Fatalf("ended %q, %q; not %q, %q", v.StopReason, v.StopDetail, reason, detail)
	}
	if n := k.count(`SELECT count(*) FROM runs WHERE stop_detail LIKE '%' || $1 || '%'`, crewErr); n != 0 {
		t.Fatalf("%d runs hold the error's text", n)
	}
	for _, line := range strings.Split(log.String(), "\n") {
		if strings.Contains(line, crewErr) && strings.Contains(line, run) {
			return
		}
	}
	t.Fatalf("no line of the log names the run and the error: %s", log.String())
}

// A microVM that does not start ends its run with the fixed sentence, not Docker's error (13.9).
func TestARunWhoseMicroVMDidNotStartEndsWithAFixedDetail(t *testing.T) {
	k := newKit(t)
	var log lockedLog
	k.d.Log = logTo(&log)
	const crewErr = "/var/lib/crew/sandboxes/t41-run: no space left on device"
	k.f.StartErr = errors.New("creating the sandbox: " + crewErr)
	run := k.start(StartRequest{})
	endedByCrew(t, k, &log, run, ReasonSandbox, NoStart, crewErr)
}

// A run whose subtask cannot be read after a restart ends with the fixed sentence (13.9).
func TestARunWhoseSubtaskCannotBeReadAfterARestartEndsWithAFixedDetail(t *testing.T) {
	k := newKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run := k.start(StartRequest{})
	k.wait(run, "working")
	k.d.Close()
	// scopeOf refuses a subtask of the improver that is not the improver's.
	if _, err := k.db.Server.Exec(context.Background(), `UPDATE subtasks SET scope = 'improver'
		WHERE id = (SELECT subtask FROM runs WHERE id = $1)`, run); err != nil {
		t.Fatal(err)
	}
	var log lockedLog
	k.d = k.driver()
	k.d.Log = logTo(&log)
	k.d.Reconcile(context.Background())
	endedByCrew(t, k, &log, run, ReasonRestart, NoScopeAfterRestart, "is the improver's and names a project or a draft")
}

// A run the harness cannot follow again after a restart ends with the fixed sentence (13.9).
func TestARunThatCannotBeFollowedAgainEndsWithAFixedDetail(t *testing.T) {
	k := newKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run := k.start(StartRequest{})
	k.wait(run, "working")
	k.d.Close()
	k.f.Kill(run)
	var log lockedLog
	k.d = k.driver()
	k.d.Log = logTo(&log)
	k.d.Reconcile(context.Background())
	endedByCrew(t, k, &log, run, ReasonRestart, NotFollowedAfterRestart, harness.ErrNoRun.Error())
}

// A run whose microVM died right as it was followed again after a restart is stopped and ends with
// the fixed sentence (13.9).
func TestARunWhoseMicroVMDiedAfterFollowEndsWithAFixedDetail(t *testing.T) {
	k := newKit(t)
	k.play(harness.Script{Recording: agyRun, Hold: true})
	run := k.start(StartRequest{})
	k.wait(run, "working")
	k.d.Close()
	k.f.Dead(run)
	var log lockedLog
	k.d = k.driver()
	k.d.Log = logTo(&log)
	k.d.Reconcile(context.Background())
	endedByCrew(t, k, &log, run, ReasonRestart, NotFollowedAfterRestart, "its microVM does not run")
	_, stopped, _, _ := k.f.Calls()
	if len(stopped) != 1 || stopped[0] != run {
		t.Fatalf("stopped = %v, want [%s]", stopped, run)
	}
}
