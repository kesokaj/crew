package gitendpoint

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slotRig serves the repositories a, b and c. A token <repo>-<n> pushes refs/heads/p/<n> of its
// repository with a rule, so the server's program runs for each push: here a program that says it
// started, keeps the score it runs with, and holds the push until the test lets it go.
type slotRig struct {
	t    *testing.T
	e    *Endpoint
	srv  *httptest.Server
	url  string // where push sends: the server's, unless a test serves the endpoint otherwise
	dir  string // the program's marks
	work string // a repository with one commit, which every push sends
	done map[string]chan error
}

func newSlotRig(t *testing.T, set func(*Endpoint)) *slotRig {
	t.Helper()
	g := &slotRig{t: t, dir: t.TempDir(), work: t.TempDir(), done: map[string]chan error{}}
	program := filepath.Join(t.TempDir(), "crew")
	script := fmt.Sprintf(`#!/bin/sh
read old new ref
name=$(basename "$(pwd)" .git)-$(basename "$ref")
cat /proc/self/oom_score_adj >%[1]s/score-$name
touch %[1]s/started-$name
while [ ! -e %[1]s/go-$name ]; do sleep 0.05; done
exit 0
`, g.dir)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Endpoint{Dir: t.TempDir(), Self: program, TreeTime: time.Minute}
	ctx := context.Background()
	for _, repo := range []string{"a", "b", "c"} {
		if err := e.Ensure(ctx, repo); err != nil {
			t.Fatal(err)
		}
		if err := e.Init(ctx, repo, map[string]string{"README.md": repo + "\n"}, "The fixture"); err != nil {
			t.Fatal(err)
		}
	}
	e.Auth = func(r *http.Request, repo string) (Access, error) {
		own, n, ok := strings.Cut(Token(r), "-")
		if !ok || own != repo {
			return Access{}, ErrRefused
		}
		ref := "refs/heads/p/" + n
		return Access{Repo: repo, Who: Token(r), Read: []string{"refs/heads/main"}, Push: []string{ref},
			Paths: map[string]PathRule{ref: {Base: "refs/heads/main", Allow: []string{Everything}}}}, nil
	}
	if set != nil {
		set(e)
	}
	g.e = e
	g.srv = httptest.NewServer(e)
	g.url = g.srv.URL
	t.Cleanup(func() {
		// Every held push is let go, so no git outlives the test.
		for name := range g.done {
			os.WriteFile(filepath.Join(g.dir, "go-"+name), nil, 0o644)
		}
		for _, c := range g.done {
			<-c
		}
		// A git that never ends would keep the server's Close waiting: it is a failure, not a hang.
		closed := make(chan struct{})
		go func() { g.srv.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(20 * time.Second):
			t.Error("a request of the endpoint did not end")
		}
	})
	if out, err := git(t, g.work, "", "init", "-q"); err != nil {
		t.Fatal(out)
	}
	if out, err := git(t, g.work, "", "commit", "-q", "--allow-empty", "-m", "work"); err != nil {
		t.Fatal(out)
	}
	return g
}

// push starts a push of token, as <repo>-<n>, and returns at once; wait reads its end.
func (g *slotRig) push(token string) {
	repo, n, _ := strings.Cut(token, "-")
	c := make(chan error, 1)
	g.done[token] = c
	go func() {
		out, err := git(g.t, g.work, token, "push", "-q", g.url+"/git/"+repo+".git", "HEAD:refs/heads/p/"+n)
		if err != nil {
			err = fmt.Errorf("%v: %s", err, out)
		}
		c <- err
	}()
}

func (g *slotRig) wait(token string) error {
	select {
	case err := <-g.done[token]:
		delete(g.done, token)
		return err
	case <-time.After(30 * time.Second):
		g.t.Fatalf("the push of %s did not end", token)
		return nil
	}
}

func (g *slotRig) started(token string) bool {
	_, err := os.Stat(filepath.Join(g.dir, "started-"+token))
	return err == nil
}

// waitStarted waits until the push of token is in the server's program.
func (g *slotRig) waitStarted(token string) {
	g.t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if g.started(token) {
			return
		}
	}
	g.t.Fatalf("the push of %s did not start", token)
}

// waitHeld waits until n requests of service hold slots of repo.
func (g *slotRig) waitHeld(service, repo string, n int) {
	g.t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		s := g.e.slotsOf(service)
		s.mu.Lock()
		held := len(s.repos[repo])
		s.mu.Unlock()
		if held == n {
			return
		}
	}
	g.t.Fatalf("%d requests of %s did not hold slots of %s", n, service, repo)
}

