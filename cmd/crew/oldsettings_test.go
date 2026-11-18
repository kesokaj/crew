package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The server copied from styrman-crew kept its idle timeout and the Docker disk of a project in
// settings.json in its data folder, and read CREW_IDLE_TIMEOUT and CREW_DOCKER_DISK_GB. Crew reads
// none of them (design 17.2, since 0.85): a folder or an install file that still holds them, even with
// values the old code refused, changes nothing of what the server starts sandboxes with, and the file
// stays as it was.
func TestAnOldSettingsFileAndItsVariablesChangeNothing(t *testing.T) {
	t.Setenv("CREW_RUNTIME_IMAGE", "crew-runtime:0.0.0")
	t.Setenv("CREW_SELF_CONTAINER", "crew-server")
	clean := t.TempDir()
	t.Setenv("CREW_DATA_DIR", clean)
	want := orchConfig(loadEnv())

	old := t.TempDir()
	stored := []byte(`{"dockerDiskGB": 1, "idleTimeout": "not a duration", "userMaxCpus": -3}` + "\n")
	path := filepath.Join(old, "settings.json")
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREW_DATA_DIR", old)
	t.Setenv("CREW_IDLE_TIMEOUT", "not a duration")
	t.Setenv("CREW_DOCKER_DISK_GB", "1")
	t.Setenv("CREW_VCPUS", "2")

	e := loadEnv()
	if e.DataDir != old {
		t.Fatalf("the data folder is %q, not the one with the old file", e.DataDir)
	}
	if got := orchConfig(e); !reflect.DeepEqual(got, want) {
		t.Fatalf("the old settings changed the sandboxes' config:\n got %+v\nwant %+v", *got, *want)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(stored) {
		t.Fatalf("settings.json was changed or removed: %q, %v", after, err)
	}
}
