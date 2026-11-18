package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/runspec"
)

// A run that judges does not start on an index file of main that the guards refuse or hold: not JSON
// Crew reads exactly, which may name for agy what it names nothing for Crew, through /proc or /dev, past
// its 1,024 entries, or past 1 MiB, or that git cannot list, never with the branch's copies of what it may name. A link of main
// to an index file names nothing here, as the workspace never gets main's links, and the run starts. A
// builder's run reads as before (13.8, since 1.07, after its security review and its second round).
func TestAJudgeDoesNotStartOnAnIndexFileCrewCannotReadExactly(t *testing.T) {
	entries := strings.TrimSuffix(strings.Repeat(`{"path": "notes"},`, 1025), ",")
	pad := func(n int) string {
		head := `{"entries": [{"path": "notes"}]`
		return head + strings.Repeat(" ", n-len(head)-1) + "}"
	}
	for name, c := range map[string]struct {
		files   map[string]string
		builder bool
		noList  bool // git cannot list an index file of main
		stops   string
		took    string // whose notes/a.md the run has
	}{
		"read exactly":                 {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}]}`}, took: "main's"},
		"a file of 1 MiB":              {files: map[string]string{".agents/rules.json": pad(1 << 20)}, took: "main's"},
		"a mistyped entry":             {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}, {"path": 7}]}`}, stops: "is not JSON Crew reads"},
		"a key Crew does not read":     {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes", "note": "x"}]}`}, stops: "is not JSON Crew reads"},
		"a comment":                    {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}]} // more`}, stops: "is not JSON Crew reads"},
		"an entry through /proc":       {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}, {"path": "/proc/self/cwd/x"}]}`}, stops: "through /proc or /dev"},
		"1025 entries":                 {files: map[string]string{".agents/rules.json": `{"entries": [` + entries + `]}`}, stops: "names more than 1024 folders"},
		"a file past 1 MiB":            {files: map[string]string{".agents/rules.json": pad(1<<20 + 1)}, stops: "is longer than 1048576 bytes"},
		"git cannot list one":          {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}]}`}, noList: true, stops: "exit status 128"},
		"a link to an index file":      {files: map[string]string{".agents/rules.json": "-> ../cfg/r.json", "cfg/r.json": `{"entries": [{"path": "notes"}]}`}, took: "the branch's"},
		"a builder, a mistyped entry":  {files: map[string]string{".agents/rules.json": `{"entries": [{"path": "notes"}, {"path": 7}]}`}, builder: true, took: "the branch's"},
		"a builder, a file past 1 MiB": {files: map[string]string{".agents/rules.json": pad(1<<20 + 1)}, builder: true, took: "main's"},
	} {
		t.Run(name, func(t *testing.T) {
			k := agyKit(t, !c.builder)
			if c.builder {
				var r runspec.Run
				b, _ := os.ReadFile(filepath.Join(k.home, runspec.RunFile))
				json.Unmarshal(b, &r)
				r.MainInstructions = true
				k.writeRun(t, r)
			}
			if c.noList {
				real, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				bin := strings.Split(os.Getenv("PATH"), ":")[0]
				fake := "#!/bin/sh\nfor a; do [ \"$a\" = -l ] && exit 128; done\nexec " + real + " \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			ws := k.env.Workspace
			origin := filepath.Join(t.TempDir(), "origin.git")
			gitIn(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
			seed := t.TempDir()
			gitIn(t, seed, "init", "-q", "-b", "main")
			for f, body := range c.files {
				os.MkdirAll(filepath.Dir(filepath.Join(seed, f)), 0o755)
				if to, ok := strings.CutPrefix(body, "-> "); ok {
					os.Symlink(to, filepath.Join(seed, f))
					continue
				}
				os.WriteFile(filepath.Join(seed, f), []byte(body), 0o644)
			}
			os.MkdirAll(filepath.Join(seed, "notes"), 0o755)
			os.WriteFile(filepath.Join(seed, "notes/a.md"), []byte("main's"), 0o644)
			gitIn(t, seed, "add", "-A")
			gitIn(t, seed, "commit", "-q", "-m", "main")
			gitIn(t, seed, "push", "-q", origin, "main")
			os.MkdirAll(ws, 0o755)
			gitIn(t, ws, "init", "-q", "-b", "task/1-x")
			gitIn(t, ws, "remote", "add", "origin", origin)
			gitIn(t, ws, "fetch", "-q", "origin", "main")
			gitIn(t, ws, "reset", "-q", "--hard", "FETCH_HEAD")
			os.WriteFile(filepath.Join(ws, "notes/a.md"), []byte("the branch's"), 0o644)
			gitIn(t, ws, "commit", "-q", "-am", "branch")
			ex := k.turn(t)
			if c.stops != "" {
				if ex.Reason != "error" || ex.Step != "workspace" || !strings.Contains(ex.Error, "the instructions: ") || !strings.Contains(ex.Error, c.stops) {
					t.Fatalf("%+v", ex)
				}
				return
			}
			if b, _ := os.ReadFile(filepath.Join(ws, "notes/a.md")); ex.Reason != "ok" || string(b) != c.took {
				t.Fatalf("%+v, notes/a.md: %q", ex, b)
			}
		})
	}
}
