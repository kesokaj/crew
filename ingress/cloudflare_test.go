package ingress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFetchIP_ValidIPv4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "  203.0.113.42\n")
	}))
	defer server.Close()

	client := server.Client()
	ip, err := fetchIP(client, server.URL)
	if err != nil {
		t.Fatalf("fetchIP() error = %v", err)
	}
	if ip != "203.0.113.42" {
		t.Errorf("fetchIP() = %q, want %q", ip, "203.0.113.42")
	}
}

func TestFetchIP_RejectsIPv6(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "2001:db8::1\n")
	}))
	defer server.Close()

	client := server.Client()
	_, err := fetchIP(client, server.URL)
	if err == nil {
		t.Error("fetchIP() should reject IPv6 addresses (devbox only uses IPv4 A records)")
	}
}

func TestFetchIP_InvalidResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"html error page", "<html><body>Error</body></html>"},
		{"empty", ""},
		{"garbage text", "not-an-ip-address"},
		{"partial IP", "192.168.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			client := server.Client()
			_, err := fetchIP(client, server.URL)
			if err == nil {
				t.Errorf("fetchIP() with body %q should return error", tt.body)
			}
		})
	}
}

func TestFetchIP_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, "1.2.3.4")
	}))
	defer server.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := fetchIP(client, server.URL)
	if err == nil {
		t.Error("fetchIP() should timeout")
	}
}

func TestFetchIP_HTTPError(t *testing.T) {
	_, err := fetchIP(http.DefaultClient, "http://127.0.0.1:1") // should fail to connect
	if err == nil {
		t.Error("fetchIP() with unreachable server should return error")
	}
}

// fakeCloudflare is Cloudflare's API for two zones: it answers zone lookups, says no record is
// there, and keeps the names of the records made in each zone.
func fakeCloudflare(t *testing.T, zones map[string]string) (*httptest.Server, func() map[string][]string) {
	t.Helper()
	var mu sync.Mutex
	made := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == "GET" && len(parts) == 1 && parts[0] == "zones":
			if id, ok := zones[r.URL.Query().Get("name")]; ok {
				fmt.Fprintf(w, `{"success": true, "result": [{"id": %q, "name": %q}]}`, id, r.URL.Query().Get("name"))
				return
			}
			fmt.Fprint(w, `{"success": true, "result": []}`)
		case r.Method == "GET" && len(parts) == 3 && parts[2] == "dns_records":
			fmt.Fprint(w, `{"success": true, "result": []}`)
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "dns_records":
			var rec cfRecord
			json.NewDecoder(r.Body).Decode(&rec)
			mu.Lock()
			made[parts[1]] = append(made[parts[1]], rec.Name)
			mu.Unlock()
			fmt.Fprintf(w, `{"success": true, "result": {"id": "r1", "name": %q}}`, rec.Name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string][]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string][]string{}
		for k, v := range made {
			out[k] = append([]string(nil), v...)
		}
		return out
	}
}

// With hosting, the apps domain's zone gets its wildcard and its portal, beside Crew's two names in
// Crew's zone; without it, the apps zone is not touched (19.4, plan M19 D33).
func TestTheAppsZoneGetsItsRecords(t *testing.T) {
	srv, made := fakeCloudflare(t, map[string]string{"crew.example.com": "Z1", "apps.test": "Z2"})
	clients := func(domain string) (*CloudflareClient, error) {
		return newCloudflareClient(srv.URL, srv.Client(), "tok", domain, quiet())
	}
	cfg := hostedConfig()
	cfg.Hosting = false
	if err := syncZones("203.0.113.9", cfg, clients); err != nil {
		t.Fatal(err)
	}
	if got := made(); fmt.Sprint(got) != "map[Z1:[crew.example.com auth.crew.example.com]]" {
		t.Fatalf("without hosting: %v", got)
	}
	cfg.Hosting = true
	if err := syncZones("203.0.113.9", cfg, clients); err != nil {
		t.Fatal(err)
	}
	if got := made()["Z2"]; fmt.Sprint(got) != "[*.apps.test auth.apps.test]" {
		t.Fatalf("the apps zone: %v", got)
	}
}
