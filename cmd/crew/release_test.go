package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/version"
	"github.com/kesokaj/crew/internal/versions"
)

// crew release write turns what crew-ctl build gathered into release.json, with the schema of this
// program, and refuses a folder of another version.
func TestReleaseWriteMakesReleaseJSON(t *testing.T) {
	old := version.Version
	version.Version = "0.1.0"
	defer func() { version.Version = old }()
	dir := filepath.Join(t.TempDir(), "0.1.0")
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, n := range release.Images {
		if err := os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--image", n+"="+n+":0.1.0@sha256:"+strings.Repeat("b", 64))
	}
	in := "100644 blob " + strings.Repeat("1", 40) + "\tgo.mod\n" +
		"100644 blob " + strings.Repeat("2", 40) + "\tharness/agents/reviewer.md\n" +
		"agent harness/agents/reviewer.md\tmodel: sonnet\n" +
		"agent harness/agents/reviewer.md\teffort: high\n"
	var out strings.Builder
	err := releaseCmd(append([]string{"write", "--dir", dir, "--commit", strings.Repeat("c", 40), "--machine-setup", "2", "--pin", "agy=1.3.4"}, args...),
		strings.NewReader(in), &out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := db.Steps()
	m := f.Manifest
	if m.Schema != len(steps) || m.ReadBy != db.ReadBy(steps) || m.MachineSetup != 2 || len(m.Files) != 2 ||
		m.Agents["harness/agents/reviewer.md"] != "model: sonnet\neffort: high" || m.Pins["agy"] != "1.3.4" {
		t.Fatalf("%+v", m)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "0.2.0")
	if err := releaseCmd([]string{"write", "--dir", other, "--commit", "c", "--machine-setup", "2"}, strings.NewReader(in), &out); err == nil ||
		!strings.Contains(err.Error(), "program of 0.1.0") {
		t.Fatalf("a folder of another version: %v", err)
	}
	if err := releaseCmd([]string{"write", "--dir", dir, "--commit", "c", "--machine-setup", "2"}, strings.NewReader("agent go.mod\tmodel: x\n"), &out); err == nil {
		t.Fatal("a line for a file that is not an agent's was taken")
	}
}

// release.json names each image by its config digest, which every image store keeps, whatever id the
// builder's store gave, once the file holds the image of that id: a build on the containerd store
// gives the manifest's or the index's digest (D48). The files are ones docker save wrote on Docker 29.
func TestReleaseWriteNamesEachImageByItsConfig(t *testing.T) {
	old := version.Version
	version.Version = "0.1.0"
	defer func() { version.Version = old }()
	const (
		classicConfig = "sha256:3470e023120a4ab9f26de8e0b4cca08435feefc051b605a5eff309d326b3f148"
		builtConfig   = "sha256:9f7fd7f4d9a4dafe018345ba9cfd8868197f4d1d15cec370e8e294f696419496"
		builtIndex    = "sha256:564491266a39cf42fd4651931e0f790cebfd0f50df22a6552c17f20da32c914b"
	)
	write := func(runtimeID string) (*release.Folder, error) {
		dir := filepath.Join(t.TempDir(), "0.1.0")
		if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
			t.Fatal(err)
		}
		var args []string
		for _, n := range release.Images {
			src, id := "classic.tar", classicConfig
			if n == "crew-runtime" {
				src, id = "containerd-built.tar", runtimeID
			}
			b, err := os.ReadFile(filepath.Join("..", "..", "internal", "release", "testdata", "images", src))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "images", n+".tar"), b, 0o644); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--image", n+"="+n+":0.1.0@"+id)
		}
		var out strings.Builder
		if err := releaseCmd(append([]string{"write", "--dir", dir, "--commit", strings.Repeat("c", 40), "--machine-setup", "2"}, args...),
			strings.NewReader("100644 blob "+strings.Repeat("1", 40)+"\tgo.mod\n"), &out); err != nil {
			return nil, err
		}
		return release.Read(dir)
	}
	f, err := write(builtIndex)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Manifest.Images["crew-runtime"].ID; got != builtConfig {
		t.Fatalf("crew-runtime is named %s, want its config %s", got, builtConfig)
	}
	if got := f.Manifest.Images["crew-server"].ID; got != classicConfig {
		t.Fatalf("crew-server is named %s", got)
	}
	if _, err := write(classicConfig); err == nil || !strings.Contains(err.Error(), "which is not the image its file holds, "+builtConfig) {
		t.Fatalf("an id of another image: %v", err)
	}
}

