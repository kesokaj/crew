package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pid = "0f8fad5b-d9cb-469f-a165-70867728950e"

// writeHosted writes a .tar.gz of the hosted folder as a backup does, and gives its path and what
// was noted.
func writeHosted(t *testing.T, hosted string) (string, []string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	f, _ := os.Create(file)
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	var notes []string
	if err := addHosted(tw, hosted, t.TempDir(), func(s string) { notes = append(notes, s) }); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	zw.Close()
	f.Close()
	return file, notes
}

func names(t *testing.T, file string) []string {
	f, _ := os.Open(file)
	defer f.Close()
	zr, _ := gzip.NewReader(f)
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

// The backup holds each hosted project's data/ and never its copies or its logs, nor a link
// (plan M19 D18).
func TestTheBackupHoldsEachHostedProjectsData(t *testing.T) {
	hosted := t.TempDir()
	for _, p := range []string{"data/db/main.db", "copies/3/main.db", "logs/2026-10-25.log"} {
		os.MkdirAll(filepath.Join(hosted, pid, filepath.Dir(p)), 0o750)
		os.WriteFile(filepath.Join(hosted, pid, p), []byte("x"), 0o640)
	}
	os.Symlink("/etc/shadow", filepath.Join(hosted, pid, "data", "link"))
	os.MkdirAll(filepath.Join(hosted, "not-a-project", "data"), 0o750)
	file, notes := writeHosted(t, hosted)
	got := strings.Join(names(t, file), ",")
	if got != "hosted/"+pid+"/data/,hosted/"+pid+"/data/db/,hosted/"+pid+"/data/db/main.db" || len(notes) != 0 {
		t.Fatalf("the backup holds %s (%v)", got, notes)
	}
}

// A file whose size changes while the backup reads it is left out with a note, and the backup ends
// well.
func TestAFileThatChangesWhileReadIsLeftOut(t *testing.T) {
	hosted := t.TempDir()
	data := filepath.Join(hosted, pid, "data")
	os.MkdirAll(data, 0o750)
	os.WriteFile(filepath.Join(data, "a.db"), []byte("aaaa"), 0o640)
	os.WriteFile(filepath.Join(data, "b.db"), []byte("bbbb"), 0o640)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var notes []string
	err := addTreeTolerant(tw, "hosted/"+pid+"/data", data, t.TempDir(), func(s string) { notes = append(notes, s) })
	if err != nil {
		t.Fatal(err)
	}
	// Played: a.db is truncated between its stat and its copy.
	f, _ := os.Open(filepath.Join(data, "a.db"))
	defer f.Close()
	aside, ok, err := copyAside(f, 10, t.TempDir())
	if ok || err != nil || aside != nil {
		t.Fatalf("a file shorter than its stat was taken: %v %v", ok, err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes of a steady folder: %v", notes)
	}
}

// A file the disk has no room for, beside the margin the database needs, is left out with a note, and
// the backup goes on (security review of M19).
func TestAFileWithNoRoomIsLeftOut(t *testing.T) {
	old := freeSpace
	freeSpace = func(string) (uint64, error) { return hostedMargin + 2, nil }
	t.Cleanup(func() { freeSpace = old })
	hosted := t.TempDir()
	data := filepath.Join(hosted, pid, "data")
	os.MkdirAll(data, 0o750)
	os.WriteFile(filepath.Join(data, "small"), []byte("a"), 0o640)
	os.WriteFile(filepath.Join(data, "big"), []byte("abcdef"), 0o640)
	file, notes := writeHosted(t, hosted)
	got := strings.Join(names(t, file), ",")
	if !strings.Contains(got, "data/small") || strings.Contains(got, "data/big") || len(notes) != 1 || !strings.Contains(notes[0], "big") {
		t.Fatalf("the backup holds %s, notes %v", got, notes)
	}
}

// The restore puts a project's data back only where its data/ is absent or empty, and never writes
// through a link.
func TestTheRestoreWritesOnlyAnEmptyData(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, pid, "data", "db"), 0o750)
	os.WriteFile(filepath.Join(src, pid, "data", "db", "main.db"), []byte("from the backup"), 0o640)
	file, _ := writeHosted(t, src)

	absent := t.TempDir()
	if err := restoreHosted(file, absent); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(absent, pid, "data", "db", "main.db")); string(b) != "from the backup" {
		t.Fatalf("absent: %q", b)
	}
	kept := t.TempDir()
	os.MkdirAll(filepath.Join(kept, pid, "data"), 0o750)
	os.WriteFile(filepath.Join(kept, pid, "data", "now.db"), []byte("now"), 0o640)
	if err := restoreHosted(file, kept); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(kept, pid, "data", "db")); err == nil {
		t.Fatal("a data folder with files was written")
	}
	linked, outside := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(linked, pid), 0o750)
	os.Symlink(outside, filepath.Join(linked, pid, "data"))
	err := restoreHosted(file, linked)
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the restore wrote through a link (%v)", err)
	}
}

