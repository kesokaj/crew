package runs

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

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/redact"
)

// workFolder is a conversation's work folder as it is stored (11.8): a gzip tar of its files after the
// secrets the sandbox was given were taken out of them (D18), sealed with the key file for its
// conversation (D41), with the sha256 of the tar, and its files and their bytes.
type workFolder struct {
	gz    []byte
	sum   string
	size  int64
	files int
}

// scrubFolder reads the gzip tar the harness wrote beside a sandbox's home and writes it again with
// each file's secrets removed by the run's own Redactor (D33): the values the broker gave the sandbox,
// and the shapes of secrets in a text. The harness wrote the archive, so it is read with the limits it
// was written with.
func scrubFolder(file string, r *redact.Redactor) (workFolder, error) {
	f, err := os.Open(file)
	if err != nil {
		return workFolder{}, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return workFolder{}, err
	}
	tr := tar.NewReader(zr)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	h := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(zw, h))
	var wf workFolder
	entries := 0
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return workFolder{}, err
		}
		if entries++; entries > harness.FolderMaxEntries {
			return workFolder{}, fmt.Errorf("%w: more than %d entries", harness.ErrFolderLimit, harness.FolderMaxEntries)
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: hd.Name, Mode: 0o755, ModTime: hd.ModTime}); err != nil {
				return workFolder{}, err
			}
		case tar.TypeReg:
			if hd.Size < 0 || wf.size+hd.Size > harness.FolderMaxBytes {
				return workFolder{}, fmt.Errorf("%w: more than %d MB of files", harness.ErrFolderLimit, harness.FolderMaxBytes>>20)
			}
			data, err := io.ReadAll(io.LimitReader(tr, hd.Size))
			if err != nil {
				return workFolder{}, err
			}
			data = r.File(data)
			// A removed value may be shorter than its marker: the limit holds after the scrub too.
			if wf.size+int64(len(data)) > harness.FolderMaxBytes {
				return workFolder{}, fmt.Errorf("%w: more than %d MB of files", harness.ErrFolderLimit, harness.FolderMaxBytes>>20)
			}
			mode := int64(0o644)
			if hd.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: hd.Name, Mode: mode, Size: int64(len(data)), ModTime: hd.ModTime}); err != nil {
				return workFolder{}, err
			}
			if _, err := tw.Write(data); err != nil {
				return workFolder{}, err
			}
			wf.size += int64(len(data))
			wf.files++
		default:
			return workFolder{}, fmt.Errorf("the work folder holds %q, which is not a folder or a regular file", hd.Name)
		}
	}
	if err := tw.Close(); err != nil {
		return workFolder{}, err
	}
	if err := zw.Close(); err != nil {
		return workFolder{}, err
	}
	wf.gz, wf.sum = buf.Bytes(), hex.EncodeToString(h.Sum(nil))
	return wf, nil
}

// keepFolder takes the work folder a turn left beside its sandbox's home: scrubbed, one at a time, and
// the file removed. A folder that could not be taken gives the note why instead.
func (d *Driver) keepFolder(lr *liveRun, end *harness.TurnEnd) (*workFolder, string) {
	if end.FolderPath == "" {
		return nil, end.FolderNote
	}
	defer os.Remove(end.FolderPath)
	// One folder is in memory at a time (D17).
	d.folderMu.Lock()
	defer d.folderMu.Unlock()
	wf, err := scrubFolder(end.FolderPath, lr.redact)
	if err == nil {
		// The scrub takes out only what it knows; what the agent wrote is sealed, so a login it copied
		// in another form is not kept in plain text in the database or its backups (rule 10, D41).
		wf.gz, err = d.sealFolder(lr.conversation, wf.gz)
	}
	if err != nil {
		d.Log.Warn("a conversation's work folder is not kept", "run", lr.id, "error", err)
		return nil, "the work folder of the last turn was not saved: " + err.Error()
	}
	return &wf, end.FolderNote
}

func folderPurpose(conv string) string { return "work-folder:" + conv }

// cliFilePurpose seals a CLI's file for its conversation and its path, so one moved to another does not
// open (plan of 2026-10-26, D4); the conversation is a uuid, so the colon cannot be read two ways.
func cliFilePurpose(conv, path string) string { return "cli-file:" + conv + ":" + path }

func (d *Driver) sealFolder(conv string, gz []byte) ([]byte, error) {
	if d.Seal == nil {
		return nil, errors.New("Crew has no key file to seal it with")
	}
	return d.Seal.Seal(folderPurpose(conv), gz)
}

// storeFolder writes a turn's work folder in tx when it changed, with the turn of the conversation it
// was saved after; a note alone says why the last one was not saved and keeps the one before (D17).
func storeFolder(ctx context.Context, tx pgx.Tx, conv string, wf *workFolder, note string) error {
	var turn int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) FROM turns WHERE conversation = $1`, conv).Scan(&turn); err != nil {
		return err
	}
	if wf == nil {
		if note == "" {
			return nil
		}
		empty, sum := emptyFolder()
		_, err := tx.Exec(ctx, `INSERT INTO work_folders (conversation, gzip, sha256, bytes, files, turn, note) VALUES ($1, $2, $3, 0, 0, NULL, $4)
			ON CONFLICT (conversation) DO UPDATE SET note = $4`, conv, empty, sum, note)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_folders (conversation, gzip, sha256, bytes, files, turn, note) VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''))
		ON CONFLICT (conversation) DO UPDATE SET gzip = $2, sha256 = $3, bytes = $4, files = $5, turn = $6, note = nullif($7, ''), saved_at = now()
		WHERE work_folders.sha256 <> $3 OR work_folders.note IS DISTINCT FROM nullif($7, '')`,
		conv, wf.gz, wf.sum, wf.size, wf.files, turn, note)
	return err
}

// emptyFolder is the stored form of an empty work folder.
func emptyFolder() ([]byte, string) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	h := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(zw, h))
	tw.Close()
	zw.Close()
	return buf.Bytes(), hex.EncodeToString(h.Sum(nil))
}

// folderOf is the stored work folder of a conversation, opened, to put back in a new sandbox; nil when
// it has none. One that does not open with this key file is not put back, and its note says so (D41).
func (d *Driver) folderOf(ctx context.Context, conv string) ([]byte, error) {
	var sealed []byte
	err := d.Pool.QueryRow(ctx, `SELECT gzip FROM work_folders WHERE conversation = $1 AND files > 0`, conv).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if d.Seal == nil {
		return nil, errors.New("Crew has no key file to open it with")
	}
	gz, err := d.Seal.Open(folderPurpose(conv), sealed)
	if err != nil {
		if _, uerr := d.Pool.Exec(ctx, `UPDATE work_folders SET note = $2 WHERE conversation = $1`, conv,
			"the saved work folder does not open with this key file, so it was not put back"); uerr != nil {
			d.Log.Warn("noting a work folder that does not open", "conversation", conv, "error", uerr)
		}
		return nil, err
	}
	return gz, nil
}

// OpenFolder returns the unsealed gzip tar of a conversation's saved work folder, or nil when none is saved.
func (d *Driver) OpenFolder(ctx context.Context, conv string) ([]byte, error) {
	return d.folderOf(ctx, conv)
}
