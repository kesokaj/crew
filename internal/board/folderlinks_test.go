package board_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
)

// mainWithLinks gives the project's main the files, a link as "-> target", in the canonical repository
// and in the quarantine one, as a remote's main or the terminal would.
func mainWithLinks(t *testing.T, e *env, p *board.Project, files map[string]string) {
	t.Helper()
	canon := e.git.Canonical(p.Name)
	seed := t.TempDir()
	git(t, seed, "clone", "-q", canon, ".")
	for f, body := range files {
		full := filepath.Join(seed, f)
		os.RemoveAll(full)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if to, ok := strings.CutPrefix(body, "-> "); ok {
			os.Symlink(to, full)
			continue
		}
		os.WriteFile(full, []byte(body), 0o644)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "main with links")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, canon, "push", "-q", e.git.Quarantine.Path(p.Name), "main")
}

// A link of main that stands for a folder of code is followed within the tree by each guard that reads
// what is code or instructions: a file where it leads is code, so the ticket never merges or comes in
// by itself. A link out of the workspace is an error for each, never a link that leads to nothing (13.8;
// since 1.06).
func TestAFolderLinkOfMainIsFollowedByTheCodeGuards(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	mainWithLinks(t, e, p, map[string]string{".agents/skills": "-> ../shared/skills", "shared/skills/flows/SKILL.md": "# flows\n"})
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	push := func(script func(dir string)) string {
		e.pushTree(p, *tk.Branch, func(dir string) {
			os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644)
			script(dir)
		})
		head, _ := e.b.Head(e.ctx, p, tk)
		if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
			t.Fatalf("follows: %v %v", ok, err)
		}
		return head
	}
	fetched := push(func(string) {})
	head := push(func(dir string) {
		os.WriteFile(filepath.Join(dir, "shared/skills/flows/SKILL.md"), []byte("# flows, run this\n"), 0o644)
	})
	guards := map[string]func() ([]string, error){
		"the merge's check":     func() ([]string, error) { return e.b.CodeChanges(e.ctx, p, tk) },
		"a pull request's push": func() ([]string, error) { return board.CodeChangesAtForTest(e.ctx, e.git, p, head) },
		"a branch that moved on GitHub": func() ([]string, error) {
			return e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched)
		},
	}
	for name, guard := range guards {
		t.Run(name, func(t *testing.T) {
			if files, err := guard(); err != nil || !slices.Equal(files, []string{"shared/skills/flows/SKILL.md"}) {
				t.Fatalf("%v %v", files, err)
			}
		})
	}
	mainWithLinks(t, e, p, map[string]string{".agents/agents": "-> /opt/agents"})
	for name, guard := range guards {
		t.Run(name+", a link out", func(t *testing.T) {
			if files, err := guard(); !errors.Is(err, cli.ErrLinkWay) {
				t.Fatalf("%v %v", files, err)
			}
		})
	}
}

// The check of VENDORED.md reads the skills where a link of .agents/skills leads, and VENDORED.md
// through a link, as the CLI opens them: a branch that changes a skill there is checked, and one that
// takes rows out of the file a link leads to does not pass (12.9, 13.8; since 1.06).
func TestTheCheckOfVendoredMDFollowsTheLinksOfSkills(t *testing.T) {
	rows := "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | 6654f6b | MIT |\n"
	for name, c := range map[string]struct {
		main, branch map[string]string
		want         string
	}{
		"a folder of skills that is a link": {
			main: map[string]string{".agents/skills": "-> ../shared/skills", "shared/skills/VENDORED.md": rows},
			branch: map[string]string{"shared/skills/flows/SKILL.md": "# flows\n",
				"shared/skills/VENDORED.md": "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | | MIT |\n"},
			want: "VENDORED.md: flows has no a pin of a commit",
		},
		"VENDORED.md that is a link": {
			main:   map[string]string{".agents/skills/VENDORED.md": "-> ../../docs/vendored.md", "docs/vendored.md": rows},
			branch: map[string]string{".agents/skills/x/SKILL.md": "# x\n", "docs/vendored.md": "# Vendored\n"},
			want:   "takes rows out of .agents/skills/VENDORED.md: flows",
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.running("shop", "SF")
			mainWithLinks(t, e, p, c.main)
			e.reconcile(p)
			e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{".agents/skills/**", "shared/**", "docs/**"}})
			e.reconcile(p)
			e.b.Checks = scripted()
			e.push(p, *e.ticket(p, "SF-1").Branch, c.branch)
			e.reconcile(p)
			if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, c.want) {
				t.Fatalf("SF-1 is in %s, waiting %q", e.stage(p, "SF-1"), w)
			}
		})
	}
}

// A branch that moved on GitHub is judged by the folder links of main as it is now too: a folder of
// rules that came to main as a link after the branch left it makes what it leads to instruct agents
// (13.8, since 1.06).
func TestABranchThatMovedOnGitHubIsJudgedByTheFolderLinksOfMain(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	push := func(script func(dir string)) string {
		e.pushTree(p, *tk.Branch, script)
		head, _ := e.b.Head(e.ctx, p, tk)
		if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
			t.Fatalf("follows: %v %v", ok, err)
		}
		return head
	}
	fetched := push(func(dir string) {
		os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
		os.WriteFile(filepath.Join(dir, "notes/a.md"), []byte("do this\n"), 0o644)
	})
	push(func(dir string) { os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644) })
	if files, err := e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched); err != nil || len(files) != 0 {
		t.Fatalf("before: %v %v", files, err)
	}
	mainWithLinks(t, e, p, map[string]string{".agents/rules": "-> ../notes"})
	if files, err := e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched); err != nil || !slices.Equal(files, []string{"notes/a.md"}) {
		t.Fatalf("%v %v", files, err)
	}
}

