package updater

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/release"
)

// The check's own (16.4, step 3): a database server beside the real one, with passwords made for
// each check, and an ingress secret of its own.
const (
	CheckDBContainer = "crew-db-check"
	// checkSecrets are made new for each check, in the check's folder.
	checkSecretBytes = 32
)

var checkSecrets = []string{"db_postgres", "db_crew", "db_crew_migrate", "db_crew_read", "ingress_token"}

// Run runs a command and returns what it printed, both streams; the real one, ExecRun, keeps at most
// maxOutput bytes from the output's start and as much from its end. A test's records the commands.
type Run func(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error)

// Compose is the real machine: Docker Compose with the deploy files crew-ctl installed, and docker
// exec into the database's and the servers' containers. The updater runs as root in its own
// container with the Docker socket, and sees the deploy files, /etc/crew, and the data folder at
// the paths they have on the machine, so Compose resolves every path as crew-ctl does.
type Compose struct {
	// Lib is /usr/local/lib/crew: compose.yaml, versions.lock, and db/10-roles.sh.
	Lib string
	// Etc is /etc/crew, with crew.env.
	Etc string
	// Check is the check's folder, the updater's check/: its secrets and its database's files. UID
	// and GID are the user crew's, whom the check's containers run as.
	Check    string
	UID, GID int
	// Runtime is the Docker runtime of a microVM, DefaultRuntime when empty. Work is the folder the
	// gate's browser tests are unpacked into, made new for each.
	Runtime string
	Work    string
	// Env is what Compose needs besides the version: CREW_UID, CREW_GID, CREW_DOCKER_GID, CREW_ETC.
	Env []string
	Run Run
}

// ExecRun is Run by os/exec. What the command prints, both streams, is kept at most maxOutput bytes
// from its start and as much from its end.
func ExecRun(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var kept capped
	cmd.Stdout, cmd.Stderr = &kept, &kept
	err := cmd.Run()
	out := kept.String()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(shown(args), " "), err, ends(out, 600))
	}
	return out, nil
}

// maxOutput bounds what the updater keeps of a command's output, from its start and from its end:
// some commands run code agents wrote, which could print without end (review of the gate's restore).
const maxOutput = 1 << 20

// capped keeps the first maxOutput bytes written to it and the last maxOutput; String marks what was
// left out between them. The end grows to twice its bound before it is cut, so a command that prints
// without end costs one copy of each byte, not one of the whole end for each write.
type capped struct {
	head, tail []byte
	left       bool
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := maxOutput - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head, p = append(c.head, p[:k]...), p[k:]
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > 2*maxOutput {
		c.tail, c.left = append(c.tail[:0:0], c.tail[len(c.tail)-maxOutput:]...), true
	}
	return n, nil
}

func (c *capped) String() string {
	tail, left := c.tail, c.left
	if len(tail) > maxOutput {
		tail, left = tail[len(tail)-maxOutput:], true
	}
	if left {
		return string(c.head) + "\n…\n" + string(tail)
	}
	return string(c.head) + string(tail)
}

// shown is a command's arguments as an error tells them: the value of each -e NAME=VALUE is left out,
// since the install's own values, such as its admin email, are no part of a gate's report.
func shown(args []string) []string {
	out := append([]string(nil), args...)
	for i := 1; i < len(out); i++ {
		if out[i-1] == "-e" {
			if k, _, ok := strings.Cut(out[i], "="); ok {
				out[i] = k + "=…"
			}
		}
	}
	return out
}

