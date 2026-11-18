package board_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// A package file is a manifest or a lock file of the ecosystems Crew names, by its base name at any
// depth (13.8): a name that only contains one, or a file in a folder named like one, is not.
func TestWhatAPackageFileIs(t *testing.T) {
	for p, want := range map[string]string{
		"go.mod": "manifest", "ingress/go.mod": "manifest", "go.work": "manifest", "web/package.json": "manifest",
		"requirements.txt": "manifest", "requirements-dev.txt": "manifest", "py/requirements_test.txt": "manifest",
		"pyproject.toml": "manifest", "setup.py": "manifest", "setup.cfg": "manifest", "Pipfile": "manifest",
		"rust/Cargo.toml": "manifest", "Gemfile": "manifest", "composer.json": "manifest", "pom.xml": "manifest",
		"app/build.gradle": "manifest", "app/build.gradle.kts": "manifest", "deno.json": "manifest",
		"testdata/mod/go.mod": "manifest", "vendor/x/go.mod": "manifest",
		"go.sum": "lock", "ingress/go.sum": "lock", "go.work.sum": "lock", "web/package-lock.json": "lock",
		"npm-shrinkwrap.json": "lock", "yarn.lock": "lock", "pnpm-lock.yaml": "lock", "bun.lockb": "lock",
		"Pipfile.lock": "lock", "poetry.lock": "lock", "uv.lock": "lock", "Cargo.lock": "lock", "Gemfile.lock": "lock",
		"composer.lock": "lock", "deno.lock": "lock",
		"dev-requirements.txt": "manifest", "requirements/base.txt": "manifest", "constraints.txt": "manifest",
		"settings.gradle": "manifest", "settings.gradle.kts": "manifest", "gradle/libs.versions.toml": "manifest",
		"Package.swift": "manifest", "mix.exs": "manifest", "packages.config": "manifest", "Directory.Packages.props": "manifest",
		"src/App/App.csproj": "manifest", "Lib.fsproj": "manifest", "Old.vbproj": "manifest",
		".npmrc": "manifest", "web/.npmrc": "manifest", ".yarnrc": "manifest", ".yarnrc.yml": "manifest", ".pnpmfile.cjs": "manifest",
		"pip.conf": "manifest", ".cargo/config.toml": "manifest", "rust/.cargo/config": "manifest",
		"bun.lock": "lock", "Package.resolved": "lock", "mix.lock": "lock", "gradle.lockfile": "lock", "packages.lock.json": "lock",
		"uv.toml": "manifest", "bunfig.toml": "manifest", "nuget.config": "manifest", "NuGet.Config": "manifest", "gems.rb": "manifest",
		"pnpm-workspace.yaml": "manifest", "gradle/wrapper/gradle-wrapper.properties": "manifest", ".mvn/maven.config": "manifest",
		".mvn/extensions.xml": "manifest", ".mvn/wrapper/maven-wrapper.properties": "manifest", ".bundle/config": "manifest",
		"gems.locked": "lock", ".yarn/releases/yarn-4.1.0.cjs": "lock", "web/.yarn/plugins/x.cjs": "lock",
		".mvn/settings.xml": "manifest", ".yarn/plugins/x.cjs": "lock", "web/.yarn/releases/x.cjs": "lock",
		"NuGet.config": "manifest", "deno.jsonc": "manifest", "package.yaml": "manifest", "web/package.json5": "manifest",
		"crew.gemspec": "manifest", "Directory.Build.props": "manifest", "pdm.lock": "lock",
		"requirements/dev.in": "manifest", "requirements.in": "manifest", "gradle/wrapper/gradle-wrapper.jar": "lock",
		".mvn/wrapper/maven-wrapper.jar": "lock", ".gemspec": "", "notes.in": "", "wrapper.jar": "",
		"settings.xml": "", "bundle/config": "", "yarn/releases/x.cjs": "", ".yarnrc.json": "",
		".csproj": "", "config.toml": "", "cargo/config.toml": "", "docs/requirements.md": "", "notes.txt": "",
		"go.mod.bak": "", "mygo.mod": "", "go.mod/readme.md": "", "requirements.md": "", "requirements": "",
		"package.json6": "", "versions.lock": "", "vendor/x/a.go": "", "node_modules/x/index.js": "", "": "",
	} {
		if got := board.PackageFileKind(p); got != want {
			t.Errorf("%q: %q, want %q", p, got, want)
		}
	}
}

// review_covers_head refuses when it cannot tell whether a review is owed (BUG-012): git cannot read
// the ticket's branch, or the database cannot be read, and the reason it gives holds no error's text,
// which an agent could read through get_ticket. With the project's Review off it passes, and reads
// nothing: no database and no git are given.
func TestAReviewOwedThatCannotBeToldRefuses(t *testing.T) {
	ctx := context.Background()
	d, _ := settings.ProjectDefaults("shop", "SF")
	bad := "not a branch Crew makes"
	c := &board.CheckContext{Project: &board.Project{Name: "shop"}, Ticket: &board.Ticket{Stage: settings.StageTest, Branch: &bad},
		Pipeline: board.PipelineOf(d), Git: &gitstore.Store{}}
	r, err := board.ReviewCoversHeadForTest(ctx, c)
	if err != nil || r.Passed || !strings.Contains(r.Detail, "cannot be compared with main") {
		t.Fatalf("an unreadable branch: %+v %v", r, err)
	}
	c = &board.CheckContext{Tx: failingTx{}, Project: &board.Project{Name: "shop"}, Ticket: &board.Ticket{Stage: settings.StageTest},
		Pipeline: board.PipelineOf(d)}
	r, err = board.ReviewCoversHeadForTest(ctx, c)
	if err != nil || r.Passed || r.Detail != "Crew could not tell whether a review of its head is owed" {
		t.Fatalf("an unreadable database: %+v %v", r, err)
	}
	for _, st := range d["stages"].([]any) {
		if s := st.(map[string]any); s["id"] == settings.StageReview {
			s["on"] = false
		}
	}
	c = &board.CheckContext{Project: &board.Project{Name: "shop"}, Ticket: &board.Ticket{Stage: settings.StageTest}, Pipeline: board.PipelineOf(d)}
	if r, err := board.ReviewCoversHeadForTest(ctx, c); err != nil || !r.Passed {
		t.Fatalf("with Review off: %+v %v", r, err)
	}
}

