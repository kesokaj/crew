package hold

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kesokaj/crew/internal/db"
)

// WriteProbe writes the database every period, so the inbox can say when it could be written again
// (21, D22). Two writes in a row that fail are a time it could not be written, from the first: its
// time is kept in memory and in File, written once, so a restart keeps it. The first write after it
// records from and to. A server that starts while it cannot write, with no file, does not know the
// from.
type WriteProbe struct {
	Pool db.Querier
	// File keeps the from across a restart: <data>/backups/.db-unwritable (D31).
	File string
	// Write is the probe's write; nil updates crew_state's row to itself.
	Write func(ctx context.Context) error
	Now   func() time.Time
	Log   *slog.Logger

	mu        sync.Mutex
	checked   bool       // a check ran since Start
	failedAt  *time.Time // the first of the failures in a row, nil when not known
	failures  int        // in a row
	unwritten bool       // two in a row: a time the database could not be written
}

// notKnown is what File holds when the from is not known.
const notKnown = "not known"

// Start reads File: a time the database could not be written that a server before this one saw.
func (p *WriteProbe) Start() {
	b, err := os.ReadFile(p.File)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.Log.Warn("reading the time the database could not be written", "file", p.File, "error", err)
		}
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unwritten, p.failures = true, 2
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
		p.failedAt = &t
	}
}

// Run checks every period until ctx ends.
func (p *WriteProbe) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		p.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *WriteProbe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *WriteProbe) write(ctx context.Context) error {
	if p.Write != nil {
		return p.Write(ctx)
	}
	_, err := p.Pool.Exec(ctx, `UPDATE crew_state SET id = id WHERE id = 1`)
	return err
}

// Check is one write.
func (p *WriteProbe) Check(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := p.write(wctx)
	cancel()
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	first := !p.checked
	p.checked = true
	if err != nil {
		p.failures++
		if p.failures == 1 && !first {
			p.failedAt = &now
		}
		if p.failures == 2 && !p.unwritten {
			p.unwritten = true
			p.Log.Warn("Crew cannot write its database", "error", err)
			from := notKnown
			if p.failedAt != nil {
				from = p.failedAt.UTC().Format(time.RFC3339)
			}
			if werr := os.WriteFile(p.File, []byte(from+"\n"), 0o600); werr != nil {
				p.Log.Warn("keeping the time the database could not be written", "file", p.File, "error", werr)
			}
		}
		return
	}
	if p.unwritten {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := WritableAgain(wctx, p.Pool, p.failedAt, now)
		cancel()
		if err != nil {
			p.Log.Warn("recording that the database can be written again", "error", err)
			return
		}
		p.Log.Info("Crew can write its database again")
		if err := os.Remove(p.File); err != nil && !errors.Is(err, os.ErrNotExist) {
			p.Log.Warn("removing the time the database could not be written", "file", p.File, "error", err)
		}
	}
	p.failures, p.failedAt, p.unwritten = 0, nil, false
}
