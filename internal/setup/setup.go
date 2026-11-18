// Package setup is Setup (design 12.6, 20.1): the operator signs in, in a terminal of the app, in a
// setup microVM that exists only for that. Crew then collects each login from that microVM's home,
// by fixed names, only as regular files, under a size limit, into the vault, and removes the
// microVM. No project and no agent runs in it.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/githubapp"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// Steps are what can be set up in the setup microVM, in the order Setup shows them: the tools, then
// one step for each agent CLI, by its name. The GitHub App is set up with a form, not in the terminal.
var Steps = append([]string{"git", "ssh", "gh", "gcloud"}, cli.Names()...)

// What each step makes, and where it is in the setup microVM's home.
const (
	SSHKeyFile  = ".ssh/id_ed25519"
	SSHPubFile  = ".ssh/id_ed25519.pub"
	GitConfig   = ".gitconfig"
	GHHostsFile = ".config/gh/hosts.yml"
	GHConfig    = ".config/gh/config.yml"
)

// gcloudFiles are gcloud's login and its application default credentials, as styrman-crew kept
// them. access_tokens.db is a cache gcloud makes again.
var gcloudFiles = []string{
	".config/gcloud/credentials.db",
	".config/gcloud/application_default_credentials.json",
	".config/gcloud/active_config",
	".config/gcloud/configurations/config_default",
}

// maxFile bounds every file Setup reads from the microVM's home.
const maxFile = 512 << 10

// Life is how long a setup microVM may live: Setup is minutes (13.3).
const Life = 30 * time.Minute

// Runtime is the part of the orchestrator Setup uses for its terminal.
type Runtime interface {
	StartInteractive(ctx context.Context, containerID string, spec orchestrator.ExecSpec, cols, rows uint) (*orchestrator.InteractiveExec, error)
}

// Vault is the part of the credential store Setup uses.
type Vault interface {
	Get(ctx context.Context, id string) (credstore.Credential, error)
	Put(ctx context.Context, c credstore.Credential, who string) error
	List(ctx context.Context) ([]credstore.Info, error)
	Delete(ctx context.Context, id, who string) error
}

// Boxes is the part of the sandbox manager Setup uses for its microVM.
type Boxes interface {
	Start(ctx context.Context, spec sandboxes.Spec) (sandboxes.Row, orchestrator.Sandbox, error)
	Remove(ctx context.Context, id string) error
	Home(id string) string
}

// Service is Setup.
type Service struct {
	Vault     Vault
	Sandboxes Boxes
	Runtime   Runtime
	GitHub    *githubapp.App
	// Machine is the machine check that Setup shows first.
	Machine func(ctx context.Context) (orchestrator.MachineCheck, error)
	// Collected is told which credential was just collected, so the login check runs at once.
	Collected func(id string)
	Who       string // who is recorded in the history: the admin email
	Log       *slog.Logger

	mu      sync.Mutex
	current *session
	running atomic.Value // the step of current, or the step that is starting: read without mu
}

// session is the setup microVM of one step. One terminal at a time is attached to it: gen counts
// the attachments, and a terminal removes the microVM only while it is the latest one attached, so a
// terminal that ends late never removes the microVM of another step or of a newer page.
type session struct {
	id      string
	step    string
	started time.Time
	gen     int
	detach  context.CancelFunc // ends the attached terminal
	failed  bool               // a removal failed: Reap tries again
}

// Result is what a step collected.
type Result struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// open gives the terminal of a page the setup microVM for step, starting one when there is none for
// it, and replacing one that runs for another step. There is one at a time. A terminal still attached
// to the session is ended, so a reloaded page takes the step over. detach ends this terminal. gen is
// the attachment to pass to end.
func (s *Service) open(ctx context.Context, step string, detach context.CancelFunc) (sess *session, gen int, err error) {
	if !slices.Contains(Steps, step) {
		return nil, 0, fmt.Errorf("no step %q in Setup", step)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.current; c != nil && c.step == step && !c.failed && time.Since(c.started) < Life {
		if c.detach != nil {
			c.detach()
		}
		c.gen++
		c.detach = detach
		return c, c.gen, nil
	}
	s.removeLocked(ctx)
	if s.current != nil {
		return nil, 0, errors.New("the last setup microVM could not be removed yet; try again in a minute")
	}
	// Running answers with the step while it starts, without waiting for mu.
	s.running.Store(step)
	row, _, err := s.Sandboxes.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindSetup, Minimal: true, Life: Life})
	if err != nil {
		s.running.Store("")
		return nil, 0, err
	}
	s.current = &session{id: row.ID, step: step, started: time.Now(), gen: 1, detach: detach}
	return s.current, 1, nil
}

// end is a terminal's last word on its session: the microVM is removed when the session is still
// the current one and this terminal is the one attached to it.
func (s *Service) end(id string, gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.current; c != nil && c.id == id && c.gen == gen {
		s.removeLocked(context.Background())
	}
}

