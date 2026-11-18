package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// tree is a small module laid out as Crew's: cmd/crew-run uses internal/runspec, which embeds a
// file; internal/other is no input of the runtime image.
func tree() fstest.MapFS {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s), Mode: 0o644} }
	x := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s), Mode: 0o755} }
	return fstest.MapFS{
		"go.mod":                            f("module example.com/crew\n\ngo 1.27\n"),
		"go.sum":                            f(""),
		"versions.lock":                     f("GO_VERSION=1.27\nMACHINE_SETUP=2\n"),
		"image/Dockerfile":                  f("FROM scratch\n"),
		"image/configs/crew-setup":          x("#!/bin/sh\n"),
		"cmd/crew-run/main.go":              f("package main\n\nimport _ \"example.com/crew/internal/runspec\"\n\nfunc main() {}\n"),
		"cmd/crew-run/main_test.go":         f("package main\n"),
		"cmd/crew-harnessbuild/main.go":     f("package main\n\nfunc main() {}\n"),
		"internal/runspec/runspec.go":       f("package runspec\n\nimport _ \"embed\"\n\n//go:embed schema.json\nvar Schema []byte\n"),
		"internal/runspec/runspec_win.go":   f("//go:build windows\n\npackage runspec\n"),
		"internal/runspec/schema.json":      f("{}"),
		"internal/other/other.go":           f("package other\n"),
		"harness/agents/builder.md":         f("---\nmodel: x\n---\nBuild.\n"),
		"harness/hooks/stop.sh":             x("#!/bin/sh\n"),
		"harness/settings.json":             f("{}"),
		"internal/db/migrations/0001_a.sql": f("SELECT 1;\n"),
		"deploy/crew-ctl":                   x("#!/bin/sh\n"),
		"deploy/release.sh":                 x("#!/bin/sh\n"),
		"deploy/host-setup.sh":              x("#!/bin/sh\n"),
		"deploy/proof.sh":                   x("#!/bin/sh\n"),
		"deploy/compose.yaml":               f("services: {}\n"),
		"deploy/db/10-roles.sh":             x("#!/bin/sh\n"),
		"deploy/lib/kata-config.sh":         f("x=1\n"),
		"deploy/firewall/crew-firewall.sh":  x("#!/bin/sh\n"),
		"deploy/Dockerfile.server":          f("FROM scratch\n"),
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The runtime image's inputs are what its build reads: image/, versions.lock, go.mod and go.sum, what
// harness/ holds besides text, and the Go files and embedded files of the packages crew-run and
// crew-harnessbuild are built from, for linux. Not a test, not another platform's file, not a package
// the programs do not use, and not text for agents (16.2 step 5).
func TestTheRuntimeInputsAreWhatTheImageIsBuiltFrom(t *testing.T) {
	in, err := RuntimeInputs(tree())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/crew-harnessbuild/main.go", "cmd/crew-run/main.go", "go.mod", "go.sum", "harness/hooks/stop.sh",
		"harness/settings.json", "image/Dockerfile", "image/configs/crew-setup", "internal/runspec/runspec.go",
		"internal/runspec/schema.json", "versions.lock"}
	if got := keys(in); !slices.Equal(got, want) {
		t.Fatalf("inputs %v\nwant   %v", got, want)
	}
}

// A change to internal/runspec, to versions.lock, or to a hook changes the inputs' hash; a change to a
// prompt or to a package crew-run does not use does not.
func TestTheRuntimeHashChangesWithItsInputsOnly(t *testing.T) {
	base, _ := RuntimeInputs(tree())
	h := RuntimeSHA256(base)
	for name, tc := range map[string]struct {
		path, body string
		differs    bool
	}{
		"runspec":       {"internal/runspec/runspec.go", "package runspec\n\nimport _ \"embed\"\n\n//go:embed schema.json\nvar Schema []byte\nvar X int\n", true},
		"versions.lock": {"versions.lock", "GO_VERSION=1.28\nMACHINE_SETUP=2\n", true},
		"embedded":      {"internal/runspec/schema.json", "{\"a\":1}", true},
		"a hook":        {"harness/hooks/stop.sh", "#!/bin/sh\nexit 1\n", true},
		"a prompt":      {"harness/agents/builder.md", "---\nmodel: x\n---\nBuild well.\n", false},
		"another pkg":   {"internal/other/other.go", "package other\nvar Y int\n", false},
		"a test":        {"cmd/crew-run/main_test.go", "package main\nvar Z int\n", false},
	} {
		tr := tree()
		tr[tc.path] = &fstest.MapFile{Data: []byte(tc.body), Mode: 0o644}
		in, err := RuntimeInputs(tr)
		if err != nil {
			t.Fatal(name, err)
		}
		if (RuntimeSHA256(in) != h) != tc.differs {
			t.Errorf("%s: the hash differs: %v, want %v", name, RuntimeSHA256(in) != h, tc.differs)
		}
	}
	a := Manifest{Runtime: base, RuntimeSHA256: h}
	if !SameRuntime(a, a) || SameRuntime(Manifest{}, a) || SameRuntime(Manifest{}, Manifest{}) || SameRuntime(a, Manifest{Runtime: base, RuntimeSHA256: h, Files: nil}) != true || SameRuntime(a, Manifest{Runtime: base, RuntimeSHA256: strings.Repeat("0", 64)}) {
		t.Fatal("SameRuntime")
	}
}

