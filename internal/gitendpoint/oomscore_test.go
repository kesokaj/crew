package gitendpoint

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every git the endpoint starts, of a fetch and of an advertisement of refs as of a push, sets its
// score first, and a request whose score cannot be set is not taken (since 1.08).
func TestEveryGitOfTheEndpointSetsItsScoreFirst(t *testing.T) {
	g := newSlotRig(t, nil)
	main := g.e.Ref(context.Background(), "a", "refs/heads/main")
	requests := map[string]func() (*http.Response, error){
		"an advertisement of refs": func() (*http.Response, error) {
			r, _ := http.NewRequest(http.MethodGet, g.srv.URL+"/git/a.git/info/refs?service=git-upload-pack", nil)
			r.Header.Set("Authorization", "Bearer a-1")
			return http.DefaultClient.Do(r)
		},
		"a fetch": func() (*http.Response, error) {
			r, _ := http.NewRequest(http.MethodPost, g.srv.URL+"/git/a.git/git-upload-pack",
				strings.NewReader(fmt.Sprintf("0032want %s\n00000009done\n", main)))
			r.Header.Set("Authorization", "Bearer a-1")
			r.Header.Set("Content-Type", "application/x-git-upload-pack-request")
			return http.DefaultClient.Do(r)
		},
	}
	old := oomScoreFile
	t.Cleanup(func() { oomScoreFile = old })
	for name, send := range requests {
		oomScoreFile = filepath.Join(t.TempDir(), "score")
		resp, err := send()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		score, err := os.ReadFile(oomScoreFile)
		if resp.StatusCode != http.StatusOK || err != nil || string(score) != GitOOMScore || !strings.Contains(string(b), "0008NAK") && name == "a fetch" {
			t.Fatalf("%s: %d %q, score %q %v", name, resp.StatusCode, b, score, err)
		}
		oomScoreFile = filepath.Join(t.TempDir(), "none", "score")
		if resp, err = send(); err != nil {
			t.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(b), "NAK") || strings.Contains(string(b), "refs/heads/main") {
			t.Fatalf("%s whose score could not be set: %d %q", name, resp.StatusCode, b)
		}
	}
}
