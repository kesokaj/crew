package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/blobstore"
	"github.com/kesokaj/crew/internal/db/sqlite"
)

// AllowedThemes lists all 40 runtime-switchable UI themes from ui-sim/DECISION.md.
var AllowedThemes = []string{
	"linear", "claude", "vercel", "warp", "supabase", "vesper", "flexoki", "oxocarbon", "poimandres", "night-owl", "synthwave",
	"default", "shadcn-zinc-blue", "shadcn-stone-amber", "shadcn-zinc-violet", "shadcn-olive-emerald", "shadcn-mauve-rose", "shadcn-mist-cyan", "shadcn-taupe-orange", "shadcn-zinc-teal",
	"catppuccin", "catppuccin-macchiato", "catppuccin-frappe", "tokyo-night", "tokyo-night-storm", "tokyo-night-moon", "dracula", "one-dark", "github", "gruvbox", "nord", "rose-pine", "rose-pine-moon", "kanagawa", "kanagawa-dragon", "solarized", "everforest", "ayu", "ayu-mirage", "monokai",
}

func (s *Server) m25Routes(api *routes) {
	api.HandleFunc("GET /api/prefs", s.getPrefs)
	api.HandleFunc("PUT /api/prefs", s.putPrefs)
	api.HandleFunc("GET /api/storage/status", s.getStorageStatus)
	api.HandleFunc("PUT /api/storage/config", s.putStorageConfig)
	api.HandleFunc("GET /api/projects/{p}/factory-line", s.getFactoryLine)
	api.HandleFunc("GET /api/projects/{p}/skills-registry", s.getSkillsRegistry)
	s.m25TrajectoryRoutes(api)
}

