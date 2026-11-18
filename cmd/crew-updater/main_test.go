package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/updater"
)

// The updater of the machine installs only root's release folders, and loads the builds of the
// server's folder, beside them in the data folder, which Compose mounts at the same path (16.2 step
// 4, 16.11).
func TestTheUpdatersFolders(t *testing.T) {
	t.Setenv("CREW_RUNTIME", "kata-qemu")
	u := newUpdater("/var/lib/crew", "/etc/crew", 999, 998, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if u.Releases != "/var/lib/crew/releases" || u.Dir != "/var/lib/crew/updater" || u.Builds != "/var/lib/crew/builds" || !u.RequireRoot {
		t.Fatalf("%+v", u)
	}
	// The gate's browser tests are unpacked in a folder of the updater's own, and its microVMs start
	// with the machine's runtime for them (16.2 step 5).
	c, ok := u.Machine.(*updater.Compose)
	if !ok || c.Work != "/var/lib/crew/updater/gate-e2e" || c.Runtime != "kata-qemu" || c.Check != "/var/lib/crew/updater/check" {
		t.Fatalf("%+v", u.Machine)
	}
}

// startIn sets what run reads for an updater whose data folder is under dir, as the user crew of the
// test, and gives the socket's path.
func startIn(t *testing.T, dir string) string {
	t.Helper()
	sock := filepath.Join(dir, "updater.sock")
	t.Setenv("CREW_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CREW_ETC", filepath.Join(dir, "etc"))
	t.Setenv("CREW_UPDATER_SOCKET", sock)
	t.Setenv("CREW_UID", strconv.Itoa(os.Getuid()))
	t.Setenv("CREW_GID", strconv.Itoa(os.Getgid()))
	return sock
}

// A second crew-updater, also one started by docker exec in the updater's container, stops before it
// reads a request or touches the socket (16.11, since 0.53). On dave, `crew-updater --version` in the
// updater's container started a second updater: it took the socket over and removed it when it was
// stopped, and its start would have recorded a running request as stopped.
func TestASecondUpdaterStopsBeforeItTouchesAnything(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("runs as the user crew, not root")
	}
	dir := t.TempDir()
	sock := startIn(t, dir)
	own := filepath.Join(dir, "data", "updater")
	if err := os.MkdirAll(filepath.Join(own, "requests"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The first updater: its socket, its state, a request that runs, and its lock.
	files := map[string]string{
		sock:                                     "the first updater's socket",
		filepath.Join(own, "state.json"):         `{"running":"0.2.16","updater":"0.2.16"}`,
		filepath.Join(own, "requests", "7.json"): `{"id":7,"version":"0.2.17","action":"install","state":"running","step":5}`,
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The lock is beside the socket, in root's folder (security review of 0.53).
	first, err := updater.Exclusive(filepath.Dir(sock))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Were it to serve, it would serve until this ends.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "another crew-updater runs") {
		t.Fatalf("the second updater ran: %v", err)
	}
	for p, body := range files {
		got, rerr := os.ReadFile(p)
		if rerr != nil || string(got) != body {
			t.Fatalf("the second updater changed %s: %q %v", p, got, rerr)
		}
	}
	// Once the first has stopped, an updater takes the lock.
	first.Close()
	again, err := updater.Exclusive(filepath.Dir(sock))
	if err != nil {
		t.Fatalf("the lock outlived its updater: %v", err)
	}
	again.Close()
}

// The updater refuses root's ids for the user crew (16.11, since 0.53): they go to the snapshots'
// owner and to the user every microVM of the gate runs as. It refuses before it makes or reads anything.
func TestTheUpdaterRefusesRootForTheUserCrew(t *testing.T) {
	for _, ids := range [][2]string{{"0", "998"}, {"999", "0"}, {"-1", "998"}, {"999", "-1"}} {
		dir := t.TempDir()
		startIn(t, dir)
		t.Setenv("CREW_UID", ids[0])
		t.Setenv("CREW_GID", ids[1])
		err := run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "must be the") || !strings.Contains(err.Error(), "not root's") {
			t.Fatalf("%v: %v", ids, err)
		}
		if _, serr := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(serr) {
			t.Fatalf("%v: the data folder was made: %v", ids, serr)
		}
	}
}