func (g *slotRig) let(token string) {
	if err := os.WriteFile(filepath.Join(g.dir, "go-"+token), nil, 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// A repository takes two pushes at once and the server four; one past either waits for its slot, then
// is refused, and never reaches git's hook (6.1, since 1.08).
func TestPushesPastTheirSlotsWaitAndThenAreRefused(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 2 * time.Second })
	g.push("a-1")
	g.push("a-2")
	g.waitStarted("a-1")
	g.waitStarted("a-2")
	g.push("a-3")
	// Its advertisement of refs waits for a slot too, so git shows the text.
	if err := g.wait("a-3"); err == nil || !strings.Contains(err.Error(), busyText("git-receive-pack")) || g.started("a-3") {
		t.Fatalf("a third push of one repository: %v, started %v", err, g.started("a-3"))
	}
	g.push("b-1")
	g.push("b-2")
	g.waitStarted("b-1")
	g.waitStarted("b-2")
	g.push("c-1")
	if err := g.wait("c-1"); err == nil || !strings.Contains(err.Error(), busyText("git-receive-pack")) || g.started("c-1") {
		t.Fatalf("a fifth push of the server: %v, started %v", err, g.started("c-1"))
	}
	// A slot that ends is taken again.
	g.let("a-1")
	if err := g.wait("a-1"); err != nil {
		t.Fatal(err)
	}
	g.push("c-2")
	g.waitStarted("c-2")
	// The push of c that was refused gave back the slot of c it held while it waited for the server's.
	g.let("a-2")
	if err := g.wait("a-2"); err != nil {
		t.Fatal(err)
	}
	g.push("c-3")
	g.waitStarted("c-3")
	for _, tok := range []string{"b-1", "b-2", "c-2", "c-3"} {
		g.let(tok)
		if err := g.wait(tok); err != nil {
			t.Fatal(err)
		}
	}
}

// A push past the slots waits, and goes on when one is let go.
func TestAPushWaitsForASlotThatEnds(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 30 * time.Second })
	g.push("a-1")
	g.push("a-2")
	g.waitStarted("a-1")
	g.waitStarted("a-2")
	g.push("a-3")
	time.Sleep(time.Second)
	if g.started("a-3") {
		t.Fatal("a third push of one repository started beside two")
	}
	g.let("a-1")
	g.waitStarted("a-3")
	for _, tok := range []string{"a-1", "a-2", "a-3"} {
		g.let(tok)
		if err := g.wait(tok); err != nil {
			t.Fatal(err)
		}
	}
}

// A push's git, its hook and the server's program run with a raised score, so the kernel stops them
// before the server when its memory is full; a push whose score cannot be set is not taken (since 1.08).
func TestAPushRunsWithARaisedScoreForTheKernel(t *testing.T) {
	g := newSlotRig(t, nil)
	g.let("a-1")
	g.push("a-1")
	if err := g.wait("a-1"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(g.dir, "score-a-1")); err != nil || strings.TrimSpace(string(b)) != GitOOMScore {
		t.Fatalf("the score of a push: %q %v", b, err)
	}
	old := oomScoreFile
	oomScoreFile = filepath.Join(t.TempDir(), "none", "oom_score_adj")
	t.Cleanup(func() { oomScoreFile = old })
	g.let("a-2")
	g.push("a-2")
	if err := g.wait("a-2"); err == nil || g.started("a-2") {
		t.Fatalf("a push whose score could not be set: %v, started %v", err, g.started("a-2"))
	}
}

// rawPost sends a POST of service to repo with token whose body never comes: a body of 1000 bytes, or
// with chunked one chunk of a body of unknown length. The connection is closed with the test.
func rawPost(t *testing.T, srv *httptest.Server, repo, service, token string, chunked bool) net.Conn {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	length := "Content-Length: 1000\r\n"
	if chunked {
		length = "Transfer-Encoding: chunked\r\n"
	}
	fmt.Fprintf(conn, "POST /git/%s.git/%s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/x-%s-request\r\n%s\r\n",
		repo, service, token, service, length)
	if chunked {
		fmt.Fprintf(conn, "4\r\n0000\r\n")
	}
	return conn
}

