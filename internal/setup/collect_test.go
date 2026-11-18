package setup

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]credstore.Credential
}

func (v *memStore) Get(_ context.Context, id string) (credstore.Credential, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, ok := v.m[id]
	if !ok {
		return c, credstore.ErrNotFound
	}
	return c, nil
}
func (v *memStore) Put(_ context.Context, c credstore.Credential, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[c.ID] = c
	return nil
}
func (v *memStore) List(context.Context) ([]credstore.Info, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []credstore.Info
	for _, c := range v.m {
		out = append(out, credstore.Info{ID: c.ID, Account: c.Account})
	}
	return out, nil
}
func (v *memStore) Delete(_ context.Context, id, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, id)
	return nil
}

// tokeninfo stands in for Google: it knows one access token.
func tokeninfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "tokgood") {
			io.WriteString(w, `{"email":"agy-only@example.com","email_verified":"true"}`)
			return
		}
		http.Error(w, "invalid", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	old := cli.TokenInfoURL
	cli.TokenInfoURL = srv.URL
	t.Cleanup(func() { cli.TokenInfoURL = old })
}

func service() (*Service, *memStore, *[]string) {
	v := &memStore{m: map[string]credstore.Credential{}}
	var collected []string
	return &Service{Vault: v, Who: "admin@example.com",
		Log: slog.New(slog.DiscardHandler), Collected: func(id string) { collected = append(collected, id) }}, v, &collected
}

func write(t *testing.T, home string, files map[string]string) {
	for rel, data := range files {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

var pubLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl ada@example.com"

func TestEachStepCollectsWhatItMade(t *testing.T) {
	tokeninfo(t)
	cases := []struct {
		step    string
		files   map[string]string
		id      string
		account string
		check   func(t *testing.T, c credstore.Credential)
	}{
		{"git", map[string]string{GitConfig: "[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n"}, credstore.GitIdentity, "ada@example.com",
			func(t *testing.T, c credstore.Credential) {
				var id credstore.Identity
				json.Unmarshal(c.Data, &id)
				if id.Name != "Ada Lovelace" {
					t.Fatalf("identity %+v", id)
				}
			}},
		{"ssh", map[string]string{SSHKeyFile: fakeKeyHeader + "\n" + fakeKeyBody + "\n" + fakeKeyFooter + "\n", SSHPubFile: pubLine + "\n"}, credstore.SSHKey, "",
			func(t *testing.T, c credstore.Credential) {
				files, err := credstore.ParseBundle(c.Data)
				if err != nil || len(files) != 2 || files[0].Path != SSHKeyFile {
					t.Fatalf("bundle %v %v", files, err)
				}
			}},
		{"gh", map[string]string{GHHostsFile: "github.com:\n    git_protocol: ssh\n    users:\n        ada:\n            oauth_token: gho_x\n    oauth_token: gho_x\n    user: ada\n"}, credstore.GH, "ada", nil},
		{"gcloud", map[string]string{
			".config/gcloud/credentials.db": "db", ".config/gcloud/application_default_credentials.json": "{}",
			".config/gcloud/active_config": "default", ".config/gcloud/configurations/config_default": "[core]\naccount = dev@example.com\n",
		}, credstore.Gcloud, "dev@example.com", nil},
		{"agy", map[string]string{cli.Agy{}.LoginFile(): `{"auth_method":"consumer","token":{"access_token":"tokgood","refresh_token":"1//r"}}`}, credstore.Agy, "agy-only@example.com", nil},
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			s, v, collected := service()
			home := t.TempDir()
			write(t, home, c.files)
			r := s.Collect(context.Background(), c.step, home)
			if !r.OK {
				t.Fatalf("Collect: %+v", r)
			}
			got, ok := v.m[c.id]
			if !ok || got.Account != c.account {
				t.Fatalf("stored %+v", got)
			}
			if c.check != nil {
				c.check(t, got)
			}
			if len(*collected) != 1 || (*collected)[0] != c.id {
				t.Fatalf("Collected told %v", *collected)
			}
			if strings.Contains(r.Message, "gho_x") || strings.Contains(r.Message, "sk-") {
				t.Fatalf("the result shows a secret: %s", r.Message)
			}
		})
	}
}

func TestCollectRefusesWhatItCannotTrust(t *testing.T) {
	tokeninfo(t)
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("[user]\nname = x\nemail = y\n"), 0o600)
	cases := map[string]struct {
		step  string
		setup func(home string)
	}{
		"a link instead of the file": {"git", func(h string) { os.Symlink(outside, filepath.Join(h, GitConfig)) }},
		"a linked folder": {"agy", func(h string) {
			d := t.TempDir()
			write(t, d, map[string]string{"antigravity-oauth-token": `{"token":{"access_token":"tokgood"}}`})
			os.MkdirAll(filepath.Join(h, ".gemini"), 0o700)
			os.Symlink(d, filepath.Join(h, ".gemini", "antigravity-cli"))
		}},
		"a folder where the file should be": {"git", func(h string) { os.MkdirAll(filepath.Join(h, GitConfig), 0o700) }},
		"a file that is too large":          {"git", func(h string) { write(t, h, map[string]string{GitConfig: strings.Repeat("x", maxFile+1)}) }},
		"an identity without a mail":        {"git", func(h string) { write(t, h, map[string]string{GitConfig: "[user]\nname = Ada\n"}) }},
		"a public key that is the private":  {"ssh", func(h string) { write(t, h, map[string]string{SSHKeyFile: fakeKeyHeader, SSHPubFile: fakeKeyHeader}) }},
		"an agy login Google refuses": {"agy", func(h string) {
			write(t, h, map[string]string{cli.Agy{}.LoginFile(): `{"token":{"access_token":"madeup"}}`})
		}},
		"claude, which Crew no longer runs": {"claude", func(h string) { write(t, h, map[string]string{".crew/setup/claude-login": fakeAPIToken}) }},
		"gh with no login":                  {"gh", func(h string) { write(t, h, map[string]string{GHHostsFile: "github.com:\n    user: ada\n"}) }},
		"a step that does not exist":        {"ANTHROPIC_API_KEY", func(string) {}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, v, _ := service()
			home := t.TempDir()
			c.setup(home)
			if r := s.Collect(context.Background(), c.step, home); r.OK || len(v.m) != 0 {
				t.Fatalf("collected: %+v", r)
			}
		})
	}
}

func TestStatusShowsNoSecret(t *testing.T) {
	s, _, _ := service()
	home := t.TempDir()
	write(t, home, map[string]string{SSHKeyFile: fakeKeyHeader + "\n" + fakeKeyBody + "\n" + fakeKeyFooter + "\n", SSHPubFile: pubLine})
	s.Collect(context.Background(), "ssh", home)
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.PublicKey != pubLine || len(st.Credentials) != len(credstore.Setup) {
		t.Fatalf("status %+v", st)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), fakeKeyBody) {
		t.Fatal("the status holds the private key")
	}
	for _, it := range st.Credentials {
		if it.Set != (it.ID == credstore.SSHKey) {
			t.Fatalf("%s set=%v", it.ID, it.Set)
		}
	}
}

func TestGHToken(t *testing.T) {
	hosts := "github.com:\n    git_protocol: ssh\n    users:\n        ada:\n            oauth_token: other\n    oauth_token: mine\n    user: ada\nexample.com:\n    oauth_token: theirs\n"
	if got := GHToken(hosts); got != "mine" {
		t.Fatalf("GHToken = %q", got)
	}
	if got := GHToken("example.com:\n    oauth_token: theirs\n"); got != "" {
		t.Fatalf("another host's token: %q", got)
	}
}
