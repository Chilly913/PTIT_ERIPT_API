// Package storage is a thin wrapper over MinIO / S3. The database stores
// object keys (relative paths); URLs are built at response time.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

// Storage puts, deletes and links objects in a single bucket.
type Storage struct {
	client    *minio.Client
	bucket    string
	publicURL string
	expiry    time.Duration
}

// New creates the client. It does not contact the server.
func New(cfg config.StorageConfig) (*Storage, error) {
	lookup := minio.BucketLookupPath
	if !cfg.PathStyle {
		lookup = minio.BucketLookupDNS
	}
	cl, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       cfg.UseSSL,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return &Storage{
		client:    cl,
		bucket:    cfg.Bucket,
		publicURL: PublicBaseURL(cfg),
		expiry:    cfg.PresignExpiry,
	}, nil
}

// Client exposes the underlying MinIO client for advanced use (multipart etc.).
func (s *Storage) Client() *minio.Client { return s.client }

// Bucket returns the configured bucket name.
func (s *Storage) Bucket() string { return s.bucket }

// Put uploads r (size -1 if unknown) under key.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, CleanKey(key), r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

// Delete removes key. Deleting a missing object is not an error.
func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, CleanKey(key), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

// PresignedGet returns a temporary download URL. expiry <= 0 uses the configured default.
func (s *Storage) PresignedGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		expiry = s.expiry
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, CleanKey(key), expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("storage: presign %q: %w", key, err)
	}
	return u.String(), nil
}

// URL builds the public URL of key; absolute URLs and empty keys pass through.
func (s *Storage) URL(key string) string { return JoinURL(s.publicURL, key) }

// EnsureBucket creates the bucket if missing (dev/test convenience).
func (s *Storage) EnsureBucket(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("storage: bucket exists: %w", err)
	}
	if ok {
		return nil
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
}

// PublicBaseURL returns cfg.PublicURL or derives it from the endpoint:
// path-style "scheme://endpoint/bucket", virtual-host "scheme://bucket.endpoint".
func PublicBaseURL(cfg config.StorageConfig) string {
	if cfg.PublicURL != "" {
		return strings.TrimRight(cfg.PublicURL, "/")
	}
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	if cfg.PathStyle {
		return fmt.Sprintf("%s://%s/%s", scheme, cfg.Endpoint, cfg.Bucket)
	}
	return fmt.Sprintf("%s://%s.%s", scheme, cfg.Bucket, cfg.Endpoint)
}

// JoinURL joins base and an object key, escaping each path segment.
func JoinURL(base, key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
		return key
	}
	segs := strings.Split(CleanKey(key), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + "/" + strings.Join(segs, "/")
}

// CleanKey normalises a key: forward slashes, no leading slash.
func CleanKey(key string) string {
	return strings.TrimLeft(strings.ReplaceAll(key, "\\", "/"), "/")
}
