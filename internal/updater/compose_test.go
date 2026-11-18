package updater_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

type ran struct {
	env   []string
	stdin string
	cmd   string
}

// recorder is a Run that records each command and answers from out by the start of the command.
type recorder struct {
	ran []ran
	out map[string]string
	// seq answers a command by its start with each answer in turn, before out.
	seq map[string][]string
	// fail fails each command that holds it.
	fail string
}

func (r *recorder) run(_ context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
	cmd := name + " " + strings.Join(args, " ")
	r.ran = append(r.ran, ran{env: env, stdin: string(stdin), cmd: cmd})
	if r.fail != "" && strings.Contains(cmd, r.fail) {
		return "", errors.New("failed: " + r.fail)
	}
	for k, v := range r.seq {
		if strings.HasPrefix(cmd, k) && len(v) > 0 {
			r.seq[k] = v[1:]
			return v[0], nil
		}
	}
	for k, v := range r.out {
		if strings.HasPrefix(cmd, k) {
			return v, nil
		}
	}
	return "", nil
}

// serverID and runtimeID are the lookups of checkRelease's images by their tags (imageRef).
const (
	serverID  = "docker image inspect --format {{.Id}} crew-server:0.2.0"
	runtimeID = "docker image inspect --format {{.Id}} crew-runtime:0.2.0"
)

// imageVolumes is the lookup of the folders an image declares as volumes.
const imageVolumes = "docker image inspect --format {{json .Config.Volumes}} "

// scratchAddress is the lookup of a container's address on the scratch network.
const scratchAddress = `docker inspect --type container --format {{with index .NetworkSettings.Networks "crew-scratch"}}{{.IPAddress}}{{end}}`

