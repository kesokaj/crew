package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

func TestInstallByHandReplacesPendingGateWithByHandFlag(t *testing.T) {
	c := newCtl(t)
	c.install(good)
	data := filepath.Join(c.root, "data")
	releaseFolder(t, data, "0.1.0", true)
	dir := releaseFolder(t, data, "0.3.0", false)
	f, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteGate(release.Gate{
		Passed:      false,
		At:          time.Now().UTC(),
		InstallTest: "skipped",
		Live:        release.LivePending,
	}); err != nil {
		t.Fatal(err)
	}
	c.updaterOn("0.1.0")

	out, _ := c.run("install", "--from", dir, "--confirm", "install")
	if strings.Contains(out, "is pending") {
		t.Fatalf("install --from left gate.json pending:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, release.GateFile)); !os.IsNotExist(err) {
		t.Fatalf("expected pending gate.json to be removed on install --from, got err=%v", err)
	}
	after, err := release.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Manifest.ByHand {
		t.Fatal("expected release.json to have byHand: true after install --from")
	}
}