// A path with a control character in it is never the same runtime: one with a newline can make other
// files under other names hash as the running release's, and the live steps would get the login
// without the press (security review of M16 P5).
func TestARuntimeInputWhosePathHasAControlCharacterIsNeverTheSame(t *testing.T) {
	h1, h2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	running := map[string]string{"image/a": h1, "image/b": h2}
	forged := map[string]string{"image/a\n" + h2 + "  image/b": h1}
	if RuntimeSHA256(running) != RuntimeSHA256(forged) {
		t.Fatal("the two inputs are expected to hash the same, which is why the path is checked")
	}
	run := Manifest{Runtime: running, RuntimeSHA256: RuntimeSHA256(running)}
	if SameRuntime(run, Manifest{Runtime: forged, RuntimeSHA256: RuntimeSHA256(forged)}) {
		t.Fatal("a path with a newline is the same runtime")
	}
	for _, c := range []string{"\t", "\x00", "\u202e", "\u0085"} {
		in := map[string]string{"image/a" + c: h1}
		if SameRuntime(Manifest{Runtime: in, RuntimeSHA256: RuntimeSHA256(in)}, Manifest{Runtime: in, RuntimeSHA256: RuntimeSHA256(in)}) {
			t.Errorf("a path with %q is the same runtime", c)
		}
	}
	if !SameRuntime(run, run) {
		t.Fatal("plain paths are no longer the same")
	}
}

// A path that is never the same runtime is in the diff, even when its hash is the running release's:
// otherwise the gate waits for a press it never offers (review of M16, F1). An empty diff says why.
func TestARuntimeDiffListsEveryPathThatIsNeverTheSame(t *testing.T) {
	h1 := strings.Repeat("1", 64)
	in := map[string]string{"image/a\t": h1, "image/b": h1}
	m := Manifest{Runtime: in, RuntimeSHA256: RuntimeSHA256(in)}
	if SameRuntime(m, m) {
		t.Fatal("a path with a tab is the same runtime")
	}
	if d := RuntimeDiff(m, m); !slices.Equal(d, []string{"image/a\t"}) || NoDiff(m, m) != "" {
		t.Fatalf("diff %q, why %q", d, NoDiff(m, m))
	}
	plain := map[string]string{"image/b": h1}
	p := Manifest{Runtime: plain, RuntimeSHA256: RuntimeSHA256(plain)}
	if d := RuntimeDiff(p, p); len(d) != 0 || NoDiff(p, p) != "" {
		t.Fatalf("the same runtime: diff %q, why %q", d, NoDiff(p, p))
	}
	bad := Manifest{Runtime: plain, RuntimeSHA256: strings.Repeat("0", 64)}
	for _, tc := range []struct {
		running, next Manifest
		want          string
	}{
		{p, bad, NoDiffBadHash},
		{bad, p, NoDiffBadHash},
		{Manifest{}, Manifest{}, NoDiffNoInputs},
		// A candidate whose recorded hash is not its inputs' gives no diff to press on, though its
		// inputs differ (security review of ef7a82a..4a601e2).
		{p, Manifest{Runtime: in, RuntimeSHA256: strings.Repeat("0", 64)}, NoDiffBadHash},
	} {
		if NoDiff(tc.running, tc.next) != tc.want {
			t.Errorf("%+v %+v: why %q", tc.running, tc.next, NoDiff(tc.running, tc.next))
		}
	}
}

// Crew's own tree: crew-run is built from internal/runspec and internal/cli, and not from the
// server's packages, and every input is a file of the tree.
func TestCrewsRuntimeInputs(t *testing.T) {
	root := os.DirFS("../..")
	in, err := RuntimeInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cmd/crew-run/main.go", "internal/runspec/runspec.go", "versions.lock", "image/Dockerfile"} {
		if in[p] == "" {
			t.Errorf("%s is no input", p)
		}
	}
	for p := range in {
		if strings.HasPrefix(p, "internal/httpapi/") || strings.HasPrefix(p, "internal/updater/") || strings.HasSuffix(p, "_test.go") || TextFile(p) {
			t.Errorf("%s is an input", p)
		}
	}
	found := false
	for p := range in {
		found = found || strings.HasPrefix(p, "internal/cli/")
	}
	if !found {
		t.Error("internal/cli is no input: the adapter of agy runs in the image")
	}
}

