package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/sandboxes"
	"github.com/kesokaj/crew/internal/setup"
)

// The commands of crew admin that work with the sandboxes and the logins. They need Docker and the
// key file, which only the server's container holds, so they are run there:
//
//	sudo docker exec crew-server crew admin sandbox start KIND [--cli C] [--repo OWNER/NAME]... [--gcloud, a terminal only]
//	sudo docker exec crew-server crew admin sandbox ls | rm ID | creds ID
//	sudo docker exec crew-server crew admin login check CLI [--real]
//	sudo docker exec crew-server crew admin login set CLI FILE
//	sudo docker exec crew-server crew admin login offer CLI FILE
//	sudo docker exec crew-server crew admin probe ghcr OWNER/NAME
//
// They print names and states, never a secret: a login is given by the path of its file.

func adminMachine(ctx context.Context, e env) (*machine, func(), error) {
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return nil, nil, err
	}
	m, err := openMachine(ctx, e, pool, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return m, pool.Close, nil
}

type repos []string

func (r *repos) String() string     { return strings.Join(*r, ",") }
func (r *repos) Set(v string) error { *r = append(*r, v); return nil }

func adminSandbox(ctx context.Context, e env, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError("admin sandbox start KIND [--cli C] [--repo OWNER/NAME]... [--gcloud, a terminal only] | ls | rm ID | creds ID")
	}
	var spec sandboxes.Spec
	if args[0] == "start" {
		s, err := startSpec(args[1:])
		if err != nil {
			return err
		}
		spec = s
	}
	m, done, err := adminMachine(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	switch args[0] {
	case "start":
		row, sb, err := m.sandboxes.Start(ctx, spec)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s kind=%s cli=%s repos=%v gcloud=%v\n", row.ID, sb.Container, row.Kind, row.CLI, row.Repos, row.Gcloud)
		return nil
	case "ls":
		live, err := m.sandboxes.Live(ctx)
		if err != nil {
			return err
		}
		for _, s := range live {
			fmt.Fprintf(stdout, "%s kind=%s cli=%s repos=%v started=%s\n", s.ID, s.Kind, s.CLI, s.Repos, s.Started.Format(time.RFC3339))
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return usageError("admin sandbox rm ID")
		}
		if err := m.sandboxes.Remove(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "removed", args[1])
		return nil
	case "creds":
		if len(args) != 2 {
			return usageError("admin sandbox creds ID")
		}
		code, out, err := m.orch.Exec(ctx, orchestrator.SandboxName(args[1]), orchestrator.ExecSpec{
			Cmd: []string{"crew-run", "creds"}, User: orchestrator.GuestUser, Env: []string{"HOME=/home/" + orchestrator.GuestUser},
		})
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, out)
		if code != 0 {
			return fmt.Errorf("crew-run creds ended with %d", code)
		}
		return nil
	}
	return usageError(fmt.Sprintf("admin sandbox: unknown command %q", args[0]))
}

