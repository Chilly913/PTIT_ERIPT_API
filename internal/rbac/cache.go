package rbac

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultCacheTTL bounds staleness if an invalidation is ever missed.
const DefaultCacheTTL = 5 * time.Minute

// Entry is a cached roles/permissions snapshot of one user.
type Entry struct {
	Roles       []string `json:"r"`
	Permissions []string `json:"p"`
}

// Cache stores per-user Entries. Load returns a version stamp that must be passed
// back to Store: an entry is only served while its stamp is still current, so a
// snapshot computed concurrently with an invalidation is never served.
type Cache interface {
	// Load returns the cached entry (nil on miss) and the current stamp.
	Load(ctx context.Context, userID int64) (e *Entry, stamp string, err error)
	// Store saves e as computed at stamp.
	Store(ctx context.Context, userID int64, stamp string, e Entry) error
	// InvalidateUser drops one user's entry (role/direct-permission change).
	InvalidateUser(ctx context.Context, userID int64) error
	// InvalidateAll drops every entry (role -> permission matrix change).
	InvalidateAll(ctx context.Context) error
}

// RedisCache implements Cache with three keys:
//
//	<prefix>rbac:gen          global generation, INCR on matrix changes (no TTL)
//	<prefix>rbac:uver:<id>    per-user version, INCR on user changes (TTL > entry TTL)
//	<prefix>rbac:user:<id>    JSON entry {"s": "<gen>:<uver>", "r": [...], "p": [...]}
//
// Load is a single MGET; an entry whose stamp differs from "<gen>:<uver>" is a miss.
type RedisCache struct {
	rdb    redis.UniversalClient
	prefix string
	ttl    time.Duration
}

// NewRedisCache builds a Redis-backed cache; ttl <= 0 uses DefaultCacheTTL.
// prefix is the app-wide key prefix (config REDIS_PREFIX, e.g. "eript:").
func NewRedisCache(rdb redis.UniversalClient, prefix string, ttl time.Duration) *RedisCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &RedisCache{rdb: rdb, prefix: prefix, ttl: ttl}
}

func (c *RedisCache) genKey() string { return c.prefix + "rbac:gen" }
func (c *RedisCache) uverKey(id int64) string {
	return c.prefix + "rbac:uver:" + strconv.FormatInt(id, 10)
}
func (c *RedisCache) entryKey(id int64) string {
	return c.prefix + "rbac:user:" + strconv.FormatInt(id, 10)
}
func (c *RedisCache) uverTTL() time.Duration { return 4 * c.ttl }
func str(v any) string                       { s, _ := v.(string); return s }

type redisEntry struct {
	Stamp string `json:"s"`
	Entry
}

// Load implements Cache.
func (c *RedisCache) Load(ctx context.Context, userID int64) (*Entry, string, error) {
	vals, err := c.rdb.MGet(ctx, c.genKey(), c.uverKey(userID), c.entryKey(userID)).Result()
	if err != nil {
		return nil, "", fmt.Errorf("rbac cache: load: %w", err)
	}
	stamp := str(vals[0]) + ":" + str(vals[1])
	raw := str(vals[2])
	if raw == "" {
		return nil, stamp, nil
	}
	var re redisEntry
	if err := json.Unmarshal([]byte(raw), &re); err != nil || re.Stamp != stamp {
		return nil, stamp, nil
	}
	return &re.Entry, stamp, nil
}

// Store implements Cache.
func (c *RedisCache) Store(ctx context.Context, userID int64, stamp string, e Entry) error {
	b, err := json.Marshal(redisEntry{Stamp: stamp, Entry: e})
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, c.entryKey(userID), b, c.ttl).Err(); err != nil {
		return fmt.Errorf("rbac cache: store: %w", err)
	}
	return nil
}

// InvalidateUser implements Cache.
func (c *RedisCache) InvalidateUser(ctx context.Context, userID int64) error {
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Incr(ctx, c.uverKey(userID))
		p.Expire(ctx, c.uverKey(userID), c.uverTTL())
		p.Del(ctx, c.entryKey(userID))
		return nil
	})
	if err != nil {
		return fmt.Errorf("rbac cache: invalidate user %d: %w", userID, err)
	}
	return nil
}

// InvalidateAll implements Cache.
func (c *RedisCache) InvalidateAll(ctx context.Context) error {
	if err := c.rdb.Incr(ctx, c.genKey()).Err(); err != nil {
		return fmt.Errorf("rbac cache: invalidate all: %w", err)
	}
	return nil
}

// MemoryCache is an in-process Cache for tests and single-instance tools.
type MemoryCache struct {
	mu      sync.Mutex
	gen     int64
	uver    map[int64]int64
	entries map[int64]memEntry
}

type memEntry struct {
	stamp string
	e     Entry
}

// NewMemoryCache returns an empty MemoryCache (entries never expire).
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{uver: map[int64]int64{}, entries: map[int64]memEntry{}}
}

func (m *MemoryCache) stamp(id int64) string { return fmt.Sprintf("%d:%d", m.gen, m.uver[id]) }

// Load implements Cache.
func (m *MemoryCache) Load(_ context.Context, userID int64) (*Entry, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stamp(userID)
	if me, ok := m.entries[userID]; ok && me.stamp == s {
		e := Entry{Roles: append([]string{}, me.e.Roles...), Permissions: append([]string{}, me.e.Permissions...)}
		return &e, s, nil
	}
	return nil, s, nil
}

// Store implements Cache.
func (m *MemoryCache) Store(_ context.Context, userID int64, stamp string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[userID] = memEntry{stamp: stamp, e: e}
	return nil
}

// InvalidateUser implements Cache.
func (m *MemoryCache) InvalidateUser(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uver[userID]++
	delete(m.entries, userID)
	return nil
}

// InvalidateAll implements Cache.
func (m *MemoryCache) InvalidateAll(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	clear(m.entries)
	return nil
}
