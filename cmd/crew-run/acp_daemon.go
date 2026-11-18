package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// acpSocketPath returns the Unix socket path where `crew-run acp-daemon` listens inside the project
// microVM (design 12.2, 12.4, since 1.37). It uses `/run/crew/acp.sock` when `/run/crew` is
// writable by `agent`, and always links or falls back to `~/.crew/acp.sock`.
func acpSocketPath(home string) string {
	if custom := strings.TrimSpace(os.Getenv("CREW_ACP_SOCK")); custom != "" {
		return custom
	}
	fallback := filepath.Join(home, runspec.Dir, "acp.sock")
	if info, err := os.Stat("/run/crew"); err == nil && info.IsDir() {
		testFile := "/run/crew/.probe"
		if f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Close()
			os.Remove(testFile)
			return "/run/crew/acp.sock"
		}
	}
	return fallback
}

func acpDaemon(e env, args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sockPath := acpSocketPath(e.Home)
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		sockPath = strings.TrimSpace(args[0])
	}
	if err := runACPDaemon(ctx, e, sockPath, stderr); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "crew-run acp-daemon:", err)
		return 1
	}
	fmt.Fprintln(stdout, "crew-run acp-daemon: stopped")
	return 0
}

type acpMux struct {
	env    env
	stderr io.Writer

	writeMu    sync.Mutex
	childStdin io.WriteCloser
	initResult json.RawMessage
	readyCh    chan struct{}

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]muxPending
	conns   map[*muxConn]struct{}
	bySess  map[string]map[*muxConn]struct{}
	sessKey map[string]string
}

type muxPending struct {
	conn     *muxConn
	clientID json.RawMessage
	method   string
}

type muxConn struct {
	c      net.Conn
	mu     sync.Mutex
	authed map[string]bool
}

func (mc *muxConn) writeLine(line []byte) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	_ = mc.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := mc.c.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func runACPDaemon(ctx context.Context, e env, sockPath string, stderr io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	defer func() {
		ln.Close()
		_ = os.Remove(sockPath)
	}()
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return err
	}
	homeSock := filepath.Join(e.Home, runspec.Dir, "acp.sock")
	if homeSock != sockPath {
		_ = os.MkdirAll(filepath.Dir(homeSock), 0o700)
		_ = os.Remove(homeSock)
		_ = os.Symlink(sockPath, homeSock)
	}

	m := &acpMux{
		env:     e,
		stderr:  stderr,
		readyCh: make(chan struct{}),
		pending: make(map[int64]muxPending),
		conns:   make(map[*muxConn]struct{}),
		bySess:  make(map[string]map[*muxConn]struct{}),
		sessKey: make(map[string]string),
	}

	go m.superviseChild(ctx)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mc := &muxConn{c: c, authed: make(map[string]bool)}
		m.mu.Lock()
		m.conns[mc] = struct{}{}
		m.mu.Unlock()
		go m.serveConn(ctx, mc)
	}
}

func (m *acpMux) superviseChild(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		first = false
		_ = m.runChildOnce(ctx)
	}
}

func (m *acpMux) runChildOnce(ctx context.Context) error {
	argv := cli.ACPCommand()
	if custom := strings.TrimSpace(os.Getenv("CREW_ACP_CMD")); custom != "" {
		argv = []string{"sh", "-c", custom}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = m.env.Home
	cmd.Env = cleanEnv(m.env.Home)
	cmd.Stderr = m.stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	m.writeMu.Lock()
	m.childStdin = stdin
	m.writeMu.Unlock()

	initID := m.nextID.Add(1)
	initCh := make(chan json.RawMessage, 1)
	go func() {
		req, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      initID,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion":    1,
				"clientCapabilities": map[string]any{},
			},
		})
		_ = m.writeToChild(req)
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		m.dispatchChildLine(line, initID, initCh)
	}
	_ = stdin.Close()
	werr := cmd.Wait()
	m.failPendingOnChildExit()
	return werr
}

func (m *acpMux) failPendingOnChildExit() {
	m.mu.Lock()
	pending := m.pending
	m.pending = make(map[int64]muxPending)
	m.bySess = make(map[string]map[*muxConn]struct{})
	m.mu.Unlock()
	for _, pend := range pending {
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      pend.clientID,
			"error": map[string]any{
				"code":    -32000,
				"message": "acp child process exited",
			},
		})
		_ = pend.conn.writeLine(resp)
	}
}

func (m *acpMux) writeToChild(line []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if m.childStdin == nil {
		return errors.New("acp child stdin is closed")
	}
	_, err := m.childStdin.Write(append(line, '\n'))
	return err
}

