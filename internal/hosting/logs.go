package hosting

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// LogsEvery is how often what a hosted project printed is copied into its log files (plan M19 D17).
var LogsEvery = time.Minute

// logDay is the name of a day's log file.
const logDay = "2006-01-02"

// MaxLogDay bounds one day's log file: a project that prints more loses the rest of the day, with one
// line that says so, rather than the machine's disk (plan M19 D17).
const MaxLogDay = 256 << 20

// maxLogTail is how much of the end of a day's file one read of the logs takes.
const maxLogTail = 4 << 20

// logLine writes one of Crew's own lines into a hosted project's log (plan M19 D17): a start, a
// stop, a restart, a deploy's end.
func (s *Service) logLine(projectID, format string, args ...any) {
	now := time.Now().UTC()
	s.appendLog(projectID, now, fmt.Sprintf("%s crew: %s\n", now.Format(time.RFC3339Nano), fmt.Sprintf(format, args...)))
}

func (s *Service) appendLog(projectID string, day time.Time, text string) {
	if s.Dir == "" || text == "" {
		return
	}
	dir := filepath.Join(s.projectDir(projectID), "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, day.UTC().Format(logDay)+".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() >= MaxLogDay {
		return
	}
	if fi.Size()+int64(len(text)) > MaxLogDay {
		text = fmt.Sprintf("%s crew: the log of the day reached %d MiB; the rest of the day is not kept\n", time.Now().UTC().Format(time.RFC3339Nano), MaxLogDay>>20)
	}
	_, _ = f.WriteString(text)
}

// RunLogs copies the logs every LogsEvery until ctx ends.
func (s *Service) RunLogs(ctx context.Context) {
	t := time.NewTicker(LogsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.LogsRound(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("copying the hosted projects' logs", "error", err)
		}
	}
}

// LogsRound appends what each serving sandbox printed since the last round to its project's file of
// the day, and removes the files older than hosting.log_days.
func (s *Service) LogsRound(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.name, p.key, coalesce(h.sandbox, '') FROM hosting h JOIN projects p ON p.id = h.project`)
	if err != nil {
		return err
	}
	type row struct{ id, name, key, sandbox string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.key, &r.sandbox); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range all {
		if r.sandbox != "" {
			s.copyLogs(ctx, r.id, r.sandbox)
		}
		st, err := settingsOf(ctx, s.Pool, r.id, r.name, r.key)
		if err != nil {
			return err
		}
		s.pruneLogs(r.id, st.LogDays)
	}
	return nil
}

// copyLogs appends the lines a sandbox printed after the last one copied, which Docker stamps.
func (s *Service) copyLogs(ctx context.Context, projectID, sandbox string) {
	s.logsMu.Lock()
	if s.logsAt == nil {
		s.logsAt = map[string]time.Time{}
	}
	since, known := s.logsAt[sandbox]
	s.logsMu.Unlock()
	// After a restart of Crew, the copy goes on from the last line copied of the same sandbox; a new
	// sandbox's log is copied from its start (review of M19, finding 8, and round 2).
	if !known {
		since = s.copiedUntil(projectID, sandbox)
	}
	out, err := s.Runtime.HostedLogs(ctx, sandbox, since)
	if err != nil {
		return
	}
	last := since
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		stamp, _, _ := strings.Cut(line, " ")
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !at.After(since) {
			continue
		}
		last = at
		b.WriteString(line + "\n")
	}
	s.appendLog(projectID, time.Now(), b.String())
	if last.After(since) {
		s.recordCopied(projectID, sandbox, last)
	}
	s.logsMu.Lock()
	s.logsAt[sandbox] = last
	s.logsMu.Unlock()
}

// copiedName is the file in a project's logs that names the sandbox and the stamp of the last line
// copied from it. Its name is no day's, so the reads and the pruning of the logs pass it by.
const copiedName = ".copied"

// copiedUntil is the stamp of the last line copied from the sandbox, or the zero time when the last
// copy was of another sandbox or there was none.
func (s *Service) copiedUntil(projectID, sandbox string) time.Time {
	b, err := os.ReadFile(filepath.Join(s.projectDir(projectID), "logs", copiedName))
	if err != nil {
		return time.Time{}
	}
	id, stamp, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if id != sandbox || err != nil {
		return time.Time{}
	}
	return at
}

func (s *Service) recordCopied(projectID, sandbox string, at time.Time) {
	if s.Dir == "" {
		return
	}
	dir := filepath.Join(s.projectDir(projectID), "logs")
	tmp := filepath.Join(dir, copiedName+".new")
	if err := os.WriteFile(tmp, []byte(sandbox+" "+at.Format(time.RFC3339Nano)+"\n"), 0o640); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, copiedName))
}

// pruneLogs removes a project's log files of the days before the last days.
func (s *Service) pruneLogs(projectID string, days int) {
	if days <= 0 || s.Dir == "" {
		return
	}
	dir := filepath.Join(s.projectDir(projectID), "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	oldest := time.Now().UTC().AddDate(0, 0, -(days - 1)).Format(logDay)
	for _, e := range entries {
		day, ok := strings.CutSuffix(e.Name(), ".log")
		if _, err := time.Parse(logDay, day); ok && err == nil && day < oldest {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// MaxLogLines bounds what one read of a project's log gives.
const MaxLogLines = 2000

// Logs are the newest n lines of a project's log, oldest first.
func (s *Service) Logs(projectID string, n int) ([]string, error) {
	n = min(max(n, 1), MaxLogLines)
	dir := filepath.Join(s.projectDir(projectID), "logs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if day, ok := strings.CutSuffix(e.Name(), ".log"); ok && e.Type().IsRegular() {
			if _, err := time.Parse(logDay, day); err == nil {
				days = append(days, e.Name())
			}
		}
	}
	slices.Sort(days)
	var out []string
	for i := len(days) - 1; i >= 0 && len(out) < n; i-- {
		raw, err := readTail(filepath.Join(dir, days[i]), maxLogTail)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			continue
		}
		if len(lines) > n-len(out) {
			lines = lines[len(lines)-(n-len(out)):]
		}
		out = append(lines, out...)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// readTail reads at most the last max bytes of a file, from the first whole line in them.
func readTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	from := max(fi.Size()-limit, 0)
	buf := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
		return nil, err
	}
	if from > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}