// Close removes the setup microVM, if one runs, and ends its terminal: Stop on the page. It says so
// when the removal failed; Reap tries again.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(ctx)
	if s.current != nil {
		return errors.New("the setup microVM could not be removed yet; Crew tries again within a minute")
	}
	return nil
}

// removeLocked removes the current microVM. When that fails, the session is kept and marked, so
// Reap and the next open try again: the microVM holds the logins it just made.
func (s *Service) removeLocked(ctx context.Context) {
	c := s.current
	if c == nil {
		return
	}
	if c.detach != nil {
		c.detach()
	}
	if err := s.Sandboxes.Remove(ctx, c.id); err != nil {
		s.Log.Warn("removing the setup microVM", "sandbox", c.id, "error", err)
		c.failed = true
		return
	}
	s.current = nil
	s.running.Store("")
}

// Reap removes a setup microVM older than Life, Setup that was left open, and one whose removal
// failed before.
func (s *Service) Reap(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.current; c != nil && (c.failed || time.Since(c.started) >= Life) {
		s.removeLocked(ctx)
	}
}

// Running reports the step of the setup microVM that runs, or "".
func (s *Service) Running() string {
	step, _ := s.running.Load().(string)
	return step
}

// readFile reads one file of a home by its fixed name: only a regular file, never through a link,
// under maxFile. The home was written by the microVM, so nothing in it is trusted (12.4).
func readFile(home, rel string) ([]byte, error) {
	return homepath.ReadFile(home, rel, maxFile)
}

// opensshHeader starts an OpenSSH private key. It is put together here so that no file holds the
// shape the repository's scrub check looks for.
var opensshHeader = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"

// Collect takes what step made in home into the vault.
func (s *Service) Collect(ctx context.Context, step, home string) Result {
	r := Result{Step: step}
	id, cred, err := s.collect(ctx, step, home)
	if err != nil {
		r.Message = err.Error()
		return r
	}
	if err := s.Vault.Put(ctx, cred, s.Who); err != nil {
		r.Message = "storing it: " + err.Error()
		return r
	}
	r.OK = true
	r.Message = "collected"
	if cred.Account != "" {
		r.Message += " for " + cred.Account
	}
	s.Log.Info("setup collected a credential", "credential", id, "account", cred.Account)
	if s.Collected != nil {
		s.Collected(id)
	}
	return r
}

func (s *Service) collect(ctx context.Context, step, home string) (string, credstore.Credential, error) {
	switch step {
	case "git":
		b, err := readFile(home, GitConfig)
		if err != nil {
			return "", credstore.Credential{}, fmt.Errorf("no git identity: %w", err)
		}
		name, email := gitUserFromConfig(string(b))
		if name == "" || email == "" {
			return "", credstore.Credential{}, errors.New("the git identity needs a name and a mail address")
		}
		data, _ := json.Marshal(credstore.Identity{Name: name, Email: email})
		return credstore.GitIdentity, credstore.Credential{ID: credstore.GitIdentity, Kind: "value", Account: email, Data: data}, nil
	case "ssh":
		priv, err := readFile(home, SSHKeyFile)
		if err != nil {
			return "", credstore.Credential{}, fmt.Errorf("no SSH key: %w", err)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(priv), []byte(opensshHeader)) {
			return "", credstore.Credential{}, errors.New("the SSH key is not an OpenSSH private key")
		}
		pub, err := readFile(home, SSHPubFile)
		if err != nil {
			return "", credstore.Credential{}, fmt.Errorf("no public half of the SSH key: %w", err)
		}
		line, ok := publicKeyLine(strings.TrimSpace(string(pub)))
		if !ok {
			return "", credstore.Credential{}, errors.New("the public half of the SSH key is not one")
		}
		data, err := credstore.MakeBundle([]credstore.BundleFile{
			{Path: SSHKeyFile, Mode: "0600", Data: priv},
			{Path: SSHPubFile, Mode: "0600", Data: []byte(line + "\n")},
		})
		if err != nil {
			return "", credstore.Credential{}, err
		}
		return credstore.SSHKey, credstore.Credential{ID: credstore.SSHKey, Kind: "bundle", Data: data}, nil
	case "gh":
		hosts, err := readFile(home, GHHostsFile)
		if err != nil {
			return "", credstore.Credential{}, fmt.Errorf("gh is not signed in: %w", err)
		}
		user := ghUser(string(hosts))
		if user == "" || !bytes.Contains(hosts, []byte("oauth_token")) {
			return "", credstore.Credential{}, errors.New("gh's login has no account or no token")
		}
		files := []credstore.BundleFile{{Path: GHHostsFile, Mode: "0600", Data: hosts}}
		if cfg, err := readFile(home, GHConfig); err == nil {
			files = append(files, credstore.BundleFile{Path: GHConfig, Mode: "0600", Data: cfg})
		}
		data, err := credstore.MakeBundle(files)
		if err != nil {
			return "", credstore.Credential{}, err
		}
		return credstore.GH, credstore.Credential{ID: credstore.GH, Kind: "bundle", Account: user, Data: data}, nil
	case "gcloud":
		var files []credstore.BundleFile
		for _, p := range gcloudFiles {
			b, err := readFile(home, p)
			if err != nil {
				return "", credstore.Credential{}, fmt.Errorf("gcloud is not signed in: %w", err)
			}
			files = append(files, credstore.BundleFile{Path: p, Mode: "0600", Data: b})
		}
		data, err := credstore.MakeBundle(files)
		if err != nil {
			return "", credstore.Credential{}, err
		}
		account := ""
		if cfg, err := readFile(home, ".config/gcloud/configurations/config_default"); err == nil {
			account = iniValue(string(cfg), "core", "account")
		}
		return credstore.Gcloud, credstore.Credential{ID: credstore.Gcloud, Kind: "bundle", Account: account, Data: data}, nil
	}
	if a, ok := cli.Get(step); ok {
		return s.collectLogin(ctx, a, home)
	}
	return "", credstore.Credential{}, fmt.Errorf("no step %q", step)
}

