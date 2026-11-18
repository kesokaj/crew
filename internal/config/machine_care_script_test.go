package config

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMachineCareScript runs deploy/lib/machine-care-test.sh, the test of the security updates' settings,
// the restart timer, and the producer check that host-setup.sh takes from deploy/lib/machine-care.sh
// (16.10), so `go test ./...` exercises the real shell functions against a scratch dir.
func TestMachineCareScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	out, err := exec.Command("bash", filepath.Join(repoRoot(t), "deploy", "lib", "machine-care-test.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("machine-care-test.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), " 0 failed") {
		t.Fatalf("machine-care-test.sh exited 0 without its summary line:\n%s", out)
	}
}
