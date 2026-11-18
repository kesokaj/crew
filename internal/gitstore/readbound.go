package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// MaxOutput is the most of git's output one read of the server takes: the bound of a tree's listing
// (6.1, since 1.04). Every tree a push brings lists within it; a diff of two trees near the bound can
// list more, and is then an error. A file of a branch can be far longer from a pack of a few hundred
// kilobytes, and a remote's main or a tree stored before 1.03 did not come through the git endpoint.
// A history keeps each commit's fields to their cuts as git's output streams (history.go), and only
// what it keeps is bounded so: a commit's message past the bound is cut, not an error (since 1.09).
const MaxOutput = gitendpoint.MaxListBytes

// maxErrText is the most of git's own error text an error keeps.
const maxErrText = 64 << 10

// ErrOutputTooLarge is a read of git's output past its bound. Its text is fixed, and it is never
// ErrNoRev: a reader that takes "no such commit" for nothing to show must not take this for it.
var ErrOutputTooLarge = fmt.Errorf("git's answer is longer than %d bytes, more than Crew reads", MaxOutput)

// maxOutput is the bound of a read: Store.MaxOutput only lowers it, never raises it.
func (s *Store) maxOutput() int64 {
	if s.MaxOutput > 0 && s.MaxOutput < MaxOutput {
		return s.MaxOutput
	}
	return MaxOutput
}

// read runs git in dir and reads its output, at most the bound: past it, git is stopped and the read
// is ErrOutputTooLarge, before more is held. Otherwise it returns the output with git's error text,
// and the command's own error, an *exec.ExitError when git said no.
func (s *Store) read(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env)
	cmd.Stdin = stdin
	errText := &gitendpoint.LimitedText{Max: maxErrText}
	cmd.Stderr = errText
	// What git started and left holding its error text is not waited for long.
	cmd.WaitDelay = 5 * time.Second
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	limit := s.maxOutput()
	out, rerr := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(out)) > limit {
		cancel()
		cmd.Wait()
		return nil, "", ErrOutputTooLarge
	}
	err = cmd.Wait()
	if err == nil {
		err = rerr
	}
	return out, string(bytes.TrimSpace(errText.Bytes())), err
}

// stream runs git in dir with its output handed to w as it comes, nothing of it held here: w keeps
// what it keeps, each field within its own cut, and an error of w stops git and is the read's. A
// history is read this way (since 1.09): a commit's message past the bound is cut, not an error.
func (s *Store) stream(ctx context.Context, dir string, w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(nil)
	errText := &gitendpoint.LimitedText{Max: maxErrText}
	cmd.Stderr = errText
	cmd.WaitDelay = 5 * time.Second
	r, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(errText.Bytes()))
	}
	return nil
}

func (s *Store) output(ctx context.Context, dir string, args ...string) (string, error) {
	return s.outputEnv(ctx, dir, nil, args...)
}

func (s *Store) outputEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, errText, err := s.read(ctx, dir, env, nil, args...)
	if errors.Is(err, ErrOutputTooLarge) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, errText)
	}
	return string(out), nil
}

func (s *Store) run(ctx context.Context, dir string, env []string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env)
	// At the end of ctx the whole group goes, the ssh of a push with its git, and what is left
	// holding the output is not waited for long: a remote that stalls ends the command.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	// Only what an error says is kept, and only its start: a remote's messages can be any length.
	out := &gitendpoint.LimitedText{Max: maxErrText}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}

// outputLimited is output of at most limit bytes, and never more than the bound of a read; cut says
// there was more, and git is stopped then.
func (s *Store) outputLimited(ctx context.Context, dir string, limit int, args ...string) (string, bool, error) {
	limit = int(min(int64(limit), s.maxOutput()))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(nil)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, err
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	cut := len(b) > limit
	if cut {
		b = b[:limit]
		cancel()
	}
	werr := cmd.Wait()
	if err != nil {
		return "", false, err
	}
	if werr != nil && !cut {
		return "", false, fmt.Errorf("git %s: %v", args[0], werr)
	}
	return string(b), cut, nil
}

// bundle writes one git bundle of every branch and tag of dir to w, streamed: nothing of it is held.
func (s *Store) bundle(ctx context.Context, dir string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, s.git(), append(append([]string{}, noHooks...), "bundle", "create", "-q", "-", "--branches", "--tags")...)
	cmd.Dir = dir
	cmd.Env = gitEnv(nil)
	cmd.Stdout = w
	errText := &gitendpoint.LimitedText{Max: maxErrText}
	cmd.Stderr = errText
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git bundle: %v: %s", err, bytes.TrimSpace(errText.Bytes()))
	}
	return nil
}
