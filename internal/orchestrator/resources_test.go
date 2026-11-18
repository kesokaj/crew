package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/docker/docker/api/types/system"

	"github.com/kesokaj/crew/internal/config"
)

func sizingOrch() (*Orchestrator, *fakeDocker) {
	fd := newFakeDocker()
	return &Orchestrator{docker: fd, cfg: &config.Config{}, logger: slog.New(slog.DiscardHandler)}, fd
}

func TestHostIsReadFromTheDaemonAndCachedOnSuccess(t *testing.T) {
	o, fd := sizingOrch()
	fd.fail["Info"] = errors.New("daemon starting")
	if _, err := o.Host(context.Background()); err == nil {
		t.Fatal("Host() succeeded while Info failed")
	}
	delete(fd.fail, "Info")
	fd.info = system.Info{NCPU: 8, MemTotal: 16 << 30}
	h, err := o.Host(context.Background())
	if err != nil || h != (Host{CPUs: 8, MemMiB: 16 << 10}) {
		t.Fatalf("Host() = %+v, %v; want {8 16384}, nil", h, err)
	}
	fd.info = system.Info{NCPU: 1, MemTotal: 1 << 30}
	if h2, _ := o.Host(context.Background()); h2 != h {
		t.Fatalf("Host() asked again after a success: %+v", h2)
	}
	if n := fd.callsTo("Info"); n != 2 {
		t.Fatalf("Info called %d times, want 2 (one failure, one success)", n)
	}
}

func TestHostRefusesAnEmptyAnswer(t *testing.T) {
	o, fd := sizingOrch()
	fd.info = system.Info{}
	if _, err := o.Host(context.Background()); err == nil {
		t.Fatal("Host() accepted NCPU 0 / MemTotal 0")
	}
}

func TestCappedBufferStopsAtItsLimitAndMaxExecOutputIs2MiB(t *testing.T) {
	if maxExecOutput != 2<<20 {
		t.Fatalf("maxExecOutput = %d, want %d", maxExecOutput, 2<<20)
	}
	c := &cappedBuffer{max: 6}
	if n, err := c.Write([]byte("abc")); err != nil || n != 3 || c.truncated || c.String() != "abc" {
		t.Fatalf("within the limit: n=%d err=%v truncated=%v str=%q", n, err, c.truncated, c.String())
	}
	if n, err := c.Write([]byte("def")); err != nil || n != 3 || c.truncated || c.String() != "abcdef" {
		t.Fatalf("at the limit: n=%d err=%v truncated=%v str=%q", n, err, c.truncated, c.String())
	}
	if n, err := c.Write([]byte("gh")); err != nil || n != 2 || !c.truncated || c.String() != "abcdef\n… (truncated)" {
		t.Fatalf("past the limit once full: n=%d err=%v truncated=%v str=%q", n, err, c.truncated, c.String())
	}
	c2 := &cappedBuffer{max: 6}
	if n, err := c2.Write([]byte("abcdefgh")); err != nil || n != 8 || !c2.truncated || c2.String() != "abcdef\n… (truncated)" {
		t.Fatalf("straddling the limit: n=%d err=%v truncated=%v str=%q", n, err, c2.truncated, c2.String())
	}
}
