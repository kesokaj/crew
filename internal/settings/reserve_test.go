package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The machine's reserve holds the ceilings of the core services that always run, the server's with its
// pushes and fetches among them, so memory is never overcommitted (13.10, since 1.08): the capacity
// check gives the sandboxes only what is left.
func TestTheReserveHoldsTheCoreServicesCeilings(t *testing.T) {
	b, err := os.ReadFile("../../deploy/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			MemLimit string   `yaml:"mem_limit"`
			Profiles []string `yaml:"profiles"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	var ceilings float64
	for _, name := range []string{"crew-db", "crew-server", "crew-caddy"} {
		s, ok := c.Services[name]
		if !ok || len(s.Profiles) > 0 {
			t.Fatalf("%s is not a service that always runs", name)
		}
		gb, err := strconv.ParseFloat(strings.TrimSuffix(s.MemLimit, "g"), 64)
		if err != nil || !strings.HasSuffix(s.MemLimit, "g") {
			t.Fatalf("%s: mem_limit %q is not in gigabytes", name, s.MemLimit)
		}
		ceilings += gb
	}
	d, err := Defaults(Machine{})
	if err != nil {
		t.Fatal(err)
	}
	reserve, err := strconv.ParseFloat(fmt.Sprint(d["machine_reserve"].(Doc)["memory_gb"]), 64)
	if err != nil {
		t.Fatal(err)
	}
	if reserve < ceilings+1 {
		t.Fatalf("the reserve is %v GB, less than the core services' ceilings, %v GB, and 1 for the rest", reserve, ceilings)
	}
}
