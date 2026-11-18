package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/blobstore"
	"github.com/kesokaj/crew/internal/db/sqlite"
	"github.com/kesokaj/crew/internal/redact"
)

func (s *Server) offloadTrajectoryBlob(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	runID := strings.TrimSpace(r.PathValue("run"))
	var exists bool
	if err := b.Pool.QueryRow(ctx, `SELECT exists (
		SELECT 1 FROM runs r JOIN subtasks st ON st.id = r.subtask WHERE r.id = $1::uuid AND st.project = $2
	)`, runID, p.ID).Scan(&exists); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid run id")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run does not belong to this project")
		return
	}
	if err := s.syncProjectTrajectories(ctx, b.Pool, s.ensureWAL(), p); err != nil {
		s.boardError(w, "indexing run before offload", err)
		return
	}
	rows, err := b.Pool.Query(ctx, `SELECT seq, at, type, payload FROM events WHERE run = $1::uuid ORDER BY seq ASC`, runID)
	if err != nil {
		s.boardError(w, "reading run events", err)
		return
	}
	var buf bytes.Buffer
	var lines int
	red := redact.New()
	for rows.Next() {
		var seq int64
		var at time.Time
		var typ string
		var payload json.RawMessage
		if ser := rows.Scan(&seq, &at, &typ, &payload); ser != nil {
			rows.Close()
			s.boardError(w, "scanning run event", ser)
			return
		}
		rec, _ := json.Marshal(map[string]any{
			"seq":     seq,
			"at":      at.UTC(),
			"type":    typ,
			"payload": json.RawMessage(red.Text(string(payload))),
		})
		buf.Write(rec)
		buf.WriteByte('\n')
		lines++
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		s.boardError(w, "iterating run events", rerr)
		return
	}
	if lines == 0 {
		rec, _ := json.Marshal(map[string]any{"runId": runID, "project": p.Name, "empty": true})
		buf.Write(rec)
		buf.WriteByte('\n')
	}

	compressed, contentType, err := blobstore.CompressArchive(buf.Bytes())
	if err != nil {
		s.boardError(w, "compressing trajectory archive", err)
		return
	}
	cfg := s.readStorageConfig(ctx, b.Pool)
	st := s.activeBlobStore(cfg.BlobDriver, cfg.GCSBucket)
	key := fmt.Sprintf("trajectories/%s/%s.jsonl.zst", p.Name, runID)
	meta, err := st.Put(ctx, key, compressed, contentType)
	if err != nil {
		s.boardError(w, "writing trajectory archive to BlobStore", err)
		return
	}
	if _, err := b.Pool.Exec(ctx, `INSERT INTO blob_objects (blob_key, project_id, run_id, kind, driver, sha256, size_bytes, content_type, created_at)
		VALUES ($1, $2, $3::uuid, 'trajectory', $4, $5, $6, $7, $8)
		ON CONFLICT (blob_key) DO UPDATE SET
			driver = EXCLUDED.driver,
			sha256 = EXCLUDED.sha256,
			size_bytes = EXCLUDED.size_bytes,
			content_type = EXCLUDED.content_type`,
		meta.Key, p.ID, runID, meta.Driver, meta.SHA256, meta.SizeBytes, meta.ContentType, meta.CreatedAt); err != nil {
		s.boardError(w, "recording blob object metadata", err)
		return
	}
	if _, err := b.Pool.Exec(ctx, `UPDATE trajectories SET blob_key = $2 WHERE run_id = $1::uuid`, runID, meta.Key); err != nil {
		s.boardError(w, "linking blob key on trajectory", err)
		return
	}
	if wal := s.ensureWAL(); wal != nil {
		_ = wal.PutBlob(sqlite.BlobRecord{
			BlobKey: meta.Key, ProjectID: p.ID, RunID: runID, Kind: "trajectory",
			Driver: meta.Driver, SHA256: meta.SHA256, SizeBytes: meta.SizeBytes,
			ContentType: meta.ContentType, CreatedAt: meta.CreatedAt,
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"blobKey":    meta.Key,
		"meta":       meta,
		"eventLines": lines,
	})
}

func (s *Server) readTrajectoryBlob(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	runID := strings.TrimSpace(r.PathValue("run"))
	var blobKey, driver, wantSHA, ct string
	var sizeBytes int64
	err := b.Pool.QueryRow(ctx, `SELECT blob_key, driver, sha256, content_type, size_bytes
		FROM blob_objects WHERE project_id = $1 AND run_id = $2::uuid AND kind = 'trajectory'`, p.ID, runID).
		Scan(&blobKey, &driver, &wantSHA, &ct, &sizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "trajectory blob not offloaded yet")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid trajectory blob lookup")
		return
	}
	cfg := s.readStorageConfig(ctx, b.Pool)
	st := s.activeBlobStore(driver, cfg.GCSBucket)
	rawCompressed, meta, err := st.Get(ctx, blobKey)
	if err != nil {
		s.boardError(w, "reading blob from BlobStore", err)
		return
	}
	if meta.SHA256 != "" && meta.SHA256 != wantSHA {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "blob SHA-256 verification failed")
		return
	}
	plain, err := blobstore.DecompressArchive(rawCompressed)
	if err != nil {
		s.boardError(w, "decompressing trajectory archive", err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(plain), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	sample := lines
	if len(sample) > 10 {
		sample = sample[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"blob": map[string]any{
			"key":         blobKey,
			"driver":      driver,
			"sha256":      wantSHA,
			"contentType": ct,
			"sizeBytes":   sizeBytes,
		},
		"verified":    true,
		"eventLines":  len(lines),
		"sampleLines": sample,
	})
}
