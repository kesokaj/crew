package hosting

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/harness"
)

// The copy of the data (plan M19 D18): a link is copied as a link and never followed, a hard link
// becomes a file of its own, a FIFO is left out with a note, and folders keep their files.
func TestTheCopyOfTheDataFollowsNoLink(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("the machine's"), 0o600)
	os.MkdirAll(filepath.Join(src, "db", "wal"), 0o750)
	os.WriteFile(filepath.Join(src, "db", "main.db"), []byte("rows"), 0o640)
	os.WriteFile(filepath.Join(src, "db", "wal", "1"), []byte("log"), 0o600)
	os.Symlink(secret, filepath.Join(src, "out"))
	os.Symlink("db/main.db", filepath.Join(src, "in"))
	os.Link(filepath.Join(src, "db", "main.db"), filepath.Join(src, "hard"))
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	var notes []string
	if err := copyTree(src, dst, time.Now().Add(time.Minute), &notes); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "db", "wal", "1")); string(b) != "log" {
		t.Fatalf("a file in a folder: %q", b)
	}
	if target, err := os.Readlink(filepath.Join(dst, "out")); err != nil || target != secret {
		t.Fatalf("the link out: %q %v", target, err)
	}
	if target, _ := os.Readlink(filepath.Join(dst, "in")); target != "db/main.db" {
		t.Fatalf("the link in: %q", target)
	}
	a, _ := os.Stat(filepath.Join(dst, "hard"))
	b, _ := os.Stat(filepath.Join(dst, "db", "main.db"))
	if a == nil || b == nil || os.SameFile(a, b) {
		t.Fatal("the hard link is not a file of its own")
	}
	if _, err := os.Lstat(filepath.Join(dst, "pipe")); err == nil || len(notes) != 1 || !strings.HasPrefix(notes[0], "pipe") {
		t.Fatalf("the FIFO: %v %v", err, notes)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "db", "wal", "1")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode: %v", fi.Mode())
	}
}

// A copy past its bound stops, and the deploy says so; the sandbox that serves is unpaused.
func TestACopyPastItsBoundFails(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a"), []byte("x"), 0o640)
	var notes []string
	if err := copyTree(src, filepath.Join(t.TempDir(), "c"), time.Now().Add(-time.Second), &notes); !errors.Is(err, errTooLong) {
		t.Fatalf("past the bound: %v", err)
	}
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("a", "x")
	old := CopyTimeout
	CopyTimeout = -time.Second
	t.Cleanup(func() { CopyTimeout = old })
	d := h.deploy(asked)
	if d.State != "failed" || !strings.Contains(d.Detail, "the copy of the data did not finish") {
		t.Fatalf("%s %q", d.State, d.Detail)
	}
	if sb := SandboxID(h.id, 1); h.rt.paused[sb] || !slices.Contains(h.rt.calls, "unpause "+sb) {
		t.Fatal("the sandbox that serves was left paused")
	}
	if _, err := os.Stat(h.s.copyDir(h.id, "2")); err == nil {
		t.Fatal("the half copy was kept")
	}
}

// Putting back a copy empties the data folder, removing links without following them.
func TestReplaceDataRemovesLinksWithoutFollowingThem(t *testing.T) {
	data, cp := t.TempDir(), t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "keep"), []byte("machine"), 0o600)
	os.Symlink(outside, filepath.Join(data, "dir-link"))
	os.WriteFile(filepath.Join(data, "new"), []byte("n"), 0o640)
	os.WriteFile(filepath.Join(cp, "old"), []byte("o"), 0o640)
	if err := replaceData(data, cp); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "keep")); string(b) != "machine" {
		t.Fatal("a link was followed")
	}
	entries, _ := os.ReadDir(data)
	if len(entries) != 1 || entries[0].Name() != "old" {
		t.Fatalf("the data: %v", entries)
	}
}

// A copy of the data that the disk has no room for, beside the margin, fails the deploy before it is
// made (review of M19, finding 9).
func TestACopyWithNoRoomFailsTheDeploy(t *testing.T) {
	h := newHosted(t, nil)
	h.deploy(asked)
	h.write("shop.db", strings.Repeat("x", 100))
	harness.FreeSpace = func(dir string) (uint64, error) {
		if strings.Contains(dir, "copies") {
			return harness.DiskMargin + 10, nil
		}
		return 1 << 40, nil
	}
	d := h.deploy(asked)
	if d.State != "failed" || !strings.Contains(d.Detail, "the copy of the data needs 100 bytes") {
		t.Fatalf("%s %q", d.State, d.Detail)
	}
	if _, err := os.Stat(h.s.copyDir(h.id, "2")); err == nil {
		t.Fatal("a copy was begun")
	}
}
