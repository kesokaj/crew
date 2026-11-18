package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitendpoint"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/httpapi"
	"github.com/kesokaj/crew/internal/orchestrator"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The commands of crew admin for runs (design 12.4, 16.11). A run is followed by the server, so these
// ask the server in the same container, through its own API, as the ingress would: with the secret
// they share, for the admin email, from Crew's own address. Only someone who may run a command in the
// server's container can do that, which is the operator on the machine.
//
//	sudo docker exec crew-server crew admin run start --cli C --model M [--effort E] [--title T]
//	     (--prompt TEXT | --prompt-file FILE) [--timeout 10m] [--max-turns N] [--no-tools]
//	     [--without-login] [--minimal]
//	sudo docker exec crew-server crew admin run ls | show ID | events ID [--after N] [--raw]
//	sudo docker exec crew-server crew admin run stop ID | finish ID
//	sudo docker exec crew-server crew admin run continue SUBTASK [--text T] [--way same|files|rebuilt]
//	     [--cli C] [--model M] [--effort E]
//	sudo docker exec crew-server crew admin run wait ID STATE... [--timeout 10m]
//	sudo docker exec crew-server crew admin repo seed NAME URL
//	sudo docker exec crew-server crew admin proof event

type apiClient struct {
	base, token, email, origin string
	hc                         *http.Client
}

func newAPIClient(e env) (*apiClient, error) {
	tok, err := os.ReadFile(e.IngressTokenFile)
	if err != nil {
		return nil, fmt.Errorf("the secret shared with the ingress (run this in the server's container): %w", err)
	}
	port := e.Listen
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	return &apiClient{base: "http://127.0.0.1:" + port, token: strings.TrimSpace(string(tok)), email: e.AdminEmail,
		origin: "https://" + e.Hostname, hc: &http.Client{Timeout: 2 * time.Minute}}, nil
}

// signedIn gives a request of crew admin the time of a sign-in, now, and no device: root on the
// machine, which runs it through Docker, holds the key file and the database already, so a fresh
// sign-in would add nothing here (20.3, "Two doors").
func (c *apiClient) signedIn(req *http.Request) {
	req.Header.Set(httpapi.HeaderAuthTime, strconv.FormatInt(time.Now().Unix(), 10))
}

