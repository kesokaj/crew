package gitendpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkRepo is a repository whose main holds main's files, links among them as "-> target", with
// the branch task/1-a checked out on it; commit commits the work tree on the branch.
func linkRepo(t *testing.T, main map[string]string) (work string, commit func(files map[string]string) string) {
	t.Helper()
	work = t.TempDir()
	write := func(files map[string]string) {
		for p, body := range files {
			full := filepath.Join(work, p)
			os.RemoveAll(full)
			os.MkdirAll(filepath.Dir(full), 0o755)
			if to, ok := strings.CutPrefix(body, "-> "); ok {
				if err := os.Symlink(to, full); err != nil {
					t.Fatal(err)
				}
				continue
			}
			os.WriteFile(full, []byte(body), 0o644)
		}
	}
	commit = func(files map[string]string) string {
		t.Helper()
		write(files)
		git(t, work, "", "add", "-A")
		if out, err := git(t, work, "", "commit", "-q", "--allow-empty", "-m", "work"); err != nil {
			t.Fatal(out)
		}
		out, _ := git(t, work, "", "rev-parse", "HEAD")
		return strings.TrimSpace(out)
	}
	if out, err := git(t, work, "", "init", "-q", "-b", "main"); err != nil {
		t.Fatal(out)
	}
	commit(main)
	git(t, work, "", "checkout", "-q", "-b", "task/1-a")
	t.Chdir(work)
	return work, commit
}

var within = hookBounds{list: MaxListBytes, index: MaxIndexBytes}

// A push that makes or changes a link on the way to an index file is refused, whatever may_change
// names: the CLI reads what the link leads to as the index file (13.8, since 1.05).
func TestAPushThatChangesALinkOnTheWayToAnIndexFileIsRefused(t *testing.T) {
	plugins := `{"entries": [{"path": "tools"}]}`
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/", "cfg/"}}
	for name, c := range map[string]struct {
		files map[string]string
		link  string
	}{
		"the index file":                  {map[string]string{".agents/plugins.json": "-> ../cfg/p.json", "cfg/p.json": plugins}, ".agents/plugins.json"},
		"a folder above it":               {map[string]string{".agents": "-> cfg", "cfg/plugins.json": plugins}, ".agents"},
		"a link on its way":               {map[string]string{".agents/plugins.json": "-> x", ".agents/x": "-> ../cfg/p.json", "cfg/p.json": plugins}, ".agents/x"},
		"a link to no file":               {map[string]string{".agents/rules.json": "-> ../cfg/r.json"}, ".agents/rules.json"},
		"a link out of the tree, back in": {map[string]string{".agents/skills.json": "-> /home/agent/work/cfg/s.json", "cfg/s.json": "{}"}, ".agents/skills.json"},
	} {
		t.Run(name, func(t *testing.T) {
			_, commit := linkRepo(t, map[string]string{"README.md": "hi\n", ".agents/AGENTS.md": "x\n"})
			refused, err := testPush(context.Background(), rule, commit(c.files), within)
			if err != nil || !strings.Contains(strings.Join(refused, "\n"), c.link+" is a link on the way to an index file, which no push changes") {
				t.Fatalf("%v %v", refused, err)
			}
		})
	}
}

