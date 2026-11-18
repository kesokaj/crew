package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheAgyAccountComesFromGoogle(t *testing.T) {
	var gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotQuery, gotBody = r.URL.RawQuery, string(b)
		switch {
		case strings.Contains(gotBody, "tokgood"):
			io.WriteString(w, `{"email":"Agy-Only@example.com","email_verified":"true","scope":"openid"}`)
		case strings.Contains(gotBody, "unverif"):
			io.WriteString(w, `{"email":"x@example.com","email_verified":"false"}`)
		default:
			http.Error(w, `{"error":"invalid_token"}`, http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	g := Google{Endpoint: srv.URL}
	// The file's own id_token names another account; only Google's answer counts (spike item 56).
	idToken := "eyJhbGciOiJub25lIn0" + ".eyJlbWFpbCI6Im90aGVyQGV4YW1wbGUuY29tIn0."
	file := []byte(fmt.Sprintf(`{"id_token":%q,"token":{"access_token":"tokgood"}}`, idToken))
	email, err := g.agyAccount(context.Background(), file)
	if err != nil || email != "agy-only@example.com" {
		t.Fatalf("AgyAccount = %q, %v", email, err)
	}
	if gotQuery != "" {
		t.Fatalf("the token went in the address: %q", gotQuery)
	}
	for _, f := range []string{`{"token":{"access_token":"madeup"}}`, `{"token":{"access_token":"unverif"}}`, `{}`, `not json`} {
		if e, err := g.agyAccount(context.Background(), []byte(f)); err == nil {
			t.Errorf("%s: %q", f, e)
		}
	}
}