func adminLogin(ctx context.Context, e env, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return usageError("admin login check CLI [--real] | set CLI FILE | offer CLI FILE")
	}
	a, ok := cli.Get(args[1])
	if !ok {
		return usageError(fmt.Sprintf("admin login: no CLI %q", args[1]))
	}
	m, done, err := adminMachine(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	switch args[0] {
	case "check":
		r, err := m.logins.Check(ctx, a.Name(), len(args) > 2 && args[2] == "--real")
		if err != nil {
			return err
		}
		r.Limits = nil
		return json.NewEncoder(stdout).Encode(r)
	case "set", "offer":
		if len(args) != 3 {
			return usageError("admin login " + args[0] + " CLI FILE")
		}
		b, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		if a.LoginFile() == "" {
			b = []byte(strings.TrimSpace(string(b)))
		}
		if args[0] == "offer" {
			kept, why := m.logins.Offer(ctx, a, b)
			fmt.Fprintf(stdout, "kept=%v %s\n", kept, why)
			return nil
		}
		if err := a.CheckLogin(b); err != nil {
			return err
		}
		account, err := a.Account(ctx, b)
		if err != nil {
			return errors.New("the vendor does not know this login: " + err.Error())
		}
		kind := "value"
		if a.LoginFile() != "" {
			kind = "file"
		}
		if err := m.vault.Put(ctx, credstore.Credential{ID: a.Name(), Kind: kind, Account: account, Data: b}, "crew admin"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stored the login of %s%s\n", a.Name(), map[bool]string{true: " for " + account, false: ""}[account != ""])
		return nil
	}
	return usageError(fmt.Sprintf("admin login: unknown command %q", args[0]))
}

// adminProbe asks GitHub's registry whether the gh login of Setup may push an image to OWNER/NAME
// (section 24, item 32). It starts an upload and leaves it unfinished, so nothing is published. The token
// stays in this process.
func adminProbe(ctx context.Context, e env, args []string, stdout io.Writer) error {
	if len(args) != 2 || args[0] != "ghcr" || !strings.Contains(args[1], "/") {
		return usageError("admin probe ghcr OWNER/NAME")
	}
	m, done, err := adminMachine(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	c, err := m.vault.Get(ctx, credstore.GH)
	if err != nil {
		return fmt.Errorf("the gh login of Setup: %w", err)
	}
	files, err := credstore.ParseBundle(c.Data)
	if err != nil {
		return err
	}
	tok := ""
	for _, f := range files {
		if f.Path == setup.GHHostsFile {
			tok = setup.GHToken(string(f.Data))
		}
	}
	if tok == "" {
		return errors.New("the gh login of Setup has no token")
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:"+url.PathEscape(args[1])+":push,pull", nil)
	req.SetBasicAuth(c.Account, tok)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	var t struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&t)
	resp.Body.Close()
	fmt.Fprintf(stdout, "a registry token for %s with push: %v (HTTP %d)\n", args[1], t.Token != "", resp.StatusCode)
	if t.Token == "" {
		return nil
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://ghcr.io/v2/"+args[1]+"/blobs/uploads/", nil)
	req.Header.Set("Authorization", "Bearer "+t.Token)
	resp, err = hc.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	fmt.Fprintf(stdout, "start an upload: HTTP %d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	// The upload is left unfinished: GitHub's registry cannot cancel one (405), and one left
	// unfinished lapses, with nothing published. Its address is not followed, so the push token goes
	// nowhere but ghcr.io.
	if resp.StatusCode == http.StatusAccepted {
		fmt.Fprintln(stdout, "the upload is left unfinished; it lapses, and nothing is published")
	}
	return nil
}

// startSpec reads crew admin sandbox start's KIND and flags. Only a terminal takes --gcloud: no run and
// no conversation gets gcloud (AGENTS.md safety rule 8, design 0.79), so the command never records one
// that the broker would not give.
func startSpec(args []string) (sandboxes.Spec, error) {
	if len(args) < 1 {
		return sandboxes.Spec{}, usageError("admin sandbox start KIND [--cli C] [--repo OWNER/NAME]... [--gcloud, a terminal only]")
	}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	c := fs.String("cli", "", "the one CLI it runs")
	g := fs.Bool("gcloud", false, "gcloud, for a terminal only")
	var rs repos
	fs.Var(&rs, "repo", "a repository its GitHub token may read")
	if err := fs.Parse(args[1:]); err != nil {
		return sandboxes.Spec{}, usageError(err.Error())
	}
	if *g && args[0] != sandboxes.KindTerminal {
		return sandboxes.Spec{}, usageError("--gcloud: only a terminal may hold gcloud; no run and no conversation gets it (12.6)")
	}
	return sandboxes.Spec{Kind: args[0], CLI: *c, Repos: rs, Gcloud: *g, Minimal: true}, nil
}
