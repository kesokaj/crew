package versions

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kesokaj/crew/internal/harness"
)

const agyURL = "https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.5-4952523841339392/linux-x64/cli_linux_x64.tar.gz"

// vendor is a server that answers as the vendor's bucket would, reached for any address: the client
// dials it whatever host the address names, and trusts its certificate for that name.
func vendor(t *testing.T, h http.HandlerFunc) (*http.Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := srv.Client()
	tr := c.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	tr.TLSClientConfig.ServerName = "example.com"
	return &http.Client{Transport: tr}, &n
}

func lockOf(sum string) map[string]string {
	return map[string]string{"AGY_VERSION": "1.4.5", "AGY_URL": agyURL, "AGY_SHA512": sum}
}

func sumOf(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

// The server downloads agy's tarball as the candidate's versions.lock pins it, and hands the build the
// file only when its sha512 is the pin's (12.8, 16.2 step 4, since 0.57): from the vendor's address, the
// request names nothing of the installation (AGENTS.md, Clean 7), and the file is the answer whole.
func TestAgysTarballIsDownloadedAndChecked(t *testing.T) {
	defer roomy()()
	body := []byte("agy's tarball")
	var got *http.Request
	c, n := vendor(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write(body)
	})
	dir := filepath.Join(t.TempDir(), "b7.agy")
	path, err := fetchAgy(context.Background(), c, lockOf(sumOf(body)), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(body) || path != filepath.Join(dir, "cli_linux_x64.tar.gz") {
		t.Fatalf("%s: %q", path, b)
	}
	if n.Load() != 1 || got.Method != http.MethodGet || got.Host != "storage.googleapis.com" ||
		got.URL.Path != "/antigravity-public/antigravity-cli/1.4.5-4952523841339392/linux-x64/cli_linux_x64.tar.gz" {
		t.Fatalf("%d requests, the last %s %s%s", n.Load(), got.Method, got.Host, got.URL.Path)
	}
	for k, v := range got.Header {
		if k != "User-Agent" && k != "Accept-Encoding" {
			t.Errorf("the request names %s: %v", k, v)
		}
		if strings.Contains(strings.ToLower(strings.Join(v, " ")), "crew") {
			t.Errorf("the request names Crew in %s", k)
		}
	}
}

// Anything but the pin's file gives the build nothing, and leaves nothing on the machine: another
// checksum, an answer that is not 200, a redirect (not followed), an answer past the cap, a lock of
// another address (no request made), and a disk without room.
func TestAgysTarballThatIsNotThePinsIsRefused(t *testing.T) {
	defer roomy()()
	body := []byte("agy's tarball")
	old := maxAgy
	maxAgy = 64
	defer func() { maxAgy = old }()
	for name, c := range map[string]struct {
		h        http.HandlerFunc
		lock     map[string]string
		disk     uint64
		requests int32
		why      string
	}{
		"another checksum": {func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("another tarball")) }, lockOf(sumOf(body)), 0, 1, "sha512"},
		"a 404":            {func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 404) }, lockOf(sumOf(body)), 0, 1, "404"},
		"a redirect": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://evil.example/agy.tar.gz", http.StatusFound)
		}, lockOf(sumOf(body)), 0, 1, "redirect"},
		"past the cap": {func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(strings.Repeat("x", 65)))
		}, lockOf(sumOf([]byte(strings.Repeat("x", 65)))), 0, 1, "64 bytes"},
		"past the cap with no length": {func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(strings.Repeat("x", 40)))
			w.(http.Flusher).Flush()
			w.Write([]byte(strings.Repeat("x", 40)))
		}, lockOf(sumOf([]byte(strings.Repeat("x", 80)))), 0, 1, "64 bytes"},
		"another host": {func(w http.ResponseWriter, _ *http.Request) { w.Write(body) },
			map[string]string{"AGY_VERSION": "1.4.5", "AGY_URL": strings.Replace(agyURL, "storage.googleapis.com", "evil.example", 1), "AGY_SHA512": sumOf(body)}, 0, 0, "vendor"},
		"no pin":  {func(w http.ResponseWriter, _ *http.Request) { w.Write(body) }, map[string]string{}, 0, 0, "versions.lock"},
		"no room": {func(w http.ResponseWriter, _ *http.Request) { w.Write(body) }, lockOf(sumOf(body)), 1 << 30, 0, "bytes free"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.disk != 0 {
				f := harness.FreeSpace
				harness.FreeSpace = func(string) (uint64, error) { return c.disk, nil }
				defer func() { harness.FreeSpace = f }()
			}
			client, n := vendor(t, c.h)
			dir := filepath.Join(t.TempDir(), "b7.agy")
			path, err := fetchAgy(context.Background(), client, c.lock, dir)
			if err == nil || path != "" || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("%q, %v", path, err)
			}
			if strings.Contains(err.Error(), "evil") {
				t.Fatalf("the reason names the lock's address: %v", err)
			}
			if n.Load() != c.requests {
				t.Fatalf("%d requests", n.Load())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("left %v", entries)
			}
		})
	}
}

// The download stops reading at the cap: an answer that never ends is cut, not read to its end into
// the machine's disk (12.8, since 0.57).
func TestAgysDownloadStopsReadingAtTheCap(t *testing.T) {
	defer roomy()()
	old := maxAgy
	maxAgy = 64
	defer func() { maxAgy = old }()
	var whole atomic.Bool
	chunk := []byte(strings.Repeat("x", 64<<10))
	c, _ := vendor(t, func(w http.ResponseWriter, _ *http.Request) {
		for range 512 { // 32 MiB, far past what the sockets between hold
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		whole.Store(true)
	})
	if _, err := fetchAgy(context.Background(), c, lockOf(sumOf(body64())), filepath.Join(t.TempDir(), "b7.agy")); err == nil {
		t.Fatal("an answer past the cap was taken")
	}
	if whole.Load() {
		t.Fatal("the download read the whole answer past its cap")
	}
}

func body64() []byte { return []byte(strings.Repeat("x", 64)) }

// roomy gives the disk room for every download.
func roomy() func() {
	f := harness.FreeSpace
	harness.FreeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	return func() { harness.FreeSpace = f }
}
