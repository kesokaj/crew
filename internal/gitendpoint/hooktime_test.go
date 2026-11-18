package gitendpoint

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test of the files a push changes runs within the hook's time limit too, and is killed 5 seconds
// after it, as the server's program takes the signal to stop as a request: a test that does not end in
// time refuses the push with a fixed text, one that ends in time says as before (6.1, 13.8; since 1.07).
func TestTheTestOfAPushsFilesRunsWithinTheTimeLimit(t *testing.T) {
	for _, c := range []struct {
		name, program, says string
		ok                  bool
	}{
		{"a test that takes the signal as a request and goes on", "trap '' TERM\nsleep 20\nexit 0\n", "did not end within 1 seconds, or was stopped", false},
		{"a test the kernel kills, as for its memory", "kill -KILL $$\n", "did not end within 1 seconds, or was stopped", false},
		{"a test that ends in time and refuses", "echo 'crew: refused here' >&2\nexit 1\n", "refused here", false},
		{"a test that ends in time and takes it", "cat >/dev/null\nexit 0\n", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "crew")
			if err := os.WriteFile(program, []byte("#!/bin/sh\n"+c.program), 0o755); err != nil {
				t.Fatal(err)
			}
			_, srv := endpoint(t, func(e *Endpoint) {
				e.Self, e.TreeTime = program, time.Second
				auth := e.Auth
				e.Auth = func(r *http.Request, repo string) (Access, error) {
					a, err := auth(r, repo)
					if err == nil && a.Who == "run-a" {
						a.Paths = map[string]PathRule{"refs/heads/task/1-a": {Base: "refs/heads/main", Allow: []string{Everything}}}
					}
					return a, err
				}
			})
			work := t.TempDir()
			if out, err := git(t, work, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "c"); err != nil {
				t.Fatal(out)
			}
			dir := filepath.Join(work, "c")
			os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
			git(t, dir, "", "add", "-A")
			if out, err := git(t, dir, "", "commit", "-qm", "work"); err != nil {
				t.Fatal(out)
			}
			start := time.Now()
			out, err := git(t, dir, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a")
			took := time.Since(start)
			if (err == nil) != c.ok || !strings.Contains(out, c.says) || took > 15*time.Second {
				t.Fatalf("%v after %v: %s", err, took, out)
			}
		})
	}
}
