package gitendpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A push that makes or changes a link at, above or below a file or a folder that instructs is refused,
// whatever may_change names: the CLI reads what the link leads to in its place (13.8, since 1.06). A
// link at .agents is on the way to an index file, refused since 1.05.
func TestAPushThatMakesALinkOfAFolderThatInstructsIsRefused(t *testing.T) {
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/", "AGENTS.md", "sub/", "tools/", "lib/", "shared/"}}
	for name, c := range map[string]struct {
		files map[string]string
		link  string
	}{
		"a folder of skills":         {map[string]string{".agents/skills": "-> ../shared/skills", "shared/skills/flows/SKILL.md": "x\n"}, ".agents/skills"},
		"a folder of rules":          {map[string]string{".agents/rules": "-> ../shared/rules", "shared/rules/a.md": "x\n"}, ".agents/rules"},
		"a folder above a named one": {map[string]string{"lib": "-> shared", "shared/tools/hook.sh": "x\n"}, "lib"},
		"a skill's folder":           {map[string]string{".agents/skills/flows": "-> ../../shared/flows", "shared/flows/SKILL.md": "x\n"}, ".agents/skills/flows"},
		"an AGENTS.md at any depth":  {map[string]string{"sub/AGENTS.md": "-> ../shared/a.md", "shared/a.md": "x\n"}, "sub/AGENTS.md"},
		"a folder an index names":    {map[string]string{"tools": "-> shared/tools", "shared/tools/hook.sh": "x\n"}, "tools"},
		"a link to no folder at all": {map[string]string{".agents/agents": "-> ../nowhere"}, ".agents/agents"},
	} {
		t.Run(name, func(t *testing.T) {
			_, commit := linkRepo(t, map[string]string{"README.md": "hi\n", ".agents/plugins.json": `{"entries": [{"path": "tools"}, {"path": "lib/tools"}]}`})
			refused, err := testPush(context.Background(), rule, commit(c.files), within)
			if err != nil || !strings.Contains(strings.Join(refused, "\n"), c.link+" is a link where a file or a folder that instructs agents is, which no push makes or changes") {
				t.Fatalf("%v %v", refused, err)
			}
		})
	}
	// A link elsewhere is a file as any other.
	_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
	if refused, err := testPush(context.Background(), rule, commit(map[string]string{"docs": "-> site/docs", "site/docs/a.md": "x\n"}), within); err != nil || len(refused) > 0 {
		t.Fatalf("%v %v", refused, err)
	}
}

