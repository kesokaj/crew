// Package gitendpoint is Crew's git endpoint for sandboxes (design 6.1), in its smallest form: the
// quarantine repositories, served over smart HTTP by git http-backend on the sandbox networks only.
// A run reads only the refs its token allows, and pushes only its own branch and the ref that keeps
// its work in progress, refs/crew/wip/<run>. Nothing else of the repository is served: no dumb HTTP,
// no objects by path.
//
// The refs a run may not read are hidden (uploadpack.hideRefs), so they are neither listed nor
// fetched by name; a push is checked by a hook that is the server's own, outside every repository,
// against the refs the run may push.
package gitendpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Access is what one request may do in one repository.
type Access struct {
	Repo string
	// Read and Push are full ref names, such as refs/heads/main.
	Read []string
	Push []string
	// Who names the caller in the server's log, such as the run's id.
	Who string
	// Run is the run that asks, when a run does.
	Run string
	// Paths are what a push to a ref may change, by its full name (13.8, 7.11); a ref with none is
	// tested for its name only.
	Paths map[string]PathRule
}

// ErrRefused is the one answer to a request that is not allowed.
var ErrRefused = errors.New("refused")

// Endpoint serves the repositories under Dir.
type Endpoint struct {
	Dir string // <data>/repos: one bare repository <name>.git each, and the hooks
	Git string // the git program; "" is git on the PATH
	// Auth says what a request may do in the repository it names. An error refuses it.
	Auth func(r *http.Request, repo string) (Access, error)
	Log  *slog.Logger
	// MaxPush bounds one push, on the wire and as git counts its pack. 0 is 512 MiB.
	MaxPush int64
	// TreeTime bounds the hook's check of a push's trees, and since 1.07 its test of the files a push
	// changes, each on its own (6.1). 0 is MaxTreeTime.
	TreeTime time.Duration
	// SlotWait bounds how long a push or a fetch waits for a slot, BodyTime how long its body may take
	// once it has one, and WriteTime how long its answer may (6.1, since 1.08). 0 is MaxSlotWait,
	// MaxBodyTime and MaxWriteTime.
	SlotWait, BodyTime, WriteTime time.Duration
	refTime                       time.Duration // each read of a push's refs; 0 is a minute
	slotsOnce                     sync.Once
	pushSlots                     *slots
	fetchSlots                    *slots
	// Self is the program that tests a push's paths when started with HookCommand: the server's own.
	// A request with path rules is refused when it is not set.
	Self string
	// OnPush hears of every ref a push changed, with its value before and after, once the push has
	// ended: the record of which run pushed which commits (6.7). For a new ref, old is where it left
	// main at the time of the push, so what it brought stays known after main has moved past it; it
	// is empty when the ref shares no history with main. When the refs could not be read, it hears of
	// main alone, with new empty, if the request may push main (since 1.08).
	OnPush func(acc Access, ref, old, new string)
}

var repoName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName says name can be a repository's name.
func ValidName(name string) bool { return repoName.MatchString(name) }

func (e *Endpoint) git() string {
	if e.Git != "" {
		return e.Git
	}
	// The CGI handler runs a program by its path, not by a name on the PATH.
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return "git"
}

// Path is where the repository name is on the machine.
func (e *Endpoint) Path(name string) string { return filepath.Join(e.Dir, name+".git") }

func (e *Endpoint) hooks() string { return filepath.Join(e.Dir, ".hooks") }

// Ensure makes the bare repository name, with main as its first branch, and the endpoint's hook.
// A repository that exists is kept.
func (e *Endpoint) Ensure(ctx context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q is not a repository's name", name)
	}
	if err := os.MkdirAll(e.hooks(), 0o755); err != nil {
		return err
	}
	if err := writeIfChanged(filepath.Join(e.hooks(), "pre-receive"), []byte(preReceive), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(e.Path(name), "HEAD")); err == nil {
		return nil
	}
	return e.run(ctx, "", nil, "init", "--bare", "--initial-branch=main", e.Path(name))
}

