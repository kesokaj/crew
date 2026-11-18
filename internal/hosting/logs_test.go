package hosting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The logs (plan M19 D17): what the sandbox printed is copied once, by Docker's stamps; files older
// than hosting.log_days go; a read gives the newest lines, oldest first.
func TestTheLogsAreKeptLogDays(t *testing.T) {
	h := newHosted(t, map[string]any{"log_days": 2})
	h.deploy(asked)
	sb := SandboxID(h.id, 1)
	dir := filepath.Join(h.s.projectDir(h.id), "logs")
	old := time.Now().UTC().AddDate(0, 0, -2).Format(logDay) + ".log"
	keep := time.Now().UTC().AddDate(0, 0, -1).Format(logDay) + ".log"
	os.WriteFile(filepath.Join(dir, old), []byte("old\n"), 0o640)
	os.WriteFile(filepath.Join(dir, keep), []byte("yesterday\n"), 0o640)
	now := time.Now().UTC()
	h.rt.logs = now.Add(-time.Second).Format(time.RFC3339Nano) + " listening\n" + now.Format(time.RFC3339Nano) + " GET /healthz 200\n"
	for range 2 {
		if err := h.s.LogsRound(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatal("a file older than log_days was kept")
	}
	all := h.logs()
	if strings.Count(all, "GET /healthz 200") != 1 || !strings.Contains(all, "yesterday") {
		t.Fatalf("the log:\n%s", all)
	}
	lines, err := h.s.Logs(h.id, 2)
	if err != nil || len(lines) != 2 || !strings.HasSuffix(lines[1], "GET /healthz 200") || !strings.HasSuffix(lines[0], "listening") {
		t.Fatalf("the newest lines: %q %v", lines, err)
	}
	if lines, _ := h.s.Logs(h.id, 2000); lines[0] != "yesterday" {
		t.Fatalf("oldest first: %q", lines[0])
	}
	_ = sb
}

// A day's file stops at MaxLogDay, with a line that says so.
func TestADaysLogIsBounded(t *testing.T) {
	h := newHosted(t, nil)
	dir := filepath.Join(h.s.projectDir(h.id), "logs")
	os.MkdirAll(dir, 0o750)
	day := filepath.Join(dir, time.Now().UTC().Format(logDay)+".log")
	f, _ := os.Create(day)
	f.Truncate(MaxLogDay - 12)
	f.WriteAt([]byte("\nlast line\n"), MaxLogDay-12)
	f.Close()
	h.s.logLine(h.id, "%s", strings.Repeat("x", 100))
	h.s.logLine(h.id, "more")
	fi, _ := os.Stat(day)
	if fi.Size() > MaxLogDay+200 {
		t.Fatalf("the day's file is %d bytes", fi.Size())
	}
	tail, _ := readTail(day, 400)
	if !strings.Contains(string(tail), "the rest of the day is not kept") || strings.Contains(string(tail), "more") {
		t.Fatalf("%q", tail)
	}
}

// After a restart of Crew the copy goes on from the last line copied of the sandbox, not from the
// start of its log, also on a new day's file (review of M19, finding 8, and round 2).
func TestTheLogsAreNotCopiedTwiceAfterARestart(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	now := time.Now().UTC().Truncate(time.Second)
	first := now.Add(-2*time.Second).Format(time.RFC3339Nano) + " first\n"
	h.rt.logs = first
	h.s.LogsRound(h.ctx)
	if !strings.Contains(h.logs(), " first") {
		t.Fatalf("the log:\n%s", h.logs())
	}
	// Played: the day changed, so today's file is a new one, and Crew restarted.
	day := filepath.Join(h.s.projectDir(h.id), "logs", now.Format(logDay)+".log")
	os.Rename(day, filepath.Join(h.s.projectDir(h.id), "logs", now.AddDate(0, 0, -1).Format(logDay)+".log"))
	h.s.logsAt = nil
	h.rt.logs = first + now.Format(time.RFC3339Nano) + " second\n"
	h.s.LogsRound(h.ctx)
	if all := h.logs(); strings.Count(all, " first") != 1 || strings.Count(all, " second") != 1 {
		t.Fatalf("the log:\n%s", all)
	}
}

// A new deploy's sandbox has its log copied from its start, also the lines it printed before the
// last line of the sandbox it replaced (review of M19, round 2).
func TestANewSandboxsLogIsCopiedFromItsStart(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	now := time.Now().UTC().Truncate(time.Second)
	h.rt.logsOf = map[string]string{SandboxID(h.id, 1): now.Format(time.RFC3339Nano) + " old last\n"}
	h.s.LogsRound(h.ctx)
	h.deploy(asked)
	h.rt.logsOf[SandboxID(h.id, 2)] = now.Add(-30*time.Second).Format(time.RFC3339Nano) + " new started\n"
	h.s.logsAt = nil // and after a restart of Crew as well
	h.s.LogsRound(h.ctx)
	if all := h.logs(); !strings.Contains(all, " old last") || !strings.Contains(all, " new started") {
		t.Fatalf("the log:\n%s", all)
	}
}