// A link of main on the way to an index file is followed within the tree: the file it leads to, and
// the folders that file names, instruct agents on a push, while a push that leaves the link as it is
// goes on (13.8, since 1.05).
func TestALinkOfMainToAnIndexFileIsFollowedByTheHook(t *testing.T) {
	_, commit := linkRepo(t, map[string]string{
		".agents/plugins.json": "-> ../cfg/p.json", "cfg/p.json": `{"entries": [{"path": "tools"}]}`, "README.md": "hi\n",
	})
	git(t, ".", "", "tag", "start")
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"the file it leads to":           {map[string]string{"cfg/p.json": `{"entries": [{"path": "src"}]}`}, "cfg/p.json instructs agents"},
		"a folder that file names":       {map[string]string{"tools/hook.sh": "x\n"}, "tools/hook.sh instructs agents"},
		"a file put where its folder is": {map[string]string{"cfg": "x\n"}, "cfg instructs agents"},
		"another file":                   {map[string]string{"README.md": "there\n"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			git(t, ".", "", "reset", "-q", "--hard", "start")
			refused, err := testPush(context.Background(), rule, commit(c.files), within)
			got := strings.Join(refused, "\n")
			if err != nil || (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

// A way of main's through links that goes round is an error, and the push is refused, never read as
// an index file that names nothing (since 1.05).
func TestALinkOfMainThatGoesRoundRefusesThePush(t *testing.T) {
	_, commit := linkRepo(t, map[string]string{".agents/plugins.json": "-> a", ".agents/a": "-> b", ".agents/b": "-> a"})
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	if refused, err := testPush(context.Background(), rule, commit(map[string]string{"README.md": "hi\n"}), within); !errors.Is(err, errIndexLink) {
		t.Fatalf("%v %v", refused, err)
	}
}

// The hook reads the index files of main as it is now too: a folder an index file names that came to
// main after the branch left it instructs agents on the branch's push (13.8, since 1.05).
func TestTheHookReadsTheIndexFilesOfMainAsItIsNow(t *testing.T) {
	_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
	git(t, ".", "", "checkout", "-q", "main")
	commit(map[string]string{".agents/rules.json": "-> ../cfg/r.json", "cfg/r.json": `{"entries": [{"path": "notes"}]}`})
	git(t, ".", "", "checkout", "-q", "task/1-a")
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	refused, err := testPush(context.Background(), rule, commit(map[string]string{"notes/a.md": "x\n", "cfg/r.json": "{}"}), within)
	got := strings.Join(refused, "\n")
	if err != nil || !strings.Contains(got, "notes/a.md instructs agents") || !strings.Contains(got, "cfg/r.json instructs agents") {
		t.Fatalf("%q %v", got, err)
	}
}

// A link that leads out of the workspace, and an index file that names a folder through /proc, refuse
// the push, on its side or on main's: the sandbox's own links can lead back into the workspace
// (since 1.05).
func TestAWayOutOfTheWorkspaceRefusesThePush(t *testing.T) {
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/"}}
	t.Run("a pushed link out", func(t *testing.T) {
		_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
		if refused, err := testPush(context.Background(), rule, commit(map[string]string{".agents/skills.json": "-> /proc/self/cwd/s.json"}), within); !errors.Is(err, errIndexLink) {
			t.Fatalf("%v %v", refused, err)
		}
	})
	t.Run("main's entry through /proc", func(t *testing.T) {
		_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
		git(t, ".", "", "checkout", "-q", "main")
		commit(map[string]string{".agents/rules.json": `{"entries": [{"path": "/proc/self/cwd/notes"}]}`})
		git(t, ".", "", "checkout", "-q", "task/1-a")
		if refused, err := testPush(context.Background(), rule, commit(map[string]string{"notes/a.md": "x\n"}), within); !errors.Is(err, errIndexOutside) {
			t.Fatalf("%v %v", refused, err)
		}
	})
}

// A git that cannot say where main is refuses the push, never reads it as a main with no index files
// (since 1.05).
func TestAMainGitCannotFindRefusesThePush(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
	head := commit(map[string]string{"notes/a.md": "x\n"})
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	if refused, err := testPush(context.Background(), rule, head, within); err != nil || len(refused) != 0 {
		t.Fatalf("with main: %v %v", refused, err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nfor a; do [ \"$a\" = for-each-ref ] && exit 128; done\nexec "+real+" \"$@\"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if refused, err := testPush(context.Background(), rule, head, within); !errors.Is(err, errIndexRead) {
		t.Fatalf("a git that cannot find main: %v %v", refused, err)
	}
}
