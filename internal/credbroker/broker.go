// Package credbroker hands each sandbox the credentials it may have (design 12.6). It listens on
// the sandboxes' own networks only: the second of the server's two doors (20.3).
//
// A request is answered only when it comes to a sandbox's own door, from that sandbox's address,
// with that sandbox's token (sandboxes.Manager.Authorize); the network alone is not enough. What a
// sandbox gets follows from its kind (sandboxes.GrantFor). The SSH key and the gh login are never
// handed out: they stay with the server. ANTHROPIC_API_KEY is never set.
//
//	GET /v1/creds  {"env": {NAME: value}, "files": [{"path", "mode", "data"}]}, data in base64
//	GET /v1/git    {"username", "token"}: the temporary GitHub token, for git over HTTPS
//
// Credentials are copied in, never linked: each request reads the one current copy from the vault,
// so a credential renewed in Setup reaches every sandbox that asks afterwards.
package credbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// Vault is the part of the credential store the broker reads.
type Vault interface {
	Get(ctx context.Context, id string) (credstore.Credential, error)
}

// Auth knows a request's sandbox: sandboxes.Manager.
type Auth interface {
	Authorize(ctx context.Context, local, remote, token string) (sandboxes.Row, error)
}

// GitHubTokens makes a sandbox's temporary GitHub token (12.6). Nil, or an error, and the
// sandbox starts without one.
type GitHubTokens interface {
	TokenFor(ctx context.Context, sandbox string, repos []string) (string, error)
}

// SignedIn reports the CLIs that are signed in, for the project terminal.
type SignedIn func(ctx context.Context) []string

// Broker serves credentials to sandboxes.
type Broker struct {
	Vault    Vault
	Auth     Auth
	GitHub   GitHubTokens
	SignedIn SignedIn
	// NoToken records that GitHub gave a sandbox no token (21); nil records nothing.
	NoToken func(ctx context.Context, row sandboxes.Row, err error)
	Log     *slog.Logger
}

// File is a credential written into a sandbox's home.
type File struct {
	Path string `json:"path"` // relative to the home
	Mode string `json:"mode"`
	Data string `json:"data"` // base64
}

// Creds is the answer to /v1/creds.
type Creds struct {
	Env   map[string]string `json:"env"`
	Files []File            `json:"files"`
	// noToken is why GitHub gave no token. Only the sandbox's own request records it, as Crew also
	// collects a sandbox's credentials to know what to redact (8.3).
	noToken error
}

// Handler returns the broker's routes.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/creds", b.creds)
	mux.HandleFunc("GET /v1/git", b.git)
	return mux
}

func (b *Broker) caller(r *http.Request) (sandboxes.Row, bool) {
	local := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		local = a.String()
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	row, err := b.Auth.Authorize(r.Context(), local, r.RemoteAddr, strings.TrimSpace(tok))
	if err != nil {
		b.Log.Warn("broker: refused", "from", r.RemoteAddr, "at", local)
		return sandboxes.Row{}, false
	}
	return row, true
}

func (b *Broker) creds(w http.ResponseWriter, r *http.Request) {
	row, ok := b.caller(r)
	if !ok {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	c, err := b.Collect(r.Context(), row)
	if err != nil {
		b.Log.Error("broker: reading the credentials", "sandbox", row.ID, "error", err)
		http.Error(w, "the credentials cannot be read", http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(c.Env)+len(c.Files))
	for k := range c.Env {
		names = append(names, k)
	}
	for _, f := range c.Files {
		names = append(names, f.Path)
	}
	slices.Sort(names)
	if c.noToken != nil && b.NoToken != nil {
		b.NoToken(r.Context(), row, c.noToken)
	}
	b.Log.Info("broker: served credentials", "sandbox", row.ID, "kind", row.Kind, "names", names)
	writeJSON(w, c)
}

// Collect is what a sandbox gets, by the table in 12.6. A credential that is not set up is left out.
func (b *Broker) Collect(ctx context.Context, row sandboxes.Row) (Creds, error) {
	var signedIn []string
	if row.Kind == sandboxes.KindTerminal && b.SignedIn != nil {
		signedIn = b.SignedIn(ctx)
	}
	g := sandboxes.GrantFor(row.Kind, row.CLI, signedIn, row.Gcloud)
	c := Creds{Env: map[string]string{}, Files: []File{}}
	get := func(id string) (credstore.Credential, bool, error) {
		cr, err := b.Vault.Get(ctx, id)
		if errors.Is(err, credstore.ErrUnreadable) {
			// The key was lost (21): the sandbox starts without it, and Setup asks for it again.
			b.Log.Warn("broker: a credential does not open with the key file", "credential", id, "sandbox", row.ID)
		}
		if errors.Is(err, credstore.ErrNotFound) {
			return cr, false, nil
		}
		return cr, err == nil, err
	}
	for _, name := range g.CLIs {
		a, ok := cli.Get(name)
		if !ok {
			continue
		}
		cr, found, err := get(name) // the login of a CLI is kept under the CLI's name
		if err != nil {
			return Creds{}, err
		}
		if !found {
			continue
		}
		c.Files = append(c.Files, File{Path: a.LoginFile(), Mode: "0600", Data: base64.StdEncoding.EncodeToString(cr.Data)})
	}
	if g.GitIdentity {
		cr, ok, err := get(credstore.GitIdentity)
		if err != nil {
			return Creds{}, err
		}
		if ok {
			var id credstore.Identity
			if json.Unmarshal(cr.Data, &id) == nil && id.Name != "" && id.Email != "" {
				c.Env["CREW_GIT_NAME"], c.Env["CREW_GIT_EMAIL"] = id.Name, id.Email
			}
		}
	}
	if g.Gcloud {
		cr, ok, err := get(credstore.Gcloud)
		if err != nil {
			return Creds{}, err
		}
		if ok {
			files, err := credstore.ParseBundle(cr.Data)
			if err != nil {
				return Creds{}, err
			}
			for _, f := range files {
				c.Files = append(c.Files, File{Path: f.Path, Mode: f.Mode, Data: base64.StdEncoding.EncodeToString(f.Data)})
			}
		}
	}
	if g.GitHubToken && b.GitHub != nil {
		tok, err := b.GitHub.TokenFor(ctx, row.ID, row.Repos)
		if err != nil {
			// The sandbox starts without one, and reading another private repository fails
			// with a clear error (12.6, 21).
			b.Log.Warn("broker: no GitHub token for the sandbox", "sandbox", row.ID, "error", err)
			c.noToken = err
		} else if tok != "" {
			c.Env["GH_TOKEN"] = tok
		}
	}
	delete(c.Env, "ANTHROPIC_API_KEY") // never, whatever was stored (12.6)
	return c, nil
}

func (b *Broker) git(w http.ResponseWriter, r *http.Request) {
	row, ok := b.caller(r)
	if !ok {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	host := r.URL.Query().Get("host")
	g := sandboxes.GrantFor(row.Kind, row.CLI, nil, row.Gcloud)
	if host != "github.com" || !g.GitHubToken || b.GitHub == nil {
		http.Error(w, "no credential", http.StatusNotFound)
		return
	}
	tok, err := b.GitHub.TokenFor(r.Context(), row.ID, row.Repos)
	if err != nil || tok == "" {
		http.Error(w, "no credential", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"username": "x-access-token", "token": tok})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