// status reads the answer to a raw request: its status and body, or "" when none came within d.
func status(conn net.Conn, d time.Duration) (int, string) {
	conn.SetReadDeadline(time.Now().Add(d))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	var b strings.Builder
	bufio.NewReader(resp.Body).WriteTo(&b)
	return resp.StatusCode, b.String()
}

// Fetches have slots of their own, four of a repository and eight of the server; one past either waits
// for its slot, then is refused with a fixed text, and a push still goes on (since 1.08).
func TestFetchesPastTheirSlotsWaitAndThenAreRefused(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = time.Second })
	var held []net.Conn
	for _, repo := range []string{"a", "b"} {
		for n := 1; n <= MaxRepoFetches; n++ {
			held = append(held, rawPost(t, g.srv, repo, "git-upload-pack", fmt.Sprintf("%s-%d", repo, n), false))
		}
		g.waitHeld("git-upload-pack", repo, MaxRepoFetches)
		past := rawPost(t, g.srv, repo, "git-upload-pack", repo+"-9", false)
		if code, body := status(past, 10*time.Second); code != http.StatusServiceUnavailable || !strings.Contains(body, busyText("git-upload-pack")) {
			t.Fatalf("a fifth fetch of %s: %d %q", repo, code, body)
		}
	}
	past := rawPost(t, g.srv, "c", "git-upload-pack", "c-1", false)
	if code, body := status(past, 10*time.Second); code != http.StatusServiceUnavailable || !strings.Contains(body, busyText("git-upload-pack")) {
		t.Fatalf("a ninth fetch of the server: %d %q", code, body)
	}
	for _, c := range held {
		if code, _ := status(c, 50*time.Millisecond); code != 0 {
			t.Fatalf("a held fetch was answered: %d", code)
		}
	}
	g.let("a-1")
	g.push("a-1")
	if err := g.wait("a-1"); err != nil {
		t.Fatalf("a push beside eight fetches: %v", err)
	}
}

// A body that does not come within its time holds no slot: its read fails, git ends, and the slot is
// taken by the next push (since 1.08).
func TestABodySentSlowlyHoldsNoSlot(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait, e.BodyTime = 10*time.Second, time.Second })
	slow := []net.Conn{rawPost(t, g.srv, "a", "git-receive-pack", "a-1", false), rawPost(t, g.srv, "a", "git-receive-pack", "a-2", false)}
	g.waitHeld("git-receive-pack", "a", 2)
	g.let("a-3")
	start := time.Now()
	g.push("a-3")
	if err := g.wait("a-3"); err != nil || time.Since(start) > 8*time.Second {
		t.Fatalf("a push after two slow bodies: %v after %v", err, time.Since(start))
	}
	for _, c := range slow {
		if code, body := status(c, 5*time.Second); code != http.StatusRequestTimeout || !strings.Contains(body, "did not arrive whole in time") {
			t.Fatalf("a slow body: %d %q", code, body)
		}
	}
}

// A push that waits for a slot has written nothing of its body to the disk: a body of unknown length
// is read into a file only once the push has its slot (since 1.08).
func TestAPushThatWaitsWritesNothingOfItsBody(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 30 * time.Second })
	g.push("a-1")
	g.push("a-2")
	g.waitStarted("a-1")
	g.waitStarted("a-2")
	g.waitHeld("git-receive-pack", "a", 2)
	before, _ := filepath.Glob(filepath.Join(tmp, "crew-git-*"))
	waiting := rawPost(t, g.srv, "a", "git-receive-pack", "a-3", true)
	time.Sleep(500 * time.Millisecond)
	if files, _ := filepath.Glob(filepath.Join(tmp, "crew-git-*")); len(files) != len(before) {
		t.Fatalf("a push that waits wrote: %v, before it %v", files, before)
	}
	waiting.Close()
	g.let("a-1")
	g.let("a-2")
}

// A push whose body ends before its length holds no slot, and leaves no git behind: git http-backend
// spins without end on such a body, so every push is read whole first (since 1.08).
func TestAPushWhoseBodyEndsShortHoldsNoSlot(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 5 * time.Second })
	for _, tok := range []string{"a-1", "a-2"} {
		c := rawPost(t, g.srv, "a", "git-receive-pack", tok, false)
		fmt.Fprint(c, "0000")
		c.Close()
	}
	g.let("a-3")
	g.push("a-3")
	if err := g.wait("a-3"); err != nil {
		t.Fatalf("a push after two short bodies: %v", err)
	}
}
