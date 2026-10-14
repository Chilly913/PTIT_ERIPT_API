package auth

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/storage"
)

// fakeStore is an in-memory ObjectStore.
type fakeStore struct {
	base, bucket string
	mu           sync.Mutex
	objects      map[string]int64
	deleted      []string
}

func newFakeStore(base string) *fakeStore {
	return &fakeStore{base: base, bucket: "lms-videos", objects: map[string]int64{}}
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	n, err := io.Copy(io.Discard, r)
	f.mu.Lock()
	f.objects[key] = n
	f.mu.Unlock()
	return err
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) URL(key string) string { return storage.JoinURL(f.base, key) }
func (f *fakeStore) Bucket() string        { return f.bucket }

func sp(s string) *string { return &s }

func TestMediaPublic(t *testing.T) {
	// Default dev setup: base derived from the MinIO endpoint.
	m := newMediaResolver(newFakeStore("http://localhost:9000/lms-videos"), "https://eript-lms.io.vn")
	cases := map[string]string{
		"users/avatars/a.jpg":                         "/minio/lms-videos/users/avatars/a.jpg",
		"/storage/users/avatars/a.jpg":                "/storage/users/avatars/a.jpg",
		"storage/x.png":                               "/storage/x.png",
		"/minio/lms-videos/x.png":                     "/minio/lms-videos/x.png",
		"http://minio:9000/lms-videos/x.png":          "/minio/lms-videos/x.png",
		"http://localhost:8000/storage/x.png":         "/storage/x.png",
		"https://eript-lms.io.vn/uploads/x.png":       "/uploads/x.png",
		"https://lh3.googleusercontent.com/a/abc=s96": "https://lh3.googleusercontent.com/a/abc=s96",
		"data:image/png;base64,AAA":                   "data:image/png;base64,AAA",
	}
	for in, want := range cases {
		if got := m.Public(sp(in)); got == nil || *got != want {
			t.Errorf("Public(%q) = %v, want %q", in, got, want)
		}
	}
	if m.Public(nil) != nil || m.Public(sp("  ")) != nil {
		t.Error("empty must be nil")
	}

	// Production: S3_PUBLIC_URL on the site host → relative like legacy.
	prod := newMediaResolver(newFakeStore("https://eript-lms.io.vn/minio/lms-videos"), "https://eript-lms.io.vn")
	if got := *prod.Public(sp("users/avatars/a b.jpg")); got != "/minio/lms-videos/users/avatars/a%20b.jpg" {
		t.Errorf("prod: %s", got)
	}
	// CDN on another host stays absolute.
	cdn := newMediaResolver(newFakeStore("https://cdn.example.com/lms"), "https://eript-lms.io.vn")
	if got := *cdn.Public(sp("k.jpg")); got != "https://cdn.example.com/lms/k.jpg" {
		t.Errorf("cdn: %s", got)
	}
	if got := cdn.StorageKey("https://cdn.example.com/lms/users/avatars/k.jpg"); got != "users/avatars/k.jpg" {
		t.Errorf("cdn key: %s", got)
	}
}

func TestMediaStorageKey(t *testing.T) {
	m := newMediaResolver(newFakeStore("http://localhost:9000/lms-videos"), "https://eript-lms.io.vn")
	cases := map[string]string{
		"/minio/lms-videos/users/avatars/a.jpg":            "users/avatars/a.jpg",
		"http://localhost:9000/lms-videos/users/a%20b.jpg": "users/a b.jpg",
		"users/avatars/a.jpg":                              "users/avatars/a.jpg",
		"/storage/users/avatars/a.jpg":                     "/storage/users/avatars/a.jpg",
		"https://lh3.googleusercontent.com/a/x":            "https://lh3.googleusercontent.com/a/x",
		"/minio/other-bucket/x.jpg":                        "/minio/other-bucket/x.jpg",
	}
	for in, want := range cases {
		if got := m.StorageKey(in); got != want {
			t.Errorf("StorageKey(%q) = %q, want %q", in, got, want)
		}
	}
}
