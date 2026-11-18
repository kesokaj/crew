package projterm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// The close codes a terminal's page reads: the shell ended, or another page took the terminal.
const (
	CloseEnded    = 4000
	CloseReplaced = 4001
)

// target is the microVM a terminal attaches to.
type target struct {
	projectID, project string
	sandbox            string
	run                string // the run's id, when it is a jump into a run's microVM
	ticket             string
	session            string // the tmux session, so a reconnect finds what was typed and printed
}

// Attach serves a terminal over a WebSocket: the project terminal, or with run, the microVM of that
// running run of the project (13.11). host is Crew's own address: a page of another address is
// refused before anything starts.
func (s *Service) Attach(w http.ResponseWriter, r *http.Request, host, ref, run, who string) {
	t, err := s.target(r.Context(), ref, run)
	if err != nil {
		code := http.StatusConflict
		var e *Error
		if !errors.As(err, &e) && !errors.Is(err, ErrNotOpen) {
			s.Log.Error("attaching a terminal", "error", err)
			code = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), code)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{host}})
	if err != nil {
		return // Accept has answered
	}
	defer conn.CloseNow()
	key := t.sandbox + "/" + t.session
	if !s.claim(key, conn) {
		return
	}
	defer s.release(key, conn)
	if t.run != "" {
		// A jump into a run's microVM is an entry in its ticket's history.
		err := pgx.BeginFunc(r.Context(), s.Pool, func(tx pgx.Tx) error {
			return db.RecordProjectChange(r.Context(), tx, t.projectID, who, "tickets", t.ticket, nil,
				map[string]any{"terminal": "opened inside run " + t.run})
		})
		if err != nil {
			s.Log.Warn("recording a jump into a run", "run", t.run, "error", err)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cols, rows := dim(r.URL.Query().Get("cols"), 100), dim(r.URL.Query().Get("rows"), 30)
	st, err := s.Runtime.Interactive(ctx, orchestrator.SandboxName(t.sandbox), orchestrator.ExecSpec{
		Cmd:  []string{"tmux", "new-session", "-A", "-s", t.session, "-c", Workspace, "bash", "-l"},
		User: orchestrator.GuestUser,
		Env:  []string{"HOME=/home/" + orchestrator.GuestUser, "TERM=xterm-256color", "LANG=C.UTF-8"},
	}, cols, rows)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "the terminal did not start")
		return
	}
	defer st.Close()
	log := &logWriter{s: s, t: t}
	defer log.flush()
	if t.run == "" {
		// Edit mode lasts while a page is on the terminal: with none, nothing pushes in its name.
		defer s.afterDetach(t, key, conn)
	}
	go func() {
		<-ctx.Done()
		st.Close()
	}()
	// From the page to the terminal.
	go func() {
		defer cancel()
		var touched time.Time
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				// Logged before the shell has it, so what was typed comes before what it printed.
				log.add("in", data)
				if _, err := st.Write(data); err != nil {
					return
				}
				if t.run == "" && time.Since(touched) > 10*time.Second {
					touched = time.Now()
					s.Pool.Exec(ctx, `UPDATE project_terminals SET input_at = now() WHERE project = $1 AND sandbox = $2`, t.projectID, t.sandbox)
				}
				continue
			}
			var m struct {
				Type       string
				Cols, Rows uint
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch {
			case m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 500:
				st.Resize(ctx, m.Cols, m.Rows)
			case m.Type == "ping":
				conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`))
			}
		}
	}()
	// From the terminal to the page, until the shell ends or the page goes.
	buf := make([]byte, 32<<10)
	for {
		n, err := st.Read(buf)
		if n > 0 {
			log.add("out", buf[:n])
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}
	if ctx.Err() == nil {
		conn.Close(CloseEnded, "the shell ended")
	}
}

func (s *Service) target(ctx context.Context, ref, run string) (target, error) {
	p, err := s.project(ctx, s.Pool, ref)
	if err != nil {
		return target{}, err
	}
	if run != "" {
		t := target{projectID: p.id, project: p.name, run: run, session: "crew-jump"}
		var state string
		var sandbox *string
		err := s.Pool.QueryRow(ctx, `SELECT r.state, r.sandbox, coalesce(pr.key || '-' || tk.number, '') FROM runs r
			JOIN subtasks st ON st.id = r.subtask JOIN projects pr ON pr.id = st.project LEFT JOIN tickets tk ON tk.id = st.ticket
			WHERE r.id::text = $1 AND st.project = $2`, run, p.id).Scan(&state, &sandbox, &t.ticket)
		if errors.Is(err, pgx.ErrNoRows) {
			return target{}, refuse("%s has no such run", p.name)
		}
		if err != nil {
			return target{}, err
		}
		if state == "ended" || sandbox == nil {
			return target{}, refuse("the run has ended, and its microVM is gone: open the project terminal instead")
		}
		t.sandbox = *sandbox
		return t, nil
	}
	t := target{projectID: p.id, project: p.name, session: "crew-terminal"}
	err = s.Pool.QueryRow(ctx, `SELECT x.sandbox FROM project_terminals x JOIN sandboxes b ON b.id = x.sandbox
		WHERE x.project = $1 AND b.removed_at IS NULL`, p.id).Scan(&t.sandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return target{}, ErrNotOpen
	}
	return t, err
}

// One page at a time on a terminal: two pages on one tmux session garble each other. The newer
// takes it.
var (
	attachMu sync.Mutex
	attached = map[string]*websocket.Conn{}
)

func (s *Service) claim(session string, c *websocket.Conn) bool {
	attachMu.Lock()
	defer attachMu.Unlock()
	if old, ok := attached[session]; ok {
		old.Close(CloseReplaced, "another page took this terminal")
	}
	attached[session] = c
	return true
}

func (s *Service) release(session string, c *websocket.Conn) {
	attachMu.Lock()
	defer attachMu.Unlock()
	if attached[session] == c {
		delete(attached, session)
	}
}

// afterDetach switches the project terminal back to diagnostics once no page is on it.
func (s *Service) afterDetach(t target, key string, mine *websocket.Conn) {
	attachMu.Lock()
	c, ok := attached[key]
	other := ok && c != mine
	attachMu.Unlock()
	if other {
		return
	}
	if _, err := s.SetMode(context.Background(), t.projectID, ModeDiagnostics, "crew"); err != nil && !errors.Is(err, ErrNotOpen) {
		s.Log.Warn("switching a terminal back to diagnostics", "project", t.project, "error", err)
	}
}

// LogPurpose is what a terminal's log is sealed for (credstore.Vault.Seal).
const LogPurpose = "terminal_log"

// MaxLog is the most of one attachment the log keeps; past it, one row says the rest was not kept.
const MaxLog = 16 << 20

func (s *Service) maxLog() int {
	if s.MaxLog > 0 {
		return s.MaxLog
	}
	return MaxLog
}

// logWriter keeps what was typed and printed in terminal_log, sealed, in chunks of at most a second
// or 16 KB, and at most MaxLog of one attachment.
type logWriter struct {
	s     *Service
	t     target
	mu    sync.Mutex
	dir   string
	buf   []byte
	last  time.Time
	kept  int
	ended bool
}

func (l *logWriter) add(dir string, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	if max := l.s.maxLog(); l.kept+len(l.buf)+len(data) > max {
		l.flushLocked()
		l.dir, l.buf = "out", []byte(fmt.Sprintf("\r\n[Crew keeps at most %d bytes of one terminal's log: the rest of it was not kept.]\r\n", max))
		l.flushLocked()
		l.ended = true
		return
	}
	if l.dir != dir || len(l.buf) > 16<<10 || time.Since(l.last) > time.Second {
		l.flushLocked()
		l.dir, l.last = dir, time.Now()
	}
	l.buf = append(l.buf, data...)
}

func (l *logWriter) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushLocked()
}

func (l *logWriter) flushLocked() {
	if len(l.buf) == 0 {
		return
	}
	var run any
	if l.t.run != "" {
		run = l.t.run
	}
	l.kept += len(l.buf)
	sealed, err := l.s.Seal(l.buf)
	l.buf = nil
	if err != nil {
		l.s.Log.Warn("sealing a terminal's log", "error", err)
		return
	}
	if _, err := l.s.Pool.Exec(context.Background(), `INSERT INTO terminal_log (project, sandbox, run, dir, data, sealed) VALUES ($1, $2, $3, $4, $5, true)`,
		l.t.projectID, l.t.sandbox, run, l.dir, sealed); err != nil {
		l.s.Log.Warn("keeping a terminal's log", "error", err)
	}
}

func dim(v string, def uint) uint {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 || n > 999 {
		return def
	}
	return uint(n)
}
