// Package deploy holds the tests of crew-ctl that need no root (design 16.11: "the same without
// root against a scratch folder, with make check, on every change"). Every path crew-ctl writes to
// is pointed at a scratch folder, and docker, systemctl, and the other tools it calls are fakes on
// PATH that write down how they were called.
package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/updater"
)

type ctl struct {
	t    *testing.T
	root string
	log  string
	env  []string
	// dir is the deploy folder crew-ctl runs from: this one, or that of a checkout made of this tree
	// (inCheckout).
	dir string
}

// fakes writes a fake of each tool that appends its arguments to the log and succeeds.
func newCtl(t *testing.T) *ctl {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls.log")
	for _, tool := range []string{"docker", "systemctl", "iptables", "ip6tables", "userdel", "apt-get", "hostname"} {
		script := "#!/bin/sh\necho \"" + tool + " $*\" >> " + log + "\n"
		if tool == "hostname" {
			script += "echo testbox\n"
		}
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &ctl{t: t, root: root, log: log}
	c.env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"CREW_CTL_TEST=1",
		"CREW_CTL_TEST_NO_MACHINE=1",
		"CREW_CTL_ETC_DIR="+filepath.Join(root, "etc", "crew"),
		"CREW_CTL_LIB_DIR="+filepath.Join(root, "lib", "crew"),
		"CREW_CTL_SBIN_DIR="+filepath.Join(root, "sbin"),
		"CREW_CTL_UNIT_DIR="+filepath.Join(root, "units"),
		"CREW_CTL_REBOOT_REQUIRED="+filepath.Join(root, "reboot-required"),
		"CREW_CTL_LOCK="+filepath.Join(root, "crew-ctl.lock"),
	)
	return c
}

func (c *ctl) run(args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command("bash", append([]string{"crew-ctl"}, args...)...)
	cmd.Env = c.env
	cmd.Dir = c.dir
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatal(err)
	}
	return string(out), code
}