func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(b, &e)
		if e.Error.Message == "" {
			e.Error.Message = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("%s: %s", resp.Status, e.Error.Message)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func printRun(w io.Writer, v runs.RunView) {
	reason := v.StopReason
	if v.Stopping != "" {
		reason = "stopping: " + v.Stopping
	}
	fmt.Fprintf(w, "%s subtask=%d #%d %s state=%s %s cli=%s model=%s way=%s turn=%d events=%d steps=%d tokens=%d/%d sandbox=%s wait=%s\n",
		v.ID, v.Subtask, v.Number, quoteIf(v.Title), v.State, reason, v.CLI, v.Model, v.Way, v.Turn, v.Events, v.ToolSteps,
		v.TokensIn, v.TokensOut, v.Sandbox, v.WaitFor)
}

func quoteIf(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return fmt.Sprintf("%q", s)
}

func adminRun(ctx context.Context, e env, args []string, stdout io.Writer) error {
	usage := "admin run start|ls|show|events|stop|finish|continue|wait"
	if len(args) == 0 {
		return usageError(usage)
	}
	c, err := newAPIClient(e)
	if err != nil {
		return err
	}
	switch args[0] {
	case "start":
		fs := flag.NewFlagSet("start", flag.ContinueOnError)
		cliName := fs.String("cli", "", "the agent CLI")
		model := fs.String("model", "", "the model, as the CLI takes it")
		effort := fs.String("effort", "", "the effort, where the CLI takes one")
		title := fs.String("title", "", "the subtask's title")
		prompt := fs.String("prompt", "", "the prompt")
		promptFile := fs.String("prompt-file", "", "a file with the prompt")
		timeout := fs.Duration("timeout", time.Hour, "the run's time limit")
		maxTurns := fs.Int("max-turns", 200, "the run's limit of tool steps")
		noTools := fs.Bool("no-tools", false, "a call with no tools")
		withoutLogin := fs.Bool("without-login", false, "start without the CLI's login, to show a stop for a sign-in")
		minimal := fs.Bool("minimal", false, "no Docker inside the microVM")
		if err := fs.Parse(args[1:]); err != nil {
			return usageError(err.Error())
		}
		text := *prompt
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				return err
			}
			text = string(b)
		}
		var out struct{ ID string }
		if err := c.do(ctx, http.MethodPost, "/api/runs", map[string]any{"cli": *cliName, "model": *model, "effort": *effort,
			"title": *title, "prompt": text, "timeoutSeconds": int(timeout.Seconds()), "maxTurns": *maxTurns, "noTools": *noTools,
			"withoutLogin": *withoutLogin, "minimal": *minimal}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.ID)
		return nil
	case "ls":
		var l runs.List
		if err := c.do(ctx, http.MethodGet, "/api/runs", nil, &l); err != nil {
			return err
		}
		for _, group := range []struct {
			name string
			list []runs.RunView
		}{{"running", l.Running}, {"waiting", l.Waiting}, {"finished", l.Finished}} {
			fmt.Fprintf(stdout, "%s: %d\n", group.name, len(group.list))
			for _, v := range group.list {
				printRun(stdout, v)
			}
		}
		return nil
	case "show":
		if len(args) != 2 {
			return usageError("admin run show ID")
		}
		var det runs.Detail
		if err := c.do(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(args[1]), nil, &det); err != nil {
			return err
		}
		printRun(stdout, det.RunView)
		if det.StopDetail != "" {
			fmt.Fprintf(stdout, "stop: %s\n", det.StopDetail)
		}
		fmt.Fprintf(stdout, "conversation %s at the CLI %s, branch %s, base %s, wip %s, prompt sha256 %s\n",
			det.Conversation, det.CLIConversation, det.Branch, det.Base, det.WIP, det.PromptSHA256)
		for _, t := range det.Turns {
			text := strings.ReplaceAll(t.Text, "\n", " ")
			if len(text) > 160 {
				text = text[:160] + "…"
			}
			fmt.Fprintf(stdout, "  turn %d %s%s: %s %v\n", t.Number, t.Speaker, map[bool]string{true: " (rebuilt)"}[t.Rebuilt], text, t.ToolCalls)
		}
		for _, f := range det.CLIFiles {
			fmt.Fprintf(stdout, "  CLI file %s, %d bytes stored, kept until %s\n", f.Path, f.Bytes, f.KeepUntil.Format(time.RFC3339))
		}
		if len(det.Warnings) > 2 {
			fmt.Fprintf(stdout, "warnings: %s\n", det.Warnings)
		}
		return nil
	case "events":
		fs := flag.NewFlagSet("events", flag.ContinueOnError)
		after := fs.Int64("after", 0, "only the events after this seq")
		raw := fs.Bool("raw", false, "with the line the CLI printed")
		if len(args) < 2 {
			return usageError("admin run events ID [--after N] [--raw]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return usageError(err.Error())
		}
		var out struct{ Events []runs.Event }
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/runs/%s/events?after=%d&raw=%v&limit=1000", url.PathEscape(args[1]), *after, *raw), nil, &out); err != nil {
			return err
		}
		for _, ev := range out.Events {
			fmt.Fprintf(stdout, "%d %s %s %s\n", ev.Seq, ev.At.Format(time.RFC3339), ev.Type, ev.Payload)
			if *raw && ev.Raw != "" {
				fmt.Fprintf(stdout, "    %s\n", ev.Raw)
			}
		}
		return nil
	case "stop", "finish":
		if len(args) != 2 {
			return usageError("admin run " + args[0] + " ID")
		}
		if err := c.do(ctx, http.MethodPost, "/api/runs/"+url.PathEscape(args[1])+"/"+args[0], map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: asked\n", args[0])
		return nil
	case "continue":
		fs := flag.NewFlagSet("continue", flag.ContinueOnError)
		text := fs.String("text", "", "the next turn's text; empty is Crew's message about the stop")
		way := fs.String("way", "", "same, files, or rebuilt; empty takes the first that is possible")
		cliName := fs.String("cli", "", "another CLI")
		model := fs.String("model", "", "another model")
		effort := fs.String("effort", "", "another effort")
		if len(args) < 2 {
			return usageError("admin run continue SUBTASK [--text T] [--way same|files|rebuilt] [--cli C] [--model M]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return usageError(err.Error())
		}
		var out struct{ Run string }
		if err := c.do(ctx, http.MethodPost, "/api/subtasks/"+url.PathEscape(args[1])+"/continue",
			map[string]any{"text": *text, "way": *way, "cli": *cliName, "model": *model, "effort": *effort}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.Run)
		return nil
	case "wait":
		if len(args) < 3 {
			return usageError("admin run wait ID STATE... [--timeout 10m]")
		}
		states := []string{}
		timeout := 10 * time.Minute
		for i := 2; i < len(args); i++ {
			if args[i] == "--timeout" && i+1 < len(args) {
				d, err := time.ParseDuration(args[i+1])
				if err != nil {
					return usageError(err.Error())
				}
				timeout, i = d, i+1
				continue
			}
			states = append(states, args[i])
		}
		deadline := time.Now().Add(timeout)
		for {
			var det runs.Detail
			if err := c.do(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(args[1]), nil, &det); err != nil {
				return err
			}
			if slices.Contains(states, det.State) {
				printRun(stdout, det.RunView)
				return nil
			}
			if time.Now().After(deadline) {
				printRun(stdout, det.RunView)
				return fmt.Errorf("the run is %s after %s", det.State, timeout)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return usageError(usage)
}

// adminRepo brings a repository's branches into one of the git endpoint's repositories (6.1). A
// repository on GitHub is read with the App's token that can only read, which is revoked after.
func adminRepo(ctx context.Context, e env, args []string, stdout io.Writer) error {
	if len(args) != 3 || args[0] != "seed" || !gitendpoint.ValidName(args[1]) {
		return usageError("admin repo seed NAME URL")
	}
	name, src := args[1], args[2]
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("the URL must be https, with no credentials in it")
	}
	ep := &gitendpoint.Endpoint{Dir: e.DataDir + "/repos"}
	if err := ep.Ensure(ctx, name); err != nil {
		return err
	}
	header := ""
	if u.Host == "github.com" {
		m, done, err := adminMachine(ctx, e)
		if err != nil {
			return err
		}
		defer done()
		owner := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		holder := "seed-" + name
		tok, err := m.github.TokenFor(ctx, holder, []string{owner})
		if err != nil {
			return fmt.Errorf("a token of the GitHub App for %s: %w", owner, err)
		}
		defer m.github.Revoke(context.WithoutCancel(ctx), holder)
		header = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
	}
	if err := ep.Fetch(ctx, name, src, header); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: main is %s\n", name, ep.Ref(ctx, name, "refs/heads/main"))
	return nil
}

// proofStore stores the event a microVM printed, one line of JSON on in, and reads it back: the check
// server's smoke run, which has no Docker socket, so the updater starts its microVM (16.4 step 4, since
// 0.52).
func proofStore(ctx context.Context, in io.Reader, stdout io.Writer) error {
	line, err := io.ReadAll(io.LimitReader(in, 4097))
	if err != nil {
		return err
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 || len(line) > 4096 {
		return errors.New("the event is one line of at most 4096 bytes")
	}
	pool, err := db.Open(ctx, db.ConnFromEnv(db.RoleServer))
	if err != nil {
		return err
	}
	defer pool.Close()
	at, err := runs.StoreProof(ctx, pool, line)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "an event a microVM printed is stored and read back: %s (stored at %s)\n", line, at.Format(time.RFC3339))
	return nil
}

// proofEvent is the part of install's proof that M3 adds (16.11): a microVM prints an event, and the
// server stores it and reads it back. The microVM is a check sandbox, which gets no login.
func proofEvent(ctx context.Context, e env, stdout io.Writer) error {
	m, done, err := adminMachine(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	row, sb, err := m.sandboxes.Start(ctx, sandboxes.Spec{Kind: sandboxes.KindCheck, Minimal: true, Life: 10 * time.Minute})
	if err != nil {
		return fmt.Errorf("the microVM did not start: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.sandboxes.Remove(rctx, row.ID)
	}()
	code, out, err := m.orch.Exec(ctx, sb.Container, orchestrator.ExecSpec{Cmd: []string{"crew-run", "proof"},
		User: orchestrator.GuestUser, Env: []string{"HOME=/home/" + orchestrator.GuestUser}})
	if err != nil || code != 0 {
		return fmt.Errorf("crew-run proof ended with %d: %v %s", code, err, out)
	}
	line, err := homepath.ReadFile(m.sandboxes.Home(row.ID), ".crew/proof.jsonl", 4096)
	if err != nil {
		return fmt.Errorf("the event in the microVM's home: %w", err)
	}
	line = bytes.TrimSpace(line)
	at, err := runs.StoreProof(ctx, m.runs.Pool, line)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "an event a microVM printed is stored and read back: %s (stored at %s)\n", line, at.Format(time.RFC3339))
	return nil
}
