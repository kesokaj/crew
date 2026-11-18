package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/runspec"
)

var worktreeMu sync.Mutex

// lockBareMirror acquires both the in-process mutex and a cross-process exclusive flock on
// `/home/agent/repo.git/crew.lock` (opened via os.OpenRoot) so concurrent `crew-run` processes in
// the same project microVM never race on `/home/agent/repo.git` (design 12.2, 12.4, 13.1).
func lockBareMirror(home string) (func(), error) {
	worktreeMu.Lock()
	bareRel := filepath.Base(runspec.RepoMirror)
	if err := homepath.MkdirAll(home, bareRel, 0o755); err != nil {
		worktreeMu.Unlock()
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		worktreeMu.Unlock()
		return nil, err
	}
	f, err := root.OpenFile(bareRel+"/crew.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	root.Close()
	if err != nil {
		worktreeMu.Unlock()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		worktreeMu.Unlock()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		worktreeMu.Unlock()
	}, nil
}

func sanitizeWorktreeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func worktreeDir(home, runID string) string {
	return filepath.Join(home, filepath.Base(runspec.WorktreesDir), sanitizeWorktreeID(runID))
}

func bareMirrorDir(home string) string {
	return filepath.Join(home, filepath.Base(runspec.RepoMirror))
}

// workspaceWorktree prepares an isolated git worktree at `/home/agent/worktrees/<run-id>` backed by
// `/home/agent/repo.git` inside the project's shared microVM (design 12.2, 12.4, 13.1, since 1.37).
func (t *turnRun) workspaceWorktree(ctx context.Context) (string, error) {
	unlock, err := lockBareMirror(t.env.Home)
	if err != nil {
		return "", err
	}
	defer unlock()

	ws := worktreeDir(t.env.Home, t.spec.Run)
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	t.env.Workspace = ws
	g := t.spec.Git

	if g.Path == "" && g.Branch == "" && g.WIP == "" {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			return "", err
		}
		if t.spec.Project == "" && t.spec.MCP != "" && t.env.Broker != "" && t.token != "" {
			prefix := strings.TrimRight(t.env.Broker, "/") + "/git/"
			if _, err := t.gitCmd(ctx, ws, "config", "--file", filepath.Join(t.env.Home, ".gitconfig"), "http."+prefix+".extraHeader", "Authorization: Bearer "+t.token); err != nil {
				return "", err
			}
		}
		return ws, nil
	}

	if g.Path == "" || g.Branch == "" || t.env.Broker == "" {
		return "", errors.New("run.json names no repository and branch, or there is no door")
	}
	url := strings.TrimRight(t.env.Broker, "/") + g.Path
	if _, err := os.Stat(filepath.Join(ws, ".git")); err == nil {
		if err := t.configureWorktreeToken(ctx, ws, url); err != nil {
			return "", err
		}
		if g.Refresh {
			if err := t.refresh(ctx, ws); err != nil {
				return "", err
			}
		}
		return ws, nil
	}
	return t.createWorktreeLocked(ctx, ws, url)
}

