package setup

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/githubapp"
)

func TestTheGitHubAppMayOnlyReadContents(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	perms := `{"contents":"read","metadata":"read"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			fmt.Fprintf(w, `{"slug":"crew","owner":{"login":"kesokaj"},"permissions":%s}`, perms)
		case "/app/installations":
			fmt.Fprint(w, `[{"id":1,"account":{"login":"kesokaj"}}]`)
		}
	}))
	defer srv.Close()
	s, v, _ := service()
	s.GitHub = &githubapp.App{API: srv.URL, Log: s.Log}

	info, err := s.SetGitHubApp(context.Background(), " 12345 ", pemKey)
	if err != nil || info.Slug != "crew" || v.m[credstore.GitHubApp].Account != "12345" {
		t.Fatalf("SetGitHubApp = %+v, %v", info, err)
	}
	delete(v.m, credstore.GitHubApp)
	perms = `{"contents":"write","metadata":"read"}`
	if _, err := s.SetGitHubApp(context.Background(), "12345", pemKey); err == nil || len(v.m) != 0 {
		t.Fatal("an App that may write was stored")
	}
	perms = `{"contents":"read","metadata":"read","issues":"write"}`
	if _, err := s.SetGitHubApp(context.Background(), "12345", pemKey); err == nil || len(v.m) != 0 {
		t.Fatal("an App with more permissions was stored")
	}
	if _, err := s.SetGitHubApp(context.Background(), "abc", pemKey); err == nil {
		t.Fatal("a bad id was taken")
	}
}
