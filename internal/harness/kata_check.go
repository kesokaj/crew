package harness

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// ExecFresh runs a check in a check sandbox (13.3, 13.9): a microVM with no login, no run token, and
// no Crew MCP, whose home gets the tree as an archive before it starts. The tree is unpacked as the
// sandbox's user, the project's setup runs, then each command in turn, each bounded by the spec's
// timeout. When the check carries held-out files or asks for a cut (9.6, 13.6), Crew cuts the
// sandbox's network from outside the microVM after the setup, proves from the guest that no route out
// remains, and only then writes and unpacks the held-out files over the tree before the commands run.
// The sandbox goes when the check ends.
func (k *Kata) ExecFresh(ctx context.Context, spec CheckSpec) ([]CheckResult, error) {
	if len(spec.Tree) == 0 || len(spec.Tree) > MaxTree || len(spec.HeldOut) > MaxTree-len(spec.Tree) {
		return nil, fmt.Errorf("a check needs a tree of at most %d bytes", MaxTree)
	}
	var heldDirs, heldPaths []string
	if len(spec.HeldOut) > 0 {
		var err error
		heldDirs, heldPaths, err = heldOutPaths(spec.HeldOut)
		if err != nil {
			return nil, err
		}
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 30 * time.Minute
	}
	row, sb, err := k.Sandboxes.Start(ctx, sandboxes.Spec{
		Kind: sandboxes.KindCheck, Project: spec.Project, Repos: spec.Repos, Mode: spec.Mode, Sizing: spec.Sizing, Life: 3 * time.Hour,
		Prepare: func(_, home, _ string) error { return writeHome(home, checkTree, spec.Tree, 0o644) },
	})
	if err != nil {
		return nil, fmt.Errorf("the check sandbox: %w", err)
	}
	defer k.remove(row.ID)
	sh := func(line string, limit time.Duration) (int, string, error) {
		ctx, cancel := context.WithTimeout(ctx, limit+time.Minute)
		defer cancel()
		// timeout's own exit code, 124, says the command ran past its time.
		return k.Runtime.Exec(ctx, sb.Container, orchestrator.ExecSpec{
			Cmd:  []string{"timeout", "--kill-after=10s", fmt.Sprintf("%ds", int(limit.Seconds())), "bash", "-lc", line},
			User: orchestrator.GuestUser, Env: []string{"HOME=/home/" + orchestrator.GuestUser},
		})
	}
	if code, out, err := sh("mkdir -p ~/work && tar -xf ~/"+checkTree+" -C ~/work && rm -f ~/"+checkTree, 10*time.Minute); err != nil || code != 0 {
		return nil, fmt.Errorf("unpacking the tree in the check sandbox: exit %d %v: %s", code, err, tail(out))
	}
	var results []CheckResult
	if strings.TrimSpace(spec.Setup) != "" {
		code, out, err := sh("cd ~/work && "+spec.Setup, spec.Timeout)
		if err != nil || code != 0 {
			why := fmt.Sprintf(SetupError+" ended with %d", code)
			if code == 124 {
				why = SetupError + " ran longer than " + spec.Timeout.String()
			}
			if err != nil {
				why = SetupError + ": " + err.Error()
			}
			return setupEnded(spec, CheckResult{Code: code, Error: why, Output: tail(out)}), nil
		}
	}
	if spec.Cut || len(spec.HeldOut) > 0 {
		if err := k.Sandboxes.Cut(ctx, row.ID); err != nil {
			return nil, fmt.Errorf("cutting the check sandbox's network: %w", err)
		}
		host, port, ok := strings.Cut(sb.Door, ":")
		if !ok || host == "" || port == "" {
			return nil, errors.New("proving the check sandbox has no route out: the sandbox's door is not known")
		}
		probe := func(p string) (int, string, error) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			return k.Runtime.Exec(ctx, sb.Container, orchestrator.ExecSpec{
				Cmd: []string{"timeout", "4", "bash", "-c", "exec 3<>/dev/tcp/" + p},
			})
		}
		for _, p := range []string{host + "/" + port, "1.1.1.1/443"} {
			code, out, err := probe(p)
			if err != nil {
				return nil, fmt.Errorf("proving the check sandbox has no route out: %w", err)
			}
			if code == 0 {
				return nil, fmt.Errorf("the check sandbox reached %s after its network was cut", strings.Replace(p, "/", ":", 1))
			}
			if code == 126 || code == 127 {
				return nil, fmt.Errorf("proving the check sandbox has no route out (exit %d): %s", code, strings.TrimSpace(out))
			}
		}
	}
	if len(spec.HeldOut) > 0 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return nil, err
		}
		heldArchive := "held-out-" + hex.EncodeToString(rnd[:]) + ".tar"
		if err := writeHome(k.Sandboxes.Home(row.ID), heldArchive, spec.HeldOut, 0o644); err != nil {
			return nil, fmt.Errorf("writing the held-out files: %w", err)
		}
		if code, out, err := sh(placeHeldOutCmd(heldArchive, heldDirs, heldPaths), 10*time.Minute); err != nil || code != 0 {
			return nil, fmt.Errorf("placing the held-out files in the check sandbox: exit %d %v: %s", code, err, tail(out))
		}
	}
	for _, c := range spec.Cmds {
		code, out, err := sh("cd ~/work && "+c.Run, spec.Timeout)
		r := CheckResult{Name: c.Name, Code: code, Passed: err == nil && code == 0, Output: tail(out)}
		switch {
		case err != nil:
			r.Error = "the check sandbox: " + err.Error()
		case code == 124:
			r.Error = "it ran longer than " + spec.Timeout.String()
		}
		results = append(results, r)
	}
	return results, nil
}