func composeKit(t *testing.T) (*updater.Compose, *recorder) {
	t.Helper()
	lib := t.TempDir()
	if err := os.MkdirAll(filepath.Join(lib, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	roles, err := os.ReadFile(filepath.Join("..", "..", "deploy", "db", "10-roles.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "db", "10-roles.sh"), roles, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recorder{out: map[string]string{"docker inspect --format {{.State.Health.Status}} crew-db-check": "healthy\n", imageVolumes: "null\n",
		scratchAddress + " crew-db-check": "172.16.4.2\n", scratchAddress + " crew-server-check": "172.16.4.3\n",
		// checkRelease's images by their tags, as Docker's classic store gives them: their config digests.
		serverID: "sha256:" + strings.Repeat("r", 64) + "\n", runtimeID: "sha256:" + strings.Repeat("e", 64) + "\n"}}
	return &updater.Compose{Lib: lib, Etc: "/etc/crew", Env: []string{"CREW_UID=999"}, Run: r.run,
		Check: filepath.Join(t.TempDir(), "check"), UID: os.Getuid(), GID: os.Getgid()}, r
}

// The real machine's commands: Compose with the installed files and the version of the call, crew
// admin in a container of that version, and the scratch database with the privileges of the roles
// script.
func TestTheRealMachinesCommands(t *testing.T) {
	ctx := context.Background()
	c, r := composeKit(t)
	if err := c.StartServer(ctx, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	got := r.ran[0]
	if !strings.Contains(got.cmd, "compose -p crew -f "+c.Lib+"/compose.yaml --env-file "+c.Lib+"/versions.lock --env-file /etc/crew/crew.env up -d --no-deps crew-server crew-caddy") ||
		!contains(got.env, "CREW_VERSION=0.2.0") || !contains(got.env, "CREW_UID=999") {
		t.Fatalf("%+v", got)
	}
	r.ran = nil
	if err := c.Migrate(ctx, "0.2.0"); err != nil || !strings.HasSuffix(r.ran[0].cmd, "crew-admin admin migrate --at-once") {
		t.Fatalf("%v %+v", err, r.ran)
	}
	// The check's database is a server of its own, with secrets made new for it, which differ each
	// time and are the user crew's alone.
	r.ran = nil
	if err := c.Scratch(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 3 || !strings.HasPrefix(r.ran[0].cmd, "docker rm -f -v crew-server-check crew-db-check") ||
		!strings.HasSuffix(r.ran[1].cmd, "--profile check up -d --no-deps crew-db-check") {
		t.Fatalf("%+v", r.ran)
	}
	first, err := os.ReadFile(c.CheckToken())
	if err != nil || len(first) != 64 {
		t.Fatalf("%q %v", first, err)
	}
	// The secrets' folder is root's alone; the files are readable through it to Docker only.
	if fi, _ := os.Stat(filepath.Dir(c.CheckToken())); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if err := c.Scratch(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(c.CheckToken()); string(again) == string(first) {
		t.Fatal("a second check got the same secret")
	}
	if err := c.DropScratch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Check); !os.IsNotExist(err) {
		t.Fatalf("the check's folder is left: %v", err)
	}
	// A restore of the real database goes into a new one, which takes the name only when complete.
	r.ran = nil
	if err := c.Restore(ctx, "0.1.0", "/s/0.1.0.tar.gz"); err != nil {
		t.Fatal(err)
	}
	last := r.ran[len(r.ran)-1]
	if !strings.Contains(r.ran[2].cmd, "-e CREW_DB_NAME=crew_restore crew-admin admin restore /s/0.1.0.tar.gz") ||
		!strings.Contains(last.stdin, "ALTER DATABASE crew RENAME TO crew_before_restore;\nALTER DATABASE crew_restore RENAME TO crew;") {
		t.Fatalf("%+v", r.ran)
	}
	r.ran = nil
	r.fail = "admin restore"
	if err := c.Restore(ctx, "0.1.0", "/s/0.1.0.tar.gz"); err == nil {
		t.Fatal("a failed restore was reported as done")
	}
	for _, x := range r.ran {
		if strings.Contains(x.stdin, "RENAME") || strings.Contains(x.stdin, "DROP DATABASE IF EXISTS crew ") {
			t.Fatalf("a failed restore touched the real database: %+v", r.ran)
		}
	}
}

// Loading checks that Docker has each image with the id release.json names.
func TestLoadingChecksTheImageIDs(t *testing.T) {
	c, r := composeKit(t)
	dir := filepath.Join(t.TempDir(), "0.2.0")
	_ = os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		saved(t, filepath.Join(dir, "images", n+".tar"), n+":0.2.0")
		imgs[n] = release.Image{Tag: n + ":0.2.0", ID: "sha256:" + n}
	}
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs}, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := release.Read(dir)
	r.out["docker image inspect --format {{.Id}} crew-server:0.2.0"] = "sha256:crew-server\n"
	r.out["docker image inspect --format {{.Id}} crew-caddy:0.2.0"] = "sha256:other\n"
	err := c.Load(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "crew-caddy:0.2.0 loaded as sha256:other") {
		t.Fatalf("%v", err)
	}
	// An image file that would also move another tag, the running version's, is not loaded: named in
	// its manifest.json, or in its index.json, as the containerd image store writes it.
	for _, extra := range [][]string{{"crew-server:0.1.0"}, {"", "docker.io/library/crew-server:0.1.0"}} {
		saved(t, filepath.Join(dir, "images", "crew-server.tar"), append([]string{"crew-server:0.2.0"}, extra...)...)
		r.ran = nil
		err = c.Load(context.Background(), f)
		if err == nil || !strings.Contains(err.Error(), "also names crew-server:0.1.0") || len(r.ran) != 0 {
			t.Fatalf("%v %+v", err, r.ran)
		}
	}
}

// saved writes a file as docker save writes one, with the tags its manifest.json names; after an
// empty tag, the rest are names in its index.json only.
func saved(t *testing.T, path string, tags ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var repo, index []string
	for i, tag := range tags {
		if tag == "" {
			index = tags[i+1:]
			break
		}
		repo = append(repo, tag)
	}
	tw := tar.NewWriter(f)
	write := func(name string, v any) {
		b, _ := json.Marshal(v)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	var manifests []map[string]any
	for _, n := range index {
		manifests = append(manifests, map[string]any{"annotations": map[string]string{"io.containerd.image.name": n}})
	}
	write("index.json", map[string]any{"manifests": manifests})
	write("manifest.json", []map[string]any{{"RepoTags": repo, "Layers": []string{}}})
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A build's image file is loaded only when it names its own tag alone, in manifest.json or in
// index.json: one that also names the running version's would move that tag (16.2 step 4).
func TestABuildsImageNamesOnlyItsTag(t *testing.T) {
	c, r := composeKit(t)
	p := filepath.Join(t.TempDir(), "crew-server.tar")
	saved(t, p, "crew-server:0.2.0")
	r.out["docker image inspect --format {{.Id}} crew-server:0.2.0"] = "sha256:abc\n"
	r.out["docker image ls"] = "crew-server:0.2.0 sha256:abc\n"
	if id, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err != nil || id != "sha256:abc" {
		t.Fatalf("%q %v", id, err)
	}
	for _, tags := range [][]string{{"crew-server:0.2.0", "crew-server:0.1.0"}, {"crew-server:0.2.0", "", "docker.io/library/crew-server:0.1.0"}, {"crew-server:0.1.0"}, {}} {
		saved(t, p, tags...)
		r.ran = nil
		if _, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err == nil || len(r.ran) != 0 {
			t.Fatalf("%v: %v, ran %+v", tags, err, r.ran)
		}
	}
	saved(t, p, "crew-server:0.2.0")
	r.out["docker image inspect --format {{.Id}} crew-server:0.2.0"] = "nothing\n"
	if _, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err == nil {
		t.Fatal("an id that is no id was taken")
	}
}

// rawTar writes a tar of the given entries, in order, under the names given.
func rawTar(t *testing.T, path string, entries ...[2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e[1]))
	}
	tw.Close()
}

// The names an image file gives are read as Docker reads them: a name spelled another way is the same
// file, a second one of a metadata file is refused, and the legacy repositories file names tags too
// (security review of M16 P3). docker save writing one tag in both manifest.json and index.json names
// it once.
func TestAnImageFilesNamesAreReadAsDockerReadsThem(t *testing.T) {
	dir := t.TempDir()
	manifest := func(tag string) string {
		return `[{"Config": "blobs/sha256/x", "RepoTags": ["` + tag + `"], "Layers": []}]`
	}
	index := func(name string) string {
		return `{"manifests": [{"annotations": {"io.containerd.image.name": "` + name + `"}}]}`
	}
	p := filepath.Join(dir, "a.tar")
	for name, entries := range map[string][][2]string{
		"a second manifest.json": {{"manifest.json", manifest("crew-server:0.2.0")}, {"./manifest.json", manifest("crew-server:0.1.0")}},
		"a second index.json":    {{"index.json", index("docker.io/library/crew-server:0.2.0")}, {"./index.json", index("crew-server:0.1.0")}, {"manifest.json", manifest("crew-server:0.2.0")}},
		"an absolute manifest":   {{"/manifest.json", manifest("crew-server:0.1.0")}, {"manifest.json", manifest("crew-server:0.2.0")}},
		"a second repositories":  {{"repositories", `{}`}, {"./repositories", `{}`}, {"manifest.json", manifest("crew-server:0.2.0")}},
	} {
		rawTar(t, p, entries...)
		if tags, err := updater.RepoTags(p); err == nil {
			t.Errorf("%s: read as %v", name, tags)
		}
	}
	rawTar(t, p, [2]string{"./index.json", index("crew-server:0.1.0")}, [2]string{"manifest.json", manifest("crew-server:0.2.0")})
	if tags, err := updater.RepoTags(p); err != nil || !slices.Contains(tags, "crew-server:0.1.0") {
		t.Fatalf("./index.json: %v %v", tags, err)
	}
	rawTar(t, p, [2]string{"repositories", `{"crew-server": {"0.1.0": "abc"}}`}, [2]string{"manifest.json", manifest("crew-server:0.2.0")})
	if tags, err := updater.RepoTags(p); err != nil || !slices.Contains(tags, "crew-server:0.1.0") {
		t.Fatalf("repositories: %v %v", tags, err)
	}
	c, r := composeKit(t)
	saved(t, p, "crew-server:0.2.0", "", "docker.io/library/crew-server:0.2.0")
	r.out["docker image inspect --format {{.Id}} crew-server:0.2.0"] = "sha256:abc\n"
	r.out["docker image ls"] = "crew-server:0.2.0 sha256:abc\n"
	if _, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err != nil {
		t.Fatalf("one tag named in both files: %v", err)
	}
}

// A load creates its own tag and moves no other, whatever the file said: the tags Docker has are read
// before and after, and a tag that moved is put back, one that appeared is taken away, and the load is
// refused. PostgreSQL's shared tag is one of them (security review of M16 P3).
func TestALoadMovesNoTag(t *testing.T) {
	const ls = "docker image ls --no-trunc --format {{.Repository}}:{{.Tag}} {{.ID}}"
	for name, tc := range map[string]struct {
		tag, after, put, rm string
	}{
		"the running server's":  {"crew-server:0.2.0", "crew-server:0.1.0 sha256:new\ncrew-server:0.2.0 sha256:abc\npostgres:18 sha256:pg\n", "docker tag sha256:old crew-server:0.1.0", "docker image rm crew-server:0.2.0"},
		"postgres's shared tag": {"postgres:18", "crew-server:0.1.0 sha256:old\npostgres:18 sha256:evil\n", "docker tag sha256:pg postgres:18", ""},
		"a tag of another name": {"crew-server:0.2.0", "crew-server:0.1.0 sha256:old\ncrew-server:0.2.0 sha256:abc\ncrew-updater:9.9.9 sha256:x\npostgres:18 sha256:pg\n", "", "docker image rm crew-updater:9.9.9"},
	} {
		t.Run(name, func(t *testing.T) {
			c, r := composeKit(t)
			p := filepath.Join(t.TempDir(), "x.tar")
			saved(t, p, tc.tag)
			r.seq = map[string][]string{ls: {"crew-server:0.1.0 sha256:old\npostgres:18 sha256:pg\n<none>:<none> sha256:zz\n", tc.after}}
			r.out["docker image inspect --format {{.Id}} "+tc.tag] = "sha256:abc\n"
			_, err := c.LoadImage(context.Background(), p, tc.tag)
			if err == nil || !strings.Contains(err.Error(), "moved") && !strings.Contains(err.Error(), "also tagged") {
				t.Fatalf("err = %v", err)
			}
			var ran []string
			for _, x := range r.ran {
				ran = append(ran, x.cmd)
			}
			for _, want := range []string{tc.put, tc.rm} {
				if want != "" && !slices.Contains(ran, want) {
					t.Errorf("no %q in %v", want, ran)
				}
			}
		})
	}
	// A load that moves nothing is taken.
	c, r := composeKit(t)
	p := filepath.Join(t.TempDir(), "x.tar")
	saved(t, p, "crew-server:0.2.0")
	r.seq = map[string][]string{ls: {"crew-server:0.1.0 sha256:old\n", "crew-server:0.1.0 sha256:old\ncrew-server:0.2.0 sha256:abc\n"}}
	r.out["docker image inspect --format {{.Id}} crew-server:0.2.0"] = "sha256:abc\n"
	if _, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err != nil {
		t.Fatal(err)
	}
}

// A load that fails is compared too: a tag it moved before it failed is put back (review of M16 P3,
// round 2). Only the release's own names are the load's to answer for: a tag of another name that
// appears meanwhile, such as a registry push's, is left alone. A tag that cannot be put back is said.
func TestALoadThatFailsIsUndoneToo(t *testing.T) {
	const ls = "docker image ls --no-trunc --format {{.Repository}}:{{.Tag}} {{.ID}}"
	c, r := composeKit(t)
	p := filepath.Join(t.TempDir(), "x.tar")
	saved(t, p, "crew-server:0.2.0")
	r.seq = map[string][]string{ls: {"crew-server:0.1.0 sha256:old\n", "crew-server:0.1.0 sha256:new\nghcr.io/kesokaj/crew-server:0.1.0 sha256:push\n"}}
	r.fail = "docker load"
	_, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0")
	var ran []string
	for _, x := range r.ran {
		ran = append(ran, x.cmd)
	}
	if err == nil || !slices.Contains(ran, "docker tag sha256:old crew-server:0.1.0") {
		t.Fatalf("%v, ran %v", err, ran)
	}
	for _, x := range ran {
		if strings.Contains(x, "ghcr.io") {
			t.Fatalf("a tag of another name was touched: %v", ran)
		}
	}
	c, r = composeKit(t)
	saved(t, p, "crew-server:0.2.0")
	r.seq = map[string][]string{ls: {"crew-server:0.1.0 sha256:old\n", "crew-server:0.1.0 sha256:new\ncrew-server:0.2.0 sha256:abc\n"}}
	r.fail = "docker tag"
	if _, err := c.LoadImage(context.Background(), p, "crew-server:0.2.0"); err == nil || !strings.Contains(err.Error(), "could not be put back") {
		t.Fatalf("a tag that cannot be put back: %v", err)
	}
}

// browserTar is a browser.tar as release.sh packs it, with the given entries added.
func browserTar(t *testing.T, extra ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	files := map[string]string{"./package.json": "{}", "./playwright.gate.config.ts": "export default {}\n",
		"./gate/screens.spec.ts": "// the screens\n", "./node_modules/@playwright/test/cli.js": "// playwright\n"}
	for _, name := range []string{"./package.json", "./playwright.gate.config.ts", "./gate/screens.spec.ts", "./node_modules/@playwright/test/cli.js"} {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[name]))})
		tw.Write([]byte(files[name]))
	}
	tw.WriteHeader(&tar.Header{Name: "./node_modules/.bin/playwright", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	for _, h := range extra {
		tw.WriteHeader(h)
		// A large entry is its header alone: the size is read before the body.
		if h.Size > 0 && h.Size < 1<<20 {
			tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	tw.Close()
	return b.Bytes()
}

// checkRelease is a release folder whose deploy archive is the candidate's own: its compose.yaml
// mounts the Docker socket into the check server, which the updater must not take, and its
// versions.lock names the browser tests' image as lock says.
func checkRelease(t *testing.T, lock string, browser []byte) *release.Folder {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "0.2.0")
	_ = os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		saved(t, filepath.Join(dir, "images", n+".tar"), n+":0.2.0")
		imgs[n] = release.Image{Tag: n + ":0.2.0", ID: "sha256:" + strings.Repeat(n[len(n)-1:], 64)}
	}
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s), Mode: 0o644} }
	tree := fstest.MapFS{
		"deploy/crew-ctl": f(""), "deploy/release.sh": f(""), "deploy/host-setup.sh": f(""), "deploy/proof.sh": f(""),
		"deploy/compose.yaml":   f("services:\n  crew-check:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n    group_add: [\"999\"]\n"),
		"deploy/db/10-roles.sh": f(""), "deploy/lib/x.sh": f(""), "deploy/firewall/x.sh": f(""),
		"versions.lock": f(lock),
	}
	var arch bytes.Buffer
	if err := release.DeployArchive(tree, &arch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, release.DeployFile), arch.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, release.BrowserFile), browser, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Write(dir, release.Facts{Version: "0.2.0", BuiltAt: time.Now(), Images: imgs, Deploy: release.DeployFile,
		Browser: release.BrowserFile}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rf, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return rf
}

