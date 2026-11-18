package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/runspec"
)

// follow reads each turn's spool as it grows, and ends the turn when its exit.json is there and the
// spool is read to its end. It also notices a microVM that is gone.
func (r *kataRun) follow() {
	tick := time.NewTicker(r.k.poll())
	defer tick.Stop()
	lastAlive := time.Now()
	for {
		select {
		case <-r.done:
			return
		case <-tick.C:
		case <-r.wake:
		}
		r.mu.Lock()
		turn, offset, done := r.turn, r.offset, r.turnDone
		r.mu.Unlock()
		if done {
			continue
		}
		ended := r.exitFile(turn) != nil
		if !r.read(turn, offset, ended) {
			return
		}
		if ex := r.exitFile(turn); ex != nil {
			// The CLI ended before crew-run wrote exit.json, so the spool is complete: read what
			// came since, then end the turn.
			r.mu.Lock()
			offset = r.offset
			r.mu.Unlock()
			if !r.read(turn, offset, true) || !r.end(turn, *ex) {
				return
			}
			continue
		}
		if time.Since(lastAlive) > r.k.aliveEvery() {
			lastAlive = time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			alive, err := r.k.Runtime.Running(ctx, r.container)
			cancel()
			if err == nil && !alive && r.exitFile(turn) == nil {
				if !r.endBy(turn, runspec.Exit{Exit: -1, Reason: "sandbox", Detail: "the microVM ended during the turn"}, true) {
					return
				}
			}
		}
	}
}

// send passes an event on, unless the run was stopped.
func (r *kataRun) send(e RunEvent) bool {
	select {
	case r.events <- e:
		return true
	case <-r.done:
		return false
	}
}

// read reads the spool of turn from offset to its end, and sends every complete line. With final,
// the CLI has ended, so a last line without a newline is complete too.
func (r *kataRun) read(turn int, offset int64, final bool) bool {
	f, err := homepath.Open(r.home, path.Join(r.turnDir(turn), runspec.EventsFile))
	if err != nil {
		return true // not there yet
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return true
	}
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, n, err := readLine(br)
		if n == 0 && err != nil {
			return true
		}
		complete := err == nil // ReadSlice ended at a newline
		if !complete && !final && n <= maxLine {
			return true // the rest of this line is still being written
		}
		// A line with no end that is already too long is cut here, and not read again from its
		// start every round: what follows it is read as lines of their own.
		next := offset + n
		body := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(body)) > 0 {
			var e RunEvent
			if len(body) > maxLine {
				e = RunEvent{Event: cli.Event{Type: cli.EventError, Payload: map[string]any{
					"kind": "line too long", "bytes": len(body)}}, Raw: body[:64<<10]}
			} else {
				e = RunEvent{Event: r.a.Parse(body), Raw: append([]byte(nil), body...)}
			}
			e.Run, e.Turn, e.At, e.Offset = r.id, turn, time.Now().UTC(), next
			if e.ConversationID != "" && cli.SafeID(e.ConversationID) {
				r.mu.Lock()
				if r.conv == "" {
					r.conv = e.ConversationID
				}
				r.mu.Unlock()
			}
			if !r.send(e) {
				return false
			}
		}
		r.mu.Lock()
		r.offset = next
		r.mu.Unlock()
		offset = next
		if err != nil {
			return true
		}
	}
}

// readLine reads up to and with the next newline, and says how many bytes it read. A line over
// maxLine is read to its end but kept only up to maxLine plus one byte, so it is known to be too long.
func readLine(br *bufio.Reader) ([]byte, int64, error) {
	var buf []byte
	var n int64
	for {
		chunk, err := br.ReadSlice('\n')
		n += int64(len(chunk))
		switch {
		case len(buf)+len(chunk) <= maxLine+1:
			buf = append(buf, chunk...)
		case len(buf) <= maxLine:
			buf = append(buf, chunk[:maxLine+1-len(buf)]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, n, err
	}
}

func (r *kataRun) exitFile(turn int) *runspec.Exit {
	b, err := homepath.ReadFile(r.home, path.Join(r.turnDir(turn), runspec.ExitFile), maxExitFile)
	if err != nil {
		return nil
	}
	var ex runspec.Exit
	if json.Unmarshal(b, &ex) != nil {
		ex = runspec.Exit{Exit: -1, Reason: cli.ReasonError, Detail: "exit.json does not read"}
	}
	return &ex
}

// end ends a turn by its exit.json: its last result, the CLI's own files of the conversation, and
// the end event.
func (r *kataRun) end(turn int, ex runspec.Exit) bool { return r.endBy(turn, ex, false) }

// endBy ends a turn; byHarness says the reason is the harness's own, not the home's.
func (r *kataRun) endBy(turn int, ex runspec.Exit, byHarness bool) bool {
	result, conv := r.lastResult(turn)
	r.mu.Lock()
	if r.conv == "" {
		r.conv = conv
	}
	if r.conv == "" && cli.SafeID(ex.Conversation) {
		r.conv = ex.Conversation
	}
	// exit.json is the sandbox's word: an id it names that is not one of the CLI's is not kept.
	if !cli.SafeID(ex.Conversation) {
		ex.Conversation = r.conv
	}
	r.turnDone = true
	r.mu.Unlock()
	files := map[string][]byte{}
	for _, p := range r.a.ConversationFiles(ex.Conversation, r.workspaceRel()) {
		b, err := homepath.ReadFile(r.home, p, maxCLIFile)
		if err != nil {
			r.k.Log.Warn("a CLI's file of the conversation is not kept", "run", r.id, "path", p, "error", err)
			continue
		}
		files[p] = b
	}
	end := &TurnEnd{Exit: ex, Result: result, Files: files, ByHarness: byHarness}
	if r.keep != "" {
		end.FolderPath, end.FolderNote = keepFolder(r.home, r.keep)
	}
	return r.send(RunEvent{Run: r.id, Turn: turn, At: time.Now().UTC(),
		Event: cli.Event{Type: cli.EventStatus, Payload: map[string]any{"status": "ended", "reason": ex.Reason,
			"detail": ex.Detail, "exit": ex.Exit}},
		End: end})
}

// lastResult reads the turn's spool once more for its last result line and the conversation's id.
func (r *kataRun) lastResult(turn int) ([]byte, string) {
	f, err := homepath.Open(r.home, path.Join(r.turnDir(turn), runspec.EventsFile))
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var result []byte
	conv := ""
	for {
		line, _, err := readLine(br)
		body := bytes.TrimRight(line, "\r\n")
		if len(body) > 0 && len(body) <= maxLine {
			e := r.a.Parse(body)
			if e.Result {
				result = append(result[:0], body...)
			}
			if conv == "" && cli.SafeID(e.ConversationID) {
				conv = e.ConversationID
			}
		}
		if err != nil {
			return result, conv
		}
	}
}