func (m *acpMux) dispatchChildLine(line []byte, initID int64, initCh chan<- json.RawMessage) {
	var env struct {
		ID     json.RawMessage `json:"id,omitempty"`
		Method string          `json:"method,omitempty"`
		Params json.RawMessage `json:"params,omitempty"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  json.RawMessage `json:"error,omitempty"`
	}
	if json.Unmarshal(line, &env) != nil {
		return
	}

	hasID := len(env.ID) > 0 && string(env.ID) != "null"
	if hasID && env.Method != "" {
		var p struct {
			SessionID string `json:"sessionId"`
			Options   []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(env.Params, &p)
		m.mu.Lock()
		var target *muxConn
		if subs := m.bySess[p.SessionID]; len(subs) > 0 {
			for mc := range subs {
				target = mc
				break
			}
		} else if p.SessionID == "" && len(m.conns) == 1 {
			for mc := range m.conns {
				target = mc
			}
		}
		m.mu.Unlock()
		if target != nil {
			_ = target.writeLine(line)
			return
		}
		if env.Method == "session/request_permission" {
			opt := "reject_once"
			for _, o := range p.Options {
				if strings.HasPrefix(o.Kind, "reject") && o.OptionID != "" {
					opt = o.OptionID
					break
				}
			}
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result": map[string]any{
					"outcome": map[string]any{
						"outcome":  "selected",
						"optionId": opt,
					},
				},
			})
			_ = m.writeToChild(resp)
		}
		return
	}

	if hasID {
		var numID int64
		if json.Unmarshal(env.ID, &numID) != nil {
			return
		}
		if numID == initID {
			m.mu.Lock()
			if len(m.initResult) == 0 {
				m.initResult = env.Result
				close(m.readyCh)
			}
			m.mu.Unlock()
			select {
			case initCh <- env.Result:
			default:
			}
			return
		}

		m.mu.Lock()
		pend, ok := m.pending[numID]
		if ok {
			delete(m.pending, numID)
			if pend.method == "session/new" || pend.method == "session/load" {
				var sr struct {
					SessionID string `json:"sessionId"`
				}
				if json.Unmarshal(env.Result, &sr) == nil && sr.SessionID != "" {
					if m.bySess[sr.SessionID] == nil {
						m.bySess[sr.SessionID] = make(map[*muxConn]struct{})
					}
					m.bySess[sr.SessionID][pend.conn] = struct{}{}
					pend.conn.authed[sr.SessionID] = true
				}
			}
		}
		m.mu.Unlock()
		if !ok {
			return
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      pend.clientID,
		}
		if len(env.Error) > 0 && string(env.Error) != "null" {
			resp["error"] = env.Error
		} else {
			resp["result"] = env.Result
		}
		out, _ := json.Marshal(resp)
		_ = pend.conn.writeLine(out)
		return
	}

	if env.Method == "session/update" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(env.Params, &p)
		if p.SessionID == "" {
			return
		}
		m.mu.Lock()
		var targets []*muxConn
		for mc := range m.bySess[p.SessionID] {
			targets = append(targets, mc)
		}
		m.mu.Unlock()
		for _, mc := range targets {
			_ = mc.writeLine(line)
		}
	}
}

func (m *acpMux) serveConn(ctx context.Context, mc *muxConn) {
	defer func() {
		mc.c.Close()
		m.mu.Lock()
		delete(m.conns, mc)
		for _, set := range m.bySess {
			delete(set, mc)
		}
		m.mu.Unlock()
	}()

	sc := bufio.NewScanner(mc.c)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		raw := append([]byte(nil), sc.Bytes()...)
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &req) != nil {
			continue
		}
		if req.Method == "" {
			if len(req.ID) > 0 {
				_ = m.writeToChild(raw)
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-m.readyCh:
		}

		if req.Method == "initialize" {
			m.mu.Lock()
			initRes := m.initResult
			m.mu.Unlock()
			out, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  initRes,
			})
			_ = mc.writeLine(out)
			continue
		}

		if req.Method == "crew/authSession" {
			var ap struct {
				SessionID string `json:"sessionId"`
				CtrlKey   string `json:"ctrlKey"`
			}
			_ = json.Unmarshal(req.Params, &ap)
			m.mu.Lock()
			existing := m.sessKey[ap.SessionID]
			ok := false
			if ap.SessionID != "" && ap.CtrlKey != "" {
				if existing == "" {
					// Only the connection that created/owns the session (or a pre-load registration when no
					// connection holds the session yet) may register the initial control key.
					if mc.authed[ap.SessionID] || len(m.bySess[ap.SessionID]) == 0 {
						m.sessKey[ap.SessionID] = ap.CtrlKey
						mc.authed[ap.SessionID] = true
						ok = true
					}
				} else if subtle.ConstantTimeCompare([]byte(existing), []byte(ap.CtrlKey)) == 1 {
					mc.authed[ap.SessionID] = true
					ok = true
				}
			}
			m.mu.Unlock()
			if !ok {
				errOut, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"code": -32001, "message": "invalid session control key"},
				})
				_ = mc.writeLine(errOut)
				continue
			}
			out, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"ok": true},
			})
			_ = mc.writeLine(out)
			continue
		}

		var sp struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(req.Params, &sp) == nil && sp.SessionID != "" {
			m.mu.Lock()
			want := m.sessKey[sp.SessionID]
			if req.Method == "session/load" && want == "" && len(m.bySess[sp.SessionID]) == 0 {
				mc.authed[sp.SessionID] = true
			}
			authed := mc.authed[sp.SessionID]
			if authed {
				if m.bySess[sp.SessionID] == nil {
					m.bySess[sp.SessionID] = make(map[*muxConn]struct{})
				}
				m.bySess[sp.SessionID][mc] = struct{}{}
				if req.Method == "session/close" {
					delete(m.sessKey, sp.SessionID)
				}
			}
			m.mu.Unlock()
			if !authed {
				errOut, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"code": -32001, "message": "unauthorized session control"},
				})
				_ = mc.writeLine(errOut)
				continue
			}
		}

		dID := m.nextID.Add(1)
		m.mu.Lock()
		m.pending[dID] = muxPending{conn: mc, clientID: req.ID, method: req.Method}
		m.mu.Unlock()

		fwd, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      dID,
			"method":  req.Method,
			"params":  req.Params,
		})
		if err := m.writeToChild(fwd); err != nil {
			m.mu.Lock()
			delete(m.pending, dID)
			m.mu.Unlock()
			errOut, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32000, "message": err.Error()},
			})
			_ = mc.writeLine(errOut)
		}
	}
}