// tarOf is fsys as git archive writes it: each file with its mode.
func tarOf(t *testing.T, fsys fs.FS) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: p + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: p, Typeflag: tar.TypeReg, Mode: int64(st.Mode().Perm()), Size: int64(len(body))}); err != nil {
			return err
		}
		_, err = tw.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A tree read from git archive's tar gives what the tree itself gives: the same inputs, schema, and
// machine setup, and the deploy archive byte for byte, which holds only the deploy files, at their
// paths, with their modes (16.12).
func TestATreeFromItsArchiveIsTheTree(t *testing.T) {
	direct, err := TreeOf(tree())
	if err != nil {
		t.Fatal(err)
	}
	fromTar, err := ReadTree(bytes.NewReader(tarOf(t, tree())))
	if err != nil {
		t.Fatal(err)
	}
	if RuntimeSHA256(direct.Runtime) != RuntimeSHA256(fromTar.Runtime) || direct.Schema != 1 || fromTar.Schema != 1 ||
		fromTar.MachineSetup != 2 || !bytes.Equal(direct.Deploy, fromTar.Deploy) {
		t.Fatalf("direct %+v\nfrom tar %+v", direct, fromTar)
	}
	arch, err := tarFS(gunzip(t, fromTar.Deploy))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fs.WalkDir(arch, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	want := []string{"deploy/compose.yaml", "deploy/crew-ctl", "deploy/db/10-roles.sh", "deploy/firewall/crew-firewall.sh",
		"deploy/host-setup.sh", "deploy/lib/kata-config.sh", "deploy/proof.sh", "deploy/release.sh", "versions.lock"}
	if !slices.Equal(names, want) {
		t.Fatalf("the deploy archive holds %v", names)
	}
	if st, _ := fs.Stat(arch, "deploy/release.sh"); st.Mode().Perm() != 0o755 {
		t.Fatalf("release.sh has the mode %v", st.Mode())
	}
	if st, _ := fs.Stat(arch, "deploy/compose.yaml"); st.Mode().Perm() != 0o644 {
		t.Fatalf("compose.yaml has the mode %v", st.Mode())
	}
	// A link or a path out of the tree is refused, not followed.
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "deploy/crew-ctl", Typeflag: tar.TypeSymlink, Linkname: "/etc/crew/secret.key"})
	tw.Close()
	if _, err := ReadTree(&b); err == nil || !strings.Contains(err.Error(), "not a file") {
		t.Fatalf("a link: %v", err)
	}
	b.Reset()
	tw = tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "../x", Typeflag: tar.TypeReg})
	tw.Close()
	if _, err := ReadTree(&b); err == nil {
		t.Fatal("a path out of the tree was taken")
	}
}

func gunzip(t *testing.T, b []byte) *bytes.Reader {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(out)
}

// The build context's ignore file decides what the image's COPY lines take, and a .syso file is linked
// into crew-run: both are inputs. A C file in a runtime package is refused, since the image builds
// without cgo (security review of M16 P3).
func TestTheIgnoreFileAndSysoFilesAreInputs(t *testing.T) {
	tr := tree()
	tr[".dockerignore"] = &fstest.MapFile{Data: []byte(".git\n"), Mode: 0o644}
	tr["internal/runspec/rsrc_linux_amd64.syso"] = &fstest.MapFile{Data: []byte("obj"), Mode: 0o644}
	in, err := RuntimeInputs(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".dockerignore", "internal/runspec/rsrc_linux_amd64.syso"} {
		if in[p] == "" {
			t.Errorf("%s is no input", p)
		}
	}
	tr[".dockerignore"] = &fstest.MapFile{Data: []byte(".git\ninternal/runspec/guard.go\n"), Mode: 0o644}
	again, _ := RuntimeInputs(tr)
	if RuntimeSHA256(again) == RuntimeSHA256(in) {
		t.Error("a changed .dockerignore leaves the hash")
	}
	tr["internal/runspec/x.c"] = &fstest.MapFile{Data: []byte("int x;"), Mode: 0o644}
	if _, err := RuntimeInputs(tr); err == nil || !strings.Contains(err.Error(), "C") {
		t.Fatalf("a C file: %v", err)
	}
}