// treeTar is the parts of this repository's tree a release reads, as git archive writes them, with
// versions.lock's text replaced by lock when it is not empty.
func treeTar(t *testing.T, lock string) string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, top := range []string{"go.mod", "go.sum", "versions.lock", "image", "harness", "cmd", "internal", "deploy"} {
		err := filepath.WalkDir(filepath.Join("..", "..", top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), p)
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if rel == "versions.lock" && lock != "" {
				body = []byte(lock)
			}
			st, _ := d.Info()
			if err := tw.WriteHeader(&tar.Header{Name: rel, Typeflag: tar.TypeReg, Mode: int64(st.Mode().Perm()), Size: int64(len(body))}); err != nil {
				return err
			}
			_, err = tw.Write(body)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	p := filepath.Join(t.TempDir(), "tree.tar")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// With the tree, release.json carries the runtime image's inputs and the deploy archive by its hash,
// as the running release reads them from a commit Crew builds (16.2 step 4); a tree of another
// commit is refused, and an archive changed after the build fails Verify.
func TestReleaseWriteTakesTheTree(t *testing.T) {
	old := version.Version
	version.Version = "0.1.0"
	defer func() { version.Version = old }()
	dir := filepath.Join(t.TempDir(), "0.1.0")
	os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	var args []string
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644)
		args = append(args, "--image", n+"="+n+":0.1.0@sha256:"+strings.Repeat("b", 64))
	}
	lock, _ := os.ReadFile("../../versions.lock")
	setup := regexp.MustCompile(`(?m)^MACHINE_SETUP=(\d+)`).FindStringSubmatch(string(lock))[1]
	in := "100644 blob " + strings.Repeat("1", 40) + "\tgo.mod\n"
	tree := treeTar(t, "")
	var out strings.Builder
	if err := releaseCmd(append([]string{"write", "--dir", dir, "--commit", strings.Repeat("c", 40), "--machine-setup", setup, "--tree", tree}, args...),
		strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	tf, _ := os.Open(tree)
	want, err := release.ReadTree(tf)
	tf.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := f.Manifest
	if m.RuntimeSHA256 == "" || m.RuntimeSHA256 != release.RuntimeSHA256(want.Runtime) || m.Runtime["internal/runspec/runspec.go"] == "" ||
		m.Deploy == nil || m.Deploy.File != release.DeployFile || m.Deploy.Bytes != int64(len(want.Deploy)) {
		t.Fatalf("%+v %+v", m.Deploy, m.RuntimeSHA256)
	}
	if err := f.Verify(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, release.DeployFile), []byte("another archive"), 0o644)
	if err := f.Verify(); err == nil || !strings.Contains(err.Error(), "deploy archive") {
		t.Fatalf("a changed deploy archive: %v", err)
	}
	other := filepath.Join(t.TempDir(), "0.1.0")
	os.MkdirAll(filepath.Join(other, "images"), 0o755)
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(other, "images", n+".tar"), []byte(n), 0o644)
	}
	moved := regexp.MustCompile(`(?m)^MACHINE_SETUP=\d+`).ReplaceAllString(string(lock), "MACHINE_SETUP=99")
	err = releaseCmd(append([]string{"write", "--dir", other, "--commit", strings.Repeat("c", 40), "--machine-setup", setup, "--tree", treeTar(t, moved)}, args...),
		strings.NewReader(in), &out)
	if err == nil || !strings.Contains(err.Error(), "another commit's") {
		t.Fatalf("a tree of another commit: %v", err)
	}
}

