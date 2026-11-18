package ingress

import (
	"reflect"
	"testing"
)

func TestParseEmailList(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"alice@example.com", []string{"alice@example.com"}},
		{"alice@example.com, bob@example.com", []string{"alice@example.com", "bob@example.com"}},
		{" alice@example.com , , bob@example.com , ", []string{"alice@example.com", "bob@example.com"}},
	}
	for _, tt := range tests {
		got := parseEmailList(tt.input)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseEmailList(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeHostname(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/myapp", "myapp"},
		{"My_App", "my-app"},
		{"/My_Complex_App", "my-complex-app"},
		{"already-clean", "already-clean"},
	}
	for _, tt := range tests {
		got := sanitizeHostname(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeHostname(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