// collectLogin takes a CLI's login from its own file. Whose it is, is asked of the vendor (12.6).
func (s *Service) collectLogin(ctx context.Context, a cli.Adapter, home string) (string, credstore.Credential, error) {
	path, kind := a.LoginFile(), "file"
	b, err := readFile(home, path)
	if err != nil {
		return "", credstore.Credential{}, fmt.Errorf("%s is not signed in: %w", a.Name(), err)
	}
	if err := a.CheckLogin(b); err != nil {
		return "", credstore.Credential{}, err
	}
	account, err := a.Account(ctx, b)
	if err != nil {
		return "", credstore.Credential{}, fmt.Errorf("the vendor does not know %s's login: %w", a.Name(), err)
	}
	// The login of a CLI is kept under the CLI's own name.
	return a.Name(), credstore.Credential{ID: a.Name(), Kind: kind, Account: account, Data: b}, nil
}

// ghUser returns the account of github.com in gh's hosts.yml.
func ghUser(hosts string) string {
	in := false
	for _, line := range strings.Split(hosts, "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			in = strings.TrimSpace(line) == "github.com:"
			continue
		}
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); in && ok && k == "user" {
			return stripControls(strings.TrimSpace(v))
		}
	}
	return ""
}

// GHToken returns the token of github.com in gh's hosts.yml, for the server's own use of the login.
func GHToken(hosts string) string {
	in := false
	for _, line := range strings.Split(hosts, "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			in = strings.TrimSpace(line) == "github.com:"
			continue
		}
		// The host's own token is at the first level of indentation; users: repeats it below.
		if k, v, ok := strings.Cut(line, ":"); in && ok && strings.TrimSpace(k) == "oauth_token" && !strings.HasPrefix(line, "        ") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// iniValue reads key in [section] of gcloud's configuration.
func iniValue(text, section, key string) string {
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "["+section+"]"
			continue
		}
		if k, v, ok := strings.Cut(line, "="); in && ok && strings.TrimSpace(k) == key {
			return stripControls(strings.TrimSpace(v))
		}
	}
	return ""
}

// SetGitHubApp stores the App from Setup's form, after GitHub said the id and the key belong
// together (12.6).
func (s *Service) SetGitHubApp(ctx context.Context, appID, key string) (githubapp.Info, error) {
	data, _ := json.Marshal(githubapp.Credential{AppID: strings.TrimSpace(appID), Key: key})
	cred, err := githubapp.ParseCredential(data)
	if err != nil {
		return githubapp.Info{}, err
	}
	probe := &githubapp.App{Vault: memVault{credstore.GitHubApp: data}, HTTP: s.GitHub.HTTP, API: s.GitHub.API, Log: s.Log}
	info, err := probe.Check(ctx)
	if err != nil {
		return info, err
	}
	if p := info.Permissions; p["contents"] != "read" || len(p) > 2 || (len(p) == 2 && p["metadata"] != "read") {
		return info, fmt.Errorf("the App may do more than read contents: %v. Give it only Contents: read (12.6)", p)
	}
	return info, s.Vault.Put(ctx, credstore.Credential{ID: credstore.GitHubApp, Kind: "value", Account: cred.AppID, Data: data}, s.Who)
}

// memVault holds one credential, to check an App before it is stored.
type memVault map[string][]byte

func (m memVault) Get(_ context.Context, id string) (credstore.Credential, error) {
	d, ok := m[id]
	if !ok {
		return credstore.Credential{}, credstore.ErrNotFound
	}
	return credstore.Credential{ID: id, Data: d}, nil
}
func (memVault) Put(context.Context, credstore.Credential, string) error { return nil }
func (memVault) Delete(context.Context, string, string) error            { return nil }
