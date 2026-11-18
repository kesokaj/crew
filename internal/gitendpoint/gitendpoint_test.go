package gitendpoint

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// endpoint serves one repository, fixture, with main and two task branches, to two runs: run a may
// read main and its branch and push its branch and its wip ref; run b only reads main.
func endpoint(t *testing.T, set ...func(*Endpoint)) (*Endpoint, *httptest.Server) {
	t.Helper()
	e := &Endpoint{Dir: t.TempDir()}
	ctx := context.Background()
	if err := e.Ensure(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.Init(ctx, "fixture", map[string]string{"README.md": "fixture\n"}, "The fixture"); err != nil {
		t.Fatal(err)
	}
	main := e.Ref(ctx, "fixture", "refs/heads/main")
	for _, b := range []string{"task/1-a", "task/2-b"} {
		if err := e.run(ctx, e.Path("fixture"), nil, "update-ref", "refs/heads/"+b, main); err != nil {
			t.Fatal(err)
		}
	}
	access := map[string]Access{
		"token-a": {Repo: "fixture", Who: "run-a",
			Read: []string{"refs/heads/main", "refs/heads/task/1-a"},
			Push: []string{"refs/heads/task/1-a", "refs/crew/wip/run-a"}},
		"token-b": {Repo: "fixture", Who: "run-b", Read: []string{"refs/heads/main"}},
		// The operator's terminal in edit mode: main, and the branches under a prefix of its own.
		"token-t": {Repo: "fixture", Who: "terminal",
			Read: []string{"refs/heads/"},
			Push: []string{"refs/heads/main", "refs/heads/you/"}},
	}
	e.Auth = func(r *http.Request, repo string) (Access, error) {
		a, ok := access[Token(r)]
		if !ok {
			return Access{}, ErrRefused
		}
		return a, nil
	}
	// What a test sets is set before the server serves anything.
	for _, f := range set {
		f(e)
	}
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return e, srv
}

// git runs git as a sandbox would, with the token as its bearer.
func git(t *testing.T, dir, token string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-c", "http.extraHeader=Authorization: Bearer " + token}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Env = gitEnv([]string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"})
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestARunSeesOnlyTheRefsItMayRead(t *testing.T) {
	_, srv := endpoint(t)
	out, err := git(t, t.TempDir(), "token-a", "ls-remote", srv.URL+"/git/fixture.git")
	if err != nil {
		t.Fatal(out)
	}
	if !strings.Contains(out, "refs/heads/main") || !strings.Contains(out, "refs/heads/task/1-a") || strings.Contains(out, "task/2-b") {
		t.Fatalf("%s", out)
	}
	out, err = git(t, t.TempDir(), "token-b", "ls-remote", srv.URL+"/git/fixture.git")
	if err != nil || strings.Contains(out, "task/") {
		t.Fatalf("%v %s", err, out)
	}
}

// A hidden ref cannot be fetched by name, nor by the id of its commit when no shown ref reaches it.
func TestAHiddenRefCannotBeFetched(t *testing.T) {
	e, srv := endpoint(t)
	ctx := context.Background()
	// A commit only task/2-b has.
	work := t.TempDir()
	if out, err := git(t, work, "token-a", "clone", "-q", "-b", "main", srv.URL+"/git/fixture.git", "c"); err != nil {
		t.Fatal(out)
	}
	c := filepath.Join(work, "c")
	os.WriteFile(filepath.Join(c, "secret.txt"), []byte("b's work\n"), 0o644)
	git(t, c, "", "add", "-A")
	git(t, c, "", "commit", "-qm", "b")
	if out, err := git(t, c, "", "push", "-q", e.Path("fixture"), "HEAD:refs/heads/task/2-b"); err != nil {
		t.Fatal(out)
	}
	hidden := e.Ref(ctx, "fixture", "refs/heads/task/2-b")
	dir := t.TempDir()
	if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "main", srv.URL+"/git/fixture.git", "a"); err != nil {
		t.Fatal(out)
	}
	a := filepath.Join(dir, "a")
	if out, err := git(t, a, "token-a", "fetch", "origin", "refs/heads/task/2-b"); err == nil {
		t.Fatalf("fetched a hidden branch by name: %s", out)
	}
	for _, v := range []string{"0", "2"} {
		if out, err := git(t, a, "token-a", "-c", "protocol.version="+v, "fetch", "origin", hidden); err == nil {
			t.Fatalf("protocol %s fetched a hidden commit by its id: %s", v, out)
		}
	}
}

