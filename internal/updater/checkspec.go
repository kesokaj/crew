package updater

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/kesokaj/crew/internal/config"
	"github.com/kesokaj/crew/internal/release"
)

// The check of a release (16.4 steps 3 and 4, since 0.51). The candidate's server is code agents
// wrote, so the updater writes its container's spec itself, from what it knows and the release's
// image ids, and never from a Compose file the release brought: no Docker socket, no group of
// Docker's, no folder of the sandboxes, no key file, the check's own secrets, and the scratch network,
// which has no way out. It runs code agents wrote, so it runs in a microVM itself, with no other runtime
// to fall back to (since 0.52). With no socket the check server starts no microVM: the updater starts
// the smoke run's, and the browser tests', from the release's images, in microVMs, as it runs the
// release's crew admin on the scratch copy before the check server starts.

// DefaultRuntime is the Docker runtime of a microVM, the one the host setup installs.
const DefaultRuntime = "mvm"

// checkMounts are the check's own secrets the check server reads, by their names in /run/secrets.
var checkMounts = []string{"db_crew", "db_crew_read", "ingress_token"}

// CheckArgs is the docker run of the check server for a release: its spec, which the updater writes
// itself. image is its server's image as imageRef gives it; env holds CREW_HOSTNAME, CREW_ADMIN_EMAIL,
// CREW_DATA_DIR, and TZ; db is the address of the check's database on the scratch network.
func (c *Compose) CheckArgs(f *release.Folder, image string, env map[string]string, db string) ([]string, error) {
	if !strings.HasPrefix(image, "sha256:") {
		return nil, fmt.Errorf("the release %s names no id of its server's image", f.Manifest.Version)
	}
	// The check server is code agents wrote: it runs in a microVM, never on another runtime, whatever
	// the server is allowed (since 0.52).
	if !config.IsVMRuntime(c.runtime()) {
		return nil, fmt.Errorf("the check server runs in a microVM, and the runtime set, %q, is no microVM's", c.runtime())
	}
	if !peerAddress(db) {
		return nil, fmt.Errorf("the check's database is given no address on crew-scratch: %q", tail(db, 80))
	}
	// It runs as the user crew, with no capability, no way to gain one, and bounds of its own, beside the
	// running Crew (security review of M16 P4).
	args := []string{"run", "-d", "--name", CheckContainer, "--runtime", c.runtime(), "--user", fmt.Sprintf("%d:%d", c.UID, c.GID),
		"--network", "crew-scratch", "--add-host", CheckDBContainer + ":" + db, "--restart", "no", "--label", "crew.check=" + f.Manifest.Version,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "4096", "--memory", "8g",
		// It forwards nothing, as the running server does not (13.6): a server that forwards starts no sandbox.
		"--sysctl", "net.ipv4.ip_forward=0",
		"--log-driver", "json-file", "--log-opt", "max-size=50m", "--log-opt", "max-file=3"}
	for _, k := range []string{"CREW_HOSTNAME", "CREW_ADMIN_EMAIL", "CREW_DATA_DIR"} {
		if env[k] == "" {
			return nil, fmt.Errorf("%s is not in the install file", k)
		}
		args = append(args, "-e", k+"="+env[k])
	}
	tz := env["TZ"]
	if tz == "" {
		tz = "UTC"
	}
	args = append(args, "-e", "TZ="+tz, "-e", "CREW_DB_HOST="+CheckDBContainer, "-e", "CREW_CHECK=1",
		"-e", "CREW_RUNTIME_IMAGE=crew-runtime:"+f.Manifest.Version, "-e", "CREW_SELF_CONTAINER="+CheckContainer)
	for _, s := range checkMounts {
		args = append(args, "-v", filepath.Join(c.Check, "secrets", s)+":/run/secrets/"+s+":ro")
	}
	return append(args, image, "serve", "--check"), nil
}