const playwrightLock = "PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.64.0-noble@sha256:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" + "\n"

func installFile(t *testing.T, c *updater.Compose) {
	t.Helper()
	c.Etc = t.TempDir()
	if err := os.WriteFile(filepath.Join(c.Etc, "crew.env"), []byte("# Crew's install file\nCREW_HOSTNAME=crew.example.org\nCREW_ADMIN_EMAIL=admin@example.org\nCREW_TIMEZONE=Europe/Stockholm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The check server's spec is the updater's own (16.4 step 3, since 0.51): no Docker socket, no group
// of Docker's, no sandboxes' folder, no key file, no capability, the check's own secrets, the scratch
// network, and the image by the id release.json names, also when the release's own compose.yaml
// mounts the socket. It runs code agents wrote, so it runs in a microVM (since 0.52).
func TestTheCheckServersSpecIsTheUpdatersOwn(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	if err := c.StartCheck(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 3 || r.ran[0].cmd != scratchAddress+" crew-db-check" || r.ran[1].cmd != serverID {
		t.Fatalf("%+v", r.ran)
	}
	cmd := r.ran[2].cmd
	for _, bad := range []string{"docker.sock", "--group-add", "group_add", "sandboxes", "secret.key", "jwt_key", "compose", "crew-server:0.2.0",
		"/etc/crew/secrets", "--privileged", "crew-db ", "crew-out", "--cap-add"} {
		if strings.Contains(cmd, bad) {
			t.Errorf("the check server's spec holds %q: %s", bad, cmd)
		}
	}
	for _, want := range []string{"docker run -d --name crew-server-check ", "--runtime mvm ", "--network crew-scratch ", "-e CREW_DB_HOST=crew-db-check ",
		"--add-host crew-db-check:172.16.4.2 ",
		"-e CREW_HOSTNAME=crew.example.org ", "-e TZ=Europe/Stockholm ", "-e CREW_CHECK=1 ", "--cap-drop ALL ", "--security-opt no-new-privileges ",
		"--pids-limit 4096 ", "--memory 8g ", "--sysctl net.ipv4.ip_forward=0 ",
		" sha256:" + strings.Repeat("r", 64) + " serve --check",
		filepath.Join(c.Check, "secrets", "db_crew") + ":/run/secrets/db_crew:ro",
		filepath.Join(c.Check, "secrets", "ingress_token") + ":/run/secrets/ingress_token:ro"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the check server's spec lacks %q: %s", want, cmd)
		}
	}
	if strings.Count(cmd, " -v ") != 3 {
		t.Errorf("the check server mounts more than the check's three secrets: %s", cmd)
	}
}

// The check server runs in a microVM or not at all: a runtime that is no microVM's is refused, also
// when the server is told to allow one, and a check server that does not start says it did not start
// in a microVM (review and security review of M16 P4d, rounds 1 and 2).
func TestTheCheckServerRunsInAMicroVMOrNotAtAll(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	t.Setenv("CREW_ALLOW_UNSAFE_RUNTIME", "1")
	for _, rt := range []string{"runc", "io.containerd.runc.v2", "runsc"} {
		c.Runtime = rt
		r.ran = nil
		if err := c.StartCheck(context.Background(), f); err == nil || !strings.Contains(err.Error(), "microVM") {
			t.Fatalf("a check server on %s: %v %+v", rt, err, r.ran)
		}
		// Its database's address is read first; nothing starts.
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Fatalf("a check server on %s started: %s", rt, x.cmd)
			}
		}
	}
	c.Runtime = ""
	r.fail = "docker run -d --name crew-server-check"
	if err := c.StartCheck(context.Background(), f); err == nil || !strings.Contains(err.Error(), "did not start in a microVM") {
		t.Fatalf("a check server that did not start: %v", err)
	}
}

// The release's crew admin reads the snapshot into the scratch copy and applies its schema there
// (16.4 step 3). It is code agents wrote, with a copy of the whole database, so it runs as the check
// server does: in a microVM, from a spec the updater writes itself, with the migrate role's secret
// alone, the one snapshot read only, and the database's address; never through a service of the
// Compose file on runc (security review of the gate's fix, 2026-10-21). Its root is read only, with a
// small tmpfs, and its image declares no volume, so it writes nothing of its own to the machine's
// disk (what it writes into the scratch database does reach it); and what it is given of the snapshot
// is a copy in the check's own folder, root's, of the file the updater opened, so a link in the
// snapshots' folder, which is crew's, cannot point the mount at another file (security review of the
// change).
func TestTheStateIsTriedByTheReleasesAdminInMicroVMs(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	snapshot := writeSnapshot(t, c)
	copied := filepath.Join(c.Check, "0.1.0.tar.gz")
	var bounded, seen []string
	c.Run = func(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
		if d, ok := ctx.Deadline(); len(args) > 0 && args[0] == "run" && ok && time.Until(d) <= 30*time.Minute {
			bounded = append(bounded, args[len(args)-1])
		}
		if len(args) > 0 && args[0] == "run" && slices.Contains(args, "restore") {
			b, err := os.ReadFile(copied)
			fi, _ := os.Stat(copied)
			seen = append(seen, fmt.Sprintf("%q %v %v", b, err, fi != nil && fi.Mode().Perm() == 0o600))
		}
		return r.run(ctx, env, stdin, name, args...)
	}
	if err := c.RestoreScratch(context.Background(), f, snapshot); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 7 || r.ran[0].cmd != scratchAddress+" crew-db-check" || r.ran[1].cmd != serverID || r.ran[2].cmd != imageVolumes+"sha256:"+strings.Repeat("r", 64) ||
		r.ran[3].cmd != "docker rm -f crew-gate-restore" || r.ran[5].cmd != "docker rm -f crew-gate-restore" {
		t.Fatalf("%+v", r.ran)
	}
	image := " sha256:" + strings.Repeat("r", 64) + " admin "
	secret := filepath.Join(c.Check, "secrets", "db_crew_migrate") + ":/run/secrets/db_crew_migrate:ro"
	for i, tc := range []struct {
		suffix string
		mounts int
	}{{image + "restore " + copied + " --no-months", 2}, {image + "migrate", 1}} {
		cmd := r.ran[4+2*i].cmd
		if !strings.HasPrefix(cmd, "docker run --rm --name crew-gate-restore ") || !strings.HasSuffix(cmd, tc.suffix) {
			t.Errorf("not the release's crew admin by its image's id: %s", cmd)
		}
		for _, want := range []string{"--runtime mvm ", "--network crew-scratch ", "--add-host crew-db-check:172.16.4.2 ",
			fmt.Sprintf("--user %d:%d ", c.UID, c.GID), "--cap-drop ALL ", "--security-opt no-new-privileges ", "--pids-limit 1024 ", "--memory 2g ",
			"-e CREW_DB_HOST=crew-db-check ", "-e TZ=Europe/Stockholm ", "-v " + secret + " ", "--read-only ", "--tmpfs /tmp:rw,size=64m "} {
			if !strings.Contains(cmd, want) {
				t.Errorf("crew admin's spec lacks %q: %s", want, cmd)
			}
		}
		for _, bad := range []string{"docker.sock", "compose", "crew-admin-check", "crew-server:0.2.0", "/run/secrets/db_crew:", "db_crew_read",
			"ingress_token", "/etc/crew/secrets", "secret.key", "jwt_key", "--privileged", "--cap-add", "crew-db ", "crew-out",
			"-v " + filepath.Dir(snapshot) + ":", "CREW_ADMIN_EMAIL", "CREW_HOSTNAME"} {
			if strings.Contains(cmd, bad) {
				t.Errorf("crew admin's spec holds %q: %s", bad, cmd)
			}
		}
		if n := strings.Count(cmd, " -v "); n != tc.mounts {
			t.Errorf("%d mounts, not %d: %s", n, tc.mounts, cmd)
		}
	}
	if !strings.Contains(r.ran[4].cmd, " -v "+copied+":"+copied+":ro ") || strings.Contains(r.ran[4].cmd, snapshot) || strings.Contains(r.ran[6].cmd, "0.1.0.tar.gz") {
		t.Errorf("the restore reads the one snapshot's copy, read only, and the migrate none: %+v", r.ran)
	}
	if len(seen) != 1 || seen[0] != `"the snapshot" <nil> true` {
		t.Errorf("the copy the restore reads: %q", seen)
	}
	if _, err := os.Lstat(copied); !os.IsNotExist(err) {
		t.Errorf("the snapshot's copy is left after the restore: %v", err)
	}
	if len(bounded) != 2 {
		t.Errorf("each command is bounded to 30 minutes: %q", bounded)
	}
}

// The release's crew admin runs in a microVM or not at all, and never without the database's
// address: nothing starts on a runtime that is no microVM's, also when the server is told to allow
// one, or when the scratch database has no address there. A restore that fails stops the step, says
// which command failed, and its microVM is removed; a snapshot whose path a mount cannot take is
// refused.
func TestTheReleasesAdminRunsInAMicroVMOrNotAtAll(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	ctx := context.Background()
	noRun := func(what string) {
		t.Helper()
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Fatalf("%s: crew admin started: %s", what, x.cmd)
			}
		}
	}
	t.Setenv("CREW_ALLOW_UNSAFE_RUNTIME", "1")
	for _, rt := range []string{"runc", "io.containerd.runc.v2", "runsc"} {
		c.Runtime = rt
		r.ran = nil
		if err := c.RestoreScratch(ctx, f, "/s/0.1.0.tar.gz"); err == nil || !strings.Contains(err.Error(), "microVM") {
			t.Fatalf("crew admin on %s: %v %+v", rt, err, r.ran)
		}
		noRun(rt)
	}
	c.Runtime = ""
	for _, none := range []string{"", "<no value>\n", "127.0.0.1\n", "::\n"} {
		r.ran = nil
		r.out[scratchAddress+" crew-db-check"] = none
		if err := c.RestoreScratch(ctx, f, "/s/0.1.0.tar.gz"); err == nil || !strings.Contains(err.Error(), "crew-db-check has no address on crew-scratch") {
			t.Fatalf("%q: %v", none, err)
		}
		noRun(fmt.Sprintf("address %q", none))
	}
	r.out[scratchAddress+" crew-db-check"] = "172.16.4.2\n"
	for _, bad := range []string{"", "s/0.1.0.tar.gz", "/s/a:b.tar.gz", "/s/a,b.tar.gz", "/s/a\nb.tar.gz"} {
		r.ran = nil
		if err := c.RestoreScratch(ctx, f, bad); err == nil || !strings.Contains(err.Error(), "snapshot") {
			t.Fatalf("%q: %v", bad, err)
		}
		noRun(fmt.Sprintf("snapshot %q", bad))
	}
	r.ran = nil
	r.fail = " admin restore "
	err := c.RestoreScratch(ctx, f, writeSnapshot(t, c))
	if err == nil || !strings.Contains(err.Error(), "crew admin restore") || !strings.Contains(err.Error(), "microVM") {
		t.Fatalf("a restore that failed: %v", err)
	}
	if last := r.ran[len(r.ran)-1].cmd; last != "docker rm -f crew-gate-restore" {
		t.Fatalf("a failed restore's microVM is left: %+v", r.ran)
	}
	for _, x := range r.ran {
		if strings.Contains(x.cmd, " admin migrate") {
			t.Fatalf("the migrate ran after a failed restore: %+v", r.ran)
		}
	}
}