// The same commit read the server's way, from the canonical repository with gitstore and
// versions.ReadCommit, and crew-ctl's way, with git archive, git ls-tree, crew-ctl's agent_lines, and
// crew release write --tree, gives the same release.json: the same files, agents, schema, machine
// setup, runtime image's inputs, and deploy archive (16.2 step 4, 16.10).
func TestAnExportedCommitGivesTheReleaseJSONOfCrewCtl(t *testing.T) {
	old := version.Version
	version.Version = "0.1.0"
	defer func() { version.Version = old }()
	ctx := context.Background()
	// A repository of the parts of this tree a release reads, at one commit.
	repo := t.TempDir()
	tf, _ := os.Open(treeTar(t, ""))
	tr := tar.NewReader(tf)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		p := filepath.Join(repo, h.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		body, _ := io.ReadAll(tr)
		os.WriteFile(p, body, os.FileMode(h.Mode))
	}
	tf.Close()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(out)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "add", "-A")
	git(repo, "commit", "-q", "-m", "one")
	head := strings.TrimSpace(git(repo, "rev-parse", "HEAD"))

	// crew-ctl's way.
	work := t.TempDir()
	git(repo, "archive", "--format=tar", "-o", filepath.Join(work, "tree.tar"), head)
	lines := exec.Command("bash", "-c", `source ../../deploy/crew-ctl && REPO="$1" agent_lines`, "sh", repo)
	lines.Env = append(os.Environ(), "CREW_CTL_TEST=1", "GIT_CONFIG_GLOBAL=/dev/null")
	agentLines, err := lines.Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "0.1.0")
	os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	var args []string
	for _, n := range release.Images {
		os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n), 0o644)
		args = append(args, "--image", n+"="+n+":0.1.0@sha256:"+strings.Repeat("b", 64))
	}
	lock, _ := os.ReadFile("../../versions.lock")
	setup := regexp.MustCompile(`(?m)^MACHINE_SETUP=(\d+)`).FindStringSubmatch(string(lock))[1]
	var out strings.Builder
	if err := releaseCmd(append([]string{"write", "--dir", dir, "--commit", head, "--machine-setup", setup, "--tree", filepath.Join(work, "tree.tar")}, args...),
		strings.NewReader(git(repo, "ls-tree", "-r", head)+string(agentLines)), &out); err != nil {
		t.Fatal(err)
	}
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	byCtl := f.Manifest

	// The server's way.
	store := &gitstore.Store{Dir: filepath.Join(t.TempDir(), "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: t.TempDir()}}
	os.MkdirAll(store.Dir, 0o755)
	git(store.Dir, "clone", "-q", "--bare", repo, store.Canonical("crew"))
	var exported bytes.Buffer
	if err := store.Archive(ctx, "crew", head, &exported); err != nil {
		t.Fatal(err)
	}
	listed, err := store.LsTree(ctx, "crew", head)
	if err != nil {
		t.Fatal(err)
	}
	c, err := versions.ReadCommit(exported.Bytes(), listed, func(p string) ([]byte, error) { return store.FileAt(ctx, "crew", head, p) })
	if err != nil {
		t.Fatal(err)
	}
	deploy := sha256.Sum256(c.Tree.Deploy)
	if !maps.Equal(byCtl.Files, c.Files) || !maps.Equal(byCtl.Agents, c.Agents) || len(byCtl.Agents) == 0 ||
		byCtl.RuntimeSHA256 != release.RuntimeSHA256(c.Tree.Runtime) || !maps.Equal(byCtl.Runtime, c.Tree.Runtime) ||
		byCtl.Schema != c.Tree.Schema || byCtl.ReadBy != c.Tree.ReadBy || byCtl.MachineSetup != c.Tree.MachineSetup ||
		byCtl.Deploy.SHA256 != hex.EncodeToString(deploy[:]) || !strings.HasPrefix(c.Postgres, "18") {
		t.Fatalf("crew-ctl's release.json and the server's facts differ:\nfiles %d/%d agents %v/%v runtime %s/%s deploy %s/%x",
			len(byCtl.Files), len(c.Files), byCtl.Agents, c.Agents, byCtl.RuntimeSHA256, release.RuntimeSHA256(c.Tree.Runtime), byCtl.Deploy.SHA256, deploy)
	}
}