func TestARunPushesOnlyItsOwnRefs(t *testing.T) {
	// Every ref a push changed is heard of once, with its value before and after; a refused push
	// changes nothing and is not heard of.
	var heard []string
	e, srv := endpoint(t, func(e *Endpoint) {
		e.OnPush = func(acc Access, ref, old, new string) { heard = append(heard, acc.Who+" "+ref+" "+old+" "+new) }
	})
	ctx := context.Background()
	dir := t.TempDir()
	url := srv.URL + "/git/fixture.git"
	if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "task/1-a", url, "a"); err != nil {
		t.Fatal(out)
	}
	a := filepath.Join(dir, "a")
	os.WriteFile(filepath.Join(a, "work.txt"), []byte("work\n"), 0o644)
	git(t, a, "", "add", "-A")
	git(t, a, "", "commit", "-qm", "work")
	head, _ := git(t, a, "", "rev-parse", "HEAD")
	head = strings.TrimSpace(head)

	if out, err := git(t, a, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a"); err != nil {
		t.Fatalf("its own branch: %s", out)
	}
	if out, err := git(t, a, "token-a", "push", "-q", "origin", "HEAD:refs/crew/wip/run-a"); err != nil {
		t.Fatalf("its own wip ref: %s", out)
	}
	if e.Ref(ctx, "fixture", "refs/heads/task/1-a") != head || e.Ref(ctx, "fixture", "refs/crew/wip/run-a") != head {
		t.Fatal("the push did not land")
	}
	before := e.Ref(ctx, "fixture", "refs/heads/main")
	for _, ref := range []string{"refs/heads/main", "refs/heads/task/2-b", "refs/crew/wip/run-b", "refs/heads/new"} {
		if out, err := git(t, a, "token-a", "push", "-q", "origin", "HEAD:"+ref); err == nil {
			t.Errorf("pushed %s: %s", ref, out)
		}
	}
	if out, err := git(t, a, "token-a", "push", "-q", "origin", ":refs/heads/task/1-a"); err == nil {
		t.Errorf("removed its branch: %s", out)
	}
	if e.Ref(ctx, "fixture", "refs/heads/main") != before || e.Ref(ctx, "fixture", "refs/heads/task/2-b") == head ||
		e.Ref(ctx, "fixture", "refs/heads/task/1-a") != head {
		t.Fatal("a refused push changed a ref")
	}
	if out, err := git(t, a, "token-b", "push", "-q", url, "HEAD:refs/heads/task/1-a"); err == nil {
		t.Fatalf("a run that may not push pushed: %s", out)
	}
	if want := []string{"run-a refs/heads/task/1-a " + before + " " + head, "run-a refs/crew/wip/run-a " + before + " " + head}; !slices.Equal(heard, want) {
		t.Errorf("heard %q, want %q", heard, want)
	}
}