// A failed command's error quotes the start and the end of what it printed, at most 600 bytes of it,
// cut at whole characters: Kata prints the cause first and a long stack trace after it, which pushed
// the cause out of the end alone ("…ectory", the gate of 0.2.11 on dave), and psql or a Go program
// prints it last. What is short is quoted whole.
func TestAFailedCommandsErrorKeepsItsCauseAndItsEnd(t *testing.T) {
	ctx := context.Background()
	kata := "docker: Error response from daemon: failed to create task for container: Internal: failed to create shim task: ENOENT: No such file or directory\n\nStack backtrace:\n"
	for i := 0; i < 10; i++ {
		kata += fmt.Sprintf("%4d: <unknown>\n", i)
	}
	kata += "\nStack backtrace:\n"
	for i := 0; i < 22; i++ {
		kata += fmt.Sprintf("%4d: <unknown>\n", i)
	}
	kata += "\nRun 'docker run --help' for more information\n"
	const prefix = "sh -c cat; exit 125: exit status 125: "
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"kata":  {kata, []string{"ENOENT: No such file or directory", "Run 'docker run --help' for more information", " … "}},
		"short": {"psql: error: connection refused\n", []string{"psql: error: connection refused"}},
		"utf-8": {"a" + strings.Repeat("é", 700) + "b", []string{"aé", "éb", " … "}},
	} {
		_, err := updater.ExecRun(ctx, nil, []byte(tc.out), "sh", "-c", "cat; exit 125")
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Fatalf("%s: %v", name, err)
		}
		quoted := strings.TrimPrefix(err.Error(), prefix)
		for _, w := range tc.want {
			if !strings.Contains(quoted, w) {
				t.Errorf("%s: the error lacks %q: %q", name, w, quoted)
			}
		}
		if len(quoted) > 600+len(" … ") || !utf8.ValidString(quoted) {
			t.Errorf("%s: %d bytes, valid UTF-8 %v: %q", name, len(quoted), utf8.ValidString(quoted), quoted)
		}
	}
	if _, err := updater.ExecRun(ctx, nil, []byte("psql: error: connection refused\n"), "sh", "-c", "cat; exit 125"); err == nil ||
		!strings.HasSuffix(err.Error(), ": psql: error: connection refused") {
		t.Fatalf("a short output is not quoted whole: %v", err)
	}
}

