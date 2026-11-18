package sandboxes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
)

// Sealer seals and opens snapshot archives with the machine's key file (`/etc/crew/secret.key`, R169).
type Sealer interface {
	Seal(purpose string, data []byte) ([]byte, error)
	Open(purpose string, blob []byte) ([]byte, error)
}

// ProjectSnapshot is a row in `project_snapshots`.
type ProjectSnapshot struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Label       string    `json:"label"`
	ArchivePath string    `json:"archivePath"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	ImageTag    string    `json:"imageTag"`
	CreatedAt   time.Time `json:"createdAt"`
}

// SnapshotMaxBytes bounds one snapshot's uncompressed file contents (512 MB).
const SnapshotMaxBytes = 512 << 20

// SnapshotMaxEntries bounds the number of entries in one snapshot archive.
const SnapshotMaxEntries = 50000

// ErrSnapshotChecksum is returned when a snapshot archive on disk does not match its recorded sha256.
var ErrSnapshotChecksum = errors.New("snapshot archive checksum mismatch")

// DefaultMaxSnapshots is the default per-project snapshot retention cap (`sandbox.max_snapshots`, 14.3).
const DefaultMaxSnapshots = 5

func snapshotPurpose(projectID, snapID string) string {
	return "project-snapshot:" + projectID + ":" + snapID
}

// isCredentialRelPath returns true for relative paths inside `/home/agent` that hold live credentials,
// ephemeral per-run worktrees/control state, or guest runtime caches (such as the 20 GB sparse
// `.dockerd` loopback image and `.crew/browser` profile) that must never be included in a snapshot
// archive (Safety Rule 10, R169).
func isCredentialRelPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	loginFile := filepath.ToSlash((cli.Agy{}).LoginFile())
	if rel == ".crew/token" || rel == ".crew/creds.env" ||
		strings.HasPrefix(rel, ".crew/turn-") ||
		rel == ".crew/runs" || strings.HasPrefix(rel, ".crew/runs/") ||
		rel == ".crew/browser" || strings.HasPrefix(rel, ".crew/browser/") ||
		rel == ".dockerd" || strings.HasPrefix(rel, ".dockerd/") ||
		rel == "worktrees" || strings.HasPrefix(rel, "worktrees/") ||
		rel == "repo.git/worktrees" || strings.HasPrefix(rel, "repo.git/worktrees/") ||
		rel == loginFile ||
		rel == ".git-credentials" ||
		strings.HasPrefix(rel, ".config/gh/") ||
		strings.HasPrefix(rel, ".ssh/") {
		return true
	}
	return false
}

// SnapshotProjectHome creates a credential-stripped, secret-key-sealed `.tar.gz` snapshot of the
// project's persistent home directory (`<CREW_DATA_DIR>/projects/<id>/home`), records its `sha256`
// in `project_snapshots`, and updates `project_sandboxes.snapshot_id` (design 1.37, 13.3, R169).
func (m *Manager) SnapshotProjectHome(ctx context.Context, projectID, label string, seal Sealer, redactFn func([]byte) []byte) (ProjectSnapshot, error) {
	if projectID == "" {
		return ProjectSnapshot{}, errors.New("projectID is required")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "snapshot-" + time.Now().UTC().Format("20060102-150405")
	}
	if len(label) > 240 {
		label = label[:240]
	}

	unlock := lockProject(projectID)
	defer unlock()

	ps, err := m.GetProjectSandbox(ctx, projectID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if ps.ActiveSessions > 0 {
		return ProjectSnapshot{}, fmt.Errorf("%w (%d active)", ErrProjectSandboxBusy, ps.ActiveSessions)
	}
	if ps.State == StateRunning && ps.SandboxID != "" {
		if pr, ok := m.Runtime.(PausableRuntime); ok {
			if err := pr.PauseSandbox(ctx, ps.SandboxID); err != nil {
				return ProjectSnapshot{}, fmt.Errorf("pausing project sandbox for snapshot: %w", err)
			}
			defer func() {
				_ = pr.UnpauseSandbox(context.WithoutCancel(ctx), ps.SandboxID)
			}()
		}
	}
	home := m.ProjectHome(projectID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return ProjectSnapshot{}, err
	}
	snapDir := m.ProjectSnapshotsDir(projectID)
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		return ProjectSnapshot{}, err
	}

	snapID, err := newID("snap")
	if err != nil {
		return ProjectSnapshot{}, err
	}

	root, err := os.OpenRoot(home)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	defer root.Close()

	var rawTarGz bytes.Buffer
	zw := gzip.NewWriter(&rawTarGz)
	tw := tar.NewWriter(zw)

	var totalBytes int64
	entries := 0
	var walkRoot func(relDir string) error
	walkRoot = func(relDir string) error {
		openTarget := "."
		if relDir != "" {
			openTarget = filepath.FromSlash(relDir)
		}
		dirFile, err := root.OpenFile(openTarget, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		dirEntries, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			return readErr
		}
		slices.SortFunc(dirEntries, func(a, b os.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
		for _, d := range dirEntries {
			name := d.Name()
			relSlash := name
			if relDir != "" {
				relSlash = relDir + "/" + name
			}
			if isCredentialRelPath(relSlash) {
				continue
			}
			info, err := root.Lstat(filepath.FromSlash(relSlash))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			modeType := info.Mode()
			// Skip symlinks, hard-linked regular files (which could alias a stripped credential file),
			// and special files (sockets, fifos, devices).
			if modeType&os.ModeSymlink != 0 || (!info.IsDir() && !modeType.IsRegular()) {
				continue
			}
			if modeType.IsRegular() {
				if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
					continue
				}
			}
			entries++
			if entries > SnapshotMaxEntries {
				return fmt.Errorf("snapshot exceeds %d entries", SnapshotMaxEntries)
			}
			if info.IsDir() {
				if err := tw.WriteHeader(&tar.Header{
					Typeflag: tar.TypeDir,
					Name:     relSlash + "/",
					Mode:     0o755,
					ModTime:  info.ModTime(),
				}); err != nil {
					return err
				}
				if err := walkRoot(relSlash); err != nil {
					return err
				}
				continue
			}
			if info.Size() < 0 || totalBytes+info.Size() > SnapshotMaxBytes {
				return fmt.Errorf("snapshot exceeds %d MB", SnapshotMaxBytes>>20)
			}
			rf, err := root.OpenFile(filepath.FromSlash(relSlash), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			finfo, statErr := rf.Stat()
			if statErr != nil {
				_ = rf.Close()
				return statErr
			}
			if !finfo.Mode().IsRegular() {
				_ = rf.Close()
				continue
			}
			if finfo.Size() < 0 || totalBytes+finfo.Size() > SnapshotMaxBytes {
				_ = rf.Close()
				return fmt.Errorf("snapshot exceeds %d MB", SnapshotMaxBytes>>20)
			}
			data, readErr := io.ReadAll(io.LimitReader(rf, SnapshotMaxBytes-totalBytes+1))
			_ = rf.Close()
			if readErr != nil {
				return readErr
			}
			if int64(len(data)) > SnapshotMaxBytes-totalBytes {
				return fmt.Errorf("snapshot exceeds %d MB", SnapshotMaxBytes>>20)
			}
			if redactFn != nil {
				data = redactFn(data)
			}
			totalBytes += int64(len(data))
			if totalBytes > SnapshotMaxBytes {
				return fmt.Errorf("snapshot exceeds %d MB", SnapshotMaxBytes>>20)
			}
			mode := int64(0o644)
			if finfo.Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg,
				Name:     relSlash,
				Mode:     mode,
				Size:     int64(len(data)),
				ModTime:  finfo.ModTime(),
			}); err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		return nil
	}
	if walkErr := walkRoot(""); walkErr != nil {
		return ProjectSnapshot{}, walkErr
	}
	if err := tw.Close(); err != nil {
		return ProjectSnapshot{}, err
	}
	if err := zw.Close(); err != nil {
		return ProjectSnapshot{}, err
	}

	payload := rawTarGz.Bytes()
	if seal != nil {
		payload, err = seal.Seal(snapshotPurpose(projectID, snapID), payload)
		if err != nil {
			return ProjectSnapshot{}, fmt.Errorf("sealing project snapshot: %w", err)
		}
	}
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	archivePath := filepath.Join(snapDir, snapID+".tar.gz.sealed")
	if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
		return ProjectSnapshot{}, err
	}

	var snap ProjectSnapshot
	err = pgx.BeginFunc(ctx, m.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO project_snapshots (id, project_id, label, archive_path, sha256, size_bytes, image_tag)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7)
			RETURNING id, project_id::text, label, archive_path, sha256, size_bytes, image_tag, created_at`,
			snapID, projectID, label, archivePath, sumHex, int64(len(payload)), ps.ImageTag).Scan(
			&snap.ID, &snap.ProjectID, &snap.Label, &snap.ArchivePath, &snap.SHA256, &snap.SizeBytes, &snap.ImageTag, &snap.CreatedAt,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_sandboxes (project_id, sandbox_id, state, home_path, image_tag, snapshot_id, last_active_at, updated_at)
			VALUES ($1::uuid, '', $2, $3, $4, $5, now(), now())
			ON CONFLICT (project_id) DO UPDATE
			SET snapshot_id = EXCLUDED.snapshot_id,
			    updated_at = now()`,
			projectID, StateStopped, home, ps.ImageTag, snapID)
		return err
	})
	if err != nil {
		_ = os.Remove(archivePath)
		return ProjectSnapshot{}, err
	}
	m.pruneProjectSnapshots(ctx, projectID, m.ProjectConfigFor(ctx, projectID).MaxSnapshots)
	return snap, nil
}

func (m *Manager) pruneProjectSnapshots(ctx context.Context, projectID string, maxKeep int) {
	if maxKeep <= 0 {
		return
	}
	rows, err := m.Pool.Query(ctx, `SELECT id, archive_path FROM project_snapshots
		WHERE project_id = $1::uuid
		ORDER BY created_at DESC, id DESC
		OFFSET $2`, projectID, maxKeep)
	if err != nil {
		return
	}
	type stale struct {
		id   string
		path string
	}
	old, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stale, error) {
		var s stale
		return s, r.Scan(&s.id, &s.path)
	})
	if err != nil {
		return
	}
	for _, s := range old {
		_, _ = m.Pool.Exec(ctx, `DELETE FROM project_snapshots WHERE id = $1 AND project_id = $2::uuid`, s.id, projectID)
		if s.path != "" {
			_ = os.Remove(s.path)
		}
	}
}

// RestoreProjectHome verifies the snapshot's `sha256` and secret-key seal before touching the existing
// project home, stops the project microVM if running, unpacks into a staging folder, and swaps it in.
func (m *Manager) RestoreProjectHome(ctx context.Context, projectID, snapshotID string, seal Sealer) (ProjectSandbox, error) {
	var snap ProjectSnapshot
	err := m.Pool.QueryRow(ctx, `SELECT id, project_id::text, label, archive_path, sha256, size_bytes, image_tag, created_at
		FROM project_snapshots WHERE id = $1 AND project_id = $2::uuid`, snapshotID, projectID).Scan(
		&snap.ID, &snap.ProjectID, &snap.Label, &snap.ArchivePath, &snap.SHA256, &snap.SizeBytes, &snap.ImageTag, &snap.CreatedAt,
	)
	if err != nil {
		return ProjectSandbox{}, err
	}

	payload, err := os.ReadFile(snap.ArchivePath)
	if err != nil {
		return ProjectSandbox{}, err
	}
	gotSum := sha256.Sum256(payload)
	if hex.EncodeToString(gotSum[:]) != snap.SHA256 {
		return ProjectSandbox{}, ErrSnapshotChecksum
	}
	if seal != nil {
		payload, err = seal.Open(snapshotPurpose(projectID, snapshotID), payload)
		if err != nil {
			return ProjectSandbox{}, fmt.Errorf("opening sealed snapshot: %w", err)
		}
	}

	// Unpack into a sibling staging folder first so a corrupted tar never damages the current home.
	root := m.ProjectRoot(projectID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ProjectSandbox{}, err
	}
	stageDir, err := os.MkdirTemp(root, "home-restore-*")
	if err != nil {
		return ProjectSandbox{}, err
	}
	defer os.RemoveAll(stageDir)

	zr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return ProjectSandbox{}, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)

	var totalBytes int64
	entries := 0
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ProjectSandbox{}, err
		}
		entries++
		if entries > SnapshotMaxEntries {
			return ProjectSandbox{}, fmt.Errorf("snapshot exceeds %d entries", SnapshotMaxEntries)
		}
		cleanRel := filepath.Clean(filepath.FromSlash(hd.Name))
		if cleanRel == "." || strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			return ProjectSandbox{}, fmt.Errorf("unsafe path %q in snapshot", hd.Name)
		}
		if isCredentialRelPath(filepath.ToSlash(cleanRel)) {
			continue
		}
		target := filepath.Join(stageDir, cleanRel)
		if !strings.HasPrefix(target, stageDir+string(filepath.Separator)) {
			return ProjectSandbox{}, fmt.Errorf("path %q escapes staging directory", hd.Name)
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return ProjectSandbox{}, err
			}
		case tar.TypeReg:
			if hd.Size < 0 || totalBytes+hd.Size > SnapshotMaxBytes {
				return ProjectSandbox{}, fmt.Errorf("snapshot exceeds %d MB", SnapshotMaxBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return ProjectSandbox{}, err
			}
			data, err := io.ReadAll(io.LimitReader(tr, hd.Size))
			if err != nil {
				return ProjectSandbox{}, err
			}
			totalBytes += int64(len(data))
			mode := os.FileMode(0o644)
			if hd.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := os.WriteFile(target, data, mode); err != nil {
				return ProjectSandbox{}, err
			}
		default:
			return ProjectSandbox{}, fmt.Errorf("unsupported entry type in snapshot: %q", hd.Name)
		}
	}

	// Stop the VM if running before swapping the persistent home directory.
	if _, err := m.StopProjectSandbox(ctx, projectID); err != nil {
		return ProjectSandbox{}, err
	}

	home := m.ProjectHome(projectID)
	oldHome := home + ".old-" + snap.ID
	_ = os.RemoveAll(oldHome)
	if _, err := os.Stat(home); err == nil {
		if err := os.Rename(home, oldHome); err != nil {
			return ProjectSandbox{}, err
		}
	}
	if err := os.Rename(stageDir, home); err != nil {
		_ = os.Rename(oldHome, home)
		return ProjectSandbox{}, err
	}
	if fi, err := os.Lstat(filepath.Join(oldHome, ".dockerd")); err == nil && fi.IsDir() {
		_ = os.Rename(filepath.Join(oldHome, ".dockerd"), filepath.Join(home, ".dockerd"))
	}
	_ = os.RemoveAll(oldHome)

	var ps ProjectSandbox
	err = m.Pool.QueryRow(ctx, `UPDATE project_sandboxes
		SET snapshot_id = $2,
		    image_tag = CASE WHEN $3 <> '' THEN $3 ELSE image_tag END,
		    updated_at = now()
		WHERE project_id = $1::uuid
		RETURNING project_id::text, sandbox_id, state, home_path, image_tag, snapshot_id, active_sessions, last_active_at, updated_at`,
		projectID, snapshotID, snap.ImageTag).Scan(
		&ps.ProjectID, &ps.SandboxID, &ps.State, &ps.HomePath, &ps.ImageTag, &ps.SnapshotID,
		&ps.ActiveSessions, &ps.LastActiveAt, &ps.UpdatedAt,
	)
	return ps, err
}

// ListProjectSnapshots returns the project's snapshots, newest first.
func (m *Manager) ListProjectSnapshots(ctx context.Context, projectID string) ([]ProjectSnapshot, error) {
	rows, err := m.Pool.Query(ctx, `SELECT id, project_id::text, label, archive_path, sha256, size_bytes, image_tag, created_at
		FROM project_snapshots WHERE project_id = $1::uuid ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProjectSnapshot, error) {
		var s ProjectSnapshot
		err := r.Scan(&s.ID, &s.ProjectID, &s.Label, &s.ArchivePath, &s.SHA256, &s.SizeBytes, &s.ImageTag, &s.CreatedAt)
		return s, err
	})
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ProjectSnapshot{}
	}
	return list, nil
}
