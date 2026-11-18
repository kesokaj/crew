package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/runspec"
)

// instructionsFromMain gives a run main's copies of the files its CLI reads as instructions, in place
// of the branch's, before every turn: a builder cannot instruct the agent that judges its work (13.8),
// and every run has the project's skills and context as main holds them, also a builder whose branch
// is older than a skill merged since (12.9). Only the builder of a ticket that may change them keeps
// its branch's. What main has and the branch does not track is excluded from git, so a builder's
// commit does not take it. What the branch holds of them, tracked or not, goes: what git tracks is marked
// skip-worktree first, as keepOutRepoConfig does, and no link is followed, so nothing outside the
// workspace is removed. Then main's copies are written through the workspace's root, which a link
// cannot lead out of. A git that fails is a failure of the turn. A run of an eval reads no main, and
// takes the copies of its case's base commit instead (9.6).
func (t *turnRun) instructionsFromMain(ctx context.Context) error {
	pats := t.a.WorkspaceInstructions()
	ws := runspec.Workspace
	if t.env.Workspace != "" {
		ws = t.env.Workspace
	}
	if len(pats) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return nil // a run with no repository
	}
	// A run that reads no main takes the commit of its branch's history it was given (9.6).
	at := "FETCH_HEAD"
	if t.spec.Git.OwnOnly {
		if at = t.spec.Git.InstructionsAt; at == "" {
			return errors.New("a run that reads no main was given no commit to take its instructions from")
		}
	} else if _, err := t.gitStdout(ctx, ws, "fetch", "-q", "origin", "refs/heads/main"); err != nil {
		return err
	}
	main, err := t.gitStdout(ctx, ws, "rev-parse", "--verify", "-q", at+"^{commit}")
	if err != nil {
		return err
	}
	main = strings.TrimSpace(main)
	// The folders main's index files name are instructions too, wherever they are (13.8).
	// A link of main is not written into the workspace below, so agy reads nothing through one here: an
	// index file is read only when main holds it as a file, and a link of one names nothing (since 1.07,
	// after its security review's second round, where 1.07 read a link's text as a file that does not
	// read). One that names the whole workspace counts only for the guards: here it would take every
	// file from main. A run that judges does not start on an index file the guards refuse or hold, past
	// cli.MaxIndexBytes, through /proc or /dev, past its entries or not JSON Crew reads exactly, nor when
	// git cannot list one, never with the branch's copies of what it may name; a builder's run reads as
	// before (the operator's popup answers of session 74, "Any Indexed error" and "Covered by the stop").
	var ferr error
	_, named, nerr := cli.Indexed(func(f string) ([]byte, cli.Way) {
		entry, err := t.gitStdout(ctx, ws, "ls-tree", "-l", "-z", main, "--", f)
		if err != nil {
			ferr = errors.Join(ferr, err)
			return nil, cli.Way{}
		}
		meta, _, _ := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if len(fields) != 4 || !strings.HasPrefix(fields[0], "100") {
			return nil, cli.Way{} // not there, or not a file
		}
		if size, err := strconv.ParseInt(fields[3], 10, 64); t.spec.NoRepoConfig && (err != nil || size > cli.MaxIndexBytes) {
			ferr = errors.Join(ferr, fmt.Errorf("an index file of main, %s, is longer than %d bytes, more than Crew reads", f, cli.MaxIndexBytes))
			return nil, cli.Way{}
		}
		b, err := t.gitStdout(ctx, ws, "cat-file", "blob", fields[2])
		if err != nil {
			ferr = errors.Join(ferr, err)
			return nil, cli.Way{}
		}
		return []byte(b), cli.Way{}
	})
	if err := errors.Join(ferr, nerr); t.spec.NoRepoConfig && err != nil {
		return err
	}
	named = slices.DeleteFunc(named, func(p string) bool { return p == cli.AllPaths })
	pats = append(append([]string{}, pats...), named...)
	tracked, err := t.gitStdout(ctx, ws, "ls-files", "-z")
	if err != nil {
		return err
	}
	for _, f := range strings.Split(strings.TrimRight(tracked, "\x00"), "\x00") {
		if f != "" && cli.Matches(f, pats) {
			if _, err := t.gitStdout(ctx, ws, "update-index", "--skip-worktree", "--", f); err != nil {
				return err
			}
		}
	}
	// The branch's copies, and what the agent wrote, go: a match is removed whole, a link as itself.
	var gone []string
	err = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case rel == ".":
		case cli.Below(rel, pats):
			gone = append(gone, p)
			if d.IsDir() {
				return filepath.SkipDir
			}
		case cli.Above(rel, pats) && !d.IsDir():
			// A file or a link where a folder of instructions belongs: what it stands for goes with it.
			gone = append(gone, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range gone {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	listed, err := t.gitStdout(ctx, ws, "ls-tree", "-r", "-z", main)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer root.Close()
	isTracked := map[string]bool{}
	for _, f := range strings.Split(strings.TrimRight(tracked, "\x00"), "\x00") {
		isTracked[f] = true
	}
	var untracked []string
	n := 0
	var totalBytes int64
	for _, l := range strings.Split(strings.TrimRight(listed, "\x00"), "\x00") {
		meta, f, ok := strings.Cut(l, "\t")
		// main's own files only: a link or a submodule of main is not written as a file.
		if !ok || !strings.HasPrefix(meta, "100") || !cli.Below(f, pats) {
			continue
		}
		if strings.Count(f, "/")+1 > cli.MaxInstructionDepth {
			return fmt.Errorf("an instruction file of main, %s, is deeper than %d folders", f, cli.MaxInstructionDepth)
		}
		if n+1 > cli.MaxInstructionEntries {
			return fmt.Errorf("main holds more than %d instruction files", cli.MaxInstructionEntries)
		}
		body, err := t.gitStdout(ctx, ws, "cat-file", "blob", main+":"+f)
		if err != nil {
			return err
		}
		totalBytes += int64(len(body))
		if totalBytes > cli.MaxInstructionBytes {
			return fmt.Errorf("main's instruction files are larger than %d MiB", cli.MaxInstructionBytes>>20)
		}
		if err := writeIn(root, f, []byte(body)); err != nil {
			return err
		}
		if !isTracked[f] {
			untracked = append(untracked, f)
		}
		n++
	}
	if err := t.exclude(ctx, ws, untracked); err != nil {
		return err
	}
	if len(gone) > 0 || n > 0 {
		t.logf("took %d files that instruct an agent from main in place of %d of the branch's: a run that judges reads main's (13.8)", n, len(gone))
	}
	return nil
}

// writeIn writes a file at rel inside root, making its folders: a folder that is a link out of the
// workspace is refused by the root.
func writeIn(root *os.Root, rel string, data []byte) error {
	dir := path.Dir(rel)
	if dir != "." {
		cur := ""
		for _, part := range strings.Split(dir, "/") {
			cur = path.Join(cur, part)
			if err := root.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// exclude adds files to the clone's own list of what git ignores, each once and anchored at the root.
func (t *turnRun) exclude(ctx context.Context, ws string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	rel, err := t.gitStdout(ctx, ws, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	name := strings.TrimSpace(rel)
	if !filepath.IsAbs(name) {
		name = filepath.Join(ws, name)
	}
	have, _ := os.ReadFile(name)
	lines := map[string]bool{}
	for _, l := range strings.Split(string(have), "\n") {
		lines[l] = true
	}
	var add strings.Builder
	for _, f := range files {
		// A name git would read as a pattern is written with its special characters escaped.
		line := "/" + strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "!", `\!`, "#", `\#`).Replace(f)
		if !lines[line] {
			add.WriteString(line + "\n")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	// The clone is the agent's: a link where the file belongs is not followed.
	out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if len(have) > 0 && !strings.HasSuffix(string(have), "\n") {
		if _, err := out.WriteString("\n"); err != nil {
			out.Close()
			return err
		}
	}
	if _, err := out.WriteString(add.String()); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
