package updater

import (
	"os"
	"strings"
	"testing"
)

// An updater that runs as root takes its lock only in a folder that is root's alone (16.11, round 2 of
// the security review of 0.53): a folder another user owns, as the user crew owns the data folder, is
// refused before a lock is made in it.
func TestARootUpdatersLockFolderIsRootsAlone(t *testing.T) {
	was := asRoot
	asRoot = func() bool { return true }
	defer func() { asRoot = was }()
	dir := t.TempDir()
	f, err := Exclusive(dir)
	if err == nil {
		f.Close()
		t.Fatal("a lock in a folder of another user was taken")
	}
	if !strings.Contains(err.Error(), "not root's alone") {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/updater.lock"); !os.IsNotExist(err) {
		t.Fatalf("a lock file was made: %v", err)
	}
}
