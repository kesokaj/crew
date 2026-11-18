package gitstore_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
)

var me = gitstore.Identity{Name: "Op Erator", Email: "op@example.com"}

func newStore(t *testing.T) *gitstore.Store {
	t.Helper()
	dir := t.TempDir()
	return &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}, Protocols: "file"}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitstore.CleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushWork commits files on branch, cut from main, and pushes it to the quarantine repository, as a
// run does through the git endpoint.
func pushWork(t *testing.T, s *gitstore.Store, name, branch string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Quarantine.Path(name), ".")
	git(t, work, "checkout", "-q", "-b", branch)
	for p, body := range files {
		os.MkdirAll(filepath.Join(work, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(work, p), []byte(body), 0o644)
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "work")
	git(t, work, "push", "-q", "origin", branch)
}

func TestANewLocalRepositoryHasItsFirstCommitInBoth(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "# shop\n"}, "SF: first", me); err != nil {
		t.Fatal(err)
	}
	main := s.Main(ctx, "shop")
	if main == "" {
		t.Fatal("no main in the canonical repository")
	}
	if got := git(t, s.Quarantine.Path("shop"), "rev-parse", "refs/heads/main"); got != main {
		t.Fatalf("the quarantine main is %s, the canonical %s", got, main)
	}
	if got := git(t, s.Canonical("shop"), "log", "-1", "--format=%an <%ae> %s"); got != "Op Erator <op@example.com> SF: first" {
		t.Fatalf("the first commit is %q", got)
	}
	if err := s.CreateLocal(ctx, "shop", nil, "again", me); !errors.Is(err, gitstore.ErrExists) {
		t.Fatalf("a second repository of the same name: %v", err)
	}
	if !s.Exists("shop") {
		t.Fatal("Exists says no")
	}
}

func TestAnExistingRepositoryKeepsItsHistory(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	git(t, src, "init", "-q", "--initial-branch=main")
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "one")
	git(t, src, "tag", "v1")
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("b"), 0o644)
	git(t, src, "commit", "-q", "-am", "two")
	s := newStore(t)
	if err := s.CreateFrom(ctx, "old", "file://"+src, ""); err != nil {
		t.Fatal(err)
	}
	if got := git(t, s.Canonical("old"), "rev-list", "--count", "main"); got != "2" {
		t.Fatalf("main has %s commits", got)
	}
	if git(t, s.Canonical("old"), "rev-parse", "v1") == "" {
		t.Fatal("the tag is lost")
	}
}

// Only https is taken by default: a transport such as ext:: or file:// cannot be named in an address.
func TestAnAddressOfAnotherTransportIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Protocols = ""
	src := t.TempDir()
	git(t, src, "init", "-q", "--initial-branch=main")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "x")
	mark := filepath.Join(t.TempDir(), "ran")
	for _, u := range []string{"file://" + src, "ext::sh -c touch% " + mark} {
		if err := s.CreateFrom(ctx, "x", u, ""); err == nil {
			t.Fatalf("%s was fetched", u)
		}
		if s.Exists("x") {
			t.Fatalf("a failed creation left the repositories of %s", u)
		}
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the ext:: address ran its command")
	}
}

func TestARepositoryWithoutMainIsRefusedAndRemoved(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	git(t, src, "init", "-q", "--initial-branch=trunk")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "x")
	s := newStore(t)
	err := s.CreateFrom(ctx, "trunk", "file://"+src, "")
	if err == nil || !strings.Contains(err.Error(), "no branch main") {
		t.Fatalf("err = %v", err)
	}
	if s.Exists("trunk") {
		t.Fatal("the repositories were left behind")
	}
}