// writeSnapshot writes a snapshot in a folder of its own, as the updater's snapshots are, and makes the
// check's folder, which Scratch makes before the restore.
func writeSnapshot(t *testing.T, c *updater.Compose) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Check, 0o711); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "0.1.0.tar.gz")
	if err := os.WriteFile(p, []byte("the snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The snapshot is opened as a plain file, never through a link, before anything starts: a link, a
// folder, or a pipe where it must be is refused, and so is a file that is not crew's or has a second
// name, a hard link, since root copies it; nothing is copied or run (security review of the gate's
// restore, rounds 1 and 2: the snapshots' folder is crew's, and Docker, as root, would follow a link
// there).
func TestTheSnapshotIsTakenOnlyAsAPlainFile(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	snapshot := writeSnapshot(t, c)
	dir := filepath.Dir(snapshot)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("not the snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.tar.gz")
	folder := filepath.Join(dir, "folder.tar.gz")
	pipe := filepath.Join(dir, "pipe.tar.gz")
	if err := errors.Join(os.Symlink(secret, link), os.Mkdir(folder, 0o700), syscall.Mkfifo(pipe, 0o600)); err != nil {
		t.Fatal(err)
	}
	refused := func(bad, why string) {
		t.Helper()
		r.ran = nil
		if err := c.RestoreScratch(context.Background(), f, bad); err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("%s: %v", bad, err)
		}
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Fatalf("%s: crew admin started: %s", bad, x.cmd)
			}
		}
		if left, _ := os.ReadDir(c.Check); len(left) != 0 {
			t.Fatalf("%s: the check's folder holds %v", bad, left)
		}
	}
	for _, bad := range []string{link, folder, pipe, filepath.Join(dir, "none.tar.gz")} {
		refused(bad, "snapshot")
	}
	refused(folder, "is no plain file")
	refused(pipe, "is no plain file")
	c.UID++
	refused(snapshot, "is not crew's alone")
	c.UID--
	if err := os.Link(snapshot, filepath.Join(dir, "second.tar.gz")); err != nil {
		t.Fatal(err)
	}
	refused(snapshot, "is not crew's alone")
}

