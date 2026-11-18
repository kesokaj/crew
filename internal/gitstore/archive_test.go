package gitstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// failingWriter takes n bytes, then fails.
type failingWriter struct{ n int }

var errFull = errors.New("the check sandbox went away")

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		k := w.n
		w.n = 0
		return k, errFull
	}
	w.n -= len(p)
	return len(p), nil
}

// The archive writer's error path (M6, open item 4): a reader that goes away in the middle ends the
// archive with its error at once, and git, blocked writing what nobody reads, is stopped rather than
// waited for; a commit that is not there is an error before anything is written.
func TestAnArchiveWhoseReaderGoesAwayEndsAtOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	big := strings.Repeat("a line of a large file\n", 400000) // about 9 MB, more than a pipe holds
	if err := s.CreateLocal(ctx, "shop", map[string]string{"big.txt": big, "small.txt": "x\n"}, "First", me); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Archive(ctx, "shop", s.Main(ctx, "shop"), &failingWriter{n: 1000}) }()
	select {
	case err := <-done:
		if !errors.Is(err, errFull) {
			t.Fatalf("the archive ended with %v, not the writer's error", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the archive waited on git after its reader went away")
	}
	w := &failingWriter{n: 1 << 30}
	if err := s.Archive(ctx, "shop", strings.Repeat("ab", 20), w); err == nil || w.n != 1<<30 {
		t.Fatalf("a commit that is not there: %v, %d bytes written", err, 1<<30-w.n)
	}
}