func TestABranchIsCutFromMainAndReadWhereRunsPush(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "x\n"}, "first", me); err != nil {
		t.Fatal(err)
	}
	const br = "task/SF-1-sign-in"
	if err := s.CreateBranch(ctx, "shop", br); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "shop", br); err != nil {
		t.Fatalf("a branch that exists: %v", err)
	}
	if err := s.CreateBranch(ctx, "shop", "main"); err == nil {
		t.Fatal("main was taken as a ticket's branch")
	}
	b, err := s.ReadBranch(ctx, "shop", br)
	if err != nil || b.Pushed {
		t.Fatalf("before a push: %+v, %v", b, err)
	}
	pushWork(t, s, "shop", br, map[string]string{"docs/auth.md": "x", "src/a.go": "y"})
	b, err = s.ReadBranch(ctx, "shop", br)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(b.Changed)
	if !b.Pushed || b.Ahead != 1 || !slices.Equal(b.Changed, []string{"docs/auth.md", "src/a.go"}) {
		t.Fatalf("after a push: %+v", b)
	}
}

// An export carries every branch and tag; read back, the canonical repository has them all and the
// quarantine one has the branches, so work in progress is where its runs left it.
func TestABundleReadsBackAsTheSameRepositories(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "x\n"}, "first", me); err != nil {
		t.Fatal(err)
	}
	pushWork(t, s, "shop", "task/SF-2-cart", map[string]string{"cart.go": "c"})
	git(t, s.Canonical("shop"), "tag", "v1", "main")
	var buf bytes.Buffer
	if err := s.Bundle(ctx, "shop", &buf); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "repo.bundle")
	os.WriteFile(file, buf.Bytes(), 0o600)
	s2 := newStore(t)
	if err := s2.CreateFromBundle(ctx, "shop", file); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1"} {
		if a, b := git(t, s.Canonical("shop"), "rev-parse", ref), git(t, s2.Canonical("shop"), "rev-parse", ref); a != b {
			t.Errorf("%s: %s, read back %s", ref, a, b)
		}
	}
	const pushed = "refs/heads/task/SF-2-cart"
	if a, b := git(t, s.Quarantine.Path("shop"), "rev-parse", pushed), git(t, s2.Canonical("shop"), "rev-parse", pushed); a != b {
		t.Errorf("the pushed branch: %s, read back %s", a, b)
	}
	// Writing the bundle did not change the canonical repository: what a sandbox pushed is not in it.
	if refs := git(t, s.Canonical("shop"), "for-each-ref", "--format=%(refname)", "refs/heads/"); refs != "refs/heads/main" {
		t.Errorf("the canonical repository's branches after the bundle: %q", refs)
	}
	if b, _ := s2.ReadBranch(ctx, "shop", "task/SF-2-cart"); !b.Pushed || b.Ahead != 1 {
		t.Fatalf("the quarantine copy of the branch: %+v", b)
	}
	os.WriteFile(file, []byte("not a bundle"), 0o600)
	if err := newStore(t).CreateFromBundle(ctx, "bad", file); err == nil {
		t.Fatal("a file that is not a bundle was read")
	}
}

