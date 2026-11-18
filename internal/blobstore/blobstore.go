package blobstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
)

const (
	// DriverLocalFS identifies the local filesystem immutable blob store.
	DriverLocalFS = "localfs"
	// DriverGCS identifies a Google Cloud Storage JSON API blob store.
	DriverGCS = "gcs"
)

// ErrNotFound is returned when a blob key does not exist in the store.
var ErrNotFound = errors.New("blob not found")

// ErrInvalidKey is returned when a blob key is empty, absolute, or escapes its root.
var ErrInvalidKey = errors.New("invalid blob key")

// ObjectMeta describes a stored immutable blob object.
type ObjectMeta struct {
	Key         string    `json:"key"`
	Driver      string    `json:"driver"`
	ContentType string    `json:"contentType"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Store is the immutable BlobStore contract (design 5.1, R220) for compressed trajectory archives,
// sealed sandbox snapshots, E2E media, and release artifacts.
type Store interface {
	Driver() string
	Put(ctx context.Context, key string, data []byte, contentType string) (ObjectMeta, error)
	Get(ctx context.Context, key string) ([]byte, ObjectMeta, error)
	Stat(ctx context.Context, key string) (ObjectMeta, error)
	List(ctx context.Context, prefix string) ([]ObjectMeta, error)
	Delete(ctx context.Context, key string) error
	Health(ctx context.Context) error
}

// CleanKey normalizes and validates a forward-slash relative blob key.
func CleanKey(raw string) (string, error) {
	if strings.ContainsRune(raw, '\\') || strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, raw)
	}
	trimmed := strings.TrimLeft(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty key", ErrInvalidKey)
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, ".") {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, raw)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".meta.json") {
			return "", fmt.Errorf("%w: %q", ErrInvalidKey, raw)
		}
	}
	return cleaned, nil
}

// CompressArchive compresses JSONL trajectory payloads using zstd when available,
// falling back transparently to gzip if zstd is absent in the container.
func CompressArchive(raw []byte) (compressed []byte, contentType string, err error) {
	if zstdPath, lerr := exec.LookPath("zstd"); lerr == nil && zstdPath != "" {
		cmd := exec.Command(zstdPath, "-q", "-c")
		cmd.Stdin = bytes.NewReader(raw)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if rerr := cmd.Run(); rerr != nil {
			return nil, "", fmt.Errorf("zstd compress: %w (%s)", rerr, strings.TrimSpace(stderr.String()))
		}
		return out.Bytes(), "application/zstd", nil
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		_ = gw.Close()
		return nil, "", fmt.Errorf("gzip compress: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, "", fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), "application/gzip", nil
}

// DecompressArchive restores compressed zstd or gzip archive payloads back into raw bytes.
func DecompressArchive(compressed []byte) ([]byte, error) {
	if len(compressed) >= 4 && compressed[0] == 0x28 && compressed[1] == 0xb5 && compressed[2] == 0x2f && compressed[3] == 0xfd {
		zstdPath, err := exec.LookPath("zstd")
		if err != nil {
			return nil, fmt.Errorf("zstd binary required to decompress archive: %w", err)
		}
		cmd := exec.Command(zstdPath, "-d", "-q", "-c")
		cmd.Stdin = bytes.NewReader(compressed)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("zstd decompress: %w (%s)", err, strings.TrimSpace(stderr.String()))
		}
		return out.Bytes(), nil
	}
	if len(compressed) >= 2 && compressed[0] == 0x1f && compressed[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer gr.Close()
		out, err := io.ReadAll(gr)
		if err != nil {
			return nil, fmt.Errorf("gzip read: %w", err)
		}
		return out, nil
	}
	return compressed, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