// A folder the app swaps for a link between the listing and the walk into it leads nowhere out of the
// project's data/, and a file that went after the listing is left out with a note (security review of
// M19, and round 2).
func TestTheWalkOfHostedDataIsNotLedOut(t *testing.T) {
	hosted, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("not the app's"), 0o640)
	data := filepath.Join(hosted, pid, "data")
	os.MkdirAll(filepath.Join(data, "sub"), 0o750)
	os.WriteFile(filepath.Join(data, "sub", "a"), []byte("a"), 0o640)
	os.WriteFile(filepath.Join(data, "gone"), []byte("g"), 0o640)
	old := walked
	walked = func(folder, rel string) {
		switch rel {
		case "sub":
			os.RemoveAll(filepath.Join(folder, "sub"))
			if err := os.Symlink(outside, filepath.Join(folder, "sub")); err != nil {
				t.Fatal(err)
			}
		case "gone":
			os.Remove(filepath.Join(folder, "gone"))
		}
	}
	t.Cleanup(func() { walked = old })
	file, notes := writeHosted(t, hosted)
	got := strings.Join(names(t, file), ",")
	if strings.Contains(got, "secret") || strings.Contains(got, "data/gone") {
		t.Fatalf("the backup holds %s", got)
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, " "), "gone") {
		t.Fatalf("notes %v", notes)
	}
}

// A project whose data/ cannot be read is left out with a note, and the backup of the others goes on
// (security review of M19, round 2).
func TestAnUnreadableDataIsLeftOut(t *testing.T) {
	hosted := t.TempDir()
	const other = "1f8fad5b-d9cb-469f-a165-70867728950e"
	for _, p := range []string{pid, other} {
		os.MkdirAll(filepath.Join(hosted, p, "data"), 0o750)
		os.WriteFile(filepath.Join(hosted, p, "data", "main.db"), []byte("x"), 0o640)
	}
	locked := filepath.Join(hosted, pid, "data")
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o750) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("this user reads a folder of mode 000")
	}
	file, notes := writeHosted(t, hosted)
	got := strings.Join(names(t, file), ",")
	if !strings.Contains(got, other+"/data/main.db") || strings.Contains(got, pid+"/data/main.db") || len(notes) != 1 {
		t.Fatalf("the backup holds %s, notes %v", got, notes)
	}
}

// A file is on the disk twice while it is backed up, aside and in the archive: the room is needed
// twice (security review of M19, round 2).
func TestAFileNeedsRoomTwice(t *testing.T) {
	old := freeSpace
	freeSpace = func(string) (uint64, error) { return hostedMargin + 6, nil }
	t.Cleanup(func() { freeSpace = old })
	hosted := t.TempDir()
	data := filepath.Join(hosted, pid, "data")
	os.MkdirAll(data, 0o750)
	os.WriteFile(filepath.Join(data, "six"), []byte("abcdef"), 0o640)
	os.WriteFile(filepath.Join(data, "three"), []byte("abc"), 0o640)
	file, notes := writeHosted(t, hosted)
	got := strings.Join(names(t, file), ",")
	if !strings.Contains(got, "data/three") || strings.Contains(got, "data/six") || len(notes) != 1 {
		t.Fatalf("the backup holds %s, notes %v", got, notes)
	}
}

// A link the app puts into its empty data/ after the restore looked leads nowhere out of it, not into
// another project's data (security review of M19, round 2).
func TestTheRestoreWritesThroughTheProjectsOwnData(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, pid, "data", "x"), 0o750)
	os.WriteFile(filepath.Join(src, pid, "data", "x", "planted"), []byte("from the backup"), 0o640)
	file, _ := writeHosted(t, src)

	hosted := t.TempDir()
	const other = "1f8fad5b-d9cb-469f-a165-70867728950e"
	os.MkdirAll(filepath.Join(hosted, pid, "data"), 0o750)
	os.MkdirAll(filepath.Join(hosted, other, "data"), 0o750)
	os.WriteFile(filepath.Join(hosted, other, "data", "keep"), []byte("theirs"), 0o640)
	old := restoring
	restoring = func(data string) {
		if err := os.Symlink("../../"+other+"/data", filepath.Join(data, "x")); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { restoring = old })
	restoreHosted(file, hosted)
	if _, err := os.Stat(filepath.Join(hosted, other, "data", "planted")); err == nil {
		t.Fatal("the restore wrote into another project's data")
	}
}

// A backup with no hosted project's data, such as the updater's snapshot, writes nothing where hosted
// data goes: the gate's copy restores it in a microVM whose root cannot be written (BUG-006).
func TestARestoreWithNoHostedDataWritesNothingThere(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap.tar.gz")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: entryHosted, Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	zw.Close()
	f.Close()
	// Nothing can be made under a plain file, whoever runs the test.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := restoreHosted(file, filepath.Join(blocked, "hosted")); err != nil {
		t.Fatalf("a restore with no hosted data tried to write there: %v", err)
	}
}
