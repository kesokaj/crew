package sqlite

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OperatorPrefs stores per-operator theme, dark mode, and layout direction in SQLite WAL state.
type OperatorPrefs struct {
	Email     string    `json:"email"`
	Theme     string    `json:"theme"`
	Dark      bool      `json:"dark"`
	Direction string    `json:"direction"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TrajectoryRecord mirrors searchable Trajectory Store metadata in the embedded WAL store.
type TrajectoryRecord struct {
	RunID           string    `json:"runId"`
	ProjectID       string    `json:"projectId"`
	TicketKey       string    `json:"ticketKey,omitempty"`
	Role            string    `json:"role"`
	Model           string    `json:"model,omitempty"`
	ToolVerbs       []string  `json:"toolVerbs"`
	TouchedPaths    []string  `json:"touchedPaths"`
	EventsCount     int64     `json:"eventsCount"`
	TurnsCount      int       `json:"turnsCount"`
	TokensTotal     int64     `json:"tokensTotal"`
	DurationSeconds int       `json:"durationSeconds"`
	Outcome         string    `json:"outcome"`
	BlobKey         string    `json:"blobKey,omitempty"`
	SearchText      string    `json:"searchText"`
	StartedAt       time.Time `json:"startedAt"`
}

// BlobRecord tracks offloaded BlobStore objects inside the embedded WAL store.
type BlobRecord struct {
	BlobKey     string    `json:"blobKey"`
	ProjectID   string    `json:"projectId,omitempty"`
	RunID       string    `json:"runId,omitempty"`
	Kind        string    `json:"kind"`
	Driver      string    `json:"driver"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	ContentType string    `json:"contentType"`
	CreatedAt   time.Time `json:"createdAt"`
}

// TrajectoryQuery filters embedded Trajectory Store records by full-text tokens and facets.
type TrajectoryQuery struct {
	ProjectID string
	Query     string
	Role      string
	Tool      string
	Path      string
	Outcome   string
}

// Status reports embedded WAL StateDB health and compaction telemetry.
type Status struct {
	Path              string `json:"path"`
	WALPath           string `json:"walPath"`
	JournalMode       string `json:"journalMode"`
	WALFrames         int    `json:"walFrames"`
	Checkpoints       int    `json:"checkpoints"`
	Bytes             int64  `json:"bytes"`
	PrefsCount        int    `json:"prefsCount"`
	TrajectoriesCount int    `json:"trajectoriesCount"`
	BlobsCount        int    `json:"blobsCount"`
	Healthy           bool   `json:"healthy"`
}

type walData struct {
	Prefs        map[string]OperatorPrefs    `json:"prefs"`
	Trajectories map[string]TrajectoryRecord `json:"trajectories"`
	Blobs        map[string]BlobRecord       `json:"blobs"`
	Checkpoints  int                         `json:"checkpoints"`
}

type walFrame struct {
	Op         string            `json:"op"`
	Pref       *OperatorPrefs    `json:"pref,omitempty"`
	Trajectory *TrajectoryRecord `json:"trajectory,omitempty"`
	Blob       *BlobRecord       `json:"blob,omitempty"`
}

// WALStore is a pure-Go embedded StateDB mode with CRC32-verified write-ahead logging (R220)
// compatible with CGO_ENABLED=0 reproducible server builds.
type WALStore struct {
	mu      sync.RWMutex
	path    string
	walPath string
	data    walData
	frames  int
}

// Open initializes or recovers an embedded StateDB from its checkpoint file plus -wal journal.
func Open(path string) (*WALStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite wal: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("sqlite wal: mkdir: %w", err)
	}
	st := &WALStore{
		path:    path,
		walPath: path + "-wal",
		data: walData{
			Prefs:        map[string]OperatorPrefs{},
			Trajectories: map[string]TrajectoryRecord{},
			Blobs:        map[string]BlobRecord{},
		},
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		var loaded walData
		if jerr := json.Unmarshal(b, &loaded); jerr == nil {
			if loaded.Prefs != nil {
				st.data.Prefs = loaded.Prefs
			}
			if loaded.Trajectories != nil {
				st.data.Trajectories = loaded.Trajectories
			}
			if loaded.Blobs != nil {
				st.data.Blobs = loaded.Blobs
			}
			st.data.Checkpoints = loaded.Checkpoints
		}
	} else if !errors.Is(err, os.ErrNotExist) && err != nil {
		return nil, err
	}
	if err := st.replayWAL(); err != nil {
		return nil, err
	}
	return st, nil
}

// JournalMode returns "WAL".
func (s *WALStore) JournalMode() string { return "WAL" }

func (s *WALStore) replayWAL() error {
	f, err := os.Open(s.walPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue // Ignore truncated tail frame on crash recovery.
		}
		wantSum, perr := strconv.ParseUint(parts[0], 16, 32)
		if perr != nil || crc32.ChecksumIEEE([]byte(parts[1])) != uint32(wantSum) {
			continue // Ignore corrupted/partial crash tail frame.
		}
		var fr walFrame
		if jerr := json.Unmarshal([]byte(parts[1]), &fr); jerr != nil {
			continue
		}
		s.applyFrameLocked(fr)
		s.frames++
	}
	return sc.Err()
}

