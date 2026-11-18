package release_test

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// module is the import path of Crew's own packages. Packages of other modules are pinned by go.mod and
// go.sum, which are protected.
const module = "github.com/kesokaj/crew"

// envWriters are the functions that change a process's environment, or replace the process with one
// of an environment the caller chooses (Exec), by the package that has them.
var envWriters = map[string][]string{
	"os":                    {"Setenv", "Unsetenv", "Clearenv"},
	"syscall":               {"Setenv", "Unsetenv", "Clearenv", "Exec"},
	"golang.org/x/sys/unix": {"Setenv", "Unsetenv", "Clearenv", "Exec"},
}

// Protection by path decides which releases wait for Install; it is no boundary inside the server's
// process, where cmd/crew/ reads what the ingress's door answers (CREW_DOOR_NAME, check mode) from the
// environment, after every linked package's init has run. So no file the server links that is not
// protected may change the environment, and one that should has to be protected first (16.8; review of
// 7888ce6..b15241e, finding 1).
func TestNoUnprotectedFileOfTheServerChangesItsEnvironment(t *testing.T) {
	found, pkgs, unprotected := unprotectedEnvWrites(t, filepath.Join("..", ".."))
	// The walk reaches past the protected packages, or the test proves nothing.
	for _, want := range []string{"cmd/crew", ".", "internal/pipeline", "internal/release"} {
		if _, ok := pkgs[want]; !ok {
			t.Fatalf("the server's packages do not hold %s: %v", want, slices.Sorted(maps.Keys(pkgs)))
		}
	}
	if unprotected == 0 {
		t.Fatal("no file of the server is unprotected: the walk read nothing to check")
	}
	for _, w := range found {
		t.Errorf("%s changes the server's environment, and is not protected", w)
	}
	t.Logf("%d packages of the server, %d of their files not protected", len(pkgs), unprotected)
}

// A tree whose server links writers is caught in each file that is not protected, through the module's
// root package and a file of another architecture, and passed where the file is protected.
func TestUnprotectedEnvWritesCatchesAWriterInATree(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"cmd/crew/main.go": `package main; import (_ "github.com/kesokaj/crew"; _ "github.com/kesokaj/crew/internal/pipeline"; ` +
			`_ "github.com/kesokaj/crew/internal/config"; "os"); func init() { os.Setenv("A", "b") }`,
		"crew.go":                              `package crew; import "syscall"; var exec = syscall.Exec`,
		"internal/pipeline/pipeline.go":        `package pipeline; import "os"; func init() { os.Setenv("A", "b") }`,
		"internal/pipeline/link.go":            "package pipeline\n//go:linkname setenv os.Setenv\nfunc setenv(string, string) error",
		"internal/pipeline/arch_arm64.go":      `package pipeline; import ("os"; _ "github.com/kesokaj/crew/internal/onlyarm"); func init() { os.Unsetenv("A") }`,
		"internal/pipeline/arch_arm64_test.go": `package pipeline; import "os"; func init() { os.Setenv("T", "1") }`,
		"internal/onlyarm/arm.go":              `package onlyarm; import "os"; func init() { os.Clearenv() }`,
		"internal/config/config.go":            `package config; import "os"; func init() { os.Setenv("A", "b") }`,
	}
	for name, src := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, _, _ := unprotectedEnvWrites(t, root)
	slices.Sort(found)
	want := []string{"crew.go:1: syscall.Exec", "internal/onlyarm/arm.go:1: os.Clearenv",
		"internal/pipeline/arch_arm64.go:1: os.Unsetenv",
		"internal/pipeline/link.go:2: a go:linkname directive", "internal/pipeline/pipeline.go:1: os.Setenv"}
	if !slices.Equal(found, want) {
		t.Fatalf("%q, want %q", found, want)
	}
}

// unprotectedEnvWrites walks the server's packages under root and gives each environment writer in a
// file that is not protected, with the packages and the number of such files it read.
func unprotectedEnvWrites(t *testing.T, root string) ([]string, map[string][]string, int) {
	t.Helper()
	pkgs := linkedPackages(t, root, "cmd/crew")
	var found []string
	unprotected := 0
	for _, dir := range slices.Sorted(maps.Keys(pkgs)) {
		for _, name := range pkgs[dir] {
			path := pathpkg.Join(dir, name)
			running := release.Manifest{Files: map[string]string{path: "a"}}
			next := release.Manifest{Files: map[string]string{path: "b"}}
			if k := release.Classify(running, next, 2); len(k.Protected) != 0 {
				continue
			}
			unprotected++
			src, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			found = append(found, envWrites(t, path, string(src))...)
		}
	}
	return found, pkgs, unprotected
}