// The release's image declares no volume: Docker would make one on the machine's disk for it, which a
// read-only root does not cover, so a release whose server image declares one is refused before its
// crew admin starts (security review of the gate's restore, round 2).
func TestAReleaseWhoseImageDeclaresAVolumeIsRefused(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	f := checkRelease(t, playwrightLock, browserTar(t))
	for _, vols := range []string{`{"/var/lib/crew":{}}`, "", "not json"} {
		r.ran = nil
		r.out[imageVolumes] = vols + "\n"
		if err := c.RestoreScratch(context.Background(), f, writeSnapshot(t, c)); err == nil || !strings.Contains(err.Error(), "volume") {
			t.Fatalf("%q: %v", vols, err)
		}
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Fatalf("%q: crew admin started: %s", vols, x.cmd)
			}
		}
	}
	r.out[imageVolumes] = "{}\n"
	if err := c.RestoreScratch(context.Background(), f, writeSnapshot(t, c)); err != nil {
		t.Fatalf("an image with an empty set of volumes: %v", err)
	}
}

// A command's output is kept in the updater's memory at most 1 MiB from its start and 1 MiB from its
// end: some commands run code agents wrote, which could print without end (review of the gate's
// restore). What is short is kept whole.
func TestACommandsOutputIsBoundedAtItsStartAndItsEnd(t *testing.T) {
	out, err := updater.ExecRun(context.Background(), nil, nil, "sh", "-c", `printf START; head -c 5242880 /dev/zero | tr '\0' a; printf END`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 2<<20+16 || !strings.HasPrefix(out, "STARTaaa") || !strings.HasSuffix(out, "aaaEND") || !strings.Contains(out, "\n…\n") {
		t.Fatalf("%d bytes kept, from %q to %q", len(out), out[:16], out[len(out)-16:])
	}
	short, err := updater.ExecRun(context.Background(), nil, nil, "sh", "-c", "printf 'one\ntwo\n'; printf err >&2")
	if err != nil || short != "one\ntwo\nerr" {
		t.Fatalf("%q %v", short, err)
	}
}

// The release's own configuration of its gate's browser tests names an outputDir under /tmp, outside
// their folder, which the microVM sees read only, so that a gate on an updater that does not yet name
// the results' folder itself can pass its browser step (the gate of 0.2.14 on dave, on updater
// 0.2.13). This reads the configuration's text only: that Playwright writes there was shown by a probe
// in Kata (.agents/scratch/fix-browser2-probe.log), not by this test.
func TestTheGatesBrowserConfigWritesOutsideItsFolder(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "tools", "e2e", "browser", "playwright.gate.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s*outputDir: '/tmp/[a-z-]+',`).Match(body) {
		t.Fatalf("playwright.gate.config.ts sets no outputDir under /tmp:\n%s", body)
	}
}

// The Compose file the machine installs runs no code of the release that is checked: the check's
// crew admin was a service of it on runc, and is the updater's own spec now.
func TestTheInstalledComposeFileRunsNoReleaseThatIsChecked(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"crew-admin-check", "CREW_CHECK_VERSION"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("deploy/compose.yaml holds %q", bad)
		}
	}
}

