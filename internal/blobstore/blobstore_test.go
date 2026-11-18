package blobstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kesokaj/crew/internal/blobstore"
)

func TestLocalFSAndCompressionRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := blobstore.NewLocalFS(root)
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	if err := st.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}

	raw := []byte("{\"seq\":1,\"kind\":\"tool_call\",\"tool\":\"read_chat\"}\n{\"seq\":2,\"kind\":\"result\"}\n")
	compressed, ct, err := blobstore.CompressArchive(raw)
	if err != nil {
		t.Fatalf("CompressArchive: %v", err)
	}
	meta, err := st.Put(ctx, "trajectories/shop/run-1.jsonl.zst", compressed, ct)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if meta.Driver != blobstore.DriverLocalFS || meta.SHA256 == "" || meta.SizeBytes != int64(len(compressed)) {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	got, gotMeta, err := st.Get(ctx, "trajectories/shop/run-1.jsonl.zst")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotMeta.SHA256 != meta.SHA256 {
		t.Fatalf("digest mismatch: got %s want %s", gotMeta.SHA256, meta.SHA256)
	}
	restored, err := blobstore.DecompressArchive(got)
	if err != nil {
		t.Fatalf("DecompressArchive: %v", err)
	}
	if !bytes.Equal(restored, raw) {
		t.Fatalf("restored archive mismatch: got %q want %q", restored, raw)
	}

	list, err := st.List(ctx, "trajectories/shop/")
	if err != nil || len(list) != 1 || list[0].Key != "trajectories/shop/run-1.jsonl.zst" {
		t.Fatalf("List: %v (%+v)", err, list)
	}

	// Reject traversal & symlink escapes.
	for _, bad := range []string{"../escape", "/..", ".hidden/file", `a\b`, "a.meta.json"} {
		if _, err := st.Put(ctx, bad, []byte("x"), "text/plain"); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("expected ErrInvalidKey for %q, got %v", bad, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sym")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get(ctx, "sym"); !errors.Is(err, blobstore.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey on symlink, got %v", err)
	}

	if err := st.Delete(ctx, "trajectories/shop/run-1.jsonl.zst"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := st.Get(ctx, "trajectories/shop/run-1.jsonl.zst"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestGCSDriverRoundTrip(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/crew-blobs/o":
			name := r.URL.Query().Get("name")
			b, _ := io.ReadAll(r.Body)
			objects[name] = b
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "size": "4", "contentType": "application/zstd"})
		case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/crew-blobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "crew-blobs"})
		case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/crew-blobs/o":
			pref := r.URL.Query().Get("prefix")
			var items []map[string]any
			for k, v := range objects {
				if strings.HasPrefix(k, pref) {
					items = append(items, map[string]any{"name": k, "size": string(rune('0' + len(v))), "contentType": "application/zstd"})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case strings.HasPrefix(r.URL.Path, "/storage/v1/b/crew-blobs/o/"):
			name := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/crew-blobs/o/")
			data, ok := objects[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.Method == http.MethodDelete {
				delete(objects, name)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.URL.Query().Get("alt") == "media" {
				_, _ = w.Write(data)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "contentType": "application/zstd"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	g := &blobstore.GCS{Bucket: "crew-blobs", Prefix: "factory-a", Endpoint: srv.URL}
	if err := g.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	meta, err := g.Put(ctx, "trajectories/shop/r1.jsonl.zst", []byte("data"), "application/zstd")
	if err != nil || meta.Driver != blobstore.DriverGCS {
		t.Fatalf("Put: %v (%+v)", err, meta)
	}
	got, _, err := g.Get(ctx, "trajectories/shop/r1.jsonl.zst")
	if err != nil || string(got) != "data" {
		t.Fatalf("Get: %v (%q)", err, got)
	}
	list, err := g.List(ctx, "trajectories/")
	if err != nil || len(list) != 1 || list[0].Key != "trajectories/shop/r1.jsonl.zst" {
		t.Fatalf("List: %v (%+v)", err, list)
	}
	if err := g.Delete(ctx, "trajectories/shop/r1.jsonl.zst"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
