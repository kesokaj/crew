package blobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultGCSEndpoint = "https://storage.googleapis.com"

// GCS is a zero-dependency Google Cloud Storage JSON API driver using stdlib net/http.
type GCS struct {
	Bucket    string
	Prefix    string
	Endpoint  string
	TokenFunc func(context.Context) (string, error)
	Client    *http.Client
}

// Driver returns DriverGCS.
func (g *GCS) Driver() string { return DriverGCS }

func (g *GCS) baseURL() string {
	if strings.TrimSpace(g.Endpoint) != "" {
		return strings.TrimRight(g.Endpoint, "/")
	}
	return defaultGCSEndpoint
}

func (g *GCS) httpClient() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *GCS) fullName(cleaned string) string {
	p := strings.Trim(g.Prefix, "/")
	if p == "" {
		return cleaned
	}
	return p + "/" + cleaned
}

func (g *GCS) stripPrefix(full string) string {
	p := strings.Trim(g.Prefix, "/")
	if p != "" && strings.HasPrefix(full, p+"/") {
		return strings.TrimPrefix(full, p+"/")
	}
	return full
}

func (g *GCS) doReq(ctx context.Context, method, rawURL string, body []byte, contentType string) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if g.TokenFunc != nil {
		tok, err := g.TokenFunc(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("gcs token: %w", err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	return out, resp.StatusCode, err
}

type gcsObjectJSON struct {
	Name        string            `json:"name"`
	Size        string            `json:"size"`
	ContentType string            `json:"contentType"`
	TimeCreated string            `json:"timeCreated"`
	Metadata    map[string]string `json:"metadata"`
}

func (o gcsObjectJSON) toMeta(cleaned string) ObjectMeta {
	sz, _ := strconv.ParseInt(o.Size, 10, 64)
	ts, _ := time.Parse(time.RFC3339, o.TimeCreated)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	ct := o.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	return ObjectMeta{
		Key:         cleaned,
		Driver:      DriverGCS,
		ContentType: ct,
		SHA256:      o.Metadata["sha256"],
		SizeBytes:   sz,
		CreatedAt:   ts,
	}
}

// Put uploads key via GCS simple media upload and attaches its verified SHA-256 digest.
func (g *GCS) Put(ctx context.Context, key string, data []byte, contentType string) (ObjectMeta, error) {
	if strings.TrimSpace(g.Bucket) == "" {
		return ObjectMeta{}, fmt.Errorf("gcs: bucket is required")
	}
	cleaned, err := CleanKey(key)
	if err != nil {
		return ObjectMeta{}, err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	name := g.fullName(cleaned)
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		g.baseURL(), url.PathEscape(g.Bucket), url.QueryEscape(name))
	body, code, err := g.doReq(ctx, http.MethodPost, u, data, contentType)
	if err != nil {
		return ObjectMeta{}, err
	}
	if code < 200 || code >= 300 {
		return ObjectMeta{}, fmt.Errorf("gcs put %s: status %d: %s", cleaned, code, strings.TrimSpace(string(body)))
	}
	var obj gcsObjectJSON
	_ = json.Unmarshal(body, &obj)
	meta := obj.toMeta(cleaned)
	meta.ContentType = contentType
	meta.SizeBytes = int64(len(data))
	meta.SHA256 = sha256Hex(data)
	return meta, nil
}

// Get downloads the object bytes and verifies metadata when returned by Stat.
func (g *GCS) Get(ctx context.Context, key string) ([]byte, ObjectMeta, error) {
	cleaned, err := CleanKey(key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	name := g.fullName(cleaned)
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		g.baseURL(), url.PathEscape(g.Bucket), url.PathEscape(name))
	data, code, err := g.doReq(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	if code == http.StatusNotFound {
		return nil, ObjectMeta{}, ErrNotFound
	}
	if code < 200 || code >= 300 {
		return nil, ObjectMeta{}, fmt.Errorf("gcs get %s: status %d", cleaned, code)
	}
	meta := ObjectMeta{
		Key:         cleaned,
		Driver:      DriverGCS,
		ContentType: "application/octet-stream",
		SHA256:      sha256Hex(data),
		SizeBytes:   int64(len(data)),
		CreatedAt:   time.Now().UTC(),
	}
	if sm, serr := g.Stat(ctx, cleaned); serr == nil {
		if sm.SHA256 != "" && sm.SHA256 != meta.SHA256 {
			return nil, ObjectMeta{}, fmt.Errorf("gcs: digest mismatch for %s", cleaned)
		}
		meta.ContentType = sm.ContentType
		meta.CreatedAt = sm.CreatedAt
	}
	return data, meta, nil
}

// Stat reads object metadata without downloading its body.
func (g *GCS) Stat(ctx context.Context, key string) (ObjectMeta, error) {
	cleaned, err := CleanKey(key)
	if err != nil {
		return ObjectMeta{}, err
	}
	name := g.fullName(cleaned)
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
		g.baseURL(), url.PathEscape(g.Bucket), url.PathEscape(name))
	body, code, err := g.doReq(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return ObjectMeta{}, err
	}
	if code == http.StatusNotFound {
		return ObjectMeta{}, ErrNotFound
	}
	if code < 200 || code >= 300 {
		return ObjectMeta{}, fmt.Errorf("gcs stat %s: status %d", cleaned, code)
	}
	var obj gcsObjectJSON
	if err := json.Unmarshal(body, &obj); err != nil {
		return ObjectMeta{}, err
	}
	return obj.toMeta(cleaned), nil
}

// List enumerates objects under the configured prefix.
func (g *GCS) List(ctx context.Context, prefix string) ([]ObjectMeta, error) {
	fullPrefix := strings.TrimLeft(strings.TrimSpace(prefix), "/")
	if p := strings.Trim(g.Prefix, "/"); p != "" {
		if fullPrefix == "" {
			fullPrefix = p + "/"
		} else {
			fullPrefix = p + "/" + fullPrefix
		}
	}
	u := fmt.Sprintf("%s/storage/v1/b/%s/o?prefix=%s",
		g.baseURL(), url.PathEscape(g.Bucket), url.QueryEscape(fullPrefix))
	body, code, err := g.doReq(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("gcs list: status %d", code)
	}
	var res struct {
		Items []gcsObjectJSON `json:"items"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	out := make([]ObjectMeta, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, it.toMeta(g.stripPrefix(it.Name)))
	}
	return out, nil
}

// Delete removes the object from GCS.
func (g *GCS) Delete(ctx context.Context, key string) error {
	cleaned, err := CleanKey(key)
	if err != nil {
		return err
	}
	name := g.fullName(cleaned)
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
		g.baseURL(), url.PathEscape(g.Bucket), url.PathEscape(name))
	_, code, err := g.doReq(ctx, http.MethodDelete, u, nil, "")
	if err != nil {
		return err
	}
	if code == http.StatusNotFound || (code >= 200 && code < 300) {
		return nil
	}
	return fmt.Errorf("gcs delete %s: status %d", cleaned, code)
}

// Health verifies bucket metadata reachability.
func (g *GCS) Health(ctx context.Context) error {
	if strings.TrimSpace(g.Bucket) == "" {
		return fmt.Errorf("gcs: bucket is required")
	}
	u := fmt.Sprintf("%s/storage/v1/b/%s", g.baseURL(), url.PathEscape(g.Bucket))
	_, code, err := g.doReq(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("gcs bucket %s unreachable: status %d", g.Bucket, code)
	}
	return nil
}