// A branch that moved on GitHub is judged by the folder links of both its heads too: a folder of rules
// that Crew's branch made a link, before 1.06 or with no hook between, makes what it leads to instruct
// agents, though main has no such link (13.8, since 1.06).
func TestABranchThatMovedOnGitHubIsJudgedByTheFolderLinksOfItsHeads(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF", []string{}...)
	e.add(p, ready("One"), board.OriginPlan)
	e.ok(p)
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none")})
	e.reconcile(p)
	tk := e.ticket(p, "SF-1")
	push := func(script func(dir string)) string {
		e.pushTree(p, *tk.Branch, script)
		head, _ := e.b.Head(e.ctx, p, tk)
		if ok, err := e.git.FollowsBranch(e.ctx, "shop", *tk.Branch, head); !ok || err != nil {
			t.Fatalf("follows: %v %v", ok, err)
		}
		return head
	}
	// Each head is made from main, with the link: the one on GitHub, then Crew's.
	withLink := func(note string, more func(dir string)) func(dir string) {
		return func(dir string) {
			os.MkdirAll(filepath.Join(dir, ".agents"), 0o755)
			os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
			os.Symlink("../notes", filepath.Join(dir, ".agents/rules"))
			os.WriteFile(filepath.Join(dir, "notes/a.md"), []byte(note), 0o644)
			more(dir)
		}
	}
	fetched := push(withLink("do this\n", func(string) {}))
	push(withLink("one\n", func(dir string) { os.WriteFile(filepath.Join(dir, "README.md"), []byte("more\n"), 0o644) }))
	files, err := e.b.InstructionsInForTest(e.ctx, p, *tk.Branch, fetched)
	if err != nil || !slices.Equal(files, []string{"notes/a.md"}) {
		t.Fatalf("%v %v", files, err)
	}
}

// The check of VENDORED.md counts a link of skills to the root as every file a skill, and reads the
// skills and VENDORED.md where a link of main leads that came after the branch left it (12.9, 13.8;
// since 1.06, after its review and security review).
func TestTheCheckOfVendoredMDReadsALinkToTheRootAndMainsNewerLinks(t *testing.T) {
	incomplete := "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | | MIT |\n"
	setup := func(t *testing.T) (*env, *board.Project, string) {
		e := newEnv(t)
		p := e.running("shop", "SF")
		e.reconcile(p)
		e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{"**"}})
		e.reconcile(p)
		e.b.Checks = scripted()
		return e, p, *e.ticket(p, "SF-1").Branch
	}
	t.Run("a link to the root", func(t *testing.T) {
		e, p, branch := setup(t)
		mainWithLinks(t, e, p, map[string]string{".agents/skills": "-> .."})
		e.push(p, branch, map[string]string{"flows/SKILL.md": "# flows\n", "VENDORED.md": incomplete})
		e.reconcile(p)
		if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, "VENDORED.md: flows has no a pin of a commit") {
			t.Fatalf("SF-1 is in %s, waiting %q", e.stage(p, "SF-1"), w)
		}
	})
	t.Run("a link of main the branch does not have", func(t *testing.T) {
		e, p, branch := setup(t)
		e.push(p, branch, map[string]string{"shared/skills/flows/SKILL.md": "# flows\n", "shared/skills/VENDORED.md": incomplete})
		mainWithLinks(t, e, p, map[string]string{".agents/skills": "-> ../shared/skills", "shared/skills/README.md": "skills\n"})
		e.reconcile(p)
		if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, "shared/skills/VENDORED.md: flows has no a pin of a commit") {
			t.Fatalf("SF-1 is in %s, waiting %q", e.stage(p, "SF-1"), w)
		}
	})
	t.Run("a link of main to a file of another name", func(t *testing.T) {
		e, p, branch := setup(t)
		e.push(p, branch, map[string]string{".agents/skills/x/SKILL.md": "# x\n", "docs/third-party.md": incomplete})
		mainWithLinks(t, e, p, map[string]string{".agents/skills/VENDORED.md": "-> ../../docs/third-party.md", "docs/third-party.md": "# Third party\n"})
		e.reconcile(p)
		if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, "docs/third-party.md: flows has no a pin of a commit") {
			t.Fatalf("SF-1 is in %s, waiting %q", e.stage(p, "SF-1"), w)
		}
	})
	// A way out of the workspace is an error of the check, on main's side and on the branch's alone.
	t.Run("a way out of main", func(t *testing.T) {
		e, p, branch := setup(t)
		e.push(p, branch, map[string]string{"README.md": "more\n"})
		mainWithLinks(t, e, p, map[string]string{".agents/skills": "-> /opt/skills"})
		tk := e.ticket(p, "SF-1")
		if r, err := board.SkillsVendoredForTest(e.ctx, &board.CheckContext{Project: p, Ticket: tk, Git: e.git}); !errors.Is(err, cli.ErrLinkWay) {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("a way out of the branch", func(t *testing.T) {
		e, p, branch := setup(t)
		e.pushTree(p, branch, func(dir string) {
			os.MkdirAll(filepath.Join(dir, ".agents"), 0o755)
			os.Symlink("/opt/skills", filepath.Join(dir, ".agents/skills"))
		})
		tk := e.ticket(p, "SF-1")
		if r, err := board.SkillsVendoredForTest(e.ctx, &board.CheckContext{Project: p, Ticket: tk, Git: e.git}); !errors.Is(err, cli.ErrLinkWay) {
			t.Fatalf("%+v %v", r, err)
		}
	})
}