// The check server has no socket, so the updater starts the smoke run's microVM, by a name of its
// own, from the release's runtime image by its id, with no network, and the check server stores the
// event it printed; a microVM whose run failed, by a timeout too, is removed by its name. Since 0.53 it
// is bounded as the restore's is: no capability, no way to gain one, bounds of its own, and a read-only
// root with one small tmpfs, /tmp, which holds HOME and the proof's file (security review of 0.2.16).
func TestTheUpdaterStartsTheSmokeMicroVM(t *testing.T) {
	c, r := composeKit(t)
	f := checkRelease(t, playwrightLock, browserTar(t))
	r.out["docker run --rm --name crew-gate-smoke"] = "starting\n{\"type\":\"crew_proof\",\"kernel\":\"6.12-kata\"}\n"
	if err := c.Smoke(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 4 || r.ran[0].cmd != runtimeID || r.ran[1].cmd != "docker rm -f crew-gate-smoke" ||
		r.ran[2].cmd != "docker run --rm --name crew-gate-smoke --runtime mvm --network none --user 65534:65534 --cap-drop ALL --security-opt no-new-privileges "+
			"--pids-limit 256 --memory 1g --read-only --tmpfs /tmp:rw,size=16m -e HOME=/tmp --entrypoint crew-run sha256:"+strings.Repeat("e", 64)+" proof" ||
		r.ran[3].cmd != "docker exec -i crew-server-check crew admin proof store" || r.ran[3].stdin != "{\"type\":\"crew_proof\",\"kernel\":\"6.12-kata\"}\n" {
		t.Fatalf("%+v", r.ran)
	}
	r.ran = nil
	r.out["docker run --rm --name crew-gate-smoke"] = "no event\n"
	if err := c.Smoke(context.Background(), f); err == nil || len(r.ran) != 3 {
		t.Fatalf("a microVM that printed no event: %v %+v", err, r.ran)
	}
	r.ran = nil
	r.fail = "crew-run sha256"
	if err := c.Smoke(context.Background(), f); err == nil || len(r.ran) != 4 || r.ran[3].cmd != "docker rm -f crew-gate-smoke" {
		t.Fatalf("a microVM whose run failed is left: %v %+v", err, r.ran)
	}
}

// The browser tests are the release's own, with the packages its build installed in its sandbox, and
// code agents wrote: they run in a microVM by a name of its own, on the scratch network alone with
// the check's ingress secret, in Microsoft's Playwright image by its hash, and fetch nothing; their
// folder is removed after. Their folder is read only, and their results go to a folder the updater
// names in the microVM's tmpfs /tmp, outside it, never to a mount point inside it: Playwright removes its
// results' folder as it starts (the gate of 0.2.14 on dave: "EROFS: read-only file system, rmdir
// '/work/test-results'").
func TestTheBrowserTestsRunInMicroVMs(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	c.Work = filepath.Join(t.TempDir(), "gate-e2e")
	f := checkRelease(t, playwrightLock, browserTar(t))
	var seen []string
	c.Run = func(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
		if b, err := os.ReadFile(filepath.Join(c.Work, "gate", "screens.spec.ts")); err == nil && len(args) > 0 && args[0] == "run" {
			seen = append(seen, string(b))
			if _, err := os.Lstat(filepath.Join(c.Work, "node_modules", ".bin", "playwright")); err == nil {
				seen = append(seen, "a link of browser.tar was made")
			}
		}
		return r.run(ctx, env, stdin, name, args...)
	}
	if err := c.Browser(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	img := "mcr.microsoft.com/playwright:v1.64.0-noble@sha256:" + strings.Repeat("d", 64)
	if len(r.ran) != 3 || r.ran[0].cmd != scratchAddress+" crew-server-check" || r.ran[1].cmd != "docker rm -f crew-gate-browser" ||
		!strings.HasPrefix(r.ran[2].cmd, "docker run --rm --name crew-gate-browser --runtime mvm --network crew-scratch --add-host crew-server-check:172.16.4.3 -e CREW_ADMIN_EMAIL=admin@example.org -e CREW_VERSION=0.2.0 ") ||
		!strings.Contains(r.ran[2].cmd, c.CheckToken()+":/run/check-token:ro") ||
		!strings.Contains(r.ran[2].cmd, " -v "+c.Work+":/work:ro ") || !strings.Contains(r.ran[2].cmd, " --tmpfs /tmp:rw,size=512m ") ||
		!strings.HasSuffix(r.ran[2].cmd, img+" node node_modules/@playwright/test/cli.js test -c playwright.gate.config.ts --output /tmp/gate/results") ||
		strings.Contains(r.ran[2].cmd, "--tmpfs /work") || strings.Contains(r.ran[2].cmd, "npm") || strings.Contains(r.ran[2].cmd, "bridge") {
		t.Fatalf("%+v", r.ran)
	}
	if len(seen) != 1 || seen[0] != "// the screens\n" {
		t.Fatalf("the tests were not unpacked before they ran: %q", seen)
	}
	if _, err := os.Stat(c.Work); !os.IsNotExist(err) {
		t.Fatalf("the tests' folder is left: %v", err)
	}
}

// The browser tests are code agents wrote, so their microVM is bounded as the check server's and the
// restore's are: the user crew, no capability, no way to gain one, bounds of its own, and a read-only
// root with one tmpfs, /tmp, for HOME and the results (security review of the gate's browser fix,
// 2026-10-21). The user crew reads the tests: their folder is not root's alone while they run.
func TestTheBrowserMicroVMIsBounded(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	c.Work = filepath.Join(t.TempDir(), "gate-e2e")
	f := checkRelease(t, playwrightLock, browserTar(t))
	var mode os.FileMode
	c.Run = func(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			if st, err := os.Stat(c.Work); err == nil {
				mode = st.Mode().Perm()
			}
		}
		return r.run(ctx, env, stdin, name, args...)
	}
	if err := c.Browser(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	run := r.ran[len(r.ran)-1].cmd
	img := " mcr.microsoft.com/playwright:v1.64.0-noble@sha256:" + strings.Repeat("d", 64) + " "
	before, _, ok := strings.Cut(run, img)
	if !ok {
		t.Fatalf("no image in %s", run)
	}
	for _, want := range []string{fmt.Sprintf(" --user %d:%d ", c.UID, c.GID), " --cap-drop ALL ", " --security-opt no-new-privileges ",
		" --pids-limit 4096 ", " --memory 4g ", " --read-only ", " --tmpfs /tmp:rw,size=512m ", " -e HOME=/tmp "} {
		if !strings.Contains(before+" ", want) {
			t.Errorf("the browser microVM has no %q: %s", strings.TrimSpace(want), run)
		}
	}
	if strings.Count(run, "--tmpfs") != 1 || strings.Contains(run, "--privileged") || strings.Contains(run, "--cap-add") {
		t.Errorf("the browser microVM: %s", run)
	}
	if mode&0o005 != 0o005 {
		t.Errorf("the tests' folder is %v while they run: the user crew cannot read it", mode)
	}
}

// The browser tests and the smoke run are code agents wrote, as the check server and the restore are:
// on a runtime that is no microVM's they refuse, and start nothing, whoever calls them (security
// review of the browser microVM's bounds, 2026-10-21).
func TestTheBrowserAndSmokeMicroVMsRefuseAnotherRuntime(t *testing.T) {
	for _, rt := range []string{"runc", "runsc", "io.containerd.runc.v2"} {
		c, r := composeKit(t)
		installFile(t, c)
		c.Work = filepath.Join(t.TempDir(), "gate-e2e")
		c.Runtime = rt
		f := checkRelease(t, playwrightLock, browserTar(t))
		// A folder of an earlier run, which Browser removes first when it goes on.
		marker := filepath.Join(c.Work, "earlier")
		if err := os.MkdirAll(c.Work, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.Browser(context.Background(), f); err == nil || !strings.Contains(err.Error(), "is no microVM's") {
			t.Errorf("%s: Browser: %v", rt, err)
		}
		if err := c.Smoke(context.Background(), f); err == nil || !strings.Contains(err.Error(), "is no microVM's") {
			t.Errorf("%s: Smoke: %v", rt, err)
		}
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Errorf("%s: a container started: %s", rt, x.cmd)
			}
		}
		// Before anything: the tests' folder is not touched either (round 2 of the reviews).
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("%s: the tests' folder was touched before the refusal: %v", rt, err)
		}
	}
}