type operatorPrefView struct {
	Email       string    `json:"email"`
	Theme       string    `json:"theme"`
	Dark        bool      `json:"dark"`
	Direction   string    `json:"direction"`
	ThemesCount int       `json:"themesCount"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (s *Server) prefEmail(r *http.Request) string {
	if e := strings.TrimSpace(r.Header.Get(HeaderEmail)); e != "" {
		return e
	}
	if e := strings.TrimSpace(s.AdminEmail); e != "" {
		return e
	}
	return "operator@crew.local"
}

func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	email := s.prefEmail(r)
	out := operatorPrefView{
		Email:       email,
		Theme:       "catppuccin",
		Dark:        true,
		Direction:   "A",
		ThemesCount: len(AllowedThemes),
		UpdatedAt:   time.Now().UTC(),
	}
	err := pool.QueryRow(r.Context(), `SELECT theme, dark, direction, updated_at FROM operator_prefs WHERE email = $1`, email).
		Scan(&out.Theme, &out.Dark, &out.Direction, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if wal := s.SQLite.Load(); wal != nil {
			if p, ok := wal.GetPrefs(email); ok && slices.Contains(AllowedThemes, p.Theme) {
				out.Theme, out.Dark, out.Direction, out.UpdatedAt = p.Theme, p.Dark, p.Direction, p.UpdatedAt
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err != nil {
		s.boardError(w, "reading operator preferences", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	var req struct {
		Theme     string `json:"theme"`
		Dark      bool   `json:"dark"`
		Direction string `json:"direction"`
	}
	if !decode(w, r, &req) {
		return
	}
	theme := strings.TrimSpace(req.Theme)
	if !slices.Contains(AllowedThemes, theme) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported theme id")
		return
	}
	dir := strings.ToUpper(strings.TrimSpace(req.Direction))
	if dir == "" {
		dir = "A"
	}
	if dir != "A" && dir != "B" && dir != "C" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "direction must be A, B, or C")
		return
	}
	email := s.prefEmail(r)
	var updatedAt time.Time
	err := pool.QueryRow(r.Context(), `INSERT INTO operator_prefs (email, theme, dark, direction, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (email) DO UPDATE SET
			theme = EXCLUDED.theme,
			dark = EXCLUDED.dark,
			direction = EXCLUDED.direction,
			updated_at = now()
		RETURNING updated_at`, email, theme, req.Dark, dir).Scan(&updatedAt)
	if err != nil {
		s.boardError(w, "saving operator preferences", err)
		return
	}
	if wal := s.SQLite.Load(); wal != nil {
		_ = wal.SavePrefs(sqlite.OperatorPrefs{
			Email: email, Theme: theme, Dark: req.Dark, Direction: dir, UpdatedAt: updatedAt,
		})
	}
	writeJSON(w, http.StatusOK, operatorPrefView{
		Email:       email,
		Theme:       theme,
		Dark:        req.Dark,
		Direction:   dir,
		ThemesCount: len(AllowedThemes),
		UpdatedAt:   updatedAt,
	})
}

func (s *Server) ensureWAL() *sqlite.WALStore {
	if wal := s.SQLite.Load(); wal != nil {
		return wal
	}
	base := s.BlobDir
	if strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	wal, err := sqlite.Open(filepath.Join(base, "state.db"))
	if err != nil {
		return nil
	}
	s.SQLite.CompareAndSwap(nil, wal)
	return s.SQLite.Load()
}

func (s *Server) activeBlobStore(driver, bucket string) blobstore.Store {
	if driver == blobstore.DriverGCS && strings.TrimSpace(bucket) != "" {
		if p := s.Blobs.Load(); p != nil && *p != nil && (*p).Driver() == blobstore.DriverGCS {
			return *p
		}
		return &blobstore.GCS{Bucket: strings.TrimSpace(bucket), Prefix: "crew-blobs"}
	}
	if p := s.Blobs.Load(); p != nil && *p != nil {
		return *p
	}
	root := s.BlobDir
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(os.TempDir(), "crew-blobs")
	}
	fs, err := blobstore.NewLocalFS(root)
	if err != nil {
		return &blobstore.LocalFS{Root: root}
	}
	var st blobstore.Store = fs
	s.Blobs.CompareAndSwap(nil, &st)
	return st
}

type storageConfig struct {
	StateDBMode string `json:"stateDbMode"`
	BlobDriver  string `json:"blobDriver"`
	GCSBucket   string `json:"gcsBucket"`
}

func (s *Server) readStorageConfig(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) storageConfig {
	cfg := storageConfig{StateDBMode: "postgres", BlobDriver: blobstore.DriverLocalFS}
	if err := pool.QueryRow(ctx, `SELECT state_db_mode, blob_driver, gcs_bucket FROM storage_settings WHERE id = true`).
		Scan(&cfg.StateDBMode, &cfg.BlobDriver, &cfg.GCSBucket); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		if s.Log != nil {
			s.Log.Warn("reading storage settings", "error", err)
		}
	}
	return cfg
}

func (s *Server) getStorageStatus(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	ctx := r.Context()
	cfg := s.readStorageConfig(ctx, pool)
	pgHealthy := pool.Ping(ctx) == nil
	wal := s.ensureWAL()
	var sqliteStats sqlite.Status
	if wal != nil {
		sqliteStats = wal.Stats()
	}

	var objects, bytesTotal, trajTotal, trajOffloaded, vmCount, snapCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0) FROM blob_objects`).Scan(&objects, &bytesTotal); err != nil {
		s.boardError(w, "counting blob objects", err)
		return
	}
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE blob_key <> '') FROM trajectories`).Scan(&trajTotal, &trajOffloaded); err != nil {
		s.boardError(w, "counting indexed trajectories", err)
		return
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_sandboxes), (SELECT count(*) FROM project_snapshots)`).Scan(&vmCount, &snapCount); err != nil {
		s.boardError(w, "counting runtime sandboxes and snapshots", err)
		return
	}
	rows, err := pool.Query(ctx, `SELECT blob_key, coalesce(project_id::text, ''), coalesce(run_id::text, ''), kind, driver, sha256, size_bytes, content_type, created_at
		FROM blob_objects ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		s.boardError(w, "listing blob objects", err)
		return
	}
	type blobRow struct {
		Key         string    `json:"key"`
		ProjectID   string    `json:"projectId,omitempty"`
		RunID       string    `json:"runId,omitempty"`
		Kind        string    `json:"kind"`
		Driver      string    `json:"driver"`
		SHA256      string    `json:"sha256"`
		SizeBytes   int64     `json:"sizeBytes"`
		ContentType string    `json:"contentType"`
		CreatedAt   time.Time `json:"createdAt"`
	}
	recent, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (blobRow, error) {
		var x blobRow
		return x, row.Scan(&x.Key, &x.ProjectID, &x.RunID, &x.Kind, &x.Driver, &x.SHA256, &x.SizeBytes, &x.ContentType, &x.CreatedAt)
	})
	if err != nil {
		s.boardError(w, "reading blob objects", err)
		return
	}
	if recent == nil {
		recent = []blobRow{}
	}
	st := s.activeBlobStore(cfg.BlobDriver, cfg.GCSBucket)
	blobErr := ""
	blobHealthy := true
	if herr := st.Health(ctx); herr != nil {
		blobHealthy = false
		blobErr = herr.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stateDb": map[string]any{
			"mode":            cfg.StateDBMode,
			"postgresHealthy": pgHealthy,
			"sqlite":          sqliteStats,
		},
		"blobStore": map[string]any{
			"driver":                cfg.BlobDriver,
			"gcsBucket":             cfg.GCSBucket,
			"healthy":               blobHealthy,
			"error":                 blobErr,
			"objects":               objects,
			"bytes":                 bytesTotal,
			"trajectoriesTotal":     trajTotal,
			"trajectoriesOffloaded": trajOffloaded,
			"recent":                recent,
		},
		"computeRuntime": map[string]any{
			"driver":         "docker-kata",
			"kubernetesSeam": true,
			"projectVMs":     vmCount,
			"snapshots":      snapCount,
		},
	})
}

func (s *Server) putStorageConfig(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	var req struct {
		StateDBMode string `json:"stateDbMode"`
		BlobDriver  string `json:"blobDriver"`
		GCSBucket   string `json:"gcsBucket"`
		Checkpoint  bool   `json:"checkpoint"`
	}
	if !decode(w, r, &req) {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.StateDBMode))
	if mode == "" {
		mode = "postgres"
	}
	if mode != "postgres" && mode != "sqlite" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "stateDbMode must be postgres or sqlite")
		return
	}
	drv := strings.ToLower(strings.TrimSpace(req.BlobDriver))
	if drv == "" {
		drv = blobstore.DriverLocalFS
	}
	if drv != blobstore.DriverLocalFS && drv != blobstore.DriverGCS {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "blobDriver must be localfs or gcs")
		return
	}
	if _, err := pool.Exec(r.Context(), `INSERT INTO storage_settings (id, state_db_mode, blob_driver, gcs_bucket, updated_by, updated_at)
		VALUES (true, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE SET
			state_db_mode = EXCLUDED.state_db_mode,
			blob_driver = EXCLUDED.blob_driver,
			gcs_bucket = EXCLUDED.gcs_bucket,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, mode, drv, strings.TrimSpace(req.GCSBucket), s.prefEmail(r)); err != nil {
		s.boardError(w, "updating storage settings", err)
		return
	}
	if req.Checkpoint || mode == "sqlite" {
		if wal := s.ensureWAL(); wal != nil {
			_ = wal.Checkpoint()
		}
	}
	s.getStorageStatus(w, r)
}
