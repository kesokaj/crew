// Command crew-updater installs and rolls back releases of Crew (design 16.4, 16.5). It runs in its
// own container beside the server, as root there, with the Docker socket, and listens on
// /run/crew/updater.sock, which only root and the server's container can open (16.11).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/kesokaj/crew/internal/updater"
	"github.com/kesokaj/crew/internal/version"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("crew-updater stopped", "error", err)
		os.Exit(1)
	}
}

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, log *slog.Logger) error {
	data := get("CREW_DATA_DIR", "/var/lib/crew")
	etc := get("CREW_ETC", "/etc/crew")
	// The ids own the snapshots, and every microVM of the gate runs as them: never root's (since 0.53).
	gid, err := strconv.Atoi(os.Getenv("CREW_GID"))
	if err != nil || gid <= 0 {
		return errors.New("CREW_GID must be the group of the user crew, not root's")
	}
	uid, err := strconv.Atoi(os.Getenv("CREW_UID"))
	if err != nil || uid <= 0 {
		return errors.New("CREW_UID must be the user crew, not root's")
	}
	dir := filepath.Join(data, "updater")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sock := get("CREW_UPDATER_SOCKET", "/run/crew/updater.sock")
	// One updater at a time, before it reads a request or touches the socket (16.11, since 0.53): a
	// second one, also one started by docker exec in this container, would record a running request as
	// stopped and take the socket over. The lock is beside the socket, in root's folder.
	lock, err := updater.Exclusive(filepath.Dir(sock))
	if err != nil {
		return err
	}
	defer lock.Close()
	// The snapshots are written by crew admin, which runs as crew; the requests only the updater
	// writes.
	snapshots := filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(snapshots, 0o700); err != nil {
		return err
	}
	if err := os.Chown(snapshots, uid, gid); err != nil {
		return err
	}
	u := newUpdater(data, etc, uid, gid, log)
	if err := u.Begin(version.Version, os.Getenv("CREW_VERSION")); err != nil {
		return err
	}
	if err := u.Recover(ctx); err != nil {
		log.Error("recovering from a request that was cut off", "error", err)
	}
	log.Info("crew-updater is serving", "version", version.Version, "socket", sock)
	return u.Serve(ctx, sock, gid)
}

// newUpdater is the updater of the machine, with its folders under data and the install's under etc:
// the release folders it installs, its own folder, and the server's builds, which it loads (16.2
// step 4).
func newUpdater(data, etc string, uid, gid int, log *slog.Logger) *updater.Updater {
	dir := filepath.Join(data, "updater")
	return &updater.Updater{
		Machine: &updater.Compose{
			Lib:     get("CREW_LIB_DIR", "/usr/local/lib/crew"),
			Etc:     etc,
			Check:   filepath.Join(dir, "check"),
			Work:    filepath.Join(dir, "gate-e2e"),
			Runtime: os.Getenv("CREW_RUNTIME"),
			UID:     uid,
			GID:     gid,
			Env: []string{"CREW_UID=" + os.Getenv("CREW_UID"), "CREW_GID=" + os.Getenv("CREW_GID"),
				"CREW_DOCKER_GID=" + os.Getenv("CREW_DOCKER_GID"), "CREW_ETC=" + etc, "CREW_DATA_DIR=" + data,
				"CREW_UPDATER_VERSION=" + version.Version},
			Run: updater.ExecRun,
		},
		Releases:     filepath.Join(data, "releases"),
		Dir:          dir,
		Builds:       filepath.Join(data, "builds"),
		MachineSetup: func() (int, error) { return machineSetup(filepath.Join(etc, "install.json")) },
		Keep:         5,
		RequireRoot:  true,
		Log:          log,
	}
}

// machineSetup is the number of the machine setup install.json records (16.4).
func machineSetup(path string) (int, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var rec struct {
		MachineSetup string `json:"machine_setup"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(rec.MachineSetup)
	if err != nil {
		return 0, fmt.Errorf("install.json: machine_setup %q", rec.MachineSetup)
	}
	return n, nil
}