// imageRef is the image of the release's part name as this machine's Docker names it, its tag's id,
// once that is the image release.json names: its config digest, a manifest of its file that names it,
// or the manifest pulled.json names (D48). A config digest is no name on the containerd image store,
// whose ids are manifests', so the id release.json holds is not given to Docker as it is.
func (c *Compose) imageRef(ctx context.Context, f *release.Folder, name string) (string, error) {
	img, ok := f.Manifest.Images[name]
	if !ok || !strings.HasPrefix(img.ID, "sha256:") {
		return "", fmt.Errorf("the release %s names no id of its %s image", f.Manifest.Version, name)
	}
	out, err := c.Run(ctx, nil, nil, "docker", "image", "inspect", "--format", "{{.Id}}", img.Tag)
	if err != nil {
		return "", fmt.Errorf("the image %s: %w", img.Tag, err)
	}
	got := strings.TrimSpace(out)
	if got == img.ID {
		return got, nil
	}
	if pl, ok := f.Pulled[name]; ok && pl.Config == img.ID && got == pl.Manifest {
		return got, nil
	}
	if p, err := f.ImagePath(name); err == nil {
		if file, err := release.ReadImageFile(p); err == nil && file.AcceptsID(img.ID, got) && strings.HasPrefix(got, "sha256:") {
			return got, nil
		}
	}
	return "", fmt.Errorf("%s is %s on this machine, and release.json names %s", img.Tag, got, img.ID)
}

// adminTime bounds each command of the release's crew admin on the scratch copy.
const adminTime = 30 * time.Minute

// RestoreScratch reads snapshot into the check's database, without the ended months, and applies the
// release's schema to it, with the release's crew admin. That is code agents wrote, with a copy of the
// whole database, so it runs as the check server does: in a microVM, from a spec the updater writes
// itself, with the migrate role's secret alone, the one snapshot read only, and the database's address,
// one microVM for each command (security review of the gate's fix, 2026-10-21). What it reads of the
// snapshot is a copy the updater made in the check's own folder (copySnapshot).
func (c *Compose) RestoreScratch(ctx context.Context, f *release.Folder, snapshot string) error {
	if !config.IsVMRuntime(c.runtime()) {
		return fmt.Errorf("the release's crew admin runs in a microVM, and the runtime set, %q, is no microVM's", c.runtime())
	}
	// A mount names its paths with colons and commas: the updater's own path never holds one.
	if !filepath.IsAbs(snapshot) || strings.ContainsAny(snapshot, ":,") || strings.ContainsFunc(snapshot, unicode.IsControl) {
		return fmt.Errorf("the snapshot %q is no path a mount takes", tail(snapshot, 120))
	}
	env, err := c.installEnv()
	if err != nil {
		return fmt.Errorf("the install file: %w", err)
	}
	db, err := c.scratchAddress(ctx, CheckDBContainer)
	if err != nil {
		return err
	}
	image, err := c.imageRef(ctx, f, "crew-server")
	if err != nil {
		return err
	}
	if err := c.noVolumes(ctx, image); err != nil {
		return err
	}
	copied, err := c.copySnapshot(snapshot)
	if err != nil {
		return err
	}
	defer os.Remove(copied)
	for _, cmd := range [][]string{{"restore", copied, "--no-months"}, {"migrate"}} {
		args, err := c.adminArgs(image, env, db, cmd[0] == "restore", copied)
		if err != nil {
			return err
		}
		actx, cancel := context.WithTimeout(ctx, adminTime)
		_, err = c.runVM(actx, restoreVM, append(args, cmd...)...)
		cancel()
		if err != nil {
			return fmt.Errorf("crew admin %s of the release in a microVM (%s): %w", cmd[0], c.runtime(), err)
		}
	}
	return nil
}

// noVolumes refuses a release whose server image declares a volume: Docker makes one on the machine's
// disk for each, which the read-only root of the restore's microVM does not cover (security review of
// the gate's restore, round 2).
func (c *Compose) noVolumes(ctx context.Context, image string) error {
	out, err := c.Run(ctx, nil, nil, "docker", "image", "inspect", "--format", "{{json .Config.Volumes}}", image)
	if err != nil {
		return fmt.Errorf("the volumes of the release's server image: %w", err)
	}
	if v := strings.TrimSpace(out); v != "null" && v != "{}" {
		return fmt.Errorf("the release's server image declares a volume, or its volumes cannot be read: %q", tail(v, 120))
	}
	return nil
}