// The finder of envWrites sees each way a file can name a writer, and nothing else.
func TestEnvWritesFindsEveryName(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"a call", `import "os"; func f() { os.Setenv("A", "b") }`, []string{"x.go:1: os.Setenv"}},
		{"a value", `import "os"; var g = os.Unsetenv`, []string{"x.go:1: os.Unsetenv"}},
		{"another name", `import o "os"; func f() { o.Clearenv() }`, []string{"x.go:1: os.Clearenv"}},
		{"syscall", `import "syscall"; func f() { syscall.Setenv("A", "b") }`, []string{"x.go:1: syscall.Setenv"}},
		{"unix", `import "golang.org/x/sys/unix"; func f() { unix.Unsetenv("A") }`, []string{"x.go:1: golang.org/x/sys/unix.Unsetenv"}},
		{"a dot import", `import . "os"; func f() { Setenv("A", "b") }`, []string{`x.go:1: a dot import of "os"`}},
		{"exec", `import "syscall"; func f() { syscall.Exec("/x", nil, []string{"A=b"}) }`, []string{"x.go:1: syscall.Exec"}},
		{"a linkname", "import _ \"unsafe\"\n//go:linkname f os.Setenv\nfunc f(string, string) error", []string{"x.go:2: a go:linkname directive"}},
		{"a read", `import "os"; func f() string { return os.Getenv("A") }`, nil},
		{"another package's Setenv", `import "example.com/os"; func f() { os.Setenv("A", "b") }`, nil},
	}
	for _, c := range cases {
		got := envWrites(t, "x.go", "package x; "+c.src)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// linkedPackages walks the imports of Crew's own packages from the command in dir, as the server's
// image builds it (linux, amd64, no cgo), and gives each package's Go files that are not tests, with
// the non-test files its build constraints leave out, which another build would link, and the
// packages those files import (register row 76).
func linkedPackages(t *testing.T, root, dir string) map[string][]string {
	t.Helper()
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled = "linux", "amd64", false
	pkgs := map[string][]string{}
	queue := []string{dir}
	enqueue := func(imp string) {
		if imp == module {
			queue = append(queue, ".")
		} else if rest, ok := strings.CutPrefix(imp, module+"/"); ok {
			queue = append(queue, rest)
		}
	}
	for len(queue) > 0 {
		dir, queue = queue[0], queue[1:]
		if _, ok := pkgs[dir]; ok {
			continue
		}
		p, err := ctxt.ImportDir(filepath.Join(root, dir), 0)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		files := append([]string(nil), p.GoFiles...)
		for _, name := range p.IgnoredGoFiles {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			files = append(files, name)
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("%s/%s: %v", dir, name, err)
			}
			for _, spec := range f.Imports {
				enqueue(strings.Trim(spec.Path.Value, `"`))
			}
		}
		pkgs[dir] = files
		for _, imp := range p.Imports {
			enqueue(imp)
		}
	}
	return pkgs
}

// envWrites lists each name of an environment writer in src, as "path:line: package.Name", each dot
// import of a package that has one, whose names cannot be told from the file's own, and each
// go:linkname directive, which can name a writer by its symbol alone.
func envWrites(t *testing.T, path, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				found = append(found, fmt.Sprintf("%s:%d: a go:linkname directive", path, fset.Position(c.Pos()).Line))
			}
		}
	}
	local := map[string]string{}
	for _, imp := range f.Imports {
		ipath, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := envWriters[ipath]; !ok {
			continue
		}
		name := ipath[strings.LastIndex(ipath, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			found = append(found, fmt.Sprintf("%s:%d: a dot import of %q", path, fset.Position(imp.Pos()).Line, ipath))
		}
		local[name] = ipath
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ipath, ok := local[id.Name]; ok && slices.Contains(envWriters[ipath], sel.Sel.Name) {
			found = append(found, fmt.Sprintf("%s:%d: %s.%s", path, fset.Position(sel.Pos()).Line, ipath, sel.Sel.Name))
		}
		return true
	})
	return found
}
