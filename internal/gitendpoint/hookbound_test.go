package gitendpoint

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An index file a push brings is read only up to MaxIndexBytes, its size taken from the listing first:
// past it the push is refused with a fixed text, and within it what it names is read as before (13.8,
// since 1.04).
func TestAPushOfAnIndexFilePastItsBoundIsRefused(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, srv := endpoint(t, func(e *Endpoint) {
		e.Self = self
		auth := e.Auth
		e.Auth = func(r *http.Request, repo string) (Access, error) {
			a, err := auth(r, repo)
			if err == nil && a.Who == "run-a" {
				a.Paths = map[string]PathRule{"refs/heads/task/1-a": {Base: "refs/heads/main", Allow: []string{Everything, ".agents/plugins.json"}}}
			}
			return a, err
		}
	})
	work := t.TempDir()
	if out, err := git(t, work, "token-a", "clone", "-q", "-b", "task/1-a", srv.URL+"/git/fixture.git", "c"); err != nil {
		t.Fatal(out)
	}
	c := filepath.Join(work, "c")
	push := func(size int) (string, error) {
		t.Helper()
		os.MkdirAll(filepath.Join(c, ".agents"), 0o755)
		head := `{"entries": [{"path": "tools"}]`
		os.WriteFile(filepath.Join(c, ".agents/plugins.json"), []byte(head+strings.Repeat(" ", size-len(head)-1)+"}"), 0o644)
		os.MkdirAll(filepath.Join(c, "tools"), 0o755)
		os.WriteFile(filepath.Join(c, "tools/hook.sh"), []byte("x\n"), 0o644)
		git(t, c, "", "add", "-A")
		if out, err := git(t, c, "", "commit", "-qm", "work"); err != nil {
			t.Fatal(out)
		}
		return git(t, c, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a")
	}
	// Within the bound, the folder it names instructs agents, and its file is refused for it.
	if out, err := push(MaxIndexBytes); err == nil || !strings.Contains(out, "tools/hook.sh instructs agents") {
		t.Fatalf("an index file within the bound: %v %s", err, out)
	}
	git(t, c, "", "reset", "-q", "--hard", "HEAD~1")
	if out, err := push(MaxIndexBytes + 1); err == nil || !strings.Contains(out, "an index file of this push is longer than 1048576 bytes") {
		t.Fatalf("an index file past the bound: %v %s", err, out)
	}
}

// The hook's listing of what a push changes is bounded, and an index file it cannot read refuses the
// push, never names nothing (since 1.04).
func TestTheHooksReadsAreBoundedAndRefuseWhatTheyCannotRead(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}, {"checkout", "-q", "-b", "task/1-a"}} {
		if out, err := git(t, work, "", args...); err != nil {
			t.Fatal(out)
		}
	}
	os.MkdirAll(filepath.Join(work, ".agents"), 0o755)
	os.WriteFile(filepath.Join(work, ".agents/plugins.json"), []byte(`{"entries": [{"path": "tools"}]}`), 0o644)
	os.WriteFile(filepath.Join(work, strings.Repeat("n", 100)), []byte("x\n"), 0o644)
	git(t, work, "", "add", "-A")
	if out, err := git(t, work, "", "commit", "-qm", "work"); err != nil {
		t.Fatal(out)
	}
	commit, _ := git(t, work, "", "rev-parse", "HEAD")
	commit = strings.TrimSpace(commit)
	t.Chdir(work)
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/plugins.json"}}
	within := hookBounds{list: MaxListBytes, index: MaxIndexBytes}
	if refused, err := testPush(context.Background(), rule, commit, within); err != nil || len(refused) != 0 {
		t.Fatalf("within the bounds: %v %v", refused, err)
	}
	if _, err := testPush(context.Background(), rule, commit, hookBounds{list: 64, index: MaxIndexBytes}); !errors.Is(err, errPushList) {
		t.Fatalf("a listing past its bound: %v", err)
	}
	// A git that cannot read a blob, or list a path, as when it is killed.
	fakes := t.TempDir()
	for name, fails := range map[string]string{"blob": "cat-file", "listing": "ls-tree"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(fakes, name)
			os.MkdirAll(dir, 0o755)
			script := "#!/bin/sh\nfor a; do [ \"$a\" = " + fails + " ] && exit 1; done\nexec " + real + " \"$@\"\n"
			os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if refused, err := testPush(context.Background(), rule, commit, within); !errors.Is(err, errIndexRead) {
				t.Fatalf("an index file git cannot read: %v %v", refused, err)
			}
		})
	}
}

// What git says when a command of the endpoint fails is kept to its start (since 1.04).
func TestAnEndpointCommandsErrorTextIsCut(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nhead -c 1048576 /dev/zero | tr '\\0' e >&2\nexit 1\n"), 0o755)
	e := &Endpoint{Dir: t.TempDir(), Git: fake}
	if err := e.run(context.Background(), e.Dir, nil, "update-ref", "refs/heads/x", "HEAD"); err == nil || len(err.Error()) > 65<<10 {
		t.Fatalf("the error text: %d bytes", len(fmt.Sprint(err)))
	}
}