// inCheckout runs crew-ctl from a git checkout, which its build and its gate read: this repository,
// or, where this tree is no checkout, a commit of a copy of it. Crew's project's check sandbox gets the
// tree as git archive writes it, with no .git (16.2, since 0.53), and make check runs there too.
func (c *ctl) inCheckout() {
	c.t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		return
	}
	dst := c.t.TempDir()
	err = filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, p)
		switch {
		case d.IsDir() && (d.Name() == ".check" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		case !d.Type().IsRegular():
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), body, info.Mode().Perm())
	})
	if err != nil {
		c.t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "the tree"}} {
		if out, err := exec.Command("git", append([]string{"-C", dst}, args...)...).CombinedOutput(); err != nil {
			c.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	c.dir = filepath.Join(dst, "deploy")
}

// withoutProxy is env without a proxy of its caller's: make check in a Sources check sandbox runs with
// one, and with the module proxy, which release.sh would hand on beside the ones a test gives it.
func withoutProxy(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "GOPROXY", "CREW_SANDBOX":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (c *ctl) calls() string {
	b, _ := os.ReadFile(c.log)
	return string(b)
}

func (c *ctl) answers(name, text string) string {
	c.t.Helper()
	p := filepath.Join(c.root, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		c.t.Fatal(err)
	}
	return p
}

// install fakes what an install leaves in /etc/crew, so the verbs that need one can run.
func (c *ctl) install(env string) {
	c.t.Helper()
	etc := filepath.Join(c.root, "etc", "crew")
	if err := os.MkdirAll(filepath.Join(etc, "secrets"), 0o700); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "crew.env"), []byte(env), 0o600); err != nil {
		c.t.Fatal(err)
	}
	j := `{"version": "1.2.3", "data_dir": "` + filepath.Join(c.root, "data") + `",
		"machine": {"docker": "found 29.7.2", "kata": "installed 3.31.0", "daemon_json": {"ipv6": "<absent>"}}}`
	if err := os.WriteFile(filepath.Join(etc, "install.json"), []byte(j), 0o600); err != nil {
		c.t.Fatal(err)
	}
	for _, d := range []string{"data", "lib/crew"} {
		if err := os.MkdirAll(filepath.Join(c.root, d), 0o755); err != nil {
			c.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(c.root, "lib", "crew", "compose.yaml"), []byte("name: crew\n"), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

const good = `CREW_HOSTNAME=crew.example.com
CREW_ADMIN_EMAIL=admin@example.com
GOOGLE_CLIENT_ID=1234-abc.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=not-a-real-secret
CREW_TLS=internal
`

func TestTheTestSeamsAreRefusedOutsideTheTests(t *testing.T) {
	cmd := exec.Command("bash", "crew-ctl", "status")
	cmd.Env = append(os.Environ(), "CREW_CTL_ETC_DIR=/tmp/x")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "for the tests only") {
		t.Fatalf("an override outside the tests was taken: %v %s", err, out)
	}
}

func TestDoctorChecksTheAnswers(t *testing.T) {
	c := newCtl(t)
	out, code := c.run("doctor", "--json", "--env-file", c.answers("good.env", good))
	if code != 0 {
		t.Fatalf("good answers: exit %d\n%s", code, out)
	}
	var res struct {
		OK     bool `json:"ok"`
		Checks []struct{ Check, Status, Detail string }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.OK {
		t.Fatalf("%v\n%s", err, out)
	}

	for name, c2 := range map[string]struct{ text, want string }{
		"no admin":          {strings.Replace(good, "CREW_ADMIN_EMAIL=admin@example.com", "CREW_ADMIN_EMAIL=", 1), "CREW_ADMIN_EMAIL must be given"},
		"not mail":          {strings.Replace(good, "admin@example.com", "admin", 1), "CREW_ADMIN_EMAIL: not a mail address"},
		"not a host":        {strings.Replace(good, "crew.example.com", "crew", 1), "CREW_HOSTNAME: not a host name"},
		"a dollar":          {good + "CREW_TIMEZONE=${HOME}\n", "CREW_TIMEZONE: no spaces"},
		"same domain":       {good + "CREW_APPS_DOMAIN=crew.example.com\n", "must not be CREW_HOSTNAME"},
		"under crew":        {good + "CREW_APPS_DOMAIN=apps.crew.example.com\n", "must not be CREW_HOSTNAME"},
		"over crew":         {good + "CREW_APPS_DOMAIN=example.com\n", "must not be CREW_HOSTNAME"},
		"sibling subdomain": {good + "CREW_APPS_DOMAIN=apps.example.com\n", ""},
		"tls word":          {strings.Replace(good, "CREW_TLS=internal", "CREW_TLS=letsencrypt", 1), "CREW_TLS: acme, staging, or internal"},
		"client needed":     {strings.Replace(strings.Replace(good, "CREW_TLS=internal", "CREW_TLS=acme", 1), "GOOGLE_CLIENT_ID=1234-abc.apps.googleusercontent.com", "GOOGLE_CLIENT_ID=", 1), "GOOGLE_CLIENT_ID must be given"},
		"client id form":    {strings.Replace(good, "1234-abc.apps.googleusercontent.com", "1234", 1), "GOOGLE_CLIENT_ID: a Google client id"},
		"relative data":     {good + "CREW_DATA_DIR=var/lib/crew\n", "CREW_DATA_DIR: an absolute path"},
		"two level suffix":  {good + "CREW_APPS_DOMAIN=apps.example.co.uk\n", ""},
	} {
		out, code := c.run("doctor", "--env-file", c.answers(name+".env", c2.text))
		if c2.want == "" {
			if code != 0 {
				t.Errorf("%s: exit %d\n%s", name, code, out)
			}
			continue
		}
		if code != 1 || !strings.Contains(out, c2.want) {
			t.Errorf("%s: exit %d, want 1 and %q\n%s", name, code, c2.want, out)
		}
	}
}

// An installation that is there already (21): doctor says it is a repair, and what is wrong with
// it: an install that stopped before it finished, or services that do not run.
func TestDoctorSaysWhatIsWrongWithAnInstallation(t *testing.T) {
	c := newCtl(t)
	etc := filepath.Join(c.root, "etc", "crew")
	if err := os.MkdirAll(etc, 0o700); err != nil {
		t.Fatal(err)
	}
	answers := c.answers("good.env", good)
	if out, _ := c.run("doctor", "--env-file", answers); strings.Contains(out, "already there") {
		t.Fatalf("a fresh machine has an installation:\n%s", out)
	}
	for rec, want := range map[string]string{
		`{"data_dir": "/var/lib/crew", "machine_setup": "1"}`: "it stopped before it finished: crew-ctl install goes on from where it stopped",
		`{"data_dir": "/var/lib/crew", "version": "1.2.3"}`:   "1.2.3 is installed, but not running: crew-caddy crew-server crew-updater crew-db",
		`{"data_dir": "/srv/crew", "version": "1.2.3"}`:       "an installation of Crew with the data folder /srv/crew",
	} {
		if err := os.WriteFile(filepath.Join(etc, "install.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _ := c.run("doctor", "--env-file", answers)
		if !strings.Contains(out, want) || (!strings.Contains(rec, "/srv") && !strings.Contains(out, "this is a repair or an update")) {
			t.Errorf("%s:\n%s", rec, out)
		}
	}
}

// Doctor checks the sign-in client and its redirect addresses against Google's token and sign-in
// endpoints, and checks auth.<CREW_APPS_DOMAIN> in DNS when CREW_APPS_DOMAIN is set (16.11, 19.4).
// Google and the name lookups are faked: the test reaches no network.
func TestDoctorSaysTheRedirectAddress(t *testing.T) {
	c := newCtl(t)
	bin := filepath.Join(c.root, "bin")
	mismatchURI := filepath.Join(c.root, "mismatch-uri")
	authStatus := filepath.Join(c.root, "auth-status")
	for tool, script := range map[string]string{
		"curl": "#!/bin/sh\necho \"curl $*\" >> " + c.log + "\ncat >/dev/null\n" +
			"case \"$*\" in\n" +
			"  *oauth2.googleapis.com*) echo '{\"error\": \"invalid_grant\"}' ;;\n" +
			"  *accounts.google.com/o/oauth2/v2/auth*)\n" +
			"    bad=\"$(cat " + mismatchURI + " 2>/dev/null || true)\"\n" +
			"    st=\"$(cat " + authStatus + " 2>/dev/null || echo 200)\"\n" +
			"    if [ -n \"$bad\" ] && case \"$*\" in *\"redirect_uri=$bad\"*) true ;; *) false ;; esac; then\n" +
			"      printf 'Error 400: redirect_uri_mismatch\\n400'\n" +
			"    elif [ \"$st\" != 200 ]; then\n" +
			"      printf 'Error %s: invalid_request\\n%s' \"$st\" \"$st\"\n" +
			"    else\n" +
			"      printf '<!doctype html><title>Sign in</title>\\n200'\n" +
			"    fi\n" +
			"    ;;\n" +
			"  *) echo 203.0.113.7 ;;\n" +
			"esac\n",
		"getent": "#!/bin/sh\necho \"203.0.113.7 STREAM $3\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const want = "https://auth.crew.example.com/oauth2/google/authorization-code-callback"
	const wantApps = "https://auth.crew-apps.example/oauth2/google/authorization-code-callback"
	acme := strings.Replace(good, "CREW_TLS=internal", "CREW_TLS=acme", 1)
	out, code := c.run("doctor", "--env-file", c.answers("acme.env", acme))
	if code != 0 || !strings.Contains(out, "it lists the redirect address "+want) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(c.calls(), "accounts.google.com/o/oauth2/v2/auth") || !strings.Contains(c.calls(), "redirect_uri="+want) {
		t.Errorf("Google's sign-in endpoint was not asked with the redirect address:\n%s", c.calls())
	}

	// Hosted projects sign in at the apps domain's own portal, a second redirect address and DNS name (19.4).
	apps := acme + "CREW_APPS_DOMAIN=crew-apps.example\n"
	out, code = c.run("doctor", "--env-file", c.answers("apps.env", apps))
	if code != 0 || !strings.Contains(out, "and for hosted projects "+wantApps) {
		t.Fatalf("with an apps domain: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "auth.crew-apps.example points at this machine (203.0.113.7)") {
		t.Errorf("auth.<CREW_APPS_DOMAIN> DNS was not checked:\n%s", out)
	}
	if !strings.Contains(c.calls(), "redirect_uri="+wantApps) {
		t.Errorf("Google was not asked with the apps domain's redirect address:\n%s", c.calls())
	}

	// When Google returns redirect_uri_mismatch for a redirect address, doctor fails and names the missing URI.
	for _, bad := range []string{want, wantApps} {
		if err := os.WriteFile(mismatchURI, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		out, code = c.run("doctor", "--env-file", c.answers("apps.env", apps))
		if code != 1 || !strings.Contains(out, "redirect_uri_mismatch") || !strings.Contains(out, bad) {
			t.Errorf("mismatch for %s: exit %d\n%s", bad, code, out)
		}
	}
	_ = os.Remove(mismatchURI)

	// Other 4xx OAuth errors fail closed; 5xx errors warn rather than silently passing as ok.
	if err := os.WriteFile(authStatus, []byte("400"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code = c.run("doctor", "--env-file", c.answers("acme.env", acme))
	if code != 1 || !strings.Contains(out, "HTTP 400") {
		t.Errorf("HTTP 400 without mismatch: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(authStatus, []byte("503"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code = c.run("doctor", "--env-file", c.answers("acme.env", acme))
	if code != 0 || !strings.Contains(out, "warn") || !strings.Contains(out, "gave no answer") {
		t.Errorf("HTTP 503 should warn, not pass ok: exit %d\n%s", code, out)
	}
	_ = os.Remove(authStatus)

	out, _ = c.run("doctor", "--env-file", c.answers("internal.env", good))
	if strings.Contains(out, "redirect address") {
		t.Errorf("a stand-in sign-in client has no redirect address at Google:\n%s", out)
	}
}

func TestStatusSaysWhenNothingIsInstalled(t *testing.T) {
	c := newCtl(t)
	out, code := c.run("status", "--json")
	if code != 2 || !strings.Contains(out, `"installed": false`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestInstallNeedsBuild(t *testing.T) {
	c := newCtl(t)
	out, code := c.run("install", "--dry-run", "--env-file", c.answers("a.env", good))
	if code == 0 || !strings.Contains(out, "needs --build") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// Install puts the deploy files in place only once the images are built: a build that fails leaves
// the machine's files those of the release that runs, which its updater reads (the install of 0.2.12
// on dave failed at the runtime image and left the checkout's Compose file beside updater 0.2.10).
func TestInstallWritesTheDeployFilesAfterTheImages(t *testing.T) {
	c := newCtl(t)
	c.inCheckout()
	out, _ := c.run("install", "--build", "--dry-run", "--env-file", c.answers("a.env", good))
	lines := strings.Split(out, "\n")
	images, copies := -1, 0
	lib, sbin := filepath.Join(c.root, "lib", "crew"), filepath.Join(c.root, "sbin")
	for i, l := range lines {
		if images < 0 && (strings.HasPrefix(l, "+ docker build ") || strings.HasPrefix(l, "+ bash ") && strings.Contains(l, "/release.sh build ")) {
			images = i
		}
		// Every copy into the machine's folders of crew-ctl and its files.
		if strings.HasPrefix(l, "+ install ") && (strings.Contains(l, lib) || strings.Contains(l, sbin)) {
			copies++
			if images < 0 {
				t.Fatalf("a deploy file is put in place before the images are built: %s\n%s", l, out)
			}
		}
	}
	if images < 0 || copies < 6 || !strings.Contains(out, "/deploy/compose.yaml ") || !strings.Contains(out, "/deploy/crew-ctl ") {
		t.Fatalf("the dry run names no build, or not every deploy file (%d copies):\n%s", copies, out)
	}
	// The restart timer starts only once the crew-ctl its service runs is in place (16.10).
	timer, ctlCopy := strings.Index(out, "+ systemctl start crew-restart.timer"), strings.Index(out, "/deploy/crew-ctl "+sbin)
	if timer < 0 || ctlCopy < 0 || timer < ctlCopy {
		t.Fatalf("the restart timer starts before crew-ctl is in place (%d, %d):\n%s", timer, ctlCopy, out)
	}
	// The runtime image gets agy's tarball, which crew-ctl downloads as the checkout's versions.lock
	// pins it, as its build context agy, and no other image does (12.8, since 0.57).
	if !strings.Contains(out, "+ curl --proto =https --max-redirs 0 https://storage.googleapis.com/antigravity-public/") {
		t.Fatalf("the dry run downloads no agy:\n%s", out)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "+ docker build ") && strings.Contains(l, "/image/Dockerfile") != strings.Contains(l, "--build-context agy=") {
			t.Errorf("the build context agy: %s", l)
		}
	}
}

func TestEditValidatesBeforeItWrites(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	before, _ := os.ReadFile(filepath.Join(c.root, "etc", "crew", "crew.env"))
	for _, kv := range []string{"CREW_ADMIN_EMAIL=nobody", "NOT_A_KEY=1", "CREW_TLS=maybe", "CREW_HOSTNAME"} {
		out, code := c.run("edit", "--dry-run", kv)
		if code == 0 {
			t.Errorf("%s was accepted\n%s", kv, out)
		}
	}
	// A secret is never taken from the command line, where the process list and the history keep it.
	out, code := c.run("edit", "--dry-run", "GOOGLE_CLIENT_SECRET=other-secret")
	if code == 0 || !strings.Contains(out, "give GOOGLE_CLIENT_SECRET without a value") || strings.Contains(out, "other-secret") {
		t.Fatalf("a secret on the command line: exit %d\n%s", code, out)
	}
	out, code = c.run("edit", "--dry-run", "CREW_TIMEZONE=Europe/Stockholm")
	if code != 0 || !strings.Contains(out, "+ write") || !strings.Contains(out, "CREW_TIMEZONE: Europe/Stockholm") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	after, _ := os.ReadFile(filepath.Join(c.root, "etc", "crew", "crew.env"))
	if string(before) != string(after) {
		t.Fatal("a dry run changed the install file")
	}
	if calls := c.calls(); strings.Contains(calls, "docker compose") && !strings.Contains(out, "+ docker compose") {
		t.Fatalf("a dry run ran docker: %s", calls)
	}
}

// Uninstall prints its plan, asks for its word, and takes out what install put in (16.11).
func TestUninstallLevels(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	if out, code := c.run("uninstall", "--level", "2", "--confirm", "wrong"); code == 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("a wrong word: exit %d\n%s", code, out)
	}
	if calls := c.calls(); strings.Contains(calls, "compose") || strings.Contains(calls, "systemctl disable") {
		t.Fatalf("a refused uninstall changed something: %s", calls)
	}

	out, code := c.run("uninstall", "--level", "1", "--dry-run")
	if code != 0 || !strings.Contains(out, "keep everything") || !strings.Contains(out, "+ systemctl disable --now crew-firewall.timer") ||
		!strings.Contains(out, "+ systemctl disable --now crew-restart.timer") {
		t.Fatalf("level 1: exit %d\n%s", code, out)
	}

	out, code = c.run("uninstall", "--level", "2", "--dry-run")
	// The updater's lock beside its socket goes with it, so the folder can go too (since 0.53).
	for _, want := range []string{"compose -p crew", "down --remove-orphans", "+ systemctl disable --now crew-firewall.service",
		"rm -rf -- " + filepath.Join(c.root, "lib", "crew"), "keep " + filepath.Join(c.root, "data"), "/updater.lock",
		// The restart timer and the settings of the security updates (16.10).
		"+ systemctl disable --now crew-restart.timer", "+ rm -f " + filepath.Join(c.root, "units", "crew-restart.service"),
		"+ rm -f " + filepath.Join(c.root, "units", "crew-restart.timer"),
		"/etc/apt/apt.conf.d/52crew-unattended-upgrades /etc/apt/apt.conf.d/21crew-auto-upgrades"} {
		if !strings.Contains(out, want) {
			t.Errorf("level 2 does not show %q\n%s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "rm -rf -- "+filepath.Join(c.root, "data")) {
		t.Fatalf("level 2 removes the data: exit %d\n%s", code, out)
	}

	out, code = c.run("uninstall", "--level", "3", "--with-runtime", "--dry-run")
	for _, want := range []string{"rm -rf -- " + filepath.Join(c.root, "data"), "rm -rf -- " + filepath.Join(c.root, "etc", "crew"),
		"userdel crew", "rm -rf /opt/kata", "put back the keys of daemon.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("level 3 does not show %q\n%s", want, out)
		}
	}
	// Docker was found, not installed: uninstall leaves it.
	if code != 0 || strings.Contains(out, "apt-get purge") {
		t.Fatalf("level 3 removes a Docker Crew did not install: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(c.root, "data")); err != nil {
		t.Fatal("a dry run removed the data folder")
	}
}

// Uninstall --with-runtime takes out each folder of Kata that Crew unpacked, and 3.31's /opt/kata with
// its shim wrapper only when Crew installed it (13.2, 16.11, since 0.68).
func TestUninstallTakesOutCrewsKataFolders(t *testing.T) {
	for name, tc := range map[string]struct {
		machine   string
		want, not []string
	}{
		"moved from a 3.31 Crew installed": {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1", "/opt/kata"]}`,
			[]string{"rm -rf /opt/kata-4.3.1", "rm -rf /opt/kata\n", "rm -f /usr/local/bin/containerd-shim-mvm-v2"}, nil},
		"moved from a 3.31 Crew found": {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1"]}`,
			[]string{"rm -rf /opt/kata-4.3.1"}, []string{"rm -rf /opt/kata\n", "containerd-shim-mvm-v2"}},
		"3.31 removed while a newer Kata ran": {`{"kata": "installed 3.31.0", "kata_dirs_gone": ["/opt/kata"]}`,
			nil, []string{"rm -rf /opt/kata", "containerd-shim-mvm-v2"}},
		"3.31, before the move": {`{"kata": "installed 3.31.0"}`,
			[]string{"rm -rf /opt/kata\n", "rm -f /usr/local/bin/containerd-shim-mvm-v2"}, []string{"/opt/kata-"}},
		"a folder that is no Kata's": {`{"kata": "installed 4.3.1", "kata_dirs": ["/etc", "/opt/kata-1/../../etc", "/opt/kata-4.3.1 /etc"]}`,
			[]string{"not a folder of Kata, left: '/etc'", "not a folder of Kata, left: '/opt/kata-1/../../etc'", "not a folder of Kata, left: '/opt/kata-4.3.1 /etc'"},
			[]string{"rm -rf /etc", "rm -rf /opt/kata-1", "rm -rf /opt/kata-4.3.1"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCtl(t)
			c.install(good)
			j := `{"version": "1.2.3", "data_dir": "` + filepath.Join(c.root, "data") + `", "machine": ` + tc.machine + `}`
			if err := os.WriteFile(filepath.Join(c.root, "etc", "crew", "install.json"), []byte(j), 0o600); err != nil {
				t.Fatal(err)
			}
			out, code := c.run("uninstall", "--level", "3", "--with-runtime", "--dry-run")
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out+"\n", w) {
					t.Errorf("does not show %q\n%s", w, out)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(out+"\n", n) {
					t.Errorf("shows %q\n%s", n, out)
				}
			}
		})
	}
}

// kata_ours hands the host setup, as KATA_OURS, the folders of Kata install.json says Crew put there, the
// only ones it may remove (13.2): what it unpacked, and 3.31's /opt/kata only when Crew installed it, from
// one run to the next as the record changes (review of 507b11c).
func TestKataOursFromTheRecord(t *testing.T) {
	for name, tc := range map[string]struct{ machine, want string }{
		"3.31 Crew installed":                {`{"kata": "installed 3.31.0"}`, "/opt/kata"},
		"3.31 Crew found":                    {`{"kata": "found 3.31.0"}`, ""},
		"moved, /opt/kata still in use":      {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1", "/opt/kata"]}`, "/opt/kata-4.3.1\n/opt/kata"},
		"moved, /opt/kata removed":           {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1"]}`, "/opt/kata-4.3.1"},
		"moved from a 3.31 Crew found":       {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1"]}`, "/opt/kata-4.3.1"},
		"a line that would make two":         {`{"kata": "installed 4.3.1", "kata_dirs": ["/opt/kata-4.3.1\n/etc"]}`, ""},
		"no record of Kata":                  {`{}`, ""},
		"3.31 Crew installed, since removed": {`{"kata": "installed 3.31.0", "kata_dirs_gone": ["/opt/kata"]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCtl(t)
			c.install(good)
			j := `{"version": "1.2.3", "machine": ` + tc.machine + `}`
			if err := os.WriteFile(filepath.Join(c.root, "etc", "crew", "install.json"), []byte(j), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", `source crew-ctl; kata_ours`)
			cmd.Env = c.env
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Errorf("kata_ours: %q (%v), want %q", out, err, tc.want)
			}
		})
	}
	c := newCtl(t)
	cmd := exec.Command("bash", "-c", `source crew-ctl; kata_ours; echo "rc=$?"`)
	cmd.Env = c.env
	if out, _ := cmd.CombinedOutput(); strings.TrimSpace(string(out)) != "rc=0" {
		t.Errorf("no install.json: %q", out)
	}
}

// doctor's line for Kata (16.11, since 0.68): an older Kata is no failure, install moves it; a newer one
// is left with a warning; 3.31's /opt/kata, which Crew did not install, is named once nothing uses it.
func TestDoctorSaysWhichKataRuns(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	root := filepath.Join(c.root, "machine")
	shim := "runtime-rs/bin/containerd-shim-kata-v2"
	mk := func(dir, version string) {
		if err := os.MkdirAll(filepath.Join(root, dir, filepath.Dir(shim)), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(root, dir, "VERSION"), []byte(version+"\n"), 0o644)
	}
	mk("opt/kata-4.3.1", "4.3.1")
	mk("opt/kata-4.9.0", "4.9.0")
	if err := os.MkdirAll(filepath.Join(root, "opt/kata/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "opt/kata/bin/kata-runtime"), []byte("#!/bin/sh\necho 'kata-runtime  : 3.31.0'\n"), 0o755)
	proc := filepath.Join(c.root, "proc")
	os.MkdirAll(filepath.Join(proc, "1"), 0o755)
	daemon := filepath.Join(c.root, "daemon.json")
	mvm := func(typ string) {
		os.WriteFile(daemon, []byte(`{"runtimes": {"mvm": {"runtimeType": "`+typ+`"}}}`), 0o644)
	}
	doctor := func() string {
		cmd := exec.Command("bash", "-c", `source crew-ctl; DOC_RESULTS=(); doctor_kata "$1" "$2" "$3" 4.3.1; printf '%s\n' "${DOC_RESULTS[@]}"`,
			"_", daemon, root, proc)
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	// As on dave: styrman-crew put 3.31 there, and Crew found it.
	os.WriteFile(filepath.Join(c.root, "etc", "crew", "install.json"), []byte(`{"machine": {"kata": "found 3.31.0"}}`), 0o600)
	mvm("io.containerd.mvm.v2")
	if out := doctor(); !strings.Contains(out, "ok|kata|3.31.0: install moves it to 4.3.1, beside it, and switches after its proof") ||
		strings.Contains(out, "no longer used") {
		t.Errorf("3.31 runs:\n%s", out)
	}
	mvm(filepath.Join(root, "opt/kata-4.3.1", shim))
	if out := doctor(); !strings.Contains(out, "ok|kata|4.3.1\n") ||
		!strings.Contains(out, "warn|kata|/opt/kata is no longer used, and Crew did not install it") {
		t.Errorf("4.3.1 runs, 3.31's folder unused and not Crew's:\n%s", out)
	}
	os.Symlink(filepath.Join(root, "opt/kata/bin/containerd-shim-kata-v2"), filepath.Join(proc, "1", "exe"))
	if out := doctor(); strings.Contains(out, "no longer used") {
		t.Errorf("a 3.31 shim still runs, and doctor says /opt/kata is unused:\n%s", out)
	}
	os.Remove(filepath.Join(proc, "1", "exe"))
	os.WriteFile(filepath.Join(c.root, "etc", "crew", "install.json"), []byte(`{"machine": {"kata": "installed 3.31.0"}}`), 0o600)
	if out := doctor(); strings.Contains(out, "no longer used") {
		t.Errorf("/opt/kata is Crew's, which the host setup removes, and doctor names it:\n%s", out)
	}
	mvm(filepath.Join(root, "opt/kata-4.9.0", shim))
	if out := doctor(); !strings.Contains(out, "warn|kata|4.9.0, newer than 4.3.1: left as it is") {
		t.Errorf("a newer Kata:\n%s", out)
	}
	mvm("runc")
	if out := doctor(); !strings.Contains(out, "warn|kata|Docker's runtime mvm starts no Kata that crew-ctl knows") {
		t.Errorf("mvm starts no Kata:\n%s", out)
	}
}

// install.json keeps what Crew put in: a later install that finds the thing does not make it
// "found", so uninstall still takes it out (16.11).
func TestTheRecordKeepsWhatCrewInstalled(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	rec := c.answers("record", "docker found 29.7.2\nkata found 3.31.0\nuser found crew\npackage zstd\nunit /etc/x.service\ndaemon_json {\"ipv6\": true}\n"+
		"kata_dir /opt/kata-4.3.1\nkata_dir /opt/kata\nkata_dir /opt/kata-4.3.1\nkata_dir /opt/kata-4.2.0\nkata_dir_gone /opt/kata-4.2.0\n")
	cmd := exec.Command("bash", "-c", `source crew-ctl; merge_host_record "$1"; merge_host_record "$1"`, "_", rec)
	cmd.Env = c.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(c.root, "etc", "crew", "install.json"))
	var j struct {
		Machine map[string]any `json:"machine"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	m := j.Machine
	if m["kata"] != "installed 3.31.0" || m["docker"] != "found 29.7.2" {
		t.Errorf("kata %v, docker %v", m["kata"], m["docker"])
	}
	if kd, _ := m["kata_dirs"].([]any); len(kd) != 2 || kd[0] != "/opt/kata-4.3.1" || kd[1] != "/opt/kata" {
		t.Errorf("kata_dirs %v: each folder once, in order, and one the host setup removed taken out", m["kata_dirs"])
	}
	if g, _ := m["kata_dirs_gone"].([]any); len(g) != 1 || g[0] != "/opt/kata-4.2.0" {
		t.Errorf("kata_dirs_gone %v: the folder the host setup removed", m["kata_dirs_gone"])
	}
	if pk, _ := m["packages"].([]any); len(pk) != 1 {
		t.Errorf("packages %v: once, however often recorded", m["packages"])
	}
	if dj, _ := m["daemon_json"].(map[string]any); dj["ipv6"] != "<absent>" {
		t.Errorf("daemon_json %v: the value before Crew's first change is the one kept", dj)
	}
}

// project export writes what crew admin streams into a folder of the machine, and project import
// streams a folder back, with the name and the key given (6.2). Neither takes a bad name or an
// export into a folder that holds something.
func TestProjectExportAndImport(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	src := filepath.Join(c.root, "src")
	os.MkdirAll(filepath.Join(src, "plan"), 0o755)
	os.WriteFile(filepath.Join(src, "project.md"), []byte("---\nname: shop\n---\n"), 0o644)
	os.WriteFile(filepath.Join(src, "plan", "original.md"), []byte("plan\n"), 0o644)
	// The fake server streams src for an export, and lists what an import is given.
	docker := "#!/bin/sh\necho \"docker $*\" >> " + c.log + "\n" +
		"case \"$*\" in\n" +
		"*'project export'*) tar -c -C " + src + " . ;;\n" +
		"*'project import'*) tar -t | sort >> " + c.log + " ;;\n" +
		"esac\n"
	os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte(docker), 0o755)

	out := filepath.Join(c.root, "out")
	if o, code := c.run("project", "export", "shop", out); code != 0 {
		t.Fatalf("export: exit %d\n%s", code, o)
	}
	if b, err := os.ReadFile(filepath.Join(out, "plan", "original.md")); err != nil || string(b) != "plan\n" {
		t.Fatalf("the folder: %q %v", b, err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder's mode: %v", fi.Mode())
	}
	if o, code := c.run("project", "export", "shop", out); code == 0 || !strings.Contains(o, "is not an empty folder") {
		t.Fatalf("an export into a folder with files: exit %d\n%s", code, o)
	}
	link := filepath.Join(c.root, "link")
	os.Symlink(filepath.Join(c.root, "elsewhere"), link)
	if o, code := c.run("project", "export", "shop", link); code == 0 || !strings.Contains(o, "is a link") {
		t.Fatalf("an export into a link: exit %d\n%s", code, o)
	}
	// An empty folder of its own is taken, and the export goes into it, unpacked beside it first.
	empty := filepath.Join(c.root, "empty")
	os.Mkdir(empty, 0o755)
	if o, code := c.run("project", "export", "shop", empty); code != 0 {
		t.Fatalf("an export into an empty folder: exit %d\n%s", code, o)
	}
	if left, _ := filepath.Glob(filepath.Join(c.root, ".crew-export.*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	// A parent that others may write to, and that is not sticky, is refused: another user could
	// replace the new folder there. A sticky one, as /tmp is, is taken.
	open := filepath.Join(c.root, "open")
	os.Mkdir(open, 0o777)
	os.Chmod(open, 0o777)
	if o, code := c.run("project", "export", "shop", filepath.Join(open, "out")); code == 0 || !strings.Contains(o, "not sticky") {
		t.Fatalf("an export under a folder anyone may write to: exit %d\n%s", code, o)
	}
	os.Chmod(open, os.ModeSticky|0o777)
	if o, code := c.run("project", "export", "shop", filepath.Join(open, "out")); code != 0 {
		t.Fatalf("an export under a sticky folder: exit %d\n%s", code, o)
	}
	if o, code := c.run("project", "export", "../shop", filepath.Join(c.root, "x")); code == 0 {
		t.Fatalf("a bad name: exit %d\n%s", code, o)
	}
	if o, code := c.run("project", "import", out, "--name", "shop-copy", "--key", "SC"); code != 0 {
		t.Fatalf("import: exit %d\n%s", code, o)
	}
	calls := c.calls()
	for _, want := range []string{"docker exec crew-server crew admin project export shop",
		"docker exec -i crew-server crew admin project import - --name shop-copy --key SC", "./plan/original.md", "./project.md"} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q in\n%s", want, calls)
		}
	}
	if o, code := c.run("project", "import", c.root); code == 0 || !strings.Contains(o, "not an export") {
		t.Fatalf("a folder that is not an export: exit %d\n%s", code, o)
	}
}

// Every folder of the data folder that the server's container mounts is one the host setup makes,
// with its owner: Docker would make a missing one itself, owned by root (design 13.1, 16.11).
func TestTheHostSetupMakesEveryFolderTheServerMounts(t *testing.T) {
	compose, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile("host-setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	mounts := regexp.MustCompile(`- \$\{CREW_DATA_DIR:-/var/lib/crew\}/([a-z]+):`).FindAllStringSubmatch(string(compose), -1)
	if len(mounts) < 4 {
		t.Fatalf("found %d mounts of the data folder in compose.yaml", len(mounts))
	}
	made := regexp.MustCompile(`(?s)install -d -m 0700 -o crew -g crew (.*?)\n[^ ]`).FindStringSubmatch(string(setup))
	if made == nil {
		t.Fatal("host-setup.sh makes no folders as crew's")
	}
	// The release folders and the updater's are root's, made by lines of their own.
	rootOwned := map[string]bool{}
	for _, m := range regexp.MustCompile(`install -d -m 07[15]+ -o root -g root "\$\{DATA_DIR\}/([a-z]+)"`).FindAllStringSubmatch(string(setup), -1) {
		rootOwned[m[1]] = true
	}
	for _, m := range mounts {
		if m[1] == "db" || m[1] == "caddy" {
			continue // the database's and the ingress's, made by their own lines
		}
		if !strings.Contains(made[1], `"${DATA_DIR}/`+m[1]+`"`) && !rootOwned[m[1]] {
			t.Errorf("compose.yaml mounts %s, which host-setup.sh does not make", m[1])
		}
	}
	if !rootOwned["releases"] || !rootOwned["updater"] {
		t.Errorf("the release folders and the updater's are not root's: %v", rootOwned)
	}
}

// pause and resume are passed on to crew admin, in a container of the server's image (7.9).
func TestPauseAndResumeArePassedOnToCrewAdmin(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	for _, verb := range []string{"pause", "resume"} {
		o, code := c.run(verb, "--dry-run")
		if code != 0 || !strings.Contains(o+c.calls(), "crew-admin admin "+verb) {
			t.Fatalf("%s did not reach crew admin: exit %d\n%s\n%s", verb, code, o, c.calls())
		}
	}
}

// okMachine is an updater's machine on which every step works.
type okMachine struct{}

func (okMachine) Load(context.Context, *release.Folder) error { return nil }
func (okMachine) Snapshot(_ context.Context, _, file string) error {
	return os.WriteFile(file, []byte("snapshot"), 0o600)
}
func (okMachine) Scratch(context.Context) error                                 { return nil }
func (okMachine) DropScratch(context.Context) error                             { return nil }
func (okMachine) RestoreScratch(context.Context, *release.Folder, string) error { return nil }
func (okMachine) StartCheck(context.Context, *release.Folder) error             { return nil }
func (okMachine) Smoke(context.Context, *release.Folder) error                  { return nil }
func (okMachine) Selftest(context.Context) error                                { return nil }
func (okMachine) Browser(context.Context, *release.Folder) error                { return nil }
func (okMachine) StopCheck(context.Context) error                               { return nil }
func (okMachine) Health(context.Context, string) error                          { return nil }
func (okMachine) Proof(context.Context, string) error                           { return nil }
func (okMachine) StopServer(context.Context) error                              { return nil }
func (okMachine) StartServer(context.Context, string) error                     { return nil }
func (okMachine) Migrate(context.Context, string) error                         { return nil }
func (okMachine) Restore(context.Context, string, string) error                 { return nil }
func (okMachine) Remove(context.Context, string) error                          { return nil }
func (okMachine) LoadImage(context.Context, string, string) (string, error)     { return "sha256:x", nil }
func (okMachine) Untag(context.Context, string) error                           { return nil }

// releaseFolder writes a release folder of version under data/releases, past the gate when gated.
func releaseFolder(t *testing.T, data, version string, gated bool) string {
	t.Helper()
	dir := filepath.Join(data, "releases", version)
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	imgs := map[string]release.Image{}
	for _, n := range release.Images {
		if err := os.WriteFile(filepath.Join(dir, "images", n+".tar"), []byte(n+version), 0o644); err != nil {
			t.Fatal(err)
		}
		imgs[n] = release.Image{Tag: n + ":" + version, ID: "sha256:" + n}
	}
	if _, err := release.Write(dir, release.Facts{Version: version, Commit: "c", BuiltAt: time.Now(), Schema: 51, MachineSetup: 2, Images: imgs}, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if gated {
		f, _ := release.Read(dir)
		if err := f.WriteGate(release.Gate{Passed: true, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// updaterOn serves an updater on a socket of the kit, as the real one does, with a machine on which
// every step works, for crew-ctl to ask.
func (c *ctl) updaterOn(running string) *updater.Updater {
	c.t.Helper()
	return c.updaterWith(running, okMachine{})
}

// updaterWith is updaterOn with the machine given.
func (c *ctl) updaterWith(running string, m updater.Machine) *updater.Updater {
	c.t.Helper()
	data := filepath.Join(c.root, "data")
	sockDir, err := os.MkdirTemp("", "crewsock")
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "u.sock")
	u := &updater.Updater{Machine: m, Releases: filepath.Join(data, "releases"), Dir: filepath.Join(data, "updater"),
		WatchFor: 30 * time.Millisecond, WatchEvery: 10 * time.Millisecond, HealthFor: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := u.Begin("0.1.0", running); err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(cancel)
	go func() { _ = u.Serve(ctx, sock, os.Getgid()) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.env = append(c.env, "CREW_CTL_UPDATER_SOCK="+sock)
	return u
}

// Row 56: crew-ctl update and rollback ask the updater over its socket, the same way the app does,
// with no server; the steps are printed as they come.
func TestUpdateAndRollbackAskTheUpdaterOverItsSocket(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	data := filepath.Join(c.root, "data")
	releaseFolder(t, data, "0.1.0", true)
	releaseFolder(t, data, "0.2.0", true)
	releaseFolder(t, data, "0.3.0", false)
	u := c.updaterOn("0.1.0")
	// With no --version, the newest release that passed the gate.
	out, code := c.run("update")
	if code != 0 || !strings.Contains(out, "the updater took request 1: 0.2.0") || !strings.Contains(out, "ok Watch:") || !strings.Contains(out, "Crew 0.2.0 runs") {
		t.Fatalf("update: exit %d\n%s", code, out)
	}
	if st, _ := u.State(); st.Running != "0.2.0" || st.Previous != "0.1.0" {
		t.Fatalf("state %+v", st)
	}
	// status names the updater's own version and the one Crew runs, which differ until the server
	// replaces the updater (16.8): this updater is 0.1.0 and never replaces itself.
	out, _ = c.run("status")
	if !strings.Contains(out, "Crew 0.2.0") || !regexp.MustCompile(`updater +is 0\.1\.0; Crew runs 0\.2\.0\n`).MatchString(out) {
		t.Fatalf("status:\n%s", out)
	}
	// A release with no gate is refused, with the updater's reason.
	out, code = c.run("update", "--version", "0.3.0")
	if code == 0 || !strings.Contains(out, "did not pass the gate") {
		t.Fatalf("an ungated release: exit %d\n%s", code, out)
	}
	// rollback goes to the version before.
	out, code = c.run("rollback")
	if code != 0 || !strings.Contains(out, "Crew 0.1.0 runs") {
		t.Fatalf("rollback: exit %d\n%s", code, out)
	}
	// A build by hand, given by install --from on a running Crew, asks for a word first.
	out, code = c.run("install", "--from", filepath.Join(data, "releases", "0.3.0"))
	if code == 0 || !strings.Contains(out, "a build by hand") || !strings.Contains(out, "--confirm install") {
		t.Fatalf("by hand without the word: exit %d\n%s", code, out)
	}
}

// agentsMachine fails the restore of the scratch copy with what the release's crew admin printed:
// code agents wrote, with a terminal's escapes in it.
type agentsMachine struct{ okMachine }

func (agentsMachine) RestoreScratch(context.Context, *release.Folder, string) error {
	return errors.New("crew admin restore: \x1b]52;c;aGk=\x07\x1b[2K\rok Switch: done\u202e\u009b31m\n  ok Switch: forged\tline")
}

// What an updater's step says is shown as text in crew-ctl's terminal, which runs as root: a step's
// detail and a request's error can hold output of code agents wrote, so their control and format
// characters are dropped, and each is one line, so no line of it passes for one of crew-ctl's own
// (security review of the gate's restore, rounds 1 and 2).
func TestFollowShowsWhatAStepSaidAsText(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	data := filepath.Join(c.root, "data")
	releaseFolder(t, data, "0.1.0", true)
	releaseFolder(t, data, "0.2.0", true)
	c.updaterWith("0.1.0", agentsMachine{})
	out, code := c.run("update")
	if code == 0 || !strings.Contains(out, "-- Try the state: the stored state on a copy: crew admin restore: ]52;c;aGk=[2Kok Switch: done31m   ok Switch: forged line") {
		t.Fatalf("update: exit %d\n%q", code, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  ok Switch") {
			t.Errorf("a line passes for one of crew-ctl's own: %q", line)
		}
	}
	for _, bad := range []string{"\x1b", "\x07", "\r", "\u202e", "\u009b"} {
		if strings.Contains(out, bad) {
			t.Errorf("crew-ctl printed %q: %q", bad, out)
		}
	}
}

func TestBuildAndUpdateRefuseWhatTheyCannotDo(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	for _, args := range [][]string{
		{"build"}, {"build", "--version", "latest"}, {"build", "--version", "1.2"},
		{"update", "--version", "latest"}, {"rollback", "--dry-run"},
	} {
		out, code := c.run(args...)
		if code == 0 {
			t.Fatalf("%v went ahead:\n%s", args, out)
		}
	}
	out, _ := c.run("build")
	if !strings.Contains(out, "needs --version") {
		t.Fatal(out)
	}
	// A folder named for another version than its release.json.
	data := filepath.Join(c.root, "data")
	dir := releaseFolder(t, data, "0.4.0", false)
	moved := filepath.Join(c.root, "0.5.0")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	out, code := c.run("install", "--from", moved, "--dry-run")
	if code == 0 || !strings.Contains(out, "must be named so") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// A roll back to a version install built, which has no gate, is root's build by hand.
	releaseFolder(t, data, "0.0.0-dev.abcdef1", false)
	out, code = c.run("rollback", "--version", "0.0.0-dev.abcdef1", "--dry-run")
	if code != 0 || !strings.Contains(out, `"byHand": true`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// The dry run of update says what it would ask, and asks nothing.
	releaseFolder(t, data, "1.6.0", true)
	out, code = c.run("update", "--dry-run")
	if code != 0 || !strings.Contains(out, `+ ask the updater over`) || !strings.Contains(out, `"version": "1.6.0"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// An update goes forward: an older release is a roll back, and says so.
	releaseFolder(t, data, "1.0.0", true)
	out, code = c.run("update", "--version", "1.0.0", "--dry-run")
	if code == 0 || !strings.Contains(out, "is not newer than 1.2.3") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// The gate runs on the commit its release was built from, on an installed Crew, and names what is
// wrong otherwise.
func TestTheGateRefusesWhatItCannotTest(t *testing.T) {
	c := newCtl(t)
	c.inCheckout()
	if out, code := c.run("gate", "0.2.0"); code == 0 || !strings.Contains(out, "install Crew first") {
		t.Fatalf("not installed: %d\n%s", code, out)
	}
	c.install(good)
	data := filepath.Join(c.root, "data")
	releaseFolder(t, data, "0.2.0", false)
	for args, want := range map[string]string{
		"gate":          "gate X.Y.Z",
		"gate latest":   "is not a version",
		"gate 0.9.9":    "no release folder 0.9.9",
		"gate 0.2.0 -n": "unknown flag",
		"gate 0.2.0":    "check out that commit",
	} {
		out, code := c.run(strings.Fields(args)...)
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d\n%s", args, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "releases", "0.2.0", "gate.json")); err == nil {
		t.Fatal("a refused gate wrote gate.json")
	}
}

// A build by hand from a folder elsewhere is kept only as a copy of that very release: a folder of
// the same version that differs is refused, and so is a folder that holds a link (16.10).
func TestABuildByHandIsKeptOnlyWhenItIsWhatWasChecked(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	data := filepath.Join(c.root, "data")
	releaseFolder(t, data, "0.3.0", false)
	elsewhere := filepath.Join(c.root, "brought")
	src := releaseFolder(t, elsewhere, "0.3.0", false) // built at another moment: another release.json
	out, code := c.run("install", "--from", src, "--confirm", "install")
	if code == 0 || !strings.Contains(out, "another release 0.3.0 is on this machine already") {
		t.Fatalf("a differing folder: exit %d\n%s", code, out)
	}
	linked := releaseFolder(t, filepath.Join(c.root, "linked"), "0.4.0", false)
	if err := os.Symlink("/etc/passwd", filepath.Join(linked, "images", "extra.tar")); err != nil {
		t.Fatal(err)
	}
	out, code = c.run("install", "--from", linked, "--confirm", "install")
	if code == 0 || !strings.Contains(out, "holds a link") {
		t.Fatalf("a folder with a link: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(data, "releases", "0.4.0")); err == nil {
		t.Fatal("a refused folder was kept")
	}
}

// A release folder given to install on a fresh machine is loaded image by image, and an image that
// Docker gives another id than release.json names is refused.
func TestLoadingAReleaseChecksEachID(t *testing.T) {
	c := newCtl(t)
	dir := releaseFolder(t, filepath.Join(c.root, "brought"), "0.5.0", false)
	// A docker that loads anything and says each image has the id sha256:other.
	bin := filepath.Join(c.root, "bin")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> "+c.log+"\n"+
		"case \"$1 $2\" in \"image inspect\") echo sha256:other ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source ./crew-ctl && load_release "$0"`, dir)
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "loaded as sha256:other, and release.json names sha256:") {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(c.calls(), "docker load --quiet --input "+dir+"/images/") {
		t.Fatalf("calls:\n%s", c.calls())
	}
	// The tag the refused load gave is taken away again.
	if !strings.Contains(c.calls(), "docker image rm crew-caddy:0.5.0") && !strings.Contains(c.calls(), "docker image rm crew-server:0.5.0") {
		t.Fatalf("the refused load's tag stays:\n%s", c.calls())
	}
}

// The selftest runs in a database of its own, made for it with crew's privileges and removed after
// it, so the running server's pipeline neither sees its project nor works it (16.11).
func TestTheSelftestHasADatabaseOfItsOwn(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	out, code := c.run("selftest", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"DROP DATABASE IF EXISTS crew_selftest WITH (FORCE);", "CREATE DATABASE crew_selftest OWNER crew_migrate;",
		"GRANT CONNECT ON DATABASE crew_selftest TO crew, crew_migrate, crew_read;", "\\connect crew_selftest",
		"run --rm -T -e CREW_DB_NAME=crew_selftest crew-admin admin migrate", "run --rm -T -e CREW_DB_NAME=crew_selftest crew-admin admin selftest",
		`DROP DATABASE IF EXISTS crew_selftest WITH (FORCE)`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "PASSWORD") || strings.Contains(out, "DATABASE crew ") {
		t.Fatalf("the selftest touches roles or the real database:\n%s", out)
	}
}

// The gate reads updates.install_test from the running server's export, where YAML quotes "off" (it
// is a boolean in YAML 1.1), and an export that names no such setting is read as unread, never as
// local.
func TestTheGateReadsTheInstallTestSetting(t *testing.T) {
	for export, want := range map[string]string{
		"updates:\n  install_test: \"off\"\nversion: 1\n": "off",
		"updates:\n  install_test: local\nversion: 1\n":   "local",
		"version: 1\n": "unread",
	} {
		c := newCtl(t)
		if err := os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte("#!/bin/sh\nprintf '%s' '"+export+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && install_test_setting`)
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%q: %q %v, want %s", export, out, err, want)
		}
	}
}

// The install test's note says why it did not run, and names updates.install_test as off only when the
// setting was read so: a gate that failed before reads no setting (review of M16, F4).
func TestTheGatesInstallTestNoteSaysWhyItDidNotRun(t *testing.T) {
	for _, tc := range []struct {
		rc, dry, export, want string
	}{
		{"1", "0", "updates:\n  install_test: \"off\"\n", "skipped|the install test did not run: an earlier step of the gate failed|0"},
		{"0", "1", "updates:\n  install_test: \"off\"\n", "skipped|the install test did not run: a dry run reads no setting|0"},
		{"0", "0", "updates:\n  install_test: \"off\"\n", "skipped|updates.install_test is off|0"},
		{"0", "0", "version: 1\n", "skipped|the install test did not run: updates.install_test could not be read|1"},
	} {
		c := newCtl(t)
		if err := os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> "+c.log+"\nprintf '%s' '"+tc.export+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && DRY="$1" && GATE_STEPS=() && gate_install_test "$0" >/dev/null && echo "${ITEST}|${NOTE}|${#GATE_STEPS[@]}"`, tc.rc, tc.dry)
		cmd.Env = c.env
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != tc.want {
			t.Errorf("rc %s, dry %s, %q: %q %v, want %s", tc.rc, tc.dry, tc.export, out, err, tc.want)
		}
		if tc.rc != "0" || tc.dry == "1" {
			if strings.Contains(c.calls(), "settings export") {
				t.Errorf("rc %s, dry %s: the setting was read:\n%s", tc.rc, tc.dry, c.calls())
			}
		}
	}
}

// crew-ctl gate sends every step it ran to the updater, with its verdict and its detail, bound to the
// release.json it ran on by its hash; the updater writes gate.json (16.2 step 5, since 0.52).
func TestTheGatesRequestHoldsTheSteps(t *testing.T) {
	c := newCtl(t)
	dir := releaseFolder(t, filepath.Join(c.root, "rel"), "0.7.0", false)
	cmd := exec.Command("bash", "-c", `source ./crew-ctl && GATE_STEPS=("make check|1|" "the install test|0|it could not | be read") && gate_body "$0" failed ""`, dir)
	cmd.Env = c.env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var a updater.Ask
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	f, _ := release.Read(dir)
	g := a.Gate
	if a.Version != "0.7.0" || a.AskedBy != "crew-ctl" || g == nil || g.ManifestSHA256 != f.ManifestSHA256 || len(g.Steps) != 2 ||
		g.Steps[0].Name != "make check" || !g.Steps[0].OK || g.Steps[1].OK || g.Steps[1].Detail != "it could not | be read" ||
		g.InstallTest != "failed" || g.InstallTestNote != "" {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "gate.json")); err == nil {
		t.Fatal("crew-ctl wrote gate.json itself")
	}
}

// A release's title, given at its build, heads its notes, which the Versions screen and the Baseline's
// card read; it is one line of at most 120 characters (M15 on dave: commit lines read as titles).
func TestABuildsTitleHeadsItsNotes(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	for title, want := range map[string]string{"a\nb": "one line", "a\u202eb": "direction-changing", "a\u200bb": "invisible", strings.Repeat("x", 121): "at most 120", "   ": "is empty"} {
		out, code := c.run("build", "--version", "1.2.3", "--title", title)
		if code == 0 || !strings.Contains(out, want) {
			t.Fatalf("--title %q: exit %d\n%s", title, code, out)
		}
	}
	// 120 characters of two bytes each are a title; 121 are not, in the C locale sudo leaves (review
	// of 0.50).
	for title, ok := range map[string]bool{strings.Repeat("é", 120): true, strings.Repeat("é", 121): false} {
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && check_title "$1"`, "sh", title)
		cmd.Env = append(c.env, "LC_ALL=C", "LANG=C")
		if out, err := cmd.CombinedOutput(); (err == nil) != ok {
			t.Fatalf("a title of %d characters: %v %s", len([]rune(title)), err, out)
		}
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "The review's second round over the fixes")
	for title, want := range map[string]string{"": "# Crew 0.2.3\n\nThe last commits:\n\n- The review's second round over the fixes (",
		"Evals measure only what ran": "# Crew 0.2.3: Evals measure only what ran\n\nThe last commits:\n\n- The review's second round"} {
		cmd := exec.Command("bash", "-c", `source ./crew-ctl && REPO="$1" TITLE_ARG="$2" release_notes 0.2.3 HEAD ""`, "sh", repo, title)
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), want) {
			t.Fatalf("the notes with the title %q: %v\n%s", title, err, out)
		}
	}
}

// gitRepo is a repository with one commit of files, as a checkout crew-ctl runs from.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for p, body := range files {
		full := filepath.Join(repo, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, "crew-ctl") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "one"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

// crew-ctl build runs deploy/release.sh, the script a release Crew builds runs (16.2, 16.10): on the
// commit exported with git archive, never on the checkout's files as they lie.
func TestBuildRunsReleaseShOnTheExportedCommit(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	ctlBody, _ := os.ReadFile("crew-ctl")
	repo := gitRepo(t, map[string]string{"deploy/crew-ctl": string(ctlBody), "deploy/release.sh": "#!/bin/sh\n",
		"deploy/compose.yaml": "services: {}\n", "versions.lock": "MACHINE_SETUP=2\n" + agyPin("1.4.5", agyURL("1.4.5"), strings.Repeat("ab", 64))})
	cmd := exec.Command("bash", filepath.Join(repo, "deploy", "crew-ctl"), "build", "--version", "1.2.3", "--dry-run")
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tree := filepath.Join(c.root, "data", "releases", ".1.2.3.tree")
	for _, want := range []string{"+ git archive ", "release.sh build --tree " + tree + " --version 1.2.3",
		"+ curl --proto =https --max-redirs 0 " + agyURL("1.4.5") + " ", " --agy ",
		"release.sh write --tree " + tree + " --tree-tar " + tree + ".tar", "--machine-setup 2"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "docker build") {
		t.Errorf("crew-ctl built an image itself:\n%s", out)
	}
	// A commit whose versions.lock names another address for agy is refused, in a dry run too.
	evil := gitRepo(t, map[string]string{"deploy/crew-ctl": string(ctlBody), "deploy/release.sh": "#!/bin/sh\n",
		"deploy/compose.yaml": "services: {}\n", "versions.lock": "MACHINE_SETUP=2\n" +
			agyPin("1.4.5", strings.Replace(agyURL("1.4.5"), "storage.googleapis.com", "evil.example", 1), strings.Repeat("ab", 64))})
	cmd = exec.Command("bash", filepath.Join(evil, "deploy", "crew-ctl"), "build", "--version", "1.2.3", "--dry-run")
	cmd.Env = c.env
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not the address of the vendor's tarball") || strings.Contains(string(out), "+ curl") ||
		strings.Contains(string(out), "release.sh build") {
		t.Fatalf("another address: %v\n%s", err, out)
	}
}

// agyURL is the vendor's address of agy's tarball at a version, and agyPin its lines of versions.lock.
func agyURL(v string) string {
	return "https://storage.googleapis.com/antigravity-public/antigravity-cli/" + v + "-4952523841339392/linux-x64/cli_linux_x64.tar.gz"
}

func agyPin(v, url, sum string) string {
	return "AGY_VERSION=" + v + "\nAGY_URL=" + url + "\nAGY_SHA512=" + sum + "\n"
}

// agyFolder is a folder that holds agy's tarball, as a build is given it.
func agyFolder(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "agy")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "cli_linux_x64.tar.gz"), []byte("agy's tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeCurl is a curl that answers with FAKE_STATUS and FAKE_BODY, its headers to the file of -D, and
// logs its arguments.
func fakeCurl(c *ctl) {
	// A proxy, with FAKE_PROXY, answers the CONNECT with its own 200 first.
	fake := `#!/bin/bash
echo "curl $*" >> ` + c.log + `
while [ $# -gt 0 ]; do
  if [ "$1" = -D ]; then
    { [ -z "${FAKE_PROXY:-}" ] || printf 'HTTP/1.1 200 Connection established\r\n\r\n'
      printf 'HTTP/2 %s\r\ncontent-type: application/x-tar\r\n\r\n' "${FAKE_STATUS}"; } > "$2"
  fi
  shift
done
printf '%s' "${FAKE_BODY}"
`
	os.WriteFile(filepath.Join(c.root, "bin", "curl"), []byte(fake), 0o755)
}

// crew-ctl's pattern of agy's address is the adapter's, which the server's download and the daily look
// use: one shape, the vendor's bucket alone.
func TestCrewCtlsAgyAddressIsTheAdapters(t *testing.T) {
	body, err := os.ReadFile("crew-ctl")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^AGY_TARBALL='([^']+)'$`).FindSubmatch(body)
	if m == nil || string(m[1]) != cli.AgyTarball.String() {
		t.Fatalf("crew-ctl's AGY_TARBALL %q, the adapter's %q", m, cli.AgyTarball.String())
	}
}

// crew-ctl hands the tarball it downloaded to the build on both its paths, crew-ctl build's
// write_release through release.sh --agy, and install --build's build_images as the runtime image's
// context, and removes its folder after, whether the build ran or the download was refused; a refused
// download builds nothing (the review of 77f8ff9..f9d8ede: the real paths were tested by no test).
func TestCrewCtlHandsAgyToBothBuildPaths(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	fakeCurl(c)
	// $0 is not crew-ctl's path, or it runs its main as a command.
	ctlPath, _ := filepath.Abs("crew-ctl")
	body := "agy's tarball"
	sum := sha512.Sum512([]byte(body))
	lock := "MACHINE_SETUP=2\n" + agyPin("1.4.5", agyURL("1.4.5"), hex.EncodeToString(sum[:]))
	// release.sh as the build sees it: what it was given of agy is kept in the release folder.
	rel := t.TempDir()
	os.WriteFile(filepath.Join(rel, "release.sh"), []byte(`#!/bin/bash
echo "release.sh $*" >> `+c.log+`
while [ $# -gt 0 ]; do case "$1" in --out) out="$2"; shift 2 ;; --agy) agy="$2"; shift 2 ;; *) shift ;; esac; done
mkdir -p "${out}"; [ -z "${agy:-}" ] || cat "${agy}/cli_linux_x64.tar.gz" > "${out}/agy-seen"
`), 0o755)
	repo := gitRepo(t, map[string]string{"deploy/compose.yaml": "services: {}\n", "versions.lock": lock})
	for _, served := range []string{body, "another tarball"} {
		tmp := filepath.Join(c.root, "tmp-"+strconv.Itoa(len(served)))
		os.MkdirAll(tmp, 0o755)
		env := append(c.env, "TMPDIR="+tmp, "FAKE_STATUS=200", "FAKE_BODY="+served)
		ok := served == body
		os.Remove(c.log)
		// write_release, as crew-ctl build runs it.
		cmd := exec.Command("bash", "-c", `source "$1" && REPO="$2" && SELF_DIR="$3" && write_release 1.2.`+strconv.Itoa(len(served)), "-", ctlPath, repo, rel)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		seen, _ := os.ReadFile(filepath.Join(c.root, "data", "releases", "1.2."+strconv.Itoa(len(served)), "agy-seen"))
		if ok != (err == nil) || ok != (string(seen) == body) {
			t.Fatalf("write_release, %q served: %v, saw %q\n%s", served, err, seen, out)
		}
		if !ok && strings.Contains(c.calls(), "release.sh build") {
			t.Fatalf("a refused download was built:\n%s", c.calls())
		}
		if ok && !regexp.MustCompile(`release\.sh build .*--agy `+regexp.QuoteMeta(tmp)+`/tmp\.`).MatchString(c.calls()) {
			t.Fatalf("release.sh was not given the download's folder:\n%s", c.calls())
		}
		// build_images, as install --build runs it.
		os.Remove(c.log)
		cmd = exec.Command("bash", "-c", `source "$1" && REPO="$2" && build_images 1.2.3`, "-", ctlPath, repo)
		cmd.Env = env
		out, err = cmd.CombinedOutput()
		calls := c.calls()
		runtime := regexp.MustCompile(`docker build --progress=plain -f ` + regexp.QuoteMeta(repo) + `/image/Dockerfile --build-context agy=(\S+) `).FindStringSubmatch(calls)
		if ok != (err == nil) || ok != (runtime != nil) || (ok && !strings.HasPrefix(runtime[1], tmp+"/tmp.")) {
			t.Fatalf("build_images, %q served: %v, %q\n%s\n%s", served, err, runtime, out, calls)
		}
		if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
			t.Fatalf("%q served: left %v", served, entries)
		}
	}
	// A dry run downloads nothing and removes nothing, whatever the lock, even beside a folder of the
	// name it prints.
	evil := t.TempDir()
	os.WriteFile(filepath.Join(evil, "versions.lock"), []byte(agyPin("1.4.5", "https://evil.example/agy.tar.gz", hex.EncodeToString(sum[:]))), 0o644)
	cwd := t.TempDir()
	os.Mkdir(filepath.Join(cwd, "<a folder of mktemp>"), 0o755)
	cmd := exec.Command("bash", "-c", `source "$1" && DRY=1 && REPO="$2" && build_images 1.2.3`, "-", ctlPath, evil)
	cmd.Env, cmd.Dir = c.env, cwd
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "not the address of the vendor's tarball") {
		t.Fatalf("a dry run of another address: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "<a folder of mktemp>")); err != nil {
		t.Fatal("a dry run removed a folder")
	}
}

// crew-ctl downloads agy's tarball on the machine as the tree's versions.lock pins it (12.8, since
// 0.57): from the vendor's address alone, over https with no redirect, at most AGY_MAX bytes also
// when the answer gives no length, and only a file of the pin's sha512 is kept.
func TestCrewCtlFetchesAgysPinAlone(t *testing.T) {
	c := newCtl(t)
	body := "agy's tarball"
	sum := sha512.Sum512([]byte(body))
	good := hex.EncodeToString(sum[:])
	// A curl that answers with FAKE_STATUS and FAKE_BODY, its headers to the file of -D.
	fakeCurl(c)
	for name, tc := range map[string]struct {
		status, body, lock, max, why string
		curl, proxy                  bool
	}{
		"the pin":                 {"200", body, agyPin("1.4.5", agyURL("1.4.5"), good), "", "", true, false},
		"the pin through a proxy": {"200", body, agyPin("1.4.5", agyURL("1.4.5"), good), "", "", true, true},
		// Behind a proxy the CONNECT's 200 comes first: the vendor's answer is the last (security review
		// of 77f8ff9..f9d8ede).
		"a redirect through a proxy": {"302", "", agyPin("1.4.5", agyURL("1.4.5"), good), "", "answered 302", true, true},
		"another checksum":           {"200", "another tarball", agyPin("1.4.5", agyURL("1.4.5"), good), "", "sha512", true, false},
		"a redirect":                 {"302", "", agyPin("1.4.5", agyURL("1.4.5"), good), "", "answered 302", true, false},
		"past the cap":               {"200", body, agyPin("1.4.5", agyURL("1.4.5"), good), "8", "more than 8 bytes", true, false},
		"another host": {"200", body, agyPin("1.4.5", strings.Replace(agyURL("1.4.5"), "storage.googleapis.com", "evil.example", 1), good), "",
			"not the address of the vendor's tarball", false, false},
		"another version": {"200", body, agyPin("1.4.6", agyURL("1.4.5"), good), "", "not the address of the vendor's tarball", false, false},
		"no checksum":     {"200", body, agyPin("1.4.5", agyURL("1.4.5"), "x"), "", "AGY_SHA512", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			os.Remove(c.log)
			lock := filepath.Join(t.TempDir(), "versions.lock")
			os.WriteFile(lock, []byte(tc.lock), 0o644)
			dir := filepath.Join(t.TempDir(), "agy")
			script := `source ./crew-ctl && fetch_agy "$0" "$1"`
			if tc.max != "" {
				script = `source ./crew-ctl && AGY_MAX=` + tc.max + ` && fetch_agy "$0" "$1"`
			}
			cmd := exec.Command("bash", "-c", script, lock, dir)
			cmd.Env = append(c.env, "FAKE_STATUS="+tc.status, "FAKE_BODY="+tc.body)
			if tc.proxy {
				cmd.Env = append(cmd.Env, "FAKE_PROXY=1")
			}
			out, err := cmd.CombinedOutput()
			calls := c.calls()
			if tc.curl != strings.Contains(calls, "curl ") {
				t.Fatalf("curl ran: %v\n%s", !tc.curl, calls)
			}
			if tc.curl && (!strings.HasPrefix(calls, "curl -q ") || !strings.Contains(calls, "--proto =https --max-redirs 0 ") || !strings.Contains(calls, " "+agyURL("1.4.5")) ||
				strings.Contains(calls, " -L") || strings.Contains(calls, "--location")) {
				t.Fatalf("curl: %s", calls)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "cli_linux_x64.tar.gz"))
			if tc.why == "" {
				if err != nil || string(got) != body {
					t.Fatalf("%v %q\n%s", err, got, out)
				}
				return
			}
			if err == nil || !strings.Contains(string(out), tc.why) || len(got) != 0 || strings.Contains(string(out), "evil") {
				t.Fatalf("%v, kept %q\n%s", err, got, out)
			}
		})
	}
}

// release.sh builds the four images from the tree it is given, with the proxy of a Sources sandbox
// when it has one, saves them and PostgreSQL's as files, records what the runtime image got of agy,
// and writes release.json with the release's own program from the tree's archive.
func TestReleaseShBuildsFromTheTree(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	log := filepath.Join(root, "docker.log")
	fake := `#!/bin/sh
echo "docker $*" >> ` + log + `
case "$1" in
save) while [ $# -gt 0 ]; do [ "$1" = --output ] && echo image > "$2"; shift; done ;;
run) case "$*" in *versions.json*) echo '{"agy": "1.4.0"}' ;; esac ;;
image) echo sha256:` + strings.Repeat("a", 64) + ` ;;
esac
`
	os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755)
	tree := filepath.Join(root, "tree")
	os.MkdirAll(tree, 0o755)
	os.WriteFile(filepath.Join(tree, "versions.lock"), []byte("POSTGRES_IMAGE=postgres:18.6-trixie@sha256:"+strings.Repeat("b", 64)+"\nMACHINE_SETUP=2\n"+
		"WEB_NODE_IMAGE=node:22-trixie-slim@sha256:"+strings.Repeat("c", 64)+"\n"), 0o644)
	browserTree(t, tree)
	out := filepath.Join(root, "out")
	env := append(withoutProxy(os.Environ()), "PATH="+bin+":"+os.Getenv("PATH"), "HTTPS_PROXY=http://10.9.1.254:8091",
		"GOPROXY=http://10.9.1.254:8091/goproxy")
	agy := agyFolder(t, root)
	cmd := exec.Command("bash", "release.sh", "build", "--tree", tree, "--version", "1.2.3", "--out", out, "--agy", agy)
	cmd.Env = env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	calls, _ := os.ReadFile(log)
	// agy's tarball is the runtime image's build context agy, and no other image's (12.8, since 0.57).
	for _, l := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(l, "docker build ") && strings.Contains(l, "/image/Dockerfile") != strings.Contains(l, "--build-context agy="+agy+" ") {
			t.Errorf("the build context agy: %s", l)
		}
		// Each build is given the module proxy of a Sources sandbox (13.6, since 0.61).
		if strings.HasPrefix(l, "docker build ") && !strings.Contains(l, " --build-arg GOPROXY ") {
			t.Errorf("no module proxy: %s", l)
		}
	}
	for _, want := range []string{
		"build --progress=plain -f " + tree + "/deploy/Dockerfile.server --build-arg HTTPS_PROXY --build-arg GOPROXY --build-arg CREW_VERSION=1.2.3 -t crew-server:1.2.3 " + tree,
		"-f " + tree + "/image/Dockerfile", "-f " + tree + "/ingress/Dockerfile", "--target updater",
		"pull --quiet postgres:18.6-trixie@sha256:", "tag postgres@sha256:" + strings.Repeat("b", 64) + " postgres:18.6-trixie",
		"save --output " + out + "/images/postgres.tar postgres:18.6-trixie",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("no %q in\n%s", want, calls)
		}
	}
	for _, f := range []string{"crew-server", "crew-caddy", "crew-runtime", "crew-updater", "postgres"} {
		if _, err := os.Stat(filepath.Join(out, "images", f+".tar")); err != nil {
			t.Error(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "pins.json")); !strings.Contains(string(b), "1.4.0") {
		t.Fatalf("pins.json: %s", b)
	}
	facts := filepath.Join(root, "facts")
	os.WriteFile(facts, []byte(""), 0o644)
	os.WriteFile(filepath.Join(root, "tree.tar"), []byte(""), 0o644)
	cmd = exec.Command("bash", "release.sh", "write", "--tree", tree, "--tree-tar", filepath.Join(root, "tree.tar"), "--version", "1.2.3",
		"--out", out, "--commit", strings.Repeat("c", 40), "--facts", facts, "--machine-setup", "2")
	cmd.Env = env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	calls, _ = os.ReadFile(log)
	if !regexp.MustCompile(`run --rm -i --user 0 --network none .*--entrypoint crew crew-server:1\.2\.3 release write --dir /out/1\.2\.3 .*--tree /tree\.tar --machine-setup 2 --pin agy=1\.4\.0 --pin postgres=18\.6-trixie`).Match(calls) {
		t.Fatalf("release write:\n%s", calls)
	}
	if _, err := os.Stat(filepath.Join(out, "pins.json")); err == nil {
		t.Fatal("pins.json was left in the release folder")
	}
}

// browserTree writes the gate's browser tests into a tree, as a commit of Crew has them.
func browserTree(t *testing.T, tree string) {
	t.Helper()
	for name, body := range map[string]string{"package.json": "{}", "package-lock.json": "{}", "playwright.gate.config.ts": "export default {}\n",
		"gate/screens.spec.ts": "// the screens\n"} {
		p := filepath.Join(tree, "tools", "e2e", "browser", name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A build's first step builds and saves the runtime image alone, from image/Dockerfile, with what it
// got of agy; the second runs the other Dockerfiles and never touches the runtime image (the
// operator's answer of 2026-10-20 to the security review of M16 P3).
func TestReleaseShBuildsTheRuntimeImageInAStepOfItsOwn(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	log := filepath.Join(root, "docker.log")
	fake := `#!/bin/sh
echo "docker $*" >> ` + log + `
case "$1" in
save) while [ $# -gt 0 ]; do [ "$1" = --output ] && echo image > "$2"; shift; done ;;
run) case "$*" in *versions.json*) echo '{"agy": "1.4.0"}' ;; esac ;;
esac
`
	os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755)
	tree := filepath.Join(root, "tree")
	os.MkdirAll(tree, 0o755)
	os.WriteFile(filepath.Join(tree, "versions.lock"), []byte("POSTGRES_IMAGE=postgres:18.6-trixie@sha256:"+strings.Repeat("b", 64)+
		"\nWEB_NODE_IMAGE=node:22-trixie-slim@sha256:"+strings.Repeat("c", 64)+"\n"), 0o644)
	browserTree(t, tree)
	env := append(withoutProxy(os.Environ()), "PATH="+bin+":"+os.Getenv("PATH"), "HTTPS_PROXY=http://10.9.1.254:8091")
	step := func(verb string, more ...string) string {
		os.Remove(log)
		cmd := exec.Command("bash", append([]string{"release.sh", verb, "--tree", tree, "--version", "1.2.3", "--out", filepath.Join(root, verb)}, more...)...)
		cmd.Env = env
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, b)
		}
		calls, _ := os.ReadFile(log)
		return string(calls)
	}
	// The runtime image is built only with agy's tarball given: release.sh downloads none (12.8, since
	// 0.57).
	for _, more := range [][]string{nil, {"--agy", filepath.Join(root, "empty")}} {
		os.MkdirAll(filepath.Join(root, "empty"), 0o755)
		os.Remove(log)
		cmd := exec.Command("bash", append([]string{"release.sh", "build-runtime", "--tree", tree, "--version", "1.2.3", "--out", filepath.Join(root, "x")}, more...)...)
		cmd.Env = env
		b, err := cmd.CombinedOutput()
		calls, _ := os.ReadFile(log)
		if err == nil || !strings.Contains(string(b), "agy's tarball") || strings.Contains(string(calls), "docker build") {
			t.Fatalf("build-runtime %v: %v\n%s\n%s", more, err, b, calls)
		}
	}
	first := step("build-runtime", "--agy", agyFolder(t, root))
	if n := strings.Count(first, "docker build "); n != 1 || !strings.Contains(first, "-f "+tree+"/image/Dockerfile") ||
		!strings.Contains(first, "save --output "+root+"/build-runtime/images/crew-runtime.tar crew-runtime:1.2.3") {
		t.Fatalf("the first step:\n%s", first)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "build-runtime", "pins.json")); !strings.Contains(string(b), "1.4.0") {
		t.Fatalf("pins.json: %s", b)
	}
	second := step("build-rest")
	if strings.Contains(second, "image/Dockerfile") || strings.Contains(second, "crew-runtime") {
		t.Fatalf("the second step touched the runtime image:\n%s", second)
	}
	for _, f := range []string{"crew-server", "crew-caddy", "crew-updater", "postgres"} {
		if _, err := os.Stat(filepath.Join(root, "build-rest", "images", f+".tar")); err != nil {
			t.Error(err)
		}
	}
	// The browser tests' packages are installed in a container from the lock file, with no install
	// script, through the sandbox's proxy, and packed with the tests.
	if !regexp.MustCompile(`docker run --rm --user [0-9]+:[0-9]+ -e HTTPS_PROXY -v \S+/build-rest/browser:/work -w /work -e HOME=/tmp -e npm_config_cache=/tmp/\.npm node:22-trixie-slim@sha256:c+ npm ci --ignore-scripts --no-audit --no-fund`).MatchString(second) {
		t.Fatalf("the browser tests' packages:\n%s", second)
	}
	list, err := exec.Command("tar", "-tf", filepath.Join(root, "build-rest", "browser.tar")).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"./package.json", "./playwright.gate.config.ts", "./gate/screens.spec.ts"} {
		if !strings.Contains(string(list), want+"\n") {
			t.Errorf("browser.tar has no %s:\n%s", want, list)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "build-rest", "browser")); err == nil {
		t.Fatal("the browser tests' folder is left")
	}
}

// Every folder of the data folder that the Compose file mounts is one the host setup makes, owned as
// it must be: a missing one Docker makes as root's, which the server cannot write (review of M16 P3,
// builds/). A new one is a new machine setup.
func TestTheHostSetupMakesEveryMountedDataFolder(t *testing.T) {
	compose, _ := os.ReadFile("compose.yaml")
	setup, _ := os.ReadFile("host-setup.sh")
	made := regexp.MustCompile(`"\$\{DATA_DIR\}/([a-z/]+)"`).FindAllStringSubmatch(string(setup), -1)
	have := map[string]bool{}
	for _, m := range made {
		have[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`- \$\{CREW_DATA_DIR:-/var/lib/crew\}/([a-z/]+):`).FindAllStringSubmatch(string(compose), -1) {
		// The check's database folder is the updater's, made new for each check (16.4).
		if !have[m[1]] && !strings.HasPrefix(m[1], "updater/check/") {
			t.Errorf("compose.yaml mounts %s of the data folder, and the host setup does not make it", m[1])
		}
	}
	if !have["builds"] {
		t.Error("the host setup does not make builds/")
	}
}

// install --upgrade-runtime, which the updater's refusal of a release that needs a newer machine
// setup names, is an install from this checkout: the host setup runs again and records the setup's
// number (16.4; review of M16 P3, round 2: the flag did not exist).
func TestUpgradeRuntimeIsAnInstallFromTheCheckout(t *testing.T) {
	c := newCtl(t)
	out, code := c.run("install", "--upgrade-runtime", "--dry-run", "--env-file", c.answers("a.env", good))
	if strings.Contains(out, "unknown flag") || strings.Contains(out, "needs --build") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out2, code2 := c.run("install", "--upgrade-runtime", "--from", c.root, "--dry-run", "--env-file", c.answers("b.env", good)); code2 == 0 ||
		!strings.Contains(out2, "--upgrade-runtime") {
		t.Fatalf("with --from: exit %d\n%s", code2, out2)
	}
}

// An update replaces the containers, not crew-ctl and its files (16.11, since 0.55): every run of
// crew-ctl compares the files in place with the running release's deploy archive, and when they differ
// says the command for root: this crew-ctl's install --deploy-files. That checks the archive against
// release.json and release.json against gate.json, unpacks only the deploy files, puts them in place,
// records the hash of each, in install.json and for Versions in the releases folder, and changes
// nothing else; it refuses where Crew is not installed or the release does not check, and an archive
// that does not check is offered to no one (reviews of 864fb57..97382bc).
func TestTheDeployFilesAreComparedAndPutInPlace(t *testing.T) {
	c := newCtl(t)
	if out, code := c.run("install", "--deploy-files"); code == 0 || !strings.Contains(out, "not installed") {
		t.Fatalf("on a machine with no Crew: exit %d\n%s", code, out)
	}
	c.install(good)
	// The machine's sbin, which install takes as there.
	if err := os.MkdirAll(filepath.Join(c.root, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The running release, 1.2.3: the deploy archive of this tree, release.json naming it by its
	// hash, and gate.json naming release.json by its.
	repo, _ := filepath.Abs("..")
	rel := filepath.Join(c.root, "data", "releases", "1.2.3")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	var arch bytes.Buffer
	if err := release.DeployArchive(os.DirFS(repo), &arch); err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(rel, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	manifest := []byte(`{"version": "1.2.3", "deploy": {"file": "deploy.tar.gz", "sha256": "` + hash(arch.Bytes()) + `", "bytes": 1}}`)
	write(release.DeployFile, arch.Bytes())
	write("release.json", manifest)
	write("gate.json", []byte(`{"version": "1.2.3", "passed": true, "live": "skipped", "manifestSha256": "`+hash(manifest)+`"}`))
	self, _ := filepath.Abs("crew-ctl")
	behind := "are not those of the running release 1.2.3"
	if out, _ := c.run("status"); !strings.Contains(out, behind) || !strings.Contains(out, self+" install --deploy-files") ||
		!strings.Contains(out, "deploy/compose.yaml first") {
		t.Fatalf("the files in place are older, and status says nothing:\n%s", out)
	}
	// doctor says it too, on stderr, so doctor --json stays JSON on stdout (P5 of M16).
	if out, _ := c.run("doctor"); !strings.Contains(out, behind) {
		t.Fatalf("doctor says nothing of the files in place:\n%s", out)
	}
	cmd := exec.Command("bash", "crew-ctl", "doctor", "--json")
	cmd.Env, cmd.Dir = c.env, c.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Run()
	var doc any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil || !strings.Contains(stderr.String(), behind) {
		t.Fatalf("doctor --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	// A dry run changes nothing: no file in place, no record.
	if out, code := c.run("install", "--deploy-files", "--dry-run"); code != 0 || !strings.Contains(out, "+ install -m 0755") {
		t.Fatalf("a dry run: exit %d\n%s", code, out)
	}
	recordFile := filepath.Join(c.root, "data", "releases", release.DeployRecord)
	inst, _ := os.ReadFile(filepath.Join(c.root, "etc", "crew", "install.json"))
	compose, _ := os.ReadFile(filepath.Join(c.root, "lib", "crew", "compose.yaml"))
	if _, err := os.Stat(filepath.Join(c.root, "sbin", "crew-ctl")); err == nil || strings.Contains(string(inst), "deploy_files") ||
		string(compose) != "name: crew\n" {
		t.Fatalf("a dry run changed the machine: %s", inst)
	}
	if _, err := os.Stat(recordFile); err == nil {
		t.Fatal("a dry run wrote the record")
	}
	before := c.calls()
	if out, code := c.run("install", "--deploy-files"); code != 0 || !strings.Contains(out, "those of 1.2.3 (gated)") {
		t.Fatalf("install --deploy-files: exit %d\n%s", code, out)
	}
	if calls := c.calls(); calls != before {
		t.Fatalf("install --deploy-files did more than copy the files: %s", strings.TrimPrefix(calls, before))
	}
	for target, src := range map[string]string{"sbin/crew-ctl": "deploy/crew-ctl", "lib/crew/compose.yaml": "deploy/compose.yaml",
		"lib/crew/versions.lock": "versions.lock", "lib/crew/firewall/crew-firewall.sh": "deploy/firewall/crew-firewall.sh"} {
		got, _ := os.ReadFile(filepath.Join(c.root, target))
		want, _ := os.ReadFile(filepath.Join(repo, src))
		if len(want) == 0 || string(got) != string(want) {
			t.Fatalf("%s is not %s", target, src)
		}
	}
	var instJSON struct {
		DeployFiles map[string]string `json:"deploy_files"`
		Version     string            `json:"version"`
	}
	b, _ := os.ReadFile(filepath.Join(c.root, "etc", "crew", "install.json"))
	if err := json.Unmarshal(b, &instJSON); err != nil || len(instJSON.DeployFiles["deploy/crew-ctl"]) != 64 || instJSON.Version != "1.2.3" {
		t.Fatalf("install.json: %s %v", b, err)
	}
	var rec struct {
		Files map[string]string `json:"files"`
	}
	b, _ = os.ReadFile(recordFile)
	if err := json.Unmarshal(b, &rec); err != nil || rec.Files["deploy/compose.yaml"] != instJSON.DeployFiles["deploy/compose.yaml"] {
		t.Fatalf("the record for Versions: %s %v", b, err)
	}
	if out, _ := c.run("status"); strings.Contains(out, behind) {
		t.Fatalf("the release's own files in place, and status says they differ:\n%s", out)
	}
	// A file changed in place is named.
	if err := os.WriteFile(filepath.Join(c.root, "lib", "crew", "compose.yaml"), []byte("name: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _ := c.run("status"); !strings.Contains(out, behind) || !strings.Contains(out, "deploy/compose.yaml first") {
		t.Fatalf("a changed file:\n%s", out)
	}
	// An archive that is not the one release.json names is offered to no one, and refused.
	write(release.DeployFile, append(append([]byte{}, arch.Bytes()...), 0))
	if out, _ := c.run("status"); !strings.Contains(out, "does not check") || strings.Contains(out, "install --deploy-files") {
		t.Fatalf("a changed archive:\n%s", out)
	}
	if out, code := c.run("install", "--deploy-files"); code == 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("a changed archive put in place: exit %d\n%s", code, out)
	}
	write(release.DeployFile, arch.Bytes())
	// A gate of another release.json is refused; a release with no gate, by hand, is said so.
	write("gate.json", []byte(`{"version": "1.2.3", "passed": true, "manifestSha256": "`+strings.Repeat("0", 64)+`"}`))
	if out, code := c.run("install", "--deploy-files"); code == 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("a gate of another release: exit %d\n%s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(c.root, "lib", "crew", "compose.yaml")); string(got) != "name: other\n" {
		t.Fatal("a refused install --deploy-files changed a file")
	}
	// Only a gate that passed, of this release.json and its version, whose live part is not pending or
	// failed; with no gate only a release by hand, and only the running one (round 2 of the reviews of
	// 864fb57..97382bc).
	for why, gate := range map[string]string{
		"failed":        `{"version": "1.2.3", "passed": false, "manifestSha256": "` + hash(manifest) + `"}`,
		"pending":       `{"version": "1.2.3", "passed": true, "live": "pending", "manifestSha256": "` + hash(manifest) + `"}`,
		"live failed":   `{"version": "1.2.3", "passed": true, "live": "failed", "manifestSha256": "` + hash(manifest) + `"}`,
		"other version": `{"version": "1.2.4", "passed": true, "manifestSha256": "` + hash(manifest) + `"}`,
	} {
		write("gate.json", []byte(gate))
		if out, code := c.run("install", "--deploy-files"); code == 0 || !strings.Contains(out, "nothing was changed") {
			t.Fatalf("a gate %s: exit %d\n%s", why, code, out)
		}
		if out, _ := c.run("status"); strings.Contains(out, "install --deploy-files") {
			t.Fatalf("a gate %s offered:\n%s", why, out)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(c.root, "lib", "crew", "compose.yaml")); string(got) != "name: other\n" {
		t.Fatal("a refused install --deploy-files changed a file")
	}
	if err := os.Remove(filepath.Join(rel, "gate.json")); err != nil {
		t.Fatal(err)
	}
	if out, code := c.run("install", "--deploy-files"); code == 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("no gate and not by hand: exit %d\n%s", code, out)
	}
	byHand := []byte(`{"version": "1.2.3", "byHand": true, "deploy": {"file": "deploy.tar.gz", "sha256": "` + hash(arch.Bytes()) + `", "bytes": 1}}`)
	write("release.json", byHand)
	if out, code := c.run("install", "--deploy-files"); code != 0 || !strings.Contains(out, "a release by hand") {
		t.Fatalf("the running release by hand: exit %d\n%s", code, out)
	}
	// The record is root's, readable by the server.
	if st, err := os.Stat(recordFile); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("the record's mode: %v %v", st, err)
	}
	// A file in place that is a link is no file of the release, even to the same bytes.
	inPlace := filepath.Join(c.root, "lib", "crew", "compose.yaml")
	if err := os.Remove(inPlace); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "deploy", "compose.yaml"), inPlace); err != nil {
		t.Fatal(err)
	}
	if out, _ := c.run("status"); !strings.Contains(out, "deploy/compose.yaml first") {
		t.Fatalf("a link in place:\n%s", out)
	}
	// A release whose own crew-ctl has no install --deploy-files, as one before 0.55: this crew-ctl
	// puts its files in place, and runs none of them.
	old := filepath.Join(c.root, "old")
	if err := os.MkdirAll(filepath.Join(old, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"versions.lock", "deploy/release.sh", "deploy/host-setup.sh", "deploy/proof.sh", "deploy/compose.yaml", "deploy/db/10-roles.sh",
		"deploy/lib/kata-config.sh", "deploy/firewall/crew-firewall.sh"} {
		body, _ := os.ReadFile(filepath.Join(repo, f))
		os.MkdirAll(filepath.Dir(filepath.Join(old, f)), 0o755)
		os.WriteFile(filepath.Join(old, f), body, 0o644)
	}
	os.WriteFile(filepath.Join(old, "deploy", "crew-ctl"), []byte("#!/bin/sh\necho old crew-ctl ran >&2\nexit 1\n"), 0o755)
	arch.Reset()
	if err := release.DeployArchive(os.DirFS(old), &arch); err != nil {
		t.Fatal(err)
	}
	manifest = []byte(`{"version": "1.2.3", "deploy": {"file": "deploy.tar.gz", "sha256": "` + hash(arch.Bytes()) + `", "bytes": 1}}`)
	write(release.DeployFile, arch.Bytes())
	write("release.json", manifest)
	write("gate.json", []byte(`{"version": "1.2.3", "passed": true, "live": "passed", "manifestSha256": "`+hash(manifest)+`"}`))
	if out, code := c.run("install", "--deploy-files"); code != 0 || strings.Contains(out, "old crew-ctl ran") {
		t.Fatalf("a release with an older crew-ctl: exit %d\n%s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(c.root, "sbin", "crew-ctl")); !strings.Contains(string(got), "old crew-ctl ran") {
		t.Fatal("the older crew-ctl was not put in place")
	}
}

// install --deploy-files of the running release makes install.json name it, where an install of a
// build by hand and an update left another (dave after 0.3.0); another release's files leave it, and
// an install that stopped half way stays unmarked, for doctor to say.
func TestTheRunningReleasesDeployFilesNameItInInstallsRecord(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	if err := os.MkdirAll(filepath.Join(c.root, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.Abs("..")
	var arch bytes.Buffer
	if err := release.DeployArchive(os.DirFS(repo), &arch); err != nil {
		t.Fatal(err)
	}
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	for _, v := range []string{"1.2.3", "1.2.4"} {
		rel := filepath.Join(c.root, "data", "releases", v)
		manifest := []byte(`{"version": "` + v + `", "deploy": {"file": "deploy.tar.gz", "sha256": "` + hash(arch.Bytes()) + `", "bytes": 1}}`)
		for name, body := range map[string][]byte{release.DeployFile: arch.Bytes(), "release.json": manifest,
			"gate.json": []byte(`{"version": "` + v + `", "passed": true, "live": "passed", "manifestSha256": "` + hash(manifest) + `"}`)} {
			if err := os.MkdirAll(rel, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rel, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(c.root, "data", "updater"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.root, "data", "updater", "state.json"), []byte(`{"running": "1.2.3", "previous": "0.0.0-dev.ae698b46d9f1"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	instFile := filepath.Join(c.root, "etc", "crew", "install.json")
	setVersion := func(v string) {
		t.Helper()
		var d map[string]any
		b, _ := os.ReadFile(instFile)
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		if v == "" {
			delete(d, "version")
		} else {
			d["version"] = v
		}
		b, _ = json.Marshal(d)
		if err := os.WriteFile(instFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	version := func() (string, bool) {
		t.Helper()
		var d map[string]any
		b, _ := os.ReadFile(instFile)
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		v, ok := d["version"].(string)
		return v, ok
	}
	setVersion("0.0.0-dev.ae698b46d9f1")
	if out, code := c.run("install", "--deploy-files", "--version", "1.2.4"); code != 0 || !strings.Contains(out, "those of 1.2.4") {
		t.Fatalf("another release's files: exit %d\n%s", code, out)
	}
	if v, _ := version(); v != "0.0.0-dev.ae698b46d9f1" {
		t.Fatalf("another release's files named it in install.json: %q", v)
	}
	if out, code := c.run("install", "--deploy-files", "--dry-run"); code != 0 || !strings.Contains(out, "+ record in "+instFile+": version 1.2.3") {
		t.Fatalf("a dry run does not say it names the running release: exit %d\n%s", code, out)
	}
	if v, _ := version(); v != "0.0.0-dev.ae698b46d9f1" {
		t.Fatalf("a dry run changed install.json: %q", v)
	}
	if out, code := c.run("install", "--deploy-files"); code != 0 || !strings.Contains(out, "those of 1.2.3") {
		t.Fatalf("the running release's files: exit %d\n%s", code, out)
	}
	if v, _ := version(); v != "1.2.3" {
		t.Fatalf("the running release's files left install.json naming %q", v)
	}
	setVersion("")
	if out, code := c.run("install", "--deploy-files"); code != 0 {
		t.Fatalf("an install that stopped half way: exit %d\n%s", code, out)
	}
	if v, ok := version(); ok {
		t.Fatalf("an install that stopped half way was marked finished: %q", v)
	}
}

// The reader of a deploy archive, as root, takes the deploy files' own names only: a name that leads
// out of the deploy folders, a link, or a device is neither compared nor unpacked (security review of
// 864fb57..97382bc).
func TestTheDeployArchiveIsReadByItsNamesOnly(t *testing.T) {
	c := newCtl(t)
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range []struct {
		name string
		typ  byte
	}{{"deploy/compose.yaml", tar.TypeReg}, {"deploy//etc/passwd", tar.TypeReg}, {"deploy/lib/../../../evil", tar.TypeReg},
		{"deploy/lib/x/y.sh", tar.TypeReg}, {"/deploy/proof.sh", tar.TypeReg}, {"deploy/lib/a\x1b[2Jb.sh", tar.TypeReg}, {"deploy/db/a b.sh", tar.TypeReg}, {"deploy/firewall/zero", tar.TypeSymlink}, {"other.txt", tar.TypeReg}} {
		body := []byte("x\n")
		h := &tar.Header{Name: m.name, Mode: 0o644, Typeflag: m.typ}
		if m.typ == tar.TypeSymlink {
			h.Linkname, body = "/dev/zero", nil
		}
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	arch := filepath.Join(dir, "deploy.tar.gz")
	if err := os.WriteFile(arch, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source crew-ctl; deploy_py compare "$1" "$2" "$3"; deploy_py unpack "$1" "$4"`, "_", arch,
		filepath.Join(dir, "lib"), filepath.Join(dir, "sbin"), out)
	cmd.Env = c.env
	got, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(got)) != "deploy/compose.yaml" {
		t.Fatalf("compared %q: %v", got, err)
	}
	var unpacked []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != arch {
			unpacked = append(unpacked, strings.TrimPrefix(p, dir+"/"))
		}
		return nil
	})
	if len(unpacked) != 1 || unpacked[0] != "out/deploy/compose.yaml" {
		t.Fatalf("unpacked %v", unpacked)
	}
}

// Every Python crew-ctl runs as root is isolated: a module in the caller's folder named as one it
// imports is never run (security review of 864fb57..97382bc).
func TestCrewCtlsPythonImportsNothingFromTheCallersFolder(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	plant := t.TempDir()
	marker := filepath.Join(plant, "ran")
	for _, mod := range []string{"hashlib", "json", "tarfile", "tempfile", "shutil", "gzip", "re"} {
		if err := os.WriteFile(filepath.Join(plant, mod+".py"), []byte("open("+strconv.Quote(marker)+", 'w').write('ran')\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	self, _ := filepath.Abs("crew-ctl")
	for _, args := range [][]string{{"status"}, {"edit", "--dry-run", "CREW_TIMEZONE=Europe/Stockholm"}} {
		cmd := exec.Command("bash", append([]string{self}, args...)...)
		cmd.Env, cmd.Dir = c.env, plant
		cmd.CombinedOutput()
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a module of the caller's folder ran")
	}
	// crew-ctl, and every script it runs as root.
	files, _ := filepath.Glob("*.sh")
	more, _ := filepath.Glob("lib/*")
	files = append(append(append(files, more...), "crew-ctl"), "firewall/crew-firewall.sh", "db/10-roles.sh")
	for _, f := range files {
		body, _ := os.ReadFile(f)
		for _, m := range regexp.MustCompile(`python3 (-\S*)`).FindAllSubmatch(body, -1) {
			if string(m[1]) != "-I" {
				t.Fatalf("%s calls python3 %s, without -I", f, m[1])
			}
		}
	}
}

// restart-if-quiet (16.10) asks the server whether the machine may restart, and restarts it only on the
// server's exit 0 to a machine that needs it: no file, a no, the server down, or an install that holds
// the lock is no restart.
func TestRestartIfQuiet(t *testing.T) {
	c := newCtl(t)
	// A docker whose exit is FAKE_RC, and that prints what the server would.
	fake := "#!/bin/sh\necho \"docker $*\" >> " + c.log + "\necho \"${FAKE_OUT:-}\"\nexit ${FAKE_RC:-0}\n"
	if err := os.WriteFile(filepath.Join(c.root, "bin", "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	reset := func() { os.Remove(c.log) }
	if out, code := c.run("restart-if-quiet"); code != 0 || c.calls() != "" {
		t.Fatalf("not installed: exit %d, calls %q\n%s", code, c.calls(), out)
	}
	c.install(good)

	reset()
	out, code := c.run("restart-if-quiet")
	if code != 0 || !strings.Contains(c.calls(), "docker exec crew-server crew admin restart-check --reboot-required=no") || strings.Contains(c.calls(), "systemctl reboot") {
		t.Fatalf("no file: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	req := filepath.Join(c.root, "reboot-required")
	os.WriteFile(req, []byte("*** System restart required ***\n"), 0o644)
	os.WriteFile(req+".pkgs", []byte("linux-image-6.12.112+deb13-amd64\n"), 0o644)
	reset()
	base := c.env
	c.env = append(base, "FAKE_RC=1", "FAKE_OUT=crew: no restart: not quiet: a run sandbox going")
	out, code = c.run("restart-if-quiet")
	if code != 0 || !strings.Contains(c.calls(), "--reboot-required=yes") || strings.Contains(c.calls(), "systemctl reboot") ||
		!strings.Contains(out, "not quiet: a run sandbox going") || !strings.Contains(out, "linux-image-6.12.112+deb13-amd64") {
		t.Fatalf("a no: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	// The server down: Docker's own error, no restart.
	reset()
	c.env = append(base, "FAKE_RC=125", "FAKE_OUT=Error response from daemon: No such container: crew-server")
	if out, code = c.run("restart-if-quiet"); code != 0 || strings.Contains(c.calls(), "systemctl reboot") {
		t.Fatalf("the server down: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	// A restart the server has not said yes to for more than 8 days, a day past its own 7: whatever it
	// answered, or failed to, the unit fails, so it shows (security review of M17's Close). At 7 days
	// it is the server's to decide.
	// The days count from when crew-ctl first saw the restart wanted, not from the file the updates
	// write again with each that asks for one (round 2): that file is new here, the stamp old.
	seen := filepath.Join(c.root, "crew-restart-wanted")
	if _, err := os.Stat(seen); err != nil {
		t.Fatalf("the first sight of a wanted restart is not kept: %v", err)
	}
	for _, age := range []struct {
		days int
		fail bool
	}{{7, false}, {9, true}} {
		at := time.Now().Add(-time.Duration(age.days) * 24 * time.Hour)
		if err := os.Chtimes(seen, at, at); err != nil {
			t.Fatal(err)
		}
		for _, rc := range []string{"1", "125"} {
			reset()
			c.env = append(base, "FAKE_RC="+rc, "FAKE_OUT=crew: no restart: not quiet")
			out, code = c.run("restart-if-quiet")
			if (code != 0) != age.fail || strings.Contains(c.calls(), "systemctl reboot") ||
				strings.Contains(out, "for more than 8 days") != age.fail {
				t.Fatalf("a restart wanted %d days, exit %s from the server: exit %d, calls %q\n%s", age.days, rc, code, c.calls(), out)
			}
		}
	}
	now := time.Now()
	os.Chtimes(seen, now, now)
	// No restart wanted any more: the stamp goes with it, and the next one counts from its own sight.
	os.Rename(req, req+".away")
	reset()
	c.env = append(base, "FAKE_RC=0")
	if out, code = c.run("restart-if-quiet"); code != 0 {
		t.Fatalf("no restart wanted: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("the stamp is kept with no restart wanted: %v", err)
	}
	os.Rename(req+".away", req)

	// Without flock no lock keeps the restart out of an install: the unit fails, and asks nothing.
	nolock := t.TempDir()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.Name() == "flock" {
				continue
			}
			os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(nolock, e.Name()))
		}
	}
	reset()
	c.env = append(base, "FAKE_RC=0", "PATH="+filepath.Join(c.root, "bin")+":"+nolock)
	out, code = c.run("restart-if-quiet")
	if code == 0 || c.calls() != "" || !strings.Contains(out, "flock is missing") {
		t.Fatalf("no flock: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	// An install that runs holds the lock: crew-ctl asks nothing.
	reset()
	c.env = append(base, "FAKE_RC=0")
	// The holder is the sleeping process itself, so killing it lets go of the lock.
	hold := exec.Command("bash", "-c", `exec 8>"$1"; flock 8; exec sleep 30`, "hold", filepath.Join(c.root, "crew-ctl.lock"))
	if err := hold.Start(); err != nil {
		t.Skip("no flock:", err)
	}
	for i := 0; i < 50; i++ {
		if exec.Command("flock", "-n", filepath.Join(c.root, "crew-ctl.lock"), "true").Run() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, code = c.run("restart-if-quiet")
	hold.Process.Kill()
	hold.Wait()
	if code != 0 || c.calls() != "" || !strings.Contains(out, "an install or an uninstall runs") {
		t.Fatalf("the lock held: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	// A lock held for more than two hours is no install that runs: the timer's unit fails, so it shows.
	reset()
	hold = exec.Command("bash", "-c", `exec 8>"$1"; flock 8; touch -d '3 hours ago' "$1"; exec sleep 30`, "hold", filepath.Join(c.root, "crew-ctl.lock"))
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if exec.Command("flock", "-n", filepath.Join(c.root, "crew-ctl.lock"), "true").Run() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	out, code = c.run("restart-if-quiet")
	hold.Process.Kill()
	hold.Wait()
	if code == 0 || c.calls() != "" || !strings.Contains(out, "held for more than two hours") {
		t.Fatalf("a lock held for hours: exit %d, calls %q\n%s", code, c.calls(), out)
	}

	// The server's words are printed without control characters.
	reset()
	c.env = append(base, "FAKE_RC=1", "FAKE_OUT=crew: no restart: \x1b]0;owned\x07 \u009b2J not quiet")
	if out, code = c.run("restart-if-quiet"); code != 0 || strings.ContainsAny(out, "\x1b\x07\u009b") || !strings.Contains(out, "not quiet") {
		t.Fatalf("control characters: exit %d\n%q", code, out)
	}

	// The yes: the server's exit 0, and the machine restarts.
	reset()
	c.env = append(base, "FAKE_RC=0", "FAKE_OUT=the machine restarts for security updates, in the hours 02:00-05:00")
	out, code = c.run("restart-if-quiet")
	if code != 0 || !strings.Contains(c.calls(), "systemctl reboot") {
		t.Fatalf("a yes: exit %d, calls %q\n%s", code, c.calls(), out)
	}
	// A yes without the file is never a restart: the server's answer to "no" is not one.
	os.Remove(req)
	reset()
	if out, code = c.run("restart-if-quiet"); code != 0 || strings.Contains(c.calls(), "systemctl reboot") {
		t.Fatalf("no file, exit 0: exit %d, calls %q\n%s", code, c.calls(), out)
	}
}

// The lock of install, uninstall and the restart timer is root's own (security review of
// 398035b..31089ee): in /run, where only root makes files; made 0600; touched when taken, so a
// lock file left from long ago is not taken for one held for hours; a link in its place is refused.
func TestTheLockIsRootsOwn(t *testing.T) {
	script, err := os.ReadFile("crew-ctl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `CTL_LOCK="${CREW_CTL_LOCK:-/run/crew-ctl.lock}"`) {
		t.Fatal("the lock is not /run/crew-ctl.lock")
	}
	c := newCtl(t)
	c.install(good)
	lock := filepath.Join(c.root, "crew-ctl.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(lock, 0o644)
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	os.Remove(lock)
	if out, code := c.run("restart-if-quiet"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	st, err := os.Stat(lock)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the lock made: %v %v", st, err)
	}
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	c.run("restart-if-quiet")
	if st, _ := os.Stat(lock); time.Since(st.ModTime()) > time.Minute {
		t.Fatalf("the lock was not touched when taken: %s", st.ModTime())
	}
	os.Remove(lock)
	target := filepath.Join(c.root, "target")
	os.WriteFile(target, []byte("keep"), 0o644)
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	os.Remove(c.log)
	if out, code := c.run("restart-if-quiet"); code == 0 || !strings.Contains(out, "is a link") || c.calls() != "" {
		t.Fatalf("a link: exit %d, calls %q\n%s", code, c.calls(), out)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("the link's target: %q", b)
	}
}