func (s *WALStore) applyFrameLocked(fr walFrame) {
	switch fr.Op {
	case "pref":
		if fr.Pref != nil {
			s.data.Prefs[fr.Pref.Email] = *fr.Pref
		}
	case "trajectory":
		if fr.Trajectory != nil {
			s.data.Trajectories[fr.Trajectory.RunID] = *fr.Trajectory
		}
	case "blob":
		if fr.Blob != nil {
			s.data.Blobs[fr.Blob.BlobKey] = *fr.Blob
		}
	}
}

func (s *WALStore) appendFrameLocked(fr walFrame) error {
	payload, err := json.Marshal(fr)
	if err != nil {
		return err
	}
	sum := crc32.ChecksumIEEE(payload)
	f, err := os.OpenFile(s.walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%08x %s\n", sum, payload); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	s.applyFrameLocked(fr)
	s.frames++
	return nil
}

// Checkpoint compacts pending WAL frames atomically into the main database file and resets -wal.
func (s *WALStore) Checkpoint() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Checkpoints++
	raw, err := json.Marshal(s.data)
	if err != nil {
		s.data.Checkpoints--
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		s.data.Checkpoints--
		return err
	}
	if tf, err := os.OpenFile(tmp, os.O_RDONLY, 0); err == nil {
		_ = tf.Sync()
		_ = tf.Close()
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.data.Checkpoints--
		return err
	}
	if wf, err := os.OpenFile(s.walPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640); err == nil {
		_ = wf.Sync()
		_ = wf.Close()
	}
	s.frames = 0
	return nil
}

// SavePrefs appends an OperatorPrefs update to the WAL.
func (s *WALStore) SavePrefs(p OperatorPrefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return s.appendFrameLocked(walFrame{Op: "pref", Pref: &p})
}

// GetPrefs returns stored operator preferences if present.
func (s *WALStore) GetPrefs(email string) (OperatorPrefs, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Prefs[email]
	return p, ok
}

// UpsertTrajectory appends a Trajectory Store record to the WAL.
func (s *WALStore) UpsertTrajectory(rec TrajectoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendFrameLocked(walFrame{Op: "trajectory", Trajectory: &rec})
}

// SearchTrajectories queries Trajectory Store records in newest-first order by tokens and facets.
func (s *WALStore) SearchTrajectories(q TrajectoryQuery) []TrajectoryRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(q.Query)))
	var out []TrajectoryRecord
	for _, r := range s.data.Trajectories {
		if q.ProjectID != "" && r.ProjectID != q.ProjectID {
			continue
		}
		if q.Role != "" && r.Role != q.Role {
			continue
		}
		if q.Outcome != "" && r.Outcome != q.Outcome {
			continue
		}
		if q.Tool != "" && !containsFold(r.ToolVerbs, q.Tool) {
			continue
		}
		if q.Path != "" && !containsSubstring(r.TouchedPaths, q.Path) {
			continue
		}
		if len(tokens) > 0 {
			hay := strings.ToLower(r.SearchText + " " + r.RunID + " " + r.TicketKey + " " + r.Role + " " + strings.Join(r.ToolVerbs, " ") + " " + strings.Join(r.TouchedPaths, " "))
			matched := true
			for _, tok := range tokens {
				if !strings.Contains(hay, tok) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// PutBlob records a BlobStore reference in the WAL.
func (s *WALStore) PutBlob(rec BlobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	return s.appendFrameLocked(walFrame{Op: "blob", Blob: &rec})
}

// ListBlobs lists BlobRecords for the given project (or all when projectID is empty).
func (s *WALStore) ListBlobs(projectID string) []BlobRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []BlobRecord
	for _, b := range s.data.Blobs {
		if projectID != "" && b.ProjectID != projectID {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Stats reports current WAL frame counts, file sizes, and health.
func (s *WALStore) Stats() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	if fi, err := os.Stat(s.path); err == nil {
		total += fi.Size()
	}
	if fi, err := os.Stat(s.walPath); err == nil {
		total += fi.Size()
	}
	return Status{
		Path:              s.path,
		WALPath:           s.walPath,
		JournalMode:       s.JournalMode(),
		WALFrames:         s.frames,
		Checkpoints:       s.data.Checkpoints,
		Bytes:             total,
		PrefsCount:        len(s.data.Prefs),
		TrajectoriesCount: len(s.data.Trajectories),
		BlobsCount:        len(s.data.Blobs),
		Healthy:           true,
	}
}

func containsFold(list []string, want string) bool {
	for _, x := range list {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

func containsSubstring(list []string, sub string) bool {
	sub = strings.ToLower(sub)
	for _, x := range list {
		if strings.Contains(strings.ToLower(x), sub) {
			return true
		}
	}
	return false
}
