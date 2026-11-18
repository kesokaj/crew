package gitendpoint

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// heard keeps what OnPush heard: each ref, and whether it came with a new value.
type heard struct {
	mu    sync.Mutex
	calls []string
}

func (h *heard) add(acc Access, ref, old, new string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if new == "" {
		ref += " with no value"
	}
	h.calls = append(h.calls, ref)
}

// lockedText is a log's text, written from the server's goroutines.
type lockedText struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedText) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedText) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (h *heard) refs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// The slots of a push go back as soon as git has ended, before OnPush writes its record: a record that
// waits holds up no other push (since 1.08).
func TestTheSlotsGoBackBeforeThePushIsRecorded(t *testing.T) {
	entered, open := make(chan string, 8), make(chan struct{})
	g := newSlotRig(t, func(e *Endpoint) {
		e.SlotWait = 30 * time.Second
		e.OnPush = func(acc Access, ref, old, new string) {
			entered <- ref
			<-open
		}
	})
	t.Cleanup(func() { close(open) })
	for _, tok := range []string{"a-1", "a-2", "a-3"} {
		g.let(tok)
	}
	g.push("a-1")
	g.push("a-2")
	for range 2 {
		select {
		case <-entered:
		case <-time.After(20 * time.Second):
			t.Fatal("a push was not heard of")
		}
	}
	g.push("a-3")
	g.waitStarted("a-3")
}

// cancelAtEOF ends a request's context once its body is read whole, as Go's server does when the
// client closes its side after its body.
type cancelAtEOF struct {
	io.ReadCloser
	cancel func()
}

func (c cancelAtEOF) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if err == io.EOF {
		c.cancel()
	}
	return n, err
}

// prefixRig serves the slot rig's endpoint to tokens that may push every ref under refs/heads/p/, and
// the refs more, with a ref refs/heads/p/old there before, and hears of what was pushed.
func prefixRig(t *testing.T, set func(*Endpoint), more ...string) (*slotRig, *heard) {
	h := &heard{}
	g := newSlotRig(t, func(e *Endpoint) {
		auth := e.Auth
		e.Auth = func(r *http.Request, repo string) (Access, error) {
			a, err := auth(r, repo)
			a.Push = append([]string{"refs/heads/p/"}, more...)
			return a, err
		}
		e.OnPush = h.add
		if set != nil {
			set(e)
		}
	})
	ctx := context.Background()
	if err := g.e.run(ctx, g.e.Path("a"), nil, "update-ref", "refs/heads/p/old", g.e.Ref(ctx, "a", "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	return g, h
}

// A push is heard of for the refs it changed alone, also when its client closes its side once its
// body is sent, which ends the request's context (since 1.08).
func TestAPushIsHeardOfForItsOwnRefsWhenItsClientClosesItsSide(t *testing.T) {
	g, h := prefixRig(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		r = r.WithContext(ctx)
		if r.Method == http.MethodPost {
			r.Body = cancelAtEOF{r.Body, cancel}
		}
		g.e.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	g.let("a-1")
	g.push("a-1")
	if err := g.wait("a-1"); err != nil {
		t.Fatal(err)
	}
	if got := h.refs(); len(got) != 1 || got[0] != "refs/heads/p/1" {
		t.Fatalf("heard of %v, want refs/heads/p/1 alone", got)
	}
}

// A push whose refs cannot be read, before git or after it, is heard of for no ref, never for every ref
// as new, and says so in the log; when it may push main, main is heard of with no value, so the
// terminal does not take it (since 1.08).
func TestAPushWhoseRefsCannotBeReadIsHeardOfForNoRef(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		t.Run([]string{"", "before", "after"}[n], func(t *testing.T) {
			dir := t.TempDir()
			// The endpoint's git, which fails its n-th listing of refs.
			script := "#!/bin/sh\nif [ \"$1\" = for-each-ref ]; then echo x >>" + dir + "/n; [ \"$(wc -l <" + dir + "/n)\" -eq " +
				[]string{"", "1", "2"}[n] + " ] && exit 1; fi\nexec " + real + " \"$@\"\n"
			wrapped := filepath.Join(dir, "git")
			if err := os.WriteFile(wrapped, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			log := &lockedText{}
			g, h := prefixRig(t, func(e *Endpoint) { e.Git, e.Log = wrapped, slog.New(slog.NewTextHandler(log, nil)) }, "refs/heads/main")
			g.let("a-old")
			if out, err := git(t, g.work, "a-old", "push", "-q", "-f", g.url+"/git/a.git", "HEAD:refs/heads/p/old"); err != nil {
				t.Fatal(out)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "n")); strings.Count(string(b), "x") < n {
				t.Fatalf("git listed refs %d times, not failing the %d-th", strings.Count(string(b), "x"), n)
			}
			if got := h.refs(); len(got) != 1 || got[0] != "refs/heads/main with no value" || !strings.Contains(log.String(), "it is recorded for no ref") {
				t.Fatalf("heard of %v, log %q", got, log.String())
			}
		})
	}
}

// Each read of a push's refs has a time of its own: a push whose git and hook take longer than one read
// may is still heard of (since 1.08).
func TestASlowPushIsHeardOf(t *testing.T) {
	g, h := prefixRig(t, func(e *Endpoint) { e.refTime = time.Second })
	g.push("a-1")
	g.waitStarted("a-1")
	time.Sleep(2 * time.Second)
	g.let("a-1")
	if err := g.wait("a-1"); err != nil {
		t.Fatal(err)
	}
	if got := h.refs(); len(got) != 1 || got[0] != "refs/heads/p/1" {
		t.Fatalf("heard of %v, want refs/heads/p/1", got)
	}
}
