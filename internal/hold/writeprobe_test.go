package hold_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/hold"
)

type probeKit struct {
	t    *testing.T
	d    *dbtest.DB
	file string
	now  time.Time
	fail bool
}

func (k *probeKit) probe() *hold.WriteProbe {
	p := &hold.WriteProbe{Pool: k.d.Server, File: k.file, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return k.now },
		Write: func(context.Context) error {
			if k.fail {
				return errors.New("could not extend file: No space left on device")
			}
			return nil
		}}
	p.Start()
	return p
}

func (k *probeKit) at(minutes int) { k.now = time.Date(2026, 10, 24, 8, minutes, 0, 0, time.UTC) }

func (k *probeKit) kept() *hold.Unwritable {
	k.t.Helper()
	u, err := hold.ReadUnwritable(context.Background(), k.d.Server)
	if err != nil {
		k.t.Fatal(err)
	}
	return u
}

// The write probe (21, D22, D31): two writes in a row that fail are a time the database could not be
// written, from the first; the first write after it keeps from and to for the inbox. One that fails
// alone is no such time. A server restarted during it keeps the from through its file; one that
// starts while it cannot write, with no file, says the from is not known.
func TestTheWriteProbeKeepsTheTimeTheDatabaseCouldNotBeWritten(t *testing.T) {
	ctx := context.Background()
	k := &probeKit{t: t, d: dbtest.New(t, false), file: filepath.Join(t.TempDir(), ".db-unwritable")}
	p := k.probe()

	k.at(0)
	k.fail = true
	p.Check(ctx)
	k.at(1)
	k.fail = false
	p.Check(ctx)
	if u := k.kept(); u != nil {
		t.Fatalf("one failed write: %+v", u)
	}

	k.at(10)
	k.fail = true
	p.Check(ctx)
	k.at(11)
	p.Check(ctx)
	if _, err := os.Stat(k.file); err != nil {
		t.Fatalf("no file during the outage: %v", err)
	}
	// The server restarts during the outage: the file keeps the from.
	p = k.probe()
	k.at(20)
	p.Check(ctx)
	k.at(25)
	k.fail = false
	p.Check(ctx)
	u := k.kept()
	if u == nil || u.From == nil || !u.From.Equal(time.Date(2026, 10, 24, 8, 10, 0, 0, time.UTC)) || !u.To.Equal(k.now) {
		t.Fatalf("after a restart in the outage: %+v", u)
	}
	if _, err := os.Stat(k.file); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}

	// A server that starts while it cannot write, and kept no file: the from is not known.
	k.fail = true
	p = k.probe()
	k.at(30)
	p.Check(ctx)
	k.at(31)
	p.Check(ctx)
	k.at(33)
	k.fail = false
	p.Check(ctx)
	if u := k.kept(); u == nil || u.From != nil || !u.To.Equal(k.now) {
		t.Fatalf("a start during the outage: %+v", u)
	}
}
