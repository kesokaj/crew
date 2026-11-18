package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/setup"
)

type vault map[string][]byte

func (v vault) Get(_ context.Context, id string) (credstore.Credential, error) {
	b, ok := v[id]
	if !ok {
		return credstore.Credential{}, credstore.ErrNotFound
	}
	return credstore.Credential{ID: id, Data: b}, nil
}

const hosts = "github.com:\n    users:\n        op:\n            oauth_token: gho_made_up_user\n    git_protocol: ssh\n    oauth_token: gho_made_up_0123\n    user: op\n"

// ghLogin is gh's login as Setup keeps it: a bundle with hosts.yml and config.yml (setup.go, "gh").
func ghLogin(t *testing.T, hosts string) []byte {
	t.Helper()
	b, err := credstore.MakeBundle([]credstore.BundleFile{{Path: setup.GHHostsFile, Mode: "0600", Data: []byte(hosts)},
		{Path: setup.GHConfig, Mode: "0600", Data: []byte("git_protocol: ssh\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fakeAPI(t *testing.T, status int, body string, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user/repos" || r.Header.Get("Authorization") != "Bearer gho_made_up_0123" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		json.NewDecoder(r.Body).Decode(got)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCrewCreatesAPrivateRepositoryWithTheGHLogin(t *testing.T) {
	var got map[string]any
	srv := fakeAPI(t, 201, `{"full_name":"op/shop","ssh_url":"git@github.com:op/shop.git","html_url":"https://github.com/op/shop"}`, &got)
	c := &Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: srv.URL}
	r, err := c.CreateRepo(context.Background(), "shop", "A shop.")
	if err != nil || r.SSHURL != "git@github.com:op/shop.git" || r.FullName != "op/shop" {
		t.Fatalf("%+v %v", r, err)
	}
	if got["private"] != true || got["name"] != "shop" || got["auto_init"] != false {
		t.Fatalf("the request: %v", got)
	}
}

func TestGitHubsRefusalsAreToldWithoutTheToken(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{422, `{"message":"Repository creation failed.","errors":[{"message":"name already exists on this account"}]}`, "exists already"},
		{403, `{"message":"Resource not accessible by integration"}`, "HTTP 403"},
		{201, `{"full_name":"op/shop","ssh_url":"https://github.com/op/shop.git"}`, "no SSH address"},
	} {
		var got map[string]any
		srv := fakeAPI(t, c.status, c.body, &got)
		_, err := (&Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: srv.URL}).CreateRepo(context.Background(), "shop", "")
		var ge *Error
		if !errors.As(err, &ge) || !strings.Contains(ge.Msg, c.want) || strings.Contains(ge.Msg, "gho_") {
			t.Errorf("HTTP %d: %v", c.status, err)
		}
	}
	// A refused login.
	srv := fakeAPI(t, 201, `{}`, new(map[string]any))
	other := strings.ReplaceAll(hosts, "gho_made_up_0123", "gho_other")
	if _, err := (&Client{Vault: vault{credstore.GH: ghLogin(t, other)}, API: srv.URL}).CreateRepo(context.Background(), "shop", ""); err == nil ||
		!strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "gho_") {
		t.Fatalf("a refused login: %v", err)
	}
	if _, err := (&Client{Vault: vault{credstore.GH: []byte(hosts)}, API: srv.URL}).CreateRepo(context.Background(), "shop", ""); err == nil {
		t.Fatal("a login that is not Setup's bundle was read")
	}
	if _, err := (&Client{Vault: vault{}, API: srv.URL}).CreateRepo(context.Background(), "shop", ""); !errors.Is(err, ErrNoGH) {
		t.Fatalf("no gh: %v", err)
	}
	if _, err := (&Client{Vault: vault{credstore.GH: ghLogin(t, hosts)}, API: srv.URL}).CreateRepo(context.Background(), "../x", ""); err == nil {
		t.Fatal("a name with a slash")
	}
}

func TestTheKeyOfAPushIsInAFileOfItsOwnAndGoesAfter(t *testing.T) {
	dir := t.TempDir()
	// As Setup keeps it: a bundle with the private key and its public half.
	key, err := credstore.MakeBundle([]credstore.BundleFile{
		{Path: setup.SSHKeyFile, Mode: "0600", Data: []byte("-----BEGIN OPENSSH " + "PRIVATE KEY-----\nmade-up\n-----END OPENSSH " + "PRIVATE KEY-----")},
		{Path: setup.SSHPubFile, Mode: "0600", Data: []byte("ssh-ed25519 AAAA made-up\n")}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Vault: vault{credstore.SSHKey: key}, Dir: dir}
	env, done, err := c.SSHEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.TrimPrefix(env[0], "GIT_SSH_COMMAND=")
	for _, want := range []string{"Hostname=ssh.github.com", "-p 443", "HostKeyAlias=github.com",
		"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "BatchMode=yes", "GlobalKnownHostsFile=/dev/null", "-F /dev/null",
		"ServerAliveInterval=15", "ServerAliveCountMax=4", "env LD_PRELOAD=libnss_wrapper.so NSS_WRAPPER_PASSWD="} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the ssh command lacks %s: %s", want, cmd)
		}
	}
	keys, _ := filepath.Glob(filepath.Join(dir, "ssh-*", "key"))
	if len(keys) != 1 {
		t.Fatalf("keys %v", keys)
	}
	fi, _ := os.Stat(keys[0])
	b, _ := os.ReadFile(keys[0])
	known, _ := os.ReadFile(filepath.Join(filepath.Dir(keys[0]), "known_hosts"))
	if fi.Mode().Perm() != 0o600 || !strings.HasSuffix(string(b), "KEY-----\n") || string(known) != KnownHosts {
		t.Fatalf("mode %v, key ends %q, known hosts %q", fi.Mode(), b[len(b)-6:], known)
	}
	// ssh gets an entry in passwd for the uid it runs as, whatever uid crew has on the machine.
	pw, _ := os.ReadFile(filepath.Join(filepath.Dir(keys[0]), "passwd"))
	// The preload is the ssh's alone, in its command, not git's.
	if want := fmt.Sprintf("crew:x:%d:%d:", os.Getuid(), os.Getgid()); !strings.HasPrefix(string(pw), want) ||
		!strings.Contains(cmd, "NSS_WRAPPER_PASSWD="+filepath.Join(filepath.Dir(keys[0]), "passwd")) ||
		slices.ContainsFunc(env, func(v string) bool { return strings.HasPrefix(v, "LD_PRELOAD=") }) {
		t.Fatalf("passwd %q, env %v", pw, env)
	}
	done()
	if left, _ := filepath.Glob(filepath.Join(dir, "ssh-*")); len(left) != 0 {
		t.Fatalf("the key stayed: %v", left)
	}
	if _, _, err := (&Client{Vault: vault{}, Dir: dir}).SSHEnv(context.Background()); err == nil {
		t.Fatal("a push with no key")
	}
	for _, badHost := range []string{"-oProxyCommand=x", "ssh.github.com;id", "ssh.github.com evil"} {
		if _, _, err := (&Client{Vault: vault{credstore.SSHKey: key}, Dir: dir, SSHHost: badHost}).SSHEnv(context.Background()); err == nil {
			t.Fatalf("bad SSHHost %q was taken", badHost)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, "ssh-*")); len(left) != 0 {
			t.Fatalf("the key stayed after bad SSHHost %q: %v", badHost, left)
		}
	}
}

