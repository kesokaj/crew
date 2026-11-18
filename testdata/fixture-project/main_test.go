package main

import (
	"io"
	"net/http/httptest"
	"testing"
)

func TestHello(t *testing.T) {
	for in, want := range map[string]string{"": "Hello, world!", "Ada": "Hello, Ada!"} {
		if got := Hello(in); got != want {
			t.Errorf("Hello(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoutes(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()
	for path, want := range map[string]string{"/healthz": "ok\n", "/hello/Ada": "Hello, Ada!\n"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Errorf("GET %s = %q, want %q", path, body, want)
		}
	}
}
