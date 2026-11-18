// Package gitstore keeps the two repositories of each project on the machine (design 6.1): the
// canonical one, the source of truth for the code, which no sandbox reaches; and the quarantine one,
// which the git endpoint serves to sandboxes. Only this package writes main, and it writes it to the
// canonical repository first and then to the quarantine one.
package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// Identity is who a commit of Crew's is made as: the operator's git identity from Setup (12.6).
type Identity struct {
	Name  string
	Email string
}

// Store holds the repositories.
type Store struct {
	Dir        string                // <data>/canonical: one bare repository <name>.git each
	Quarantine *gitendpoint.Endpoint // the git endpoint's repositories, <data>/repos
	Git        string                // the git program; "" is git on the PATH
	// Protocols are the transports CreateFrom may use, as GIT_ALLOW_PROTOCOL takes them. "" is https
	// only, so no other transport, such as ext::, can be named in an address. Tests add file.
	Protocols string
	// MaxOutput lowers the bound of one read of git's output, MaxOutput, which nothing raises; 0 is
	// MaxOutput. Tests make it small.
	MaxOutput int64
}

// ErrExists is returned when a repository of that name is there already.
var ErrExists = errors.New("a repository of that name exists")

// ErrNoMain is returned when a repository brought in from outside has no main.
var ErrNoMain = errors.New("the repository has no branch main")

// Canonical is where the canonical repository of name is.
func (s *Store) Canonical(name string) string { return filepath.Join(s.Dir, name+".git") }