// A microVM has a network stack of its own, which Docker's name server on the scratch network does
// not answer: the check server and the browser tests reach the container they need by the address the
// updater gives them, and neither starts when that container has none (M16's first gate on dave,
// 2026-10-21: the check server's database was "down").
func TestAMicroVMOnTheScratchNetworkIsGivenTheAddressItReaches(t *testing.T) {
	c, r := composeKit(t)
	installFile(t, c)
	c.Work = filepath.Join(t.TempDir(), "gate-e2e")
	f := checkRelease(t, playwrightLock, browserTar(t))
	for _, none := range []string{"", "\n", "<no value>\n", "not-an-address\n", "127.0.0.1\n", "0.0.0.0\n", "::\n", "::1\n"} {
		r.ran = nil
		r.out[scratchAddress+" crew-db-check"] = none
		if err := c.StartCheck(context.Background(), f); err == nil || !strings.Contains(err.Error(), "crew-db-check has no address on crew-scratch") {
			t.Fatalf("%q: %v", none, err)
		}
		r.out[scratchAddress+" crew-server-check"] = none
		if err := c.Browser(context.Background(), f); err == nil || !strings.Contains(err.Error(), "crew-server-check has no address on crew-scratch") {
			t.Fatalf("%q: %v", none, err)
		}
		for _, x := range r.ran {
			if strings.HasPrefix(x.cmd, "docker run") {
				t.Fatalf("%q: a microVM started: %s", none, x.cmd)
			}
		}
	}
	// The spec itself takes no address that is not a peer's (security review of the fix).
	for _, db := range []string{"", "127.0.0.1", "crew-db-check"} {
		if _, err := c.CheckArgs(f, f.Manifest.Images["crew-server"].ID, map[string]string{"CREW_HOSTNAME": "h", "CREW_ADMIN_EMAIL": "a@x", "CREW_DATA_DIR": "/d"}, db); err == nil {
			t.Fatalf("CheckArgs took %q", db)
		}
	}
	// A lookup that fails says so, and starts nothing either.
	r.ran = nil
	r.fail = "{{with index .NetworkSettings.Networks"
	err := c.StartCheck(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "crew-db-check has no address on crew-scratch: failed") || strings.Contains(err.Error(), "<nil>") || len(r.ran) != 1 {
		t.Fatalf("%v %+v", err, r.ran)
	}
	r.fail = ""
	r.ran = nil
	r.out[scratchAddress+" crew-db-check"] = "fd00::4\n"
	if err := c.StartCheck(context.Background(), f); err != nil || !strings.Contains(r.ran[2].cmd, "--add-host crew-db-check:fd00::4 ") {
		t.Fatalf("%v %+v", err, r.ran)
	}
}

// What the browser tests run with is refused before anything runs: an image other than Microsoft's
// Playwright by its hash, which versions.lock, a file agents can change, names; and a browser.tar
// with an entry out of its folder, too much in it, or no configuration of the gate's tests.
func TestTheBrowserTestsRefuseWhatTheyDoNotTake(t *testing.T) {
	plain := func(t *testing.T) []byte { return browserTar(t) }
	for name, tc := range map[string]struct {
		lock    string
		browser func(t *testing.T) []byte
		want    string
	}{
		"another registry": {"PLAYWRIGHT_IMAGE=evil.example/playwright@sha256:" + strings.Repeat("d", 64) + "\n", plain, "not Microsoft's Playwright"},
		"a flag":           {"PLAYWRIGHT_IMAGE=--volume=/:/h@sha256:" + strings.Repeat("d", 64) + "\n", plain, "not Microsoft's Playwright"},
		"no hash":          {"PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.64.0-noble\n", plain, "not Microsoft's Playwright"},
		"no image":         {"GO_VERSION=1.27\n", plain, "not Microsoft's Playwright"},
		"out of its folder": {playwrightLock, func(t *testing.T) []byte {
			return browserTar(t, &tar.Header{Name: "../x", Typeflag: tar.TypeReg, Size: 1})
		}, "leads out"},
		"absolute": {playwrightLock, func(t *testing.T) []byte {
			return browserTar(t, &tar.Header{Name: "/etc/x", Typeflag: tar.TypeReg, Size: 1})
		}, "leads out"},
		"a second copy": {playwrightLock, func(t *testing.T) []byte {
			return browserTar(t, &tar.Header{Name: "package.json", Typeflag: tar.TypeReg, Size: 1})
		}, "exists"},
		"too large": {playwrightLock, func(t *testing.T) []byte {
			return browserTar(t, &tar.Header{Name: "big", Typeflag: tar.TypeReg, Size: 513 << 20})
		}, "more than"},
		"no configuration": {playwrightLock, func(t *testing.T) []byte {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			tw.WriteHeader(&tar.Header{Name: "gate/x.spec.ts", Typeflag: tar.TypeReg, Mode: 0o644})
			tw.Close()
			return b.Bytes()
		}, "playwright.gate.config.ts"},
	} {
		t.Run(name, func(t *testing.T) {
			c, r := composeKit(t)
			installFile(t, c)
			c.Work = filepath.Join(t.TempDir(), "gate-e2e")
			f := checkRelease(t, tc.lock, tc.browser(t))
			err := c.Browser(context.Background(), f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			for _, x := range r.ran {
				if strings.HasPrefix(x.cmd, "docker run") {
					t.Fatalf("a refused run started a microVM: %+v", r.ran)
				}
			}
			if _, err := os.Stat("/etc/x"); err == nil && name == "absolute" {
				t.Fatal("an absolute entry was written")
			}
		})
	}
}