func TestGitReachesSSHGitHubOnPort443WithGitHubsHostKeys(t *testing.T) {
	dir := t.TempDir()
	run := func(work string, extraEnv []string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), extraEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	hostKey := filepath.Join(dir, "host_ed25519")
	wrongKey := filepath.Join(dir, "wrong_ed25519")
	clientKey := filepath.Join(dir, "client_ed25519")
	for _, k := range []string{hostKey, wrongKey, clientKey} {
		run(dir, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", k)
	}
	hostPub, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	wrongPub, err := os.ReadFile(wrongKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, err := os.ReadFile(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := credstore.MakeBundle([]credstore.BundleFile{
		{Path: setup.SSHKeyFile, Mode: "0600", Data: clientPriv},
		{Path: setup.SSHPubFile, Mode: "0600", Data: clientPub},
	})
	if err != nil {
		t.Fatal(err)
	}

	bare := filepath.Join(dir, "op", "shop.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	run(dir, nil, "git", "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(seed, nil, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, nil, "git", "add", "README.md")
	run(seed, nil, "git", "-c", "user.name=op", "-c", "user.email=op@example.com", "commit", "-q", "-m", "one")
	seedHead := run(seed, nil, "git", "rev-parse", "HEAD")
	run(seed, nil, "git", "push", "-q", bare, "main:refs/heads/main")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, clientPub, 0o600); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	group := filepath.Join(dir, "group")
	uid, gid := os.Getuid(), os.Getgid()
	if err := os.WriteFile(passwd, fmt.Appendf(nil, "git:x:%d:%d:git:%s:/bin/sh\n", uid, gid, dir), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(group, fmt.Appendf(nil, "git:x:%d:\n", gid), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "sshd.pid")
	cfg := filepath.Join(dir, "sshd_config")
	cfgBody := fmt.Sprintf("ListenAddress 127.0.0.1:%d\nPidFile %s\nHostKey %s\nAuthorizedKeysFile %s\n"+
		"PubkeyAuthentication yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes no\n",
		port, pidFile, hostKey, authKeys)
	if err := os.WriteFile(cfg, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	sshd := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", cfg)
	sshd.Env = append(os.Environ(), "LD_PRELOAD=libnss_wrapper.so", "NSS_WRAPPER_PASSWD="+passwd, "NSS_WRAPPER_GROUP="+group)
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sshd.Process.Kill()
		_ = sshd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sshd did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	c := &Client{
		Vault:      vault{credstore.SSHKey: bundle},
		Dir:        t.TempDir(),
		KnownHosts: "github.com " + string(hostPub),
		SSHHost:    "127.0.0.1",
		SSHPort:    port,
	}
	env, done, err := c.SSHEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(work, nil, "git", "init", "-q", "-b", "main")
	run(work, env, "git", "fetch", "-q", "git@github.com:op/shop.git", "+refs/heads/main:refs/remotes/origin/main")
	if got := run(work, nil, "git", "rev-parse", "refs/remotes/origin/main"); got != seedHead {
		t.Fatalf("fetched %s, want %s", got, seedHead)
	}
	run(work, nil, "git", "checkout", "-q", "-B", "main", "refs/remotes/origin/main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, nil, "git", "add", "README.md")
	run(work, nil, "git", "-c", "user.name=op", "-c", "user.email=op@example.com", "commit", "-q", "-m", "two")
	pushedHead := run(work, nil, "git", "rev-parse", "HEAD")
	run(work, env, "git", "push", "-q", "git@github.com:op/shop.git", "main:refs/heads/main")
	if got := run(bare, nil, "git", "rev-parse", "refs/heads/main"); got != pushedHead {
		t.Fatalf("remote main %s, want %s", got, pushedHead)
	}

	bad := &Client{
		Vault:      vault{credstore.SSHKey: bundle},
		Dir:        t.TempDir(),
		KnownHosts: "github.com " + string(wrongPub),
		SSHHost:    "127.0.0.1",
		SSHPort:    port,
	}
	badEnv, badDone, err := bad.SSHEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer badDone()
	badCmd := exec.Command("git", "fetch", "-q", "git@github.com:op/shop.git", "+refs/heads/main:refs/remotes/origin/main")
	badCmd.Dir = work
	badCmd.Env = append(os.Environ(), badEnv...)
	if out, err := badCmd.CombinedOutput(); err == nil {
		t.Fatalf("fetch with wrong host key succeeded: %s", out)
	}
}

func TestOnlyGitHubsSSHAddressIsAnAddress(t *testing.T) {
	for _, u := range []string{"git@github.com:op/shop.git", "git@github.com:Op-1/shop.site.git"} {
		if !SSHURL.MatchString(u) {
			t.Errorf("%s refused", u)
		}
	}
	for _, u := range []string{"https://github.com/op/shop.git", "git@github.com:op/shop", "git@github.com:op/../x.git x", "ext::sh -c id",
		"git@github.com.evil:op/shop.git", "-oProxyCommand=x"} {
		if SSHURL.MatchString(u) {
			t.Errorf("%s taken", u)
		}
	}
}

// The folders that a server which stopped left, with their keys, go when the server starts.
func TestWhatPushesLeftIsRemoved(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ssh-123"), 0o700)
	os.WriteFile(filepath.Join(dir, "ssh-123", "key"), []byte("made-up"), 0o600)
	os.WriteFile(filepath.Join(dir, "other"), []byte("kept"), 0o600)
	if err := (&Client{Dir: dir}).Clean(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ssh-123")); err == nil {
		t.Fatal("a key that a push left stayed")
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); err != nil {
		t.Fatal("Clean removed what is not a push's")
	}
}
