package gitendpoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The header the endpoint's fetch sends, such as a token of the GitHub App for an administrator's run,
// is taken out of its error where git repeats it, also its start where the kept error text cuts it
// short (since 1.05).
func TestTheEndpointsFetchTakesItsHeaderOutOfAnError(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s3cr", 10)
	header := "Authorization: Basic " + token
	dir := t.TempDir()
	for name, before := range map[string]int{"whole": 100, "cut short": 64<<10 - 30} {
		t.Run(name, func(t *testing.T) {
			fake := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			os.WriteFile(fake, []byte(fmt.Sprintf("#!/bin/sh\nfor a; do [ \"$a\" = fetch ] && { head -c %d /dev/zero | tr '\\0' e; printf '%%s' \"$GIT_CONFIG_VALUE_0\"; exit 1; }; done\nexec %s \"$@\"\n", before, real)), 0o755)
			e := &Endpoint{Dir: t.TempDir(), Git: fake}
			if err := os.MkdirAll(e.Path("shop"), 0o755); err != nil {
				t.Fatal(err)
			}
			err := e.Fetch(context.Background(), "shop", "https://github.com/o/shop.git", header)
			if err == nil || !strings.Contains(err.Error(), "git fetch") || strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("%.200q", fmt.Sprint(err)[max(0, len(fmt.Sprint(err))-200):])
			}
		})
	}
}