// A link of main that stands for a folder that instructs is followed within the tree: a file where it
// leads instructs agents on a push, and one beside it does not (13.8, since 1.06).
func TestAFolderLinkOfMainIsFollowedByTheHook(t *testing.T) {
	_, commit := linkRepo(t, map[string]string{
		".agents/rules": "-> ../shared/rules", "shared/rules/a.md": "x\n", ".agents/skills/flows": "-> ../../vendor/flows",
		"vendor/flows/SKILL.md": "x\n", "README.md": "hi\n",
		".agents/plugins.json": `{"entries": [{"path": "lib/tools"}]}`, "lib": "-> shared", "shared/tools/hook.sh": "x\n",
	})
	git(t, ".", "", "tag", "start")
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"where a folder leads":        {map[string]string{"shared/rules/b.md": "do this\n"}, "shared/rules/b.md instructs agents"},
		"where a skill's folder goes": {map[string]string{"vendor/flows/SKILL.md": "y\n"}, "vendor/flows/SKILL.md instructs agents"},
		"below a folder above one":    {map[string]string{"shared/tools/hook.sh": "y\n"}, "shared/tools/hook.sh instructs agents"},
		"a file beside it":            {map[string]string{"shared/README.md": "x\n"}, ""},
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

// A link of main that stands for a folder that instructs and leads out of the workspace refuses the
// push, never read as one that leads to nothing (13.8, since 1.06).
func TestAFolderLinkOfMainOutOfTheWorkspaceRefusesThePush(t *testing.T) {
	_, commit := linkRepo(t, map[string]string{".agents/skills": "-> /opt/skills", "README.md": "hi\n"})
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	if refused, err := testPush(context.Background(), rule, commit(map[string]string{"README.md": "there\n"}), within); !errors.Is(err, errLinkedWay) {
		t.Fatalf("%v %v", refused, err)
	}
	// So does a tree whose listing is past the bound, before any link of it is read.
	if refused, err := testPush(context.Background(), rule, commit(map[string]string{"README.md": "again\n"}), hookBounds{list: 40, index: MaxIndexBytes}); !errors.Is(err, errLinkList) {
		t.Fatalf("%v %v", refused, err)
	}
}

// The hook follows the links of each side on its own: of main as it is now, which may have moved on
// since the branch left it, and of where the branch left main, which the branch may have taken out
// (13.8, since 1.06).
func TestTheHookFollowsTheFolderLinksOfMainAndOfTheBase(t *testing.T) {
	t.Run("main as it is now", func(t *testing.T) {
		_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
		git(t, ".", "", "checkout", "-q", "main")
		commit(map[string]string{".agents/rules": "-> ../notes", "notes/a.md": "x\n"})
		git(t, ".", "", "checkout", "-q", "task/1-a")
		rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
		refused, err := testPush(context.Background(), rule, commit(map[string]string{"notes/b.md": "do this\n"}), within)
		if got := strings.Join(refused, "\n"); err != nil || !strings.Contains(got, "notes/b.md instructs agents") {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("the base", func(t *testing.T) {
		work, commit := linkRepo(t, map[string]string{".agents/rules": "-> ../notes", "notes/a.md": "x\n"})
		git(t, ".", "", "checkout", "-q", "main")
		git(t, ".", "", "rm", "-q", ".agents/rules")
		commit(map[string]string{})
		git(t, ".", "", "checkout", "-q", "task/1-a")
		git(t, work, "", "rm", "-q", ".agents/rules")
		rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything, ".agents/"}}
		refused, err := testPush(context.Background(), rule, commit(map[string]string{"notes/b.md": "do this\n"}), within)
		if got := strings.Join(refused, "\n"); err != nil || !strings.Contains(got, "notes/b.md instructs agents") {
			t.Fatalf("%q %v", got, err)
		}
	})
}

// The hook reads every lookup of a way from one listing of each side's tree, and each link's text once:
// a push of links with long ways starts a few git processes, not one for each step of each way (13.8,
// since 1.06, after its security review).
func TestTheHookListsEachTreeOnceForItsLinks(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
	deep := strings.Repeat("d/", 100) + "x.md"
	files := map[string]string{deep: "x\n"}
	for i := range 200 {
		files[fmt.Sprintf("p%d/AGENTS.md", i)] = "-> ../" + deep
	}
	head := commit(files)
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\necho >> "+count+"\nexec "+real+" \"$@\"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	// Each of the 200 links and the file they lead to is refused.
	refused, err := testPush(context.Background(), rule, head, within)
	calls, _ := os.ReadFile(count)
	if err != nil || len(refused) != 201 || strings.Count(string(calls), "\n") > 40 {
		t.Fatalf("%d refused, %d git calls: %v", len(refused), strings.Count(string(calls), "\n"), err)
	}
}

// A link of main to the root of the tree makes every path instruct agents, and a tree of main past the
// bound of a listing refuses the push, never read as one with no links (13.8, since 1.06).
func TestAFolderLinkOfMainToTheRootOrPastTheBound(t *testing.T) {
	rule := PathRule{Base: "refs/heads/main", Allow: []string{Everything}}
	t.Run("to the root", func(t *testing.T) {
		_, commit := linkRepo(t, map[string]string{".agents/rules": "-> ..", "README.md": "hi\n"})
		refused, err := testPush(context.Background(), rule, commit(map[string]string{"src/a.go": "package a\n"}), within)
		if got := strings.Join(refused, "\n"); err != nil || !strings.Contains(got, "src/a.go instructs agents") {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("main past the bound", func(t *testing.T) {
		_, commit := linkRepo(t, map[string]string{"README.md": "hi\n"})
		git(t, ".", "", "checkout", "-q", "main")
		big := map[string]string{}
		for i := range 4 {
			big[fmt.Sprintf("%s%d.md", strings.Repeat("n", 100), i)] = "x\n"
		}
		commit(big)
		git(t, ".", "", "checkout", "-q", "task/1-a")
		head := commit(map[string]string{"a.md": "x\n"})
		if refused, err := testPush(context.Background(), rule, head, hookBounds{list: 250, index: MaxIndexBytes}); !errors.Is(err, errLinkList) {
			t.Fatalf("%v %v", refused, err)
		}
	})
}
