package auth

import (
	"context"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/storage"
)

// ObjectStore is the subset of *storage.Storage the module uses (faked in tests).
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, key string) error
	URL(key string) string
	Bucket() string
}

var _ ObjectStore = (*storage.Storage)(nil)

// mediaResolver ports App\Support\PublicMediaUrl. Values stored in the DB are
// object keys (new data) or legacy public paths / absolute URLs.
type mediaResolver struct {
	store   ObjectStore
	appHost string
}

func newMediaResolver(store ObjectStore, appURL string) mediaResolver {
	host := ""
	if u, err := url.Parse(appURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	return mediaResolver{store: store, appHost: host}
}

var internalHosts = []string{"localhost", "127.0.0.1", "backend", "nginx", "minio", "host.docker.internal"}

// rewrite is PublicMediaUrl::toPublic for absolute and already-public values;
// ok=false means p is a bare storage key.
func (m mediaResolver) rewrite(p string) (string, bool) {
	if strings.HasPrefix(p, "data:") || strings.HasPrefix(p, "blob:") {
		return p, true
	}
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(p)
		if err != nil {
			return p, true
		}
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		upath := u.EscapedPath() // PHP parse_url keeps the path encoded
		if host == "minio" || strings.Contains(host, "minio") || port == "9000" || port == "9001" {
			return "/minio/" + strings.TrimLeft(upath, "/"), true
		}
		rewriteHost := slices.Contains(internalHosts, host) || (m.appHost != "" && host == m.appHost)
		if rewriteHost && upath != "" {
			return upath, true
		}
		return p, true
	}
	for _, pre := range []string{"/storage/", "/minio/", "/uploads/", "/images/"} {
		if strings.HasPrefix(p, pre) {
			return p, true
		}
	}
	if strings.HasPrefix(p, "storage/") {
		return "/" + p, true
	}
	return "", false
}

// Public returns the browser-reachable URL of a stored value (nil for empty).
// Bare keys resolve through the object store (MinIO) and are then rewritten
// like legacy, e.g. "/minio/lms-videos/users/avatars/x.jpg".
func (m mediaResolver) Public(v *string) *string {
	if v == nil {
		return nil
	}
	p := strings.TrimSpace(*v)
	if p == "" {
		return nil
	}
	if out, ok := m.rewrite(p); ok {
		return &out
	}
	key := strings.TrimLeft(p, "/")
	if m.store == nil {
		out := "/storage/" + key
		return &out
	}
	out := m.URL(key)
	return &out
}

// URL returns the public URL of an object key.
func (m mediaResolver) URL(key string) string {
	u := m.store.URL(key)
	if out, ok := m.rewrite(u); ok {
		return out
	}
	return u
}

// StorageKey converts a client-supplied media value (URL returned by
// /auth/upload, public path or key) into what is stored: the object key when
// the value points into our bucket, otherwise the public form unchanged.
func (m mediaResolver) StorageKey(v string) string {
	p := strings.TrimSpace(v)
	if p == "" {
		return ""
	}
	pub, ok := m.rewrite(p)
	if !ok {
		return strings.TrimLeft(p, "/") // already a key
	}
	if key, ok := m.ownKey(pub); ok {
		return key
	}
	return pub
}

// ownKey extracts the object key from a public URL of our bucket.
func (m mediaResolver) ownKey(pub string) (string, bool) {
	if m.store == nil {
		return "", false
	}
	prefixes := []string{"/minio/" + m.store.Bucket() + "/"}
	if base := strings.TrimSuffix(m.URL("x"), "x"); base != "" {
		prefixes = append(prefixes, base)
	}
	for _, pre := range prefixes {
		if rest, ok := strings.CutPrefix(pub, pre); ok && rest != "" {
			if k, err := url.PathUnescape(rest); err == nil {
				rest = k
			}
			return storage.CleanKey(rest), true
		}
	}
	return "", false
}
