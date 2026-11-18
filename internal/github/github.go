// Package github is the server's own use of GitHub (design 6.6): a new repository through GitHub's
// API with the gh login from Setup, and git over SSH with the key from Setup. Both stay with the
// server: neither is ever written into a sandbox (12.6).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/setup"
)

// KnownHosts are GitHub's SSH host keys, as GitHub publishes them (https://api.github.com/meta,
// read 2026-10-15). They are pinned, never learned on first use, so a push cannot go to a host that
// only claims to be GitHub.
const KnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`

// SSHURL is the SSH address of a repository on GitHub, the one form Crew pushes to.
var SSHURL = regexp.MustCompile(`^git@github\.com:[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}\.git$`)

// RepoName is a repository's name on GitHub, as Crew gives it.
var RepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// Vault is where the server keeps the credentials of Setup.
type Vault interface {
	Get(ctx context.Context, id string) (credstore.Credential, error)
}

// Client is the server's GitHub.
type Client struct {
	Vault Vault
	// API is GitHub's API, https://api.github.com when empty; a test gives its own.
	API  string
	HTTP *http.Client
	// Dir is where the key of one push is written, in a folder of its own that is removed after: a
	// folder of the server's own, which Clean empties when the server starts.
	Dir string
	// KnownHosts replaces GitHub's keys, for a test against a server of its own.
	KnownHosts string
	// SSH is the ssh program, "ssh" when empty.
	SSH string
	// SSHHost and SSHPort replace ssh.github.com and 443, for a test against a server of its own.
	SSHHost string
	SSHPort int
}

// Repo is a repository GitHub created.
type Repo struct {
	FullName string `json:"full_name"`
	SSHURL   string `json:"ssh_url"`
	HTMLURL  string `json:"html_url"`
}

// Error is GitHub's refusal, told to the operator (21: "GitHub refuses to create the repository").
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// ErrNoGH says gh is not signed in in Setup.
var ErrNoGH = &Error{Msg: "gh is not signed in: sign it in in Setup, or create an empty repository on GitHub and give Crew its SSH address"}

func (c *Client) api() string {
	if c.API != "" {
		return strings.TrimRight(c.API, "/")
	}
	return "https://api.github.com"
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// token is the gh login's token, read from its hosts.yml in the bundle Setup keeps (12.6).
func (c *Client) token(ctx context.Context) (string, error) {
	cred, err := c.Vault.Get(ctx, credstore.GH)
	if errors.Is(err, credstore.ErrNotFound) {
		return "", ErrNoGH
	}
	if err != nil {
		return "", err
	}
	files, err := credstore.ParseBundle(cred.Data)
	if err != nil {
		return "", fmt.Errorf("gh's login of Setup does not read: %w", err)
	}
	for _, f := range files {
		if f.Path == setup.GHHostsFile {
			if tok := setup.GHToken(string(f.Data)); tok != "" {
				return tok, nil
			}
		}
	}
	return "", ErrNoGH
}

// CreateRepo creates a private repository under the account gh is signed in as (6.6, R182), with
// nothing in it, and returns it.
func (c *Client) CreateRepo(ctx context.Context, name, description string) (Repo, error) {
	if !RepoName.MatchString(name) {
		return Repo{}, &Error{Msg: "the repository's name: letters, digits, '.', '-', and '_', at most 100"}
	}
	tok, err := c.token(ctx)
	if err != nil {
		return Repo{}, err
	}
	if len(description) > 350 {
		description = description[:350]
	}
	body, _ := json.Marshal(map[string]any{"name": name, "private": true, "description": description,
		"auto_init": false, "has_wiki": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api()+"/user/repos", bytes.NewReader(body))
	if err != nil {
		return Repo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return Repo{}, &Error{Msg: "GitHub cannot be reached: " + strings.ReplaceAll(err.Error(), tok, "…")}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusCreated:
	case resp.StatusCode == http.StatusUnauthorized:
		return Repo{}, &Error{Msg: "GitHub refused gh's login (HTTP 401): sign gh in again in Setup"}
	case resp.StatusCode == http.StatusUnprocessableEntity && bytes.Contains(b, []byte("already exists")):
		return Repo{}, &Error{Msg: fmt.Sprintf("GitHub says a repository named %s exists already on this account", name)}
	default:
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		return Repo{}, &Error{Msg: fmt.Sprintf("GitHub refused to create the repository (HTTP %d): %s", resp.StatusCode, oneLine(e.Message, 200))}
	}
	var r Repo
	if err := json.Unmarshal(b, &r); err != nil || !SSHURL.MatchString(r.SSHURL) {
		return Repo{}, &Error{Msg: "GitHub's answer names no SSH address Crew can push to"}
	}
	return r, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// Clean removes the folders of pushes that a server that stopped left behind, with their keys. The
// server calls it when it starts.
func (c *Client) Clean() error {
	if c.Dir == "" {
		return nil
	}
	left, err := filepath.Glob(filepath.Join(c.Dir, "ssh-*"))
	if err != nil {
		return err
	}
	for _, d := range left {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

// SSHEnv is the environment of one git command that reaches GitHub over SSH with the key from Setup:
// ssh.github.com on port 443, the key in a file of its own, GitHub's pinned host keys under
// HostKeyAlias=github.com, and nothing asked. done removes the key.
func (c *Client) SSHEnv(ctx context.Context) (env []string, done func(), err error) {
	cred, err := c.Vault.Get(ctx, credstore.SSHKey)
	if errors.Is(err, credstore.ErrNotFound) {
		return nil, nil, &Error{Msg: "no SSH key: make it in Setup, and add its public half to the GitHub account"}
	}
	if err != nil {
		return nil, nil, err
	}
	// Setup keeps the key as a bundle with its public half (12.6).
	files, err := credstore.ParseBundle(cred.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("the SSH key of Setup does not read: %w", err)
	}
	var key []byte
	for _, f := range files {
		if f.Path == setup.SSHKeyFile {
			key = f.Data
		}
	}
	if len(bytes.TrimSpace(key)) == 0 {
		return nil, nil, &Error{Msg: "the SSH key of Setup holds no private key: make it again in Setup"}
	}
	dir, err := os.MkdirTemp(c.Dir, "ssh-")
	if err != nil {
		return nil, nil, err
	}
	done = func() { os.RemoveAll(dir) }
	// git runs GIT_SSH_COMMAND through the shell: the folder must hold no character it reads.
	if strings.ContainsAny(dir, " '\"\\$`;&|<>(){}*?!#~\n\t") {
		done()
		return nil, nil, fmt.Errorf("the folder of the key %q has a character the shell reads", dir)
	}
	if !bytes.HasSuffix(key, []byte("\n")) {
		key = append(append([]byte(nil), key...), '\n')
	}
	known := c.KnownHosts
	if known == "" {
		known = KnownHosts
	}
	if err := os.WriteFile(filepath.Join(dir, "key"), key, 0o600); err != nil {
		done()
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(known), 0o600); err != nil {
		done()
		return nil, nil, err
	}
	// ssh refuses to run for a uid with no entry in /etc/passwd, and the server's container runs as
	// the machine's user crew, whose uid differs between machines. nss_wrapper gives the one ssh of
	// this push an entry for the uid it runs as, from files beside the key.
	uid, gid := os.Getuid(), os.Getgid()
	if err := os.WriteFile(filepath.Join(dir, "passwd"), fmt.Appendf(nil, "crew:x:%d:%d:crew:%s:/bin/false\n", uid, gid, dir), 0o600); err != nil {
		done()
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "group"), fmt.Appendf(nil, "crew:x:%d:\n", gid), 0o600); err != nil {
		done()
		return nil, nil, err
	}
	ssh := c.SSH
	if ssh == "" {
		ssh = "ssh"
	}
	host := c.SSHHost
	if host == "" {
		host = "ssh.github.com"
	}
	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " '\"\\$`;&|<>(){}*?!#~\n\t") {
		done()
		return nil, nil, fmt.Errorf("the SSH host %q has a character the shell reads", host)
	}
	port := c.SSHPort
	if port <= 0 {
		port = 443
	}
	// The passwd entry is the ssh's alone, not git's; a connection that stalls ends within a minute.
	cmd := fmt.Sprintf("env LD_PRELOAD=libnss_wrapper.so NSS_WRAPPER_PASSWD=%s NSS_WRAPPER_GROUP=%s "+
		"%s -F /dev/null -o Hostname=%s -p %d -o HostKeyAlias=github.com -i %s -o IdentitiesOnly=yes -o IdentityAgent=none -o UserKnownHostsFile=%s "+
		"-o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=yes -o BatchMode=yes -o ConnectTimeout=20 "+
		"-o ServerAliveInterval=15 -o ServerAliveCountMax=4",
		filepath.Join(dir, "passwd"), filepath.Join(dir, "group"), ssh, host, port, filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts"))
	return []string{"GIT_SSH_COMMAND=" + cmd, "GIT_TERMINAL_PROMPT=0"}, done, nil
}
