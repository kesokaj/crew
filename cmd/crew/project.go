package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/export"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/github"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/version"
)

// openBoard makes the board and the exporter over the database and the repositories of the data
// folder (6.1). The vault, when there is one, gives the operator's git identity for a new
// repository's first commit, and the GitHub App the token that reads an existing one.
func openBoard(e env, pool *pgxpool.Pool, vault *credstore.Vault, app interface {
	TokenFor(ctx context.Context, holder string, repos []string) (string, error)
	Revoke(ctx context.Context, holder string)
}, log *slog.Logger) (*board.Board, *export.Exporter) {
	g := &gitstore.Store{Dir: filepath.Join(e.DataDir, "canonical"), Quarantine: &gitendpoint.Endpoint{Dir: filepath.Join(e.DataDir, "repos"), Log: log}}
	b := &board.Board{Pool: pool, Git: g, Log: log}
	b.Identity = func(ctx context.Context) (gitstore.Identity, error) {
		if vault == nil {
			return gitstore.Identity{}, errors.New("the key file is missing, so Setup's credentials cannot be read")
		}
		c, err := vault.Get(ctx, credstore.GitIdentity)
		if err != nil {
			return gitstore.Identity{}, err
		}
		var id credstore.Identity
		if err := json.Unmarshal(c.Data, &id); err != nil || id.Name == "" || id.Email == "" {
			return gitstore.Identity{}, errors.New("the git identity is incomplete")
		}
		return gitstore.Identity{Name: id.Name, Email: id.Email}, nil
	}
	b.ReadHeader = func(ctx context.Context, raw string) (string, func(), error) {
		u, err := url.Parse(raw)
		if err != nil || u.Host != "github.com" || app == nil {
			// Another host, or no App: the repository is read as it is served, which works for a
			// public one.
			return "", func() {}, nil
		}
		repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		// One holder for each clone: a token is never shared, and a revoked holder never blocks the
		// next clone of the same repository.
		holder := "project-clone-" + rand.Text()
		tok, err := app.TokenFor(ctx, holder, []string{repo})
		if err != nil {
			return "", nil, fmt.Errorf("a token of the GitHub App for %s: %w", repo, err)
		}
		header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
		return header, func() { app.Revoke(context.WithoutCancel(ctx), holder) }, nil
	}
	// GitHub through the gh login and the SSH key of Setup, which stay with the server (6.6, 12.6).
	if vault != nil {
		// The key of a push is written into a folder of the server's own, in its container; one that
		// a server which stopped left is removed now.
		gh := &github.Client{Vault: vault, Dir: filepath.Join(os.TempDir(), "crew-push")}
		if err := os.MkdirAll(gh.Dir, 0o700); err != nil {
			log.Error("the folder of the pushes' keys", "error", err)
		} else if err := gh.Clean(); err != nil {
			log.Error("removing what pushes left", "error", err)
		}
		b.GitHub, b.Pulls = gh, gh
	}
	return b, &export.Exporter{Pool: pool, Board: b, Git: g, Version: version.Version}
}

// ensureFixtureCanonical gives the fixture project, which runs used before projects were rows, its
// canonical repository beside the quarantine one (6.1).
func ensureFixtureCanonical(ctx context.Context, b *board.Board) error {
	if err := b.Git.Quarantine.Ensure(ctx, runs.FixtureRepo); err != nil {
		return err
	}
	if err := b.Git.Quarantine.Init(ctx, runs.FixtureRepo, fixtureFiles, "The fixture project"); err != nil {
		return err
	}
	return b.Git.Adopt(ctx, runs.FixtureRepo)
}

// adminProject lists, exports, and imports projects (6.2). It runs in the server's container, which
// has the repositories:
//
//	sudo docker exec crew-server crew admin project ls
//	sudo docker exec crew-server crew admin project export NAME > shop.tar
//	sudo docker exec -i crew-server crew admin project import - [--name NAME] [--key KEY] < shop.tar
func adminProject(ctx context.Context, e env, args []string, stdin io.Reader, stdout io.Writer) error {
	usage := usageError("admin project ls | export NAME | import - [--name NAME] [--key KEY]")
	if len(args) == 0 {
		return usage
	}
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	b, x := openBoard(e, pool, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	switch args[0] {
	case "ls":
		ps, err := b.Projects(ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			fmt.Fprintf(stdout, "%-24s %-6s %-9s %s\n", p.Name, p.Key, p.State, p.RepoSource)
		}
		return nil
	case "export":
		if len(args) != 2 {
			return usage
		}
		return x.Write(ctx, args[1], stdout)
	case "import":
		fs := flag.NewFlagSet("import", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "the project's name, when not the export's")
		key := fs.String("key", "", "the project's key, when not the export's")
		if len(args) < 2 || args[1] != "-" || fs.Parse(args[2:]) != nil || fs.NArg() != 0 {
			return usage
		}
		p, err := x.Read(ctx, stdin, export.As{Name: *name, Key: *key}, "crew-ctl")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "imported %s (%s), %s\n", p.Name, p.Key, p.State)
		return nil
	}
	return usage
}

// adminAPI calls one route of the server's API as the operator, as the ingress would (see
// newAPIClient), and prints the answer: the board's routes from the machine, without a browser.
//
//	sudo docker exec crew-server crew admin api GET /api/projects/shop/board
//	sudo docker exec -i crew-server crew admin api POST /api/projects - < body.json
func adminAPI(ctx context.Context, e env, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 2 || len(args) > 3 || !strings.HasPrefix(args[1], "/api/") ||
		!slices.Contains([]string{"GET", "POST", "PUT", "PATCH"}, args[0]) {
		return usageError("admin api GET|POST|PUT|PATCH /api/... [BODY | -]")
	}
	c, err := newAPIClient(e)
	if err != nil {
		return err
	}
	var body io.Reader
	if len(args) == 3 {
		body = strings.NewReader(args[2])
		if args[2] == "-" {
			body = io.LimitReader(stdin, 4<<20)
		}
	}
	req, err := http.NewRequestWithContext(ctx, args[0], c.base+args[1], body)
	if err != nil {
		return err
	}
	req.Header.Set(httpapi.HeaderIngress, c.token)
	req.Header.Set(httpapi.HeaderEmail, c.email)
	c.signedIn(req)
	req.Header.Set("Origin", c.origin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("the server does not answer: %w", err)
	}
	defer resp.Body.Close()
	fmt.Fprintf(stdout, "HTTP %d\n", resp.StatusCode)
	if _, err := io.Copy(stdout, io.LimitReader(resp.Body, 64<<20)); err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	return nil
}
