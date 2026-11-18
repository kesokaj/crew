package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/cli"
)

func broker(t *testing.T, token string, answer any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/v1/creds" {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func home(t *testing.T, token string) string {
	h := t.TempDir()
	os.MkdirAll(filepath.Join(h, ".crew"), 0o700)
	os.WriteFile(filepath.Join(h, ".crew", "token"), []byte(token+"\n"), 0o600)
	return h
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestCredsWritesTheFilesAndTheValues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	h := home(t, "tok")
	srv := broker(t, "tok", map[string]any{
		"env": map[string]string{"CREW_SOME_VALUE": "it's-a-token", "CREW_GIT_NAME": "Ada", "CREW_GIT_EMAIL": "ada@example.com"},
		"files": []map[string]string{
			{"path": ".gemini/antigravity-cli/antigravity-oauth-token", "mode": "0600", "data": b64(`{"t":1}`)},
		},
	})
	var out, errb bytes.Buffer
	if code := creds(env{Home: h, Broker: srv.URL}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got, err := os.ReadFile(filepath.Join(h, ".gemini/antigravity-cli/antigravity-oauth-token"))
	fi, _ := os.Stat(filepath.Join(h, ".gemini/antigravity-cli/antigravity-oauth-token"))
	if err != nil || string(got) != `{"t":1}` || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agy's file: %q %v %v", got, fi.Mode(), err)
	}
	settings, _ := os.ReadFile(filepath.Join(h, ".gemini/antigravity-cli/settings.json"))
	if !strings.Contains(string(settings), `"useG1Credits": false`) {
		t.Fatalf("agy's paid credits are not written as off: %s", settings)
	}
	envf, _ := os.ReadFile(filepath.Join(h, EnvFile))
	fi, _ = os.Stat(filepath.Join(h, EnvFile))
	if fi.Mode().Perm() != 0o600 || !strings.Contains(string(envf), `export CREW_SOME_VALUE='it'\''s-a-token'`) ||
		!strings.Contains(string(envf), "unset ANTHROPIC_API_KEY") {
		t.Fatalf("the env file (%v):\n%s", fi.Mode(), envf)
	}
	// The shell reads it back to the same value.
	sh := exec.Command("sh", "-c", `. "$1"; printf %s "$CREW_SOME_VALUE"`, "sh", filepath.Join(h, EnvFile))
	if v, err := sh.Output(); err != nil || string(v) != "it's-a-token" {
		t.Fatalf("sourced: %q %v", v, err)
	}
	cfg, _ := os.ReadFile(filepath.Join(h, ".gitconfig"))
	if !strings.Contains(string(cfg), "name = Ada") || !strings.Contains(string(cfg), "email = ada@example.com") {
		t.Fatalf("gitconfig:\n%s", cfg)
	}
	// It says what it wrote, never a value.
	if strings.Contains(out.String(), "it's-a-token") || !strings.Contains(out.String(), "variable CREW_SOME_VALUE") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestCredsRefusesWhatItShouldNotWrite(t *testing.T) {
	cases := map[string]any{
		"a path out of the home": map[string]any{"files": []map[string]string{{"path": "../x", "mode": "0600", "data": b64("x")}}},
		"an absolute path":       map[string]any{"files": []map[string]string{{"path": "/etc/x", "mode": "0600", "data": b64("x")}}},
		"a file others can read": map[string]any{"files": []map[string]string{{"path": "x", "mode": "0644", "data": b64("x")}}},
		"ANTHROPIC_API_KEY":      map[string]any{"env": map[string]string{"ANTHROPIC_API_KEY": "sk"}},
		"a strange name":         map[string]any{"env": map[string]string{"A;rm": "x"}},
		"a value of two lines":   map[string]any{"env": map[string]string{"A": "x\nexport B=1"}},
	}
	for name, ans := range cases {
		h := home(t, "tok")
		srv := broker(t, "tok", ans)
		var out, errb bytes.Buffer
		if code := creds(env{Home: h, Broker: srv.URL}, nil, &out, &errb); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCredsWithoutTheRightToken(t *testing.T) {
	h := home(t, "wrong")
	srv := broker(t, "tok", map[string]any{})
	var out, errb bytes.Buffer
	if code := creds(env{Home: h, Broker: srv.URL}, nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "403") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if code := creds(env{Home: h}, nil, &out, &errb); code != 1 {
		t.Fatalf("with no broker: exit %d", code)
	}
}

func TestCredsReplacesALinkInsteadOfFollowingIt(t *testing.T) {
	h := home(t, "tok")
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("keep"), 0o600)
	os.MkdirAll(filepath.Join(h, "d"), 0o700)
	os.Symlink(outside, filepath.Join(h, "d", "f"))
	srv := broker(t, "tok", map[string]any{"files": []map[string]string{{"path": "d/f", "mode": "0600", "data": b64("new")}}})
	var out, errb bytes.Buffer
	if code := creds(env{Home: h, Broker: srv.URL}, []string{"--quiet"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatal("the link was followed")
	}
	if out.Len() != 0 {
		t.Fatalf("--quiet printed %q", out.String())
	}
}

// A login check runs the adapter's commands with the login from the broker, never with
// ANTHROPIC_API_KEY, and prints the adapter's result.
func TestLoginCheckRunsTheCLIWithItsLogin(t *testing.T) {
	h := home(t, "tok")
	srv := broker(t, "tok", map[string]any{"files": []map[string]string{
		{"path": ".gemini/antigravity-cli/antigravity-oauth-token", "mode": "0600", "data": b64(`{"token":{"access_token":"made-up"}}`)},
	}})
	bin := t.TempDir()
	// A stand-in agy: it answers like a refused login unless the broker's token file is in its home,
	// and says so if ANTHROPIC_API_KEY reached it. It plays agy's recorded answers.
	rec, _ := filepath.Abs("../../testdata/agy")
	os.WriteFile(filepath.Join(bin, "agy"), []byte(`#!/bin/sh
[ -n "$ANTHROPIC_API_KEY" ] && { echo "API KEY LEAKED"; exit 9; }
grep -q made-up "$HOME/.gemini/antigravity-cli/antigravity-oauth-token" || { echo "authentication required: sign in"; exit 1; }
case "$*" in
models) cat "`+rec+`/models.txt" ;;
"-p /credits") cat "`+rec+`/credits.txt" ;;
"-p /usage --output-format json") cat "`+rec+`/usage-json.json" ;;
*) exit 3 ;;
esac
`), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-reach-it")
	var out, errb bytes.Buffer
	if code := loginCheck(env{Home: h, Broker: srv.URL}, []string{"agy", "--real"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r cli.LoginResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.State != "ok" {
		t.Fatalf("result %s: %v", out.String(), err)
	}
	// The list of models comes from agy models, the credits from the real check, and the limits from
	// /usage, with no model request (12.3, 12.7).
	if len(r.Models) != 10 || r.Models[2].Name != "gemini-3.8-flash-low" || r.Models[2].Model != "gemini-3.8-flash" ||
		r.ModelsError != "" || r.Credits != "0" {
		t.Fatalf("models %+v %q, credits %q", r.Models, r.ModelsError, r.Credits)
	}
	if len(r.Limits) != 4 || r.Limits[0].Group != "gemini" || r.Limits[0].Used < 0.05 || r.Limits[0].Used > 0.07 {
		t.Fatalf("limits %+v", r.Limits)
	}
}

// The asker closes standard input once a line of the answer was the last one needed, so a CLI that
// reads until its input ends finishes instead of running into the time limit (12.3).
func TestTheAskerClosesStandardInputAfterTheAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ask := commandAsker(t.TempDir(), nil)
	out, _, code, err := ask(ctx, []byte("question\n"), func(l []byte) bool { return string(l) == "answer to question" },
		"sh", "-c", `read -r q; echo "answer to $q"; cat >/dev/null; echo ended`)
	if err != nil || code != 0 || string(out) != "answer to question\nended\n" {
		t.Fatalf("%q %d %v", out, code, err)
	}
}
