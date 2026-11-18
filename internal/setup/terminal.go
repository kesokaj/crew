package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/kesokaj/crew/internal/orchestrator"
)

// The terminal of Setup: one WebSocket per step. Binary frames carry the terminal's bytes both ways;
// text frames carry JSON: from the page {"type":"resize","cols","rows"}, and to it
// {"type":"status","text"} while the microVM starts and {"type":"done", Result} at the end.

// CloseDone is the close code after a step ended and its result was sent.
const CloseDone = 4000

// Terminal serves GET /api/setup/terminal?step=STEP&cols=C&rows=R. host is Crew's own address: a
// terminal connection must come from a page of it (20.3), which websocket.Accept checks against
// the Origin header.
func (s *Service) Terminal(host string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		step := r.URL.Query().Get("step")
		cols, rows := dim(r.URL.Query().Get("cols"), 100), dim(r.URL.Query().Get("rows"), 30)
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{host}})
		if err != nil {
			return // Accept has answered
		}
		defer conn.CloseNow()
		// ctx ends when the page goes away, when another page takes the step over, or on Stop.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		send := func(v any) {
			b, _ := json.Marshal(v)
			wctx, c := context.WithTimeout(ctx, 5*time.Second)
			defer c()
			conn.Write(wctx, websocket.MessageText, b)
		}
		done := func(ok bool, message string) {
			send(map[string]any{"type": "done", "step": step, "ok": ok, "message": message})
			conn.Close(CloseDone, "done")
		}

		// The page is read from the start, so a page that goes away while the microVM starts is
		// noticed then, and the start is given up. Frames wait here until the terminal runs.
		frames := make(chan frame, 64)
		go func() {
			defer cancel()
			defer close(frames)
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				select {
				case frames <- frame{typ, data}:
				case <-ctx.Done():
					return
				}
			}
		}()

		send(map[string]string{"type": "status", "text": "Starting the setup microVM…"})
		sess, gen, err := s.open(ctx, step, cancel)
		if err != nil {
			done(false, "the setup microVM did not start: "+err.Error())
			return
		}
		// Whatever happens below, this terminal's last word is about its own session only.
		defer s.end(sess.id, gen)
		home := s.Sandboxes.Home(sess.id)
		ex, err := s.Runtime.StartInteractive(ctx, orchestrator.SandboxName(sess.id), orchestrator.ExecSpec{
			Cmd:  []string{"crew-setup", step},
			User: orchestrator.GuestUser,
			Env:  []string{"HOME=/home/" + orchestrator.GuestUser, "TERM=xterm-256color", "LANG=C.UTF-8"},
		}, cols, rows)
		if err != nil {
			done(false, "the terminal did not start: "+err.Error())
			return
		}
		defer ex.Close()
		// A page that goes away ends the step: closing the stream ends the read below.
		go func() {
			<-ctx.Done()
			ex.Close()
		}()

		// From the page to the terminal.
		go func() {
			for f := range frames {
				if f.typ == websocket.MessageBinary {
					if _, err := ex.Write(f.data); err != nil {
						cancel()
						return
					}
					continue
				}
				var m struct {
					Type       string
					Cols, Rows uint
				}
				if json.Unmarshal(f.data, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 500 {
					ex.Resize(ctx, m.Cols, m.Rows)
				}
			}
		}()
		// From the terminal to the page, until the step ends.
		buf := make([]byte, 32<<10)
		for {
			n, err := ex.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.Log.Debug("setup terminal", "error", err)
				}
				break
			}
		}
		if ctx.Err() != nil {
			return // the page went, another page took the step, or Stop removed the microVM
		}
		code, _ := ex.ExitCodeWait(context.Background(), 2*time.Second)
		res := Result{Step: step, Message: fmt.Sprintf("the step did not finish (exit code %d); the terminal shows what it printed last", code)}
		if code == 0 {
			cctx, c := context.WithTimeout(context.Background(), time.Minute)
			res = s.Collect(cctx, step, home)
			c()
		}
		// The microVM goes with its step, collected or not (12.6, step 4), before the page is told.
		s.end(sess.id, gen)
		send(map[string]any{"type": "done", "step": res.Step, "ok": res.OK, "message": res.Message})
		conn.Close(CloseDone, "done")
	}
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

func dim(v string, def uint) uint {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 || n > 999 {
		return def
	}
	return uint(n)
}
