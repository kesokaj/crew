package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/homepath"
	"github.com/kesokaj/crew/internal/runspec"
)

// What the broker answers (internal/credbroker).
type credsAnswer struct {
	Env   map[string]string `json:"env"`
	Files []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Data string `json:"data"`
	} `json:"files"`
}

// EnvFile holds the values a login shell exports, written by creds. Only the sandbox's user reads it.
const EnvFile = ".crew/creds.env"

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// creds pulls the sandbox's credentials from the broker (12.6). A headless run has no login shell,
// so crew-run does what styrman-crew's login shell did: files go into the home, and values go into
// EnvFile (or .crew/runs/<runID>/creds.env when --run is passed) for the shell and into the
// environment of what crew-run starts.
func creds(e env, args []string, stdout, stderr io.Writer) int {
	quiet := false
	runID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--quiet":
			quiet = true
		case "--run":
			if i+1 < len(args) {
				runID = strings.TrimSpace(args[i+1])
				i++
			}
		}
	}
	ctrlRel := runspec.Dir
	if runID != "" {
		ctrlRel = runspec.RunControlDir(runID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ans, err := pullIn(ctx, e, ctrlRel, "/v1/creds")
	if err != nil {
		fmt.Fprintln(stderr, "crew-run creds:", err)
		return 1
	}
	var c credsAnswer
	if err := json.Unmarshal(ans, &c); err != nil {
		fmt.Fprintln(stderr, "crew-run creds: the broker's answer:", err)
		return 1
	}
	got, err := applyIn(e.Home, ctrlRel, c)
	if err != nil {
		fmt.Fprintln(stderr, "crew-run creds:", err)
		return 1
	}
	if !quiet {
		if len(got) == 0 {
			fmt.Fprintln(stdout, "crew-run creds: this sandbox gets no credentials")
		}
		for _, g := range got {
			fmt.Fprintln(stdout, "crew-run creds:", g)
		}
	}
	return 0
}

// pull asks the broker, with the sandbox's token from its control folder (12.4).
func pull(ctx context.Context, e env, urlPath string) ([]byte, error) {
	return pullIn(ctx, e, runspec.Dir, urlPath)
}

func pullIn(ctx context.Context, e env, ctrlRel, urlPath string) ([]byte, error) {
	if e.Broker == "" {
		return nil, errors.New("CREW_CRED_BROKER is not set: this is not a sandbox of Crew")
	}
	tokRel := path.Join(ctrlRel, "token")
	tok, err := homepath.ReadFile(e.Home, tokRel, 4096)
	if err != nil && ctrlRel != runspec.Dir {
		tok, err = homepath.ReadFile(e.Home, path.Join(runspec.Dir, "token"), 4096)
	}
	if err != nil {
		return nil, fmt.Errorf("the sandbox's token: %w", err)
	}
	return pullWithToken(ctx, e, urlPath, strings.TrimSpace(string(tok)))
}

func pullWithToken(ctx context.Context, e env, urlPath, token string) ([]byte, error) {
	if e.Broker == "" {
		return nil, errors.New("CREW_CRED_BROKER is not set: this is not a sandbox of Crew")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.Broker, "/")+urlPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the broker does not answer: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the broker said %s", resp.Status)
	}
	return body, nil
}

// apply writes the files and EnvFile, and returns what it wrote, by name only.
func apply(home string, c credsAnswer) ([]string, error) {
	return applyIn(home, runspec.Dir, c)
}

func applyIn(home, ctrlRel string, c credsAnswer) ([]string, error) {
	if ctrlRel == "" {
		ctrlRel = runspec.Dir
	}
	var got []string
	for _, f := range c.Files {
		if err := homepath.Valid(f.Path); err != nil {
			return got, err
		}
		mode, err := strconv.ParseUint(f.Mode, 8, 32)
		if err != nil || mode&0o7077 != 0 {
			return got, fmt.Errorf("%s: mode %q is not one only its owner can read", f.Path, f.Mode)
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return got, fmt.Errorf("%s: %w", f.Path, err)
		}
		if err := homepath.WriteFile(home, f.Path, data, os.FileMode(mode)); err != nil {
			return got, err
		}
		got = append(got, "file ~/"+f.Path)
	}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		if !envName.MatchString(k) || cli.ForbiddenEnv(k) {
			return got, fmt.Errorf("the broker sent a variable crew-run does not set: %q", k)
		}
		if strings.ContainsAny(c.Env[k], "\x00\n\r") {
			return got, fmt.Errorf("the value of %s is not one line", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by crew-run creds from the broker (design 12.6). Removed with the sandbox.\n")
	for _, forbidden := range cli.ForbiddenEnvNames() {
		b.WriteString("unset " + forbidden + "\n")
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(c.Env[k]))
		got = append(got, "variable "+k)
	}
	envRel := path.Join(ctrlRel, "creds.env")
	if err := homepath.WriteFile(home, envRel, []byte(b.String()), 0o600); err != nil {
		return got, err
	}
	for _, name := range cli.Names() {
		a, _ := cli.Get(name)
		has := false
		for _, f := range c.Files {
			has = has || f.Path == a.LoginFile()
		}
		if has {
			if err := a.PrepareHome(home); err != nil {
				return got, fmt.Errorf("preparing the home for %s: %w", name, err)
			}
		}
	}
	if name, email := c.Env["CREW_GIT_NAME"], c.Env["CREW_GIT_EMAIL"]; name != "" && email != "" {
		for _, kv := range [][2]string{{"user.name", name}, {"user.email", email}} {
			cmd := exec.Command("git", "config", "--global", kv[0], kv[1])
			cmd.Env = append(os.Environ(), "HOME="+home)
			if out, err := cmd.CombinedOutput(); err != nil {
				return got, fmt.Errorf("git config %s: %v: %s", kv[0], err, out)
			}
		}
		got = append(got, "git identity")
	}
	return got, nil
}

// writeFile replaces path with data at mode, through a file beside it, so a reader never sees half a
// file, and a link at path is replaced, not followed.
func writeFile(filePath string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".crew-run-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filePath)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
