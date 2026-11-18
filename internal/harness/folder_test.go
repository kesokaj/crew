package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// entries reads a gzip tar into its names with their modes, and the content of its files.
func entries(t *testing.T, gz []byte) (map[string]int64, map[string]string) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	modes, data := map[string]int64{}, map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return modes, data
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[h.Name] = h.Mode
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			data[h.Name] = string(b)
		}
	}
}

// saveWithin runs SaveFolder and fails the test when it does not return within ten seconds: a FIFO
// it opened would hold it for ever.
func saveWithin(t *testing.T, home, out string) (int, int64, int, error) {
	t.Helper()
	return saveIn(t, home, out, 10*time.Second)
}

// saveIn is saveWithin with a time of its own, for a folder of 200 MB, which takes longer under -race.
func saveIn(t *testing.T, home, out string, within time.Duration) (int, int64, int, error) {
	t.Helper()
	type res struct {
		files   int
		size    int64
		skipped int
		err     error
	}
	done := make(chan res, 1)
	go func() {
		f, s, k, err := SaveFolder(home, ConversationFolder, out)
		done <- res{f, s, k, err}
	}()
	select {
	case r := <-done:
		return r.files, r.size, r.skipped, r.err
	case <-time.After(within):
		t.Fatal("SaveFolder blocked")
	}
	return 0, 0, 0, nil
}

// The work folder is saved through its root: folders and regular files, modes masked, a link and a
// FIFO left out and not waited on (D17).
func TestAWorkFolderKeepsFoldersAndRegularFilesOnly(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ConversationFolder)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "sub/run.sh"), []byte("#!/bin/sh\n"), 0o700)
	os.WriteFile(filepath.Join(dir, "setuid"), []byte("x"), 0o755)
	os.Chmod(filepath.Join(dir, "setuid"), 0o4755)
	os.WriteFile(filepath.Join(home, "outside.txt"), []byte("outside"), 0o600)
	os.Symlink(filepath.Join(home, "outside.txt"), filepath.Join(dir, "link"))
	os.Symlink("/etc", filepath.Join(dir, "etc"))
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), FolderFile)
	files, size, skipped, err := saveWithin(t, home, out)
	if err != nil || files != 3 || size != 6+10+1 || skipped != 3 {
		t.Fatalf("files %d, bytes %d, skipped %d, %v", files, size, skipped, err)
	}
	gz, _ := os.ReadFile(out)
	modes, data := entries(t, gz)
	want := map[string]int64{"notes.txt": 0o644, "sub/": 0o755, "sub/run.sh": 0o755, "setuid": 0o755}
	if len(modes) != len(want) {
		t.Fatalf("entries %v", modes)
	}
	for n, m := range want {
		if modes[n] != m {
			t.Errorf("%s: mode %o, want %o (%v)", n, modes[n], m, modes)
		}
	}
	if data["notes.txt"] != "notes\n" {
		t.Fatalf("notes.txt: %q", data["notes.txt"])
	}
}

// A link to another folder of the home swapped in for the work folder between its check and its open
// is refused, and nothing of that folder is written (security review of M20's close, finding 3).
func TestAWorkFolderSwappedForALinkIsRefused(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ConversationFolder)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes\n"), 0o600)
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	os.WriteFile(filepath.Join(home, ".gemini", "token"), []byte("the login"), 0o600)
	t.Cleanup(func() { checked = func(string, string) {} })
	checked = func(home, rel string) {
		if err := os.Rename(filepath.Join(home, rel), filepath.Join(home, "was")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(".gemini", filepath.Join(home, rel)); err != nil {
			t.Error(err)
		}
	}
	out := filepath.Join(t.TempDir(), FolderFile)
	if _, _, _, err := saveWithin(t, home, out); err == nil {
		gz, _ := os.ReadFile(out)
		_, data := entries(t, gz)
		t.Fatalf("a swapped folder was saved: %q", data)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was written: %v", err)
	}
}

// The fake's folder of the same files is the same tar on every turn, whatever second it is written
// in, as a real sandbox's unchanged files keep their times: a turn that changes nothing stores
// nothing (BUG-010).
func TestTheFakesFolderOfTheSameFilesIsTheSame(t *testing.T) {
	f := &Fake{Dir: t.TempDir()}
	r := &fakeRun{spec: RunSpec{KeepFolder: ConversationFolder}}
	files := map[string][]byte{"notes.md": []byte("notes\n"), "data/out.bin": {0xff, 0x00}}
	tarOf := func() []byte {
		out, note := f.folder(r, files)
		if out == "" {
			t.Fatalf("not saved: %s", note)
		}
		gz, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		return b
	}
	first := tarOf()
	time.Sleep(1100 * time.Millisecond)
	if !bytes.Equal(first, tarOf()) {
		t.Fatal("the same files written a second later make another tar")
	}
}

