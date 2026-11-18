package deploy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// quietGrepInPipe is a pipe into grep -q (or --quiet). grep -q stops reading at its first match, so
// a command that writes on after it gets SIGPIPE, and with pipefail the pipe fails: a match reads as
// no match, and with a ! in front a failed check as a passed one (BUG-021). grep without -q reads
// its input to the end; its output goes to /dev/null instead.
var quietGrepInPipe = regexp.MustCompile(`\|\s*grep\s+(-[A-Za-z]*q[A-Za-z]*|--quiet)\b`)

// No shell script of the deploy files or the tools pipes into a quiet grep: not only those that set
// pipefail, since a library they source runs under it without saying so (deploy/lib/kata-config.sh,
// sourced by host-setup.sh).
func TestNoShellScriptPipesIntoAQuietGrep(t *testing.T) {
	seen := 0
	for _, root := range []string{".", filepath.Join("..", "tools")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".py") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.HasSuffix(path, ".sh") && !strings.HasPrefix(string(b), "#!/bin/bash") &&
				!strings.HasPrefix(string(b), "#!/usr/bin/env bash") && !strings.HasPrefix(string(b), "#!/bin/sh") {
				return nil
			}
			seen++
			for i, line := range strings.Split(string(b), "\n") {
				if quietGrepInPipe.MatchString(line) {
					t.Errorf("%s:%d pipes into a quiet grep: %s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// crew-ctl, the firewall and its test, the host setup and its libraries, the proof, and the install
	// test at least.
	if seen < 10 {
		t.Fatalf("only %d shell scripts were read", seen)
	}
}