func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, data) {
		return os.Chmod(path, mode)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fetch brings every branch of url into the repository, for its first content. header, when set, is
// sent with the request, such as a token that can only read; it is given through the environment, so
// it is never on a command line, and taken out of an error (since 1.05).
func (e *Endpoint) Fetch(ctx context.Context, name, url, header string) error {
	var env []string
	if header != "" {
		env = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + header}
	}
	return Redact(e.run(ctx, e.Path(name), env, "fetch", "--no-tags", url, "+refs/heads/*:refs/heads/*"), header)
}

// Init gives an empty repository one commit on main that holds files, for a fixture with no other
// source. A repository with a main is not changed.
func (e *Endpoint) Init(ctx context.Context, name string, files map[string]string, message string) error {
	if e.run(ctx, e.Path(name), nil, "rev-parse", "--verify", "-q", "refs/heads/main") == nil {
		return nil
	}
	work, err := os.MkdirTemp("", "crew-init-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for path, body := range files {
		p := filepath.Join(work, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	env := []string{"GIT_AUTHOR_NAME=Crew", "GIT_AUTHOR_EMAIL=crew@localhost", "GIT_COMMITTER_NAME=Crew", "GIT_COMMITTER_EMAIL=crew@localhost"}
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main"},
		{"add", "-A"},
		{"commit", "-q", "--no-verify", "-m", message},
		{"push", "-q", e.Path(name), "main:refs/heads/main"},
	} {
		if err := e.run(ctx, work, env, args...); err != nil {
			return err
		}
	}
	return nil
}

// Ref returns the commit a ref of the repository points at, or "" when it has none.
func (e *Endpoint) Ref(ctx context.Context, name, ref string) string {
	cmd := exec.CommandContext(ctx, e.git(), "rev-parse", "--verify", "-q", ref+"^{commit}")
	cmd.Dir = e.Path(name)
	cmd.Env = gitEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (e *Endpoint) run(ctx context.Context, dir string, env []string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.git(), args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env)
	out := &LimitedText{Max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}

// gitEnv is the environment of a git command of the server: no configuration of the machine or of a
// user, so nothing but what is given here applies.
func gitEnv(extra []string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	return append(env, extra...)
}

// CleanEnv returns the caller's environment without any GIT_* variable, with GIT_CONFIG_NOSYSTEM=1,
// GIT_CONFIG_GLOBAL=/dev/null and GIT_TERMINAL_PROMPT=0, a default author and committer, and extra:
// a test helper that runs git never inherits GIT_DIR, GIT_WORK_TREE or GIT_INDEX_FILE from a shell or
// hook.
func CleanEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "GIT_") })
	defaults := []string{
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x",
	}
	for _, d := range defaults {
		key, _, _ := strings.Cut(d, "=")
		if !slices.ContainsFunc(extra, func(e string) bool { return strings.HasPrefix(e, key+"=") }) {
			env = append(env, d)
		}
	}
	return append(env, extra...)
}

// The three requests of smart HTTP; nothing else is served.
var servicePath = regexp.MustCompile(`^/git/([a-z0-9][a-z0-9-]{0,62})\.git/(info/refs|git-upload-pack|git-receive-pack)$`)

// ServeHTTP serves /git/<name>.git/... to a caller Auth allows.
func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := servicePath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	repo, what := m[1], m[2]
	service := what
	if what == "info/refs" {
		service = r.URL.Query().Get("service")
		if r.Method != http.MethodGet || (service != "git-upload-pack" && service != "git-receive-pack") {
			http.NotFound(w, r)
			return
		}
	} else if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	acc, err := e.Auth(r, repo)
	if err != nil || acc.Repo != repo {
		// Basic auth is how git offers a token it was given; it asks for one only after a 401.
		w.Header().Set("WWW-Authenticate", `Basic realm="crew"`)
		http.Error(w, "refused", http.StatusUnauthorized)
		return
	}
	if service == "git-receive-pack" && len(acc.Push) == 0 {
		http.Error(w, "this run may not push", http.StatusForbidden)
		return
	}
	if service == "git-receive-pack" && len(acc.Paths) > 0 && e.Self == "" {
		http.Error(w, "the files of this push cannot be tested", http.StatusInternalServerError)
		return
	}
	if _, err := os.Stat(filepath.Join(e.Path(repo), "HEAD")); err != nil {
		http.NotFound(w, r)
		return
	}
	// Only the headers smart HTTP needs reach git: every other one would reach it as an HTTP_ variable,
	// such as a Git_Protocol header that asks for version 2 under another spelling. Version 2 fetches
	// a hidden commit by its id: its fetch is a request of its own, and checks nothing against what
	// was listed. Version 0 checks that every commit asked for is reached from a ref that is shown.
	r.Header = smartHeaders(r.Header, service)
	e.work(w, r, acc, repo, service)
}