// failingTx is a transaction whose every read fails.
type failingTx struct{ pgx.Tx }

func (failingTx) QueryRow(context.Context, string, ...any) pgx.Row { return failingRow{} }

type failingRow struct{}

func (failingRow) Scan(...any) error { return errors.New("the database is gone") }

// A package file past the cap of what Compare lists is still listed, cut and with no counts, so the
// reviewer reads it in its workspace; one within the cap has its counts, and a lock file no diff.
func TestAPackageFilePastTheCapIsListedCut(t *testing.T) {
	e := newEnv(t)
	p := e.project("shop", "SF")
	files := map[string]string{"go.mod": "module shop\n\nrequire example.com/x v1.0.0\n", "go.sum": "example.com/x v1.0.0 h1:x=\n",
		"zz/go.mod": "module zz\n", "zz/go.sum": "example.com/y v1.0.0 h1:y=\n"}
	for i := 0; i < gitstore.MaxFiles+5; i++ { // sorted between go.sum and zz/
		files[fmt.Sprintf("m/%04d.txt", i)] = fmt.Sprintf("file %d\n", i)
	}
	e.push(p, "task/SF-1-many", files)
	head, err := e.git.Head(e.ctx, "shop", "task/SF-1-many")
	if err != nil || head == "" {
		t.Fatalf("head %q: %v", head, err)
	}
	got, err := e.b.PackageFiles(e.ctx, p, head)
	if err != nil || len(got) != 4 {
		t.Fatalf("%+v %v", got, err)
	}
	mod, sum, zmod, zsum := got[0], got[1], got[2], got[3]
	if mod.Path != "go.mod" || mod.Cut || mod.Added != 3 || !strings.Contains(mod.Patch, "+require example.com/x v1.0.0") {
		t.Errorf("go.mod within the cap: %+v", mod)
	}
	if sum.Path != "go.sum" || !sum.Lock || sum.Cut || sum.Added != 1 || sum.Patch != "" {
		t.Errorf("go.sum within the cap: %+v", sum)
	}
	for _, f := range []board.PackageFile{zmod, zsum} {
		if !strings.HasPrefix(f.Path, "zz/") || !f.Cut || f.Added != 0 || f.Removed != 0 || f.Patch != "" {
			t.Errorf("%s past the cap: %+v", f.Path, f)
		}
	}
	if !zsum.Lock || zmod.Lock {
		t.Errorf("kinds past the cap: %+v %+v", zmod, zsum)
	}
}

// A waiting reason names package files quoted, since a path is the builder's text, and at most five.
func TestAWaitingReasonQuotesItsPackageFiles(t *testing.T) {
	var files []board.PackageFile
	for _, p := range []string{"go.mod", "a, b/go.mod", "x\ny/go.sum", "d/go.mod", "e/go.mod", "f/go.mod", "g/go.mod"} {
		files = append(files, board.PackageFile{FileDiff: gitstore.FileDiff{Path: p}})
	}
	want := `"go.mod", "a, b/go.mod", "x\ny/go.sum", "d/go.mod", "e/go.mod" and 2 more`
	if got := board.PackagePathsForTest(files); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := board.PackagePathsForTest(files[:1]); got != `"go.mod"` {
		t.Errorf("one: %s", got)
	}
}

// A project's remote has no SQL writer in the repository other than insertProject in
// internal/board/projects.go, and no UPDATE projects writes remote (16.8, register row 76).
func TestAProjectsRemoteHasNoWriterButInsertProject(t *testing.T) {
	var insertWriters []string
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, "_test.go") {
				return err
			}
			if !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".sql") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, filepath.Join("..", "..")+string(filepath.Separator)))
			text := string(b)
			for _, part := range strings.Split(text, "UPDATE projects")[1:] {
				stmt, _, _ := strings.Cut(part, ";")
				stmt, _, _ = strings.Cut(stmt, "`")
				if strings.Contains(stmt, "remote") {
					t.Errorf("%s updates projects.remote: %s", rel, stmt)
				}
			}
			for _, part := range strings.Split(text, "INSERT INTO projects")[1:] {
				cols, _, _ := strings.Cut(part, ")")
				if strings.Contains(cols, "remote") {
					insertWriters = append(insertWriters, rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(insertWriters) != 1 || insertWriters[0] != "internal/board/projects.go" {
		t.Fatalf("writers of projects.remote = %v, want [internal/board/projects.go]", insertWriters)
	}
}
