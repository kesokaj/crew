package release_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// sandboxGuards are the functions that keep a sandbox apart, by package, each as Type.name for a
// method and name for a function: whose it is, its bridge and its mode, the commands in it, a failed
// start's clean-up, its start, doors, removal and adoption, the proof of Model only, a hosted microVM
// with its bridge and proof, the helpers they call, the microVM runtimes an install may name, and the
// check that the server's network forwards nothing (13.6, since 0.83). Each is protected (16.8),
// wherever a split puts it (security review of the release of 0.3.4, finding 1, its extensions, and
// the review of its fixes).
var sandboxGuards = map[string][]string{
	"orchestrator": {"Orchestrator.sandboxTarget", "Orchestrator.notOurs", "Orchestrator.labelledOurs",
		"Orchestrator.ours", "Orchestrator.ownLabels", "Orchestrator.bridge", "modeOf", "Orchestrator.Exec",
		"Orchestrator.StartInteractive", "Orchestrator.ExecDetached",
		"Orchestrator.Running", "Orchestrator.clearFailedStart", "Orchestrator.leftContainer",
		"Orchestrator.StartSandbox", "Orchestrator.CutSandbox", "Orchestrator.RemoveSandbox", "Orchestrator.AdoptSandbox",
		"Orchestrator.SandboxAt", "Orchestrator.SandboxMode", "Orchestrator.proveModelOnly",
		"Orchestrator.StartHosted", "Orchestrator.CreateHostedNet", "Orchestrator.JoinIngress",
		"Orchestrator.ProbeHosted", "Orchestrator.Pause", "Orchestrator.HostedRunning",
		"brokerIPForSubnet", "Orchestrator.bridgeSubnet", "Orchestrator.forceRemove",
		"Orchestrator.waitReady"},
	"config": {"CheckRuntime", "IsVMRuntime", "CheckIPForward", "IPForwardOff", "IPForwardMustRefuse"},
}

// A release that changes the file a sandbox guard is defined in is protected, so moving one into a new
// file does not drop it from the list.
func TestEverySandboxGuardIsInAProtectedFile(t *testing.T) {
	for pkg, guards := range sandboxGuards {
		where := definedIn(t, pkg)
		for _, name := range guards {
			path, ok := where[name]
			if !ok {
				t.Errorf("%s is defined in no file of internal/%s", name, pkg)
				continue
			}
			running := release.Manifest{Files: map[string]string{path: "a"}}
			next := release.Manifest{Files: map[string]string{path: "b"}}
			if k := release.Classify(running, next, 2); len(k.Protected) == 0 {
				t.Errorf("%s is in %s, which is not protected", name, path)
			}
		}
	}
}

// definedIn is the file of each function of internal/<pkg>, by Type.name or name; a name defined twice
// fails, so a moved guard cannot hide behind another of its name.
func definedIn(t *testing.T, pkg string) map[string]string {
	t.Helper()
	dir := filepath.Join("..", pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				typ := fn.Recv.List[0].Type
				if star, ok := typ.(*ast.StarExpr); ok {
					typ = star.X
				}
				if id, ok := typ.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			path := "internal/" + pkg + "/" + e.Name()
			if prev, ok := where[name]; ok {
				t.Errorf("%s is defined in %s and %s", name, prev, path)
			}
			where[name] = path
		}
	}
	return where
}