// copySnapshot copies the snapshot into the check's own folder, root's, and gives the copy's path: the
// snapshot is opened without following a link and must be a plain file of crew's with one name, and
// the copy is of the file that was opened. The snapshots' folder is crew's, and Docker, which is root,
// follows a link in a mount's source, so a link there could otherwise give the release another file;
// a hard link there would have root copy a file crew cannot read (security review of the gate's
// restore, rounds 1 and 2). The copy is the user crew's alone, as the snapshot is.
func (c *Compose) copySnapshot(snapshot string) (string, error) {
	src, err := os.OpenFile(snapshot, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("the snapshot: %w", err)
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return "", fmt.Errorf("the snapshot: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("the snapshot %s is no plain file", tail(snapshot, 120))
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(c.UID) || st.Nlink != 1 {
		return "", fmt.Errorf("the snapshot %s is not crew's alone, by one name", tail(snapshot, 120))
	}
	dst := filepath.Join(c.Check, filepath.Base(snapshot))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("the snapshot's copy: %w", err)
	}
	_, cerr := io.Copy(out, src)
	if err := errors.Join(cerr, out.Chown(c.UID, c.GID), out.Close()); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("the snapshot's copy: %w", err)
	}
	return dst, nil
}

// adminArgs is the spec of the release's crew admin on the scratch copy, up to its command: the
// release's server image by its id, as the user crew, with no capability, no way to gain one, and
// bounds of its own, its root read only with a small tmpfs, so it writes nothing of its own to the
// machine's disk (what it writes into the scratch database does reach it), on the scratch network
// alone; with the snapshot's copy, read only, when it restores.
func (c *Compose) adminArgs(image string, env map[string]string, db string, restore bool, snapshot string) ([]string, error) {
	tz := env["TZ"]
	if tz == "" {
		tz = "UTC"
	}
	args := []string{"--runtime", c.runtime(), "--user", fmt.Sprintf("%d:%d", c.UID, c.GID),
		"--network", "crew-scratch", "--add-host", CheckDBContainer + ":" + db,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "1024", "--memory", "2g",
		"--read-only", "--tmpfs", "/tmp:rw,size=64m",
		"-e", "CREW_DB_HOST=" + CheckDBContainer, "-e", "CREW_DATA_DIR=" + env["CREW_DATA_DIR"], "-e", "TZ=" + tz,
		"-v", filepath.Join(c.Check, "secrets", "db_crew_migrate") + ":/run/secrets/db_crew_migrate:ro"}
	if restore {
		args = append(args, "-v", snapshot+":"+snapshot+":ro")
	}
	return append(args, image, "admin"), nil
}

// installEnv is what the check server needs of the install file and the updater's own environment.
func (c *Compose) installEnv() (map[string]string, error) {
	env := map[string]string{}
	body, err := os.ReadFile(filepath.Join(c.Etc, "crew.env"))
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	env["TZ"] = env["CREW_TIMEZONE"]
	for _, e := range c.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == "CREW_DATA_DIR" {
			env[k] = v
		}
	}
	if env["CREW_DATA_DIR"] == "" {
		env["CREW_DATA_DIR"] = "/var/lib/crew"
	}
	return env, nil
}

// StartCheck starts the release's server in check mode on the scratch database, from the spec the
// updater writes (CheckArgs).
func (c *Compose) StartCheck(ctx context.Context, f *release.Folder) error {
	env, err := c.installEnv()
	if err != nil {
		return fmt.Errorf("the install file: %w", err)
	}
	db, err := c.scratchAddress(ctx, CheckDBContainer)
	if err != nil {
		return err
	}
	image, err := c.imageRef(ctx, f, "crew-server")
	if err != nil {
		return err
	}
	args, err := c.CheckArgs(f, image, env, db)
	if err != nil {
		return err
	}
	if _, err := c.Run(ctx, nil, nil, "docker", args...); err != nil {
		return fmt.Errorf("the check server did not start in a microVM (%s), and runs nowhere else: %w", c.runtime(), err)
	}
	return nil
}

// scratchAddress is a container's address on the scratch network. A microVM has a network stack of
// its own, which Docker's name server (127.0.0.11, in the container's namespace on the machine) does
// not answer, so a microVM there reaches a container by the address the updater gives it with
// --add-host, as the sandboxes reach the server by its address (M16's first gate on dave, 2026-10-21).
func (c *Compose) scratchAddress(ctx context.Context, name string) (string, error) {
	out, err := c.Run(ctx, nil, nil, "docker", "inspect", "--type", "container", "--format", `{{with index .NetworkSettings.Networks "crew-scratch"}}{{.IPAddress}}{{end}}`, name)
	if err != nil {
		return "", fmt.Errorf("%s has no address on crew-scratch: %w", name, err)
	}
	addr := strings.TrimSpace(out)
	if !peerAddress(addr) {
		return "", fmt.Errorf("%s has no address on crew-scratch: %q", name, tail(addr, 80))
	}
	return addr, nil
}

