package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runspec"
	"github.com/kesokaj/crew/internal/sandboxes"
)

func newCtrlKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ProjectSandboxes is the project-scoped warm Kata microVM manager (`*sandboxes.Manager`).
type ProjectSandboxes interface {
	EnsureProjectSandbox(ctx context.Context, projectID string, spec sandboxes.Spec) (sandboxes.ProjectSandbox, orchestrator.Sandbox, error)
	AcquireProjectSession(ctx context.Context, projectID string) error
	ReleaseProjectSession(ctx context.Context, projectID string) error
}

// Steerer is implemented by harnesses (`Kata` and `Fake`) that can steer an active ACP turn mid-turn.
type Steerer interface {
	Steer(ctx context.Context, runID, text string) error
}

func (k *Kata) projectPool() ProjectSandboxes {
	if k.ProjectSandboxes != nil {
		return k.ProjectSandboxes
	}
	if ps, ok := k.Sandboxes.(ProjectSandboxes); ok {
		return ps
	}
	return nil
}

func (k *Kata) useProjectACP(spec RunSpec) bool {
	_, knownCLI := cli.Get(spec.CLI)
	return k.projectPool() != nil &&
		spec.Project != "" &&
		knownCLI &&
		!spec.Judge &&
		!spec.Improver &&
		!spec.WithoutLogin
}

