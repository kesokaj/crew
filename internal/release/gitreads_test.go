package release_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// starters are the functions that start a program, by the package that has them.
var starters = map[string][]string{
	"os/exec":               {"Command", "CommandContext", "Cmd"},
	"os":                    {"StartProcess"},
	"syscall":               {"ForkExec", "StartProcess", "Exec"},
	"golang.org/x/sys/unix": {"Exec", "ForkExec"},
}

// gitStarts are the places a file of the package at dir names a function that starts a program, or
// os/exec's Cmd, dot-imports a package that has them, or holds a go:linkname directive, by whether the
// file is protected: each read of git's output is to go through the protected reader, so no file
// around it reads git unbounded (6.1, 16.8; since 1.04). A call into another package that starts git is
// not seen.
func gitStarts(t *testing.T, root, dir string) (found, protected []string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no Go file in %s: %v", dir, err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := dir + "/" + filepath.Base(name)
		guarded := slices.ContainsFunc(release.Protected, func(p string) bool { return strings.HasPrefix(rel, p) })
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		var starts []string
		local := map[string]string{}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if _, ok := starters[path]; !ok {
				continue
			}
			as := filepath.Base(path)
			if im.Name != nil {
				as = im.Name.Name
			}
			if as == "." {
				starts = append(starts, rel+": a dot-import of "+path)
			}
			local[as] = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && slices.Contains(starters[local[id.Name]], sel.Sel.Name) {
					starts = append(starts, rel+": "+local[id.Name]+"."+sel.Sel.Name)
				}
			}
			return true
		})
		for _, g := range f.Comments {
			for _, c := range g.List {
				if strings.HasPrefix(c.Text, "//go:linkname") {
					starts = append(starts, rel+": a go:linkname directive")
				}
			}
		}
		if guarded {
			protected = append(protected, starts...)
		} else {
			found = append(found, starts...)
		}
	}
	return found, protected
}

// Of internal/gitstore, only the protected files start git: the reader with its bound, and the
// listing of a tree a check takes (since 1.04: the security review of the change, and the operator's
// popup answer of session 71, "Cap, move, pin by test").
func TestOnlyTheProtectedFilesOfGitstoreStartGit(t *testing.T) {
	found, protected := gitStarts(t, filepath.Join("..", ".."), "internal/gitstore")
	// The walk sees the protected reader start git, or it proves nothing.
	if !slices.Contains(protected, "internal/gitstore/readbound.go: os/exec.CommandContext") {
		t.Fatalf("the walk did not find the protected reader: %v", protected)
	}
	for _, s := range found {
		t.Errorf("%s starts a program, and is not protected", s)
	}
}

// The walk finds a start through another name of the package, a dot-import, and a directive, and only
// in a file that is not protected.
func TestGitStartsCatchesAStartInATree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "gitstore")
	os.MkdirAll(dir, 0o755)
	for name, src := range map[string]string{
		"a.go":         `package gitstore; import run "os/exec"; var _ = run.Command("git")`,
		"b.go":         `package gitstore; import . "syscall"; var _ = Getpid`,
		"c.go":         "package gitstore\nimport _ \"unsafe\"\n//go:linkname start os.StartProcess\nfunc start()",
		"d.go":         `package gitstore; import "os"; var _ = os.StartProcess`,
		"e.go":         `package gitstore; import "os/exec"; var _ = &exec.Cmd{Path: "git"}`,
		"readbound.go": `package gitstore; import "os/exec"; var _ = exec.CommandContext`,
		"a_test.go":    `package gitstore; import "os/exec"; var _ = exec.Command`,
	} {
		os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644)
	}
	found, protected := gitStarts(t, root, "internal/gitstore")
	slices.Sort(found)
	want := []string{"internal/gitstore/a.go: os/exec.Command", "internal/gitstore/b.go: a dot-import of syscall",
		"internal/gitstore/c.go: a go:linkname directive", "internal/gitstore/d.go: os.StartProcess", "internal/gitstore/e.go: os/exec.Cmd"}
	if !slices.Equal(found, want) || !slices.Equal(protected, []string{"internal/gitstore/readbound.go: os/exec.CommandContext"}) {
		t.Fatalf("found %q, protected %q", found, protected)
	}
}
