package promptasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type Cache struct {
	mu      sync.RWMutex
	entries map[string]CacheEntry
}

func OpenCache(_ string) (*Cache, error) {
	return &Cache{entries: map[string]CacheEntry{}}, nil
}

func OpenSQLiteCache(path string) (*Cache, error) {
	return OpenCache(path)
}

func OpenMemoryCache() (*Cache, error) {
	return OpenCache("")
}

func (c *Cache) Close() error { return nil }

func (c *Cache) Init(_ context.Context) error {
	if c.entries == nil {
		c.entries = map[string]CacheEntry{}
	}
	return nil
}

func (c *Cache) Get(_ context.Context, key string) (CacheEntry, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	return entry, ok, nil
}

func (c *Cache) Put(_ context.Context, entry CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]CacheEntry{}
	}
	if entry.Hash == "" {
		entry.Hash = HashText(entry.Value)
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	c.entries[entry.Key] = entry
	return nil
}

func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func CacheKey(kind, body string) string {
	return kind + ":" + HashText(body)
}

type SQLiteCache = Cache

var _ CacheStore = (*Cache)(nil)