func (k *Kata) startProjectACPRun(ctx context.Context, spec RunSpec, a cli.Adapter, rs runspec.Run, kind string, env []string) (RunHandle, error) {
	pool := k.projectPool()
	r := &kataRun{
		k:       k,
		id:      spec.Run,
		a:       a,
		spec:    rs,
		turn:    1,
		conv:    spec.Continue,
		keep:    spec.KeepFolder,
		acp:     true,
		project: spec.Project,
		ctrlKey: newCtrlKey(),
		events:  make(chan RunEvent, 64),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	ps, sb, err := pool.EnsureProjectSandbox(ctx, spec.Project, sandboxes.Spec{
		Kind:    kind,
		CLI:     spec.CLI,
		Project: spec.Project,
		RunID:   spec.Run,
		Repos:   spec.Repos,
		Mode:    spec.Mode,
		Minimal: false,
		Sizing:  spec.Sizing,
		Life:    24 * time.Hour,
		Image:   spec.Image,
		Env:     env,
		Prepare: func(id, home, hash string) error {
			r.home = home
			if tokBytes, err := homepath.ReadFile(home, path.Join(runspec.RunControlDir(r.id), "token"), 4096); err == nil {
				r.token = strings.TrimSpace(string(tokBytes))
			}
			if err := r.writeCtrl(rs, 1, spec.Prompt); err != nil {
				return err
			}
			for p, data := range spec.Files {
				if err := writeHome(home, p, data, 0o600); err != nil {
					return fmt.Errorf("putting back %s: %w", p, err)
				}
			}
			if spec.KeepFolder != "" {
				if err := PutFolder(home, spec.KeepFolder, spec.Folder); err != nil && len(spec.Folder) > 0 {
					k.Log.Warn("the work folder was not put back", "run", spec.Run, "error", err)
				}
			}
			if spec.OnSandbox != nil {
				return spec.OnSandbox(id, hash)
			}
			return nil
		},
	})
	if err != nil {
		return RunHandle{}, err
	}
	if err := pool.AcquireProjectSession(ctx, spec.Project); err != nil && k.Log != nil {
		k.Log.Warn("recording active ACP session", "project", spec.Project, "run", spec.Run, "error", err)
	}
	r.sandbox, r.container, r.home = ps.SandboxID, sb.Container, ps.HomePath
	if err := r.startTurn(ctx); err != nil {
		_ = pool.ReleaseProjectSession(context.Background(), spec.Project)
		return RunHandle{}, err
	}
	k.put(r)
	go r.follow()
	return RunHandle{Run: spec.Run, Sandbox: ps.SandboxID}, nil
}

func (r *kataRun) turnDir(turn int) string {
	if r.acp {
		return runspec.RunTurnDir(r.id, turn)
	}
	return runspec.TurnDir(turn)
}

func (r *kataRun) stopFile() string {
	if r.acp {
		return path.Join(runspec.RunControlDir(r.id), "stop")
	}
	return runspec.StopFile
}

func (r *kataRun) workspaceRel() string {
	if r.acp && r.spec.Git.Path != "" {
		return path.Join(runspec.WorktreesDir, r.id)
	}
	return runspec.Workspace
}

func (r *kataRun) writeCtrl(rs runspec.Run, turn int, prompt string) error {
	if !r.acp {
		return writeControl(r.home, rs, turn, prompt)
	}
	ctrlDir := runspec.RunControlDir(r.id)
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := writeHome(r.home, path.Join(ctrlDir, "run.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if r.token != "" {
		if err := writeHome(r.home, path.Join(ctrlDir, "token"), []byte(r.token+"\n"), 0o600); err != nil {
			return err
		}
	}
	if r.ctrlKey != "" {
		if err := writeHome(r.home, path.Join(ctrlDir, "ctrl.key"), []byte(r.ctrlKey+"\n"), 0o600); err != nil {
			return err
		}
	}
	return writeHome(r.home, path.Join(r.turnDir(turn), runspec.PromptFile), []byte(prompt), 0o644)
}

func (r *kataRun) clearTurnDir(turn int) error {
	if !r.acp {
		return clearTurn(r.home, turn)
	}
	root, err := os.OpenRoot(r.home)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range []string{runspec.ExitFile, runspec.EventsFile, runspec.StderrFile, runspec.SteerFile} {
		if err := root.RemoveAll(path.Join(r.turnDir(turn), f)); err != nil {
			return fmt.Errorf("clearing the turn's folder: %w", err)
		}
	}
	return nil
}

func (r *kataRun) stopACP(ctx context.Context) error {
	env := []string{"HOME=/home/" + orchestrator.GuestUser}
	if r.ctrlKey != "" {
		env = append(env, "CREW_CTRL_KEY="+r.ctrlKey)
	}
	_, _, _ = r.k.Runtime.Exec(ctx, r.container, orchestrator.ExecSpec{
		Cmd:  []string{"crew-run", "acp-cancel", "--run", r.id},
		User: orchestrator.GuestUser,
		Env:  env,
	})
	if root, err := os.OpenRoot(r.home); err == nil {
		_ = root.RemoveAll(runspec.RunControlDir(r.id))
		root.Close()
	}
	if pool := r.k.projectPool(); pool != nil && r.project != "" {
		return pool.ReleaseProjectSession(ctx, r.project)
	}
	return nil
}

// Steer sends a live mid-turn steering prompt into an active ACP turn (`crew-run acp-steer`).
func (k *Kata) Steer(ctx context.Context, runID, text string) error {
	r, ok := k.get(runID)
	if !ok {
		return ErrNoRun
	}
	r.mu.Lock()
	turn, done, acp := r.turn, r.turnDone, r.acp
	r.mu.Unlock()
	if done {
		return errors.New("no active turn to steer")
	}
	if !acp {
		return errors.New("run is not an ACP session")
	}
	if err := writeHome(r.home, path.Join(r.turnDir(turn), runspec.SteerFile), []byte(strings.TrimSpace(text)+"\n"), 0o644); err != nil {
		return err
	}
	env := []string{"HOME=/home/" + orchestrator.GuestUser}
	if r.ctrlKey != "" {
		env = append(env, "CREW_CTRL_KEY="+r.ctrlKey)
	}
	code, out, err := k.Runtime.Exec(ctx, r.container, orchestrator.ExecSpec{
		Cmd:  []string{"crew-run", "acp-steer", "--run", r.id, filepath.Base(runspec.TurnDir(turn))},
		User: orchestrator.GuestUser,
		Env:  env,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("acp-steer exited with %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}