// ends is what a command printed as its error quotes it, at most n bytes: when it is longer, its start
// and its end, each cut at a whole character, since what says why can be either. Kata prints the cause
// first and a long stack trace after it, which pushed the cause out of the end alone (the gate of 0.2.11
// on dave: "…ectory"); psql and a Go program print it last.
func ends(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	head, from := n/2, len(s)-n/2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for from < len(s) && !utf8.RuneStart(s[from]) {
		from++
	}
	return strings.TrimSpace(s[:head]) + " … " + strings.TrimSpace(s[from:])
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func (c *Compose) compose(ctx context.Context, version string, extra []string, args ...string) (string, error) {
	env := append([]string{}, c.Env...)
	if version != "" {
		env = append(env, "CREW_VERSION="+version)
	}
	env = append(env, extra...)
	base := []string{"compose", "-p", "crew", "-f", filepath.Join(c.Lib, "compose.yaml"),
		"--env-file", filepath.Join(c.Lib, "versions.lock"), "--env-file", filepath.Join(c.Etc, "crew.env")}
	return c.Run(ctx, env, nil, "docker", append(base, args...)...)
}

// admin runs crew admin of version in a container that lives for one command.
func (c *Compose) admin(ctx context.Context, version string, extra []string, args ...string) (string, error) {
	run := []string{"run", "--rm", "-T"}
	for _, e := range extra {
		run = append(run, "-e", e)
	}
	return c.compose(ctx, version, nil, append(append(run, "crew-admin", "admin"), args...)...)
}

// Load loads each image file and checks that Docker has the tag with the id release.json names: its
// config digest, or a manifest of the file that names it, which is the id the containerd image store
// gives (D48). An image a release pull took from a registry is in Docker already, and is checked by
// the digests pulled.json names (16.12).
func (c *Compose) Load(ctx context.Context, f *release.Folder) error {
	for _, name := range release.Images {
		img := f.Manifest.Images[name]
		if pl, ok := f.Pulled[name]; ok {
			out, err := c.Run(ctx, nil, nil, "docker", "image", "inspect", "--format", "{{.Id}}", img.Tag)
			if err != nil {
				return fmt.Errorf("the pulled image %s is not on this machine: %w", img.Tag, err)
			}
			if got := strings.TrimSpace(out); pl.Config != img.ID || (got != img.ID && got != pl.Manifest) {
				return fmt.Errorf("%s is %s on this machine, and release.json names %s", img.Tag, got, img.ID)
			}
			continue
		}
		p, err := f.ImagePath(name)
		if err != nil {
			return err
		}
		// A saved image names the tags it gets when loaded; one that names another tag than its own
		// would move that tag, the running version's too.
		tags, err := RepoTags(p)
		if err != nil {
			return fmt.Errorf("the image %s: %w", name, err)
		}
		for _, t := range tags {
			if t != img.Tag {
				return fmt.Errorf("the image file of %s also names %s: a release names only its own", img.Tag, t)
			}
		}
		file, err := release.ReadImageFile(p)
		if err != nil {
			return fmt.Errorf("the image file of %s: %w", img.Tag, err)
		}
		if file.Config != "" && file.Config != img.ID {
			return fmt.Errorf("the image file of %s holds the image %s, and release.json names %s", img.Tag, file.Config, img.ID)
		}
		if _, err := c.Run(ctx, nil, nil, "docker", "load", "--quiet", "--input", p); err != nil {
			return err
		}
		out, err := c.Run(ctx, nil, nil, "docker", "image", "inspect", "--format", "{{.Id}}", img.Tag)
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(out); !file.AcceptsID(img.ID, got) {
			return fmt.Errorf("%s loaded as %s, and release.json names %s", img.Tag, got, img.ID)
		}
	}
	return nil
}

// LoadImage loads a build's image file, which must name tag alone: one that names another would move
// that tag, the running version's too. The file is not taken at its word: the tags of the release's
// own names (ownTag) are read before and after the load, also when the load failed, and a load that
// moved one, tag itself included when it was there before, or gave another, is undone and refused.
// The updater runs no install meanwhile, so no other change of those tags is in the way. It returns
// the id Docker gives the image.
func (c *Compose) LoadImage(ctx context.Context, path, tag string) (string, error) {
	tags, err := RepoTags(path)
	if err != nil {
		return "", err
	}
	if len(tags) == 0 || slices.ContainsFunc(tags, func(t string) bool { return t != tag }) {
		return "", fmt.Errorf("the image file names %v, not %s alone", tags, tag)
	}
	before, err := c.tags(ctx)
	if err != nil {
		return "", err
	}
	_, lerr := c.Run(ctx, nil, nil, "docker", "load", "--quiet", "--input", path)
	bg := context.WithoutCancel(ctx)
	after, err := c.tags(bg)
	if err != nil {
		// Once more: what the load moved cannot be told without it.
		if after, err = c.tags(bg); err != nil {
			return "", fmt.Errorf("the tags after the load of %s cannot be read, so what it moved is not known: %w", tag, err)
		}
	}
	var moved, stuck []string
	for t, id := range after {
		old, had := before[t]
		switch {
		case had && old != id:
			moved = append(moved, t)
			if _, err := c.Run(bg, nil, nil, "docker", "tag", old, t); err != nil {
				stuck = append(stuck, t)
			}
		case !had && (t != tag || lerr != nil):
			moved = append(moved, t)
			if _, err := c.Run(bg, nil, nil, "docker", "image", "rm", t); err != nil {
				stuck = append(stuck, t)
			}
		}
	}
	sort.Strings(moved)
	sort.Strings(stuck)
	switch {
	case len(stuck) > 0:
		return "", fmt.Errorf("the load of %s moved or gave %s, and %s could not be put back: it is refused", tag, strings.Join(moved, ", "), strings.Join(stuck, ", "))
	case lerr != nil:
		return "", lerr
	case len(moved) > 0:
		if _, had := before[tag]; !had {
			if _, err := c.Run(bg, nil, nil, "docker", "image", "rm", tag); err != nil {
				return "", fmt.Errorf("the load of %s moved or also tagged %s, and its own tag could not be taken away: it is refused", tag, strings.Join(moved, ", "))
			}
		}
		return "", fmt.Errorf("the load of %s moved or also tagged %s: it is undone and refused", tag, strings.Join(moved, ", "))
	}
	out, err := c.Run(ctx, nil, nil, "docker", "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !strings.HasPrefix(id, "sha256:") || after[tag] != id {
		return "", fmt.Errorf("%s loaded with the id %q", tag, id)
	}
	return id, nil
}

// ownTag reports whether a tag is of one of a release's own names, which Compose starts by tag: the
// load answers for those. A registry push's names, ghcr.io/..., are another's.
func ownTag(t string) bool {
	repo, _, _ := strings.Cut(t, ":")
	return slices.Contains(release.Images, repo)
}

// tags is every tag Docker has of a release's own names, with its image's id.
func (c *Compose) tags(ctx context.Context) (map[string]string, error) {
	out, err := c.Run(ctx, nil, nil, "docker", "image", "ls", "--no-trunc", "--format", "{{.Repository}}:{{.Tag}} {{.ID}}")
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		t, id, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok || strings.Contains(t, "<none>") || !ownTag(t) {
			continue
		}
		tags[t] = id
	}
	return tags, nil
}