// mergeBase is the newest commit a and b share, or "" when they share none.
func (e *Endpoint) mergeBase(ctx context.Context, repo, a, b string) string {
	cmd := exec.CommandContext(ctx, e.git(), "merge-base", "--", a, b)
	cmd.Dir = e.Path(repo)
	cmd.Env = gitEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refValues reads the commits refs name in the repository; a ref that is not there is missing, and a
// git that cannot say is an error.
func (e *Endpoint) refValues(ctx context.Context, repo string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname)", "--"}, refs...)
	cmd := exec.CommandContext(ctx, e.git(), args...)
	cmd.Dir = e.Path(repo)
	cmd.Env = gitEnv(nil)
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if ref, id, ok := strings.Cut(l, " "); ok {
			out[ref] = id
		}
	}
	return out, nil
}

// smartHeaders are the headers of a request that reach git: the ones smart HTTP needs, and no other.
// git compresses a long list of what it wants; a push it never compresses, so a push is not inflated
// past its limit.
func smartHeaders(h http.Header, service string) http.Header {
	keep := http.Header{}
	for _, k := range []string{"Content-Type", "Accept"} {
		if v := h.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	if service == "git-upload-pack" && h.Get("Content-Encoding") == "gzip" {
		keep.Set("Content-Encoding", "gzip")
	}
	return keep
}

func (e *Endpoint) maxPush() int64 {
	if e.MaxPush > 0 {
		return e.MaxPush
	}
	return 512 << 20
}

// env is the environment of git http-backend for one request: the refs it may read, and the refs
// its push may update, as configuration that only this process sees.
func (e *Endpoint) env(acc Access) []string {
	cfg := [][2]string{
		// The pack a push brings, as git counts it, after any compression of the request.
		{"receive.maxInputSize", strconv.FormatInt(e.maxPush(), 10)},
		{"http.uploadpack", "true"},
		{"http.receivepack", strconv.FormatBool(len(acc.Push) > 0)},
		{"core.hooksPath", e.hooks()},
		{"uploadpack.allowAnySHA1InWant", "false"},
		{"uploadpack.allowReachableSHA1InWant", "false"},
		{"uploadpack.allowTipSHA1InWant", "false"},
		{"uploadpack.allowFilter", "false"},
		{"receive.denyDeletes", "true"},
		{"receive.fsckObjects", "true"},
		// Every ref is hidden, then the ones it may read or push are shown again: a later entry wins.
		{"transfer.hideRefs", "refs/"},
		{"transfer.hideRefs", "HEAD"},
	}
	seen := map[string]bool{}
	for _, ref := range append(append([]string{}, acc.Read...), acc.Push...) {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			cfg = append(cfg, [2]string{"transfer.hideRefs", "!" + ref})
		}
	}
	env := []string{
		"GIT_PROJECT_ROOT=" + e.Dir,
		"GIT_HTTP_EXPORT_ALL=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"HOME=/nonexistent",
		"REMOTE_USER=" + acc.Who,
		"CREW_PUSH_REFS=" + strings.Join(acc.Push, " "),
		envHook + "=" + e.Self,
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(cfg)),
	}
	env = append(env, e.treeBoundEnv()...)
	for i, kv := range cfg {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	if len(acc.Paths) > 0 {
		b, _ := json.Marshal(acc.Paths)
		env = append(env, envPaths+"="+string(b))
	}
	return append(env, "HTTP_GIT_PROTOCOL=")
}

// Token reads the token a request carries: a bearer, or the password of basic auth, which is how git
// sends a token it was given in a URL or by a helper.
func Token(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	return ""
}