// openRegular opens a FIFO without waiting for a writer and refuses it, as an entry swapped for one
// after the walk listed it is opened: the walk's Lstat is not what keeps it from blocking.
func TestOpeningAFIFOSwappedInIsRefusedAtOnce(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Symlink("/etc/hostname", filepath.Join(dir, "linked"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"swapped", "linked"} {
		done := make(chan error, 1)
		go func() {
			f, _, err := openRegular(root, name)
			if f != nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, errNotRegular) {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("opening %s blocked", name)
		}
	}
}

// Over 200 MB of files before gzip, or 20000 entries, nothing is saved (D17). The large file is
// sparse: its size, not what it compresses to, is counted.
func TestAWorkFolderOverItsLimitsIsNotSaved(t *testing.T) {
	big := t.TempDir()
	os.MkdirAll(filepath.Join(big, ConversationFolder), 0o755)
	f, err := os.Create(filepath.Join(big, ConversationFolder, "big"))
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(201 << 20)
	f.Close()
	many := t.TempDir()
	os.MkdirAll(filepath.Join(many, ConversationFolder), 0o755)
	for i := range FolderMaxEntries + 1 {
		os.WriteFile(filepath.Join(many, ConversationFolder, fmt.Sprint(i)), nil, 0o644)
	}
	for name, home := range map[string]string{"201 MB": big, "20001 entries": many} {
		out := filepath.Join(t.TempDir(), FolderFile)
		if _, _, _, err := saveWithin(t, home, out); !errors.Is(err, ErrFolderLimit) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: something was written", name)
		}
		if _, err := os.Stat(out + ".part"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a part was left", name)
		}
	}
	// Exactly 200 MB is saved.
	f, _ = os.Create(filepath.Join(big, ConversationFolder, "big"))
	f.Truncate(200 << 20)
	f.Close()
	if _, size, _, err := saveIn(t, big, filepath.Join(t.TempDir(), FolderFile), 5*time.Minute); err != nil || size != 200<<20 {
		t.Fatalf("200 MB: %d %v", size, err)
	}
}

// tarOf makes a gzip tar of headers, each with as many zero bytes as its size.
func tarOf(t *testing.T, hs ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range hs {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := io.CopyN(tw, zeros{}, h.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// A work folder put back in a new sandbox is the one saved; a name that leaves it, a link, or more
// than 200 MB refuses the whole archive and leaves the folder empty; a setuid file is masked (D17).
func TestAWorkFolderPutBackIsCheckedAsItIsWritten(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, ConversationFolder, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, ConversationFolder, "sub/a.txt"), []byte("a\n"), 0o644)
	out := filepath.Join(t.TempDir(), FolderFile)
	if _, _, _, err := saveWithin(t, src, out); err != nil {
		t.Fatal(err)
	}
	gz, _ := os.ReadFile(out)
	home := t.TempDir()
	if err := PutFolder(home, ConversationFolder, gz); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ConversationFolder, "sub/a.txt")); err != nil || string(b) != "a\n" {
		t.Fatalf("put back: %q %v", b, err)
	}
	setuid := tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "s", Mode: 0o4755, Size: 1})
	home = t.TempDir()
	if err := PutFolder(home, ConversationFolder, setuid); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(home, ConversationFolder, "s")); err != nil || st.Mode() != 0o755 {
		t.Fatalf("setuid put back as %v %v", st.Mode(), err)
	}
	for name, gz := range map[string][]byte{
		"../x":   tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "ok", Mode: 0o644, Size: 1}, tar.Header{Typeflag: tar.TypeReg, Name: "../x", Mode: 0o644, Size: 1}),
		"/x":     tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "/x", Mode: 0o644, Size: 1}),
		"a link": tarOf(t, tar.Header{Typeflag: tar.TypeSymlink, Name: "l", Linkname: "/etc/passwd"}),
		"a FIFO": tarOf(t, tar.Header{Typeflag: tar.TypeFifo, Name: "f"}),
		"201 MB": tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "a", Mode: 0o644, Size: 101 << 20}, tar.Header{Typeflag: tar.TypeReg, Name: "b", Mode: 0o644, Size: 100 << 20}),
	} {
		home := t.TempDir()
		if err := PutFolder(home, ConversationFolder, gz); err == nil {
			t.Errorf("%s: put back", name)
		}
		left, err := os.ReadDir(filepath.Join(home, ConversationFolder))
		if err != nil || len(left) != 0 {
			t.Errorf("%s: left %v %v", name, left, err)
		}
		if _, err := os.Stat(filepath.Join(home, "x")); err == nil {
			t.Errorf("%s: wrote outside the folder", name)
		}
	}
}