// The fixture's quarantine repository from before projects were rows gets its canonical one.
func TestAdoptMakesTheCanonicalRepository(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Quarantine.Ensure(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Quarantine.Init(ctx, "fixture", map[string]string{"a": "b"}, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Adopt(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if s.Main(ctx, "fixture") != s.Quarantine.Ref(ctx, "fixture", "refs/heads/main") {
		t.Fatal("the canonical main differs")
	}
	if err := s.Adopt(ctx, "fixture"); err != nil {
		t.Fatalf("a second adopt: %v", err)
	}
}

// Of a bundle, the quarantine repository gets main and the branches Crew makes only.
func TestABundlePublishesOnlyCrewsBranches(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateLocal(ctx, "shop", map[string]string{"README.md": "x\n"}, "first", me); err != nil {
		t.Fatal(err)
	}
	git(t, s.Canonical("shop"), "branch", "weird/thing", "main")
	git(t, s.Canonical("shop"), "branch", "task/SF-1-ok", "main")
	var buf bytes.Buffer
	if err := s.Bundle(ctx, "shop", &buf); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "repo.bundle")
	os.WriteFile(file, buf.Bytes(), 0o600)
	s2 := newStore(t)
	if err := s2.CreateFromBundle(ctx, "shop", file); err != nil {
		t.Fatal(err)
	}
	got := git(t, s2.Quarantine.Path("shop"), "for-each-ref", "--format=%(refname)", "refs/heads/")
	if got != "refs/heads/main\nrefs/heads/task/SF-1-ok" {
		t.Fatalf("the quarantine repository has %q", got)
	}
}

// badRepo is a bare repository whose main holds a tree with .git in it, which git's fsck refuses.
func badRepo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	git(t, src, "init", "-q", "--bare", "--initial-branch=main")
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = src
	cmd.Env = gitstore.CleanEnv()
	cmd.Stdin = strings.NewReader("[core]\n\thooksPath = /tmp\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(strings.TrimSpace(string(out)))
	tree := append([]byte("100644 .git\x00"), raw...)
	cmd = exec.Command("git", "hash-object", "-w", "-t", "tree", "--literally", "--stdin")
	cmd.Dir = src
	cmd.Env = gitstore.CleanEnv()
	cmd.Stdin = bytes.NewReader(tree)
	if out, err = cmd.Output(); err != nil {
		t.Fatal(err)
	}
	commit := git(t, src, "commit-tree", strings.TrimSpace(string(out)), "-m", "bad")
	git(t, src, "update-ref", "refs/heads/main", commit)
	return src
}

// Objects from outside are checked as git's fsck checks them: a tree that holds .git is refused, by
// an address and in a bundle.
func TestObjectsFromOutsideAreChecked(t *testing.T) {
	ctx := context.Background()
	src := badRepo(t)
	s := newStore(t)
	if err := s.CreateFrom(ctx, "bad", "file://"+src, ""); err == nil {
		t.Fatal("a tree that holds .git was fetched")
	}
	if s.Exists("bad") {
		t.Fatal("the refused repository was left")
	}
	file := filepath.Join(t.TempDir(), "bad.bundle")
	git(t, src, "bundle", "create", "-q", file, "--branches")
	if err := s.CreateFromBundle(ctx, "bad", file); err == nil {
		t.Fatal("a bundle with a tree that holds .git was read")
	}
	if s.Exists("bad") {
		t.Fatal("the refused bundle's repository was left")
	}
}

// A creation that fails while it makes the repository leaves no claim behind: the name is free again.
func TestAFailedCreationFreesTheName(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.CreateLocal(ctx, "shop", map[string]string{"a": "b"}, "first", me); err == nil {
		t.Fatal("a creation with a cancelled context succeeded")
	}
	if s.Exists("shop") {
		t.Fatal("the failed creation's folder was left, and the name is taken")
	}
	if err := s.CreateLocal(context.Background(), "shop", map[string]string{"a": "b"}, "first", me); err != nil {
		t.Fatalf("the retry: %v", err)
	}
}

// Of two that make the same name at once, one makes it and the other is refused, and the one that
// made it keeps its repositories.
func TestTwoCreatesOfOneNameAtOnce(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		s := newStore(t)
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() { errs <- s.CreateLocal(ctx, "shop", map[string]string{"README.md": "x\n"}, "first", me) }()
		}
		e1, e2 := <-errs, <-errs
		ok := 0
		for _, e := range []error{e1, e2} {
			if e == nil {
				ok++
			} else if !errors.Is(e, gitstore.ErrExists) {
				t.Fatalf("round %d: %v", round, e)
			}
		}
		if ok != 1 || s.Main(ctx, "shop") == "" || s.Quarantine.Ref(ctx, "shop", "refs/heads/main") == "" {
			t.Fatalf("round %d: %d made it (%v, %v); main %q", round, ok, e1, e2, s.Main(ctx, "shop"))
		}
	}
}

