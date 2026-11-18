package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/kesokaj/crew/internal/cli"
)

// loginCheck checks the login of one CLI in a login-check sandbox (12.6), which the broker gives
// that CLI's login and nothing else:
//
//	crew-run login-check CLI [--real]
//
// It prints one line of JSON, the adapter's result. It ends with 0 when the check ran, whatever it
// found, and with 1 when it could not run.
func loginCheck(e env, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: crew-run login-check CLI [--real]")
		return 2
	}
	a, ok := cli.Get(args[0])
	if !ok {
		fmt.Fprintf(stderr, "crew-run login-check: no CLI %q\n", args[0])
		return 2
	}
	real := len(args) > 1 && args[1] == "--real"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	body, err := pull(ctx, e, "/v1/creds")
	if err != nil {
		fmt.Fprintln(stderr, "crew-run login-check:", err)
		return 1
	}
	var c credsAnswer
	if err := json.Unmarshal(body, &c); err != nil {
		fmt.Fprintln(stderr, "crew-run login-check: the broker's answer:", err)
		return 1
	}
	if _, err := apply(e.Home, c); err != nil {
		fmt.Fprintln(stderr, "crew-run login-check:", err)
		return 1
	}
	run := commandRunner(e.Home, c.Env)
	res := a.LoginCheck(ctx, run, real)
	if res.State == cli.LoginOK {
		// The list of models and what is left of the limits are read where the login is, with no
		// model request (12.3, 12.7). Either may fail without changing what the check found.
		if res.Models == nil {
			m, err := a.ReadModels(ctx, run, commandAsker(e.Home, c.Env))
			if err != nil {
				res.ModelsError = err.Error()
			}
			res.Models = m
		}
		if l, err := a.ReadLimits(ctx, run); err == nil && l != nil {
			res.Limits = l
		}
	}
	json.NewEncoder(stdout).Encode(res)
	return 0
}

// commandRunner runs a CLI as the sandbox's user, in its home, with the values from the broker and
// never ANTHROPIC_API_KEY (12.6).
func commandRunner(home string, values map[string]string) cli.Runner {
	ask := commandAsker(home, values)
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
		return ask(ctx, nil, nil, name, args...)
	}
}

// commandAsker is commandRunner with standard input: in is written, and standard input is closed
// once done says a line of standard output was the last one needed.
func commandAsker(home string, values map[string]string) cli.Asker {
	return func(ctx context.Context, in []byte, done func([]byte) bool, name string, args ...string) ([]byte, []byte, int, error) {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, name, args...)
		cmd.Dir = home
		// At the time limit the whole process group ends: a child the CLI started could otherwise
		// keep its output open, and the reading would never end.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = 5 * time.Second
		env := cleanEnv(home)
		for k, v := range values {
			if !cli.ForbiddenEnv(k) && k != "HOME" && k != "CREW_CTRL_KEY" {
				env = append(env, k+"="+v)
			}
		}
		cmd.Env = env
		var out, errb bytes.Buffer
		cmd.Stderr = &errb
		var stdin io.WriteCloser
		var stdout io.ReadCloser
		var err error
		if done == nil {
			cmd.Stdin = bytes.NewReader(in)
			cmd.Stdout = &out
		} else {
			if stdin, err = cmd.StdinPipe(); err != nil {
				return nil, nil, -1, err
			}
			if stdout, err = cmd.StdoutPipe(); err != nil {
				return nil, nil, -1, err
			}
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, -1, err
		}
		if done != nil {
			stop := context.AfterFunc(cctx, func() { stdout.Close() })
			defer stop()
			stdin.Write(in)
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			closed := false
			for sc.Scan() {
				out.Write(sc.Bytes())
				out.WriteByte('\n')
				if !closed && done(sc.Bytes()) {
					stdin.Close()
					closed = true
				}
			}
			if !closed {
				stdin.Close()
			}
		}
		err = cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out.Bytes(), errb.Bytes(), exitErr.ExitCode(), nil
		}
		if err != nil {
			return nil, nil, -1, err
		}
		return out.Bytes(), errb.Bytes(), 0, nil
	}
}
