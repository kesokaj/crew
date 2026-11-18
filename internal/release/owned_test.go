package release

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Owned takes a folder only when root owns every file of it, nobody else may write any, and it holds
// no link (16.8). The owner is stood in for, so the test runs as any user.
func TestOwnedTakesOnlyRootsFilesThatNobodyElseWrites(t *testing.T) {
	saved := ownerOf
	defer func() { ownerOf = saved }()
	ownerOf = func(fs.FileInfo) (uint32, bool) { return 0, true }
	dir := filepath.Join(t.TempDir(), "0.1.0")
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"release.json", "images/crew-server.tar"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &Folder{Dir: dir}
	if err := f.Owned(); err != nil {
		t.Fatalf("root's folder: %v", err)
	}
	// Writable by the group.
	if err := os.Chmod(filepath.Join(dir, "images", "crew-server.tar"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := f.Owned(); err == nil || !strings.Contains(err.Error(), "crew-server.tar is not root's alone") {
		t.Fatalf("a file the group writes: %v", err)
	}
	_ = os.Chmod(filepath.Join(dir, "images", "crew-server.tar"), 0o644)
	// A link.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "images", "crew-caddy.tar")); err != nil {
		t.Fatal(err)
	}
	if err := f.Owned(); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("a link: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "images", "crew-caddy.tar"))
	// Another owner.
	ownerOf = func(fs.FileInfo) (uint32, bool) { return 999, true }
	if err := f.Owned(); err == nil {
		t.Fatal("a folder of another user passed")
	}
}
