package credbroker_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The login shell's part (image/configs/crew-credentials.sh): it runs crew-run creds, reads what
// that wrote, never keeps ANTHROPIC_API_KEY, and leaves a marker when the broker does not answer.
func TestTheLoginShellReadsWhatCrewRunWrote(t *testing.T) {
	snippet, _ := filepath.Abs("../../image/configs/crew-credentials.sh")
	for _, c := range []struct {
		name     string
		crewRun  string
		want     string
		marker   bool
		hasToken bool
	}{
		{"the broker answers", "mkdir -p \"$HOME/.crew\"; echo \"export GH_TOKEN='ghs_x'\" > \"$HOME/.crew/creds.env\"", "ghs_x", false, true},
		{"the broker does not answer", "exit 1", "", true, true},
		{"no token: not a sandbox", "echo should-not-run >&2; exit 1", "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			os.MkdirAll(filepath.Join(home, ".crew"), 0o700)
			if c.hasToken {
				os.WriteFile(filepath.Join(home, ".crew", "token"), []byte("t"), 0o600)
			}
			os.WriteFile(filepath.Join(bin, "crew-run"), []byte("#!/bin/sh\n"+c.crewRun+"\n"), 0o755)
			cmd := exec.Command("sh", "-c", `. "$1"; printf '%s|%s' "$GH_TOKEN" "${ANTHROPIC_API_KEY-unset}"`, "sh", snippet)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "CREW_CRED_BROKER=http://10.9.1.254:8091", "ANTHROPIC_API_KEY=sk-made-up"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if string(out) != c.want+"|unset" {
				t.Fatalf("got %q", out)
			}
			_, err = os.Stat(filepath.Join(home, ".crew", "broker-unreachable"))
			if (err == nil) != c.marker {
				t.Fatalf("marker present: %v", err == nil)
			}
		})
	}
}

func TestTheGitHelperAsksOnlyForGitHub(t *testing.T) {
	helper, _ := filepath.Abs("../../image/configs/crew-git-credential")
	b, _ := os.ReadFile(helper)
	if !strings.Contains(string(b), `[ "${host}" = "github.com" ] || exit 0`) || !strings.Contains(string(b), "Authorization: Bearer") {
		t.Fatal("the git helper must ask only for github.com, with the sandbox's token")
	}
	cmd := exec.Command("bash", helper, "get")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=evil.example.com\n\n")
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".crew"), 0o700)
	os.WriteFile(filepath.Join(home, ".crew", "token"), []byte("t"), 0o600)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "CREW_CRED_BROKER=http://127.0.0.1:1"}
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("another host: %q %v", out, err)
	}
}
