package deploy

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The server and everything it starts, the git endpoint's pushes and fetches among them, have a
// ceiling of 4 GiB, with no swap beyond it, which the machine's reserve holds (6.1, 13.10; since 1.08).
func TestTheServerHasACeilingOnItsMemory(t *testing.T) {
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			MemLimit  string `yaml:"mem_limit"`
			SwapLimit string `yaml:"memswap_limit"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if s := c.Services["crew-server"]; s.MemLimit != "4g" || s.SwapLimit != "4g" {
		t.Errorf("crew-server: mem_limit %q and memswap_limit %q, want 4g and 4g", s.MemLimit, s.SwapLimit)
	}
}
