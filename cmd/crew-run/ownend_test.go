package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/runspec"
)

// Each end of crew-run's own names its step and gives the step's fixed sentence as its detail; the
// error, which can name the sandbox's door or git's text, is apart, for the server's log (13.9, since
// 1.00). The CLI does not start after any of the steps before its call.
func TestCrewRunsOwnEndsGiveTheStepsSentence(t *testing.T) {
	dir := func(k *turnKit, rel string) {
		if err := os.MkdirAll(filepath.Join(k.home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, k *turnKit, edit func(*runspec.Run)) {
		b, _ := os.ReadFile(filepath.Join(k.home, runspec.RunFile))
		var r runspec.Run
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		edit(&r)
		k.writeRun(t, r)
	}
	for _, c := range []struct {
		name, step, err string
		setUp           func(t *testing.T, k *turnKit)
	}{
		{"no run.json", runspec.StepRunFile, "run.json", func(t *testing.T, k *turnKit) {
			os.Remove(filepath.Join(k.home, runspec.RunFile))
		}},
		{"run.json does not read", runspec.StepRunFile, "run.json", func(t *testing.T, k *turnKit) {
			os.WriteFile(filepath.Join(k.home, runspec.RunFile), []byte("{"), 0o644)
		}},
		{"no CLI", runspec.StepCLI, `"not-a-cli"`, func(t *testing.T, k *turnKit) {
			run(t, k, func(r *runspec.Run) { r.CLI = "not-a-cli" })
		}},
		{"no prompt", runspec.StepPrompt, "the prompt", func(t *testing.T, k *turnKit) {
			os.Remove(filepath.Join(k.home, runspec.TurnDir(1), runspec.PromptFile))
		}},
		{"no token", runspec.StepToken, "the run's token", func(t *testing.T, k *turnKit) {
			os.Remove(filepath.Join(k.home, runspec.TokenFile))
		}},
		{"no broker", runspec.StepBroker, "the broker: ", func(t *testing.T, k *turnKit) {
			srv := httptest.NewServer(http.NotFoundHandler())
			srv.Close()
			k.env.Broker = srv.URL
		}},
		{"the broker's answer", runspec.StepBroker, "the broker's answer", func(t *testing.T, k *turnKit) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) }))
			t.Cleanup(srv.Close)
			k.env.Broker = srv.URL
		}},
		{"the credentials", runspec.StepCredentials, "the credentials", func(t *testing.T, k *turnKit) {
			os.WriteFile(filepath.Join(k.home, ".gemini"), []byte("a file, not a folder"), 0o644)
		}},
		{"no door for MCP", runspec.StepMCP, "no door", func(t *testing.T, k *turnKit) {
			run(t, k, func(r *runspec.Run) { r.MCP = "mcp" })
		}},
		{"MCP does not answer", runspec.StepMCP, "HTTP 404", func(t *testing.T, k *turnKit) {
			run(t, k, func(r *runspec.Run) { r.MCP = "/not-mcp" })
		}},
		{"the CLI's settings", runspec.StepSettings, "the CLI's settings", func(t *testing.T, k *turnKit) {
			// With no MCP server, the CLI's settings take out agy's file of them, which cannot go.
			dir(k, ".gemini/config/mcp_config.json/x") // agyMCPFile in internal/cli/agy.go
		}},
		{"the workspace", runspec.StepWorkspace, "git", func(t *testing.T, k *turnKit) {
			run(t, k, func(r *runspec.Run) { r.Git.Path = "/git/not-a-repo.git" })
		}},
		{"the spool", runspec.StepSpool, "the spool", func(t *testing.T, k *turnKit) {
			dir(k, filepath.Join(runspec.TurnDir(1), runspec.EventsFile))
		}},
		{"standard error", runspec.StepStderr, "standard error", func(t *testing.T, k *turnKit) {
			dir(k, filepath.Join(runspec.TurnDir(1), runspec.StderrFile))
		}},
		{"the CLI does not start", runspec.StepStart, "the CLI did not start: ", func(t *testing.T, k *turnKit) {
			git, err := exec.LookPath("git")
			if err != nil {
				t.Skip("no git")
			}
			// The stand-in agy is in a folder of its own, which this PATH leaves out.
			if _, err := os.Stat(filepath.Join(filepath.Dir(git), "agy")); err == nil {
				t.Skip("an agy is beside git")
			}
			t.Setenv("PATH", filepath.Dir(git))
		}},
		{"the setup's log", runspec.StepSetupLog, "the setup's log", func(t *testing.T, k *turnKit) {
			run(t, k, func(r *runspec.Run) { r.Setup = "true" })
			dir(k, filepath.Join(runspec.TurnDir(1), runspec.SetupLog))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newTurnKit(t, true)
			c.setUp(t, k)
			ex := k.turn(t)
			want, ok := runspec.StepDetail(c.step)
			if !ok || want == "" {
				t.Fatalf("step %q has no sentence", c.step)
			}
			if ex.Reason != "error" || ex.Step != c.step || ex.Detail != want || !strings.Contains(ex.Error, c.err) {
				t.Fatalf("%+v", ex)
			}
			// The door's address is in no detail.
			u, _ := url.Parse(k.env.Broker)
			if u != nil && u.Host != "" && strings.Contains(ex.Detail, u.Host) {
				t.Fatalf("the detail names the door: %q", ex.Detail)
			}
			if c.step != runspec.StepSpool && c.step != runspec.StepStderr && c.step != runspec.StepStart {
				if _, err := os.Stat(filepath.Join(k.home, "args.txt")); err == nil {
					t.Fatal("the CLI started")
				}
			}
		})
	}
}

// A work in progress that could not be kept is noted with a fixed sentence and a mark; git's error,
// which relays the endpoint's refusal, is apart (13.9, since 1.00). After an end of crew-run's own in
// the CLI's call, both errors are kept, in order.
func TestTheWorkNotKeptIsAFixedNote(t *testing.T) {
	for _, c := range []struct {
		name, step, reason, detail, before string
	}{
		{"after the CLI's end", "", "ok", "agy answered", ""},
		{"after crew-run's own", runspec.StepStderr, "error", "", "standard error: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newTurnKit(t, true)
			b, _ := os.ReadFile(filepath.Join(k.home, runspec.RunFile))
			var r runspec.Run
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			r.Git.WIP = "refs/crew/wip/not-this-runs"
			k.writeRun(t, r)
			want := c.detail
			if c.step != "" {
				os.MkdirAll(filepath.Join(k.home, runspec.TurnDir(1), runspec.StderrFile), 0o755)
				want, _ = runspec.StepDetail(c.step)
			}
			ex := k.turn(t)
			if ex.Reason != c.reason || ex.Step != c.step || !ex.WIPNotKept || ex.Detail != want+runspec.WIPNotKept ||
				!strings.HasPrefix(ex.Error, c.before) || !strings.Contains(ex.Error, "the work in progress was not kept: git") ||
				!strings.Contains(ex.Error, "may not push") {
				t.Fatalf("%+v", ex)
			}
			u, _ := url.Parse(k.env.Broker)
			if strings.Contains(ex.Detail, u.Host) || strings.Contains(ex.Detail, "may not push") {
				t.Fatalf("the detail holds git's text: %q", ex.Detail)
			}
		})
	}
}