// heldOutPaths reads the paths in a held-out tar archive and their parent directories, refusing any
// entry that is not a regular file or directory or whose path is not a safe relative path in ~/work.
func heldOutPaths(archive []byte) (dirs, paths []string, err error) {
	tr := tar.NewReader(bytes.NewReader(archive))
	seenDir := map[string]bool{}
	seenPath := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the held-out archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir {
			return nil, nil, fmt.Errorf("the held-out archive holds %q, which is not a file or folder", h.Name)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "" || homepath.Valid(name) != nil {
			return nil, nil, fmt.Errorf("the held-out archive holds an unsafe path %q", h.Name)
		}
		for d := path.Dir(name); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if !seenDir[d] {
				seenDir[d] = true
				dirs = append(dirs, d)
			}
		}
		if !seenPath[name] {
			seenPath[name] = true
			paths = append(paths, name)
		}
	}
	if len(paths) == 0 {
		return nil, nil, errors.New("the held-out archive holds no files")
	}
	slices.Sort(dirs)
	return dirs, paths, nil
}

func placeHeldOutCmd(archive string, dirs, paths []string) string {
	var b strings.Builder
	b.WriteString("if [ -L ~/work ] || [ ! -d ~/work ]; then rm -rf ~/work && mkdir -p ~/work; fi")
	for _, d := range dirs {
		q := "~/work/" + shellQuote(d)
		b.WriteString(" && if [ -L " + q + " ] || { [ -e " + q + " ] && [ ! -d " + q + " ]; }; then rm -rf -- " + q + "; fi")
	}
	b.WriteString(" && rm -rf --")
	for _, p := range paths {
		b.WriteString(" ~/work/" + shellQuote(p))
	}
	b.WriteString(" && tar -xf ~/" + archive + " -C ~/work && rm -f ~/" + archive)
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// checkTree is the archive of the tree in a check sandbox's home.
const checkTree = "tree.tar"

// tail is the end of a command's output, which says why it failed.
func tail(s string) string {
	const n = 8 << 10
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
