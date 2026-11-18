package board

import "testing"

// The daily look's tickets are the backend's: Analysis discarding one is feedback on no agent's role
// (9.5; 12.8, since 0.56).
func TestTheDailyLooksAuthorIsNoRole(t *testing.T) {
	if r := roleOf(Backend + " · daily look"); r != "" {
		t.Fatalf("role %q", r)
	}
	if roleOf("builder · r_1") != "builder" {
		t.Fatal("a builder's ticket names its role")
	}
}