// pushRepo is a store with one repository that has main, and an ssh that stands in for a remote.
func pushRepo(t *testing.T, ssh string) (*gitstore.Store, []string) {
	t.Helper()
	dir := t.TempDir()
	s := &gitstore.Store{Dir: filepath.Join(dir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(dir, "repos")}}
	if err := s.CreateLocal(context.Background(), "shop", map[string]string{"README.md": "x\n"}, "first", me); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	return s, []string{"GIT_SSH_COMMAND=" + bin, "PIDFILE=" + filepath.Join(dir, "pid")}
}

func alive(t *testing.T, pidfile string) bool {
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	// A process killed whose parent went too stays a zombie in a container whose first process
	// reaps nothing: it is dead all the same.
	for i := 0; i < 20; i++ {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		if f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(f) > 0 && f[0] == "Z" {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// A push that stalls ends at its context with its whole group: the ssh and what the ssh started.
func TestAStalledPushEndsWithItsWholeGroup(t *testing.T) {
	s, env := pushRepo(t, "#!/bin/sh\nsleep 30 &\necho $! > \"$PIDFILE\"\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Push(ctx, "shop", "git@github.com:op/shop.git", []string{"refs/heads/main"}, env); err == nil {
		t.Fatal("a stalled push succeeded")
	}
	pidfile := strings.TrimPrefix(env[1], "PIDFILE=")
	if took := time.Since(start); took > 4*time.Second || alive(t, pidfile) {
		t.Fatalf("the push took %v, and the ssh's child is alive: %v", took, alive(t, pidfile))
	}
}

// What leaves the group and keeps the output open is not waited for long.
func TestAPushIsNotHeldByWhatLeftItsGroup(t *testing.T) {
	s, env := pushRepo(t, "#!/bin/sh\nsetsid sh -c 'echo $$ > \"$PIDFILE\"; exec sleep 30' </dev/null >/dev/null &\nexec sleep 30\n")
	pidfile := strings.TrimPrefix(env[1], "PIDFILE=")
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidfile); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	s.Push(ctx, "shop", "git@github.com:op/shop.git", []string{"refs/heads/main"}, env)
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("the push waited %v for a process outside its group", took)
	}
}

// CleanEnv drops the caller's GIT_* variables so a test helper that runs git never writes into a
// repository named by GIT_DIR, GIT_WORK_TREE or GIT_INDEX_FILE in the caller's environment (register
// row 30, CREW-36).
func TestCleanEnvDropsCallersGitVariables(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "sentinel.git")
	sentinelWork := filepath.Join(t.TempDir(), "sentinel-work")
	t.Setenv("GIT_DIR", sentinel)
	t.Setenv("GIT_WORK_TREE", sentinelWork)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "sentinel-index"))
	work := t.TempDir()
	git(t, work, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "a.txt")
	git(t, work, "commit", "-q", "-m", "one")
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sentinel GIT_DIR was touched: %v", err)
	}
	if _, err := os.Stat(sentinelWork); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sentinel GIT_WORK_TREE was touched: %v", err)
	}
}

func TestTreeAndBlob(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateLocal(ctx, "shop", map[string]string{
		"README.md":     "# Shop\n",
		"bin/image.png": "\x89\x50\x00\x01",
	}, "initial", me); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Tree(ctx, "shop", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "README.md" || entries[1].Path != "bin/image.png" {
		t.Fatalf("Tree = %+v", entries)
	}
	b, err := s.ReadBlob(ctx, "shop", "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if b.Binary || b.Content != "# Shop\n" {
		t.Fatalf("ReadBlob(README.md) = %+v", b)
	}
	bin, err := s.ReadBlob(ctx, "shop", "main", "bin/image.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bin.Binary || bin.Content != "" {
		t.Fatalf("ReadBlob(bin/image.png) = %+v", bin)
	}
	if _, err := s.ReadBlob(ctx, "shop", "main", "../secret"); !errors.Is(err, gitstore.ErrNoRev) {
		t.Fatalf("ReadBlob(../secret) err = %v", err)
	}
}