// Exists says whether either repository of name is there.
func (s *Store) Exists(name string) bool {
	for _, p := range []string{s.Canonical(name), s.Quarantine.Path(name)} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (s *Store) prepare(ctx context.Context, name string) error {
	if !gitendpoint.ValidName(name) {
		return fmt.Errorf("%q is not a repository's name", name)
	}
	if s.Exists(name) {
		return ErrExists
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	// The canonical folder is the claim: of two that make the same name at once, one makes it and
	// the other gets ErrExists, and so never removes what the first one made.
	if err := os.Mkdir(s.Canonical(name), 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	if err := s.run(ctx, "", nil, "init", "-q", "--bare", "--initial-branch=main", s.Canonical(name)); err != nil {
		// The folder is this call's own, since its Mkdir made it: removed, the name is free again.
		os.RemoveAll(s.Canonical(name))
		return err
	}
	return nil
}

// CreateLocal makes a new repository with one commit on main that holds files (6.6, "New local
// repository"). Either both repositories are made, or neither is left.
func (s *Store) CreateLocal(ctx context.Context, name string, files map[string]string, message string, id Identity) (err error) {
	if err := s.prepare(ctx, name); err != nil {
		return err
	}
	defer s.removeOnError(name, &err)
	work, err := os.MkdirTemp("", "crew-new-*")
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
	env := id.env()
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main"},
		{"add", "-A"},
		{"commit", "-q", "--no-verify", "-m", message},
		{"push", "-q", s.Canonical(name), "main:refs/heads/main"},
	} {
		if err := s.run(ctx, work, env, args...); err != nil {
			return err
		}
	}
	return s.publish(ctx, name, []string{"refs/heads/main"})
}

// CreateFrom makes the repositories from an existing one at url (6.6, "Existing repository"), with
// its branches and tags, and so its history. header, when set, is sent with the requests, such as a
// token that can only read; it goes through the environment, never a command line. The repository
// must have a main.
func (s *Store) CreateFrom(ctx context.Context, name, url, header string) (err error) {
	if err := s.prepare(ctx, name); err != nil {
		return err
	}
	defer s.removeOnError(name, &err)
	protocols := s.Protocols
	if protocols == "" {
		protocols = "https"
	}
	env := []string{"GIT_ALLOW_PROTOCOL=" + protocols}
	if header != "" {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0="+header)
	}
	if err := s.run(ctx, s.Canonical(name), env, "-c", "fetch.fsckObjects=true", "fetch", "-q", "--", url,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return gitendpoint.Redact(err, header)
	}
	if s.ref(ctx, s.Canonical(name), "refs/heads/main") == "" {
		return ErrNoMain
	}
	return s.publish(ctx, name, []string{"refs/heads/main"})
}

// CreateFromBundle makes the repositories from a bundle that Bundle wrote: every branch and tag in
// the canonical one, and every branch in the quarantine one, so work in progress is where its runs
// left it. The bundle is checked first.
func (s *Store) CreateFromBundle(ctx context.Context, name, bundle string) (err error) {
	if err := s.prepare(ctx, name); err != nil {
		return err
	}
	defer s.removeOnError(name, &err)
	if err := s.run(ctx, s.Canonical(name), nil, "bundle", "verify", "-q", bundle); err != nil {
		return fmt.Errorf("the repository's bundle: %w", err)
	}
	if err := s.run(ctx, s.Canonical(name), nil, "-c", "fetch.fsckObjects=true", "fetch", "-q", "--", bundle,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return err
	}
	if s.ref(ctx, s.Canonical(name), "refs/heads/main") == "" {
		return ErrNoMain
	}
	heads, err := s.refs(ctx, s.Canonical(name), "refs/heads/")
	if err != nil {
		return err
	}
	// The quarantine repository gets main and the branches Crew makes, nothing else of the bundle.
	var publish []string
	for _, h := range heads {
		if b := strings.TrimPrefix(h, "refs/heads/"); b == "main" || ValidBranch(b) {
			publish = append(publish, h)
		}
	}
	return s.publish(ctx, name, publish)
}

// Adopt makes the canonical repository of a quarantine repository that exists without one, such as
// the fixture's from before projects were rows. One that exists is kept.
func (s *Store) Adopt(ctx context.Context, name string) error {
	if _, err := os.Stat(filepath.Join(s.Canonical(name), "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if err := s.run(ctx, "", nil, "init", "-q", "--bare", "--initial-branch=main", s.Canonical(name)); err != nil {
		return err
	}
	return s.run(ctx, s.Canonical(name), nil, "fetch", "-q", "--", s.Quarantine.Path(name), "+refs/heads/main:refs/heads/main")
}

// publish makes the quarantine repository and gives it refs of the canonical one.
func (s *Store) publish(ctx context.Context, name string, refs []string) error {
	if err := s.Quarantine.Ensure(ctx, name); err != nil {
		return err
	}
	args := []string{"push", "-q", "--", s.Quarantine.Path(name)}
	for _, r := range refs {
		args = append(args, r+":"+r)
	}
	return s.run(ctx, s.Canonical(name), nil, args...)
}

func (s *Store) removeOnError(name string, err *error) {
	if *err == nil || errors.Is(*err, ErrExists) {
		return
	}
	s.Remove(name)
}

// Remove removes both repositories of name: for a project whose creation failed, and for one that
// was deleted (7.13). It says what could not be removed.
func (s *Store) Remove(name string) error {
	if !gitendpoint.ValidName(name) {
		return fmt.Errorf("%q is not a repository's name", name)
	}
	return errors.Join(os.RemoveAll(s.Canonical(name)), os.RemoveAll(s.Quarantine.Path(name)))
}

// Main returns the commit main points at in the canonical repository.
func (s *Store) Main(ctx context.Context, name string) string {
	return s.ref(ctx, s.Canonical(name), "refs/heads/main")
}

// CreateBranch cuts branch from main in the canonical repository, when it is not there (7.12: the
// backend creates a ticket's branch when the ticket enters In progress). The builder pushes its work
// to the branch of that name in the quarantine repository.
func (s *Store) CreateBranch(ctx context.Context, name, branch string) error {
	if !ValidBranch(branch) {
		return fmt.Errorf("%q is not a branch Crew makes", branch)
	}
	main := s.Main(ctx, name)
	if main == "" {
		return errors.New("the repository has no main")
	}
	ref := "refs/heads/" + branch
	if s.ref(ctx, s.Canonical(name), ref) != "" {
		return nil
	}
	return s.run(ctx, s.Canonical(name), nil, "update-ref", ref, main, strings.Repeat("0", len(main)))
}

var branchName = regexp.MustCompile(`^(task|sub|subtask|eval|conv)/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidBranch says branch is one of the branches Crew makes (6.1).
func ValidBranch(branch string) bool {
	return branchName.MatchString(branch) && !strings.Contains(branch, "..") && !strings.HasSuffix(branch, ".lock")
}

// Branch is what the quarantine repository holds of a ticket's branch: what its checks read (7.4).
type Branch struct {
	Pushed  bool     // the branch is in the quarantine repository
	Ahead   int      // its commits that main does not have
	Changed []string // the files it changed since it left main
}

// ReadBranch reads branch in the quarantine repository.
func (s *Store) ReadBranch(ctx context.Context, name, branch string) (Branch, error) {
	var b Branch
	if !ValidBranch(branch) {
		return b, fmt.Errorf("%q is not a branch Crew makes", branch)
	}
	dir := s.Quarantine.Path(name)
	ref := "refs/heads/" + branch
	if s.ref(ctx, dir, ref) == "" {
		return b, nil
	}
	b.Pushed = true
	out, err := s.output(ctx, dir, "rev-list", "--count", "refs/heads/main.."+ref)
	if err != nil {
		return b, err
	}
	if b.Ahead, err = strconv.Atoi(strings.TrimSpace(out)); err != nil {
		return b, err
	}
	out, err = s.output(ctx, dir, "diff", "--name-only", "-z", "refs/heads/main..."+ref)
	if err != nil {
		return b, err
	}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			b.Changed = append(b.Changed, p)
		}
	}
	return b, nil
}

// Bundle writes every branch and tag of the project's code to w, as one git bundle: the canonical
// repository's, with the branches runs pushed to the quarantine one over them. The bundle is made in
// a temporary repository, so the canonical one is not changed by what sandboxes pushed. main is the
// canonical repository's own.
func (s *Store) Bundle(ctx context.Context, name string, w io.Writer) error {
	tmp, err := os.MkdirTemp("", "crew-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := s.run(ctx, "", nil, "init", "-q", "--bare", tmp); err != nil {
		return err
	}
	if err := s.run(ctx, tmp, nil, "fetch", "-q", "--", s.Canonical(name), "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return err
	}
	heads, err := s.refs(ctx, s.Quarantine.Path(name), "refs/heads/")
	if err != nil {
		return err
	}
	args := []string{"fetch", "-q", "--", s.Quarantine.Path(name)}
	for _, h := range heads {
		if h != "refs/heads/main" {
			args = append(args, "+"+h+":"+h)
		}
	}
	if len(args) > 4 {
		if err := s.run(ctx, tmp, nil, args...); err != nil {
			return err
		}
	}
	return s.bundle(ctx, tmp, w)
}

func (s *Store) refs(ctx context.Context, dir, prefix string) ([]string, error) {
	out, err := s.output(ctx, dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			refs = append(refs, l)
		}
	}
	return refs, nil
}

func (s *Store) ref(ctx context.Context, dir, ref string) string {
	out, err := s.output(ctx, dir, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Push pushes refs of the canonical repository to a remote (6.6), never forced: a remote whose main
// went another way refuses it, and nothing there is lost. env carries how to reach it, such as the
// SSH command with the key from Setup. Only SSH is allowed, and what Protocols adds for tests.
func (s *Store) Push(ctx context.Context, name, remote string, refs []string, env []string) error {
	return s.pushFrom(ctx, s.Canonical(name), name, remote, refs, env)
}

// PushCommit pushes commit, as the quarantine repository holds it, to the ticket's branch ref on the
// remote: the head that was checked goes out, never what the branch holds by the time the push runs
// (review of M10). Never forced.
func (s *Store) PushCommit(ctx context.Context, name, remote, commit, ref string, env []string) error {
	if s.Quarantine == nil {
		return errors.New("gitstore: no quarantine repository")
	}
	if !ValidCommit(commit) || !strings.HasPrefix(ref, "refs/heads/task/") || strings.ContainsAny(ref, ":+ \t\n") {
		return fmt.Errorf("gitstore: %q of %q is not a ticket's branch at a commit", ref, commit)
	}
	if !gitendpoint.ValidName(name) || strings.HasPrefix(remote, "-") {
		return errors.New("gitstore: a push names a repository and a remote")
	}
	protocols := "ssh"
	if s.Protocols != "" {
		protocols += ":" + s.Protocols
	}
	return s.run(ctx, s.Quarantine.Path(name), append(append([]string{}, env...), "GIT_ALLOW_PROTOCOL="+protocols),
		"push", "--porcelain", "--", remote, commit+":"+ref)
}

// ChangedSince are the files a commit of a ticket's branch changes since it left main, in the
// quarantine repository: what the commit holds, not what the branch holds later.
func (s *Store) ChangedSince(ctx context.Context, name, commit string) ([]string, error) {
	if !ValidCommit(commit) {
		return nil, ErrNoRev
	}
	dir := s.Quarantine.Path(name)
	base, err := s.output(ctx, dir, "merge-base", "refs/heads/main", commit)
	if err != nil {
		return nil, err
	}
	out, err := s.output(ctx, dir, "diff", "--no-renames", "--name-only", "-z", strings.TrimSpace(base), commit, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func (s *Store) pushFrom(ctx context.Context, dir, name, remote string, refs []string, env []string) error {
	if !gitendpoint.ValidName(name) || len(refs) == 0 || strings.HasPrefix(remote, "-") {
		return errors.New("gitstore: a push names a repository, a remote, and its refs")
	}
	protocols := "ssh"
	if s.Protocols != "" {
		protocols += ":" + s.Protocols
	}
	args := []string{"push", "--porcelain", "--", remote}
	for _, r := range refs {
		if !strings.HasPrefix(r, "refs/") || strings.ContainsAny(r, ":+ \t\n") {
			return fmt.Errorf("gitstore: %q is not a ref to push", r)
		}
		args = append(args, r+":"+r)
	}
	return s.run(ctx, dir, append(append([]string{}, env...), "GIT_ALLOW_PROTOCOL="+protocols), args...)
}

func (s *Store) git() string {
	if s.Git != "" {
		return s.Git
	}
	return "git"
}

func (id Identity) env() []string {
	return []string{"GIT_AUTHOR_NAME=" + id.Name, "GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name, "GIT_COMMITTER_EMAIL=" + id.Email}
}

// gitEnv is the environment of a git command of the server: no configuration of the machine or of a
// user, so nothing but what is given here applies.
func gitEnv(extra []string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	return append(env, extra...)
}

// CleanEnv returns the caller's environment without any GIT_* variable, with GIT_CONFIG_NOSYSTEM=1,
// GIT_CONFIG_GLOBAL=/dev/null and GIT_TERMINAL_PROMPT=0, a default author and committer, and extra:
// a test helper that runs git never inherits GIT_DIR, GIT_WORK_TREE or GIT_INDEX_FILE from a shell or
// hook.
func CleanEnv(extra ...string) []string { return gitendpoint.CleanEnv(extra...) }

// noHooks comes first on every command line: no hook of any repository runs for the server.
var noHooks = []string{"-c", "core.hooksPath=/dev/null"}
