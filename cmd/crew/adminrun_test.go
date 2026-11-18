package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/httpapi"
)

// The run commands ask the server as the ingress would: its secret, the admin email, and Crew's own
// address as the origin of a change.
func TestAdminRunAsksTheServerAsTheIngressWould(t *testing.T) {
	var got []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r), append(bodies, string(b))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runs":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"r-1"}`)
		case r.URL.Path == "/api/runs/r-2/stop":
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":{"code":409,"status":"FAILED_PRECONDITION","message":"the run has ended"}}`)
		default:
			io.WriteString(w, `{"run":"r-3"}`)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	os.WriteFile(tokenFile, []byte("the-secret\n"), 0o600)
	e := env{IngressTokenFile: tokenFile, Listen: strings.TrimPrefix(srv.URL, "http://"), AdminEmail: "op@example.com", Hostname: "crew.example.com"}

	var out bytes.Buffer
	if err := adminRun(context.Background(), e, []string{"start", "--cli", "claude", "--model", "haiku", "--prompt", "hi", "--without-login"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "r-1" {
		t.Fatalf("%q", out.String())
	}
	r := got[0]
	if r.Header.Get(httpapi.HeaderIngress) != "the-secret" || r.Header.Get(httpapi.HeaderEmail) != "op@example.com" ||
		r.Header.Get("Origin") != "https://crew.example.com" || !freshNow(r.Header.Get(httpapi.HeaderAuthTime)) || r.Header.Get(httpapi.HeaderSession) != "" {
		t.Fatalf("%v", r.Header)
	}
	var body map[string]any
	json.Unmarshal([]byte(bodies[0]), &body)
	if body["cli"] != "claude" || body["prompt"] != "hi" || body["withoutLogin"] != true || body["timeoutSeconds"] != 3600.0 {
		t.Fatalf("%v", body)
	}
	if err := adminRun(context.Background(), e, []string{"stop", "r-2"}, &out); err == nil || !strings.Contains(err.Error(), "the run has ended") {
		t.Fatalf("%v", err)
	}
	out.Reset()
	if err := adminRun(context.Background(), e, []string{"continue", "7", "--way", "files"}, &out); err != nil || strings.TrimSpace(out.String()) != "r-3" {
		t.Fatalf("%v %q", err, out.String())
	}
	if got[2].URL.Path != "/api/subtasks/7/continue" || !strings.Contains(bodies[2], `"way":"files"`) {
		t.Fatalf("%s %s", got[2].URL.Path, bodies[2])
	}
}

// run show names each CLI file with the bytes stored, which are sealed since 0.78, not compressed alone
// (plan of 2026-10-26, D14).
func TestAdminRunShowSaysTheBytesStored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"r-9","cliFiles":[{"path":"conv.db","bytes":128,"keepUntil":"2026-11-02T00:00:00Z"}]}`)
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("the-secret\n"), 0o600)
	e := env{IngressTokenFile: tokenFile, Listen: strings.TrimPrefix(srv.URL, "http://"), AdminEmail: "op@example.com", Hostname: "crew.example.com"}
	var out bytes.Buffer
	if err := adminRun(context.Background(), e, []string{"show", "r-9"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  CLI file conv.db, 128 bytes stored, kept until 2026-11-02T00:00:00Z\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestJSONStringsTakesTheValuesOfALoginFile(t *testing.T) {
	// Split so the repository's own scrub check does not take the made-up token for a real one.
	access := "ya" + "29.a-long-access-token"
	got := jsonStrings([]byte(`{"login":{"value":"`+access+`","expiry":"2026-10-13T14:32:18Z","n":1,"short":"x"},"list":["another-long-value-here"]}`), 16)
	slices.Sort(got)
	want := []string{"2026-10-13T14:32:18Z", "another-long-value-here", access}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if jsonStrings([]byte("not json"), 16) != nil {
		t.Fatal("not JSON")
	}
}

// freshNow says whether a request's time of sign-in is now: crew admin's requests are fresh, and name
// no device (20.3, "Two doors").
func freshNow(v string) bool {
	n, err := strconv.ParseInt(v, 10, 64)
	return err == nil && time.Since(time.Unix(n, 0)).Abs() < time.Minute
}