// peerAddress reports whether addr is a peer's address on a network: not a loopback's, and not the
// unspecified one, which would point a microVM at itself.
func peerAddress(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified()
}

func (c *Compose) runtime() string {
	if c.Runtime != "" {
		return c.Runtime
	}
	return DefaultRuntime
}

// maxProofLine bounds the event the smoke microVM prints.
const maxProofLine = 4096

// Smoke is the check server's smoke run (16.4 step 4, 16.8): the updater starts one microVM from the
// release's runtime image, with no network and nothing of the machine's, in which crew-run prints an
// event; the check server stores it and reads it back. Since 0.53 the microVM is bounded as the
// restore's is: no capability, no way to gain one, bounds of its own, and a read-only root with one
// small tmpfs, /tmp, which holds HOME and the proof's file.
func (c *Compose) Smoke(ctx context.Context, f *release.Folder) error {
	if !config.IsVMRuntime(c.runtime()) {
		return fmt.Errorf("the smoke run runs in a microVM, and the runtime set, %q, is no microVM's", c.runtime())
	}
	image, err := c.imageRef(ctx, f, "crew-runtime")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := c.runVM(ctx, smokeVM, "--runtime", c.runtime(), "--network", "none", "--user", "65534:65534",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256", "--memory", "1g",
		"--read-only", "--tmpfs", "/tmp:rw,size=16m", "-e", "HOME=/tmp", "--entrypoint", "crew-run", image, "proof")
	if err != nil {
		return fmt.Errorf("the microVM: %w", err)
	}
	line := lastLine(out)
	if len(line) == 0 || len(line) > maxProofLine || !json.Valid([]byte(line)) {
		return fmt.Errorf("the microVM printed no event: %q", tail(out, 200))
	}
	if _, err := c.Run(ctx, nil, []byte(line+"\n"), "docker", "exec", "-i", CheckContainer, "crew", "admin", "proof", "store"); err != nil {
		return fmt.Errorf("the check server did not store the microVM's event: %w", err)
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Selftest runs the selftest in the check server.
func (c *Compose) Selftest(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := c.Run(ctx, nil, nil, "docker", "exec", CheckContainer, "crew", "admin", "selftest")
	return err
}

// The release's browser tests of the screens: its browser.tar, which the build packed with their
// packages in its sandbox (16.2 step 5), and the image versions.lock names for them.
const (
	browserConfig = "playwright.gate.config.ts"
	// browserResults is the folder of the tests' results, in the microVM's tmpfs /tmp.
	browserResults = "/tmp/gate/results"
	// maxBrowserFiles bounds what is taken out of browser.tar, and maxBrowserEntries its entries: the
	// tests and their packages are some tens of MB.
	maxBrowserFiles   = 512 << 20
	maxBrowserEntries = 50000
)

// playwrightImage is the one image the browser tests run in: Microsoft's Playwright, by its hash. The
// name comes from the release's own versions.lock, so agents could change it; only this repository is
// taken, and nothing is fetched by the run itself.
var playwrightImage = regexp.MustCompile(`^mcr\.microsoft\.com/playwright:[A-Za-z0-9][A-Za-z0-9._-]{0,127}@sha256:[0-9a-f]{64}$`)

// The names of the microVMs the updater starts for a release: a microVM whose command ended by a
// timeout or a stop is removed by its name.
const (
	restoreVM = "crew-gate-restore"
	smokeVM   = "crew-gate-smoke"
	browserVM = "crew-gate-browser"
)

// runVM runs a microVM the updater starts, named name: one of an earlier run that was left is removed
// first, and this one is removed when its command fails, by a timeout too.
func (c *Compose) runVM(ctx context.Context, name string, args ...string) (string, error) {
	before, cancelBefore := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	_, _ = c.Run(before, nil, nil, "docker", "rm", "-f", name)
	cancelBefore()
	out, err := c.Run(ctx, nil, nil, "docker", append([]string{"run", "--rm", "--name", name}, args...)...)
	if err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, _ = c.Run(rctx, nil, nil, "docker", "rm", "-f", name)
	}
	return out, err
}

// Browser runs the release's browser tests, which only read, against the check server. They are code
// agents wrote, so they run in a microVM, on the scratch network alone, with the check's own ingress
// secret as a file and their folder read only; their packages came with the release, so the run
// fetches nothing. The microVM is bounded as the restore's is: the user crew, no capability, no way
// to gain one, bounds of its own, and a read-only root with one tmpfs, /tmp, which holds HOME and the
// results (security review of the gate's browser fix, 2026-10-21; Chromium starts so in Kata). The
// results go to browserResults, outside the tests' folder: Playwright removes its results' folder as
// it starts, which neither a read-only folder nor a mount point inside one allows (the gates of 0.2.11
// and 0.2.14 on dave).
func (c *Compose) Browser(ctx context.Context, f *release.Folder) error {
	// Whoever calls it: the check server, whose spec refuses the same, is no guard of its own here.
	if !config.IsVMRuntime(c.runtime()) {
		return fmt.Errorf("the browser tests run in a microVM, and the runtime set, %q, is no microVM's", c.runtime())
	}
	if c.Work == "" {
		return errors.New("the browser tests' folder is not set")
	}
	tests, err := f.BrowserPath()
	if err != nil {
		return err
	}
	deploy, err := f.DeployPath()
	if err != nil {
		return err
	}
	lock, err := archiveFile(deploy, "versions.lock")
	if err != nil {
		return fmt.Errorf("the release's versions.lock: %w", err)
	}
	img := lockValue(lock, "PLAYWRIGHT_IMAGE")
	if !playwrightImage.MatchString(img) {
		return fmt.Errorf("the release's versions.lock names %q for the browser tests, not Microsoft's Playwright image by its hash", tail(img, 120))
	}
	if err := os.RemoveAll(c.Work); err != nil {
		return err
	}
	// The microVM runs as the user crew, which reads the tests: they are the release's own, no secret.
	if err := os.MkdirAll(c.Work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(c.Work)
	if err := os.Chmod(c.Work, 0o755); err != nil {
		return err
	}
	if err := unpackBrowser(tests, c.Work); err != nil {
		return fmt.Errorf("the browser tests: %w", err)
	}
	env, err := c.installEnv()
	if err != nil {
		return fmt.Errorf("the install file: %w", err)
	}
	server, err := c.scratchAddress(ctx, CheckContainer)
	if err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	_, err = c.runVM(tctx, browserVM, "--runtime", c.runtime(), "--network", "crew-scratch", "--add-host", CheckContainer+":"+server,
		"-e", "CREW_ADMIN_EMAIL="+env["CREW_ADMIN_EMAIL"], "-e", "CREW_VERSION="+f.Manifest.Version,
		"-e", "CREW_INGRESS_TOKEN_FILE=/run/check-token", "-v", c.CheckToken()+":/run/check-token:ro",
		"-v", c.Work+":/work:ro", "-e", "HOME=/tmp", "-w", "/work",
		"--user", fmt.Sprintf("%d:%d", c.UID, c.GID), "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "4096", "--memory", "4g", "--read-only", "--tmpfs", "/tmp:rw,size=512m", img,
		"node", "node_modules/@playwright/test/cli.js", "test", "-c", browserConfig, "--output", browserResults)
	return err
}

// unpackBrowser writes the files of browser.tar into dir: plain files and folders only, by cleaned
// names that stay inside it. A link, which the packages' bin folders hold, is left out: the tests run
// by the package's own file.
func unpackBrowser(archive, dir string) error {
	r, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	tr := tar.NewReader(r)
	var total int64
	entries := 0
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if entries++; entries > maxBrowserEntries {
			return fmt.Errorf("more than %d entries", maxBrowserEntries)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%q leads out of its folder", h.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		case tar.TypeReg:
		default:
			continue
		}
		if total += h.Size; total > maxBrowserFiles {
			return fmt.Errorf("more than %d bytes", maxBrowserFiles)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(out, io.LimitReader(tr, h.Size))
		if err := errors.Join(cerr, out.Close()); err != nil {
			return err
		}
		if name == browserConfig {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("it holds no %s", browserConfig)
	}
	return nil
}

// archiveFile is one file of a deploy archive, at most 1 MB.
func archiveFile(archive, name string) (string, error) {
	r, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer r.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("the archive holds no %s", name)
		}
		if err != nil {
			return "", err
		}
		if path.Clean(h.Name) == name && h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			return string(b), err
		}
	}
}

// lockValue is a value of versions.lock.
func lockValue(lock, key string) string {
	v := ""
	for _, line := range strings.Split(lock, "\n") {
		if k, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			v = val
		}
	}
	return v
}
