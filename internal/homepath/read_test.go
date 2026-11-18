package homepath

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadFileReadsOnlyARegularFileUnderTheLimit(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "a", "b"), 0o700)
	os.WriteFile(filepath.Join(home, "a", "b", "ok"), []byte("hello"), 0o600)
	os.WriteFile(filepath.Join(home, "big"), make([]byte, 100), 0o600)
	os.Symlink(filepath.Join(home, "a", "b", "ok"), filepath.Join(home, "link"))
	os.Symlink(filepath.Join(home, "a"), filepath.Join(home, "dirlink"))
	if err := syscall.Mkfifo(filepath.Join(home, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	if b, err := ReadFile(home, "a/b/ok", 10); err != nil || string(b) != "hello" {
		t.Fatalf("a regular file: %q, %v", b, err)
	}
	for rel, want := range map[string]error{"link": ErrNotRegular, "dirlink/b/ok": ErrNotRegular, "big": ErrTooLarge} {
		if _, err := ReadFile(home, rel, 10); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", rel, err, want)
		}
	}
	for _, rel := range []string{"../x", "/etc/passwd", "a/../a/b/ok"} {
		if _, err := ReadFile(home, rel, 10); err == nil {
			t.Errorf("%s was read", rel)
		}
	}

	// A FIFO is refused at once, not waited on: nothing ever writes to this one.
	done := make(chan error, 1)
	go func() { _, err := ReadFile(home, "fifo", 10); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO waited")
	}
}

func TestWriteFileAndMkdirAllRefuseSymlinkParentsAndReplaceSymlinkTargets(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep-safe"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(home, ".crew/runs/r1/token", []byte("tok-1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile regular: %v", err)
	}
	b, err := ReadFile(home, ".crew/runs/r1/token", 64)
	if err != nil || string(b) != "tok-1\n" {
		t.Fatalf("ReadFile after WriteFile: %q, %v", b, err)
	}

	// A symlink at the leaf is replaced atomically without modifying the outside target.
	leafLink := filepath.Join(home, ".crew", "leaf")
	if err := os.Symlink(victim, leafLink); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(home, ".crew/leaf", []byte("inside-only"), 0o600); err != nil {
		t.Fatalf("WriteFile replacing leaf symlink: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep-safe" {
		t.Fatalf("outside victim was modified through leaf symlink: %q", got)
	}

	// A symlink in any parent folder is refused by MkdirAll, WriteFile, and Remove.
	if err := os.Symlink(outside, filepath.Join(home, "escapedir")); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(home, "escapedir/sub", 0o700); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("MkdirAll through symlink dir = %v, want ErrNotRegular", err)
	}
	if err := WriteFile(home, "escapedir/victim.txt", []byte("pwned"), 0o600); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("WriteFile through symlink dir = %v, want ErrNotRegular", err)
	}
	if err := Remove(home, "escapedir/victim.txt"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Remove through symlink dir = %v, want ErrNotRegular", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep-safe" {
		t.Fatalf("outside victim was modified or removed through parent symlink: %q", got)
	}
}
