package gitendpoint

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// waitWaiting waits until n requests of service wait for a slot of repo.
func (g *slotRig) waitWaiting(service, repo string, n int) {
	g.t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		s := g.e.slotsOf(service)
		s.mu.Lock()
		waiting := s.waiting[repo]
		s.mu.Unlock()
		if waiting == n {
			return
		}
	}
	g.t.Fatalf("%d requests of %s did not wait for slots of %s", n, service, repo)
}

// At most twice a repository's slots wait beside them; one more is refused at once, with the text
// (since 1.08).
func TestRequestsPastTwiceTheSlotsWaitingAreRefusedAtOnce(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 30 * time.Second })
	g.push("a-1")
	g.push("a-2")
	g.waitStarted("a-1")
	g.waitStarted("a-2")
	for n := 3; n <= 2+2*MaxRepoPushes; n++ {
		rawPost(t, g.srv, "a", "git-receive-pack", fmt.Sprintf("a-%d", n), false)
	}
	g.waitWaiting("git-receive-pack", "a", 2*MaxRepoPushes)
	start := time.Now()
	past := rawPost(t, g.srv, "a", "git-receive-pack", "a-9", false)
	if code, body := status(past, 5*time.Second); code != http.StatusServiceUnavailable || !strings.Contains(body, busyText("git-receive-pack")) || time.Since(start) > 3*time.Second {
		t.Fatalf("a push past the ones that wait: %d %q after %v", code, body, time.Since(start))
	}
	g.let("a-1")
	g.let("a-2")
}

// fetchPack sends a whole fetch of main of repo with token, as git's protocol 0 asks for it, and reads
// none of the answer.
func fetchPack(t *testing.T, g *slotRig, repo, token string) net.Conn {
	t.Helper()
	main := g.e.Ref(context.Background(), repo, "refs/heads/main")
	body := fmt.Sprintf("0032want %s\n00000009done\n", main)
	conn, err := net.Dial("tcp", strings.TrimPrefix(g.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST /git/%s.git/git-upload-pack HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/x-git-upload-pack-request\r\nContent-Length: %d\r\n\r\n%s",
		repo, token, len(body), body)
	return conn
}

// A fetch whose client stops reading holds no slot: its answer's write ends within its time, and git
// with it (since 1.08).
func TestAFetchWhoseClientStopsReadingHoldsNoSlot(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait, e.WriteTime = 15*time.Second, 3*time.Second })
	big := make([]byte, 32<<20)
	rand.Read(big)
	ctx := context.Background()
	if err := g.e.Ensure(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if err := g.e.Init(ctx, "d", map[string]string{"big.bin": string(big)}, "big"); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= MaxRepoFetches; n++ {
		fetchPack(t, g, "d", fmt.Sprintf("d-%d", n))
	}
	g.waitHeld("git-upload-pack", "d", MaxRepoFetches)
	// An advertisement of refs takes a slot of the fetches too, and its answer is small, within the
	// short write time this test sets.
	start := time.Now()
	if out, err := git(t, t.TempDir(), "d-9", "ls-remote", g.srv.URL+"/git/d.git"); err != nil || !strings.Contains(out, "refs/heads/main") || time.Since(start) > 15*time.Second {
		t.Fatalf("a listing beside fetches that read nothing: %v after %v: %s", err, time.Since(start), out)
	}
}

// An advertisement of refs that says it has a body is refused at once, before it takes a slot: git
// would wait for that body and hold the slot (since 1.08).
func TestAnAdvertisementWithABodyIsRefused(t *testing.T) {
	g := newSlotRig(t, func(e *Endpoint) { e.SlotWait = 3 * time.Second })
	for _, n := range []string{"1", "2"} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(g.srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		fmt.Fprintf(conn, "GET /git/a.git/info/refs?service=git-receive-pack HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer a-%s\r\nContent-Length: 1\r\n\r\n", n)
		if code, body := status(conn, 5*time.Second); code != http.StatusBadRequest || !strings.Contains(body, "takes no body") {
			t.Fatalf("an advertisement with a body: %d %q", code, body)
		}
	}
	g.let("a-3")
	g.push("a-3")
	if err := g.wait("a-3"); err != nil {
		t.Fatalf("a push after two advertisements with a body: %v", err)
	}
}
