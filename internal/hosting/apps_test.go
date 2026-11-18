package hosting_test

import (
	"testing"

	"github.com/kesokaj/crew/internal/hosting"
)

// Hosting needs an apps domain of its own (19.4, R172): none, Crew's own address, or a name under it
// or over it is refused with the reason, as a guard if the install file was edited by hand after
// crew-ctl checked that the two registered domains differ (plan M19 D23).
func TestAppsDomainProblem(t *testing.T) {
	for apps, want := range map[string]bool{
		"":                         true,
		"crew.example.com":         true,
		"apps.crew.example.com":    true,
		"example.com":              true,
		"CREW.example.com.":        true,
		"crew-apps.example":        false,
		"apps.example.org":         false,
		"xcrew.example.com":        false,
		"crew.example.com.evil.io": false,
	} {
		if got := hosting.AppsDomainProblem(apps, "crew.example.com") != ""; got != want {
			t.Errorf("%q: refused %v, want %v (%q)", apps, got, want, hosting.AppsDomainProblem(apps, "crew.example.com"))
		}
	}
}

// A project is hosted under its name as it is, so the name must be a DNS label, and not one of the
// ingress's own names (plan M19 D10).
func TestNameProblem(t *testing.T) {
	for name, want := range map[string]bool{
		"shop": false, "a": false, "shop-2": false, "a1b2": false,
		"crew": true, "auth": true, "shop-": true, "-shop": true, "Shop": true, "a_b": true, "": true,
		"abcdefghij-abcdefghij-abcdefghij-abcdefghij-abcdefghij-abcdefghij": true,
	} {
		if got := hosting.NameProblem(name) != ""; got != want {
			t.Errorf("%q: refused %v, want %v", name, got, want)
		}
	}
}
