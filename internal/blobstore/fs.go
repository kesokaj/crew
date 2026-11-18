package blobstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalFS persists immutable blobs and sidecar metadata beneath a local root directory.
type LocalFS struct {
	Root string
}

// NewLocalFS ensures root exists and returns a LocalFS Store.
func NewLocalFS(root string) (*LocalFS, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("blobstore: root directory is required")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("blobstore: mkdir root: %w", err)
	}
	return &LocalFS{Root: root}, nil
}

// Driver returns DriverLocalFS.
func (s *LocalFS) Driver() string { return DriverLocalFS }

func (s *LocalFS) resolve(key string) (cleaned, dataPath, metaPath string, err error) {
	cleaned, err = CleanKey(key)
	if err != nil {
		return "", "", "", err
	}
	dataPath = filepath.Join(s.Root, filepath.FromSlash(cleaned))
	metaPath = dataPath + ".meta.json"
	if err := rejectSymlinks(s.Root, cleaned); err != nil {
		return "", "", "", err
	}
	return cleaned, dataPath, metaPath, nil
}

func rejectSymlinks(root, cleaned string) error {
	cur := root
	for _, seg := range strings.Split(cleaned, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink at %s", ErrInvalidKey, seg)
		}
	}
	return nil
}

func atomicWriteFile(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// Put writes a blob and its verified ObjectMeta sidecar atomically.
func (s *LocalFS) Put(_ context.Context, key string, data []byte, contentType string) (ObjectMeta, error) {
	cleaned, dataPath, metaPath, err := s.resolve(key)
	if err != nil {
		return ObjectMeta{}, err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	meta := ObjectMeta{
		Key:         cleaned,
		Driver:      DriverLocalFS,
		ContentType: contentType,
		SHA256:      sha256Hex(data),
		SizeBytes:   int64(len(data)),
		CreatedAt:   time.Now().UTC(),
	}
	if err := atomicWriteFile(dataPath, data); err != nil {
		return ObjectMeta{}, err
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return ObjectMeta{}, err
	}
	if err := atomicWriteFile(metaPath, metaBytes); err != nil {
		return ObjectMeta{}, err
	}
	return meta, nil
}

// Get reads a blob payload and verifies its SHA-256 digest when sidecar metadata exists.
func (s *LocalFS) Get(_ context.Context, key string) ([]byte, ObjectMeta, error) {
	cleaned, dataPath, metaPath, err := s.resolve(key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	data, err := os.ReadFile(dataPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectMeta{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	meta, err := readOrSynthesizeMeta(cleaned, dataPath, metaPath, data)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	if sum := sha256Hex(data); meta.SHA256 != "" && meta.SHA256 != sum {
		return nil, ObjectMeta{}, fmt.Errorf("blobstore: digest mismatch for %s: got %s want %s", cleaned, sum, meta.SHA256)
	}
	return data, meta, nil
}

// Stat returns metadata for key without reading the full payload when sidecar metadata is present.
func (s *LocalFS) Stat(_ context.Context, key string) (ObjectMeta, error) {
	cleaned, dataPath, metaPath, err := s.resolve(key)
	if err != nil {
		return ObjectMeta{}, err
	}
	if mb, err := os.ReadFile(metaPath); err == nil {
		var meta ObjectMeta
		if jerr := json.Unmarshal(mb, &meta); jerr == nil && meta.Key != "" {
			return meta, nil
		}
	}
	fi, err := os.Stat(dataPath)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectMeta{}, ErrNotFound
	}
	if err != nil {
		return ObjectMeta{}, err
	}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		return ObjectMeta{}, err
	}
	return ObjectMeta{
		Key:         cleaned,
		Driver:      DriverLocalFS,
		ContentType: "application/octet-stream",
		SHA256:      sha256Hex(data),
		SizeBytes:   fi.Size(),
		CreatedAt:   fi.ModTime().UTC(),
	}, nil
}

func readOrSynthesizeMeta(cleaned, dataPath, metaPath string, data []byte) (ObjectMeta, error) {
	if mb, err := os.ReadFile(metaPath); err == nil {
		var meta ObjectMeta
		if jerr := json.Unmarshal(mb, &meta); jerr == nil && meta.Key != "" {
			return meta, nil
		}
	}
	fi, err := os.Stat(dataPath)
	if err != nil {
		return ObjectMeta{}, err
	}
	return ObjectMeta{
		Key:         cleaned,
		Driver:      DriverLocalFS,
		ContentType: "application/octet-stream",
		SHA256:      sha256Hex(data),
		SizeBytes:   fi.Size(),
		CreatedAt:   fi.ModTime().UTC(),
	}, nil
}

// List returns metadata for all stored blobs matching the optional key prefix.
func (s *LocalFS) List(ctx context.Context, prefix string) ([]ObjectMeta, error) {
	normPrefix := strings.TrimLeft(strings.TrimSpace(prefix), "/")
	var out []ObjectMeta
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, os.ErrNotExist) {
				return nil
			}
			return werr
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), ".meta.json") || strings.HasPrefix(d.Name(), ".blob-") {
			return nil
		}
		rel, err := filepath.Rel(s.Root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if normPrefix != "" && !strings.HasPrefix(key, normPrefix) {
			return nil
		}
		meta, err := s.Stat(ctx, key)
		if err != nil {
			return err
		}
		out = append(out, meta)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete removes both the blob payload and its metadata sidecar.
func (s *LocalFS) Delete(_ context.Context, key string) error {
	_, dataPath, metaPath, err := s.resolve(key)
	if err != nil {
		return err
	}
	_ = os.Remove(metaPath)
	if err := os.Remove(dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Health verifies that Root exists and can write and clean up a probe file.
func (s *LocalFS) Health(_ context.Context) error {
	if err := os.MkdirAll(s.Root, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Root, ".health-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Close()
	return os.Remove(name)
}
