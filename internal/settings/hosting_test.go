package settings_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/settings"
)

// A hosted project's settings are bounded (19.3, plan M19 D13, D35): the app runs as no root, so its
// port is 1024 or more; the health address and the data folder are plain paths, and the data folder
// is none of the guest's own; the Dockerfile's name reaches a script as an argument, so it is plain;
// the numbers have a floor and a ceiling; a listed address is a mail address. Each refusal names its
// key.
func TestHostingBounds(t *testing.T) {
	defaults, _ := settings.ProjectDefaults("shop", "SF")
	refused := map[string]string{
		"hosting.port":            "{port: 80}",
		"hosting.port ":           "{port: 70000}",
		"hosting.health":          "{health: healthz}",
		"hosting.health ":         "{health: '/h?x=1'}",
		"hosting.data":            "{data: /}",
		"hosting.data ":           "{data: data}",
		"hosting.data  ":          "{data: /tmp}",
		"hosting.data   ":         "{data: /etc/app}",
		"hosting.data    ":        "{data: /srv/../etc}",
		"hosting.data     ":       "{data: /./etc}",
		"hosting.dockerfile":      "{dockerfile: '../Dockerfile'}",
		"hosting.dockerfile ":     "{dockerfile: '/Dockerfile'}",
		"hosting.dockerfile  ":    "{dockerfile: 'Docker file'}",
		"hosting.dockerfile   ":   "{dockerfile: '$(id)'}",
		"hosting.dockerfile    ":  "{dockerfile: './Dockerfile'}",
		"hosting.dockerfile     ": "{dockerfile: 'a//Dockerfile'}",
		"hosting.public_rps":      "{public_rps: 0}",
		"hosting.public_rps ":     "{public_rps: 1001}",
		"hosting.log_days":        "{log_days: 0}",
		"hosting.log_days ":       "{log_days: 366}",
		"hosting.data_copies":     "{data_copies: 0}",
		"hosting.data_copies ":    "{data_copies: 11}",
		"hosting.allowed":         "{allowed: ['not a mail']}",
	}
	for key, value := range refused {
		d, err := settings.Parse([]byte("version: 1\nhosting: " + value + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		err = settings.ValidateProject(d, defaults)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(key)+":") {
			t.Errorf("%s: %v, want a refusal naming %s", value, err, strings.TrimSpace(key))
		}
	}
	for _, value := range []string{
		"{port: 1024, health: /api/health-v1, data: /srv/data, dockerfile: deploy/Dockerfile.web}",
		"{port: 65535, public_rps: 1000, log_days: 365, data_copies: 10, allowed: [a.b@example.com]}",
		"{public_rps: 1, log_days: 1, data_copies: 1, data: /data}",
	} {
		d, err := settings.Parse([]byte("version: 1\nhosting: " + value + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		if err := settings.ValidateProject(d, defaults); err != nil {
			t.Errorf("%s: refused: %v", value, err)
		}
	}
}