// Untag takes a tag a load gave away again.
func (c *Compose) Untag(ctx context.Context, tag string) error {
	_, err := c.Run(ctx, nil, nil, "docker", "image", "rm", tag)
	return err
}

// Snapshot dumps the real database into file with version's crew admin.
func (c *Compose) Snapshot(ctx context.Context, version, file string) error {
	_, err := c.admin(ctx, version, nil, "backup", "--snapshot", file)
	return err
}

// psql runs SQL in crew-db as its superuser, over the container's own socket.
func (c *Compose) psql(ctx context.Context, database, sql string) error {
	_, err := c.Run(ctx, nil, []byte(sql), "docker", "exec", "-i", "crew-db",
		"psql", "-X", "-q", "-U", "postgres", "-d", database, "-v", "ON_ERROR_STOP=1", "-f", "-")
	return err
}

// makeDatabase makes name again, empty, with the privileges the roles script gives crew.
func (c *Compose) makeDatabase(ctx context.Context, name string) error {
	script, err := os.ReadFile(filepath.Join(c.Lib, "db", "10-roles.sh"))
	if err != nil {
		return err
	}
	grants, err := db.Grants(script, name)
	if err != nil {
		return err
	}
	if err := c.psql(ctx, "postgres", fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE);\nCREATE DATABASE %s OWNER crew_migrate;\n", name, name)); err != nil {
		return err
	}
	return c.psql(ctx, name, strings.Join(grants, ";\n")+";\n")
}