func TestOnlyTheThreeRequestsOfSmartHTTPAreServed(t *testing.T) {
	_, srv := endpoint(t)
	for _, p := range []string{"/git/fixture.git/HEAD", "/git/fixture.git/objects/info/packs", "/git/fixture.git/config",
		"/git/fixture.git/info/refs", "/git/../fixture.git/info/refs?service=git-upload-pack", "/v1/creds"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		req.Header.Set("Authorization", "Bearer token-a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/git/fixture.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

func TestARunReachesOnlyItsRepository(t *testing.T) {
	e, srv := endpoint(t)
	if err := e.Ensure(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if out, err := git(t, t.TempDir(), "token-a", "ls-remote", srv.URL+"/git/other.git"); err == nil {
		t.Fatalf("%s", out)
	}
}

func TestEnsureRefusesANameThatIsAPath(t *testing.T) {
	e := &Endpoint{Dir: t.TempDir()}
	for _, n := range []string{"", "../x", "a/b", "A", ".hooks"} {
		if err := e.Ensure(context.Background(), n); err == nil {
			t.Errorf("%q", n)
		}
	}
	if !errors.Is(ErrRefused, ErrRefused) {
		t.Fatal()
	}
}

// A header that asks for protocol version 2 under another spelling does not reach git: every request
// is served as version 0, which checks that a commit asked for is reached from a ref that is shown.
func TestNoSpellingOfTheProtocolHeaderReachesGit(t *testing.T) {
	_, srv := endpoint(t)
	for _, name := range []string{"Git-Protocol", "Git_Protocol", "git_protocol"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/git/fixture.git/info/refs?service=git-upload-pack", nil)
		req.Header.Set("Authorization", "Bearer token-a")
		req.Header[name] = []string{"version=2"}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || strings.Contains(string(b), "version 2") || !strings.Contains(string(b), "refs/heads/main") {
			t.Errorf("%s: %d %q", name, resp.StatusCode, b)
		}
	}
}

// A push larger than git's post buffer comes chunked, which a CGI program cannot take: the endpoint
// reads it first. A push larger than the limit is refused, also as git counts its pack.
func TestAPushIsTakenInChunksAndBounded(t *testing.T) {
	_, srv := endpoint(t, func(e *Endpoint) { e.MaxPush = 600 << 10 })
	dir := t.TempDir()
	url := srv.URL + "/git/fixture.git"
	if out, err := git(t, dir, "token-a", "clone", "-q", "-b", "task/1-a", url, "a"); err != nil {
		t.Fatal(out)
	}
	a := filepath.Join(dir, "a")
	write := func(name string, size int) {
		b := make([]byte, size)
		rand.Read(b)
		os.WriteFile(filepath.Join(a, name), b, 0o644)
		git(t, a, "", "add", "-A")
		git(t, a, "", "commit", "-qm", name)
	}
	write("medium.bin", 200<<10)
	if out, err := git(t, a, "token-a", "-c", "http.postBuffer=4096", "push", "-q", "origin", "HEAD:refs/heads/task/1-a"); err != nil {
		t.Fatalf("a chunked push: %v %s", err, out)
	}
	write("large.bin", 900<<10)
	if out, err := git(t, a, "token-a", "push", "-q", "origin", "HEAD:refs/heads/task/1-a"); err == nil {
		t.Fatalf("a push over the limit was taken: %s", out)
	}
	if out, err := git(t, a, "token-a", "-c", "http.postBuffer=4096", "push", "-q", "origin", "HEAD:refs/heads/task/1-a"); err == nil {
		t.Fatalf("a chunked push over the limit was taken: %s", out)
	}
}

// A push reaches git uncompressed, and git bounds its pack itself, also when the wire limit is not
// reached first.
func TestAPushIsNotInflatedAndGitBoundsItsPack(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Type", "application/x-git-receive-pack-request")
	h.Set("Git_Protocol", "version=2")
	if got := smartHeaders(h, "git-receive-pack"); got.Get("Content-Encoding") != "" || len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if got := smartHeaders(h, "git-upload-pack"); got.Get("Content-Encoding") != "gzip" || got.Get("Git_Protocol") != "" {
		t.Fatalf("%v", got)
	}
	e := &Endpoint{Dir: "/x", MaxPush: 1234}
	env := strings.Join(e.env(Access{Push: []string{"refs/heads/a"}}), "\n")
	if !strings.Contains(env, "=receive.maxInputSize\nGIT_CONFIG_VALUE_0=1234") || !strings.HasSuffix(env, "\nHTTP_GIT_PROTOCOL=") {
		t.Fatalf("%s", env)
	}
}

// A prefix allows the refs under it and not itself; main only goes on from where it is, and a push
// that would rewrite it is refused.
func TestATerminalPushesMainForwardAndItsOwnBranches(t *testing.T) {
	var heard []string
	e, srv := endpoint(t, func(e *Endpoint) {
		e.OnPush = func(acc Access, ref, old, new string) { heard = append(heard, ref) }
	})
	ctx := context.Background()
	dir := t.TempDir()
	url := srv.URL + "/git/fixture.git"
	if out, err := git(t, dir, "token-t", "clone", "-q", url, "w"); err != nil {
		t.Fatal(out)
	}
	w := filepath.Join(dir, "w")
	before := e.Ref(ctx, "fixture", "refs/heads/main")
	git(t, w, "", "checkout", "-q", "-B", "main", "origin/main")
	os.WriteFile(filepath.Join(w, "op.txt"), []byte("by hand\n"), 0o644)
	git(t, w, "", "add", "-A")
	git(t, w, "", "commit", "-qm", "by hand")
	if out, err := git(t, w, "token-t", "push", "-q", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("main, forward: %s", out)
	}
	if out, err := git(t, w, "token-t", "push", "-q", "origin", "HEAD:refs/heads/you/try"); err != nil {
		t.Fatalf("a branch of its own: %s", out)
	}
	for _, ref := range []string{"refs/heads/you", "refs/heads/task/1-a", "refs/heads/yours"} {
		if out, err := git(t, w, "token-t", "push", "-q", "origin", "HEAD:"+ref); err == nil {
			t.Errorf("pushed %s: %s", ref, out)
		}
	}
	// A main that leaves out what is there.
	git(t, w, "", "reset", "-q", "--hard", before)
	os.WriteFile(filepath.Join(w, "other.txt"), []byte("other\n"), 0o644)
	git(t, w, "", "add", "-A")
	git(t, w, "", "commit", "-qm", "another history")
	if out, err := git(t, w, "token-t", "push", "-q", "-f", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "main only goes on") {
		t.Errorf("main was rewritten: %v %s", err, out)
	}
	if !slices.Equal(heard, []string{"refs/heads/main", "refs/heads/you/try"}) {
		t.Errorf("heard %v", heard)
	}
}