func (t *turnRun) createWorktreeLocked(ctx context.Context, ws, url string) (string, error) {
	g := t.spec.Git
	bare := bareMirrorDir(t.env.Home)
	if err := t.ensureBareMirror(ctx, bare, url); err != nil {
		return "", err
	}
	out, err := t.gitCmd(ctx, bare, "ls-remote", "origin", "refs/heads/"+g.Branch)
	if err != nil {
		return "", err
	}
	from := g.Base
	if strings.TrimSpace(out) != "" && !strings.HasPrefix(g.Base, "refs/crew/wip/") {
		from = "refs/heads/" + g.Branch
	}
	if from == "" {
		from = "refs/heads/main"
	}
	if _, err := t.gitCmd(ctx, bare, "fetch", "-q", "origin", from); err != nil {
		return "", err
	}
	revOut, err := t.gitStdout(ctx, bare, "rev-parse", "--verify", "-q", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", err
	}
	startCommit := strings.TrimSpace(revOut)

	if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
		return "", err
	}
	_, _ = t.gitCmd(ctx, bare, "worktree", "prune")
	localBranch := g.Branch
	if g.Branch == "main" || g.Refresh {
		localBranch = "crew-wt/" + sanitizeWorktreeID(t.spec.Run)
	}
	if _, err := t.gitCmd(ctx, bare, "worktree", "add", "--force", "-B", localBranch, ws, startCommit); err != nil {
		localBranch = "crew-wt/" + sanitizeWorktreeID(t.spec.Run)
		if _, err2 := t.gitCmd(ctx, bare, "worktree", "add", "--force", "-B", localBranch, ws, startCommit); err2 != nil {
			return "", err
		}
	}

	wtSteps := [][]string{
		{"config", "--worktree", "core.bare", "false"},
		{"config", "--worktree", "core.hooksPath", "/dev/null"},
		{"config", "--worktree", "branch." + localBranch + ".remote", "origin"},
		{"config", "--worktree", "branch." + localBranch + ".merge", "refs/heads/" + g.Branch},
		{"config", "--worktree", "push.default", "upstream"},
	}
	for _, s := range wtSteps {
		if _, err := t.gitCmd(ctx, ws, s...); err != nil {
			return "", err
		}
	}
	if err := t.configureWorktreeToken(ctx, ws, url); err != nil {
		return "", err
	}
	if !g.OwnOnly {
		if _, err := t.gitCmd(ctx, ws, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main"); err != nil {
			return "", err
		}
	}
	if g.Refresh && strings.TrimSpace(out) != "" {
		if err := t.refresh(ctx, ws); err != nil {
			return "", err
		}
	}
	return ws, nil
}

// configureWorktreeToken writes the run token to worktree-local git config only for tool-enabled
// runs that may push a branch; read-only runs keep no token in config.worktree.
func (t *turnRun) configureWorktreeToken(ctx context.Context, ws, url string) error {
	if t.spec.Tools && t.spec.Git.WIP != "" && t.token != "" {
		if _, err := t.gitCmd(ctx, ws, "config", "--worktree", "http."+url+".extraHeader", "Authorization: Bearer "+t.token); err != nil {
			return err
		}
		wtID := sanitizeWorktreeID(t.spec.Run)
		_ = os.Chmod(filepath.Join(bareMirrorDir(t.env.Home), "worktrees", wtID, "config.worktree"), 0o600)
		return nil
	}
	_, _ = t.gitCmd(ctx, ws, "config", "--worktree", "--unset-all", "http."+url+".extraHeader")
	return nil
}

func (t *turnRun) clearWorktreeToken(ws string) {
	if t.env.Broker == "" || t.spec.Git.Path == "" {
		return
	}
	unlock, err := lockBareMirror(t.env.Home)
	if err == nil {
		defer unlock()
	}
	cleanCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := strings.TrimRight(t.env.Broker, "/") + t.spec.Git.Path
	_, _ = t.gitCmd(cleanCtx, ws, "config", "--worktree", "--unset-all", "http."+url+".extraHeader")
}

func (t *turnRun) ensureBareMirror(ctx context.Context, bare, url string) error {
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		if err := os.MkdirAll(bare, 0o755); err != nil {
			return err
		}
		if _, err := t.gitCmd(ctx, bare, "init", "--bare", "-q"); err != nil {
			return err
		}
	}
	if _, err := t.gitCmd(ctx, bare, "config", "core.repositoryformatversion", "1"); err != nil {
		return err
	}
	if _, err := t.gitCmd(ctx, bare, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	if _, err := t.gitCmd(ctx, bare, "config", "core.hooksPath", "/dev/null"); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(bare, "hooks"))
	if _, err := t.gitCmd(ctx, bare, "remote", "set-url", "origin", url); err != nil {
		if _, err := t.gitCmd(ctx, bare, "remote", "add", "origin", url); err != nil {
			return err
		}
	}
	return nil
}

// removeWorktree detaches and deletes `/home/agent/worktrees/<run-id>` when a run ends or is cancelled.
func removeWorktree(ctx context.Context, e env, runID string) error {
	unlock, err := lockBareMirror(e.Home)
	if err != nil {
		return err
	}
	defer unlock()

	ws := worktreeDir(e.Home, runID)
	bare := bareMirrorDir(e.Home)
	t := &turnRun{env: e}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err == nil {
		_, _ = t.gitCmd(ctx, bare, "worktree", "remove", "--force", ws)
		_, _ = t.gitCmd(ctx, bare, "worktree", "prune")
	}
	return os.RemoveAll(ws)
}