// Scratch makes the check's database server again, empty, with new passwords and a new ingress
// secret, and waits for it to answer.
func (c *Compose) Scratch(ctx context.Context) error {
	if err := c.DropScratch(ctx); err != nil {
		return err
	}
	// The folder and the secrets are root's, so nothing the user crew does can lead a write of root's
	// elsewhere; the secrets are readable to the containers through Docker, which mounts them. Only the
	// database's own folder is crew's, made new after the folder was removed.
	secrets := filepath.Join(c.Check, "secrets")
	if err := os.MkdirAll(c.Check, 0o711); err != nil {
		return err
	}
	if err := os.Mkdir(secrets, 0o700); err != nil {
		return err
	}
	for _, name := range checkSecrets {
		b := make([]byte, checkSecretBytes)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(secrets, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(hex.EncodeToString(b))
		if err := errors.Join(werr, f.Close()); err != nil {
			return err
		}
	}
	db := filepath.Join(c.Check, "db")
	if err := os.Mkdir(db, 0o700); err != nil {
		return err
	}
	if err := os.Chown(db, c.UID, c.GID); err != nil {
		return err
	}
	if _, err := c.compose(ctx, "", nil, "--profile", "check", "up", "-d", "--no-deps", "crew-db-check"); err != nil {
		return err
	}
	for i := 0; i < 120; i++ {
		out, err := c.Run(ctx, nil, nil, "docker", "inspect", "--format", "{{.State.Health.Status}}", CheckDBContainer)
		if err == nil && strings.TrimSpace(out) == "healthy" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("the check's database did not answer in two minutes")
}

// CheckToken is the file of the check server's ingress secret, which the gate's browser tests read.
func (c *Compose) CheckToken() string { return filepath.Join(c.Check, "secrets", "ingress_token") }

// DropScratch removes the check's server and database and its folder.
func (c *Compose) DropScratch(ctx context.Context) error {
	if out, err := c.Run(ctx, nil, nil, "docker", "rm", "-f", "-v", CheckContainer, CheckDBContainer); err != nil && !strings.Contains(out, "No such container") {
		return err
	}
	if c.Check == "" {
		return errors.New("the check's folder is not set")
	}
	return os.RemoveAll(c.Check)
}

// StopCheck removes the check server.
func (c *Compose) StopCheck(ctx context.Context) error {
	_, err := c.Run(ctx, nil, nil, "docker", "rm", "-f", CheckContainer)
	return err
}

// Health asks the server in container for its health.
func (c *Compose) Health(ctx context.Context, container string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := c.Run(ctx, nil, nil, "docker", "exec", container, "crew", "health")
	return err
}

// Proof starts one microVM from the server's runtime image and stores an event it printed.
func (c *Compose) Proof(ctx context.Context, container string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := c.Run(ctx, nil, nil, "docker", "exec", container, "crew", "admin", "proof", "event")
	return err
}

// StopServer stops the server: no new run starts until the next one is up (16.7).
func (c *Compose) StopServer(ctx context.Context) error {
	_, err := c.Run(ctx, nil, nil, "docker", "stop", "--time", "20", ServerContainer)
	return err
}

// StartServer starts version's server and ingress, made again from its images.
func (c *Compose) StartServer(ctx context.Context, version string) error {
	_, err := c.compose(ctx, version, nil, "up", "-d", "--no-deps", "crew-server", "crew-caddy")
	return err
}

// Migrate applies version's schema to the real database, every new step in one transaction.
func (c *Compose) Migrate(ctx context.Context, version string) error {
	_, err := c.admin(ctx, version, nil, "migrate", "--at-once")
	return err
}

// Restore reads snapshot back into the real database, with its month files (16.6). It is read into a
// new database first, so that no table of a newer schema is left beside it; the new one takes the
// real one's name only when it is complete, and a restore that fails leaves the real one as it was.
func (c *Compose) Restore(ctx context.Context, version, snapshot string) error {
	const next, before = "crew_restore", "crew_before_restore"
	if err := c.makeDatabase(ctx, next); err != nil {
		return err
	}
	on := []string{"CREW_DB_NAME=" + next}
	if _, err := c.admin(ctx, version, on, "restore", snapshot); err != nil {
		_ = c.psql(context.WithoutCancel(ctx), "postgres", "DROP DATABASE IF EXISTS "+next+" WITH (FORCE);\n")
		return err
	}
	if _, err := c.admin(ctx, version, on, "migrate"); err != nil {
		_ = c.psql(context.WithoutCancel(ctx), "postgres", "DROP DATABASE IF EXISTS "+next+" WITH (FORCE);\n")
		return err
	}
	return c.psql(ctx, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %[2]s WITH (FORCE);
ALTER DATABASE crew RENAME TO %[2]s;
ALTER DATABASE %[1]s RENAME TO crew;
DROP DATABASE %[2]s WITH (FORCE);
`, next, before))
}

// Remove removes the images of a version that is no longer kept. PostgreSQL's image is shared by
// releases, and stays.
func (c *Compose) Remove(ctx context.Context, version string) error {
	var tags []string
	for _, name := range release.Images {
		if name != "postgres" {
			tags = append(tags, name+":"+version)
		}
	}
	out, err := c.Run(ctx, nil, nil, "docker", append([]string{"image", "rm"}, tags...)...)
	if err != nil && strings.Contains(out, "No such image") {
		return nil
	}
	return err
}

// RepoTags reads the names a file of docker save gives its images when loaded: the tags of its
// manifest.json, the image names of its index.json, as Docker's containerd image store writes them,
// and the tags of the legacy repositories file. A name is read as Docker reads it, cleaned, so
// ./manifest.json is manifest.json; a second entry of one of these files is refused, since one of
// Docker's loaders takes the last and another the first (security review of M16 P3).
func RepoTags(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var tags []string
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := pathpkg.Clean(strings.TrimLeft(hdr.Name, "/"))
		switch name {
		case "manifest.json", "index.json", "repositories", "oci-layout":
			if seen[name] {
				return nil, fmt.Errorf("the file holds %s twice", name)
			}
			seen[name] = true
		}
		switch name {
		case "manifest.json":
			var m []struct {
				RepoTags []string `json:"RepoTags"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
				return nil, fmt.Errorf("manifest.json: %w", err)
			}
			for _, e := range m {
				tags = append(tags, e.RepoTags...)
			}
		case "index.json":
			var idx struct {
				Manifests []struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"manifests"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&idx); err != nil {
				return nil, fmt.Errorf("index.json: %w", err)
			}
			for _, m := range idx.Manifests {
				if n := m.Annotations["io.containerd.image.name"]; n != "" {
					tags = append(tags, shortName(n))
				} else if n := m.Annotations["org.opencontainers.image.ref.name"]; strings.Contains(n, ":") {
					tags = append(tags, shortName(n))
				}
			}
		case "repositories":
			var repos map[string]map[string]string
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&repos); err != nil {
				return nil, fmt.Errorf("repositories: %w", err)
			}
			for repo, byTag := range repos {
				for tag := range byTag {
					tags = append(tags, shortName(repo)+":"+tag)
				}
			}
		}
	}
	if !seen["manifest.json"] {
		return nil, errors.New("no manifest.json: not a file docker save wrote")
	}
	return tags, nil
}

// shortName is an image name as Docker shows a local one: without docker.io/library/.
func shortName(n string) string {
	n = strings.TrimPrefix(n, "docker.io/")
	return strings.TrimPrefix(n, "library/")
}
