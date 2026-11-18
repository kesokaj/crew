package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func harness() map[string]string {
	return map[string]string{
		"context/AGENTS.md":     "# AGENTS.md\n\nBe honest.\n",
		"skills/alpha/SKILL.md": "---\nname: alpha\ndescription: \"Alpha does things\"\n---\nbody\n",
		"agents/reviewer.md":    "---\nname: reviewer\ndescription: Reviews diffs\ntools: Read, Grep\n---\nbody\n",
		"rules/style.md":        "---\ntrigger: always_on\ndescription: \"Style\"\n---\nUse slog.\n",
	}
}

func TestRunWritesTheSkeleton(t *testing.T) {
	src := writeTree(t, harness())
	out := filepath.Join(t.TempDir(), "skel")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-src", src, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, rel := range []string{".gemini/config/rules/AGENTS.md", ".gemini/config/rules/style.md", ".gemini/config/skills/alpha/SKILL.md",
		".gemini/config/agents/reviewer/agent.md"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, ".claude")); err == nil {
		t.Error("a home for claude was written: agy is the only CLI")
	}
	if !strings.Contains(stdout.String(), "1 skills, 1 agents, 1 rules") {
		t.Errorf("summary = %q", stdout.String())
	}
}

func TestRunExitsOneOnARefusal(t *testing.T) {
	files := harness()
	files["skills/alpha/SKILL.md"] = "---\nname: alpha\ndescription: fixes #1\n---\nbody\n"
	src := writeTree(t, files)
	out := filepath.Join(t.TempDir(), "skel")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-src", src, "-out", out}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "alpha") {
		t.Errorf("stderr = %q, want the refused skill named", stderr.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused build wrote a skeleton")
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"-src", "x"}, {"-out", "y"}, {"-src", "x", "-out", "y", "extra"}, {"-nope"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
